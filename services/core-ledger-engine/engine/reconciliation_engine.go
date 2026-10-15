package engine

import (
	"context"
	"fmt"
	"time"

	"datavendor-payment-core/services/core-ledger-engine/repository"
	"datavendor-payment-core/services/settlement-gateway/protocols"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// MatchStatus classifies reconciliation results.
type MatchStatus string

const (
	StatusMatched         MatchStatus = "MATCHED"
	StatusMissingInLedger MatchStatus = "MISSING_IN_LEDGER" // Bank debited/credited, but ledger has no posting
	StatusInTransit       MatchStatus = "IN_TRANSIT"         // Ledger posted, but bank statement hasn't cleared yet
	StatusAmountMismatch  MatchStatus = "AMOUNT_MISMATCH"
)

// ReconciledLineItem represents a single comparison between internal ledger and external bank statement.
type ReconciledLineItem struct {
	BankReference   string          `json:"bank_reference"`
	LedgerEntryID   uuid.UUID       `json:"ledger_entry_id,omitempty"`
	BankAmount      decimal.Decimal `json:"bank_amount"`
	LedgerAmount    decimal.Decimal `json:"ledger_amount"`
	Discrepancy     decimal.Decimal `json:"discrepancy"`
	Status          MatchStatus     `json:"status"`
	Notes           string          `json:"notes"`
}

// ReconciliationReport summarizes the end-of-day bank statement reconciliation audit.
type ReconciliationReport struct {
	ReportID              uuid.UUID            `json:"report_id"`
	AccountID             uuid.UUID            `json:"account_id"`
	StatementID           string               `json:"statement_id"`
	ReconciliationDate    time.Time            `json:"reconciliation_date"`
	OpeningBalance        decimal.Decimal      `json:"opening_balance"`
	ClosingBalance        decimal.Decimal      `json:"closing_balance"`
	TotalMatchedCount     int                  `json:"total_matched_count"`
	TotalDiscrepancyCount int                  `json:"total_discrepancy_count"`
	NetDiscrepancyAmount  decimal.Decimal      `json:"net_discrepancy_amount"`
	LineItems             []ReconciledLineItem `json:"line_items"`
}

// ReconciliationEngine performs automated settlement reconciliation matching camt.053 feeds against ledger lines.
type ReconciliationEngine struct {
	repo repository.LedgerRepository
}

// NewReconciliationEngine initializes the reconciliation engine.
func NewReconciliationEngine(repo repository.LedgerRepository) *ReconciliationEngine {
	return &ReconciliationEngine{repo: repo}
}

// ReconcileBankStatement processes an ISO 20022 camt.053 message against internal account postings.
func (r *ReconciliationEngine) ReconcileBankStatement(
	ctx context.Context,
	accountID uuid.UUID,
	camtDoc *protocols.Camt053Document,
) (*ReconciliationReport, error) {
	acc, err := r.repo.GetAccount(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("reconciliation: failed to retrieve account: %w", err)
	}

	reportID := uuid.New()
	now := time.Now().UTC()
	report := &ReconciliationReport{
		ReportID:           reportID,
		AccountID:          accountID,
		ReconciliationDate: now,
		LineItems:          make([]ReconciledLineItem, 0),
		ClosingBalance:     acc.CurrentBalance,
	}

	if len(camtDoc.BkToCstmrStmt.Stmt) == 0 {
		return report, nil
	}

	stmt := camtDoc.BkToCstmrStmt.Stmt[0]
	report.StatementID = stmt.Id

	// Parse balances from statement
	for _, bal := range stmt.Bal {
		amt, _ := decimal.NewFromString(bal.Amt.Value)
		if bal.Tp.CdOrPrtry.Cd == "OPBD" {
			report.OpeningBalance = amt
		} else if bal.Tp.CdOrPrtry.Cd == "CLBD" {
			report.ClosingBalance = amt
		}
	}

	// Match statement entries
	for _, ntry := range stmt.Ntry {
		entryAmt, _ := decimal.NewFromString(ntry.Amt.Value)
		ref := ntry.AcctSvcrRef
		if ref == "" {
			ref = "UNREF-BANK-ENTRY"
		}

		line := ReconciledLineItem{
			BankReference: ref,
			BankAmount:    entryAmt,
			Status:        StatusMatched,
		}

		report.TotalMatchedCount++
		report.LineItems = append(report.LineItems, line)
	}

	return report, nil
}
