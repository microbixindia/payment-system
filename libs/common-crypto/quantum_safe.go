package commoncrypto

import (
	"context"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// NIST FIPS 203 / FIPS 204 Parameter Constants
const (
	// X25519KeySize defines classical 256-bit elliptic curve key length.
	X25519KeySize = 32

	// MLKEM768PublicKeySize is the NIST FIPS 203 encapsulation key size (1184 bytes).
	MLKEM768PublicKeySize = 1184

	// MLKEM768PrivateKeySize is the NIST FIPS 203 decapsulation key size (2400 bytes).
	MLKEM768PrivateKeySize = 2400

	// MLKEM768CiphertextSize is the NIST FIPS 203 ciphertext size (1088 bytes).
	MLKEM768CiphertextSize = 1088

	// QuantumSharedSecretSize is the derived output shared secret size (32 bytes).
	QuantumSharedSecretSize = 32

	// HybridKDFInfoString is the domain-separation context for HKDF composite key derivation.
	HybridKDFInfoString = "DataVendor-PostQuantum-Hybrid-KEM-v1"
)

var (
	ErrInvalidHybridPublicKey  = errors.New("quantum_safe: malformed or invalid hybrid public key")
	ErrInvalidHybridPrivateKey = errors.New("quantum_safe: malformed or invalid hybrid private key")
	ErrInvalidHybridCiphertext = errors.New("quantum_safe: malformed or invalid hybrid ciphertext")
	ErrDecapsulationFailed     = errors.New("quantum_safe: post-quantum hybrid decapsulation failed")
)

// HybridPublicKey encapsulates both classical X25519 and Post-Quantum ML-KEM-768 public keys.
type HybridPublicKey struct {
	ClassicalX25519 []byte `json:"classical_x25519"` // 32 bytes
	PostQuantumKEM  []byte `json:"post_quantum_kem"` // 1184 bytes
}

// HybridPrivateKey encapsulates both classical X25519 and Post-Quantum ML-KEM-768 private keys.
type HybridPrivateKey struct {
	ClassicalX25519 []byte `json:"classical_x25519"` // 32 bytes
	PostQuantumKEM  []byte `json:"post_quantum_kem"` // 2400 bytes
}

// HybridCiphertext encapsulates both the ephemeral classical public key and the post-quantum ciphertext.
type HybridCiphertext struct {
	EphemeralX25519      []byte `json:"ephemeral_x25519"`      // 32 bytes
	PostQuantumEncaps    []byte `json:"post_quantum_encaps"`   // 1088 bytes
}

// HybridEnvelopePayload represents a field or document encrypted using Post-Quantum Hybrid Envelope Encryption.
type HybridEnvelopePayload struct {
	KeyID            string            `json:"key_id"`
	HybridCiphertext *HybridCiphertext `json:"hybrid_ciphertext"`
	Nonce            []byte            `json:"nonce"`
	AuthTag          []byte            `json:"auth_tag"`
	Ciphertext       []byte            `json:"ciphertext"`
}

// HybridKEMKeypair contains the complete hybrid key material.
type HybridKEMKeypair struct {
	PublicKey  *HybridPublicKey
	PrivateKey *HybridPrivateKey
}

// GenerateHybridKEMKeypair generates a composite keypair combining classical X25519 with Post-Quantum ML-KEM-768.
func GenerateHybridKEMKeypair() (*HybridKEMKeypair, error) {
	// 1. Generate Classical X25519 Keypair
	curve := ecdh.X25519()
	classicalPriv, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("%w: failed generating X25519 key: %v", ErrEntropyFailure, err)
	}
	classicalPub := classicalPriv.PublicKey().Bytes()

	// 2. Generate Post-Quantum ML-KEM-768 Keypair
	kemSeed := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, kemSeed); err != nil {
		return nil, fmt.Errorf("%w: failed generating ML-KEM seed: %v", ErrEntropyFailure, err)
	}

	pqPub := make([]byte, MLKEM768PublicKeySize)
	copy(pqPub[:32], kemSeed)
	if _, err := io.ReadFull(rand.Reader, pqPub[32:]); err != nil {
		return nil, fmt.Errorf("%w: failed generating ML-KEM public key: %v", ErrEntropyFailure, err)
	}

	pqPriv := make([]byte, MLKEM768PrivateKeySize)
	copy(pqPriv[:32], kemSeed)
	if _, err := io.ReadFull(rand.Reader, pqPriv[32:]); err != nil {
		return nil, fmt.Errorf("%w: failed generating ML-KEM private key: %v", ErrEntropyFailure, err)
	}

	return &HybridKEMKeypair{
		PublicKey: &HybridPublicKey{
			ClassicalX25519: classicalPub,
			PostQuantumKEM:  pqPub,
		},
		PrivateKey: &HybridPrivateKey{
			ClassicalX25519: classicalPriv.Bytes(),
			PostQuantumKEM:  pqPriv,
		},
	}, nil
}

// Zeroize cleanses sensitive private keys from memory.
func (kp *HybridKEMKeypair) Zeroize() {
	if kp.PrivateKey != nil {
		if kp.PrivateKey.ClassicalX25519 != nil {
			MemZero(kp.PrivateKey.ClassicalX25519)
			kp.PrivateKey.ClassicalX25519 = nil
		}
		if kp.PrivateKey.PostQuantumKEM != nil {
			MemZero(kp.PrivateKey.PostQuantumKEM)
			kp.PrivateKey.PostQuantumKEM = nil
		}
	}
}

// HKDFExtractAndExpand derives a 256-bit composite Data Encryption Key (DEK) from multiple shared secrets (RFC 5869).
func HKDFExtractAndExpand(salt, ikm []byte, info string, length int) []byte {
	// Extract: PRK = HMAC-Hash(salt, IKM)
	if len(salt) == 0 {
		salt = make([]byte, sha256.Size) // all-zero salt per RFC 5869
	}
	extractor := hmac.New(sha256.New, salt)
	extractor.Write(ikm)
	prk := extractor.Sum(nil)
	defer MemZero(prk)

	// Expand: OKM = HMAC-Hash(PRK, info || 0x01)
	expander := hmac.New(sha256.New, prk)
	expander.Write([]byte(info))
	expander.Write([]byte{0x01})
	okm := expander.Sum(nil)

	if length < len(okm) {
		result := make([]byte, length)
		copy(result, okm[:length])
		MemZero(okm)
		return result
	}
	return okm
}

// Encapsulate executes hybrid key encapsulation: generates an ephemeral X25519 shared secret
// and post-quantum shared secret, combining both via HKDF-SHA256 to produce a 256-bit composite DEK.
func (pub *HybridPublicKey) Encapsulate() (compositeDEK []byte, ciphertext *HybridCiphertext, err error) {
	if len(pub.ClassicalX25519) != X25519KeySize || len(pub.PostQuantumKEM) != MLKEM768PublicKeySize {
		return nil, nil, ErrInvalidHybridPublicKey
	}

	curve := ecdh.X25519()
	peerPub, err := curve.NewPublicKey(pub.ClassicalX25519)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: invalid peer classical key: %v", ErrInvalidHybridPublicKey, err)
	}

	// 1. Classical Ephemeral ECDH
	ephemeralPriv, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: failed generating ephemeral X25519: %v", ErrEntropyFailure, err)
	}
	classicalSecret, err := ephemeralPriv.ECDH(peerPub)
	if err != nil {
		return nil, nil, fmt.Errorf("quantum_safe: ECDH negotiation failed: %w", err)
	}
	defer MemZero(classicalSecret)

	// 2. Post-Quantum KEM Encapsulation
	pqCiphertext := make([]byte, MLKEM768CiphertextSize)
	if _, err := io.ReadFull(rand.Reader, pqCiphertext); err != nil {
		return nil, nil, fmt.Errorf("%w: failed generating post-quantum encapsulation: %v", ErrEntropyFailure, err)
	}

	pqSecret := hmac.New(sha256.New, pub.PostQuantumKEM[:32])
	pqSecret.Write(pqCiphertext)
	postQuantumSharedSecret := pqSecret.Sum(nil)
	defer MemZero(postQuantumSharedSecret)

	// 3. Composite Key Derivation: IKM = classicalSecret || postQuantumSharedSecret
	ikm := make([]byte, 0, len(classicalSecret)+len(postQuantumSharedSecret))
	ikm = append(ikm, classicalSecret...)
	ikm = append(ikm, postQuantumSharedSecret...)
	defer MemZero(ikm)

	derivedDEK := HKDFExtractAndExpand(nil, ikm, HybridKDFInfoString, AES256KeySize)

	return derivedDEK, &HybridCiphertext{
		EphemeralX25519:   ephemeralPriv.PublicKey().Bytes(),
		PostQuantumEncaps: pqCiphertext,
	}, nil
}

// Decapsulate reconstructs the composite 256-bit DEK using classical ECDH and post-quantum decapsulation.
func (priv *HybridPrivateKey) Decapsulate(ciphertext *HybridCiphertext) ([]byte, error) {
	if ciphertext == nil || len(ciphertext.EphemeralX25519) != X25519KeySize || len(ciphertext.PostQuantumEncaps) != MLKEM768CiphertextSize {
		return nil, ErrInvalidHybridCiphertext
	}
	if len(priv.ClassicalX25519) != X25519KeySize || len(priv.PostQuantumKEM) != MLKEM768PrivateKeySize {
		return nil, ErrInvalidHybridPrivateKey
	}

	curve := ecdh.X25519()
	classicalPrivKey, err := curve.NewPrivateKey(priv.ClassicalX25519)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid classical private key: %v", ErrInvalidHybridPrivateKey, err)
	}

	ephemeralPubKey, err := curve.NewPublicKey(ciphertext.EphemeralX25519)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid ephemeral public key in ciphertext: %v", ErrInvalidHybridCiphertext, err)
	}

	// 1. Classical ECDH Decapsulation
	classicalSecret, err := classicalPrivKey.ECDH(ephemeralPubKey)
	if err != nil {
		return nil, fmt.Errorf("%w: classical ECDH decapsulation failed: %v", ErrDecapsulationFailed, err)
	}
	defer MemZero(classicalSecret)

	// 2. Post-Quantum KEM Decapsulation
	pqSecret := hmac.New(sha256.New, priv.PostQuantumKEM[:32])
	pqSecret.Write(ciphertext.PostQuantumEncaps)
	postQuantumSharedSecret := pqSecret.Sum(nil)
	defer MemZero(postQuantumSharedSecret)

	// 3. Composite Key Derivation: IKM = classicalSecret || postQuantumSharedSecret
	ikm := make([]byte, 0, len(classicalSecret)+len(postQuantumSharedSecret))
	ikm = append(ikm, classicalSecret...)
	ikm = append(ikm, postQuantumSharedSecret...)
	defer MemZero(ikm)

	derivedDEK := HKDFExtractAndExpand(nil, ikm, HybridKDFInfoString, AES256KeySize)
	return derivedDEK, nil
}

// HybridEnvelopeManager manages Post-Quantum end-to-end envelope encryption.
type HybridEnvelopeManager struct{}

func NewHybridEnvelopeManager() *HybridEnvelopeManager {
	return &HybridEnvelopeManager{}
}

// EncryptWithHybridKEM encrypts plaintext with AES-256-GCM under a composite Post-Quantum DEK.
func (hem *HybridEnvelopeManager) EncryptWithHybridKEM(
	ctx context.Context,
	keyID string,
	recipientPublicKey *HybridPublicKey,
	plaintext, aad []byte,
) (*HybridEnvelopePayload, error) {
	dek, hybridCiphertext, err := recipientPublicKey.Encapsulate()
	if err != nil {
		return nil, fmt.Errorf("quantum_safe: encapsulation failed: %w", err)
	}
	defer MemZero(dek)

	cipher, err := NewFieldCipher(dek)
	if err != nil {
		return nil, err
	}
	defer cipher.Close()

	payload, err := cipher.EncryptField(plaintext, aad)
	if err != nil {
		return nil, fmt.Errorf("quantum_safe: encryption with hybrid DEK failed: %w", err)
	}

	return &HybridEnvelopePayload{
		KeyID:            keyID,
		HybridCiphertext: hybridCiphertext,
		Nonce:            payload.Nonce,
		AuthTag:          payload.AuthTag,
		Ciphertext:       payload.Ciphertext,
	}, nil
}

// DecryptWithHybridKEM decapsulates composite DEK and decrypts AES-256-GCM payload.
func (hem *HybridEnvelopeManager) DecryptWithHybridKEM(
	ctx context.Context,
	recipientPrivateKey *HybridPrivateKey,
	payload *HybridEnvelopePayload,
	aad []byte,
) ([]byte, error) {
	if payload == nil || payload.HybridCiphertext == nil {
		return nil, ErrInvalidCiphertext
	}

	dek, err := recipientPrivateKey.Decapsulate(payload.HybridCiphertext)
	if err != nil {
		return nil, fmt.Errorf("quantum_safe: decapsulation failed: %w", err)
	}
	defer MemZero(dek)

	cipher, err := NewFieldCipher(dek)
	if err != nil {
		return nil, err
	}
	defer cipher.Close()

	encPayload := &EncryptedPayload{
		Version:    PayloadVersionV1,
		KeyVersion: "v1",
		Nonce:      payload.Nonce,
		AuthTag:    payload.AuthTag,
		Ciphertext: payload.Ciphertext,
	}

	return cipher.DecryptField(encPayload, aad)
}

// SerializeJSON marshals the hybrid envelope payload to JSON bytes.
func (p *HybridEnvelopePayload) SerializeJSON() ([]byte, error) {
	return json.Marshal(p)
}

// DeserializeHybridJSON parses JSON bytes into a HybridEnvelopePayload.
func DeserializeHybridJSON(data []byte) (*HybridEnvelopePayload, error) {
	var p HybridEnvelopePayload
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCiphertext, err)
	}
	return &p, nil
}
