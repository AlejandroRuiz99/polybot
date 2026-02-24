package paper

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/alejandrodnm/polybot/internal/domain"
	"github.com/alejandrodnm/polybot/internal/application/engine"
	"github.com/google/uuid"
)

// placeVirtualOrders creates a YES+NO order pair with the default config size.
func (pe *Engine) placeVirtualOrders(ctx context.Context, opp domain.Opportunity) error {
	return pe.placeVirtualOrdersWithSize(ctx, opp, pe.cfg.OrderSize)
}

// placeVirtualOrdersWithSize creates a YES+NO order pair with multi-tick bid optimization.
func (pe *Engine) placeVirtualOrdersWithSize(ctx context.Context, opp domain.Opportunity, orderSize float64) error {
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

	yesQueue := engine.QueuePosition(opp.YesBook, yesBid)
	noQueue := engine.QueuePosition(opp.NoBook, noBid)

	yesBidOpt, yesQueueOpt := pe.optimizeBid(opp.YesBook, yesBid, noBid, orderSize, pe.cfg.FeeRate, true)
	noBidOpt, noQueueOpt := pe.optimizeBid(opp.NoBook, noBid, yesBidOpt, orderSize, pe.cfg.FeeRate, false)

	if domain.FillCostPerEvent(yesBidOpt, noBidOpt, pe.cfg.FeeRate) > 0 {
		yesBidOpt = yesBid
		noBidOpt = noBid
		yesQueueOpt = yesQueue
		noQueueOpt = noQueue
	}

	optimized := yesBidOpt != yesBid || noBidOpt != noBid

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
		"market", engine.TruncateStr(opp.Market.Question, 40),
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

// optimizeBid tries tick-ups on a bid and picks the best one.
func (pe *Engine) optimizeBid(
	book domain.OrderBook,
	currentBid, counterBid, orderSize, feeRate float64,
	isYesSide bool,
) (bestBid float64, bestQueue float64) {
	bestBid = currentBid
	bestQueue = engine.QueuePosition(book, currentBid)

	if bestQueue <= orderSize {
		return
	}

	bestScore := bidOptScore(bestQueue, orderSize, 0.0)

	for ticks := 1; float64(ticks)*bidTickStep <= maxBidTickUp; ticks++ {
		candidate := currentBid + float64(ticks)*bidTickStep

		var fillCost float64
		if isYesSide {
			fillCost = domain.FillCostPerEvent(candidate, counterBid, feeRate)
		} else {
			fillCost = domain.FillCostPerEvent(counterBid, candidate, feeRate)
		}
		if fillCost > 0 {
			break
		}

		newQueue := engine.QueuePosition(book, candidate)
		tickCost := float64(ticks) * bidTickStep * orderSize
		score := bidOptScore(newQueue, orderSize, tickCost)

		if score > bestScore {
			bestScore = score
			bestBid = candidate
			bestQueue = newQueue
		}
	}

	return bestBid, bestQueue
}

func bidOptScore(queue, orderSize, tickCost float64) float64 {
	if orderSize <= 0 {
		return 0
	}
	fillSpeedProxy := orderSize / (queue + orderSize + 1)
	return fillSpeedProxy - tickCost/orderSize
}

// expireResolvedAndNearEnd handles market resolution and near-end expiry.
func (pe *Engine) expireResolvedAndNearEnd(ctx context.Context, oppByCondition map[string]domain.Opportunity) int {
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

		if !order.EndDate.IsZero() && time.Now().After(order.EndDate) {
			shouldExpire = true
			reason = "RESOLVED"
		}

		if _, exists := oppByCondition[order.ConditionID]; !exists {
			if !order.EndDate.IsZero() && time.Until(order.EndDate) < 0 {
				shouldExpire = true
				reason = "RESOLVED (disappeared)"
			}
		}

		if !order.EndDate.IsZero() {
			hoursLeft := time.Until(order.EndDate).Hours()
			if hoursLeft > 0 && hoursLeft < nearEndHours {
				shouldExpire = true
				reason = fmt.Sprintf("NEAR END (%.0fh left)", hoursLeft)
			}
		}

		if shouldExpire {
			seenConditions[order.ConditionID] = true
			slog.Warn("paper: expiring orders",
				"reason", reason,
				"market", engine.TruncateStr(order.Question, 30),
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
func (pe *Engine) refreshQueues(ctx context.Context, oppByCondition map[string]domain.Opportunity) {
	openOrders, err := pe.store.GetOpenPaperOrders(ctx)
	if err != nil {
		return
	}

	for _, order := range openOrders {
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

		newQueue := engine.QueuePosition(book, order.BidPrice)
		if err := pe.store.UpdatePaperOrderQueue(ctx, order.ID, newQueue); err != nil {
			slog.Debug("paper: error updating queue", "err", err)
		}
	}
}

// checkFills fetches recent trades and simulates queue-aware filling.
func (pe *Engine) checkFills(ctx context.Context) (int, error) {
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

		sort.Slice(trades, func(i, j int) bool {
			return trades[i].Timestamp.Before(trades[j].Timestamp)
		})

		for _, order := range orders {
			var cumSellUSDC float64
			var lastSellTrade *domain.Trade

			for i := range trades {
				t := &trades[i]
				if t.Timestamp.Before(order.PlacedAt) {
					continue
				}
				if t.Side != "SELL" || t.Price > order.BidPrice {
					continue
				}
				cumSellUSDC += t.Size * t.Price
				lastSellTrade = t
			}

			effectiveFilled := cumSellUSDC - order.QueueAhead
			if effectiveFilled <= 0 {
				if cumSellUSDC > 0 {
					slog.Debug("paper: sell volume hasn't reached us yet",
						"side", order.Side,
						"market", engine.TruncateStr(order.Question, 25),
						"sellVol", fmt.Sprintf("$%.0f", cumSellUSDC),
						"queueAhead", fmt.Sprintf("$%.0f", order.QueueAhead),
						"needed", fmt.Sprintf("$%.0f", order.QueueAhead+order.Size),
					)
				}
				continue
			}

			if effectiveFilled > order.Size {
				effectiveFilled = order.Size
			}

			newlyFilled := effectiveFilled - order.FilledSize
			if newlyFilled <= 0 {
				continue
			}

			fillPrice := order.BidPrice
			fillTime := time.Now().UTC()
			if lastSellTrade != nil {
				fillTime = lastSellTrade.Timestamp
			}

			if effectiveFilled >= order.Size {
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
					"market", engine.TruncateStr(order.Question, 30),
					"bidPrice", fmt.Sprintf("%.4f", fillPrice),
					"queueAhead", fmt.Sprintf("$%.0f", order.QueueAhead),
					"totalSellVol", fmt.Sprintf("$%.0f", cumSellUSDC),
					"prevFilled", fmt.Sprintf("$%.2f", order.FilledSize),
				)
				totalFills++
			} else {
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
					"market", engine.TruncateStr(order.Question, 30),
					"filled", fmt.Sprintf("$%.2f / $%.2f (%.0f%%)", effectiveFilled, order.Size, pct),
					"newThisCycle", fmt.Sprintf("$%.2f", newlyFilled),
				)
			}
		}
	}

	return totalFills, nil
}

func tradeID(t *domain.Trade) string {
	if t == nil {
		return ""
	}
	return t.ID
}

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
