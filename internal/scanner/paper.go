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
	paperMaxMarkets      = 3
	paperMaxPartialHours = 6
	paperNearEndHours    = 24 // expire orders when < 24h to resolution
	paperBidTickUp       = 0.01
	paperMaxCapitalTotal = 2000 // hard cap: never deploy more than this
)

// PaperConfig holds paper trading-specific settings.
type PaperConfig struct {
	OrderSize  float64
	MaxMarkets int
	FeeRate    float64
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

	// 2. Expire orders near resolution or for resolved markets (GAP #3, #6, #7)
	resolved := pe.expireResolvedAndNearEnd(ctx, oppByCondition)
	result.MarketsResolved = resolved

	// 3. Refresh queue positions with current book data (GAP #4)
	pe.refreshQueues(ctx, oppByCondition)

	// 4. Check fills on existing open orders (with queue-adjusted logic)
	fills, err := pe.checkFills(ctx)
	if err != nil {
		slog.Warn("paper: error checking fills", "err", err)
	}
	result.NewFills = fills

	// 5. Place new orders — with bid optimization (GAP #7, #8) and capital tracking (GAP #9)
	activeConditions, err := pe.store.GetActivePaperConditions(ctx)
	if err != nil {
		slog.Warn("paper: error getting active conditions", "err", err)
	}

	activeSet := make(map[string]bool, len(activeConditions))
	for _, c := range activeConditions {
		activeSet[c] = true
	}

	// Calculate current capital deployed (GAP #9)
	currentCapital := pe.calculateDeployedCapital(ctx)
	result.CapitalDeployed = currentCapital

	newOrders := 0
	for _, opp := range opps {
		if len(activeConditions)+newOrders/2 >= pe.cfg.MaxMarkets {
			break
		}
		if activeSet[opp.Market.ConditionID] {
			continue
		}
		// Only FILLS=PROFIT markets (with 0% maker fee, this is yesP + noP < 1.0)
		if opp.FillCostPerPair > 0 {
			continue
		}
		if opp.YourDailyReward <= 0 {
			continue
		}
		// GAP #7: skip markets too close to resolution
		hoursLeft := opp.Market.HoursToResolution()
		if hoursLeft > 0 && hoursLeft < paperNearEndHours {
			continue
		}
		// GAP #5: skip if spread doesn't qualify for rewards
		if !opp.QualifiesReward {
			continue
		}
		// GAP #9: capital limit
		orderCapital := pe.cfg.OrderSize * 2
		if currentCapital+orderCapital > paperMaxCapitalTotal {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("capital limit reached: $%.0f/$%.0f", currentCapital, float64(paperMaxCapitalTotal)))
			break
		}

		if err := pe.placeVirtualOrders(ctx, opp); err != nil {
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

	// 6. Build positions with reward accrual + spread check (GAP #1, #5)
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
		// GAP #6: warn about positions near resolution
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

// placeVirtualOrders creates a YES+NO order pair with bid optimization (GAP #8).
func (pe *PaperEngine) placeVirtualOrders(ctx context.Context, opp domain.Opportunity) error {
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

	// GAP #8: Bid optimization — if queue is large, bid 1 tick higher
	// to get to front of queue, but only if FILLS=PROFIT is maintained.
	yesBidOpt, noBidOpt := yesBid, noBid
	optimized := false
	if yesQueue > pe.cfg.OrderSize {
		candidate := yesBid + paperBidTickUp
		if domain.FillCostPerEvent(candidate, noBid, pe.cfg.FeeRate) <= 0 {
			yesBidOpt = candidate
			yesQueue = 0 // front of new price level
			optimized = true
		}
	}
	if noQueue > pe.cfg.OrderSize {
		candidate := noBid + paperBidTickUp
		if domain.FillCostPerEvent(yesBidOpt, candidate, pe.cfg.FeeRate) <= 0 {
			noBidOpt = candidate
			noQueue = 0
			optimized = true
		}
	}

	yesOrder := domain.VirtualOrder{
		ID:          uuid.New().String(),
		ConditionID: opp.Market.ConditionID,
		TokenID:     opp.Market.YesToken().TokenID,
		Side:        "YES",
		BidPrice:    yesBidOpt,
		Size:        pe.cfg.OrderSize,
		PlacedAt:    now,
		Status:      domain.PaperStatusOpen,
		PairID:      pairID,
		Question:    opp.Market.Question,
		QueueAhead:  yesQueue,
		DailyReward: opp.YourDailyReward,
		EndDate:     opp.Market.EndDate,
	}

	noOrder := domain.VirtualOrder{
		ID:          uuid.New().String(),
		ConditionID: opp.Market.ConditionID,
		TokenID:     opp.Market.NoToken().TokenID,
		Side:        "NO",
		BidPrice:    noBidOpt,
		Size:        pe.cfg.OrderSize,
		PlacedAt:    now,
		Status:      domain.PaperStatusOpen,
		PairID:      pairID,
		Question:    opp.Market.Question,
		QueueAhead:  noQueue,
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
		optLabel = " [BID OPTIMIZED +1c]"
	}
	slog.Info("paper: placed virtual orders"+optLabel,
		"market", truncateStr(opp.Market.Question, 40),
		"yesBid", fmt.Sprintf("%.4f", yesBidOpt),
		"noBid", fmt.Sprintf("%.4f", noBidOpt),
		"yesQueue", fmt.Sprintf("$%.0f", yesQueue),
		"noQueue", fmt.Sprintf("$%.0f", noQueue),
		"reward", fmt.Sprintf("$%.4f/d", opp.YourDailyReward),
		"endIn", fmt.Sprintf("%.0fh", opp.Market.HoursToResolution()),
	)

	return nil
}

// expireResolvedAndNearEnd handles GAP #3 (market resolution) and GAP #6/#7 (near-end).
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
			// Market disappeared from the scan — likely resolved or deactivated
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

// refreshQueues updates queueAhead for open orders using current book data (GAP #4).
func (pe *PaperEngine) refreshQueues(ctx context.Context, oppByCondition map[string]domain.Opportunity) {
	openOrders, err := pe.store.GetOpenPaperOrders(ctx)
	if err != nil {
		return
	}

	for _, order := range openOrders {
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

// checkFills fetches recent trades and simulates queue-aware filling.
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

		// GAP #5: log trade data coverage
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

		// Sort trades by timestamp for proper queue simulation
		sort.Slice(trades, func(i, j int) bool {
			return trades[i].Timestamp.Before(trades[j].Timestamp)
		})

		for _, order := range orders {
			var cumSellUSDC float64
			var fillTrade *domain.Trade

			for i := range trades {
				t := &trades[i]
				if t.Timestamp.Before(order.PlacedAt) {
					continue
				}
				if t.Side != "SELL" || t.Price > order.BidPrice {
					continue
				}

				cumSellUSDC += t.Size * t.Price

				if cumSellUSDC > order.QueueAhead+order.Size {
					fillTrade = t
					break
				}
			}

			if fillTrade == nil {
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

			if err := pe.store.MarkPaperOrderFilled(ctx, order.ID, fillTrade.Timestamp, fillTrade.Price); err != nil {
				slog.Warn("paper: error marking order filled", "err", err)
				continue
			}
			fill := domain.PaperFill{
				OrderID:   order.ID,
				TradeID:   fillTrade.ID,
				Price:     fillTrade.Price,
				Size:      fillTrade.Size,
				Timestamp: fillTrade.Timestamp,
			}
			if err := pe.store.SavePaperFill(ctx, fill); err != nil {
				slog.Warn("paper: error saving fill", "err", err)
			}

			slog.Info("paper: order FILLED (queue-adjusted)",
				"side", order.Side,
				"market", truncateStr(order.Question, 30),
				"bidPrice", fmt.Sprintf("%.4f", order.BidPrice),
				"fillPrice", fmt.Sprintf("%.4f", fillTrade.Price),
				"queueAhead", fmt.Sprintf("$%.0f", order.QueueAhead),
				"sellVolNeeded", fmt.Sprintf("$%.0f", cumSellUSDC),
			)

			totalFills++
		}
	}

	return totalFills, nil
}

// buildPositions reconstructs positions with reward accrual (GAP #1) and spread check (GAP #5).
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
				pos.YesFilled = o.Status == domain.PaperStatusFilled
			case "NO":
				pos.NoOrder = o
				pos.NoFilled = o.Status == domain.PaperStatusFilled
			}
		}

		pos.IsComplete = pos.YesFilled && pos.NoFilled
		pos.IsResolved = allResolved(orders)

		// Partial detection
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

		// GAP #1: Reward accrual — calculate how much reward this position earned
		if pos.YesOrder != nil && pos.YesOrder.DailyReward > 0 {
			pos.DailyReward = pos.YesOrder.DailyReward
			activeHours := pe.activeHours(pos)
			pos.RewardAccrued = pos.DailyReward * (activeHours / 24.0)
		}

		// GAP #5: Check spread qualification with current data
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
	var latest time.Time

	if pos.YesOrder != nil {
		earliest = pos.YesOrder.PlacedAt
	}
	if pos.NoOrder != nil && (earliest.IsZero() || pos.NoOrder.PlacedAt.Before(earliest)) {
		earliest = pos.NoOrder.PlacedAt
	}

	if earliest.IsZero() {
		return 0
	}

	// Active until: filled (both sides), expired, resolved, or now
	latest = time.Now()
	if pos.IsComplete && pos.YesOrder.FilledAt != nil && pos.NoOrder.FilledAt != nil {
		// Use the LATER fill time (when both sides were done)
		if pos.YesOrder.FilledAt.After(*pos.NoOrder.FilledAt) {
			latest = *pos.YesOrder.FilledAt
		} else {
			latest = *pos.NoOrder.FilledAt
		}
	}

	return latest.Sub(earliest).Hours()
}

// calculateDeployedCapital sums the Size of all OPEN and FILLED orders.
func (pe *PaperEngine) calculateDeployedCapital(ctx context.Context) float64 {
	openOrders, _ := pe.store.GetOpenPaperOrders(ctx)
	filledOrders, _ := pe.store.GetAllPaperOrders(ctx, "FILLED")

	total := 0.0
	for _, o := range openOrders {
		total += o.Size
	}
	for _, o := range filledOrders {
		total += o.Size
	}
	return total
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
		if pos.YesOrder != nil && (pos.YesOrder.Status == domain.PaperStatusOpen || pos.YesOrder.Status == domain.PaperStatusFilled) {
			hasActive = true
		}
		if pos.NoOrder != nil && (pos.NoOrder.Status == domain.PaperStatusOpen || pos.NoOrder.Status == domain.PaperStatusFilled) {
			hasActive = true
		}
		if !hasActive {
			continue
		}

		active++
		if pos.IsComplete {
			complete++
			// GAP #1: fill PnL uses maker fee (already in FillCostPair via config FeeRate)
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
		NetPnL:          totalReward + fillPnL,
		AvgPartialMins:  avgPartial,
		FillsYes:        fillsYes,
		FillsNo:         fillsNo,
		OrdersPlaced:    result.NewOrders,
		CapitalDeployed: result.CapitalDeployed,
		MarketsResolved: result.MarketsResolved,
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

func queuePosition(book domain.OrderBook, bidPrice float64) float64 {
	total := 0.0
	for _, entry := range book.Bids {
		if entry.Price >= bidPrice {
			total += entry.Size * entry.Price
		}
	}
	return total
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
