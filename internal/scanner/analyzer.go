package scanner

import (
	"context"
	"fmt"
	"time"

	"github.com/alejandrodnm/polybot/internal/domain"
)

const (
	// defaultFeeRate conservador cuando el mercado no devuelve su fee real (C6).
	defaultFeeRate = 0.02 // 2%
	defaultOrderSize = 100.0
)

// Analyzer calcula las métricas de cada mercado y produce una Opportunity.
type Analyzer struct {
	orderSize       float64
	defaultFeeRate  float64
}

// NewAnalyzer crea un Analyzer con los parámetros dados.
func NewAnalyzer(orderSize, feeRate float64) *Analyzer {
	if orderSize <= 0 {
		orderSize = defaultOrderSize
	}
	if feeRate <= 0 {
		feeRate = defaultFeeRate
	}
	return &Analyzer{orderSize: orderSize, defaultFeeRate: feeRate}
}

// Analyze calcula todas las métricas para un mercado dado sus orderbooks YES y NO.
func (a *Analyzer) Analyze(_ context.Context, market domain.Market, yesBook, noBook domain.OrderBook) (domain.Opportunity, error) {
	if yesBook.BestAsk() == 0 || noBook.BestAsk() == 0 {
		return domain.Opportunity{}, fmt.Errorf("analyzer: empty book for market %s", market.ConditionID)
	}

	yesAsk := yesBook.BestAsk()
	noAsk := noBook.BestAsk()
	yesNoSum := yesAsk + noAsk
	spreadTotal := domain.SpreadTotal(yesAsk, noAsk)
	qualifies := spreadTotal <= market.Rewards.MaxSpread && market.Rewards.MaxSpread > 0

	// C6: usar fee real del mercado, o default conservador
	feeRate := market.EffectiveFeeRate(a.defaultFeeRate)

	// C3: arbitraje neto (gap positivo = oportunidad de arbitraje real tras fees)
	arbGap := domain.EstimateArbitrageGap(yesAsk, noAsk, feeRate)

	// Competition en USDC (C1)
	competition := yesBook.DepthWithinUSDC(market.Rewards.MaxSpread) +
		noBook.DepthWithinUSDC(market.Rewards.MaxSpread)

	// C1: tu ganancia real = cuota del pool × factor spread
	yourDailyReward := domain.EstimateYourDailyReward(
		a.orderSize, competition,
		market.Rewards.DailyRate,
		spreadTotal, market.Rewards.MaxSpread,
	)
	netProfit := domain.EstimateNetProfit(yourDailyReward, a.orderSize, feeRate)

	// YourShare y SpreadScore para mostrar en output
	yourShare := 0.0
	spreadScore := 0.0
	if competition > 0 {
		yourShare = a.orderSize / (a.orderSize + competition)
	}
	spreadScore = domain.ComputeSpreadScore(spreadTotal, market.Rewards.MaxSpread)

	// Score legacy (pool total, no ajustado)
	legacyScore := domain.RewardScore(a.orderSize, spreadTotal, market.Rewards.DailyRate)

	return domain.Opportunity{
		Market:          market,
		YesBook:         yesBook,
		NoBook:          noBook,
		ScannedAt:       time.Now(),
		SpreadTotal:     spreadTotal,
		QualifiesReward: qualifies,
		YesAsk:          yesAsk,
		NoAsk:           noAsk,
		YesNoSum:        yesNoSum,
		ArbitrageGap:    arbGap,
		HasArbitrage:    arbGap > 0,
		Competition:     competition,
		YourShare:       yourShare,
		SpreadScore:     spreadScore,
		YourDailyReward: yourDailyReward,
		NetProfitEst:    netProfit,
		RewardScore:     legacyScore,
	}, nil
}
