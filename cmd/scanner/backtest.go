package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/alejandrodnm/polybot/internal/adapters/notify"
	"github.com/alejandrodnm/polybot/internal/adapters/polymarket"
	"github.com/alejandrodnm/polybot/internal/scanner"
)

func runBacktest(ctx context.Context, s *scanner.Scanner, client *polymarket.Client, notifier *notify.Console, orderSize float64) {
	slog.Info("=== BACKTEST MODE: scan + cross-reference with real trades ===")

	opps, err := s.RunOnce(ctx)
	if err != nil {
		slog.Error("scan failed", "err", err)
		os.Exit(1)
	}

	if len(opps) == 0 {
		slog.Warn("no opportunities found — nothing to backtest")
		return
	}

	if err := notifier.Notify(ctx, opps); err != nil {
		slog.Warn("notifier error", "err", err)
	}

	slog.Info("fetching real trades for top markets...", "count", min(10, len(opps)))

	results, err := scanner.Backtest(ctx, opps, client, orderSize)
	if err != nil {
		slog.Error("backtest failed", "err", err)
		os.Exit(1)
	}

	notifier.PrintBacktest(results)
	slog.Info("backtest complete", "markets_tested", len(results))
}
