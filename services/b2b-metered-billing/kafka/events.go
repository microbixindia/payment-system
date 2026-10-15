package kafka

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

var (
	ErrInvalidEventPayload = errors.New("billing/events: invalid consumption event payload")
	ErrNegativeBytes       = errors.New("billing/events: bytes transferred cannot be negative")
)

// DatasetConsumptionEvent represents a telemetry event emitted when an enterprise client
// streams, queries, or downloads dataset partitions.
type DatasetConsumptionEvent struct {
	EventID             uuid.UUID `json:"event_id"`
	TenantID            uuid.UUID `json:"tenant_id"`
	DatasetID           string    `json:"dataset_id"`
	BytesTransferred    int64     `json:"bytes_transferred"`
	QueryCount          int64     `json:"query_count"`
	ComputeMilliseconds int64     `json:"compute_ms"`
	Timestamp           time.Time `json:"timestamp"`
	IdempotencyKey      string    `json:"idempotency_key"`
	Signature           string    `json:"signature"`
}

// Validate ensures telemetry integrity.
func (e *DatasetConsumptionEvent) Validate() error {
	if e.TenantID == uuid.Nil {
		return fmt.Errorf("%w: tenant_id is required", ErrInvalidEventPayload)
	}
	if e.DatasetID == "" {
		return fmt.Errorf("%w: dataset_id is required", ErrInvalidEventPayload)
	}
	if e.BytesTransferred < 0 {
		return ErrNegativeBytes
	}
	if e.QueryCount < 0 {
		return fmt.Errorf("%w: query count cannot be negative", ErrInvalidEventPayload)
	}
	if e.ComputeMilliseconds < 0 {
		return fmt.Errorf("%w: compute ms cannot be negative", ErrInvalidEventPayload)
	}
	return nil
}

// Serialize encodes event to JSON bytes for Kafka transport.
func (e *DatasetConsumptionEvent) Serialize() ([]byte, error) {
	return json.Marshal(e)
}

// DeserializeEvent parses JSON bytes into a DatasetConsumptionEvent.
func DeserializeEvent(data []byte) (*DatasetConsumptionEvent, error) {
	var ev DatasetConsumptionEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidEventPayload, err)
	}
	if err := ev.Validate(); err != nil {
		return nil, err
	}
	return &ev, nil
}
