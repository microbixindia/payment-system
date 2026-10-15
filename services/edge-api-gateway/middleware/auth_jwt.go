package middleware

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

type contextKey string

const (
	UserContextKey   contextKey = "dv_auth_user"
	ScopesContextKey contextKey = "dv_auth_scopes"
)

var (
	ErrMalformedToken = errors.New("auth: malformed Bearer token")
	ErrTokenExpired   = errors.New("auth: token timestamp expired")
	ErrInvalidSig     = errors.New("auth: invalid cryptographic signature")
)

// JWTClaims represents standard fintech gateway authentication claims.
type JWTClaims struct {
	Sub       uuid.UUID `json:"sub"`
	TenantID  uuid.UUID `json:"tenant_id"`
	Roles     []string  `json:"roles"`
	Scopes    []string  `json:"scopes"`
	ExpiresAt int64     `json:"exp"`
	IssuedAt  int64     `json:"iat"`
}

// TokenValidator verifies HS256 / RS256 authorization tokens.
type TokenValidator struct {
	secretKey []byte
}

// NewTokenValidator initializes token validator with shared secret / HMAC key.
func NewTokenValidator(secretKey []byte) *TokenValidator {
	return &TokenValidator{secretKey: secretKey}
}

// ValidateToken parses and verifies signature and expiration.
func (v *TokenValidator) ValidateToken(tokenString string) (*JWTClaims, error) {
	parts := strings.Split(tokenString, ".")
	if len(parts) != 3 {
		return nil, ErrMalformedToken
	}

	// Verify HMAC-SHA256 signature
	dataToSign := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, v.secretKey)
	mac.Write([]byte(dataToSign))
	expectedSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(parts[2]), []byte(expectedSig)) {
		return nil, ErrInvalidSig
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrMalformedToken
	}

	var claims JWTClaims
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return nil, ErrMalformedToken
	}

	if claims.ExpiresAt < time.Now().Unix() {
		return nil, ErrTokenExpired
	}

	return &claims, nil
}

// AuthenticateMiddleware extracts and validates Bearer token from Authorization header.
func (v *TokenValidator) AuthenticateMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
			http.Error(w, "Unauthorized: Missing or invalid Authorization header", http.StatusUnauthorized)
			return
		}

		tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
		claims, err := v.ValidateToken(tokenStr)
		if err != nil {
			http.Error(w, "Unauthorized: "+err.Error(), http.StatusUnauthorized)
			return
		}

		ctx := context.WithValue(r.Context(), UserContextKey, claims)
		ctx = context.WithValue(ctx, ScopesContextKey, claims.Scopes)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
