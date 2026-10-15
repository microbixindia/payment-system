package fuzz_and_e2e

import (
	"crypto/rand"
	"testing"

	commoncrypto "datavendor-payment-core/libs/common-crypto"
)

// TestFuzz_MerkleAuditBlockIntegrity generates random transaction blocks, computes Merkle roots,
// signs with Ed25519, and verifies audit inclusion proofs across 500 randomized trials.
func TestFuzz_MerkleAuditBlockIntegrity(t *testing.T) {
	kp, err := commoncrypto.GenerateEd25519KeyPair()
	if err != nil {
		t.Fatalf("failed to generate ed25519 key pair: %v", err)
	}
	defer kp.ZeroizePrivateKey()

	for trial := 1; trial <= 500; trial++ {
		// Variable block size between 2 and 64 transactions
		blockSize := 2 + (trial % 63)
		transactions := make([][]byte, blockSize)

		for j := 0; j < blockSize; j++ {
			txPayload := make([]byte, 64)
			rand.Read(txPayload)
			transactions[j] = txPayload
		}

		// Build Merkle Tree
		tree, err := commoncrypto.NewMerkleTree(transactions)
		if err != nil {
			t.Fatalf("[Trial %d] Merkle tree creation failed: %v", trial, err)
		}

		// Sign Merkle root with Ed25519
		rootBytes := tree.Root.Hash
		signature, err := commoncrypto.SignPayload(kp.PrivateKey, rootBytes)
		if err != nil {
			t.Fatalf("[Trial %d] Signing Merkle root failed: %v", trial, err)
		}

		// Verify signature
		if !commoncrypto.VerifySignature(kp.PublicKey, rootBytes, signature) {
			t.Fatalf("[Trial %d] Signature verification failed on valid Merkle root", trial)
		}

		// Verify random leaf proof
		targetIdx := trial % blockSize
		proof, err := tree.GenerateAuditProof(targetIdx)
		if err != nil {
			t.Fatalf("[Trial %d] Proof generation failed for leaf %d: %v", trial, targetIdx, err)
		}

		valid, err := commoncrypto.VerifyProof(proof, transactions[targetIdx])
		if !valid || err != nil {
			t.Fatalf("[Trial %d] Proof verification failed for leaf %d: %v", trial, targetIdx, err)
		}

		// Tampering test
		tampered := make([]byte, len(transactions[targetIdx]))
		copy(tampered, transactions[targetIdx])
		tampered[0] ^= 0x55 // bit flip

		validTampered, _ := commoncrypto.VerifyProof(proof, tampered)
		if validTampered {
			t.Fatalf("[Trial %d] Security failure: Merkle proof accepted tampered transaction data!", trial)
		}
	}
}
