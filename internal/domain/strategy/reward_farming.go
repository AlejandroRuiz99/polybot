package strategy

import (
	"context"
	"fmt"
	"time"

	"github.com/alejandrodnm/polybot/internal/domain"
)

// RewardFarming implementa la estrategia de liquidity reward farming.
// Analiza mercados buscando la mejor relación spread-bajo / reward-alto.
type RewardFarming struct {
	orderSize     float64
	feeRate       float64
	fillsPerDay   float64
	goldMinReward float64
}

// RewardFarmingConfig configura la estrategia.
type RewardFarmingConfig struct {
	OrderSize     float64
	FeeRate       float64
	FillsPerDay   float64
	GoldMinReward float64
}

// NewRewardFarming crea la estrategia con la configuración dada.
func NewRewardFarming(cfg RewardFarmingConfig) *RewardFarming {
	if cfg.FillsPerDay <= 0 {
		cfg.FillsPerDay = 1.0
	}
	if cfg.GoldMinReward <= 0 {
		cfg.GoldMinReward = 0.01
	}
	return &RewardFarming{
		orderSize:     cfg.OrderSize,
		feeRate:       cfg.FeeRate,
		fillsPerDay:   cfg.FillsPerDay,
		goldMinReward: cfg.GoldMinReward,
	}
}

// Analyze implementa Strategy con el análisis completo de reward farming.
func (s *RewardFarming) Analyze(_ context.Context, market domain.Market, yesBook, noBook domain.OrderBook) (domain.Opportunity, error) {
	if yesBook.BestAsk() == 0 || noBook.BestAsk() == 0 {
		return domain.Opportunity{}, fmt.Errorf("reward_farming: empty orderbook for %s", market.ConditionID)
	}

	feeRate := market.EffectiveFeeRate(s.feeRate)

	spreadTotal := domain.SpreadTotal(yesBook.BestAsk(), noBook.BestAsk())
	qualifies := spreadTotal <= market.Rewards.MaxSpread && market.Rewards.MaxSpread > 0

	arb := domain.CalculateArbitrage(yesBook, noBook, feeRate)

	competition := yesBook.DepthWithinUSDC(market.Rewards.MaxSpread) +
		noBook.DepthWithinUSDC(market.Rewards.MaxSpread)

	yourDailyReward := domain.EstimateYourDailyReward(
		s.orderSize, competition,
		market.Rewards.DailyRate,
		spreadTotal, market.Rewards.MaxSpread,
	)

	yourShare := 0.0
	if competition > 0 {
		yourShare = s.orderSize / (s.orderSize + competition)
	}
	spreadScore := domain.ComputeSpreadScore(spreadTotal, market.Rewards.MaxSpread)

	yesBid := yesBook.BestBid()
	noBid := noBook.BestBid()
	if yesBid == 0 {
		yesBid = yesBook.BestAsk()
	}
	if noBid == 0 {
		noBid = noBook.BestAsk()
	}

	fillCostPair := domain.FillCostPerEvent(yesBid, noBid, feeRate)
	fillCostUSD := domain.FillCostUSDC(s.orderSize, yesBid, noBid, fillCostPair)
	breakEven := domain.BreakEvenFills(yourDailyReward, fillCostUSD)

	pnl0 := yourDailyReward
	pnl1 := domain.EstimateNetProfit(yourDailyReward, fillCostUSD, 1.0)
	pnl3 := domain.EstimateNetProfit(yourDailyReward, fillCostUSD, 3.0)

	combined := pnl1
	category := domain.Categorize(yourDailyReward, arb, s.goldMinReward)
	legacyScore := domain.RewardScore(s.orderSize, spreadTotal, market.Rewards.DailyRate)

	return domain.Opportunity{
		Market:          market,
		YesBook:         yesBook,
		NoBook:          noBook,
		ScannedAt:       time.Now(),
		SpreadTotal:     spreadTotal,
		QualifiesReward: qualifies,
		Arbitrage:       arb,
		Competition:     competition,
		YourShare:       yourShare,
		SpreadScore:     spreadScore,
		YourDailyReward: yourDailyReward,
		FillCostPerPair: fillCostPair,
		FillCostUSDC:    fillCostUSD,
		BreakEvenFills:  breakEven,
		PnLNoFills:      pnl0,
		PnL1Fill:        pnl1,
		PnL3Fills:       pnl3,
		CombinedScore:   combined,
		Category:        category,
		NetProfitEst:    pnl1,
		RewardScore:     legacyScore,
	}, nil
}
