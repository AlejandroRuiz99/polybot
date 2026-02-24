package polymarket

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/alejandrodnm/polybot/internal/domain"
)

const (
	gammaMarketsPath  = "/markets"
	gammaConditionMax = 20
)

// EnrichWithGamma obtiene metadata de Gamma (question, slug, endDate, volume24h, fee)
// y la añade a los mercados. Los mercados sin datos en Gamma se devuelven sin enriquecer.
func (c *Client) EnrichWithGamma(ctx context.Context, markets []domain.Market) ([]domain.Market, error) {
	conditionIDs := make([]string, len(markets))
	for i, m := range markets {
		conditionIDs[i] = m.ConditionID
	}

	metadata, err := c.fetchGammaMetadata(ctx, conditionIDs)
	if err != nil {
		return nil, fmt.Errorf("gamma.EnrichWithGamma: %w", err)
	}

	enriched := 0
	for i, m := range markets {
		if gm, ok := metadata[m.ConditionID]; ok {
			enrichFromGamma(&markets[i], gm)
			enriched++
		}
	}

	slog.Debug("gamma enrichment complete",
		"markets", len(markets),
		"enriched", enriched,
	)
	return markets, nil
}

// fetchGammaMetadata obtiene la metadata de Gamma para los condition_ids dados.
func (c *Client) fetchGammaMetadata(ctx context.Context, conditionIDs []string) (map[string]gammaMarket, error) {
	result := make(map[string]gammaMarket, len(conditionIDs))

	for i := 0; i < len(conditionIDs); i += gammaConditionMax {
		end := i + gammaConditionMax
		if end > len(conditionIDs) {
			end = len(conditionIDs)
		}
		batch := conditionIDs[i:end]

		url := fmt.Sprintf("%s%s?condition_ids=%s&limit=%d",
			c.gammaBase,
			gammaMarketsPath,
			strings.Join(batch, ","),
			gammaConditionMax,
		)

		var resp gammaMarketsResponse
		if err := c.get(ctx, c.gammaLimiter, url, &resp); err != nil {
			slog.Debug("gamma batch failed, skipping",
				"batch", fmt.Sprintf("%d-%d", i, end),
				"err", err,
			)
			continue
		}

		for _, gm := range resp {
			result[gm.ConditionID] = gm
		}
	}

	return result, nil
}
