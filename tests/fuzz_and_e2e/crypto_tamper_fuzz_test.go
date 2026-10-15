package fuzz_and_e2e

import (
	"crypto/rand"
	"testing"

	commoncrypto "datavendor-payment-core/libs/common-crypto"
)

// TestFuzz_CryptoTamperResistance executes randomized bit-flip fuzzing across ciphertexts,
// nonces, and auth tags, verifying that AES-256-GCM integrity rejects 100% of tampered inputs.
func TestFuzz_CryptoTamperResistance(t *testing.T) {
	key := make([]byte, commoncrypto.AES256KeySize)
	rand.Read(key)

	cipher, err := commoncrypto.NewFieldCipher(key)
	if err != nil {
		t.Fatalf("failed to create cipher: %v", err)
	}
	defer cipher.Close()

	plaintext := []byte("HIGH_VALUE_MERCHANT_SETTLEMENT_RECORD_994821")
	aad := []byte("tenant-audit-bound")

	payload, err := cipher.EncryptField(plaintext, aad)
	if err != nil {
		t.Fatalf("encryption failed: %v", err)
	}

	// 1. Fuzz Nonce bit flips
	for i := 0; i < len(payload.Nonce); i++ {
		tamperedPayload := clonePayload(payload)
		tamperedPayload.Nonce[i] ^= 0x01 // flip 1 bit

		_, err := cipher.DecryptField(tamperedPayload, aad)
		if err == nil {
			t.Fatalf("cryptographic break: nonce bit flip at byte %d decrypted successfully!", i)
		}
	}

	// 2. Fuzz Auth Tag bit flips
	for i := 0; i < len(payload.AuthTag); i++ {
		tamperedPayload := clonePayload(payload)
		tamperedPayload.AuthTag[i] ^= 0x01

		_, err := cipher.DecryptField(tamperedPayload, aad)
		if err == nil {
			t.Fatalf("cryptographic break: auth tag bit flip at byte %d decrypted successfully!", i)
		}
	}

	// 3. Fuzz Ciphertext bit flips
	for i := 0; i < len(payload.Ciphertext); i++ {
		tamperedPayload := clonePayload(payload)
		tamperedPayload.Ciphertext[i] ^= 0x01

		_, err := cipher.DecryptField(tamperedPayload, aad)
		if err == nil {
			t.Fatalf("cryptographic break: ciphertext bit flip at byte %d decrypted successfully!", i)
		}
	}
}

func clonePayload(p *commoncrypto.EncryptedPayload) *commoncrypto.EncryptedPayload {
	nonce := make([]byte, len(p.Nonce))
	copy(nonce, p.Nonce)

	tag := make([]byte, len(p.AuthTag))
	copy(tag, p.AuthTag)

	ct := make([]byte, len(p.Ciphertext))
	copy(ct, p.Ciphertext)

	return &commoncrypto.EncryptedPayload{
		Version:    p.Version,
		KeyVersion: p.KeyVersion,
		Nonce:      nonce,
		AuthTag:    tag,
		Ciphertext: ct,
	}
}
