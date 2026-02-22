package strategy

import (
	"context"

	"github.com/alejandrodnm/polybot/internal/domain"
)

// Strategy define el contrato para analizar mercados y detectar oportunidades.
// Cada estrategia encapsula una lógica de trading diferente.
type Strategy interface {
	// Name devuelve el identificador único de la estrategia.
	Name() string

	// Analyze evalúa un mercado con su orderbook y devuelve una Opportunity
	// con todas las métricas calculadas. Devuelve error si los datos son insuficientes.
	Analyze(ctx context.Context, market domain.Market, yesBook, noBook domain.OrderBook) (domain.Opportunity, error)

	// Filter devuelve true si la oportunidad cumple los criterios mínimos de la estrategia.
	Filter(opp domain.Opportunity) bool
}

// Registry mantiene las estrategias disponibles indexadas por nombre.
type Registry map[string]Strategy

// NewRegistry crea un registry vacío.
func NewRegistry() Registry {
	return make(Registry)
}

// Register añade una estrategia al registry.
func (r Registry) Register(s Strategy) {
	r[s.Name()] = s
}

// Get devuelve la estrategia por nombre.
func (r Registry) Get(name string) (Strategy, bool) {
	s, ok := r[name]
	return s, ok
}
