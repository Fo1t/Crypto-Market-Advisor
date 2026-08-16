// Package domain contains the core typed vocabulary of the application.
// Every cross-layer value that has a fixed set of states lives here as a typed
// constant so that string literals never leak into business logic.
package domain

import "fmt"

// RecommendationAction is the top-level decision produced by the LLM.
type RecommendationAction string

// The four top-level actions the model may return.
const (
	RecommendationOpenLong  RecommendationAction = "OPEN_LONG"
	RecommendationOpenShort RecommendationAction = "OPEN_SHORT"
	RecommendationNoAction  RecommendationAction = "NO_ACTION"
	RecommendationManage    RecommendationAction = "MANAGE_POSITION"
)

// Valid reports whether the action is one of the four known values.
func (a RecommendationAction) Valid() bool {
	switch a {
	case RecommendationOpenLong, RecommendationOpenShort, RecommendationNoAction, RecommendationManage:
		return true
	}
	return false
}

// IsEntry reports whether the action asks the user to open a new position.
func (a RecommendationAction) IsEntry() bool {
	return a == RecommendationOpenLong || a == RecommendationOpenShort
}

// Direction returns the trade direction implied by an entry action.
func (a RecommendationAction) Direction() (Direction, bool) {
	switch a {
	case RecommendationOpenLong:
		return DirectionLong, true
	case RecommendationOpenShort:
		return DirectionShort, true
	}
	return "", false
}

// ManagementActionType enumerates sub-actions of MANAGE_POSITION.
type ManagementActionType string

// Management sub-actions for an existing position.
const (
	ManagementHold             ManagementActionType = "HOLD"
	ManagementClosePartial     ManagementActionType = "CLOSE_PARTIAL"
	ManagementCloseFull        ManagementActionType = "CLOSE_FULL"
	ManagementMoveStopLoss     ManagementActionType = "MOVE_STOP_LOSS"
	ManagementUpdateTakeProfit ManagementActionType = "UPDATE_TAKE_PROFIT"
	ManagementMultipleChanges  ManagementActionType = "MULTIPLE_CHANGES"
)

// Valid reports whether the management action is known.
func (m ManagementActionType) Valid() bool {
	switch m {
	case ManagementHold, ManagementClosePartial, ManagementCloseFull,
		ManagementMoveStopLoss, ManagementUpdateTakeProfit, ManagementMultipleChanges:
		return true
	}
	return false
}

// Direction is the side of a trade.
type Direction string

// Trade directions.
const (
	DirectionLong  Direction = "LONG"
	DirectionShort Direction = "SHORT"
)

// Valid reports whether the direction is known.
func (d Direction) Valid() bool { return d == DirectionLong || d == DirectionShort }

// Sign returns +1 for long and -1 for short, used in P&L math.
func (d Direction) Sign() int {
	if d == DirectionShort {
		return -1
	}
	return 1
}

// RiskLevel is the qualitative risk assessment attached to a recommendation.
type RiskLevel string

// Risk levels, ordered from calm to dangerous.
const (
	RiskLow      RiskLevel = "low"
	RiskMedium   RiskLevel = "medium"
	RiskHigh     RiskLevel = "high"
	RiskExtreme  RiskLevel = "extreme"
	RiskUnknown  RiskLevel = "unknown"
	riskLevelSet           = "low|medium|high|extreme"
)

// Valid reports whether the risk level is known.
func (r RiskLevel) Valid() bool {
	switch r {
	case RiskLow, RiskMedium, RiskHigh, RiskExtreme:
		return true
	}
	return false
}

// AllowedRiskLevels returns the human readable set, used in prompts and errors.
func AllowedRiskLevels() string { return riskLevelSet }

// PositionStatus is the lifecycle state of a user position.
type PositionStatus string

// Position lifecycle states.
const (
	PositionOpen            PositionStatus = "OPEN"
	PositionPartiallyClosed PositionStatus = "PARTIALLY_CLOSED"
	PositionClosed          PositionStatus = "CLOSED"
)

// Valid reports whether the status is known.
func (p PositionStatus) Valid() bool {
	switch p {
	case PositionOpen, PositionPartiallyClosed, PositionClosed:
		return true
	}
	return false
}

// FeeType describes how a fill was executed for fee purposes.
type FeeType string

// Fee types recorded on a fill.
const (
	FeeMaker  FeeType = "maker"
	FeeTaker  FeeType = "taker"
	FeeCustom FeeType = "custom"
)

// Valid reports whether the fee type is known.
func (f FeeType) Valid() bool {
	switch f {
	case FeeMaker, FeeTaker, FeeCustom:
		return true
	}
	return false
}

// PositionEventType enumerates the append-only position history events.
type PositionEventType string

// Position audit-trail event types.
const (
	EventOpened        PositionEventType = "OPENED"
	EventPartialClose  PositionEventType = "PARTIAL_CLOSE"
	EventFullClose     PositionEventType = "FULL_CLOSE"
	EventFeeAdded      PositionEventType = "FEE_ADDED"
	EventFundingAdded  PositionEventType = "FUNDING_ADDED"
	EventPlanUpdated   PositionEventType = "PLAN_UPDATED"
	EventNoteAdded     PositionEventType = "NOTE_ADDED"
	EventStopLossMoved PositionEventType = "STOP_LOSS_MOVED"
)

// UserDecision records what the user did with a recommendation.
type UserDecision string

// User decisions on a recommendation.
const (
	DecisionPending  UserDecision = "PENDING"
	DecisionOpened   UserDecision = "OPENED"
	DecisionSkipped  UserDecision = "SKIPPED"
	DecisionIgnored  UserDecision = "IGNORED"
	DecisionExpired  UserDecision = "EXPIRED"
	decisionValidSet              = "PENDING|OPENED|SKIPPED|IGNORED|EXPIRED"
)

// Valid reports whether the decision is known.
func (u UserDecision) Valid() bool {
	switch u {
	case DecisionPending, DecisionOpened, DecisionSkipped, DecisionIgnored, DecisionExpired:
		return true
	}
	return false
}

// AllowedDecisions returns the human readable set of decisions.
func AllowedDecisions() string { return decisionValidSet }

// DataQualityStatus flags how complete a feature snapshot is.
type DataQualityStatus string

// Data quality levels of a feature snapshot.
const (
	DataQualityOK       DataQualityStatus = "ok"
	DataQualityDegraded DataQualityStatus = "degraded"
	DataQualityUnusable DataQualityStatus = "unusable"
)

// MarketRegime is the deterministic classification of the current market.
type MarketRegime string

// Market regimes produced by the deterministic classifier.
const (
	RegimeStrongUptrend   MarketRegime = "strong_uptrend"
	RegimeWeakUptrend     MarketRegime = "weak_uptrend"
	RegimeStrongDowntrend MarketRegime = "strong_downtrend"
	RegimeWeakDowntrend   MarketRegime = "weak_downtrend"
	RegimeRange           MarketRegime = "range"
	RegimeBreakout        MarketRegime = "breakout"
	RegimeCompression     MarketRegime = "compression"
	RegimeUncertain       MarketRegime = "uncertain"
)

// RegimeTag is an additional non-exclusive descriptor of the market.
type RegimeTag string

// Non-exclusive regime tags.
const (
	TagHighVolatility  RegimeTag = "high_volatility"
	TagLowVolatility   RegimeTag = "low_volatility"
	TagStrongMomentum  RegimeTag = "strong_momentum"
	TagWeakMomentum    RegimeTag = "weak_momentum"
	TagOverbought      RegimeTag = "overbought"
	TagOversold        RegimeTag = "oversold"
	TagVolumeSpike     RegimeTag = "volume_spike"
	TagVolumeDry       RegimeTag = "volume_dry"
	TagNearResistance  RegimeTag = "near_resistance"
	TagNearSupport     RegimeTag = "near_support"
	TagSqueeze         RegimeTag = "squeeze"
	TagExpandingRanges RegimeTag = "expanding_ranges"
)

// StructureState classifies market structure from swing points.
type StructureState string

// Market structure states.
const (
	StructureBullish    StructureState = "bullish"
	StructureBearish    StructureState = "bearish"
	StructureRange      StructureState = "range"
	StructureTransition StructureState = "transition"
	StructureUncertain  StructureState = "uncertain"
)

// PatternDirection is the bias of a detected pattern.
type PatternDirection string

// Pattern directions.
const (
	PatternBullish PatternDirection = "bullish"
	PatternBearish PatternDirection = "bearish"
	PatternNeutral PatternDirection = "neutral"
)

// LevelType distinguishes support from resistance.
type LevelType string

// Support and resistance level types.
const (
	LevelSupport    LevelType = "support"
	LevelResistance LevelType = "resistance"
)

// RecommendationFreshness tells the UI how much to trust a stored card.
type RecommendationFreshness string

// Recommendation freshness states shown in the UI.
const (
	FreshnessFresh      RecommendationFreshness = "fresh"
	FreshnessStale      RecommendationFreshness = "stale"
	FreshnessIncomplete RecommendationFreshness = "incomplete"
)

// TradeResult is the classification of a finished trade.
type TradeResult string

// Trade results.
const (
	ResultWin       TradeResult = "win"
	ResultLoss      TradeResult = "loss"
	ResultBreakeven TradeResult = "breakeven"
	ResultOpen      TradeResult = "open"
)

// OutcomeStatus records whether a recommendation outcome could be resolved.
type OutcomeStatus string

// Outcome resolution states.
const (
	OutcomePending   OutcomeStatus = "pending"
	OutcomeTPHit     OutcomeStatus = "tp_hit"
	OutcomeSLHit     OutcomeStatus = "sl_hit"
	OutcomeAmbiguous OutcomeStatus = "ambiguous"
	OutcomeNeither   OutcomeStatus = "neither"
	OutcomeNoTrade   OutcomeStatus = "no_trade"
)

// BacktestMode selects the engine used for a backtest run.
type BacktestMode string

// Backtest engines.
const (
	BacktestTechnical BacktestMode = "technical"
	BacktestLLM       BacktestMode = "llm"
)

// BacktestStatus is the lifecycle of a backtest run.
type BacktestStatus string

// Backtest run states.
const (
	BacktestPending   BacktestStatus = "pending"
	BacktestRunning   BacktestStatus = "running"
	BacktestCompleted BacktestStatus = "completed"
	BacktestFailed    BacktestStatus = "failed"
	BacktestCanceled  BacktestStatus = "canceled"
)

// ComponentStatus is used by the health endpoints.
type ComponentStatus string

// Health states of a dependency.
const (
	ComponentOnline   ComponentStatus = "online"
	ComponentDegraded ComponentStatus = "degraded"
	ComponentOffline  ComponentStatus = "offline"
	ComponentDisabled ComponentStatus = "disabled"
)

// ParseDirection converts an untrusted string into a Direction.
func ParseDirection(s string) (Direction, error) {
	d := Direction(s)
	if !d.Valid() {
		return "", fmt.Errorf("unknown direction %q", s)
	}
	return d, nil
}

// ParseAction converts an untrusted string into a RecommendationAction.
func ParseAction(s string) (RecommendationAction, error) {
	a := RecommendationAction(s)
	if !a.Valid() {
		return "", fmt.Errorf("unknown action %q", s)
	}
	return a, nil
}

// The languages every model answer carries. They are UI languages, not a user
// setting: one inference produces all three, so switching language in the
// interface shows the same recommendation rather than asking for a new one.
const (
	// DefaultLanguage is what a stored row's own narrative columns hold, and
	// what the UI falls back to when a record predates a language.
	DefaultLanguage = "en"
)

// SupportedLanguages lists them in the order the prompt and the validator
// expect. Adding one means teaching the prompt, the validator and the frontend
// locales about it together.
func SupportedLanguages() []string { return []string{"en", "ru", "zh-CN"} }
