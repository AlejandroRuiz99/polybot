package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/alejandrodnm/polybot/internal/domain"
	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS scans (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    scanned_at      DATETIME NOT NULL,
    condition_id    TEXT NOT NULL,
    question        TEXT,
    slug            TEXT,
    spread_total    REAL NOT NULL,
    reward_score    REAL NOT NULL,
    competition     REAL NOT NULL,
    net_profit_est  REAL NOT NULL,
    qualifies       INTEGER NOT NULL,
    daily_rate      REAL NOT NULL,
    max_spread      REAL NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_scans_scanned_at ON scans(scanned_at);
CREATE INDEX IF NOT EXISTS idx_scans_condition_id ON scans(condition_id);
`

// SQLiteStorage implementa ports.Storage usando SQLite (pure Go, sin CGo).
type SQLiteStorage struct {
	db *sql.DB
}

// NewSQLiteStorage abre (o crea) la base de datos SQLite en la ruta dada.
// Aplica el schema si es la primera vez.
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

	return &SQLiteStorage{db: db}, nil
}

// SaveScan persiste todas las oportunidades de un ciclo en una transacción.
func (s *SQLiteStorage) SaveScan(ctx context.Context, opportunities []domain.Opportunity) error {
	if len(opportunities) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage.SaveScan: begin tx: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO scans
			(scanned_at, condition_id, question, slug, spread_total, reward_score,
			 competition, net_profit_est, qualifies, daily_rate, max_spread)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("storage.SaveScan: prepare: %w", err)
	}
	defer stmt.Close()

	for _, opp := range opportunities {
		qualifies := 0
		if opp.QualifiesReward {
			qualifies = 1
		}
		_, err := stmt.ExecContext(ctx,
			opp.ScannedAt.UTC(),
			opp.Market.ConditionID,
			opp.Market.Question,
			opp.Market.Slug,
			opp.SpreadTotal,
			opp.RewardScore,
			opp.Competition,
			opp.NetProfitEst,
			qualifies,
			opp.Market.Rewards.DailyRate,
			opp.Market.Rewards.MaxSpread,
		)
		if err != nil {
			return fmt.Errorf("storage.SaveScan: insert %s: %w", opp.Market.ConditionID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage.SaveScan: commit: %w", err)
	}
	return nil
}

// GetHistory devuelve las oportunidades guardadas en el rango de tiempo dado.
func (s *SQLiteStorage) GetHistory(ctx context.Context, from, to time.Time) ([]domain.Opportunity, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT scanned_at, condition_id, question, slug,
		       spread_total, reward_score, competition, net_profit_est,
		       qualifies, daily_rate, max_spread
		FROM scans
		WHERE scanned_at BETWEEN ? AND ?
		ORDER BY reward_score DESC
	`, from.UTC(), to.UTC())
	if err != nil {
		return nil, fmt.Errorf("storage.GetHistory: query: %w", err)
	}
	defer rows.Close()

	var opps []domain.Opportunity
	for rows.Next() {
		var opp domain.Opportunity
		var scannedAt string
		var qualifies int

		err := rows.Scan(
			&scannedAt,
			&opp.Market.ConditionID,
			&opp.Market.Question,
			&opp.Market.Slug,
			&opp.SpreadTotal,
			&opp.RewardScore,
			&opp.Competition,
			&opp.NetProfitEst,
			&qualifies,
			&opp.Market.Rewards.DailyRate,
			&opp.Market.Rewards.MaxSpread,
		)
		if err != nil {
			return nil, fmt.Errorf("storage.GetHistory: scan row: %w", err)
		}

		opp.ScannedAt, _ = time.Parse(time.RFC3339, scannedAt)
		opp.QualifiesReward = qualifies == 1
		opps = append(opps, opp)
	}

	return opps, rows.Err()
}

// Close cierra la conexión a la base de datos.
func (s *SQLiteStorage) Close() error {
	return s.db.Close()
}
