package commoncrypto

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

var (
	// ErrEmptyBlindIndexInput is returned when input string is empty.
	ErrEmptyBlindIndexInput = errors.New("commoncrypto: blind index input value cannot be empty")

	// ErrInvalidSalt is returned when salt length is insufficient.
	ErrInvalidSalt = errors.New("commoncrypto: blind index salt must be at least 16 bytes")
)

// BlindIndexEngine generates deterministic cryptographic hashes of sensitive search terms
// (e.g. corporate emails, bank account numbers, tax IDs) using HMAC-SHA256.
// This allows sub-5ms database lookups without decrypting entire table rows.
type BlindIndexEngine struct {
	salt []byte
}

// NewBlindIndexEngine creates an index engine initialized with an isolated secret salt.
func NewBlindIndexEngine(salt []byte) (*BlindIndexEngine, error) {
	if len(salt) < 16 {
		return nil, ErrInvalidSalt
	}
	saltCopy := make([]byte, len(salt))
	copy(saltCopy, salt)
	return &BlindIndexEngine{salt: saltCopy}, nil
}

// ComputeBlindIndex normalizes the input (trimming and lowercasing) and calculates
// an HMAC-SHA256 blind index returned as a 64-character lowercase hex string.
func (b *BlindIndexEngine) ComputeBlindIndex(value string) (string, error) {
	normalized := strings.TrimSpace(strings.ToLower(value))
	if normalized == "" {
		return "", ErrEmptyBlindIndexInput
	}

	mac := hmac.New(sha256.New, b.salt)
	mac.Write([]byte(normalized))
	hashBytes := mac.Sum(nil)

	return hex.EncodeToString(hashBytes), nil
}

// ComputeTruncatedIndex calculates an index with truncated length for balance
// between search collision resilience and anonymity (k-anonymity).
func (b *BlindIndexEngine) ComputeTruncatedIndex(value string, length int) (string, error) {
	fullIndex, err := b.ComputeBlindIndex(value)
	if err != nil {
		return "", err
	}
	if length > len(fullIndex) || length <= 0 {
		return fullIndex, nil
	}
	return fullIndex[:length], nil
}
