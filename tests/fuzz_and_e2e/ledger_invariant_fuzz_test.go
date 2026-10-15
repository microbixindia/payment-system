package fuzz_and_e2e

import (
	"math/rand"
	"testing"
	"time"

	"datavendor-payment-core/services/core-ledger-engine/repository"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// TestFuzz_DoubleEntryInvariants executes 10,000 randomized property tests verifying that
// any transaction violating SUM(debits) == SUM(credits) is strictly rejected, and all balanced
// transactions are strictly accepted.
func TestFuzz_DoubleEntryInvariants(t *testing.T) {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	ledgerID := uuid.New()

	for iteration := 0; iteration < 10000; iteration++ {
		// Generate 2 to 10 posting lines
		numLegs := 2 + r.Intn(9)
		if numLegs%2 != 0 {
			numLegs++ // ensure even legs for pairing
		}

		totalAmount := decimal.Zero
		debitPostings := make([]repository.PostingRequest, 0, numLegs/2)
		creditPostings := make([]repository.PostingRequest, 0, numLegs/2)

		for i := 0; i < numLegs/2; i++ {
			// Random amount between $1.0000 and $50,000.0000
			cents := int64(100 + r.Intn(5000000))
			amt := decimal.New(cents, -2).Round(4)
			totalAmount = totalAmount.Add(amt)

			debitPostings = append(debitPostings, repository.PostingRequest{
				AccountID: uuid.New(),
				Direction: repository.DirectionDebit,
				Amount:    amt,
			})
			creditPostings = append(creditPostings, repository.PostingRequest{
				AccountID: uuid.New(),
				Direction: repository.DirectionCredit,
				Amount:    amt,
			})
		}

		allPostings := append(debitPostings, creditPostings...)

		// 1. Verify that balanced entry passes invariant validation
		balancedReq := repository.CreateJournalEntryRequest{
			LedgerID:       ledgerID,
			IdempotencyKey: uuid.New().String(),
			Currency:       "USD",
			Postings:       allPostings,
		}

		if err := balancedReq.ValidateInvariants(); err != nil {
			t.Fatalf("[Iteration %d] Balanced entry failed validation: %v", iteration, err)
		}

		// 2. Perturb one line by exactly $0.0001 (fractional drift) and verify invariant rejection
		drift := decimal.NewFromFloat(0.0001)
		unbalancedPostings := make([]repository.PostingRequest, len(allPostings))
		copy(unbalancedPostings, allPostings)
		unbalancedPostings[0].Amount = unbalancedPostings[0].Amount.Add(drift)

		unbalancedReq := repository.CreateJournalEntryRequest{
			LedgerID:       ledgerID,
			IdempotencyKey: uuid.New().String(),
			Currency:       "USD",
			Postings:       unbalancedPostings,
		}

		if err := unbalancedReq.ValidateInvariants(); err == nil {
			t.Fatalf("[Iteration %d] Fuzzer invariant failure: Unbalanced transaction with $0.0001 drift was accepted!", iteration)
		}
	}
}
