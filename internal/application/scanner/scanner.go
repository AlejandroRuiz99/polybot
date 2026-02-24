package scanner

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/alejandrodnm/polybot/internal/domain"
	"github.com/alejandrodnm/polybot/internal/ports"
)

// Config contiene la configuración del scanner.
type Config struct {
	ScanInterval    time.Duration
	Filter          FilterConfig
	AnalysisWorkers int // goroutines para análisis paralelo (0 = NumCPU*2)
	DryRun          bool
}

// Scanner es el orquestador principal del loop de escaneo.
type Scanner struct {
	cfg             Config
	markets         ports.MarketProvider
	books           ports.BookProvider
	storage         ports.Storage
	notifier        ports.Notifier
	analyzer        *Analyzer
	filter          *Filter
	previousGoldIDs map[string]bool // Gold markets del ciclo anterior para alertas
}

// New crea un Scanner con todas las dependencias inyectadas.
// La strategy se inyecta desde fuera (cmd/) para respetar la inversión de dependencias.
func New(
	cfg Config,
	markets ports.MarketProvider,
	books ports.BookProvider,
	storage ports.Storage,
	notifier ports.Notifier,
	strategy StrategyAnalyzer,
) *Scanner {
	return &Scanner{
		cfg:             cfg,
		markets:         markets,
		books:           books,
		storage:         storage,
		notifier:        notifier,
		analyzer:        NewAnalyzer(strategy),
		filter:          NewFilter(cfg.Filter),
		previousGoldIDs: make(map[string]bool),
	}
}

// SetFilter replaces the scanner's filter (used by live engine to widen criteria).
func (s *Scanner) SetFilter(f *Filter) {
	s.filter = f
}

// Run ejecuta el loop de escaneo hasta que el contexto se cancele.
// Si cfg.DryRun está activo, solo ejecuta un ciclo.
func (s *Scanner) Run(ctx context.Context) error {
	slog.Info("scanner starting",
		"interval", s.cfg.ScanInterval,
		"dry_run", s.cfg.DryRun,
		"workers", s.cfg.AnalysisWorkers,
	)

	if err := s.runCycle(ctx); err != nil {
		slog.Error("scan cycle failed", "err", err)
		if s.cfg.DryRun {
			return err
		}
	}

	if s.cfg.DryRun {
		return nil
	}

	ticker := time.NewTicker(s.cfg.ScanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("scanner stopped")
			return nil
		case <-ticker.C:
			if err := s.runCycle(ctx); err != nil {
				slog.Error("scan cycle failed", "err", err)
			}
		}
	}
}

// RunOnce ejecuta exactamente un ciclo de escaneo y devuelve las oportunidades.
func (s *Scanner) RunOnce(ctx context.Context) ([]domain.Opportunity, error) {
	return s.cycle(ctx)
}

// runCycle ejecuta un ciclo completo y notifica/persiste los resultados.
func (s *Scanner) runCycle(ctx context.Context) error {
	start := time.Now()

	opps, err := s.cycle(ctx)
	if err != nil {
		return err
	}

	// Detectar nuevos mercados Gold y emitir alertas
	s.emitGoldAlerts(opps)

	if err := s.notifier.Notify(ctx, opps); err != nil {
		slog.Warn("notifier error", "err", err)
	}

	if s.storage != nil {
		if err := s.storage.SaveScan(ctx, opps); err != nil {
			slog.Warn("storage error", "err", err)
		}
	}

	gold, silver := countCategories(opps)
	slog.Info("scan cycle complete",
		"opportunities", len(opps),
		"gold", gold,
		"silver", silver,
		"duration", time.Since(start).Round(time.Millisecond),
	)
	return nil
}

// cycle hace fetch → concurrent analyze → filter → rank y devuelve las oportunidades.
func (s *Scanner) cycle(ctx context.Context) ([]domain.Opportunity, error) {
	markets, err := s.markets.FetchSamplingMarkets(ctx)
	if err != nil {
		return nil, fmt.Errorf("scanner.cycle: fetch markets: %w", err)
	}

	tokenIDs := extractTokenIDs(markets)
	books, err := s.books.FetchOrderBooks(ctx, tokenIDs)
	if err != nil {
		return nil, fmt.Errorf("scanner.cycle: fetch books: %w", err)
	}

	// Análisis paralelo: reduce tiempo de ciclo de ~20s a ~3-5s
	opps := analyzeMarketsConcurrent(ctx, s.analyzer, markets, books, s.cfg.AnalysisWorkers)

	filtered := s.filter.Apply(opps)
	ranked := rankByScore(filtered)
	return ranked, nil
}

// emitGoldAlerts registra alertas para mercados Gold nuevos (no vistos en el ciclo anterior).
// Si además hay true arbitrage (gap > 0), la alerta usa nivel ERROR para máxima visibilidad.
func (s *Scanner) emitGoldAlerts(opps []domain.Opportunity) {
	newGoldIDs := make(map[string]bool, len(opps))

	for _, opp := range opps {
		if opp.Category != domain.CategoryGold {
			continue
		}
		newGoldIDs[opp.Market.ConditionID] = true

		if s.previousGoldIDs[opp.Market.ConditionID] {
			continue // ya conocido
		}

		attrs := []any{
			"market", opp.Market.Question,
			"yes_ask", fmt.Sprintf("%.4f", opp.Arbitrage.BestAskYES),
			"no_ask", fmt.Sprintf("%.4f", opp.Arbitrage.BestAskNO),
			"arb_gap", fmt.Sprintf("%.4f", opp.Arbitrage.ArbitrageGap),
			"your_daily_reward", fmt.Sprintf("$%.4f", opp.YourDailyReward),
			"combined_score", fmt.Sprintf("$%.4f", opp.CombinedScore),
			"end", opp.Market.EndDate.Format("2006-01-02"),
		}

		if opp.Arbitrage.HasArbitrage {
			// TRUE ARBITRAGE: cada fill es ganancia garantizada — máxima prioridad
			attrs = append(attrs,
				"max_fillable", fmt.Sprintf("$%.0f", opp.Arbitrage.MaxFillable),
				"action", "BID YES@"+fmt.Sprintf("%.4f", opp.Arbitrage.BestAskYES)+
					" + BID NO@"+fmt.Sprintf("%.4f", opp.Arbitrage.BestAskNO),
			)
			slog.Error("*** TRUE ARBITRAGE + REWARDS ***", attrs...)
		} else {
			slog.Warn("NEW GOLD (low fill risk)", attrs...)
		}
	}

	s.previousGoldIDs = newGoldIDs
}

// extractTokenIDs extrae todos los token_ids de los mercados.
func extractTokenIDs(markets []domain.Market) []string {
	ids := make([]string, 0, len(markets)*2)
	for _, m := range markets {
		for _, t := range m.Tokens {
			if t.TokenID != "" {
				ids = append(ids, t.TokenID)
			}
		}
	}
	return ids
}

// getBooksForMarket busca los orderbooks YES y NO para un mercado.
func getBooksForMarket(m domain.Market, books map[string]domain.OrderBook) (yes, no domain.OrderBook, ok bool) {
	yes, okYes := books[m.YesToken().TokenID]
	no, okNo := books[m.NoToken().TokenID]
	return yes, no, okYes && okNo
}

// rankByScore ordena por CombinedScore descendente:
// Gold primero (arb + reward), luego Silver (reward puro), etc.
func rankByScore(opps []domain.Opportunity) []domain.Opportunity {
	sort.Slice(opps, func(i, j int) bool {
		// Primero por categoría (Gold < Silver < Bronze), luego por CombinedScore
		if opps[i].Category != opps[j].Category {
			return opps[i].Category < opps[j].Category
		}
		return opps[i].CombinedScore > opps[j].CombinedScore
	})
	return opps
}

// countCategories cuenta oportunidades Gold y Silver.
func countCategories(opps []domain.Opportunity) (gold, silver int) {
	for _, o := range opps {
		switch o.Category {
		case domain.CategoryGold:
			gold++
		case domain.CategorySilver:
			silver++
		}
	}
	return
}
