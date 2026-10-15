package commoncrypto

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestShamirSecretSharing_3of5SplitReconstruction(t *testing.T) {
	splitter := NewShamirKeySplitter()

	// 256-bit master key
	masterSecret := make([]byte, AES256KeySize)
	if _, err := rand.Read(masterSecret); err != nil {
		t.Fatalf("failed generating master secret: %v", err)
	}

	h := sha256.New()
	h.Write(masterSecret)
	expectedFingerprint := hex.EncodeToString(h.Sum(nil))

	// Split into 5 shares with threshold k=3
	shares, err := splitter.Split(masterSecret, 3, 5)
	if err != nil {
		t.Fatalf("failed to split secret: %v", err)
	}

	if len(shares) != 5 {
		t.Fatalf("expected 5 shares, got %d", len(shares))
	}

	// Case 1: Combine using shares [0, 1, 2]
	subset1 := []Share{shares[0], shares[1], shares[2]}
	reconstructed1, err := splitter.Combine(subset1, 3)
	if err != nil {
		t.Fatalf("failed combining subset1: %v", err)
	}
	if !bytes.Equal(masterSecret, reconstructed1) {
		t.Fatalf("reconstruction 1 mismatch!")
	}
	if !VerifySecretFingerprint(reconstructed1, expectedFingerprint) {
		t.Fatalf("fingerprint verification failed for subset1")
	}

	// Case 2: Combine using non-consecutive permutation [4, 1, 3]
	subset2 := []Share{shares[4], shares[1], shares[3]}
	reconstructed2, err := splitter.Combine(subset2, 3)
	if err != nil {
		t.Fatalf("failed combining subset2: %v", err)
	}
	if !bytes.Equal(masterSecret, reconstructed2) {
		t.Fatalf("reconstruction 2 mismatch!")
	}

	// Case 3: Combine with insufficient shares (2 shares when k=3) must fail
	insufficient := []Share{shares[0], shares[1]}
	_, err = splitter.Combine(insufficient, 3)
	if err == nil {
		t.Fatalf("expected error combining with 2 shares when k=3, but it succeeded")
	}

	// Case 4: Serialization and deserialization roundtrip
	serialized := shares[0].SerializeShare()
	deserialized, err := DeserializeShare(serialized)
	if err != nil {
		t.Fatalf("failed deserializing share: %v", err)
	}
	if deserialized.Index != shares[0].Index || !bytes.Equal(deserialized.Value, shares[0].Value) {
		t.Fatalf("serialized share mismatch")
	}
}

func TestShamirSecretSharing_InvalidThresholds(t *testing.T) {
	splitter := NewShamirKeySplitter()
	secret := []byte("VALID_256_BIT_SECRET_32_BYTES_XX")

	// k < 2
	_, err := splitter.Split(secret, 1, 5)
	if err == nil {
		t.Fatalf("expected error with k < 2")
	}

	// k > n
	_, err = splitter.Split(secret, 6, 5)
	if err == nil {
		t.Fatalf("expected error with k > n")
	}
}
