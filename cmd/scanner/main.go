package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/alejandrodnm/polybot/config"
	"github.com/alejandrodnm/polybot/internal/adapters/notify"
	"github.com/alejandrodnm/polybot/internal/adapters/polymarket"
	"github.com/alejandrodnm/polybot/internal/adapters/storage"
	"github.com/alejandrodnm/polybot/internal/scanner"
)

func main() {
	configPath := flag.String("config", "config/config.yaml", "path to config file")
	once := flag.Bool("once", false, "run one scan cycle and exit")
	dryRun := flag.Bool("dry-run", false, "use local fixtures instead of real API")
	verbose := flag.Bool("verbose", false, "set log level to debug")
	logFormat := flag.String("format", "", "log format: text|json (overrides config)")
	table := flag.Bool("table", false, "print full table + portfolio (default: compact 1-line)")
	validate := flag.Bool("validate", false, "print step-by-step calculation for top 3 markets")
	backtest := flag.Bool("backtest", false, "scan once + fetch real trades to validate fill rates")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "err", err, "path", *configPath)
		os.Exit(1)
	}

	if *verbose {
		cfg.Log.Level = "debug"
	}
	if *logFormat != "" {
		cfg.Log.Format = *logFormat
	}
	setupLogger(cfg.Log)

	slog.Info("polybot starting",
		"config", *configPath,
		"interval", cfg.ScanInterval(),
		"dry_run", *dryRun,
		"once", *once,
		"validate", *validate,
		"backtest", *backtest,
	)

	client := polymarket.NewClient(cfg.API.CLOBBase, cfg.API.GammaBase)

	var store *storage.SQLiteStorage
	if !*dryRun {
		store, err = storage.NewSQLiteStorage(cfg.Storage.DSN)
		if err != nil {
			slog.Error("failed to open storage", "err", err, "dsn", cfg.Storage.DSN)
			os.Exit(1)
		}
		defer store.Close()
	}

	notifier := notify.NewConsole(cfg.Scanner.OrderSizeUSDC, *table || *backtest, *validate)

	scanCfg := scanner.DefaultConfig()
	scanCfg.ScanInterval = cfg.ScanInterval()
	scanCfg.OrderSize = cfg.Scanner.OrderSizeUSDC
	scanCfg.FeeRate = cfg.Scanner.FeeRateDefault
	scanCfg.FillsPerDay = cfg.Scanner.ArbFillsPerDay
	scanCfg.GoldMinReward = cfg.Scanner.GoldMinReward
	scanCfg.AnalysisWorkers = cfg.Scanner.AnalysisWorkers
	scanCfg.DryRun = *dryRun || *once || *backtest
	scanCfg.Filter = scanner.FilterConfig{
		MinYourDailyReward:   cfg.Scanner.MinYourDailyReward,
		MinRewardScore:       cfg.Scanner.MinRewardScore,
		MaxSpreadTotal:       cfg.Scanner.MaxSpreadTotal,
		MaxCompetition:       cfg.Scanner.MaxCompetition,
		RequireQualifies:     cfg.Scanner.RequireQualifies,
		MinHoursToResolution: cfg.Scanner.MinHoursToResolution,
		OnlyFillsProfit:      cfg.Scanner.OnlyFillsProfit,
	}

	s := scanner.New(scanCfg, client, client, store, notifier)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if *backtest {
		runBacktest(ctx, s, client, notifier, scanCfg.OrderSize)
		return
	}

	if err := s.Run(ctx); err != nil {
		slog.Error("scanner exited with error", "err", err)
		os.Exit(1)
	}

	slog.Info("polybot stopped cleanly")
}

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

func setupLogger(cfg config.LogConfig) {
	var level slog.Level
	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if cfg.Format == "json" {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}
	slog.SetDefault(slog.New(handler))
}
