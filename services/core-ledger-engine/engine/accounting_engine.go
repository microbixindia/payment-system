package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"datavendor-payment-core/services/core-ledger-engine/repository"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

var (
	ErrZeroBalance = errors.New("accounting: zero net balance discrepancy")
)

// AccountingEngine manages double-entry financial business logic, reconciliation,
// and transactional lifecycle management.
type AccountingEngine struct {
	repo repository.LedgerRepository
}

// NewAccountingEngine constructs an AccountingEngine backed by a repository.
func NewAccountingEngine(repo repository.LedgerRepository) *AccountingEngine {
	return &AccountingEngine{
		repo: repo,
	}
}

// TransferRequest parameters for moving funds between accounts.
type TransferRequest struct {
	LedgerID       uuid.UUID
	SourceAccount  uuid.UUID
	DestAccount    uuid.UUID
	Amount         decimal.Decimal
	Currency       string
	IdempotencyKey string
	CorrelationID  string
	Description    string
}

// ExecuteTransfer processes an atomic two-legged double-entry transfer.
func (e *AccountingEngine) ExecuteTransfer(ctx context.Context, req TransferRequest) (*repository.JournalEntry, error) {
	if req.Amount.LessThanOrEqual(decimal.Zero) {
		return nil, repository.ErrNegativePostingAmount
	}

	srcAccount, err := e.repo.GetAccount(ctx, req.SourceAccount)
	if err != nil {
		return nil, fmt.Errorf("source account error: %w", err)
	}

	destAccount, err := e.repo.GetAccount(ctx, req.DestAccount)
	if err != nil {
		return nil, fmt.Errorf("destination account error: %w", err)
	}

	// Determine directions based on account normal balances:
	// For standard asset-to-asset transfer (e.g. Bank Account to Clearing Account):
	// Source is Credited (reduced), Destination is Debited (increased).
	srcDirection := repository.DirectionCredit
	destDirection := repository.DirectionDebit

	// If source is a Liability/Equity/Revenue account, a decrease is a DEBIT
	if srcAccount.NormalBalance == repository.DirectionCredit {
		srcDirection = repository.DirectionDebit
	}
	// If dest is a Liability/Equity/Revenue account, an increase is a CREDIT
	if destAccount.NormalBalance == repository.DirectionCredit {
		destDirection = repository.DirectionCredit
	}

	postings := []repository.PostingRequest{
		{
			AccountID:   req.SourceAccount,
			Direction:   srcDirection,
			Amount:      req.Amount,
			Description: fmt.Sprintf("Transfer outflow: %s", req.Description),
		},
		{
			AccountID:   req.DestAccount,
			Direction:   destDirection,
			Amount:      req.Amount,
			Description: fmt.Sprintf("Transfer inflow: %s", req.Description),
		},
	}

	entryReq := repository.CreateJournalEntryRequest{
		LedgerID:       req.LedgerID,
		IdempotencyKey: req.IdempotencyKey,
		CorrelationID:  req.CorrelationID,
		ExternalRef:    fmt.Sprintf("tx-%d", time.Now().UnixNano()),
		Description:    req.Description,
		Currency:       req.Currency,
		Postings:       postings,
	}

	return e.repo.PostJournalEntry(ctx, entryReq)
}

// VerifyAccountBalance returns current verified balance of an account.
func (e *AccountingEngine) VerifyAccountBalance(ctx context.Context, accountID uuid.UUID) (decimal.Decimal, error) {
	acc, err := e.repo.GetAccount(ctx, accountID)
	if err != nil {
		return decimal.Zero, err
	}
	return acc.CurrentBalance, nil
}
