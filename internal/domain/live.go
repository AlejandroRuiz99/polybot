package domain

import "time"

// LiveOrderStatus represents the lifecycle of a real order on Polymarket CLOB.
type LiveOrderStatus string

const (
	LiveStatusOpen      LiveOrderStatus = "OPEN"
	LiveStatusPartial   LiveOrderStatus = "PARTIAL"
	LiveStatusFilled    LiveOrderStatus = "FILLED"
	LiveStatusCancelled LiveOrderStatus = "CANCELLED"
	LiveStatusExpired   LiveOrderStatus = "EXPIRED"
	LiveStatusMerged    LiveOrderStatus = "MERGED"
)

// LiveOrder is a real order placed on Polymarket CLOB.
type LiveOrder struct {
	ID            string          // UUID (local tracking)
	CLOBOrderID   string          // Polymarket order hash (0x...)
	ConditionID   string
	TokenID       string
	Side          string          // "YES" or "NO"
	BidPrice      float64
	Size          float64         // USDC total
	FilledSize    float64         // USDC filled so far
	PlacedAt      time.Time
	Status        LiveOrderStatus
	FilledAt      *time.Time
	FilledPrice   float64
	PairID        string          // links YES+NO for same market
	Question      string
	QueueAhead    float64
	DailyReward   float64
	EndDate       time.Time
	MergedAt      *time.Time
	NegRisk       bool            // whether the market uses NegRisk adapter
	CompetitionAt float64         // competition level at placement (for stale detection)
}

// LiveFill is a real fill event detected from CLOB.
type LiveFill struct {
	ID          int64
	OrderID     string  // local tracking ID
	CLOBTradeID string
	Price       float64
	Size        float64
	Timestamp   time.Time
}

// MergeResult represents the result of an on-chain CTF merge.
type MergeResult struct {
	ConditionID  string
	PairID       string
	TxHash       string
	GasUsedPOL   float64
	GasCostUSD   float64
	USDCReceived float64
	SpreadProfit float64 // USDCReceived - capital_deployed
	Success      bool
	Error        string
	ExecutedAt   time.Time
}

// LivePosition is the current state of a real position in a market.
type LivePosition struct {
	ConditionID     string
	PairID          string
	Question        string
	YesOrder        *LiveOrder
	NoOrder         *LiveOrder
	YesFilled       bool
	NoFilled        bool
	IsComplete      bool
	IsMerged        bool
	PartialSince    *time.Time
	FillCostPair    float64
	DailyReward     float64
	RewardAccrued   float64
	SpreadQualifies bool
	HoursToEnd      float64
	CapitalDeployed float64
	MergeProfit     float64
	MergeReturn     float64
	CycleHours      float64
}

// PartialDuration returns how long this position has been partially filled.
func (p LivePosition) PartialDuration() time.Duration {
	if p.PartialSince == nil || p.IsComplete {
		return 0
	}
	return time.Since(*p.PartialSince)
}

// CircuitBreaker tracks consecutive losses and enforces trading pauses.
type CircuitBreaker struct {
	ConsecutiveLosses int
	MaxLosses         int
	CooldownUntil     time.Time
	CooldownDuration  time.Duration
	TotalPnL          float64
	MaxDrawdown       float64 // negative dollar amount threshold
	Triggered         bool
	TriggeredReason   string
}

// IsOpen returns true if trading is allowed (circuit not triggered).
func (cb *CircuitBreaker) IsOpen() bool {
	if cb.Triggered {
		return false
	}
	if time.Now().Before(cb.CooldownUntil) {
		return false
	}
	return true
}

// RecordLoss records a negative merge result and may trip the breaker.
func (cb *CircuitBreaker) RecordLoss(loss float64) {
	cb.ConsecutiveLosses++
	cb.TotalPnL += loss
	if cb.ConsecutiveLosses >= cb.MaxLosses {
		cb.CooldownUntil = time.Now().Add(cb.CooldownDuration)
		cb.ConsecutiveLosses = 0
		cb.TriggeredReason = "consecutive losses"
	}
	if cb.TotalPnL < cb.MaxDrawdown {
		cb.Triggered = true
		cb.TriggeredReason = "max drawdown exceeded"
	}
}

// RecordWin resets consecutive loss counter.
func (cb *CircuitBreaker) RecordWin(profit float64) {
	cb.ConsecutiveLosses = 0
	cb.TotalPnL += profit
}

// LiveDailySummary is the daily snapshot for live trading.
type LiveDailySummary struct {
	Date            time.Time
	ActivePositions int
	CompletePairs   int
	PartialFills    int
	TotalReward     float64
	TotalFillPnL    float64
	NetPnL          float64
	AvgPartialMins  float64
	FillsYes        int
	FillsNo         int
	OrdersPlaced    int
	OrdersCancelled int
	CapitalDeployed float64
	Merges          int
	MergeProfit     float64
	GasCostUSD      float64
	CompoundBalance float64
	Rotations       int
}

// LiveStats aggregates statistics for the live trading run.
type LiveStats struct {
	StartDate         time.Time
	EndDate           time.Time
	DaysRunning       int
	TotalOrders       int
	TotalFills        int
	CompletePairs     int
	PartialFills      int
	AvgPartialMins    float64
	TotalReward       float64
	TotalMergeProfit  float64
	TotalGasCostUSD   float64
	NetPnL            float64
	DailyAvgPnL       float64
	FillRateReal      float64
	MarketsMonitored  int
	TotalRotations    int
	CompoundBalance   float64
	CompoundGrowth    float64
	AvgCycleHours     float64
	InitialCapital    float64
	Dailies           []LiveDailySummary
}

// PlaceOrderRequest is sent to the CLOB order executor.
type PlaceOrderRequest struct {
	TokenID     string
	ConditionID string
	Price       float64
	Size        float64
	Side        string  // "BUY" (maker bid)
	NegRisk     bool
}

// PlacedOrder is the response from the CLOB after placing an order.
type PlacedOrder struct {
	CLOBOrderID string
	Status      string
	TakenAmount float64 // immediately filled (taker portion)
	MadeAmount  float64 // resting in book (maker portion)
}
