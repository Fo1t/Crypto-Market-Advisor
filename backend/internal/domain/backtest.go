package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// BacktestParams is the user-supplied configuration of a run.
type BacktestParams struct {
	Mode                 BacktestMode    `json:"mode"`
	Symbol               string          `json:"symbol"`
	Timeframe            Timeframe       `json:"timeframe"`
	DateFrom             time.Time       `json:"date_from"`
	DateTo               time.Time       `json:"date_to"`
	AnalysisInterval     string          `json:"analysis_interval,omitempty"`
	InitialCapital       decimal.Decimal `json:"initial_capital"`
	AllocationPct        decimal.Decimal `json:"allocation_pct"`
	Leverage             decimal.Decimal `json:"leverage"`
	SlippagePct          decimal.Decimal `json:"slippage_pct"`
	MakerFeePct          decimal.Decimal `json:"maker_fee_pct"`
	TakerFeePct          decimal.Decimal `json:"taker_fee_pct"`
	FundingRatePct       decimal.Decimal `json:"funding_rate_pct"`
	MaintenanceMarginPct decimal.Decimal `json:"maintenance_margin_pct"`
	MaxOpenPositions     int             `json:"max_open_positions"`
	MinConfidence        int             `json:"min_confidence"`
	UseCache             bool            `json:"use_cache"`
	// InferencePause is how long the replay waits after each request that
	// actually reached the model, so a long run does not hold the GPU at full
	// load from start to finish. A cached answer costs nothing and is not paused.
	InferencePause time.Duration `json:"inference_pause_ms,omitempty"`
	Confirm        bool          `json:"confirm"`

	// BreakEvenAfterTP moves the stop to the entry price once the first partial
	// take profit is filled.
	BreakEvenAfterTP bool `json:"break_even_after_tp"`

	ExitMode BacktestExitMode `json:"exit_mode"`
	// TrailingATRMult is the Chandelier multiplier used by ExitModeTrailingATR.
	TrailingATRMult  decimal.Decimal `json:"trailing_atr_mult,omitempty"`
	TakeProfitLadder []PnLExitStep   `json:"take_profit_ladder,omitempty"`
	StopLossLadder   []PnLExitStep   `json:"stop_loss_ladder,omitempty"`

	// ATR multipliers of the technical plan under ExitModeSignal. They decide the
	// payoff profile of every deterministic trade, which is what separates a
	// system that survives its own transaction cost from one that does not, so
	// they are parameters rather than constants. Zero means the shipped default.
	ATRStopMult    decimal.Decimal `json:"atr_stop_mult,omitempty"`
	ATRTarget1Mult decimal.Decimal `json:"atr_target1_mult,omitempty"`
	ATRTarget2Mult decimal.Decimal `json:"atr_target2_mult,omitempty"`
	// ATRTarget1ClosePct is how much of the position the first target banks.
	ATRTarget1ClosePct decimal.Decimal `json:"atr_target1_close_pct,omitempty"`
	// CostFloorMultiple overrides how many round trips a target must be worth
	// for the cost filter to allow the trade. Zero means the shipped default.
	CostFloorMultiple decimal.Decimal `json:"cost_floor_multiple,omitempty"`
	// MinRelativeStrengthPct overrides the percentile an asset must reach among
	// its peers. Zero means the shipped default.
	MinRelativeStrengthPct decimal.Decimal `json:"min_relative_strength_pct,omitempty"`

	// MarketGateLongBufferPct is the buffer a long demands above the benchmark
	// average; zero asks for none, which is the shipped behaviour.
	MarketGateLongBufferPct decimal.Decimal `json:"market_gate_long_buffer_pct,omitempty"`
	// MarketGateAllowFallingAverage lifts the demand that the benchmark average
	// itself be rising. It is a pointer because that demand ships on: a stored
	// run from before the field existed has to be told apart from one where the
	// user deliberately lifted it.
	MarketGateAllowFallingAverage *bool `json:"market_gate_allow_falling_average,omitempty"`

	// EntryPullbackATR turns the entry into a resting limit order that many
	// average bar ranges below the signal price for a long, and above it for a
	// short. Zero enters at the market on the signal bar, which is what a run
	// did before the option existed.
	EntryPullbackATR decimal.Decimal `json:"entry_pullback_atr,omitempty"`
	// EntryValidBars is how long that order rests before the signal is
	// abandoned. Zero means DefaultEntryValidBars.
	EntryValidBars int `json:"entry_valid_bars,omitempty"`

	// Strategies is the deterministic policy snapshot taken when the run was
	// created, so a stored run stays reproducible after the settings change.
	Strategies *StrategySet `json:"strategies,omitempty"`
}

// EquityPoint is one sample of the simulated account value. The curve is
// recorded per bar and downsampled before it is stored, because its only
// consumer is a chart.
type EquityPoint struct {
	Time   time.Time `json:"t"`
	Equity float64   `json:"e"`
}

// BacktestExitMode selects where the exit levels of a simulated trade come from.
type BacktestExitMode string

const (
	// ExitModeSignal keeps the levels the signal itself produced: ATR-derived
	// targets in technical mode, the model's own plan in LLM mode.
	ExitModeSignal BacktestExitMode = "signal"
	// ExitModePnLLadder replaces them with fixed return-on-margin steps, so the
	// same management rule can be measured across every signal.
	ExitModePnLLadder BacktestExitMode = "pnl_ladder"
	// ExitModeTrailingATR drops the target altogether and rides the position
	// behind a Chandelier stop: the extreme reached since entry, less a multiple
	// of the average bar range.
	ExitModeTrailingATR BacktestExitMode = "trailing_atr"
)

// Valid reports whether the exit mode is one the engine implements.
func (m BacktestExitMode) Valid() bool {
	return m == ExitModeSignal || m == ExitModePnLLadder || m == ExitModeTrailingATR
}

// PnLExitStep closes a share of the position once the trade reaches a given
// return on margin. PnLPct is the gross leveraged return in percent, stated as
// a positive magnitude on both sides: 50 means +50% for a take-profit step and
// -50% for a stop-loss step. Fees and funding are charged on top, so the net
// result of a step is slightly smaller than its threshold.
type PnLExitStep struct {
	PnLPct   float64 `json:"pnl_pct"`
	ClosePct float64 `json:"close_pct"`
}

// BacktestMetrics is the performance summary of a run.
type BacktestMetrics struct {
	TotalReturnPct   float64         `json:"total_return_pct"`
	FinalCapital     decimal.Decimal `json:"final_capital"`
	Trades           int             `json:"trades"`
	Wins             int             `json:"wins"`
	Losses           int             `json:"losses"`
	WinRate          float64         `json:"win_rate"`
	ProfitFactor     *float64        `json:"profit_factor,omitempty"`
	Expectancy       float64         `json:"expectancy"`
	MaxDrawdownPct   float64         `json:"max_drawdown_pct"`
	Sharpe           *float64        `json:"sharpe,omitempty"`
	AverageTradePct  float64         `json:"average_trade_pct"`
	AverageMFEPct    float64         `json:"average_mfe_pct"`
	AverageMAEPct    float64         `json:"average_mae_pct"`
	LongTrades       int             `json:"long_trades"`
	ShortTrades      int             `json:"short_trades"`
	LongWinRate      float64         `json:"long_win_rate"`
	ShortWinRate     float64         `json:"short_win_rate"`
	AvgHoldingMinute float64         `json:"average_holding_minutes"`
	TotalFees        decimal.Decimal `json:"total_fees"`
	TotalFunding     decimal.Decimal `json:"total_funding"`
	// AnalysisPoints is how many decision points the replay actually evaluated,
	// which is not the estimate: a run whose date range reaches further back than
	// the stored candles silently replays only the part it has data for.
	AnalysisPoints int `json:"analysis_points"`
	// ReplayFrom and ReplayTo are the first and last candle the replay saw, so a
	// run that covered a month of a requested year says so.
	ReplayFrom *time.Time `json:"replay_from,omitempty"`
	ReplayTo   *time.Time `json:"replay_to,omitempty"`
	// DecisionReasons counts why each analysis point did or did not become a
	// trade. Without it a run with no trades is indistinguishable from a broken
	// one, and the user has no way to tell which rule refused.
	DecisionReasons map[string]int `json:"decision_reasons,omitempty"`
	// UnfilledEntries counts signals that placed a resting order the market never
	// came back to.
	UnfilledEntries int      `json:"unfilled_entries"`
	DegradedSteps   int      `json:"degraded_steps"`
	DataIssues      []string `json:"data_issues,omitempty"`
	InferencesUsed  int      `json:"inferences_used"`
	CacheHits       int      `json:"cache_hits"`
}

// BacktestRun is the persisted record of a backtest.
type BacktestRun struct {
	ID             uuid.UUID        `json:"id"`
	Mode           BacktestMode     `json:"mode"`
	Symbol         string           `json:"symbol"`
	AssetID        *int64           `json:"asset_id,omitempty"`
	Timeframe      Timeframe        `json:"timeframe"`
	DateFrom       time.Time        `json:"date_from"`
	DateTo         time.Time        `json:"date_to"`
	Interval       string           `json:"analysis_interval"`
	Status         BacktestStatus   `json:"status"`
	Params         BacktestParams   `json:"params"`
	Metrics        *BacktestMetrics `json:"metrics,omitempty"`
	EstimatedSteps int              `json:"estimated_steps"`
	CompletedSteps int              `json:"completed_steps"`
	ErrorMessage   string           `json:"error_message,omitempty"`
	StartedAt      *time.Time       `json:"started_at,omitempty"`
	FinishedAt     *time.Time       `json:"finished_at,omitempty"`
	CreatedAt      time.Time        `json:"created_at"`
}

// BacktestTrade is one simulated trade.
type BacktestTrade struct {
	ID            uuid.UUID           `json:"id"`
	RunID         uuid.UUID           `json:"run_id"`
	Symbol        string              `json:"symbol"`
	Direction     Direction           `json:"direction"`
	OpenedAt      time.Time           `json:"opened_at"`
	ClosedAt      *time.Time          `json:"closed_at,omitempty"`
	EntryPrice    decimal.Decimal     `json:"entry_price"`
	ExitPrice     *decimal.Decimal    `json:"exit_price,omitempty"`
	Quantity      decimal.Decimal     `json:"quantity"`
	Leverage      decimal.Decimal     `json:"leverage"`
	AllocationPct decimal.Decimal     `json:"allocation_pct"`
	GrossPnL      decimal.Decimal     `json:"gross_pnl"`
	Fees          decimal.Decimal     `json:"fees"`
	Funding       decimal.Decimal     `json:"funding"`
	NetPnL        decimal.Decimal     `json:"net_pnl"`
	PnLPct        float64             `json:"pnl_pct"`
	MFEPct        *float64            `json:"mfe_pct,omitempty"`
	MAEPct        *float64            `json:"mae_pct,omitempty"`
	ExitReason    string              `json:"exit_reason"`
	Confidence    *int                `json:"confidence,omitempty"`
	Executions    []BacktestExecution `json:"executions,omitempty"`
	// StrategyVotes records which strategies asked for this trade.
	StrategyVotes []StrategyVote `json:"strategy_votes,omitempty"`
}

// BacktestExecution is an auditable partial fill or funding event belonging to
// one simulated trade. The aggregate trade remains convenient for metrics,
// while executions preserve the actual staged-exit accounting.
type BacktestExecution struct {
	Kind       string          `json:"kind"`
	ExecutedAt time.Time       `json:"executed_at"`
	Price      decimal.Decimal `json:"price"`
	Quantity   decimal.Decimal `json:"quantity"`
	ClosePct   float64         `json:"close_pct,omitempty"`
	GrossPnL   decimal.Decimal `json:"gross_pnl"`
	Fee        decimal.Decimal `json:"fee"`
	Funding    decimal.Decimal `json:"funding"`
	FeeType    FeeType         `json:"fee_type,omitempty"`
}
