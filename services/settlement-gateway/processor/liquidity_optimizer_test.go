package processor

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestLiquidityOptimizer_BilateralNetting(t *testing.T) {
	ctx := context.Background()
	engine := NewLiquidityNettingEngine()

	// Scenario: Bank A and Bank B have mutual obligations
	// A owes B: $10,000,000 and $5,000,000 (total $15M)
	// B owes A: $12,000,000
	// Expected Bilateral Net: A owes B $3,000,000.
	// Gross: $27M, Net: $3M, Savings: $24M (88.89% savings)
	bankA := "DBEUMM21XXX" // Deutsche Bank
	bankB := "BNPAFRPPXXX" // BNP Paribas

	obligations := []GrossObligation{
		{ID: uuid.New(), DebtorBIC: bankA, CreditorBIC: bankB, Amount: decimal.NewFromInt(10000000), Currency: "EUR", CreatedAt: time.Now()},
		{ID: uuid.New(), DebtorBIC: bankA, CreditorBIC: bankB, Amount: decimal.NewFromInt(5000000), Currency: "EUR", CreatedAt: time.Now()},
		{ID: uuid.New(), DebtorBIC: bankB, CreditorBIC: bankA, Amount: decimal.NewFromInt(12000000), Currency: "EUR", CreatedAt: time.Now()},
	}

	report, err := engine.ExecuteBilateralNetting(ctx, "EUR", obligations)
	if err != nil {
		t.Fatalf("bilateral netting failed: %v", err)
	}

	if !report.GrossVolume.Equal(decimal.NewFromInt(27000000)) {
		t.Errorf("expected gross volume 27M, got %s", report.GrossVolume)
	}

	if !report.NetVolume.Equal(decimal.NewFromInt(3000000)) {
		t.Errorf("expected net volume 3M, got %s", report.NetVolume)
	}

	if len(report.NetTransfers) != 1 {
		t.Fatalf("expected 1 net transfer, got %d", len(report.NetTransfers))
	}

	netTx := report.NetTransfers[0]
	if netTx.DebtorBIC != bankA || netTx.CreditorBIC != bankB {
		t.Errorf("expected debtor %s -> creditor %s, got %s -> %s", bankA, bankB, netTx.DebtorBIC, netTx.CreditorBIC)
	}
	if !netTx.Amount.Equal(decimal.NewFromInt(3000000)) {
		t.Errorf("expected net amount 3M, got %s", netTx.Amount)
	}

	expectedSavings := decimal.NewFromInt(24000000)
	if !report.LiquiditySavingsAmount.Equal(expectedSavings) {
		t.Errorf("expected savings %s, got %s", expectedSavings, report.LiquiditySavingsAmount)
	}
}

func TestLiquidityOptimizer_MultilateralNettingCCP(t *testing.T) {
	ctx := context.Background()
	engine := NewLiquidityNettingEngine()

	ccpBIC := "CLSXBIC1XXX" // Continuous Linked Settlement / Central Counterparty

	bankA := "DBEUMM21XXX"
	bankB := "BNPAFRPPXXX"
	bankC := "CHASUS33XXX"

	// Gross obligations:
	// A -> B: $10,000,000
	// B -> C: $15,000,000
	// C -> A: $8,000,000
	// Gross Volume: $33,000,000
	// Net positions:
	// A: Receivables $8M - Payables $10M = -$2M (Debtor)
	// B: Receivables $10M - Payables $15M = -$5M (Debtor)
	// C: Receivables $15M - Payables $8M = +$7M (Creditor)
	// Invariant: (-$2M) + (-$5M) + (+$7M) = $0.00!
	obligations := []GrossObligation{
		{ID: uuid.New(), DebtorBIC: bankA, CreditorBIC: bankB, Amount: decimal.NewFromInt(10000000), Currency: "USD", CreatedAt: time.Now()},
		{ID: uuid.New(), DebtorBIC: bankB, CreditorBIC: bankC, Amount: decimal.NewFromInt(15000000), Currency: "USD", CreatedAt: time.Now()},
		{ID: uuid.New(), DebtorBIC: bankC, CreditorBIC: bankA, Amount: decimal.NewFromInt(8000000), Currency: "USD", CreatedAt: time.Now()},
	}

	report, err := engine.ExecuteMultilateralNetting(ctx, "USD", ccpBIC, obligations)
	if err != nil {
		t.Fatalf("multilateral netting failed: %v", err)
	}

	if !report.GrossVolume.Equal(decimal.NewFromInt(33000000)) {
		t.Errorf("expected gross volume 33M, got %s", report.GrossVolume)
	}

	// Net Volume required: A pays CCP $2M + B pays CCP $5M = $7M
	if !report.NetVolume.Equal(decimal.NewFromInt(7000000)) {
		t.Errorf("expected net volume 7M, got %s", report.NetVolume)
	}

	posA := report.ParticipantPositions[bankA]
	if !posA.NetPosition.Equal(decimal.NewFromInt(-2000000)) {
		t.Errorf("Bank A net position mismatch: %s", posA.NetPosition)
	}
	posB := report.ParticipantPositions[bankB]
	if !posB.NetPosition.Equal(decimal.NewFromInt(-5000000)) {
		t.Errorf("Bank B net position mismatch: %s", posB.NetPosition)
	}
	posC := report.ParticipantPositions[bankC]
	if !posC.NetPosition.Equal(decimal.NewFromInt(7000000)) {
		t.Errorf("Bank C net position mismatch: %s", posC.NetPosition)
	}
}

func TestLiquidityOptimizer_CircularDebtElimination(t *testing.T) {
	ctx := context.Background()
	engine := NewLiquidityNettingEngine()

	bankA := "BANKAAAAXXX"
	bankB := "BANKBBBBXXX"
	bankC := "BANKCCCCXXX"

	// Pure 3-node cycle:
	// A owes B $5,000,000
	// B owes C $5,000,000
	// C owes A $5,000,000
	// Total loop volume: $15M
	// Expected: Perfectly cancels to 0 residual obligations!
	obligations := []GrossObligation{
		{ID: uuid.New(), DebtorBIC: bankA, CreditorBIC: bankB, Amount: decimal.NewFromInt(5000000), Currency: "EUR", CreatedAt: time.Now()},
		{ID: uuid.New(), DebtorBIC: bankB, CreditorBIC: bankC, Amount: decimal.NewFromInt(5000000), Currency: "EUR", CreatedAt: time.Now()},
		{ID: uuid.New(), DebtorBIC: bankC, CreditorBIC: bankA, Amount: decimal.NewFromInt(5000000), Currency: "EUR", CreatedAt: time.Now()},
	}

	pruned, cancelledVolume, err := engine.EliminateCircularDebt(ctx, "EUR", obligations)
	if err != nil {
		t.Fatalf("circular debt elimination failed: %v", err)
	}

	if len(pruned) != 0 {
		t.Errorf("expected 0 residual obligations after full cycle cancellation, got %d", len(pruned))
	}

	expectedCancelled := decimal.NewFromInt(15000000)
	if !cancelledVolume.Equal(expectedCancelled) {
		t.Errorf("expected cancelled volume 15M, got %s", cancelledVolume)
	}
}
