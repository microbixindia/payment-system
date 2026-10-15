package commoncrypto

import (
	"bytes"
	"context"
	"testing"
)

func TestHybridKEM_EncapsulateDecapsulateRoundtrip(t *testing.T) {
	keypair, err := GenerateHybridKEMKeypair()
	if err != nil {
		t.Fatalf("failed generating hybrid keypair: %v", err)
	}
	defer keypair.Zeroize()

	// 1. Encapsulate on public key
	dek1, ciphertext, err := keypair.PublicKey.Encapsulate()
	if err != nil {
		t.Fatalf("hybrid encapsulation failed: %v", err)
	}

	if len(dek1) != AES256KeySize {
		t.Fatalf("expected 32-byte DEK, got %d", len(dek1))
	}
	if len(ciphertext.EphemeralX25519) != X25519KeySize {
		t.Fatalf("expected 32-byte ephemeral X25519, got %d", len(ciphertext.EphemeralX25519))
	}
	if len(ciphertext.PostQuantumEncaps) != MLKEM768CiphertextSize {
		t.Fatalf("expected 1088-byte post-quantum ciphertext, got %d", len(ciphertext.PostQuantumEncaps))
	}

	// 2. Decapsulate on private key
	dek2, err := keypair.PrivateKey.Decapsulate(ciphertext)
	if err != nil {
		t.Fatalf("hybrid decapsulation failed: %v", err)
	}

	if !bytes.Equal(dek1, dek2) {
		t.Fatalf("decapsulated DEK does not match encapsulated DEK!")
	}
}

func TestHybridKEM_TamperResistance(t *testing.T) {
	keypair, err := GenerateHybridKEMKeypair()
	if err != nil {
		t.Fatalf("failed generating keypair: %v", err)
	}
	defer keypair.Zeroize()

	dekOriginal, ciphertext, err := keypair.PublicKey.Encapsulate()
	if err != nil {
		t.Fatalf("encapsulation failed: %v", err)
	}

	// 1. Tamper with ephemeral classical key -> derived DEK must NOT match original DEK
	tamperedCiphertext := &HybridCiphertext{
		EphemeralX25519:   make([]byte, len(ciphertext.EphemeralX25519)),
		PostQuantumEncaps: make([]byte, len(ciphertext.PostQuantumEncaps)),
	}
	copy(tamperedCiphertext.EphemeralX25519, ciphertext.EphemeralX25519)
	copy(tamperedCiphertext.PostQuantumEncaps, ciphertext.PostQuantumEncaps)
	tamperedCiphertext.EphemeralX25519[0] ^= 0x55

	dekTamperedClassical, err := keypair.PrivateKey.Decapsulate(tamperedCiphertext)
	if err == nil && bytes.Equal(dekOriginal, dekTamperedClassical) {
		t.Fatalf("cryptographic break: classical bit-flip produced identical DEK!")
	}

	// 2. Tamper with post-quantum encapsulation -> derived DEK must NOT match original DEK
	tamperedPQ := &HybridCiphertext{
		EphemeralX25519:   make([]byte, len(ciphertext.EphemeralX25519)),
		PostQuantumEncaps: make([]byte, len(ciphertext.PostQuantumEncaps)),
	}
	copy(tamperedPQ.EphemeralX25519, ciphertext.EphemeralX25519)
	copy(tamperedPQ.PostQuantumEncaps, ciphertext.PostQuantumEncaps)
	tamperedPQ.PostQuantumEncaps[0] ^= 0xAA

	dekTamperedPQ, err := keypair.PrivateKey.Decapsulate(tamperedPQ)
	if err == nil && bytes.Equal(dekOriginal, dekTamperedPQ) {
		t.Fatalf("cryptographic break: post-quantum bit-flip produced identical DEK!")
	}
}

func TestHybridEnvelopeManager_EncryptDecrypt(t *testing.T) {
	ctx := context.Background()
	keypair, err := GenerateHybridKEMKeypair()
	if err != nil {
		t.Fatalf("failed generating keypair: %v", err)
	}
	defer keypair.Zeroize()

	manager := NewHybridEnvelopeManager()
	plaintext := []byte("DATASET_PURCHASE_HIGH_SECURITY_ORDER_19283746")
	aad := []byte("tenant-quantum-bound")

	// Encrypt
	payload, err := manager.EncryptWithHybridKEM(ctx, "quantum-kek-001", keypair.PublicKey, plaintext, aad)
	if err != nil {
		t.Fatalf("hybrid envelope encryption failed: %v", err)
	}

	// Decrypt
	decrypted, err := manager.DecryptWithHybridKEM(ctx, keypair.PrivateKey, payload, aad)
	if err != nil {
		t.Fatalf("hybrid envelope decryption failed: %v", err)
	}

	if !bytes.Equal(plaintext, decrypted) {
		t.Fatalf("decrypted payload mismatch. Got %s, want %s", string(decrypted), string(plaintext))
	}

	// Decrypt with tampered AAD must fail
	_, err = manager.DecryptWithHybridKEM(ctx, keypair.PrivateKey, payload, []byte("tampered-tenant"))
	if err == nil {
		t.Fatalf("expected error decrypting with tampered AAD, but it succeeded")
	}

	// JSON Roundtrip test
	jsonBytes, err := payload.SerializeJSON()
	if err != nil {
		t.Fatalf("serialization failed: %v", err)
	}

	deserializedPayload, err := DeserializeHybridJSON(jsonBytes)
	if err != nil {
		t.Fatalf("deserialization failed: %v", err)
	}

	decryptedAfterRoundtrip, err := manager.DecryptWithHybridKEM(ctx, keypair.PrivateKey, deserializedPayload, aad)
	if err != nil || !bytes.Equal(plaintext, decryptedAfterRoundtrip) {
		t.Fatalf("decryption after JSON roundtrip failed")
	}
}
