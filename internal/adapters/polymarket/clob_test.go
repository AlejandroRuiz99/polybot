package polymarket_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/alejandrodnm/polybot/internal/adapters/polymarket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestClient(clobSrv, gammaSrv *httptest.Server) *polymarket.Client {
	clobURL := ""
	gammaURL := ""
	if clobSrv != nil {
		clobURL = clobSrv.URL
	}
	if gammaSrv != nil {
		gammaURL = gammaSrv.URL
	}
	return polymarket.NewClient(clobURL, gammaURL)
}

func TestFetchSamplingMarkets_Success(t *testing.T) {
	data, err := os.ReadFile("../../../testdata/fixtures/clob_sampling_markets.json")
	require.NoError(t, err)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/sampling-markets", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	}))
	defer srv.Close()

	client := newTestClient(srv, nil)
	markets, err := client.FetchSamplingMarkets(context.Background())

	require.NoError(t, err)
	require.Len(t, markets, 2)

	m := markets[0]
	assert.Equal(t, "0xabc123", m.ConditionID)
	assert.Equal(t, "0xq001", m.QuestionID)
	assert.True(t, m.Active)
	assert.False(t, m.Closed)
	assert.InDelta(t, 25.5, m.Rewards.DailyRate, 0.001)
	assert.InDelta(t, 0.04, m.Rewards.MaxSpread, 0.0001)
	assert.InDelta(t, 10.0, m.Rewards.MinSize, 0.001)

	assert.Equal(t, "token_yes_001", m.YesToken().TokenID)
	assert.Equal(t, "token_no_001", m.NoToken().TokenID)
	assert.InDelta(t, 0.72, m.YesToken().Price, 0.001)
}

func TestFetchSamplingMarkets_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := newTestClient(srv, nil)
	_, err := client.FetchSamplingMarkets(context.Background())
	assert.Error(t, err)
}

func TestFetchOrderBooks_Batch(t *testing.T) {
	data, err := os.ReadFile("../../../testdata/fixtures/clob_orderbooks_batch.json")
	require.NoError(t, err)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/books", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	}))
	defer srv.Close()

	client := newTestClient(srv, nil)
	books, err := client.FetchOrderBooks(context.Background(), []string{"token_yes_001", "token_no_001"})

	require.NoError(t, err)
	require.Len(t, books, 2)

	yesBook, ok := books["token_yes_001"]
	require.True(t, ok)
	assert.Equal(t, "token_yes_001", yesBook.TokenID)
	assert.InDelta(t, 0.70, yesBook.BestBid(), 0.001)
	assert.InDelta(t, 0.72, yesBook.BestAsk(), 0.001)
	assert.InDelta(t, 0.71, yesBook.Midpoint(), 0.001)

	noBook, ok := books["token_no_001"]
	require.True(t, ok)
	assert.InDelta(t, 0.27, noBook.BestBid(), 0.001)
	assert.InDelta(t, 0.29, noBook.BestAsk(), 0.001)
}

func TestFetchOrderBooks_BatchSplitting(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		// Devuelve array vacío para simplificar
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]any{})
	}))
	defer srv.Close()

	client := newTestClient(srv, nil)

	// 25 token_ids → debe hacer 2 requests (batch de 20 + batch de 5)
	tokenIDs := make([]string, 25)
	for i := range tokenIDs {
		tokenIDs[i] = "token_" + string(rune('a'+i%26))
	}

	_, err := client.FetchOrderBooks(context.Background(), tokenIDs)
	require.NoError(t, err)
	assert.Equal(t, 2, callCount, "debe hacer 2 requests batch para 25 tokens")
}
