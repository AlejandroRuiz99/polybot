package polymarket

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"time"

	"golang.org/x/time/rate"
)

const (
	defaultCLOBBase  = "https://clob.polymarket.com"
	defaultGammaBase = "https://gamma-api.polymarket.com"

	// Rate limits al 60% de los límites reales documentados.
	// CLOB /books: 500/10s → 300/10s → 30/s
	booksRatePerSec = 30
	// Gamma /markets: 300/10s → 180/10s → 18/s
	gammaRatePerSec = 18
	// CLOB general (sampling-markets, etc.): 9000/10s → 5400/10s → 540/s
	generalRatePerSec = 540

	maxRetries    = 3
	baseRetryWait = 500 * time.Millisecond
)

// Client es el HTTP client de Polymarket con rate limiting y retries.
type Client struct {
	http         *http.Client
	clobBase     string
	gammaBase    string
	clobLimiter  *rate.Limiter
	gammaLimiter *rate.Limiter
	booksLimiter *rate.Limiter
}

// NewClient crea un Client con los base URLs dados.
// Si clobBase o gammaBase están vacíos, usa los URLs de producción.
func NewClient(clobBase, gammaBase string) *Client {
	if clobBase == "" {
		clobBase = defaultCLOBBase
	}
	if gammaBase == "" {
		gammaBase = defaultGammaBase
	}
	return &Client{
		http:         &http.Client{Timeout: 10 * time.Second},
		clobBase:     clobBase,
		gammaBase:    gammaBase,
		clobLimiter:  rate.NewLimiter(generalRatePerSec, 50),
		gammaLimiter: rate.NewLimiter(gammaRatePerSec, 10),
		booksLimiter: rate.NewLimiter(booksRatePerSec, 5),
	}
}

// get hace un GET con rate limiting y retries.
func (c *Client) get(ctx context.Context, limiter *rate.Limiter, url string, out any) error {
	return c.doWithRetry(ctx, limiter, func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		return c.http.Do(req)
	}, out)
}

// post hace un POST JSON con rate limiting y retries.
func (c *Client) post(ctx context.Context, limiter *rate.Limiter, url string, body, out any) error {
	return c.doWithRetry(ctx, limiter, func() (*http.Response, error) {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal body: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		return c.http.Do(req)
	}, out)
}

// doWithRetry ejecuta la función con backoff exponencial y jitter.
func (c *Client) doWithRetry(ctx context.Context, limiter *rate.Limiter, fn func() (*http.Response, error), out any) error {
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if err := limiter.Wait(ctx); err != nil {
			return fmt.Errorf("rate limiter: %w", err)
		}

		resp, err := fn()
		if err != nil {
			if attempt == maxRetries {
				return fmt.Errorf("request failed after %d retries: %w", maxRetries, err)
			}
			c.sleep(ctx, attempt)
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			slog.Warn("rate limited by API", "attempt", attempt+1)
			c.sleep(ctx, attempt)
			continue
		}

		if resp.StatusCode >= 500 {
			resp.Body.Close()
			if attempt == maxRetries {
				return fmt.Errorf("server error %d after %d retries", resp.StatusCode, maxRetries)
			}
			c.sleep(ctx, attempt)
			continue
		}

		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return fmt.Errorf("client error %d: %s", resp.StatusCode, string(body))
		}

		defer resp.Body.Close()
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		return nil
	}
	return fmt.Errorf("exhausted %d retries", maxRetries)
}

// sleep espera con backoff exponencial y jitter, respetando el contexto.
func (c *Client) sleep(ctx context.Context, attempt int) {
	wait := time.Duration(math.Pow(2, float64(attempt))) * baseRetryWait
	select {
	case <-time.After(wait):
	case <-ctx.Done():
	}
}
