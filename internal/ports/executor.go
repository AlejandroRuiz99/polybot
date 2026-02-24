package ports

import (
	"context"

	"github.com/alejandrodnm/polybot/internal/domain"
)

// OrderExecutor places, cancels, and monitors real orders on Polymarket CLOB.
type OrderExecutor interface {
	// PlaceOrder signs and submits a limit maker order to the CLOB.
	PlaceOrder(ctx context.Context, req domain.PlaceOrderRequest) (domain.PlacedOrder, error)

	// CancelOrder cancels a specific order by its CLOB order ID.
	CancelOrder(ctx context.Context, clobOrderID string) error

	// CancelAll cancels all open orders for this wallet.
	CancelAll(ctx context.Context) error

	// GetOpenOrders returns all currently open/partial orders from the CLOB.
	GetOpenOrders(ctx context.Context) ([]domain.LiveOrder, error)

	// GetBalance returns the available USDC.e balance in the CLOB.
	GetBalance(ctx context.Context) (float64, error)

	// IsNegRisk returns true if the given token/market uses the NegRisk adapter.
	IsNegRisk(ctx context.Context, tokenID string) (bool, error)

	// TokenBalance returns the on-chain ERC-1155 balance (in shares) for a token.
	// This is the ground truth — if > 0, the order was filled regardless of DB state.
	TokenBalance(ctx context.Context, tokenID string) (float64, error)
}

// MergeExecutor executes on-chain CTF merge transactions.
type MergeExecutor interface {
	// MergePositions merges amount YES+NO tokens into USDC.e on-chain.
	// conditionID is the market's condition ID.
	// amount is the number of token sets to merge (in USDC units).
	// negRisk indicates if the market uses the NegRisk adapter.
	MergePositions(ctx context.Context, conditionID string, amount float64, negRisk bool) (domain.MergeResult, error)

	// EstimateGasCostUSD returns the current estimated gas cost in USD for a merge tx.
	EstimateGasCostUSD(ctx context.Context) (float64, error)

	// EnsureApprovals verifies and sets ERC1155 setApprovalForAll on all three
	// Polymarket exchange contracts. Should be called on startup.
	EnsureApprovals(ctx context.Context) error
}
