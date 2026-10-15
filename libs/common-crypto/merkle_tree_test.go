package commoncrypto

import (
	"crypto/rand"
	"fmt"
	"testing"
)

func TestMerkleTree_BuildAndVerify(t *testing.T) {
	numTransactions := 16
	transactions := make([][]byte, numTransactions)
	for i := 0; i < numTransactions; i++ {
		txData := []byte(fmt.Sprintf("TX_SETTLEMENT_RECORD_ID_%06d_AMOUNT_10000_USD", i))
		transactions[i] = txData
	}

	tree, err := NewMerkleTree(transactions)
	if err != nil {
		t.Fatalf("failed to build Merkle tree: %v", err)
	}

	rootHex := tree.RootHex()
	if len(rootHex) != 64 {
		t.Fatalf("expected 64-character SHA-256 root hex, got %d chars: %s", len(rootHex), rootHex)
	}

	// Verify proofs for all leaves
	for i := 0; i < numTransactions; i++ {
		proof, err := tree.GenerateAuditProof(i)
		if err != nil {
			t.Fatalf("failed generating proof for leaf %d: %v", i, err)
		}

		valid, err := VerifyProof(proof, transactions[i])
		if err != nil || !valid {
			t.Fatalf("Merkle proof verification failed for leaf %d: %v", i, err)
		}

		// Tampering test: modify 1 byte of transaction data -> proof verification must fail
		tamperedData := make([]byte, len(transactions[i]))
		copy(tamperedData, transactions[i])
		tamperedData[0] ^= 0xFF

		validTampered, _ := VerifyProof(proof, tamperedData)
		if validTampered {
			t.Fatalf("security violation: proof validated tampered transaction data!")
		}
	}
}

func TestMerkleTree_OddLeafBalancing(t *testing.T) {
	// Test tree creation with an odd number of transactions (e.g. 7)
	numTxs := 7
	transactions := make([][]byte, numTxs)
	for i := 0; i < numTxs; i++ {
		buf := make([]byte, 32)
		rand.Read(buf)
		transactions[i] = buf
	}

	tree, err := NewMerkleTree(transactions)
	if err != nil {
		t.Fatalf("failed to construct tree with odd leaves: %v", err)
	}

	for i := 0; i < numTxs; i++ {
		proof, err := tree.GenerateAuditProof(i)
		if err != nil {
			t.Fatalf("proof generation error on odd leaf %d: %v", i, err)
		}

		valid, err := VerifyProof(proof, transactions[i])
		if !valid || err != nil {
			t.Fatalf("proof failed for odd leaf %d: %v", i, err)
		}
	}
}
