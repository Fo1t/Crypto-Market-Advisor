package strategies

import (
	"testing"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

// TestPresetsAreUsablePolicies guards the profiles the settings screen offers:
// each has to survive validation, keep the safety vetoes and name at least one
// directional rule, or the user could pick a policy that cannot trade.
func TestPresetsAreUsablePolicies(t *testing.T) {
	presets := Presets()
	if len(presets) < 2 {
		t.Fatal("there must be something to choose between")
	}

	defaults := 0
	seen := map[string]bool{}
	for _, preset := range presets {
		if seen[preset.ID] {
			t.Fatalf("duplicate preset %s", preset.ID)
		}
		seen[preset.ID] = true
		if preset.Default {
			defaults++
		}
		if err := Validate(preset.Set); err != nil {
			t.Fatalf("%s is not a valid policy: %v", preset.ID, err)
		}
		if preset.Set.Sides != domain.SidesLongOnly {
			t.Fatalf("%s must stay long-only, got %q", preset.ID, preset.Set.Sides)
		}
		for _, id := range []string{IDCostFloor, IDCriticalNews, IDMarketGate, IDTrendGate} {
			cfg, ok := preset.Set.Find(id)
			if !ok || !cfg.Enabled || !cfg.HardVeto {
				t.Fatalf("%s must keep the %s veto, got %+v", preset.ID, id, cfg)
			}
		}
		if preset.ProfitFactor4h <= 0 || preset.Trades4h <= 0 {
			t.Fatalf("%s must carry the measurement it is offered on: %+v", preset.ID, preset)
		}
	}
	if defaults != 1 {
		t.Fatalf("exactly one preset must be the default, found %d", defaults)
	}
	if DefaultPreset().ID != PresetRegimeMomentum {
		t.Fatalf("the measured winner must be the default, got %s", DefaultPreset().ID)
	}
}

// TestDefaultSetMatchesTheDefaultPreset keeps the two entry points from drifting
// apart: a fresh installation and the preset list have to describe the same
// policy.
func TestDefaultSetMatchesTheDefaultPreset(t *testing.T) {
	set, preset := DefaultSet(), DefaultPreset().Set
	if set.MinSignal != preset.MinSignal {
		t.Fatalf("thresholds differ: %v vs %v", set.MinSignal, preset.MinSignal)
	}
	for _, item := range preset.Items {
		got, ok := set.Find(item.ID)
		if !ok || got.Enabled != item.Enabled || got.Weight != item.Weight {
			t.Fatalf("%s differs: %+v vs %+v", item.ID, got, item)
		}
	}
}

// TestShippedPresetTradesARisingMarket is the end-to-end guard on the profile a
// fresh installation gets: given the picture it was built for - a market rising
// as a whole and an asset that has just run - it must actually open a position.
// Without this a preset could ship that never trades at all.
func TestShippedPresetTradesARisingMarket(t *testing.T) {
	const bars = 260
	candles := make([]domain.Candle, 0, bars)
	price := 100.0
	for i := 0; i < bars; i++ {
		step := 0.2
		if i >= bars-TrailingMoveLookback {
			step = 3.0 // the run the momentum rule is looking for
		}
		price += step
		candles = append(candles, domain.Candle{
			Open: price - step, High: price + 0.5, Low: price - step - 0.5, Close: price,
		})
	}

	distance, slope := 8.0, 4.0
	in := Input{
		Timeframe: domain.TF4h,
		Price:     price,
		Candles:   candles,
		Analysis: domain.TimeframeAnalysis{
			Timeframe:  domain.TF4h,
			Indicators: domain.Indicators{ATRPct: f(1.2), ATRPercentile: f(50)},
			Scores:     domain.SignalScores{Net: 0.5},
		},
		Snapshot: domain.FeatureSnapshot{
			TrendAlignment: domain.TrendAlignment{
				Bullish: []domain.Timeframe{domain.TF4h, domain.TF1d}, AlignmentScore: 0.8,
			},
			MarketContext: domain.MarketContext{
				Benchmark: "BTC", Trend: domain.MarketTrendUp,
				PriceVsEMA200Pct: &distance, EMA200SlopePct: &slope,
			},
		},
		RoundTripCostPct: 0.115,
	}

	decision := Evaluate(in, DefaultSet())
	if decision.Action != domain.RecommendationOpenLong {
		t.Fatalf("the shipped preset must trade the picture it was built for, got %s (%s)",
			decision.Action, decision.Reason)
	}
	if decision.Confidence < 50 {
		t.Fatalf("confidence must land on the published scale, got %d", decision.Confidence)
	}
}
