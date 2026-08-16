// Package supportresistance derives price levels from swing points, calendar
// extremes and round numbers, then clusters them into a compact list.
package supportresistance

import (
	"math"
	"sort"
	"time"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

// Options tunes level detection.
type Options struct {
	// ClusterTolerancePct merges candidate prices that sit within this
	// percentage of each other.
	ClusterTolerancePct float64
	// MaxLevels caps how many levels are returned.
	MaxLevels int
	// IncludePsychological adds round-number levels near the current price.
	IncludePsychological bool
}

// DefaultOptions returns sensible defaults for crypto perpetuals.
func DefaultOptions() Options {
	return Options{ClusterTolerancePct: 0.35, MaxLevels: 8, IncludePsychological: true}
}

type candidate struct {
	price  float64
	weight float64
	origin string
	age    int
}

// Detect returns support and resistance levels sorted by strength.
func Detect(candles []domain.Candle, swings []domain.SwingPoint, currentPrice float64, opts Options) []domain.Level {
	if len(candles) == 0 || currentPrice <= 0 {
		return nil
	}
	if opts.MaxLevels <= 0 {
		opts = DefaultOptions()
	}

	last := len(candles) - 1
	var candidates []candidate

	for _, s := range swings {
		age := last - s.Index
		// Recent swings matter more, but old ones are not worthless.
		weight := 1.0 / (1.0 + float64(age)/60.0)
		origin := "swing_low"
		if s.IsHigh {
			origin = "swing_high"
		}
		candidates = append(candidates, candidate{price: s.Price, weight: weight, origin: origin, age: age})
	}

	candidates = append(candidates, calendarExtremes(candles)...)

	if opts.IncludePsychological {
		candidates = append(candidates, psychological(currentPrice)...)
	}
	if len(candidates) == 0 {
		return nil
	}

	clusters := cluster(candidates, opts.ClusterTolerancePct)
	levels := make([]domain.Level, 0, len(clusters))
	for _, cl := range clusters {
		lvl := domain.Level{
			Price:       cl.price,
			Strength:    math.Min(1, cl.weight/2.5),
			Touches:     cl.touches,
			DistancePct: (cl.price - currentPrice) / currentPrice * 100,
			Origin:      cl.origin,
		}
		if cl.price >= currentPrice {
			lvl.Type = domain.LevelResistance
		} else {
			lvl.Type = domain.LevelSupport
		}
		levels = append(levels, lvl)
	}

	// Prefer strong levels close to price: a strong level 40% away is noise.
	sort.SliceStable(levels, func(i, j int) bool {
		return score(levels[i]) > score(levels[j])
	})
	if len(levels) > opts.MaxLevels {
		levels = levels[:opts.MaxLevels]
	}
	sort.SliceStable(levels, func(i, j int) bool { return levels[i].Price < levels[j].Price })
	return levels
}

func score(l domain.Level) float64 {
	distance := math.Abs(l.DistancePct)
	return l.Strength / (1 + distance/5)
}

// calendarExtremes adds previous day and previous week high/low when the candle
// series spans enough calendar time.
func calendarExtremes(candles []domain.Candle) []candidate {
	if len(candles) == 0 {
		return nil
	}
	last := candles[len(candles)-1].OpenTime.UTC()

	prevDayStart := last.Truncate(24 * time.Hour).Add(-24 * time.Hour)
	prevDayEnd := prevDayStart.Add(24 * time.Hour)

	weekday := int(last.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	weekStart := last.Truncate(24*time.Hour).AddDate(0, 0, -(weekday - 1))
	prevWeekStart := weekStart.AddDate(0, 0, -7)

	var out []candidate
	if hi, lo, ok := extremes(candles, prevDayStart, prevDayEnd); ok {
		out = append(out,
			candidate{price: hi, weight: 0.9, origin: "prev_day_high"},
			candidate{price: lo, weight: 0.9, origin: "prev_day_low"},
		)
	}
	if hi, lo, ok := extremes(candles, prevWeekStart, weekStart); ok {
		out = append(out,
			candidate{price: hi, weight: 1.1, origin: "prev_week_high"},
			candidate{price: lo, weight: 1.1, origin: "prev_week_low"},
		)
	}
	return out
}

func extremes(candles []domain.Candle, from, to time.Time) (high, low float64, ok bool) {
	high, low = math.Inf(-1), math.Inf(1)
	for _, c := range candles {
		t := c.OpenTime.UTC()
		if t.Before(from) || !t.Before(to) {
			continue
		}
		high = math.Max(high, c.High)
		low = math.Min(low, c.Low)
		ok = true
	}
	return high, low, ok
}

// psychological returns round-number levels bracketing the current price.
func psychological(price float64) []candidate {
	step := roundStep(price)
	if step <= 0 {
		return nil
	}
	base := math.Floor(price/step) * step
	return []candidate{
		{price: base, weight: 0.5, origin: "psychological"},
		{price: base + step, weight: 0.5, origin: "psychological"},
	}
}

// roundStep picks a human-meaningful round increment for the price magnitude.
func roundStep(price float64) float64 {
	switch {
	case price >= 10000:
		return 5000
	case price >= 1000:
		return 500
	case price >= 100:
		return 50
	case price >= 10:
		return 5
	case price >= 1:
		return 0.5
	case price > 0:
		return math.Pow(10, math.Floor(math.Log10(price)))
	default:
		return 0
	}
}

type clusterResult struct {
	price   float64
	weight  float64
	touches int
	origin  string
}

// cluster merges nearby candidates into weighted levels.
func cluster(candidates []candidate, tolerancePct float64) []clusterResult {
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].price < candidates[j].price })

	var out []clusterResult
	var current []candidate

	flush := func() {
		if len(current) == 0 {
			return
		}
		var sumWeight, weighted float64
		origins := map[string]float64{}
		for _, c := range current {
			sumWeight += c.weight
			weighted += c.price * c.weight
			origins[c.origin] += c.weight
		}
		best, bestWeight := "", 0.0
		for o, w := range origins {
			if w > bestWeight {
				best, bestWeight = o, w
			}
		}
		out = append(out, clusterResult{
			price:   weighted / sumWeight,
			weight:  sumWeight,
			touches: len(current),
			origin:  best,
		})
		current = nil
	}

	for _, c := range candidates {
		if len(current) == 0 {
			current = append(current, c)
			continue
		}
		ref := current[0].price
		if ref > 0 && math.Abs(c.price-ref)/ref*100 <= tolerancePct {
			current = append(current, c)
			continue
		}
		flush()
		current = append(current, c)
	}
	flush()
	return out
}

// Nearest returns the closest support below and resistance above the price.
func Nearest(levels []domain.Level, price float64) (support, resistance *domain.Level) {
	for i := range levels {
		l := levels[i]
		if l.Price < price && (support == nil || l.Price > support.Price) {
			lv := l
			support = &lv
		}
		if l.Price > price && (resistance == nil || l.Price < resistance.Price) {
			lv := l
			resistance = &lv
		}
	}
	return support, resistance
}
