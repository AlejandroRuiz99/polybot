package ports

import (
	"context"

	"github.com/alejandrodnm/polybot/internal/domain"
)

// BookProvider obtiene orderbooks del CLOB usando el endpoint batch.
type BookProvider interface {
	// FetchOrderBooks devuelve los orderbooks para los token_ids dados.
	// Internamente agrupa los IDs en batches de máx 20 para minimizar requests.
	FetchOrderBooks(ctx context.Context, tokenIDs []string) (map[string]domain.OrderBook, error)
}
