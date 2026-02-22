package polymarket

import "encoding/json"

// DTOs raw de la API de Polymarket. Solo se usan dentro de este paquete.
// La conversión a domain entities se hace en mapping.go.

// --- CLOB API ---

// samplingMarketsResponse es la respuesta paginada de GET /sampling-markets.
type samplingMarketsResponse struct {
	Limit      int              `json:"limit"`
	Count      int              `json:"count"`
	NextCursor string           `json:"next_cursor"`
	Data       []samplingMarket `json:"data"`
}

// samplingMarket es un mercado con rewards activos del CLOB.
type samplingMarket struct {
	ConditionID  string      `json:"condition_id"`
	QuestionID   string      `json:"question_id"`
	Tokens       []clobToken `json:"tokens"`
	Rewards      clobRewards `json:"rewards"`
	MakerBaseFee float64     `json:"maker_base_fee"`
	TakerBaseFee float64     `json:"taker_base_fee"`
	Active       bool        `json:"active"`
	Closed       bool        `json:"closed"`
}

// clobToken representa un token (YES/NO) en el CLOB.
type clobToken struct {
	TokenID string  `json:"token_id"`
	Outcome string  `json:"outcome"`
	Price   float64 `json:"price"`
	Winner  bool    `json:"winner"`
}

// clobRewards contiene la configuración de rewards del mercado.
type clobRewards struct {
	Rates     []rewardRate `json:"rates"`
	MinSize   float64      `json:"min_size"`
	MaxSpread float64      `json:"max_spread"`
}

// rewardRate es la tasa de reward por asset.
type rewardRate struct {
	AssetAddress    string  `json:"asset_address"`
	RewardsDailyRate float64 `json:"rewards_daily_rate"`
}

// orderBookRequest es el body del POST /books batch.
type orderBookRequest struct {
	TokenID string `json:"token_id"`
}

// orderBookResponse es la respuesta de un item en POST /books.
type orderBookResponse struct {
	AssetID string          `json:"asset_id"`
	Bids    []bookEntryRaw  `json:"bids"`
	Asks    []bookEntryRaw  `json:"asks"`
}

// bookEntryRaw es un nivel de precio raw de la API (strings para mayor precisión).
type bookEntryRaw struct {
	Price string `json:"price"`
	Size  string `json:"size"`
}

// --- Gamma API ---

// gammaMarketsResponse es la respuesta de GET /markets de Gamma.
type gammaMarketsResponse []gammaMarket

// gammaMarket contiene la metadata enriquecida de un mercado.
// Gamma devuelve algunos campos numéricos como strings JSON, usamos json.Number.
type gammaMarket struct {
	ConditionID  string      `json:"conditionId"`
	Question     string      `json:"question"`
	Slug         string      `json:"slug"`
	EndDateISO   string      `json:"endDateIso"`
	Volume       json.Number `json:"volume"`
	Volume24h    json.Number `json:"volume24hr"`
	Liquidity    json.Number `json:"liquidity"`
	MakerBaseFee json.Number `json:"makerBaseFee"`
	Active       bool        `json:"active"`
	Closed       bool        `json:"closed"`
}
