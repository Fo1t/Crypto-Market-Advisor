// Package settings persists the UI-editable configuration and applies it to
// the running services.
//
// Environment variables provide the defaults; anything the user changes in the
// Settings screen is stored in the database and re-applied on every start, so a
// restart never silently reverts a deliberate change.
package settings

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"github.com/crypto-market-advisor/advisor/internal/analysis/strategies"
	"github.com/crypto-market-advisor/advisor/internal/config"
	"github.com/crypto-market-advisor/advisor/internal/domain"
	"github.com/crypto-market-advisor/advisor/internal/logging"
	"github.com/crypto-market-advisor/advisor/internal/repository"
)

const storageKey = "app_settings_v1"

// General holds interface and scheduling preferences.
type General struct {
	Language          string   `json:"language"`
	AnalysisIntervalS int      `json:"analysis_interval_seconds"`
	Timeframes        []string `json:"timeframes"`
	AnalysisEnabled   bool     `json:"analysis_enabled"`
}

// LLM holds the inference server settings.
type LLM struct {
	BaseURL       string  `json:"base_url"`
	Model         string  `json:"model"`
	TimeoutS      int     `json:"timeout_seconds"`
	Temperature   float64 `json:"temperature"`
	MaxTokens     int     `json:"max_tokens"`
	ContextSize   int     `json:"context_size"`
	Enabled       bool    `json:"enabled"`
	MaxConcurrent int     `json:"max_concurrent_requests"`
	PromptVersion string  `json:"prompt_version"`
}

// Risk holds the risk engine bounds.
type Risk struct {
	MinLeverage      int             `json:"min_leverage"`
	MaxLeverage      int             `json:"max_leverage"`
	MaxAllocationPct decimal.Decimal `json:"max_recommended_allocation_pct"`
	// RiskPerTradePct is the share of capital a stop-out may cost. Above zero it
	// derives the advised size from the stop distance instead of using a fixed
	// share, so a quiet asset and a volatile one carry the same risk.
	//
	// It is a pointer because zero is a meaningful choice - it restores the fixed
	// allocation - and a document stored before the field existed has to be told
	// apart from a user who deliberately switched the rule off.
	RiskPerTradePct                *float64 `json:"risk_per_trade_pct"`
	HighVolatilityATRPct           float64  `json:"high_volatility_atr_pct"`
	ExtremeVolatilityATRPct        float64  `json:"extreme_volatility_atr_pct"`
	MinConfidence                  int      `json:"min_confidence"`
	CriticalNewsMaxLeverage        int      `json:"critical_news_max_leverage"`
	CriticalNewsHighVolMaxLeverage int      `json:"critical_news_high_vol_max_leverage"`
	CriticalNewsMaxAgeS            int      `json:"critical_news_max_age_seconds"`
}

// News holds the user-facing controls for ingestion and analysis context. The
// lower-level transport safety limits remain environment-only by design.
type News struct {
	Enabled              bool `json:"enabled"`
	FetchIntervalS       int  `json:"fetch_interval_seconds"`
	LLMLookbackHours     int  `json:"llm_lookback_hours"`
	LLMMaxAssetItems     int  `json:"llm_max_asset_items"`
	LLMMaxGlobalItems    int  `json:"llm_max_global_items"`
	HistoryMinSampleSize int  `json:"history_min_sample_size"`
	BybitEnabled         bool `json:"bybit_enabled"`
}

// Exchange holds the fee profile. There is no exchange integration: these
// values are entered by the user and are used for accounting only.
type Exchange struct {
	Exchange     string           `json:"exchange"`
	MakerFeePct  *decimal.Decimal `json:"maker_fee_pct"`
	TakerFeePct  *decimal.Decimal `json:"taker_fee_pct"`
	SlippagePct  decimal.Decimal  `json:"slippage_pct"`
	FeesVerified bool             `json:"fees_configured"`
}

// Settings is the complete editable configuration.
type Settings struct {
	General    General            `json:"general"`
	LLM        LLM                `json:"llm"`
	Risk       Risk               `json:"risk"`
	News       News               `json:"news"`
	Exchange   Exchange           `json:"exchange"`
	Strategies domain.StrategySet `json:"strategies"`
	UpdatedAt  time.Time          `json:"updated_at"`
}

// Applier is implemented by services that accept live configuration changes.
type Applier interface {
	ApplySettings(s Settings) error
}

// Service loads, stores and distributes settings.
type Service struct {
	repos    *repository.Repositories
	base     config.Config
	log      *slog.Logger
	appliers []Applier

	mu      sync.RWMutex
	current Settings
}

// NewService builds the settings service seeded from environment configuration.
func NewService(repos *repository.Repositories, base config.Config, logger *slog.Logger) *Service {
	s := &Service{repos: repos, base: base, log: logging.For(logger, logging.CategorySystem)}
	s.current = fromConfig(base)
	return s
}

// Register adds a service that wants to be notified about changes.
func (s *Service) Register(a Applier) { s.appliers = append(s.appliers, a) }

// Load reads stored settings and applies them, creating defaults on first run.
func (s *Service) Load(ctx context.Context) error {
	var stored Settings
	found, err := s.repos.Settings.Get(ctx, storageKey, &stored)
	if err != nil {
		return fmt.Errorf("load settings: %w", err)
	}
	if found && s.base.App.SettingsFromEnv {
		s.log.Info("SETTINGS_FROM_ENV is set: re-seeding stored settings from the environment")
		found = false
	}
	if !found {
		defaults := fromConfig(s.base)
		defaults.UpdatedAt = time.Now().UTC()
		if err := s.repos.Settings.Put(ctx, storageKey, defaults); err != nil {
			return fmt.Errorf("store default settings: %w", err)
		}
		s.set(defaults)
		return s.apply(defaults)
	}

	stored = normalize(stored, fromConfig(s.base))
	s.set(stored)
	return s.apply(stored)
}

// Current returns the active settings.
func (s *Service) Current() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current
}

// Update validates, stores and applies a new settings document.
func (s *Service) Update(ctx context.Context, next Settings) (Settings, error) {
	if err := validate(next); err != nil {
		return Settings{}, err
	}
	next.UpdatedAt = time.Now().UTC()
	next.Exchange.FeesVerified = next.Exchange.MakerFeePct != nil && next.Exchange.TakerFeePct != nil

	if err := s.repos.Settings.Put(ctx, storageKey, next); err != nil {
		return Settings{}, fmt.Errorf("store settings: %w", err)
	}
	s.set(next)
	if err := s.apply(next); err != nil {
		return Settings{}, err
	}
	s.log.Info("settings updated")
	return next, nil
}

// FeesConfig projects the exchange settings into the accounting configuration.
func (s *Service) FeesConfig() config.FeesConfig {
	cur := s.Current()
	out := config.FeesConfig{
		Exchange:      cur.Exchange.Exchange,
		SlippagePct:   cur.Exchange.SlippagePct,
		FundingManual: true,
	}
	if cur.Exchange.MakerFeePct != nil {
		out.MakerPct = *cur.Exchange.MakerFeePct
	}
	if cur.Exchange.TakerFeePct != nil {
		out.TakerPct = *cur.Exchange.TakerFeePct
	}
	out.Configured = cur.Exchange.MakerFeePct != nil && cur.Exchange.TakerFeePct != nil
	return out
}

// AnalysisConfig projects the editable scheduling controls over the
// environment-owned analysis settings.
func (s *Service) AnalysisConfig() config.AnalysisConfig {
	cur := s.Current()
	out := s.base.Analysis
	out.Enabled = cur.General.AnalysisEnabled
	if cur.General.AnalysisIntervalS > 0 {
		out.Interval = time.Duration(cur.General.AnalysisIntervalS) * time.Second
	}
	if len(cur.General.Timeframes) > 0 {
		out.Timeframes = cur.General.Timeframes
	}
	return out
}

// StrategySet returns the active deterministic decision policy.
func (s *Service) StrategySet() domain.StrategySet {
	return strategies.Normalize(s.Current().Strategies)
}

// RiskConfig projects the risk settings into the engine configuration.
func (s *Service) RiskConfig() config.RiskConfig {
	cur := s.Current()
	return config.RiskConfig{
		MinLeverage:                    cur.Risk.MinLeverage,
		MaxLeverage:                    cur.Risk.MaxLeverage,
		MaxRecommendedAllocPct:         cur.Risk.MaxAllocationPct,
		RiskPerTradePct:                floatOr(cur.Risk.RiskPerTradePct, 0),
		HighVolatilityATRPct:           cur.Risk.HighVolatilityATRPct,
		ExtremeVolatilityATRPct:        cur.Risk.ExtremeVolatilityATRPct,
		MinConfidence:                  cur.Risk.MinConfidence,
		CriticalNewsMaxLeverage:        cur.Risk.CriticalNewsMaxLeverage,
		CriticalNewsHighVolMaxLeverage: cur.Risk.CriticalNewsHighVolMaxLeverage,
		CriticalNewsMaxAge:             time.Duration(cur.Risk.CriticalNewsMaxAgeS) * time.Second,
	}
}

// NewsConfig projects editable news controls over environment-owned transport
// and clustering limits.
func (s *Service) NewsConfig() config.NewsConfig {
	cur := s.Current()
	out := s.base.News
	out.Enabled = cur.News.Enabled
	out.FetchInterval = time.Duration(cur.News.FetchIntervalS) * time.Second
	out.LLMLookback = time.Duration(cur.News.LLMLookbackHours) * time.Hour
	out.LLMMaxAssetItems = cur.News.LLMMaxAssetItems
	out.LLMMaxGlobalItems = cur.News.LLMMaxGlobalItems
	out.HistoryMinSampleSize = cur.News.HistoryMinSampleSize
	out.BybitEnabled = cur.News.BybitEnabled
	return out
}

// LLMConfig projects the LLM settings into the client configuration.
func (s *Service) LLMConfig() config.LLMConfig {
	cur := s.Current()
	out := s.base.LLM
	out.BaseURL = cur.LLM.BaseURL
	out.Model = cur.LLM.Model
	out.Timeout = time.Duration(cur.LLM.TimeoutS) * time.Second
	out.Temperature = cur.LLM.Temperature
	out.MaxTokens = cur.LLM.MaxTokens
	out.ContextSize = cur.LLM.ContextSize
	out.Enabled = cur.LLM.Enabled
	out.MaxConcurrent = cur.LLM.MaxConcurrent
	out.PromptVersion = cur.LLM.PromptVersion
	return out
}

func (s *Service) set(v Settings) {
	s.mu.Lock()
	s.current = v
	s.mu.Unlock()
}

func (s *Service) apply(v Settings) error {
	for _, a := range s.appliers {
		if err := a.ApplySettings(v); err != nil {
			return fmt.Errorf("apply settings: %w", err)
		}
	}
	return nil
}

func fromConfig(cfg config.Config) Settings {
	out := Settings{
		Strategies: strategies.DefaultSet(),
		General: General{
			Language:          "en",
			AnalysisIntervalS: int(cfg.Analysis.Interval.Seconds()),
			Timeframes:        cfg.Analysis.Timeframes,
			AnalysisEnabled:   cfg.Analysis.Enabled,
		},
		LLM: LLM{
			BaseURL:       cfg.LLM.BaseURL,
			Model:         cfg.LLM.Model,
			TimeoutS:      int(cfg.LLM.Timeout.Seconds()),
			Temperature:   cfg.LLM.Temperature,
			MaxTokens:     cfg.LLM.MaxTokens,
			ContextSize:   cfg.LLM.ContextSize,
			Enabled:       cfg.LLM.Enabled,
			MaxConcurrent: cfg.LLM.MaxConcurrent,
			PromptVersion: cfg.LLM.PromptVersion,
		},
		Risk: Risk{
			MinLeverage:                    cfg.Risk.MinLeverage,
			MaxLeverage:                    cfg.Risk.MaxLeverage,
			MaxAllocationPct:               cfg.Risk.MaxRecommendedAllocPct,
			RiskPerTradePct:                &cfg.Risk.RiskPerTradePct,
			HighVolatilityATRPct:           cfg.Risk.HighVolatilityATRPct,
			ExtremeVolatilityATRPct:        cfg.Risk.ExtremeVolatilityATRPct,
			MinConfidence:                  cfg.Risk.MinConfidence,
			CriticalNewsMaxLeverage:        cfg.Risk.CriticalNewsMaxLeverage,
			CriticalNewsHighVolMaxLeverage: cfg.Risk.CriticalNewsHighVolMaxLeverage,
			CriticalNewsMaxAgeS:            int(cfg.Risk.CriticalNewsMaxAge.Seconds()),
		},
		News: News{
			Enabled:              cfg.News.Enabled,
			FetchIntervalS:       int(cfg.News.FetchInterval.Seconds()),
			LLMLookbackHours:     int(cfg.News.LLMLookback.Hours()),
			LLMMaxAssetItems:     cfg.News.LLMMaxAssetItems,
			LLMMaxGlobalItems:    cfg.News.LLMMaxGlobalItems,
			HistoryMinSampleSize: cfg.News.HistoryMinSampleSize,
			BybitEnabled:         cfg.News.BybitEnabled,
		},
		Exchange: Exchange{
			Exchange:     cfg.Fees.Exchange,
			SlippagePct:  cfg.Fees.SlippagePct,
			FeesVerified: cfg.Fees.Configured,
		},
	}
	if cfg.Fees.Configured {
		maker, taker := cfg.Fees.MakerPct, cfg.Fees.TakerPct
		out.Exchange.MakerFeePct = &maker
		out.Exchange.TakerFeePct = &taker
	}
	return out
}

func validate(s Settings) error {
	if err := strategies.Validate(s.Strategies); err != nil {
		return err
	}
	if s.Risk.MinLeverage < 1 || s.Risk.MaxLeverage > 125 || s.Risk.MinLeverage > s.Risk.MaxLeverage {
		return fmt.Errorf("invalid leverage bounds %d..%d", s.Risk.MinLeverage, s.Risk.MaxLeverage)
	}
	if s.Risk.MaxAllocationPct.LessThanOrEqual(decimal.Zero) || s.Risk.MaxAllocationPct.GreaterThan(decimal.NewFromInt(100)) {
		return fmt.Errorf("max allocation must be in (0,100]")
	}
	if s.Risk.RiskPerTradePct != nil && (*s.Risk.RiskPerTradePct < 0 || *s.Risk.RiskPerTradePct > 20) {
		return fmt.Errorf("risk per trade must be in [0,20]")
	}
	if s.Risk.MinConfidence < 0 || s.Risk.MinConfidence > 100 {
		return fmt.Errorf("min confidence must be in [0,100]")
	}
	if s.Risk.CriticalNewsMaxLeverage < s.Risk.MinLeverage || s.Risk.CriticalNewsMaxLeverage > s.Risk.MaxLeverage {
		return fmt.Errorf("critical news leverage must be inside the configured leverage bounds")
	}
	if s.Risk.CriticalNewsHighVolMaxLeverage < s.Risk.MinLeverage || s.Risk.CriticalNewsHighVolMaxLeverage > s.Risk.CriticalNewsMaxLeverage {
		return fmt.Errorf("high-volatility critical news leverage must not exceed the critical-news limit")
	}
	if s.Risk.CriticalNewsMaxAgeS < 60 {
		return fmt.Errorf("critical news max age must be at least 60 seconds")
	}
	if s.News.FetchIntervalS < 60 {
		return fmt.Errorf("news fetch interval must be at least 60 seconds")
	}
	if s.News.LLMLookbackHours < 1 || s.News.LLMLookbackHours > 168 {
		return fmt.Errorf("news lookback must be between 1 and 168 hours")
	}
	if s.News.LLMMaxAssetItems < 1 || s.News.LLMMaxAssetItems > 100 || s.News.LLMMaxGlobalItems < 1 || s.News.LLMMaxGlobalItems > 100 {
		return fmt.Errorf("news item limits must be between 1 and 100")
	}
	if s.News.HistoryMinSampleSize < 1 || s.News.HistoryMinSampleSize > 1000 {
		return fmt.Errorf("news history sample size must be between 1 and 1000")
	}
	if s.LLM.Temperature < 0 || s.LLM.Temperature > 2 {
		return fmt.Errorf("temperature must be in [0,2]")
	}
	if s.LLM.MaxConcurrent < 1 {
		return fmt.Errorf("max concurrent requests must be at least 1")
	}
	if s.LLM.ContextSize < 4096 {
		return fmt.Errorf("LLM context size must be at least 4096 tokens")
	}
	if s.LLM.MaxTokens < 256 || s.LLM.MaxTokens > s.LLM.ContextSize-2048 {
		return fmt.Errorf("LLM max output tokens must leave at least 2048 tokens for the prompt")
	}
	if s.LLM.TimeoutS < 5 {
		return fmt.Errorf("llm timeout must be at least 5 seconds")
	}
	if s.General.AnalysisIntervalS < 60 {
		return fmt.Errorf("analysis interval must be at least 60 seconds")
	}
	if len(s.General.Timeframes) == 0 {
		return fmt.Errorf("at least one timeframe must be enabled")
	}
	if _, err := domain.ParseTimeframes(s.General.Timeframes); err != nil {
		return err
	}
	switch s.General.Language {
	case "ru", "en", "zh-CN":
	default:
		return fmt.Errorf("unsupported language %q", s.General.Language)
	}
	for _, fee := range []*decimal.Decimal{s.Exchange.MakerFeePct, s.Exchange.TakerFeePct} {
		if fee != nil && (fee.IsNegative() || fee.GreaterThan(decimal.NewFromInt(5))) {
			return fmt.Errorf("fee percentages must be between 0 and 5")
		}
	}
	return nil
}

// normalize fills fields introduced after app_settings_v1 was first shipped.
// A zero duration/count cannot be a valid user value, so it is an unambiguous
// marker for an older stored document.
// floatOr dereferences an optional number.
func floatOr(value *float64, fallback float64) float64 {
	if value == nil {
		return fallback
	}
	return *value
}

func normalize(stored, defaults Settings) Settings {
	if stored.Risk.CriticalNewsMaxLeverage == 0 {
		stored.Risk.CriticalNewsMaxLeverage = defaults.Risk.CriticalNewsMaxLeverage
	}
	if stored.Risk.CriticalNewsHighVolMaxLeverage == 0 {
		stored.Risk.CriticalNewsHighVolMaxLeverage = defaults.Risk.CriticalNewsHighVolMaxLeverage
	}
	if stored.Risk.CriticalNewsMaxAgeS == 0 {
		stored.Risk.CriticalNewsMaxAgeS = defaults.Risk.CriticalNewsMaxAgeS
	}
	// A document stored before risk-based sizing existed adopts the configured
	// default; one that carries an explicit zero keeps it.
	if stored.Risk.RiskPerTradePct == nil {
		stored.Risk.RiskPerTradePct = defaults.Risk.RiskPerTradePct
	}
	if stored.News.FetchIntervalS == 0 {
		stored.News = defaults.News
	}
	if stored.LLM.PromptVersion == "" || stored.LLM.PromptVersion == "market_advisor_v2_multilingual" {
		stored.LLM.PromptVersion = defaults.LLM.PromptVersion
	}
	if stored.LLM.ContextSize == 0 {
		stored.LLM.ContextSize = defaults.LLM.ContextSize
	}
	// A stored policy from an older version gains the strategies added since and
	// loses the ones that no longer exist, keeping the user's own edits.
	stored.Strategies = strategies.Normalize(stored.Strategies)
	return stored
}
