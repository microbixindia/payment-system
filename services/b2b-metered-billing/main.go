package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	"datavendor-payment-core/services/b2b-metered-billing/aggregator"
	"datavendor-payment-core/services/b2b-metered-billing/billing"
	"datavendor-payment-core/services/b2b-metered-billing/kafka"
)

type billingHandler struct {
	tracker   *aggregator.UsageTracker
	enforcer  *aggregator.QuotaEnforcer
	calc      *billing.PricingCalculator
	generator *billing.InvoiceGenerator
}

func (h *billingHandler) HandleEvents(ctx context.Context, events []*kafka.DatasetConsumptionEvent) error {
	h.tracker.IngestBatch(events)
	return nil
}

func main() {
	log.Println("[INFO] Initializing DataVendor B2B Metered Billing Service...")

	tracker := aggregator.NewUsageTracker()
	enforcer := aggregator.NewQuotaEnforcer()
	calc := billing.NewPricingCalculator()
	gen := billing.NewInvoiceGenerator(calc)

	bh := &billingHandler{
		tracker:   tracker,
		enforcer:  enforcer,
		calc:      calc,
		generator: gen,
	}

	consumerCfg := kafka.ConsumerConfig{
		Topic:             "datavendor-dataset-telemetry-v1",
		GroupID:           "billing-telemetry-consumer-prod",
		WorkerPoolSize:    16,
		QueueCapacity:     50000,
		BatchSize:         500,
		BackpressureLimit: 0.85,
	}

	consumer := kafka.NewStreamConsumer(consumerCfg, bh)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	consumer.Start(ctx)
	defer consumer.Stop()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status":   "HEALTHY",
			"service":  "b2b-metered-billing",
			"backpressure_paused": consumer.IsPaused(),
		})
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8082"
	}

	server := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	log.Printf("[INFO] Metered Billing Service listening on port %s", port)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("[FATAL] Metered Billing Service crashed: %v", err)
	}
}
