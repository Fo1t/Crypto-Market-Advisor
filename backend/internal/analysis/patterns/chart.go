package patterns

import (
	"math"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

// ChartOptions tunes chart-pattern detection.
type ChartOptions struct {
	// EqualTolerancePct decides when two pivots count as "the same level".
	EqualTolerancePct float64
	// RangeLookback is how many candles the range/breakout logic considers.
	RangeLookback int
	// MaxAge ignores formations whose last pivot is older than this many candles.
	MaxAge int
}

// DefaultChartOptions returns defaults suited to crypto volatility.
func DefaultChartOptions() ChartOptions {
	return ChartOptions{EqualTolerancePct: 1.2, RangeLookback: 60, MaxAge: 40}
}

// DetectChart finds chart formations from confirmed swing points and price action.
func DetectChart(candles []domain.Candle, swings []domain.SwingPoint, opts ChartOptions) []domain.Pattern {
	if opts.RangeLookback <= 0 {
		opts = DefaultChartOptions()
	}
	if len(candles) < 20 {
		return nil
	}

	highs := filterSwings(swings, true)
	lows := filterSwings(swings, false)
	last := len(candles) - 1

	var out []domain.Pattern
	out = append(out, detectDoubleAndTriple(highs, lows, last, opts)...)
	out = append(out, detectHeadAndShoulders(highs, lows, last, opts)...)
	out = append(out, detectTrendlineFormations(highs, lows, last, opts)...)
	out = append(out, detectFlagsAndPennants(candles)...)
	out = append(out, detectBreakouts(candles, opts)...)
	return out
}

func filterSwings(swings []domain.SwingPoint, high bool) []domain.SwingPoint {
	out := make([]domain.SwingPoint, 0, len(swings))
	for _, s := range swings {
		if s.IsHigh == high {
			out = append(out, s)
		}
	}
	return out
}

func nearLevel(a, b, tolerancePct float64) bool {
	if a == 0 {
		return false
	}
	return math.Abs(a-b)/a*100 <= tolerancePct
}

func pattern(name string, dir domain.PatternDirection, strength float64, age int, target *float64, note string) domain.Pattern {
	return domain.Pattern{
		Name: name, Kind: "chart", Direction: dir,
		Strength: clamp01(strength), CandleIndex: -age, AgeCandles: age,
		Target: target, Note: note,
	}
}

// detectDoubleAndTriple looks for repeated tests of the same extreme.
func detectDoubleAndTriple(highs, lows []domain.SwingPoint, last int, opts ChartOptions) []domain.Pattern {
	var out []domain.Pattern

	if len(highs) >= 2 {
		a, b := highs[len(highs)-2], highs[len(highs)-1]
		age := last - b.Index
		if age <= opts.MaxAge && nearLevel(a.Price, b.Price, opts.EqualTolerancePct) {
			if len(highs) >= 3 && nearLevel(highs[len(highs)-3].Price, b.Price, opts.EqualTolerancePct) {
				out = append(out, pattern("triple_top", domain.PatternBearish, 0.8, age, nil, "three tests of the same resistance"))
			} else {
				out = append(out, pattern("double_top", domain.PatternBearish, 0.7, age, nil, "two tests of the same resistance"))
			}
		}
	}
	if len(lows) >= 2 {
		a, b := lows[len(lows)-2], lows[len(lows)-1]
		age := last - b.Index
		if age <= opts.MaxAge && nearLevel(a.Price, b.Price, opts.EqualTolerancePct) {
			if len(lows) >= 3 && nearLevel(lows[len(lows)-3].Price, b.Price, opts.EqualTolerancePct) {
				out = append(out, pattern("triple_bottom", domain.PatternBullish, 0.8, age, nil, "three tests of the same support"))
			} else {
				out = append(out, pattern("double_bottom", domain.PatternBullish, 0.7, age, nil, "two tests of the same support"))
			}
		}
	}
	return out
}

// detectHeadAndShoulders needs three highs with a dominant middle one (or the
// mirror image for the inverse formation).
func detectHeadAndShoulders(highs, lows []domain.SwingPoint, last int, opts ChartOptions) []domain.Pattern {
	var out []domain.Pattern

	if len(highs) >= 3 {
		l, h, r := highs[len(highs)-3], highs[len(highs)-2], highs[len(highs)-1]
		age := last - r.Index
		if age <= opts.MaxAge &&
			h.Price > l.Price && h.Price > r.Price &&
			nearLevel(l.Price, r.Price, opts.EqualTolerancePct*2) {
			target := neckline(lows, l.Index, r.Index)
			out = append(out, pattern("head_and_shoulders", domain.PatternBearish, 0.8, age, target, "left shoulder, head, right shoulder"))
		}
	}
	if len(lows) >= 3 {
		l, h, r := lows[len(lows)-3], lows[len(lows)-2], lows[len(lows)-1]
		age := last - r.Index
		if age <= opts.MaxAge &&
			h.Price < l.Price && h.Price < r.Price &&
			nearLevel(l.Price, r.Price, opts.EqualTolerancePct*2) {
			target := neckline(highs, l.Index, r.Index)
			out = append(out, pattern("inverse_head_and_shoulders", domain.PatternBullish, 0.8, age, target, "inverted shoulders and head"))
		}
	}
	return out
}

// neckline averages the pivots that sit between the two shoulders.
func neckline(pivots []domain.SwingPoint, fromIdx, toIdx int) *float64 {
	var sum float64
	count := 0
	for _, p := range pivots {
		if p.Index > fromIdx && p.Index < toIdx {
			sum += p.Price
			count++
		}
	}
	if count == 0 {
		return nil
	}
	v := sum / float64(count)
	return &v
}

// detectTrendlineFormations classifies the geometry of the last two highs and
// the last two lows into triangles, wedges, channels and ranges.
func detectTrendlineFormations(highs, lows []domain.SwingPoint, last int, opts ChartOptions) []domain.Pattern {
	if len(highs) < 2 || len(lows) < 2 {
		return nil
	}
	h1, h2 := highs[len(highs)-2], highs[len(highs)-1]
	l1, l2 := lows[len(lows)-2], lows[len(lows)-1]

	age := last - maxInt(h2.Index, l2.Index)
	if age > opts.MaxAge {
		return nil
	}

	highSlope := slopePct(h1.Price, h2.Price)
	lowSlope := slopePct(l1.Price, l2.Price)
	flat := opts.EqualTolerancePct

	highFlat := math.Abs(highSlope) <= flat
	lowFlat := math.Abs(lowSlope) <= flat

	var out []domain.Pattern
	switch {
	case highFlat && lowSlope > flat:
		out = append(out, pattern("ascending_triangle", domain.PatternBullish, 0.7, age, &h2.Price, "flat highs, rising lows"))
	case lowFlat && highSlope < -flat:
		out = append(out, pattern("descending_triangle", domain.PatternBearish, 0.7, age, &l2.Price, "flat lows, falling highs"))
	case highSlope < -flat && lowSlope > flat:
		out = append(out, pattern("symmetrical_triangle", domain.PatternNeutral, 0.6, age, nil, "converging highs and lows"))
	case highFlat && lowFlat:
		out = append(out, pattern("rectangle_range", domain.PatternNeutral, 0.6, age, nil, "horizontal range"))
	case highSlope > flat && lowSlope > flat:
		if converging(h1.Price, h2.Price, l1.Price, l2.Price) {
			out = append(out, pattern("rising_wedge", domain.PatternBearish, 0.65, age, nil, "converging rising trendlines"))
		} else {
			out = append(out, pattern("ascending_channel", domain.PatternBullish, 0.6, age, nil, "parallel rising trendlines"))
		}
	case highSlope < -flat && lowSlope < -flat:
		if converging(h1.Price, h2.Price, l1.Price, l2.Price) {
			out = append(out, pattern("falling_wedge", domain.PatternBullish, 0.65, age, nil, "converging falling trendlines"))
		} else {
			out = append(out, pattern("descending_channel", domain.PatternBearish, 0.6, age, nil, "parallel falling trendlines"))
		}
	}
	return out
}

func converging(h1, h2, l1, l2 float64) bool {
	before := math.Abs(h1 - l1)
	after := math.Abs(h2 - l2)
	return after < before*0.8
}

func slopePct(from, to float64) float64 {
	if from == 0 {
		return 0
	}
	return (to - from) / from * 100
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// detectFlagsAndPennants looks for an impulse followed by a tight consolidation.
func detectFlagsAndPennants(candles []domain.Candle) []domain.Pattern {
	const impulseLen = 8
	const consolidationLen = 6
	need := impulseLen + consolidationLen
	if len(candles) < need+2 {
		return nil
	}
	last := len(candles) - 1
	impulseStart := last - need + 1
	impulseEnd := impulseStart + impulseLen - 1

	from := candles[impulseStart].Close
	to := candles[impulseEnd].Close
	if from == 0 {
		return nil
	}
	move := (to - from) / from * 100

	consolidation := candles[impulseEnd+1:]
	hi, lo := consolidation[0].High, consolidation[0].Low
	for _, c := range consolidation {
		hi = math.Max(hi, c.High)
		lo = math.Min(lo, c.Low)
	}
	if to == 0 {
		return nil
	}
	rangePct := (hi - lo) / to * 100

	impulseHi, impulseLo := candles[impulseStart].High, candles[impulseStart].Low
	for _, c := range candles[impulseStart : impulseEnd+1] {
		impulseHi = math.Max(impulseHi, c.High)
		impulseLo = math.Min(impulseLo, c.Low)
	}
	impulseRangePct := (impulseHi - impulseLo) / to * 100
	if impulseRangePct == 0 {
		return nil
	}

	tight := rangePct < impulseRangePct*0.5
	if !tight {
		return nil
	}

	// A pennant converges; a flag drifts against the impulse in a channel.
	firstHalf := consolidation[:len(consolidation)/2]
	secondHalf := consolidation[len(consolidation)/2:]
	converge := spread(secondHalf) < spread(firstHalf)*0.8

	switch {
	case move >= 3 && converge:
		return []domain.Pattern{pattern("pennant", domain.PatternBullish, 0.6, 0, nil, "bullish impulse then converging consolidation")}
	case move >= 3:
		return []domain.Pattern{pattern("bull_flag", domain.PatternBullish, 0.65, 0, nil, "impulse then shallow pullback")}
	case move <= -3 && converge:
		return []domain.Pattern{pattern("pennant", domain.PatternBearish, 0.6, 0, nil, "bearish impulse then converging consolidation")}
	case move <= -3:
		return []domain.Pattern{pattern("bear_flag", domain.PatternBearish, 0.65, 0, nil, "impulse then shallow bounce")}
	}
	return nil
}

func spread(candles []domain.Candle) float64 {
	if len(candles) == 0 {
		return 0
	}
	hi, lo := candles[0].High, candles[0].Low
	for _, c := range candles {
		hi = math.Max(hi, c.High)
		lo = math.Min(lo, c.Low)
	}
	return hi - lo
}

// detectBreakouts classifies the most recent interaction with the prior range.
func detectBreakouts(candles []domain.Candle, opts ChartOptions) []domain.Pattern {
	lookback := opts.RangeLookback
	if len(candles) < lookback+5 {
		lookback = len(candles) - 5
	}
	if lookback < 15 {
		return nil
	}
	last := len(candles) - 1

	// The reference range excludes the last 3 candles so that a breakout is
	// measured against history, not against itself.
	refEnd := last - 3
	refStart := refEnd - lookback + 1
	if refStart < 0 {
		refStart = 0
	}
	hi, lo := candles[refStart].High, candles[refStart].Low
	for _, c := range candles[refStart : refEnd+1] {
		hi = math.Max(hi, c.High)
		lo = math.Min(lo, c.Low)
	}
	if hi <= lo {
		return nil
	}

	recent := candles[refEnd+1:]
	var out []domain.Pattern

	brokeUp, brokeDown := -1, -1
	for i, c := range recent {
		if c.Close > hi && brokeUp < 0 {
			brokeUp = i
		}
		if c.Close < lo && brokeDown < 0 {
			brokeDown = i
		}
	}
	closeNow := candles[last].Close

	switch {
	case brokeUp >= 0 && closeNow > hi:
		age := len(recent) - 1 - brokeUp
		name := "breakout"
		strength := 0.7
		if age >= 1 && candles[last].Low <= hi {
			name = "breakout_retest"
			strength = 0.75
		}
		out = append(out, pattern(name, domain.PatternBullish, strength, age, &hi, "close above prior range high"))
	case brokeUp >= 0 && closeNow < hi:
		out = append(out, pattern("failed_breakout", domain.PatternBearish, 0.7, len(recent)-1-brokeUp, &hi, "breakout rejected back into range"))
	case brokeDown >= 0 && closeNow < lo:
		age := len(recent) - 1 - brokeDown
		name := "breakdown"
		strength := 0.7
		if age >= 1 && candles[last].High >= lo {
			name = "breakdown_retest"
			strength = 0.75
		}
		out = append(out, pattern(name, domain.PatternBearish, strength, age, &lo, "close below prior range low"))
	case brokeDown >= 0 && closeNow > lo:
		out = append(out, pattern("failed_breakdown", domain.PatternBullish, 0.7, len(recent)-1-brokeDown, &lo, "breakdown reclaimed into range"))
	}
	return out
}
