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
	ScanInterval time.Duration
	Filter       FilterConfig
	OrderSize    float64
	FeeRate      float64
	DryRun       bool
}

// DefaultConfig devuelve una configuración sensata para producción.
func DefaultConfig() Config {
	return Config{
		ScanInterval: 30 * time.Second,
		Filter:       DefaultFilterConfig(),
		OrderSize:    defaultOrderSize,
		FeeRate:      defaultFeeRate,
	}
}

// Scanner es el orquestador principal del loop de escaneo.
type Scanner struct {
	cfg      Config
	markets  ports.MarketProvider
	books    ports.BookProvider
	storage  ports.Storage
	notifier ports.Notifier
	analyzer *Analyzer
	filter   *Filter
}

// New crea un Scanner con todas las dependencias inyectadas.
func New(
	cfg Config,
	markets ports.MarketProvider,
	books ports.BookProvider,
	storage ports.Storage,
	notifier ports.Notifier,
) *Scanner {
	return &Scanner{
		cfg:      cfg,
		markets:  markets,
		books:    books,
		storage:  storage,
		notifier: notifier,
		analyzer: NewAnalyzer(cfg.OrderSize, cfg.FeeRate),
		filter:   NewFilter(cfg.Filter),
	}
}

// Run ejecuta el loop de escaneo hasta que el contexto se cancele.
// Si cfg.DryRun está activo, solo ejecuta un ciclo.
func (s *Scanner) Run(ctx context.Context) error {
	slog.Info("scanner starting",
		"interval", s.cfg.ScanInterval,
		"dry_run", s.cfg.DryRun,
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

	if err := s.notifier.Notify(ctx, opps); err != nil {
		slog.Warn("notifier error", "err", err)
	}

	if s.storage != nil {
		if err := s.storage.SaveScan(ctx, opps); err != nil {
			slog.Warn("storage error", "err", err)
		}
	}

	slog.Info("scan cycle complete",
		"opportunities", len(opps),
		"duration", time.Since(start).Round(time.Millisecond),
	)
	return nil
}

// cycle hace fetch → analyze → filter → rank y devuelve las oportunidades.
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

	var opps []domain.Opportunity
	for _, market := range markets {
		yesBook, noBook, ok := getBooksForMarket(market, books)
		if !ok {
			slog.Debug("missing books for market", "condition_id", market.ConditionID)
			continue
		}

		opp, err := s.analyzer.Analyze(ctx, market, yesBook, noBook)
		if err != nil {
			slog.Debug("analyze failed", "condition_id", market.ConditionID, "err", err)
			continue
		}
		opps = append(opps, opp)
	}

	filtered := s.filter.Apply(opps)
	ranked := rankByScore(filtered)
	return ranked, nil
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

// rankByScore ordena las oportunidades por YourDailyReward descendente (C4).
// Esto pone primero los mercados más rentables para TI, no los de mayor pool total.
func rankByScore(opps []domain.Opportunity) []domain.Opportunity {
	sort.Slice(opps, func(i, j int) bool {
		return opps[i].YourDailyReward > opps[j].YourDailyReward
	})
	return opps
}
