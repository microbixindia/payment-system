package router

import (
	"encoding/json"
	"net/http"

	"datavendor-payment-core/services/edge-api-gateway/middleware"
)

// GatewayRouter wires middleware chains and API routes.
type GatewayRouter struct {
	mux           *http.ServeMux
	tokenVal      *middleware.TokenValidator
	rateLimiter   *middleware.TokenBucketRateLimiter
}

// NewGatewayRouter constructs the router with security controls.
func NewGatewayRouter(jwtSecret []byte) *GatewayRouter {
	return &GatewayRouter{
		mux:         http.NewServeMux(),
		tokenVal:    middleware.NewTokenValidator(jwtSecret),
		rateLimiter: middleware.NewTokenBucketRateLimiter(100.0, 50.0), // 100 burst, 50 rps
	}
}

// SetupRoutes registers routes and returns the top-level http.Handler.
func (g *GatewayRouter) SetupRoutes() http.Handler {
	// Liveness & Readiness Probes (Unauthenticated, public)
	g.mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "UP", "service": "edge-api-gateway"})
	})

	g.mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "READY", "dependencies": "OK"})
	})

	// Authenticated API routes with RBAC enforcement
	ledgerPostingHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "ledger posting endpoint ready"})
	})

	settlementHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "settlement clearing endpoint ready"})
	})

	// Wrap handlers with JWT + RBAC
	protectedPosting := g.tokenVal.AuthenticateMiddleware(
		middleware.RequireScope("ledger:write")(ledgerPostingHandler),
	)
	g.mux.Handle("/api/v1/ledger/postings", protectedPosting)

	protectedSettlement := g.tokenVal.AuthenticateMiddleware(
		middleware.RequireScope("settlement:execute")(settlementHandler),
	)
	g.mux.Handle("/api/v1/settlement/execute", protectedSettlement)

	// Wrap root with ZeroTrust Security Headers and Rate Limiter
	var handler http.Handler = g.mux
	handler = g.rateLimiter.RateLimitMiddleware(handler)
	handler = middleware.ZeroTrustSecurityMiddleware(handler)

	return handler
}
