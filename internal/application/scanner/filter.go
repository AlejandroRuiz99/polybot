package scanner

import (
	"github.com/alejandrodnm/polybot/internal/domain"
)

// FilterConfig contiene los parámetros configurables de filtrado.
type FilterConfig struct {
	// MinYourDailyReward descarta oportunidades donde tu ganancia diaria es menor a esto (C1, C4).
	MinYourDailyReward float64
	// MinRewardScore descarta oportunidades cuyo score de pool total es menor (legacy).
	MinRewardScore float64
	// MaxSpreadTotal descarta mercados cuyo spread supera este valor.
	MaxSpreadTotal float64
	// MaxCompetition descarta mercados con demasiados LPs compitiendo (USDC en book).
	MaxCompetition float64
	// RequireQualifies si true, solo incluye mercados que califican para el reward.
	RequireQualifies bool
	// MinHoursToResolution descarta mercados que se resuelven antes de X horas (C5).
	MinHoursToResolution float64
	// OnlyFillsProfit si true, descarta mercados donde un fill te cuesta dinero (FillCostUSDC > 0).
	OnlyFillsProfit bool
}

// DefaultFilterConfig devuelve una configuración de filtrado conservadora.
func DefaultFilterConfig() FilterConfig {
	return FilterConfig{
		MinYourDailyReward:   0.0,
		MinRewardScore:       0.0,
		MaxSpreadTotal:       0.10,
		MaxCompetition:       50_000,
		RequireQualifies:     true,
		MinHoursToResolution: 0,
		OnlyFillsProfit:      true, // solo mercados donde los fills son gratis o te dan dinero
	}
}

// Filter aplica los filtros configurados sobre una lista de oportunidades.
type Filter struct {
	cfg FilterConfig
}

// NewFilter crea un Filter con la configuración dada.
func NewFilter(cfg FilterConfig) *Filter {
	return &Filter{cfg: cfg}
}

// Apply devuelve las oportunidades que pasan todos los filtros.
func (f *Filter) Apply(opps []domain.Opportunity) []domain.Opportunity {
	result := make([]domain.Opportunity, 0, len(opps))
	for _, opp := range opps {
		if f.passes(opp) {
			result = append(result, opp)
		}
	}
	return result
}

// passes devuelve true si la oportunidad supera todos los criterios.
func (f *Filter) passes(opp domain.Opportunity) bool {
	if f.cfg.RequireQualifies && !opp.QualifiesReward {
		return false
	}
	if f.cfg.MinYourDailyReward > 0 && opp.YourDailyReward < f.cfg.MinYourDailyReward {
		return false
	}
	if f.cfg.MinRewardScore > 0 && opp.RewardScore < f.cfg.MinRewardScore {
		return false
	}
	if f.cfg.MaxSpreadTotal > 0 && opp.SpreadTotal > f.cfg.MaxSpreadTotal {
		return false
	}
	if f.cfg.MaxCompetition > 0 && opp.Competition > f.cfg.MaxCompetition {
		return false
	}
	// C5: filtrar mercados que se resuelven pronto
	if f.cfg.MinHoursToResolution > 0 {
		hours := opp.Market.HoursToResolution()
		if hours > 0 && hours < f.cfg.MinHoursToResolution {
			return false
		}
	}
	// Descartar mercados tóxicos: fill cost > 0 significa que cada fill te cuesta dinero
	if f.cfg.OnlyFillsProfit && opp.FillCostUSDC > 0 {
		return false
	}
	return true
}
