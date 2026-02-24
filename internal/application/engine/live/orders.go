package live

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/alejandrodnm/polybot/internal/domain"
	"github.com/alejandrodnm/polybot/internal/application/engine"
	"github.com/google/uuid"
)

// updateSpreadHistory records spread samples for all current opportunities
// and prunes stale entries for condition IDs no longer in the scan.
func (le *Engine) updateSpreadHistory(opps []domain.Opportunity) {
	le.spreadMu.Lock()
	defer le.spreadMu.Unlock()

	now := time.Now()
	seen := make(map[string]bool, len(opps))

	for _, opp := range opps {
		cid := opp.Market.ConditionID
		seen[cid] = true
		sample := spreadSample{
			SpreadTotal: opp.SpreadTotal,
			FillCost:    opp.FillCostPerPair,
			ScannedAt:   now,
		}
		history := le.spreadHistory[cid]
		history = append(history, sample)
		if len(history) > spreadStabilityWindow {
			history = history[len(history)-spreadStabilityWindow:]
		}
		le.spreadHistory[cid] = history
	}

	for cid, history := range le.spreadHistory {
		if seen[cid] {
			continue
		}
		if len(history) > 0 && now.Sub(history[len(history)-1].ScannedAt) > 2*time.Hour {
			delete(le.spreadHistory, cid)
		}
	}
}

// spreadStable returns true if the market spread has been stable across recent scans.
func (le *Engine) spreadStable(conditionID string) bool {
	le.spreadMu.RLock()
	history := le.spreadHistory[conditionID]
	le.spreadMu.RUnlock()

	if len(history) < spreadStabilityWindow {
		return len(history) >= 1
	}

	for _, s := range history {
		if s.FillCost > 0.02 {
			return false
		}
	}

	if len(history) >= 2 {
		spreads := make([]float64, len(history))
		for i, s := range history {
			spreads[i] = s.SpreadTotal
		}
		mean := 0.0
		for _, v := range spreads {
			mean += v
		}
		mean /= float64(len(spreads))

		if mean == 0 {
			return true
		}

		variance := 0.0
		for _, v := range spreads {
			diff := v - mean
			variance += diff * diff
		}
		variance /= float64(len(spreads))
		cv := math.Sqrt(variance) / math.Abs(mean)

		if cv > spreadVarianceMax {
			return false
		}
	}

	return true
}

// placeOrderPair places YES+NO maker bid orders for a market.
func (le *Engine) placeOrderPair(ctx context.Context, opp domain.Opportunity, orderSize float64) error {
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

	origYes, origNo := yesBid, noBid
	feeR := opp.Market.EffectiveFeeRate(le.cfg.FeeRate)

	yesBid, yesQueue := le.optimizeBid(opp.YesBook, yesBid, noBid, orderSize, feeR, true)
	noBid, noQueue := le.optimizeBid(opp.NoBook, noBid, yesBid, orderSize, feeR, false)
	yesBid, yesQueue = le.optimizeBid(opp.YesBook, origYes, noBid, orderSize, feeR, true)

	slog.Info("live: bid optimized",
		"market", engine.TruncateStr(opp.Market.Question, 40),
		"yesOrig", fmt.Sprintf("%.2f", origYes),
		"yesFinal", fmt.Sprintf("%.2f", yesBid),
		"yesQueue", fmt.Sprintf("%.0f", yesQueue),
		"noOrig", fmt.Sprintf("%.2f", origNo),
		"noFinal", fmt.Sprintf("%.2f", noBid),
		"noQueue", fmt.Sprintf("%.0f", noQueue),
		"mergeCost", fmt.Sprintf("%.4f", domain.FillCostPerEvent(yesBid, noBid, opp.Market.EffectiveFeeRate(le.cfg.FeeRate))),
	)

	feeRate := opp.Market.EffectiveFeeRate(le.cfg.FeeRate)
	for domain.FillCostPerEvent(yesBid, noBid, feeRate) > 0 {
		if yesBid > noBid {
			yesBid -= bidTickStep
		} else {
			noBid -= bidTickStep
		}
		if yesBid <= 0.01 || noBid <= 0.01 {
			return fmt.Errorf("cannot find profitable bid pair")
		}
	}

	yesTokenID := opp.Market.YesToken().TokenID
	noTokenID := opp.Market.NoToken().TokenID

	negRisk, err := le.executor.IsNegRisk(ctx, yesTokenID)
	if err != nil {
		slog.Warn("live: neg-risk check failed, assuming false", "err", err)
		negRisk = false
	}
	if negRisk {
		slog.Debug("live: skipping NegRisk market (merge not supported)",
			"market", engine.TruncateStr(opp.Market.Question, 35))
		return fmt.Errorf("NegRisk markets cannot be merged — skipping to avoid locked capital")
	}

	yesReq := domain.PlaceOrderRequest{
		TokenID:     yesTokenID,
		ConditionID: opp.Market.ConditionID,
		Price:       yesBid,
		Size:        orderSize,
		Side:        "BUY",
		NegRisk:     negRisk,
	}
	yesPlaced, err := le.executor.PlaceOrder(ctx, yesReq)
	if err != nil {
		return fmt.Errorf("place YES: %w", err)
	}

	noReq := domain.PlaceOrderRequest{
		TokenID:     noTokenID,
		ConditionID: opp.Market.ConditionID,
		Price:       noBid,
		Size:        orderSize,
		Side:        "BUY",
		NegRisk:     negRisk,
	}
	noPlaced, err := le.executor.PlaceOrder(ctx, noReq)
	if err != nil {
		slog.Warn("live: NO order failed, cancelling YES", "yes_id", yesPlaced.CLOBOrderID, "err", err)
		if cancelErr := le.executor.CancelOrder(ctx, yesPlaced.CLOBOrderID); cancelErr != nil {
			slog.Warn("live: could not cancel YES after NO failure", "err", cancelErr)
		}
		return fmt.Errorf("place NO: %w", err)
	}

	conservativeYesQueue := yesQueue * queueConservativeMult
	conservativeNoQueue := noQueue * queueConservativeMult

	competition := opp.YesBook.BidDepthWithinUSDC(0.05) + opp.NoBook.BidDepthWithinUSDC(0.05)

	yesOrder := domain.LiveOrder{
		ID:            uuid.New().String(),
		CLOBOrderID:   yesPlaced.CLOBOrderID,
		ConditionID:   opp.Market.ConditionID,
		TokenID:       yesTokenID,
		Side:          "YES",
		BidPrice:      yesBid,
		Size:          orderSize,
		PairID:        pairID,
		PlacedAt:      now,
		Status:        domain.LiveStatusOpen,
		Question:      opp.Market.Question,
		QueueAhead:    conservativeYesQueue,
		DailyReward:   opp.YourDailyReward,
		EndDate:       opp.Market.EndDate,
		NegRisk:       negRisk,
		CompetitionAt: competition,
	}

	noOrder := domain.LiveOrder{
		ID:            uuid.New().String(),
		CLOBOrderID:   noPlaced.CLOBOrderID,
		ConditionID:   opp.Market.ConditionID,
		TokenID:       noTokenID,
		Side:          "NO",
		BidPrice:      noBid,
		Size:          orderSize,
		PairID:        pairID,
		PlacedAt:      now,
		Status:        domain.LiveStatusOpen,
		Question:      opp.Market.Question,
		QueueAhead:    conservativeNoQueue,
		DailyReward:   opp.YourDailyReward,
		EndDate:       opp.Market.EndDate,
		NegRisk:       negRisk,
		CompetitionAt: competition,
	}

	if err := le.store.SaveLiveOrder(ctx, yesOrder); err != nil {
		slog.Warn("live: error saving YES order", "err", err)
	}
	if err := le.store.SaveLiveOrder(ctx, noOrder); err != nil {
		slog.Warn("live: error saving NO order", "err", err)
	}

	slog.Info("live: placed order pair",
		"market", engine.TruncateStr(opp.Market.Question, 35),
		"yes_price", fmt.Sprintf("$%.2f", yesBid),
		"no_price", fmt.Sprintf("$%.2f", noBid),
		"size", fmt.Sprintf("$%.2f", orderSize),
		"spread_profit", fmt.Sprintf("$%.4f", (1.0-yesBid-noBid)*orderSize),
		"neg_risk", negRisk,
	)

	return nil
}

// optimizeBid walks bid price upward, maximising Expected Value.
func (le *Engine) optimizeBid(book domain.OrderBook, currentBid, counterBid, orderSize, feeRate float64, isYesSide bool) (bestBid, bestQueue float64) {
	bestBid = currentBid
	bestQueue = engine.QueuePosition(book, currentBid)

	baseFillCost := fillCostForSide(currentBid, counterBid, feeRate, isYesSide)
	baseProfit := math.Max(-baseFillCost, 0.001)
	baseFillProb := fillProbability(bestQueue, orderSize)
	bestEV := baseFillProb * baseProfit * orderSize

	for tick := bidTickStep; tick <= maxBidTickUp; tick += bidTickStep {
		candidate := math.Round((currentBid+tick)*100) / 100
		if candidate >= 1.0 {
			break
		}

		fc := fillCostForSide(candidate, counterBid, feeRate, isYesSide)
		if fc > 0 {
			break
		}

		profit := -fc
		queue := engine.QueuePosition(book, candidate)
		fp := fillProbability(queue, orderSize)
		ev := fp * profit * orderSize

		if ev > bestEV {
			bestEV = ev
			bestBid = candidate
			bestQueue = queue
		}
	}
	return bestBid, bestQueue
}

func fillCostForSide(bid, counterBid, feeRate float64, isYesSide bool) float64 {
	if isYesSide {
		return domain.FillCostPerEvent(bid, counterBid, feeRate)
	}
	return domain.FillCostPerEvent(counterBid, bid, feeRate)
}

func fillProbability(queueAhead, orderSize float64) float64 {
	if queueAhead <= 0 {
		return 0.95
	}
	return orderSize / (orderSize + queueAhead)
}

func askDepthShares(book domain.OrderBook) float64 {
	var total float64
	for _, entry := range book.Asks {
		total += entry.Size
	}
	return total
}

func queuePositionConservative(book domain.OrderBook, bidPrice float64) float64 {
	return engine.QueuePosition(book, bidPrice) * queueConservativeMult
}

// syncOrderState polls CLOB for current order status and detects fills.
func (le *Engine) syncOrderState(ctx context.Context, oppByCondition map[string]domain.Opportunity) (newFills int, err error) {
	openOrders, err := le.store.GetOpenLiveOrders(ctx)
	if err != nil {
		return 0, fmt.Errorf("syncOrderState: get open orders: %w", err)
	}

	if len(openOrders) == 0 {
		return 0, nil
	}

	clobOrders, err := le.executor.GetOpenOrders(ctx)
	if err != nil {
		return 0, fmt.Errorf("syncOrderState: get clob orders: %w", err)
	}

	clobByID := make(map[string]domain.LiveOrder, len(clobOrders))
	for _, co := range clobOrders {
		clobByID[co.CLOBOrderID] = co
	}

	for _, local := range openOrders {
		if local.CLOBOrderID == "" {
			continue
		}

		clobOrder, exists := clobByID[local.CLOBOrderID]

		if !exists {
			if local.FilledSize == 0 {
				slog.Info("live: order disappeared with no fills — marking CANCELLED (likely auto-cancel)",
					"side", local.Side,
					"market", engine.TruncateStr(local.Question, 30),
					"clob_id", local.CLOBOrderID,
				)
				_ = le.store.UpdateLiveOrderStatus(ctx, local.ID, domain.LiveStatusCancelled)
				continue
			}

			if local.Status == domain.LiveStatusOpen || local.Status == domain.LiveStatusPartial {
				now := time.Now().UTC()
				if err := le.store.UpdateLiveOrderFill(ctx, local.ID, local.Size, local.BidPrice, domain.LiveStatusFilled, &now); err != nil {
					slog.Warn("live: error marking order filled", "id", local.ID, "err", err)
				}

				fill := domain.LiveFill{
					OrderID:     local.ID,
					CLOBTradeID: "",
					Price:       local.BidPrice,
					Size:        local.Size - local.FilledSize,
					Timestamp:   now,
				}
				_ = le.store.SaveLiveFill(ctx, fill)
				newFills++

				slog.Info("live: order filled",
					"side", local.Side,
					"market", engine.TruncateStr(local.Question, 30),
					"price", fmt.Sprintf("$%.2f", local.BidPrice),
					"size", fmt.Sprintf("$%.2f", local.Size),
				)
			}
			continue
		}

		if clobOrder.FilledSize > local.FilledSize {
			newFilledAmount := clobOrder.FilledSize - local.FilledSize
			status := domain.LiveStatusPartial

			var filledAt *time.Time
			if clobOrder.FilledSize >= local.Size*0.999 {
				status = domain.LiveStatusFilled
				now := time.Now().UTC()
				filledAt = &now
				newFills++
			}

			if err := le.store.UpdateLiveOrderFill(ctx, local.ID, clobOrder.FilledSize, local.BidPrice, status, filledAt); err != nil {
				slog.Warn("live: error updating partial fill", "id", local.ID, "err", err)
			}

			if newFilledAmount > 0 {
				fill := domain.LiveFill{
					OrderID:     local.ID,
					CLOBTradeID: "",
					Price:       local.BidPrice,
					Size:        newFilledAmount,
					Timestamp:   time.Now().UTC(),
				}
				_ = le.store.SaveLiveFill(ctx, fill)
			}
		}
	}

	return newFills, nil
}
