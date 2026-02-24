package live

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/alejandrodnm/polybot/internal/domain"
	"github.com/alejandrodnm/polybot/internal/application/engine"
	"github.com/alejandrodnm/polybot/internal/ports"
)

const (
	MaxMarkets             = 10
	maxPartialHours        = 6
	nearEndHours           = 24
	minShares              = 5
	minOrderUSDC           = 0.10
	staleHours             = 4.0
	competitionMult        = 3.0
	mergeDelayMins         = 2
	blockMinutes           = 15
	maxBidTickUp           = 0.45
	bidTickStep            = 0.01
	minMergeProfitUSDC     = 0.05
	maxMarketConcentration = 0.15
	queueConservativeMult  = 1.5
	minVolume24h           = 5000
	minAskDepthShares      = 10
	maxSpreadPct           = 0.60
	spreadStabilityWindow  = 3
	spreadVarianceMax      = 0.10
	circuitBreakerLosses   = 3
	circuitBreakerCooldown = 30 * time.Minute
	circuitBreakerDrawdown = -0.05
)

// spreadSample is a snapshot of spread quality for a market at a given time.
type spreadSample struct {
	SpreadTotal float64
	FillCost    float64
	ScannedAt   time.Time
}

// Config holds configuration for the live execution engine.
type Config struct {
	OrderSize      float64
	MaxMarkets     int
	FeeRate        float64
	InitialCapital float64
	MaxExposure    float64
	MinMergeProfit float64
}

// CycleResult contains everything produced by one live trading cycle.
type CycleResult struct {
	Positions       []domain.LivePosition
	NewOrders       int
	NewFills        int
	CompletePairs   int
	PartialAlerts   []string
	Warnings        []string
	CapitalDeployed float64
	TotalReward     float64
	Merges          int
	MergeProfit     float64
	GasCostUSD      float64
	CompoundBalance float64
	TotalRotations  int
	AvgCycleHours   float64
	KellyFraction   float64
	CircuitOpen     bool
}

// Engine executes real trades on Polymarket.
type Engine struct {
	scanner  engine.ScannerService
	books    ports.BookProvider
	executor ports.OrderExecutor
	merger   ports.MergeExecutor
	store    ports.LiveStorage
	cfg      Config
	breaker  domain.CircuitBreaker

	spreadHistory map[string][]spreadSample
	spreadMu      sync.RWMutex

	lastGasUpdate time.Time
	cachedGasUSD  float64
	lastScan      time.Time
}

// New creates a real-money trading engine.
func New(
	scanner engine.ScannerService,
	books ports.BookProvider,
	executor ports.OrderExecutor,
	merger ports.MergeExecutor,
	store ports.LiveStorage,
	cfg Config,
) *Engine {
	if cfg.MaxMarkets <= 0 {
		cfg.MaxMarkets = MaxMarkets
	}
	if cfg.MinMergeProfit <= 0 {
		cfg.MinMergeProfit = minMergeProfitUSDC
	}

	return &Engine{
		scanner:       scanner,
		books:         books,
		executor:      executor,
		merger:        merger,
		store:         store,
		cfg:           cfg,
		spreadHistory: make(map[string][]spreadSample),
		lastScan:      time.Now().Add(-5 * time.Minute),
		breaker: domain.CircuitBreaker{
			MaxLosses:        circuitBreakerLosses,
			CooldownDuration: circuitBreakerCooldown,
			MaxDrawdown:      cfg.InitialCapital * circuitBreakerDrawdown,
		},
	}
}

// RestoreCircuitBreaker loads a previously saved circuit breaker state.
func (le *Engine) RestoreCircuitBreaker(cb domain.CircuitBreaker) {
	cb.MaxLosses = le.breaker.MaxLosses
	cb.CooldownDuration = le.breaker.CooldownDuration
	cb.MaxDrawdown = le.breaker.MaxDrawdown
	le.breaker = cb
}

// RunOnce executes one live trading cycle. Orchestrates: protection → scan →
// sync → maintenance → merge → placement → reporting.
func (le *Engine) RunOnce(ctx context.Context) (*CycleResult, error) {
	result := &CycleResult{}

	// 1. Protection: check circuit breaker
	if !le.breaker.IsOpen() {
		result.CircuitOpen = false
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("CIRCUIT BREAKER: %s — pausing until %s",
				le.breaker.TriggeredReason,
				le.breaker.CooldownUntil.Format("15:04:05")))
		slog.Warn("live: circuit breaker active, skipping cycle",
			"reason", le.breaker.TriggeredReason)
		return result, nil
	}
	result.CircuitOpen = true

	// 2. Discovery: get balance + scan markets
	balance, err := le.executor.GetBalance(ctx)
	if err != nil {
		return nil, fmt.Errorf("live.RunOnce: get balance: %w", err)
	}
	slog.Info("live: cycle start", "balance", fmt.Sprintf("$%.2f", balance))

	opps, err := le.scanner.RunOnce(ctx)
	if err != nil {
		return nil, fmt.Errorf("live.RunOnce: scan: %w", err)
	}

	oppByCondition := make(map[string]domain.Opportunity, len(opps))
	for _, opp := range opps {
		oppByCondition[opp.Market.ConditionID] = opp
	}

	// 3. Verification: sync state + spread history
	le.updateSpreadHistory(opps)

	newFills, err := le.syncOrderState(ctx, oppByCondition)
	if err != nil {
		slog.Warn("live: error syncing order state", "err", err)
	}
	result.NewFills = newFills

	// 4. Maintenance: cancel resolved + rotate stale
	le.cancelResolvedOrders(ctx, oppByCondition)

	staleRotated := le.rotateStaleOrders(ctx, oppByCondition)
	if staleRotated > 0 {
		result.TotalRotations = staleRotated
		slog.Info("live: rotated stale orders", "pairs", staleRotated)
	}

	// 5. Merge: execute on-chain merges for complete pairs
	merges, mergeProfit, gasCost, err := le.mergeCompletePairs(ctx)
	if err != nil {
		slog.Warn("live: error merging pairs", "err", err)
	}
	result.Merges = merges
	result.MergeProfit = mergeProfit
	result.GasCostUSD = gasCost

	// 6. Capital allocation
	compoundBalance, totalMergeProfit, totalRotations, avgCycleHours := le.getCompoundMetrics(ctx)
	result.CompoundBalance = compoundBalance
	result.TotalRotations += totalRotations
	result.AvgCycleHours = avgCycleHours

	activeConditions, _ := le.store.GetActiveLiveConditions(ctx)

	deployedOpen, deployedPartial, deployedFilled := le.calculateDeployedCapital(ctx)
	currentCapital := deployedOpen + deployedPartial + deployedFilled
	result.CapitalDeployed = currentCapital

	effectiveCapital, kellyF := le.capitalAllocation(ctx, totalMergeProfit)
	result.KellyFraction = kellyF

	// 7. Placement pipeline: filter + place orders
	pOut := le.runPlacementPipeline(ctx, placementInput{
		opps:             opps,
		activeConditions: activeConditions,
		balance:          balance,
		currentCapital:   currentCapital,
		effectiveCapital: effectiveCapital,
	})
	result.NewOrders = pOut.newOrders
	result.CapitalDeployed = pOut.capitalAfter
	result.Warnings = append(result.Warnings, pOut.warnings...)

	// 8. Reporting: build positions + alerts
	positions, totalReward := le.buildPositions(ctx, oppByCondition)
	result.Positions = positions
	result.TotalReward = totalReward

	for _, pos := range positions {
		if pos.IsComplete {
			result.CompletePairs++
		}
		if pos.PartialSince != nil && !pos.IsComplete {
			dur := pos.PartialDuration()
			if dur > maxPartialHours*time.Hour {
				result.PartialAlerts = append(result.PartialAlerts,
					fmt.Sprintf("PARTIAL >%dh: %s (%.0fh)", maxPartialHours, pos.Question, dur.Hours()))
			}
		}
		if pos.HoursToEnd > 0 && pos.HoursToEnd < 48 {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("NEAR END (%.0fh): %s", pos.HoursToEnd, engine.TruncateStr(pos.Question, 30)))
		}
	}

	le.saveDailySummary(ctx, result)
	le.lastScan = time.Now()
	return result, nil
}
