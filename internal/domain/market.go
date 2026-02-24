package domain

import "time"

// Market representa un mercado de predicción binario en Polymarket.
type Market struct {
	ConditionID string
	QuestionID  string
	Question    string    // enriquecido desde Gamma
	Slug        string    // enriquecido desde Gamma
	EndDate     time.Time // fecha de resolución, enriquecido desde Gamma
	Volume24h   float64   // volumen últimas 24h en USDC, enriquecido desde Gamma
	MakerBaseFee float64  // fee real del mercado (0 = usar default de config)
	Tokens      [2]Token
	Rewards     RewardConfig
	Active      bool
	Closed      bool
}

// Token es uno de los dos lados del mercado (YES/NO).
type Token struct {
	TokenID string
	Outcome string  // "Yes" | "No"
	Price   float64 // último precio mid del CLOB
}

// RewardConfig contiene la configuración de rewards del mercado.
type RewardConfig struct {
	// DailyRate es el total de USDC/día distribuidos entre los LPs.
	DailyRate float64
	// MinSize es el tamaño mínimo de orden para calificar al reward.
	MinSize float64
	// MaxSpread es el spread máximo (YES ask + NO ask - 1) para calificar.
	MaxSpread float64
}

// HasRewards devuelve true si el mercado tiene rewards activos configurados.
func (m Market) HasRewards() bool {
	return m.Rewards.DailyRate > 0 && m.Rewards.MaxSpread > 0
}

// HoursToResolution devuelve las horas hasta que el mercado se resuelve.
// Devuelve 0 si EndDate no está definido.
func (m Market) HoursToResolution() float64 {
	if m.EndDate.IsZero() {
		return 0
	}
	h := time.Until(m.EndDate).Hours()
	if h < 0 {
		return 0
	}
	return h
}

// EffectiveFeeRate devuelve el fee rate a usar: el del mercado si existe,
// o el defaultFeeRate si el mercado devuelve 0.
func (m Market) EffectiveFeeRate(defaultFeeRate float64) float64 {
	if m.MakerBaseFee > 0 {
		return m.MakerBaseFee
	}
	return defaultFeeRate
}

// YesToken devuelve el token YES del mercado.
func (m Market) YesToken() Token {
	for _, t := range m.Tokens {
		if t.Outcome == "Yes" {
			return t
		}
	}
	return m.Tokens[0]
}

// NoToken devuelve el token NO del mercado.
func (m Market) NoToken() Token {
	for _, t := range m.Tokens {
		if t.Outcome == "No" {
			return t
		}
	}
	return m.Tokens[1]
}

// TruncateQuestion devuelve la pregunta del mercado truncada a maxLen caracteres.
// Si la pregunta está vacía usa los primeros caracteres del conditionID como fallback.
func TruncateQuestion(question, conditionID string, maxLen int) string {
	q := question
	if q == "" {
		if len(conditionID) > 20 {
			q = conditionID[:20] + "..."
		} else {
			q = conditionID
		}
	}
	if len(q) > maxLen {
		q = q[:maxLen-3] + "..."
	}
	return q
}
