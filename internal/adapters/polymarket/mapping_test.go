package polymarket_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
)

func TestMapping_SamplingMarketsRewardSum(t *testing.T) {
	// Fixture con múltiples rates → debe sumarlas
	fixture := `{
		"limit": 1, "count": 1, "next_cursor": "LTE=",
		"data": [{
			"condition_id": "0xtest",
			"question_id": "0xq",
			"tokens": [
				{"token_id": "tid_yes", "outcome": "Yes", "price": 0.6},
				{"token_id": "tid_no",  "outcome": "No",  "price": 0.4}
			],
			"rewards": {
				"rates": [
					{"asset_address": "0xa", "rewards_daily_rate": 10.0},
					{"asset_address": "0xb", "rewards_daily_rate": 15.0}
				],
				"min_size": 5.0,
				"max_spread": 0.03
			},
			"active": true,
			"closed": false
		}]
	}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fixture))
	}))
	defer srv.Close()

	client := newTestClient(srv, nil)
	markets, err := client.FetchSamplingMarkets(context.Background())
	require.NoError(t, err)
	require.Len(t, markets, 1)

	// DailyRate debe ser la suma: 10 + 15 = 25
	assert.InDelta(t, 25.0, markets[0].Rewards.DailyRate, 0.001)
	assert.Equal(t, "Yes", markets[0].YesToken().Outcome)
	assert.Equal(t, "No", markets[0].NoToken().Outcome)
}

func TestMapping_OrderBooksSorted(t *testing.T) {
	data, err := os.ReadFile("../../../testdata/fixtures/clob_orderbooks_batch.json")
	require.NoError(t, err)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	}))
	defer srv.Close()

	client := newTestClient(srv, nil)
	books, err := client.FetchOrderBooks(context.Background(), []string{"token_yes_001"})
	require.NoError(t, err)

	book := books["token_yes_001"]

	// Bids: mayor a menor
	require.Len(t, book.Bids, 2)
	assert.Greater(t, book.Bids[0].Price, book.Bids[1].Price)

	// Asks: menor a mayor
	require.Len(t, book.Asks, 2)
	assert.Less(t, book.Asks[0].Price, book.Asks[1].Price)
}
