// Package divergences finds regular and hidden divergences between price and
// an oscillator (RSI, MACD histogram, OBV).
package divergences

import (
	"math"

	"github.com/crypto-market-advisor/advisor/internal/analysis/indicators"
	"github.com/crypto-market-advisor/advisor/internal/domain"
)

// Options tunes divergence detection.
type Options struct {
	// Lookback limits how far back pivot pairs may be taken from.
	Lookback int
	// MinBarsApart rejects pivot pairs that are too close to be meaningful.
	MinBarsApart int
	// MaxBarsApart rejects pivot pairs that are too far apart to be related.
	MaxBarsApart int
	// MinDeltaPct is the minimum relative price difference between pivots.
	MinDeltaPct float64
}

// DefaultOptions returns sensible defaults.
func DefaultOptions() Options {
	return Options{Lookback: 120, MinBarsApart: 4, MaxBarsApart: 60, MinDeltaPct: 0.15}
}

// Detect finds divergences between price pivots and an indicator series.
// `swings` must be confirmed pivots aligned with the same candle indices.
func Detect(candles []domain.Candle, swings []domain.SwingPoint, indicatorName string, series []float64, opts Options) []domain.Divergence {
	if len(candles) == 0 || len(series) != len(candles) || len(swings) < 2 {
		return nil
	}
	if opts.Lookback <= 0 {
		opts = DefaultOptions()
	}
	last := len(candles) - 1
	minIndex := last - opts.Lookback
	if minIndex < 0 {
		minIndex = 0
	}

	var out []domain.Divergence
	out = append(out, scan(candles, swings, indicatorName, series, opts, minIndex, true)...)
	out = append(out, scan(candles, swings, indicatorName, series, opts, minIndex, false)...)
	return out
}

// scan walks pairs of consecutive same-kind pivots and compares the direction
// of the price move with the direction of the indicator move.
func scan(candles []domain.Candle, swings []domain.SwingPoint, name string, series []float64, opts Options, minIndex int, highs bool) []domain.Divergence {
	pivots := make([]domain.SwingPoint, 0, len(swings))
	for _, s := range swings {
		if s.IsHigh == highs && s.Index >= minIndex && indicators.Valid(series[s.Index]) {
			pivots = append(pivots, s)
		}
	}
	if len(pivots) < 2 {
		return nil
	}

	last := len(candles) - 1
	var out []domain.Divergence

	// Only the most recent pairs are interesting; older ones add noise.
	start := len(pivots) - 4
	if start < 1 {
		start = 1
	}
	for i := start; i < len(pivots); i++ {
		a, b := pivots[i-1], pivots[i]
		gap := b.Index - a.Index
		if gap < opts.MinBarsApart || gap > opts.MaxBarsApart {
			continue
		}
		if a.Price == 0 {
			continue
		}
		priceDelta := (b.Price - a.Price) / a.Price * 100
		if math.Abs(priceDelta) < opts.MinDeltaPct {
			continue
		}
		indDelta := series[b.Index] - series[a.Index]
		if indDelta == 0 {
			continue
		}

		var divType string
		var direction domain.PatternDirection
		switch {
		case highs && priceDelta > 0 && indDelta < 0:
			divType, direction = "regular", domain.PatternBearish
		case highs && priceDelta < 0 && indDelta > 0:
			divType, direction = "hidden", domain.PatternBearish
		case !highs && priceDelta < 0 && indDelta > 0:
			divType, direction = "regular", domain.PatternBullish
		case !highs && priceDelta > 0 && indDelta < 0:
			divType, direction = "hidden", domain.PatternBullish
		default:
			continue
		}

		strength := strengthOf(priceDelta, indDelta, last-b.Index)
		out = append(out, domain.Divergence{
			Indicator:  name,
			Type:       divType,
			Direction:  direction,
			Strength:   strength,
			FromIndex:  a.Index - last,
			ToIndex:    b.Index - last,
			AgeCandles: last - b.Index,
		})
	}
	return out
}

// strengthOf grows with the size of the disagreement and decays with age.
func strengthOf(priceDelta, indDelta float64, age int) float64 {
	magnitude := math.Min(1, math.Abs(priceDelta)/3) * math.Min(1, math.Abs(indDelta)/10)
	if magnitude == 0 {
		magnitude = math.Min(1, math.Abs(priceDelta)/3) * 0.4
	}
	decay := 1.0 / (1.0 + float64(age)/25.0)
	return math.Max(0.1, math.Min(1, 0.35+magnitude)) * decay
}
