package live

import (
	"context"
	"log/slog"
	"math"
	"time"

	"github.com/alejandrodnm/polybot/internal/domain"
)

// calculateDeployedCapital sums capital by order status.
func (le *Engine) calculateDeployedCapital(ctx context.Context) (open, partial, filled float64) {
	openOrders, err := le.store.GetOpenLiveOrders(ctx)
	if err != nil {
		return 0, 0, 0
	}
	for _, o := range openOrders {
		switch o.Status {
		case domain.LiveStatusOpen:
			open += o.Size
		case domain.LiveStatusPartial:
			partial += o.Size - o.FilledSize
		case domain.LiveStatusFilled:
			filled += o.FilledSize
		}
	}
	return open, partial, filled
}

// getCompoundMetrics returns P&L and rotation stats from merge history.
func (le *Engine) getCompoundMetrics(ctx context.Context) (balance, totalProfit float64, rotations int, avgCycleHours float64) {
	merges, err := le.store.GetMergeResults(ctx)
	if err != nil {
		return le.cfg.InitialCapital, 0, 0, 0
	}

	var totalGas float64
	var cycleDurations []float64

	for _, m := range merges {
		if m.Success {
			totalProfit += m.SpreadProfit
			totalGas += m.GasCostUSD
			rotations++
		}
	}

	filledOrders, _ := le.store.GetAllLiveOrders(ctx, string(domain.LiveStatusMerged))
	for _, o := range filledOrders {
		if o.MergedAt != nil {
			cycleH := o.MergedAt.Sub(o.PlacedAt).Hours()
			cycleDurations = append(cycleDurations, cycleH)
		}
	}
	if len(cycleDurations) > 0 {
		sum := 0.0
		for _, h := range cycleDurations {
			sum += h
		}
		avgCycleHours = sum / float64(len(cycleDurations))
	}

	deployedOpen, deployedPartial, deployedFilled := le.calculateDeployedCapital(ctx)
	deployed := deployedOpen + deployedPartial + deployedFilled

	balance = le.cfg.InitialCapital + totalProfit - deployed
	_ = totalGas

	return balance, totalProfit, rotations, avgCycleHours
}

// kellyFraction computes the optimal Kelly fraction from real merge history.
func (le *Engine) kellyFraction(ctx context.Context) float64 {
	merges, err := le.store.GetMergeResults(ctx)
	if err != nil || len(merges) < 3 {
		return 0.5
	}

	wins := 0
	totalWin := 0.0
	totalLoss := 0.0
	for _, m := range merges {
		if !m.Success {
			continue
		}
		if m.SpreadProfit > 0 {
			wins++
			totalWin += m.SpreadProfit
		} else {
			totalLoss += math.Abs(m.SpreadProfit)
		}
	}

	attempted := len(merges)
	if attempted == 0 {
		return 0.5
	}

	p := float64(wins) / float64(attempted)
	q := 1.0 - p
	if p <= 0 || q <= 0 {
		return 0.25
	}

	avgWin := totalWin / float64(max(wins, 1))
	avgLoss := totalLoss / float64(max(attempted-wins, 1))
	if avgLoss <= 0 {
		return 0.5
	}

	b := avgWin / avgLoss
	kelly := (p*b - q) / b
	kelly = kelly / 2
	return math.Max(0.1, math.Min(kelly, 0.8))
}

// buildPositions constructs the current portfolio view with reward accrual.
func (le *Engine) buildPositions(ctx context.Context, oppByCondition map[string]domain.Opportunity) ([]domain.LivePosition, float64) {
	openOrders, err := le.store.GetOpenLiveOrders(ctx)
	if err != nil {
		return nil, 0
	}

	byPair := make(map[string][]domain.LiveOrder)
	for _, o := range openOrders {
		byPair[o.PairID] = append(byPair[o.PairID], o)
	}

	var positions []domain.LivePosition
	totalReward := 0.0

	for pairID, orders := range byPair {
		var yes, no *domain.LiveOrder
		for i := range orders {
			switch orders[i].Side {
			case "YES":
				yes = &orders[i]
			case "NO":
				no = &orders[i]
			}
		}

		pos := domain.LivePosition{PairID: pairID}

		if yes != nil {
			pos.YesOrder = yes
			pos.ConditionID = yes.ConditionID
			pos.Question = yes.Question
			pos.YesFilled = yes.Status == domain.LiveStatusFilled || yes.Status == domain.LiveStatusMerged
			pos.CapitalDeployed += yes.Size
			pos.DailyReward = yes.DailyReward
			pos.HoursToEnd = time.Until(yes.EndDate).Hours()
			if pos.HoursToEnd < 0 {
				pos.HoursToEnd = 0
			}
		}
		if no != nil {
			pos.NoOrder = no
			if pos.ConditionID == "" {
				pos.ConditionID = no.ConditionID
				pos.Question = no.Question
				pos.HoursToEnd = time.Until(no.EndDate).Hours()
			}
			pos.NoFilled = no.Status == domain.LiveStatusFilled || no.Status == domain.LiveStatusMerged
			pos.CapitalDeployed += no.Size
		}

		pos.IsComplete = pos.YesFilled && pos.NoFilled

		if yes != nil && !pos.IsComplete {
			activeHours := time.Since(yes.PlacedAt).Hours()
			blocks := int(activeHours * 60 / blockMinutes)
			if blocks > 0 && yes.DailyReward > 0 {
				pos.RewardAccrued = yes.DailyReward * float64(blocks) * float64(blockMinutes) / 60.0 / 24.0
				totalReward += pos.RewardAccrued
			}
		}

		if opp, exists := oppByCondition[pos.ConditionID]; exists {
			pos.SpreadQualifies = opp.QualifiesReward
			if opp.YourDailyReward > 0 {
				pos.DailyReward = opp.YourDailyReward
			}
		}

		if (pos.YesFilled != pos.NoFilled) && !pos.IsComplete {
			if pos.PartialSince == nil {
				now := time.Now()
				pos.PartialSince = &now
			}
		}

		positions = append(positions, pos)
	}

	return positions, totalReward
}

// saveDailySummary persists the daily live trading summary.
func (le *Engine) saveDailySummary(ctx context.Context, result *CycleResult) {
	_, totalMergeProfit, _, _ := le.getCompoundMetrics(ctx)
	summary := domain.LiveDailySummary{
		Date:            time.Now().UTC().Truncate(24 * time.Hour),
		ActivePositions: len(result.Positions),
		CompletePairs:   result.CompletePairs,
		TotalReward:     result.TotalReward,
		NetPnL:          totalMergeProfit,
		OrdersPlaced:    result.NewOrders,
		CapitalDeployed: result.CapitalDeployed,
		Merges:          result.Merges,
		MergeProfit:     result.MergeProfit,
		GasCostUSD:      result.GasCostUSD,
		CompoundBalance: result.CompoundBalance,
		Rotations:       result.TotalRotations,
	}
	if err := le.store.SaveLiveDaily(ctx, summary); err != nil {
		slog.Warn("live: error saving daily summary", "err", err)
	}
	if err := le.store.SaveCircuitBreaker(ctx, le.breaker); err != nil {
		slog.Warn("live: error saving circuit breaker state", "err", err)
	}
}

// velocityScore ranks opportunities for live trading.
func velocityScore(opp domain.Opportunity) float64 {
	yesQ := queuePositionConservative(opp.YesBook, opp.YesBook.BestBid())
	noQ := queuePositionConservative(opp.NoBook, opp.NoBook.BestBid())
	totalQueue := yesQ + noQ

	profitPerPair := -opp.FillCostPerPair
	if profitPerPair <= 0 {
		return 0
	}

	velocityFactor := 1.0
	if totalQueue > 0 {
		velocityFactor = 100.0 / (100.0 + totalQueue)
	}

	volumeFactor := 1.0
	if opp.Market.Volume24h > 0 {
		volumeFactor = 1.0 + math.Log10(opp.Market.Volume24h/1000+1)
	}

	rewardBonus := 1.0 + opp.YourDailyReward*10
	return profitPerPair * velocityFactor * volumeFactor * rewardBonus
}
