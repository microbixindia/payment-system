package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	"datavendor-payment-core/services/settlement-gateway/processor"
)

func main() {
	log.Println("[INFO] Initializing DataVendor Settlement Gateway (SWIFT ISO 20022 / FedNow / ACH)...")

	pipeline := processor.NewSettlementPipeline()

	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":   "HEALTHY",
			"service":  "settlement-gateway",
			"protocols": "SWIFT-ISO20022,FEDNOW,ACH-NACHA",
		})
	})

	mux.HandleFunc("/api/v1/settlement/execute", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}

		var inst processor.SettlementInstruction
		if err := json.NewDecoder(r.Body).Decode(&inst); err != nil {
			http.Error(w, "Bad Request: "+err.Error(), http.StatusBadRequest)
			return
		}

		res, err := pipeline.ExecuteClearing(r.Context(), inst)
		if err != nil {
			http.Error(w, "Settlement Failed: "+err.Error(), http.StatusBadGateway)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"instruction_id": res.InstructionID,
			"selected_rail":  res.SelectedRail,
			"payload_format": res.PayloadFormat,
			"raw_message":    string(res.MessageBytes),
		})
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8083"
	}

	server := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	log.Printf("[INFO] Settlement Gateway listening on port %s", port)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("[FATAL] Settlement Gateway crashed: %v", err)
	}
}
