package domain

import "time"

// Opportunity es el resultado del análisis de un mercado.
type Opportunity struct {
	Market    Market
	YesBook   OrderBook
	NoBook    OrderBook
	ScannedAt time.Time

	// --- Spread y calificación ---
	SpreadTotal     float64
	QualifiesReward bool

	// --- Análisis de arbitraje ---
	Arbitrage ArbitrageResult

	// --- Tu reward puro (sin costes) ---
	Competition     float64 // USDC dentro del max_spread (ambos tokens)
	YourShare       float64 // orderSize / (orderSize + competition)
	SpreadScore     float64 // ((maxSpread - spread) / maxSpread)²
	YourDailyReward float64 // reward bruto diario estimado para ti

	// --- Costes reales de fill ---
	FillCostPerPair float64 // coste por share pair: (yesP + noP)(1+fee) - 1.0
	FillCostUSDC    float64 // coste en $ por evento de fill
	BreakEvenFills  float64 // fills/día antes de perder dinero (∞ = fills gratis)

	// --- P&L bajo escenarios ---
	PnLNoFills  float64 // reward puro, 0 fills (mejor caso)
	PnL1Fill    float64 // reward - 1 fill/día (conservador)
	PnL3Fills   float64 // reward - 3 fills/día (activo)

	// --- Score y categoría ---
	CombinedScore float64             // = PnL1Fill (escenario conservador como ranking)
	Category      OpportunityCategory // Gold / Silver / Bronze / Avoid

	// --- Legacy ---
	RewardScore  float64
	NetProfitEst float64 // deprecated, usar PnL escenarios
}

// IsArbitrage devuelve true si hay arbitraje neto rentable (tras fees).
func (o Opportunity) IsArbitrage() bool {
	return o.Arbitrage.HasArbitrage
}

// YesMidpoint devuelve el midpoint del token YES.
func (o Opportunity) YesMidpoint() float64 {
	return o.YesBook.Midpoint()
}

// NoMidpoint devuelve el midpoint del token NO.
func (o Opportunity) NoMidpoint() float64 {
	return o.NoBook.Midpoint()
}

// APR devuelve el APR estimado basado en CombinedScore (PnL 1fill/day) y orderSize.
func (o Opportunity) APR(orderSize float64) float64 {
	if orderSize <= 0 || o.CombinedScore <= 0 {
		return 0
	}
	capital := orderSize * 2
	return (o.CombinedScore / capital) * 365 * 100
}

// Verdict devuelve una descripción honesta de la rentabilidad.
func (o Opportunity) Verdict() string {
	if o.FillCostPerPair <= 0 {
		return "FILLS=PROFIT" // true arb
	}
	if o.BreakEvenFills > 10 {
		return "SAFE"
	}
	if o.BreakEvenFills > 3 {
		return "OK"
	}
	if o.BreakEvenFills > 1 {
		return "RISKY"
	}
	return "AVOID"
}
