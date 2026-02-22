package scanner

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/alejandrodnm/polybot/internal/domain"
	"github.com/alejandrodnm/polybot/internal/ports"
	"github.com/google/uuid"
)

const (
	paperMaxMarkets      = 3  // max simultaneous paper positions
	paperCycleInterval   = 60 * time.Second
	paperMaxPartialHours = 6  // warn if partial fill older than this
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
	lastScan time.Time // tracks when we last checked trades for fills
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
	Positions     []domain.PaperPosition
	NewOrders     int
	NewFills      int
	CompletePairs int
	PartialAlerts []string
}

// RunOnce executes a single paper trading cycle: scan, place, check fills, report.
func (pe *PaperEngine) RunOnce(ctx context.Context) (*PaperCycleResult, error) {
	result := &PaperCycleResult{}

	// 1. Scan markets
	opps, err := pe.scanner.RunOnce(ctx)
	if err != nil {
		return nil, fmt.Errorf("paper.RunOnce: scan: %w", err)
	}

	// 2. Check fills on existing open orders
	fills, err := pe.checkFills(ctx)
	if err != nil {
		slog.Warn("paper: error checking fills", "err", err)
	}
	result.NewFills = fills

	// 3. Place new orders on top markets (if we have capacity)
	activeConditions, err := pe.store.GetActivePaperConditions(ctx)
	if err != nil {
		slog.Warn("paper: error getting active conditions", "err", err)
	}

	activeSet := make(map[string]bool, len(activeConditions))
	for _, c := range activeConditions {
		activeSet[c] = true
	}

	newOrders := 0
	for _, opp := range opps {
		if len(activeConditions)+newOrders >= pe.cfg.MaxMarkets {
			break
		}
		if activeSet[opp.Market.ConditionID] {
			continue
		}
		// Only place orders on FILLS=PROFIT markets
		if opp.FillCostPerPair > 0 {
			continue
		}
		if opp.YourDailyReward <= 0 {
			continue
		}

		if err := pe.placeVirtualOrders(ctx, opp); err != nil {
			slog.Warn("paper: error placing virtual orders",
				"market", opp.Market.Question, "err", err)
			continue
		}
		activeSet[opp.Market.ConditionID] = true
		newOrders += 2
	}
	result.NewOrders = newOrders

	// 4. Build positions and detect partials
	positions, err := pe.buildPositions(ctx)
	if err != nil {
		slog.Warn("paper: error building positions", "err", err)
	}
	result.Positions = positions

	for _, pos := range positions {
		if pos.IsComplete {
			result.CompletePairs++
		}
		if pos.PartialSince != nil && !pos.IsComplete {
			dur := pos.PartialDuration()
			if dur > paperMaxPartialHours*time.Hour {
				alert := fmt.Sprintf("PARTIAL >%dh: %s (%s filled %.0fh ago)",
					paperMaxPartialHours, pos.Question, partialSide(pos), dur.Hours())
				result.PartialAlerts = append(result.PartialAlerts, alert)
				slog.Warn("paper: long partial fill", "market", pos.Question,
					"side", partialSide(pos), "hours", dur.Hours())
			}
		}
	}

	// 5. Save daily summary
	pe.saveDailySummary(ctx, result)

	pe.lastScan = time.Now()
	return result, nil
}

// placeVirtualOrders creates a YES+NO order pair for a market.
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

	yesOrder := domain.VirtualOrder{
		ID:          uuid.New().String(),
		ConditionID: opp.Market.ConditionID,
		TokenID:     opp.Market.YesToken().TokenID,
		Side:        "YES",
		BidPrice:    yesBid,
		Size:        pe.cfg.OrderSize,
		PlacedAt:    now,
		Status:      domain.PaperStatusOpen,
		PairID:      pairID,
		Question:    opp.Market.Question,
		QueueAhead:  yesQueue,
	}

	noOrder := domain.VirtualOrder{
		ID:          uuid.New().String(),
		ConditionID: opp.Market.ConditionID,
		TokenID:     opp.Market.NoToken().TokenID,
		Side:        "NO",
		BidPrice:    noBid,
		Size:        pe.cfg.OrderSize,
		PlacedAt:    now,
		Status:      domain.PaperStatusOpen,
		PairID:      pairID,
		Question:    opp.Market.Question,
		QueueAhead:  noQueue,
	}

	if err := pe.store.SavePaperOrder(ctx, yesOrder); err != nil {
		return err
	}
	if err := pe.store.SavePaperOrder(ctx, noOrder); err != nil {
		return err
	}

	slog.Info("paper: placed virtual orders",
		"market", truncateStr(opp.Market.Question, 40),
		"yesBid", fmt.Sprintf("%.4f", yesBid),
		"noBid", fmt.Sprintf("%.4f", noBid),
		"yesQueue", fmt.Sprintf("$%.0f", yesQueue),
		"noQueue", fmt.Sprintf("$%.0f", noQueue),
	)

	return nil
}

// checkFills fetches recent trades and checks if any would fill our open orders.
func (pe *PaperEngine) checkFills(ctx context.Context) (int, error) {
	openOrders, err := pe.store.GetOpenPaperOrders(ctx)
	if err != nil {
		return 0, fmt.Errorf("paper.checkFills: get open orders: %w", err)
	}

	if len(openOrders) == 0 {
		return 0, nil
	}

	// Group orders by tokenID
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

		// Only consider trades after we placed orders
		for _, order := range orders {
			for _, trade := range trades {
				if trade.Timestamp.Before(order.PlacedAt) {
					continue
				}
				// A SELL at price <= our bid fills us (someone sells into our bid)
				if trade.Side == "SELL" && trade.Price <= order.BidPrice {
					if err := pe.store.MarkPaperOrderFilled(ctx, order.ID, trade.Timestamp, trade.Price); err != nil {
						slog.Warn("paper: error marking order filled", "err", err)
						continue
					}
					fill := domain.PaperFill{
						OrderID:   order.ID,
						TradeID:   trade.ID,
						Price:     trade.Price,
						Size:      trade.Size,
						Timestamp: trade.Timestamp,
					}
					if err := pe.store.SavePaperFill(ctx, fill); err != nil {
						slog.Warn("paper: error saving fill", "err", err)
					}

					slog.Info("paper: order FILLED",
						"side", order.Side,
						"market", truncateStr(order.Question, 30),
						"bidPrice", fmt.Sprintf("%.4f", order.BidPrice),
						"fillPrice", fmt.Sprintf("%.4f", trade.Price),
					)

					totalFills++
					break // one fill per order is enough
				}
			}
		}
	}

	return totalFills, nil
}

// buildPositions reconstructs the current state of all paper positions.
func (pe *PaperEngine) buildPositions(ctx context.Context) ([]domain.PaperPosition, error) {
	allOrders, err := pe.store.GetAllPaperOrders(ctx, "")
	if err != nil {
		return nil, err
	}

	// Group by pairID
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

		// Detect partial: one side filled, the other still open
		if (pos.YesFilled && !pos.NoFilled) || (!pos.YesFilled && pos.NoFilled) {
			var filledAt *time.Time
			if pos.YesFilled && pos.YesOrder != nil && pos.YesOrder.FilledAt != nil {
				filledAt = pos.YesOrder.FilledAt
			} else if pos.NoFilled && pos.NoOrder != nil && pos.NoOrder.FilledAt != nil {
				filledAt = pos.NoOrder.FilledAt
			}
			pos.PartialSince = filledAt
		}

		// Compute fill cost for the pair
		if pos.YesOrder != nil && pos.NoOrder != nil {
			pos.FillCostPair = domain.FillCostPerEvent(
				pos.YesOrder.BidPrice, pos.NoOrder.BidPrice, pe.cfg.FeeRate)
		}

		positions = append(positions, pos)
	}

	return positions, nil
}

// saveDailySummary persists today's paper trading summary.
func (pe *PaperEngine) saveDailySummary(ctx context.Context, result *PaperCycleResult) {
	today := time.Now().UTC().Truncate(24 * time.Hour)

	active, complete, partial := 0, 0, 0
	var totalPartialMins float64
	partialCount := 0
	fillsYes, fillsNo := 0, 0

	for _, pos := range result.Positions {
		// Only count non-expired positions
		hasOpen := false
		if pos.YesOrder != nil && (pos.YesOrder.Status == domain.PaperStatusOpen || pos.YesOrder.Status == domain.PaperStatusFilled) {
			hasOpen = true
		}
		if pos.NoOrder != nil && (pos.NoOrder.Status == domain.PaperStatusOpen || pos.NoOrder.Status == domain.PaperStatusFilled) {
			hasOpen = true
		}
		if !hasOpen {
			continue
		}

		active++
		if pos.IsComplete {
			complete++
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
	}

	avgPartial := 0.0
	if partialCount > 0 {
		avgPartial = totalPartialMins / float64(partialCount)
	}

	// Estimate daily reward: sum of daily rewards for active positions
	// (we don't have the reward rate stored per order, so use fill cost as proxy for PnL)
	fillPnL := 0.0
	for _, pos := range result.Positions {
		if pos.IsComplete && pos.FillCostPair < 0 {
			fillPnL += -pos.FillCostPair * (pe.cfg.OrderSize / maxF(pos.YesOrder.BidPrice, 0.01))
		}
	}

	summary := domain.PaperDailySummary{
		Date:            today,
		ActivePositions: active,
		CompletePairs:   complete,
		PartialFills:    partial,
		TotalReward:     0, // will be calculated when we have continuous tracking
		TotalFillPnL:    fillPnL,
		NetPnL:          fillPnL,
		AvgPartialMins:  avgPartial,
		FillsYes:        fillsYes,
		FillsNo:         fillsNo,
		OrdersPlaced:    result.NewOrders,
	}

	if err := pe.store.SavePaperDaily(ctx, summary); err != nil {
		slog.Warn("paper: error saving daily summary", "err", err)
	}
}

// queuePosition estimates USDC ahead in the book at the given price.
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
