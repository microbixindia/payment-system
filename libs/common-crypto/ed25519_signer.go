package commoncrypto

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
)

var (
	ErrInvalidSignature       = errors.New("commoncrypto/ed25519: signature verification failed")
	ErrInvalidEd25519PublicKey = errors.New("commoncrypto/ed25519: public key must be 32 bytes")
	ErrInvalidEd25519PrivKey   = errors.New("commoncrypto/ed25519: private key must be 64 bytes")
)

// KeyPair represents an Ed25519 asymmetric signing and verification key pair.
type KeyPair struct {
	PublicKey  ed25519.PublicKey
	PrivateKey ed25519.PrivateKey
}

// GenerateEd25519KeyPair generates a fresh 256-bit Ed25519 key pair using system CSPRNG.
func GenerateEd25519KeyPair() (*KeyPair, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("%w: failed generating ed25519 key: %v", ErrEntropyFailure, err)
	}

	return &KeyPair{
		PublicKey:  pub,
		PrivateKey: priv,
	}, nil
}

// SignPayload signs arbitrary message bytes using the 256-bit Ed25519 private key.
func SignPayload(privateKey ed25519.PrivateKey, message []byte) ([]byte, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, ErrInvalidEd25519PrivKey
	}
	return ed25519.Sign(privateKey, message), nil
}

// VerifySignature verifies an Ed25519 signature in constant time against public key and original message.
func VerifySignature(publicKey ed25519.PublicKey, message, signature []byte) bool {
	if len(publicKey) != ed25519.PublicKeySize || len(signature) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(publicKey, message, signature)
}

// HexSign computes the signature and returns it as a lowercase hex string.
func HexSign(privateKey ed25519.PrivateKey, message []byte) (string, error) {
	sig, err := SignPayload(privateKey, message)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(sig), nil
}

// HexVerify checks a hex-encoded signature.
func HexVerify(publicKey ed25519.PublicKey, message []byte, sigHex string) bool {
	sigBytes, err := hex.DecodeString(sigHex)
	if err != nil {
		return false
	}
	return VerifySignature(publicKey, message, sigBytes)
}

// ZeroizePrivateKey cleanses sensitive Ed25519 private key bytes from memory.
func (kp *KeyPair) ZeroizePrivateKey() {
	if kp.PrivateKey != nil {
		MemZero(kp.PrivateKey)
		kp.PrivateKey = nil
	}
}
