package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// EntryPlan describes where the user should enter.
type EntryPlan struct {
	Type         string   `json:"type"` // market | zone | market_or_zone | limit
	CurrentPrice float64  `json:"current_price"`
	PreferredMin *float64 `json:"preferred_min,omitempty"`
	PreferredMax *float64 `json:"preferred_max,omitempty"`
}

// PriceTarget is one take-profit or stop-loss step.
type PriceTarget struct {
	Price    float64 `json:"price"`
	ClosePct float64 `json:"close_pct"`
	Reason   string  `json:"reason,omitempty"`
}

// LeveragePlan carries both the model suggestion and the risk-adjusted value.
type LeveragePlan struct {
	LLMSuggested int    `json:"llm_suggested"`
	RiskMaximum  int    `json:"risk_maximum"`
	Recommended  int    `json:"recommended"`
	Reason       string `json:"reason,omitempty"`
	RiskReason   string `json:"risk_reason,omitempty"`
}

// ManagementAction is one requested change to an existing position.
type ManagementAction struct {
	Type          ManagementActionType `json:"type"`
	NewStopLoss   *float64             `json:"new_stop_loss,omitempty"`
	NewTakeProfit []PriceTarget        `json:"new_take_profit,omitempty"`
	ClosePct      *float64             `json:"close_pct,omitempty"`
	Reason        string               `json:"reason,omitempty"`
}

// ManagementPlan groups management actions for one position.
type ManagementPlan struct {
	PositionID uuid.UUID          `json:"position_id"`
	Actions    []ManagementAction `json:"actions"`
}

// RecommendationNarrative contains every human-readable field produced by the
// model. A recommendation stores one narrative for each supported UI language;
// prices, actions and other machine fields remain shared.
type RecommendationNarrative struct {
	Summary           string   `json:"summary"`
	LeverageReason    string   `json:"leverage_reason,omitempty"`
	TakeProfitReasons []string `json:"take_profit_reasons"`
	StopLossReasons   []string `json:"stop_loss_reasons"`
	ManagementReasons []string `json:"management_reasons"`
	SignalsFor        []string `json:"signals_for"`
	SignalsAgainst    []string `json:"signals_against"`
	Invalidation      []string `json:"invalidation_conditions"`
}

// TradePlan is the mutable part of a position's plan (TP/SL levels).
type TradePlan struct {
	TakeProfit []PriceTarget `json:"take_profit"`
	StopLoss   []PriceTarget `json:"stop_loss"`
	UpdatedAt  time.Time     `json:"updated_at"`
	Note       string        `json:"note,omitempty"`
}

// Recommendation is the final, risk-adjusted advisory shown to the user.
// Rows are immutable once written: decisions and outcomes live in other tables.
type Recommendation struct {
	ID              uuid.UUID                          `json:"id"`
	AnalysisRunID   *uuid.UUID                         `json:"analysis_run_id,omitempty"`
	AssetID         int64                              `json:"asset_id"`
	Symbol          string                             `json:"symbol"`
	CreatedAt       time.Time                          `json:"created_at"`
	DismissedAt     *time.Time                         `json:"dismissed_at,omitempty"`
	Action          RecommendationAction               `json:"action"`
	Confidence      int                                `json:"confidence"`
	RiskLevel       RiskLevel                          `json:"risk_level"`
	Summary         string                             `json:"summary"`
	ReferencePrice  decimal.Decimal                    `json:"reference_price"`
	AllocationPct   decimal.Decimal                    `json:"recommended_allocation_pct"`
	Leverage        LeveragePlan                       `json:"leverage"`
	Entry           *EntryPlan                         `json:"entry,omitempty"`
	TakeProfit      []PriceTarget                      `json:"take_profit"`
	StopLoss        []PriceTarget                      `json:"stop_loss"`
	Management      *ManagementPlan                    `json:"management,omitempty"`
	SignalsFor      []string                           `json:"signals_for"`
	SignalsAgainst  []string                           `json:"signals_against"`
	Invalidation    []string                           `json:"invalidation_conditions"`
	ModelName       string                             `json:"model_name"`
	PromptVersion   string                             `json:"prompt_version"`
	SchemaVersion   int                                `json:"schema_version"`
	MarketRegime    MarketRegime                       `json:"market_regime"`
	DataQuality     DataQualityStatus                  `json:"data_quality"`
	RiskEngineNotes []string                           `json:"risk_engine_notes,omitempty"`
	Translations    map[string]RecommendationNarrative `json:"translations,omitempty"`
	NewsAssessment  *NewsAssessment                    `json:"news_assessment,omitempty"`
}

// Freshness classifies how old the recommendation is relative to staleAfter.
func (r Recommendation) Freshness(now time.Time, staleAfter time.Duration) RecommendationFreshness {
	if r.DataQuality != DataQualityOK {
		return FreshnessIncomplete
	}
	if now.Sub(r.CreatedAt) > staleAfter {
		return FreshnessStale
	}
	return FreshnessFresh
}

// Decision records what the user did with a recommendation.
type Decision struct {
	RecommendationID uuid.UUID    `json:"recommendation_id"`
	Decision         UserDecision `json:"decision"`
	LinkedPositionID *uuid.UUID   `json:"linked_position_id,omitempty"`
	DecidedAt        time.Time    `json:"decided_at"`
	Note             string       `json:"note,omitempty"`
}

// Outcome is what the market actually did after a recommendation.
type Outcome struct {
	RecommendationID uuid.UUID     `json:"recommendation_id"`
	EvaluatedAt      time.Time     `json:"evaluated_at"`
	Finalized        bool          `json:"finalized"`
	PriceAfter5m     *float64      `json:"price_after_5m,omitempty"`
	PriceAfter15m    *float64      `json:"price_after_15m,omitempty"`
	PriceAfter1h     *float64      `json:"price_after_1h,omitempty"`
	PriceAfter4h     *float64      `json:"price_after_4h,omitempty"`
	PriceAfter24h    *float64      `json:"price_after_24h,omitempty"`
	MFEPct           *float64      `json:"max_favorable_excursion_pct,omitempty"`
	MAEPct           *float64      `json:"max_adverse_excursion_pct,omitempty"`
	FirstTPHitIndex  *int          `json:"first_tp_hit_index,omitempty"`
	FirstSLHitIndex  *int          `json:"first_sl_hit_index,omitempty"`
	Status           OutcomeStatus `json:"status"`
	Ambiguous        bool          `json:"ambiguous"`
	AmbiguityReason  string        `json:"ambiguity_reason,omitempty"`
	Result           TradeResult   `json:"result,omitempty"`
}

// InferenceStatus is the terminal state of one LLM call.
type InferenceStatus string

// Inference statuses.
const (
	InferenceOK             InferenceStatus = "ok"
	InferenceRepaired       InferenceStatus = "repaired"
	InferenceInvalid        InferenceStatus = "invalid_response"
	InferenceTransportError InferenceStatus = "transport_error"
	InferenceTimeout        InferenceStatus = "timeout"
	InferenceEmpty          InferenceStatus = "empty_response"
	InferenceCached         InferenceStatus = "cached"
	InferenceDisabled       InferenceStatus = "llm_disabled"
)

// InferenceRecord is the persisted trace of one LLM call.
type InferenceRecord struct {
	ID               uuid.UUID  `json:"id"`
	RecommendationID *uuid.UUID `json:"recommendation_id,omitempty"`
	AnalysisRunID    *uuid.UUID `json:"analysis_run_id,omitempty"`
	// BacktestRunID is set for the inferences a replay paid for, which is what
	// makes the answers of one run recoverable as a set afterwards.
	BacktestRunID    *uuid.UUID      `json:"backtest_run_id,omitempty"`
	Symbol           string          `json:"symbol"`
	CreatedAt        time.Time       `json:"created_at"`
	ModelName        string          `json:"model_name"`
	PromptVersion    string          `json:"prompt_version"`
	SchemaVersion    int             `json:"schema_version"`
	CacheKey         *string         `json:"cache_key,omitempty"`
	Input            any             `json:"llm_input,omitempty"`
	RawOutput        string          `json:"llm_raw_output,omitempty"`
	ParsedOutput     any             `json:"parsed_output,omitempty"`
	Status           InferenceStatus `json:"status"`
	ErrorMessage     string          `json:"error_message,omitempty"`
	RepairAttempted  bool            `json:"repair_attempted"`
	LatencyMS        int             `json:"latency_ms"`
	PromptTokens     *int            `json:"prompt_tokens,omitempty"`
	CompletionTokens *int            `json:"completion_tokens,omitempty"`
}

// InferenceContextUsage reports the token footprint of recent inferences as the
// LLM server itself measured it.
type InferenceContextUsage struct {
	Samples          int        `json:"samples"`
	LastPromptTokens int        `json:"last_prompt_tokens"`
	LastOutputTokens int        `json:"last_output_tokens"`
	LastAt           *time.Time `json:"last_at,omitempty"`
	PeakPromptTokens int        `json:"peak_prompt_tokens"`
	PeakAt           *time.Time `json:"peak_at,omitempty"`
}
