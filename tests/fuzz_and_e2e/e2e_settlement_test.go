package fuzz_and_e2e

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"testing"
	"time"

	commoncrypto "datavendor-payment-core/libs/common-crypto"
	"datavendor-payment-core/services/b2b-metered-billing/aggregator"
	"datavendor-payment-core/services/b2b-metered-billing/billing"
	"datavendor-payment-core/services/b2b-metered-billing/kafka"
	"datavendor-payment-core/services/core-ledger-engine/repository"
	"datavendor-payment-core/services/settlement-gateway/processor"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// TestE2E_FullSettlementLifecycle tests the full lifecycle from telemetry consumption event
// to tiered billing calculation, automated invoice generation, GAAP double-entry posting,
// and SWIFT ISO 20022 XML financial message generation.
func TestE2E_FullSettlementLifecycle(t *testing.T) {
	ctx := context.Background()

	// STEP 1: Secure customer PII & credentials via Field-level AES-256-GCM
	masterKey := make([]byte, commoncrypto.AES256KeySize)
	rand.Read(masterKey)
	cipher, err := commoncrypto.NewFieldCipher(masterKey)
	if err != nil {
		t.Fatalf("failed to initialize cipher: %v", err)
	}
	defer cipher.Close()

	sensitiveBankAcct := []byte("DE89370400440532013000")
	encryptedAcct, err := cipher.EncryptField(sensitiveBankAcct, []byte("tenant-alpha"))
	if err != nil {
		t.Fatalf("failed encrypting account: %v", err)
	}

	// STEP 2: Ingest high-volume telemetry consumption events (e.g. 25 Terabytes)
	tenantID := uuid.New()
	datasetID := "financial-timeseries-tick-level-2026"
	twentyFiveTB := int64(25) * billing.BytesInTerabyte

	tracker := aggregator.NewUsageTracker()
	tracker.IngestBatch([]*kafka.DatasetConsumptionEvent{
		{
			EventID:             uuid.New(),
			TenantID:            tenantID,
			DatasetID:           datasetID,
			BytesTransferred:    twentyFiveTB,
			QueryCount:          200000,
			ComputeMilliseconds: 4000000,
			Timestamp:           time.Now().UTC(),
		},
	})

	usage := tracker.GetUsage(tenantID, datasetID)
	if usage.TotalBytes != twentyFiveTB {
		t.Fatalf("usage tracking failed. Got %d bytes", usage.TotalBytes)
	}

	// STEP 3: Compute Tiered B2B Pricing and Generate Invoice
	pricingCalc := billing.NewPricingCalculator()
	invoiceGen := billing.NewInvoiceGenerator(pricingCalc)
	taxRate := decimal.NewFromFloat(0.0825) // 8.25% Texas State & Local Sales Tax

	invoice := invoiceGen.GenerateMonthlyInvoice(
		tenantID,
		"2026-10",
		usage.TotalBytes,
		usage.TotalQueries,
		usage.TotalComputeMs,
		taxRate,
	)

	if invoice.Status != billing.InvoiceStatusIssued {
		t.Fatalf("expected invoice status ISSUED, got %s", invoice.Status)
	}

	// STEP 4: Post GAAP Double-Entry Journal Entry in Core Ledger Engine
	ledgerRepo := repository.NewInMemLedgerRepository()
	ledgerID := uuid.New()
	_ = ledgerRepo.CreateLedger(ctx, &repository.Ledger{ID: ledgerID, Name: "DataVendor Main Ledger", BaseCurrency: "USD"})

	arAccount := &repository.Account{
		ID:            uuid.New(),
		LedgerID:      ledgerID,
		Code:          "1200",
		Name:          "Accounts Receivable",
		AccountType:   repository.AccountTypeAsset,
		NormalBalance: repository.DirectionDebit,
		Currency:      "USD",
	}
	revAccount := &repository.Account{
		ID:            uuid.New(),
		LedgerID:      ledgerID,
		Code:          "4010",
		Name:          "Dataset Sales Revenue",
		AccountType:   repository.AccountTypeRevenue,
		NormalBalance: repository.DirectionCredit,
		Currency:      "USD",
	}
	taxAccount := &repository.Account{
		ID:            uuid.New(),
		LedgerID:      ledgerID,
		Code:          "2100",
		Name:          "Sales Tax Payable",
		AccountType:   repository.AccountTypeLiability,
		NormalBalance: repository.DirectionCredit,
		Currency:      "USD",
	}

	_ = ledgerRepo.CreateAccount(ctx, arAccount)
	_ = ledgerRepo.CreateAccount(ctx, revAccount)
	_ = ledgerRepo.CreateAccount(ctx, taxAccount)

	journalReq := invoice.BuildLedgerEntryRequest(ledgerID, arAccount.ID, revAccount.ID, taxAccount.ID)
	journalEntry, err := ledgerRepo.PostJournalEntry(ctx, journalReq)
	if err != nil {
		t.Fatalf("failed posting invoice to ledger: %v", err)
	}

	if journalEntry.State != repository.StatePosted {
		t.Fatalf("journal entry state must be POSTED, got %s", journalEntry.State)
	}

	// Verify ledger balance equals invoice total due
	arAcc, _ := ledgerRepo.GetAccount(ctx, arAccount.ID)
	if !arAcc.CurrentBalance.Equal(invoice.TotalAmountDue) {
		t.Fatalf("AR balance %s != invoice total %s", arAcc.CurrentBalance, invoice.TotalAmountDue)
	}

	// STEP 5: Execute Banking Settlement via Settlement Pipeline (SWIFT ISO 20022 pacs.008)
	decryptedBankAcct, err := cipher.DecryptField(encryptedAcct, []byte("tenant-alpha"))
	if err != nil {
		t.Fatalf("failed decrypting bank account: %v", err)
	}

	pipeline := processor.NewSettlementPipeline()
	settlementInst := processor.SettlementInstruction{
		ID:              uuid.New(),
		Amount:          invoice.TotalAmountDue,
		Currency:        invoice.Currency,
		Urgency:         "STANDARD",
		DebtorName:      "Enterprise Tenant Alpha Corp",
		DebtorAccount:   string(decryptedBankAcct),
		DebtorRouting:   "DBEUMM21XXX", // Deutsche Bank BIC
		CreditorName:    "DataVendor Settlement Inc",
		CreditorAccount: "US99CHAS000192837465",
		CreditorRouting: "CHASUS33XXX", // JP Morgan Chase BIC
		Memo:            fmt.Sprintf("Payment for %s", invoice.InvoiceNumber),
	}

	result, err := pipeline.ExecuteClearing(ctx, settlementInst)
	if err != nil {
		t.Fatalf("settlement clearing failed: %v", err)
	}

	if result.SelectedRail != processor.RailSWIFT {
		t.Fatalf("expected rail SWIFT_ISO20022, got %s", result.SelectedRail)
	}

	xmlOutput := string(result.MessageBytes)
	if !strings.Contains(xmlOutput, "FIToFICstmrCdtTrf") {
		t.Errorf("missing pacs.008 credit transfer root in generated clearing message")
	}
	if !strings.Contains(xmlOutput, invoice.TotalAmountDue.StringFixed(2)) {
		t.Errorf("missing exact settlement amount in XML message")
	}
}
