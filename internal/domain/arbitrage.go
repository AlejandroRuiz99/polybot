package domain

import "math"

// OpportunityCategory clasifica la oportunidad según la combinación reward + arbitraje.
type OpportunityCategory int

const (
	CategoryGold   OpportunityCategory = iota // 🥇 Arbitraje + Rewards — el santo grial
	CategorySilver                             // 🥈 Solo rewards, fills seguros
	CategoryBronze                             // 🥉 Rewards pero fills arriesgados
	CategoryAvoid                              // ❌ Sin ventaja clara
)

func (c OpportunityCategory) String() string {
	switch c {
	case CategoryGold:
		return "GOLD"
	case CategorySilver:
		return "SILV"
	case CategoryBronze:
		return "BRNZ"
	default:
		return "SKIP"
	}
}

func (c OpportunityCategory) Icon() string {
	switch c {
	case CategoryGold:
		return "[G]"
	case CategorySilver:
		return "[S]"
	case CategoryBronze:
		return "[B]"
	default:
		return "[ ]"
	}
}

// DepthLevel analiza el arbitraje a una profundidad de capital específica.
type DepthLevel struct {
	DepthUSDC    float64 // capital analizado en USDC ($50, $100, $200, $500)
	AvgPriceYES  float64 // precio medio ponderado de YES a esta profundidad
	AvgPriceNO   float64 // precio medio ponderado de NO a esta profundidad
	Sum          float64 // AvgPriceYES + AvgPriceNO
	GapAfterFees float64 // 1.0 - Sum - fees (positivo = arbitraje rentable)
	Profitable   bool    // GapAfterFees > 0
}

// ArbitrageResult contiene el análisis completo de arbitraje para un mercado binario.
type ArbitrageResult struct {
	// Nivel superficial (best ask)
	BestAskYES  float64
	BestAskNO   float64
	DepthYES    float64 // USDC disponibles en el best ask YES
	DepthNO     float64 // USDC disponibles en el best ask NO
	MaxFillable float64 // min(DepthYES, DepthNO) — cuánto puedes arbitrar en 1 operación

	SumBestAsk   float64
	FeesTotal    float64
	ArbitrageGap float64 // 1.0 - SumBestAsk - FeesTotal (positivo = arbitraje neto)
	HasArbitrage bool    // ArbitrageGap > 0

	// Análisis a distintas profundidades del book
	AtDepth []DepthLevel
}

// ProfitableDepths devuelve los niveles de profundidad donde el arbitraje es rentable.
func (a ArbitrageResult) ProfitableDepths() []DepthLevel {
	var out []DepthLevel
	for _, d := range a.AtDepth {
		if d.Profitable {
			out = append(out, d)
		}
	}
	return out
}

// MaxProfitableDepth devuelve el mayor capital en USDC donde el arbitraje sigue siendo rentable.
func (a ArbitrageResult) MaxProfitableDepth() float64 {
	max := 0.0
	for _, d := range a.AtDepth {
		if d.Profitable {
			max = d.DepthUSDC
		}
	}
	return max
}

// --- Funciones de cálculo ---

// VolumeWeightedPrice calcula el precio medio ponderado por volumen
// para comprar hasta maxUSDC en USDC recorriendo los asks del book.
func VolumeWeightedPrice(asks []BookEntry, maxUSDC float64) float64 {
	if len(asks) == 0 || maxUSDC <= 0 {
		return 0
	}
	totalShares := 0.0
	totalCost := 0.0
	remaining := maxUSDC

	for _, ask := range asks {
		levelCost := ask.Size * ask.Price
		if levelCost <= remaining {
			totalShares += ask.Size
			totalCost += levelCost
			remaining -= levelCost
		} else {
			// Fill parcial de este nivel
			sharesToBuy := remaining / ask.Price
			totalShares += sharesToBuy
			totalCost += remaining
			break
		}
	}

	if totalShares == 0 {
		return 0
	}
	return totalCost / totalShares
}

// CalculateArbitrage analiza el arbitraje entre YES y NO para un mercado binario.
// Evalúa la superficie (best ask) y múltiples profundidades del book.
func CalculateArbitrage(yesBook, noBook OrderBook, feeRate float64) ArbitrageResult {
	result := ArbitrageResult{}

	if len(yesBook.Asks) == 0 || len(noBook.Asks) == 0 {
		return result
	}

	// Nivel superficial: best ask
	result.BestAskYES = yesBook.BestAsk()
	result.BestAskNO = noBook.BestAsk()
	result.DepthYES = yesBook.Asks[0].Size * yesBook.Asks[0].Price
	result.DepthNO = noBook.Asks[0].Size * noBook.Asks[0].Price
	result.MaxFillable = math.Min(result.DepthYES, result.DepthNO)

	result.SumBestAsk = result.BestAskYES + result.BestAskNO
	result.FeesTotal = result.SumBestAsk * feeRate
	result.ArbitrageGap = 1.0 - result.SumBestAsk - result.FeesTotal
	result.HasArbitrage = result.ArbitrageGap > 0

	// Análisis a distintas profundidades: $50, $100, $200, $500
	for _, depth := range []float64{50, 100, 200, 500} {
		avgYES := VolumeWeightedPrice(yesBook.Asks, depth)
		avgNO := VolumeWeightedPrice(noBook.Asks, depth)
		if avgYES == 0 || avgNO == 0 {
			break
		}
		sum := avgYES + avgNO
		fees := sum * feeRate
		gap := 1.0 - sum - fees

		result.AtDepth = append(result.AtDepth, DepthLevel{
			DepthUSDC:    depth,
			AvgPriceYES:  avgYES,
			AvgPriceNO:   avgNO,
			Sum:          sum,
			GapAfterFees: gap,
			Profitable:   gap > 0,
		})
	}

	return result
}

// Umbrales de clasificación por coste de fill:
//
//	Gold:   gap > -0.02  → fills cuestan < 2c por $1 (o son ganancia si gap > 0)
//	Silver: gap > -0.05  → fills cuestan 2-5c por $1 (aceptable con buenos rewards)
//	Bronze: gap ≤ -0.05  → fills costosos, solo vale si el reward compensa mucho
//
// El ArbitrageGap es la métrica clave: 1.0 - (YES ask + NO ask) - fees
// Si gap > 0: cada fill es una ganancia garantizada (true arbitrage — rarísimo)
// Si gap = -0.01: cada fill cuesta 1c por share pair → casi neutral
// Si gap = -0.05: cada fill cuesta 5c por share pair → caro
const (
	goldArbThreshold   = -0.02 // Gold: fills < 2% de coste
	silverArbThreshold = -0.05 // Silver: fills 2-5%
)

// ComputeCombinedScore calcula el score combinado para ranking.
//
// En reward farming, los fills son POCO FRECUENTES (el objetivo es tener órdenes en
// el book, no transaccionar). Por eso el score base ES el reward diario.
//
// Solo sumamos arb profit cuando hay TRUE arbitraje (gap > 0): cada fill es ganancia
// garantizada, por lo que sí conviene maximizarlos.
//
// Para gap < 0 (la mayoría), el riesgo de fill ya queda capturado en la categoría
// (Gold/Silver/Bronze). No penalizamos el score porque los fills son esporádicos.
func ComputeCombinedScore(yourDailyReward float64, arb ArbitrageResult, orderSize, fillsPerDay float64) float64 {
	if arb.HasArbitrage && arb.ArbitrageGap > 0 {
		// True arb: cada fill es ganancia → añadir al score
		return yourDailyReward + arb.ArbitrageGap*orderSize*fillsPerDay
	}
	return yourDailyReward
}

// Categorize clasifica una oportunidad por coste de fill y calidad del reward.
//
//	Gold   = reward suficiente + fills baratos (gap > -0.02)
//	Silver = reward suficiente + fills moderados (gap > -0.05)
//	Bronze = reward suficiente + fills caros
//	Avoid  = reward insuficiente
//
// Nota: el true arbitrage (gap > 0) automáticamente cae en Gold y activa alertas
// especiales, pero NO es el requisito mínimo para Gold.
func Categorize(yourDailyReward float64, arb ArbitrageResult, goldMinReward float64) OpportunityCategory {
	if yourDailyReward < goldMinReward {
		return CategoryAvoid
	}
	switch {
	case arb.ArbitrageGap > goldArbThreshold:
		return CategoryGold
	case arb.ArbitrageGap > silverArbThreshold:
		return CategorySilver
	default:
		return CategoryBronze
	}
}
