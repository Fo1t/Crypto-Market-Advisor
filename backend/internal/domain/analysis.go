package domain

import (
	"time"

	"github.com/google/uuid"
)

// SchemaVersion is the version of the feature snapshot / LLM contract.
const SchemaVersion = 1

// Pattern is a detected candlestick or chart formation.
type Pattern struct {
	Name        string           `json:"name"`
	Kind        string           `json:"kind"` // "candlestick" or "chart"
	Direction   PatternDirection `json:"direction"`
	Strength    float64          `json:"strength"`
	CandleIndex int              `json:"candle_index"`
	AgeCandles  int              `json:"age_candles"`
	Timeframe   Timeframe        `json:"timeframe,omitempty"`
	Target      *float64         `json:"target,omitempty"`
	Note        string           `json:"note,omitempty"`
}

// Level is a support or resistance price level.
type Level struct {
	Price       float64   `json:"price"`
	Type        LevelType `json:"type"`
	Strength    float64   `json:"strength"`
	Touches     int       `json:"touches"`
	DistancePct float64   `json:"distance_pct"`
	Origin      string    `json:"origin"`
	Timeframe   Timeframe `json:"timeframe,omitempty"`
}

// Divergence is a price/indicator disagreement.
type Divergence struct {
	Indicator  string           `json:"indicator"`
	Type       string           `json:"type"` // regular | hidden
	Direction  PatternDirection `json:"direction"`
	Strength   float64          `json:"strength"`
	FromIndex  int              `json:"from_index"`
	ToIndex    int              `json:"to_index"`
	AgeCandles int              `json:"age_candles"`
	Timeframe  Timeframe        `json:"timeframe,omitempty"`
}

// SwingPoint is a confirmed pivot high or low.
type SwingPoint struct {
	Index          int       `json:"index"`
	Time           time.Time `json:"time"`
	Price          float64   `json:"price"`
	IsHigh         bool      `json:"is_high"`
	Label          string    `json:"label"` // HH, HL, LH, LL
	ConfirmedAfter int       `json:"confirmed_after_candles"`
}

// StructureEvent is a break of structure or change of character.
type StructureEvent struct {
	Type       string    `json:"type"` // BOS | CHoCH
	Direction  Direction `json:"direction"`
	Price      float64   `json:"price"`
	Time       time.Time `json:"time"`
	AgeCandles int       `json:"age_candles"`
}

// MarketStructure summarises swing analysis for one timeframe.
type MarketStructure struct {
	State       StructureState   `json:"state"`
	Swings      []SwingPoint     `json:"swings,omitempty"`
	LastHigh    *float64         `json:"last_high,omitempty"`
	LastLow     *float64         `json:"last_low,omitempty"`
	Events      []StructureEvent `json:"events,omitempty"`
	Description string           `json:"description,omitempty"`
}

// Regime is the deterministic market classification.
type Regime struct {
	Primary MarketRegime `json:"primary"`
	Tags    []RegimeTag  `json:"tags,omitempty"`
	Score   float64      `json:"score"`
}

// Indicators holds the numeric technical state of one timeframe.
// Pointers mark values that could not be computed from the available history.
type Indicators struct {
	SMA map[string]float64 `json:"sma,omitempty"`
	EMA map[string]float64 `json:"ema,omitempty"`

	RSI       *float64 `json:"rsi,omitempty"`
	RSIState  string   `json:"rsi_state,omitempty"`
	RSISlope  *float64 `json:"rsi_slope,omitempty"`
	StochRSIK *float64 `json:"stoch_rsi_k,omitempty"`
	StochRSID *float64 `json:"stoch_rsi_d,omitempty"`
	StochK    *float64 `json:"stoch_k,omitempty"`
	StochD    *float64 `json:"stoch_d,omitempty"`
	ROC       *float64 `json:"roc,omitempty"`
	Momentum  *float64 `json:"momentum,omitempty"`
	CCI       *float64 `json:"cci,omitempty"`
	WilliamsR *float64 `json:"williams_r,omitempty"`

	MACD          *float64 `json:"macd,omitempty"`
	MACDSignal    *float64 `json:"macd_signal,omitempty"`
	MACDHistogram *float64 `json:"macd_hist,omitempty"`
	MACDState     string   `json:"macd_state,omitempty"`

	ADX           *float64 `json:"adx,omitempty"`
	PlusDI        *float64 `json:"plus_di,omitempty"`
	MinusDI       *float64 `json:"minus_di,omitempty"`
	TrendStrength string   `json:"trend_strength,omitempty"`
	AroonUp       *float64 `json:"aroon_up,omitempty"`
	AroonDown     *float64 `json:"aroon_down,omitempty"`

	ATR           *float64 `json:"atr,omitempty"`
	ATRPct        *float64 `json:"atr_pct,omitempty"`
	ATRPercentile *float64 `json:"atr_percentile,omitempty"`
	BBUpper       *float64 `json:"bb_upper,omitempty"`
	BBMiddle      *float64 `json:"bb_middle,omitempty"`
	BBLower       *float64 `json:"bb_lower,omitempty"`
	BBWidth       *float64 `json:"bb_width,omitempty"`
	BBPercentB    *float64 `json:"bb_percent_b,omitempty"`
	KeltnerUpper  *float64 `json:"keltner_upper,omitempty"`
	KeltnerLower  *float64 `json:"keltner_lower,omitempty"`
	RealizedVol   *float64 `json:"realized_volatility,omitempty"`
	VolPercentile *float64 `json:"volatility_percentile,omitempty"`

	Volume           *float64 `json:"volume,omitempty"`
	VolumeSMA        *float64 `json:"volume_sma,omitempty"`
	RelativeVolume   *float64 `json:"relative_volume,omitempty"`
	VolumePercentile *float64 `json:"volume_percentile,omitempty"`
	OBV              *float64 `json:"obv,omitempty"`
	OBVSlope         *float64 `json:"obv_slope,omitempty"`
	MFI              *float64 `json:"mfi,omitempty"`
	VWAP             *float64 `json:"vwap,omitempty"`
	CMF              *float64 `json:"cmf,omitempty"`

	PriceVsEMA50Pct  *float64 `json:"price_vs_ema_50_pct,omitempty"`
	PriceVsEMA200Pct *float64 `json:"price_vs_ema_200_pct,omitempty"`
	PriceVsVWAPPct   *float64 `json:"price_vs_vwap_pct,omitempty"`
	DistFromHighPct  *float64 `json:"distance_from_high_pct,omitempty"`
	DistFromLowPct   *float64 `json:"distance_from_low_pct,omitempty"`
}

// SignalScores are the deterministic scores computed without the LLM.
// They let the LLM be compared against a pure technical baseline later on.
type SignalScores struct {
	TechnicalBull     float64 `json:"technical_bull_score"`
	TechnicalBear     float64 `json:"technical_bear_score"`
	Trend             float64 `json:"trend_score"`
	Momentum          float64 `json:"momentum_score"`
	Pattern           float64 `json:"pattern_score"`
	VolatilityRisk    float64 `json:"volatility_risk_score"`
	Net               float64 `json:"net_score"`
	DeterministicBias string  `json:"deterministic_bias"`
}

// TimeframeAnalysis is the complete analysis of one timeframe.
type TimeframeAnalysis struct {
	Timeframe         Timeframe        `json:"timeframe"`
	CandlesUsed       int              `json:"candles_used"`
	LastClosedCandle  time.Time        `json:"last_closed_candle"`
	Close             float64          `json:"close"`
	Indicators        Indicators       `json:"indicators"`
	Patterns          []Pattern        `json:"patterns,omitempty"`
	ChartPatterns     []Pattern        `json:"chart_patterns,omitempty"`
	Structure         MarketStructure  `json:"structure"`
	Levels            []Level          `json:"levels,omitempty"`
	Divergences       []Divergence     `json:"divergences,omitempty"`
	Regime            Regime           `json:"regime"`
	Scores            SignalScores     `json:"scores"`
	Bias              PatternDirection `json:"bias"`
	CandleSourceMix   map[string]int   `json:"candle_source_mix,omitempty"`
	CandleProviderMix map[string]int   `json:"candle_provider_mix,omitempty"`
}

// TrendAlignment groups timeframes by their directional bias.
type TrendAlignment struct {
	Bullish        []Timeframe `json:"bullish"`
	Bearish        []Timeframe `json:"bearish"`
	Neutral        []Timeframe `json:"neutral"`
	AlignmentScore float64     `json:"alignment_score"`
	Conflicts      []string    `json:"conflicts,omitempty"`
}

// HistoricalPerformance is the aggregate track record fed back to the LLM.
type HistoricalPerformance struct {
	SampleSize        int                `json:"sample_size"`
	LongWinRate       *float64           `json:"long_win_rate,omitempty"`
	ShortWinRate      *float64           `json:"short_win_rate,omitempty"`
	SymbolWinRate     *float64           `json:"symbol_win_rate,omitempty"`
	RegimeWinRate     map[string]float64 `json:"regime_win_rate,omitempty"`
	PatternWinRate    map[string]float64 `json:"pattern_win_rate,omitempty"`
	AvgRealizedPnLPct *float64           `json:"avg_realized_pnl_pct,omitempty"`
	Note              string             `json:"note,omitempty"`
}

// SimilarCase is one historically similar situation with its real outcome.
type SimilarCase struct {
	Similarity      float64              `json:"similarity"`
	Timestamp       time.Time            `json:"timestamp"`
	Symbol          string               `json:"symbol"`
	FeaturesSummary map[string]any       `json:"features_summary"`
	Recommendation  RecommendationAction `json:"recommendation"`
	Confidence      int                  `json:"confidence"`
	UserOpened      bool                 `json:"user_opened"`
	MarketOutcome   *CaseOutcome         `json:"market_outcome,omitempty"`
	TradeOutcome    *CaseOutcome         `json:"user_trade_outcome,omitempty"`
}

// CaseOutcome is the factual result attached to a similar case.
type CaseOutcome struct {
	RealizedPnLPct *float64 `json:"realized_pnl_pct,omitempty"`
	MFEPct         *float64 `json:"max_favorable_excursion_pct,omitempty"`
	MAEPct         *float64 `json:"max_adverse_excursion_pct,omitempty"`
	Status         string   `json:"status,omitempty"`
}

// PositionContext is the compact view of a user position handed to the LLM.
type PositionContext struct {
	PositionID     uuid.UUID `json:"position_id"`
	Direction      Direction `json:"direction"`
	EntryPrice     float64   `json:"entry_price"`
	Leverage       float64   `json:"leverage"`
	RemainingPct   float64   `json:"remaining_pct"`
	UnrealizedPct  float64   `json:"unrealized_pct"`
	AgeMinutes     int       `json:"age_minutes"`
	CurrentStops   []float64 `json:"current_stop_loss,omitempty"`
	CurrentTargets []float64 `json:"current_take_profit,omitempty"`
	SizeKnown      bool      `json:"size_known"`
}

// FeatureSnapshot is the full deterministic analysis of one symbol at one time.
// It is stored verbatim; the LLM receives a compacted projection of it.
type FeatureSnapshot struct {
	SchemaVersion         int                             `json:"schema_version"`
	Timestamp             time.Time                       `json:"timestamp"`
	LatestClosedCandle    time.Time                       `json:"latest_closed_candle_timestamp"`
	Symbol                string                          `json:"symbol"`
	Price                 float64                         `json:"price"`
	Market                MarketInfo                      `json:"market"`
	Timeframes            map[Timeframe]TimeframeAnalysis `json:"timeframes"`
	TrendAlignment        TrendAlignment                  `json:"trend_alignment"`
	AggregateRegime       Regime                          `json:"market_regime"`
	AggregateScores       SignalScores                    `json:"signal_scores"`
	KeyLevels             []Level                         `json:"support_resistance"`
	ActivePositions       []PositionContext               `json:"active_user_positions"`
	HistoricalPerformance HistoricalPerformance           `json:"historical_performance"`
	SimilarCases          []SimilarCase                   `json:"similar_historical_cases"`
	NewsContext           NewsSnapshot                    `json:"news_context"`
	MarketContext         MarketContext                   `json:"market_context"`
	UniverseContext       UniverseContext                 `json:"universe_context"`
	DataQuality           DataQuality                     `json:"data_quality"`
	RecentCandles         []Candle                        `json:"recent_candles,omitempty"`
}

// MarketContext is the state of the market as a whole rather than of one
// instrument. Crypto assets move together far more than they move apart, so an
// asset that looks strong on its own chart is still swimming against the tide
// when the benchmark is in a downtrend, and that is invisible in a per-symbol
// analysis.
type MarketContext struct {
	// Benchmark is the symbol the state was read from, empty when unknown.
	Benchmark string `json:"benchmark,omitempty"`
	// Trend is where the benchmark stands against its own long daily average.
	Trend MarketContextTrend `json:"trend,omitempty"`
	// PriceVsEMA200Pct is that distance in percent, kept so the strength of the
	// statement is visible rather than only its sign.
	PriceVsEMA200Pct *float64 `json:"price_vs_ema_200_pct,omitempty"`
	// EMA200SlopePct is how much that average has moved over the last month, in
	// percent. Price above a falling average is a rally inside a downtrend, which
	// is a different statement from an uptrend.
	EMA200SlopePct *float64 `json:"ema_200_slope_pct,omitempty"`
	// AsOf is the close time of the benchmark candle the state was read from.
	AsOf time.Time `json:"as_of,omitempty"`
}

// UniverseContext is where this asset stands among the ones being tracked.
//
// A signal says whether an asset looks good on its own chart. It cannot say
// whether it looks better than the fourteen others competing for the same
// capital, and when only a few positions can be open at once that comparison
// decides which of them are actually taken.
type UniverseContext struct {
	// RankPct is the percentile among the tracked assets, 100 being the
	// strongest. Zero members means no ranking was available.
	RankPct float64 `json:"rank_pct"`
	// Score is the risk-adjusted momentum the rank was derived from.
	Score float64 `json:"score"`
	// Members is how many assets took part in the comparison.
	Members int       `json:"members"`
	AsOf    time.Time `json:"as_of,omitempty"`
}

// Known reports whether the ranking was actually computed.
func (c UniverseContext) Known() bool { return c.Members >= 2 }

// MarketContextTrend classifies the benchmark.
type MarketContextTrend string

// Benchmark trend states.
const (
	MarketTrendUnknown MarketContextTrend = ""
	MarketTrendUp      MarketContextTrend = "up"
	MarketTrendDown    MarketContextTrend = "down"
	MarketTrendFlat    MarketContextTrend = "flat"
)

// Known reports whether the benchmark state was actually measured.
func (c MarketContext) Known() bool {
	return c.Trend == MarketTrendUp || c.Trend == MarketTrendDown || c.Trend == MarketTrendFlat
}

// AnalysisRun is the persisted record of one analysis cycle.
type AnalysisRun struct {
	ID                 uuid.UUID       `json:"id"`
	AssetID            int64           `json:"asset_id"`
	Symbol             string          `json:"symbol"`
	AnalysisTimestamp  time.Time       `json:"analysis_timestamp"`
	LatestClosedCandle *time.Time      `json:"latest_closed_candle_timestamp"`
	Price              float64         `json:"price"`
	Snapshot           FeatureSnapshot `json:"features_snapshot"`
	FeatureVector      []float64       `json:"feature_vector,omitempty"`
	Scores             SignalScores    `json:"signal_scores"`
	Regime             MarketRegime    `json:"market_regime"`
	DataQuality        DataQuality     `json:"data_quality"`
	DurationMS         int             `json:"duration_ms"`
	TriggeredBy        string          `json:"triggered_by"`
	CreatedAt          time.Time       `json:"created_at"`
	// StrategyDecision is the deterministic verdict of the enabled strategies
	// for this cycle. It is produced with or without the LLM.
	StrategyDecision *StrategyDecision `json:"strategy_decision,omitempty"`
}
