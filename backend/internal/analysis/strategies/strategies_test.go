package strategies

import (
	"math"
	"testing"
	"time"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

func f(v float64) *float64 { return &v }

// setOf builds a policy with only the named strategies enabled, so a test
// exercises one rule at a time instead of the whole catalog. It permits both sides, so
// a test of the voting arithmetic is not also a test of the shipped side
// restriction; that one has its own test below.
func setOf(minSignal float64, weights map[string]float64, vetoes ...string) domain.StrategySet {
	set := domain.StrategySet{MinSignal: minSignal, Sides: domain.SidesBoth}
	veto := map[string]bool{}
	for _, id := range vetoes {
		veto[id] = true
	}
	for _, def := range Catalog() {
		weight, enabled := weights[def.ID]
		set.Items = append(set.Items, domain.StrategyConfig{
			ID: def.ID, Enabled: enabled, Weight: weight, HardVeto: veto[def.ID],
		})
	}
	return set
}

func bullishInput() Input {
	return Input{
		Timeframe: domain.TF1h,
		Price:     100,
		Now:       time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Analysis: domain.TimeframeAnalysis{
			Timeframe: domain.TF1h,
			Indicators: domain.Indicators{
				ADX: f(32), PlusDI: f(28), MinusDI: f(12),
				PriceVsEMA50Pct: f(2.5), PriceVsEMA200Pct: f(6),
				EMA:    map[string]float64{"50": 103, "200": 98},
				ATRPct: f(1.2),
			},
			Structure: domain.MarketStructure{State: domain.StructureBullish},
			Scores:    domain.SignalScores{Net: 0.6},
		},
		Snapshot: domain.FeatureSnapshot{
			TrendAlignment: domain.TrendAlignment{
				Bullish:        []domain.Timeframe{domain.TF1h, domain.TF4h},
				AlignmentScore: 0.8,
			},
		},
	}
}

// TestWeightsCompeteWithFilters is the rule the whole engine exists for: two
// directional strategies weighing 0.7 and 0.3 lose to a single filter of 2.
func TestWeightsCompeteWithFilters(t *testing.T) {
	in := bullishInput()
	// One resistance level 0.2% above price argues against the long.
	in.Snapshot.KeyLevels = []domain.Level{
		{Price: 100.2, Type: domain.LevelResistance, Strength: 0.9, DistancePct: 0.2},
	}

	without := Evaluate(in, setOf(0.5, map[string]float64{
		IDEMATrend: 0.7, IDADXDirectional: 0.3,
	}))
	if without.Action != domain.RecommendationOpenLong {
		t.Fatalf("without a filter the long must be taken, got %s (%s)", without.Action, without.Reason)
	}

	with := Evaluate(in, setOf(0.5, map[string]float64{
		IDEMATrend: 0.7, IDADXDirectional: 0.3, IDOpposingLevel: 2,
	}))
	if with.Action != domain.RecommendationNoAction || with.Reason != domain.StrategyReasonBlocked {
		t.Fatalf("a filter weighing 2 must outweigh 1.0 of direction, got %s (%s)", with.Action, with.Reason)
	}
	if with.BlockScore <= with.NetScore {
		t.Fatalf("blocking score %.2f must exceed the net %.2f", with.BlockScore, with.NetScore)
	}
}

// TestFilterOnlyBlocksItsOwnSide keeps a resistance level from blocking a short.
func TestFilterOnlyBlocksItsOwnSide(t *testing.T) {
	in := bullishInput()
	in.Analysis.Indicators.PlusDI, in.Analysis.Indicators.MinusDI = f(10), f(30)
	in.Analysis.Indicators.PriceVsEMA50Pct, in.Analysis.Indicators.PriceVsEMA200Pct = f(-3), f(-7)
	in.Analysis.Indicators.EMA = map[string]float64{"50": 96, "200": 102}
	in.Analysis.Structure.State = domain.StructureBearish
	in.Analysis.Scores.Net = -0.6
	in.Snapshot.KeyLevels = []domain.Level{
		{Price: 100.2, Type: domain.LevelResistance, Strength: 0.9, DistancePct: 0.2},
	}

	decision := Evaluate(in, setOf(0.5, map[string]float64{
		IDEMATrend: 0.7, IDADXDirectional: 0.3, IDOpposingLevel: 2,
	}))
	if decision.Action != domain.RecommendationOpenShort {
		t.Fatalf("resistance above price must not block a short, got %s (%s)", decision.Action, decision.Reason)
	}
}

// TestHardVetoIgnoresWeights covers the escape hatch: a critical news filter
// stops the entry no matter how much directional weight agrees.
func TestHardVetoIgnoresWeights(t *testing.T) {
	in := bullishInput()
	in.CriticalNewsMaxAge = 2 * time.Hour
	in.Snapshot.NewsContext = domain.NewsSnapshot{
		AssetSpecific: []domain.NewsSnapshotItem{{Critical: true, AgeMinutes: 15, Title: "exchange halt"}},
	}

	weights := map[string]float64{
		IDEMATrend: 5, IDADXDirectional: 5, IDCriticalNews: 0.1,
	}
	soft := Evaluate(in, setOf(0.5, weights))
	if soft.Action != domain.RecommendationOpenLong {
		t.Fatalf("a 0.1 weighted filter must not stop 10 of direction, got %s", soft.Reason)
	}

	hard := Evaluate(in, setOf(0.5, weights, IDCriticalNews))
	if hard.Action != domain.RecommendationNoAction || hard.Reason != domain.StrategyReasonVetoed {
		t.Fatalf("a hard veto must stop the entry, got %s (%s)", hard.Action, hard.Reason)
	}
}

// TestMinSignalKeepsWeakAgreementOut guards the second gate: even without any
// filter, a thin majority is not a trade.
func TestMinSignalKeepsWeakAgreementOut(t *testing.T) {
	in := bullishInput()
	decision := Evaluate(in, setOf(3.0, map[string]float64{IDEMATrend: 1, IDADXDirectional: 1}))
	if decision.Action != domain.RecommendationNoAction || decision.Reason != domain.StrategyReasonBelowMinSignal {
		t.Fatalf("expected the minimum signal to reject the entry, got %s (%s)", decision.Action, decision.Reason)
	}
}

// TestDisabledStrategyDoesNotVote makes the settings switch meaningful.
func TestDisabledStrategyDoesNotVote(t *testing.T) {
	in := bullishInput()
	decision := Evaluate(in, setOf(0.1, map[string]float64{IDADXDirectional: 1}))
	for _, vote := range decision.Votes {
		if vote.ID != IDADXDirectional {
			t.Fatalf("a disabled strategy voted: %s", vote.ID)
		}
	}
	if len(decision.Votes) != 1 {
		t.Fatalf("expected exactly one vote, got %d", len(decision.Votes))
	}
}

// TestConfidenceGrowsWithAgreement checks the mapping onto the 0-100 scale the
// risk engine and the min-confidence filter already speak.
func TestConfidenceGrowsWithAgreement(t *testing.T) {
	policy := setOf(0.2, map[string]float64{
		IDEMATrend: 1, IDADXDirectional: 1, IDMarketStructure: 1, IDCompositeScore: 1,
	})

	agreed := Evaluate(bullishInput(), policy)

	// The same policy, but the composite score and the structure now argue the
	// other way: the surviving margin shrinks and so must the confidence.
	contested := bullishInput()
	contested.Analysis.Scores.Net = -0.6
	contested.Analysis.Structure.State = domain.StructureBearish
	split := Evaluate(contested, policy)

	if agreed.Action != domain.RecommendationOpenLong || split.Action != domain.RecommendationOpenLong {
		t.Fatalf("both cases must still take the long: %s and %s", agreed.Reason, split.Reason)
	}
	for _, decision := range []domain.StrategyDecision{agreed, split} {
		if decision.Confidence < 50 || decision.Confidence > 95 {
			t.Fatalf("confidence must stay inside 50..95, got %d", decision.Confidence)
		}
	}
	if split.Confidence >= agreed.Confidence {
		t.Fatalf("disagreement must lower confidence: %d vs %d", split.Confidence, agreed.Confidence)
	}
}

// TestConfidenceClearsTheDefaultMinimum locks the mapping that the first
// version got wrong: with the shipped defaults a broad one-sided vote has to
// read as a confident signal. When it did not, every entry landed in the 50-60
// band and the configured minimum confidence rejected practically all of them.
func TestConfidenceClearsTheDefaultMinimum(t *testing.T) {
	// A broad classic profile rather than the shipped preset: the preset's two
	// rules need candle history and a market context, which this fixture has no
	// reason to carry. What is under test here is the confidence mapping.
	broad := setOf(1.0, map[string]float64{
		IDMACD: 1.0, IDMarketStructure: 0.9, IDMomentum: 0.8, IDEMATrend: 0.8,
		IDADXDirectional: 0.7, IDCompositeScore: 0.6,
	})
	broad.Sides = domain.SidesLongOnly
	decision := Evaluate(bullishInput(), broad)
	if !decision.IsEntry() {
		t.Fatalf("a one-sided classic profile must take this trend, got %s (%s)", decision.Action, decision.Reason)
	}
	if decision.Confidence < 60 {
		t.Fatalf("a one-sided vote must clear the usual minimum confidence, got %d", decision.Confidence)
	}
}

// TestFiltersStayQuietInAnOrdinaryMarket guards the second half of the same
// bug: filters calibrated in absolute percentages fired on almost every bar and
// silently became a constant tax on the signal.
func TestFiltersStayQuietInAnOrdinaryMarket(t *testing.T) {
	in := bullishInput()
	// A perfectly ordinary 5m picture: average range 0.2%, levels half a percent
	// away, volatility in the middle of its own range.
	in.Analysis.Indicators.ATRPct = f(0.2)
	in.Analysis.Indicators.ATRPercentile = f(50)
	in.Snapshot.KeyLevels = []domain.Level{
		{Price: 100.5, Type: domain.LevelResistance, Strength: 0.9, DistancePct: 0.5},
		{Price: 99.5, Type: domain.LevelSupport, Strength: 0.9, DistancePct: -0.5},
	}

	// The filters come from the shipped catalog; the directional side is stated
	// explicitly so the test does not depend on which preset happens to ship.
	policy := Normalize(setOf(1.0, map[string]float64{
		IDMACD: 1.0, IDEMATrend: 0.8, IDADXDirectional: 0.7,
	}))
	policy.Sides = domain.SidesLongOnly
	decision := Evaluate(in, policy)
	if decision.Action != domain.RecommendationOpenLong {
		t.Fatalf("an ordinary bullish picture must still be tradable, got %s (%s)", decision.Action, decision.Reason)
	}
	// A filter that argues against the side nobody is taking costs nothing; only
	// one aimed at the direction actually chosen is a tax on the signal.
	for _, vote := range decision.Votes {
		if vote.Kind == domain.StrategyFilter && filterApplies(vote.Blocks, decision.Direction) {
			t.Fatalf("no filter should block the traded side in an ordinary market, got %s (%s)", vote.ID, vote.Detail)
		}
	}

	// The same levels are in the way once the bars themselves become tiny.
	in.Analysis.Indicators.ATRPct = f(1.5)
	blocked := Evaluate(in, DefaultSet())
	found := false
	for _, vote := range blocked.Votes {
		found = found || vote.ID == IDOpposingLevel
	}
	if !found {
		t.Fatal("a level within the average bar range must still be reported")
	}
}

// TestNormalizeRepairsStoredPolicy covers upgrades: an old document keeps its
// edits, gains the strategies added since, and loses anything unknown.
func TestNormalizeRepairsStoredPolicy(t *testing.T) {
	stored := domain.StrategySet{
		MinSignal: 0,
		Items: []domain.StrategyConfig{
			{ID: IDEMATrend, Enabled: false, Weight: 2.5},
			{ID: "strategy_from_the_future", Enabled: true, Weight: 9},
			{ID: IDCompositeScore, Enabled: true, Weight: 500},
			{ID: IDVWAP, Enabled: true, Weight: -0.4},
			{ID: IDMACD, Enabled: true, Weight: 0.8, HardVeto: true},
		},
	}
	got := Normalize(stored)

	if got.MinSignal != DefaultMinSignal {
		t.Fatalf("an invalid minimum must fall back to the default, got %v", got.MinSignal)
	}
	if len(got.Items) != len(Catalog()) {
		t.Fatalf("every known strategy must be present: %d vs %d", len(got.Items), len(Catalog()))
	}
	if _, ok := got.Find("strategy_from_the_future"); ok {
		t.Fatal("an unknown strategy must be dropped")
	}
	ema, _ := got.Find(IDEMATrend)
	if ema.Enabled || ema.Weight != 2.5 {
		t.Fatalf("user edits must survive normalisation: %+v", ema)
	}
	// The fallback is the catalog default of that strategy, whatever it is.
	var defaultWeight float64
	for _, def := range Catalog() {
		if def.ID == IDCompositeScore {
			defaultWeight = def.Weight
		}
	}
	composite, _ := got.Find(IDCompositeScore)
	if composite.Weight != defaultWeight {
		t.Fatalf("an out-of-range weight must fall back to the default %v, got %v", defaultWeight, composite.Weight)
	}
	// A negative weight is a deliberate choice - fading a strategy - not a typo.
	vwap, _ := got.Find(IDVWAP)
	if vwap.Weight != -0.4 {
		t.Fatalf("a faded strategy must keep its negative weight, got %v", vwap.Weight)
	}
	macd, _ := got.Find(IDMACD)
	if macd.HardVeto {
		t.Fatal("a directional strategy cannot carry a hard veto")
	}
}

// TestNegativeWeightFadesAStrategy covers the counter-trend profile: the same
// bullish picture has to produce a short once the trend strategies are faded.
func TestNegativeWeightFadesAStrategy(t *testing.T) {
	in := bullishInput()

	long := Evaluate(in, setOf(0.5, map[string]float64{IDEMATrend: 1, IDADXDirectional: 1}))
	if long.Action != domain.RecommendationOpenLong {
		t.Fatalf("the plain profile must go long, got %s (%s)", long.Action, long.Reason)
	}

	faded := Evaluate(in, setOf(0.5, map[string]float64{IDEMATrend: -1, IDADXDirectional: -1}))
	if faded.Action != domain.RecommendationOpenShort {
		t.Fatalf("a faded profile must take the other side, got %s (%s)", faded.Action, faded.Reason)
	}
	if faded.Confidence != long.Confidence {
		t.Fatalf("fading must mirror the vote, not weaken it: %d vs %d", faded.Confidence, long.Confidence)
	}
}

// TestRegimeAdaptiveWeighting covers the idea itself: the same vote has to
// count for more when the market suits its style and for less when it does not.
func TestRegimeAdaptiveWeighting(t *testing.T) {
	in := bullishInput()
	in.Analysis.Regime = domain.Regime{Primary: domain.RegimeStrongUptrend}
	in.Analysis.Indicators.ADX = f(32)

	policy := setOf(0.1, map[string]float64{IDEMATrend: 1})
	plain := Evaluate(in, policy)
	policy.RegimeAdaptive = true
	trending := Evaluate(in, policy)
	if trending.NetScore <= plain.NetScore {
		t.Fatalf("a trend follower must weigh more inside a trend: %.3f vs %.3f",
			trending.NetScore, plain.NetScore)
	}

	// The same follower inside a quiet range has to be damped instead.
	in.Analysis.Regime = domain.Regime{Primary: domain.RegimeRange}
	in.Analysis.Indicators.ADX = f(12)
	ranging := Evaluate(in, policy)
	if ranging.NetScore >= plain.NetScore {
		t.Fatalf("a trend follower must weigh less inside a range: %.3f vs %.3f",
			ranging.NetScore, plain.NetScore)
	}
}

// TestExtensionGuardBlocksLateEntries covers the second idea: joining a move
// that already ran far from its own average is what the losing trades did.
func TestExtensionGuardBlocksLateEntries(t *testing.T) {
	in := bullishInput()
	in.Analysis.Indicators.ATR = f(1)
	in.Analysis.Indicators.EMA = map[string]float64{"20": 100, "50": 103, "200": 98}

	in.Price = 100.8 // less than one average range above the anchor
	if got := extensionGuard(in); got.blocks != domain.BlocksNothing {
		t.Fatalf("a normal distance must not block anything: %+v", got)
	}
	in.Price = 104 // four average ranges above it
	got := extensionGuard(in)
	if got.blocks != domain.BlocksLong || got.strength < 0.4 {
		t.Fatalf("an extended move must block the long side: %+v", got)
	}
	in.Price = 96
	if got := extensionGuard(in); got.blocks != domain.BlocksShort {
		t.Fatalf("the mirror case must block the short side: %+v", got)
	}
}

// TestSidesRestrictTheDirection covers the shipped asymmetry: the ensemble is
// long-only by default because its short signals were measured to be
// anti-predictive, and the restriction has to hold whatever the vote says.
func TestSidesRestrictTheDirection(t *testing.T) {
	in := bullishInput()
	// A bearish picture the policy would otherwise want to short.
	in.Analysis.Indicators.PlusDI, in.Analysis.Indicators.MinusDI = f(12), f(28)
	in.Analysis.Indicators.PriceVsEMA50Pct = f(-2.5)
	in.Analysis.Indicators.PriceVsEMA200Pct = f(-6)
	in.Analysis.Indicators.EMA = map[string]float64{"50": 97, "200": 102}
	in.Analysis.Structure = domain.MarketStructure{State: domain.StructureBearish}
	in.Analysis.Scores = domain.SignalScores{Net: -0.6}

	weights := map[string]float64{IDEMATrend: 1, IDADXDirectional: 1, IDMarketStructure: 1}

	both := setOf(0.5, weights)
	if decision := Evaluate(in, both); decision.Action != domain.RecommendationOpenShort {
		t.Fatalf("an unrestricted policy must take the short, got %s (%s)", decision.Action, decision.Reason)
	}

	longOnly := setOf(0.5, weights)
	longOnly.Sides = domain.SidesLongOnly
	decision := Evaluate(in, longOnly)
	if decision.Action != domain.RecommendationNoAction || decision.Reason != domain.StrategyReasonSideDisabled {
		t.Fatalf("a long-only policy must refuse the short, got %s (%s)", decision.Action, decision.Reason)
	}
	// The vote itself is still reported: the user has to be able to see what was
	// refused and why, otherwise the restriction is indistinguishable from silence.
	if decision.NetScore >= 0 || len(decision.Votes) == 0 {
		t.Fatalf("the refused vote must still be visible: net %.2f, %d votes", decision.NetScore, len(decision.Votes))
	}

	shortOnly := setOf(0.5, weights)
	shortOnly.Sides = domain.SidesShortOnly
	if got := Evaluate(in, shortOnly); got.Action != domain.RecommendationOpenShort {
		t.Fatalf("a short-only policy must still take the short, got %s (%s)", got.Action, got.Reason)
	}
}

// TestDefaultPolicyIsLongOnlyAndGated pins the two defaults the history argued
// for, so neither can be lost silently in a later edit.
func TestDefaultPolicyIsLongOnlyAndGated(t *testing.T) {
	set := DefaultSet()
	if set.Sides != domain.SidesLongOnly {
		t.Fatalf("the shipped policy must be long-only, got %q", set.Sides)
	}
	gate, ok := set.Find(IDTrendGate)
	if !ok || !gate.Enabled || !gate.HardVeto {
		t.Fatalf("the trend gate must ship enabled as a veto, got %+v", gate)
	}
	// An older stored document knows about neither and must be upgraded rather
	// than left on the previous behaviour.
	stored := domain.StrategySet{MinSignal: 1, Items: []domain.StrategyConfig{{ID: IDMACD, Enabled: true, Weight: 1}}}
	upgraded := Normalize(stored)
	if upgraded.Sides != domain.SidesLongOnly {
		t.Fatalf("an upgraded policy must adopt the shipped side restriction, got %q", upgraded.Sides)
	}
	if gate, ok := upgraded.Find(IDTrendGate); !ok || !gate.Enabled || !gate.HardVeto {
		t.Fatalf("an upgraded policy must adopt the trend gate, got %+v", gate)
	}
}

// TestTrendGateBlocksTheCounterTrendSide covers the filter itself: it argues
// against the side that fights the slowest timeframe on screen, and stays quiet
// while price sits on its own average.
func TestTrendGateBlocksTheCounterTrendSide(t *testing.T) {
	daily := func(distance float64) Input {
		in := bullishInput()
		in.Snapshot.Timeframes = map[domain.Timeframe]domain.TimeframeAnalysis{
			domain.TF1d: {
				Timeframe:  domain.TF1d,
				Indicators: domain.Indicators{PriceVsEMA200Pct: f(distance), ATRPct: f(2)},
			},
		}
		return in
	}

	if got := trendGate(daily(12)); got.blocks != domain.BlocksShort {
		t.Fatalf("a daily uptrend must argue against shorts, got %+v", got)
	}
	if got := trendGate(daily(-12)); got.blocks != domain.BlocksLong {
		t.Fatalf("a daily downtrend must argue against longs, got %+v", got)
	}
	// Within one average bar range of the average there is no trend to defend.
	if got := trendGate(daily(0.5)); got.blocks != domain.BlocksNothing {
		t.Fatalf("price on its own average must not block anything, got %+v", got)
	}
}

// TestMarketGateBlocksAgainstTheBenchmark covers the market-wide filter: an
// asset with a clean chart of its own is still refused a long while the
// benchmark is below its own long daily average.
func TestMarketGateBlocksAgainstTheBenchmark(t *testing.T) {
	withContext := func(trend domain.MarketContextTrend, distance, slope float64) Input {
		in := bullishInput()
		in.Snapshot.MarketContext = domain.MarketContext{
			Benchmark: "BTC", Trend: trend,
			PriceVsEMA200Pct: &distance, EMA200SlopePct: &slope,
		}
		return in
	}

	if got := marketGate(withContext(domain.MarketTrendDown, -12, -3)); got.blocks != domain.BlocksLong {
		t.Fatalf("a falling benchmark must argue against longs, got %+v", got)
	}
	if got := marketGate(withContext(domain.MarketTrendUp, 12, 3)); got.blocks != domain.BlocksShort {
		t.Fatalf("a rising benchmark must argue against shorts, got %+v", got)
	}
	if got := marketGate(withContext(domain.MarketTrendFlat, 0.2, 1)); got.blocks != domain.BlocksNothing {
		t.Fatalf("a benchmark on its own average must not block anything, got %+v", got)
	}

	// The rule the four unseen four-hour windows argued for: price above the
	// average while the average itself falls is a rally inside a downtrend, and
	// trading it is what the policy lost most of its money to.
	if got := marketGate(withContext(domain.MarketTrendUp, 6, -2)); got.blocks != domain.BlocksLong {
		t.Fatalf("a rally above a falling average must still refuse longs, got %+v", got)
	}
	lifted := withContext(domain.MarketTrendUp, 6, -2)
	lifted.MarketGateAllowFallingAverage = true
	if got := marketGate(lifted); got.blocks == domain.BlocksLong {
		t.Fatalf("lifting the demand must let that trade through, got %+v", got)
	}

	// A buffer demands that the benchmark be clear of its average, not merely on
	// the right side of it.
	buffered := withContext(domain.MarketTrendUp, 2, 3)
	buffered.MarketGateLongBufferPct = 5
	if got := marketGate(buffered); got.blocks != domain.BlocksLong {
		t.Fatalf("a long must be refused until the buffer is there, got %+v", got)
	}

	// Missing context and a missing slope both have to let a trade through: a
	// filter that fires on absent data would stop the system on a short history.
	if got := marketGate(bullishInput()); got.blocks != domain.BlocksNothing {
		t.Fatalf("an unknown benchmark must not block anything, got %+v", got)
	}
	noSlope := bullishInput()
	distance := 12.0
	noSlope.Snapshot.MarketContext = domain.MarketContext{
		Benchmark: "BTC", Trend: domain.MarketTrendUp, PriceVsEMA200Pct: &distance,
	}
	if got := marketGate(noSlope); got.blocks == domain.BlocksLong {
		t.Fatalf("an unknown slope must not refuse the long, got %+v", got)
	}

	// End to end through a policy that does produce a direction: the veto has to
	// stop it. The shipped preset is deliberately not used here - its two rules
	// stay silent on this fixture, and the test would then pass for the wrong
	// reason.
	policy := setOf(0.5, map[string]float64{IDEMATrend: 1, IDADXDirectional: 1})
	policy.Sides = domain.SidesLongOnly
	for i, item := range policy.Items {
		if item.ID == IDMarketGate {
			policy.Items[i].Enabled, policy.Items[i].Weight, policy.Items[i].HardVeto = true, 2.0, true
		}
	}
	decision := Evaluate(withContext(domain.MarketTrendDown, -12, -3), policy)
	if decision.Action != domain.RecommendationNoAction || decision.Reason != domain.StrategyReasonVetoed {
		t.Fatalf("the gate must veto a long against a falling market, got %s (%s)",
			decision.Action, decision.Reason)
	}
	if Evaluate(bullishInput(), policy).Action != domain.RecommendationOpenLong {
		t.Fatal("without market context the same picture must still be tradable")
	}
}

// TestCostFloorScalesWithTheConfiguredMultiple pins the knob that decides how
// many round trips a target has to be worth.
func TestCostFloorScalesWithTheConfiguredMultiple(t *testing.T) {
	in := bullishInput()
	in.Analysis.Indicators.ATRPct = f(0.4)
	in.RoundTripCostPct = 0.115

	// 2 * 0.4% of target against 3 * 0.115% of cost: comfortably payable.
	if got := costFloor(in); got.blocks != domain.BlocksNothing {
		t.Fatalf("a payable target must not be blocked: %+v", got)
	}
	// The same trade against a demand for sixteen round trips is not.
	in.CostFloorMultiple = 16
	if got := costFloor(in); got.blocks != domain.BlocksBoth {
		t.Fatalf("a stricter floor must block the same trade: %+v", got)
	}
}

// TestRelativeStrengthGateRefusesLaggards covers the cross-sectional filter:
// when only a few positions can be open, an asset in the bottom of its own
// universe is not worth one of them. It ships off, so the test drives it
// directly rather than through the default policy.
func TestRelativeStrengthGateRefusesLaggards(t *testing.T) {
	ranked := func(rank float64) Input {
		in := bullishInput()
		in.MinRelativeStrengthPct = 60
		in.Snapshot.UniverseContext = domain.UniverseContext{RankPct: rank, Members: 14}
		return in
	}

	if got := relativeStrengthGate(ranked(20)); got.blocks != domain.BlocksLong {
		t.Fatalf("a laggard must not be bought, got %+v", got)
	}
	if got := relativeStrengthGate(ranked(95)); got.blocks != domain.BlocksShort {
		t.Fatalf("the strongest asset must not be shorted, got %+v", got)
	}
	// A threshold above the middle makes both demands overlap: an asset in the
	// band satisfies neither side, and the filter has to say so rather than
	// appearing to permit the side it did not check.
	if got := relativeStrengthGate(ranked(50)); got.blocks != domain.BlocksBoth {
		t.Fatalf("an asset that is neither strong nor weak enough must block both sides, got %+v", got)
	}
	middle := ranked(50)
	middle.MinRelativeStrengthPct = 50
	if got := relativeStrengthGate(middle); got.blocks != domain.BlocksNothing {
		t.Fatalf("exactly at a symmetric threshold nothing is blocked, got %+v", got)
	}
	// Without a ranking there are no peers to lose to.
	if got := relativeStrengthGate(bullishInput()); got.blocks != domain.BlocksNothing {
		t.Fatalf("an unranked universe must not block anything, got %+v", got)
	}
	// It is off in the shipped policy: the history did not confirm it.
	if cfg, ok := DefaultSet().Find(IDRelStrengthGate); !ok || cfg.Enabled {
		t.Fatalf("the cross-sectional gate must ship disabled, got %+v", cfg)
	}
}

// TestRegimeRulesReadTheMoveAndTheRegime covers the three rules derived from the
// forward-return study. Each is silent outside the condition it was measured on,
// which is what keeps them from becoming yet another always-on opinion.
func TestRegimeRulesReadTheMoveAndTheRegime(t *testing.T) {
	// A series that rises by a fixed step, then moves by the requested number of
	// average ranges over the final ten bars.
	series := func(moveRanges float64) []domain.Candle {
		const quiet, step = 40, 1.0
		out := make([]domain.Candle, 0, quiet+TrailingMoveLookback)
		price := 100.0
		for i := 0; i < quiet; i++ {
			out = append(out, domain.Candle{Open: price, High: price + step, Low: price - step, Close: price})
		}
		// The yardstick is the average range of the twenty bars before the move,
		// which is 2 * step here.
		total := moveRanges * 2 * step
		for i := 0; i < TrailingMoveLookback; i++ {
			price += total / TrailingMoveLookback
			out = append(out, domain.Candle{Open: price, High: price + step, Low: price - step, Close: price})
		}
		return out
	}
	withMarket := func(distance, slope float64, moveRanges float64) Input {
		in := bullishInput()
		in.Candles = series(moveRanges)
		in.Snapshot.MarketContext = domain.MarketContext{
			Benchmark: "BTC", Trend: domain.MarketTrendUp,
			PriceVsEMA200Pct: &distance, EMA200SlopePct: &slope,
		}
		return in
	}

	// Buying strength: only while the market as a whole rises, and only once the
	// move is large enough to be one.
	if got := regimeMomentum(withMarket(5, 2, 3)); got.direction != domain.PatternBullish {
		t.Fatalf("a strong move in a rising market must vote long, got %+v", got)
	}
	if got := regimeMomentum(withMarket(5, 2, 0.5)); got != noOpinion {
		t.Fatalf("an ordinary move must not vote, got %+v", got)
	}
	if got := regimeMomentum(withMarket(5, -2, 3)); got != noOpinion {
		t.Fatalf("a falling average must silence the rule, got %+v", got)
	}
	// Strength grows with the size of the move.
	weak, strong := regimeMomentum(withMarket(5, 2, 3)), regimeMomentum(withMarket(5, 2, 8))
	if !(strong.strength > weak.strength) {
		t.Fatalf("a bigger move must weigh more: %.2f vs %.2f", strong.strength, weak.strength)
	}

	// Buying a collapse asks nothing of the regime: the effect was measured in
	// both a rising and a falling market.
	if got := capitulationLong(withMarket(5, 2, -6)); got.direction != domain.PatternBullish {
		t.Fatalf("a collapse must vote long, got %+v", got)
	}
	if got := capitulationLong(withMarket(-5, -2, -6)); got.direction != domain.PatternBullish {
		t.Fatalf("the same holds while the market falls, got %+v", got)
	}
	if got := capitulationLong(withMarket(5, 2, -2)); got != noOpinion {
		t.Fatalf("an ordinary dip is not a capitulation, got %+v", got)
	}

	// Fading a rally is the one rule that argues for a short, and only inside a
	// falling market.
	if got := bearRallyFade(withMarket(-5, -2, 3)); got.direction != domain.PatternBearish {
		t.Fatalf("a rally inside a downtrend must vote short, got %+v", got)
	}
	if got := bearRallyFade(withMarket(5, 2, 3)); got != noOpinion {
		t.Fatalf("the same rally in a rising market must be ignored, got %+v", got)
	}

	// Only the first survived a full replay, and it is what the shipped preset is
	// built around. The other two measured well on forward returns and lost the
	// money back to their own stops, so they stay available and switched off.
	if cfg, ok := DefaultSet().Find(IDRegimeMomentum); !ok || !cfg.Enabled {
		t.Fatalf("the shipped policy must use the momentum rule, got %+v", cfg)
	}
	for _, id := range []string{IDCapitulationLong, IDBearRallyFade} {
		if cfg, ok := DefaultSet().Find(id); !ok || cfg.Enabled {
			t.Fatalf("%s must ship disabled, got %+v", id, cfg)
		}
	}
}

// TestTrailingMoveIsMeasuredAgainstEarlierBars pins the yardstick: a violent
// stretch must not inflate the range it is compared against, or every move would
// look ordinary.
func TestTrailingMoveIsMeasuredAgainstEarlierBars(t *testing.T) {
	if _, ok := trailingMoveRanges(nil); ok {
		t.Fatal("no candles means no measurement")
	}
	short := make([]domain.Candle, TrailingMoveLookback)
	if _, ok := trailingMoveRanges(short); ok {
		t.Fatal("a series shorter than the yardstick must not produce a number")
	}

	candles := make([]domain.Candle, 0, 40)
	price := 100.0
	for i := 0; i < 30; i++ {
		candles = append(candles, domain.Candle{Open: price, High: price + 1, Low: price - 1, Close: price})
	}
	// Ten bars that add ten points on a two-point average range: five ranges.
	for i := 0; i < TrailingMoveLookback; i++ {
		price++
		candles = append(candles, domain.Candle{Open: price, High: price + 50, Low: price - 50, Close: price})
	}
	move, ok := trailingMoveRanges(candles)
	if !ok {
		t.Fatal("the measurement must succeed")
	}
	if math.Abs(move-5) > 1e-9 {
		t.Fatalf("the wide bars of the move itself must not count: expected 5 ranges, got %.3f", move)
	}
}
