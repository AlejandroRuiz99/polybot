package paper

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/alejandrodnm/polybot/internal/domain"
	"github.com/alejandrodnm/polybot/internal/application/engine"
)

// rotateStaleOrders cancels order pairs based on time, spread, or competition spike.
func (pe *Engine) rotateStaleOrders(ctx context.Context, oppByCondition map[string]domain.Opportunity) int {
	openOrders, err := pe.store.GetOpenPaperOrders(ctx)
	if err != nil {
		return 0
	}

	byPair := make(map[string][]domain.VirtualOrder)
	for _, o := range openOrders {
		if o.Status == domain.PaperStatusOpen {
			byPair[o.PairID] = append(byPair[o.PairID], o)
		}
	}

	expired := 0
	for _, orders := range byPair {
		if len(orders) < 2 {
			continue
		}

		allOpen := true
		var oldest time.Time
		for _, o := range orders {
			if o.Status != domain.PaperStatusOpen {
				allOpen = false
				break
			}
			if oldest.IsZero() || o.PlacedAt.Before(oldest) {
				oldest = o.PlacedAt
			}
		}
		if !allOpen || oldest.IsZero() {
			continue
		}

		age := time.Since(oldest).Hours()
		conditionID := orders[0].ConditionID
		rotateReason := ""

		if age >= staleHours {
			rotateReason = fmt.Sprintf("stale (%.1fh, no fills)", age)
		}

		if rotateReason == "" {
			if opp, exists := oppByCondition[conditionID]; exists {
				if opp.FillCostPerPair > 0 {
					rotateReason = fmt.Sprintf("spread no longer profitable (fillCost $%.4f > 0)", opp.FillCostPerPair)
				}
			}
		}

		if rotateReason == "" {
			if opp, exists := oppByCondition[conditionID]; exists {
				currentComp := opp.YesBook.BidDepthWithinUSDC(0.05) + opp.NoBook.BidDepthWithinUSDC(0.05)
				originalCompProxy := orders[0].QueueAhead + orders[1].QueueAhead
				if originalCompProxy > 0 && currentComp > originalCompProxy*competitionMult {
					rotateReason = fmt.Sprintf("competition spiked %.1fx (now $%.0f vs $%.0f at placement)",
						currentComp/originalCompProxy, currentComp, originalCompProxy)
				}
			}
		}

		if rotateReason == "" {
			continue
		}

		if err := pe.store.ExpirePaperOrders(ctx, conditionID); err != nil {
			slog.Warn("paper: error expiring stale pair", "err", err)
			continue
		}

		slog.Info("paper: ROTATED pair",
			"reason", rotateReason,
			"market", engine.TruncateStr(orders[0].Question, 30),
			"age", fmt.Sprintf("%.1fh", age),
		)
		expired++
	}

	return expired
}

// mergeCompletePairs simulates CTF merges for fully filled pairs.
func (pe *Engine) mergeCompletePairs(ctx context.Context) (merges int, totalProfit float64, err error) {
	filledOrders, err := pe.store.GetAllPaperOrders(ctx, string(domain.PaperStatusFilled))
	if err != nil {
		return 0, 0, fmt.Errorf("paper.mergeCompletePairs: %w", err)
	}

	byPair := make(map[string][]domain.VirtualOrder)
	for _, o := range filledOrders {
		byPair[o.PairID] = append(byPair[o.PairID], o)
	}

	now := time.Now().UTC()
	mergeDelay := time.Duration(mergeDelayMins) * time.Minute

	for _, orders := range byPair {
		var yes, no *domain.VirtualOrder
		for i := range orders {
			switch orders[i].Side {
			case "YES":
				yes = &orders[i]
			case "NO":
				no = &orders[i]
			}
		}
		if yes == nil || no == nil {
			continue
		}

		lastFillTime := yes.PlacedAt
		if yes.FilledAt != nil && yes.FilledAt.After(lastFillTime) {
			lastFillTime = *yes.FilledAt
		}
		if no.FilledAt != nil && no.FilledAt.After(lastFillTime) {
			lastFillTime = *no.FilledAt
		}
		if now.Sub(lastFillTime) < mergeDelay {
			slog.Debug("paper: merge delayed (waiting for confirmation)",
				"market", engine.TruncateStr(yes.Question, 30),
				"waitRemaining", fmt.Sprintf("%.0fs", mergeDelay.Seconds()-now.Sub(lastFillTime).Seconds()),
			)
			continue
		}

		yesPrice := yes.FilledPrice
		if yesPrice == 0 {
			yesPrice = yes.BidPrice
		}
		noPrice := no.FilledPrice
		if noPrice == 0 {
			noPrice = no.BidPrice
		}

		spread := 1.0 - yesPrice - noPrice
		yesShares := yes.Size / yesPrice
		noShares := no.Size / noPrice
		mergeable := min(yesShares, noShares)
		grossProfit := mergeable * spread

		netProfit := grossProfit - mergeGasCost
		if netProfit <= 0 {
			slog.Info("paper: skipping merge (unprofitable after gas)",
				"market", engine.TruncateStr(yes.Question, 30),
				"spread", fmt.Sprintf("$%.4f", spread),
				"grossProfit", fmt.Sprintf("$%.4f", grossProfit),
				"gasCost", fmt.Sprintf("$%.4f", mergeGasCost),
			)
			continue
		}

		if err := pe.store.MarkPaperOrderMerged(ctx, yes.ID, now); err != nil {
			slog.Warn("paper: error marking YES as merged", "err", err)
			continue
		}
		if err := pe.store.MarkPaperOrderMerged(ctx, no.ID, now); err != nil {
			slog.Warn("paper: error marking NO as merged", "err", err)
			continue
		}

		cycleTime := now.Sub(yes.PlacedAt)
		capitalUsed := mergeable * (yesPrice + noPrice)
		slog.Info("paper: MERGED pair (compound rotation)",
			"market", engine.TruncateStr(yes.Question, 30),
			"spread", fmt.Sprintf("$%.4f", spread),
			"shares", fmt.Sprintf("%.1f", mergeable),
			"grossProfit", fmt.Sprintf("$%.4f", grossProfit),
			"netProfit", fmt.Sprintf("$%.4f", netProfit),
			"gasCost", fmt.Sprintf("$%.4f", mergeGasCost),
			"capital_used", fmt.Sprintf("$%.2f", capitalUsed),
			"cycle", fmt.Sprintf("%.1fh", cycleTime.Hours()),
		)

		merges++
		totalProfit += netProfit
	}

	return merges, totalProfit, nil
}

// getCompoundMetrics computes the compound balance and rotation stats from MERGED orders.
func (pe *Engine) getCompoundMetrics(ctx context.Context) (balance, totalProfit float64, rotations int, avgCycleHours float64) {
	mergedOrders, err := pe.store.GetAllPaperOrders(ctx, string(domain.PaperStatusMerged))
	if err != nil {
		return pe.cfg.InitialCapital, 0, 0, 0
	}

	byPair := make(map[string][]domain.VirtualOrder)
	for _, o := range mergedOrders {
		byPair[o.PairID] = append(byPair[o.PairID], o)
	}

	var totalCycleHours float64

	for _, orders := range byPair {
		var yes, no *domain.VirtualOrder
		for i := range orders {
			switch orders[i].Side {
			case "YES":
				yes = &orders[i]
			case "NO":
				no = &orders[i]
			}
		}
		if yes == nil || no == nil {
			continue
		}

		yesPrice := yes.FilledPrice
		if yesPrice == 0 {
			yesPrice = yes.BidPrice
		}
		noPrice := no.FilledPrice
		if noPrice == 0 {
			noPrice = no.BidPrice
		}

		spread := 1.0 - yesPrice - noPrice
		yesShares := yes.Size / yesPrice
		noShares := no.Size / noPrice
		mergeable := min(yesShares, noShares)
		grossProfit := mergeable * spread
		netProfit := grossProfit - mergeGasCost

		totalProfit += netProfit
		rotations++

		if yes.MergedAt != nil {
			totalCycleHours += yes.MergedAt.Sub(yes.PlacedAt).Hours()
		}
	}

	deployedOpen, deployedPartial, deployedFilled := pe.calculateDeployedCapital(ctx)
	deployed := deployedOpen + deployedPartial + deployedFilled
	balance = pe.cfg.InitialCapital + totalProfit - deployed

	if rotations > 0 {
		avgCycleHours = totalCycleHours / float64(rotations)
	}

	return balance, totalProfit, rotations, avgCycleHours
}

// calculateDeployedCapital returns capital broken down by order state.
func (pe *Engine) calculateDeployedCapital(ctx context.Context) (deployedOpen, deployedPartial, deployedFilled float64) {
	openOrders, _ := pe.store.GetOpenPaperOrders(ctx)
	filledOrders, _ := pe.store.GetAllPaperOrders(ctx, string(domain.PaperStatusFilled))

	for _, o := range openOrders {
		switch o.Status {
		case domain.PaperStatusOpen:
			deployedOpen += o.Size
		case domain.PaperStatusPartial:
			deployedPartial += o.Size
		}
	}
	for _, o := range filledOrders {
		deployedFilled += o.Size
	}
	return
}

// kellyFraction computes the optimal fraction of bankroll to deploy using Kelly Criterion.
func (pe *Engine) kellyFraction(ctx context.Context) float64 {
	stats, err := pe.store.GetPaperStats(ctx)
	if err != nil || stats.TotalOrders < 50 {
		return 1.0
	}

	totalPairsAttempted := stats.TotalOrders / 2
	if totalPairsAttempted == 0 {
		return 1.0
	}

	completed := stats.CompletePairs + stats.TotalRotations
	p := float64(completed) / float64(totalPairsAttempted)

	if stats.TotalRotations >= 5 && stats.TotalMergeProfit > 0 {
		avgProfitPerRotation := stats.TotalMergeProfit / float64(stats.TotalRotations)
		avgCapitalPerPair := 2 * pe.cfg.OrderSize

		b := avgProfitPerRotation / avgCapitalPerPair
		q := 1 - p

		if b > 0 {
			kelly := (p*b - q) / b
			halfKelly := kelly / 2.0

			if halfKelly < 0.25 {
				halfKelly = 0.25
			}
			if halfKelly > 1.0 {
				halfKelly = 1.0
			}

			slog.Debug("paper: Kelly computed from merge data",
				"p", fmt.Sprintf("%.2f", p),
				"b", fmt.Sprintf("%.4f", b),
				"fullKelly", fmt.Sprintf("%.2f", kelly),
				"halfKelly", fmt.Sprintf("%.2f", halfKelly),
			)
			return halfKelly
		}
	}

	fillRate := float64(stats.TotalFills) / float64(max(stats.DaysRunning, 1))
	switch {
	case fillRate > 4:
		return 1.0
	case fillRate > 2:
		return 0.9
	case fillRate > 1:
		return 0.8
	default:
		return 0.7
	}
}

// buildPositions reconstructs positions with block-based reward accrual and spread check.
func (pe *Engine) buildPositions(ctx context.Context, oppByCondition map[string]domain.Opportunity) ([]domain.PaperPosition, error) {
	allOrders, err := pe.store.GetAllPaperOrders(ctx, "")
	if err != nil {
		return nil, err
	}

	byPair := make(map[string][]domain.VirtualOrder)
	for _, o := range allOrders {
		byPair[o.PairID] = append(byPair[o.PairID], o)
	}

	var positions []domain.PaperPosition
	for pairID, orders := range byPair {
		pos := domain.PaperPosition{PairID: pairID}

		for i := range orders {
			o := &orders[i]
			if pos.ConditionID == "" {
				pos.ConditionID = o.ConditionID
				pos.Question = o.Question
			}

			switch o.Side {
			case "YES":
				pos.YesOrder = o
				pos.YesFilled = o.Status == domain.PaperStatusFilled || o.Status == domain.PaperStatusMerged
			case "NO":
				pos.NoOrder = o
				pos.NoFilled = o.Status == domain.PaperStatusFilled || o.Status == domain.PaperStatusMerged
			}
		}

		pos.IsComplete = pos.YesFilled && pos.NoFilled
		pos.IsResolved = allResolved(orders)

		if (pos.YesFilled && !pos.NoFilled) || (!pos.YesFilled && pos.NoFilled) {
			var filledAt *time.Time
			if pos.YesFilled && pos.YesOrder != nil && pos.YesOrder.FilledAt != nil {
				filledAt = pos.YesOrder.FilledAt
			} else if pos.NoFilled && pos.NoOrder != nil && pos.NoOrder.FilledAt != nil {
				filledAt = pos.NoOrder.FilledAt
			}
			pos.PartialSince = filledAt
		}

		if pos.YesOrder != nil && pos.NoOrder != nil {
			pos.FillCostPair = domain.FillCostPerEvent(
				pos.YesOrder.BidPrice, pos.NoOrder.BidPrice, pe.cfg.FeeRate)
			pos.CapitalDeployed = pos.YesOrder.Size + pos.NoOrder.Size
		}

		if pos.YesOrder != nil && pos.YesOrder.DailyReward > 0 {
			pos.DailyReward = pos.YesOrder.DailyReward
			activeHours := pe.activeHours(pos)
			blocks := float64(int(activeHours * 60 / blockMinutes))
			pos.RewardAccrued = pos.DailyReward * (blocks * blockMinutes / 60.0 / 24.0)
		}

		if opp, exists := oppByCondition[pos.ConditionID]; exists {
			pos.SpreadQualifies = opp.QualifiesReward
			pos.HoursToEnd = opp.Market.HoursToResolution()
		}

		positions = append(positions, pos)
	}

	return positions, nil
}

func (pe *Engine) activeHours(pos domain.PaperPosition) float64 {
	var earliest time.Time

	if pos.YesOrder != nil {
		earliest = pos.YesOrder.PlacedAt
	}
	if pos.NoOrder != nil && (earliest.IsZero() || pos.NoOrder.PlacedAt.Before(earliest)) {
		earliest = pos.NoOrder.PlacedAt
	}

	if earliest.IsZero() {
		return 0
	}

	latest := time.Now()
	if pos.IsComplete && pos.YesOrder != nil && pos.NoOrder != nil &&
		pos.YesOrder.FilledAt != nil && pos.NoOrder.FilledAt != nil {
		if pos.YesOrder.FilledAt.After(*pos.NoOrder.FilledAt) {
			latest = *pos.YesOrder.FilledAt
		} else {
			latest = *pos.NoOrder.FilledAt
		}
	}

	return latest.Sub(earliest).Hours()
}

// saveDailySummary persists today's paper trading summary.
func (pe *Engine) saveDailySummary(ctx context.Context, result *CycleResult) {
	today := time.Now().UTC().Truncate(24 * time.Hour)

	active, complete, partial := 0, 0, 0
	var totalPartialMins float64
	partialCount := 0
	fillsYes, fillsNo := 0, 0
	totalReward := 0.0
	fillPnL := 0.0

	for _, pos := range result.Positions {
		if pos.IsResolved {
			continue
		}
		hasActive := false
		if pos.YesOrder != nil {
			s := pos.YesOrder.Status
			if s == domain.PaperStatusOpen || s == domain.PaperStatusFilled || s == domain.PaperStatusPartial {
				hasActive = true
			}
		}
		if pos.NoOrder != nil {
			s := pos.NoOrder.Status
			if s == domain.PaperStatusOpen || s == domain.PaperStatusFilled || s == domain.PaperStatusPartial {
				hasActive = true
			}
		}
		if !hasActive {
			continue
		}

		active++
		if pos.IsComplete {
			complete++
			if pos.FillCostPair < 0 {
				fillPnL += -pos.FillCostPair * (pe.cfg.OrderSize / maxF(pos.YesOrder.BidPrice, 0.01))
			}
		}
		if pos.PartialSince != nil && !pos.IsComplete {
			partial++
			totalPartialMins += pos.PartialDuration().Minutes()
			partialCount++
		}
		if pos.YesFilled {
			fillsYes++
		}
		if pos.NoFilled {
			fillsNo++
		}
		totalReward += pos.RewardAccrued
	}

	avgPartial := 0.0
	if partialCount > 0 {
		avgPartial = totalPartialMins / float64(partialCount)
	}

	summary := domain.PaperDailySummary{
		Date:            today,
		ActivePositions: active,
		CompletePairs:   complete,
		PartialFills:    partial,
		TotalReward:     totalReward,
		TotalFillPnL:    fillPnL,
		NetPnL:          totalReward + fillPnL + result.MergeProfit,
		AvgPartialMins:  avgPartial,
		FillsYes:        fillsYes,
		FillsNo:         fillsNo,
		OrdersPlaced:    result.NewOrders,
		CapitalDeployed: result.CapitalDeployed,
		MarketsResolved: result.MarketsResolved,
		Rotations:       result.Merges,
		MergeProfit:     result.MergeProfit,
		CompoundBalance: result.CompoundBalance,
	}

	if err := pe.store.SavePaperDaily(ctx, summary); err != nil {
		slog.Warn("paper: error saving daily summary", "err", err)
	}
}

// compoundVelocityScore ranks opportunities by expected compound rotation speed.
func compoundVelocityScore(opp domain.Opportunity) float64 {
	yesQueue := engine.QueuePosition(opp.YesBook, opp.YesBook.BestBid())
	noQueue := engine.QueuePosition(opp.NoBook, opp.NoBook.BestBid())
	totalQueue := yesQueue + noQueue

	profitPerPair := -opp.FillCostPerPair
	if profitPerPair <= 0 {
		return 0
	}

	velocityFactor := 1.0
	if totalQueue > 0 {
		velocityFactor = 100.0 / (100.0 + totalQueue)
	}

	rewardBonus := 1.0 + opp.YourDailyReward*10
	return profitPerPair * velocityFactor * rewardBonus
}

// optimalOrderSize calculates competition-aware order size.
func (pe *Engine) optimalOrderSize(opp domain.Opportunity) float64 {
	competition := opp.YesBook.BidDepthWithinUSDC(0.05) + opp.NoBook.BidDepthWithinUSDC(0.05)
	if competition <= 0 {
		competition = opp.Competition
	}
	if competition <= 0 {
		competition = 1
	}

	dailyRate := opp.Market.Rewards.DailyRate
	if dailyRate <= 0 {
		return pe.cfg.OrderSize
	}

	attractiveness := dailyRate / competition
	baseAttractiveness := dailyRate / (competition + pe.cfg.OrderSize)

	scaleFactor := 1.0
	if baseAttractiveness > 0 {
		scaleFactor = attractiveness / baseAttractiveness
	}

	optimal := pe.cfg.OrderSize * scaleFactor
	if optimal > pe.cfg.OrderSize*2 {
		optimal = pe.cfg.OrderSize * 2
	}
	if optimal < minOrderSize {
		optimal = minOrderSize
	}

	if optimal != pe.cfg.OrderSize && (optimal < pe.cfg.OrderSize*0.8 || optimal > pe.cfg.OrderSize*1.2) {
		slog.Debug("paper: adaptive sizing",
			"market", engine.TruncateStr(opp.Market.Question, 25),
			"default", fmt.Sprintf("$%.0f", pe.cfg.OrderSize),
			"optimal", fmt.Sprintf("$%.0f", optimal),
			"bidCompetition", fmt.Sprintf("$%.0f", competition),
			"dailyRate", fmt.Sprintf("$%.2f", dailyRate),
		)
	}

	return optimal
}

func allResolved(orders []domain.VirtualOrder) bool {
	for _, o := range orders {
		if o.Status == domain.PaperStatusResolved {
			return true
		}
	}
	return false
}

func partialSide(pos domain.PaperPosition) string {
	if pos.YesFilled {
		return "YES"
	}
	if pos.NoFilled {
		return "NO"
	}
	return "NONE"
}

func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

