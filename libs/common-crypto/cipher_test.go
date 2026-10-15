package commoncrypto

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func TestFieldCipher_EncryptDecrypt(t *testing.T) {
	key := make([]byte, AES256KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("failed to generate random key: %v", err)
	}

	cipher, err := NewFieldCipher(key)
	if err != nil {
		t.Fatalf("failed to create field cipher: %v", err)
	}
	defer cipher.Close()

	plaintext := []byte("DATA_VENDOR_ENTERPRISE_API_SECRET_KEY_99281726")
	aad := []byte("tenant_uuid_883921-prod")

	// 1. Encrypt field
	payload, err := cipher.EncryptField(plaintext, aad)
	if err != nil {
		t.Fatalf("encryption failed: %v", err)
	}

	if len(payload.Nonce) != GCMStandardNonceSize {
		t.Errorf("expected nonce size %d, got %d", GCMStandardNonceSize, len(payload.Nonce))
	}
	if len(payload.AuthTag) != GCMTagSize {
		t.Errorf("expected tag size %d, got %d", GCMTagSize, len(payload.AuthTag))
	}

	// 2. Decrypt field
	decrypted, err := cipher.DecryptField(payload, aad)
	if err != nil {
		t.Fatalf("decryption failed: %v", err)
	}

	if !bytes.Equal(plaintext, decrypted) {
		t.Errorf("decrypted payload mismatch. Got %s, want %s", string(decrypted), string(plaintext))
	}

	// 3. Serialization Round-trip
	serialized := payload.Serialize()
	deserialized, err := DeserializePayload(serialized)
	if err != nil {
		t.Fatalf("deserialization failed: %v", err)
	}

	decryptedAfterRoundtrip, err := cipher.DecryptField(deserialized, aad)
	if err != nil {
		t.Fatalf("decryption after serialization roundtrip failed: %v", err)
	}

	if !bytes.Equal(plaintext, decryptedAfterRoundtrip) {
		t.Errorf("roundtrip decrypted mismatch. Got %s, want %s", string(decryptedAfterRoundtrip), string(plaintext))
	}
}

func TestFieldCipher_TamperResistance(t *testing.T) {
	key := make([]byte, AES256KeySize)
	rand.Read(key)

	cipher, err := NewFieldCipher(key)
	if err != nil {
		t.Fatalf("failed to create cipher: %v", err)
	}
	defer cipher.Close()

	plaintext := []byte("tamper-sensitive-banking-record")
	aad := []byte("account-12345")

	payload, err := cipher.EncryptField(plaintext, aad)
	if err != nil {
		t.Fatalf("encryption error: %v", err)
	}

	// Tamper with AAD
	invalidAAD := []byte("account-99999")
	_, err = cipher.DecryptField(payload, invalidAAD)
	if err == nil {
		t.Fatalf("expected authentication failure when AAD is altered, got nil")
	}

	// Tamper with Ciphertext
	payload.Ciphertext[0] ^= 0xFF
	_, err = cipher.DecryptField(payload, aad)
	if err == nil {
		t.Fatalf("expected authentication failure when ciphertext is bit-flipped, got nil")
	}

	// Tamper with Auth Tag
	payload.AuthTag[0] ^= 0xFF
	_, err = cipher.DecryptField(payload, aad)
	if err == nil {
		t.Fatalf("expected authentication failure when auth tag is bit-flipped, got nil")
	}
}

func TestFieldCipher_InvalidKeyLengths(t *testing.T) {
	shortKey := make([]byte, 16)
	_, err := NewFieldCipher(shortKey)
	if err == nil {
		t.Fatalf("expected error creating cipher with 16 byte key, got nil")
	}

	longKey := make([]byte, 64)
	_, err = NewFieldCipher(longKey)
	if err == nil {
		t.Fatalf("expected error creating cipher with 64 byte key, got nil")
	}
}
