// Package config loads and validates all runtime configuration from the
// environment. Nothing else in the application reads os.Getenv directly.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Config is the fully resolved application configuration.
type Config struct {
	App       AppConfig
	HTTP      HTTPConfig
	Database  DatabaseConfig
	CoinGecko CoinGeckoConfig
	Bybit     BybitMarketConfig
	News      NewsConfig
	Analysis  AnalysisConfig
	LLM       LLMConfig
	Risk      RiskConfig
	Fees      FeesConfig
	Backtest  BacktestConfig
	Logging   LoggingConfig
}

// AppConfig holds process-wide settings.
type AppConfig struct {
	Env             string
	ShutdownTimeout time.Duration
	// SettingsFromEnv re-seeds the stored settings from the environment on
	// start. Without it, values edited in the Settings screen win over .env,
	// so a deliberate UI change is never silently reverted by a restart.
	SettingsFromEnv bool
}

// HTTPConfig configures the REST server.
type HTTPConfig struct {
	Host            string
	Port            int
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	CORSAllowOrigin []string
}

// Addr returns the listen address.
func (h HTTPConfig) Addr() string { return fmt.Sprintf("%s:%d", h.Host, h.Port) }

// DatabaseConfig configures the PostgreSQL connection pool.
type DatabaseConfig struct {
	URL             string
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	ConnectTimeout  time.Duration
	AutoMigrate     bool
}

// CoinGeckoConfig configures market metadata and the current fallback candle
// source. Public Bybit market data is configured separately in Stage 2.
type CoinGeckoConfig struct {
	BaseURL       string
	APIKey        string
	APIKeyHeader  string
	VSCurrency    string
	Timeout       time.Duration
	MaxRetries    int
	RetryBaseWait time.Duration
	RateLimitRPM  int
	CacheTTL      time.Duration
	UniverseSize  int
}

// BybitMarketConfig controls public, unauthenticated spot market-data calls.
type BybitMarketConfig struct {
	Enabled       bool
	BaseURL       string
	Timeout       time.Duration
	MaxRetries    int
	RetryBaseWait time.Duration
	RateLimitRPM  int
}

// NewsConfig controls the local News Intelligence ingestion pipeline. All
// built-in providers are public and must work without API keys.
type NewsConfig struct {
	Enabled                  bool
	FetchInterval            time.Duration
	FetchConcurrency         int
	HTTPTimeout              time.Duration
	MaxRetries               int
	RetryBaseWait            time.Duration
	MaxResponseBytes         int64
	AllowPrivateFeeds        bool
	LLMLookback              time.Duration
	LLMMaxAssetItems         int
	LLMMaxGlobalItems        int
	ClusterTimeWindow        time.Duration
	TitleSimilarityThreshold float64
	HistoryMinSampleSize     int
	ReactionInterval         time.Duration
	ReactionBaselineGrace    time.Duration
	BybitEnabled             bool
	GDELTEnabled             bool
}

// AnalysisConfig controls the scheduler and analysis pipeline.
type AnalysisConfig struct {
	Interval time.Duration
	// BenchmarkSymbol is the asset whose daily trend stands for the market as a
	// whole. Empty disables the market-wide filter rather than guessing.
	BenchmarkSymbol      string
	Timeframes           []string
	MarketDataInterval   time.Duration
	UniverseRefresh      time.Duration
	MaxConcurrentSymbols int
	CandleHistoryLimit   int
	ManualCooldown       time.Duration
	StaleAfter           time.Duration
	OutcomeInterval      time.Duration
	Enabled              bool
}

// LLMConfig configures the OpenAI-compatible inference endpoint.
type LLMConfig struct {
	BaseURL     string
	APIKey      string
	Model       string
	Timeout     time.Duration
	Temperature float64
	MaxTokens   int
	ContextSize int
	// ContextWarnPct and ContextCriticalPct are the share of the context window
	// at which the UI starts warning. The snapshot deliberately fills the window
	// up to the safety reserve, so a healthy run already sits fairly high and
	// these thresholds mark a real risk of the server refusing the request.
	ContextWarnPct     float64
	ContextCriticalPct float64
	// SnapshotMaxTokens optionally caps the market snapshot below what the
	// context window allows. Zero means "use the whole available window".
	SnapshotMaxTokens int
	MaxConcurrent     int
	Enabled           bool
	PromptVersion     string
	HealthInterval    time.Duration
}

// RiskConfig configures the deterministic risk engine.
type RiskConfig struct {
	MinLeverage             int
	MaxLeverage             int
	MaxRecommendedAllocPct  decimal.Decimal
	HighVolatilityATRPct    float64
	ExtremeVolatilityATRPct float64
	MinConfidence           int
	// RiskPerTradePct is the share of equity a stop-out is allowed to cost. When
	// it is set the advisory size is derived from the stop distance instead of
	// being a fixed share of capital, so a quiet asset and a volatile one carry
	// the same risk. Zero keeps the fixed-allocation behaviour.
	RiskPerTradePct                float64
	CriticalNewsMaxLeverage        int
	CriticalNewsHighVolMaxLeverage int
	CriticalNewsMaxAge             time.Duration
}

// FeesConfig holds the user-configured fee profile. Public market endpoints do
// not expose the user's tier, so these values are supplied rather than fetched.
type FeesConfig struct {
	Exchange      string
	MakerPct      decimal.Decimal
	TakerPct      decimal.Decimal
	Configured    bool
	SlippagePct   decimal.Decimal
	FundingManual bool
}

// BacktestConfig limits backtest resource usage.
type BacktestConfig struct {
	MaxInferences            int
	CacheEnabled             bool
	DefaultSlippage          decimal.Decimal
	DefaultFundingRate       decimal.Decimal
	DefaultMaintenanceMargin decimal.Decimal
	DefaultMaxOpenPositions  int
	MaxConcurrent            int
	// DefaultInferencePause is what a new LLM run waits after each request that
	// reached the model. Zero runs the GPU flat out, which a long replay can do
	// for hours.
	DefaultInferencePause time.Duration
	// RunTimeout bounds one background run. An LLM replay spends seconds per
	// step, so the ceiling has to hold MaxInferences steps at the observed
	// latency plus the configured pause; a run cut short by this timeout is
	// stored as failed and its inferences are wasted.
	RunTimeout time.Duration
}

// LoggingConfig configures slog.
type LoggingConfig struct {
	Level  string
	Format string
}

// Load reads configuration from the process environment, applying defaults and
// validating the result.
func Load() (*Config, error) {
	cfg := &Config{
		App: AppConfig{
			Env:             env("APP_ENV", "development"),
			ShutdownTimeout: envDuration("APP_SHUTDOWN_TIMEOUT", 20*time.Second),
			SettingsFromEnv: envBool("SETTINGS_FROM_ENV", false),
		},
		HTTP: HTTPConfig{
			Host:            env("HTTP_HOST", "0.0.0.0"),
			Port:            envInt("HTTP_PORT", 8080),
			ReadTimeout:     envDuration("HTTP_READ_TIMEOUT", 30*time.Second),
			WriteTimeout:    envDuration("HTTP_WRITE_TIMEOUT", 60*time.Second),
			IdleTimeout:     envDuration("HTTP_IDLE_TIMEOUT", 120*time.Second),
			CORSAllowOrigin: envList("HTTP_CORS_ORIGINS", []string{"*"}),
		},
		Database: DatabaseConfig{
			URL:             env("DATABASE_URL", "postgres://advisor:advisor@localhost:5432/advisor?sslmode=disable"),
			MaxConns:        int32(envInt("DATABASE_MAX_CONNS", 10)),
			MinConns:        int32(envInt("DATABASE_MIN_CONNS", 2)),
			MaxConnLifetime: envDuration("DATABASE_MAX_CONN_LIFETIME", time.Hour),
			ConnectTimeout:  envDuration("DATABASE_CONNECT_TIMEOUT", 10*time.Second),
			AutoMigrate:     envBool("DATABASE_AUTO_MIGRATE", true),
		},
		CoinGecko: CoinGeckoConfig{
			BaseURL:       strings.TrimRight(env("COINGECKO_BASE_URL", "https://api.coingecko.com/api/v3"), "/"),
			APIKey:        env("COINGECKO_API_KEY", ""),
			APIKeyHeader:  env("COINGECKO_API_KEY_HEADER", "x-cg-demo-api-key"),
			VSCurrency:    env("COINGECKO_VS_CURRENCY", "usd"),
			Timeout:       envDuration("COINGECKO_TIMEOUT", 20*time.Second),
			MaxRetries:    envInt("COINGECKO_MAX_RETRIES", 3),
			RetryBaseWait: envDuration("COINGECKO_RETRY_BASE_WAIT", time.Second),
			RateLimitRPM:  envInt("COINGECKO_RATE_LIMIT_RPM", 25),
			CacheTTL:      envDuration("COINGECKO_CACHE_TTL", 45*time.Second),
			UniverseSize:  envInt("MARKET_UNIVERSE_SIZE", 20),
		},
		Bybit: BybitMarketConfig{
			Enabled:       envBool("BYBIT_MARKET_DATA_ENABLED", true),
			BaseURL:       strings.TrimRight(env("BYBIT_MARKET_BASE_URL", "https://api.bybit.com"), "/"),
			Timeout:       envDuration("BYBIT_MARKET_TIMEOUT", 15*time.Second),
			MaxRetries:    envInt("BYBIT_MARKET_MAX_RETRIES", 2),
			RetryBaseWait: envDuration("BYBIT_MARKET_RETRY_BASE_WAIT", time.Second),
			RateLimitRPM:  envInt("BYBIT_MARKET_RATE_LIMIT_RPM", 300),
		},
		News: NewsConfig{
			Enabled:                  envBool("NEWS_ENABLED", true),
			FetchInterval:            envDuration("NEWS_FETCH_INTERVAL", 5*time.Minute),
			FetchConcurrency:         envInt("NEWS_FETCH_CONCURRENCY", 4),
			HTTPTimeout:              envDuration("NEWS_HTTP_TIMEOUT", 15*time.Second),
			MaxRetries:               envInt("NEWS_MAX_RETRIES", 2),
			RetryBaseWait:            envDuration("NEWS_RETRY_BASE_WAIT", time.Second),
			MaxResponseBytes:         int64(envInt("NEWS_MAX_RESPONSE_BYTES", 2*1024*1024)),
			AllowPrivateFeeds:        envBool("NEWS_ALLOW_PRIVATE_FEEDS", false),
			LLMLookback:              envDuration("NEWS_LLM_LOOKBACK", 24*time.Hour),
			LLMMaxAssetItems:         envInt("NEWS_LLM_MAX_ASSET_ITEMS", 8),
			LLMMaxGlobalItems:        envInt("NEWS_LLM_MAX_GLOBAL_ITEMS", 5),
			ClusterTimeWindow:        envDuration("NEWS_CLUSTER_TIME_WINDOW", 6*time.Hour),
			TitleSimilarityThreshold: envFloat("NEWS_TITLE_SIMILARITY_THRESHOLD", 0.72),
			HistoryMinSampleSize:     envInt("NEWS_HISTORY_MIN_SAMPLE_SIZE", 20),
			ReactionInterval:         envDuration("NEWS_REACTION_INTERVAL", 5*time.Minute),
			ReactionBaselineGrace:    envDuration("NEWS_REACTION_BASELINE_GRACE", 2*time.Hour),
			BybitEnabled:             envBool("NEWS_BYBIT_ENABLED", true),
			GDELTEnabled:             envBool("NEWS_GDELT_ENABLED", false),
		},
		Analysis: AnalysisConfig{
			Interval:             envDuration("ANALYSIS_INTERVAL", 5*time.Minute),
			BenchmarkSymbol:      strings.ToUpper(env("ANALYSIS_BENCHMARK_SYMBOL", "BTC")),
			Timeframes:           envList("ANALYSIS_TIMEFRAMES", []string{"1m", "5m", "15m", "1h", "4h", "1d"}),
			MarketDataInterval:   envDuration("MARKET_DATA_INTERVAL", time.Minute),
			UniverseRefresh:      envDuration("MARKET_UNIVERSE_REFRESH", 24*time.Hour),
			MaxConcurrentSymbols: envInt("ANALYSIS_MAX_CONCURRENT_SYMBOLS", 4),
			CandleHistoryLimit:   envInt("ANALYSIS_CANDLE_HISTORY", 500),
			ManualCooldown:       envDuration("ANALYSIS_MANUAL_COOLDOWN", 60*time.Second),
			StaleAfter:           envDuration("RECOMMENDATION_STALE_AFTER", 30*time.Minute),
			OutcomeInterval:      envDuration("OUTCOME_EVALUATION_INTERVAL", 5*time.Minute),
			Enabled:              envBool("ANALYSIS_ENABLED", true),
		},
		LLM: LLMConfig{
			BaseURL:     strings.TrimRight(env("LLM_BASE_URL", "http://llm:8080/v1"), "/"),
			APIKey:      env("LLM_API_KEY", ""),
			Model:       env("LLM_MODEL", "Qwen3-8B"),
			Timeout:     envDuration("LLM_TIMEOUT", 180*time.Second),
			Temperature: envFloat("LLM_TEMPERATURE", 0.2),
			MaxTokens:   envInt("LLM_MAX_TOKENS", 1800),
			ContextSize: envInt("LLM_CONTEXT_SIZE", 16384),

			ContextWarnPct:     envFloat("LLM_CONTEXT_WARN_PCT", 90),
			ContextCriticalPct: envFloat("LLM_CONTEXT_CRITICAL_PCT", 97),
			SnapshotMaxTokens:  envInt("LLM_SNAPSHOT_MAX_TOKENS", 0),
			MaxConcurrent:      envInt("LLM_MAX_CONCURRENT_REQUESTS", 1),
			Enabled:            envBool("LLM_ENABLED", true),
			PromptVersion:      env("LLM_PROMPT_VERSION", "market_advisor_v3_news"),
			HealthInterval:     envDuration("LLM_HEALTH_INTERVAL", 60*time.Second),
		},
		Risk: RiskConfig{
			MinLeverage:                    envInt("RISK_MIN_LEVERAGE", 5),
			MaxLeverage:                    envInt("RISK_MAX_LEVERAGE", 50),
			MaxRecommendedAllocPct:         envDecimal("MAX_RECOMMENDED_ALLOCATION_PCT", decimal.NewFromInt(15)),
			HighVolatilityATRPct:           envFloat("RISK_HIGH_VOLATILITY_ATR_PCT", 1.5),
			ExtremeVolatilityATRPct:        envFloat("RISK_EXTREME_VOLATILITY_ATR_PCT", 3.0),
			MinConfidence:                  envInt("RISK_MIN_CONFIDENCE", 55),
			RiskPerTradePct:                envFloat("RISK_PER_TRADE_PCT", 0.75),
			CriticalNewsMaxLeverage:        envInt("RISK_CRITICAL_NEWS_MAX_LEVERAGE", 15),
			CriticalNewsHighVolMaxLeverage: envInt("RISK_CRITICAL_NEWS_HIGH_VOL_MAX_LEVERAGE", 8),
			CriticalNewsMaxAge:             envDuration("RISK_CRITICAL_NEWS_MAX_AGE", 2*time.Hour),
		},
		Fees: FeesConfig{
			Exchange:      env("EXCHANGE", "bybit"),
			SlippagePct:   envDecimal("DEFAULT_SLIPPAGE_PCT", decimal.NewFromFloat(0.02)),
			FundingManual: envBool("FUNDING_MANUAL_ONLY", true),
		},
		Backtest: BacktestConfig{
			MaxInferences:            envInt("BACKTEST_MAX_INFERENCES", 2000),
			CacheEnabled:             envBool("BACKTEST_CACHE_ENABLED", true),
			DefaultSlippage:          envDecimal("BACKTEST_DEFAULT_SLIPPAGE_PCT", decimal.NewFromFloat(0.02)),
			DefaultFundingRate:       envDecimal("BACKTEST_DEFAULT_FUNDING_RATE_PCT", decimal.Zero),
			DefaultMaintenanceMargin: envDecimal("BACKTEST_DEFAULT_MAINTENANCE_MARGIN_PCT", decimal.Zero),
			DefaultMaxOpenPositions:  envInt("BACKTEST_DEFAULT_MAX_OPEN_POSITIONS", 1),
			MaxConcurrent:            envInt("BACKTEST_MAX_CONCURRENT", 1),
			DefaultInferencePause:    envDuration("BACKTEST_INFERENCE_PAUSE", 0),
			RunTimeout:               envDuration("BACKTEST_RUN_TIMEOUT", 12*time.Hour),
		},
		Logging: LoggingConfig{
			Level:  env("LOG_LEVEL", "info"),
			Format: env("LOG_FORMAT", "json"),
		},
	}

	// Fees are deliberately not defaulted to invented numbers. They stay
	// "unconfigured" until the user supplies them; accounting then flags every
	// derived value as approximate.
	maker, makerOK := envOptionalDecimal("DEFAULT_MAKER_FEE_PCT")
	taker, takerOK := envOptionalDecimal("DEFAULT_TAKER_FEE_PCT")
	cfg.Fees.MakerPct = maker
	cfg.Fees.TakerPct = taker
	cfg.Fees.Configured = makerOK && takerOK

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate checks invariants that would otherwise fail deep inside the app.
func (c *Config) Validate() error {
	var errs []error

	if c.HTTP.Port <= 0 || c.HTTP.Port > 65535 {
		errs = append(errs, fmt.Errorf("HTTP_PORT out of range: %d", c.HTTP.Port))
	}
	if c.Database.URL == "" {
		errs = append(errs, errors.New("DATABASE_URL is required"))
	}
	if c.Risk.MinLeverage < 1 || c.Risk.MaxLeverage > 125 || c.Risk.MinLeverage > c.Risk.MaxLeverage {
		errs = append(errs, fmt.Errorf("invalid leverage bounds %d..%d", c.Risk.MinLeverage, c.Risk.MaxLeverage))
	}
	if c.Risk.MaxRecommendedAllocPct.LessThanOrEqual(decimal.Zero) ||
		c.Risk.MaxRecommendedAllocPct.GreaterThan(decimal.NewFromInt(100)) {
		errs = append(errs, errors.New("MAX_RECOMMENDED_ALLOCATION_PCT must be in (0,100]"))
	}
	if c.LLM.Temperature < 0 || c.LLM.Temperature > 2 {
		errs = append(errs, fmt.Errorf("LLM_TEMPERATURE out of range: %v", c.LLM.Temperature))
	}
	if c.LLM.MaxConcurrent < 1 {
		errs = append(errs, errors.New("LLM_MAX_CONCURRENT_REQUESTS must be >= 1"))
	}
	if c.LLM.ContextSize < 4096 {
		errs = append(errs, errors.New("LLM_CONTEXT_SIZE must be >= 4096"))
	}
	if c.LLM.MaxTokens < 256 || c.LLM.MaxTokens > c.LLM.ContextSize-2048 {
		errs = append(errs, errors.New("LLM_MAX_TOKENS must leave at least 2048 tokens for the prompt"))
	}
	if c.Analysis.Interval < time.Minute {
		errs = append(errs, errors.New("ANALYSIS_INTERVAL must be >= 1m"))
	}
	if len(c.Analysis.Timeframes) == 0 {
		errs = append(errs, errors.New("ANALYSIS_TIMEFRAMES must not be empty"))
	}
	if c.CoinGecko.UniverseSize < 1 || c.CoinGecko.UniverseSize > 250 {
		errs = append(errs, errors.New("MARKET_UNIVERSE_SIZE must be in [1,250]"))
	}
	if c.Bybit.Enabled && (!strings.HasPrefix(c.Bybit.BaseURL, "https://") || c.Bybit.Timeout <= 0 || c.Bybit.RateLimitRPM < 1) {
		errs = append(errs, errors.New("invalid public Bybit market-data configuration"))
	}
	if c.Backtest.RunTimeout < time.Minute {
		errs = append(errs, errors.New("BACKTEST_RUN_TIMEOUT must be >= 1m"))
	}
	if c.Backtest.DefaultMaxOpenPositions < 1 || c.Backtest.DefaultMaxOpenPositions > 20 {
		errs = append(errs, errors.New("BACKTEST_DEFAULT_MAX_OPEN_POSITIONS must be in [1,20]"))
	}
	if c.Backtest.DefaultMaintenanceMargin.IsNegative() || c.Backtest.DefaultMaintenanceMargin.GreaterThanOrEqual(decimal.NewFromInt(100)) {
		errs = append(errs, errors.New("BACKTEST_DEFAULT_MAINTENANCE_MARGIN_PCT must be in [0,100)"))
	}
	if c.Backtest.DefaultFundingRate.Abs().GreaterThan(decimal.NewFromInt(5)) {
		errs = append(errs, errors.New("BACKTEST_DEFAULT_FUNDING_RATE_PCT must be in [-5,5]"))
	}
	if c.News.FetchInterval < time.Minute {
		errs = append(errs, errors.New("NEWS_FETCH_INTERVAL must be >= 1m"))
	}
	if c.News.FetchConcurrency < 1 || c.News.FetchConcurrency > 32 {
		errs = append(errs, errors.New("NEWS_FETCH_CONCURRENCY must be in [1,32]"))
	}
	if c.News.HTTPTimeout <= 0 {
		errs = append(errs, errors.New("NEWS_HTTP_TIMEOUT must be > 0"))
	}
	if c.News.MaxRetries < 0 || c.News.MaxRetries > 10 {
		errs = append(errs, errors.New("NEWS_MAX_RETRIES must be in [0,10]"))
	}
	if c.News.MaxResponseBytes < 64*1024 || c.News.MaxResponseBytes > 16*1024*1024 {
		errs = append(errs, errors.New("NEWS_MAX_RESPONSE_BYTES must be in [65536,16777216]"))
	}
	if c.News.LLMLookback <= 0 {
		errs = append(errs, errors.New("NEWS_LLM_LOOKBACK must be > 0"))
	}
	if c.News.LLMMaxAssetItems < 0 || c.News.LLMMaxAssetItems > 50 ||
		c.News.LLMMaxGlobalItems < 0 || c.News.LLMMaxGlobalItems > 50 {
		errs = append(errs, errors.New("NEWS LLM item limits must be in [0,50]"))
	}
	if c.News.ClusterTimeWindow <= 0 {
		errs = append(errs, errors.New("NEWS_CLUSTER_TIME_WINDOW must be > 0"))
	}
	if c.News.TitleSimilarityThreshold <= 0 || c.News.TitleSimilarityThreshold > 1 {
		errs = append(errs, errors.New("NEWS_TITLE_SIMILARITY_THRESHOLD must be in (0,1]"))
	}
	if c.News.HistoryMinSampleSize < 1 {
		errs = append(errs, errors.New("NEWS_HISTORY_MIN_SAMPLE_SIZE must be >= 1"))
	}
	if c.News.ReactionInterval < time.Minute {
		errs = append(errs, errors.New("NEWS_REACTION_INTERVAL must be >= 1m"))
	}
	if c.News.ReactionBaselineGrace < 5*time.Minute {
		errs = append(errs, errors.New("NEWS_REACTION_BASELINE_GRACE must be >= 5m"))
	}
	if c.Risk.CriticalNewsMaxLeverage < c.Risk.MinLeverage || c.Risk.CriticalNewsMaxLeverage > c.Risk.MaxLeverage {
		errs = append(errs, errors.New("RISK_CRITICAL_NEWS_MAX_LEVERAGE must be within configured leverage bounds"))
	}
	if c.Risk.CriticalNewsHighVolMaxLeverage < c.Risk.MinLeverage || c.Risk.CriticalNewsHighVolMaxLeverage > c.Risk.CriticalNewsMaxLeverage {
		errs = append(errs, errors.New("RISK_CRITICAL_NEWS_HIGH_VOL_MAX_LEVERAGE must be between minimum and critical-news cap"))
	}
	if c.Risk.CriticalNewsMaxAge <= 0 {
		errs = append(errs, errors.New("RISK_CRITICAL_NEWS_MAX_AGE must be > 0"))
	}
	if c.Fees.Configured {
		if c.Fees.MakerPct.LessThan(decimal.Zero) || c.Fees.TakerPct.LessThan(decimal.Zero) {
			errs = append(errs, errors.New("fee percentages must not be negative"))
		}
	}
	return errors.Join(errs...)
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func envInt(key string, def int) int {
	raw := env(key, "")
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return v
}

func envFloat(key string, def float64) float64 {
	raw := env(key, "")
	if raw == "" {
		return def
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return def
	}
	return v
}

func envBool(key string, def bool) bool {
	raw := env(key, "")
	if raw == "" {
		return def
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return def
	}
	return v
}

func envDuration(key string, def time.Duration) time.Duration {
	raw := env(key, "")
	if raw == "" {
		return def
	}
	if v, err := time.ParseDuration(raw); err == nil {
		return v
	}
	// Bare numbers are interpreted as seconds for operator convenience.
	if secs, err := strconv.Atoi(raw); err == nil {
		return time.Duration(secs) * time.Second
	}
	return def
}

func envList(key string, def []string) []string {
	raw := env(key, "")
	if raw == "" {
		return def
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return def
	}
	return out
}

func envDecimal(key string, def decimal.Decimal) decimal.Decimal {
	if v, ok := envOptionalDecimal(key); ok {
		return v
	}
	return def
}

func envOptionalDecimal(key string) (decimal.Decimal, bool) {
	raw := env(key, "")
	if raw == "" {
		return decimal.Zero, false
	}
	v, err := decimal.NewFromString(raw)
	if err != nil {
		return decimal.Zero, false
	}
	return v, true
}
