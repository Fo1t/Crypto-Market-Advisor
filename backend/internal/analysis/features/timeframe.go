// Package features turns raw candles into the structured snapshot that the
// risk engine, the LLM and the UI all consume.
package features

import (
	"math"
	"time"

	"github.com/crypto-market-advisor/advisor/internal/analysis/divergences"
	"github.com/crypto-market-advisor/advisor/internal/analysis/indicators"
	"github.com/crypto-market-advisor/advisor/internal/analysis/marketstructure"
	"github.com/crypto-market-advisor/advisor/internal/analysis/patterns"
	"github.com/crypto-market-advisor/advisor/internal/analysis/regime"
	"github.com/crypto-market-advisor/advisor/internal/analysis/supportresistance"
	"github.com/crypto-market-advisor/advisor/internal/domain"
)

// MinCandles is the smallest series that still yields a meaningful analysis.
const MinCandles = 30

// maPeriods are the moving-average lengths computed for every timeframe.
var maPeriods = []int{9, 20, 21, 50, 100, 200}

// AnalyzeTimeframe runs the full deterministic analysis of one timeframe.
// The caller must pass closed candles only, oldest first.
func AnalyzeTimeframe(tf domain.Timeframe, candles []domain.Candle) domain.TimeframeAnalysis {
	res := domain.TimeframeAnalysis{
		Timeframe:   tf,
		CandlesUsed: len(candles),
		Bias:        domain.PatternNeutral,
	}
	if len(candles) == 0 {
		return res
	}

	last := len(candles) - 1
	res.LastClosedCandle = candles[last].CloseTime
	res.Close = candles[last].Close
	res.CandleSourceMix = sourceMix(candles)
	res.CandleProviderMix = providerMix(candles)

	if len(candles) < MinCandles {
		res.Structure = domain.MarketStructure{State: domain.StructureUncertain, Description: "insufficient history"}
		res.Regime = domain.Regime{Primary: domain.RegimeUncertain}
		return res
	}

	o, h, l, c, v := split(candles)
	res.Indicators = computeIndicators(tf, candles, o, h, l, c, v)

	swings := marketstructure.FindSwings(candles, marketstructure.DefaultDepth)
	res.Structure = marketstructure.Analyze(candles, marketstructure.DefaultDepth)

	res.Patterns = patterns.DetectCandlestick(candles, 5)
	res.ChartPatterns = patterns.DetectChart(candles, swings, patterns.DefaultChartOptions())
	res.Levels = supportresistance.Detect(candles, swings, res.Close, supportresistance.DefaultOptions())

	res.Divergences = collectDivergences(candles, swings, c)
	res.Regime = regime.Classify(res.Indicators, res.Structure, res.ChartPatterns)
	res.Scores = regime.Score(res.Indicators, res.Structure, res.Patterns, res.ChartPatterns, res.Divergences)

	switch res.Scores.DeterministicBias {
	case "bullish":
		res.Bias = domain.PatternBullish
	case "bearish":
		res.Bias = domain.PatternBearish
	}
	return res
}

func split(candles []domain.Candle) (o, h, l, c, v []float64) {
	n := len(candles)
	o, h, l, c, v = make([]float64, n), make([]float64, n), make([]float64, n), make([]float64, n), make([]float64, n)
	for i, cd := range candles {
		o[i], h[i], l[i], c[i], v[i] = cd.Open, cd.High, cd.Low, cd.Close, cd.Volume
	}
	return o, h, l, c, v
}

func sourceMix(candles []domain.Candle) map[string]int {
	mix := map[string]int{}
	for _, c := range candles {
		src := string(c.Source)
		if src == "" {
			src = string(domain.CandleSourceNative)
		}
		mix[src]++
	}
	return mix
}

func providerMix(candles []domain.Candle) map[string]int {
	mix := map[string]int{}
	for _, candle := range candles {
		provider := candle.Provider
		if provider == "" {
			provider = "coingecko"
		}
		mix[provider]++
	}
	return mix
}

//nolint:gocyclo // one long assignment block; splitting it would only scatter it.
func computeIndicators(tf domain.Timeframe, candles []domain.Candle, o, h, l, c, v []float64) domain.Indicators {
	ind := domain.Indicators{SMA: map[string]float64{}, EMA: map[string]float64{}}
	last := len(c) - 1
	price := c[last]

	for _, p := range maPeriods {
		if len(c) < p {
			continue
		}
		if val, ok := indicators.Last(indicators.SMA(c, p)); ok {
			ind.SMA[key(p)] = round(val)
		}
		if val, ok := indicators.Last(indicators.EMA(c, p)); ok {
			ind.EMA[key(p)] = round(val)
		}
	}

	rsiSeries := indicators.RSI(c, 14)
	ind.RSI = lastPtr(rsiSeries)
	if ind.RSI != nil {
		ind.RSIState = rsiState(*ind.RSI)
		if s := indicators.Slope(rsiSeries, 3); indicators.Valid(s) {
			ind.RSISlope = ptr(round(s))
		}
	}

	stochRSIK, stochRSID := indicators.StochRSI(c, 14, 14, 3, 3)
	ind.StochRSIK = lastPtr(stochRSIK)
	ind.StochRSID = lastPtr(stochRSID)

	stochK, stochD := indicators.Stochastic(h, l, c, 14, 3, 3)
	ind.StochK = lastPtr(stochK)
	ind.StochD = lastPtr(stochD)

	ind.ROC = lastPtr(indicators.ROC(c, 10))
	ind.Momentum = lastPtr(indicators.Momentum(c, 10))
	ind.CCI = lastPtr(indicators.CCI(h, l, c, 20))
	ind.WilliamsR = lastPtr(indicators.WilliamsR(h, l, c, 14))

	macd := indicators.MACD(c, 12, 26, 9)
	ind.MACD = lastPtr(macd.MACD)
	ind.MACDSignal = lastPtr(macd.Signal)
	ind.MACDHistogram = lastPtr(macd.Histogram)
	ind.MACDState = macdState(macd)

	adx := indicators.ADX(h, l, c, 14)
	ind.ADX = lastPtr(adx.ADX)
	ind.PlusDI = lastPtr(adx.PlusDI)
	ind.MinusDI = lastPtr(adx.MinusDI)
	ind.TrendStrength = trendStrength(ind.ADX)

	aroon := indicators.Aroon(h, l, 25)
	ind.AroonUp = lastPtr(aroon.Up)
	ind.AroonDown = lastPtr(aroon.Down)

	atrSeries := indicators.ATR(h, l, c, 14)
	ind.ATR = lastPtr(atrSeries)
	if ind.ATR != nil && price > 0 {
		ind.ATRPct = ptr(round(*ind.ATR / price * 100))
		if p := indicators.Percentile(atrSeries, *ind.ATR, 100); indicators.Valid(p) {
			ind.ATRPercentile = ptr(round(p))
		}
	}

	bb := indicators.Bollinger(c, 20, 2)
	ind.BBUpper = lastPtr(bb.Upper)
	ind.BBMiddle = lastPtr(bb.Middle)
	ind.BBLower = lastPtr(bb.Lower)
	ind.BBWidth = lastPtr(bb.Width)
	ind.BBPercentB = lastPtr(bb.PercentB)

	kelt := indicators.Keltner(h, l, c, 20, 14, 2)
	ind.KeltnerUpper = lastPtr(kelt.Upper)
	ind.KeltnerLower = lastPtr(kelt.Lower)

	volSeries := indicators.RealizedVolatility(c, 20)
	ind.RealizedVol = lastPtr(volSeries)
	if ind.RealizedVol != nil {
		if p := indicators.Percentile(volSeries, *ind.RealizedVol, 100); indicators.Valid(p) {
			ind.VolPercentile = ptr(round(p))
		}
	}

	if hasVolume(v) {
		ind.Volume = ptr(round(v[last]))
		volSMA := indicators.SMA(v, 20)
		ind.VolumeSMA = lastPtr(volSMA)
		if ind.VolumeSMA != nil && *ind.VolumeSMA > 0 {
			ind.RelativeVolume = ptr(round(v[last] / *ind.VolumeSMA))
		}
		if p := indicators.Percentile(v, v[last], 100); indicators.Valid(p) {
			ind.VolumePercentile = ptr(round(p))
		}

		obvSeries := indicators.OBV(c, v)
		ind.OBV = lastPtr(obvSeries)
		if s := indicators.Slope(obvSeries, 10); indicators.Valid(s) {
			ind.OBVSlope = ptr(round(s))
		}
		ind.MFI = lastPtr(indicators.MFI(h, l, c, v, 14))
		ind.CMF = lastPtr(indicators.CMF(h, l, c, v, 20))

		if tf == domain.TF1d {
			ind.VWAP = lastPtr(indicators.RollingVWAP(h, l, c, v, 20))
		} else {
			ind.VWAP = lastPtr(indicators.AnchoredVWAP(h, l, c, v, dailyResets(candles)))
		}
	}

	if ema50, ok := ind.EMA[key(50)]; ok && ema50 != 0 {
		ind.PriceVsEMA50Pct = ptr(round((price - ema50) / ema50 * 100))
	}
	if ema200, ok := ind.EMA[key(200)]; ok && ema200 != 0 {
		ind.PriceVsEMA200Pct = ptr(round((price - ema200) / ema200 * 100))
	}
	if ind.VWAP != nil && *ind.VWAP != 0 {
		ind.PriceVsVWAPPct = ptr(round((price - *ind.VWAP) / *ind.VWAP * 100))
	}

	window := 100
	if len(candles) < window {
		window = len(candles)
	}
	hi, lo := h[len(h)-window], l[len(l)-window]
	for _, cd := range candles[len(candles)-window:] {
		hi = math.Max(hi, cd.High)
		lo = math.Min(lo, cd.Low)
	}
	if hi > 0 {
		ind.DistFromHighPct = ptr(round((price - hi) / hi * 100))
	}
	if lo > 0 {
		ind.DistFromLowPct = ptr(round((price - lo) / lo * 100))
	}
	_ = o
	return ind
}

// dailyResets marks the candles that start a new UTC day, anchoring VWAP.
func dailyResets(candles []domain.Candle) []bool {
	out := make([]bool, len(candles))
	var prevDay time.Time
	for i, c := range candles {
		day := c.OpenTime.UTC().Truncate(24 * time.Hour)
		if i == 0 || !day.Equal(prevDay) {
			out[i] = true
		}
		prevDay = day
	}
	return out
}

func collectDivergences(candles []domain.Candle, swings []domain.SwingPoint, closes []float64) []domain.Divergence {
	opts := divergences.DefaultOptions()
	var out []domain.Divergence

	out = append(out, divergences.Detect(candles, swings, "rsi", indicators.RSI(closes, 14), opts)...)
	out = append(out, divergences.Detect(candles, swings, "macd", indicators.MACD(closes, 12, 26, 9).Histogram, opts)...)

	volumes := make([]float64, len(candles))
	for i, c := range candles {
		volumes[i] = c.Volume
	}
	if hasVolume(volumes) {
		out = append(out, divergences.Detect(candles, swings, "obv", indicators.OBV(closes, volumes), opts)...)
	}
	return out
}

func hasVolume(v []float64) bool {
	for _, x := range v {
		if x > 0 {
			return true
		}
	}
	return false
}

func rsiState(v float64) string {
	switch {
	case v >= 70:
		return "overbought"
	case v >= 60:
		return "bullish"
	case v <= 30:
		return "oversold"
	case v <= 40:
		return "bearish"
	default:
		return "neutral"
	}
}

func macdState(res indicators.MACDResult) string {
	macd, ok1 := indicators.Last(res.MACD)
	signal, ok2 := indicators.Last(res.Signal)
	hist, ok3 := indicators.Last(res.Histogram)
	if !ok1 || !ok2 || !ok3 {
		return ""
	}
	prevHist, hasPrev := indicators.LastAt(res.Histogram, 1)

	switch {
	case hasPrev && prevHist <= 0 && hist > 0:
		return "bullish_cross"
	case hasPrev && prevHist >= 0 && hist < 0:
		return "bearish_cross"
	case macd > signal && macd > 0:
		return "bullish"
	case macd < signal && macd < 0:
		return "bearish"
	case macd > signal:
		return "improving"
	default:
		return "weakening"
	}
}

func trendStrength(adx *float64) string {
	if adx == nil {
		return ""
	}
	switch {
	case *adx >= 50:
		return "very_strong"
	case *adx >= 40:
		return "strong"
	case *adx >= 25:
		return "moderate"
	case *adx >= 20:
		return "weak"
	default:
		return "absent"
	}
}

func key(period int) string {
	switch period {
	case 9:
		return "9"
	case 20:
		return "20"
	case 21:
		return "21"
	case 50:
		return "50"
	case 100:
		return "100"
	case 200:
		return "200"
	default:
		return ""
	}
}

func lastPtr(series []float64) *float64 {
	v, ok := indicators.Last(series)
	if !ok {
		return nil
	}
	return ptr(round(v))
}

func ptr(v float64) *float64 { return &v }

// round keeps snapshots compact: six significant decimals are far beyond what
// any downstream consumer needs, and shorter JSON means more room in the
// LLM context window.
func round(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	abs := math.Abs(v)
	switch {
	case abs >= 1000:
		return math.Round(v*100) / 100
	case abs >= 1:
		return math.Round(v*10000) / 10000
	default:
		return math.Round(v*1000000) / 1000000
	}
}
