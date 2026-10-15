package aggregator

import (
	"sync"
	"time"

	"datavendor-payment-core/services/b2b-metered-billing/kafka"
	"github.com/google/uuid"
)

// TenantDatasetUsage aggregates consumption metrics for a specific tenant and dataset.
type TenantDatasetUsage struct {
	TenantID            uuid.UUID `json:"tenant_id"`
	DatasetID           string    `json:"dataset_id"`
	TotalBytes          int64     `json:"total_bytes"`
	TotalQueries        int64     `json:"total_queries"`
	TotalComputeMs      int64     `json:"total_compute_ms"`
	EventCount          int64     `json:"event_count"`
	WindowStart         time.Time `json:"window_start"`
	WindowEnd           time.Time `json:"window_end"`
}

// UsageTracker provides lock-free/thread-safe sliding window metric aggregation.
type UsageTracker struct {
	mu    sync.RWMutex
	usage map[string]*TenantDatasetUsage // key: tenantID:datasetID
}

// NewUsageTracker initializes usage tracker state.
func NewUsageTracker() *UsageTracker {
	return &UsageTracker{
		usage: make(map[string]*TenantDatasetUsage),
	}
}

func compositeKey(tenantID uuid.UUID, datasetID string) string {
	return tenantID.String() + ":" + datasetID
}

// IngestBatch processes incoming event batches and updates aggregates.
func (u *UsageTracker) IngestBatch(events []*kafka.DatasetConsumptionEvent) {
	u.mu.Lock()
	defer u.mu.Unlock()

	for _, ev := range events {
		key := compositeKey(ev.TenantID, ev.DatasetID)
		agg, exists := u.usage[key]
		if !exists {
			agg = &TenantDatasetUsage{
				TenantID:    ev.TenantID,
				DatasetID:   ev.DatasetID,
				WindowStart: ev.Timestamp,
			}
			u.usage[key] = agg
		}

		agg.TotalBytes += ev.BytesTransferred
		agg.TotalQueries += ev.QueryCount
		agg.TotalComputeMs += ev.ComputeMilliseconds
		agg.EventCount++
		agg.WindowEnd = ev.Timestamp
	}
}

// GetUsage retrieves current usage metrics for a given tenant and dataset.
func (u *UsageTracker) GetUsage(tenantID uuid.UUID, datasetID string) *TenantDatasetUsage {
	u.mu.RLock()
	defer u.mu.RUnlock()

	key := compositeKey(tenantID, datasetID)
	agg, exists := u.usage[key]
	if !exists {
		return &TenantDatasetUsage{
			TenantID:  tenantID,
			DatasetID: datasetID,
		}
	}

	copied := *agg
	return &copied
}

// GetTotalBytesForTenant sums bytes transferred across all datasets for a tenant.
func (u *UsageTracker) GetTotalBytesForTenant(tenantID uuid.UUID) int64 {
	u.mu.RLock()
	defer u.mu.RUnlock()

	var total int64
	for _, agg := range u.usage {
		if agg.TenantID == tenantID {
			total += agg.TotalBytes
		}
	}
	return total
}
