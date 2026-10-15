package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	"datavendor-payment-core/services/core-ledger-engine/engine"
	"datavendor-payment-core/services/core-ledger-engine/repository"
	"github.com/google/uuid"
)

func main() {
	log.Println("[INFO] Initializing DataVendor Core Ledger Engine...")

	repo := repository.NewInMemLedgerRepository()
	accountingEngine := engine.NewAccountingEngine(repo)

	// Seed root ledger
	ctx := context.Background()
	mainLedger := &repository.Ledger{
		ID:           uuid.New(),
		Name:         "DataVendor Master Operating Ledger",
		BaseCurrency: "USD",
	}
	if err := repo.CreateLedger(ctx, mainLedger); err != nil {
		log.Fatalf("[FATAL] Failed to initialize default ledger: %v", err)
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":  "HEALTHY",
			"service": "core-ledger-engine",
			"ledger":  mainLedger.ID.String(),
		})
	})

	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "READY"})
	})

	mux.HandleFunc("/api/v1/ledger/postings", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}

		var req repository.CreateJournalEntryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Bad Request: "+err.Error(), http.StatusBadRequest)
			return
		}

		entry, err := repo.PostJournalEntry(r.Context(), req)
		if err != nil {
			http.Error(w, "Unprocessable Entity: "+err.Error(), http.StatusUnprocessableEntity)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(entry)
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	server := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	log.Printf("[INFO] Core Ledger Engine listening on port %s", port)
	_ = accountingEngine // keep reference for transfer engine extensions
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("[FATAL] Core Ledger Engine failed: %v", err)
	}
}
