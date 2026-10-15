package commoncrypto

import (
	"testing"
)

func TestEd25519Signer_SignAndVerify(t *testing.T) {
	kp, err := GenerateEd25519KeyPair()
	if err != nil {
		t.Fatalf("failed generating Ed25519 key pair: %v", err)
	}
	defer kp.ZeroizePrivateKey()

	message := []byte("SWIFT_PACS008_FINANCIAL_SETTLEMENT_MESSAGE_HASH_E839174")

	// Sign message
	sigHex, err := HexSign(kp.PrivateKey, message)
	if err != nil {
		t.Fatalf("signing failed: %v", err)
	}

	if len(sigHex) != 128 { // 64 bytes = 128 hex chars
		t.Errorf("expected 128-char signature hex, got %d", len(sigHex))
	}

	// Verify valid signature
	if !HexVerify(kp.PublicKey, message, sigHex) {
		t.Fatalf("valid signature failed verification!")
	}

	// Tampered message check
	tamperedMessage := []byte("SWIFT_PACS008_FINANCIAL_SETTLEMENT_MESSAGE_HASH_E839175")
	if HexVerify(kp.PublicKey, tamperedMessage, sigHex) {
		t.Fatalf("tampered message unexpectedly verified!")
	}

	// Different public key check
	kp2, _ := GenerateEd25519KeyPair()
	defer kp2.ZeroizePrivateKey()
	if HexVerify(kp2.PublicKey, message, sigHex) {
		t.Fatalf("signature verified with wrong public key!")
	}
}
