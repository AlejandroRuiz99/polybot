package storage

// sqlite.go — almacenamiento eficiente y sin ruido.
//
// Estrategia:
//   - `cycles`: resumen ligero por ciclo (gold/silver count, best score). Siempre 1 fila.
//   - `opportunities`: UNA fila por mercado (UPSERT). Solo Gold y Silver.
//     Bronze/Avoid no se persisten — no aportan señal útil como histórico.
//   - Cache en memoria: evita writes si el estado no cambió (> 5% en score,
//     o cambio de categoría/arbitraje). En un ciclo normal con 369 mercados,
//     la mayoría no cambia → reducción ~90% de escrituras a disco.
//   - Prune automático al arrancar: cycles > 30d, opportunities no vistas en 14d.

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/alejandrodnm/polybot/internal/domain"
	_ "modernc.org/sqlite"
)

const schema = `
-- Resumen ligero por ciclo de scan
CREATE TABLE IF NOT EXISTS cycles (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    scanned_at DATETIME NOT NULL,
    total      INTEGER  NOT NULL DEFAULT 0,
    gold       INTEGER  NOT NULL DEFAULT 0,
    silver     INTEGER  NOT NULL DEFAULT 0,
    best_score REAL     NOT NULL DEFAULT 0
);

-- Una fila por mercado Gold/Silver, sin duplicados
CREATE TABLE IF NOT EXISTS opportunities (
    condition_id   TEXT PRIMARY KEY,
    question       TEXT,
    slug           TEXT,
    category       TEXT    NOT NULL,
    combined_score REAL    NOT NULL DEFAULT 0,
    your_daily_rwd REAL    NOT NULL DEFAULT 0,
    arb_gap        REAL    NOT NULL DEFAULT 0,
    has_arbitrage  INTEGER NOT NULL DEFAULT 0,
    yes_ask        REAL    NOT NULL DEFAULT 0,
    no_ask         REAL    NOT NULL DEFAULT 0,
    yes_no_sum     REAL    NOT NULL DEFAULT 0,
    max_fillable   REAL    NOT NULL DEFAULT 0,
    spread_total   REAL    NOT NULL DEFAULT 0,
    competition    REAL    NOT NULL DEFAULT 0,
    daily_rate     REAL    NOT NULL DEFAULT 0,
    max_spread     REAL    NOT NULL DEFAULT 0,
    end_date       DATETIME,
    first_seen     DATETIME NOT NULL,
    last_seen      DATETIME NOT NULL,
    peak_combined  REAL    NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_cycles_at    ON cycles(scanned_at DESC);
CREATE INDEX IF NOT EXISTS idx_opp_cat      ON opportunities(category);
CREATE INDEX IF NOT EXISTS idx_opp_last     ON opportunities(last_seen DESC);
CREATE INDEX IF NOT EXISTS idx_opp_combined ON opportunities(combined_score DESC);
`

const (
	retentionCycles = 30 * 24 * time.Hour // ciclos: 30 días
	retentionOpps   = 14 * 24 * time.Hour // oportunidades: 14 días (la mayoría se resuelven antes)
	scoreChangePct  = 0.05                // 5% de cambio en score → reescribir
)

// cachedState es el snapshot del último estado guardado de un mercado.
type cachedState struct {
	category     string
	combined     float64
	hasArbitrage bool
}

// SQLiteStorage implementa ports.Storage usando SQLite (pure Go, sin CGo).
type SQLiteStorage struct {
	db    *sql.DB
	cache map[string]cachedState // conditionID → estado guardado
	mu    sync.Mutex
}

// NewSQLiteStorage abre (o crea) la base de datos en la ruta dada.
// Aplica el schema, limpia datos antiguos y precarga la cache.
func NewSQLiteStorage(path string) (*SQLiteStorage, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("storage.NewSQLiteStorage: open %q: %w", path, err)
	}
	db.SetMaxOpenConns(1) // SQLite es single-writer
	db.SetMaxIdleConns(1)

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("storage.NewSQLiteStorage: apply schema: %w", err)
	}

	s := &SQLiteStorage{
		db:    db,
		cache: make(map[string]cachedState),
	}
	s.pruneOld(context.Background())
	s.warmCache(context.Background())
	return s, nil
}

// SaveScan persiste el resumen del ciclo y hace upsert de las oportunidades Gold/Silver
// que cambiaron respecto al ciclo anterior (usando caché en memoria).
func (s *SQLiteStorage) SaveScan(ctx context.Context, opportunities []domain.Opportunity) error {
	if len(opportunities) == 0 {
		return nil
	}

	now := time.Now().UTC()

	// 1. Resumen del ciclo — siempre una fila, pesa ~50 bytes
	gold, silver, bestScore := cycleSummary(opportunities)
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO cycles (scanned_at, total, gold, silver, best_score) VALUES (?, ?, ?, ?, ?)`,
		now, len(opportunities), gold, silver, bestScore,
	); err != nil {
		return fmt.Errorf("storage.SaveScan: insert cycle: %w", err)
	}

	// 2. Upsert de Gold/Silver que cambiaron
	toWrite := s.filterChanged(opportunities, now)
	if len(toWrite) == 0 {
		return nil // nada nuevo — la gran mayoría de ciclos terminan aquí
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage.SaveScan: begin tx: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO opportunities
			(condition_id, question, slug, category, combined_score, your_daily_rwd,
			 arb_gap, has_arbitrage, yes_ask, no_ask, yes_no_sum, max_fillable,
			 spread_total, competition, daily_rate, max_spread, end_date,
			 first_seen, last_seen, peak_combined)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(condition_id) DO UPDATE SET
			question       = excluded.question,
			category       = excluded.category,
			combined_score = excluded.combined_score,
			your_daily_rwd = excluded.your_daily_rwd,
			arb_gap        = excluded.arb_gap,
			has_arbitrage  = excluded.has_arbitrage,
			yes_ask        = excluded.yes_ask,
			no_ask         = excluded.no_ask,
			yes_no_sum     = excluded.yes_no_sum,
			max_fillable   = excluded.max_fillable,
			spread_total   = excluded.spread_total,
			competition    = excluded.competition,
			daily_rate     = excluded.daily_rate,
			max_spread     = excluded.max_spread,
			end_date       = excluded.end_date,
			last_seen      = excluded.last_seen,
			peak_combined  = MAX(peak_combined, excluded.combined_score)
	`)
	if err != nil {
		return fmt.Errorf("storage.SaveScan: prepare: %w", err)
	}
	defer stmt.Close()

	for _, opp := range toWrite {
		hasArb := 0
		if opp.Arbitrage.HasArbitrage {
			hasArb = 1
		}
		var endDate *time.Time
		if !opp.Market.EndDate.IsZero() {
			t := opp.Market.EndDate.UTC()
			endDate = &t
		}

		if _, err := stmt.ExecContext(ctx,
			opp.Market.ConditionID,
			opp.Market.Question,
			opp.Market.Slug,
			opp.Category.String(),
			opp.CombinedScore,
			opp.YourDailyReward,
			opp.Arbitrage.ArbitrageGap,
			hasArb,
			opp.Arbitrage.BestAskYES,
			opp.Arbitrage.BestAskNO,
			opp.Arbitrage.SumBestAsk,
			opp.Arbitrage.MaxFillable,
			opp.SpreadTotal,
			opp.Competition,
			opp.Market.Rewards.DailyRate,
			opp.Market.Rewards.MaxSpread,
			endDate,
			now, // first_seen: ignorado en ON CONFLICT (no se sobreescribe)
			now, // last_seen
			opp.CombinedScore,
		); err != nil {
			return fmt.Errorf("storage.SaveScan: upsert %s: %w", opp.Market.ConditionID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage.SaveScan: commit: %w", err)
	}
	return nil
}

// GetHistory devuelve oportunidades Gold/Silver cuyo last_seen está en el rango dado.
// Ordenadas por combined_score desc — las mejores primero.
func (s *SQLiteStorage) GetHistory(ctx context.Context, from, to time.Time) ([]domain.Opportunity, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT condition_id, question, slug, category,
		       combined_score, your_daily_rwd, arb_gap, has_arbitrage,
		       spread_total, competition, daily_rate, max_spread, last_seen
		FROM opportunities
		WHERE last_seen BETWEEN ? AND ?
		ORDER BY combined_score DESC
	`, from.UTC(), to.UTC())
	if err != nil {
		return nil, fmt.Errorf("storage.GetHistory: query: %w", err)
	}
	defer rows.Close()

	var opps []domain.Opportunity
	for rows.Next() {
		var opp domain.Opportunity
		var lastSeen, catStr string
		var hasArb int

		if err := rows.Scan(
			&opp.Market.ConditionID,
			&opp.Market.Question,
			&opp.Market.Slug,
			&catStr, // category — solo para ordenar en DB, no necesario en el struct
			&opp.CombinedScore,
			&opp.YourDailyReward,
			&opp.Arbitrage.ArbitrageGap,
			&hasArb,
			&opp.SpreadTotal,
			&opp.Competition,
			&opp.Market.Rewards.DailyRate,
			&opp.Market.Rewards.MaxSpread,
			&lastSeen,
		); err != nil {
			return nil, fmt.Errorf("storage.GetHistory: scan row: %w", err)
		}

		opp.ScannedAt, _ = time.Parse(time.RFC3339, lastSeen)
		opp.Arbitrage.HasArbitrage = hasArb == 1
		opp.QualifiesReward = true
		opps = append(opps, opp)
	}

	return opps, rows.Err()
}

// Close cierra la conexión a la base de datos.
func (s *SQLiteStorage) Close() error {
	return s.db.Close()
}

// --- helpers internos ---

// filterChanged devuelve las oportunidades Gold/Silver que cambiaron respecto al
// estado en caché, y actualiza la caché con el nuevo estado.
func (s *SQLiteStorage) filterChanged(opps []domain.Opportunity, _ time.Time) []domain.Opportunity {
	s.mu.Lock()
	defer s.mu.Unlock()

	var toWrite []domain.Opportunity
	for _, opp := range opps {
		// Solo persistir señal útil
		if opp.Category == domain.CategoryBronze || opp.Category == domain.CategoryAvoid {
			continue
		}

		cid := opp.Market.ConditionID
		cat := opp.Category.String()
		hasArb := opp.Arbitrage.HasArbitrage

		if prev, ok := s.cache[cid]; ok {
			// Saltar si no cambió nada significativo
			unchanged := prev.category == cat &&
				prev.hasArbitrage == hasArb &&
				relChange(prev.combined, opp.CombinedScore) < scoreChangePct
			if unchanged {
				continue
			}
		}

		toWrite = append(toWrite, opp)
		s.cache[cid] = cachedState{
			category:     cat,
			combined:     opp.CombinedScore,
			hasArbitrage: hasArb,
		}
	}
	return toWrite
}

// pruneOld elimina datos antiguos para mantener la DB ligera.
func (s *SQLiteStorage) pruneOld(ctx context.Context) {
	cutoffCycles := time.Now().UTC().Add(-retentionCycles)
	cutoffOpps := time.Now().UTC().Add(-retentionOpps)
	s.db.ExecContext(ctx, `DELETE FROM cycles WHERE scanned_at < ?`, cutoffCycles)
	s.db.ExecContext(ctx, `DELETE FROM opportunities WHERE last_seen < ?`, cutoffOpps)
}

// warmCache precarga la caché desde la DB al arrancar, evitando escrituras
// redundantes en el primer ciclo tras un reinicio.
func (s *SQLiteStorage) warmCache(ctx context.Context) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT condition_id, category, combined_score, has_arbitrage FROM opportunities`,
	)
	if err != nil {
		return
	}
	defer rows.Close()

	s.mu.Lock()
	defer s.mu.Unlock()
	for rows.Next() {
		var cid, cat string
		var combined float64
		var hasArb int
		if rows.Scan(&cid, &cat, &combined, &hasArb) == nil {
			s.cache[cid] = cachedState{
				category:     cat,
				combined:     combined,
				hasArbitrage: hasArb == 1,
			}
		}
	}
}

// cycleSummary extrae conteos Gold/Silver y el mejor score del ciclo.
func cycleSummary(opps []domain.Opportunity) (gold, silver int, best float64) {
	for _, o := range opps {
		switch o.Category {
		case domain.CategoryGold:
			gold++
		case domain.CategorySilver:
			silver++
		}
		if o.CombinedScore > best {
			best = o.CombinedScore
		}
	}
	return
}

// relChange devuelve el cambio relativo entre dos valores (0.0 – ∞).
func relChange(old, new float64) float64 {
	if old == 0 {
		return 1.0 // forzar escritura si antes era 0
	}
	return math.Abs(new-old) / math.Abs(old)
}
