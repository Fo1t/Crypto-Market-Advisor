// Package marketstructure derives swing points, HH/HL/LH/LL labels and
// structural events (BOS / CHoCH) from OHLCV data.
//
// Anti-repainting rule: a pivot at index i is only reported once `depth`
// candles have closed after it. Every returned swing therefore carries the
// number of candles that were needed to confirm it, so backtesting can respect
// the same delay instead of pretending the pivot was known immediately.
package marketstructure

import (
	"github.com/crypto-market-advisor/advisor/internal/domain"
)

// DefaultDepth is the fractal half-width used for pivot detection.
const DefaultDepth = 3

// Analyze computes the market structure of a closed-candle series.
func Analyze(candles []domain.Candle, depth int) domain.MarketStructure {
	if depth <= 0 {
		depth = DefaultDepth
	}
	structure := domain.MarketStructure{State: domain.StructureUncertain}
	if len(candles) < depth*2+3 {
		structure.Description = "insufficient history"
		return structure
	}

	swings := FindSwings(candles, depth)
	if len(swings) == 0 {
		structure.Description = "no confirmed swing points"
		return structure
	}
	structure.Swings = swings
	structure.State = classify(swings)
	structure.Events = detectEvents(candles, swings, structure.State)

	if hi, ok := lastSwing(swings, true); ok {
		p := hi.Price
		structure.LastHigh = &p
	}
	if lo, ok := lastSwing(swings, false); ok {
		p := lo.Price
		structure.LastLow = &p
	}
	structure.Description = describe(structure.State, swings)
	return structure
}

// FindSwings returns confirmed pivots in chronological order.
func FindSwings(candles []domain.Candle, depth int) []domain.SwingPoint {
	n := len(candles)
	var out []domain.SwingPoint

	// i must have `depth` candles on both sides; the right side is what makes
	// the pivot confirmed, so we never look past the last closed candle.
	for i := depth; i < n-depth; i++ {
		if isPivotHigh(candles, i, depth) {
			out = append(out, domain.SwingPoint{
				Index: i, Time: candles[i].OpenTime, Price: candles[i].High,
				IsHigh: true, ConfirmedAfter: depth,
			})
		}
		if isPivotLow(candles, i, depth) {
			out = append(out, domain.SwingPoint{
				Index: i, Time: candles[i].OpenTime, Price: candles[i].Low,
				IsHigh: false, ConfirmedAfter: depth,
			})
		}
	}
	label(out)
	return out
}

func isPivotHigh(candles []domain.Candle, i, depth int) bool {
	h := candles[i].High
	for j := i - depth; j <= i+depth; j++ {
		if j == i {
			continue
		}
		if candles[j].High >= h {
			return false
		}
	}
	return true
}

func isPivotLow(candles []domain.Candle, i, depth int) bool {
	l := candles[i].Low
	for j := i - depth; j <= i+depth; j++ {
		if j == i {
			continue
		}
		if candles[j].Low <= l {
			return false
		}
	}
	return true
}

// label assigns HH/HL/LH/LL by comparing each swing with the previous swing of
// the same kind.
func label(swings []domain.SwingPoint) {
	var lastHigh, lastLow *domain.SwingPoint
	for i := range swings {
		s := &swings[i]
		if s.IsHigh {
			if lastHigh != nil {
				if s.Price > lastHigh.Price {
					s.Label = "HH"
				} else {
					s.Label = "LH"
				}
			}
			lastHigh = s
			continue
		}
		if lastLow != nil {
			if s.Price > lastLow.Price {
				s.Label = "HL"
			} else {
				s.Label = "LL"
			}
		}
		lastLow = s
	}
}

// classify turns the last few labelled swings into a structural state.
func classify(swings []domain.SwingPoint) domain.StructureState {
	labels := make([]string, 0, 4)
	for i := len(swings) - 1; i >= 0 && len(labels) < 4; i-- {
		if swings[i].Label != "" {
			labels = append(labels, swings[i].Label)
		}
	}
	if len(labels) < 2 {
		return domain.StructureUncertain
	}

	var bull, bear int
	for _, l := range labels {
		switch l {
		case "HH", "HL":
			bull++
		case "LH", "LL":
			bear++
		}
	}
	switch {
	case bull >= 3 && bear == 0:
		return domain.StructureBullish
	case bear >= 3 && bull == 0:
		return domain.StructureBearish
	case bull > bear && bear > 0:
		return domain.StructureTransition
	case bear > bull && bull > 0:
		return domain.StructureTransition
	case bull == bear:
		return domain.StructureRange
	case bull > bear:
		return domain.StructureBullish
	default:
		return domain.StructureBearish
	}
}

// detectEvents finds the most recent break of structure and change of character.
func detectEvents(candles []domain.Candle, swings []domain.SwingPoint, state domain.StructureState) []domain.StructureEvent {
	var events []domain.StructureEvent
	last := len(candles) - 1

	high, hasHigh := lastSwing(swings, true)
	low, hasLow := lastSwing(swings, false)

	// A break is only meaningful if it happened after the swing was confirmed.
	if hasHigh {
		confirmIdx := high.Index + high.ConfirmedAfter
		for i := confirmIdx; i <= last; i++ {
			if candles[i].Close > high.Price {
				eventType := "BOS"
				if state == domain.StructureBearish {
					eventType = "CHoCH"
				}
				events = append(events, domain.StructureEvent{
					Type: eventType, Direction: domain.DirectionLong,
					Price: high.Price, Time: candles[i].OpenTime, AgeCandles: last - i,
				})
				break
			}
		}
	}
	if hasLow {
		confirmIdx := low.Index + low.ConfirmedAfter
		for i := confirmIdx; i <= last; i++ {
			if candles[i].Close < low.Price {
				eventType := "BOS"
				if state == domain.StructureBullish {
					eventType = "CHoCH"
				}
				events = append(events, domain.StructureEvent{
					Type: eventType, Direction: domain.DirectionShort,
					Price: low.Price, Time: candles[i].OpenTime, AgeCandles: last - i,
				})
				break
			}
		}
	}
	return events
}

func lastSwing(swings []domain.SwingPoint, high bool) (domain.SwingPoint, bool) {
	for i := len(swings) - 1; i >= 0; i-- {
		if swings[i].IsHigh == high {
			return swings[i], true
		}
	}
	return domain.SwingPoint{}, false
}

func describe(state domain.StructureState, swings []domain.SwingPoint) string {
	labels := ""
	count := 0
	for i := len(swings) - 1; i >= 0 && count < 4; i-- {
		if swings[i].Label == "" {
			continue
		}
		if labels != "" {
			labels = swings[i].Label + " -> " + labels
		} else {
			labels = swings[i].Label
		}
		count++
	}
	if labels == "" {
		return string(state)
	}
	return string(state) + " (" + labels + ")"
}
