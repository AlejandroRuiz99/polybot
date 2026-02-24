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
	paper := flag.Bool("paper", false, "run paper trading simulation (no real money)")
	paperReport := flag.Bool("paper-report", false, "print paper trading report and exit")
	paperMarkets := flag.Int("paper-markets", 10, "max simultaneous markets in paper mode")
	paperCapital := flag.Float64("paper-capital", 1000, "initial capital for compound rotation tracking (USDC)")

	live := flag.Bool("live", false, "run REAL MONEY trading engine (requires POLY_PRIVATE_KEY env var)")
	liveReport := flag.Bool("live-report", false, "print live trading report and exit")
	liveCapital := flag.Float64("live-capital", 20, "initial capital for live trading (USDC)")
	liveMaxExposure := flag.Float64("live-max-exposure", 50, "max total USDC deployed at any time")
	liveOrderSize := flag.Float64("live-order-size", 5, "target order size per side in USDC")
	liveMaxMarkets := flag.Int("live-markets", 5, "max simultaneous markets in live mode")
	polygonRPC := flag.String("polygon-rpc", "https://polygon-rpc.com", "Polygon RPC endpoint for on-chain merges")

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
		"paper", *paper,
	)

	client := polymarket.NewClient(cfg.API.CLOBBase, cfg.API.GammaBase)

	store, err := storage.NewSQLiteStorage(cfg.Storage.DSN)
	if err != nil {
		slog.Error("failed to open storage", "err", err, "dsn", cfg.Storage.DSN)
		os.Exit(1)
	}
	defer store.Close()

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

	switch {
	case *liveReport:
		runLiveReport(ctx, store)

	case *paperReport:
		runPaperReport(ctx, store, notifier, *paperCapital)

	case *backtest:
		runBacktest(ctx, s, client, notifier, scanCfg.OrderSize)

	case *paper:
		runPaper(ctx, s, client, store, notifier, scanner.PaperConfig{
			OrderSize:      cfg.Scanner.OrderSizeUSDC,
			MaxMarkets:     *paperMarkets,
			FeeRate:        cfg.Scanner.FeeRateDefault,
			InitialCapital: *paperCapital,
		})

	case *live:
		privKey := os.Getenv("POLY_PRIVATE_KEY")
		if privKey == "" {
			slog.Error("POLY_PRIVATE_KEY environment variable is required for live trading")
			os.Exit(1)
		}
		rpcURL := os.Getenv("POLYGON_RPC")
		if rpcURL == "" {
			rpcURL = *polygonRPC
		}
		orderSize := *liveOrderSize
		if orderSize <= 0 {
			orderSize = cfg.Scanner.OrderSizeUSDC
		}
		runLive(ctx, s, store, scanner.LiveConfig{
			OrderSize:      orderSize,
			MaxMarkets:     *liveMaxMarkets,
			FeeRate:        cfg.Scanner.FeeRateDefault,
			InitialCapital: *liveCapital,
			MaxExposure:    *liveMaxExposure,
			MinMergeProfit: 0.05,
		}, privKey, cfg.API.CLOBBase, cfg.API.GammaBase, rpcURL)

	default:
		if err := s.Run(ctx); err != nil {
			slog.Error("scanner exited with error", "err", err)
			os.Exit(1)
		}
		slog.Info("polybot stopped cleanly")
	}
}
