package live

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/alejandrodnm/polybot/internal/domain"
	"github.com/alejandrodnm/polybot/internal/application/engine"
)

// mergeCompletePairs executes real on-chain merges for fully filled pairs.
func (le *Engine) mergeCompletePairs(ctx context.Context) (merges int, totalProfit, totalGas float64, err error) {
	filledOrders, err := le.store.GetAllLiveOrders(ctx, string(domain.LiveStatusFilled))
	if err != nil {
		return 0, 0, 0, fmt.Errorf("mergeCompletePairs: %w", err)
	}

	byPair := make(map[string][]domain.LiveOrder)
	for _, o := range filledOrders {
		byPair[o.PairID] = append(byPair[o.PairID], o)
	}

	now := time.Now().UTC()
	mergeDelay := time.Duration(mergeDelayMins) * time.Minute

	gasCostUSD, _ := le.merger.EstimateGasCostUSD(ctx)
	if gasCostUSD <= 0 {
		gasCostUSD = 0.05
	}

	for _, orders := range byPair {
		var yes, no *domain.LiveOrder
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
			continue
		}

		yesSets := yes.FilledSize / yes.BidPrice
		noSets := no.FilledSize / no.BidPrice
		mergeable := math.Min(yesSets, noSets)
		if mergeable < 1 {
			continue
		}
		mergeAmountUSDC := math.Floor(mergeable)

		yesCostMerged := mergeAmountUSDC * yes.BidPrice
		noCostMerged := mergeAmountUSDC * no.BidPrice
		capitalSpent := yesCostMerged + noCostMerged
		grossReceipt := mergeAmountUSDC
		spread := grossReceipt - capitalSpent

		netProfit := spread - gasCostUSD
		if netProfit < le.cfg.MinMergeProfit {
			slog.Debug("live: skipping merge (not profitable after gas)",
				"market", engine.TruncateStr(yes.Question, 30),
				"spread", fmt.Sprintf("$%.4f", spread),
				"gas", fmt.Sprintf("$%.4f", gasCostUSD),
				"net", fmt.Sprintf("$%.4f", netProfit),
			)
			if netProfit < 0 {
				le.breaker.RecordLoss(netProfit)
			}
			continue
		}

		mergeResult, err := le.merger.MergePositions(ctx, yes.ConditionID, mergeAmountUSDC, yes.NegRisk)
		if err != nil {
			slog.Warn("live: merge failed", "condition", yes.ConditionID, "err", err)
			continue
		}

		mergeResult.PairID = yes.PairID
		mergeResult.SpreadProfit = netProfit

		if err := le.store.SaveMergeResult(ctx, mergeResult); err != nil {
			slog.Warn("live: error saving merge result", "err", err)
		}

		mergedAt := time.Now().UTC()
		_ = le.store.MarkLiveOrderMerged(ctx, yes.ID, mergedAt)
		_ = le.store.MarkLiveOrderMerged(ctx, no.ID, mergedAt)

		merges++
		totalProfit += netProfit
		totalGas += mergeResult.GasCostUSD

		if netProfit > 0 {
			le.breaker.RecordWin(netProfit)
		} else {
			le.breaker.RecordLoss(netProfit)
		}

		slog.Info("live: MERGED pair",
			"market", engine.TruncateStr(yes.Question, 30),
			"usdc_in", fmt.Sprintf("$%.2f", capitalSpent),
			"usdc_out", fmt.Sprintf("$%.2f", grossReceipt),
			"gas", fmt.Sprintf("$%.4f", gasCostUSD),
			"net_profit", fmt.Sprintf("$%.4f", netProfit),
		)
	}

	return merges, totalProfit, totalGas, nil
}
