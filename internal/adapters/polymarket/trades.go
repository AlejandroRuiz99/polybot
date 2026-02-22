package polymarket

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/alejandrodnm/polybot/internal/domain"
)

const (
	dataAPIBase    = "https://data-api.polymarket.com"
	tradesPerPage  = 1000
	tradesMaxPages = 3
)

type rawDataTrade struct {
	ID          string      `json:"id"`
	ConditionID string      `json:"conditionId"`
	Asset       string      `json:"asset"`
	Side        string      `json:"side"`
	Price       json.Number `json:"price"`
	Size        json.Number `json:"size"`
	Timestamp   json.Number `json:"timestamp"`
	Status      string      `json:"status"`
}

// FetchTrades obtiene los trades recientes de un mercado usando la Data API pública.
// Usa condition_id en lugar de token_id para obtener trades de ambos lados.
func (c *Client) FetchTrades(ctx context.Context, tokenID string) ([]domain.Trade, error) {
	var all []domain.Trade

	for page := 0; page < tradesMaxPages; page++ {
		offset := page * tradesPerPage
		url := fmt.Sprintf("%s/trades?asset=%s&limit=%d&offset=%d",
			dataAPIBase, tokenID, tradesPerPage, offset)

		var resp []rawDataTrade
		if err := c.get(ctx, c.clobLimiter, url, &resp); err != nil {
			return nil, fmt.Errorf("data-api.FetchTrades: %w", err)
		}

		if len(resp) == 0 {
			break
		}

		for _, rt := range resp {
			price, _ := rt.Price.Float64()
			size, _ := rt.Size.Float64()
			ts := parseTradeTimestamp(rt.Timestamp)

			all = append(all, domain.Trade{
				ID:        rt.ID,
				TokenID:   rt.Asset,
				Side:      rt.Side,
				Price:     price,
				Size:      size,
				Timestamp: ts,
			})
		}

		slog.Debug("fetched trades page",
			"token", tokenID[:min(8, len(tokenID))]+"...",
			"page", page,
			"count", len(resp),
			"total", len(all),
		)

		if len(resp) < tradesPerPage {
			break
		}
	}

	return all, nil
}

// FetchTradesByCondition obtiene todos los trades de un mercado por condition_id.
func (c *Client) FetchTradesByCondition(ctx context.Context, conditionID string) ([]domain.Trade, error) {
	var all []domain.Trade

	for page := 0; page < tradesMaxPages; page++ {
		offset := page * tradesPerPage
		url := fmt.Sprintf("%s/trades?market=%s&limit=%d&offset=%d",
			dataAPIBase, conditionID, tradesPerPage, offset)

		var resp []rawDataTrade
		if err := c.get(ctx, c.clobLimiter, url, &resp); err != nil {
			return nil, fmt.Errorf("data-api.FetchTradesByCondition: %w", err)
		}

		if len(resp) == 0 {
			break
		}

		for _, rt := range resp {
			price, _ := rt.Price.Float64()
			size, _ := rt.Size.Float64()
			ts := parseTradeTimestamp(rt.Timestamp)

			all = append(all, domain.Trade{
				ID:        rt.ID,
				TokenID:   rt.Asset,
				Side:      rt.Side,
				Price:     price,
				Size:      size,
				Timestamp: ts,
			})
		}

		if len(resp) < tradesPerPage {
			break
		}
	}

	return all, nil
}

func parseTradeTimestamp(n json.Number) time.Time {
	s := n.String()
	// Try as unix timestamp (seconds or milliseconds)
	if sec, err := strconv.ParseInt(s, 10, 64); err == nil {
		if sec > 1e12 {
			return time.Unix(sec/1000, (sec%1000)*int64(time.Millisecond))
		}
		return time.Unix(sec, 0)
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		sec := int64(f)
		nsec := int64((f - float64(sec)) * 1e9)
		return time.Unix(sec, nsec)
	}
	// Try as ISO string
	for _, layout := range []string{
		time.RFC3339Nano, time.RFC3339,
		"2006-01-02T15:04:05.000Z", "2006-01-02T15:04:05Z",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
