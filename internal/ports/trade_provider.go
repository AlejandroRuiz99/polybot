package ports

import (
	"context"

	"github.com/alejandrodnm/polybot/internal/domain"
)

// TradeProvider obtiene trades históricos de un token.
type TradeProvider interface {
	FetchTrades(ctx context.Context, tokenID string) ([]domain.Trade, error)
}
