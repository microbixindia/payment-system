package billing

import (
	"fmt"
	"time"

	"datavendor-payment-core/services/core-ledger-engine/repository"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// InvoiceStatus tracks billing invoice state.
type InvoiceStatus string

const (
	InvoiceStatusDraft   InvoiceStatus = "DRAFT"
	InvoiceStatusIssued  InvoiceStatus = "ISSUED"
	InvoiceStatusPaid    InvoiceStatus = "PAID"
	InvoiceStatusOverdue InvoiceStatus = "OVERDUE"
)

// InvoiceLineItem represents an individual charge line on the invoice.
type InvoiceLineItem struct {
	ID          uuid.UUID       `json:"id"`
	Description string          `json:"description"`
	Quantity    decimal.Decimal `json:"quantity"`
	Unit        string          `json:"unit"`
	UnitPrice   decimal.Decimal `json:"unit_price"`
	Amount      decimal.Decimal `json:"amount"`
}

// B2BInvoice represents an enterprise bill generated for dataset consumption.
type B2BInvoice struct {
	ID             uuid.UUID         `json:"id"`
	InvoiceNumber  string            `json:"invoice_number"`
	TenantID       uuid.UUID         `json:"tenant_id"`
	BillingPeriod  string            `json:"billing_period"`
	IssueDate      time.Time         `json:"issue_date"`
	DueDate        time.Time         `json:"due_date"`
	Currency       string            `json:"currency"`
	LineItems      []InvoiceLineItem `json:"line_items"`
	Subtotal       decimal.Decimal   `json:"subtotal"`
	TaxRate        decimal.Decimal   `json:"tax_rate"` // e.g. 0.0825 (8.25%)
	TaxAmount      decimal.Decimal   `json:"tax_amount"`
	TotalAmountDue decimal.Decimal   `json:"total_amount_due"`
	Status         InvoiceStatus     `json:"status"`
}

// InvoiceGenerator orchestrates conversion of usage metrics into legal enterprise tax invoices.
type InvoiceGenerator struct {
	calculator *PricingCalculator
}

// NewInvoiceGenerator constructs the invoice generator.
func NewInvoiceGenerator(calc *PricingCalculator) *InvoiceGenerator {
	return &InvoiceGenerator{calculator: calc}
}

// GenerateMonthlyInvoice builds an invoice from measured telemetry metrics.
func (ig *InvoiceGenerator) GenerateMonthlyInvoice(
	tenantID uuid.UUID,
	period string,
	bytesTransferred, queries, computeMs int64,
	taxRate decimal.Decimal,
) *B2BInvoice {
	pricing := ig.calculator.CalculateCharges(bytesTransferred, queries, computeMs)

	now := time.Now().UTC()
	dueDate := now.AddDate(0, 0, 30) // Net 30 terms
	invoiceID := uuid.New()
	invoiceNum := fmt.Sprintf("INV-%s-%s", now.Format("200601"), invoiceID.String()[:8])

	lineItems := make([]InvoiceLineItem, 0)

	// Add tier breakdown items
	for _, tier := range pricing.TierBreakdowns {
		lineItems = append(lineItems, InvoiceLineItem{
			ID:          uuid.New(),
			Description: fmt.Sprintf("Data Volume: %s", tier.TierDescription),
			Quantity:    tier.GigabytesBilled,
			Unit:        "GB",
			UnitPrice:   tier.RatePerGB,
			Amount:      tier.AmountUSD,
		})
	}

	// Add API query fees
	if pricing.QueryCost.GreaterThan(decimal.Zero) {
		lineItems = append(lineItems, InvoiceLineItem{
			ID:          uuid.New(),
			Description: "API Query Telemetry Requests",
			Quantity:    decimal.NewFromInt(queries),
			Unit:        "Queries",
			UnitPrice:   ig.calculator.queryFee,
			Amount:      pricing.QueryCost,
		})
	}

	// Add compute fees
	if pricing.ComputeCost.GreaterThan(decimal.Zero) {
		lineItems = append(lineItems, InvoiceLineItem{
			ID:          uuid.New(),
			Description: "Dedicated Dataset Pipeline Compute Duration",
			Quantity:    decimal.NewFromInt(computeMs),
			Unit:        "Milliseconds",
			UnitPrice:   ig.calculator.computeFeePerMs,
			Amount:      pricing.ComputeCost,
		})
	}

	taxAmount := pricing.SubtotalAmount.Mul(taxRate).Round(4)
	totalDue := pricing.SubtotalAmount.Add(taxAmount).Round(4)

	return &B2BInvoice{
		ID:             invoiceID,
		InvoiceNumber:  invoiceNum,
		TenantID:       tenantID,
		BillingPeriod:  period,
		IssueDate:      now,
		DueDate:        dueDate,
		Currency:       "USD",
		LineItems:      lineItems,
		Subtotal:       pricing.SubtotalAmount,
		TaxRate:        taxRate,
		TaxAmount:      taxAmount,
		TotalAmountDue: totalDue,
		Status:         InvoiceStatusIssued,
	}
}

// BuildLedgerEntryRequest creates the double-entry journal request to record Accounts Receivable and Revenue.
func (inv *B2BInvoice) BuildLedgerEntryRequest(
	ledgerID uuid.UUID,
	accountsReceivableAccount uuid.UUID,
	revenueAccount uuid.UUID,
	taxPayableAccount uuid.UUID,
) repository.CreateJournalEntryRequest {
	postings := []repository.PostingRequest{
		// 1. Debit Accounts Receivable (Asset increases by Total Due)
		{
			AccountID:   accountsReceivableAccount,
			Direction:   repository.DirectionDebit,
			Amount:      inv.TotalAmountDue,
			Description: fmt.Sprintf("Accounts Receivable for invoice %s", inv.InvoiceNumber),
		},
		// 2. Credit Dataset Revenue (Revenue increases by Subtotal)
		{
			AccountID:   revenueAccount,
			Direction:   repository.DirectionCredit,
			Amount:      inv.Subtotal,
			Description: fmt.Sprintf("Dataset sales revenue for invoice %s", inv.InvoiceNumber),
		},
	}

	// 3. Credit Tax Payable (Liability increases by Tax Amount)
	if inv.TaxAmount.GreaterThan(decimal.Zero) {
		postings = append(postings, repository.PostingRequest{
			AccountID:   taxPayableAccount,
			Direction:   repository.DirectionCredit,
			Amount:      inv.TaxAmount,
			Description: fmt.Sprintf("Sales tax liability for invoice %s", inv.InvoiceNumber),
		})
	}

	return repository.CreateJournalEntryRequest{
		LedgerID:       ledgerID,
		IdempotencyKey: fmt.Sprintf("inv-ledger-%s", inv.ID.String()),
		CorrelationID:  inv.InvoiceNumber,
		ExternalRef:    inv.InvoiceNumber,
		Description:    fmt.Sprintf("Revenue recognition for invoice %s", inv.InvoiceNumber),
		Currency:       inv.Currency,
		Postings:       postings,
	}
}
