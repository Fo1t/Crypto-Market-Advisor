package strategies

import (
	"fmt"
	"math"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

// This file holds published systems with well-known rules, implemented from
// their public description rather than invented here. Each one computes its own
// series from the visible candles, so it stays independent of what the shared
// analysis happens to expose.

// donchianBreakout is the entry rule of the Turtle system: a close beyond the
// extreme of the last N bars. The classic pair is a fast 20-bar channel with a
// slower 55-bar channel behind it; a break of both is the stronger statement.
func donchianBreakout(in Input) opinion {
	const fast, slow = 20, 55
	if len(in.Candles) < fast+1 {
		return noOpinion
	}
	last := in.Candles[len(in.Candles)-1]

	// The channel is measured on the bars before the current one: comparing a
	// close with a range that already contains it would always break out.
	fastHigh, fastLow, ok := channel(in.Candles, fast)
	if !ok {
		return noOpinion
	}
	slowHigh, slowLow, hasSlow := channel(in.Candles, slow)

	switch {
	case last.Close > fastHigh:
		strength := 0.7
		if hasSlow && last.Close > slowHigh {
			strength = 1
		}
		return opinion{
			direction: domain.PatternBullish, strength: strength,
			detail: fmt.Sprintf("close %.4f > %d-bar high %.4f", last.Close, fast, fastHigh),
		}
	case last.Close < fastLow:
		strength := 0.7
		if hasSlow && last.Close < slowLow {
			strength = 1
		}
		return opinion{
			direction: domain.PatternBearish, strength: strength,
			detail: fmt.Sprintf("close %.4f < %d-bar low %.4f", last.Close, fast, fastLow),
		}
	default:
		return noOpinion
	}
}

// channel returns the highest high and lowest low of the period bars that
// precede the last one.
func channel(candles []domain.Candle, period int) (float64, float64, bool) {
	end := len(candles) - 1
	start := end - period
	if start < 0 {
		return 0, 0, false
	}
	high, low := math.Inf(-1), math.Inf(1)
	for _, c := range candles[start:end] {
		high = math.Max(high, c.High)
		low = math.Min(low, c.Low)
	}
	return high, low, true
}

// superTrend follows the ATR trailing band: price above the band is an uptrend,
// price below it a downtrend, and the band only ever moves in the direction of
// the trend until it flips.
func superTrend(in Input) opinion {
	const period, factor = 10, 3.0
	if len(in.Candles) < period+2 {
		return noOpinion
	}

	atr := wilderATR(in.Candles, period)
	if len(atr) == 0 {
		return noOpinion
	}

	upper, lower := math.Inf(1), math.Inf(-1)
	trend := 1 // 1 up, -1 down
	offset := len(in.Candles) - len(atr)

	for i, value := range atr {
		candle := in.Candles[offset+i]
		mid := (candle.High + candle.Low) / 2
		basicUpper, basicLower := mid+factor*value, mid-factor*value

		previous := in.Candles[offset+i-1]
		if !math.IsInf(upper, 1) && !(basicUpper < upper || previous.Close > upper) {
			basicUpper = upper
		}
		if !math.IsInf(lower, -1) && !(basicLower > lower || previous.Close < lower) {
			basicLower = lower
		}
		upper, lower = basicUpper, basicLower

		switch {
		case candle.Close > upper:
			trend = 1
		case candle.Close < lower:
			trend = -1
		}
	}

	last := in.Candles[len(in.Candles)-1]
	band := lower
	if trend < 0 {
		band = upper
	}
	if band <= 0 || last.Close <= 0 {
		return noOpinion
	}
	// The further price stands from its own trailing band, the more established
	// the move; right on top of it the next bar may flip the trend.
	distance := math.Abs(last.Close-band) / last.Close * 100
	strength := clamp(distance/2, 0.3, 1)

	direction := domain.PatternBullish
	if trend < 0 {
		direction = domain.PatternBearish
	}
	return opinion{
		direction: direction, strength: strength,
		detail: fmt.Sprintf("band %.4f · %.2f%% away", band, distance),
	}
}

// wilderATR returns the running average true range using Wilder smoothing.
func wilderATR(candles []domain.Candle, period int) []float64 {
	if len(candles) < period+1 {
		return nil
	}
	trs := make([]float64, 0, len(candles)-1)
	for i := 1; i < len(candles); i++ {
		previous, current := candles[i-1], candles[i]
		tr := math.Max(current.High-current.Low,
			math.Max(math.Abs(current.High-previous.Close), math.Abs(current.Low-previous.Close)))
		trs = append(trs, tr)
	}

	sum := 0.0
	for _, tr := range trs[:period] {
		sum += tr
	}
	out := make([]float64, 0, len(trs)-period+1)
	value := sum / float64(period)
	out = append(out, value)
	for _, tr := range trs[period:] {
		value = (value*float64(period-1) + tr) / float64(period)
		out = append(out, value)
	}
	return out
}

// rsi2Reversion is Larry Connors' two-period RSI rule: buy panic and sell
// euphoria, but only in the direction of the long moving average. Trading it
// against the trend is what the published rules explicitly forbid.
func rsi2Reversion(in Input) opinion {
	const period, trendPeriod = 2, 200
	if len(in.Candles) < trendPeriod+period {
		return noOpinion
	}
	rsi, ok := lastRSI(in.Candles, period)
	if !ok {
		return noOpinion
	}
	last := in.Candles[len(in.Candles)-1]
	trend := simpleAverage(in.Candles, trendPeriod)
	if trend <= 0 {
		return noOpinion
	}

	switch {
	case last.Close > trend && rsi < 10:
		return opinion{
			direction: domain.PatternBullish, strength: clamp((10-rsi)/10, 0.4, 1),
			detail: fmt.Sprintf("RSI(2) %.1f above SMA200", rsi),
		}
	case last.Close < trend && rsi > 90:
		return opinion{
			direction: domain.PatternBearish, strength: clamp((rsi-90)/10, 0.4, 1),
			detail: fmt.Sprintf("RSI(2) %.1f below SMA200", rsi),
		}
	default:
		return noOpinion
	}
}

// lastRSI computes the Wilder RSI of the given period on closing prices.
func lastRSI(candles []domain.Candle, period int) (float64, bool) {
	if len(candles) < period+1 {
		return 0, false
	}
	var gain, loss float64
	for i := 1; i <= period; i++ {
		change := candles[i].Close - candles[i-1].Close
		if change >= 0 {
			gain += change
		} else {
			loss -= change
		}
	}
	gain /= float64(period)
	loss /= float64(period)

	for i := period + 1; i < len(candles); i++ {
		change := candles[i].Close - candles[i-1].Close
		up, down := 0.0, 0.0
		if change >= 0 {
			up = change
		} else {
			down = -change
		}
		gain = (gain*float64(period-1) + up) / float64(period)
		loss = (loss*float64(period-1) + down) / float64(period)
	}
	if loss == 0 {
		if gain == 0 {
			return 50, true
		}
		return 100, true
	}
	rs := gain / loss
	return 100 - 100/(1+rs), true
}

func simpleAverage(candles []domain.Candle, period int) float64 {
	if len(candles) < period {
		return 0
	}
	sum := 0.0
	for _, c := range candles[len(candles)-period:] {
		sum += c.Close
	}
	return sum / float64(period)
}

// The three rules below come from a direct measurement rather than from a
// published system, so the evidence is recorded with them.
//
// Every bar of five years of four-hour and daily candles was filed by two facts:
// the state of the market as a whole, and how far price had travelled over the
// previous ten bars, measured in the average bar range that preceded the move.
// The forward return of each group was then compared with the quiet group of the
// same regime, and the comparison was resampled by calendar month - whole months
// drawn with replacement, all assets together - to survive the fact that
// neighbouring bars share the future they are scored on.
//
// On daily bars, over a ten-day horizon and 2000 resamples:
//
//	rising market, move above four ranges : +8.7 points over the quiet group,
//	                                        positive in 100% of resamples
//	rising market, move below -4 ranges   : +3.5 points, positive in 97%
//	falling market, move below -4 ranges  : +3.2 points, positive in 96%
//	falling market, rally of 2-4 ranges   : -1.3 points, negative in 86%
//	falling market, rally above 4 ranges  : -3.7 points, negative in 90%
//
// The ensemble reads none of this: it reacts to indicators, not to how far price
// has already come relative to its own usual amplitude in the regime it is in.

// TrailingMoveLookback is the window the three regime rules measure movement
// over, in bars. Ten bars is what the study used.
const TrailingMoveLookback = 10

// trailingMoveRanges returns how far price travelled over the lookback, measured
// in the average bar range of the twenty bars *preceding* that move.
//
// The yardstick deliberately excludes the move itself: a violent stretch would
// otherwise inflate the very range it is being measured against, and every move
// would look ordinary.
func trailingMoveRanges(candles []domain.Candle) (float64, bool) {
	const yardstick = 20
	if len(candles) < TrailingMoveLookback+yardstick+1 {
		return 0, false
	}
	end := len(candles) - 1
	from := end - TrailingMoveLookback

	span := 0.0
	for _, candle := range candles[from-yardstick : from] {
		span += candle.High - candle.Low
	}
	span /= yardstick
	if span <= 0 {
		return 0, false
	}
	return (candles[end].Close - candles[from].Close) / span, true
}

// marketRising and marketFalling read the benchmark exactly as the market-wide
// filter does, so the rules below agree with it by construction.
func marketRising(in Input) bool {
	context := in.Snapshot.MarketContext
	if !context.Known() || context.PriceVsEMA200Pct == nil {
		return false
	}
	rising := context.EMA200SlopePct != nil && *context.EMA200SlopePct > 0
	return *context.PriceVsEMA200Pct >= marketGateBandPct && rising
}

func marketFalling(in Input) bool {
	context := in.Snapshot.MarketContext
	if !context.Known() || context.PriceVsEMA200Pct == nil {
		return false
	}
	falling := context.EMA200SlopePct != nil && *context.EMA200SlopePct < 0
	return *context.PriceVsEMA200Pct <= -marketGateBandPct || falling
}

// regimeMomentum buys strength while the market as a whole is rising.
//
// It is the opposite of what the shipped entry does - the resting order waits
// for a pullback - and the measurement says the pullback is the wrong instinct
// in this regime: the bars that had already run the furthest were the ones that
// went on to run further still.
func regimeMomentum(in Input) opinion {
	if !marketRising(in) {
		return noOpinion
	}
	move, ok := trailingMoveRanges(in.Candles)
	if !ok || move < 2 {
		return noOpinion
	}
	return opinion{
		direction: domain.PatternBullish,
		strength:  clamp((move-2)/4, 0.3, 1),
		detail:    fmt.Sprintf("+%.1f средних ходов за %d баров, рынок растёт", move, TrailingMoveLookback),
	}
}

// capitulationLong buys a collapse. It asks nothing of the regime, because the
// measurement found the effect in both: +3.5 points over the quiet group while
// the market rose and +3.2 while it fell.
func capitulationLong(in Input) opinion {
	move, ok := trailingMoveRanges(in.Candles)
	if !ok || move > -4 {
		return noOpinion
	}
	return opinion{
		direction: domain.PatternBullish,
		strength:  clamp((-move-4)/4, 0.4, 1),
		detail:    fmt.Sprintf("%.1f средних ходов за %d баров", move, TrailingMoveLookback),
	}
}

// bearRallyFade sells a rally that happens while the market as a whole falls.
//
// This is the one rule that argues for a short, and it is deliberately not a
// mirror of the long engine. The ensemble's own short signals were measured to
// be anti-predictive - they fire after a decline has already exhausted itself.
// This one fires on the opposite condition: after a bounce inside a downtrend.
func bearRallyFade(in Input) opinion {
	if !marketFalling(in) {
		return noOpinion
	}
	move, ok := trailingMoveRanges(in.Candles)
	if !ok || move < 1 {
		return noOpinion
	}
	return opinion{
		direction: domain.PatternBearish,
		strength:  clamp((move-1)/3, 0.3, 1),
		detail:    fmt.Sprintf("+%.1f средних ходов за %d баров, рынок падает", move, TrailingMoveLookback),
	}
}
