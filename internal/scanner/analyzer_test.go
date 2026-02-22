package scanner

import (
	"context"
	"testing"

	"github.com/alejandrodnm/polybot/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

	a := NewAnalyzer(100, 0.02)
	opp, err := a.Analyze(context.Background(), market, yesBook, noBook)

	require.NoError(t, err)
	// spread = 0.72 + 0.29 - 1.0 = 0.01
	assert.InDelta(t, 0.01, opp.SpreadTotal, 0.001)
	assert.True(t, opp.QualifiesReward)

	// C1: YourDailyReward debe ser mucho menor que el pool total
	assert.Greater(t, opp.YourDailyReward, 0.0)
	assert.Less(t, opp.YourDailyReward, market.Rewards.DailyRate)

	// C3: ArbitrageGap negativo (0.72 + 0.29 > 1.0 → no hay arb)
	assert.False(t, opp.HasArbitrage)
	assert.Equal(t, opp.YesAsk, 0.72)
	assert.Equal(t, opp.NoAsk, 0.29)
	assert.InDelta(t, 1.01, opp.YesNoSum, 0.001)

	// C6: YourShare definido
	assert.Greater(t, opp.YourShare, 0.0)
	assert.Less(t, opp.YourShare, 1.0)
}

func TestAnalyzer_Analyze_EmptyBook(t *testing.T) {
	market := domain.Market{ConditionID: "0xtest"}
	a := NewAnalyzer(100, 0.02)
	_, err := a.Analyze(context.Background(), market, domain.OrderBook{}, domain.OrderBook{})
	assert.Error(t, err)
}

func TestAnalyzer_Analyze_SpreadExceedsMaxSpread(t *testing.T) {
	market := domain.Market{
		Rewards: domain.RewardConfig{DailyRate: 25, MaxSpread: 0.004},
	}
	yesBook := makeBook("yes", 0.70, 0.72, 100)
	noBook := makeBook("no", 0.27, 0.29, 100)

	a := NewAnalyzer(100, 0.02)
	opp, err := a.Analyze(context.Background(), market, yesBook, noBook)

	require.NoError(t, err)
	assert.False(t, opp.QualifiesReward)
	assert.Equal(t, 0.0, opp.YourDailyReward) // spread > maxSpread → 0
}

func TestAnalyzer_Analyze_ArbitrageDetected(t *testing.T) {
	market := domain.Market{
		Rewards: domain.RewardConfig{DailyRate: 25, MaxSpread: 0.5},
	}
	// YES ask=0.49, NO ask=0.49 → sum=0.98 < 1.0 → arbitraje
	yesBook := makeBook("yes", 0.48, 0.49, 100)
	noBook := makeBook("no", 0.48, 0.49, 100)

	a := NewAnalyzer(100, 0.001)
	opp, err := a.Analyze(context.Background(), market, yesBook, noBook)

	require.NoError(t, err)
	assert.True(t, opp.HasArbitrage, "debe detectar arbitraje con YES+NO < 1.0")
	assert.Greater(t, opp.ArbitrageGap, 0.0)
}

func TestAnalyzer_Analyze_UsesMakerBaseFee(t *testing.T) {
	market := domain.Market{
		MakerBaseFee: 0.005, // 0.5% del mercado
		Rewards:      domain.RewardConfig{DailyRate: 25, MaxSpread: 0.04},
	}
	yesBook := makeBook("yes", 0.70, 0.72, 200)
	noBook := makeBook("no", 0.27, 0.29, 180)

	a := NewAnalyzer(100, 0.02) // defaultFeeRate=2% pero mercado tiene 0.5%
	opp, err := a.Analyze(context.Background(), market, yesBook, noBook)

	require.NoError(t, err)
	// Con fee=0.5%, los fees son 100*0.005*2=$1.0
	// Con fee=2%, los fees son 100*0.02*2=$4.0
	// NetProfit con fee del mercado debe ser mayor
	assert.Greater(t, opp.NetProfitEst, -4.0) // no debe incluir el penalizado default
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

func TestFilter_Apply_ByResolutionTime(t *testing.T) {
	cfg := DefaultFilterConfig()
	cfg.MinHoursToResolution = 48
	cfg.RequireQualifies = false
	f := NewFilter(cfg)

	import_time := "placeholder to avoid import issues"
	_ = import_time
	// El filtro por tiempo requiere market.EndDate — se testa en scanner_test.go via mocks
	// Este test verifica que sin EndDate (zero value) el filtro NO descarta el mercado
	noEndDate := domain.Opportunity{YourDailyReward: 0.5, QualifiesReward: true}
	result := f.Apply([]domain.Opportunity{noEndDate})
	assert.Len(t, result, 1, "sin EndDate definido, no debe filtrarse")
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
	assert.Equal(t, passing.RewardScore, result[0].RewardScore)
}
