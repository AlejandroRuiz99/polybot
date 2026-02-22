package config

import (
	"fmt"
	"os"
	"time"

	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"
)

// Config es la configuración completa del scanner.
type Config struct {
	Scanner ScannerConfig `yaml:"scanner"`
	API     APIConfig     `yaml:"api"`
	Storage StorageConfig `yaml:"storage"`
	Log     LogConfig     `yaml:"log"`
}

// ScannerConfig controla el comportamiento del scanner.
type ScannerConfig struct {
	IntervalSeconds      int     `yaml:"interval_seconds"`
	OrderSizeUSDC        float64 `yaml:"order_size_usdc"`
	FeeRateDefault       float64 `yaml:"fee_rate_default"`        // default conservador si la API no devuelve fee
	MinYourDailyReward   float64 `yaml:"min_your_daily_reward"`   // mínimo tu $/día para pasar el filtro
	MinRewardScore       float64 `yaml:"min_reward_score"`
	MaxSpreadTotal       float64 `yaml:"max_spread_total"`
	MaxCompetition       float64 `yaml:"max_competition"`
	RequireQualifies     bool    `yaml:"require_qualifies"`
	MinHoursToResolution float64 `yaml:"min_hours_to_resolution"` // filtrar mercados que se resuelven pronto

	// Filtro de seguridad
	OnlyFillsProfit bool `yaml:"only_fills_profit"` // true = descartar mercados donde un fill te cuesta dinero

	// Arbitraje + concurrencia
	ArbFillsPerDay  float64 `yaml:"arb_fills_per_day"`   // fills estimados/día para cálculo de arb profit
	GoldMinReward   float64 `yaml:"gold_min_reward"`     // mínimo YourDailyReward para categoría Gold
	AnalysisWorkers int     `yaml:"analysis_workers"`    // goroutines para análisis paralelo (0 = NumCPU*2)
}

// APIConfig contiene los base URLs de las APIs.
type APIConfig struct {
	CLOBBase  string `yaml:"clob_base"`
	GammaBase string `yaml:"gamma_base"`
}

// StorageConfig controla dónde se persisten los datos.
type StorageConfig struct {
	DSN string `yaml:"dsn"` // ruta al archivo SQLite, o ":memory:"
}

// LogConfig controla el formato y nivel de logging.
type LogConfig struct {
	Level  string `yaml:"level"`  // debug | info | warn | error
	Format string `yaml:"format"` // text | json
}

// Load carga la configuración desde el archivo YAML y el archivo .env si existe.
// Los valores del .env sobreescriben los del YAML para las keys que correspondan.
func Load(path string) (*Config, error) {
	// Cargar .env si existe (silencia error si no hay archivo)
	_ = godotenv.Load()

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config.Load: read %q: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config.Load: parse YAML: %w", err)
	}

	applyEnvOverrides(&cfg)
	setDefaults(&cfg)

	return &cfg, nil
}

// ScanInterval devuelve el intervalo de escaneo como time.Duration.
func (c *Config) ScanInterval() time.Duration {
	return time.Duration(c.Scanner.IntervalSeconds) * time.Second
}

// applyEnvOverrides sobreescribe valores con variables de entorno si están presentes.
func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		cfg.Log.Level = v
	}
	if v := os.Getenv("LOG_FORMAT"); v != "" {
		cfg.Log.Format = v
	}
}

// setDefaults asegura que los valores requeridos tengan valores sensatos.
func setDefaults(cfg *Config) {
	if cfg.Scanner.IntervalSeconds <= 0 {
		cfg.Scanner.IntervalSeconds = 30
	}
	if cfg.Scanner.OrderSizeUSDC <= 0 {
		cfg.Scanner.OrderSizeUSDC = 100
	}
	if cfg.Scanner.FeeRateDefault <= 0 {
		cfg.Scanner.FeeRateDefault = 0.02 // 2% default conservador
	}
	if cfg.Scanner.ArbFillsPerDay <= 0 {
		cfg.Scanner.ArbFillsPerDay = 2.0 // estimación conservadora de fills/día en mercados Gold
	}
	if cfg.Scanner.GoldMinReward <= 0 {
		cfg.Scanner.GoldMinReward = 0.01 // mínimo $0.01/día de reward para entrar en Gold/Silver
	}
	if cfg.API.CLOBBase == "" {
		cfg.API.CLOBBase = "https://clob.polymarket.com"
	}
	if cfg.API.GammaBase == "" {
		cfg.API.GammaBase = "https://gamma-api.polymarket.com"
	}
	if cfg.Storage.DSN == "" {
		cfg.Storage.DSN = "polybot.db"
	}
	if cfg.Log.Level == "" {
		cfg.Log.Level = "info"
	}
	if cfg.Log.Format == "" {
		cfg.Log.Format = "text"
	}
}
