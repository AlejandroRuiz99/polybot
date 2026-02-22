package ports

import (
	"context"

	"github.com/alejandrodnm/polybot/internal/domain"
)

// MarketProvider obtiene los mercados con rewards activos desde el CLOB.
type MarketProvider interface {
	// FetchSamplingMarkets devuelve todos los mercados actualmente
	// seleccionados para recibir rewards de liquidez.
	// Pagina automáticamente hasta obtener todos los resultados.
	FetchSamplingMarkets(ctx context.Context) ([]domain.Market, error)
}
