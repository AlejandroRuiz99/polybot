package domain

import "time"

// Opportunity es el resultado del análisis de un mercado.
// Contiene todas las métricas calculadas para decidir si vale la pena proveer liquidez.
type Opportunity struct {
	Market    Market
	YesBook   OrderBook
	NoBook    OrderBook
	ScannedAt time.Time

	// --- Spread y calificación ---
	SpreadTotal     float64 // best_ask_YES + best_ask_NO - 1.0
	QualifiesReward bool    // spread_total <= max_spread

	// --- Arbitraje (C3) ---
	YesAsk      float64 // mejor ask del token YES
	NoAsk       float64 // mejor ask del token NO
	YesNoSum    float64 // YesAsk + NoAsk (< 1.0 = hay arbitraje antes de fees)
	ArbitrageGap float64 // 1.0 - YesNoSum - fees (> 0 = arbitraje neto rentable)
	HasArbitrage bool    // ArbitrageGap > 0

	// --- Tu ganancia real (C1, C6) ---
	Competition     float64 // USDC dentro del max_spread (ambos tokens)
	YourShare       float64 // tu fracción estimada del pool (orderSize / competition)
	SpreadScore     float64 // ((maxSpread - spread) / maxSpread)²
	YourDailyReward float64 // reward diario estimado para ti (USDC)
	NetProfitEst    float64 // YourDailyReward - fees

	// --- Score heredado (para tests legacy) ---
	RewardScore float64 // S = ((v-s)/v)² × b (pool total, no ajustado por competencia)
}

// IsArbitrage devuelve true si el spread total es negativo (arbitraje implícito).
func (o Opportunity) IsArbitrage() bool {
	return o.SpreadTotal < 0
}

// YesMidpoint devuelve el midpoint del token YES.
func (o Opportunity) YesMidpoint() float64 {
	return o.YesBook.Midpoint()
}

// NoMidpoint devuelve el midpoint del token NO.
func (o Opportunity) NoMidpoint() float64 {
	return o.NoBook.Midpoint()
}

// APR devuelve el APR estimado basado en YourDailyReward y orderSize.
func (o Opportunity) APR(orderSize float64) float64 {
	if orderSize <= 0 {
		return 0
	}
	// Invertimos en 2 lados (YES + NO), capital total = 2 * orderSize
	capital := orderSize * 2
	return (o.YourDailyReward / capital) * 365 * 100
}
