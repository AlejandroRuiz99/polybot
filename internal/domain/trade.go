package domain

import "time"

// Trade representa un trade histórico de la API.
type Trade struct {
	ID        string
	TokenID   string
	Side      string  // "BUY" o "SELL"
	Price     float64
	Size      float64
	Timestamp time.Time
}

// BacktestResult es el resultado del backtesting de un mercado.
type BacktestResult struct {
	Market       Market
	Opportunity  Opportunity
	TokenYesID   string
	TokenNoID    string
	Period       time.Duration // ventana temporal analizada

	// Trades observados
	TotalTradesYes int
	TotalTradesNo  int

	// Simulación de fills a tu precio bid
	SimBidYes       float64 // precio al que habrías puesto tu bid YES
	SimBidNo        float64 // precio al que habrías puesto tu bid NO
	FillsYes        int     // trades que habrían llenado tu bid YES
	FillsNo         int     // trades que habrían llenado tu bid NO
	FillsBothPerDay float64 // min(fillsYes, fillsNo) / days — fills completos por día

	// P&L real con fill rate observado
	RealFillRate float64 // fills completos por día (observado)
	RealPnLDaily float64 // reward - (fillCost × realFillRate)

	// Veredicto
	Verdict string // PROFITABLE / MARGINAL / NOT_PROFITABLE
}
