package strategy

import (
	"context"

	"github.com/alejandrodnm/polybot/internal/domain"
)

// Strategy define el contrato para analizar mercados y detectar oportunidades.
// Cada estrategia encapsula una lógica de trading diferente.
type Strategy interface {
	// Analyze evalúa un mercado con su orderbook y devuelve una Opportunity
	// con todas las métricas calculadas. Devuelve error si los datos son insuficientes.
	Analyze(ctx context.Context, market domain.Market, yesBook, noBook domain.OrderBook) (domain.Opportunity, error)
}
