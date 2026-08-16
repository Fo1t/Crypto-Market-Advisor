package features

import (
	"math"
	"sort"
	"time"

	"github.com/crypto-market-advisor/advisor/internal/analysis/indicators"
	"github.com/crypto-market-advisor/advisor/internal/domain"
)

// MarketContextEMAPeriod is the average the benchmark is judged against. Two
// hundred daily bars is the conventional dividing line between a bull and a bear
// market, and the point of using the convention is that it was not chosen by
// fitting it to this history.
const MarketContextEMAPeriod = 200

// MarketContextSlopeBars is how far back the benchmark average is compared with
// itself to say whether it is rising. Thirty daily bars is long enough that a
// single week does not flip it and short enough to turn within a real reversal.
const MarketContextSlopeBars = 30

// MarketContextBandPct is how far from the average still counts as neither side.
// A benchmark sitting on its own average says nothing about direction, and
// treating that as a signal would flip the state on every daily close.
const MarketContextBandPct = 1.0

// MarketContextFrom reads the state of the market as a whole from a benchmark
// series, which must be daily candles that had already closed at the moment of
// the analysis. Anything shorter than the average produces an unknown state
// rather than a guess: an unknown context lets a trade through, a wrong one
// would block the right ones.
func MarketContextFrom(benchmark string, candles []domain.Candle) domain.MarketContext {
	out := domain.MarketContext{Benchmark: benchmark}
	if len(candles) < MarketContextEMAPeriod {
		return out
	}
	closes := make([]float64, 0, len(candles))
	for _, candle := range candles {
		closes = append(closes, candle.Close)
	}
	ema := indicators.EMA(closes, MarketContextEMAPeriod)
	if len(ema) == 0 {
		return out
	}

	last := candles[len(candles)-1]
	average := ema[len(ema)-1]
	if average <= 0 || last.Close <= 0 {
		return out
	}
	distance := (last.Close - average) / average * 100
	out.PriceVsEMA200Pct = &distance

	// Where the average itself is heading. Price crosses its long average on
	// every rally, including the ones inside a bear market; the average only
	// turns when the trend does, so the two say different things.
	if len(ema) > MarketContextSlopeBars {
		previous := ema[len(ema)-1-MarketContextSlopeBars]
		if previous > 0 {
			slope := (average - previous) / previous * 100
			out.EMA200SlopePct = &slope
		}
	}
	out.AsOf = last.CloseTime
	switch {
	case distance > MarketContextBandPct:
		out.Trend = domain.MarketTrendUp
	case distance < -MarketContextBandPct:
		out.Trend = domain.MarketTrendDown
	default:
		out.Trend = domain.MarketTrendFlat
	}
	return out
}

// RelativeStrengthLookback is the horizon the cross-sectional ranking uses, in
// daily bars. Roughly three months is the conventional momentum window and, as
// with the two-hundred-day average above, the point of using the convention is
// that it was not chosen by fitting it to this history.
const RelativeStrengthLookback = 90

// RelativeStrength scores how strongly an asset has moved over the lookback,
// per unit of its own risk.
//
// The raw return alone is not comparable across crypto assets: a small alt can
// double while a major moves a tenth as much at a quarter of the volatility, and
// ranking on the raw number simply ranks by volatility. Dividing by the standard
// deviation of the daily returns asks the question that matters instead - which
// asset has travelled furthest relative to how much it usually shakes.
func RelativeStrength(candles []domain.Candle) (float64, bool) {
	if len(candles) < RelativeStrengthLookback+1 {
		return 0, false
	}
	window := candles[len(candles)-RelativeStrengthLookback-1:]
	first, last := window[0].Close, window[len(window)-1].Close
	if first <= 0 || last <= 0 {
		return 0, false
	}

	changes := make([]float64, 0, len(window)-1)
	for i := 1; i < len(window); i++ {
		previous := window[i-1].Close
		if previous <= 0 {
			return 0, false
		}
		changes = append(changes, (window[i].Close-previous)/previous)
	}

	mean := 0.0
	for _, change := range changes {
		mean += change
	}
	mean /= float64(len(changes))

	variance := 0.0
	for _, change := range changes {
		d := change - mean
		variance += d * d
	}
	deviation := math.Sqrt(variance / float64(len(changes)))
	if deviation <= 0 {
		return 0, false
	}
	return (last - first) / first / deviation, true
}

// RankUniverse turns per-symbol strength scores into the percentile each symbol
// occupies, where 100 is the strongest asset on screen.
//
// A universe of one produces no ranking at all rather than a symbol that is
// simultaneously the best and the worst: with nothing to compare against, the
// filter that reads this has no argument to make.
func RankUniverse(scores map[string]float64, at time.Time) map[string]domain.UniverseContext {
	out := make(map[string]domain.UniverseContext, len(scores))
	if len(scores) < 2 {
		return out
	}
	type entry struct {
		symbol string
		score  float64
	}
	ordered := make([]entry, 0, len(scores))
	for symbol, score := range scores {
		ordered = append(ordered, entry{symbol: symbol, score: score})
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].score == ordered[j].score {
			return ordered[i].symbol < ordered[j].symbol
		}
		return ordered[i].score < ordered[j].score
	})

	last := len(ordered) - 1
	for i, item := range ordered {
		out[item.symbol] = domain.UniverseContext{
			RankPct: float64(i) / float64(last) * 100,
			Score:   item.score,
			Members: len(ordered),
			AsOf:    at.UTC(),
		}
	}
	return out
}

// DailyRanker answers where a symbol stood among its peers at a given moment.
//
// The ranking is precomputed once per daily close for the whole universe and
// then looked up, because recomputing it on every simulated bar would repeat the
// same work thousands of times. Only closes that had already happened take part
// in a rank, which is what keeps the cross-sectional view free of look-ahead.
type DailyRanker struct {
	times []time.Time
	ranks []map[string]domain.UniverseContext
}

// NewDailyRanker precomputes the ranking from the daily candles of every symbol
// in the universe. A universe of fewer than two symbols produces a ranker that
// reports nothing, which the filters treat as no argument.
func NewDailyRanker(universe map[string][]domain.Candle) *DailyRanker {
	if len(universe) < 2 {
		return &DailyRanker{}
	}
	// The union of close times is the clock: assets list at different dates and a
	// missing bar must not shift anyone's history.
	seen := map[int64]time.Time{}
	for _, candles := range universe {
		for _, candle := range candles {
			seen[candle.CloseTime.Unix()] = candle.CloseTime.UTC()
		}
	}
	times := make([]time.Time, 0, len(seen))
	for _, at := range seen {
		times = append(times, at)
	}
	sort.Slice(times, func(i, j int) bool { return times[i].Before(times[j]) })

	// Walking the clock forward lets each symbol keep a cursor instead of being
	// searched from the start for every timestamp.
	cursors := make(map[string]int, len(universe))
	out := &DailyRanker{times: times, ranks: make([]map[string]domain.UniverseContext, 0, len(times))}
	for _, at := range times {
		scores := make(map[string]float64, len(universe))
		for symbol, candles := range universe {
			i := cursors[symbol]
			for i < len(candles) && !candles[i].CloseTime.After(at) {
				i++
			}
			cursors[symbol] = i
			if score, ok := RelativeStrength(candles[:i]); ok {
				scores[symbol] = score
			}
		}
		out.ranks = append(out.ranks, RankUniverse(scores, at))
	}
	return out
}

// RankAt reports the standing of one symbol as of the last daily close that had
// happened at the given moment.
func (r *DailyRanker) RankAt(symbol string, at time.Time) domain.UniverseContext {
	if r == nil || len(r.times) == 0 {
		return domain.UniverseContext{}
	}
	// The first index strictly after the moment; the one before it is the last
	// close the analysis could legitimately have seen.
	index := sort.Search(len(r.times), func(i int) bool { return r.times[i].After(at) }) - 1
	if index < 0 {
		return domain.UniverseContext{}
	}
	return r.ranks[index][symbol]
}
