package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

var (
	ErrAccountNotFound = errors.New("ledger: account does not exist")
	ErrLedgerNotFound  = errors.New("ledger: root ledger does not exist")
)

// LedgerRepository defines operations required by high-velocity fintech accounting.
type LedgerRepository interface {
	CreateLedger(ctx context.Context, ledger *Ledger) error
	CreateAccount(ctx context.Context, account *Account) error
	GetAccount(ctx context.Context, accountID uuid.UUID) (*Account, error)
	PostJournalEntry(ctx context.Context, req CreateJournalEntryRequest) (*JournalEntry, error)
	GetJournalEntryByIdempotency(ctx context.Context, idempotencyHash string) (*JournalEntry, error)
}

// InMemLedgerRepository provides a production-grade, thread-safe, deadlock-free
// repository implementation simulating CockroachDB serializable isolation.
type InMemLedgerRepository struct {
	mu           sync.RWMutex
	ledgers      map[uuid.UUID]*Ledger
	accounts     map[uuid.UUID]*Account
	accountLocks map[uuid.UUID]*sync.Mutex
	journals     map[uuid.UUID]*JournalEntry
	idempIndex   map[string]uuid.UUID // hash -> journalEntry ID
	postings     map[uuid.UUID][]LedgerPosting
}

// NewInMemLedgerRepository initializes the repository structures.
func NewInMemLedgerRepository() *InMemLedgerRepository {
	return &InMemLedgerRepository{
		ledgers:      make(map[uuid.UUID]*Ledger),
		accounts:     make(map[uuid.UUID]*Account),
		accountLocks: make(map[uuid.UUID]*sync.Mutex),
		journals:     make(map[uuid.UUID]*JournalEntry),
		idempIndex:   make(map[string]uuid.UUID),
		postings:     make(map[uuid.UUID][]LedgerPosting),
	}
}

// CreateLedger registers a new root ledger.
func (r *InMemLedgerRepository) CreateLedger(ctx context.Context, l *Ledger) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if l.ID == uuid.Nil {
		l.ID = uuid.New()
	}
	l.CreatedAt = time.Now().UTC()
	l.UpdatedAt = l.CreatedAt
	l.Status = "ACTIVE"

	r.ledgers[l.ID] = l
	return nil
}

// CreateAccount registers an account under a ledger.
func (r *InMemLedgerRepository) CreateAccount(ctx context.Context, a *Account) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.ledgers[a.LedgerID]; !exists {
		return ErrLedgerNotFound
	}

	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	a.CreatedAt = time.Now().UTC()
	a.UpdatedAt = a.CreatedAt
	a.CurrentBalance = decimal.Zero
	a.Version = 1
	a.Status = "ACTIVE"

	r.accounts[a.ID] = a
	r.accountLocks[a.ID] = &sync.Mutex{}
	return nil
}

// GetAccount retrieves an account by ID.
func (r *InMemLedgerRepository) GetAccount(ctx context.Context, accountID uuid.UUID) (*Account, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	acc, exists := r.accounts[accountID]
	if !exists {
		return nil, ErrAccountNotFound
	}

	// Defensive copy
	copied := *acc
	return &copied, nil
}

// GetJournalEntryByIdempotency returns an existing journal entry if processed earlier.
func (r *InMemLedgerRepository) GetJournalEntryByIdempotency(ctx context.Context, hash string) (*JournalEntry, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	journalID, exists := r.idempIndex[hash]
	if !exists {
		return nil, nil
	}

	entry := r.journals[journalID]
	copied := *entry
	copied.Postings = append([]LedgerPosting(nil), r.postings[journalID]...)
	return &copied, nil
}

// PostJournalEntry atomically executes a double-entry transaction with strict deadlock prevention.
func (r *InMemLedgerRepository) PostJournalEntry(ctx context.Context, req CreateJournalEntryRequest) (*JournalEntry, error) {
	// Step 1: Validate GAAP/IFRS mathematical invariants (debits == credits, positive amounts)
	if err := req.ValidateInvariants(); err != nil {
		return nil, err
	}

	idempHash := ComputeIdempotencyHash(req.LedgerID, req.IdempotencyKey, req.Currency, req.Postings)

	// Step 2: Check idempotency cache to prevent double-spending
	existing, err := r.GetJournalEntryByIdempotency(ctx, idempHash)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	// Step 3: DEADLOCK PREVENTION - Sort unique account IDs deterministically in lexicographical order.
	// In relational databases (CockroachDB/Postgres), this translates to:
	// `SELECT ... FROM accounts WHERE id IN (...) ORDER BY id ASC FOR UPDATE;`
	accountIDSet := make(map[uuid.UUID]struct{})
	for _, p := range req.Postings {
		accountIDSet[p.AccountID] = struct{}{}
	}

	sortedAccountIDs := make([]uuid.UUID, 0, len(accountIDSet))
	for accID := range accountIDSet {
		sortedAccountIDs = append(sortedAccountIDs, accID)
	}
	sort.Slice(sortedAccountIDs, func(i, j int) bool {
		return sortedAccountIDs[i].String() < sortedAccountIDs[j].String()
	})

	// Step 4: Acquire mutex locks in deterministic sorted order
	r.mu.RLock()
	locks := make([]*sync.Mutex, 0, len(sortedAccountIDs))
	for _, accID := range sortedAccountIDs {
		lock, exists := r.accountLocks[accID]
		if !exists {
			r.mu.RUnlock()
			return nil, fmt.Errorf("%w: account %s", ErrAccountNotFound, accID)
		}
		locks = append(locks, lock)
	}
	r.mu.RUnlock()

	for _, lk := range locks {
		lk.Lock()
		defer lk.Unlock()
	}

	// Step 5: Double check idempotency under critical lock
	r.mu.Lock()
	defer r.mu.Unlock()

	if jID, exists := r.idempIndex[idempHash]; exists {
		entry := r.journals[jID]
		copied := *entry
		copied.Postings = append([]LedgerPosting(nil), r.postings[jID]...)
		return &copied, nil
	}

	// Verify all target accounts are active and valid
	totalTxAmount := decimal.Zero
	for _, p := range req.Postings {
		acc, exists := r.accounts[p.AccountID]
		if !exists {
			return nil, fmt.Errorf("%w: %s", ErrAccountNotFound, p.AccountID)
		}
		if acc.Status != "ACTIVE" {
			return nil, fmt.Errorf("%w: account %s is %s", ErrAccountInactive, p.AccountID, acc.Status)
		}
		if acc.Currency != req.Currency {
			return nil, fmt.Errorf("%w: account currency %s != tx currency %s", ErrCurrencyMismatch, acc.Currency, req.Currency)
		}
		if p.Direction == DirectionDebit {
			totalTxAmount = totalTxAmount.Add(p.Amount)
		}
	}

	// Step 6: Create Journal Entry
	now := time.Now().UTC()
	journalEntry := &JournalEntry{
		ID:              uuid.New(),
		LedgerID:        req.LedgerID,
		IdempotencyHash: idempHash,
		CorrelationID:   req.CorrelationID,
		ExternalRef:     req.ExternalRef,
		Description:     req.Description,
		State:           StatePosted,
		TotalAmount:     totalTxAmount,
		Currency:        req.Currency,
		PostedAt:        &now,
		CreatedAt:       now,
	}

	// Step 7: Apply postings and update balances atomically
	createdPostings := make([]LedgerPosting, 0, len(req.Postings))
	for seq, p := range req.Postings {
		acc := r.accounts[p.AccountID]

		// GAAP balance calculation rules:
		// ASSET / EXPENSE: Normal Debit. (Debit increases +, Credit decreases -)
		// LIABILITY / EQUITY / REVENUE: Normal Credit. (Credit increases +, Debit decreases -)
		var delta decimal.Decimal
		if acc.NormalBalance == DirectionDebit {
			if p.Direction == DirectionDebit {
				delta = p.Amount
			} else {
				delta = p.Amount.Neg()
			}
		} else { // Normal Credit
			if p.Direction == DirectionCredit {
				delta = p.Amount
			} else {
				delta = p.Amount.Neg()
			}
		}

		acc.CurrentBalance = acc.CurrentBalance.Add(delta)
		acc.Version++
		acc.UpdatedAt = now

		posting := LedgerPosting{
			ID:             uuid.New(),
			JournalEntryID: journalEntry.ID,
			AccountID:      acc.ID,
			Direction:      p.Direction,
			Amount:         p.Amount,
			PostBalance:    acc.CurrentBalance,
			SequenceNo:     seq + 1,
			Description:    p.Description,
			CreatedAt:      now,
		}

		createdPostings = append(createdPostings, posting)
	}

	// Commit state
	r.journals[journalEntry.ID] = journalEntry
	r.idempIndex[idempHash] = journalEntry.ID
	r.postings[journalEntry.ID] = createdPostings

	journalEntry.Postings = createdPostings
	return journalEntry, nil
}

// SyncAccountBalance updates an account's balance and version for anti-entropy replication and crash recovery.
func (r *InMemLedgerRepository) SyncAccountBalance(accountID uuid.UUID, balance decimal.Decimal, version int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	acc, exists := r.accounts[accountID]
	if !exists {
		return ErrAccountNotFound
	}

	acc.CurrentBalance = balance
	acc.Version = version
	acc.UpdatedAt = time.Now().UTC()
	return nil
}

// SQLDatabaseDriver provides SQL generation statements for CockroachDB / Postgres production deploys.
type SQLDatabaseDriver struct {
	db *sql.DB
}

// NewSQLDatabaseDriver wraps a standard sql.DB pool.
func NewSQLDatabaseDriver(db *sql.DB) *SQLDatabaseDriver {
	return &SQLDatabaseDriver{db: db}
}
