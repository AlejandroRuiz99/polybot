package scanner

import (
	"context"

	"github.com/alejandrodnm/polybot/internal/domain"
)

// StrategyAnalyzer es el subconjunto de strategy.Strategy que usa el Analyzer.
type StrategyAnalyzer interface {
	Analyze(ctx context.Context, market domain.Market, yesBook, noBook domain.OrderBook) (domain.Opportunity, error)
}

// Analyzer delega el cálculo de métricas a una Strategy inyectada.
type Analyzer struct {
	strategy StrategyAnalyzer
}

// NewAnalyzer crea un Analyzer que delega en la strategy dada.
func NewAnalyzer(s StrategyAnalyzer) *Analyzer {
	return &Analyzer{strategy: s}
}

// Analyze calcula todas las métricas para un mercado dado sus orderbooks YES y NO.
func (a *Analyzer) Analyze(ctx context.Context, market domain.Market, yesBook, noBook domain.OrderBook) (domain.Opportunity, error) {
	return a.strategy.Analyze(ctx, market, yesBook, noBook)
}
