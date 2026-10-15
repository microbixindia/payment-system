package middleware

import (
	"net/http"
	"strings"
	"sync"
	"time"
)

type rateBucket struct {
	tokens     float64
	capacity   float64
	fillRate   float64 // tokens per second
	lastRefill time.Time
}

// TokenBucketRateLimiter enforces token-bucket rate limiting across caller IP and Tenant IDs.
type TokenBucketRateLimiter struct {
	mu       sync.Mutex
	buckets  map[string]*rateBucket
	capacity float64
	fillRate float64
}

// NewTokenBucketRateLimiter creates a rate limiter with given capacity and refill rate.
func NewTokenBucketRateLimiter(capacity, fillRate float64) *TokenBucketRateLimiter {
	return &TokenBucketRateLimiter{
		buckets:  make(map[string]*rateBucket),
		capacity: capacity,
		fillRate: fillRate,
	}
}

// Allow checks if a request from the given key can proceed.
func (rl *TokenBucketRateLimiter) Allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	b, exists := rl.buckets[key]
	if !exists {
		b = &rateBucket{
			tokens:     rl.capacity,
			capacity:   rl.capacity,
			fillRate:   rl.fillRate,
			lastRefill: now,
		}
		rl.buckets[key] = b
	}

	// Calculate tokens to add based on elapsed time
	elapsed := now.Sub(b.lastRefill).Seconds()
	b.tokens += elapsed * b.fillRate
	if b.tokens > b.capacity {
		b.tokens = b.capacity
	}
	b.lastRefill = now

	if b.tokens >= 1.0 {
		b.tokens -= 1.0
		return true
	}

	return false
}

// RateLimitMiddleware blocks requests that exceed rate limits.
func (rl *TokenBucketRateLimiter) RateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Key on X-Forwarded-For or RemoteAddr
		clientIP := r.Header.Get("X-Forwarded-For")
		if clientIP == "" {
			clientIP = r.RemoteAddr
		}
		clientIP = strings.Split(clientIP, ",")[0]
		clientIP = strings.TrimSpace(clientIP)

		if !rl.Allow(clientIP) {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "Too Many Requests: Rate limit exceeded", http.StatusTooManyRequests)
			return
		}

		next.ServeHTTP(w, r)
	})
}
