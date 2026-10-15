package engine

import (
	"context"
	"testing"

	"datavendor-payment-core/services/core-ledger-engine/repository"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestAccountingEngine_ExecuteTransfer(t *testing.T) {
	ctx := context.Background()
	repo := repository.NewInMemLedgerRepository()
	eng := NewAccountingEngine(repo)

	ledgerID := uuid.New()
	repo.CreateLedger(ctx, &repository.Ledger{ID: ledgerID, Name: "Main", BaseCurrency: "USD"})

	acc1 := &repository.Account{
		ID:            uuid.New(),
		LedgerID:      ledgerID,
		Code:          "1010",
		Name:          "Operating Account",
		AccountType:   repository.AccountTypeAsset,
		NormalBalance: repository.DirectionDebit,
		Currency:      "USD",
	}
	acc2 := &repository.Account{
		ID:            uuid.New(),
		LedgerID:      ledgerID,
		Code:          "1020",
		Name:          "Settlement Clearing Account",
		AccountType:   repository.AccountTypeAsset,
		NormalBalance: repository.DirectionDebit,
		Currency:      "USD",
	}
	repo.CreateAccount(ctx, acc1)
	repo.CreateAccount(ctx, acc2)

	amount, _ := decimal.NewFromString("7500.0000")
	req := TransferRequest{
		LedgerID:       ledgerID,
		SourceAccount:  acc1.ID,
		DestAccount:    acc2.ID,
		Amount:         amount,
		Currency:       "USD",
		IdempotencyKey: "transfer-001",
		CorrelationID:  "corr-tx-001",
		Description:    "Liquidity pre-fund transfer",
	}

	entry, err := eng.ExecuteTransfer(ctx, req)
	if err != nil {
		t.Fatalf("transfer failed: %v", err)
	}

	if entry.State != repository.StatePosted {
		t.Errorf("expected state POSTED, got %s", entry.State)
	}

	bal1, _ := eng.VerifyAccountBalance(ctx, acc1.ID)
	bal2, _ := eng.VerifyAccountBalance(ctx, acc2.ID)

	// Since acc1 is an Asset (normal debit) and was credited, balance should decrease (-7500.0000)
	// and acc2 was debited, balance should increase (+7500.0000)
	expectedOutflow := amount.Neg()
	if !bal1.Equal(expectedOutflow) {
		t.Errorf("acc1 balance mismatch. Want %s, got %s", expectedOutflow, bal1)
	}
	if !bal2.Equal(amount) {
		t.Errorf("acc2 balance mismatch. Want %s, got %s", amount, bal2)
	}
}
