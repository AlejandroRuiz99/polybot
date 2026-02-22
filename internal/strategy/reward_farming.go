package strategy

import (
	"context"
	"fmt"
	"time"

	"github.com/alejandrodnm/polybot/internal/domain"
)

const rewardFarmingName = "reward_farming"

// RewardFarming implementa la estrategia de liquidity reward farming.
// Analiza mercados buscando la mejor relación spread-bajo / reward-alto.
type RewardFarming struct {
	orderSize      float64
	feeRate        float64
	minNetProfit   float64
	maxCompetition float64
}

// RewardFarmingConfig configura la estrategia.
type RewardFarmingConfig struct {
	OrderSize      float64
	FeeRate        float64
	MinNetProfit   float64
	MaxCompetition float64
}

// NewRewardFarming crea la estrategia con la configuración dada.
func NewRewardFarming(cfg RewardFarmingConfig) *RewardFarming {
	return &RewardFarming{
		orderSize:      cfg.OrderSize,
		feeRate:        cfg.FeeRate,
		minNetProfit:   cfg.MinNetProfit,
		maxCompetition: cfg.MaxCompetition,
	}
}

// Name implementa Strategy.
func (s *RewardFarming) Name() string {
	return rewardFarmingName
}

// Analyze implementa Strategy. Calcula métricas de reward farming para un mercado.
func (s *RewardFarming) Analyze(_ context.Context, market domain.Market, yesBook, noBook domain.OrderBook) (domain.Opportunity, error) {
	if yesBook.BestAsk() == 0 || noBook.BestAsk() == 0 {
		return domain.Opportunity{}, fmt.Errorf("reward_farming: empty orderbook for %s", market.ConditionID)
	}

	spreadTotal := domain.SpreadTotal(yesBook.BestAsk(), noBook.BestAsk())
	qualifies := spreadTotal <= market.Rewards.MaxSpread && market.Rewards.MaxSpread > 0

	score := domain.RewardScore(s.orderSize, spreadTotal, market.Rewards.DailyRate)
	netProfit := domain.EstimateNetProfit(score, s.orderSize, s.feeRate)

	competition := yesBook.DepthWithin(market.Rewards.MaxSpread) +
		noBook.DepthWithin(market.Rewards.MaxSpread)

	return domain.Opportunity{
		Market:          market,
		YesBook:         yesBook,
		NoBook:          noBook,
		ScannedAt:       time.Now(),
		SpreadTotal:     spreadTotal,
		RewardScore:     score,
		Competition:     competition,
		NetProfitEst:    netProfit,
		QualifiesReward: qualifies,
	}, nil
}

// Filter implementa Strategy. Descarta oportunidades que no valen la pena.
func (s *RewardFarming) Filter(opp domain.Opportunity) bool {
	if !opp.QualifiesReward {
		return false
	}
	if opp.NetProfitEst < s.minNetProfit {
		return false
	}
	if s.maxCompetition > 0 && opp.Competition > s.maxCompetition {
		return false
	}
	return true
}
