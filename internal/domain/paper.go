package domain

import "time"

// PaperOrderStatus represents the lifecycle of a virtual order.
type PaperOrderStatus string

const (
	PaperStatusOpen      PaperOrderStatus = "OPEN"
	PaperStatusFilled    PaperOrderStatus = "FILLED"
	PaperStatusExpired   PaperOrderStatus = "EXPIRED"
	PaperStatusCancelled PaperOrderStatus = "CANCELLED"
	PaperStatusResolved  PaperOrderStatus = "RESOLVED"
	PaperStatusMerged    PaperOrderStatus = "MERGED"
)

// VirtualOrder is a simulated order the bot would have placed.
type VirtualOrder struct {
	ID           string
	ConditionID  string
	TokenID      string
	Side         string  // "YES" or "NO"
	BidPrice     float64
	Size         float64 // USDC
	PlacedAt     time.Time
	Status       PaperOrderStatus
	FilledAt     *time.Time
	FilledPrice  float64
	PairID       string // links YES+NO orders for the same market
	Question     string
	QueueAhead   float64    // estimated USDC ahead in the book at placement time
	DailyReward  float64    // estimated daily reward at placement time
	EndDate      time.Time
	MergedAt     *time.Time // when the pair was merged (compound rotation)
}

// PaperFill records when a real trade would have filled a virtual order.
type PaperFill struct {
	ID        int64
	OrderID   string
	TradeID   string
	Price     float64
	Size      float64
	Timestamp time.Time
}

// PaperPosition is the current state of a simulated position in a market.
type PaperPosition struct {
	ConditionID      string
	PairID           string
	Question         string
	YesOrder         *VirtualOrder
	NoOrder          *VirtualOrder
	YesFilled        bool
	NoFilled         bool
	IsComplete       bool       // both sides filled
	IsMerged         bool       // pair was merged for compound rotation
	IsResolved       bool       // market has resolved
	PartialSince     *time.Time // how long only one side has been filled
	FillCostPair     float64
	DailyReward      float64
	RewardAccrued    float64    // total reward earned while orders were active
	SpreadQualifies  bool       // whether current spread qualifies for rewards
	HoursToEnd       float64    // hours remaining until market resolution
	CapitalDeployed  float64    // total USDC locked in this position
	MergeProfit      float64    // profit from merging YES+NO → $1
	MergeReturn      float64    // total USDC returned from merge
	CycleHours       float64    // time from placement to merge completion
}

// PartialDuration returns how long the position has been partially filled.
func (p PaperPosition) PartialDuration() time.Duration {
	if p.PartialSince == nil || p.IsComplete {
		return 0
	}
	return time.Since(*p.PartialSince)
}

// PaperDailySummary is the daily snapshot for the paper trading dashboard.
type PaperDailySummary struct {
	Date             time.Time
	ActivePositions  int
	CompletePairs    int
	PartialFills     int
	TotalReward      float64
	TotalFillPnL     float64
	NetPnL           float64
	AvgPartialMins   float64
	FillsYes         int
	FillsNo          int
	OrdersPlaced     int
	CapitalDeployed  float64
	MarketsResolved  int
	ResolutionPnL    float64
	Rotations        int
	MergeProfit      float64
	CompoundBalance  float64
}

// PaperStats is the aggregate statistics across the entire paper trading run.
type PaperStats struct {
	StartDate        time.Time
	EndDate          time.Time
	DaysRunning      int
	TotalOrders      int
	TotalFills       int
	CompletePairs    int
	PartialFills     int
	AvgPartialMins   float64
	MaxPartialMins   float64
	TotalReward      float64
	TotalFillPnL     float64
	NetPnL           float64
	DailyAvgPnL      float64
	FillRateReal     float64
	MarketsMonitored int
	MarketsResolved  int
	ResolutionPnL    float64
	MaxCapital       float64
	TotalRotations   int
	TotalMergeProfit float64
	CompoundBalance  float64
	CompoundGrowth   float64 // multiplier vs initial capital
	AvgCycleHours    float64
	InitialCapital   float64
	Dailies          []PaperDailySummary
}
