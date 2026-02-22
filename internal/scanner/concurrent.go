package scanner

// concurrent.go — worker pool para análisis paralelo de mercados.
//
// Ventaja sobre otros bots: analizar 300+ mercados en paralelo reduce el tiempo
// de ciclo de ~20s (secuencial) a ~3-5s (paralelo), detectando arbitrajes más rápido.

import (
	"context"
	"log/slog"
	"runtime"
	"sync"

	"github.com/alejandrodnm/polybot/internal/domain"
)

// analyzeMarketsConcurrent analiza todos los mercados en paralelo usando un worker pool.
// Los workers procesan mercados del workCh concurrentemente; el rate limiter
// de API ya fue ejercido en el fetch de books anterior.
//
// Si workers <= 0 usa runtime.NumCPU() × 2 para saturar los cores disponibles.
func analyzeMarketsConcurrent(
	ctx context.Context,
	analyzer *Analyzer,
	markets []domain.Market,
	books map[string]domain.OrderBook,
	workers int,
) []domain.Opportunity {
	if workers <= 0 {
		workers = runtime.NumCPU() * 2
	}

	type work struct {
		market  domain.Market
		yesBook domain.OrderBook
		noBook  domain.OrderBook
	}

	workCh := make(chan work, len(markets))
	resultCh := make(chan domain.Opportunity, len(markets))

	// Worker pool: cada worker toma tareas de workCh y envía resultados a resultCh.
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for w := range workCh {
				opp, err := analyzer.Analyze(ctx, w.market, w.yesBook, w.noBook)
				if err != nil {
					slog.Debug("analyze failed",
						"condition_id", w.market.ConditionID,
						"err", err,
					)
					continue
				}
				resultCh <- opp
			}
		}()
	}

	// Alimentar el work channel con los mercados que tienen books disponibles.
	queued := 0
	for _, market := range markets {
		yesBook, noBook, ok := getBooksForMarket(market, books)
		if !ok {
			slog.Debug("missing books for market", "condition_id", market.ConditionID)
			continue
		}
		workCh <- work{market: market, yesBook: yesBook, noBook: noBook}
		queued++
	}
	close(workCh)

	// Cerrar resultCh cuando todos los workers terminen.
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	opps := make([]domain.Opportunity, 0, queued)
	for opp := range resultCh {
		opps = append(opps, opp)
	}

	slog.Debug("concurrent analysis complete",
		"markets_queued", queued,
		"opportunities", len(opps),
		"workers", workers,
	)

	return opps
}
