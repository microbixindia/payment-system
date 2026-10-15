package commoncrypto

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"sync"
)

var (
	// ErrKeyNotFound indicates the requested Master Key ID is not registered in the HSM vault.
	ErrKeyNotFound = errors.New("commoncrypto/hsm: master key ID not found in vault")
)

// InMemHSMVault provides an in-memory HSM/KMS implementation for development,
// staging, and high-performance testing, implementing the KeyManagementService interface.
// In production, this can be swapped with AWS KMS, HashiCorp Vault Transit, or PKCS#11 HSM.
type InMemHSMVault struct {
	mu   sync.RWMutex
	keys map[string][]byte
}

// NewInMemHSMVault creates an empty HSM vault instance.
func NewInMemHSMVault() *InMemHSMVault {
	return &InMemHSMVault{
		keys: make(map[string][]byte),
	}
}

// RegisterKey associates a 32-byte Master Key (KEK) with a unique keyID.
func (h *InMemHSMVault) RegisterKey(keyID string, key []byte) error {
	if len(key) != AES256KeySize {
		return ErrInvalidKeyLength
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	k := make([]byte, AES256KeySize)
	copy(k, key)
	h.keys[keyID] = k
	return nil
}

// GenerateMasterKey creates and registers a brand new CSPRNG 256-bit key in the vault.
func (h *InMemHSMVault) GenerateMasterKey(keyID string) error {
	k := make([]byte, AES256KeySize)
	if _, err := io.ReadFull(rand.Reader, k); err != nil {
		return fmt.Errorf("%w: %v", ErrEntropyFailure, err)
	}
	return h.RegisterKey(keyID, k)
}

// EncryptDEK wraps a plaintext Data Encryption Key with the target Master Key (KEK).
func (h *InMemHSMVault) EncryptDEK(ctx context.Context, keyID string, plaintextDEK []byte) ([]byte, error) {
	h.mu.RLock()
	masterKey, exists := h.keys[keyID]
	h.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrKeyNotFound, keyID)
	}

	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, fmt.Errorf("commoncrypto/hsm: failed to initialize cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("commoncrypto/hsm: failed to create GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrEntropyFailure, err)
	}

	// Output format: [nonce || ciphertext_with_tag]
	ciphertext := gcm.Seal(nonce, nonce, plaintextDEK, []byte("dv-dek-wrap-context"))
	return ciphertext, nil
}

// DecryptDEK unwraps an encrypted Data Encryption Key using the target Master Key (KEK).
func (h *InMemHSMVault) DecryptDEK(ctx context.Context, keyID string, encryptedDEK []byte) ([]byte, error) {
	h.mu.RLock()
	masterKey, exists := h.keys[keyID]
	h.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrKeyNotFound, keyID)
	}

	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, fmt.Errorf("commoncrypto/hsm: failed to initialize cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("commoncrypto/hsm: failed to create GCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(encryptedDEK) < nonceSize+GCMTagSize {
		return nil, ErrInvalidCiphertext
	}

	nonce := encryptedDEK[:nonceSize]
	sealed := encryptedDEK[nonceSize:]

	plaintextDEK, err := gcm.Open(nil, nonce, sealed, []byte("dv-dek-wrap-context"))
	if err != nil {
		return nil, ErrAuthenticationFailed
	}

	return plaintextDEK, nil
}

// Destroy zeroes out all stored master keys upon vault decommissioning.
func (h *InMemHSMVault) Destroy() {
	h.mu.Lock()
	defer h.mu.Unlock()

	for k, v := range h.keys {
		MemZero(v)
		delete(h.keys, k)
	}
}
