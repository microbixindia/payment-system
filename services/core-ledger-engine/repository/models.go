package repository

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// AccountType classifies accounts in compliance with GAAP / IFRS accounting standards.
type AccountType string

const (
	AccountTypeAsset     AccountType = "ASSET"
	AccountTypeLiability AccountType = "LIABILITY"
	AccountTypeEquity    AccountType = "EQUITY"
	AccountTypeRevenue   AccountType = "REVENUE"
	AccountTypeExpense   AccountType = "EXPENSE"
)

// Direction represents debit or credit movement.
type Direction string

const (
	DirectionDebit  Direction = "DEBIT"
	DirectionCredit Direction = "CREDIT"
)

// EntryState defines the life-cycle of a journal entry.
type EntryState string

const (
	StatePending  EntryState = "PENDING"
	StatePosted   EntryState = "POSTED"
	StateRejected EntryState = "REJECTED"
	StateReversed EntryState = "REVERSED"
)

var (
	ErrUnbalancedTransaction = errors.New("ledger: double-entry invariant violation: total debits must equal total credits")
	ErrEmptyPostings         = errors.New("ledger: journal entry must contain at least 2 postings")
	ErrNegativePostingAmount = errors.New("ledger: posting line amount must be strictly greater than zero")
	ErrAccountInactive       = errors.New("ledger: target account is inactive or frozen")
	ErrDuplicateIdempotency  = errors.New("ledger: transaction with identical idempotency hash has already been processed")
	ErrCurrencyMismatch      = errors.New("ledger: multi-currency transaction within non-revaluation posting is forbidden")
)

// Ledger represents a root accounting ledger container.
type Ledger struct {
	ID           uuid.UUID `json:"id"`
	Name         string    `json:"name"`
	BaseCurrency string    `json:"base_currency"`
	Status       string    `json:"status"`
	Description  string    `json:"description"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Account represents an individual node in the chart of accounts.
type Account struct {
	ID             uuid.UUID       `json:"id"`
	LedgerID       uuid.UUID       `json:"ledger_id"`
	Code           string          `json:"code"`
	Name           string          `json:"name"`
	AccountType    AccountType     `json:"account_type"`
	NormalBalance  Direction       `json:"normal_balance"`
	Currency       string          `json:"currency"`
	CurrentBalance decimal.Decimal `json:"current_balance"`
	Status         string          `json:"status"`
	Version        int64           `json:"version"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

// JournalEntry represents an atomic financial transaction header.
type JournalEntry struct {
	ID              uuid.UUID       `json:"id"`
	LedgerID        uuid.UUID       `json:"ledger_id"`
	IdempotencyHash string          `json:"idempotency_hash"`
	CorrelationID   string          `json:"correlation_id"`
	ExternalRef     string          `json:"external_ref"`
	Description     string          `json:"description"`
	State           EntryState      `json:"state"`
	TotalAmount     decimal.Decimal `json:"total_amount"`
	Currency        string          `json:"currency"`
	PostedAt        *time.Time      `json:"posted_at"`
	CreatedAt       time.Time       `json:"created_at"`
	Postings        []LedgerPosting `json:"postings,omitempty"`
}

// LedgerPosting represents an immutable debit or credit entry affecting an account.
type LedgerPosting struct {
	ID             uuid.UUID       `json:"id"`
	JournalEntryID uuid.UUID       `json:"journal_entry_id"`
	AccountID      uuid.UUID       `json:"account_id"`
	Direction      Direction       `json:"direction"`
	Amount         decimal.Decimal `json:"amount"`
	PostBalance    decimal.Decimal `json:"post_balance"`
	SequenceNo     int             `json:"sequence_no"`
	Description    string          `json:"description"`
	CreatedAt      time.Time       `json:"created_at"`
}

// PostingRequest specifies a posting line inside a proposed journal entry.
type PostingRequest struct {
	AccountID   uuid.UUID       `json:"account_id"`
	Direction   Direction       `json:"direction"`
	Amount      decimal.Decimal `json:"amount"`
	Description string          `json:"description"`
}

// CreateJournalEntryRequest represents the payload to create and post a balanced financial entry.
type CreateJournalEntryRequest struct {
	LedgerID      uuid.UUID        `json:"ledger_id"`
	IdempotencyKey string          `json:"idempotency_key"`
	CorrelationID string           `json:"correlation_id"`
	ExternalRef   string           `json:"external_ref"`
	Description   string           `json:"description"`
	Currency      string           `json:"currency"`
	Postings      []PostingRequest `json:"postings"`
}

// ComputeIdempotencyHash calculates a deterministic SHA-256 hash across the unique transaction tuple.
func ComputeIdempotencyHash(ledgerID uuid.UUID, idempotencyKey, currency string, postings []PostingRequest) string {
	hasher := sha256.New()
	hasher.Write([]byte(ledgerID.String()))
	hasher.Write([]byte(":"))
	hasher.Write([]byte(idempotencyKey))
	hasher.Write([]byte(":"))
	hasher.Write([]byte(currency))

	for _, p := range postings {
		hasher.Write([]byte(fmt.Sprintf(":%s:%s:%s", p.AccountID.String(), p.Direction, p.Amount.StringFixed(4))))
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

// ValidateInvariants verifies that the proposed entry conforms to double-entry accounting rules:
// 1. Minimum 2 postings.
// 2. All amounts positive.
// 3. Total Debits strictly equals Total Credits.
func (req *CreateJournalEntryRequest) ValidateInvariants() error {
	if len(req.Postings) < 2 {
		return ErrEmptyPostings
	}

	totalDebits := decimal.Zero
	totalCredits := decimal.Zero

	for _, p := range req.Postings {
		if p.Amount.LessThanOrEqual(decimal.Zero) {
			return fmt.Errorf("%w: account %s amount %s", ErrNegativePostingAmount, p.AccountID, p.Amount)
		}

		switch p.Direction {
		case DirectionDebit:
			totalDebits = totalDebits.Add(p.Amount)
		case DirectionCredit:
			totalCredits = totalCredits.Add(p.Amount)
		default:
			return fmt.Errorf("ledger: invalid posting direction %q", p.Direction)
		}
	}

	if !totalDebits.Equal(totalCredits) {
		return fmt.Errorf("%w: total debits %s != total credits %s (delta: %s)",
			ErrUnbalancedTransaction, totalDebits.StringFixed(4), totalCredits.StringFixed(4), totalDebits.Sub(totalCredits).StringFixed(4))
	}

	return nil
}

// ValidateInvariants verifies that a journal entry's postings conform to double-entry accounting:
// 1. Minimum 2 postings if postings are populated.
// 2. All amounts positive.
// 3. Total Debits strictly equals Total Credits.
func (entry *JournalEntry) ValidateInvariants() error {
	if len(entry.Postings) < 2 {
		return ErrEmptyPostings
	}

	totalDebits := decimal.Zero
	totalCredits := decimal.Zero

	for _, p := range entry.Postings {
		if p.Amount.LessThanOrEqual(decimal.Zero) {
			return fmt.Errorf("%w: account %s amount %s", ErrNegativePostingAmount, p.AccountID, p.Amount)
		}

		switch p.Direction {
		case DirectionDebit:
			totalDebits = totalDebits.Add(p.Amount)
		case DirectionCredit:
			totalCredits = totalCredits.Add(p.Amount)
		default:
			return fmt.Errorf("ledger: invalid posting direction %q", p.Direction)
		}
	}

	if !totalDebits.Equal(totalCredits) {
		return fmt.Errorf("%w: total debits %s != total credits %s (delta: %s)",
			ErrUnbalancedTransaction, totalDebits.StringFixed(4), totalCredits.StringFixed(4), totalDebits.Sub(totalCredits).StringFixed(4))
	}

	return nil
}
