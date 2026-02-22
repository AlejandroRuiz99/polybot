package domain

import "math"

// RewardScore calcula el score del pool total (fórmula original, no ajustada por competencia).
//
// Fórmula: S = ((v - s) / v)² × b
func RewardScore(orderSize, spread, rewardRate float64) float64 {
	if orderSize <= 0 || rewardRate <= 0 || spread >= orderSize {
		return 0
	}
	ratio := (orderSize - spread) / orderSize
	return ratio * ratio * rewardRate
}

// EstimateYourDailyReward calcula tu ganancia diaria estimada del pool de rewards.
//
//	yourShare    = orderSize / (orderSize + competition)
//	spreadScore  = ((maxSpread - spreadTotal) / maxSpread)²
//	yourReward   = dailyRate × yourShare × spreadScore
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
func ComputeSpreadScore(spreadTotal, maxSpread float64) float64 {
	if maxSpread <= 0 || spreadTotal >= maxSpread {
		return 0
	}
	return math.Pow((maxSpread-spreadTotal)/maxSpread, 2)
}

// SpreadTotal calcula el spread total de un mercado binario.
func SpreadTotal(yesAsk, noAsk float64) float64 {
	return yesAsk + noAsk - 1.0
}

// EstimateArbitrageGap calcula el gap de arbitraje neto después de fees.
func EstimateArbitrageGap(yesAsk, noAsk, feeRate float64) float64 {
	yesNoSum := yesAsk + noAsk
	fees := yesNoSum * feeRate
	return 1.0 - yesNoSum - fees
}

// --- Métricas honestas de rentabilidad ---

// FillCostPerEvent calcula cuánto pierdes (o ganas) CADA VEZ que se llenan tus órdenes
// en ambos lados (YES + NO).
//
// En reward farming, tus órdenes están en el book como maker.
// Si alguien compra tu YES y alguien compra tu NO, has vendido ambos y debes cubrir:
//   - Compraste los tokens? No — vendiste tokens que no tenías aún.
//
// El escenario real de pérdida:
// Pones BID YES a yesPrice y BID NO a noPrice (maker).
// Si ambos fills ocurren: pagaste yesPrice + noPrice por 1 share de cada lado.
// El payout es $1.00 cuando el mercado se resuelve (uno paga $1, otro $0).
//
// Fill cost = (yesPrice + noPrice + fees) - 1.00
//
// Si positivo: pierdes esa cantidad por fill pair.
// Si negativo: GANAS esa cantidad por fill pair (true arb).
//
// Retorno: cost per share pair (positivo = coste, negativo = ganancia)
func FillCostPerEvent(yesPrice, noPrice, feeRate float64) float64 {
	return (yesPrice + noPrice) * (1 + feeRate) - 1.0
}

// FillCostUSDC calcula el coste/ganancia en USDC por evento de fill completo.
//
//	orderSize: tu orden en USDC por lado ($100)
//	yesPrice: tu bid price en YES
//	noPrice: tu bid price en NO
//	costPerPair: resultado de FillCostPerEvent
//
// En un fill completo compras shares de ambos lados.
// pairs = min(orderSize/yesPrice, orderSize/noPrice)
// total = pairs × costPerPair
//
// Se limita a 2× orderSize para evitar números absurdos en precios extremos.
func FillCostUSDC(orderSize, yesPrice, noPrice, costPerPair float64) float64 {
	if yesPrice <= 0.01 || noPrice <= 0.01 {
		return 0 // precios extremos (1c) → datos no fiables
	}
	sharesYes := orderSize / yesPrice
	sharesNo := orderSize / noPrice
	pairs := math.Min(sharesYes, sharesNo)
	result := pairs * costPerPair
	// Cap: el peor caso realista es perder 2× tu capital
	cap := orderSize * 2
	if result > cap {
		return cap
	}
	if result < -cap {
		return -cap
	}
	return result
}

// BreakEvenFills calcula cuántos fills por día puedes absorber antes de perder dinero.
//
//	dailyReward: tu reward diario estimado
//	fillCostUSDC: coste en USDC por evento de fill (positivo)
//
// Si fillCost ≤ 0: infinito (cada fill es ganancia → no hay break even)
// Si fillCost > 0: dailyReward / fillCost = fills/día antes de perder dinero
func BreakEvenFills(dailyReward, fillCostUSDC float64) float64 {
	if fillCostUSDC <= 0 {
		return math.Inf(1) // fills son gratis o ganancia
	}
	if dailyReward <= 0 {
		return 0
	}
	return dailyReward / fillCostUSDC
}

// EstimateNetProfit calcula el P&L neto diario bajo un escenario de fills dado.
//
// Polymarket makers NO pagan fees (0% maker fee) en la mayoría de mercados.
// El coste real viene SOLO de los fills adversos (si yesPrice + noPrice > $1.00).
//
//	reward: tu reward diario
//	fillCostUSDC: coste por evento de fill (puede ser negativo si hay arb)
//	fillsPerDay: estimación de fills/día (0 = solo reward, sin fills)
func EstimateNetProfit(reward, fillCostUSDC, fillsPerDay float64) float64 {
	return reward - (fillCostUSDC * fillsPerDay)
}
