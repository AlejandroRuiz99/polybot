package polymarket

import (
	"context"
	"fmt"
	"log/slog"

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
// Agrupa los IDs en batches de máx batchSize para minimizar requests.
func (c *Client) FetchOrderBooks(ctx context.Context, tokenIDs []string) (map[string]domain.OrderBook, error) {
	result := make(map[string]domain.OrderBook, len(tokenIDs))

	for i := 0; i < len(tokenIDs); i += batchSize {
		end := i + batchSize
		if end > len(tokenIDs) {
			end = len(tokenIDs)
		}
		batch := tokenIDs[i:end]

		books, err := c.fetchBooksBatch(ctx, batch)
		if err != nil {
			return nil, fmt.Errorf("clob.FetchOrderBooks batch %d-%d: %w", i, end, err)
		}
		for k, v := range books {
			result[k] = v
		}
	}

	slog.Debug("order books fetched", "tokens", len(tokenIDs), "books", len(result))
	return result, nil
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
