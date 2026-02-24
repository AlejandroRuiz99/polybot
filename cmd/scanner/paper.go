package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/alejandrodnm/polybot/internal/adapters/notify"
	"github.com/alejandrodnm/polybot/internal/adapters/polymarket"
	"github.com/alejandrodnm/polybot/internal/adapters/storage"
	"github.com/alejandrodnm/polybot/internal/scanner"
)

func runPaper(ctx context.Context, s *scanner.Scanner, client *polymarket.Client, store *storage.SQLiteStorage, notifier *notify.Console, cfg scanner.PaperConfig) {
	slog.Info("=== PAPER TRADING MODE (compound rotation) ===",
		"order_size", cfg.OrderSize,
		"max_markets", cfg.MaxMarkets,
		"initial_capital", cfg.InitialCapital,
	)

	if err := store.ApplyPaperSchema(ctx); err != nil {
		slog.Error("failed to create paper trading tables", "err", err)
		os.Exit(1)
	}

	pe := scanner.NewPaperEngine(s, client, store, cfg)

	stopFile := "STOP"
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	slog.Info("paper trading started — press Ctrl+C or create STOP file to exit")
	fmt.Printf("[PAPER] Starting compound rotation loop (60s interval, capital $%.0f)...\n", cfg.InitialCapital)

	runPaperCycle(ctx, pe, notifier, cfg.InitialCapital)

	for {
		select {
		case <-ctx.Done():
			slog.Info("paper trading stopped (signal)")
			printPaperExitSummary(ctx, store, notifier, cfg.InitialCapital)
			return
		case <-ticker.C:
			if _, err := os.Stat(stopFile); err == nil {
				slog.Info("STOP file detected — shutting down paper trading")
				os.Remove(stopFile)
				printPaperExitSummary(ctx, store, notifier, cfg.InitialCapital)
				return
			}
			runPaperCycle(ctx, pe, notifier, cfg.InitialCapital)
		}
	}
}

func runPaperCycle(ctx context.Context, pe *scanner.PaperEngine, notifier *notify.Console, initialCapital float64) {
	result, err := pe.RunOnce(ctx)
	if err != nil {
		slog.Error("paper cycle failed", "err", err)
		return
	}

	notifier.PrintPaperStatus(notify.PaperStatusInput{
		Positions:        result.Positions,
		NewOrders:        result.NewOrders,
		NewFills:         result.NewFills,
		Alerts:           result.PartialAlerts,
		Warnings:         result.Warnings,
		CapitalDeployed:  result.CapitalDeployed,
		Merges:           result.Merges,
		MergeProfit:      result.MergeProfit,
		CompoundBalance:  result.CompoundBalance,
		TotalRotations:   result.TotalRotations,
		TotalMergeProfit: result.MergeProfit,
		InitialCapital:   initialCapital,
		AvgCycleHours:    result.AvgCycleHours,
		KellyFraction:    result.KellyFraction,
	})
}

func runPaperReport(ctx context.Context, store *storage.SQLiteStorage, notifier *notify.Console, initialCapital float64) {
	if err := store.ApplyPaperSchema(ctx); err != nil {
		slog.Error("failed to init paper schema", "err", err)
		os.Exit(1)
	}

	stats, err := store.GetPaperStats(ctx)
	if err != nil {
		slog.Error("failed to get paper stats", "err", err)
		os.Exit(1)
	}

	stats.InitialCapital = initialCapital
	if stats.InitialCapital > 0 {
		stats.CompoundGrowth = (stats.InitialCapital + stats.TotalMergeProfit) / stats.InitialCapital
	}
	notifier.PrintPaperReport(stats)
}

func printPaperExitSummary(ctx context.Context, store *storage.SQLiteStorage, notifier *notify.Console, initialCapital float64) {
	stats, err := store.GetPaperStats(ctx)
	if err != nil {
		slog.Warn("could not generate exit summary", "err", err)
		return
	}
	stats.InitialCapital = initialCapital
	if stats.InitialCapital > 0 {
		stats.CompoundGrowth = (stats.InitialCapital + stats.TotalMergeProfit) / stats.InitialCapital
	}
	notifier.PrintPaperReport(stats)
}
