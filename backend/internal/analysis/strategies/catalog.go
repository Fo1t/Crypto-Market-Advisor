// Package strategies turns the already computed technical state into a set of
// independent weighted opinions and folds them into one deterministic decision.
//
// Nothing here calls the LLM and nothing here recomputes an indicator: every
// strategy reads the analysis the pipeline already produced. Each strategy is
// either directional, and votes for a side, or a filter, and argues against
// trading. Directional weight competes with blocking weight, exactly as a user
// would expect from "0.7 + 0.3 for short against 2.0 that says no".
package strategies

import (
	"fmt"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

// Definition describes one strategy and its defaults. The defaults are what a
// fresh installation gets; every field is editable in the settings screen.
type Definition struct {
	ID       string
	Kind     domain.StrategyKind
	Style    domain.StrategyStyle
	Weight   float64
	Enabled  bool
	HardVeto bool
}

// Strategy identifiers. They are stable machine keys: statistics, stored
// backtest parameters and UI translations all key off them.
const (
	IDEMATrend           = "ema_trend"
	IDADXDirectional     = "adx_di"
	IDMACD               = "macd"
	IDMarketStructure    = "market_structure"
	IDChartPatterns      = "chart_patterns"
	IDCandlePatterns     = "candle_patterns"
	IDDivergences        = "divergences"
	IDRSIReversion       = "rsi_reversion"
	IDBreakout           = "breakout"
	IDMomentum           = "momentum"
	IDVolumeConfirmation = "volume_confirmation"
	IDVWAP               = "vwap"
	IDCompositeScore     = "composite_score"
	IDHigherTimeframe    = "higher_timeframe"
	IDDonchianBreakout   = "donchian_breakout"
	IDSuperTrend         = "supertrend"
	IDRSI2Reversion      = "rsi2_reversion"
	IDRegimeMomentum     = "regime_momentum"
	IDCapitulationLong   = "capitulation_long"
	IDBearRallyFade      = "bear_rally_fade"

	IDCostFloor         = "cost_floor"
	IDExtensionGuard    = "extension_guard"
	IDTrendGate         = "trend_gate"
	IDMarketGate        = "market_gate"
	IDRelStrengthGate   = "relative_strength_gate"
	IDOpposingLevel     = "opposing_level"
	IDTimeframeConflict = "timeframe_conflict"
	IDVolatilityGuard   = "volatility_guard"
	IDRegimeGuard       = "regime_guard"
	IDCriticalNews      = "critical_news"
)

// DefaultMinSignal is the weighted margin an entry has to reach. With the
// default weights a single strong strategy is not enough on its own.
//
// The weights below were fitted against 47 593 simulated trades from a grid of
// 12 assets, three timeframes, three thresholds and two independent periods:
// strategies whose agreement went with better trades were raised, those whose
// agreement went with worse ones were lowered. That is still one market and five
// years, so treat the profile as a starting point rather than as truth.
//
// The threshold itself was re-measured after the policy became long-only and
// started trailing its exits, because both changes alter which trades survive.
// Swept from 0.6 to 2.4 over four separate years of daily bars and three windows
// of four-hour bars it is a plateau rather than a peak - every value from 1.6 to
// 2.2 lands within 0.05 of the same profit factor - and 1.8 is the point where
// the worst window of both grids is at its best.
const DefaultMinSignal = 1.8

// DefaultMinSignalPreset is the threshold of the shipped preset. It differs from
// DefaultMinSignal because a policy of two decisive rules needs a lower bar than
// one of fourteen quiet ones: the threshold is a property of the profile, not of
// the engine.
const DefaultMinSignalPreset = 1.4

// DefaultRegimeAdaptive decides whether votes are scaled by market regime. It
// ships off until the history says it earns its place.
const DefaultRegimeAdaptive = false

// DefaultSides is which directions a fresh installation may take.
//
// It ships long-only because the ensemble has no skill on the short side. The
// forward return after every signal was measured over four separate years of
// daily bars and three windows of four-hour bars: the long signals beat the
// return of the bars that produced no signal, while the short signals were
// negative at every horizon in almost every window - including the falling year
// of 2022-23, where a short with any skill should have earned the most. Raising
// the confidence threshold made the long side better and the short side worse,
// which is what an anti-predictive rule looks like.
//
// Replayed over the same history the restriction lifts the pooled profit factor
// from 1.01 to 1.03 on daily bars on its own, and to 1.25 together with the
// trend gate and the trailing exit. It is a setting, not a law: a user who
// disagrees can switch it back in the strategy editor.
const DefaultSides = domain.SidesLongOnly

// catalog is the ordered list of everything the engine can evaluate.
var catalog = []Definition{
	{ID: IDMACD, Kind: domain.StrategyDirectional, Style: domain.StyleTrend, Weight: 1.0, Enabled: true},
	{ID: IDMarketStructure, Kind: domain.StrategyDirectional, Style: domain.StyleTrend, Weight: 0.9, Enabled: true},
	{ID: IDMomentum, Kind: domain.StrategyDirectional, Style: domain.StyleTrend, Weight: 0.8, Enabled: true},
	{ID: IDEMATrend, Kind: domain.StrategyDirectional, Style: domain.StyleTrend, Weight: 0.8, Enabled: true},
	{ID: IDRSIReversion, Kind: domain.StrategyDirectional, Style: domain.StyleReversion, Weight: 0.8, Enabled: true},
	{ID: IDADXDirectional, Kind: domain.StrategyDirectional, Style: domain.StyleTrend, Weight: 0.7, Enabled: true},
	{ID: IDCompositeScore, Kind: domain.StrategyDirectional, Style: domain.StyleTrend, Weight: 0.6, Enabled: true},
	{ID: IDVWAP, Kind: domain.StrategyDirectional, Style: domain.StyleTrend, Weight: 0.6, Enabled: true},
	{ID: IDChartPatterns, Kind: domain.StrategyDirectional, Style: domain.StyleNeutral, Weight: 0.6, Enabled: true},
	{ID: IDCandlePatterns, Kind: domain.StrategyDirectional, Style: domain.StyleNeutral, Weight: 0.6, Enabled: true},
	{ID: IDBreakout, Kind: domain.StrategyDirectional, Style: domain.StyleTrend, Weight: 0.5, Enabled: true},
	{ID: IDVolumeConfirmation, Kind: domain.StrategyDirectional, Style: domain.StyleNeutral, Weight: 0.3, Enabled: true},
	{ID: IDDivergences, Kind: domain.StrategyDirectional, Style: domain.StyleReversion, Weight: 0.3, Enabled: true},
	// Connors' two-period rule improved both daily periods and did no harm on
	// the faster ones, so it ships on. The gain is small enough to be noise.
	{ID: IDRSI2Reversion, Kind: domain.StrategyDirectional, Style: domain.StyleReversion, Weight: 1.0, Enabled: true},

	// The rest are published systems kept switched off: on the tested history
	// they were neutral at best, and one good period is not evidence.
	{ID: IDHigherTimeframe, Kind: domain.StrategyDirectional, Style: domain.StyleTrend, Weight: 0.9, Enabled: false},
	{ID: IDDonchianBreakout, Kind: domain.StrategyDirectional, Style: domain.StyleTrend, Weight: 1.0, Enabled: false},
	{ID: IDSuperTrend, Kind: domain.StrategyDirectional, Style: domain.StyleTrend, Weight: 1.0, Enabled: false},

	// Rules derived from a direct measurement of what follows a move of a given
	// size in a given market regime. They ship off until a full replay confirms
	// what the forward-return study and its bootstrap argue for.
	{ID: IDRegimeMomentum, Kind: domain.StrategyDirectional, Style: domain.StyleTrend, Weight: 1.0, Enabled: false},
	{ID: IDCapitulationLong, Kind: domain.StrategyDirectional, Style: domain.StyleReversion, Weight: 1.0, Enabled: false},
	{ID: IDBearRallyFade, Kind: domain.StrategyDirectional, Style: domain.StyleReversion, Weight: 1.0, Enabled: false},

	{ID: IDCriticalNews, Kind: domain.StrategyFilter, Weight: 2.0, Enabled: true, HardVeto: true},
	{ID: IDCostFloor, Kind: domain.StrategyFilter, Weight: 2.0, Enabled: true, HardVeto: true},
	{ID: IDExtensionGuard, Kind: domain.StrategyFilter, Weight: 1.2, Enabled: false},
	// A trade against the slowest timeframe on screen is refused outright. As a
	// weighted argument the gate changed nothing; as a veto it was the single
	// change that improved the worst period of the daily grid, from a profit
	// factor of 0.76 to 0.90, while also lifting the pooled figure.
	{ID: IDTrendGate, Kind: domain.StrategyFilter, Weight: 1.5, Enabled: true, HardVeto: true},
	// The market-wide veto is the single change that removed most of what the
	// long-only policy lost in falling markets. On four-hour bars it cut the
	// worst window from 159 trades losing 0.80% per run to 9 trades losing
	// 0.12%; on daily bars it lifted the pooled profit factor from 1.30 to 1.35
	// and halved the drawdown of the bear year. Like the trend gate it only
	// works as a veto: as a weighted argument it changed nothing at all.
	{ID: IDMarketGate, Kind: domain.StrategyFilter, Weight: 2.0, Enabled: true, HardVeto: true},
	// Ranking the tracked assets against each other and refusing the laggards is
	// a documented cross-sectional effect, and it does fix the window that hurt
	// most: on daily bars the 2024-25 year went from -2.5% at a profit factor of
	// 0.94 to +10.7% at 1.27. It ships off anyway, because it does not survive
	// its own robustness test. The threshold behaves erratically rather than as a
	// plateau - on that same year 40/50/60/70 gave +5.2, +3.8, +10.7 and -0.8 -
	// and on four-hour bars every threshold made things worse (1.11 to 1.09 and
	// 1.34 to 1.12). Enable it deliberately, not by default.
	{ID: IDRelStrengthGate, Kind: domain.StrategyFilter, Weight: 1.5, Enabled: false},
	{ID: IDTimeframeConflict, Kind: domain.StrategyFilter, Weight: 1.4, Enabled: true},
	{ID: IDOpposingLevel, Kind: domain.StrategyFilter, Weight: 1.0, Enabled: true},
	{ID: IDVolatilityGuard, Kind: domain.StrategyFilter, Weight: 1.0, Enabled: true},
	{ID: IDRegimeGuard, Kind: domain.StrategyFilter, Weight: 1.0, Enabled: false},
}

// Catalog returns every known strategy in display order.
func Catalog() []Definition {
	out := make([]Definition, len(catalog))
	copy(out, catalog)
	return out
}

// Known reports whether an identifier belongs to the catalog.
func Known(id string) bool {
	for _, def := range catalog {
		if def.ID == id {
			return true
		}
	}
	return false
}

// DefaultSet is the policy a fresh installation starts from: the preset that
// measured best on the stored history, not the union of everything the catalog
// can do. The per-strategy Enabled flags below remain what an unknown identifier
// falls back to when an older stored document is upgraded.
func DefaultSet() domain.StrategySet {
	set := DefaultPreset().Set
	set.RegimeAdaptive = DefaultRegimeAdaptive
	return set
}

// Normalize repairs a stored policy: unknown identifiers are dropped, missing
// ones are added from the defaults, and nonsense values are replaced. A stored
// document from an older version therefore keeps working after an upgrade.
func Normalize(set domain.StrategySet) domain.StrategySet {
	stored := make(map[string]domain.StrategyConfig, len(set.Items))
	for _, item := range set.Items {
		stored[item.ID] = item
	}

	out := domain.StrategySet{MinSignal: set.MinSignal, RegimeAdaptive: set.RegimeAdaptive, Sides: set.Sides}
	if out.MinSignal <= 0 || out.MinSignal > 100 {
		out.MinSignal = DefaultMinSignal
	}
	if !out.Sides.Valid() {
		out.Sides = DefaultSides
	}
	for _, def := range catalog {
		item, ok := stored[def.ID]
		if !ok {
			out.Items = append(out.Items, domain.StrategyConfig{
				ID: def.ID, Enabled: def.Enabled, Weight: def.Weight, HardVeto: def.HardVeto,
			})
			continue
		}
		item.ID = def.ID
		if item.Weight < -100 || item.Weight > 100 {
			item.Weight = def.Weight
		}
		if def.Kind != domain.StrategyFilter {
			item.HardVeto = false
		}
		out.Items = append(out.Items, item)
	}
	return out
}

// Validate reports why a policy cannot be used. It is shared by the settings
// screen and by a per-run override, so both reject the same nonsense.
func Validate(set domain.StrategySet) error {
	if set.MinSignal < 0 || set.MinSignal > 100 {
		return fmt.Errorf("strategy min signal must be in [0,100]")
	}
	if set.Sides != "" && !set.Sides.Valid() {
		return fmt.Errorf("strategy sides must be both, long or short")
	}
	enabled := 0
	for _, item := range set.Items {
		if !Known(item.ID) {
			return fmt.Errorf("unknown strategy %q", item.ID)
		}
		if item.Weight < -100 || item.Weight > 100 {
			return fmt.Errorf("weight of %s must be in [-100,100]", item.ID)
		}
		if item.Enabled && item.Weight != 0 && kindOf(item.ID) == domain.StrategyDirectional {
			enabled++
		}
	}
	if len(set.Items) > 0 && enabled == 0 {
		return fmt.Errorf("at least one directional strategy has to be enabled")
	}
	return nil
}

// styleOf returns what kind of market a strategy expects to profit from.
func styleOf(id string) domain.StrategyStyle {
	for _, def := range catalog {
		if def.ID == id {
			if def.Style == "" {
				return domain.StyleNeutral
			}
			return def.Style
		}
	}
	return domain.StyleNeutral
}

// kindOf returns the kind of a known strategy.
func kindOf(id string) domain.StrategyKind {
	for _, def := range catalog {
		if def.ID == id {
			return def.Kind
		}
	}
	return domain.StrategyDirectional
}
