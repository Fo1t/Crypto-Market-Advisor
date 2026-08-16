// Package indicators implements every technical indicator the application
// needs, deterministically and without external dependencies.
//
// Convention: every function returns a slice aligned with its input. Positions
// that cannot be computed from the available history hold math.NaN(). Callers
// therefore never have to reason about offsets, and a NaN is an explicit
// "unknown" rather than a silent zero.
package indicators

import (
	"math"
	"sort"
)

// nanSlice allocates a slice of n NaNs.
func nanSlice(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = math.NaN()
	}
	return out
}

// Valid reports whether v is a usable number.
func Valid(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// Last returns the final valid value of a series.
func Last(series []float64) (float64, bool) {
	for i := len(series) - 1; i >= 0; i-- {
		if Valid(series[i]) {
			return series[i], true
		}
	}
	return math.NaN(), false
}

// LastAt returns the value at index len-1-offset if it is valid.
func LastAt(series []float64, offset int) (float64, bool) {
	i := len(series) - 1 - offset
	if i < 0 || i >= len(series) || !Valid(series[i]) {
		return math.NaN(), false
	}
	return series[i], true
}

// SMA computes the simple moving average.
func SMA(values []float64, period int) []float64 {
	out := nanSlice(len(values))
	if period <= 0 || len(values) < period {
		return out
	}
	var sum float64
	for i, v := range values {
		sum += v
		if i >= period {
			sum -= values[i-period]
		}
		if i >= period-1 {
			out[i] = sum / float64(period)
		}
	}
	return out
}

// EMA computes the exponential moving average seeded with the SMA of the first
// `period` values, which is the conventional deterministic seeding.
func EMA(values []float64, period int) []float64 {
	out := nanSlice(len(values))
	if period <= 0 || len(values) < period {
		return out
	}
	k := 2.0 / (float64(period) + 1.0)

	var seed float64
	for i := 0; i < period; i++ {
		seed += values[i]
	}
	seed /= float64(period)
	out[period-1] = seed

	for i := period; i < len(values); i++ {
		out[i] = (values[i]-out[i-1])*k + out[i-1]
	}
	return out
}

// WMA computes the linearly weighted moving average.
func WMA(values []float64, period int) []float64 {
	out := nanSlice(len(values))
	if period <= 0 || len(values) < period {
		return out
	}
	denom := float64(period*(period+1)) / 2
	for i := period - 1; i < len(values); i++ {
		var sum float64
		for j := 0; j < period; j++ {
			sum += values[i-period+1+j] * float64(j+1)
		}
		out[i] = sum / denom
	}
	return out
}

// wilderSmooth applies Wilder's smoothing (RMA) used by RSI, ATR and ADX.
func wilderSmooth(values []float64, period int) []float64 {
	out := nanSlice(len(values))
	if period <= 0 || len(values) < period {
		return out
	}
	var sum float64
	for i := 0; i < period; i++ {
		sum += values[i]
	}
	out[period-1] = sum / float64(period)
	for i := period; i < len(values); i++ {
		out[i] = (out[i-1]*float64(period-1) + values[i]) / float64(period)
	}
	return out
}

// RSI computes Wilder's relative strength index.
func RSI(closes []float64, period int) []float64 {
	out := nanSlice(len(closes))
	if period <= 0 || len(closes) <= period {
		return out
	}

	gains := make([]float64, len(closes))
	losses := make([]float64, len(closes))
	for i := 1; i < len(closes); i++ {
		change := closes[i] - closes[i-1]
		if change > 0 {
			gains[i] = change
		} else {
			losses[i] = -change
		}
	}

	// Wilder seeds with the average of the first `period` changes, which start
	// at index 1; hence the first defined RSI sits at index `period`.
	var avgGain, avgLoss float64
	for i := 1; i <= period; i++ {
		avgGain += gains[i]
		avgLoss += losses[i]
	}
	avgGain /= float64(period)
	avgLoss /= float64(period)
	out[period] = rsiValue(avgGain, avgLoss)

	for i := period + 1; i < len(closes); i++ {
		avgGain = (avgGain*float64(period-1) + gains[i]) / float64(period)
		avgLoss = (avgLoss*float64(period-1) + losses[i]) / float64(period)
		out[i] = rsiValue(avgGain, avgLoss)
	}
	return out
}

func rsiValue(avgGain, avgLoss float64) float64 {
	if avgLoss == 0 {
		if avgGain == 0 {
			return 50
		}
		return 100
	}
	rs := avgGain / avgLoss
	return 100 - (100 / (1 + rs))
}

// StochRSI computes %K and %D of the stochastic RSI.
func StochRSI(closes []float64, rsiPeriod, stochPeriod, kSmooth, dSmooth int) (k, d []float64) {
	rsi := RSI(closes, rsiPeriod)
	raw := nanSlice(len(closes))

	for i := range rsi {
		if !Valid(rsi[i]) {
			continue
		}
		lo, hi := math.Inf(1), math.Inf(-1)
		count := 0
		for j := i; j >= 0 && count < stochPeriod; j-- {
			if !Valid(rsi[j]) {
				break
			}
			lo = math.Min(lo, rsi[j])
			hi = math.Max(hi, rsi[j])
			count++
		}
		if count < stochPeriod {
			continue
		}
		if hi == lo {
			raw[i] = 50
			continue
		}
		raw[i] = (rsi[i] - lo) / (hi - lo) * 100
	}

	k = smoothIgnoringNaN(raw, kSmooth)
	d = smoothIgnoringNaN(k, dSmooth)
	return k, d
}

// smoothIgnoringNaN applies an SMA that only starts once enough valid values exist.
func smoothIgnoringNaN(values []float64, period int) []float64 {
	out := nanSlice(len(values))
	if period <= 1 {
		copy(out, values)
		return out
	}
	for i := range values {
		var sum float64
		count := 0
		for j := i; j >= 0 && count < period; j-- {
			if !Valid(values[j]) {
				break
			}
			sum += values[j]
			count++
		}
		if count == period {
			out[i] = sum / float64(period)
		}
	}
	return out
}

// Stochastic computes the classic %K/%D oscillator.
func Stochastic(highs, lows, closes []float64, kPeriod, kSmooth, dPeriod int) (k, d []float64) {
	raw := nanSlice(len(closes))
	for i := kPeriod - 1; i < len(closes); i++ {
		hi, lo := highs[i], lows[i]
		for j := i - kPeriod + 1; j <= i; j++ {
			hi = math.Max(hi, highs[j])
			lo = math.Min(lo, lows[j])
		}
		if hi == lo {
			raw[i] = 50
			continue
		}
		raw[i] = (closes[i] - lo) / (hi - lo) * 100
	}
	k = smoothIgnoringNaN(raw, kSmooth)
	d = smoothIgnoringNaN(k, dPeriod)
	return k, d
}

// ROC computes the rate of change in percent.
func ROC(values []float64, period int) []float64 {
	out := nanSlice(len(values))
	for i := period; i < len(values); i++ {
		prev := values[i-period]
		if prev == 0 {
			continue
		}
		out[i] = (values[i] - prev) / prev * 100
	}
	return out
}

// Momentum computes the absolute price difference over the period.
func Momentum(values []float64, period int) []float64 {
	out := nanSlice(len(values))
	for i := period; i < len(values); i++ {
		out[i] = values[i] - values[i-period]
	}
	return out
}

// CCI computes the commodity channel index.
func CCI(highs, lows, closes []float64, period int) []float64 {
	typical := make([]float64, len(closes))
	for i := range closes {
		typical[i] = (highs[i] + lows[i] + closes[i]) / 3
	}
	sma := SMA(typical, period)
	out := nanSlice(len(closes))

	for i := period - 1; i < len(closes); i++ {
		var meanDev float64
		for j := i - period + 1; j <= i; j++ {
			meanDev += math.Abs(typical[j] - sma[i])
		}
		meanDev /= float64(period)
		if meanDev == 0 {
			out[i] = 0
			continue
		}
		out[i] = (typical[i] - sma[i]) / (0.015 * meanDev)
	}
	return out
}

// WilliamsR computes Williams %R, ranging from -100 to 0.
func WilliamsR(highs, lows, closes []float64, period int) []float64 {
	out := nanSlice(len(closes))
	for i := period - 1; i < len(closes); i++ {
		hi, lo := highs[i], lows[i]
		for j := i - period + 1; j <= i; j++ {
			hi = math.Max(hi, highs[j])
			lo = math.Min(lo, lows[j])
		}
		if hi == lo {
			out[i] = -50
			continue
		}
		out[i] = (hi - closes[i]) / (hi - lo) * -100
	}
	return out
}

// MACDResult holds the three MACD series.
type MACDResult struct {
	MACD      []float64
	Signal    []float64
	Histogram []float64
}

// MACD computes the moving average convergence divergence.
func MACD(closes []float64, fast, slow, signal int) MACDResult {
	fastEMA := EMA(closes, fast)
	slowEMA := EMA(closes, slow)

	macd := nanSlice(len(closes))
	for i := range closes {
		if Valid(fastEMA[i]) && Valid(slowEMA[i]) {
			macd[i] = fastEMA[i] - slowEMA[i]
		}
	}

	// The signal line is an EMA of the defined part of the MACD line only.
	start := -1
	for i := range macd {
		if Valid(macd[i]) {
			start = i
			break
		}
	}
	sig := nanSlice(len(closes))
	hist := nanSlice(len(closes))
	if start >= 0 {
		sigPart := EMA(macd[start:], signal)
		copy(sig[start:], sigPart)
		for i := range closes {
			if Valid(macd[i]) && Valid(sig[i]) {
				hist[i] = macd[i] - sig[i]
			}
		}
	}
	return MACDResult{MACD: macd, Signal: sig, Histogram: hist}
}

// TrueRange computes the per-candle true range.
func TrueRange(highs, lows, closes []float64) []float64 {
	out := nanSlice(len(closes))
	if len(closes) == 0 {
		return out
	}
	out[0] = highs[0] - lows[0]
	for i := 1; i < len(closes); i++ {
		hl := highs[i] - lows[i]
		hc := math.Abs(highs[i] - closes[i-1])
		lc := math.Abs(lows[i] - closes[i-1])
		out[i] = math.Max(hl, math.Max(hc, lc))
	}
	return out
}

// ATR computes the average true range with Wilder smoothing.
func ATR(highs, lows, closes []float64, period int) []float64 {
	return wilderSmooth(TrueRange(highs, lows, closes), period)
}

// ADXResult holds ADX together with the directional indicators.
type ADXResult struct {
	ADX     []float64
	PlusDI  []float64
	MinusDI []float64
}

// ADX computes the average directional index using Wilder's method.
func ADX(highs, lows, closes []float64, period int) ADXResult {
	n := len(closes)
	res := ADXResult{ADX: nanSlice(n), PlusDI: nanSlice(n), MinusDI: nanSlice(n)}
	if n <= period*2 {
		return res
	}

	plusDM := make([]float64, n)
	minusDM := make([]float64, n)
	tr := TrueRange(highs, lows, closes)
	for i := 1; i < n; i++ {
		up := highs[i] - highs[i-1]
		down := lows[i-1] - lows[i]
		if up > down && up > 0 {
			plusDM[i] = up
		}
		if down > up && down > 0 {
			minusDM[i] = down
		}
	}

	// Wilder's accumulation starting at index 1 (the first bar has no DM).
	smoothTR := wilderSmooth(tr[1:], period)
	smoothPlus := wilderSmooth(plusDM[1:], period)
	smoothMinus := wilderSmooth(minusDM[1:], period)

	dx := nanSlice(n)
	for i := range smoothTR {
		idx := i + 1
		if !Valid(smoothTR[i]) || smoothTR[i] == 0 {
			continue
		}
		plus := 100 * smoothPlus[i] / smoothTR[i]
		minus := 100 * smoothMinus[i] / smoothTR[i]
		res.PlusDI[idx] = plus
		res.MinusDI[idx] = minus
		if plus+minus == 0 {
			dx[idx] = 0
			continue
		}
		dx[idx] = 100 * math.Abs(plus-minus) / (plus + minus)
	}

	// ADX is Wilder's smoothing of DX once DX itself is defined.
	firstDX := -1
	for i := range dx {
		if Valid(dx[i]) {
			firstDX = i
			break
		}
	}
	if firstDX < 0 {
		return res
	}
	adxPart := wilderSmooth(dx[firstDX:], period)
	copy(res.ADX[firstDX:], adxPart)
	return res
}

// AroonResult holds the Aroon up and down series.
type AroonResult struct {
	Up   []float64
	Down []float64
}

// Aroon computes the Aroon indicator.
func Aroon(highs, lows []float64, period int) AroonResult {
	n := len(highs)
	res := AroonResult{Up: nanSlice(n), Down: nanSlice(n)}
	for i := period; i < n; i++ {
		hiIdx, loIdx := i, i
		for j := i - period; j <= i; j++ {
			if highs[j] >= highs[hiIdx] {
				hiIdx = j
			}
			if lows[j] <= lows[loIdx] {
				loIdx = j
			}
		}
		res.Up[i] = float64(period-(i-hiIdx)) / float64(period) * 100
		res.Down[i] = float64(period-(i-loIdx)) / float64(period) * 100
	}
	return res
}

// BollingerResult holds the three Bollinger series plus width and %B.
type BollingerResult struct {
	Upper    []float64
	Middle   []float64
	Lower    []float64
	Width    []float64
	PercentB []float64
}

// Bollinger computes Bollinger Bands using the population standard deviation.
func Bollinger(closes []float64, period int, mult float64) BollingerResult {
	n := len(closes)
	res := BollingerResult{
		Upper: nanSlice(n), Middle: nanSlice(n), Lower: nanSlice(n),
		Width: nanSlice(n), PercentB: nanSlice(n),
	}
	middle := SMA(closes, period)
	for i := period - 1; i < n; i++ {
		var variance float64
		for j := i - period + 1; j <= i; j++ {
			diff := closes[j] - middle[i]
			variance += diff * diff
		}
		sd := math.Sqrt(variance / float64(period))
		res.Middle[i] = middle[i]
		res.Upper[i] = middle[i] + mult*sd
		res.Lower[i] = middle[i] - mult*sd
		if middle[i] != 0 {
			res.Width[i] = (res.Upper[i] - res.Lower[i]) / middle[i] * 100
		}
		if res.Upper[i] != res.Lower[i] {
			res.PercentB[i] = (closes[i] - res.Lower[i]) / (res.Upper[i] - res.Lower[i])
		}
	}
	return res
}

// KeltnerResult holds the Keltner channel series.
type KeltnerResult struct {
	Upper  []float64
	Middle []float64
	Lower  []float64
}

// Keltner computes Keltner channels from an EMA and the ATR.
func Keltner(highs, lows, closes []float64, emaPeriod, atrPeriod int, mult float64) KeltnerResult {
	n := len(closes)
	res := KeltnerResult{Upper: nanSlice(n), Middle: nanSlice(n), Lower: nanSlice(n)}
	mid := EMA(closes, emaPeriod)
	atr := ATR(highs, lows, closes, atrPeriod)
	for i := 0; i < n; i++ {
		if !Valid(mid[i]) || !Valid(atr[i]) {
			continue
		}
		res.Middle[i] = mid[i]
		res.Upper[i] = mid[i] + mult*atr[i]
		res.Lower[i] = mid[i] - mult*atr[i]
	}
	return res
}

// RealizedVolatility returns the standard deviation of log returns over the
// period, expressed in percent. It is not annualised: the timeframe context is
// carried separately, and annualising a 1m series would mislead more than help.
func RealizedVolatility(closes []float64, period int) []float64 {
	n := len(closes)
	out := nanSlice(n)
	if n < period+1 {
		return out
	}
	returns := nanSlice(n)
	for i := 1; i < n; i++ {
		if closes[i-1] > 0 && closes[i] > 0 {
			returns[i] = math.Log(closes[i] / closes[i-1])
		}
	}
	for i := period; i < n; i++ {
		var sum float64
		count := 0
		for j := i - period + 1; j <= i; j++ {
			if Valid(returns[j]) {
				sum += returns[j]
				count++
			}
		}
		if count < period/2 {
			continue
		}
		mean := sum / float64(count)
		var variance float64
		for j := i - period + 1; j <= i; j++ {
			if Valid(returns[j]) {
				d := returns[j] - mean
				variance += d * d
			}
		}
		out[i] = math.Sqrt(variance/float64(count)) * 100
	}
	return out
}

// OBV computes on-balance volume.
func OBV(closes, volumes []float64) []float64 {
	n := len(closes)
	out := nanSlice(n)
	if n == 0 {
		return out
	}
	out[0] = 0
	for i := 1; i < n; i++ {
		switch {
		case closes[i] > closes[i-1]:
			out[i] = out[i-1] + volumes[i]
		case closes[i] < closes[i-1]:
			out[i] = out[i-1] - volumes[i]
		default:
			out[i] = out[i-1]
		}
	}
	return out
}

// MFI computes the money flow index.
func MFI(highs, lows, closes, volumes []float64, period int) []float64 {
	n := len(closes)
	out := nanSlice(n)
	if n <= period {
		return out
	}
	typical := make([]float64, n)
	for i := range closes {
		typical[i] = (highs[i] + lows[i] + closes[i]) / 3
	}
	for i := period; i < n; i++ {
		var positive, negative float64
		for j := i - period + 1; j <= i; j++ {
			flow := typical[j] * volumes[j]
			switch {
			case typical[j] > typical[j-1]:
				positive += flow
			case typical[j] < typical[j-1]:
				negative += flow
			}
		}
		if negative == 0 {
			if positive == 0 {
				out[i] = 50
			} else {
				out[i] = 100
			}
			continue
		}
		ratio := positive / negative
		out[i] = 100 - (100 / (1 + ratio))
	}
	return out
}

// RollingVWAP computes a rolling volume weighted average price.
func RollingVWAP(highs, lows, closes, volumes []float64, period int) []float64 {
	n := len(closes)
	out := nanSlice(n)
	for i := period - 1; i < n; i++ {
		var pv, vol float64
		for j := i - period + 1; j <= i; j++ {
			typical := (highs[j] + lows[j] + closes[j]) / 3
			pv += typical * volumes[j]
			vol += volumes[j]
		}
		if vol > 0 {
			out[i] = pv / vol
		}
	}
	return out
}

// AnchoredVWAP computes VWAP restarting whenever resetAt[i] is true, which the
// caller uses to anchor intraday VWAP to the start of each UTC day.
func AnchoredVWAP(highs, lows, closes, volumes []float64, resetAt []bool) []float64 {
	n := len(closes)
	out := nanSlice(n)
	var pv, vol float64
	for i := 0; i < n; i++ {
		if i < len(resetAt) && resetAt[i] {
			pv, vol = 0, 0
		}
		typical := (highs[i] + lows[i] + closes[i]) / 3
		pv += typical * volumes[i]
		vol += volumes[i]
		if vol > 0 {
			out[i] = pv / vol
		}
	}
	return out
}

// CMF computes the Chaikin money flow.
func CMF(highs, lows, closes, volumes []float64, period int) []float64 {
	n := len(closes)
	out := nanSlice(n)
	mfv := make([]float64, n)
	for i := 0; i < n; i++ {
		rng := highs[i] - lows[i]
		if rng == 0 {
			mfv[i] = 0
			continue
		}
		multiplier := ((closes[i] - lows[i]) - (highs[i] - closes[i])) / rng
		mfv[i] = multiplier * volumes[i]
	}
	for i := period - 1; i < n; i++ {
		var flow, vol float64
		for j := i - period + 1; j <= i; j++ {
			flow += mfv[j]
			vol += volumes[j]
		}
		if vol > 0 {
			out[i] = flow / vol
		}
	}
	return out
}

// Percentile returns the fraction of the last `window` valid values that are
// below `value`, in the range [0,100].
func Percentile(series []float64, value float64, window int) float64 {
	if !Valid(value) {
		return math.NaN()
	}
	start := len(series) - window
	if start < 0 {
		start = 0
	}
	var below, total int
	for i := start; i < len(series); i++ {
		if !Valid(series[i]) {
			continue
		}
		total++
		if series[i] < value {
			below++
		}
	}
	if total < 5 {
		return math.NaN()
	}
	return float64(below) / float64(total) * 100
}

// Slope returns the average change per bar over the last `period` valid values.
func Slope(series []float64, period int) float64 {
	valid := make([]float64, 0, period)
	for i := len(series) - 1; i >= 0 && len(valid) < period; i-- {
		if Valid(series[i]) {
			valid = append(valid, series[i])
		}
	}
	if len(valid) < 2 {
		return math.NaN()
	}
	// valid is newest-first; reverse into chronological order.
	sort.SliceStable(valid, func(i, j int) bool { return i > j })
	return (valid[len(valid)-1] - valid[0]) / float64(len(valid)-1)
}

// StdDev returns the population standard deviation of a slice.
func StdDev(values []float64) float64 {
	if len(values) == 0 {
		return math.NaN()
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	mean := sum / float64(len(values))
	var variance float64
	for _, v := range values {
		d := v - mean
		variance += d * d
	}
	return math.Sqrt(variance / float64(len(values)))
}

// Mean returns the arithmetic mean, ignoring NaNs.
func Mean(values []float64) float64 {
	var sum float64
	count := 0
	for _, v := range values {
		if Valid(v) {
			sum += v
			count++
		}
	}
	if count == 0 {
		return math.NaN()
	}
	return sum / float64(count)
}
