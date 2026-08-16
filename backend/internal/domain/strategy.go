package domain

import "time"

// StrategyKind separates strategies that pick a side from filters that argue
// against opening anything at all.
type StrategyKind string

// Strategy kinds.
const (
	StrategyDirectional StrategyKind = "directional"
	StrategyFilter      StrategyKind = "filter"
)

// StrategyBlocks says which side a filter argues against.
type StrategyBlocks string

// Filter targets.
const (
	BlocksNothing StrategyBlocks = ""
	BlocksLong    StrategyBlocks = "long"
	BlocksShort   StrategyBlocks = "short"
	BlocksBoth    StrategyBlocks = "both"
)

// StrategyConfig is the user-controlled part of one strategy: whether it takes
// part at all and how much its vote counts. A negative weight fades the
// strategy - its vote counts for the opposite side, which is how a
// counter-trend profile is expressed. HardVeto only applies to filters and
// turns a weighted argument into an absolute one.
type StrategyConfig struct {
	ID       string  `json:"id"`
	Enabled  bool    `json:"enabled"`
	Weight   float64 `json:"weight"`
	HardVeto bool    `json:"hard_veto,omitempty"`
}

// StrategySides restricts which directions the policy may take at all.
type StrategySides string

// Permitted sides.
const (
	SidesBoth      StrategySides = "both"
	SidesLongOnly  StrategySides = "long"
	SidesShortOnly StrategySides = "short"
)

// Valid reports whether the value is one the engine understands.
func (s StrategySides) Valid() bool {
	return s == SidesBoth || s == SidesLongOnly || s == SidesShortOnly
}

// Allows reports whether a direction may be taken under this policy.
func (s StrategySides) Allows(d Direction) bool {
	switch s {
	case SidesLongOnly:
		return d == DirectionLong
	case SidesShortOnly:
		return d == DirectionShort
	default:
		return true
	}
}

// StrategySet is the complete deterministic decision policy. MinSignal is the
// weighted margin an entry must reach before it is taken at all, expressed in
// the same units as the weights.
type StrategySet struct {
	MinSignal float64          `json:"min_signal"`
	Items     []StrategyConfig `json:"items"`
	// Sides restricts the policy to one direction. The ensemble is not equally
	// skilled on both: measured over four separate years of daily bars and three
	// windows of four-hour bars, the forward return after a short signal was
	// negative almost everywhere, including in a falling market, while the long
	// side beat the unconditional return. Restricting the side is therefore a
	// policy decision the history supports, not a preference.
	Sides StrategySides `json:"sides,omitempty"`
	// RegimeAdaptive scales each vote by how well its style suits the market
	// the classifier reports: trend-following opinions count for more while a
	// trend exists, mean-reverting ones while price ranges.
	RegimeAdaptive bool `json:"regime_adaptive"`
}

// Find returns the configuration of one strategy.
func (s StrategySet) Find(id string) (StrategyConfig, bool) {
	for _, item := range s.Items {
		if item.ID == id {
			return item, true
		}
	}
	return StrategyConfig{}, false
}

// StrategyStyle says what kind of market a strategy expects to profit from.
// It is what regime-adaptive weighting keys off.
type StrategyStyle string

// Strategy styles.
const (
	StyleTrend     StrategyStyle = "trend"
	StyleReversion StrategyStyle = "reversion"
	StyleNeutral   StrategyStyle = "neutral"
)

// StrategyVote is what one strategy concluded from one analysis. Detail carries
// only numbers and machine tokens: the UI translates the strategy by its ID
// rather than displaying backend prose.
type StrategyVote struct {
	ID        string           `json:"id"`
	Kind      StrategyKind     `json:"kind"`
	Direction PatternDirection `json:"direction,omitempty"`
	Blocks    StrategyBlocks   `json:"blocks,omitempty"`
	Style     StrategyStyle    `json:"style,omitempty"`
	Strength  float64          `json:"strength"`
	Weight    float64          `json:"weight"`
	Score     float64          `json:"score"`
	HardVeto  bool             `json:"hard_veto,omitempty"`
	Detail    string           `json:"detail,omitempty"`
}

// StrategyDecisionReason is a machine key explaining the outcome.
type StrategyDecisionReason string

// Why a deterministic decision came out the way it did.
const (
	StrategyReasonEntry          StrategyDecisionReason = "entry"
	StrategyReasonNoDirection    StrategyDecisionReason = "no_direction"
	StrategyReasonBelowMinSignal StrategyDecisionReason = "below_min_signal"
	StrategyReasonBlocked        StrategyDecisionReason = "blocked_by_filters"
	StrategyReasonVetoed         StrategyDecisionReason = "hard_veto"
	StrategyReasonNoStrategies   StrategyDecisionReason = "no_enabled_strategies"
	StrategyReasonSideDisabled   StrategyDecisionReason = "side_disabled"
)

// StrategyDecision is the aggregate verdict of the enabled strategies. It is
// produced without the LLM and is stored next to it, so the two can be
// compared on the same history.
type StrategyDecision struct {
	Action      RecommendationAction   `json:"action"`
	Direction   Direction              `json:"direction,omitempty"`
	Confidence  int                    `json:"confidence"`
	LongScore   float64                `json:"long_score"`
	ShortScore  float64                `json:"short_score"`
	NetScore    float64                `json:"net_score"`
	BlockScore  float64                `json:"block_score"`
	MinSignal   float64                `json:"min_signal"`
	Reason      StrategyDecisionReason `json:"reason"`
	Votes       []StrategyVote         `json:"votes,omitempty"`
	Timeframe   Timeframe              `json:"timeframe,omitempty"`
	EvaluatedAt time.Time              `json:"evaluated_at"`
}

// IsEntry reports whether the decision asks for a position.
func (d StrategyDecision) IsEntry() bool {
	return d.Action == RecommendationOpenLong || d.Action == RecommendationOpenShort
}
