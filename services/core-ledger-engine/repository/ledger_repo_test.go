package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func setupTestLedger(t *testing.T) (*InMemLedgerRepository, uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	repo := NewInMemLedgerRepository()
	ctx := context.Background()

	ledger := &Ledger{
		ID:           uuid.New(),
		Name:         "DataVendor Master Operating Ledger",
		BaseCurrency: "USD",
	}
	if err := repo.CreateLedger(ctx, ledger); err != nil {
		t.Fatalf("failed to create test ledger: %v", err)
	}

	// 1000: Cash at Bank (Asset, Normal Debit)
	cashAccount := &Account{
		ID:            uuid.New(),
		LedgerID:      ledger.ID,
		Code:          "1000",
		Name:          "Operating Cash JPMC",
		AccountType:   AccountTypeAsset,
		NormalBalance: DirectionDebit,
		Currency:      "USD",
	}
	if err := repo.CreateAccount(ctx, cashAccount); err != nil {
		t.Fatalf("failed to create cash account: %v", err)
	}

	// 4000: Data Sales Revenue (Revenue, Normal Credit)
	revenueAccount := &Account{
		ID:            uuid.New(),
		LedgerID:      ledger.ID,
		Code:          "4000",
		Name:          "Enterprise Dataset Sales Revenue",
		AccountType:   AccountTypeRevenue,
		NormalBalance: DirectionCredit,
		Currency:      "USD",
	}
	if err := repo.CreateAccount(ctx, revenueAccount); err != nil {
		t.Fatalf("failed to create revenue account: %v", err)
	}

	return repo, ledger.ID, cashAccount.ID, revenueAccount.ID
}

func TestLedger_PostBalancedEntry(t *testing.T) {
	ctx := context.Background()
	repo, ledgerID, cashAccID, revAccID := setupTestLedger(t)

	amount, _ := decimal.NewFromString("12500.5000") // $12,500.50

	req := CreateJournalEntryRequest{
		LedgerID:       ledgerID,
		IdempotencyKey: "tx-cust-corp-001",
		CorrelationID:  "corr-9921",
		ExternalRef:    "ext-sales-order-441",
		Description:    "Dataset batch sale: Financial Tick Stream 2026",
		Currency:       "USD",
		Postings: []PostingRequest{
			{
				AccountID:   cashAccID,
				Direction:   DirectionDebit,
				Amount:      amount,
				Description: "Inflow to cash at bank",
			},
			{
				AccountID:   revAccID,
				Direction:   DirectionCredit,
				Amount:      amount,
				Description: "Recognized dataset revenue",
			},
		},
	}

	entry, err := repo.PostJournalEntry(ctx, req)
	if err != nil {
		t.Fatalf("failed to post valid balanced journal entry: %v", err)
	}

	if entry.State != StatePosted {
		t.Errorf("expected state POSTED, got %s", entry.State)
	}
	if !entry.TotalAmount.Equal(amount) {
		t.Errorf("expected total amount %s, got %s", amount, entry.TotalAmount)
	}

	// Verify account balances
	cashAcc, _ := repo.GetAccount(ctx, cashAccID)
	if !cashAcc.CurrentBalance.Equal(amount) {
		t.Errorf("cash account balance mismatch. Want %s, got %s", amount, cashAcc.CurrentBalance)
	}

	revAcc, _ := repo.GetAccount(ctx, revAccID)
	if !revAcc.CurrentBalance.Equal(amount) {
		t.Errorf("revenue account balance mismatch. Want %s, got %s", amount, revAcc.CurrentBalance)
	}
}

func TestLedger_RejectUnbalancedEntry(t *testing.T) {
	ctx := context.Background()
	repo, ledgerID, cashAccID, revAccID := setupTestLedger(t)

	debitAmount, _ := decimal.NewFromString("1000.0000")
	creditAmount, _ := decimal.NewFromString("999.0000") // 1.0000 mismatch!

	req := CreateJournalEntryRequest{
		LedgerID:       ledgerID,
		IdempotencyKey: "tx-unbalanced-001",
		CorrelationID:  "corr-err",
		Currency:       "USD",
		Postings: []PostingRequest{
			{
				AccountID: cashAccID,
				Direction: DirectionDebit,
				Amount:    debitAmount,
			},
			{
				AccountID: revAccID,
				Direction: DirectionCredit,
				Amount:    creditAmount,
			},
		},
	}

	_, err := repo.PostJournalEntry(ctx, req)
	if err == nil {
		t.Fatalf("expected error posting unbalanced entry, but it succeeded")
	}
}

func TestLedger_EnforceIdempotency(t *testing.T) {
	ctx := context.Background()
	repo, ledgerID, cashAccID, revAccID := setupTestLedger(t)

	amount, _ := decimal.NewFromString("5000.0000")
	idempKey := "unique-idemp-key-555"

	req := CreateJournalEntryRequest{
		LedgerID:       ledgerID,
		IdempotencyKey: idempKey,
		CorrelationID:  "corr-idemp",
		Currency:       "USD",
		Postings: []PostingRequest{
			{AccountID: cashAccID, Direction: DirectionDebit, Amount: amount},
			{AccountID: revAccID, Direction: DirectionCredit, Amount: amount},
		},
	}

	// First execution
	entry1, err := repo.PostJournalEntry(ctx, req)
	if err != nil {
		t.Fatalf("first execution failed: %v", err)
	}

	// Second execution with identical idempotency parameters
	entry2, err := repo.PostJournalEntry(ctx, req)
	if err != nil {
		t.Fatalf("second idempotent execution failed: %v", err)
	}

	// Must return the exact same journal entry ID
	if entry1.ID != entry2.ID {
		t.Errorf("idempotency violated: got different journal IDs %s vs %s", entry1.ID, entry2.ID)
	}

	// Account balance MUST remain $5,000.00, NOT $10,000.00
	cashAcc, _ := repo.GetAccount(ctx, cashAccID)
	if !cashAcc.CurrentBalance.Equal(amount) {
		t.Fatalf("double-spending occurred! Balance is %s, expected %s", cashAcc.CurrentBalance, amount)
	}
}
