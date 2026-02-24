package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/alejandrodnm/polybot/internal/adapters/polygon"
	"github.com/alejandrodnm/polybot/internal/adapters/polymarket"
	"github.com/alejandrodnm/polybot/internal/adapters/storage"
	"github.com/alejandrodnm/polybot/internal/domain"
	"github.com/alejandrodnm/polybot/internal/scanner"
)

func runLive(ctx context.Context, s *scanner.Scanner, store *storage.SQLiteStorage, cfg scanner.LiveConfig, privateKey, clobBase, gammaBase, rpcURL string) {
	slog.Info("=== LIVE TRADING MODE (REAL MONEY) ===",
		"order_size", cfg.OrderSize,
		"max_markets", cfg.MaxMarkets,
		"initial_capital", cfg.InitialCapital,
		"max_exposure", cfg.MaxExposure,
	)

	fmt.Printf("\n⚠️  LIVE TRADING MODE — REAL MONEY WILL BE SPENT\n")
	fmt.Printf("   Initial capital: $%.2f | Max exposure: $%.2f | Order size: $%.2f\n",
		cfg.InitialCapital, cfg.MaxExposure, cfg.OrderSize)
	fmt.Printf("   Press Ctrl+C within 5 seconds to abort...\n\n")

	abortTimer := time.NewTimer(5 * time.Second)
	select {
	case <-abortTimer.C:
	case <-ctx.Done():
		slog.Info("live trading aborted by user")
		return
	}

	if err := store.ApplyLiveSchema(ctx); err != nil {
		slog.Error("failed to create live trading tables", "err", err)
		os.Exit(1)
	}

	authClient, err := polymarket.NewAuthClient(clobBase, gammaBase, privateKey)
	if err != nil {
		slog.Error("failed to create auth client", "err", err)
		os.Exit(1)
	}

	if err := authClient.EnsureCreds(ctx); err != nil {
		slog.Error("failed to derive API credentials — check POLY_PRIVATE_KEY", "err", err)
		os.Exit(1)
	}

	slog.Info("live: authenticated with Polymarket CLOB", "address", authClient.Address())

	tradingClient, err := polymarket.NewTradingClient(authClient, rpcURL)
	if err != nil {
		slog.Error("failed to create trading client", "err", err)
		os.Exit(1)
	}

	mergeClient, err := polygon.NewMergeClient(rpcURL, privateKey)
	if err != nil {
		slog.Error("failed to create merge client", "err", err)
		os.Exit(1)
	}

	slog.Info("live: checking on-chain approvals...")
	if err := mergeClient.EnsureApprovals(ctx); err != nil {
		slog.Error("failed to ensure on-chain approvals", "err", err)
		os.Exit(1)
	}
	slog.Info("live: all approvals verified")

	balance, err := tradingClient.GetBalance(ctx)
	if err != nil {
		slog.Error("failed to get CLOB balance", "err", err)
		os.Exit(1)
	}
	slog.Info("live: CLOB balance", "usdc", fmt.Sprintf("$%.2f", balance))

	if balance < cfg.OrderSize*2 {
		slog.Error("insufficient CLOB balance",
			"balance", fmt.Sprintf("$%.2f", balance),
			"required", fmt.Sprintf("$%.2f", cfg.OrderSize*2))
		os.Exit(1)
	}

	s.SetFilter(scanner.NewFilter(scanner.FilterConfig{
		MaxSpreadTotal:   0.20,
		MaxCompetition:   100_000,
		RequireQualifies: false,
		OnlyFillsProfit:  true,
	}))
	livEngine := scanner.NewLiveEngine(s, tradingClient, mergeClient, store, cfg)

	if savedBreaker, err := store.LoadCircuitBreaker(ctx); err == nil {
		livEngine.RestoreCircuitBreaker(savedBreaker)
		slog.Info("live: circuit breaker state restored",
			"losses", savedBreaker.ConsecutiveLosses,
			"pnl", fmt.Sprintf("$%.4f", savedBreaker.TotalPnL),
			"triggered", savedBreaker.Triggered)
	}

	stopFile := "STOP_LIVE"
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	slog.Info("live trading started — press Ctrl+C or create STOP_LIVE file to exit")

	cycle := 1
	runLiveCycle(ctx, livEngine, cycle)

	for {
		select {
		case <-ctx.Done():
			slog.Info("live trading stopped (signal)", "total_cycles", cycle)
			return
		case <-ticker.C:
			if _, err := os.Stat(stopFile); err == nil {
				slog.Info("STOP_LIVE file detected — shutting down live trading", "total_cycles", cycle)
				os.Remove(stopFile)
				return
			}
			cycle++
			runLiveCycle(ctx, livEngine, cycle)
		}
	}
}

func runLiveCycle(ctx context.Context, le *scanner.LiveEngine, cycle int) {
	result, err := le.RunOnce(ctx)
	if err != nil {
		slog.Error("live cycle failed", "cycle", cycle, "err", err)
		return
	}

	slog.Info("live: cycle complete",
		"cycle", cycle,
		"positions", len(result.Positions),
		"new_orders", result.NewOrders,
		"new_fills", result.NewFills,
		"merges", result.Merges,
		"merge_profit", fmt.Sprintf("$%.4f", result.MergeProfit),
		"gas_cost", fmt.Sprintf("$%.4f", result.GasCostUSD),
		"compound_balance", fmt.Sprintf("$%.2f", result.CompoundBalance),
		"circuit_open", result.CircuitOpen,
	)

	for _, alert := range result.PartialAlerts {
		slog.Warn("live: partial alert", "msg", alert)
	}
	for _, warn := range result.Warnings {
		slog.Warn("live: warning", "msg", warn)
	}
}

func runLiveReport(ctx context.Context, store *storage.SQLiteStorage) {
	if err := store.ApplyLiveSchema(ctx); err != nil {
		slog.Error("failed to init live schema", "err", err)
		os.Exit(1)
	}

	stats, err := store.GetLiveStats(ctx)
	if err != nil {
		slog.Error("failed to get live stats", "err", err)
		os.Exit(1)
	}

	fmt.Printf("\n╔══════════════════════════════════════════════════════════════╗\n")
	fmt.Printf("║                    LIVE TRADING REPORT                       ║\n")
	fmt.Printf("╚══════════════════════════════════════════════════════════════╝\n\n")

	fmt.Printf("  Period:       %s → %s (%d days)\n",
		stats.StartDate.Format("2006-01-02"),
		stats.EndDate.Format("2006-01-02"),
		stats.DaysRunning)
	fmt.Printf("  Total Orders: %d | Fills: %d (%.0f%% fill rate)\n",
		stats.TotalOrders, stats.TotalFills, stats.FillRateReal*100)
	fmt.Printf("  Merges:       %d completed\n", stats.CompletePairs)
	fmt.Printf("  Merge Profit: $%.4f\n", stats.TotalMergeProfit)
	fmt.Printf("  Gas Cost:     $%.4f\n", stats.TotalGasCostUSD)
	fmt.Printf("  Net P&L:      $%.4f (avg $%.4f/day)\n", stats.NetPnL, stats.DailyAvgPnL)
	fmt.Printf("  Rotations:    %d\n", stats.TotalRotations)

	openOrders, _ := store.GetOpenLiveOrders(ctx)
	fmt.Printf("\n── OPEN ORDERS (%d) ──\n", len(openOrders))
	if len(openOrders) > 0 {
		fmt.Printf("  %-6s %6s %6s %8s %-35s %s\n", "SIDE", "PRICE", "SIZE$", "FILLED$", "MARKET", "AGE")
		for _, o := range openOrders {
			age := time.Since(o.PlacedAt).Truncate(time.Minute)
			q := truncateQuestion(o.Question, o.ConditionID, 35)
			fmt.Printf("  %-6s %6.2f %6.2f %8.2f %-35s %v\n",
				o.Side, o.BidPrice, o.Size, o.FilledSize, q, age)
		}
	} else {
		fmt.Println("  (none)")
	}

	partialPairs := findPartialPairs(ctx, store)
	fmt.Printf("\n── PARTIAL FILLS (%d pairs with only one side filled) ──\n", len(partialPairs))
	if len(partialPairs) > 0 {
		for _, pairID := range partialPairs {
			pairOrders, _ := store.GetLiveOrdersByPair(ctx, pairID)
			for _, o := range pairOrders {
				status := string(o.Status)
				if o.FilledSize > 0 {
					status = fmt.Sprintf("FILLED $%.2f", o.FilledSize)
				}
				q := truncateQuestion(o.Question, o.ConditionID, 30)
				fmt.Printf("  [%s] %-4s %5.2f¢ %8s  %s\n", pairID[:8], o.Side, o.BidPrice*100, status, q)
			}
			fmt.Println()
		}
	} else {
		fmt.Println("  (none)")
	}

	fmt.Printf("\n── SUMMARY ──\n")
	fmt.Printf("  Open orders:        %d\n", len(openOrders))
	fmt.Printf("  Partial fill pairs: %d (RISK: directional exposure)\n", len(partialPairs))
	fmt.Printf("  Circuit breaker:    ")
	cb, err := store.LoadCircuitBreaker(ctx)
	if err == nil && cb.Triggered {
		fmt.Printf("TRIGGERED (reason: %s)\n", cb.TriggeredReason)
	} else {
		fmt.Printf("OK\n")
	}

	if len(stats.Dailies) > 0 {
		fmt.Printf("\n── DAILY BREAKDOWN ──\n")
		fmt.Printf("  %-12s %8s %8s %8s %8s\n", "Date", "Orders", "Fills", "Merges", "NetPnL")
		for _, d := range stats.Dailies {
			fmt.Printf("  %-12s %8d %8d %8d $%7.4f\n",
				d.Date.Format("2006-01-02"),
				d.OrdersPlaced, d.FillsYes+d.FillsNo, d.Merges, d.NetPnL)
		}
	}
	fmt.Println()
}

func findPartialPairs(ctx context.Context, store *storage.SQLiteStorage) []string {
	allFilled, _ := store.GetAllLiveOrders(ctx, "FILLED")
	partialFilled, _ := store.GetAllLiveOrders(ctx, "PARTIAL")
	filledOrders := append(allFilled, partialFilled...)

	pairFills := make(map[string][]domain.LiveOrder)
	for _, o := range filledOrders {
		pairFills[o.PairID] = append(pairFills[o.PairID], o)
	}

	var partials []string
	for pairID, fills := range pairFills {
		yesF, noF := false, false
		for _, f := range fills {
			if f.Side == "YES" {
				yesF = true
			} else {
				noF = true
			}
		}
		if yesF != noF {
			partials = append(partials, pairID)
		}
	}
	return partials
}

func truncateQuestion(question, conditionID string, maxLen int) string {
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
