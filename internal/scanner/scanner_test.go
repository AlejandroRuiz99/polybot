package scanner_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alejandrodnm/polybot/internal/domain"
	"github.com/alejandrodnm/polybot/internal/ports"
	"github.com/alejandrodnm/polybot/internal/scanner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- mocks ---

type mockMarketProvider struct {
	markets []domain.Market
	err     error
}

func (m *mockMarketProvider) FetchSamplingMarkets(_ context.Context) ([]domain.Market, error) {
	return m.markets, m.err
}

type mockBookProvider struct {
	books map[string]domain.OrderBook
	err   error
}

func (m *mockBookProvider) FetchOrderBooks(_ context.Context, _ []string) (map[string]domain.OrderBook, error) {
	return m.books, m.err
}

type mockNotifier struct {
	notified []domain.Opportunity
	err      error
}

func (m *mockNotifier) Notify(_ context.Context, opps []domain.Opportunity) error {
	m.notified = opps
	return m.err
}

type mockStorage struct {
	saved []domain.Opportunity
	err   error
}

func (m *mockStorage) SaveScan(_ context.Context, opps []domain.Opportunity) error {
	m.saved = opps
	return m.err
}

func (m *mockStorage) GetHistory(_ context.Context, _, _ time.Time) ([]domain.Opportunity, error) {
	return nil, nil
}

func (m *mockStorage) Close() error { return nil }

// --- helpers ---

func makeMarket(condID, yesID, noID string, dailyRate, maxSpread float64) domain.Market {
	return domain.Market{
		ConditionID: condID,
		Active:      true,
		Tokens: [2]domain.Token{
			{TokenID: yesID, Outcome: "Yes", Price: 0.72},
			{TokenID: noID, Outcome: "No", Price: 0.28},
		},
		Rewards: domain.RewardConfig{
			DailyRate: dailyRate,
			MinSize:   10,
			MaxSpread: maxSpread,
		},
	}
}

func makeBooks(yesID, noID string) map[string]domain.OrderBook {
	return map[string]domain.OrderBook{
		yesID: {
			TokenID: yesID,
			Bids:    []domain.BookEntry{{Price: 0.70, Size: 150}},
			Asks:    []domain.BookEntry{{Price: 0.72, Size: 200}},
		},
		noID: {
			TokenID: noID,
			Bids:    []domain.BookEntry{{Price: 0.27, Size: 100}},
			Asks:    []domain.BookEntry{{Price: 0.29, Size: 180}},
		},
	}
}

func newTestScanner(mp ports.MarketProvider, bp ports.BookProvider, n ports.Notifier, s ports.Storage) *scanner.Scanner {
	cfg := scanner.DefaultConfig()
	cfg.Filter.RequireQualifies = true
	cfg.Filter.MinRewardScore = 0
	return scanner.New(cfg, mp, bp, s, n)
}

// --- tests ---

func TestScanner_RunOnce_Success(t *testing.T) {
	market := makeMarket("0xabc", "yes1", "no1", 25.5, 0.04)
	books := makeBooks("yes1", "no1")

	mp := &mockMarketProvider{markets: []domain.Market{market}}
	bp := &mockBookProvider{books: books}
	notifier := &mockNotifier{}
	storage := &mockStorage{}

	s := newTestScanner(mp, bp, notifier, storage)
	opps, err := s.RunOnce(context.Background())

	require.NoError(t, err)
	require.Len(t, opps, 1)

	opp := opps[0]
	assert.Equal(t, "0xabc", opp.Market.ConditionID)
	// spread = 0.72 + 0.29 - 1.0 = 0.01
	assert.InDelta(t, 0.01, opp.SpreadTotal, 0.001)
	assert.True(t, opp.QualifiesReward) // 0.01 <= 0.04
	assert.Greater(t, opp.RewardScore, 0.0)
}

func TestScanner_RunOnce_FiltersHighSpread(t *testing.T) {
	// maxSpread=0.004 pero spread real=0.01 → no califica
	market := makeMarket("0xdef", "yes2", "no2", 25.5, 0.004)
	books := makeBooks("yes2", "no2")

	mp := &mockMarketProvider{markets: []domain.Market{market}}
	bp := &mockBookProvider{books: books}
	notifier := &mockNotifier{}

	s := newTestScanner(mp, bp, notifier, nil)
	opps, err := s.RunOnce(context.Background())

	require.NoError(t, err)
	assert.Empty(t, opps, "debe filtrar mercado con spread > maxSpread")
}

func TestScanner_RunOnce_MarketProviderError(t *testing.T) {
	mp := &mockMarketProvider{err: errors.New("API down")}
	bp := &mockBookProvider{}
	notifier := &mockNotifier{}

	s := newTestScanner(mp, bp, notifier, nil)
	_, err := s.RunOnce(context.Background())
	assert.Error(t, err)
}

func TestScanner_RunOnce_BookProviderError(t *testing.T) {
	market := makeMarket("0xabc", "yes1", "no1", 25.5, 0.04)
	mp := &mockMarketProvider{markets: []domain.Market{market}}
	bp := &mockBookProvider{err: errors.New("books unavailable")}
	notifier := &mockNotifier{}

	s := newTestScanner(mp, bp, notifier, nil)
	_, err := s.RunOnce(context.Background())
	assert.Error(t, err)
}

func TestScanner_RunOnce_RankedByCombinedScore(t *testing.T) {
	// El mercado con mayor CombinedScore va primero.
	// Sin arbitraje, CombinedScore ≈ YourDailyReward → m2 (pool=100) antes que m1 (pool=10).
	m1 := makeMarket("0xlow", "yL", "nL", 10.0, 0.04)
	m2 := makeMarket("0xhigh", "yH", "nH", 100.0, 0.04)

	books := makeBooks("yL", "nL")
	for k, v := range makeBooks("yH", "nH") {
		books[k] = v
	}

	mp := &mockMarketProvider{markets: []domain.Market{m1, m2}}
	bp := &mockBookProvider{books: books}
	notifier := &mockNotifier{}

	s := newTestScanner(mp, bp, notifier, nil)
	opps, err := s.RunOnce(context.Background())

	require.NoError(t, err)
	require.Len(t, opps, 2)
	// Ordenado por CombinedScore descendente
	assert.GreaterOrEqual(t, opps[0].CombinedScore, opps[1].CombinedScore,
		"debe estar ordenado por CombinedScore desc")
	// Sin arb, CombinedScore ≈ YourDailyReward → el más rentable va primero
	assert.GreaterOrEqual(t, opps[0].YourDailyReward, opps[1].YourDailyReward,
		"m2 con pool mayor debe ir antes")
}
