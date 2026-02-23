package scanner

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/alejandrodnm/polybot/internal/domain"
	"github.com/alejandrodnm/polybot/internal/ports"
	"github.com/google/uuid"
)

const (
	paperMaxMarkets      = 10
	paperMaxPartialHours = 6
	paperNearEndHours    = 24
	paperDefaultCapital  = 1000
	paperMinOrderSize    = 10.0

	// Bid optimization: try up to 3 one-cent tick-ups per side.
	paperMaxBidTickUp = 0.03
	paperBidTickStep  = 0.01

	// Merge realism: gas cost per merge tx (~$0.02 in POL gas), min delay before merging.
	paperMergeGasCost   = 0.02
	paperMergeDelayMins = 2

	// Stale rotation: rotate if competition grew more than this multiplier since placement.
	paperCompetitionMultiplier = 3.0

	// Stale rotation: time-based threshold.
	paperStaleHours = 4

	// Reward accrual: use 15-minute blocks instead of continuous hours.
	paperBlockMinutes = 15
)

// PaperConfig holds paper trading-specific settings.
type PaperConfig struct {
	OrderSize      float64
	MaxMarkets     int
	FeeRate        float64
	InitialCapital float64
}

// PaperEngine runs the paper trading simulation loop.
type PaperEngine struct {
	scanner  *Scanner
	trades   ports.TradeProvider
	store    ports.PaperStorage
	cfg      PaperConfig
	lastScan time.Time
}

// NewPaperEngine creates a paper trading engine.
func NewPaperEngine(
	scanner *Scanner,
	trades ports.TradeProvider,
	store ports.PaperStorage,
	cfg PaperConfig,
) *PaperEngine {
	if cfg.MaxMarkets <= 0 {
		cfg.MaxMarkets = paperMaxMarkets
	}
	if cfg.InitialCapital <= 0 {
		cfg.InitialCapital = paperDefaultCapital
	}
	return &PaperEngine{
		scanner:  scanner,
		trades:   trades,
		store:    store,
		cfg:      cfg,
		lastScan: time.Now().Add(-5 * time.Minute),
	}
}

// PaperCycleResult contains everything produced by one paper trading cycle.
type PaperCycleResult struct {
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

// RunOnce executes a single paper trading cycle with all gap fixes.
func (pe *PaperEngine) RunOnce(ctx context.Context) (*PaperCycleResult, error) {
	result := &PaperCycleResult{}

	// 1. Scan markets — get fresh opportunities with current books
	opps, err := pe.scanner.RunOnce(ctx)
	if err != nil {
		return nil, fmt.Errorf("paper.RunOnce: scan: %w", err)
	}

	// Build lookup: conditionID → opportunity (for queue refresh, spread check, etc.)
	oppByCondition := make(map[string]domain.Opportunity, len(opps))
	for _, opp := range opps {
		oppByCondition[opp.Market.ConditionID] = opp
	}

	// 2. Expire orders near resolution or for resolved markets
	resolved := pe.expireResolvedAndNearEnd(ctx, oppByCondition)
	result.MarketsResolved = resolved

	// 3. Refresh queue positions with current book data (OPEN orders only; PARTIAL keep their queue)
	pe.refreshQueues(ctx, oppByCondition)

	// 3.5. Rotate stale orders: time-based + spread-widened + competition-spiked
	staleExpired := pe.rotateStaleOrders(ctx, oppByCondition)
	if staleExpired > 0 {
		slog.Info("paper: rotated stale orders", "pairs_freed", staleExpired)
	}

	// 4. Check fills on existing open/partial orders (FIFO + partial fill tracking)
	fills, err := pe.checkFills(ctx)
	if err != nil {
		slog.Warn("paper: error checking fills", "err", err)
	}
	result.NewFills = fills

	// 4.5. Merge complete pairs → compound rotation (with gas cost + delay)
	merges, mergeProfit, err := pe.mergeCompletePairs(ctx)
	if err != nil {
		slog.Warn("paper: error merging pairs", "err", err)
	}
	result.Merges = merges
	result.MergeProfit = mergeProfit

	// Compute compound balance (initial + all merge returns - deployed)
	compoundBalance, totalMergeProfit, totalRotations, avgCycleHours := pe.getCompoundMetrics(ctx)
	result.CompoundBalance = compoundBalance
	result.TotalRotations = totalRotations
	result.AvgCycleHours = avgCycleHours

	// 5. Place new orders — with bid optimization and compound capital tracking
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

	// Kelly Criterion — determines max deployable fraction of bankroll
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

	// Sort opportunities by compound velocity score (shorter queues + higher profit = faster cycles)
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
		if hoursLeft > 0 && hoursLeft < paperNearEndHours {
			continue
		}
		if !opp.QualifiesReward {
			continue
		}

		// Competition-aware adaptive sizing, capped to affordable amount
		orderSize := pe.optimalOrderSize(opp)
		maxAffordable := (effectiveCapital - currentCapital) / 2
		if orderSize > maxAffordable {
			orderSize = maxAffordable
		}
		if orderSize < paperMinOrderSize {
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

	// 6. Build positions with reward accrual + spread check
	positions, err := pe.buildPositions(ctx, oppByCondition)
	if err != nil {
		slog.Warn("paper: error building positions", "err", err)
	}
	result.Positions = positions

	// 7. Calculate total reward accrued
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

	// Count complete pairs and detect partial alerts
	for _, pos := range positions {
		if pos.IsComplete {
			result.CompletePairs++
		}
		if pos.PartialSince != nil && !pos.IsComplete && !pos.IsResolved {
			dur := pos.PartialDuration()
			if dur > paperMaxPartialHours*time.Hour {
				alert := fmt.Sprintf("PARTIAL >%dh: %s (%s filled %.0fh ago)",
					paperMaxPartialHours, pos.Question, partialSide(pos), dur.Hours())
				result.PartialAlerts = append(result.PartialAlerts, alert)
				slog.Warn("paper: long partial fill", "market", pos.Question,
					"side", partialSide(pos), "hours", dur.Hours())
			}
		}
		if pos.HoursToEnd > 0 && pos.HoursToEnd < 48 && !pos.IsResolved {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("NEAR END (%.0fh): %s", pos.HoursToEnd, truncateStr(pos.Question, 30)))
		}
	}

	// 8. Save daily summary
	pe.saveDailySummary(ctx, result)

	pe.lastScan = time.Now()
	return result, nil
}

// placeVirtualOrders creates a YES+NO order pair with default config size.
func (pe *PaperEngine) placeVirtualOrders(ctx context.Context, opp domain.Opportunity) error {
	return pe.placeVirtualOrdersWithSize(ctx, opp, pe.cfg.OrderSize)
}

// placeVirtualOrdersWithSize creates a YES+NO order pair with multi-tick bid optimization.
// YES and NO bids are optimized jointly to ensure yesBid + noBid < 1.0 after any tick-ups.
func (pe *PaperEngine) placeVirtualOrdersWithSize(ctx context.Context, opp domain.Opportunity, orderSize float64) error {
	pairID := uuid.New().String()
	now := time.Now().UTC()

	yesBid := opp.YesBook.BestBid()
	noBid := opp.NoBook.BestBid()
	if yesBid == 0 {
		yesBid = opp.YesBook.BestAsk() * 0.99
	}
	if noBid == 0 {
		noBid = opp.NoBook.BestAsk() * 0.99
	}

	yesQueue := queuePosition(opp.YesBook, yesBid)
	noQueue := queuePosition(opp.NoBook, noBid)

	// Multi-tick bid optimization: try 1c, 2c, 3c tick-ups.
	// Optimize YES first, then NO, maintaining joint profitability constraint.
	yesBidOpt, yesQueueOpt := pe.optimizeBid(opp.YesBook, yesBid, noBid, orderSize, pe.cfg.FeeRate, true)
	noBidOpt, noQueueOpt := pe.optimizeBid(opp.NoBook, noBid, yesBidOpt, orderSize, pe.cfg.FeeRate, false)

	// Safety guard: if joint optimization breaks profitability, fall back to originals
	if domain.FillCostPerEvent(yesBidOpt, noBidOpt, pe.cfg.FeeRate) > 0 {
		yesBidOpt = yesBid
		noBidOpt = noBid
		yesQueueOpt = yesQueue
		noQueueOpt = noQueue
	}

	optimized := yesBidOpt != yesBid || noBidOpt != noBid

	// Use bid-depth-only competition for reward estimation
	bidCompetition := opp.YesBook.BidDepthWithinUSDC(0.05) + opp.NoBook.BidDepthWithinUSDC(0.05)

	yesOrder := domain.VirtualOrder{
		ID:          uuid.New().String(),
		ConditionID: opp.Market.ConditionID,
		TokenID:     opp.Market.YesToken().TokenID,
		Side:        "YES",
		BidPrice:    yesBidOpt,
		Size:        orderSize,
		PlacedAt:    now,
		Status:      domain.PaperStatusOpen,
		PairID:      pairID,
		Question:    opp.Market.Question,
		QueueAhead:  yesQueueOpt,
		DailyReward: opp.YourDailyReward,
		EndDate:     opp.Market.EndDate,
	}

	noOrder := domain.VirtualOrder{
		ID:          uuid.New().String(),
		ConditionID: opp.Market.ConditionID,
		TokenID:     opp.Market.NoToken().TokenID,
		Side:        "NO",
		BidPrice:    noBidOpt,
		Size:        orderSize,
		PlacedAt:    now,
		Status:      domain.PaperStatusOpen,
		PairID:      pairID,
		Question:    opp.Market.Question,
		QueueAhead:  noQueueOpt,
		DailyReward: opp.YourDailyReward,
		EndDate:     opp.Market.EndDate,
	}

	if err := pe.store.SavePaperOrder(ctx, yesOrder); err != nil {
		return err
	}
	if err := pe.store.SavePaperOrder(ctx, noOrder); err != nil {
		return err
	}

	optLabel := ""
	if optimized {
		optLabel = fmt.Sprintf(" [BID OPT Y+%.0fc N+%.0fc]",
			(yesBidOpt-yesBid)*100, (noBidOpt-noBid)*100)
	}
	sizeLabel := ""
	if orderSize != pe.cfg.OrderSize {
		sizeLabel = fmt.Sprintf(" [ADAPTIVE $%.0f]", orderSize)
	}
	slog.Info("paper: placed virtual orders"+optLabel+sizeLabel,
		"market", truncateStr(opp.Market.Question, 40),
		"yesBid", fmt.Sprintf("%.4f", yesBidOpt),
		"noBid", fmt.Sprintf("%.4f", noBidOpt),
		"size", fmt.Sprintf("$%.0f", orderSize),
		"yesQueue", fmt.Sprintf("$%.0f", yesQueueOpt),
		"noQueue", fmt.Sprintf("$%.0f", noQueueOpt),
		"bidCompetition", fmt.Sprintf("$%.0f", bidCompetition),
		"reward", fmt.Sprintf("$%.4f/d", opp.YourDailyReward),
		"endIn", fmt.Sprintf("%.0fh", opp.Market.HoursToResolution()),
	)

	return nil
}

// optimizeBid tries 1c, 2c, 3c tick-ups on a bid and picks the best one.
// The other side's bid (counterBid) is held fixed for joint profitability checks.
// isYesSide=true means we're optimizing YES (counterBid is NO), false means we're optimizing NO.
func (pe *PaperEngine) optimizeBid(
	book domain.OrderBook,
	currentBid, counterBid, orderSize, feeRate float64,
	isYesSide bool,
) (bestBid float64, bestQueue float64) {
	bestBid = currentBid
	bestQueue = queuePosition(book, currentBid)

	// Only optimize if queue ahead is large enough to justify it
	if bestQueue <= orderSize {
		return
	}

	bestScore := bidOptScore(bestQueue, orderSize, 0.0)

	for ticks := 1; float64(ticks)*paperBidTickStep <= paperMaxBidTickUp; ticks++ {
		candidate := currentBid + float64(ticks)*paperBidTickStep

		// Ensure joint profitability after tick-up
		var fillCost float64
		if isYesSide {
			fillCost = domain.FillCostPerEvent(candidate, counterBid, feeRate)
		} else {
			fillCost = domain.FillCostPerEvent(counterBid, candidate, feeRate)
		}
		if fillCost > 0 {
			break // would destroy profitability, stop trying
		}

		newQueue := queuePosition(book, candidate)
		tickCost := float64(ticks) * paperBidTickStep * orderSize
		score := bidOptScore(newQueue, orderSize, tickCost)

		if score > bestScore {
			bestScore = score
			bestBid = candidate
			bestQueue = newQueue
		}
	}

	return bestBid, bestQueue
}

// bidOptScore ranks a bid optimization choice: shorter queue is good, but tick cost reduces profit.
// Returns expected "effective speed" = orderSize / (queue + orderSize) - normalized tick cost.
func bidOptScore(queue, orderSize, tickCost float64) float64 {
	if orderSize <= 0 {
		return 0
	}
	fillSpeedProxy := orderSize / (queue + orderSize + 1)
	return fillSpeedProxy - tickCost/orderSize
}

// expireResolvedAndNearEnd handles market resolution and near-end expiry.
func (pe *PaperEngine) expireResolvedAndNearEnd(ctx context.Context, oppByCondition map[string]domain.Opportunity) int {
	openOrders, err := pe.store.GetOpenPaperOrders(ctx)
	if err != nil {
		return 0
	}

	resolved := 0
	seenConditions := make(map[string]bool)

	for _, order := range openOrders {
		if seenConditions[order.ConditionID] {
			continue
		}

		shouldExpire := false
		reason := ""

		// Check 1: endDate has passed → market resolved
		if !order.EndDate.IsZero() && time.Now().After(order.EndDate) {
			shouldExpire = true
			reason = "RESOLVED"
		}

		// Check 2: market no longer appears in scan → may have resolved or closed
		if _, exists := oppByCondition[order.ConditionID]; !exists {
			if !order.EndDate.IsZero() && time.Until(order.EndDate) < 0 {
				shouldExpire = true
				reason = "RESOLVED (disappeared)"
			}
		}

		// Check 3: too close to resolution
		if !order.EndDate.IsZero() {
			hoursLeft := time.Until(order.EndDate).Hours()
			if hoursLeft > 0 && hoursLeft < paperNearEndHours {
				shouldExpire = true
				reason = fmt.Sprintf("NEAR END (%.0fh left)", hoursLeft)
			}
		}

		if shouldExpire {
			seenConditions[order.ConditionID] = true
			slog.Warn("paper: expiring orders",
				"reason", reason,
				"market", truncateStr(order.Question, 30),
				"conditionID", order.ConditionID[:14]+"...",
			)
			if err := pe.store.ExpirePaperOrders(ctx, order.ConditionID); err != nil {
				slog.Warn("paper: error expiring orders", "err", err)
			}
			resolved++
		}
	}

	return resolved
}

// refreshQueues updates queueAhead for OPEN orders using current book data.
// PARTIAL orders keep their original queue so fill calculation stays consistent.
func (pe *PaperEngine) refreshQueues(ctx context.Context, oppByCondition map[string]domain.Opportunity) {
	openOrders, err := pe.store.GetOpenPaperOrders(ctx)
	if err != nil {
		return
	}

	for _, order := range openOrders {
		// Skip PARTIAL orders — their queue was already consumed in prior cycles.
		if order.Status == domain.PaperStatusPartial {
			continue
		}

		opp, exists := oppByCondition[order.ConditionID]
		if !exists {
			continue
		}

		var book domain.OrderBook
		if order.Side == "YES" {
			book = opp.YesBook
		} else {
			book = opp.NoBook
		}

		newQueue := queuePosition(book, order.BidPrice)
		if err := pe.store.UpdatePaperOrderQueue(ctx, order.ID, newQueue); err != nil {
			slog.Debug("paper: error updating queue", "err", err)
		}
	}
}

// checkFills fetches recent trades and simulates queue-aware filling with partial fill support.
//
// Fill model:
//   - Accumulate SELL volume (at or below bid) from order.PlacedAt
//   - When cumSellUSDC > QueueAhead: we start getting filled
//   - effectiveFilled = cumSellUSDC - QueueAhead (capped at order.Size)
//   - If effectiveFilled > order.FilledSize: new fill detected
//   - Partial: effectiveFilled < order.Size → update FilledSize, set PARTIAL
//   - Complete: effectiveFilled >= order.Size → mark FILLED
func (pe *PaperEngine) checkFills(ctx context.Context) (int, error) {
	openOrders, err := pe.store.GetOpenPaperOrders(ctx)
	if err != nil {
		return 0, fmt.Errorf("paper.checkFills: get open orders: %w", err)
	}

	if len(openOrders) == 0 {
		return 0, nil
	}

	byToken := make(map[string][]domain.VirtualOrder)
	for _, o := range openOrders {
		byToken[o.TokenID] = append(byToken[o.TokenID], o)
	}

	totalFills := 0
	for tokenID, orders := range byToken {
		trades, err := pe.trades.FetchTrades(ctx, tokenID)
		if err != nil {
			slog.Warn("paper: error fetching trades for fill check",
				"token", tokenID[:min(8, len(tokenID))]+"...", "err", err)
			continue
		}

		if len(trades) > 0 {
			window := tradeCoverage(trades)
			if window < time.Hour {
				slog.Debug("paper: thin trade data",
					"token", tokenID[:min(8, len(tokenID))]+"...",
					"trades", len(trades),
					"coverage", fmt.Sprintf("%.0fm", window.Minutes()),
				)
			}
		}

		// Sort trades by timestamp: oldest first for proper FIFO queue simulation
		sort.Slice(trades, func(i, j int) bool {
			return trades[i].Timestamp.Before(trades[j].Timestamp)
		})

		for _, order := range orders {
			// Accumulate total SELL volume at or below our bid, from PlacedAt
			var cumSellUSDC float64
			var lastSellTrade *domain.Trade

			for i := range trades {
				t := &trades[i]
				if t.Timestamp.Before(order.PlacedAt) {
					continue
				}
				// Only SELL trades at or below our bid price consume our queue position.
				// (Taker sells into bids; maker buys at bid price.)
				if t.Side != "SELL" || t.Price > order.BidPrice {
					continue
				}
				cumSellUSDC += t.Size * t.Price
				lastSellTrade = t
			}

			// How much of our order has been reached by sell flow
			effectiveFilled := cumSellUSDC - order.QueueAhead
			if effectiveFilled <= 0 {
				// Queue not yet consumed
				if cumSellUSDC > 0 {
					slog.Debug("paper: sell volume hasn't reached us yet",
						"side", order.Side,
						"market", truncateStr(order.Question, 25),
						"sellVol", fmt.Sprintf("$%.0f", cumSellUSDC),
						"queueAhead", fmt.Sprintf("$%.0f", order.QueueAhead),
						"needed", fmt.Sprintf("$%.0f", order.QueueAhead+order.Size),
					)
				}
				continue
			}

			// Cap at order size
			if effectiveFilled > order.Size {
				effectiveFilled = order.Size
			}

			// Detect new fill progress since last check
			newlyFilled := effectiveFilled - order.FilledSize
			if newlyFilled <= 0 {
				continue // no new fills this cycle
			}

			fillPrice := order.BidPrice // maker limit orders fill at their bid price
			fillTime := time.Now().UTC()
			if lastSellTrade != nil {
				fillTime = lastSellTrade.Timestamp
			}

			if effectiveFilled >= order.Size {
				// Complete fill
				if err := pe.store.MarkPaperOrderFilled(ctx, order.ID, fillTime, fillPrice); err != nil {
					slog.Warn("paper: error marking order filled", "err", err)
					continue
				}
				fill := domain.PaperFill{
					OrderID:   order.ID,
					TradeID:   tradeID(lastSellTrade),
					Price:     fillPrice,
					Size:      order.Size,
					Timestamp: fillTime,
				}
				if err := pe.store.SavePaperFill(ctx, fill); err != nil {
					slog.Warn("paper: error saving fill", "err", err)
				}

				slog.Info("paper: order FILLED",
					"side", order.Side,
					"market", truncateStr(order.Question, 30),
					"bidPrice", fmt.Sprintf("%.4f", fillPrice),
					"queueAhead", fmt.Sprintf("$%.0f", order.QueueAhead),
					"totalSellVol", fmt.Sprintf("$%.0f", cumSellUSDC),
					"prevFilled", fmt.Sprintf("$%.2f", order.FilledSize),
				)
				totalFills++
			} else {
				// Partial fill: update FilledSize, keep order active
				if err := pe.store.UpdatePaperOrderPartialFill(ctx, order.ID, effectiveFilled, fillPrice); err != nil {
					slog.Warn("paper: error updating partial fill", "err", err)
					continue
				}
				fill := domain.PaperFill{
					OrderID:   order.ID,
					TradeID:   tradeID(lastSellTrade),
					Price:     fillPrice,
					Size:      newlyFilled,
					Timestamp: fillTime,
				}
				if err := pe.store.SavePaperFill(ctx, fill); err != nil {
					slog.Warn("paper: error saving partial fill", "err", err)
				}

				pct := 100 * effectiveFilled / order.Size
				slog.Info("paper: order PARTIAL FILL",
					"side", order.Side,
					"market", truncateStr(order.Question, 30),
					"filled", fmt.Sprintf("$%.2f / $%.2f (%.0f%%)", effectiveFilled, order.Size, pct),
					"newThisCycle", fmt.Sprintf("$%.2f", newlyFilled),
				)
			}
		}
	}

	return totalFills, nil
}

// tradeID returns a trade ID string or empty string if nil.
func tradeID(t *domain.Trade) string {
	if t == nil {
		return ""
	}
	return t.ID
}

// buildPositions reconstructs positions with block-based reward accrual and spread check.
func (pe *PaperEngine) buildPositions(ctx context.Context, oppByCondition map[string]domain.Opportunity) ([]domain.PaperPosition, error) {
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

		// Partial detection: one side filled, other not
		if (pos.YesFilled && !pos.NoFilled) || (!pos.YesFilled && pos.NoFilled) {
			var filledAt *time.Time
			if pos.YesFilled && pos.YesOrder != nil && pos.YesOrder.FilledAt != nil {
				filledAt = pos.YesOrder.FilledAt
			} else if pos.NoFilled && pos.NoOrder != nil && pos.NoOrder.FilledAt != nil {
				filledAt = pos.NoOrder.FilledAt
			}
			pos.PartialSince = filledAt
		}

		// Fill cost for the pair (with maker fee = config rate, typically 0%)
		if pos.YesOrder != nil && pos.NoOrder != nil {
			pos.FillCostPair = domain.FillCostPerEvent(
				pos.YesOrder.BidPrice, pos.NoOrder.BidPrice, pe.cfg.FeeRate)
			pos.CapitalDeployed = pos.YesOrder.Size + pos.NoOrder.Size
		}

		// Block-based reward accrual: accrue in discrete 15-minute blocks.
		// This prevents inflating rewards with fractional time and matches
		// Polymarket's actual reward settlement cadence.
		if pos.YesOrder != nil && pos.YesOrder.DailyReward > 0 {
			pos.DailyReward = pos.YesOrder.DailyReward
			activeHours := pe.activeHours(pos)
			blocks := float64(int(activeHours * 60 / paperBlockMinutes)) // floor to whole blocks
			pos.RewardAccrued = pos.DailyReward * (blocks * paperBlockMinutes / 60.0 / 24.0)
		}

		// Check spread qualification with current data
		if opp, exists := oppByCondition[pos.ConditionID]; exists {
			pos.SpreadQualifies = opp.QualifiesReward
			pos.HoursToEnd = opp.Market.HoursToResolution()
		}

		positions = append(positions, pos)
	}

	return positions, nil
}

// activeHours returns how many hours orders in this position have been active.
func (pe *PaperEngine) activeHours(pos domain.PaperPosition) float64 {
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
		// Use the LATER fill time (when both sides were done)
		if pos.YesOrder.FilledAt.After(*pos.NoOrder.FilledAt) {
			latest = *pos.YesOrder.FilledAt
		} else {
			latest = *pos.NoOrder.FilledAt
		}
	}

	return latest.Sub(earliest).Hours()
}

// calculateDeployedCapital returns capital broken down by order state.
//
//   - OPEN orders: full Size reserved (not yet filled, can't be used)
//   - PARTIAL orders: unfilled portion still reserved + filled portion invested in tokens
//   - FILLED orders: full Size invested in tokens (awaiting merge)
func (pe *PaperEngine) calculateDeployedCapital(ctx context.Context) (deployedOpen, deployedPartial, deployedFilled float64) {
	openOrders, _ := pe.store.GetOpenPaperOrders(ctx) // returns OPEN + PARTIAL
	filledOrders, _ := pe.store.GetAllPaperOrders(ctx, string(domain.PaperStatusFilled))

	for _, o := range openOrders {
		switch o.Status {
		case domain.PaperStatusOpen:
			deployedOpen += o.Size
		case domain.PaperStatusPartial:
			// Remaining unfilled portion is still in the order book
			deployedPartial += o.Size
		}
	}
	for _, o := range filledOrders {
		deployedFilled += o.Size
	}
	return
}

// saveDailySummary persists today's paper trading summary.
func (pe *PaperEngine) saveDailySummary(ctx context.Context, result *PaperCycleResult) {
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

// tradeCoverage returns the time span covered by the trade data.
func tradeCoverage(trades []domain.Trade) time.Duration {
	if len(trades) == 0 {
		return 0
	}
	var oldest, newest time.Time
	for _, t := range trades {
		if t.Timestamp.IsZero() {
			continue
		}
		if oldest.IsZero() || t.Timestamp.Before(oldest) {
			oldest = t.Timestamp
		}
		if newest.IsZero() || t.Timestamp.After(newest) {
			newest = t.Timestamp
		}
	}
	return newest.Sub(oldest)
}

func allResolved(orders []domain.VirtualOrder) bool {
	for _, o := range orders {
		if o.Status == domain.PaperStatusResolved {
			return true
		}
	}
	return false
}

// queuePosition returns the USDC value of bids at EXACTLY the same price level.
// FIFO applies within a price level: only same-price bids are ahead of us.
// Bids at higher prices will have already been filled before the market price reaches our level.
func queuePosition(book domain.OrderBook, bidPrice float64) float64 {
	total := 0.0
	for _, entry := range book.Bids {
		// Only count bids at the SAME price level (FIFO within level)
		if abs64(entry.Price-bidPrice) < 0.001 {
			total += entry.Size * entry.Price
		}
	}
	return total
}

func abs64(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
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

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// mergeCompletePairs finds all pairs with both sides FILLED and simulates
// the CTF merge (YES+NO → $1.00), with:
//   - Gas cost deducted (paperMergeGasCost per merge)
//   - Minimum delay after last fill (paperMergeDelayMins)
//   - Skip if merge is unprofitable after gas
func (pe *PaperEngine) mergeCompletePairs(ctx context.Context) (merges int, totalProfit float64, err error) {
	filledOrders, err := pe.store.GetAllPaperOrders(ctx, string(domain.PaperStatusFilled))
	if err != nil {
		return 0, 0, fmt.Errorf("paper.mergeCompletePairs: %w", err)
	}

	byPair := make(map[string][]domain.VirtualOrder)
	for _, o := range filledOrders {
		byPair[o.PairID] = append(byPair[o.PairID], o)
	}

	now := time.Now().UTC()
	mergeDelay := time.Duration(paperMergeDelayMins) * time.Minute

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

		// Wait at least paperMergeDelayMins after the LAST fill before merging
		// (simulates blockchain tx confirmation time)
		lastFillTime := yes.PlacedAt
		if yes.FilledAt != nil && yes.FilledAt.After(lastFillTime) {
			lastFillTime = *yes.FilledAt
		}
		if no.FilledAt != nil && no.FilledAt.After(lastFillTime) {
			lastFillTime = *no.FilledAt
		}
		if now.Sub(lastFillTime) < mergeDelay {
			slog.Debug("paper: merge delayed (waiting for confirmation)",
				"market", truncateStr(yes.Question, 30),
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

		// Spread = $1 - YES price - NO price (gross profit per merged share pair)
		spread := 1.0 - yesPrice - noPrice
		yesShares := yes.Size / yesPrice
		noShares := no.Size / noPrice
		mergeable := min(yesShares, noShares)
		grossProfit := mergeable * spread

		// Deduct gas cost; skip merge if not profitable
		netProfit := grossProfit - paperMergeGasCost
		if netProfit <= 0 {
			slog.Info("paper: skipping merge (unprofitable after gas)",
				"market", truncateStr(yes.Question, 30),
				"spread", fmt.Sprintf("$%.4f", spread),
				"grossProfit", fmt.Sprintf("$%.4f", grossProfit),
				"gasCost", fmt.Sprintf("$%.4f", paperMergeGasCost),
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
			"market", truncateStr(yes.Question, 30),
			"spread", fmt.Sprintf("$%.4f", spread),
			"shares", fmt.Sprintf("%.1f", mergeable),
			"grossProfit", fmt.Sprintf("$%.4f", grossProfit),
			"netProfit", fmt.Sprintf("$%.4f", netProfit),
			"gasCost", fmt.Sprintf("$%.4f", paperMergeGasCost),
			"capital_used", fmt.Sprintf("$%.2f", capitalUsed),
			"cycle", fmt.Sprintf("%.1fh", cycleTime.Hours()),
		)

		merges++
		totalProfit += netProfit
	}

	return merges, totalProfit, nil
}

// getCompoundMetrics computes the compound balance and rotation stats from all MERGED orders.
func (pe *PaperEngine) getCompoundMetrics(ctx context.Context) (balance, totalProfit float64, rotations int, avgCycleHours float64) {
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
		netProfit := grossProfit - paperMergeGasCost

		totalProfit += netProfit
		rotations++

		if yes.MergedAt != nil {
			totalCycleHours += yes.MergedAt.Sub(yes.PlacedAt).Hours()
		}
	}

	// Balance = initial + merged profits - capital still deployed
	deployedOpen, deployedPartial, deployedFilled := pe.calculateDeployedCapital(ctx)
	deployed := deployedOpen + deployedPartial + deployedFilled
	balance = pe.cfg.InitialCapital + totalProfit - deployed

	if rotations > 0 {
		avgCycleHours = totalCycleHours / float64(rotations)
	}

	return balance, totalProfit, rotations, avgCycleHours
}

// kellyFraction computes the optimal fraction of bankroll to deploy using
// the Kelly Criterion. Uses half-Kelly for safety.
//
//	f* = (p × b - q) / b
//	p = probability of completing a pair (both fills)
//	b = profit / capital when pair completes
//	q = 1 - p
func (pe *PaperEngine) kellyFraction(ctx context.Context) float64 {
	stats, err := pe.store.GetPaperStats(ctx)
	if err != nil || stats.TotalOrders < 50 {
		return 1.0 // warmup: full deployment until we have ~5 full cycles of data
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

// rotateStaleOrders cancels order pairs where BOTH sides are still OPEN based on:
//  1. Time-based: both sides OPEN > paperStaleHours with no fills
//  2. Spread-widened: current spread exceeds the strategy max (market conditions deteriorated)
//  3. Competition spike: competition grew 3x since placement (reward diluted too much)
func (pe *PaperEngine) rotateStaleOrders(ctx context.Context, oppByCondition map[string]domain.Opportunity) int {
	openOrders, err := pe.store.GetOpenPaperOrders(ctx)
	if err != nil {
		return 0
	}

	byPair := make(map[string][]domain.VirtualOrder)
	for _, o := range openOrders {
		// Only consider OPEN orders (not PARTIAL — one side is filling, keep it)
		if o.Status == domain.PaperStatusOpen {
			byPair[o.PairID] = append(byPair[o.PairID], o)
		}
	}

	expired := 0
	for _, orders := range byPair {
		if len(orders) < 2 {
			continue
		}

		// Check both are still OPEN (not one PARTIAL, one OPEN)
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

		// Reason 1: stale by time
		if age >= paperStaleHours {
			rotateReason = fmt.Sprintf("stale (%.1fh, no fills)", age)
		}

		// Reason 2: spread widened beyond max (market deteriorated)
		if rotateReason == "" {
			if opp, exists := oppByCondition[conditionID]; exists {
				if opp.FillCostPerPair > 0 {
					rotateReason = fmt.Sprintf("spread no longer profitable (fillCost $%.4f > 0)", opp.FillCostPerPair)
				}
			}
		}

		// Reason 3: competition spiked 3x since placement
		if rotateReason == "" {
			if opp, exists := oppByCondition[conditionID]; exists {
				// Use bid-depth-only competition (the realistic measure for reward farming)
				currentComp := opp.YesBook.BidDepthWithinUSDC(0.05) + opp.NoBook.BidDepthWithinUSDC(0.05)
				// Compare against what was recorded at placement (orders[0] has the original competition proxy)
				// We use QueueAhead as a proxy for original competition at placement time
				originalCompProxy := orders[0].QueueAhead + orders[1].QueueAhead
				if originalCompProxy > 0 && currentComp > originalCompProxy*paperCompetitionMultiplier {
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
			"market", truncateStr(orders[0].Question, 30),
			"age", fmt.Sprintf("%.1fh", age),
		)
		expired++
	}

	return expired
}

// compoundVelocityScore ranks opportunities by expected compound rotation speed.
func compoundVelocityScore(opp domain.Opportunity) float64 {
	yesQueue := queuePosition(opp.YesBook, opp.YesBook.BestBid())
	noQueue := queuePosition(opp.NoBook, opp.NoBook.BestBid())
	totalQueue := yesQueue + noQueue

	profitPerPair := -opp.FillCostPerPair
	if profitPerPair <= 0 {
		return 0
	}

	// Velocity: inverse of queue depth (shorter queue = faster fill)
	velocityFactor := 1.0
	if totalQueue > 0 {
		velocityFactor = 100.0 / (100.0 + totalQueue)
	}

	// Reward bonus: earning rewards while waiting is gravy
	rewardBonus := 1.0 + opp.YourDailyReward*10

	return profitPerPair * velocityFactor * rewardBonus
}

// optimalOrderSize calculates the competition-aware order size that maximizes
// reward per dollar deployed. (Strategy 2: Geometric Reward Maximization)
//
// The marginal reward curve is concave: dR/ds = dailyRate × C / (s+C)².
// For low-competition markets we deploy more; for high-competition less.
func (pe *PaperEngine) optimalOrderSize(opp domain.Opportunity) float64 {
	// Use bid-only competition (more accurate for reward farming)
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
	if optimal < paperMinOrderSize {
		optimal = paperMinOrderSize
	}

	if optimal != pe.cfg.OrderSize && (optimal < pe.cfg.OrderSize*0.8 || optimal > pe.cfg.OrderSize*1.2) {
		slog.Debug("paper: adaptive sizing",
			"market", truncateStr(opp.Market.Question, 25),
			"default", fmt.Sprintf("$%.0f", pe.cfg.OrderSize),
			"optimal", fmt.Sprintf("$%.0f", optimal),
			"bidCompetition", fmt.Sprintf("$%.0f", competition),
			"dailyRate", fmt.Sprintf("$%.2f", dailyRate),
		)
	}

	return optimal
}
