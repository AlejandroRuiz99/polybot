package polymarket

import (
	"sort"
	"strconv"
	"time"

	"github.com/alejandrodnm/polybot/internal/domain"
)

// mapSamplingMarkets convierte los DTOs del CLOB a domain.Market.
func mapSamplingMarkets(raw []samplingMarket) []domain.Market {
	markets := make([]domain.Market, 0, len(raw))
	for _, r := range raw {
		markets = append(markets, mapSamplingMarket(r))
	}
	return markets
}

// mapSamplingMarket convierte un samplingMarket DTO a domain.Market.
func mapSamplingMarket(r samplingMarket) domain.Market {
	m := domain.Market{
		ConditionID:  r.ConditionID,
		QuestionID:   r.QuestionID,
		MakerBaseFee: r.MakerBaseFee,
		Active:       r.Active,
		Closed:       r.Closed,
		Rewards: domain.RewardConfig{
			MinSize:   r.Rewards.MinSize,
			MaxSpread: r.Rewards.MaxSpread,
		},
	}

	for _, rate := range r.Rewards.Rates {
		m.Rewards.DailyRate += rate.RewardsDailyRate
	}

	for i, t := range r.Tokens {
		if i >= 2 {
			break
		}
		m.Tokens[i] = domain.Token{
			TokenID: t.TokenID,
			Outcome: t.Outcome,
			Price:   t.Price,
		}
	}

	return m
}

// enrichFromGamma aplica la metadata de Gamma sobre un mercado existente.
func enrichFromGamma(m *domain.Market, gm gammaMarket) {
	m.Question = gm.Question
	m.Slug = gm.Slug

	if v, err := gm.Volume24h.Float64(); err == nil {
		m.Volume24h = v
	}

	if fee, err := gm.MakerBaseFee.Float64(); err == nil && fee > 0 && m.MakerBaseFee == 0 {
		m.MakerBaseFee = fee
	}

	if gm.EndDateISO != "" {
		// Polymarket usa varios formatos; intentamos los más comunes
		for _, layout := range []string{
			time.RFC3339,
			"2006-01-02T15:04:05.000Z",
			"2006-01-02T15:04:05Z",
			"2006-01-02",
		} {
			if t, err := time.Parse(layout, gm.EndDateISO); err == nil {
				m.EndDate = t.UTC()
				break
			}
		}
	}
}

// mapOrderBooks convierte la respuesta batch de /books a un map tokenID→OrderBook.
func mapOrderBooks(raw []orderBookResponse) map[string]domain.OrderBook {
	result := make(map[string]domain.OrderBook, len(raw))
	for _, r := range raw {
		ob := domain.OrderBook{
			TokenID: r.AssetID,
			Bids:    mapBookEntries(r.Bids, false),
			Asks:    mapBookEntries(r.Asks, true),
		}
		result[r.AssetID] = ob
	}
	return result
}

// mapBookEntries convierte entries raw a domain.BookEntry y los ordena.
// ascending=true → menor a mayor (asks), ascending=false → mayor a menor (bids).
func mapBookEntries(raw []bookEntryRaw, ascending bool) []domain.BookEntry {
	entries := make([]domain.BookEntry, 0, len(raw))
	for _, r := range raw {
		price, _ := strconv.ParseFloat(r.Price, 64)
		size, _ := strconv.ParseFloat(r.Size, 64)
		if price <= 0 || size <= 0 {
			continue
		}
		entries = append(entries, domain.BookEntry{Price: price, Size: size})
	}

	sort.Slice(entries, func(i, j int) bool {
		if ascending {
			return entries[i].Price < entries[j].Price
		}
		return entries[i].Price > entries[j].Price
	})

	return entries
}
