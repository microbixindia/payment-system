package commoncrypto

import (
	"crypto/rand"
	"testing"
)

func TestBlindIndexEngine(t *testing.T) {
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		t.Fatalf("failed generating salt: %v", err)
	}

	engine, err := NewBlindIndexEngine(salt)
	if err != nil {
		t.Fatalf("failed to create blind index engine: %v", err)
	}

	// Normalization test (case and whitespace invariance)
	idx1, err := engine.ComputeBlindIndex("billing@datavendor.ai")
	if err != nil {
		t.Fatalf("blind index 1 failed: %v", err)
	}

	idx2, err := engine.ComputeBlindIndex("  BILLING@DATAVENDOR.AI ")
	if err != nil {
		t.Fatalf("blind index 2 failed: %v", err)
	}

	if idx1 != idx2 {
		t.Errorf("blind index should be identical after normalization: %s vs %s", idx1, idx2)
	}

	// Truncated index test
	truncated, err := engine.ComputeTruncatedIndex("billing@datavendor.ai", 16)
	if err != nil {
		t.Fatalf("truncated index failed: %v", err)
	}
	if len(truncated) != 16 {
		t.Errorf("expected truncated length 16, got %d", len(truncated))
	}
	if truncated != idx1[:16] {
		t.Errorf("truncated prefix mismatch")
	}

	// Salt isolation test
	salt2 := make([]byte, 32)
	rand.Read(salt2)
	engine2, _ := NewBlindIndexEngine(salt2)
	idx3, _ := engine2.ComputeBlindIndex("billing@datavendor.ai")

	if idx1 == idx3 {
		t.Errorf("different salts must produce distinct blind indexes")
	}
}
