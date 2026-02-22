package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRewardScore_ValidInputs(t *testing.T) {
	score := RewardScore(100, 2, 25)
	assert.InDelta(t, 24.01, score, 0.001)
}

func TestRewardScore_ZeroOrderSize(t *testing.T) {
	assert.Equal(t, 0.0, RewardScore(0, 2, 25))
}

func TestRewardScore_ZeroRewardRate(t *testing.T) {
	assert.Equal(t, 0.0, RewardScore(100, 2, 0))
}

func TestRewardScore_SpreadExceedsOrderSize(t *testing.T) {
	assert.Equal(t, 0.0, RewardScore(100, 150, 25))
}

func TestRewardScore_SpreadEqualToOrderSize(t *testing.T) {
	assert.Equal(t, 0.0, RewardScore(100, 100, 25))
}

// --- EstimateYourDailyReward (C1) ---

func TestEstimateYourDailyReward_Basic(t *testing.T) {
	// orderSize=100, competition=35000, dailyRate=200, spread=0.02, maxSpread=5.5
	// yourShare = 100 / (100 + 35000) = 0.002849
	// spreadScore = ((5.5 - 0.02) / 5.5)² = (5.48/5.5)² = 0.9928
	// reward = 200 * 0.002849 * 0.9928 ≈ 0.566
	reward := EstimateYourDailyReward(100, 35000, 200, 0.02, 5.5)
	assert.InDelta(t, 0.566, reward, 0.05)
}

func TestEstimateYourDailyReward_NoCompetition(t *testing.T) {
	// Sin competencia, tu share es casi 1.0
	reward := EstimateYourDailyReward(100, 0, 50, 0.01, 3.5)
	assert.Greater(t, reward, 0.0)
	assert.InDelta(t, 50.0, reward, 5.0) // casi todo el pool
}

func TestEstimateYourDailyReward_SpreadAtMax(t *testing.T) {
	// spread = maxSpread → spreadScore = 0 → reward = 0
	assert.Equal(t, 0.0, EstimateYourDailyReward(100, 5000, 50, 3.5, 3.5))
}

func TestEstimateYourDailyReward_SpreadBeyondMax(t *testing.T) {
	assert.Equal(t, 0.0, EstimateYourDailyReward(100, 5000, 50, 4.0, 3.5))
}

func TestEstimateYourDailyReward_InvalidInputs(t *testing.T) {
	assert.Equal(t, 0.0, EstimateYourDailyReward(0, 5000, 50, 0.02, 3.5))
	assert.Equal(t, 0.0, EstimateYourDailyReward(100, 5000, 0, 0.02, 3.5))
	assert.Equal(t, 0.0, EstimateYourDailyReward(100, 5000, 50, 0.02, 0))
}

func TestComputeSpreadScore(t *testing.T) {
	// spread=0.01, maxSpread=0.04 → ((0.04-0.01)/0.04)² = (0.75)² = 0.5625
	score := ComputeSpreadScore(0.01, 0.04)
	assert.InDelta(t, 0.5625, score, 0.0001)
}

func TestComputeSpreadScore_AtMax(t *testing.T) {
	assert.Equal(t, 0.0, ComputeSpreadScore(0.04, 0.04))
}

func TestEstimateArbitrageGap(t *testing.T) {
	// yesAsk=0.49, noAsk=0.49, fee=0.001
	// gap = 1 - 0.98 - (0.98 * 0.001) = 0.02 - 0.00098 ≈ 0.0190
	gap := EstimateArbitrageGap(0.49, 0.49, 0.001)
	assert.Greater(t, gap, 0.0)
	assert.InDelta(t, 0.019, gap, 0.001)
}

func TestEstimateArbitrageGap_NoArb(t *testing.T) {
	// yesAsk=0.72, noAsk=0.32 → sum=1.04 → gap negativo
	gap := EstimateArbitrageGap(0.72, 0.32, 0.001)
	assert.Less(t, gap, 0.0)
}

// --- SpreadTotal ---

func TestSpreadTotal_Normal(t *testing.T) {
	spread := SpreadTotal(0.72, 0.32)
	assert.InDelta(t, 0.04, spread, 0.0001)
}

func TestSpreadTotal_Arbitrage(t *testing.T) {
	spread := SpreadTotal(0.49, 0.49)
	assert.True(t, spread < 0)
}

// --- EstimateNetProfit ---

func TestEstimateNetProfit(t *testing.T) {
	// yourDailyReward=0.57, orderSize=100, feeRate=0.02
	// fees = 100 * 0.02 * 2 = 4.0
	// net = 0.57 - 4.0 = -3.43 (negativo con fee real de 2%)
	profit := EstimateNetProfit(0.57, 100, 0.02)
	assert.InDelta(t, -3.43, profit, 0.01)
}

// --- OrderBook ---

func TestOrderBook_BestBid_Empty(t *testing.T) {
	assert.Equal(t, 0.0, OrderBook{}.BestBid())
}

func TestOrderBook_BestAsk_Empty(t *testing.T) {
	assert.Equal(t, 0.0, OrderBook{}.BestAsk())
}

func TestOrderBook_Midpoint(t *testing.T) {
	ob := OrderBook{
		Bids: []BookEntry{{Price: 0.70, Size: 100}},
		Asks: []BookEntry{{Price: 0.72, Size: 150}},
	}
	assert.InDelta(t, 0.71, ob.Midpoint(), 0.0001)
}

func TestOrderBook_Spread(t *testing.T) {
	ob := OrderBook{
		Bids: []BookEntry{{Price: 0.70, Size: 100}},
		Asks: []BookEntry{{Price: 0.72, Size: 150}},
	}
	assert.InDelta(t, 0.02, ob.Spread(), 0.0001)
}

func TestOrderBook_DepthWithin(t *testing.T) {
	ob := OrderBook{
		Bids: []BookEntry{
			{Price: 0.70, Size: 100}, // mid=0.71, dist=0.01 ≤ 0.02 ✓
			{Price: 0.65, Size: 200}, // dist=0.06 > 0.02 ✗
		},
		Asks: []BookEntry{
			{Price: 0.72, Size: 150}, // dist=0.01 ≤ 0.02 ✓
			{Price: 0.78, Size: 300}, // dist=0.07 > 0.02 ✗
		},
	}
	depth := ob.DepthWithin(0.02)
	assert.InDelta(t, 250.0, depth, 0.001) // 100 + 150 (token units)
}

func TestOrderBook_DepthWithinUSDC(t *testing.T) {
	ob := OrderBook{
		Bids: []BookEntry{
			{Price: 0.70, Size: 100}, // within: 100*0.70 = 70 USDC
			{Price: 0.65, Size: 200}, // outside
		},
		Asks: []BookEntry{
			{Price: 0.72, Size: 150}, // within: 150*0.72 = 108 USDC
			{Price: 0.78, Size: 300}, // outside
		},
	}
	depth := ob.DepthWithinUSDC(0.02)
	assert.InDelta(t, 178.0, depth, 0.001) // 70 + 108
}

func TestMarket_HasRewards(t *testing.T) {
	m := Market{Rewards: RewardConfig{DailyRate: 25, MaxSpread: 0.04}}
	assert.True(t, m.HasRewards())

	m2 := Market{Rewards: RewardConfig{DailyRate: 0, MaxSpread: 0.04}}
	assert.False(t, m2.HasRewards())
}

func TestMarket_EffectiveFeeRate(t *testing.T) {
	// Con fee del mercado disponible
	m := Market{MakerBaseFee: 0.005}
	assert.Equal(t, 0.005, m.EffectiveFeeRate(0.02))

	// Sin fee del mercado → usa default
	m2 := Market{}
	assert.Equal(t, 0.02, m2.EffectiveFeeRate(0.02))
}

func TestParsePrice(t *testing.T) {
	assert.Equal(t, 0.72, ParsePrice("0.72"))
	assert.Equal(t, 0.0, ParsePrice(""))
}
