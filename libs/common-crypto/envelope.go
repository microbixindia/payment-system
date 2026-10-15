package commoncrypto

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

var (
	// ErrKMSOperationFailed indicates an unrecoverable KMS network or cryptographic error.
	ErrKMSOperationFailed = errors.New("commoncrypto: KMS key unwrapping or generation failed")

	// ErrCorruptedEnvelope indicates invalid JSON or unparseable envelope structure.
	ErrCorruptedEnvelope = errors.New("commoncrypto: corrupted envelope encrypted container")
)

// KeyManagementService defines the contract for KMS/HSM systems (AWS KMS, Google Cloud KMS,
// Vault, or hardware HSMs) to encrypt and decrypt Data Encryption Keys (DEKs).
type KeyManagementService interface {
	// EncryptDEK encrypts a plaintext Data Encryption Key using a Master Key / Key Encryption Key (KEK).
	EncryptDEK(ctx context.Context, keyID string, plaintextDEK []byte) (encryptedDEK []byte, err error)

	// DecryptDEK decrypts an encrypted Data Encryption Key using the specified Key Encryption Key (KEK).
	DecryptDEK(ctx context.Context, keyID string, encryptedDEK []byte) (plaintextDEK []byte, err error)
}

// EnvelopePayload represents an envelope-encrypted data structure.
// The data is protected by an ephemeral Data Encryption Key (DEK),
// and the DEK itself is protected by an external Key Encryption Key (KEK) in KMS/HSM.
type EnvelopePayload struct {
	KeyID        string `json:"key_id"`
	EncryptedDEK []byte `json:"encrypted_dek"`
	Nonce        []byte `json:"nonce"`
	AuthTag      []byte `json:"auth_tag"`
	Ciphertext   []byte `json:"ciphertext"`
}

// EnvelopeManager handles client-side envelope encryption workflows.
type EnvelopeManager struct {
	kms KeyManagementService
}

// NewEnvelopeManager constructs an EnvelopeManager backed by a KeyManagementService.
func NewEnvelopeManager(kms KeyManagementService) *EnvelopeManager {
	return &EnvelopeManager{
		kms: kms,
	}
}

// Encrypt generates a fresh random 256-bit DEK, encrypts the plaintext with AES-256-GCM,
// wraps the DEK with KMS, and securely zeroes the plaintext DEK in memory.
func (em *EnvelopeManager) Encrypt(ctx context.Context, keyID string, plaintext []byte, aad []byte) (*EnvelopePayload, error) {
	if len(plaintext) == 0 {
		return nil, ErrEmptyPlaintext
	}

	// 1. Generate an ephemeral 256-bit Data Encryption Key (DEK)
	dek := make([]byte, AES256KeySize)
	if _, err := io.ReadFull(rand.Reader, dek); err != nil {
		return nil, fmt.Errorf("%w: failed to generate DEK: %v", ErrEntropyFailure, err)
	}
	defer MemZero(dek)

	// 2. Encrypt the plaintext using the ephemeral DEK
	cipher, err := NewFieldCipher(dek)
	if err != nil {
		return nil, err
	}
	defer cipher.Close()

	payload, err := cipher.EncryptField(plaintext, aad)
	if err != nil {
		return nil, fmt.Errorf("commoncrypto: failed to encrypt data with DEK: %w", err)
	}

	// 3. Encrypt the DEK using the remote KMS / KEK
	encryptedDEK, err := em.kms.EncryptDEK(ctx, keyID, dek)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to wrap DEK: %v", ErrKMSOperationFailed, err)
	}

	return &EnvelopePayload{
		KeyID:        keyID,
		EncryptedDEK: encryptedDEK,
		Nonce:        payload.Nonce,
		AuthTag:      payload.AuthTag,
		Ciphertext:   payload.Ciphertext,
	}, nil
}

// Decrypt unwraps the encrypted DEK via KMS, decrypts the ciphertext payload,
// and zeroes the plaintext DEK immediately.
func (em *EnvelopeManager) Decrypt(ctx context.Context, envelope *EnvelopePayload, aad []byte) ([]byte, error) {
	if envelope == nil || len(envelope.EncryptedDEK) == 0 || len(envelope.Ciphertext) == 0 {
		return nil, ErrCorruptedEnvelope
	}

	// 1. Unwrap the DEK using KMS
	plaintextDEK, err := em.kms.DecryptDEK(ctx, envelope.KeyID, envelope.EncryptedDEK)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to unwrap DEK: %v", ErrKMSOperationFailed, err)
	}
	defer MemZero(plaintextDEK)

	// 2. Decrypt the payload with the unshielded DEK
	cipher, err := NewFieldCipher(plaintextDEK)
	if err != nil {
		return nil, err
	}
	defer cipher.Close()

	payload := &EncryptedPayload{
		Version:    PayloadVersionV1,
		KeyVersion: "v1",
		Nonce:      envelope.Nonce,
		AuthTag:    envelope.AuthTag,
		Ciphertext: envelope.Ciphertext,
	}

	plaintext, err := cipher.DecryptField(payload, aad)
	if err != nil {
		return nil, fmt.Errorf("commoncrypto: envelope payload decryption failed: %w", err)
	}

	return plaintext, nil
}

// SerializeJSON serializes an envelope into standard JSON bytes.
func (ep *EnvelopePayload) SerializeJSON() ([]byte, error) {
	return json.Marshal(ep)
}

// DeserializeJSON parses JSON bytes into an EnvelopePayload.
func DeserializeJSON(data []byte) (*EnvelopePayload, error) {
	var ep EnvelopePayload
	if err := json.Unmarshal(data, &ep); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorruptedEnvelope, err)
	}
	return &ep, nil
}
