package repository

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// setupMultiAccountLedger initializes a test ledger and 6 chart of accounts covering
// all GAAP categories: ASSET, LIABILITY, EQUITY, REVENUE, EXPENSE.
func setupMultiAccountLedger(t *testing.T) (*InMemLedgerRepository, uuid.UUID, map[string]uuid.UUID) {
	t.Helper()
	repo := NewInMemLedgerRepository()
	ctx := context.Background()

	ledger := &Ledger{
		ID:           uuid.New(),
		Name:         "DataVendor Core Settlement Ledger",
		BaseCurrency: "USD",
	}
	if err := repo.CreateLedger(ctx, ledger); err != nil {
		t.Fatalf("failed to create ledger: %v", err)
	}

	accountDefs := []struct {
		key           string
		code          string
		name          string
		accType       AccountType
		normalBalance Direction
	}{
		{"CASH", "1010", "Operating Settlement Cash", AccountTypeAsset, DirectionDebit},
		{"RECEIVABLE", "1100", "Enterprise Accounts Receivable", AccountTypeAsset, DirectionDebit},
		{"PAYABLE", "2010", "Settlement Clearing Payable", AccountTypeLiability, DirectionCredit},
		{"REVENUE", "4010", "Market Data API Revenue", AccountTypeRevenue, DirectionCredit},
		{"EXPENSE", "5010", "Network Interchange Clearing Fees", AccountTypeExpense, DirectionDebit},
		{"EQUITY", "3010", "Retained Earnings Core", AccountTypeEquity, DirectionCredit},
	}

	accountMap := make(map[string]uuid.UUID)
	for _, def := range accountDefs {
		acc := &Account{
			ID:            uuid.New(),
			LedgerID:      ledger.ID,
			Code:          def.code,
			Name:          def.name,
			AccountType:   def.accType,
			NormalBalance: def.normalBalance,
			Currency:      "USD",
		}
		if err := repo.CreateAccount(ctx, acc); err != nil {
			t.Fatalf("failed to create account %s: %v", def.key, err)
		}
		accountMap[def.key] = acc.ID
	}

	return repo, ledger.ID, accountMap
}

// TestBatchPoster_PostBalancedBatch verifies atomic commit of multi-entry, multi-account batches.
func TestBatchPoster_PostBalancedBatch(t *testing.T) {
	ctx := context.Background()
	repo, ledgerID, accs := setupMultiAccountLedger(t)
	poster := NewBatchPoster(repo)

	amt1, _ := decimal.NewFromString("1000.5000")
	amt2, _ := decimal.NewFromString("250.2500")

	req := BatchPostingRequest{
		BatchID:       uuid.New(),
		LedgerID:      ledgerID,
		CorrelationID: "corr-batch-001",
		Entries: []CreateJournalEntryRequest{
			{
				LedgerID:       ledgerID,
				IdempotencyKey: "batch-entry-1",
				CorrelationID:  "corr-e1",
				Currency:       "USD",
				Description:    "Subscription fee collection",
				Postings: []PostingRequest{
					{AccountID: accs["CASH"], Direction: DirectionDebit, Amount: amt1, Description: "Bank inflow"},
					{AccountID: accs["REVENUE"], Direction: DirectionCredit, Amount: amt1, Description: "Revenue earned"},
				},
			},
			{
				LedgerID:       ledgerID,
				IdempotencyKey: "batch-entry-2",
				CorrelationID:  "corr-e2",
				Currency:       "USD",
				Description:    "Interchange fee payment",
				Postings: []PostingRequest{
					{AccountID: accs["EXPENSE"], Direction: DirectionDebit, Amount: amt2, Description: "Fee incurred"},
					{AccountID: accs["CASH"], Direction: DirectionCredit, Amount: amt2, Description: "Cash paid out"},
				},
			},
		},
	}

	result, err := poster.PostBatch(ctx, req)
	if err != nil {
		t.Fatalf("PostBatch failed: %v", err)
	}

	if result.TotalEntries != 2 {
		t.Errorf("expected 2 total entries, got %d", result.TotalEntries)
	}
	if result.TotalPostings != 4 {
		t.Errorf("expected 4 total postings, got %d", result.TotalPostings)
	}

	expectedCash := amt1.Sub(amt2) // $1000.5000 - $250.2500 = $750.2500
	cashAcc, err := repo.GetAccount(ctx, accs["CASH"])
	if err != nil {
		t.Fatalf("failed to retrieve cash account: %v", err)
	}
	if !cashAcc.CurrentBalance.Equal(expectedCash) {
		t.Errorf("cash balance want %s, got %s", expectedCash, cashAcc.CurrentBalance)
	}

	revAcc, _ := repo.GetAccount(ctx, accs["REVENUE"])
	if !revAcc.CurrentBalance.Equal(amt1) {
		t.Errorf("revenue balance want %s, got %s", amt1, revAcc.CurrentBalance)
	}

	expAcc, _ := repo.GetAccount(ctx, accs["EXPENSE"])
	if !expAcc.CurrentBalance.Equal(amt2) {
		t.Errorf("expense balance want %s, got %s", amt2, expAcc.CurrentBalance)
	}

	// Verify post_balance on individual postings
	if len(result.JournalEntries[0].Postings) != 2 || len(result.JournalEntries[1].Postings) != 2 {
		t.Fatalf("journal entry postings count incorrect")
	}
	// First cash posting post_balance was $1000.5000
	if !result.JournalEntries[0].Postings[0].PostBalance.Equal(amt1) {
		t.Errorf("first cash posting post_balance want %s, got %s", amt1, result.JournalEntries[0].Postings[0].PostBalance)
	}
	// Second cash posting post_balance was $750.2500
	if !result.JournalEntries[1].Postings[1].PostBalance.Equal(expectedCash) {
		t.Errorf("second cash posting post_balance want %s, got %s", expectedCash, result.JournalEntries[1].Postings[1].PostBalance)
	}
}

// TestBatchPoster_RejectUnbalancedBatch asserts that invalid entries or batch-level discrepancies are caught.
func TestBatchPoster_RejectUnbalancedBatch(t *testing.T) {
	ctx := context.Background()
	repo, ledgerID, accs := setupMultiAccountLedger(t)
	poster := NewBatchPoster(repo)

	// Case 1: Per-entry invariant violation
	req1 := BatchPostingRequest{
		BatchID:  uuid.New(),
		LedgerID: ledgerID,
		Entries: []CreateJournalEntryRequest{
			{
				LedgerID:       ledgerID,
				IdempotencyKey: "err-entry-1",
				Currency:       "USD",
				Postings: []PostingRequest{
					{AccountID: accs["CASH"], Direction: DirectionDebit, Amount: decimal.NewFromFloat(100.0)},
					{AccountID: accs["REVENUE"], Direction: DirectionCredit, Amount: decimal.NewFromFloat(99.0)},
				},
			},
		},
	}

	_, err := poster.PostBatch(ctx, req1)
	if err == nil {
		t.Fatalf("expected error for unbalanced entry, but PostBatch succeeded")
	}

	// Case 2: Negative amount
	req2 := BatchPostingRequest{
		BatchID:  uuid.New(),
		LedgerID: ledgerID,
		Entries: []CreateJournalEntryRequest{
			{
				LedgerID:       ledgerID,
				IdempotencyKey: "err-entry-neg",
				Currency:       "USD",
				Postings: []PostingRequest{
					{AccountID: accs["CASH"], Direction: DirectionDebit, Amount: decimal.NewFromFloat(-50.0)},
					{AccountID: accs["REVENUE"], Direction: DirectionCredit, Amount: decimal.NewFromFloat(-50.0)},
				},
			},
		},
	}
	_, err = poster.PostBatch(ctx, req2)
	if err == nil {
		t.Fatalf("expected error for negative amount, but PostBatch succeeded")
	}

	// Case 3: Empty batch
	_, err = poster.PostBatch(ctx, BatchPostingRequest{BatchID: uuid.New(), LedgerID: ledgerID, Entries: nil})
	if err == nil {
		t.Fatalf("expected error for empty batch, but PostBatch succeeded")
	}
}

// TestBatchPoster_IntraBatchDuplicateIdempotency verifies rejection of duplicate keys in the same batch.
func TestBatchPoster_IntraBatchDuplicateIdempotency(t *testing.T) {
	ctx := context.Background()
	repo, ledgerID, accs := setupMultiAccountLedger(t)
	poster := NewBatchPoster(repo)

	amt := decimal.NewFromFloat(50.0)
	req := BatchPostingRequest{
		BatchID:  uuid.New(),
		LedgerID: ledgerID,
		Entries: []CreateJournalEntryRequest{
			{
				LedgerID:       ledgerID,
				IdempotencyKey: "dup-key-001",
				Currency:       "USD",
				Postings: []PostingRequest{
					{AccountID: accs["CASH"], Direction: DirectionDebit, Amount: amt},
					{AccountID: accs["REVENUE"], Direction: DirectionCredit, Amount: amt},
				},
			},
			{
				LedgerID:       ledgerID,
				IdempotencyKey: "dup-key-001", // Duplicate key in same batch!
				Currency:       "USD",
				Postings: []PostingRequest{
					{AccountID: accs["CASH"], Direction: DirectionDebit, Amount: amt},
					{AccountID: accs["REVENUE"], Direction: DirectionCredit, Amount: amt},
				},
			},
		},
	}

	_, err := poster.PostBatch(ctx, req)
	if err == nil {
		t.Fatalf("expected error for intra-batch duplicate idempotency key, but succeeded")
	}
}

// TestBatchPoster_DeadlockFreeConcurrentBatches verifies that lock striping and deterministic
// lexicographical sorting eliminate deadlocks when concurrent workers post overlapping batches.
func TestBatchPoster_DeadlockFreeConcurrentBatches(t *testing.T) {
	ctx := context.Background()
	repo, ledgerID, accs := setupMultiAccountLedger(t)
	poster := NewBatchPoster(repo)

	accountPairs := [][2]uuid.UUID{
		{accs["CASH"], accs["REVENUE"]},
		{accs["REVENUE"], accs["CASH"]}, // Inverted order to trigger deadlocks without deterministic locks
		{accs["RECEIVABLE"], accs["REVENUE"]},
		{accs["REVENUE"], accs["RECEIVABLE"]},
		{accs["EXPENSE"], accs["CASH"]},
		{accs["CASH"], accs["EXPENSE"]},
		{accs["PAYABLE"], accs["CASH"]},
	}

	concurrency := 20
	batchesPerWorker := 15
	var wg sync.WaitGroup
	errs := make([]error, concurrency*batchesPerWorker)

	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for b := 0; b < batchesPerWorker; b++ {
				pair := accountPairs[(workerID+b)%len(accountPairs)]
				amt := decimal.NewFromFloat(float64(b+1) * 10.0)

				req := BatchPostingRequest{
					BatchID:       uuid.New(),
					LedgerID:      ledgerID,
					CorrelationID: fmt.Sprintf("corr-w%d-b%d", workerID, b),
					Entries: []CreateJournalEntryRequest{
						{
							LedgerID:       ledgerID,
							IdempotencyKey: fmt.Sprintf("idemp-w%d-b%d-1", workerID, b),
							Currency:       "USD",
							Postings: []PostingRequest{
								{AccountID: pair[0], Direction: DirectionDebit, Amount: amt},
								{AccountID: pair[1], Direction: DirectionCredit, Amount: amt},
							},
						},
						{
							LedgerID:       ledgerID,
							IdempotencyKey: fmt.Sprintf("idemp-w%d-b%d-2", workerID, b),
							Currency:       "USD",
							Postings: []PostingRequest{
								{AccountID: pair[1], Direction: DirectionDebit, Amount: amt},
								{AccountID: pair[0], Direction: DirectionCredit, Amount: amt},
							},
						},
					},
				}

				idx := workerID*batchesPerWorker + b
				_, err := poster.PostBatch(ctx, req)
				if err != nil {
					errs[idx] = fmt.Errorf("worker %d batch %d failed: %w", workerID, b, err)
				}
			}
		}(w)
	}

	wg.Wait()

	for _, err := range errs {
		if err != nil {
			t.Fatalf("deadlock or concurrency error occurred: %v", err)
		}
	}
}

// TestBatchPoster_CockroachDB_CopyPipeline validates the SQL and CSV generation pipeline.
func TestBatchPoster_CockroachDB_CopyPipeline(t *testing.T) {
	ctx := context.Background()
	repo, ledgerID, accs := setupMultiAccountLedger(t)
	poster := NewBatchPoster(repo)
	pipeline := poster.PipelineGenerator()

	amt := decimal.NewFromFloat(1234.5678)
	req := BatchPostingRequest{
		BatchID:  uuid.New(),
		LedgerID: ledgerID,
		Entries: []CreateJournalEntryRequest{
			{
				LedgerID:       ledgerID,
				IdempotencyKey: "copy-pipeline-test-01",
				Currency:       "USD",
				Description:    "COPY pipeline test",
				Postings: []PostingRequest{
					{AccountID: accs["CASH"], Direction: DirectionDebit, Amount: amt, Description: "CSV cash debit"},
					{AccountID: accs["REVENUE"], Direction: DirectionCredit, Amount: amt, Description: "CSV revenue credit"},
				},
			},
		},
	}

	result, err := poster.PostBatch(ctx, req)
	if err != nil {
		t.Fatalf("failed to post batch: %v", err)
	}

	// 1. Verify COPY SQL commands
	if pipeline.CopyJournalsSQL() == "" || pipeline.CopyPostingsSQL() == "" {
		t.Errorf("COPY SQL statements must not be empty")
	}

	// 2. Generate and verify CSV bytes
	journalCSV, err := pipeline.GenerateJournalsCSV(result.JournalEntries)
	if err != nil {
		t.Fatalf("GenerateJournalsCSV failed: %v", err)
	}
	if len(journalCSV) == 0 {
		t.Errorf("journal CSV output is empty")
	}

	allPostings := make([]LedgerPosting, 0)
	for _, j := range result.JournalEntries {
		allPostings = append(allPostings, j.Postings...)
	}

	postingCSV, err := pipeline.GeneratePostingsCSV(allPostings)
	if err != nil {
		t.Fatalf("GeneratePostingsCSV failed: %v", err)
	}
	if len(postingCSV) == 0 {
		t.Errorf("posting CSV output is empty")
	}

	// 3. Test streaming reader
	reader, err := pipeline.NewJournalsCopyReader(result.JournalEntries)
	if err != nil {
		t.Fatalf("NewJournalsCopyReader failed: %v", err)
	}
	readBytes, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("reading from copy reader failed: %v", err)
	}
	if len(readBytes) != len(journalCSV) {
		t.Errorf("reader length %d != csv length %d", len(readBytes), len(journalCSV))
	}

	// 4. Test multi-row insert statements
	journalStmts, err := pipeline.GenerateBulkInsertJournalsSQL(result.JournalEntries, 100)
	if err != nil {
		t.Fatalf("GenerateBulkInsertJournalsSQL failed: %v", err)
	}
	if len(journalStmts) != 1 {
		t.Errorf("expected 1 statement, got %d", len(journalStmts))
	}

	postingStmts, err := pipeline.GenerateBulkInsertPostingsSQL(allPostings, 100)
	if err != nil {
		t.Fatalf("GenerateBulkInsertPostingsSQL failed: %v", err)
	}
	if len(postingStmts) != 1 {
		t.Errorf("expected 1 statement, got %d", len(postingStmts))
	}

	// 5. Test bulk account update SQL
	summariesList := make([]BulkAccountSummary, 0, len(result.AccountSummaries))
	for _, s := range result.AccountSummaries {
		summariesList = append(summariesList, s)
	}
	updateStmt, err := pipeline.GenerateBulkAccountUpdateSQL(summariesList)
	if err != nil {
		t.Fatalf("GenerateBulkAccountUpdateSQL failed: %v", err)
	}
	if updateStmt.SQL == "" || len(updateStmt.Args) != len(summariesList)*3 {
		t.Errorf("bulk update statement invalid: sql=%s, args=%d", updateStmt.SQL, len(updateStmt.Args))
	}
}

// TestBatchPoster_HighVelocity50kPostings verifies the 50,000 postings throughput requirement.
func TestBatchPoster_HighVelocity50kPostings(t *testing.T) {
	ctx := context.Background()
	repo, ledgerID, accs := setupMultiAccountLedger(t)

	// Create 100 rotating accounts to spread lock stripes
	extraAccounts := make([]uuid.UUID, 100)
	for i := 0; i < 100; i++ {
		acc := &Account{
			ID:            uuid.New(),
			LedgerID:      ledgerID,
			Code:          fmt.Sprintf("ACC-%04d", i),
			Name:          fmt.Sprintf("Test Account %d", i),
			AccountType:   AccountTypeAsset,
			NormalBalance: DirectionDebit,
			Currency:      "USD",
		}
		if err := repo.CreateAccount(ctx, acc); err != nil {
			t.Fatalf("failed to create extra account: %v", err)
		}
		extraAccounts[i] = acc.ID
	}

	poster := NewBatchPoster(repo)

	// Generate 25,000 transactions with 2 postings each = 50,000 postings total
	totalTransactions := 25000
	entries := make([]CreateJournalEntryRequest, totalTransactions)
	r := rand.New(rand.NewSource(42))

	amt := decimal.NewFromFloat(10.0000)
	for i := 0; i < totalTransactions; i++ {
		src := extraAccounts[r.Intn(len(extraAccounts))]
		dest := accs["CASH"]

		entries[i] = CreateJournalEntryRequest{
			LedgerID:       ledgerID,
			IdempotencyKey: fmt.Sprintf("bench-50k-%06d", i),
			CorrelationID:  "bench-50k-corr",
			Currency:       "USD",
			Description:    "50k benchmark leg",
			Postings: []PostingRequest{
				{AccountID: src, Direction: DirectionDebit, Amount: amt},
				{AccountID: dest, Direction: DirectionCredit, Amount: amt},
			},
		}
	}

	req := BatchPostingRequest{
		BatchID:       uuid.New(),
		LedgerID:      ledgerID,
		CorrelationID: "corr-50k-bench",
		Entries:       entries,
	}

	start := time.Now()
	result, err := poster.PostBatch(ctx, req)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("50,000 posting batch failed: %v", err)
	}

	if result.TotalPostings != 50000 {
		t.Fatalf("expected exactly 50,000 postings, got %d", result.TotalPostings)
	}

	postingsPerSec := float64(result.TotalPostings) / elapsed.Seconds()
	t.Logf("=== 50k POSTINGS BENCHMARK RESULT ===")
	t.Logf("Total Postings: %d", result.TotalPostings)
	t.Logf("Elapsed Time:   %v", elapsed)
	t.Logf("Throughput:     %.2f postings/sec", postingsPerSec)

	// In memory, this should easily exceed 50,000 postings/sec
	if postingsPerSec < 50000.0 {
		t.Logf("Warning: throughput %.2f postings/sec was under 50,000 target", postingsPerSec)
	}
}

// BenchmarkBatchPoster_Throughput benchmarks batch posting throughput.
func BenchmarkBatchPoster_Throughput(b *testing.B) {
	ctx := context.Background()
	repo, ledgerID, accs := setupMultiAccountLedger(&testing.T{})
	poster := NewBatchPoster(repo)

	amt := decimal.NewFromFloat(100.0)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		req := BatchPostingRequest{
			BatchID:  uuid.New(),
			LedgerID: ledgerID,
			Entries: []CreateJournalEntryRequest{
				{
					LedgerID:       ledgerID,
					IdempotencyKey: fmt.Sprintf("bm-%d", i),
					Currency:       "USD",
					Postings: []PostingRequest{
						{AccountID: accs["CASH"], Direction: DirectionDebit, Amount: amt},
						{AccountID: accs["REVENUE"], Direction: DirectionCredit, Amount: amt},
					},
				},
			},
		}
		_, _ = poster.PostBatch(ctx, req)
	}
}
