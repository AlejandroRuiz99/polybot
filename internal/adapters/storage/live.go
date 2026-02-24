package storage

// live.go — SQLite persistence for real money trading.
//
// Tables:
//   live_orders         — real CLOB orders (local + CLOB IDs)
//   live_fills          — detected fill events
//   live_merges         — completed on-chain merge transactions
//   live_daily          — daily P&L summary
//   live_circuit_breaker— circuit breaker state

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/alejandrodnm/polybot/internal/domain"
)

const liveSchema = `
CREATE TABLE IF NOT EXISTS live_orders (
    id              TEXT PRIMARY KEY,   -- local UUID
    clob_order_id   TEXT NOT NULL DEFAULT '',
    condition_id    TEXT NOT NULL,
    token_id        TEXT NOT NULL,
    side            TEXT NOT NULL,      -- YES / NO
    bid_price       REAL NOT NULL,
    size            REAL NOT NULL,
    filled_size     REAL NOT NULL DEFAULT 0,
    pair_id         TEXT NOT NULL,
    placed_at       DATETIME NOT NULL,
    status          TEXT NOT NULL DEFAULT 'OPEN',
    filled_at       DATETIME,
    filled_price    REAL NOT NULL DEFAULT 0,
    question        TEXT,
    queue_ahead     REAL NOT NULL DEFAULT 0,
    daily_reward    REAL NOT NULL DEFAULT 0,
    end_date        DATETIME,
    merged_at       DATETIME,
    neg_risk        INTEGER NOT NULL DEFAULT 0,
    competition_at  REAL NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS live_orders_status ON live_orders(status);
CREATE INDEX IF NOT EXISTS live_orders_condition ON live_orders(condition_id);
CREATE INDEX IF NOT EXISTS live_orders_pair ON live_orders(pair_id);

CREATE TABLE IF NOT EXISTS live_fills (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    order_id        TEXT NOT NULL,
    clob_trade_id   TEXT,
    price           REAL NOT NULL,
    size            REAL NOT NULL,
    timestamp       DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS live_merges (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    condition_id    TEXT NOT NULL,
    pair_id         TEXT NOT NULL DEFAULT '',
    tx_hash         TEXT NOT NULL,
    gas_used_pol    REAL NOT NULL DEFAULT 0,
    gas_cost_usd    REAL NOT NULL DEFAULT 0,
    usdc_received   REAL NOT NULL DEFAULT 0,
    spread_profit   REAL NOT NULL DEFAULT 0,
    success         INTEGER NOT NULL DEFAULT 0,
    error           TEXT,
    executed_at     DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS live_daily (
    date                DATE PRIMARY KEY,
    active_positions    INTEGER NOT NULL DEFAULT 0,
    complete_pairs      INTEGER NOT NULL DEFAULT 0,
    partial_fills       INTEGER NOT NULL DEFAULT 0,
    total_reward        REAL NOT NULL DEFAULT 0,
    total_fill_pnl      REAL NOT NULL DEFAULT 0,
    net_pnl             REAL NOT NULL DEFAULT 0,
    avg_partial_mins    REAL NOT NULL DEFAULT 0,
    fills_yes           INTEGER NOT NULL DEFAULT 0,
    fills_no            INTEGER NOT NULL DEFAULT 0,
    orders_placed       INTEGER NOT NULL DEFAULT 0,
    orders_cancelled    INTEGER NOT NULL DEFAULT 0,
    capital_deployed    REAL NOT NULL DEFAULT 0,
    merges              INTEGER NOT NULL DEFAULT 0,
    merge_profit        REAL NOT NULL DEFAULT 0,
    gas_cost_usd        REAL NOT NULL DEFAULT 0,
    compound_balance    REAL NOT NULL DEFAULT 0,
    rotations           INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS live_circuit_breaker (
    id                  INTEGER PRIMARY KEY DEFAULT 1,
    consecutive_losses  INTEGER NOT NULL DEFAULT 0,
    max_losses          INTEGER NOT NULL DEFAULT 3,
    cooldown_until      DATETIME,
    cooldown_duration_s INTEGER NOT NULL DEFAULT 1800,
    total_pnl           REAL NOT NULL DEFAULT 0,
    max_drawdown        REAL NOT NULL DEFAULT -50,
    triggered           INTEGER NOT NULL DEFAULT 0,
    triggered_reason    TEXT
);

-- Ensure exactly one row in circuit_breaker
INSERT OR IGNORE INTO live_circuit_breaker (id) VALUES (1);
`

// ApplyLiveSchema creates the live trading tables if they don't exist.
func (s *SQLiteStorage) ApplyLiveSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, liveSchema)
	if err != nil {
		return fmt.Errorf("live schema: %w", err)
	}
	return nil
}

// ─── Orders ──────────────────────────────────────────────────────────────────

// SaveLiveOrder inserts a new live order.
func (s *SQLiteStorage) SaveLiveOrder(ctx context.Context, o domain.LiveOrder) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO live_orders
		  (id, clob_order_id, condition_id, token_id, side, bid_price, size, filled_size,
		   pair_id, placed_at, status, filled_at, filled_price, question,
		   queue_ahead, daily_reward, end_date, merged_at, neg_risk, competition_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		o.ID, o.CLOBOrderID, o.ConditionID, o.TokenID, o.Side, o.BidPrice, o.Size, o.FilledSize,
		o.PairID, o.PlacedAt.UTC(), string(o.Status), nullTime(o.FilledAt), o.FilledPrice, o.Question,
		o.QueueAhead, o.DailyReward, nullTimeVal(o.EndDate), nullTime(o.MergedAt),
		boolToInt(o.NegRisk), o.CompetitionAt,
	)
	return err
}

// UpdateLiveOrderStatus updates only the status field.
func (s *SQLiteStorage) UpdateLiveOrderStatus(ctx context.Context, localID string, status domain.LiveOrderStatus) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE live_orders SET status=? WHERE id=?`, string(status), localID)
	return err
}

// UpdateLiveOrderFill updates fill data for a live order.
func (s *SQLiteStorage) UpdateLiveOrderFill(ctx context.Context, localID string, filledSize, filledPrice float64, status domain.LiveOrderStatus, filledAt *time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE live_orders SET filled_size=?, filled_price=?, status=?, filled_at=? WHERE id=?`,
		filledSize, filledPrice, string(status), nullTime(filledAt), localID)
	return err
}

// UpdateLiveOrderQueue updates the queue_ahead estimate.
func (s *SQLiteStorage) UpdateLiveOrderQueue(ctx context.Context, localID string, queueAhead float64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE live_orders SET queue_ahead=? WHERE id=?`, queueAhead, localID)
	return err
}

// MarkLiveOrderMerged marks an order as merged.
func (s *SQLiteStorage) MarkLiveOrderMerged(ctx context.Context, localID string, mergedAt time.Time) error {
	t := mergedAt.UTC()
	_, err := s.db.ExecContext(ctx,
		`UPDATE live_orders SET status='MERGED', merged_at=? WHERE id=?`, t, localID)
	return err
}

// GetOpenLiveOrders returns all OPEN and PARTIAL live orders.
func (s *SQLiteStorage) GetOpenLiveOrders(ctx context.Context) ([]domain.LiveOrder, error) {
	return s.queryLiveOrders(ctx, `WHERE status IN ('OPEN','PARTIAL')`)
}

// GetLiveOrdersByPair returns all orders for a given pair ID.
func (s *SQLiteStorage) GetLiveOrdersByPair(ctx context.Context, pairID string) ([]domain.LiveOrder, error) {
	return s.queryLiveOrders(ctx, `WHERE pair_id=?`, pairID)
}

// GetActiveLiveConditions returns distinct condition IDs with open/partial/filled orders.
func (s *SQLiteStorage) GetActiveLiveConditions(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT condition_id FROM live_orders WHERE status IN ('OPEN','PARTIAL','FILLED')`)
	if err != nil {
		return nil, err
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

// GetAllLiveOrders returns all orders with a specific status.
func (s *SQLiteStorage) GetAllLiveOrders(ctx context.Context, status string) ([]domain.LiveOrder, error) {
	return s.queryLiveOrders(ctx, `WHERE status=?`, status)
}

// CancelLiveOrdersByCondition marks all open orders for a condition as cancelled.
func (s *SQLiteStorage) CancelLiveOrdersByCondition(ctx context.Context, conditionID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE live_orders SET status='CANCELLED' WHERE condition_id=? AND status IN ('OPEN','PARTIAL')`,
		conditionID)
	return err
}

func (s *SQLiteStorage) queryLiveOrders(ctx context.Context, where string, args ...any) ([]domain.LiveOrder, error) {
	q := `SELECT id, clob_order_id, condition_id, token_id, side, bid_price, size, filled_size,
		         pair_id, placed_at, status, filled_at, filled_price, question,
		         queue_ahead, daily_reward, end_date, merged_at, neg_risk, competition_at
		  FROM live_orders ` + where + ` ORDER BY placed_at ASC`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var orders []domain.LiveOrder
	for rows.Next() {
		o, err := scanLiveOrder(rows)
		if err != nil {
			return nil, err
		}
		orders = append(orders, o)
	}
	return orders, rows.Err()
}

func scanLiveOrder(rows *sql.Rows) (domain.LiveOrder, error) {
	var o domain.LiveOrder
	var filledAt, endDate, mergedAt sql.NullString
	var statusStr string
	var negRiskInt int

	err := rows.Scan(
		&o.ID, &o.CLOBOrderID, &o.ConditionID, &o.TokenID, &o.Side,
		&o.BidPrice, &o.Size, &o.FilledSize,
		&o.PairID, &o.PlacedAt, &statusStr, &filledAt, &o.FilledPrice, &o.Question,
		&o.QueueAhead, &o.DailyReward, &endDate, &mergedAt, &negRiskInt, &o.CompetitionAt,
	)
	if err != nil {
		return o, err
	}

	o.Status = domain.LiveOrderStatus(statusStr)
	o.NegRisk = negRiskInt != 0

	if filledAt.Valid && filledAt.String != "" {
		t, _ := time.Parse(time.RFC3339, filledAt.String)
		if t.IsZero() {
			t, _ = time.Parse("2006-01-02 15:04:05", filledAt.String)
		}
		if !t.IsZero() {
			o.FilledAt = &t
		}
	}
	if endDate.Valid && endDate.String != "" {
		t, _ := time.Parse(time.RFC3339, endDate.String)
		if t.IsZero() {
			t, _ = time.Parse("2006-01-02 15:04:05", endDate.String)
		}
		o.EndDate = t
	}
	if mergedAt.Valid && mergedAt.String != "" {
		t, _ := time.Parse(time.RFC3339, mergedAt.String)
		if t.IsZero() {
			t, _ = time.Parse("2006-01-02 15:04:05", mergedAt.String)
		}
		if !t.IsZero() {
			o.MergedAt = &t
		}
	}
	return o, nil
}

// ─── Fills ───────────────────────────────────────────────────────────────────

// SaveLiveFill records a fill event.
func (s *SQLiteStorage) SaveLiveFill(ctx context.Context, f domain.LiveFill) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO live_fills (order_id, clob_trade_id, price, size, timestamp) VALUES (?,?,?,?,?)`,
		f.OrderID, f.CLOBTradeID, f.Price, f.Size, f.Timestamp.UTC())
	return err
}

// ─── Merges ──────────────────────────────────────────────────────────────────

// SaveMergeResult persists the result of an on-chain merge.
func (s *SQLiteStorage) SaveMergeResult(ctx context.Context, r domain.MergeResult) error {
	successInt := 0
	if r.Success {
		successInt = 1
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO live_merges
		  (condition_id, pair_id, tx_hash, gas_used_pol, gas_cost_usd, usdc_received, spread_profit, success, error, executed_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		r.ConditionID, r.PairID, r.TxHash, r.GasUsedPOL, r.GasCostUSD,
		r.USDCReceived, r.SpreadProfit, successInt, r.Error, r.ExecutedAt.UTC(),
	)
	return err
}

// GetMergeResults returns all recorded merge results.
func (s *SQLiteStorage) GetMergeResults(ctx context.Context) ([]domain.MergeResult, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT condition_id, pair_id, tx_hash, gas_used_pol, gas_cost_usd, usdc_received, spread_profit, success, error, executed_at
		 FROM live_merges ORDER BY executed_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []domain.MergeResult
	for rows.Next() {
		var r domain.MergeResult
		var successInt int
		var errStr sql.NullString
		if err := rows.Scan(&r.ConditionID, &r.PairID, &r.TxHash, &r.GasUsedPOL, &r.GasCostUSD,
			&r.USDCReceived, &r.SpreadProfit, &successInt, &errStr, &r.ExecutedAt); err != nil {
			return nil, err
		}
		r.Success = successInt != 0
		r.Error = errStr.String
		results = append(results, r)
	}
	return results, rows.Err()
}

// ─── Circuit Breaker ─────────────────────────────────────────────────────────

// SaveCircuitBreaker persists the current circuit breaker state.
func (s *SQLiteStorage) SaveCircuitBreaker(ctx context.Context, cb domain.CircuitBreaker) error {
	var cooldownUntil *time.Time
	if !cb.CooldownUntil.IsZero() {
		t := cb.CooldownUntil.UTC()
		cooldownUntil = &t
	}
	triggeredInt := 0
	if cb.Triggered {
		triggeredInt = 1
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE live_circuit_breaker SET
		  consecutive_losses=?, max_losses=?, cooldown_until=?,
		  cooldown_duration_s=?, total_pnl=?, max_drawdown=?,
		  triggered=?, triggered_reason=?
		WHERE id=1`,
		cb.ConsecutiveLosses, cb.MaxLosses, nullTime(cooldownUntil),
		int(cb.CooldownDuration.Seconds()), cb.TotalPnL, cb.MaxDrawdown,
		triggeredInt, cb.TriggeredReason,
	)
	return err
}

// LoadCircuitBreaker loads the persisted circuit breaker state.
func (s *SQLiteStorage) LoadCircuitBreaker(ctx context.Context) (domain.CircuitBreaker, error) {
	var cb domain.CircuitBreaker
	var triggeredInt int
	var cooldownUntilStr sql.NullString
	var cooldownDurationS int

	err := s.db.QueryRowContext(ctx, `
		SELECT consecutive_losses, max_losses, cooldown_until, cooldown_duration_s,
		       total_pnl, max_drawdown, triggered, triggered_reason
		FROM live_circuit_breaker WHERE id=1`).Scan(
		&cb.ConsecutiveLosses, &cb.MaxLosses, &cooldownUntilStr, &cooldownDurationS,
		&cb.TotalPnL, &cb.MaxDrawdown, &triggeredInt, &cb.TriggeredReason,
	)
	if err != nil {
		return cb, err
	}

	cb.Triggered = triggeredInt != 0
	cb.CooldownDuration = time.Duration(cooldownDurationS) * time.Second

	if cooldownUntilStr.Valid && cooldownUntilStr.String != "" {
		t, _ := time.Parse(time.RFC3339, cooldownUntilStr.String)
		if t.IsZero() {
			t, _ = time.Parse("2006-01-02 15:04:05", cooldownUntilStr.String)
		}
		cb.CooldownUntil = t
	}

	return cb, nil
}

// ─── Daily Summary ───────────────────────────────────────────────────────────

// SaveLiveDaily upserts a daily summary.
func (s *SQLiteStorage) SaveLiveDaily(ctx context.Context, d domain.LiveDailySummary) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO live_daily
		  (date, active_positions, complete_pairs, partial_fills, total_reward, total_fill_pnl,
		   net_pnl, avg_partial_mins, fills_yes, fills_no, orders_placed, orders_cancelled,
		   capital_deployed, merges, merge_profit, gas_cost_usd, compound_balance, rotations)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(date) DO UPDATE SET
		  active_positions=excluded.active_positions,
		  complete_pairs=excluded.complete_pairs,
		  partial_fills=excluded.partial_fills,
		  total_reward=excluded.total_reward,
		  total_fill_pnl=excluded.total_fill_pnl,
		  net_pnl=excluded.net_pnl,
		  avg_partial_mins=excluded.avg_partial_mins,
		  fills_yes=excluded.fills_yes,
		  fills_no=excluded.fills_no,
		  orders_placed=excluded.orders_placed,
		  orders_cancelled=excluded.orders_cancelled,
		  capital_deployed=excluded.capital_deployed,
		  merges=excluded.merges,
		  merge_profit=excluded.merge_profit,
		  gas_cost_usd=excluded.gas_cost_usd,
		  compound_balance=excluded.compound_balance,
		  rotations=excluded.rotations`,
		d.Date.Format("2006-01-02"),
		d.ActivePositions, d.CompletePairs, d.PartialFills, d.TotalReward,
		d.TotalFillPnL, d.NetPnL, d.AvgPartialMins, d.FillsYes, d.FillsNo,
		d.OrdersPlaced, d.OrdersCancelled, d.CapitalDeployed, d.Merges,
		d.MergeProfit, d.GasCostUSD, d.CompoundBalance, d.Rotations,
	)
	return err
}

// GetLiveDailies returns all daily summaries ordered by date.
func (s *SQLiteStorage) GetLiveDailies(ctx context.Context) ([]domain.LiveDailySummary, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT date, active_positions, complete_pairs, partial_fills, total_reward, total_fill_pnl,
		       net_pnl, avg_partial_mins, fills_yes, fills_no, orders_placed, orders_cancelled,
		       capital_deployed, merges, merge_profit, gas_cost_usd, compound_balance, rotations
		FROM live_daily ORDER BY date ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var dailies []domain.LiveDailySummary
	for rows.Next() {
		var d domain.LiveDailySummary
		var dateStr string
		if err := rows.Scan(&dateStr, &d.ActivePositions, &d.CompletePairs, &d.PartialFills,
			&d.TotalReward, &d.TotalFillPnL, &d.NetPnL, &d.AvgPartialMins, &d.FillsYes, &d.FillsNo,
			&d.OrdersPlaced, &d.OrdersCancelled, &d.CapitalDeployed, &d.Merges,
			&d.MergeProfit, &d.GasCostUSD, &d.CompoundBalance, &d.Rotations); err != nil {
			return nil, err
		}
		d.Date, _ = time.Parse("2006-01-02", dateStr)
		dailies = append(dailies, d)
	}
	return dailies, rows.Err()
}

// GetLiveStats aggregates statistics across all live trading history.
func (s *SQLiteStorage) GetLiveStats(ctx context.Context) (domain.LiveStats, error) {
	var stats domain.LiveStats

	dailies, err := s.GetLiveDailies(ctx)
	if err != nil {
		return stats, err
	}
	stats.Dailies = dailies

	if len(dailies) > 0 {
		stats.StartDate = dailies[0].Date
		stats.EndDate = dailies[len(dailies)-1].Date
		stats.DaysRunning = len(dailies)

		for _, d := range dailies {
			stats.TotalReward += d.TotalReward
			stats.TotalMergeProfit += d.MergeProfit
			stats.TotalGasCostUSD += d.GasCostUSD
			stats.NetPnL += d.NetPnL
			stats.TotalRotations += d.Rotations
		}
		stats.CompoundBalance = dailies[len(dailies)-1].CompoundBalance

		if stats.DaysRunning > 0 {
			stats.DailyAvgPnL = stats.NetPnL / float64(stats.DaysRunning)
		}
	}

	// Order stats
	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM live_orders`).Scan(&stats.TotalOrders)
	if err != nil {
		return stats, err
	}

	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM live_orders WHERE status='FILLED'`).Scan(&stats.TotalFills)
	if err != nil {
		return stats, err
	}

	// Merge stats
	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM live_merges WHERE success=1`).Scan(&stats.CompletePairs)
	if err != nil {
		return stats, err
	}

	// Fill rate
	if stats.TotalOrders > 0 {
		stats.FillRateReal = float64(stats.TotalFills) / float64(stats.TotalOrders)
	}

	return stats, nil
}

// GetPartialPairs devuelve los pairIDs donde solo uno de los dos lados (YES/NO) está filled.
func (s *SQLiteStorage) GetPartialPairs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT pair_id, side FROM live_orders WHERE status IN ('FILLED','PARTIAL')`)
	if err != nil {
		return nil, fmt.Errorf("storage.GetPartialPairs: query: %w", err)
	}
	defer rows.Close()

	type sides struct{ yes, no bool }
	pairSides := make(map[string]*sides)
	for rows.Next() {
		var pairID, side string
		if err := rows.Scan(&pairID, &side); err != nil {
			return nil, fmt.Errorf("storage.GetPartialPairs: scan: %w", err)
		}
		if pairSides[pairID] == nil {
			pairSides[pairID] = &sides{}
		}
		if side == "YES" {
			pairSides[pairID].yes = true
		} else {
			pairSides[pairID].no = true
		}
	}

	var partials []string
	for pairID, s := range pairSides {
		if s.yes != s.no {
			partials = append(partials, pairID)
		}
	}
	return partials, nil
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func nullTime(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return t.UTC()
}

func nullTimeVal(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
