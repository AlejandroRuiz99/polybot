package domain

import (
	"math"
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

// --- EstimateYourDailyReward ---

func TestEstimateYourDailyReward_Basic(t *testing.T) {
	reward := EstimateYourDailyReward(100, 35000, 200, 0.02, 5.5)
	assert.InDelta(t, 0.566, reward, 0.05)
}

func TestEstimateYourDailyReward_NoCompetition(t *testing.T) {
	reward := EstimateYourDailyReward(100, 0, 50, 0.01, 3.5)
	assert.Greater(t, reward, 0.0)
	assert.InDelta(t, 50.0, reward, 5.0)
}

func TestEstimateYourDailyReward_SpreadAtMax(t *testing.T) {
	assert.Equal(t, 0.0, EstimateYourDailyReward(100, 5000, 50, 3.5, 3.5))
}

func TestEstimateYourDailyReward_InvalidInputs(t *testing.T) {
	assert.Equal(t, 0.0, EstimateYourDailyReward(0, 5000, 50, 0.02, 3.5))
	assert.Equal(t, 0.0, EstimateYourDailyReward(100, 5000, 0, 0.02, 3.5))
	assert.Equal(t, 0.0, EstimateYourDailyReward(100, 5000, 50, 0.02, 0))
}

// --- FillCostPerEvent (honest metric) ---

func TestFillCostPerEvent_Normal(t *testing.T) {
	// yesBid=0.70, noBid=0.28, fee=2%
	// (0.70+0.28)×1.02 - 1.0 = 0.98×1.02 - 1.0 = 0.9996 - 1.0 = -0.0004
	// Wow! Con bids, pagamos MENOS que $1.00 → net neutral
	cost := FillCostPerEvent(0.70, 0.28, 0.02)
	assert.InDelta(t, -0.0004, cost, 0.001)
}

func TestFillCostPerEvent_Expensive(t *testing.T) {
	// yesBid=0.75, noBid=0.30 → sum=1.05
	// (1.05)×1.02 - 1.0 = 1.071 - 1.0 = 0.071 → 7.1c per pair
	cost := FillCostPerEvent(0.75, 0.30, 0.02)
	assert.InDelta(t, 0.071, cost, 0.001)
}

func TestFillCostPerEvent_TrueArb(t *testing.T) {
	// yesBid=0.48, noBid=0.48 → sum=0.96
	// 0.96×1.001 - 1.0 = 0.96096 - 1.0 = -0.039 → 3.9c PROFIT per pair
	cost := FillCostPerEvent(0.48, 0.48, 0.001)
	assert.Less(t, cost, 0.0, "fills should be profitable (true arb)")
}

func TestFillCostUSDC_Normal(t *testing.T) {
	// orderSize=100, yesPrice=0.50, noPrice=0.50, costPerPair=0.01
	// pairs = min(100/0.50, 100/0.50) = min(200, 200) = 200
	// total = 200 × 0.01 = $2.00
	total := FillCostUSDC(100, 0.50, 0.50, 0.01)
	assert.InDelta(t, 2.0, total, 0.001)
}

func TestFillCostUSDC_AsymmetricPrices(t *testing.T) {
	// orderSize=100, yesPrice=0.70, noPrice=0.30, costPerPair=0.02
	// pairs = min(100/0.70, 100/0.30) = min(142.8, 333.3) = 142.8
	// total = 142.8 × 0.02 = $2.857
	total := FillCostUSDC(100, 0.70, 0.30, 0.02)
	assert.InDelta(t, 2.857, total, 0.01)
}

func TestFillCostUSDC_ExtremePricesIgnored(t *testing.T) {
	// yesPrice=0.01 → extreme, return 0
	total := FillCostUSDC(100, 0.01, 0.99, 0.01)
	assert.Equal(t, 0.0, total)
}

func TestFillCostUSDC_Capped(t *testing.T) {
	// orderSize=100, max result = ±200
	total := FillCostUSDC(100, 0.02, 0.98, 0.50)
	assert.LessOrEqual(t, total, 200.0)
}

func TestBreakEvenFills_Normal(t *testing.T) {
	// reward=$0.50/day, fillCost=$0.10/fill → 5 fills before breakeven
	be := BreakEvenFills(0.50, 0.10)
	assert.InDelta(t, 5.0, be, 0.001)
}

func TestBreakEvenFills_FillsAreFree(t *testing.T) {
	// fillCost ≤ 0 → infinite (fills are profit)
	be := BreakEvenFills(0.50, -0.10)
	assert.True(t, math.IsInf(be, 1))
}

func TestBreakEvenFills_NoReward(t *testing.T) {
	assert.Equal(t, 0.0, BreakEvenFills(0, 0.10))
}

func TestEstimateNetProfit_Scenarios(t *testing.T) {
	// reward=0.50, fillCost=0.10, 0 fills → $0.50
	assert.InDelta(t, 0.50, EstimateNetProfit(0.50, 0.10, 0), 0.001)
	// 1 fill → $0.40
	assert.InDelta(t, 0.40, EstimateNetProfit(0.50, 0.10, 1), 0.001)
	// 5 fills → $0.00
	assert.InDelta(t, 0.00, EstimateNetProfit(0.50, 0.10, 5), 0.001)
	// 10 fills → -$0.50
	assert.InDelta(t, -0.50, EstimateNetProfit(0.50, 0.10, 10), 0.001)
}

// --- SpreadTotal ---

func TestSpreadTotal_Normal(t *testing.T) {
	assert.InDelta(t, 0.04, SpreadTotal(0.72, 0.32), 0.0001)
}

func TestSpreadTotal_Arbitrage(t *testing.T) {
	assert.True(t, SpreadTotal(0.49, 0.49) < 0)
}

// --- EstimateArbitrageGap ---

func TestEstimateArbitrageGap_Positive(t *testing.T) {
	gap := EstimateArbitrageGap(0.49, 0.49, 0.001)
	assert.Greater(t, gap, 0.0)
}

func TestEstimateArbitrageGap_Negative(t *testing.T) {
	gap := EstimateArbitrageGap(0.72, 0.32, 0.001)
	assert.Less(t, gap, 0.0)
}

// --- ComputeSpreadScore ---

func TestComputeSpreadScore(t *testing.T) {
	score := ComputeSpreadScore(0.01, 0.04)
	assert.InDelta(t, 0.5625, score, 0.0001)
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

func TestOrderBook_DepthWithinUSDC(t *testing.T) {
	ob := OrderBook{
		Bids: []BookEntry{
			{Price: 0.70, Size: 100},
			{Price: 0.65, Size: 200},
		},
		Asks: []BookEntry{
			{Price: 0.72, Size: 150},
			{Price: 0.78, Size: 300},
		},
	}
	depth := ob.DepthWithinUSDC(0.02)
	assert.InDelta(t, 178.0, depth, 0.001) // 70 + 108
}

func TestMarket_EffectiveFeeRate(t *testing.T) {
	m := Market{MakerBaseFee: 0.005}
	assert.Equal(t, 0.005, m.EffectiveFeeRate(0.02))
	m2 := Market{}
	assert.Equal(t, 0.02, m2.EffectiveFeeRate(0.02))
}

func TestParsePrice(t *testing.T) {
	assert.Equal(t, 0.72, ParsePrice("0.72"))
	assert.Equal(t, 0.0, ParsePrice(""))
}

// --- ArbitrageResult ---

func TestCalculateArbitrage_HasArbitrage(t *testing.T) {
	yesBook := OrderBook{Asks: []BookEntry{{Price: 0.49, Size: 200}}}
	noBook := OrderBook{Asks: []BookEntry{{Price: 0.49, Size: 150}}}
	arb := CalculateArbitrage(yesBook, noBook, 0.001)
	assert.True(t, arb.HasArbitrage)
	assert.Greater(t, arb.ArbitrageGap, 0.0)
}

func TestCalculateArbitrage_NoArbitrage(t *testing.T) {
	yesBook := OrderBook{Asks: []BookEntry{{Price: 0.72, Size: 200}}}
	noBook := OrderBook{Asks: []BookEntry{{Price: 0.32, Size: 180}}}
	arb := CalculateArbitrage(yesBook, noBook, 0.02)
	assert.False(t, arb.HasArbitrage)
}

func TestVolumeWeightedPrice_Basic(t *testing.T) {
	asks := []BookEntry{
		{Price: 0.49, Size: 100},
		{Price: 0.50, Size: 200},
	}
	vwap := VolumeWeightedPrice(asks, 100)
	assert.InDelta(t, 0.495, vwap, 0.01)
}

// --- Categorize ---

func TestCategorize_Gold_NearArb(t *testing.T) {
	arb := ArbitrageResult{HasArbitrage: false, ArbitrageGap: -0.01}
	assert.Equal(t, CategoryGold, Categorize(0.05, arb, 0.01))
}

func TestCategorize_Silver(t *testing.T) {
	arb := ArbitrageResult{HasArbitrage: false, ArbitrageGap: -0.03}
	assert.Equal(t, CategorySilver, Categorize(0.05, arb, 0.01))
}

func TestCategorize_Bronze(t *testing.T) {
	arb := ArbitrageResult{HasArbitrage: false, ArbitrageGap: -0.06}
	assert.Equal(t, CategoryBronze, Categorize(0.05, arb, 0.01))
}

func TestCategorize_Avoid_LowReward(t *testing.T) {
	arb := ArbitrageResult{HasArbitrage: true, ArbitrageGap: 0.05}
	assert.Equal(t, CategoryAvoid, Categorize(0.001, arb, 0.01))
}

// --- ComputeCombinedScore ---

func TestComputeCombinedScore_TrueArb(t *testing.T) {
	arb := ArbitrageResult{HasArbitrage: true, ArbitrageGap: 0.02}
	combined := ComputeCombinedScore(1.0, arb, 100, 2.0)
	assert.InDelta(t, 5.0, combined, 0.001) // 1.0 + 0.02×100×2
}

func TestComputeCombinedScore_NoArb_EqualsReward(t *testing.T) {
	arb := ArbitrageResult{HasArbitrage: false, ArbitrageGap: -0.03}
	combined := ComputeCombinedScore(1.0, arb, 100, 2.0)
	assert.InDelta(t, 1.0, combined, 0.001) // sin true arb = solo reward
}
