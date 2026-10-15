package billing

import (
	"testing"
	"time"

	"datavendor-payment-core/services/b2b-metered-billing/aggregator"
	"datavendor-payment-core/services/b2b-metered-billing/kafka"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestPricingCalculator_TieredVolume(t *testing.T) {
	calc := NewPricingCalculator()

	// 15 Terabytes = 10 TB in Tier 1 ($0.15/GB) + 5 TB in Tier 2 ($0.10/GB)
	fifteenTB := int64(15) * BytesInTerabyte
	queries := int64(50000)
	computeMs := int64(1000000) // 1000 seconds

	result := calc.CalculateCharges(fifteenTB, queries, computeMs)

	if len(result.TierBreakdowns) != 2 {
		t.Fatalf("expected 2 tier breakdowns for 15TB, got %d", len(result.TierBreakdowns))
	}

	// Tier 1: 10 * 1024 GB * $0.15 = 10,240 * 0.15 = $1,536.00
	expectedTier1 := decimal.NewFromFloat(1536.00)
	if !result.TierBreakdowns[0].AmountUSD.Equal(expectedTier1) {
		t.Errorf("Tier 1 charge mismatch. Got %s, want %s", result.TierBreakdowns[0].AmountUSD, expectedTier1)
	}

	// Tier 2: 5 * 1024 GB * $0.10 = 5,120 * 0.10 = $512.00
	expectedTier2 := decimal.NewFromFloat(512.00)
	if !result.TierBreakdowns[1].AmountUSD.Equal(expectedTier2) {
		t.Errorf("Tier 2 charge mismatch. Got %s, want %s", result.TierBreakdowns[1].AmountUSD, expectedTier2)
	}

	// Query cost: 50,000 * $0.001 = $50.00
	expectedQuery := decimal.NewFromFloat(50.00)
	if !result.QueryCost.Equal(expectedQuery) {
		t.Errorf("Query cost mismatch. Got %s, want %s", result.QueryCost, expectedQuery)
	}

	// Compute cost: 1,000,000 * $0.00005 = $50.00
	expectedCompute := decimal.NewFromFloat(50.00)
	if !result.ComputeCost.Equal(expectedCompute) {
		t.Errorf("Compute cost mismatch. Got %s, want %s", result.ComputeCost, expectedCompute)
	}

	// Total Subtotal: 1536 + 512 + 50 + 50 = $2,148.00
	expectedSubtotal := decimal.NewFromFloat(2148.00)
	if !result.SubtotalAmount.Equal(expectedSubtotal) {
		t.Errorf("Subtotal mismatch. Got %s, want %s", result.SubtotalAmount, expectedSubtotal)
	}
}

func TestInvoiceGenerator_LedgerInvariant(t *testing.T) {
	calc := NewPricingCalculator()
	invGen := NewInvoiceGenerator(calc)

	tenantID := uuid.New()
	tenTB := int64(10) * BytesInTerabyte
	taxRate := decimal.NewFromFloat(0.0800) // 8% sales tax

	invoice := invGen.GenerateMonthlyInvoice(tenantID, "2026-10", tenTB, 10000, 500000, taxRate)

	if invoice.Status != InvoiceStatusIssued {
		t.Errorf("expected invoice status ISSUED, got %s", invoice.Status)
	}

	ledgerID := uuid.New()
	arAccount := uuid.New()
	revAccount := uuid.New()
	taxAccount := uuid.New()

	ledgerReq := invoice.BuildLedgerEntryRequest(ledgerID, arAccount, revAccount, taxAccount)

	// Validate GAAP double-entry invariant on the generated request
	if err := ledgerReq.ValidateInvariants(); err != nil {
		t.Fatalf("invoice generated unbalanced ledger request! Violation: %v", err)
	}
}

func TestQuotaEnforcer(t *testing.T) {
	enforcer := aggregator.NewQuotaEnforcer()
	tenantID := uuid.New()

	// 50 TB quota
	enforcer.SetQuota(aggregator.TenantQuotaDefinition{
		TenantID:          tenantID,
		MonthlyByteQuota:  50 * BytesInTerabyte,
		AllowBurstOverrun: false,
	})

	// Normal check at 20 TB (40%)
	status, err := enforcer.CheckQuota(tenantID, 20*BytesInTerabyte)
	if err != nil || status != aggregator.QuotaStatusNormal {
		t.Errorf("expected NORMAL at 40%%, got %s (err: %v)", status, err)
	}

	// Warning check at 41 TB (82%)
	status, err = enforcer.CheckQuota(tenantID, 41*BytesInTerabyte)
	if err != nil || status != aggregator.QuotaStatusWarning {
		t.Errorf("expected WARNING at 82%%, got %s (err: %v)", status, err)
	}

	// Critical check at 48 TB (96%)
	status, err = enforcer.CheckQuota(tenantID, 48*BytesInTerabyte)
	if err != nil || status != aggregator.QuotaStatusCritical {
		t.Errorf("expected CRITICAL at 96%%, got %s (err: %v)", status, err)
	}

	// Exceeded check at 51 TB (102%)
	status, err = enforcer.CheckQuota(tenantID, 51*BytesInTerabyte)
	if err == nil || status != aggregator.QuotaStatusExceeded {
		t.Errorf("expected EXCEEDED with error at 102%%, got status %s", status)
	}
}

func TestUsageTracker(t *testing.T) {
	tracker := aggregator.NewUsageTracker()
	tenantID := uuid.New()
	datasetID := "financial-lob-tick-v4"

	now := time.Now().UTC()
	events := []*kafka.DatasetConsumptionEvent{
		{
			EventID:             uuid.New(),
			TenantID:            tenantID,
			DatasetID:           datasetID,
			BytesTransferred:    1024 * 1024 * 500, // 500 MB
			QueryCount:          100,
			ComputeMilliseconds: 250,
			Timestamp:           now,
		},
		{
			EventID:             uuid.New(),
			TenantID:            tenantID,
			DatasetID:           datasetID,
			BytesTransferred:    1024 * 1024 * 500, // 500 MB
			QueryCount:          150,
			ComputeMilliseconds: 350,
			Timestamp:           now,
		},
	}

	tracker.IngestBatch(events)
	usage := tracker.GetUsage(tenantID, datasetID)

	expectedBytes := int64(1024 * 1024 * 1000)
	if usage.TotalBytes != expectedBytes {
		t.Errorf("total bytes mismatch. Got %d, want %d", usage.TotalBytes, expectedBytes)
	}
	if usage.TotalQueries != 250 {
		t.Errorf("total queries mismatch. Got %d, want 250", usage.TotalQueries)
	}
	if usage.TotalComputeMs != 600 {
		t.Errorf("total compute ms mismatch. Got %d, want 600", usage.TotalComputeMs)
	}
}
