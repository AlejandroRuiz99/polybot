package scanner

import (
	"context"
	"testing"
	"time"

	"github.com/alejandrodnm/polybot/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockTradeProvider struct {
	trades map[string][]domain.Trade
}

func (m *mockTradeProvider) FetchTrades(_ context.Context, tokenID string) ([]domain.Trade, error) {
	return m.trades[tokenID], nil
}

func TestCountFillsAtPrice(t *testing.T) {
	trades := []domain.Trade{
		{Side: "SELL", Price: 0.50},
		{Side: "SELL", Price: 0.48},
		{Side: "SELL", Price: 0.52},
		{Side: "BUY", Price: 0.45},
	}
	assert.Equal(t, 2, countFillsAtPrice(trades, 0.50))
	assert.Equal(t, 1, countFillsAtPrice(trades, 0.48))
	assert.Equal(t, 3, countFillsAtPrice(trades, 0.55))
	assert.Equal(t, 0, countFillsAtPrice(trades, 0.40))
}

func TestTradePeriod(t *testing.T) {
	now := time.Now()
	trades := []domain.Trade{
		{Timestamp: now.Add(-48 * time.Hour)},
		{Timestamp: now.Add(-24 * time.Hour)},
		{Timestamp: now},
	}
	d := tradePeriod(trades, nil)
	assert.InDelta(t, 48, d.Hours(), 1)
}

func TestTradePeriod_Empty(t *testing.T) {
	d := tradePeriod(nil, nil)
	assert.Equal(t, 24*time.Hour, d)
}

func TestBacktest_Basic(t *testing.T) {
	now := time.Now()
	yesTokenID := "yes-token-123"
	noTokenID := "no-token-456"

	opp := domain.Opportunity{
		Market: domain.Market{
			ConditionID: "test-condition",
			Question:    "Will it rain?",
			Tokens: [2]domain.Token{
				{TokenID: yesTokenID, Outcome: "Yes"},
				{TokenID: noTokenID, Outcome: "No"},
			},
		},
		YesBook: domain.OrderBook{
			Bids: []domain.BookEntry{{Price: 0.65, Size: 100}},
			Asks: []domain.BookEntry{{Price: 0.67, Size: 100}},
		},
		NoBook: domain.OrderBook{
			Bids: []domain.BookEntry{{Price: 0.33, Size: 100}},
			Asks: []domain.BookEntry{{Price: 0.35, Size: 100}},
		},
		YourDailyReward: 0.50,
		FillCostUSDC:    0.10,
	}

	trades := &mockTradeProvider{
		trades: map[string][]domain.Trade{
			yesTokenID: {
				{Side: "SELL", Price: 0.64, Timestamp: now.Add(-20 * time.Hour)},
				{Side: "SELL", Price: 0.60, Timestamp: now.Add(-10 * time.Hour)},
				{Side: "BUY", Price: 0.70, Timestamp: now.Add(-5 * time.Hour)},
			},
			noTokenID: {
				{Side: "SELL", Price: 0.32, Timestamp: now.Add(-18 * time.Hour)},
				{Side: "SELL", Price: 0.30, Timestamp: now.Add(-6 * time.Hour)},
				{Side: "BUY", Price: 0.40, Timestamp: now},
			},
		},
	}

	results, err := Backtest(context.Background(), []domain.Opportunity{opp}, trades, 100)
	require.NoError(t, err)
	require.Len(t, results, 1)

	r := results[0]
	assert.Equal(t, "Will it rain?", r.Market.Question)
	assert.Equal(t, 3, r.TotalTradesYes)
	assert.Equal(t, 3, r.TotalTradesNo)
	assert.Equal(t, 2, r.FillsYes)  // sells at 0.64 and 0.60 ≤ bid 0.65
	assert.Equal(t, 2, r.FillsNo)   // sells at 0.32 and 0.30 ≤ bid 0.33
	assert.Greater(t, r.FillsBothPerDay, 0.0)
	assert.Contains(t, []string{"PROFITABLE", "MARGINAL", "NOT_PROFITABLE"}, r.Verdict)
}
