package fuzz_and_e2e

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"datavendor-payment-core/services/core-ledger-engine/repository"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// TestConcurrency_ZeroDeadlockStress tests 100 concurrent workers posting transactions
// in opposite directions between pairs of accounts to guarantee Dijkstra lock ordering
// prevents concurrency deadlocks.
func TestConcurrency_ZeroDeadlockStress(t *testing.T) {
	ctx := context.Background()
	repo := repository.NewInMemLedgerRepository()

	ledgerID := uuid.New()
	_ = repo.CreateLedger(ctx, &repository.Ledger{ID: ledgerID, Name: "Stress Ledger", BaseCurrency: "USD"})

	// Create 4 accounts
	accounts := make([]*repository.Account, 4)
	for i := 0; i < 4; i++ {
		acc := &repository.Account{
			ID:            uuid.New(),
			LedgerID:      ledgerID,
			Code:          fmt.Sprintf("ACC-%d", i),
			Name:          fmt.Sprintf("Account %d", i),
			AccountType:   repository.AccountTypeAsset,
			NormalBalance: repository.DirectionDebit,
			Currency:      "USD",
		}
		_ = repo.CreateAccount(ctx, acc)
		accounts[i] = acc
	}

	const totalWorkers = 100
	var wg sync.WaitGroup
	wg.Add(totalWorkers)

	transferAmount, _ := decimal.NewFromString("10.0000")

	for i := 0; i < totalWorkers; i++ {
		workerID := i
		go func() {
			defer wg.Done()

			// Alternating pairs: Worker 0 transfers 0 -> 1; Worker 1 transfers 1 -> 0
			// Without deterministic account sorting, this circular wait creates immediate deadlocks!
			var src, dest *repository.Account
			if workerID%2 == 0 {
				src = accounts[0]
				dest = accounts[1]
			} else {
				src = accounts[1]
				dest = accounts[0]
			}

			req := repository.CreateJournalEntryRequest{
				LedgerID:       ledgerID,
				IdempotencyKey: fmt.Sprintf("worker-tx-%d", workerID),
				CorrelationID:  fmt.Sprintf("corr-%d", workerID),
				Currency:       "USD",
				Postings: []repository.PostingRequest{
					{AccountID: src.ID, Direction: repository.DirectionCredit, Amount: transferAmount},
					{AccountID: dest.ID, Direction: repository.DirectionDebit, Amount: transferAmount},
				},
			}

			_, err := repo.PostJournalEntry(ctx, req)
			if err != nil {
				t.Errorf("worker %d failed to post: %v", workerID, err)
			}
		}()
	}

	wg.Wait()

	// Verify that sum of balances across accounts 0 and 1 remains exactly zero (net zero sum)
	acc0, _ := repo.GetAccount(ctx, accounts[0].ID)
	acc1, _ := repo.GetAccount(ctx, accounts[1].ID)

	netSum := acc0.CurrentBalance.Add(acc1.CurrentBalance)
	if !netSum.Equal(decimal.Zero) {
		t.Fatalf("concurrency race condition: net sum of accounts is %s (expected 0.0000)", netSum)
	}
}
