package scanner

import (
	"context"
	"math"
	"testing"

	"github.com/alejandrodnm/polybot/internal/domain"
	"github.com/alejandrodnm/polybot/internal/domain/strategy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestAnalyzer(orderSize, feeRate, fillsPerDay, goldMinReward float64) *Analyzer {
	return NewAnalyzer(strategy.NewRewardFarming(strategy.RewardFarmingConfig{
		OrderSize:     orderSize,
		FeeRate:       feeRate,
		FillsPerDay:   fillsPerDay,
		GoldMinReward: goldMinReward,
	}))
}

func makeBook(tokenID string, bid, ask, size float64) domain.OrderBook {
	return domain.OrderBook{
		TokenID: tokenID,
		Bids:    []domain.BookEntry{{Price: bid, Size: size}},
		Asks:    []domain.BookEntry{{Price: ask, Size: size}},
	}
}

func TestAnalyzer_Analyze_Success(t *testing.T) {
	market := domain.Market{
		ConditionID: "0xtest",
		Rewards:     domain.RewardConfig{DailyRate: 25.5, MaxSpread: 0.04, MinSize: 10},
	}
	yesBook := makeBook("yes", 0.70, 0.72, 200)
	noBook := makeBook("no", 0.27, 0.29, 180)

	a := newTestAnalyzer(100, 0.02, 1.0, 0.01)
	opp, err := a.Analyze(context.Background(), market, yesBook, noBook)

	require.NoError(t, err)
	assert.InDelta(t, 0.01, opp.SpreadTotal, 0.001)
	assert.True(t, opp.QualifiesReward)

	// Reward bruto > 0 y menor que el pool total
	assert.Greater(t, opp.YourDailyReward, 0.0)
	assert.Less(t, opp.YourDailyReward, market.Rewards.DailyRate)

	// Fill cost calculated
	assert.NotEqual(t, 0.0, opp.FillCostPerPair)
	assert.Greater(t, opp.BreakEvenFills, 0.0)

	// PnL scenarios: when fill cost > 0, more fills = worse PnL
	if opp.FillCostUSDC > 0 {
		assert.Greater(t, opp.PnLNoFills, opp.PnL1Fill)
		assert.Greater(t, opp.PnL1Fill, opp.PnL3Fills)
	}
}

func TestAnalyzer_Analyze_EmptyBook(t *testing.T) {
	market := domain.Market{ConditionID: "0xtest"}
	a := newTestAnalyzer(100, 0.02, 1.0, 0.01)
	_, err := a.Analyze(context.Background(), market, domain.OrderBook{}, domain.OrderBook{})
	assert.Error(t, err)
}

func TestAnalyzer_Analyze_TrueArbitrage(t *testing.T) {
	market := domain.Market{
		Rewards: domain.RewardConfig{DailyRate: 25, MaxSpread: 0.5},
	}
	yesBook := makeBook("yes", 0.48, 0.49, 100)
	noBook := makeBook("no", 0.48, 0.49, 100)

	a := newTestAnalyzer(100, 0.001, 1.0, 0.01)
	opp, err := a.Analyze(context.Background(), market, yesBook, noBook)

	require.NoError(t, err)
	assert.True(t, opp.Arbitrage.HasArbitrage)
	assert.Equal(t, domain.CategoryGold, opp.Category)

	// Fill cost should be NEGATIVE (fills = profit)
	assert.Less(t, opp.FillCostPerPair, 0.0)
	assert.True(t, math.IsInf(opp.BreakEvenFills, 1), "fills=profit → infinite break even")

	assert.Equal(t, "FILLS=PROFIT", opp.Verdict())
}

func TestAnalyzer_Analyze_NearArb_IsGold(t *testing.T) {
	market := domain.Market{
		Rewards: domain.RewardConfig{DailyRate: 25, MaxSpread: 0.04},
	}
	yesBook := makeBook("yes", 0.49, 0.50, 100)
	noBook := makeBook("no", 0.49, 0.50, 100)

	a := newTestAnalyzer(100, 0.001, 1.0, 0.01)
	opp, err := a.Analyze(context.Background(), market, yesBook, noBook)

	require.NoError(t, err)
	// sum bids = 0.49+0.49 = 0.98 < 1.0 even with fee → Gold
	assert.Equal(t, domain.CategoryGold, opp.Category)
}

func TestAnalyzer_Analyze_HighSpread_IsSilver(t *testing.T) {
	market := domain.Market{
		Rewards: domain.RewardConfig{DailyRate: 500, MaxSpread: 0.5},
	}
	yesBook := makeBook("yes", 0.70, 0.72, 200)
	noBook := makeBook("no", 0.27, 0.29, 180)

	a := newTestAnalyzer(100, 0.02, 1.0, 0.01)
	opp, err := a.Analyze(context.Background(), market, yesBook, noBook)

	require.NoError(t, err)
	// sum asks = 1.01, gap ≈ -0.03 → Silver
	assert.Equal(t, domain.CategorySilver, opp.Category)
}

func TestFilter_Apply_ByYourDailyReward(t *testing.T) {
	cfg := DefaultFilterConfig()
	cfg.MinYourDailyReward = 0.05
	cfg.RequireQualifies = false
	f := NewFilter(cfg)

	passing := domain.Opportunity{YourDailyReward: 0.10, QualifiesReward: true}
	failing := domain.Opportunity{YourDailyReward: 0.01, QualifiesReward: true}

	result := f.Apply([]domain.Opportunity{passing, failing})
	require.Len(t, result, 1)
	assert.Equal(t, 0.10, result[0].YourDailyReward)
}

func TestFilter_Apply_Basic(t *testing.T) {
	cfg := DefaultFilterConfig()
	cfg.MinRewardScore = 5.0
	cfg.RequireQualifies = true
	f := NewFilter(cfg)

	passing := domain.Opportunity{
		RewardScore: 10.0, SpreadTotal: 0.02, QualifiesReward: true, Competition: 1000,
	}
	lowScore := domain.Opportunity{
		RewardScore: 0.1, SpreadTotal: 0.02, QualifiesReward: true, Competition: 1000,
	}
	noQualify := domain.Opportunity{
		RewardScore: 10.0, SpreadTotal: 0.05, QualifiesReward: false, Competition: 1000,
	}

	result := f.Apply([]domain.Opportunity{passing, lowScore, noQualify})
	require.Len(t, result, 1)
}
