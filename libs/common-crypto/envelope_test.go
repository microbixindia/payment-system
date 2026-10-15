package commoncrypto

import (
	"bytes"
	"context"
	"testing"
)

func TestEnvelopeManager_EncryptDecrypt(t *testing.T) {
	ctx := context.Background()
	vault := NewMemVaultForTest(t)
	defer vault.Destroy()

	keyID := "dv-master-kek-us-east-1"
	if err := vault.GenerateMasterKey(keyID); err != nil {
		t.Fatalf("failed to generate master key: %v", err)
	}

	mgr := NewEnvelopeManager(vault)

	plaintext := []byte("DATASET_BILLING_INVOICE_CUSTOMER_PII_10928374")
	aad := []byte("dataset-order-001")

	// Encrypt envelope
	envelope, err := mgr.Encrypt(ctx, keyID, plaintext, aad)
	if err != nil {
		t.Fatalf("envelope encryption failed: %v", err)
	}

	if envelope.KeyID != keyID {
		t.Errorf("expected KeyID %s, got %s", keyID, envelope.KeyID)
	}
	if len(envelope.EncryptedDEK) == 0 {
		t.Fatalf("expected non-empty encrypted DEK")
	}

	// Decrypt envelope
	decrypted, err := mgr.Decrypt(ctx, envelope, aad)
	if err != nil {
		t.Fatalf("envelope decryption failed: %v", err)
	}

	if !bytes.Equal(plaintext, decrypted) {
		t.Fatalf("decrypted envelope mismatch. Got %s, want %s", string(decrypted), string(plaintext))
	}

	// Serialization test
	jsonBytes, err := envelope.SerializeJSON()
	if err != nil {
		t.Fatalf("failed to serialize envelope: %v", err)
	}

	deserializedEnv, err := DeserializeJSON(jsonBytes)
	if err != nil {
		t.Fatalf("failed to deserialize envelope JSON: %v", err)
	}

	decryptedAfterRoundtrip, err := mgr.Decrypt(ctx, deserializedEnv, aad)
	if err != nil {
		t.Fatalf("failed to decrypt after JSON roundtrip: %v", err)
	}

	if !bytes.Equal(plaintext, decryptedAfterRoundtrip) {
		t.Fatalf("roundtrip decrypted mismatch")
	}
}

func NewMemVaultForTest(t *testing.T) *InMemHSMVault {
	t.Helper()
	return NewInMemHSMVault()
}
