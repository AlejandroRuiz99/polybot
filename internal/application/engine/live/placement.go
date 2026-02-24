package live

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"

	"github.com/alejandrodnm/polybot/internal/domain"
)

// placementInput agrupa los datos necesarios para el pipeline de placement.
type placementInput struct {
	opps             []domain.Opportunity
	activeConditions []string
	balance          float64
	currentCapital   float64
	effectiveCapital float64
}

// placementOutput contiene los resultados del pipeline de placement.
type placementOutput struct {
	newOrders      int
	capitalAfter   float64
	warnings       []string
}

// runPlacementPipeline evalúa oportunidades, filtra por calidad, y coloca órdenes.
// Cada oportunidad pasa por gates de seguridad antes de ser ejecutada.
func (le *Engine) runPlacementPipeline(ctx context.Context, in placementInput) placementOutput {
	out := placementOutput{capitalAfter: in.currentCapital}

	sort.Slice(in.opps, func(i, j int) bool {
		return velocityScore(in.opps[i]) > velocityScore(in.opps[j])
	})

	le.logFillCostDistribution(in.opps)

	activeSet := make(map[string]bool, len(in.activeConditions))
	for _, c := range in.activeConditions {
		activeSet[c] = true
	}

	var stats pipelineStats
	balance := in.balance
	currentCapital := in.currentCapital

	for _, opp := range in.opps {
		skip, reason := le.gateCheck(opp, activeSet, len(in.activeConditions)+out.newOrders/2)
		if skip {
			stats.record(reason)
			if reason == skipReasonMaxMarkets || reason == skipReasonBreaker {
				break
			}
			continue
		}

		orderSize, sizeOK := le.calculateOrderSize(opp, in.effectiveCapital, currentCapital, balance)
		if !sizeOK {
			stats.record(skipReasonSize)
			if (in.effectiveCapital-currentCapital)/2 < minOrderUSDC {
				out.warnings = append(out.warnings,
					fmt.Sprintf("capital limit: $%.0f deployed / $%.0f deployable", currentCapital, in.effectiveCapital))
				break
			}
			continue
		}

		slog.Info("live: PLACING ORDER",
			"market", opp.Market.Question[:min(50, len(opp.Market.Question))],
			"fillCost", fmt.Sprintf("%.4f", opp.FillCostPerPair),
			"orderSize", fmt.Sprintf("$%.2f", orderSize),
			"yesBookBid", fmt.Sprintf("%.2f", opp.YesBook.BestBid()),
			"yesBookAsk", fmt.Sprintf("%.2f", opp.YesBook.BestAsk()),
			"noBookBid", fmt.Sprintf("%.2f", opp.NoBook.BestBid()),
			"noBookAsk", fmt.Sprintf("%.2f", opp.NoBook.BestAsk()),
		)

		if err := le.placeOrderPair(ctx, opp, orderSize); err != nil {
			slog.Warn("live: error placing order pair", "market", opp.Market.Question, "err", err)
			if strings.Contains(err.Error(), "NegRisk") {
				stats.record(skipReasonNegRisk)
			}
			continue
		}

		activeSet[opp.Market.ConditionID] = true
		out.newOrders += 2
		currentCapital += orderSize * 2
		balance -= orderSize * 2
	}

	out.capitalAfter = currentCapital
	stats.log(len(in.opps), out.newOrders)
	return out
}

type skipReason int

const (
	skipReasonMaxMarkets skipReason = iota
	skipReasonActive
	skipReasonBreaker
	skipReasonVolume
	skipReasonDepth
	skipReasonSpreadPct
	skipReasonFillCost
	skipReasonHours
	skipReasonSpreadStab
	skipReasonSize
	skipReasonNegRisk
)

// gateCheck aplica todos los filtros de seguridad a una oportunidad.
func (le *Engine) gateCheck(opp domain.Opportunity, activeSet map[string]bool, currentMarkets int) (skip bool, reason skipReason) {
	if currentMarkets >= le.cfg.MaxMarkets {
		return true, skipReasonMaxMarkets
	}
	if activeSet[opp.Market.ConditionID] {
		return true, skipReasonActive
	}
	if !le.breaker.IsOpen() {
		return true, skipReasonBreaker
	}
	if opp.Market.Volume24h > 0 && opp.Market.Volume24h < minVolume24h {
		return true, skipReasonVolume
	}

	yesAskDepth := askDepthShares(opp.YesBook)
	noAskDepth := askDepthShares(opp.NoBook)
	if yesAskDepth < minAskDepthShares || noAskDepth < minAskDepthShares {
		return true, skipReasonDepth
	}

	yesMid := opp.YesBook.Midpoint()
	noMid := opp.NoBook.Midpoint()
	if (yesMid > 0 && opp.YesBook.Spread()/yesMid > maxSpreadPct) ||
		(noMid > 0 && opp.NoBook.Spread()/noMid > maxSpreadPct) {
		return true, skipReasonSpreadPct
	}

	if opp.FillCostPerPair > 0.02 {
		return true, skipReasonFillCost
	}

	hoursLeft := opp.Market.HoursToResolution()
	if hoursLeft > 0 && hoursLeft < nearEndHours {
		return true, skipReasonHours
	}

	if !le.spreadStable(opp.Market.ConditionID) {
		return true, skipReasonSpreadStab
	}

	return false, 0
}

// calculateOrderSize determina el tamaño de la orden respetando límites de capital.
func (le *Engine) calculateOrderSize(opp domain.Opportunity, effectiveCapital, currentCapital, balance float64) (float64, bool) {
	orderSize := le.cfg.OrderSize
	maxAffordable := (effectiveCapital - currentCapital) / 2
	maxFromBalance := (balance - 0.5) / 2
	if maxFromBalance < maxAffordable {
		maxAffordable = maxFromBalance
	}
	if orderSize > maxAffordable {
		orderSize = maxAffordable
	}

	approxPrice := (opp.YesBook.BestBid() + opp.NoBook.BestBid()) / 2
	if approxPrice <= 0 {
		approxPrice = 0.50
	}
	minUSDCFor5Shares := float64(minShares) * approxPrice
	if minUSDCFor5Shares < minOrderUSDC {
		minUSDCFor5Shares = minOrderUSDC
	}

	return orderSize, orderSize >= minUSDCFor5Shares
}

// capitalAllocation calcula cuánto capital es desplegable basándose en Kelly y límites.
func (le *Engine) capitalAllocation(ctx context.Context, totalMergeProfit float64) (effectiveCapital, kellyF float64) {
	kellyF = le.kellyFraction(ctx)
	bankroll := le.cfg.InitialCapital + totalMergeProfit
	effectiveCapital = math.Min(bankroll*kellyF, le.cfg.MaxExposure)
	if effectiveCapital <= 0 {
		effectiveCapital = le.cfg.InitialCapital * 0.5
	}

	slog.Debug("live: capital allocation",
		"bankroll", fmt.Sprintf("$%.2f", bankroll),
		"kelly", fmt.Sprintf("%.0f%%", kellyF*100),
		"deployable", fmt.Sprintf("$%.2f", effectiveCapital),
	)
	return effectiveCapital, kellyF
}

func (le *Engine) logFillCostDistribution(opps []domain.Opportunity) {
	var fcNeg, fc001, fc003, fc005, fcPos int
	for _, opp := range opps {
		switch {
		case opp.FillCostPerPair <= 0:
			fcNeg++
		case opp.FillCostPerPair <= 0.01:
			fc001++
		case opp.FillCostPerPair <= 0.03:
			fc003++
		case opp.FillCostPerPair <= 0.05:
			fc005++
		default:
			fcPos++
		}
	}
	slog.Info("live: fill cost distribution",
		"profitable", fcNeg, "<=1%", fc001, "<=3%", fc003, "<=5%", fc005, ">5%", fcPos)
}

type pipelineStats struct {
	maxMkts, active, breaker, fillCost, hours, spread, size, negRisk int
	volume, depth, spreadPct                                         int
}

func (s *pipelineStats) record(r skipReason) {
	switch r {
	case skipReasonMaxMarkets:
		s.maxMkts++
	case skipReasonActive:
		s.active++
	case skipReasonBreaker:
		s.breaker++
	case skipReasonVolume:
		s.volume++
	case skipReasonDepth:
		s.depth++
	case skipReasonSpreadPct:
		s.spreadPct++
	case skipReasonFillCost:
		s.fillCost++
	case skipReasonHours:
		s.hours++
	case skipReasonSpreadStab:
		s.spread++
	case skipReasonSize:
		s.size++
	case skipReasonNegRisk:
		s.negRisk++
	}
}

func (s *pipelineStats) log(totalOpps, placed int) {
	slog.Info("live: placement pipeline",
		"total_opps", totalOpps,
		"skip_volume", s.volume,
		"skip_depth", s.depth,
		"skip_spread%", s.spreadPct,
		"skip_fillcost", s.fillCost,
		"skip_hours", s.hours,
		"skip_spread_stab", s.spread,
		"skip_size", s.size,
		"skip_negrisk", s.negRisk,
		"skip_maxmkts", s.maxMkts,
		"skip_active", s.active,
		"skip_breaker", s.breaker,
		"placed", placed,
	)
}
