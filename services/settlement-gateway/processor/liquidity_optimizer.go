package processor

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

var (
	ErrZeroNettingObligations = errors.New("liquidity: no valid obligations provided for netting cycle")
	ErrUnbalancedMultilateral = errors.New("liquidity: multilateral netting invariant violated (sum of net positions != 0)")
	ErrCurrencyMismatch       = errors.New("liquidity: obligations must be denominated in matching settlement currency")
)

// GrossObligation represents an atomic payment commitment from Debtor to Creditor.
type GrossObligation struct {
	ID        uuid.UUID       `json:"id"`
	DebtorBIC string          `json:"debtor_bic"`
	CreditorBIC string        `json:"creditor_bic"`
	Amount    decimal.Decimal `json:"amount"`
	Currency  string          `json:"currency"`
	Reference string          `json:"reference"`
	CreatedAt time.Time       `json:"created_at"`
}

// NetSettlementTransfer represents a single net settlement instruction executed after netting.
type NetSettlementTransfer struct {
	DebtorBIC   string          `json:"debtor_bic"`
	CreditorBIC string          `json:"creditor_bic"`
	Amount      decimal.Decimal `json:"amount"`
	Currency    string          `json:"currency"`
	Savings     decimal.Decimal `json:"savings"`
}

// MultilateralParticipantPosition tracks net debtor or creditor standing in a clearing batch.
type MultilateralParticipantPosition struct {
	BIC             string          `json:"bic"`
	GrossPayables   decimal.Decimal `json:"gross_payables"`   // Total outgoing obligations
	GrossReceivables decimal.Decimal `json:"gross_receivables"` // Total incoming obligations
	NetPosition     decimal.Decimal `json:"net_position"`     // Receivables - Payables
	IsDebtor        bool            `json:"is_debtor"`        // True if NetPosition < 0
}

// NettingOptimizationReport details the efficiency gains from netting and cycle cancellation.
type NettingOptimizationReport struct {
	Currency                string                  `json:"currency"`
	GrossVolume             decimal.Decimal         `json:"gross_volume"`
	GrossTransactionCount   int                     `json:"gross_tx_count"`
	NetVolume               decimal.Decimal         `json:"net_volume"`
	NetTransactionCount     int                     `json:"net_tx_count"`
	LiquiditySavingsAmount  decimal.Decimal         `json:"liquidity_savings_amount"`
	LiquiditySavingsRatio   decimal.Decimal         `json:"liquidity_savings_ratio"` // (Gross - Net) / Gross
	CancelledCycleVolume    decimal.Decimal         `json:"cancelled_cycle_volume"`
	NetTransfers            []NetSettlementTransfer `json:"net_transfers"`
	ParticipantPositions    map[string]*MultilateralParticipantPosition `json:"participant_positions"`
}

// LiquidityNettingEngine performs bilateral, multilateral, and cycle-canceling netting.
type LiquidityNettingEngine struct {
	mu sync.RWMutex
}

// NewLiquidityNettingEngine instantiates an optimization engine.
func NewLiquidityNettingEngine() *LiquidityNettingEngine {
	return &LiquidityNettingEngine{}
}

// ExecuteBilateralNetting nets obligations directly between counterparties per currency pair.
func (eng *LiquidityNettingEngine) ExecuteBilateralNetting(
	ctx context.Context,
	currency string,
	obligations []GrossObligation,
) (*NettingOptimizationReport, error) {
	eng.mu.Lock()
	defer eng.mu.Unlock()

	if len(obligations) == 0 {
		return nil, ErrZeroNettingObligations
	}

	grossVolume := decimal.Zero
	pairMatrix := make(map[string]map[string]decimal.Decimal)
	participants := make(map[string]*MultilateralParticipantPosition)

	// Ingestion and pair matrix aggregation
	for _, obl := range obligations {
		if obl.Currency != currency {
			return nil, fmt.Errorf("%w: expected %s, got %s", ErrCurrencyMismatch, currency, obl.Currency)
		}
		if obl.Amount.LessThanOrEqual(decimal.Zero) {
			continue
		}

		grossVolume = grossVolume.Add(obl.Amount)

		// Initialize participant positions
		if _, ok := participants[obl.DebtorBIC]; !ok {
			participants[obl.DebtorBIC] = &MultilateralParticipantPosition{
				BIC: obl.DebtorBIC, GrossPayables: decimal.Zero, GrossReceivables: decimal.Zero,
			}
		}
		if _, ok := participants[obl.CreditorBIC]; !ok {
			participants[obl.CreditorBIC] = &MultilateralParticipantPosition{
				BIC: obl.CreditorBIC, GrossPayables: decimal.Zero, GrossReceivables: decimal.Zero,
			}
		}

		participants[obl.DebtorBIC].GrossPayables = participants[obl.DebtorBIC].GrossPayables.Add(obl.Amount)
		participants[obl.CreditorBIC].GrossReceivables = participants[obl.CreditorBIC].GrossReceivables.Add(obl.Amount)

		// Matrix entry [Debtor][Creditor]
		if _, ok := pairMatrix[obl.DebtorBIC]; !ok {
			pairMatrix[obl.DebtorBIC] = make(map[string]decimal.Decimal)
		}
		pairMatrix[obl.DebtorBIC][obl.CreditorBIC] = pairMatrix[obl.DebtorBIC][obl.CreditorBIC].Add(obl.Amount)
	}

	// Calculate net positions
	for _, p := range participants {
		p.NetPosition = p.GrossReceivables.Sub(p.GrossPayables)
		p.IsDebtor = p.NetPosition.IsNegative()
	}

	// Calculate Bilateral Net settlements for each unique pair (A, B)
	processedPairs := make(map[string]bool)
	var netTransfers []NetSettlementTransfer
	netVolume := decimal.Zero

	for d, creditors := range pairMatrix {
		for c, amountAtoB := range creditors {
			pairKey := d + ":" + c
			reverseKey := c + ":" + d
			if processedPairs[pairKey] || processedPairs[reverseKey] {
				continue
			}

			amountBtoA := decimal.Zero
			if revCreditors, ok := pairMatrix[c]; ok {
				amountBtoA = revCreditors[d]
			}

			processedPairs[pairKey] = true
			processedPairs[reverseKey] = true

			grossPairTotal := amountAtoB.Add(amountBtoA)

			// Net calculation
			if amountAtoB.GreaterThan(amountBtoA) {
				netAmt := amountAtoB.Sub(amountBtoA)
				savings := grossPairTotal.Sub(netAmt)
				netTransfers = append(netTransfers, NetSettlementTransfer{
					DebtorBIC:   d,
					CreditorBIC: c,
					Amount:      netAmt,
					Currency:    currency,
					Savings:     savings,
				})
				netVolume = netVolume.Add(netAmt)
			} else if amountBtoA.GreaterThan(amountAtoB) {
				netAmt := amountBtoA.Sub(amountAtoB)
				savings := grossPairTotal.Sub(netAmt)
				netTransfers = append(netTransfers, NetSettlementTransfer{
					DebtorBIC:   c,
					CreditorBIC: d,
					Amount:      netAmt,
					Currency:    currency,
					Savings:     savings,
				})
				netVolume = netVolume.Add(netAmt)
			}
			// If amountAtoB == amountBtoA, net is 0, completely offset!
		}
	}

	savingsAmount := grossVolume.Sub(netVolume)
	var savingsRatio decimal.Decimal
	if grossVolume.GreaterThan(decimal.Zero) {
		savingsRatio = savingsAmount.Div(grossVolume).Round(4)
	}

	return &NettingOptimizationReport{
		Currency:               currency,
		GrossVolume:            grossVolume,
		GrossTransactionCount:  len(obligations),
		NetVolume:              netVolume,
		NetTransactionCount:    len(netTransfers),
		LiquiditySavingsAmount: savingsAmount,
		LiquiditySavingsRatio:  savingsRatio,
		CancelledCycleVolume:   decimal.Zero,
		NetTransfers:           netTransfers,
		ParticipantPositions:   participants,
	}, nil
}

// ExecuteMultilateralNetting coordinates multilateral clearing via a Central Clearing Counterparty (CCP).
// It resolves every participant's gross positions into a single net debtor or creditor balance.
func (eng *LiquidityNettingEngine) ExecuteMultilateralNetting(
	ctx context.Context,
	currency string,
	centralCounterpartyBIC string,
	obligations []GrossObligation,
) (*NettingOptimizationReport, error) {
	eng.mu.Lock()
	defer eng.mu.Unlock()

	if len(obligations) == 0 {
		return nil, ErrZeroNettingObligations
	}

	grossVolume := decimal.Zero
	participants := make(map[string]*MultilateralParticipantPosition)

	for _, obl := range obligations {
		if obl.Currency != currency {
			return nil, fmt.Errorf("%w: expected %s, got %s", ErrCurrencyMismatch, currency, obl.Currency)
		}
		if obl.Amount.LessThanOrEqual(decimal.Zero) {
			continue
		}

		grossVolume = grossVolume.Add(obl.Amount)

		if _, ok := participants[obl.DebtorBIC]; !ok {
			participants[obl.DebtorBIC] = &MultilateralParticipantPosition{
				BIC: obl.DebtorBIC, GrossPayables: decimal.Zero, GrossReceivables: decimal.Zero,
			}
		}
		if _, ok := participants[obl.CreditorBIC]; !ok {
			participants[obl.CreditorBIC] = &MultilateralParticipantPosition{
				BIC: obl.CreditorBIC, GrossPayables: decimal.Zero, GrossReceivables: decimal.Zero,
			}
		}

		participants[obl.DebtorBIC].GrossPayables = participants[obl.DebtorBIC].GrossPayables.Add(obl.Amount)
		participants[obl.CreditorBIC].GrossReceivables = participants[obl.CreditorBIC].GrossReceivables.Add(obl.Amount)
	}

	// Multilateral Invariant Check: Sum of all net positions must equal exactly ZERO
	totalNetCheck := decimal.Zero
	for _, p := range participants {
		p.NetPosition = p.GrossReceivables.Sub(p.GrossPayables)
		p.IsDebtor = p.NetPosition.IsNegative()
		totalNetCheck = totalNetCheck.Add(p.NetPosition)
	}

	if !totalNetCheck.Equal(decimal.Zero) {
		return nil, fmt.Errorf("%w: residual balance %s", ErrUnbalancedMultilateral, totalNetCheck.StringFixed(4))
	}

	// Route net transfers through Central Counterparty
	var netTransfers []NetSettlementTransfer
	netVolume := decimal.Zero

	for _, p := range participants {
		if p.NetPosition.IsNegative() {
			// Net Debtor pays CCP
			payAmt := p.NetPosition.Abs()
			netTransfers = append(netTransfers, NetSettlementTransfer{
				DebtorBIC:   p.BIC,
				CreditorBIC: centralCounterpartyBIC,
				Amount:      payAmt,
				Currency:    currency,
				Savings:     p.GrossPayables.Sub(payAmt),
			})
			netVolume = netVolume.Add(payAmt)
		} else if p.NetPosition.IsPositive() {
			// CCP pays Net Creditor
			recAmt := p.NetPosition
			netTransfers = append(netTransfers, NetSettlementTransfer{
				DebtorBIC:   centralCounterpartyBIC,
				CreditorBIC: p.BIC,
				Amount:      recAmt,
				Currency:    currency,
				Savings:     p.GrossReceivables.Sub(recAmt),
			})
		}
	}

	savingsAmount := grossVolume.Sub(netVolume)
	var savingsRatio decimal.Decimal
	if grossVolume.GreaterThan(decimal.Zero) {
		savingsRatio = savingsAmount.Div(grossVolume).Round(4)
	}

	return &NettingOptimizationReport{
		Currency:               currency,
		GrossVolume:            grossVolume,
		GrossTransactionCount:  len(obligations),
		NetVolume:              netVolume,
		NetTransactionCount:    len(netTransfers),
		LiquiditySavingsAmount: savingsAmount,
		LiquiditySavingsRatio:  savingsRatio,
		CancelledCycleVolume:   decimal.Zero,
		NetTransfers:           netTransfers,
		ParticipantPositions:   participants,
	}, nil
}

// EliminateCircularDebt detects directed debt cycles (e.g. A->B->C->A) using cycle detection
// and offsets the bottleneck minimum amount across the loop with zero cross-border transfers.
func (eng *LiquidityNettingEngine) EliminateCircularDebt(
	ctx context.Context,
	currency string,
	obligations []GrossObligation,
) ([]GrossObligation, decimal.Decimal, error) {
	eng.mu.Lock()
	defer eng.mu.Unlock()

	// Graph representation: Adjacency map of Debtor -> Creditor -> Remaining Amount
	graph := make(map[string]map[string]decimal.Decimal)
	var totalCycleCancelled decimal.Decimal = decimal.Zero

	for _, obl := range obligations {
		if obl.Currency != currency {
			return nil, decimal.Zero, ErrCurrencyMismatch
		}
		if _, ok := graph[obl.DebtorBIC]; !ok {
			graph[obl.DebtorBIC] = make(map[string]decimal.Decimal)
		}
		graph[obl.DebtorBIC][obl.CreditorBIC] = graph[obl.DebtorBIC][obl.CreditorBIC].Add(obl.Amount)
	}

	// Repeatedly find and cancel cycles until none remain
	for {
		cycle := findDirectedCycle(graph)
		if len(cycle) == 0 {
			break
		}

		// Find bottleneck (minimum edge weight in cycle)
		bottleneck := decimal.NewFromInt(1000000000)
		for i := 0; i < len(cycle)-1; i++ {
			u := cycle[i]
			v := cycle[i+1]
			edgeWeight := graph[u][v]
			if edgeWeight.LessThan(bottleneck) {
				bottleneck = edgeWeight
			}
		}

		if bottleneck.IsPositive() {
			// Subtract bottleneck from all edges in the cycle
			for i := 0; i < len(cycle)-1; i++ {
				u := cycle[i]
				v := cycle[i+1]
				graph[u][v] = graph[u][v].Sub(bottleneck)
				if graph[u][v].IsZero() {
					delete(graph[u], v)
				}
			}
			cycleVolume := bottleneck.Mul(decimal.NewFromInt(int64(len(cycle) - 1)))
			totalCycleCancelled = totalCycleCancelled.Add(cycleVolume)
		}
	}

	// Reconstruct pruned obligations
	var pruned []GrossObligation
	for u, neighbors := range graph {
		for v, remainingAmt := range neighbors {
			if remainingAmt.IsPositive() {
				pruned = append(pruned, GrossObligation{
					ID:          uuid.New(),
					DebtorBIC:   u,
					CreditorBIC: v,
					Amount:      remainingAmt,
					Currency:    currency,
					Reference:   "CYCLE_OPTIMIZED_RESIDUAL",
					CreatedAt:   time.Now().UTC(),
				})
			}
		}
	}

	return pruned, totalCycleCancelled, nil
}

// findDirectedCycle searches for a directed cycle using Depth-First Search with recursion stack tracking.
func findDirectedCycle(graph map[string]map[string]decimal.Decimal) []string {
	visited := make(map[string]bool)
	recStack := make(map[string]bool)
	var path []string

	// Sort nodes deterministically
	var nodes []string
	for k := range graph {
		nodes = append(nodes, k)
	}
	sort.Strings(nodes)

	var dfs func(node string) []string
	dfs = func(u string) []string {
		visited[u] = true
		recStack[u] = true
		path = append(path, u)

		var neighbors []string
		for v, amt := range graph[u] {
			if amt.IsPositive() {
				neighbors = append(neighbors, v)
			}
		}
		sort.Strings(neighbors)

		for _, v := range neighbors {
			if !visited[v] {
				if cycle := dfs(v); len(cycle) > 0 {
					return cycle
				}
			} else if recStack[v] {
				// Cycle detected: extract subpath from v to end
				cycleStart := -1
				for idx, n := range path {
					if n == v {
						cycleStart = idx
						break
					}
				}
				if cycleStart != -1 {
					cycle := append([]string(nil), path[cycleStart:]...)
					cycle = append(cycle, v) // Close loop
					return cycle
				}
			}
		}

		recStack[u] = false
		path = path[:len(path)-1]
		return nil
	}

	for _, node := range nodes {
		if !visited[node] {
			if cycle := dfs(node); len(cycle) > 0 {
				return cycle
			}
		}
	}

	return nil
}
