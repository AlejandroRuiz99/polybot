package domain

import "math"

// RewardScore calcula el score del pool total (fórmula original, no ajustada por competencia).
// Útil como referencia del tamaño del premio, NO como estimación de tu ganancia real.
//
// Fórmula: S = ((v - s) / v)² × b
//   - v: tamaño de órdenes hipotéticas en USDC
//   - s: spread total del mercado
//   - b: reward rate diario total del mercado (USDC/día)
func RewardScore(orderSize, spread, rewardRate float64) float64 {
	if orderSize <= 0 || rewardRate <= 0 || spread >= orderSize {
		return 0
	}
	ratio := (orderSize - spread) / orderSize
	return ratio * ratio * rewardRate
}

// EstimateYourDailyReward calcula tu ganancia diaria real estimada.
// Incorpora competencia (tu cuota del pool) y la posición relativa en el spread.
//
// Fórmula:
//
//	yourShare    = orderSize / (orderSize + competition)
//	spreadScore  = ((maxSpread - spreadTotal) / maxSpread)²
//	yourReward   = dailyRate × yourShare × spreadScore
//
// Devuelve 0 si los inputs son inválidos.
func EstimateYourDailyReward(orderSize, competition, dailyRate, spreadTotal, maxSpread float64) float64 {
	if orderSize <= 0 || dailyRate <= 0 || maxSpread <= 0 {
		return 0
	}
	if spreadTotal >= maxSpread {
		return 0
	}

	denom := orderSize + competition
	if denom <= 0 {
		denom = orderSize
	}
	yourShare := orderSize / denom

	spreadScore := math.Pow((maxSpread-spreadTotal)/maxSpread, 2)
	return dailyRate * yourShare * spreadScore
}

// ComputeSpreadScore devuelve el factor cuadrático de posición en el spread.
// Resultado entre 0 (spread = maxSpread) y 1 (spread = 0).
func ComputeSpreadScore(spreadTotal, maxSpread float64) float64 {
	if maxSpread <= 0 || spreadTotal >= maxSpread {
		return 0
	}
	return math.Pow((maxSpread-spreadTotal)/maxSpread, 2)
}

// SpreadTotal calcula el spread total de un mercado binario.
// Valor negativo = hay arbitraje implícito.
func SpreadTotal(yesAsk, noAsk float64) float64 {
	return yesAsk + noAsk - 1.0
}

// EstimateNetProfit calcula la ganancia neta diaria estimada.
//   - yourDailyReward: resultado de EstimateYourDailyReward()
//   - orderSize: tamaño de tus órdenes en USDC (por lado)
//   - feeRate: tasa de fee del mercado
func EstimateNetProfit(yourDailyReward, orderSize, feeRate float64) float64 {
	feeCost := orderSize * feeRate * 2 // entrada y salida (ambos lados)
	return yourDailyReward - feeCost
}

// EstimateArbitrageGap calcula el gap de arbitraje neto después de fees.
// Positivo = hay arbitraje rentable.
func EstimateArbitrageGap(yesAsk, noAsk, feeRate float64) float64 {
	yesNoSum := yesAsk + noAsk
	fees := (yesAsk + noAsk) * feeRate
	return 1.0 - yesNoSum - fees
}
