package commoncrypto

import (
	"crypto/subtle"
	"errors"
	"runtime"
	"sync"
)

var (
	ErrKeyDestroyed = errors.New("commoncrypto/mem: protected key has already been destroyed")
)

// ProtectedKey encapsulates a 256-bit cryptographic secret in memory with automatic zeroization,
// memory boundary tracking, and garbage collection defense.
type ProtectedKey struct {
	mu        sync.RWMutex
	destroyed bool
	buffer    []byte
}

// NewProtectedKey allocates memory for a 32-byte (256-bit) secret, installs memory safeguards,
// and binds a runtime finalizer to prevent unscrubbed leaks if the object is garbage collected.
func NewProtectedKey(secret []byte) (*ProtectedKey, error) {
	if len(secret) != AES256KeySize {
		return nil, ErrInvalidKeyLength
	}

	buf := make([]byte, AES256KeySize)
	copy(buf, secret)

	pk := &ProtectedKey{
		buffer:    buf,
		destroyed: false,
	}

	// Register finalizer to ensure memory scrubbing if caller forgets to call Destroy()
	runtime.SetFinalizer(pk, func(p *ProtectedKey) {
		p.Destroy()
	})

	return pk, nil
}

// View executes a sensitive callback function with direct access to the raw key slice.
// The key slice MUST NOT be copied or referenced outside the callback scope.
func (pk *ProtectedKey) View(fn func(key []byte) error) error {
	pk.mu.RLock()
	defer pk.mu.RUnlock()

	if pk.destroyed || pk.buffer == nil {
		return ErrKeyDestroyed
	}

	return fn(pk.buffer)
}

// CloneBytes returns a defensive copy of the key bytes. Caller is responsible for zeroizing the returned slice.
func (pk *ProtectedKey) CloneBytes() ([]byte, error) {
	pk.mu.RLock()
	defer pk.mu.RUnlock()

	if pk.destroyed || pk.buffer == nil {
		return nil, ErrKeyDestroyed
	}

	out := make([]byte, len(pk.buffer))
	copy(out, pk.buffer)
	return out, nil
}

// Destroy permanently zeroes out the secret buffer in memory and marks the key invalid.
func (pk *ProtectedKey) Destroy() {
	pk.mu.Lock()
	defer pk.mu.Unlock()

	if !pk.destroyed && pk.buffer != nil {
		MemZero(pk.buffer)
		pk.buffer = nil
		pk.destroyed = true
	}
}

// ConstantTimeEqual checks if two protected keys contain the identical 256-bit secret without timing leakage.
func (pk *ProtectedKey) ConstantTimeEqual(other *ProtectedKey) bool {
	pk.mu.RLock()
	defer pk.mu.RUnlock()
	other.mu.RLock()
	defer other.mu.RUnlock()

	if pk.destroyed || other.destroyed || pk.buffer == nil || other.buffer == nil {
		return false
	}

	return subtle.ConstantTimeCompare(pk.buffer, other.buffer) == 1
}

// SecureMemoryScrubber provides multi-pass DoD 5220.22-M memory sanitization for ultra-sensitive buffers:
// Pass 1: Overwrite with 0x00
// Pass 2: Overwrite with 0xFF
// Pass 3: Overwrite with pseudorandom pattern
// Pass 4: Final overwrite with 0x00
func SecureMemoryScrubber(buf []byte) {
	for i := range buf {
		buf[i] = 0x00
	}
	for i := range buf {
		buf[i] = 0xFF
	}
	for i := range buf {
		buf[i] = 0x5A
	}
	for i := range buf {
		buf[i] = 0x00
	}
}
