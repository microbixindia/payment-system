package repository

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

var (
	ErrEmptyBatch          = errors.New("batch_poster: batch contains no journal entries")
	ErrBatchUnbalanced     = errors.New("batch_poster: batch-level GAAP double-entry invariant violation: total debits != total credits")
	ErrBatchDuplicateKey   = errors.New("batch_poster: duplicate idempotency key detected within batch payload")
	ErrBatchLedgerMismatch = errors.New("batch_poster: journal entry ledger_id does not match batch root ledger_id")
	ErrBatchSizeExceeded   = errors.New("batch_poster: batch size exceeds maximum configured capacity")
	ErrNilBatchRequest     = errors.New("batch_poster: batch request cannot be nil")
)

// BatchPostingRequest defines an atomic batch containing multiple journal entries.
type BatchPostingRequest struct {
	BatchID       uuid.UUID                   `json:"batch_id"`
	LedgerID      uuid.UUID                   `json:"ledger_id"`
	CorrelationID string                      `json:"correlation_id"`
	Entries       []CreateJournalEntryRequest `json:"entries"`
}

// BulkAccountSummary records aggregate balance movements for a single account within a batch.
type BulkAccountSummary struct {
	AccountID      uuid.UUID       `json:"account_id"`
	InitialBalance decimal.Decimal `json:"initial_balance"`
	NetDebit       decimal.Decimal `json:"net_debit"`
	NetCredit      decimal.Decimal `json:"net_credit"`
	NetDelta       decimal.Decimal `json:"net_delta"`
	FinalBalance   decimal.Decimal `json:"final_balance"`
	PostingCount   int             `json:"posting_count"`
}

// BatchPostingResult details the outcome of an atomic batch posting operation.
type BatchPostingResult struct {
	BatchID           uuid.UUID                        `json:"batch_id"`
	LedgerID          uuid.UUID                        `json:"ledger_id"`
	TotalEntries      int                              `json:"total_entries"`
	TotalPostings     int                              `json:"total_postings"`
	TotalVolume       decimal.Decimal                  `json:"total_volume"`
	ExecutionDuration time.Duration                    `json:"execution_duration"`
	ThroughputPPS     float64                          `json:"throughput_pps"` // postings per second
	JournalEntries    []*JournalEntry                  `json:"journal_entries"`
	AccountSummaries  map[uuid.UUID]BulkAccountSummary `json:"account_summaries"`
	CommittedAt       time.Time                        `json:"committed_at"`
}

// BatchValidationError reports diagnostic information when invariant checks fail.
type BatchValidationError struct {
	EntryIndex     int       `json:"entry_index"`
	IdempotencyKey string    `json:"idempotency_key"`
	AccountID      uuid.UUID `json:"account_id"`
	Reason         string    `json:"reason"`
	Err            error     `json:"error"`
}

func (e *BatchValidationError) Error() string {
	if e.EntryIndex >= 0 {
		return fmt.Sprintf("batch validation failed at entry index [%d] (key: %q, account: %s): %s - %v",
			e.EntryIndex, e.IdempotencyKey, e.AccountID, e.Reason, e.Err)
	}
	return fmt.Sprintf("batch-level validation failed: %s - %v", e.Reason, e.Err)
}

func (e *BatchValidationError) Unwrap() error {
	return e.Err
}

// -----------------------------------------------------------------------------
// LOCK STRIPING & DEADLOCK ELIMINATION
// -----------------------------------------------------------------------------

// LockStripePool partitions the account lock space into striped mutex shards.
// By hashing account IDs into stripes and locking stripes in strictly ascending
// index order, cyclic wait conditions are mathematically prevented (zero deadlocks).
type LockStripePool struct {
	stripeCount uint32
	stripes     []sync.RWMutex
}

// NewLockStripePool instantiates a lock-striping pool with the specified number of shards.
func NewLockStripePool(stripeCount uint32) *LockStripePool {
	if stripeCount == 0 {
		stripeCount = 256
	}
	return &LockStripePool{
		stripeCount: stripeCount,
		stripes:     make([]sync.RWMutex, stripeCount),
	}
}

// StripeIndex maps an account UUID to its deterministic stripe slot via FNV-1a.
func (p *LockStripePool) StripeIndex(id uuid.UUID) uint32 {
	h := fnv.New32a()
	h.Write(id[:])
	return h.Sum32() % p.stripeCount
}

// LockAccounts acquires exclusive write locks across all stripes corresponding
// to the requested accounts in strictly ascending stripe index order.
// Returns an unlock function that releases the acquired locks in reverse order.
func (p *LockStripePool) LockAccounts(accountIDs []uuid.UUID) func() {
	if len(accountIDs) == 0 {
		return func() {}
	}

	// 1. Map to unique stripe indices
	stripeMap := make(map[uint32]struct{}, len(accountIDs))
	for _, id := range accountIDs {
		stripeMap[p.StripeIndex(id)] = struct{}{}
	}

	sortedStripes := make([]uint32, 0, len(stripeMap))
	for s := range stripeMap {
		sortedStripes = append(sortedStripes, s)
	}

	// 2. Strict ascending order sort ensures deterministic lock acquisition
	sort.Slice(sortedStripes, func(i, j int) bool {
		return sortedStripes[i] < sortedStripes[j]
	})

	// 3. Acquire locks in order
	for _, s := range sortedStripes {
		p.stripes[s].Lock()
	}

	// 4. Return unlock closure releasing in reverse order
	return func() {
		for i := len(sortedStripes) - 1; i >= 0; i-- {
			p.stripes[sortedStripes[i]].Unlock()
		}
	}
}

// SortAccountIDsLexicographically deterministically sorts UUIDs in ascending
// string order, preventing database row-level lock inversion deadlocks.
func SortAccountIDsLexicographically(accountIDs []uuid.UUID) []uuid.UUID {
	sorted := make([]uuid.UUID, len(accountIDs))
	copy(sorted, accountIDs)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].String() < sorted[j].String()
	})
	return sorted
}

// ExtractUniqueSortedAccountIDs extracts all unique account IDs affected by a batch
// and returns them deterministically sorted.
func ExtractUniqueSortedAccountIDs(batch *BatchPostingRequest) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{})
	for _, entry := range batch.Entries {
		for _, p := range entry.Postings {
			seen[p.AccountID] = struct{}{}
		}
	}

	unique := make([]uuid.UUID, 0, len(seen))
	for id := range seen {
		unique = append(unique, id)
	}

	return SortAccountIDsLexicographically(unique)
}

// -----------------------------------------------------------------------------
// GAAP DOUBLE-ENTRY INVARIANT VALIDATION ACROSS BATCH
// -----------------------------------------------------------------------------

// ValidateBatchInvariants enforces complete GAAP/IFRS accounting invariants across the batch:
// 1. Batch must not be nil and must contain at least one journal entry.
// 2. Zero intra-batch idempotency collisions.
// 3. Every individual entry must conform to GAAP double-entry rules:
//    - Minimum 2 postings.
//    - All posting amounts > 0.
//    - Total debits == total credits.
// 4. Batch-level equilibrium: sum of all debits across the batch strictly equals sum of all credits.
// 5. Account and currency conformity.
func ValidateBatchInvariants(batch *BatchPostingRequest) error {
	if batch == nil {
		return ErrNilBatchRequest
	}
	if len(batch.Entries) == 0 {
		return ErrEmptyBatch
	}

	seenIdempKeys := make(map[string]int, len(batch.Entries))
	batchTotalDebit := decimal.Zero
	batchTotalCredit := decimal.Zero

	for idx, entry := range batch.Entries {
		// Verify root ledger alignment if specified on batch
		if entry.LedgerID != uuid.Nil && batch.LedgerID != uuid.Nil && entry.LedgerID != batch.LedgerID {
			return &BatchValidationError{
				EntryIndex:     idx,
				IdempotencyKey: entry.IdempotencyKey,
				Reason:         fmt.Sprintf("entry ledger_id %s differs from batch ledger_id %s", entry.LedgerID, batch.LedgerID),
				Err:            ErrBatchLedgerMismatch,
			}
		}

		// Detect duplicate idempotency keys within the same batch payload
		if entry.IdempotencyKey != "" {
			if prevIdx, exists := seenIdempKeys[entry.IdempotencyKey]; exists {
				return &BatchValidationError{
					EntryIndex:     idx,
					IdempotencyKey: entry.IdempotencyKey,
					Reason:         fmt.Sprintf("duplicate idempotency key %q (previously at entry index [%d])", entry.IdempotencyKey, prevIdx),
					Err:            ErrBatchDuplicateKey,
				}
			}
			seenIdempKeys[entry.IdempotencyKey] = idx
		}

		// Verify per-entry double-entry invariants
		if err := entry.ValidateInvariants(); err != nil {
			return &BatchValidationError{
				EntryIndex:     idx,
				IdempotencyKey: entry.IdempotencyKey,
				Reason:         "per-entry double-entry invariant failed",
				Err:            err,
			}
		}

		// Accumulate batch totals
		for _, p := range entry.Postings {
			switch p.Direction {
			case DirectionDebit:
				batchTotalDebit = batchTotalDebit.Add(p.Amount)
			case DirectionCredit:
				batchTotalCredit = batchTotalCredit.Add(p.Amount)
			}
		}
	}

	// Invariant: batch aggregate debits == credits
	if !batchTotalDebit.Equal(batchTotalCredit) {
		return &BatchValidationError{
			EntryIndex: -1,
			Reason: fmt.Sprintf("batch aggregate debits (%s) != credits (%s), delta: %s",
				batchTotalDebit.StringFixed(4), batchTotalCredit.StringFixed(4), batchTotalDebit.Sub(batchTotalCredit).StringFixed(4)),
			Err: ErrBatchUnbalanced,
		}
	}

	return nil
}

// ParallelValidateBatchInvariants executes invariant checks in parallel chunks for ultra-high velocity batches.
func ParallelValidateBatchInvariants(batch *BatchPostingRequest, workers int) error {
	if batch == nil {
		return ErrNilBatchRequest
	}
	n := len(batch.Entries)
	if n == 0 {
		return ErrEmptyBatch
	}

	// For small batches, sequential is faster due to zero goroutine overhead
	if n < 500 || workers <= 1 {
		return ValidateBatchInvariants(batch)
	}

	// Intra-batch idempotency uniqueness check
	seenIdempKeys := make(map[string]int, n)
	for idx, entry := range batch.Entries {
		if entry.IdempotencyKey != "" {
			if prevIdx, exists := seenIdempKeys[entry.IdempotencyKey]; exists {
				return &BatchValidationError{
					EntryIndex:     idx,
					IdempotencyKey: entry.IdempotencyKey,
					Reason:         fmt.Sprintf("duplicate idempotency key %q (first seen at entry [%d])", entry.IdempotencyKey, prevIdx),
					Err:            ErrBatchDuplicateKey,
				}
			}
			seenIdempKeys[entry.IdempotencyKey] = idx
		}
	}

	chunkSize := (n + workers - 1) / workers
	errChan := make(chan error, workers)
	debitChan := make(chan decimal.Decimal, workers)
	creditChan := make(chan decimal.Decimal, workers)

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		start := w * chunkSize
		end := start + chunkSize
		if start >= n {
			break
		}
		if end > n {
			end = n
		}

		wg.Add(1)
		go func(startIdx, endIdx int) {
			defer wg.Done()
			subDebits := decimal.Zero
			subCredits := decimal.Zero

			for i := startIdx; i < endIdx; i++ {
				entry := batch.Entries[i]
				if entry.LedgerID != uuid.Nil && batch.LedgerID != uuid.Nil && entry.LedgerID != batch.LedgerID {
					errChan <- &BatchValidationError{
						EntryIndex:     i,
						IdempotencyKey: entry.IdempotencyKey,
						Reason:         "ledger mismatch",
						Err:            ErrBatchLedgerMismatch,
					}
					return
				}
				if err := entry.ValidateInvariants(); err != nil {
					errChan <- &BatchValidationError{
						EntryIndex:     i,
						IdempotencyKey: entry.IdempotencyKey,
						Reason:         "invariants failed",
						Err:            err,
					}
					return
				}
				for _, p := range entry.Postings {
					if p.Direction == DirectionDebit {
						subDebits = subDebits.Add(p.Amount)
					} else {
						subCredits = subCredits.Add(p.Amount)
					}
				}
			}
			debitChan <- subDebits
			creditChan <- subCredits
		}(start, end)
	}

	wg.Wait()
	close(errChan)
	close(debitChan)
	close(creditChan)

	if len(errChan) > 0 {
		return <-errChan
	}

	totalDebits := decimal.Zero
	for d := range debitChan {
		totalDebits = totalDebits.Add(d)
	}
	totalCredits := decimal.Zero
	for c := range creditChan {
		totalCredits = totalCredits.Add(c)
	}

	if !totalDebits.Equal(totalCredits) {
		return &BatchValidationError{
			EntryIndex: -1,
			Reason: fmt.Sprintf("batch aggregate debits %s != credits %s",
				totalDebits.StringFixed(4), totalCredits.StringFixed(4)),
			Err: ErrBatchUnbalanced,
		}
	}

	return nil
}

// -----------------------------------------------------------------------------
// BULK BALANCE CALCULATION ENGINE
// -----------------------------------------------------------------------------

// CalculateGAAPDelta computes the directional balance delta based on GAAP rules:
// - Normal Debit (Asset, Expense): Debit (+), Credit (-)
// - Normal Credit (Liability, Equity, Revenue): Credit (+), Debit (-)
func CalculateGAAPDelta(normalBalance Direction, direction Direction, amount decimal.Decimal) decimal.Decimal {
	if normalBalance == DirectionDebit {
		if direction == DirectionDebit {
			return amount
		}
		return amount.Neg()
	}
	// Normal Credit
	if direction == DirectionCredit {
		return amount
	}
	return amount.Neg()
}

// BulkBalanceCalculator orchestrates multi-entry balance calculations, running balances,
// and per-account delta aggregations with zero allocation in the inner loops.
type BulkBalanceCalculator struct{}

// NewBulkBalanceCalculator creates a new calculation engine instance.
func NewBulkBalanceCalculator() *BulkBalanceCalculator {
	return &BulkBalanceCalculator{}
}

// ComputeBatchPostings processes a validated batch against account states, computing
// exact running balances for each posting line and aggregated account summaries.
func (c *BulkBalanceCalculator) ComputeBatchPostings(
	batch *BatchPostingRequest,
	accountMap map[uuid.UUID]*Account,
	now time.Time,
) ([]*JournalEntry, []LedgerPosting, map[uuid.UUID]BulkAccountSummary, decimal.Decimal, error) {
	// Initialize running balances and aggregate summaries
	runningBalances := make(map[uuid.UUID]decimal.Decimal, len(accountMap))
	summaries := make(map[uuid.UUID]BulkAccountSummary, len(accountMap))

	for accID, acc := range accountMap {
		runningBalances[accID] = acc.CurrentBalance
		summaries[accID] = BulkAccountSummary{
			AccountID:      accID,
			InitialBalance: acc.CurrentBalance,
			NetDebit:       decimal.Zero,
			NetCredit:      decimal.Zero,
			NetDelta:       decimal.Zero,
			FinalBalance:   acc.CurrentBalance,
			PostingCount:   0,
		}
	}

	totalBatchVolume := decimal.Zero
	journalEntries := make([]*JournalEntry, 0, len(batch.Entries))

	// Estimate posting count for slice preallocation
	totalPostingsEst := len(batch.Entries) * 2
	allPostings := make([]LedgerPosting, 0, totalPostingsEst)

	for _, entryReq := range batch.Entries {
		idempHash := ComputeIdempotencyHash(entryReq.LedgerID, entryReq.IdempotencyKey, entryReq.Currency, entryReq.Postings)

		entryTotal := decimal.Zero
		for _, p := range entryReq.Postings {
			if p.Direction == DirectionDebit {
				entryTotal = entryTotal.Add(p.Amount)
			}
		}
		totalBatchVolume = totalBatchVolume.Add(entryTotal)

		postedAt := now
		journal := &JournalEntry{
			ID:              uuid.New(),
			LedgerID:        entryReq.LedgerID,
			IdempotencyHash: idempHash,
			CorrelationID:   entryReq.CorrelationID,
			ExternalRef:     entryReq.ExternalRef,
			Description:     entryReq.Description,
			State:           StatePosted,
			TotalAmount:     entryTotal,
			Currency:        entryReq.Currency,
			PostedAt:        &postedAt,
			CreatedAt:       now,
			Postings:        make([]LedgerPosting, 0, len(entryReq.Postings)),
		}

		for seq, p := range entryReq.Postings {
			acc, exists := accountMap[p.AccountID]
			if !exists {
				return nil, nil, nil, decimal.Zero, fmt.Errorf("%w: %s", ErrAccountNotFound, p.AccountID)
			}
			if acc.Status != "ACTIVE" {
				return nil, nil, nil, decimal.Zero, fmt.Errorf("%w: account %s is %s", ErrAccountInactive, p.AccountID, acc.Status)
			}
			if acc.Currency != entryReq.Currency {
				return nil, nil, nil, decimal.Zero, fmt.Errorf("%w: account currency %s != tx currency %s",
					ErrCurrencyMismatch, acc.Currency, entryReq.Currency)
			}

			delta := CalculateGAAPDelta(acc.NormalBalance, p.Direction, p.Amount)
			currBal := runningBalances[p.AccountID].Add(delta)
			runningBalances[p.AccountID] = currBal

			posting := LedgerPosting{
				ID:             uuid.New(),
				JournalEntryID: journal.ID,
				AccountID:      p.AccountID,
				Direction:      p.Direction,
				Amount:         p.Amount,
				PostBalance:    currBal,
				SequenceNo:     seq + 1,
				Description:    p.Description,
				CreatedAt:      now,
			}

			journal.Postings = append(journal.Postings, posting)
			allPostings = append(allPostings, posting)

			// Update summary
			summary := summaries[p.AccountID]
			summary.NetDelta = summary.NetDelta.Add(delta)
			summary.FinalBalance = currBal
			summary.PostingCount++
			if p.Direction == DirectionDebit {
				summary.NetDebit = summary.NetDebit.Add(p.Amount)
			} else {
				summary.NetCredit = summary.NetCredit.Add(p.Amount)
			}
			summaries[p.AccountID] = summary
		}

		journalEntries = append(journalEntries, journal)
	}

	return journalEntries, allPostings, summaries, totalBatchVolume, nil
}

// -----------------------------------------------------------------------------
// COCKROACHDB BULK-INSERT & COPY PIPELINE GENERATOR
// -----------------------------------------------------------------------------

// BulkInsertStatement wraps raw parameterized SQL and parameter values.
type BulkInsertStatement struct {
	SQL  string
	Args []interface{}
}

// CopyPipelineGenerator generates high-throughput CockroachDB / PostgreSQL COPY
// streams and batched multi-row INSERT / UPDATE SQL queries.
type CopyPipelineGenerator struct{}

// NewCopyPipelineGenerator constructs a generator instance.
func NewCopyPipelineGenerator() *CopyPipelineGenerator {
	return &CopyPipelineGenerator{}
}

// CopyJournalsSQL returns the standard CockroachDB COPY command for journal_entries.
func (g *CopyPipelineGenerator) CopyJournalsSQL() string {
	return `COPY journal_entries (id, ledger_id, idempotency_hash, correlation_id, external_ref, description, state, total_amount, currency, posted_at, created_at) FROM STDIN WITH (FORMAT CSV, HEADER FALSE, NULL '\N');`
}

// CopyPostingsSQL returns the standard CockroachDB COPY command for ledger_postings.
func (g *CopyPipelineGenerator) CopyPostingsSQL() string {
	return `COPY ledger_postings (id, journal_entry_id, account_id, direction, amount, post_balance, sequence_no, description, created_at) FROM STDIN WITH (FORMAT CSV, HEADER FALSE, NULL '\N');`
}

// GenerateJournalsCSV serializes journal entries into RFC 4180 / CockroachDB compliant CSV data.
func (g *CopyPipelineGenerator) GenerateJournalsCSV(entries []*JournalEntry) ([]byte, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)

	for _, entry := range entries {
		postedAtStr := `\N`
		if entry.PostedAt != nil {
			postedAtStr = entry.PostedAt.UTC().Format(time.RFC3339Nano)
		}

		record := []string{
			entry.ID.String(),
			entry.LedgerID.String(),
			entry.IdempotencyHash,
			entry.CorrelationID,
			entry.ExternalRef,
			entry.Description,
			string(entry.State),
			entry.TotalAmount.StringFixed(4),
			entry.Currency,
			postedAtStr,
			entry.CreatedAt.UTC().Format(time.RFC3339Nano),
		}

		if err := w.Write(record); err != nil {
			return nil, fmt.Errorf("failed to write journal csv record: %w", err)
		}
	}

	w.Flush()
	if err := w.Error(); err != nil {
		return nil, fmt.Errorf("journal csv flush error: %w", err)
	}

	return buf.Bytes(), nil
}

// GeneratePostingsCSV serializes ledger postings into RFC 4180 / CockroachDB compliant CSV data.
func (g *CopyPipelineGenerator) GeneratePostingsCSV(postings []LedgerPosting) ([]byte, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)

	for _, p := range postings {
		record := []string{
			p.ID.String(),
			p.JournalEntryID.String(),
			p.AccountID.String(),
			string(p.Direction),
			p.Amount.StringFixed(4),
			p.PostBalance.StringFixed(4),
			strconv.Itoa(p.SequenceNo),
			p.Description,
			p.CreatedAt.UTC().Format(time.RFC3339Nano),
		}

		if err := w.Write(record); err != nil {
			return nil, fmt.Errorf("failed to write posting csv record: %w", err)
		}
	}

	w.Flush()
	if err := w.Error(); err != nil {
		return nil, fmt.Errorf("postings csv flush error: %w", err)
	}

	return buf.Bytes(), nil
}

// NewJournalsCopyReader returns a streaming io.Reader for piping directly into CockroachDB COPY.
func (g *CopyPipelineGenerator) NewJournalsCopyReader(entries []*JournalEntry) (io.Reader, error) {
	data, err := g.GenerateJournalsCSV(entries)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(data), nil
}

// NewPostingsCopyReader returns a streaming io.Reader for piping directly into CockroachDB COPY.
func (g *CopyPipelineGenerator) NewPostingsCopyReader(postings []LedgerPosting) (io.Reader, error) {
	data, err := g.GeneratePostingsCSV(postings)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(data), nil
}

// GenerateBulkInsertJournalsSQL generates parameterized multi-row INSERT queries in chunks.
func (g *CopyPipelineGenerator) GenerateBulkInsertJournalsSQL(entries []*JournalEntry, chunkSize int) ([]BulkInsertStatement, error) {
	if chunkSize <= 0 {
		chunkSize = 1000
	}
	if len(entries) == 0 {
		return nil, nil
	}

	numStatements := (len(entries) + chunkSize - 1) / chunkSize
	statements := make([]BulkInsertStatement, 0, numStatements)

	for i := 0; i < len(entries); i += chunkSize {
		end := i + chunkSize
		if end > len(entries) {
			end = len(entries)
		}
		chunk := entries[i:end]

		var sqlBuf strings.Builder
		sqlBuf.WriteString("INSERT INTO journal_entries (id, ledger_id, idempotency_hash, correlation_id, external_ref, description, state, total_amount, currency, posted_at, created_at) VALUES ")

		args := make([]interface{}, 0, len(chunk)*11)
		for j, entry := range chunk {
			if j > 0 {
				sqlBuf.WriteString(", ")
			}
			base := j * 11
			sqlBuf.WriteString(fmt.Sprintf("($%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d)",
				base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8, base+9, base+10, base+11))

			args = append(args,
				entry.ID,
				entry.LedgerID,
				entry.IdempotencyHash,
				entry.CorrelationID,
				entry.ExternalRef,
				entry.Description,
				entry.State,
				entry.TotalAmount,
				entry.Currency,
				entry.PostedAt,
				entry.CreatedAt,
			)
		}
		sqlBuf.WriteString(" ON CONFLICT (idempotency_hash) DO NOTHING;")
		statements = append(statements, BulkInsertStatement{
			SQL:  sqlBuf.String(),
			Args: args,
		})
	}

	return statements, nil
}

// GenerateBulkInsertPostingsSQL generates parameterized multi-row INSERT queries for postings.
func (g *CopyPipelineGenerator) GenerateBulkInsertPostingsSQL(postings []LedgerPosting, chunkSize int) ([]BulkInsertStatement, error) {
	if chunkSize <= 0 {
		chunkSize = 2000
	}
	if len(postings) == 0 {
		return nil, nil
	}

	numStatements := (len(postings) + chunkSize - 1) / chunkSize
	statements := make([]BulkInsertStatement, 0, numStatements)

	for i := 0; i < len(postings); i += chunkSize {
		end := i + chunkSize
		if end > len(postings) {
			end = len(postings)
		}
		chunk := postings[i:end]

		var sqlBuf strings.Builder
		sqlBuf.WriteString("INSERT INTO ledger_postings (id, journal_entry_id, account_id, direction, amount, post_balance, sequence_no, description, created_at) VALUES ")

		args := make([]interface{}, 0, len(chunk)*9)
		for j, p := range chunk {
			if j > 0 {
				sqlBuf.WriteString(", ")
			}
			base := j * 9
			sqlBuf.WriteString(fmt.Sprintf("($%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d)",
				base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8, base+9))

			args = append(args,
				p.ID,
				p.JournalEntryID,
				p.AccountID,
				p.Direction,
				p.Amount,
				p.PostBalance,
				p.SequenceNo,
				p.Description,
				p.CreatedAt,
			)
		}
		sqlBuf.WriteString(";")
		statements = append(statements, BulkInsertStatement{
			SQL:  sqlBuf.String(),
			Args: args,
		})
	}

	return statements, nil
}

// GenerateBulkAccountUpdateSQL generates high-velocity batch update SQL for accounts
// utilizing CockroachDB / PostgreSQL FROM (VALUES ...) semantics:
// UPDATE accounts AS a SET current_balance = a.current_balance + v.delta, version = a.version + v.ver_inc ...
func (g *CopyPipelineGenerator) GenerateBulkAccountUpdateSQL(summaries []BulkAccountSummary) (BulkInsertStatement, error) {
	if len(summaries) == 0 {
		return BulkInsertStatement{}, nil
	}

	var sqlBuf strings.Builder
	sqlBuf.WriteString("UPDATE accounts AS a SET current_balance = a.current_balance + v.delta, version = a.version + v.ver_inc, updated_at = NOW() FROM (VALUES ")

	args := make([]interface{}, 0, len(summaries)*3)
	for i, s := range summaries {
		if i > 0 {
			sqlBuf.WriteString(", ")
		}
		base := i * 3
		sqlBuf.WriteString(fmt.Sprintf("($%d::uuid, $%d::numeric, $%d::bigint)", base+1, base+2, base+3))
		args = append(args, s.AccountID, s.NetDelta, int64(s.PostingCount))
	}
	sqlBuf.WriteString(") AS v(id, delta, ver_inc) WHERE a.id = v.id;")

	return BulkInsertStatement{
		SQL:  sqlBuf.String(),
		Args: args,
	}, nil
}

// -----------------------------------------------------------------------------
// HIGH-VELOCITY ATOMIC BATCH POSTING ENGINE
// -----------------------------------------------------------------------------

// BatchPosterConfig configures the concurrency and throughput characteristics of the engine.
type BatchPosterConfig struct {
	StripeCount       uint32
	MaxBatchSize      int
	WorkerConcurrency int
}

// DefaultBatchPosterConfig provides production-tuned defaults targeting >50,000 postings/sec.
func DefaultBatchPosterConfig() BatchPosterConfig {
	workers := runtime.NumCPU() * 2
	if workers < 4 {
		workers = 4
	}
	return BatchPosterConfig{
		StripeCount:       1024,
		MaxBatchSize:      100000,
		WorkerConcurrency: workers,
	}
}

// BatchPoster implements the high-velocity batch posting engine.
type BatchPoster struct {
	repo        *InMemLedgerRepository
	lockPool    *LockStripePool
	calc        *BulkBalanceCalculator
	pipelineGen *CopyPipelineGenerator
	config      BatchPosterConfig
}

// NewBatchPoster initializes a new BatchPoster engine backed by InMemLedgerRepository.
func NewBatchPoster(repo *InMemLedgerRepository, cfg ...BatchPosterConfig) *BatchPoster {
	config := DefaultBatchPosterConfig()
	if len(cfg) > 0 {
		config = cfg[0]
	}
	return &BatchPoster{
		repo:        repo,
		lockPool:    NewLockStripePool(config.StripeCount),
		calc:        NewBulkBalanceCalculator(),
		pipelineGen: NewCopyPipelineGenerator(),
		config:      config,
	}
}

// PipelineGenerator exposes the CockroachDB pipeline generator.
func (bp *BatchPoster) PipelineGenerator() *CopyPipelineGenerator {
	return bp.pipelineGen
}

// LockPool exposes the internal lock stripe pool for diagnostics or custom striping.
func (bp *BatchPoster) LockPool() *LockStripePool {
	return bp.lockPool
}

// PostBatch commits an entire batch of journal entries atomically at high velocity
// while strictly enforcing GAAP invariants, lock striping, and deadlock-free execution.
func (bp *BatchPoster) PostBatch(ctx context.Context, req BatchPostingRequest) (*BatchPostingResult, error) {
	startTime := time.Now()

	// 1. Basic sanity & capacity checks
	if len(req.Entries) == 0 {
		return nil, ErrEmptyBatch
	}
	if len(req.Entries) > bp.config.MaxBatchSize {
		return nil, fmt.Errorf("%w: batch has %d entries, max is %d",
			ErrBatchSizeExceeded, len(req.Entries), bp.config.MaxBatchSize)
	}

	// 2. Validate GAAP double-entry invariants across entire batch
	if err := ParallelValidateBatchInvariants(&req, bp.config.WorkerConcurrency); err != nil {
		return nil, err
	}

	// 3. Extract unique affected accounts and sort them deterministically
	sortedAccountIDs := ExtractUniqueSortedAccountIDs(&req)

	// 4. Acquire lock-striped mutexes in strictly ascending order (deadlock elimination)
	unlockStripes := bp.lockPool.LockAccounts(sortedAccountIDs)
	defer unlockStripes()

	// 5. Acquire repository-level account locks in deterministic order
	bp.repo.mu.RLock()
	locks := make([]*sync.Mutex, 0, len(sortedAccountIDs))
	accountSnapshots := make(map[uuid.UUID]*Account, len(sortedAccountIDs))

	for _, accID := range sortedAccountIDs {
		lk, exists := bp.repo.accountLocks[accID]
		if !exists {
			bp.repo.mu.RUnlock()
			return nil, fmt.Errorf("%w: account %s", ErrAccountNotFound, accID)
		}
		locks = append(locks, lk)

		acc := bp.repo.accounts[accID]
		if acc == nil {
			bp.repo.mu.RUnlock()
			return nil, fmt.Errorf("%w: account %s", ErrAccountNotFound, accID)
		}
		// Snapshot account state for bulk balance calculation
		accCopy := *acc
		accountSnapshots[accID] = &accCopy
	}
	bp.repo.mu.RUnlock()

	for _, lk := range locks {
		lk.Lock()
		defer lk.Unlock()
	}

	// 6. Idempotency Pre-Check under critical lock
	bp.repo.mu.RLock()
	for _, entryReq := range req.Entries {
		idempHash := ComputeIdempotencyHash(entryReq.LedgerID, entryReq.IdempotencyKey, entryReq.Currency, entryReq.Postings)
		if _, exists := bp.repo.idempIndex[idempHash]; exists {
			bp.repo.mu.RUnlock()
			return nil, fmt.Errorf("%w: key %q already committed", ErrDuplicateIdempotency, entryReq.IdempotencyKey)
		}
	}
	bp.repo.mu.RUnlock()

	// 7. Bulk Balance Calculation
	now := time.Now().UTC()
	journalEntries, allPostings, summaries, totalVolume, err := bp.calc.ComputeBatchPostings(
		&req,
		accountSnapshots,
		now,
	)
	if err != nil {
		return nil, err
	}

	// 8. Commit State Atomically
	bp.repo.mu.Lock()
	// Final safety check on idempotency
	for _, j := range journalEntries {
		if _, exists := bp.repo.idempIndex[j.IdempotencyHash]; exists {
			bp.repo.mu.Unlock()
			return nil, fmt.Errorf("%w: concurrent idempotency collision for %s", ErrDuplicateIdempotency, j.IdempotencyHash)
		}
	}

	// Commit journal entries and postings
	for _, j := range journalEntries {
		bp.repo.journals[j.ID] = j
		bp.repo.idempIndex[j.IdempotencyHash] = j.ID
		bp.repo.postings[j.ID] = j.Postings
	}

	// Update account balances and versions
	for accID, summary := range summaries {
		acc := bp.repo.accounts[accID]
		acc.CurrentBalance = summary.FinalBalance
		acc.Version += int64(summary.PostingCount)
		acc.UpdatedAt = now
	}
	bp.repo.mu.Unlock()

	duration := time.Since(startTime)
	var throughput float64
	if duration.Seconds() > 0 {
		throughput = float64(len(allPostings)) / duration.Seconds()
	}

	return &BatchPostingResult{
		BatchID:           req.BatchID,
		LedgerID:          req.LedgerID,
		TotalEntries:      len(journalEntries),
		TotalPostings:     len(allPostings),
		TotalVolume:       totalVolume,
		ExecutionDuration: duration,
		ThroughputPPS:     throughput,
		JournalEntries:    journalEntries,
		AccountSummaries:  summaries,
		CommittedAt:       now,
	}, nil
}
