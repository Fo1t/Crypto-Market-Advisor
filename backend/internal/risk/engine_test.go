package risk

import (
	"math"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/crypto-market-advisor/advisor/internal/config"
	"github.com/crypto-market-advisor/advisor/internal/domain"
)

func testConfig() config.RiskConfig {
	return config.RiskConfig{
		MinLeverage:                    5,
		MaxLeverage:                    50,
		MaxRecommendedAllocPct:         decimal.NewFromInt(15),
		HighVolatilityATRPct:           1.5,
		ExtremeVolatilityATRPct:        3.0,
		MinConfidence:                  55,
		CriticalNewsMaxLeverage:        15,
		CriticalNewsHighVolMaxLeverage: 8,
		CriticalNewsMaxAge:             2 * time.Hour,
	}
}

func TestFreshCriticalNewsCapsExposureWithoutChoosingDirection(t *testing.T) {
	engine := New(testConfig())
	in := longInput(0.5, 99000)
	in.LLMLeverage = 40
	in.Snapshot.NewsContext = domain.NewsSnapshot{
		Status:        domain.NewsContextOK,
		AssetSpecific: []domain.NewsSnapshotItem{{Critical: true, AgeMinutes: 10}},
	}
	cap, note := engine.NewsLeverageCap(in.Snapshot)
	if cap != 15 || note == "" {
		t.Fatalf("preflight cap=%d note=%q", cap, note)
	}
	got := engine.Evaluate(in)
	if got.Leverage.RiskMaximum > 15 || got.Leverage.Recommended > 15 {
		t.Fatalf("critical-news cap not enforced: %+v", got.Leverage)
	}
	if in.Action != domain.RecommendationOpenLong {
		t.Fatal("risk rule must not alter trade direction")
	}
}

// snapshot builds a snapshot with the given 1h ATR percentage.
func snapshot(atrPct float64, regime domain.MarketRegime, alignment float64) domain.FeatureSnapshot {
	atr := atrPct
	return domain.FeatureSnapshot{
		Timeframes: map[domain.Timeframe]domain.TimeframeAnalysis{
			domain.TF1h: {Indicators: domain.Indicators{ATRPct: &atr}},
		},
		AggregateRegime: domain.Regime{Primary: regime},
		TrendAlignment:  domain.TrendAlignment{AlignmentScore: alignment},
		DataQuality:     domain.DataQuality{Status: domain.DataQualityOK},
	}
}

func longInput(atrPct float64, stopPrice float64) Input {
	return Input{
		Action:         domain.RecommendationOpenLong,
		Confidence:     80,
		LLMLeverage:    20,
		LLMAllocation:  decimal.NewFromInt(8),
		LLMRiskLevel:   domain.RiskMedium,
		ReferencePrice: 100000,
		StopLoss:       []domain.PriceTarget{{Price: stopPrice, ClosePct: 100}},
		Snapshot:       snapshot(atrPct, domain.RegimeStrongUptrend, 0.8),
	}
}

func TestLeverageIsClampedByStopDistance(t *testing.T) {
	engine := New(testConfig())

	// A stop 5% away allows at most 35/5 = 7x.
	got := engine.Evaluate(longInput(0.8, 95000))

	if got.Leverage.RiskMaximum != 7 {
		t.Fatalf("expected a 7x cap from the stop distance, got %d", got.Leverage.RiskMaximum)
	}
	if got.Leverage.Recommended != 7 {
		t.Fatalf("the recommendation must be clamped to the risk maximum, got %d", got.Leverage.Recommended)
	}
	if got.Leverage.LLMSuggested != 20 {
		t.Fatalf("the model's own suggestion must be preserved for display, got %d", got.Leverage.LLMSuggested)
	}
	if len(got.Notes) == 0 {
		t.Fatal("a clamped leverage must be explained")
	}
}

func TestTightStopAllowsHigherLeverage(t *testing.T) {
	engine := New(testConfig())

	// A stop 1% away allows 35x, so the model's 20x survives.
	got := engine.Evaluate(longInput(0.5, 99000))

	if got.Leverage.Recommended != 20 {
		t.Fatalf("expected the model suggestion to survive, got %d", got.Leverage.Recommended)
	}
}

func TestHigherVolatilityLowersLeverage(t *testing.T) {
	engine := New(testConfig())

	calm := engine.Evaluate(longInput(0.4, 99000))
	elevated := engine.Evaluate(longInput(2.0, 99000))
	extreme := engine.Evaluate(longInput(4.0, 99000))

	if calm.Leverage.RiskMaximum <= elevated.Leverage.RiskMaximum {
		t.Fatalf("elevated volatility must reduce the cap: calm=%d elevated=%d",
			calm.Leverage.RiskMaximum, elevated.Leverage.RiskMaximum)
	}
	if elevated.Leverage.RiskMaximum <= extreme.Leverage.RiskMaximum {
		t.Fatalf("extreme volatility must reduce the cap further: elevated=%d extreme=%d",
			elevated.Leverage.RiskMaximum, extreme.Leverage.RiskMaximum)
	}
	if extreme.Leverage.RiskMaximum > 8 {
		t.Fatalf("extreme volatility must cap leverage at 8x, got %d", extreme.Leverage.RiskMaximum)
	}
}

func TestLeverageNeverLeavesTheConfiguredRange(t *testing.T) {
	engine := New(testConfig())

	in := longInput(6.0, 80000) // extreme volatility and a very wide stop
	got := engine.Evaluate(in)

	if got.Leverage.Recommended < 5 || got.Leverage.Recommended > 50 {
		t.Fatalf("leverage %d is outside the configured 5..50 range", got.Leverage.Recommended)
	}
	if got.Leverage.RiskMaximum < 5 {
		t.Fatalf("the risk maximum must not fall below the floor, got %d", got.Leverage.RiskMaximum)
	}
}

func TestLowConfidenceForcesMinimumLeverage(t *testing.T) {
	engine := New(testConfig())

	in := longInput(0.5, 99000)
	in.Confidence = 40
	got := engine.Evaluate(in)

	if got.Leverage.Recommended != 5 {
		t.Fatalf("confidence below the threshold must force minimum leverage, got %d", got.Leverage.Recommended)
	}
}

func TestAllocationIsCappedByConfiguration(t *testing.T) {
	engine := New(testConfig())

	in := longInput(0.5, 99000)
	in.LLMAllocation = decimal.NewFromInt(60)
	got := engine.Evaluate(in)

	if got.AllocationPct.GreaterThan(decimal.NewFromInt(15)) {
		t.Fatalf("allocation must not exceed the configured maximum, got %s", got.AllocationPct)
	}
}

func TestAllocationShrinksWithLowConfidence(t *testing.T) {
	engine := New(testConfig())

	strong := longInput(0.5, 99000)
	weak := longInput(0.5, 99000)
	weak.Confidence = 60

	got := engine.Evaluate(strong)
	gotWeak := engine.Evaluate(weak)

	if !gotWeak.AllocationPct.LessThan(got.AllocationPct) {
		t.Fatalf("lower confidence must reduce allocation: %s vs %s", gotWeak.AllocationPct, got.AllocationPct)
	}
}

func TestDegradedDataReducesExposure(t *testing.T) {
	engine := New(testConfig())

	in := longInput(0.5, 99000)
	degraded := longInput(0.5, 99000)
	degraded.Snapshot.DataQuality = domain.DataQuality{Status: domain.DataQualityDegraded, MissingFields: []string{"volume"}}

	clean := engine.Evaluate(in)
	dirty := engine.Evaluate(degraded)

	if dirty.Leverage.RiskMaximum >= clean.Leverage.RiskMaximum {
		t.Fatalf("degraded data must lower the leverage cap: %d vs %d",
			dirty.Leverage.RiskMaximum, clean.Leverage.RiskMaximum)
	}
	if !dirty.AllocationPct.LessThan(clean.AllocationPct) {
		t.Fatalf("degraded data must lower allocation: %s vs %s", dirty.AllocationPct, clean.AllocationPct)
	}
}

func TestMissingStopLossIsTreatedConservatively(t *testing.T) {
	engine := New(testConfig())

	in := longInput(0.5, 99000)
	in.StopLoss = nil
	got := engine.Evaluate(in)

	if got.Leverage.RiskMaximum > 10 {
		t.Fatalf("without a stop the cap must stay low, got %d", got.Leverage.RiskMaximum)
	}
	if got.StopDistance != nil {
		t.Fatal("stop distance must be absent when there is no stop")
	}
}

func TestNonEntryActionsCarryNoExposure(t *testing.T) {
	engine := New(testConfig())

	for _, action := range []domain.RecommendationAction{domain.RecommendationNoAction, domain.RecommendationManage} {
		in := longInput(0.5, 99000)
		in.Action = action
		got := engine.Evaluate(in)

		if !got.AllocationPct.IsZero() {
			t.Fatalf("%s must not carry an allocation, got %s", action, got.AllocationPct)
		}
		if got.Leverage.Recommended != 0 {
			t.Fatalf("%s must not carry leverage, got %d", action, got.Leverage.Recommended)
		}
	}
}

func TestRiskLevelIsRecomputedNotTrusted(t *testing.T) {
	engine := New(testConfig())

	in := longInput(5.0, 90000)
	in.LLMRiskLevel = domain.RiskLow // the model claims it is a calm setup
	in.Confidence = 55

	got := engine.Evaluate(in)
	if got.RiskLevel == domain.RiskLow {
		t.Fatal("the engine must not accept a low risk label for a volatile wide-stop trade")
	}
}

func TestShortDirectionUsesOpposingLevels(t *testing.T) {
	engine := New(testConfig())

	in := Input{
		Action:         domain.RecommendationOpenShort,
		Confidence:     75,
		LLMLeverage:    25,
		LLMAllocation:  decimal.NewFromInt(6),
		ReferencePrice: 100000,
		StopLoss:       []domain.PriceTarget{{Price: 101000, ClosePct: 100}},
		Snapshot:       snapshot(0.5, domain.RegimeStrongDowntrend, -0.7),
	}
	in.Snapshot.KeyLevels = []domain.Level{
		{Price: 99500, Type: domain.LevelSupport, Strength: 0.8, DistancePct: -0.5},
	}

	got := engine.Evaluate(in)
	found := false
	for _, note := range got.Notes {
		if note != "" && len(note) > 0 && contains(note, "opposing level") {
			found = true
		}
	}
	if !found {
		t.Fatalf("a nearby support must be flagged for a short, notes: %v", got.Notes)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestRiskLevelReflectsHowHardTheEngineClamped(t *testing.T) {
	engine := New(testConfig())

	// A calm setup with a tight stop keeps most of the configured range.
	calm := engine.Evaluate(longInput(0.3, 99500))

	// A setup where several constraints fire and leverage is cut to the floor
	// must not be labelled low risk, even though its raw ATR is modest.
	clamped := longInput(0.3, 99500)
	clamped.Snapshot.AggregateRegime.Primary = domain.RegimeUncertain
	clamped.Snapshot.AggregateRegime.Tags = []domain.RegimeTag{domain.TagHighVolatility}
	clamped.Snapshot.TrendAlignment.AlignmentScore = 0.05
	clamped.Snapshot.DataQuality = domain.DataQuality{Status: domain.DataQualityDegraded}
	percentile := 98.0
	tf := clamped.Snapshot.Timeframes[domain.TF1h]
	tf.Indicators.ATRPercentile = &percentile
	clamped.Snapshot.Timeframes[domain.TF1h] = tf

	got := engine.Evaluate(clamped)
	if got.RiskLevel == domain.RiskLow {
		t.Fatalf("a heavily clamped setup must not read as low risk (notes: %v)", got.Notes)
	}
	if got.Leverage.RiskMaximum >= calm.Leverage.RiskMaximum {
		t.Fatalf("the clamped setup must allow less leverage: %d vs %d",
			got.Leverage.RiskMaximum, calm.Leverage.RiskMaximum)
	}
}

// TestRiskSizedAllocationEqualisesRisk covers the sizing rule itself: the size
// follows the stop, so a wide stop gets a small position and a tight one a
// larger position, and both lose the same share of capital when stopped.
func TestRiskSizedAllocationEqualisesRisk(t *testing.T) {
	const riskPct, leverage = 1.0, 5

	tight, ok := riskSizedAllocation(riskPct, leverage, 2)
	if !ok {
		t.Fatal("a 2% stop must produce a size")
	}
	wide, ok := riskSizedAllocation(riskPct, leverage, 8)
	if !ok {
		t.Fatal("an 8% stop must produce a size")
	}
	if !wide.LessThan(tight) {
		t.Fatalf("a wider stop must take a smaller position: %s vs %s", wide, tight)
	}

	// The invariant that matters: allocation * leverage * stop distance is the
	// loss on the account, and it has to be the risk budget in both cases.
	for _, c := range []struct {
		allocation decimal.Decimal
		distance   float64
	}{{tight, 2}, {wide, 8}} {
		loss := c.allocation.InexactFloat64() * leverage * c.distance / 100
		if math.Abs(loss-riskPct) > 1e-9 {
			t.Fatalf("a stop-out must cost %.2f%% of capital, got %.4f%%", riskPct, loss)
		}
	}

	if _, ok := riskSizedAllocation(0, leverage, 2); ok {
		t.Fatal("no risk budget means no risk-derived size")
	}
	if _, ok := riskSizedAllocation(riskPct, leverage, 0); ok {
		t.Fatal("without a stop distance the rule has nothing to size against")
	}
}

// TestRiskBudgetOverridesTheRequestedAllocation checks the wiring end to end:
// with a budget configured the engine replaces the requested size, and without
// one it keeps the previous behaviour.
func TestRiskBudgetOverridesTheRequestedAllocation(t *testing.T) {
	sizingATR := 1.0
	in := Input{
		Action:         domain.RecommendationOpenLong,
		Confidence:     80,
		LLMLeverage:    5,
		LLMAllocation:  decimal.NewFromInt(5),
		ReferencePrice: 100,
		StopLoss:       []domain.PriceTarget{{Price: 92, ClosePct: 100}}, // 8% away
		Snapshot: domain.FeatureSnapshot{
			DataQuality: domain.DataQuality{Status: domain.DataQualityOK},
			Timeframes: map[domain.Timeframe]domain.TimeframeAnalysis{
				domain.TF1h: {Timeframe: domain.TF1h, Indicators: domain.Indicators{ATRPct: &sizingATR}},
			},
		},
	}

	base := testConfig()
	// An 8% stop caps leverage at the floor, and the existing rule then trims the
	// size by a fifth: 5% requested becomes 4%.
	fixed := New(base).Evaluate(in)
	if !fixed.AllocationPct.Equal(decimal.NewFromInt(4)) {
		t.Fatalf("without a budget the requested size stands after the usual trim, got %s", fixed.AllocationPct)
	}

	base.RiskPerTradePct = 1
	sized := New(base).Evaluate(in)
	// 1% of capital at 5x leverage and an 8% stop is a 2.5% position, trimmed by
	// the same fifth to 2%.
	if math.Abs(sized.AllocationPct.InexactFloat64()-2.0) > 0.01 {
		t.Fatalf("expected a 2%% position, got %s", sized.AllocationPct)
	}
	if sized.AllocationPct.GreaterThanOrEqual(fixed.AllocationPct) {
		t.Fatal("a wide stop must shrink the position below the requested size")
	}
}

// TestRiskBudgetSizesAgainstTheGrantedLeverage covers the case the previous test
// cannot see, because there the model already asked for the leverage it got: a
// stop-out costs margin * leverage * distance, so a size computed against a
// leverage the engine refused carries only a fraction of the intended risk.
func TestRiskBudgetSizesAgainstTheGrantedLeverage(t *testing.T) {
	sizingATR := 1.0
	in := Input{
		Action:         domain.RecommendationOpenLong,
		Confidence:     80,
		LLMLeverage:    25, // the engine will clamp this hard
		LLMAllocation:  decimal.NewFromInt(5),
		ReferencePrice: 100,
		StopLoss:       []domain.PriceTarget{{Price: 92, ClosePct: 100}}, // 8% away
		Snapshot: domain.FeatureSnapshot{
			DataQuality: domain.DataQuality{Status: domain.DataQualityOK},
			Timeframes: map[domain.Timeframe]domain.TimeframeAnalysis{
				domain.TF1h: {Timeframe: domain.TF1h, Indicators: domain.Indicators{ATRPct: &sizingATR}},
			},
		},
	}

	cfg := testConfig()
	cfg.RiskPerTradePct = 1
	out := New(cfg).Evaluate(in)

	// An 8% stop caps leverage at the floor of 5x. One percent of capital at 5x
	// and an 8% stop is a 2.5% position, trimmed by the existing fifth to 2%.
	if out.Leverage.Recommended != cfg.MinLeverage {
		t.Fatalf("expected the leverage floor, got %dx", out.Leverage.Recommended)
	}
	if math.Abs(out.AllocationPct.InexactFloat64()-2.0) > 0.01 {
		t.Fatalf("size must follow the granted %dx, not the requested %dx: got %s",
			out.Leverage.Recommended, in.LLMLeverage, out.AllocationPct)
	}

	// The risk actually taken is what the budget asked for: 2% of capital at 5x
	// with an 8% stop loses 0.8% of capital, against 0.16% had the size been
	// computed from the refused 25x.
	risk := out.AllocationPct.InexactFloat64() / 100 * float64(out.Leverage.Recommended) * 8
	if math.Abs(risk-0.8) > 0.02 {
		t.Fatalf("a stop-out should cost about 0.8%% of capital, got %.2f%%", risk)
	}
}
