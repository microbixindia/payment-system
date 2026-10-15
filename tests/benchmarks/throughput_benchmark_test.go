package benchmarks

import (
	"context"
	"crypto/rand"
	"fmt"
	"sync/atomic"
	"testing"

	commoncrypto "datavendor-payment-core/libs/common-crypto"
	"datavendor-payment-core/services/core-ledger-engine/engine"
	"datavendor-payment-core/services/core-ledger-engine/repository"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// BenchmarkAES256GCM_Throughput measures high-velocity field-level AES-256-GCM
// encryption throughput in Megabytes Per Second (MB/s) across realistic payment payload sizes.
func BenchmarkAES256GCM_Throughput(b *testing.B) {
	masterKey := make([]byte, commoncrypto.AES256KeySize)
	if _, err := rand.Read(masterKey); err != nil {
		b.Fatalf("failed entropy read: %v", err)
	}

	cipher, err := commoncrypto.NewFieldCipher(masterKey)
	if err != nil {
		b.Fatalf("failed to create field cipher: %v", err)
	}
	defer cipher.Close()

	aad := []byte("tenant_alpha_dataset_billing_context")

	payloadSizes := []struct {
		name string
		size int
	}{
		{name: "64B_PAN_Token", size: 64},
		{name: "256B_Customer_PII", size: 256},
		{name: "1KB_Financial_Msg", size: 1024},
		{name: "64KB_Settlement_Chunk", size: 64 * 1024},
		{name: "1MB_Telemetry_Dataset", size: 1024 * 1024},
	}

	for _, tc := range payloadSizes {
		b.Run(tc.name, func(b *testing.B) {
			data := make([]byte, tc.size)
			if _, err := rand.Read(data); err != nil {
				b.Fatalf("failed buffer fill: %v", err)
			}

			// b.SetBytes enables Go to output standard throughput in MB/s
			b.SetBytes(int64(tc.size))
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				payload, err := cipher.EncryptField(data, aad)
				if err != nil {
					b.Fatalf("encryption failed: %v", err)
				}
				if len(payload.Ciphertext) == 0 {
					b.Fatalf("empty ciphertext returned")
				}
			}
		})
	}
}

// BenchmarkAES256GCM_ParallelThroughput evaluates multi-threaded AES-256-GCM encryption throughput
// utilizing all available CPU cores via b.RunParallel to simulate high-concurrency ingestion.
func BenchmarkAES256GCM_ParallelThroughput(b *testing.B) {
	masterKey := make([]byte, commoncrypto.AES256KeySize)
	_, _ = rand.Read(masterKey)

	cipher, err := commoncrypto.NewFieldCipher(masterKey)
	if err != nil {
		b.Fatalf("failed to create field cipher: %v", err)
	}
	defer cipher.Close()

	payloadSize := 1024 // 1 KB
	data := make([]byte, payloadSize)
	_, _ = rand.Read(data)
	aad := []byte("high_velocity_parallel_feed")

	b.SetBytes(int64(payloadSize))
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, err := cipher.EncryptField(data, aad)
			if err != nil {
				b.Fatalf("parallel encryption failed: %v", err)
			}
		}
	})
}

// BenchmarkAES256GCM_DecryptThroughput measures decryption and authentication tag
// verification throughput in MB/s across payload sizes.
func BenchmarkAES256GCM_DecryptThroughput(b *testing.B) {
	masterKey := make([]byte, commoncrypto.AES256KeySize)
	_, _ = rand.Read(masterKey)

	cipher, err := commoncrypto.NewFieldCipher(masterKey)
	if err != nil {
		b.Fatalf("cipher init error: %v", err)
	}
	defer cipher.Close()

	aad := []byte("tenant_audit_tag")
	payloadSizes := []struct {
		name string
		size int
	}{
		{name: "64B", size: 64},
		{name: "1KB", size: 1024},
		{name: "64KB", size: 64 * 1024},
	}

	for _, tc := range payloadSizes {
		b.Run(tc.name, func(b *testing.B) {
			plaintext := make([]byte, tc.size)
			_, _ = rand.Read(plaintext)

			encrypted, err := cipher.EncryptField(plaintext, aad)
			if err != nil {
				b.Fatalf("prep encrypt failed: %v", err)
			}

			b.SetBytes(int64(tc.size))
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				decrypted, err := cipher.DecryptField(encrypted, aad)
				if err != nil {
					b.Fatalf("decryption failed: %v", err)
				}
				if len(decrypted) != tc.size {
					b.Fatalf("size mismatch")
				}
			}
		})
	}
}

// BenchmarkLedger_BatchPostings benchmarks batch ledger posting performance and measures
// Transactions Per Second (TPS) across various batch processing dimensions.
func BenchmarkLedger_BatchPostings(b *testing.B) {
	ctx := context.Background()

	batchSizes := []int{1, 10, 50, 100, 500}

	for _, batchSize := range batchSizes {
		b.Run(fmt.Sprintf("Batch_%d", batchSize), func(b *testing.B) {
			repo := repository.NewInMemLedgerRepository()
			ledgerID := uuid.New()
			_ = repo.CreateLedger(ctx, &repository.Ledger{ID: ledgerID, Name: "Batch Benchmark Ledger", BaseCurrency: "USD"})

			cashAcc := &repository.Account{
				ID:            uuid.New(),
				LedgerID:      ledgerID,
				Code:          "1000",
				Name:          "Cash Account",
				AccountType:   repository.AccountTypeAsset,
				NormalBalance: repository.DirectionDebit,
				Currency:      "USD",
			}
			revAcc := &repository.Account{
				ID:            uuid.New(),
				LedgerID:      ledgerID,
				Code:          "4000",
				Name:          "Revenue Account",
				AccountType:   repository.AccountTypeRevenue,
				NormalBalance: repository.DirectionCredit,
				Currency:      "USD",
			}
			_ = repo.CreateAccount(ctx, cashAcc)
			_ = repo.CreateAccount(ctx, revAcc)

			amount := decimal.NewFromFloat(42.50)

			// Pre-allocate requests for the batch to isolate posting throughput from generation overhead
			requests := make([]repository.CreateJournalEntryRequest, batchSize)
			for j := 0; j < batchSize; j++ {
				requests[j] = repository.CreateJournalEntryRequest{
					LedgerID:       ledgerID,
					IdempotencyKey: fmt.Sprintf("bench-prep-%d", j),
					CorrelationID:  fmt.Sprintf("corr-%d", j),
					Currency:       "USD",
					Postings: []repository.PostingRequest{
						{AccountID: cashAcc.ID, Direction: repository.DirectionDebit, Amount: amount},
						{AccountID: revAcc.ID, Direction: repository.DirectionCredit, Amount: amount},
					},
				}
			}

			var txCounter int64

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				for j := 0; j < batchSize; j++ {
					req := requests[j]
					// Dynamic idempotency key per iteration to avoid duplicate cache hits during fresh posting benchmark
					cnt := atomic.AddInt64(&txCounter, 1)
					req.IdempotencyKey = fmt.Sprintf("tx-run-%d", cnt)

					if _, err := repo.PostJournalEntry(ctx, req); err != nil {
						b.Fatalf("failed to post journal entry: %v", err)
					}
				}
			}
			b.StopTimer()

			totalTx := float64(b.N * batchSize)
			tps := totalTx / b.Elapsed().Seconds()
			b.ReportMetric(tps, "tx/s")
			b.ReportMetric(totalTx*2, "postings/s") // Each double-entry tx contains 2 postings (debit + credit)
		})
	}
}

// BenchmarkLedger_ParallelPostings measures peak ledger transactions per second (TPS)
// under concurrent multi-goroutine load across independent and contended accounts.
func BenchmarkLedger_ParallelPostings(b *testing.B) {
	ctx := context.Background()

	b.Run("IndependentAccounts_PeakScaling", func(b *testing.B) {
		repo := repository.NewInMemLedgerRepository()
		ledgerID := uuid.New()
		_ = repo.CreateLedger(ctx, &repository.Ledger{ID: ledgerID, Name: "Parallel Ledger", BaseCurrency: "USD"})

		// Create 20 distinct account pairs to evaluate non-contended parallel throughput
		numPairs := 20
		type accPair struct {
			src *repository.Account
			dst *repository.Account
		}
		pairs := make([]accPair, numPairs)

		for i := 0; i < numPairs; i++ {
			src := &repository.Account{
				ID:            uuid.New(),
				LedgerID:      ledgerID,
				Code:          fmt.Sprintf("SRC-%d", i),
				AccountType:   repository.AccountTypeAsset,
				NormalBalance: repository.DirectionDebit,
				Currency:      "USD",
			}
			dst := &repository.Account{
				ID:            uuid.New(),
				LedgerID:      ledgerID,
				Code:          fmt.Sprintf("DST-%d", i),
				AccountType:   repository.AccountTypeRevenue,
				NormalBalance: repository.DirectionCredit,
				Currency:      "USD",
			}
			_ = repo.CreateAccount(ctx, src)
			_ = repo.CreateAccount(ctx, dst)
			pairs[i] = accPair{src: src, dst: dst}
		}

		var globalCounter int64
		amt := decimal.NewFromFloat(15.75)

		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			localIdx := 0
			for pb.Next() {
				pair := pairs[localIdx%numPairs]
				localIdx++
				id := atomic.AddInt64(&globalCounter, 1)

				req := repository.CreateJournalEntryRequest{
					LedgerID:       ledgerID,
					IdempotencyKey: fmt.Sprintf("par-tx-%d", id),
					CorrelationID:  fmt.Sprintf("corr-%d", id),
					Currency:       "USD",
					Postings: []repository.PostingRequest{
						{AccountID: pair.src.ID, Direction: repository.DirectionDebit, Amount: amt},
						{AccountID: pair.dst.ID, Direction: repository.DirectionCredit, Amount: amt},
					},
				}
				if _, err := repo.PostJournalEntry(ctx, req); err != nil {
					b.Fatalf("parallel post error: %v", err)
				}
			}
		})
		b.StopTimer()

		tps := float64(atomic.LoadInt64(&globalCounter)) / b.Elapsed().Seconds()
		b.ReportMetric(tps, "tx/s")
	})

	b.Run("SharedHotAccount_ContentionStress", func(b *testing.B) {
		repo := repository.NewInMemLedgerRepository()
		ledgerID := uuid.New()
		_ = repo.CreateLedger(ctx, &repository.Ledger{ID: ledgerID, Name: "Hot Contention Ledger", BaseCurrency: "USD"})

		// Shared omnibus clearing account hit by all workers simultaneously
		clearingAcc := &repository.Account{
			ID:            uuid.New(),
			LedgerID:      ledgerID,
			Code:          "OMNIBUS-CLEARING",
			AccountType:   repository.AccountTypeLiability,
			NormalBalance: repository.DirectionCredit,
			Currency:      "USD",
		}
		_ = repo.CreateAccount(ctx, clearingAcc)

		// 10 distinct client accounts
		clientAccounts := make([]*repository.Account, 10)
		for i := 0; i < 10; i++ {
			acc := &repository.Account{
				ID:            uuid.New(),
				LedgerID:      ledgerID,
				Code:          fmt.Sprintf("CLIENT-%d", i),
				AccountType:   repository.AccountTypeAsset,
				NormalBalance: repository.DirectionDebit,
				Currency:      "USD",
			}
			_ = repo.CreateAccount(ctx, acc)
			clientAccounts[i] = acc
		}

		var globalCounter int64
		amt := decimal.NewFromFloat(5.00)

		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			localIdx := 0
			for pb.Next() {
				client := clientAccounts[localIdx%len(clientAccounts)]
				localIdx++
				id := atomic.AddInt64(&globalCounter, 1)

				req := repository.CreateJournalEntryRequest{
					LedgerID:       ledgerID,
					IdempotencyKey: fmt.Sprintf("hot-tx-%d", id),
					Currency:       "USD",
					Postings: []repository.PostingRequest{
						{AccountID: client.ID, Direction: repository.DirectionDebit, Amount: amt},
						{AccountID: clearingAcc.ID, Direction: repository.DirectionCredit, Amount: amt},
					},
				}
				if _, err := repo.PostJournalEntry(ctx, req); err != nil {
					b.Fatalf("contended post error: %v", err)
				}
			}
		})
		b.StopTimer()

		tps := float64(atomic.LoadInt64(&globalCounter)) / b.Elapsed().Seconds()
		b.ReportMetric(tps, "tx/s")
	})
}

// BenchmarkAccountingEngine_ExecuteTransfer benchmarks end-to-end transfer business logic
// in the core ledger accounting engine, including normal balance routing and account checks.
func BenchmarkAccountingEngine_ExecuteTransfer(b *testing.B) {
	ctx := context.Background()
	repo := repository.NewInMemLedgerRepository()
	acctEngine := engine.NewAccountingEngine(repo)

	ledgerID := uuid.New()
	_ = repo.CreateLedger(ctx, &repository.Ledger{ID: ledgerID, Name: "E2E Engine Ledger", BaseCurrency: "USD"})

	srcAcc := &repository.Account{
		ID:            uuid.New(),
		LedgerID:      ledgerID,
		Code:          "1000",
		AccountType:   repository.AccountTypeAsset,
		NormalBalance: repository.DirectionDebit,
		Currency:      "USD",
	}
	dstAcc := &repository.Account{
		ID:            uuid.New(),
		LedgerID:      ledgerID,
		Code:          "1200",
		AccountType:   repository.AccountTypeAsset,
		NormalBalance: repository.DirectionDebit,
		Currency:      "USD",
	}
	_ = repo.CreateAccount(ctx, srcAcc)
	_ = repo.CreateAccount(ctx, dstAcc)

	amt := decimal.NewFromFloat(100.00)
	var txCounter int64

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		id := atomic.AddInt64(&txCounter, 1)
		req := engine.TransferRequest{
			LedgerID:       ledgerID,
			SourceAccount:  srcAcc.ID,
			DestAccount:    dstAcc.ID,
			Amount:         amt,
			Currency:       "USD",
			IdempotencyKey: fmt.Sprintf("transfer-engine-%d", id),
			CorrelationID:  fmt.Sprintf("corr-e2e-%d", id),
			Description:    "High-speed inter-account transfer",
		}

		if _, err := acctEngine.ExecuteTransfer(ctx, req); err != nil {
			b.Fatalf("ExecuteTransfer failed: %v", err)
		}
	}
	b.StopTimer()

	tps := float64(b.N) / b.Elapsed().Seconds()
	b.ReportMetric(tps, "tx/s")
}

// BenchmarkMerkleTree_BuildAndProve measures cryptographic Merkle Tree block generation
// and audit inclusion proof verification throughput.
func BenchmarkMerkleTree_BuildAndProve(b *testing.B) {
	blockSizes := []int{16, 64, 256}

	for _, size := range blockSizes {
		b.Run(fmt.Sprintf("Block_%d_Leaves", size), func(b *testing.B) {
			leaves := make([][]byte, size)
			for j := 0; j < size; j++ {
				h := make([]byte, 32)
				_, _ = rand.Read(h)
				leaves[j] = h
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				tree, err := commoncrypto.NewMerkleTree(leaves)
				if err != nil {
					b.Fatalf("failed to build tree: %v", err)
				}

				proof, err := tree.GenerateAuditProof(0)
				if err != nil {
					b.Fatalf("failed proof generation: %v", err)
				}

				valid, err := commoncrypto.VerifyProof(proof, leaves[0])
				if !valid || err != nil {
					b.Fatalf("proof verification failure: %v", err)
				}
			}
			b.StopTimer()

			ops := float64(b.N) / b.Elapsed().Seconds()
			b.ReportMetric(ops, "blocks/s")
		})
	}
}

// BenchmarkBlindIndex_ComputeThroughput measures HMAC-SHA256 blind indexing throughput
// for high-speed deterministic search indexing on encrypted PII.
func BenchmarkBlindIndex_ComputeThroughput(b *testing.B) {
	salt := make([]byte, 32)
	_, _ = rand.Read(salt)

	blindEngine, err := commoncrypto.NewBlindIndexEngine(salt)
	if err != nil {
		b.Fatalf("failed to init blind index engine: %v", err)
	}

	testValues := []string{
		"john.doe@enterprise-client.com",
		"DE89370400440532013000",
		"US-SSN-992-81-0021",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		val := testValues[i%len(testValues)]
		idx, err := blindEngine.ComputeBlindIndex(val)
		if err != nil || len(idx) != 64 {
			b.Fatalf("blind indexing failed: %v", err)
		}
	}
	b.StopTimer()

	ops := float64(b.N) / b.Elapsed().Seconds()
	b.ReportMetric(ops, "indices/s")
}
