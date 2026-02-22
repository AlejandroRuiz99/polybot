package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/alejandrodnm/polybot/internal/domain"
)

const paperSchema = `
CREATE TABLE IF NOT EXISTS paper_orders (
    id            TEXT PRIMARY KEY,
    condition_id  TEXT NOT NULL,
    token_id      TEXT NOT NULL,
    side          TEXT NOT NULL,
    bid_price     REAL NOT NULL,
    size          REAL NOT NULL,
    pair_id       TEXT NOT NULL,
    placed_at     DATETIME NOT NULL,
    status        TEXT NOT NULL DEFAULT 'OPEN',
    filled_at     DATETIME,
    filled_price  REAL NOT NULL DEFAULT 0,
    question      TEXT,
    queue_ahead   REAL NOT NULL DEFAULT 0,
    daily_reward  REAL NOT NULL DEFAULT 0,
    end_date      DATETIME,
    merged_at     DATETIME
);

CREATE TABLE IF NOT EXISTS paper_fills (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    order_id    TEXT NOT NULL,
    trade_id    TEXT,
    price       REAL NOT NULL,
    size        REAL NOT NULL,
    timestamp   DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS paper_daily (
    date              DATE PRIMARY KEY,
    active_positions  INTEGER NOT NULL DEFAULT 0,
    complete_pairs    INTEGER NOT NULL DEFAULT 0,
    partial_fills     INTEGER NOT NULL DEFAULT 0,
    total_reward      REAL NOT NULL DEFAULT 0,
    total_fill_pnl    REAL NOT NULL DEFAULT 0,
    net_pnl           REAL NOT NULL DEFAULT 0,
    avg_partial_mins  REAL NOT NULL DEFAULT 0,
    fills_yes         INTEGER NOT NULL DEFAULT 0,
    fills_no          INTEGER NOT NULL DEFAULT 0,
    orders_placed     INTEGER NOT NULL DEFAULT 0,
    capital_deployed  REAL NOT NULL DEFAULT 0,
    markets_resolved  INTEGER NOT NULL DEFAULT 0,
    resolution_pnl    REAL NOT NULL DEFAULT 0,
    rotations         INTEGER NOT NULL DEFAULT 0,
    merge_profit      REAL NOT NULL DEFAULT 0,
    compound_balance  REAL NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_paper_orders_pair   ON paper_orders(pair_id);
CREATE INDEX IF NOT EXISTS idx_paper_orders_status ON paper_orders(status);
CREATE INDEX IF NOT EXISTS idx_paper_orders_cond   ON paper_orders(condition_id);
CREATE INDEX IF NOT EXISTS idx_paper_fills_order   ON paper_fills(order_id);
`

// migrate adds columns that may not exist in older schemas.
const paperMigrations = `
ALTER TABLE paper_orders ADD COLUMN daily_reward REAL NOT NULL DEFAULT 0;
ALTER TABLE paper_orders ADD COLUMN end_date DATETIME;
ALTER TABLE paper_daily ADD COLUMN capital_deployed REAL NOT NULL DEFAULT 0;
ALTER TABLE paper_daily ADD COLUMN markets_resolved INTEGER NOT NULL DEFAULT 0;
ALTER TABLE paper_daily ADD COLUMN resolution_pnl REAL NOT NULL DEFAULT 0;
`

// ApplyPaperSchema creates paper trading tables if they don't exist.
func (s *SQLiteStorage) ApplyPaperSchema(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, paperSchema); err != nil {
		return fmt.Errorf("storage.ApplyPaperSchema: %w", err)
	}
	// Run migrations silently — they fail if columns already exist, which is fine
	for _, stmt := range []string{
		"ALTER TABLE paper_orders ADD COLUMN daily_reward REAL NOT NULL DEFAULT 0",
		"ALTER TABLE paper_orders ADD COLUMN end_date DATETIME",
		"ALTER TABLE paper_orders ADD COLUMN merged_at DATETIME",
		"ALTER TABLE paper_daily ADD COLUMN capital_deployed REAL NOT NULL DEFAULT 0",
		"ALTER TABLE paper_daily ADD COLUMN markets_resolved INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE paper_daily ADD COLUMN resolution_pnl REAL NOT NULL DEFAULT 0",
		"ALTER TABLE paper_daily ADD COLUMN rotations INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE paper_daily ADD COLUMN merge_profit REAL NOT NULL DEFAULT 0",
		"ALTER TABLE paper_daily ADD COLUMN compound_balance REAL NOT NULL DEFAULT 0",
	} {
		s.db.ExecContext(ctx, stmt) // ignore errors (column already exists)
	}
	return nil
}

// SavePaperOrder inserts a new virtual order.
func (s *SQLiteStorage) SavePaperOrder(ctx context.Context, order domain.VirtualOrder) error {
	var endDate *string
	if !order.EndDate.IsZero() {
		t := order.EndDate.UTC().Format(time.RFC3339)
		endDate = &t
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO paper_orders (id, condition_id, token_id, side, bid_price, size,
		                          pair_id, placed_at, status, filled_at, filled_price,
		                          question, queue_ahead, daily_reward, end_date, merged_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		order.ID, order.ConditionID, order.TokenID, order.Side, order.BidPrice,
		order.Size, order.PairID, order.PlacedAt.UTC().Format(time.RFC3339),
		string(order.Status), nil, order.FilledPrice, order.Question,
		order.QueueAhead, order.DailyReward, endDate, nil,
	)
	if err != nil {
		return fmt.Errorf("storage.SavePaperOrder: %w", err)
	}
	return nil
}

// MarkPaperOrderFilled updates order status to FILLED.
func (s *SQLiteStorage) MarkPaperOrderFilled(ctx context.Context, orderID string, filledAt time.Time, filledPrice float64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE paper_orders SET status = 'FILLED', filled_at = ?, filled_price = ?
		WHERE id = ?`,
		filledAt.UTC().Format(time.RFC3339), filledPrice, orderID,
	)
	if err != nil {
		return fmt.Errorf("storage.MarkPaperOrderFilled: %w", err)
	}
	return nil
}

// MarkPaperOrderResolved marks an order as resolved (market ended).
func (s *SQLiteStorage) MarkPaperOrderResolved(ctx context.Context, orderID string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE paper_orders SET status = 'RESOLVED' WHERE id = ?`, orderID)
	if err != nil {
		return fmt.Errorf("storage.MarkPaperOrderResolved: %w", err)
	}
	return nil
}

// MarkPaperOrderMerged marks an order as merged (compound rotation complete).
func (s *SQLiteStorage) MarkPaperOrderMerged(ctx context.Context, orderID string, mergedAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE paper_orders SET status = 'MERGED', merged_at = ? WHERE id = ?`,
		mergedAt.UTC().Format(time.RFC3339), orderID)
	if err != nil {
		return fmt.Errorf("storage.MarkPaperOrderMerged: %w", err)
	}
	return nil
}

// UpdatePaperOrderQueue updates the queue position for an open order.
func (s *SQLiteStorage) UpdatePaperOrderQueue(ctx context.Context, orderID string, queueAhead float64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE paper_orders SET queue_ahead = ? WHERE id = ? AND status = 'OPEN'`,
		queueAhead, orderID)
	if err != nil {
		return fmt.Errorf("storage.UpdatePaperOrderQueue: %w", err)
	}
	return nil
}

// ExpirePaperOrders marks all OPEN orders for a condition as EXPIRED.
func (s *SQLiteStorage) ExpirePaperOrders(ctx context.Context, conditionID string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE paper_orders SET status = 'EXPIRED'
		WHERE condition_id = ? AND status = 'OPEN'`,
		conditionID,
	)
	if err != nil {
		return fmt.Errorf("storage.ExpirePaperOrders: %w", err)
	}
	return nil
}

// SavePaperFill records a fill event.
func (s *SQLiteStorage) SavePaperFill(ctx context.Context, fill domain.PaperFill) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO paper_fills (order_id, trade_id, price, size, timestamp)
		VALUES (?, ?, ?, ?, ?)`,
		fill.OrderID, fill.TradeID, fill.Price, fill.Size, fill.Timestamp.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("storage.SavePaperFill: %w", err)
	}
	return nil
}

// GetOpenPaperOrders returns all OPEN virtual orders.
func (s *SQLiteStorage) GetOpenPaperOrders(ctx context.Context) ([]domain.VirtualOrder, error) {
	return s.queryPaperOrders(ctx, `
		SELECT id, condition_id, token_id, side, bid_price, size,
		       pair_id, placed_at, status, filled_at, filled_price, question,
		       queue_ahead, daily_reward, end_date, merged_at
		FROM paper_orders WHERE status = 'OPEN'
		ORDER BY placed_at DESC`)
}

// GetPaperOrdersByPair returns both orders (YES + NO) for a given pair.
func (s *SQLiteStorage) GetPaperOrdersByPair(ctx context.Context, pairID string) ([]domain.VirtualOrder, error) {
	return s.queryPaperOrders(ctx, `
		SELECT id, condition_id, token_id, side, bid_price, size,
		       pair_id, placed_at, status, filled_at, filled_price, question,
		       queue_ahead, daily_reward, end_date, merged_at
		FROM paper_orders WHERE pair_id = ?
		ORDER BY side`, pairID)
}

// GetActivePaperConditions returns distinct condition_ids with at least one OPEN order.
func (s *SQLiteStorage) GetActivePaperConditions(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT condition_id FROM paper_orders WHERE status = 'OPEN'`)
	if err != nil {
		return nil, fmt.Errorf("storage.GetActivePaperConditions: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// GetAllPaperOrders returns all paper orders, optionally filtered by status.
func (s *SQLiteStorage) GetAllPaperOrders(ctx context.Context, status string) ([]domain.VirtualOrder, error) {
	if status != "" {
		return s.queryPaperOrders(ctx, `
			SELECT id, condition_id, token_id, side, bid_price, size,
			       pair_id, placed_at, status, filled_at, filled_price, question,
			       queue_ahead, daily_reward, end_date, merged_at
			FROM paper_orders WHERE status = ?
			ORDER BY placed_at DESC`, status)
	}
	return s.queryPaperOrders(ctx, `
		SELECT id, condition_id, token_id, side, bid_price, size,
		       pair_id, placed_at, status, filled_at, filled_price, question,
		       queue_ahead, daily_reward, end_date, merged_at
		FROM paper_orders ORDER BY placed_at DESC`)
}

// SavePaperDaily upserts the daily summary.
func (s *SQLiteStorage) SavePaperDaily(ctx context.Context, d domain.PaperDailySummary) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO paper_daily (date, active_positions, complete_pairs, partial_fills,
		                         total_reward, total_fill_pnl, net_pnl, avg_partial_mins,
		                         fills_yes, fills_no, orders_placed, capital_deployed,
		                         markets_resolved, resolution_pnl,
		                         rotations, merge_profit, compound_balance)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(date) DO UPDATE SET
		    active_positions = excluded.active_positions,
		    complete_pairs   = excluded.complete_pairs,
		    partial_fills    = excluded.partial_fills,
		    total_reward     = excluded.total_reward,
		    total_fill_pnl   = excluded.total_fill_pnl,
		    net_pnl          = excluded.net_pnl,
		    avg_partial_mins = excluded.avg_partial_mins,
		    fills_yes        = excluded.fills_yes,
		    fills_no         = excluded.fills_no,
		    orders_placed    = excluded.orders_placed,
		    capital_deployed = excluded.capital_deployed,
		    markets_resolved = excluded.markets_resolved,
		    resolution_pnl   = excluded.resolution_pnl,
		    rotations        = excluded.rotations,
		    merge_profit     = excluded.merge_profit,
		    compound_balance = excluded.compound_balance`,
		d.Date.UTC().Format("2006-01-02"), d.ActivePositions, d.CompletePairs,
		d.PartialFills, d.TotalReward, d.TotalFillPnL, d.NetPnL,
		d.AvgPartialMins, d.FillsYes, d.FillsNo, d.OrdersPlaced,
		d.CapitalDeployed, d.MarketsResolved, d.ResolutionPnL,
		d.Rotations, d.MergeProfit, d.CompoundBalance,
	)
	if err != nil {
		return fmt.Errorf("storage.SavePaperDaily: %w", err)
	}
	return nil
}

// GetPaperDailies returns daily summaries in chronological order.
func (s *SQLiteStorage) GetPaperDailies(ctx context.Context) ([]domain.PaperDailySummary, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT date, active_positions, complete_pairs, partial_fills,
		       total_reward, total_fill_pnl, net_pnl, avg_partial_mins,
		       fills_yes, fills_no, orders_placed, capital_deployed,
		       markets_resolved, resolution_pnl,
		       rotations, merge_profit, compound_balance
		FROM paper_daily ORDER BY date ASC`)
	if err != nil {
		return nil, fmt.Errorf("storage.GetPaperDailies: %w", err)
	}
	defer rows.Close()

	var out []domain.PaperDailySummary
	for rows.Next() {
		var d domain.PaperDailySummary
		var dateStr string
		if err := rows.Scan(
			&dateStr, &d.ActivePositions, &d.CompletePairs, &d.PartialFills,
			&d.TotalReward, &d.TotalFillPnL, &d.NetPnL, &d.AvgPartialMins,
			&d.FillsYes, &d.FillsNo, &d.OrdersPlaced, &d.CapitalDeployed,
			&d.MarketsResolved, &d.ResolutionPnL,
			&d.Rotations, &d.MergeProfit, &d.CompoundBalance,
		); err != nil {
			return nil, fmt.Errorf("storage.GetPaperDailies: scan: %w", err)
		}
		if len(dateStr) > 10 {
			dateStr = dateStr[:10]
		}
		d.Date, _ = time.Parse("2006-01-02", dateStr)
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetPaperStats computes aggregate stats from paper_orders and paper_daily.
func (s *SQLiteStorage) GetPaperStats(ctx context.Context) (domain.PaperStats, error) {
	dailies, err := s.GetPaperDailies(ctx)
	if err != nil {
		return domain.PaperStats{}, err
	}

	var stats domain.PaperStats
	stats.Dailies = dailies
	stats.DaysRunning = len(dailies)

	if len(dailies) > 0 {
		stats.StartDate = dailies[0].Date
		stats.EndDate = dailies[len(dailies)-1].Date
	}

	for _, d := range dailies {
		stats.CompletePairs += d.CompletePairs
		stats.PartialFills += d.PartialFills
		stats.TotalReward += d.TotalReward
		stats.TotalFillPnL += d.TotalFillPnL
		stats.NetPnL += d.NetPnL
		stats.TotalFills += d.FillsYes + d.FillsNo
		stats.TotalOrders += d.OrdersPlaced
		stats.MarketsResolved += d.MarketsResolved
		stats.ResolutionPnL += d.ResolutionPnL
		stats.TotalRotations += d.Rotations
		stats.TotalMergeProfit += d.MergeProfit
		if d.CapitalDeployed > stats.MaxCapital {
			stats.MaxCapital = d.CapitalDeployed
		}
	}

	if len(dailies) > 0 {
		stats.CompoundBalance = dailies[len(dailies)-1].CompoundBalance
	}

	if stats.DaysRunning > 0 {
		stats.DailyAvgPnL = stats.NetPnL / float64(stats.DaysRunning)
		stats.FillRateReal = float64(stats.TotalFills) / float64(stats.DaysRunning)
	}

	// Compute average cycle time from merged orders
	var avgCycle sql.NullFloat64
	_ = s.db.QueryRowContext(ctx, `
		SELECT AVG((julianday(merged_at) - julianday(placed_at)) * 24)
		FROM paper_orders WHERE status = 'MERGED' AND merged_at IS NOT NULL`).Scan(&avgCycle)
	if avgCycle.Valid && avgCycle.Float64 > 0 {
		stats.AvgCycleHours = avgCycle.Float64
	}

	var maxPartial sql.NullFloat64
	_ = s.db.QueryRowContext(ctx, `
		SELECT MAX(avg_partial_mins) FROM paper_daily`).Scan(&maxPartial)
	if maxPartial.Valid {
		stats.MaxPartialMins = maxPartial.Float64
	}

	var markets int
	_ = s.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT condition_id) FROM paper_orders`).Scan(&markets)
	stats.MarketsMonitored = markets

	return stats, nil
}

// queryPaperOrders is a helper to scan rows into VirtualOrder slices.
func (s *SQLiteStorage) queryPaperOrders(ctx context.Context, query string, args ...any) ([]domain.VirtualOrder, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("storage.queryPaperOrders: %w", err)
	}
	defer rows.Close()

	var out []domain.VirtualOrder
	for rows.Next() {
		var o domain.VirtualOrder
		var status, placedAt string
		var filledAt, question, endDate, mergedAt sql.NullString

		if err := rows.Scan(
			&o.ID, &o.ConditionID, &o.TokenID, &o.Side, &o.BidPrice, &o.Size,
			&o.PairID, &placedAt, &status, &filledAt, &o.FilledPrice,
			&question, &o.QueueAhead, &o.DailyReward, &endDate, &mergedAt,
		); err != nil {
			return nil, fmt.Errorf("storage.queryPaperOrders: scan: %w", err)
		}

		o.Status = domain.PaperOrderStatus(status)
		o.PlacedAt, _ = time.Parse(time.RFC3339, placedAt)
		if question.Valid {
			o.Question = question.String
		}
		if filledAt.Valid {
			t, _ := time.Parse(time.RFC3339, filledAt.String)
			o.FilledAt = &t
		}
		if endDate.Valid {
			o.EndDate, _ = time.Parse(time.RFC3339, endDate.String)
		}
		if mergedAt.Valid {
			t, _ := time.Parse(time.RFC3339, mergedAt.String)
			o.MergedAt = &t
		}

		out = append(out, o)
	}
	return out, rows.Err()
}
