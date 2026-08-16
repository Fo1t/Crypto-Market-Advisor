package patterns

import (
	"testing"
	"time"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

// candle builds one closed candle; timestamps are synthetic but ordered.
func candle(open, high, low, close float64) domain.Candle {
	return domain.Candle{Open: open, High: high, Low: low, Close: close, Volume: 1000, Closed: true}
}

// withTimes assigns increasing hourly timestamps so downstream code that reads
// them stays consistent.
func withTimes(candles []domain.Candle) []domain.Candle {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range candles {
		candles[i].OpenTime = base.Add(time.Duration(i) * time.Hour)
		candles[i].CloseTime = candles[i].OpenTime.Add(time.Hour)
	}
	return candles
}

// downtrend builds n declining candles with a body of ~1.0.
func downtrend(n int, start float64) []domain.Candle {
	out := make([]domain.Candle, 0, n)
	for i := 0; i < n; i++ {
		open := start - float64(i)
		close := open - 1
		out = append(out, candle(open, open+0.2, close-0.2, close))
	}
	return out
}

// uptrend builds n rising candles with a body of ~1.0.
func uptrend(n int, start float64) []domain.Candle {
	out := make([]domain.Candle, 0, n)
	for i := 0; i < n; i++ {
		open := start + float64(i)
		close := open + 1
		out = append(out, candle(open, close+0.2, open-0.2, close))
	}
	return out
}

func findPattern(patterns []domain.Pattern, name string) (domain.Pattern, bool) {
	for _, p := range patterns {
		if p.Name == name {
			return p, true
		}
	}
	return domain.Pattern{}, false
}

func requirePattern(t *testing.T, candles []domain.Candle, name string, dir domain.PatternDirection) domain.Pattern {
	t.Helper()
	found := DetectCandlestick(withTimes(candles), 3)
	p, ok := findPattern(found, name)
	if !ok {
		names := make([]string, 0, len(found))
		for _, f := range found {
			names = append(names, f.Name)
		}
		t.Fatalf("expected %s to be detected, got %v", name, names)
	}
	if p.Direction != dir {
		t.Fatalf("%s: expected direction %s, got %s", name, dir, p.Direction)
	}
	if p.Strength <= 0 || p.Strength > 1 {
		t.Fatalf("%s: strength must be in (0,1], got %v", name, p.Strength)
	}
	return p
}

func TestHammerAfterDowntrend(t *testing.T) {
	candles := append(downtrend(10, 100), candle(92.0, 92.35, 91.0, 92.3))
	p := requirePattern(t, candles, "hammer", domain.PatternBullish)
	if p.CandleIndex != 0 || p.AgeCandles != 0 {
		t.Fatalf("hammer must be reported on the newest candle, got index=%d age=%d", p.CandleIndex, p.AgeCandles)
	}
}

func TestHammerShapeInUptrendIsHangingMan(t *testing.T) {
	candles := append(uptrend(10, 100), candle(110.0, 110.35, 109.0, 110.3))
	requirePattern(t, candles, "hanging_man", domain.PatternBearish)

	found := DetectCandlestick(withTimes(candles), 3)
	if _, ok := findPattern(found, "hammer"); ok {
		t.Fatal("the same shape must not be reported as a hammer in an uptrend")
	}
}

func TestBullishEngulfing(t *testing.T) {
	candles := append(downtrend(10, 100),
		candle(92.0, 92.2, 90.8, 91.0),
		candle(90.9, 92.7, 90.8, 92.5),
	)
	requirePattern(t, candles, "bullish_engulfing", domain.PatternBullish)
}

func TestBearishEngulfing(t *testing.T) {
	candles := append(uptrend(10, 100),
		candle(110.0, 111.2, 109.9, 111.0),
		candle(111.2, 111.3, 109.4, 109.5),
	)
	requirePattern(t, candles, "bearish_engulfing", domain.PatternBearish)
}

func TestMorningStar(t *testing.T) {
	candles := append(downtrend(10, 100),
		candle(95.0, 95.1, 92.9, 93.0),
		candle(92.5, 92.7, 92.4, 92.6),
		candle(92.8, 94.6, 92.7, 94.5),
	)
	requirePattern(t, candles, "morning_star", domain.PatternBullish)
}

func TestEveningStar(t *testing.T) {
	candles := append(uptrend(10, 100),
		candle(110.0, 112.1, 109.9, 112.0),
		candle(112.5, 112.7, 112.4, 112.6),
		candle(112.3, 112.4, 110.4, 110.5),
	)
	requirePattern(t, candles, "evening_star", domain.PatternBearish)
}

func TestThreeWhiteSoldiers(t *testing.T) {
	candles := append(downtrend(10, 100),
		candle(90.0, 91.6, 89.9, 91.5),
		candle(90.8, 92.6, 90.7, 92.5),
		candle(91.9, 93.6, 91.8, 93.5),
	)
	requirePattern(t, candles, "three_white_soldiers", domain.PatternBullish)
}

func TestThreeBlackCrows(t *testing.T) {
	candles := append(uptrend(10, 100),
		candle(112.0, 112.1, 110.4, 110.5),
		candle(111.2, 111.3, 109.4, 109.5),
		candle(110.1, 110.2, 108.4, 108.5),
	)
	requirePattern(t, candles, "three_black_crows", domain.PatternBearish)
}

func TestDojiVariants(t *testing.T) {
	base := downtrend(10, 100)

	dragonfly := append(append([]domain.Candle{}, base...), candle(90.0, 90.02, 89.0, 90.01))
	requirePattern(t, dragonfly, "dragonfly_doji", domain.PatternBullish)

	gravestone := append(append([]domain.Candle{}, base...), candle(90.0, 91.0, 89.98, 89.99))
	requirePattern(t, gravestone, "gravestone_doji", domain.PatternBearish)
}

func TestPiercingLineAndDarkCloud(t *testing.T) {
	piercing := append(downtrend(10, 100),
		candle(95.0, 95.1, 92.9, 93.0),
		candle(92.5, 94.3, 92.4, 94.2),
	)
	requirePattern(t, piercing, "piercing_line", domain.PatternBullish)

	darkCloud := append(uptrend(10, 100),
		candle(110.0, 112.1, 109.9, 112.0),
		candle(112.5, 112.6, 110.7, 110.8),
	)
	requirePattern(t, darkCloud, "dark_cloud_cover", domain.PatternBearish)
}

func TestHaramiPatterns(t *testing.T) {
	bullish := append(downtrend(10, 100),
		candle(95.0, 95.1, 92.9, 93.0),
		candle(93.5, 94.2, 93.4, 94.0),
	)
	requirePattern(t, bullish, "bullish_harami", domain.PatternBullish)

	bearish := append(uptrend(10, 100),
		candle(110.0, 112.1, 109.9, 112.0),
		candle(111.5, 111.6, 110.8, 110.9),
	)
	requirePattern(t, bearish, "bearish_harami", domain.PatternBearish)
}

func TestNoPatternsOnFlatMarket(t *testing.T) {
	flat := make([]domain.Candle, 0, 30)
	for i := 0; i < 30; i++ {
		flat = append(flat, candle(100, 100.5, 99.5, 100.1))
	}
	found := DetectCandlestick(withTimes(flat), 1)
	for _, p := range found {
		if p.Direction != domain.PatternNeutral {
			t.Fatalf("a flat market must not produce directional patterns, got %s", p.Name)
		}
	}
}

func TestDetectorNeedsMinimumHistory(t *testing.T) {
	if got := DetectCandlestick(withTimes(downtrend(4, 100)), 3); got != nil {
		t.Fatalf("expected no detection without history, got %v", got)
	}
}

func TestPatternIndexIsRelativeToNewestCandle(t *testing.T) {
	candles := append(downtrend(10, 100),
		candle(92.0, 92.2, 90.8, 91.0),
		candle(90.9, 92.7, 90.8, 92.5),
		candle(92.5, 92.9, 92.3, 92.6),
	)
	found := DetectCandlestick(withTimes(candles), 3)
	p, ok := findPattern(found, "bullish_engulfing")
	if !ok {
		t.Fatal("expected bullish engulfing one candle back")
	}
	if p.CandleIndex != -1 || p.AgeCandles != 1 {
		t.Fatalf("expected index=-1 age=1, got index=%d age=%d", p.CandleIndex, p.AgeCandles)
	}
}
