package ports

import (
	"context"
	"time"

	"github.com/alejandrodnm/polybot/internal/domain"
)

// LiveStorage persists real trading state.
type LiveStorage interface {
	ApplyLiveSchema(ctx context.Context) error

	// Orders
	SaveLiveOrder(ctx context.Context, order domain.LiveOrder) error
	UpdateLiveOrderStatus(ctx context.Context, localID string, status domain.LiveOrderStatus) error
	UpdateLiveOrderFill(ctx context.Context, localID string, filledSize, filledPrice float64, status domain.LiveOrderStatus, filledAt *time.Time) error
	UpdateLiveOrderQueue(ctx context.Context, localID string, queueAhead float64) error
	MarkLiveOrderMerged(ctx context.Context, localID string, mergedAt time.Time) error
	GetOpenLiveOrders(ctx context.Context) ([]domain.LiveOrder, error)
	GetLiveOrdersByPair(ctx context.Context, pairID string) ([]domain.LiveOrder, error)
	GetActiveLiveConditions(ctx context.Context) ([]string, error)
	GetAllLiveOrders(ctx context.Context, status string) ([]domain.LiveOrder, error)
	CancelLiveOrdersByCondition(ctx context.Context, conditionID string) error

	// Fills
	SaveLiveFill(ctx context.Context, fill domain.LiveFill) error

	// Merges
	SaveMergeResult(ctx context.Context, result domain.MergeResult) error
	GetMergeResults(ctx context.Context) ([]domain.MergeResult, error)

	// Circuit breaker persistence
	SaveCircuitBreaker(ctx context.Context, cb domain.CircuitBreaker) error
	LoadCircuitBreaker(ctx context.Context) (domain.CircuitBreaker, error)

	// Daily summaries and stats
	SaveLiveDaily(ctx context.Context, d domain.LiveDailySummary) error
	GetLiveDailies(ctx context.Context) ([]domain.LiveDailySummary, error)
	GetLiveStats(ctx context.Context) (domain.LiveStats, error)

	// GetPartialPairs devuelve los pairIDs donde solo un lado (YES o NO) está filled.
	GetPartialPairs(ctx context.Context) ([]string, error)
}
