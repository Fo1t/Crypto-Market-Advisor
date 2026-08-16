// Package patterns detects candlestick and chart formations directly from
// OHLCV data. No image processing is involved anywhere.
package patterns

import (
	"math"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

// context carries the statistics a detector needs to judge whether a candle is
// "long" or "small" relative to recent history.
type context struct {
	candles  []domain.Candle
	avgBody  float64
	avgRange float64
	trend    int // +1 uptrend, -1 downtrend, 0 undecided
}

// detector inspects the candle at index i and reports a match.
type detector struct {
	name string
	// minCandles is how much history the detector needs before index i.
	minCandles int
	fn         func(ctx *context, i int) (domain.PatternDirection, float64, bool)
}

const (
	dojiBodyRatio      = 0.1
	smallBodyRatio     = 0.3
	longBodyRatio      = 0.6
	tinyShadowRatio    = 0.08
	nearEqualTolerance = 0.001
)

// DetectCandlestick scans the last `lookback` candles and returns every
// formation found, newest first. Only closed candles must be supplied.
func DetectCandlestick(candles []domain.Candle, lookback int) []domain.Pattern {
	if len(candles) < 6 {
		return nil
	}
	ctx := newContext(candles)

	last := len(candles) - 1
	start := last - lookback + 1
	if start < 5 {
		start = 5
	}

	var out []domain.Pattern
	for i := last; i >= start; i-- {
		ctx.trend = trendAt(candles, i)
		for _, d := range detectors {
			if i < d.minCandles {
				continue
			}
			dir, strength, ok := d.fn(ctx, i)
			if !ok {
				continue
			}
			out = append(out, domain.Pattern{
				Name:        d.name,
				Kind:        "candlestick",
				Direction:   dir,
				Strength:    clamp01(strength),
				CandleIndex: i - last, // 0 for the newest candle, negative going back
				AgeCandles:  last - i,
			})
		}
	}
	return out
}

func newContext(candles []domain.Candle) *context {
	window := 20
	if len(candles) < window {
		window = len(candles)
	}
	var bodySum, rangeSum float64
	for _, c := range candles[len(candles)-window:] {
		bodySum += c.Body()
		rangeSum += c.Range()
	}
	return &context{
		candles:  candles,
		avgBody:  bodySum / float64(window),
		avgRange: rangeSum / float64(window),
	}
}

// trendAt classifies the short-term trend leading into index i using the slope
// of closes over the preceding bars. Only past data is consulted.
func trendAt(candles []domain.Candle, i int) int {
	const window = 8
	start := i - window
	if start < 0 {
		return 0
	}
	first := candles[start].Close
	prev := candles[i-1].Close
	if first == 0 {
		return 0
	}
	change := (prev - first) / first
	switch {
	case change > 0.004:
		return 1
	case change < -0.004:
		return -1
	default:
		return 0
	}
}

func clamp01(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	return math.Max(0, math.Min(1, v))
}

func nearEqual(a, b, scale float64) bool {
	if scale <= 0 {
		return a == b
	}
	return math.Abs(a-b) <= scale*nearEqualTolerance*10
}

func bodyRatio(c domain.Candle) float64 {
	r := c.Range()
	if r == 0 {
		return 0
	}
	return c.Body() / r
}

func isDoji(c domain.Candle) bool { return bodyRatio(c) <= dojiBodyRatio }

func isLongBody(ctx *context, c domain.Candle) bool {
	return c.Body() >= ctx.avgBody*1.2 && bodyRatio(c) >= longBodyRatio
}

func isSmallBody(ctx *context, c domain.Candle) bool {
	return c.Body() <= ctx.avgBody*0.7 || bodyRatio(c) <= smallBodyRatio
}

// sizeStrength scales a base confidence by how prominent the candle body is.
func sizeStrength(ctx *context, c domain.Candle, base float64) float64 {
	if ctx.avgBody <= 0 {
		return base
	}
	ratio := c.Body() / ctx.avgBody
	return base * (0.75 + 0.25*math.Min(2, ratio))
}

// trendBonus rewards a reversal pattern that appears after a matching trend.
func trendBonus(ctx *context, want int, base float64) float64 {
	if ctx.trend == want {
		return base * 1.15
	}
	if ctx.trend == 0 {
		return base * 0.9
	}
	return base * 0.7
}

// detectors is the full candlestick catalogue.
var detectors = []detector{
	{"doji", 1, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		c := ctx.candles[i]
		if !isDoji(c) || c.Range() < ctx.avgRange*0.4 {
			return "", 0, false
		}
		return domain.PatternNeutral, 0.4, true
	}},

	{"dragonfly_doji", 1, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		c := ctx.candles[i]
		if !isDoji(c) || c.Range() == 0 {
			return "", 0, false
		}
		if c.LowerShadow() < c.Range()*0.6 || c.UpperShadow() > c.Range()*0.15 {
			return "", 0, false
		}
		return domain.PatternBullish, trendBonus(ctx, -1, 0.6), true
	}},

	{"gravestone_doji", 1, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		c := ctx.candles[i]
		if !isDoji(c) || c.Range() == 0 {
			return "", 0, false
		}
		if c.UpperShadow() < c.Range()*0.6 || c.LowerShadow() > c.Range()*0.15 {
			return "", 0, false
		}
		return domain.PatternBearish, trendBonus(ctx, 1, 0.6), true
	}},

	{"spinning_top", 1, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		c := ctx.candles[i]
		if c.Range() == 0 || isDoji(c) {
			return "", 0, false
		}
		if bodyRatio(c) > smallBodyRatio {
			return "", 0, false
		}
		if c.UpperShadow() < c.Body() || c.LowerShadow() < c.Body() {
			return "", 0, false
		}
		return domain.PatternNeutral, 0.35, true
	}},

	{"marubozu", 1, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		c := ctx.candles[i]
		if c.Range() == 0 || bodyRatio(c) < 0.9 {
			return "", 0, false
		}
		if c.UpperShadow() > c.Range()*tinyShadowRatio || c.LowerShadow() > c.Range()*tinyShadowRatio {
			return "", 0, false
		}
		if c.Bullish() {
			return domain.PatternBullish, sizeStrength(ctx, c, 0.65), true
		}
		return domain.PatternBearish, sizeStrength(ctx, c, 0.65), true
	}},

	{"hammer", 1, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		c := ctx.candles[i]
		if c.Range() == 0 || c.Body() == 0 {
			return "", 0, false
		}
		if c.LowerShadow() < c.Body()*2 || c.UpperShadow() > c.Body()*0.8 {
			return "", 0, false
		}
		if bodyRatio(c) > smallBodyRatio {
			return "", 0, false
		}
		if ctx.trend > 0 {
			return "", 0, false // that shape in an uptrend is a hanging man
		}
		return domain.PatternBullish, trendBonus(ctx, -1, 0.7), true
	}},

	{"hanging_man", 1, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		c := ctx.candles[i]
		if ctx.trend <= 0 || c.Body() == 0 {
			return "", 0, false
		}
		if c.LowerShadow() < c.Body()*2 || c.UpperShadow() > c.Body()*0.8 {
			return "", 0, false
		}
		if bodyRatio(c) > smallBodyRatio {
			return "", 0, false
		}
		return domain.PatternBearish, 0.65, true
	}},

	{"inverted_hammer", 1, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		c := ctx.candles[i]
		if ctx.trend > 0 || c.Body() == 0 {
			return "", 0, false
		}
		if c.UpperShadow() < c.Body()*2 || c.LowerShadow() > c.Body()*0.8 {
			return "", 0, false
		}
		if bodyRatio(c) > smallBodyRatio {
			return "", 0, false
		}
		return domain.PatternBullish, trendBonus(ctx, -1, 0.6), true
	}},

	{"shooting_star", 1, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		c := ctx.candles[i]
		if ctx.trend <= 0 || c.Body() == 0 {
			return "", 0, false
		}
		if c.UpperShadow() < c.Body()*2 || c.LowerShadow() > c.Body()*0.8 {
			return "", 0, false
		}
		if bodyRatio(c) > smallBodyRatio {
			return "", 0, false
		}
		return domain.PatternBearish, 0.7, true
	}},

	{"bullish_engulfing", 1, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		prev, cur := ctx.candles[i-1], ctx.candles[i]
		if !prev.Bearish() || !cur.Bullish() {
			return "", 0, false
		}
		if cur.Open > prev.Close || cur.Close < prev.Open {
			return "", 0, false
		}
		if cur.Body() <= prev.Body() {
			return "", 0, false
		}
		return domain.PatternBullish, trendBonus(ctx, -1, sizeStrength(ctx, cur, 0.75)), true
	}},

	{"bearish_engulfing", 1, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		prev, cur := ctx.candles[i-1], ctx.candles[i]
		if !prev.Bullish() || !cur.Bearish() {
			return "", 0, false
		}
		if cur.Open < prev.Close || cur.Close > prev.Open {
			return "", 0, false
		}
		if cur.Body() <= prev.Body() {
			return "", 0, false
		}
		return domain.PatternBearish, trendBonus(ctx, 1, sizeStrength(ctx, cur, 0.75)), true
	}},

	{"bullish_harami", 1, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		prev, cur := ctx.candles[i-1], ctx.candles[i]
		if !prev.Bearish() || !cur.Bullish() || isDoji(cur) {
			return "", 0, false
		}
		if !isLongBody(ctx, prev) {
			return "", 0, false
		}
		if cur.Open <= prev.Close || cur.Close >= prev.Open {
			return "", 0, false
		}
		return domain.PatternBullish, trendBonus(ctx, -1, 0.6), true
	}},

	{"bearish_harami", 1, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		prev, cur := ctx.candles[i-1], ctx.candles[i]
		if !prev.Bullish() || !cur.Bearish() || isDoji(cur) {
			return "", 0, false
		}
		if !isLongBody(ctx, prev) {
			return "", 0, false
		}
		if cur.Open >= prev.Close || cur.Close <= prev.Open {
			return "", 0, false
		}
		return domain.PatternBearish, trendBonus(ctx, 1, 0.6), true
	}},

	{"harami_cross", 1, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		prev, cur := ctx.candles[i-1], ctx.candles[i]
		if !isDoji(cur) || !isLongBody(ctx, prev) {
			return "", 0, false
		}
		lo, hi := math.Min(prev.Open, prev.Close), math.Max(prev.Open, prev.Close)
		if cur.High > hi || cur.Low < lo {
			return "", 0, false
		}
		if prev.Bearish() {
			return domain.PatternBullish, 0.65, true
		}
		return domain.PatternBearish, 0.65, true
	}},

	{"piercing_line", 1, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		prev, cur := ctx.candles[i-1], ctx.candles[i]
		if !prev.Bearish() || !cur.Bullish() || !isLongBody(ctx, prev) {
			return "", 0, false
		}
		if cur.Open >= prev.Close {
			return "", 0, false
		}
		mid := (prev.Open + prev.Close) / 2
		if cur.Close <= mid || cur.Close >= prev.Open {
			return "", 0, false
		}
		return domain.PatternBullish, trendBonus(ctx, -1, 0.7), true
	}},

	{"dark_cloud_cover", 1, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		prev, cur := ctx.candles[i-1], ctx.candles[i]
		if !prev.Bullish() || !cur.Bearish() || !isLongBody(ctx, prev) {
			return "", 0, false
		}
		if cur.Open <= prev.Close {
			return "", 0, false
		}
		mid := (prev.Open + prev.Close) / 2
		if cur.Close >= mid || cur.Close <= prev.Open {
			return "", 0, false
		}
		return domain.PatternBearish, trendBonus(ctx, 1, 0.7), true
	}},

	{"morning_star", 2, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		a, b, c := ctx.candles[i-2], ctx.candles[i-1], ctx.candles[i]
		if !a.Bearish() || !isLongBody(ctx, a) || !c.Bullish() {
			return "", 0, false
		}
		if !isSmallBody(ctx, b) || isDoji(b) {
			return "", 0, false
		}
		if math.Max(b.Open, b.Close) >= a.Close {
			return "", 0, false
		}
		if c.Close <= (a.Open+a.Close)/2 {
			return "", 0, false
		}
		return domain.PatternBullish, trendBonus(ctx, -1, 0.8), true
	}},

	{"evening_star", 2, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		a, b, c := ctx.candles[i-2], ctx.candles[i-1], ctx.candles[i]
		if !a.Bullish() || !isLongBody(ctx, a) || !c.Bearish() {
			return "", 0, false
		}
		if !isSmallBody(ctx, b) || isDoji(b) {
			return "", 0, false
		}
		if math.Min(b.Open, b.Close) <= a.Close {
			return "", 0, false
		}
		if c.Close >= (a.Open+a.Close)/2 {
			return "", 0, false
		}
		return domain.PatternBearish, trendBonus(ctx, 1, 0.8), true
	}},

	{"morning_doji_star", 2, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		a, b, c := ctx.candles[i-2], ctx.candles[i-1], ctx.candles[i]
		if !a.Bearish() || !isLongBody(ctx, a) || !c.Bullish() || !isDoji(b) {
			return "", 0, false
		}
		if math.Max(b.Open, b.Close) >= a.Close {
			return "", 0, false
		}
		if c.Close <= (a.Open+a.Close)/2 {
			return "", 0, false
		}
		return domain.PatternBullish, trendBonus(ctx, -1, 0.85), true
	}},

	{"evening_doji_star", 2, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		a, b, c := ctx.candles[i-2], ctx.candles[i-1], ctx.candles[i]
		if !a.Bullish() || !isLongBody(ctx, a) || !c.Bearish() || !isDoji(b) {
			return "", 0, false
		}
		if math.Min(b.Open, b.Close) <= a.Close {
			return "", 0, false
		}
		if c.Close >= (a.Open+a.Close)/2 {
			return "", 0, false
		}
		return domain.PatternBearish, trendBonus(ctx, 1, 0.85), true
	}},

	{"abandoned_baby_bullish", 2, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		a, b, c := ctx.candles[i-2], ctx.candles[i-1], ctx.candles[i]
		if !a.Bearish() || !c.Bullish() || !isDoji(b) {
			return "", 0, false
		}
		if b.High >= a.Low || b.High >= c.Low {
			return "", 0, false
		}
		return domain.PatternBullish, 0.9, true
	}},

	{"abandoned_baby_bearish", 2, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		a, b, c := ctx.candles[i-2], ctx.candles[i-1], ctx.candles[i]
		if !a.Bullish() || !c.Bearish() || !isDoji(b) {
			return "", 0, false
		}
		if b.Low <= a.High || b.Low <= c.High {
			return "", 0, false
		}
		return domain.PatternBearish, 0.9, true
	}},

	{"three_white_soldiers", 2, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		a, b, c := ctx.candles[i-2], ctx.candles[i-1], ctx.candles[i]
		if !a.Bullish() || !b.Bullish() || !c.Bullish() {
			return "", 0, false
		}
		if !(b.Close > a.Close && c.Close > b.Close) {
			return "", 0, false
		}
		if b.Open < a.Open || b.Open > a.Close || c.Open < b.Open || c.Open > b.Close {
			return "", 0, false
		}
		if b.Body() < ctx.avgBody*0.6 || c.Body() < ctx.avgBody*0.6 {
			return "", 0, false
		}
		if b.UpperShadow() > b.Body()*0.6 || c.UpperShadow() > c.Body()*0.6 {
			return "", 0, false
		}
		return domain.PatternBullish, 0.85, true
	}},

	{"three_black_crows", 2, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		a, b, c := ctx.candles[i-2], ctx.candles[i-1], ctx.candles[i]
		if !a.Bearish() || !b.Bearish() || !c.Bearish() {
			return "", 0, false
		}
		if !(b.Close < a.Close && c.Close < b.Close) {
			return "", 0, false
		}
		if b.Open > a.Open || b.Open < a.Close || c.Open > b.Open || c.Open < b.Close {
			return "", 0, false
		}
		if b.Body() < ctx.avgBody*0.6 || c.Body() < ctx.avgBody*0.6 {
			return "", 0, false
		}
		if b.LowerShadow() > b.Body()*0.6 || c.LowerShadow() > c.Body()*0.6 {
			return "", 0, false
		}
		return domain.PatternBearish, 0.85, true
	}},

	{"tweezer_top", 1, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		prev, cur := ctx.candles[i-1], ctx.candles[i]
		if ctx.trend <= 0 {
			return "", 0, false
		}
		if !nearEqual(prev.High, cur.High, ctx.avgRange) {
			return "", 0, false
		}
		if !prev.Bullish() || !cur.Bearish() {
			return "", 0, false
		}
		return domain.PatternBearish, 0.6, true
	}},

	{"tweezer_bottom", 1, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		prev, cur := ctx.candles[i-1], ctx.candles[i]
		if ctx.trend >= 0 {
			return "", 0, false
		}
		if !nearEqual(prev.Low, cur.Low, ctx.avgRange) {
			return "", 0, false
		}
		if !prev.Bearish() || !cur.Bullish() {
			return "", 0, false
		}
		return domain.PatternBullish, 0.6, true
	}},

	{"three_inside_up", 2, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		a, b, c := ctx.candles[i-2], ctx.candles[i-1], ctx.candles[i]
		if !a.Bearish() || !b.Bullish() || !c.Bullish() {
			return "", 0, false
		}
		if b.Open <= a.Close || b.Close >= a.Open {
			return "", 0, false
		}
		if c.Close <= b.Close {
			return "", 0, false
		}
		return domain.PatternBullish, 0.75, true
	}},

	{"three_inside_down", 2, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		a, b, c := ctx.candles[i-2], ctx.candles[i-1], ctx.candles[i]
		if !a.Bullish() || !b.Bearish() || !c.Bearish() {
			return "", 0, false
		}
		if b.Open >= a.Close || b.Close <= a.Open {
			return "", 0, false
		}
		if c.Close >= b.Close {
			return "", 0, false
		}
		return domain.PatternBearish, 0.75, true
	}},

	{"three_outside_up", 2, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		a, b, c := ctx.candles[i-2], ctx.candles[i-1], ctx.candles[i]
		if !a.Bearish() || !b.Bullish() || !c.Bullish() {
			return "", 0, false
		}
		if b.Open > a.Close || b.Close < a.Open {
			return "", 0, false
		}
		if c.Close <= b.Close {
			return "", 0, false
		}
		return domain.PatternBullish, 0.8, true
	}},

	{"three_outside_down", 2, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		a, b, c := ctx.candles[i-2], ctx.candles[i-1], ctx.candles[i]
		if !a.Bullish() || !b.Bearish() || !c.Bearish() {
			return "", 0, false
		}
		if b.Open < a.Close || b.Close > a.Open {
			return "", 0, false
		}
		if c.Close >= b.Close {
			return "", 0, false
		}
		return domain.PatternBearish, 0.8, true
	}},

	{"belt_hold_bullish", 1, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		c := ctx.candles[i]
		if !c.Bullish() || !isLongBody(ctx, c) {
			return "", 0, false
		}
		if c.LowerShadow() > c.Range()*tinyShadowRatio {
			return "", 0, false
		}
		return domain.PatternBullish, trendBonus(ctx, -1, 0.6), true
	}},

	{"belt_hold_bearish", 1, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		c := ctx.candles[i]
		if !c.Bearish() || !isLongBody(ctx, c) {
			return "", 0, false
		}
		if c.UpperShadow() > c.Range()*tinyShadowRatio {
			return "", 0, false
		}
		return domain.PatternBearish, trendBonus(ctx, 1, 0.6), true
	}},

	{"kicking_bullish", 1, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		prev, cur := ctx.candles[i-1], ctx.candles[i]
		if !prev.Bearish() || !cur.Bullish() {
			return "", 0, false
		}
		if bodyRatio(prev) < 0.9 || bodyRatio(cur) < 0.9 {
			return "", 0, false
		}
		if cur.Low <= prev.High {
			return "", 0, false
		}
		return domain.PatternBullish, 0.85, true
	}},

	{"kicking_bearish", 1, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		prev, cur := ctx.candles[i-1], ctx.candles[i]
		if !prev.Bullish() || !cur.Bearish() {
			return "", 0, false
		}
		if bodyRatio(prev) < 0.9 || bodyRatio(cur) < 0.9 {
			return "", 0, false
		}
		if cur.High >= prev.Low {
			return "", 0, false
		}
		return domain.PatternBearish, 0.85, true
	}},

	{"matching_low", 1, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		prev, cur := ctx.candles[i-1], ctx.candles[i]
		if !prev.Bearish() || !cur.Bearish() {
			return "", 0, false
		}
		if !nearEqual(prev.Close, cur.Close, ctx.avgRange) {
			return "", 0, false
		}
		return domain.PatternBullish, trendBonus(ctx, -1, 0.5), true
	}},

	{"separating_lines_bullish", 1, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		prev, cur := ctx.candles[i-1], ctx.candles[i]
		if ctx.trend <= 0 || !prev.Bearish() || !cur.Bullish() {
			return "", 0, false
		}
		if !nearEqual(prev.Open, cur.Open, ctx.avgRange) || !isLongBody(ctx, cur) {
			return "", 0, false
		}
		return domain.PatternBullish, 0.6, true
	}},

	{"separating_lines_bearish", 1, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		prev, cur := ctx.candles[i-1], ctx.candles[i]
		if ctx.trend >= 0 || !prev.Bullish() || !cur.Bearish() {
			return "", 0, false
		}
		if !nearEqual(prev.Open, cur.Open, ctx.avgRange) || !isLongBody(ctx, cur) {
			return "", 0, false
		}
		return domain.PatternBearish, 0.6, true
	}},

	{"thrusting", 1, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		prev, cur := ctx.candles[i-1], ctx.candles[i]
		if !prev.Bearish() || !cur.Bullish() || !isLongBody(ctx, prev) {
			return "", 0, false
		}
		if cur.Open >= prev.Low {
			return "", 0, false
		}
		mid := (prev.Open + prev.Close) / 2
		if cur.Close <= prev.Close || cur.Close >= mid {
			return "", 0, false
		}
		return domain.PatternBearish, 0.45, true
	}},

	{"advance_block", 2, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		a, b, c := ctx.candles[i-2], ctx.candles[i-1], ctx.candles[i]
		if !a.Bullish() || !b.Bullish() || !c.Bullish() {
			return "", 0, false
		}
		if !(b.Close > a.Close && c.Close > b.Close) {
			return "", 0, false
		}
		if !(b.Body() < a.Body() && c.Body() < b.Body()) {
			return "", 0, false
		}
		if c.UpperShadow() <= c.Body() {
			return "", 0, false
		}
		return domain.PatternBearish, 0.6, true
	}},

	{"stalled_pattern", 2, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		a, b, c := ctx.candles[i-2], ctx.candles[i-1], ctx.candles[i]
		if !a.Bullish() || !b.Bullish() || !c.Bullish() {
			return "", 0, false
		}
		if !isLongBody(ctx, a) || !isLongBody(ctx, b) {
			return "", 0, false
		}
		if !isSmallBody(ctx, c) || c.Open < b.Close {
			return "", 0, false
		}
		return domain.PatternBearish, 0.55, true
	}},

	{"three_stars_in_the_south", 2, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		a, b, c := ctx.candles[i-2], ctx.candles[i-1], ctx.candles[i]
		if !a.Bearish() || !b.Bearish() || !c.Bearish() {
			return "", 0, false
		}
		if !isLongBody(ctx, a) || a.LowerShadow() < a.Body()*0.5 {
			return "", 0, false
		}
		if b.Low <= a.Low || b.Body() >= a.Body() || b.Open > a.Open {
			return "", 0, false
		}
		if c.Body() >= b.Body() || c.High > b.High || c.Low < b.Low {
			return "", 0, false
		}
		return domain.PatternBullish, 0.7, true
	}},

	{"upside_gap_two_crows", 2, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		a, b, c := ctx.candles[i-2], ctx.candles[i-1], ctx.candles[i]
		if !a.Bullish() || !isLongBody(ctx, a) || !b.Bearish() || !c.Bearish() {
			return "", 0, false
		}
		if b.Close <= a.Close {
			return "", 0, false
		}
		if c.Open <= b.Open || c.Close >= b.Close || c.Close <= a.Close {
			return "", 0, false
		}
		return domain.PatternBearish, 0.7, true
	}},

	{"concealing_baby_swallow", 3, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		a, b, c, d := ctx.candles[i-3], ctx.candles[i-2], ctx.candles[i-1], ctx.candles[i]
		if !a.Bearish() || !b.Bearish() || !c.Bearish() || !d.Bearish() {
			return "", 0, false
		}
		if bodyRatio(a) < 0.9 || bodyRatio(b) < 0.9 {
			return "", 0, false
		}
		if c.Open >= b.Close || c.High <= c.Open {
			return "", 0, false
		}
		if d.High < c.High || d.Low > c.Low {
			return "", 0, false
		}
		return domain.PatternBullish, 0.7, true
	}},

	{"counterattack_bullish", 1, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		prev, cur := ctx.candles[i-1], ctx.candles[i]
		if !prev.Bearish() || !cur.Bullish() {
			return "", 0, false
		}
		if cur.Open >= prev.Close {
			return "", 0, false
		}
		if !nearEqual(prev.Close, cur.Close, ctx.avgRange) {
			return "", 0, false
		}
		return domain.PatternBullish, 0.6, true
	}},

	{"counterattack_bearish", 1, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		prev, cur := ctx.candles[i-1], ctx.candles[i]
		if !prev.Bullish() || !cur.Bearish() {
			return "", 0, false
		}
		if cur.Open <= prev.Close {
			return "", 0, false
		}
		if !nearEqual(prev.Close, cur.Close, ctx.avgRange) {
			return "", 0, false
		}
		return domain.PatternBearish, 0.6, true
	}},

	{"stick_sandwich", 2, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		a, b, c := ctx.candles[i-2], ctx.candles[i-1], ctx.candles[i]
		if !a.Bearish() || !b.Bullish() || !c.Bearish() {
			return "", 0, false
		}
		if !nearEqual(a.Close, c.Close, ctx.avgRange) {
			return "", 0, false
		}
		return domain.PatternBullish, 0.6, true
	}},

	{"ladder_bottom", 4, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		a, b, c, d, e := ctx.candles[i-4], ctx.candles[i-3], ctx.candles[i-2], ctx.candles[i-1], ctx.candles[i]
		if !a.Bearish() || !b.Bearish() || !c.Bearish() || !d.Bearish() || !e.Bullish() {
			return "", 0, false
		}
		if !(b.Close < a.Close && c.Close < b.Close) {
			return "", 0, false
		}
		if d.UpperShadow() < d.Body()*0.5 {
			return "", 0, false
		}
		if e.Open <= d.Open {
			return "", 0, false
		}
		return domain.PatternBullish, 0.7, true
	}},

	{"tower_top", 4, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		a, e := ctx.candles[i-4], ctx.candles[i]
		if !a.Bullish() || !isLongBody(ctx, a) || !e.Bearish() || !isLongBody(ctx, e) {
			return "", 0, false
		}
		for _, m := range ctx.candles[i-3 : i] {
			if !isSmallBody(ctx, m) {
				return "", 0, false
			}
		}
		if e.Close > a.Open {
			return "", 0, false
		}
		return domain.PatternBearish, 0.7, true
	}},

	{"tower_bottom", 4, func(ctx *context, i int) (domain.PatternDirection, float64, bool) {
		a, e := ctx.candles[i-4], ctx.candles[i]
		if !a.Bearish() || !isLongBody(ctx, a) || !e.Bullish() || !isLongBody(ctx, e) {
			return "", 0, false
		}
		for _, m := range ctx.candles[i-3 : i] {
			if !isSmallBody(ctx, m) {
				return "", 0, false
			}
		}
		if e.Close < a.Open {
			return "", 0, false
		}
		return domain.PatternBullish, 0.7, true
	}},
}
