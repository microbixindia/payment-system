package main

import (
	"crypto/rand"
	"log"
	"net/http"
	"os"
	"time"

	"datavendor-payment-core/libs/common-crypto"
	"datavendor-payment-core/services/edge-api-gateway/router"
)

func main() {
	log.Println("[INFO] Starting DataVendor Edge API Gateway...")

	jwtSecret := make([]byte, 32)
	if _, err := rand.Read(jwtSecret); err != nil {
		log.Fatalf("[FATAL] Failed to initialize cryptographic entropy: %v", err)
	}

	gatewayRouter := router.NewGatewayRouter(jwtSecret)
	handler := gatewayRouter.SetupRoutes()

	// Initialize hardened TLS 1.3 configuration
	tlsConf, err := commoncrypto.BuildHardenedTLSConfig(commoncrypto.EnterpriseTLSConfig{
		ServerName: "gateway.datavendor.internal",
	})
	if err != nil {
		log.Fatalf("[FATAL] Failed to configure enterprise TLS 1.3: %v", err)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	server := &http.Server{
		Addr:         ":" + port,
		Handler:      handler,
		TLSConfig:    tlsConf,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	log.Printf("[INFO] Edge API Gateway listening on port %s (Zero-Trust mTLS / TLS 1.3 enabled)", port)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("[FATAL] Server terminated unexpectedly: %v", err)
	}
}
