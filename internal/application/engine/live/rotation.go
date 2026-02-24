package live

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/alejandrodnm/polybot/internal/domain"
	"github.com/alejandrodnm/polybot/internal/application/engine"
)

// cancelResolvedOrders cancels orders for markets that have resolved or are near end.
// CRITICAL: if one side of a pair is already FILLED, the counterpart is kept open
// to allow the merge to complete.
func (le *Engine) cancelResolvedOrders(ctx context.Context, oppByCondition map[string]domain.Opportunity) {
	conditions, err := le.store.GetActiveLiveConditions(ctx)
	if err != nil {
		return
	}

	for _, condID := range conditions {
		opp, exists := oppByCondition[condID]

		needsCancel := false
		if !exists {
			needsCancel = true
		} else if opp.Market.HoursToResolution() > 0 && opp.Market.HoursToResolution() < nearEndHours {
			needsCancel = true
		} else if !opp.Market.Active || opp.Market.Closed {
			needsCancel = true
		}

		if !needsCancel {
			continue
		}

		openOrders, err := le.store.GetOpenLiveOrders(ctx)
		if err != nil {
			continue
		}

		pairOrders := make(map[string][]domain.LiveOrder)
		for _, o := range openOrders {
			if o.ConditionID != condID {
				continue
			}
			pairOrders[o.PairID] = append(pairOrders[o.PairID], o)
		}

		for pairID, orders := range pairOrders {
			allPairOrders, err := le.store.GetLiveOrdersByPair(ctx, pairID)
			if err != nil {
				continue
			}

			hasFill := false
			for _, po := range allPairOrders {
				if po.Status == domain.LiveStatusFilled || po.Status == domain.LiveStatusPartial || po.FilledSize > 0 {
					hasFill = true
					break
				}
			}

			if !hasFill {
				for _, po := range allPairOrders {
					if po.TokenID == "" {
						continue
					}
					bal, err := le.executor.TokenBalance(ctx, po.TokenID)
					if err != nil {
						slog.Debug("live: on-chain balance check failed", "token", po.TokenID[:16], "err", err)
						continue
					}
					if bal > 0 {
						hasFill = true
						slog.Warn("live: on-chain tokens detected that SQLite missed!",
							"side", po.Side,
							"shares", fmt.Sprintf("%.2f", bal),
							"token", po.TokenID[:20],
						)
						break
					}
				}
			}

			if hasFill {
				slog.Warn("live: market near resolution but pair has fills — keeping counterpart open",
					"condition", condID[:16],
					"pair", pairID[:8],
				)
				continue
			}

			for _, o := range orders {
				if err := le.executor.CancelOrder(ctx, o.CLOBOrderID); err != nil {
					slog.Warn("live: error cancelling order", "clob_id", o.CLOBOrderID, "err", err)
				}
				_ = le.store.UpdateLiveOrderStatus(ctx, o.ID, domain.LiveStatusCancelled)
			}
		}
	}
}

// rotateStaleOrders cancels and removes stale positions.
func (le *Engine) rotateStaleOrders(ctx context.Context, oppByCondition map[string]domain.Opportunity) int {
	openOrders, err := le.store.GetOpenLiveOrders(ctx)
	if err != nil {
		return 0
	}

	byPair := make(map[string][]domain.LiveOrder)
	for _, o := range openOrders {
		if o.PairID != "" {
			byPair[o.PairID] = append(byPair[o.PairID], o)
		}
	}

	expired := 0
	for _, orders := range byPair {
		if len(orders) < 2 {
			continue
		}

		pairID := orders[0].PairID
		freshPair, err := le.store.GetLiveOrdersByPair(ctx, pairID)
		if err != nil {
			continue
		}
		hasFill := false
		allOpen := true
		var oldest time.Time
		for _, o := range freshPair {
			if o.Status == domain.LiveStatusFilled || o.Status == domain.LiveStatusPartial || o.FilledSize > 0 {
				hasFill = true
				break
			}
			if o.Status != domain.LiveStatusOpen {
				allOpen = false
			}
			if oldest.IsZero() || o.PlacedAt.Before(oldest) {
				oldest = o.PlacedAt
			}
		}
		if !hasFill {
			for _, po := range freshPair {
				if po.TokenID == "" {
					continue
				}
				bal, err := le.executor.TokenBalance(ctx, po.TokenID)
				if err == nil && bal > 0 {
					hasFill = true
					slog.Warn("live: on-chain tokens detected during rotation check",
						"side", po.Side, "shares", fmt.Sprintf("%.2f", bal))
					break
				}
			}
		}

		if hasFill {
			slog.Debug("live: skipping rotation — pair has fills, keeping counterpart open",
				"pair", pairID[:8])
			continue
		}
		if !allOpen || oldest.IsZero() {
			continue
		}

		age := time.Since(oldest).Hours()
		conditionID := orders[0].ConditionID
		rotateReason := ""

		if age >= staleHours {
			rotateReason = fmt.Sprintf("stale %.1fh (no fills)", age)
		}

		if rotateReason == "" {
			if opp, exists := oppByCondition[conditionID]; exists {
				if opp.FillCostPerPair > 0 {
					rotateReason = fmt.Sprintf("spread unprofitable (fillCost $%.4f)", opp.FillCostPerPair)
				}
			}
		}

		if rotateReason == "" {
			if opp, exists := oppByCondition[conditionID]; exists {
				currentComp := opp.YesBook.BidDepthWithinUSDC(0.05) + opp.NoBook.BidDepthWithinUSDC(0.05)
				originalComp := orders[0].CompetitionAt
				if originalComp > 0 && currentComp > originalComp*competitionMult {
					rotateReason = fmt.Sprintf("competition spiked %.1fx", currentComp/originalComp)
				}
			}
		}

		if rotateReason == "" {
			continue
		}

		for _, o := range orders {
			if err := le.executor.CancelOrder(ctx, o.CLOBOrderID); err != nil {
				slog.Warn("live: error cancelling stale order", "clob_id", o.CLOBOrderID, "err", err)
			}
		}
		_ = le.store.CancelLiveOrdersByCondition(ctx, conditionID)

		slog.Info("live: ROTATED pair",
			"reason", rotateReason,
			"market", engine.TruncateStr(orders[0].Question, 30),
			"age", fmt.Sprintf("%.1fh", age),
		)
		expired++
	}

	return expired
}
