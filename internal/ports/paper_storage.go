package ports

import (
	"context"
	"time"

	"github.com/alejandrodnm/polybot/internal/domain"
)

// PaperStorage persists paper trading state.
type PaperStorage interface {
	ApplyPaperSchema(ctx context.Context) error

	SavePaperOrder(ctx context.Context, order domain.VirtualOrder) error
	MarkPaperOrderFilled(ctx context.Context, orderID string, filledAt time.Time, filledPrice float64) error
	MarkPaperOrderResolved(ctx context.Context, orderID string) error
	MarkPaperOrderMerged(ctx context.Context, orderID string, mergedAt time.Time) error
	UpdatePaperOrderQueue(ctx context.Context, orderID string, queueAhead float64) error
	UpdatePaperOrderPartialFill(ctx context.Context, orderID string, filledSize float64, filledPrice float64) error
	ExpirePaperOrders(ctx context.Context, conditionID string) error
	GetOpenPaperOrders(ctx context.Context) ([]domain.VirtualOrder, error) // returns OPEN and PARTIAL
	GetPaperOrdersByPair(ctx context.Context, pairID string) ([]domain.VirtualOrder, error)
	GetActivePaperConditions(ctx context.Context) ([]string, error)
	GetAllPaperOrders(ctx context.Context, status string) ([]domain.VirtualOrder, error)

	SavePaperFill(ctx context.Context, fill domain.PaperFill) error

	SavePaperDaily(ctx context.Context, d domain.PaperDailySummary) error
	GetPaperDailies(ctx context.Context) ([]domain.PaperDailySummary, error)
	GetPaperStats(ctx context.Context) (domain.PaperStats, error)
}
