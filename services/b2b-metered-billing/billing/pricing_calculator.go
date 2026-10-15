package billing

import (
	"github.com/shopspring/decimal"
)

const (
	// Byte conversion constants
	BytesInGigabyte = int64(1024 * 1024 * 1024)
	BytesInTerabyte = int64(1024 * 1024 * 1024 * 1024)
)

// PricingTier represents a volume slab and corresponding price per Gigabyte.
type PricingTier struct {
	UpToBytes   int64           // 0 means unbounded upper tier
	PricePerGB  decimal.Decimal // USD per GB
	Description string
}

// DefaultDatasetPricingTiers defines enterprise volume pricing for DataVendor datasets.
var DefaultDatasetPricingTiers = []PricingTier{
	{
		UpToBytes:   10 * BytesInTerabyte, // 0 - 10 TB
		PricePerGB:  decimal.NewFromFloat(0.1500),
		Description: "Tier 1: 0 - 10 TB ($0.15/GB)",
	},
	{
		UpToBytes:   50 * BytesInTerabyte, // 10 - 50 TB
		PricePerGB:  decimal.NewFromFloat(0.1000),
		Description: "Tier 2: 10 - 50 TB ($0.10/GB)",
	},
	{
		UpToBytes:   250 * BytesInTerabyte, // 50 - 250 TB
		PricePerGB:  decimal.NewFromFloat(0.0700),
		Description: "Tier 3: 50 - 250 TB ($0.07/GB)",
	},
	{
		UpToBytes:   0, // > 250 TB unbounded
		PricePerGB:  decimal.NewFromFloat(0.0400),
		Description: "Tier 4: Enterprise Volume > 250 TB ($0.04/GB)",
	},
}

// TierBreakdownDetail details amount billed in a specific tier slab.
type TierBreakdownDetail struct {
	TierDescription string          `json:"tier_description"`
	BytesBilled     int64           `json:"bytes_billed"`
	GigabytesBilled decimal.Decimal `json:"gigabytes_billed"`
	RatePerGB       decimal.Decimal `json:"rate_per_gb"`
	AmountUSD       decimal.Decimal `json:"amount_usd"`
}

// PricingCalculationResult returns itemized totals across volume, compute, and query dimensions.
type PricingCalculationResult struct {
	TotalBytesTransferred int64                 `json:"total_bytes_transferred"`
	TotalGigabytes        decimal.Decimal       `json:"total_gigabytes"`
	DataTransferCost      decimal.Decimal       `json:"data_transfer_cost"`
	TierBreakdowns        []TierBreakdownDetail `json:"tier_breakdowns"`

	TotalQueries int64           `json:"total_queries"`
	QueryCost    decimal.Decimal `json:"query_cost"`

	TotalComputeMs int64           `json:"total_compute_ms"`
	ComputeCost    decimal.Decimal `json:"compute_cost"`

	SubtotalAmount decimal.Decimal `json:"subtotal_amount"`
}

// PricingCalculator computes exact decimal charges for data consumption.
type PricingCalculator struct {
	tiers           []PricingTier
	queryFee        decimal.Decimal // fee per single query
	computeFeePerMs decimal.Decimal // fee per compute millisecond
}

// NewPricingCalculator initializes the pricing calculator.
func NewPricingCalculator() *PricingCalculator {
	return &PricingCalculator{
		tiers:           DefaultDatasetPricingTiers,
		queryFee:        decimal.NewFromFloat(0.0010),   // $1 per 1,000 queries
		computeFeePerMs: decimal.NewFromFloat(0.00005),  // $0.05 per second of compute
	}
}

// CalculateCharges computes graduated tiered pricing over bytes transferred, plus query and compute charges.
func (p *PricingCalculator) CalculateCharges(bytesTransferred, queries, computeMs int64) *PricingCalculationResult {
	res := &PricingCalculationResult{
		TotalBytesTransferred: bytesTransferred,
		TotalQueries:          queries,
		TotalComputeMs:        computeMs,
		TierBreakdowns:        make([]TierBreakdownDetail, 0),
	}

	totalGB := decimal.NewFromInt(bytesTransferred).Div(decimal.NewFromInt(BytesInGigabyte))
	res.TotalGigabytes = totalGB

	// 1. Calculate graduated tier slabs
	remainingBytes := bytesTransferred
	var previousLimit int64 = 0
	totalDataCost := decimal.Zero

	for _, tier := range p.tiers {
		if remainingBytes <= 0 {
			break
		}

		var bytesInThisTier int64
		if tier.UpToBytes == 0 {
			// Unbounded tier
			bytesInThisTier = remainingBytes
		} else {
			tierCapacity := tier.UpToBytes - previousLimit
			if remainingBytes > tierCapacity {
				bytesInThisTier = tierCapacity
			} else {
				bytesInThisTier = remainingBytes
			}
			previousLimit = tier.UpToBytes
		}

		if bytesInThisTier > 0 {
			gbInTier := decimal.NewFromInt(bytesInThisTier).Div(decimal.NewFromInt(BytesInGigabyte))
			costInTier := gbInTier.Mul(tier.PricePerGB)
			totalDataCost = totalDataCost.Add(costInTier)

			res.TierBreakdowns = append(res.TierBreakdowns, TierBreakdownDetail{
				TierDescription: tier.Description,
				BytesBilled:     bytesInThisTier,
				GigabytesBilled: gbInTier.Round(4),
				RatePerGB:       tier.PricePerGB,
				AmountUSD:       costInTier.Round(4),
			})

			remainingBytes -= bytesInThisTier
		}
	}

	res.DataTransferCost = totalDataCost.Round(4)

	// 2. Query fee calculation
	res.QueryCost = decimal.NewFromInt(queries).Mul(p.queryFee).Round(4)

	// 3. Compute duration fee calculation
	res.ComputeCost = decimal.NewFromInt(computeMs).Mul(p.computeFeePerMs).Round(4)

	// 4. Total subtotal
	res.SubtotalAmount = res.DataTransferCost.Add(res.QueryCost).Add(res.ComputeCost).Round(4)

	return res
}
