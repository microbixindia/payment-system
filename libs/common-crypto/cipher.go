// Package commoncrypto provides enterprise-grade cryptographic primitives,
// envelope encryption, memory zeroization, blind indexing, and TLS 1.3 connection
// hardening for the DataVendor payment core ecosystem.
package commoncrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	// AES256KeySize defines the required key length for AES-256 in bytes (256 bits).
	AES256KeySize = 32

	// GCMStandardNonceSize is the recommended NIST standard nonce length in bytes (96 bits).
	GCMStandardNonceSize = 12

	// GCMTagSize is the standard authentication tag length in bytes (128 bits).
	GCMTagSize = 16

	// PayloadVersionV1 marks the serialization format for version 1 ciphertexts.
	PayloadVersionV1 = "dv1"
)

var (
	// ErrInvalidKeyLength is returned when the key provided does not equal 32 bytes.
	ErrInvalidKeyLength = errors.New("commoncrypto: cryptographic key must be exactly 32 bytes for AES-256-GCM")

	// ErrInvalidCiphertext is returned when ciphertext is corrupted, malformed, or missing authentication tags.
	ErrInvalidCiphertext = errors.New("commoncrypto: ciphertext payload is malformed or corrupted")

	// ErrAuthenticationFailed is returned when AES-GCM integrity verification fails or tags do not match.
	ErrAuthenticationFailed = errors.New("commoncrypto: message authentication verification failed (tampered ciphertext or invalid AAD)")

	// ErrEmptyPlaintext is returned when an attempt is made to encrypt empty data.
	ErrEmptyPlaintext = errors.New("commoncrypto: plaintext cannot be empty")

	// ErrEntropyFailure is returned when the system CSPRNG fails to read random bytes.
	ErrEntropyFailure = errors.New("commoncrypto: entropy failure in cryptographic random number generator")
)

// EncryptedPayload encapsulates a field-level encrypted value with its initialization vector,
// authentication tag, optional key version, and ciphertext bytes.
type EncryptedPayload struct {
	Version    string `json:"version"`
	KeyVersion string `json:"key_version"`
	Nonce      []byte `json:"nonce"`
	AuthTag    []byte `json:"auth_tag"`
	Ciphertext []byte `json:"ciphertext"`
}

// FieldCipher manages field-level encryption and decryption operations using AES-256-GCM
// with strict memory zeroization and Additional Authenticated Data (AAD) binding.
type FieldCipher struct {
	key []byte
}

// NewFieldCipher instantiates an AES-256-GCM cipher with defensive validation of key size.
// The provided key is defensively cloned into internal memory.
func NewFieldCipher(masterKey []byte) (*FieldCipher, error) {
	if len(masterKey) != AES256KeySize {
		return nil, fmt.Errorf("%w: received %d bytes", ErrInvalidKeyLength, len(masterKey))
	}

	keyCopy := make([]byte, AES256KeySize)
	copy(keyCopy, masterKey)

	return &FieldCipher{
		key: keyCopy,
	}, nil
}

// Close zeroes out the master key buffer in memory to mitigate cold boot and heap inspection vectors.
func (fc *FieldCipher) Close() {
	if fc.key != nil {
		MemZero(fc.key)
		fc.key = nil
	}
}

// EncryptField encrypts arbitrary plaintext bytes with AES-256-GCM, generating a fresh
// 96-bit random nonce for each operation. Optional additionalAuthenticatedData (AAD)
// can be bound to the ciphertext (e.g. database primary key, account ID, or tenant UUID).
func (fc *FieldCipher) EncryptField(plaintext []byte, aad []byte) (*EncryptedPayload, error) {
	if len(plaintext) == 0 {
		return nil, ErrEmptyPlaintext
	}
	if len(fc.key) != AES256KeySize {
		return nil, ErrInvalidKeyLength
	}

	block, err := aes.NewCipher(fc.key)
	if err != nil {
		return nil, fmt.Errorf("commoncrypto: failed to instantiate AES block: %w", err)
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("commoncrypto: failed to wrap GCM mode: %w", err)
	}

	nonce := make([]byte, GCMStandardNonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrEntropyFailure, err)
	}

	// GCM Seal appends the ciphertext and the 16-byte tag to the provided prefix dst slice.
	sealed := aesGCM.Seal(nil, nonce, plaintext, aad)

	// In Go's standard library, aesGCM.Seal appends the 16-byte auth tag at the very end of sealed.
	tagOffset := len(sealed) - GCMTagSize
	if tagOffset < 0 {
		return nil, ErrInvalidCiphertext
	}

	ciphertextPart := make([]byte, tagOffset)
	copy(ciphertextPart, sealed[:tagOffset])

	authTagPart := make([]byte, GCMTagSize)
	copy(authTagPart, sealed[tagOffset:])

	// Zero out intermediate sealed buffer
	MemZero(sealed)

	return &EncryptedPayload{
		Version:    PayloadVersionV1,
		KeyVersion: "v1",
		Nonce:      nonce,
		AuthTag:    authTagPart,
		Ciphertext: ciphertextPart,
	}, nil
}

// DecryptField decrypts an EncryptedPayload using AES-256-GCM, verifying authentication tag
// and checking AAD binding before releasing plaintext bytes.
func (fc *FieldCipher) DecryptField(payload *EncryptedPayload, aad []byte) ([]byte, error) {
	if payload == nil {
		return nil, ErrInvalidCiphertext
	}
	if len(fc.key) != AES256KeySize {
		return nil, ErrInvalidKeyLength
	}
	if len(payload.Nonce) != GCMStandardNonceSize {
		return nil, fmt.Errorf("%w: invalid nonce length %d", ErrInvalidCiphertext, len(payload.Nonce))
	}
	if len(payload.AuthTag) != GCMTagSize {
		return nil, fmt.Errorf("%w: invalid auth tag length %d", ErrInvalidCiphertext, len(payload.AuthTag))
	}

	block, err := aes.NewCipher(fc.key)
	if err != nil {
		return nil, fmt.Errorf("commoncrypto: failed to instantiate AES block: %w", err)
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("commoncrypto: failed to wrap GCM mode: %w", err)
	}

	// Reconstruct the sealed buffer [ciphertext || authTag]
	reconstructed := make([]byte, 0, len(payload.Ciphertext)+len(payload.AuthTag))
	reconstructed = append(reconstructed, payload.Ciphertext...)
	reconstructed = append(reconstructed, payload.AuthTag...)
	defer MemZero(reconstructed)

	plaintext, err := aesGCM.Open(nil, payload.Nonce, reconstructed, aad)
	if err != nil {
		return nil, ErrAuthenticationFailed
	}

	return plaintext, nil
}

// Serialize encodes the EncryptedPayload into a deterministic string representation:
// format: <version>$<keyVersion>$<hex(nonce)>$<hex(authTag)>$<hex(ciphertext)>
func (p *EncryptedPayload) Serialize() string {
	return fmt.Sprintf("%s$%s$%s$%s$%s",
		p.Version,
		p.KeyVersion,
		hex.EncodeToString(p.Nonce),
		hex.EncodeToString(p.AuthTag),
		hex.EncodeToString(p.Ciphertext),
	)
}

// DeserializePayload parses a serialized ciphertext string into an EncryptedPayload struct.
func DeserializePayload(serialized string) (*EncryptedPayload, error) {
	parts := strings.Split(serialized, "$")
	if len(parts) != 5 {
		return nil, fmt.Errorf("%w: expected 5 delimited tokens, got %d", ErrInvalidCiphertext, len(parts))
	}

	if parts[0] != PayloadVersionV1 {
		return nil, fmt.Errorf("%w: unsupported payload version %q", ErrInvalidCiphertext, parts[0])
	}

	nonce, err := hex.DecodeString(parts[2])
	if err != nil || len(nonce) != GCMStandardNonceSize {
		return nil, fmt.Errorf("%w: invalid hex nonce", ErrInvalidCiphertext)
	}

	authTag, err := hex.DecodeString(parts[3])
	if err != nil || len(authTag) != GCMTagSize {
		return nil, fmt.Errorf("%w: invalid hex auth tag", ErrInvalidCiphertext)
	}

	ciphertext, err := hex.DecodeString(parts[4])
	if err != nil {
		return nil, fmt.Errorf("%w: invalid hex ciphertext", ErrInvalidCiphertext)
	}

	return &EncryptedPayload{
		Version:    parts[0],
		KeyVersion: parts[1],
		Nonce:      nonce,
		AuthTag:    authTag,
		Ciphertext: ciphertext,
	}, nil
}

// MemZero defensively overwrites byte slices with zero bytes in place
// to eliminate sensitive data from memory dumps.
func MemZero(buffer []byte) {
	for i := range buffer {
		buffer[i] = 0
	}
}

// ConstantTimeCompare wraps crypto/subtle.ConstantTimeCompare for timing-attack-safe comparisons.
func ConstantTimeCompare(x, y []byte) bool {
	return subtle.ConstantTimeCompare(x, y) == 1
}
