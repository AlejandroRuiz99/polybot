package polymarket

// clob.go — Polymarket CLOB API adapter.
//
// FetchOrderBooks usa goroutines concurrentes para disparar múltiples batch requests
// en paralelo. El rate limiter (token bucket) en doWithRetry controla el ritmo
// automáticamente — las goroutines se "autolimitan" sin necesidad de semáforo explícito.
// Resultado: reducción del tiempo de fetch de books de ~15s a ~3s en producción.

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/alejandrodnm/polybot/internal/domain"
)

const (
	samplingMarketsPath = "/sampling-markets"
	booksPath           = "/books"
	pageSize            = 100
	batchSize           = 20 // máx token_ids por request a /books
)

// FetchSamplingMarkets devuelve todos los mercados con rewards activos.
// Pagina automáticamente usando next_cursor hasta agotar los resultados.
func (c *Client) FetchSamplingMarkets(ctx context.Context) ([]domain.Market, error) {
	var all []domain.Market
	cursor := ""

	for {
		url := fmt.Sprintf("%s%s?limit=%d", c.clobBase, samplingMarketsPath, pageSize)
		if cursor != "" {
			url += "&next_cursor=" + cursor
		}

		var resp samplingMarketsResponse
		if err := c.get(ctx, c.clobLimiter, url, &resp); err != nil {
			return nil, fmt.Errorf("clob.FetchSamplingMarkets: %w", err)
		}

		markets := mapSamplingMarkets(resp.Data)
		all = append(all, markets...)

		slog.Debug("fetched sampling markets page",
			"count", len(resp.Data),
			"total", len(all),
			"has_more", resp.NextCursor != "" && resp.NextCursor != "LTE=",
		)

		// "LTE=" es el cursor vacío codificado en base64 que indica última página
		if resp.NextCursor == "" || resp.NextCursor == "LTE=" {
			break
		}
		cursor = resp.NextCursor
	}

	slog.Info("sampling markets fetched", "total", len(all))

	// Enriquecer con metadata de Gamma (nombres, fechas, fees, volumen)
	enriched, enrichErr := c.EnrichWithGamma(ctx, all)
	if enrichErr != nil {
		// El enriquecimiento es opcional — logueamos pero no fallamos
		slog.Warn("gamma enrichment failed, continuing without names", "err", enrichErr)
	} else {
		all = enriched
	}

	return all, nil
}

// FetchOrderBooks obtiene los orderbooks para los token_ids dados usando el endpoint batch.
// Lanza un goroutine por batch (máx batchSize tokens cada uno) y los ejecuta
// concurrentemente. El rate limiter en fetchBooksBatch controla el ritmo automáticamente.
func (c *Client) FetchOrderBooks(ctx context.Context, tokenIDs []string) (map[string]domain.OrderBook, error) {
	if len(tokenIDs) == 0 {
		return map[string]domain.OrderBook{}, nil
	}

	// Partir token IDs en batches de batchSize
	batches := splitBatches(tokenIDs, batchSize)

	type batchResult struct {
		books map[string]domain.OrderBook
		err   error
		idx   int
	}

	resultCh := make(chan batchResult, len(batches))
	var wg sync.WaitGroup

	for i, batch := range batches {
		i, batch := i, batch
		wg.Add(1)
		go func() {
			defer wg.Done()
			books, err := c.fetchBooksBatch(ctx, batch)
			resultCh <- batchResult{books: books, err: err, idx: i}
		}()
	}

	// Cerrar el canal cuando todos los goroutines terminen
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	result := make(map[string]domain.OrderBook, len(tokenIDs))
	var firstErr error

	for r := range resultCh {
		if r.err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("clob.FetchOrderBooks batch %d: %w", r.idx, r.err)
			}
			continue
		}
		for k, v := range r.books {
			result[k] = v
		}
	}

	if firstErr != nil {
		return nil, firstErr
	}

	slog.Debug("order books fetched", "tokens", len(tokenIDs), "books", len(result))
	return result, nil
}

// splitBatches divide tokenIDs en slices de tamaño máximo size.
func splitBatches(tokenIDs []string, size int) [][]string {
	if size <= 0 {
		size = batchSize
	}
	batches := make([][]string, 0, (len(tokenIDs)+size-1)/size)
	for i := 0; i < len(tokenIDs); i += size {
		end := i + size
		if end > len(tokenIDs) {
			end = len(tokenIDs)
		}
		batches = append(batches, tokenIDs[i:end])
	}
	return batches
}

// fetchBooksBatch hace un POST /books para un batch de token_ids.
func (c *Client) fetchBooksBatch(ctx context.Context, tokenIDs []string) (map[string]domain.OrderBook, error) {
	body := make([]orderBookRequest, len(tokenIDs))
	for i, id := range tokenIDs {
		body[i] = orderBookRequest{TokenID: id}
	}

	var resp []orderBookResponse
	url := c.clobBase + booksPath
	if err := c.post(ctx, c.booksLimiter, url, body, &resp); err != nil {
		return nil, fmt.Errorf("POST /books: %w", err)
	}

	return mapOrderBooks(resp), nil
}
