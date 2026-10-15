package commoncrypto

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

var (
	ErrInvalidThreshold = errors.New("shamir: threshold k must be at least 2 and less than or equal to total shares n")
	ErrTooFewShares     = errors.New("shamir: insufficient shares provided to reconstruct master secret")
	ErrDuplicateShare   = errors.New("shamir: duplicate share index detected")
	ErrCorruptedShare   = errors.New("shamir: corrupted share payload")
)

// Share represents a single custodian's piece of a split master secret.
type Share struct {
	Index uint8  `json:"index"` // Evaluation point x in GF(2^8), x in [1, 255]
	Value []byte `json:"value"` // Polynomial value f(x)
}

// ShamirKeySplitter implements Shamir's (k, n) Threshold Secret Sharing over Galois Field GF(256).
// It satisfies PCI-DSS Requirement 12 dual-custody and split-knowledge controls:
// A 256-bit master key is divided among n corporate custodians such that any k custodians
// can reconstruct the root key, while any k-1 custodians possess mathematically zero information.
type ShamirKeySplitter struct{}

func NewShamirKeySplitter() *ShamirKeySplitter {
	return &ShamirKeySplitter{}
}

// GF(2^8) addition and subtraction is XOR.
func gfAdd(a, b uint8) uint8 {
	return a ^ b
}

// GF(2^8) multiplication using log and exp tables with primitive polynomial x^8 + x^4 + x^3 + x^2 + 1 (0x11d).
var gfExp [512]uint8
var gfLog [256]uint8

func init() {
	var x uint8 = 1
	for i := 0; i < 255; i++ {
		gfExp[i] = x
		gfExp[i+255] = x
		gfLog[x] = uint8(i)
		// Multiply by generator 2 in GF(2^8) with reduction modulo 0x11d
		if x&0x80 != 0 {
			x = (x << 1) ^ 0x1d
		} else {
			x <<= 1
		}
	}
	gfExp[510] = gfExp[0]
	gfExp[511] = gfExp[1]
}

func gfMul(a, b uint8) uint8 {
	if a == 0 || b == 0 {
		return 0
	}
	return gfExp[int(gfLog[a])+int(gfLog[b])]
}

func gfDiv(a, b uint8) uint8 {
	if b == 0 {
		panic("shamir: division by zero in GF(2^8)")
	}
	if a == 0 {
		return 0
	}
	return gfExp[(int(gfLog[a])-int(gfLog[b])+255)%255]
}

// Split divides a 256-bit master secret into n shares with a reconstruction threshold of k.
func (s *ShamirKeySplitter) Split(secret []byte, k, n int) ([]Share, error) {
	if k < 2 || k > n || n > 255 {
		return nil, ErrInvalidThreshold
	}
	if len(secret) == 0 {
		return nil, ErrEmptyPlaintext
	}

	secretLen := len(secret)
	shares := make([]Share, n)
	for i := 0; i < n; i++ {
		shares[i] = Share{
			Index: uint8(i + 1), // x must be in [1, 255]
			Value: make([]byte, secretLen),
		}
	}

	// For each byte of the secret, generate a random polynomial of degree k-1:
	// P(x) = secret_byte + a_1*x + a_2*x^2 + ... + a_{k-1}*x^{k-1}
	coeff := make([]byte, k-1)

	for byteIdx := 0; byteIdx < secretLen; byteIdx++ {
		if _, err := io.ReadFull(rand.Reader, coeff); err != nil {
			return nil, fmt.Errorf("%w: failed generating polynomial coefficients: %v", ErrEntropyFailure, err)
		}

		secretByte := secret[byteIdx]

		for shareIdx := 0; shareIdx < n; shareIdx++ {
			x := shares[shareIdx].Index
			y := secretByte // P(0) = secret

			// Evaluate polynomial at x using Horner's method or direct powers
			var xPower uint8 = 1
			for cIdx := 0; cIdx < k-1; cIdx++ {
				xPower = gfMul(xPower, x)
				term := gfMul(coeff[cIdx], xPower)
				y = gfAdd(y, term)
			}

			shares[shareIdx].Value[byteIdx] = y
		}
	}

	// Zeroize coefficient buffer
	MemZero(coeff)

	return shares, nil
}

// Combine reconstructs the master secret from any k valid shares using Lagrange Polynomial Interpolation.
func (s *ShamirKeySplitter) Combine(shares []Share, k int) ([]byte, error) {
	if len(shares) < k {
		return nil, fmt.Errorf("%w: received %d shares, require %d", ErrTooFewShares, len(shares), k)
	}

	// Deduplicate and validate indices
	seen := make(map[uint8]bool)
	subset := shares[:k]
	secretLen := len(subset[0].Value)

	for _, sh := range subset {
		if sh.Index == 0 || len(sh.Value) != secretLen {
			return nil, ErrCorruptedShare
		}
		if seen[sh.Index] {
			return nil, ErrDuplicateShare
		}
		seen[sh.Index] = true
	}

	reconstructed := make([]byte, secretLen)

	// Lagrange Interpolation at x = 0:
	// Secret = SUM_{i=0}^{k-1} ( y_i * L_i(0) )
	// where L_i(0) = PROD_{j != i} ( (0 - x_j) / (x_i - x_j) ) = PROD ( x_j / (x_j ^ x_i) )
	for byteIdx := 0; byteIdx < secretLen; byteIdx++ {
		var secretByte uint8 = 0

		for i := 0; i < k; i++ {
			xi := subset[i].Index
			yi := subset[i].Value[byteIdx]

			var li uint8 = 1
			for j := 0; j < k; j++ {
				if i == j {
					continue
				}
				xj := subset[j].Index
				// in GF(2^8), subtraction is addition (XOR)
				denom := gfAdd(xj, xi)
				factor := gfDiv(xj, denom)
				li = gfMul(li, factor)
			}

			term := gfMul(yi, li)
			secretByte = gfAdd(secretByte, term)
		}

		reconstructed[byteIdx] = secretByte
	}

	return reconstructed, nil
}

// SerializeShare encodes a share to a safe human-readable hex string: <hex(index)>$<hex(value)>.
func (s *Share) SerializeShare() string {
	return fmt.Sprintf("%02x$%s", s.Index, hex.EncodeToString(s.Value))
}

// DeserializeShare parses a serialized share string.
func DeserializeShare(encoded string) (*Share, error) {
	var idxHex, valHex string
	n, err := fmt.Sscanf(encoded, "%2s$%s", &idxHex, &valHex)
	if err != nil || n != 2 {
		return nil, ErrCorruptedShare
	}

	idxBytes, err := hex.DecodeString(idxHex)
	if err != nil || len(idxBytes) != 1 {
		return nil, ErrCorruptedShare
	}

	valBytes, err := hex.DecodeString(valHex)
	if err != nil {
		return nil, ErrCorruptedShare
	}

	return &Share{
		Index: idxBytes[0],
		Value: valBytes,
	}, nil
}

// VerifyReconstruction verifies that reconstructed secret matches expected SHA-256 fingerprint in constant time.
func VerifySecretFingerprint(secret []byte, expectedHex string) bool {
	h := sha256.New()
	h.Write(secret)
	digest := h.Sum(nil)
	computedHex := hex.EncodeToString(digest)
	return subtle.ConstantTimeCompare([]byte(computedHex), []byte(expectedHex)) == 1
}
