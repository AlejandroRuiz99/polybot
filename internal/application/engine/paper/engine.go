package paper

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/alejandrodnm/polybot/internal/domain"
	"github.com/alejandrodnm/polybot/internal/application/engine"
	"github.com/alejandrodnm/polybot/internal/ports"
)

const (
	DefaultMaxMarkets   = 10
	maxPartialHours     = 6
	nearEndHours        = 24
	defaultCapital      = 1000
	minOrderSize        = 10.0
	maxBidTickUp        = 0.03
	bidTickStep         = 0.01
	mergeGasCost        = 0.02
	mergeDelayMins      = 2
	competitionMult     = 3.0
	staleHours          = 4
	blockMinutes        = 15
)

// Config holds paper trading-specific settings.
type Config struct {
	OrderSize      float64
	MaxMarkets     int
	FeeRate        float64
	InitialCapital float64
}

// Engine runs the paper trading simulation loop.
type Engine struct {
	scanner  engine.ScannerService
	trades   ports.TradeProvider
	store    ports.PaperStorage
	cfg      Config
	lastScan time.Time
}

// New creates a paper trading engine.
func New(
	scanner engine.ScannerService,
	trades ports.TradeProvider,
	store ports.PaperStorage,
	cfg Config,
) *Engine {
	if cfg.MaxMarkets <= 0 {
		cfg.MaxMarkets = DefaultMaxMarkets
	}
	if cfg.InitialCapital <= 0 {
		cfg.InitialCapital = defaultCapital
	}
	return &Engine{
		scanner:  scanner,
		trades:   trades,
		store:    store,
		cfg:      cfg,
		lastScan: time.Now().Add(-5 * time.Minute),
	}
}

// CycleResult contains everything produced by one paper trading cycle.
type CycleResult struct {
	Positions       []domain.PaperPosition
	NewOrders       int
	NewFills        int
	CompletePairs   int
	PartialAlerts   []string
	Warnings        []string
	CapitalDeployed float64
	TotalReward     float64
	MarketsResolved int
	Merges          int
	MergeProfit     float64
	CompoundBalance float64
	TotalRotations  int
	AvgCycleHours   float64
	KellyFraction   float64
}

// RunOnce executes a single paper trading cycle.
func (pe *Engine) RunOnce(ctx context.Context) (*CycleResult, error) {
	result := &CycleResult{}

	opps, err := pe.scanner.RunOnce(ctx)
	if err != nil {
		return nil, fmt.Errorf("paper.RunOnce: scan: %w", err)
	}

	oppByCondition := make(map[string]domain.Opportunity, len(opps))
	for _, opp := range opps {
		oppByCondition[opp.Market.ConditionID] = opp
	}

	resolved := pe.expireResolvedAndNearEnd(ctx, oppByCondition)
	result.MarketsResolved = resolved

	pe.refreshQueues(ctx, oppByCondition)

	staleExpired := pe.rotateStaleOrders(ctx, oppByCondition)
	if staleExpired > 0 {
		slog.Info("paper: rotated stale orders", "pairs_freed", staleExpired)
	}

	fills, err := pe.checkFills(ctx)
	if err != nil {
		slog.Warn("paper: error checking fills", "err", err)
	}
	result.NewFills = fills

	merges, mergeProfit, err := pe.mergeCompletePairs(ctx)
	if err != nil {
		slog.Warn("paper: error merging pairs", "err", err)
	}
	result.Merges = merges
	result.MergeProfit = mergeProfit

	compoundBalance, totalMergeProfit, totalRotations, avgCycleHours := pe.getCompoundMetrics(ctx)
	result.CompoundBalance = compoundBalance
	result.TotalRotations = totalRotations
	result.AvgCycleHours = avgCycleHours

	activeConditions, err := pe.store.GetActivePaperConditions(ctx)
	if err != nil {
		slog.Warn("paper: error getting active conditions", "err", err)
	}

	activeSet := make(map[string]bool, len(activeConditions))
	for _, c := range activeConditions {
		activeSet[c] = true
	}

	deployedOpen, deployedPartial, deployedFilled := pe.calculateDeployedCapital(ctx)
	currentCapital := deployedOpen + deployedPartial + deployedFilled
	result.CapitalDeployed = currentCapital

	kellyF := pe.kellyFraction(ctx)
	result.KellyFraction = kellyF
	bankroll := pe.cfg.InitialCapital + totalMergeProfit
	effectiveCapital := bankroll * kellyF

	slog.Debug("paper: Kelly capital allocation",
		"bankroll", fmt.Sprintf("$%.2f", bankroll),
		"kelly_f", fmt.Sprintf("%.0f%%", kellyF*100),
		"deployable", fmt.Sprintf("$%.2f", effectiveCapital),
		"deployed", fmt.Sprintf("$%.2f", currentCapital),
	)

	sort.Slice(opps, func(i, j int) bool {
		return compoundVelocityScore(opps[i]) > compoundVelocityScore(opps[j])
	})

	newOrders := 0
	for _, opp := range opps {
		if len(activeConditions)+newOrders/2 >= pe.cfg.MaxMarkets {
			break
		}
		if activeSet[opp.Market.ConditionID] {
			continue
		}
		if opp.FillCostPerPair > 0 {
			continue
		}
		if opp.YourDailyReward <= 0 {
			continue
		}
		hoursLeft := opp.Market.HoursToResolution()
		if hoursLeft > 0 && hoursLeft < nearEndHours {
			continue
		}
		if !opp.QualifiesReward {
			continue
		}

		orderSize := pe.optimalOrderSize(opp)
		maxAffordable := (effectiveCapital - currentCapital) / 2
		if orderSize > maxAffordable {
			orderSize = maxAffordable
		}
		if orderSize < minOrderSize {
			if currentCapital == 0 {
				orderSize = maxAffordable
			}
			if orderSize < 1 {
				result.Warnings = append(result.Warnings,
					fmt.Sprintf("compound capital limit: $%.0f deployed / $%.0f available (initial $%.0f + profit $%.2f)",
						currentCapital, effectiveCapital, pe.cfg.InitialCapital, totalMergeProfit))
				break
			}
		}
		orderCapital := orderSize * 2

		if err := pe.placeVirtualOrdersWithSize(ctx, opp, orderSize); err != nil {
			slog.Warn("paper: error placing virtual orders",
				"market", opp.Market.Question, "err", err)
			continue
		}
		activeSet[opp.Market.ConditionID] = true
		newOrders += 2
		currentCapital += orderCapital
	}
	result.NewOrders = newOrders
	result.CapitalDeployed = currentCapital

	positions, err := pe.buildPositions(ctx, oppByCondition)
	if err != nil {
		slog.Warn("paper: error building positions", "err", err)
	}
	result.Positions = positions

	totalReward := 0.0
	for _, pos := range positions {
		if pos.IsComplete || pos.IsResolved {
			continue
		}
		totalReward += pos.RewardAccrued
		if pos.IsComplete {
			result.CompletePairs++
		}
	}
	result.TotalReward = totalReward

	for _, pos := range positions {
		if pos.IsComplete {
			result.CompletePairs++
		}
		if pos.PartialSince != nil && !pos.IsComplete && !pos.IsResolved {
			dur := pos.PartialDuration()
			if dur > maxPartialHours*time.Hour {
				alert := fmt.Sprintf("PARTIAL >%dh: %s (%s filled %.0fh ago)",
					maxPartialHours, pos.Question, partialSide(pos), dur.Hours())
				result.PartialAlerts = append(result.PartialAlerts, alert)
				slog.Warn("paper: long partial fill", "market", pos.Question,
					"side", partialSide(pos), "hours", dur.Hours())
			}
		}
		if pos.HoursToEnd > 0 && pos.HoursToEnd < 48 && !pos.IsResolved {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("NEAR END (%.0fh): %s", pos.HoursToEnd, engine.TruncateStr(pos.Question, 30)))
		}
	}

	pe.saveDailySummary(ctx, result)
	pe.lastScan = time.Now()
	return result, nil
}
