package scanner

import (
	"context"
	"fmt"
	"time"

	"github.com/alejandrodnm/polybot/internal/domain"
)

const (
	defaultFeeRate   = 0.02
	defaultOrderSize = 100.0
)

// Analyzer calcula las métricas de cada mercado y produce una Opportunity.
type Analyzer struct {
	orderSize      float64
	defaultFeeRate float64
	fillsPerDay    float64
	goldMinReward  float64
}

// NewAnalyzer crea un Analyzer con los parámetros dados.
func NewAnalyzer(orderSize, feeRate, fillsPerDay, goldMinReward float64) *Analyzer {
	if orderSize <= 0 {
		orderSize = defaultOrderSize
	}
	if feeRate <= 0 {
		feeRate = defaultFeeRate
	}
	if fillsPerDay <= 0 {
		fillsPerDay = 1.0
	}
	if goldMinReward <= 0 {
		goldMinReward = 0.01
	}
	return &Analyzer{
		orderSize:      orderSize,
		defaultFeeRate: feeRate,
		fillsPerDay:    fillsPerDay,
		goldMinReward:  goldMinReward,
	}
}

// Analyze calcula todas las métricas para un mercado dado sus orderbooks YES y NO.
func (a *Analyzer) Analyze(_ context.Context, market domain.Market, yesBook, noBook domain.OrderBook) (domain.Opportunity, error) {
	if yesBook.BestAsk() == 0 || noBook.BestAsk() == 0 {
		return domain.Opportunity{}, fmt.Errorf("analyzer: empty book for market %s", market.ConditionID)
	}

	feeRate := market.EffectiveFeeRate(a.defaultFeeRate)

	spreadTotal := domain.SpreadTotal(yesBook.BestAsk(), noBook.BestAsk())
	qualifies := spreadTotal <= market.Rewards.MaxSpread && market.Rewards.MaxSpread > 0

	// Análisis de arbitraje
	arb := domain.CalculateArbitrage(yesBook, noBook, feeRate)

	// Competition en USDC
	competition := yesBook.DepthWithinUSDC(market.Rewards.MaxSpread) +
		noBook.DepthWithinUSDC(market.Rewards.MaxSpread)

	// Tu reward diario bruto (sin descontar nada)
	yourDailyReward := domain.EstimateYourDailyReward(
		a.orderSize, competition,
		market.Rewards.DailyRate,
		spreadTotal, market.Rewards.MaxSpread,
	)

	// Share y spread score para display
	yourShare := 0.0
	if competition > 0 {
		yourShare = a.orderSize / (a.orderSize + competition)
	}
	spreadScore := domain.ComputeSpreadScore(spreadTotal, market.Rewards.MaxSpread)

	// --- Coste real de fills ---
	// Como maker: tus BIDs están en el book. Si te llenan ambos lados:
	// pagaste yesPrice + noPrice, recibirás $1.00 cuando se resuelva.
	// Usamos BestBid como proxy de tu precio maker (compras más barato que el ask).
	yesBid := yesBook.BestBid()
	noBid := noBook.BestBid()
	if yesBid == 0 {
		yesBid = yesBook.BestAsk()
	}
	if noBid == 0 {
		noBid = noBook.BestAsk()
	}

	fillCostPair := domain.FillCostPerEvent(yesBid, noBid, feeRate)
	fillCostUSD := domain.FillCostUSDC(a.orderSize, yesBid, noBid, fillCostPair)
	breakEven := domain.BreakEvenFills(yourDailyReward, fillCostUSD)

	// P&L bajo escenarios
	pnl0 := yourDailyReward                                                 // 0 fills (solo reward)
	pnl1 := domain.EstimateNetProfit(yourDailyReward, fillCostUSD, 1.0)     // 1 fill/día
	pnl3 := domain.EstimateNetProfit(yourDailyReward, fillCostUSD, 3.0)     // 3 fills/día

	// CombinedScore = P&L con 1 fill/día (escenario conservador realista)
	combined := pnl1

	// Categoría basada en gap de fill
	category := domain.Categorize(yourDailyReward, arb, a.goldMinReward)

	legacyScore := domain.RewardScore(a.orderSize, spreadTotal, market.Rewards.DailyRate)

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
		NetProfitEst:    pnl1, // legacy compat
		RewardScore:     legacyScore,
	}, nil
}
