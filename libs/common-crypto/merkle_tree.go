package commoncrypto

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

var (
	ErrEmptyMerkleTree = errors.New("commoncrypto/merkle: cannot construct Merkle tree with zero leaf entries")
	ErrProofInvalid    = errors.New("commoncrypto/merkle: cryptographic audit proof verification failed")
)

// MerkleNode represents a single cryptographic node in the immutable audit tree.
type MerkleNode struct {
	Hash   []byte
	Left   *MerkleNode
	Right  *MerkleNode
	Parent *MerkleNode
}

// MerkleTree guarantees tamper-evident mathematical immutability for batches
// of financial journal entries, ledger postings, and SWIFT messages.
type MerkleTree struct {
	Root   *MerkleNode
	Leaves []*MerkleNode
}

// AuditPathStep defines a step along the verification path to the Merkle root.
type AuditPathStep struct {
	Hash    string `json:"hash"`
	IsRight bool   `json:"is_right"`
}

// MerkleAuditProof provides a compact, cryptographically verifiable proof of inclusion
// for any transaction in a block without requiring the full transaction ledger.
type MerkleAuditProof struct {
	LeafHash   string          `json:"leaf_hash"`
	RootHash   string          `json:"root_hash"`
	Path       []AuditPathStep `json:"path"`
	LeafIndex  int             `json:"leaf_index"`
	TotalNodes int             `json:"total_nodes"`
}

// HashLeaf computes the SHA-256 digest of raw leaf data with domain separation (prefix 0x00).
func HashLeaf(data []byte) []byte {
	h := sha256.New()
	h.Write([]byte{0x00}) // Domain separation prefix preventing second-preimage attacks (RFC 6962)
	h.Write(data)
	return h.Sum(nil)
}

// HashInterior computes the SHA-256 digest of two child hashes with domain separation (prefix 0x01).
func HashInterior(left, right []byte) []byte {
	h := sha256.New()
	h.Write([]byte{0x01}) // Domain separation prefix for internal tree nodes
	h.Write(left)
	h.Write(right)
	return h.Sum(nil)
}

// NewMerkleTree builds a balanced cryptographic Merkle tree over an array of data slices.
func NewMerkleTree(dataBlocks [][]byte) (*MerkleTree, error) {
	if len(dataBlocks) == 0 {
		return nil, ErrEmptyMerkleTree
	}

	leaves := make([]*MerkleNode, len(dataBlocks))
	for i, block := range dataBlocks {
		leaves[i] = &MerkleNode{
			Hash: HashLeaf(block),
		}
	}

	// Duplicate the last node if leaf count is odd to maintain binary balance
	currentLevel := leaves
	if len(currentLevel)%2 != 0 && len(currentLevel) > 1 {
		currentLevel = append(currentLevel, &MerkleNode{
			Hash: currentLevel[len(currentLevel)-1].Hash,
		})
	}

	for len(currentLevel) > 1 {
		var nextLevel []*MerkleNode
		for i := 0; i < len(currentLevel); i += 2 {
			left := currentLevel[i]
			var right *MerkleNode
			if i+1 < len(currentLevel) {
				right = currentLevel[i+1]
			} else {
				// Odd node promoted with duplicated hash
				right = &MerkleNode{Hash: left.Hash}
			}

			parent := &MerkleNode{
				Hash:  HashInterior(left.Hash, right.Hash),
				Left:  left,
				Right: right,
			}
			left.Parent = parent
			right.Parent = parent

			nextLevel = append(nextLevel, parent)
		}
		currentLevel = nextLevel
	}

	return &MerkleTree{
		Root:   currentLevel[0],
		Leaves: leaves,
	}, nil
}

// RootHex returns the hex-encoded string of the root Merkle hash.
func (t *MerkleTree) RootHex() string {
	if t.Root == nil {
		return ""
	}
	return hex.EncodeToString(t.Root.Hash)
}

// GenerateAuditProof constructs a compact proof of inclusion for a leaf at index.
func (t *MerkleTree) GenerateAuditProof(leafIndex int) (*MerkleAuditProof, error) {
	if leafIndex < 0 || leafIndex >= len(t.Leaves) {
		return nil, fmt.Errorf("commoncrypto/merkle: index %d out of bounds (tree size %d)", leafIndex, len(t.Leaves))
	}

	leafNode := t.Leaves[leafIndex]
	var path []AuditPathStep
	curr := leafNode

	for curr.Parent != nil {
		parent := curr.Parent
		if parent.Left == curr {
			// Sibling is on the right
			path = append(path, AuditPathStep{
				Hash:    hex.EncodeToString(parent.Right.Hash),
				IsRight: true,
			})
		} else {
			// Sibling is on the left
			path = append(path, AuditPathStep{
				Hash:    hex.EncodeToString(parent.Left.Hash),
				IsRight: false,
			})
		}
		curr = parent
	}

	return &MerkleAuditProof{
		LeafHash:   hex.EncodeToString(leafNode.Hash),
		RootHash:   t.RootHex(),
		Path:       path,
		LeafIndex:  leafIndex,
		TotalNodes: len(t.Leaves),
	}, nil
}

// VerifyProof verifies that a given data block belongs to a Merkle root using an audit proof.
func VerifyProof(proof *MerkleAuditProof, rawData []byte) (bool, error) {
	if proof == nil {
		return false, ErrProofInvalid
	}

	currentHash := HashLeaf(rawData)
	expectedLeafHex := hex.EncodeToString(currentHash)
	if expectedLeafHex != proof.LeafHash {
		return false, fmt.Errorf("%w: leaf hash mismatch (computed %s != proof %s)",
			ErrProofInvalid, expectedLeafHex, proof.LeafHash)
	}

	for _, step := range proof.Path {
		siblingHash, err := hex.DecodeString(step.Hash)
		if err != nil {
			return false, fmt.Errorf("%w: invalid sibling hash in proof: %v", ErrProofInvalid, err)
		}

		if step.IsRight {
			// Sibling is to the right: HashInterior(current, sibling)
			currentHash = HashInterior(currentHash, siblingHash)
		} else {
			// Sibling is to the left: HashInterior(sibling, current)
			currentHash = HashInterior(siblingHash, currentHash)
		}
	}

	computedRootHex := hex.EncodeToString(currentHash)
	if computedRootHex != proof.RootHash {
		return false, fmt.Errorf("%w: computed root %s != claimed root %s",
			ErrProofInvalid, computedRootHex, proof.RootHash)
	}

	return true, nil
}
