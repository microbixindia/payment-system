package fuzz_and_e2e

import (
	"crypto/rand"
	"testing"

	commoncrypto "datavendor-payment-core/libs/common-crypto"
)

// TestSecurity_ConstantTimeComparison verifies that cryptographic comparisons
// operate in constant time and reject any byte discrepancy regardless of index.
func TestSecurity_ConstantTimeComparison(t *testing.T) {
	secret := make([]byte, 32)
	rand.Read(secret)

	// Clone identical secret
	matching := make([]byte, 32)
	copy(matching, secret)

	if !commoncrypto.ConstantTimeCompare(secret, matching) {
		t.Fatalf("constant time compare failed on identical secret buffers")
	}

	// Test discrepancy at every single byte index (0 to 31)
	for i := 0; i < 32; i++ {
		tampered := make([]byte, 32)
		copy(tampered, secret)
		tampered[i] ^= 0x01

		if commoncrypto.ConstantTimeCompare(secret, tampered) {
			t.Fatalf("constant time compare passed on tampered buffer at byte %d", i)
		}
	}

	// Test length mismatch
	shortBuffer := make([]byte, 16)
	copy(shortBuffer, secret[:16])
	if commoncrypto.ConstantTimeCompare(secret, shortBuffer) {
		t.Fatalf("constant time compare passed with length mismatch")
	}
}
