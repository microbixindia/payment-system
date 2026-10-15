package engine

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/shopspring/decimal"
)

var (
	ErrUnsupportedCurrency = errors.New("accounting/fx: target currency pair not found in rate matrix")
	ErrNegativeExchangeRate = errors.New("accounting/fx: exchange rate must be strictly positive")
)

// FXRateEntry represents a currency conversion rate with timestamp and quotation precision.
type FXRateEntry struct {
	BaseCurrency  string          `json:"base_currency"`  // e.g. "USD"
	QuoteCurrency string          `json:"quote_currency"` // e.g. "EUR"
	Rate          decimal.Decimal `json:"rate"`           // 1 Base = Rate Quote
	EffectiveTime time.Time       `json:"effective_time"`
	Source        string          `json:"source"`         // "REUTERS_ECB_FIXING"
}

// FXEngine handles multi-currency transactions, triangulation, and currency revaluations (GAAP ASC 830 / IAS 21).
type FXEngine struct {
	mu          sync.RWMutex
	baseRateMap map[string]FXRateEntry // quote -> rate relative to USD
}

// NewFXEngine initializes the FX engine with standard global G10 currencies.
func NewFXEngine() *FXEngine {
	e := &FXEngine{
		baseRateMap: make(map[string]FXRateEntry),
	}

	// Seed reference rates (USD base = 1.0000)
	now := time.Now().UTC()
	e.baseRateMap["USD"] = FXRateEntry{BaseCurrency: "USD", QuoteCurrency: "USD", Rate: decimal.NewFromInt(1), EffectiveTime: now}
	e.baseRateMap["EUR"] = FXRateEntry{BaseCurrency: "USD", QuoteCurrency: "EUR", Rate: decimal.NewFromFloat(0.9250), EffectiveTime: now}
	e.baseRateMap["GBP"] = FXRateEntry{BaseCurrency: "USD", QuoteCurrency: "GBP", Rate: decimal.NewFromFloat(0.7850), EffectiveTime: now}
	e.baseRateMap["JPY"] = FXRateEntry{BaseCurrency: "USD", QuoteCurrency: "JPY", Rate: decimal.NewFromFloat(154.2000), EffectiveTime: now}
	e.baseRateMap["CHF"] = FXRateEntry{BaseCurrency: "USD", QuoteCurrency: "CHF", Rate: decimal.NewFromFloat(0.8950), EffectiveTime: now}
	e.baseRateMap["CAD"] = FXRateEntry{BaseCurrency: "USD", QuoteCurrency: "CAD", Rate: decimal.NewFromFloat(1.3650), EffectiveTime: now}

	return e
}

// SetRate registers or updates an exchange rate quote against USD.
func (e *FXEngine) SetRate(quoteCurrency string, rate decimal.Decimal, source string) error {
	if rate.LessThanOrEqual(decimal.Zero) {
		return ErrNegativeExchangeRate
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	e.baseRateMap[quoteCurrency] = FXRateEntry{
		BaseCurrency:  "USD",
		QuoteCurrency: quoteCurrency,
		Rate:          rate,
		EffectiveTime: time.Now().UTC(),
		Source:        source,
	}
	return nil
}

// ConvertAmount converts an amount between arbitrary foreign currencies via USD triangulation.
func (e *FXEngine) ConvertAmount(amount decimal.Decimal, fromCcy, toCcy string) (decimal.Decimal, error) {
	if fromCcy == toCcy {
		return amount, nil
	}

	e.mu.RLock()
	fromEntry, fromExists := e.baseRateMap[fromCcy]
	toEntry, toExists := e.baseRateMap[toCcy]
	e.mu.RUnlock()

	if !fromExists {
		return decimal.Zero, fmt.Errorf("%w: %s", ErrUnsupportedCurrency, fromCcy)
	}
	if !toExists {
		return decimal.Zero, fmt.Errorf("%w: %s", ErrUnsupportedCurrency, toCcy)
	}

	// 1. Convert 'fromCcy' to USD: usdAmount = amount / fromRate
	usdAmount := amount.DivRound(fromEntry.Rate, 8)

	// 2. Convert USD to 'toCcy': toAmount = usdAmount * toRate
	finalAmount := usdAmount.Mul(toEntry.Rate).Round(4)

	return finalAmount, nil
}

// CalculateUnrealizedGainLoss calculates balance revaluation adjustments per GAAP ASC 830.
func (e *FXEngine) CalculateUnrealizedGainLoss(
	originalForeignAmount decimal.Decimal,
	foreignCurrency string,
	historicalBookValueUSD decimal.Decimal,
) (decimal.Decimal, error) {
	currentValueUSD, err := e.ConvertAmount(originalForeignAmount, foreignCurrency, "USD")
	if err != nil {
		return decimal.Zero, err
	}

	// Gain/Loss = Current Value in Functional Currency - Historical Book Value
	gainLoss := currentValueUSD.Sub(historicalBookValueUSD).Round(4)
	return gainLoss, nil
}
