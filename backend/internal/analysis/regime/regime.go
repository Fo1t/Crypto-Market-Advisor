// Package regime classifies market state and computes deterministic signal
// scores. Everything here is rule-based: no model is involved, which is what
// makes it a usable baseline to compare the LLM against.
package regime

import (
	"math"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

// Classify derives the primary regime and its descriptive tags.
func Classify(ind domain.Indicators, structure domain.MarketStructure, patterns []domain.Pattern) domain.Regime {
	r := domain.Regime{Primary: domain.RegimeUncertain}

	adx := value(ind.ADX)
	plusDI := value(ind.PlusDI)
	minusDI := value(ind.MinusDI)
	atrPct := value(ind.ATRPct)
	atrPctile := value(ind.ATRPercentile)
	bbWidth := value(ind.BBWidth)
	rsi := value(ind.RSI)
	relVol := value(ind.RelativeVolume)

	trending := !math.IsNaN(adx) && adx >= 25
	strongTrend := !math.IsNaN(adx) && adx >= 40
	bullish := plusDI > minusDI

	switch {
	case trending && bullish && strongTrend:
		r.Primary = domain.RegimeStrongUptrend
	case trending && bullish:
		r.Primary = domain.RegimeWeakUptrend
	case trending && !bullish && strongTrend:
		r.Primary = domain.RegimeStrongDowntrend
	case trending && !bullish:
		r.Primary = domain.RegimeWeakDowntrend
	case !math.IsNaN(adx) && adx < 20:
		r.Primary = domain.RegimeRange
	}

	// Structure can promote a range into a directional read when ADX is mute.
	if r.Primary == domain.RegimeRange || r.Primary == domain.RegimeUncertain {
		switch structure.State {
		case domain.StructureBullish:
			r.Primary = domain.RegimeWeakUptrend
		case domain.StructureBearish:
			r.Primary = domain.RegimeWeakDowntrend
		}
	}

	for _, p := range patterns {
		switch p.Name {
		case "breakout", "breakdown", "breakout_retest", "breakdown_retest":
			if p.AgeCandles <= 3 {
				r.Primary = domain.RegimeBreakout
			}
		}
	}

	if !math.IsNaN(bbWidth) && bbWidth < 2 {
		if r.Primary == domain.RegimeRange || r.Primary == domain.RegimeUncertain {
			r.Primary = domain.RegimeCompression
		}
		r.Tags = append(r.Tags, domain.TagSqueeze)
	}

	if !math.IsNaN(atrPctile) {
		switch {
		case atrPctile >= 80:
			r.Tags = append(r.Tags, domain.TagHighVolatility)
		case atrPctile <= 20:
			r.Tags = append(r.Tags, domain.TagLowVolatility)
		}
	} else if !math.IsNaN(atrPct) {
		switch {
		case atrPct >= 2:
			r.Tags = append(r.Tags, domain.TagHighVolatility)
		case atrPct <= 0.3:
			r.Tags = append(r.Tags, domain.TagLowVolatility)
		}
	}

	if strongTrend {
		r.Tags = append(r.Tags, domain.TagStrongMomentum)
	} else if !math.IsNaN(adx) && adx < 15 {
		r.Tags = append(r.Tags, domain.TagWeakMomentum)
	}

	if !math.IsNaN(rsi) {
		switch {
		case rsi >= 70:
			r.Tags = append(r.Tags, domain.TagOverbought)
		case rsi <= 30:
			r.Tags = append(r.Tags, domain.TagOversold)
		}
	}

	if !math.IsNaN(relVol) {
		switch {
		case relVol >= 2:
			r.Tags = append(r.Tags, domain.TagVolumeSpike)
		case relVol <= 0.5:
			r.Tags = append(r.Tags, domain.TagVolumeDry)
		}
	}

	r.Score = confidenceOf(adx, atrPct)
	return r
}

func confidenceOf(adx, atrPct float64) float64 {
	if math.IsNaN(adx) {
		return 0.3
	}
	base := math.Min(1, adx/50)
	if !math.IsNaN(atrPct) && atrPct > 4 {
		base *= 0.8 // very volatile markets make any classification shakier
	}
	return math.Round(base*100) / 100
}

// Score computes the deterministic technical scores for one timeframe.
// Each component lands in [-1,1] before being folded into the bull/bear split.
func Score(ind domain.Indicators, structure domain.MarketStructure, candlePatterns, chartPatterns []domain.Pattern, divs []domain.Divergence) domain.SignalScores {
	trend := trendScore(ind, structure)
	momentum := momentumScore(ind)
	patternScore := patternsScore(candlePatterns, chartPatterns, divs)
	volRisk := volatilityRisk(ind)

	net := trend*0.4 + momentum*0.3 + patternScore*0.3
	bull := math.Max(0, net)
	bear := math.Max(0, -net)

	bias := "neutral"
	switch {
	case net >= 0.25:
		bias = "bullish"
	case net <= -0.25:
		bias = "bearish"
	}

	return domain.SignalScores{
		TechnicalBull:     round2(bull),
		TechnicalBear:     round2(bear),
		Trend:             round2(trend),
		Momentum:          round2(momentum),
		Pattern:           round2(patternScore),
		VolatilityRisk:    round2(volRisk),
		Net:               round2(net),
		DeterministicBias: bias,
	}
}

func trendScore(ind domain.Indicators, structure domain.MarketStructure) float64 {
	var score, weight float64

	if v := value(ind.ADX); !math.IsNaN(v) {
		dir := 0.0
		plus, minus := value(ind.PlusDI), value(ind.MinusDI)
		if !math.IsNaN(plus) && !math.IsNaN(minus) {
			if plus > minus {
				dir = 1
			} else {
				dir = -1
			}
		}
		score += dir * math.Min(1, v/40) * 1.5
		weight += 1.5
	}
	if v := value(ind.PriceVsEMA200Pct); !math.IsNaN(v) {
		score += clampRange(v/5, -1, 1)
		weight++
	}
	if v := value(ind.PriceVsEMA50Pct); !math.IsNaN(v) {
		score += clampRange(v/3, -1, 1) * 0.7
		weight += 0.7
	}
	if v := value(ind.MACDHistogram); !math.IsNaN(v) {
		score += sign(v) * 0.5
		weight += 0.5
	}

	switch structure.State {
	case domain.StructureBullish:
		score++
		weight++
	case domain.StructureBearish:
		score--
		weight++
	case domain.StructureRange, domain.StructureTransition:
		weight += 0.5
	}

	if weight == 0 {
		return 0
	}
	return clampRange(score/weight, -1, 1)
}

func momentumScore(ind domain.Indicators) float64 {
	var score, weight float64

	if v := value(ind.RSI); !math.IsNaN(v) {
		// 50 is neutral; the extremes are treated as stretched, not as signal.
		score += clampRange((v-50)/25, -1, 1)
		weight++
	}
	if v := value(ind.StochK); !math.IsNaN(v) {
		score += clampRange((v-50)/40, -1, 1) * 0.6
		weight += 0.6
	}
	if v := value(ind.ROC); !math.IsNaN(v) {
		score += clampRange(v/3, -1, 1) * 0.8
		weight += 0.8
	}
	if v := value(ind.CCI); !math.IsNaN(v) {
		score += clampRange(v/150, -1, 1) * 0.6
		weight += 0.6
	}
	if v := value(ind.MFI); !math.IsNaN(v) {
		score += clampRange((v-50)/30, -1, 1) * 0.5
		weight += 0.5
	}
	if v := value(ind.RSISlope); !math.IsNaN(v) {
		score += clampRange(v/3, -1, 1) * 0.4
		weight += 0.4
	}

	if weight == 0 {
		return 0
	}
	return clampRange(score/weight, -1, 1)
}

func patternsScore(candlePatterns, chartPatterns []domain.Pattern, divs []domain.Divergence) float64 {
	var score, weight float64

	add := func(dir domain.PatternDirection, strength float64, ageDecay float64, w float64) {
		switch dir {
		case domain.PatternBullish:
			score += strength * ageDecay * w
		case domain.PatternBearish:
			score -= strength * ageDecay * w
		}
		weight += w
	}

	for _, p := range candlePatterns {
		if p.AgeCandles > 3 {
			continue
		}
		add(p.Direction, p.Strength, 1/(1+float64(p.AgeCandles)/2), 1)
	}
	for _, p := range chartPatterns {
		add(p.Direction, p.Strength, 1/(1+float64(p.AgeCandles)/10), 1.4)
	}
	for _, d := range divs {
		if d.AgeCandles > 15 {
			continue
		}
		add(d.Direction, d.Strength, 1/(1+float64(d.AgeCandles)/10), 1.2)
	}

	if weight == 0 {
		return 0
	}
	return clampRange(score/weight, -1, 1)
}

// volatilityRisk is 0 for calm conditions and 1 for dangerous ones.
func volatilityRisk(ind domain.Indicators) float64 {
	var risk float64
	count := 0

	if v := value(ind.ATRPercentile); !math.IsNaN(v) {
		risk += v / 100
		count++
	}
	if v := value(ind.ATRPct); !math.IsNaN(v) {
		risk += math.Min(1, v/4)
		count++
	}
	if v := value(ind.VolPercentile); !math.IsNaN(v) {
		risk += v / 100
		count++
	}
	if count == 0 {
		return 0.5 // unknown volatility is not the same as low volatility
	}
	return clampRange(risk/float64(count), 0, 1)
}

func value(p *float64) float64 {
	if p == nil {
		return math.NaN()
	}
	return *p
}

func sign(v float64) float64 {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	default:
		return 0
	}
}

func clampRange(v, lo, hi float64) float64 { //nolint:unparam // hi is 1 today; keeping the bound explicit documents the range
	if math.IsNaN(v) {
		return 0
	}
	return math.Max(lo, math.Min(hi, v))
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
