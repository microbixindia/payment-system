package aggregator

import (
	"errors"
	"fmt"
	"sync"

	"github.com/google/uuid"
)

var (
	ErrQuotaExceeded = errors.New("billing/quota: tenant monthly data volume quota has been exhausted")
)

// QuotaStatus indicates current consumption level relative to plan limits.
type QuotaStatus string

const (
	QuotaStatusNormal   QuotaStatus = "NORMAL"
	QuotaStatusWarning  QuotaStatus = "WARNING"  // >= 80%
	QuotaStatusCritical QuotaStatus = "CRITICAL" // >= 95%
	QuotaStatusExceeded QuotaStatus = "EXCEEDED" // >= 100%
)

// TenantQuotaDefinition defines customer contract limits.
type TenantQuotaDefinition struct {
	TenantID          uuid.UUID
	MonthlyByteQuota  int64 // e.g. 50 * 1024 * 1024 * 1024 * 1024 (50 TB)
	AllowBurstOverrun bool
	BurstMultiplier   float64 // e.g. 1.25 allows 25% burst
}

// QuotaEnforcer audits consumption in real time and triggers policy actions.
type QuotaEnforcer struct {
	mu     sync.RWMutex
	quotas map[uuid.UUID]TenantQuotaDefinition
}

// NewQuotaEnforcer instantiates the quota enforcer.
func NewQuotaEnforcer() *QuotaEnforcer {
	return &QuotaEnforcer{
		quotas: make(map[uuid.UUID]TenantQuotaDefinition),
	}
}

// SetQuota registers or updates a tenant's quota configuration.
func (q *QuotaEnforcer) SetQuota(def TenantQuotaDefinition) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.quotas[def.TenantID] = def
}

// CheckQuota evaluates current consumption against tenant limits.
func (q *QuotaEnforcer) CheckQuota(tenantID uuid.UUID, currentBytes int64) (QuotaStatus, error) {
	q.mu.RLock()
	def, exists := q.quotas[tenantID]
	q.mu.RUnlock()

	if !exists || def.MonthlyByteQuota <= 0 {
		return QuotaStatusNormal, nil // Unlimited tier
	}

	ratio := float64(currentBytes) / float64(def.MonthlyByteQuota)

	if ratio >= 1.0 {
		if !def.AllowBurstOverrun {
			return QuotaStatusExceeded, fmt.Errorf("%w: used %d of %d bytes (%.1f%%)",
				ErrQuotaExceeded, currentBytes, def.MonthlyByteQuota, ratio*100)
		}
		maxBurst := float64(def.MonthlyByteQuota) * def.BurstMultiplier
		if float64(currentBytes) >= maxBurst {
			return QuotaStatusExceeded, fmt.Errorf("%w: hard burst limit exceeded", ErrQuotaExceeded)
		}
		return QuotaStatusCritical, nil
	}

	if ratio >= 0.95 {
		return QuotaStatusCritical, nil
	}

	if ratio >= 0.80 {
		return QuotaStatusWarning, nil
	}

	return QuotaStatusNormal, nil
}
