package scanner

// backtest.go — cruza oportunidades del scanner con trades reales de la API
// para validar si la estrategia de reward farming es realmente rentable.
//
// Para cada top mercado:
// 1. Descarga trades de las últimas 24h para YES y NO tokens
// 2. Simula: "si tuviera un BID a precio X, ¿cuántas veces me habrían llenado?"
// 3. Calcula el PnL real: reward - (fillCost × fillRate observado)

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/alejandrodnm/polybot/internal/domain"
	"github.com/alejandrodnm/polybot/internal/ports"
)

const backtestTopN = 10

// Backtest ejecuta el backtest sobre los top N mercados del último scan.
func Backtest(
	ctx context.Context,
	opps []domain.Opportunity,
	trades ports.TradeProvider,
	orderSize float64,
) ([]domain.BacktestResult, error) {
	top := opps
	if len(top) > backtestTopN {
		top = opps[:backtestTopN]
	}

	var results []domain.BacktestResult
	for i, opp := range top {
		slog.Info("backtesting market",
			"n", fmt.Sprintf("%d/%d", i+1, len(top)),
			"market", opp.Market.Question,
		)

		result, err := backtestMarket(ctx, opp, trades, orderSize)
		if err != nil {
			slog.Warn("backtest failed for market",
				"market", opp.Market.Question,
				"err", err,
			)
			continue
		}
		results = append(results, result)
	}

	return results, nil
}

func backtestMarket(
	ctx context.Context,
	opp domain.Opportunity,
	trades ports.TradeProvider,
	orderSize float64,
) (domain.BacktestResult, error) {
	yesID := opp.Market.YesToken().TokenID
	noID := opp.Market.NoToken().TokenID

	if yesID == "" || noID == "" {
		return domain.BacktestResult{}, fmt.Errorf("missing token IDs")
	}

	yesTrades, err := trades.FetchTrades(ctx, yesID)
	if err != nil {
		return domain.BacktestResult{}, fmt.Errorf("fetch YES trades: %w", err)
	}

	noTrades, err := trades.FetchTrades(ctx, noID)
	if err != nil {
		return domain.BacktestResult{}, fmt.Errorf("fetch NO trades: %w", err)
	}

	// Determinar ventana temporal
	period := tradePeriod(yesTrades, noTrades)
	days := period.Hours() / 24
	if days < 0.1 {
		days = 1 // mínimo 1 día para evitar div/0
	}

	// Precio de simulación: tu BID estaría al BestBid actual del book
	simBidYes := opp.YesBook.BestBid()
	simBidNo := opp.NoBook.BestBid()
	if simBidYes == 0 {
		simBidYes = opp.YesBook.BestAsk() * 0.99
	}
	if simBidNo == 0 {
		simBidNo = opp.NoBook.BestAsk() * 0.99
	}

	// Simular fills: un SELL al precio ≤ mi bid me habría llenado
	fillsYes := countFillsAtPrice(yesTrades, simBidYes)
	fillsNo := countFillsAtPrice(noTrades, simBidNo)

	// Fills completos por día = min(fillsYes, fillsNo) / days
	// Porque necesitas AMBOS lados para completar un par
	fillsBothPerDay := math.Min(float64(fillsYes), float64(fillsNo)) / days

	// P&L real
	realPnL := domain.EstimateNetProfit(
		opp.YourDailyReward,
		opp.FillCostUSDC,
		fillsBothPerDay,
	)

	verdict := "PROFITABLE"
	if realPnL <= 0 {
		verdict = "NOT_PROFITABLE"
	} else if realPnL < opp.YourDailyReward*0.3 {
		verdict = "MARGINAL"
	}

	return domain.BacktestResult{
		Market:          opp.Market,
		Opportunity:     opp,
		TokenYesID:      yesID,
		TokenNoID:       noID,
		Period:          period,
		TotalTradesYes:  len(yesTrades),
		TotalTradesNo:   len(noTrades),
		SimBidYes:       simBidYes,
		SimBidNo:        simBidNo,
		FillsYes:        fillsYes,
		FillsNo:         fillsNo,
		FillsBothPerDay: fillsBothPerDay,
		RealFillRate:    fillsBothPerDay,
		RealPnLDaily:    realPnL,
		Verdict:         verdict,
	}, nil
}

// countFillsAtPrice cuenta cuántos sells habrían llenado un bid a `bidPrice`.
// Un seller (SELL side) al precio ≤ bidPrice te llena.
func countFillsAtPrice(trades []domain.Trade, bidPrice float64) int {
	n := 0
	for _, t := range trades {
		if t.Side == "SELL" && t.Price <= bidPrice {
			n++
		}
	}
	return n
}

// tradePeriod calcula la ventana temporal entre el trade más viejo y el más reciente.
func tradePeriod(yes, no []domain.Trade) time.Duration {
	var oldest, newest time.Time
	for _, t := range append(yes, no...) {
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
	if oldest.IsZero() || newest.IsZero() {
		return 24 * time.Hour
	}
	d := newest.Sub(oldest)
	if d < time.Hour {
		return 24 * time.Hour
	}
	return d
}
