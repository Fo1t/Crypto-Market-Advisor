package main

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/crypto-market-advisor/advisor/internal/analysis/features"
	"github.com/crypto-market-advisor/advisor/internal/analysis/strategies"
	"github.com/crypto-market-advisor/advisor/internal/domain"
)

// The signal study answers a question the trade simulation cannot: does an
// entry rule know anything about the next few bars at all?
//
// A backtest mixes the entry with the stop, the target, the fees and the
// sizing, so a rule with real foresight and one with none can both come out at a
// profit factor near one. Here the exits are removed entirely: every bar where a
// rule asks for a side is scored by the return that actually followed, and
// compared against the return of every other bar. A rule whose conditional
// return does not beat the unconditional one has no edge to trade, and no exit
// geometry will create one.

// horizons are the forward windows, in bars, that every signal is scored over.
//
// How far out to look is the question itself when transaction cost is fixed per
// trade: a drift that keeps accumulating pays for the round trip if the position
// is simply held longer, and one that saturates does not.
var horizons = []int{1, 3, 5, 10, 20, 40}

// fundingBuckets is how the funding study slices the history. A perpetual whose
// funding sits in the top fifth of its own recent distribution is one where the
// crowd is paying heavily to stay long, and the question this answers is whether
// that says anything about what follows.
const fundingBuckets = 5

// fundingLookback is the history each settlement is ranked against, in
// settlements. Ninety days at three settlements a day.
const fundingLookback = 270

type signalStats struct {
	// forward[h] collects the direction-adjusted forward return in percent.
	forward map[int][]float64
	count   int
}

func newSignalStats() *signalStats {
	return &signalStats{forward: map[int][]float64{}}
}

func (s *signalStats) add(sign float64, candles []domain.Candle, index int) {
	s.count++
	entry := candles[index].Close
	if entry <= 0 {
		return
	}
	for _, h := range horizons {
		if index+h >= len(candles) {
			continue
		}
		change := (candles[index+h].Close - entry) / entry * 100 * sign
		s.forward[h] = append(s.forward[h], change)
	}
}

// studySignals walks every bar of every requested run and records what followed
// the decision the policy took there.
func studySignals(jobs []job, base domain.BacktestParams, workers int) {
	if workers <= 0 {
		workers = maxParallel()
	}

	type bucket struct {
		long, short, flat *signalStats
	}
	buckets := map[string]*bucket{}
	var mu sync.Mutex

	queue := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range queue {
				j := jobs[index]
				variant := labVariants[j.variant]
				local := studyOne(j, variant.apply(base))
				key := fmt.Sprintf("%s\t%s\t%s", variant.Name, j.window.name, j.tf)

				mu.Lock()
				target, ok := buckets[key]
				if !ok {
					target = &bucket{long: newSignalStats(), short: newSignalStats(), flat: newSignalStats()}
					buckets[key] = target
				}
				merge(target.long, local.long)
				merge(target.short, local.short)
				merge(target.flat, local.flat)
				mu.Unlock()
			}
		}()
	}
	for i := range jobs {
		queue <- i
	}
	close(queue)
	wg.Wait()

	keys := make([]string, 0, len(buckets))
	for key := range buckets {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	fmt.Print("variant\twindow\ttf\tside\tsignals")
	for _, h := range horizons {
		fmt.Printf("\tfwd%d\twin%d%%", h, h)
	}
	fmt.Println()
	for _, key := range keys {
		b := buckets[key]
		printSide(key, "long", b.long)
		printSide(key, "short", b.short)
		printSide(key, "none", b.flat)
	}
}

func merge(dst, src *signalStats) {
	dst.count += src.count
	for h, values := range src.forward {
		dst.forward[h] = append(dst.forward[h], values...)
	}
}

func printSide(key, side string, s *signalStats) {
	if s.count == 0 {
		return
	}
	fmt.Printf("%s\t%s\t%d", key, side, s.count)
	for _, h := range horizons {
		values := s.forward[h]
		fmt.Printf("\t%+.3f\t%.1f", mean(values), hitRate(values))
	}
	fmt.Println()
}

func hitRate(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	wins := 0
	for _, v := range values {
		if v > 0 {
			wins++
		}
	}
	return float64(wins) / float64(len(values)) * 100
}

type sideStats struct {
	long, short, flat *signalStats
}

// studyFunding scores the forward return of every bar by where the prevailing
// funding rate stood in its own recent distribution.
//
// Unlike the signal study this asks nothing about the strategy: it measures a
// property of the market directly, on every bar rather than on the handful that
// produced a signal, which is the only way an effect of this size can be told
// apart from noise on the history available.
func studyFunding(jobs []job, workers int) {
	if workers <= 0 {
		workers = maxParallel()
	}
	type bucketKey struct {
		window string
		tf     domain.Timeframe
		bucket int
	}
	buckets := map[bucketKey]*signalStats{}
	var mu sync.Mutex

	queue := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range queue {
				j := jobs[index]
				local := fundingStudyOne(j)
				mu.Lock()
				for bucket, stats := range local {
					key := bucketKey{window: j.window.name, tf: j.tf, bucket: bucket}
					target, ok := buckets[key]
					if !ok {
						target = newSignalStats()
						buckets[key] = target
					}
					merge(target, stats)
				}
				mu.Unlock()
			}
		}()
	}
	for i := range jobs {
		queue <- i
	}
	close(queue)
	wg.Wait()

	keys := make([]bucketKey, 0, len(buckets))
	for key := range buckets {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].window != keys[j].window {
			return keys[i].window < keys[j].window
		}
		return keys[i].bucket < keys[j].bucket
	})

	fmt.Print("window	tf	funding	bars")
	for _, h := range horizons {
		fmt.Printf("	fwd%d	win%d%%", h, h)
	}
	fmt.Println()
	for _, key := range keys {
		label := fmt.Sprintf("q%d", key.bucket+1)
		printSide(fmt.Sprintf("%s	%s", key.window, key.tf), label, buckets[key])
	}
}

// fundingStudyOne buckets the bars of one symbol by the percentile of the
// funding rate in force at that moment.
func fundingStudyOne(j job) map[int]*signalStats {
	out := map[int]*signalStats{}
	if len(j.funding) == 0 {
		return out
	}
	candles := j.series[j.tf]

	// The settlement in force at a bar is the last one that had already happened.
	cursor := 0
	for i := features.MinCandles; i < len(candles); i++ {
		current := candles[i]
		if current.OpenTime.Before(j.window.from) {
			continue
		}
		for cursor < len(j.funding) && !j.funding[cursor].SettledAt.After(current.CloseTime) {
			cursor++
		}
		if cursor < fundingLookback {
			continue // not enough of its own history to rank against
		}

		history := j.funding[cursor-fundingLookback : cursor]
		currentRate := j.funding[cursor-1].Rate
		below := 0
		for _, past := range history {
			if past.Rate < currentRate {
				below++
			}
		}
		bucket := below * fundingBuckets / len(history)
		if bucket >= fundingBuckets {
			bucket = fundingBuckets - 1
		}
		if out[bucket] == nil {
			out[bucket] = newSignalStats()
		}
		out[bucket].add(1, candles, i)
	}
	return out
}

// marketRegimeAt classifies the market as the shipped filter sees it: the
// benchmark against its own long daily average, and whether that average is
// itself rising. Splitting a study by this is what separates "what follows a
// rally" from "what follows a rally inside a bear market", which are different
// questions with, quite possibly, different answers.
func marketRegimeAt(benchmark []domain.Candle, at time.Time) string {
	if len(benchmark) == 0 {
		return "нет данных"
	}
	end := sort.Search(len(benchmark), func(i int) bool { return benchmark[i].CloseTime.After(at) })
	context := features.MarketContextFrom("", benchmark[:end])
	if !context.Known() || context.PriceVsEMA200Pct == nil {
		return "нет данных"
	}
	distance := *context.PriceVsEMA200Pct
	slope := 0.0
	if context.EMA200SlopePct != nil {
		slope = *context.EMA200SlopePct
	}
	switch {
	case distance >= 1 && slope > 0:
		return "рынок растёт"
	case distance <= -1 || slope < 0:
		return "рынок падает"
	default:
		return "рынок неясен"
	}
}

// studyMoves scores the forward return of every bar by how far price has just
// travelled, measured in the instrument's own average bar ranges.
//
// This asks the one question that decides whether a fast timeframe is tradable
// at all: after a sharp move, does price continue or come back? Momentum and
// short-term reversal are opposite answers, and the ensemble currently assumes
// the first. Measuring it needs no strategy - only the bars - so the answer
// comes from a hundred thousand samples rather than a handful of windows.
func studyMoves(jobs []job, workers int, lookback int) {
	if workers <= 0 {
		workers = maxParallel()
	}
	type bucketKey struct {
		regime string
		tf     domain.Timeframe
		bucket int
	}
	buckets := map[bucketKey]*signalStats{}
	var mu sync.Mutex

	queue := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range queue {
				j := jobs[index]
				local := moveStudyOne(j, lookback)
				mu.Lock()
				for bucket, stats := range local {
					key := bucketKey{regime: bucket.regime, tf: j.tf, bucket: bucket.move}
					if buckets[key] == nil {
						buckets[key] = newSignalStats()
					}
					merge(buckets[key], stats)
				}
				mu.Unlock()
			}
		}()
	}
	for i := range jobs {
		queue <- i
	}
	close(queue)
	wg.Wait()

	keys := make([]bucketKey, 0, len(buckets))
	for key := range buckets {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].regime != keys[j].regime {
			return keys[i].regime < keys[j].regime
		}
		return keys[i].bucket < keys[j].bucket
	})

	fmt.Printf("режим	tf	движение за %d баров, ATR	баров", lookback)
	for _, h := range horizons {
		fmt.Printf("	fwd%d	win%d%%", h, h)
	}
	fmt.Println()
	for _, key := range keys {
		printSide(fmt.Sprintf("%s	%s", key.regime, key.tf), moveBucketLabel(key.bucket), buckets[key])
	}
}

// moveBuckets are the edges, in average bar ranges, of the trailing move.
var moveBuckets = []float64{-4, -2, -1, 1, 2, 4}

func moveBucketLabel(bucket int) string {
	switch {
	case bucket == 0:
		return fmt.Sprintf("<%.0f", moveBuckets[0])
	case bucket >= len(moveBuckets):
		return fmt.Sprintf(">%.0f", moveBuckets[len(moveBuckets)-1])
	default:
		return fmt.Sprintf("%.0f..%.0f", moveBuckets[bucket-1], moveBuckets[bucket])
	}
}

// moveBucket identifies one cell of the study: a market regime and a size of the
// move that preceded the bar.
type moveBucket struct {
	regime string
	move   int
}

// moveStudyOne buckets the bars of one symbol by the trailing move and the
// regime the market as a whole was in at that moment.
func moveStudyOne(j job, lookback int) map[moveBucket]*signalStats {
	out := map[moveBucket]*signalStats{}
	candles := j.series[j.tf]

	for i := lookback + 20; i < len(candles); i++ {
		current := candles[i]
		if current.OpenTime.Before(j.window.from) {
			continue
		}
		// The average range is measured on the bars before the move, so a violent
		// move does not inflate its own yardstick.
		window := candles[i-lookback-20 : i-lookback]
		span := 0.0
		for _, c := range window {
			span += c.High - c.Low
		}
		span /= float64(len(window))
		if span <= 0 {
			continue
		}
		move := (current.Close - candles[i-lookback].Close) / span

		index := len(moveBuckets)
		for b, edge := range moveBuckets {
			if move < edge {
				index = b
				break
			}
		}
		bucket := moveBucket{regime: marketRegimeAt(j.benchmark, current.CloseTime), move: index}
		if out[bucket] == nil {
			out[bucket] = newSignalStats()
		}
		out[bucket].add(1, candles, i)
	}
	return out
}

// studyOne replays one symbol and records the decision on every bar. It uses the
// same analysis and the same policy evaluation the engine does, so a signal seen
// here is a signal the backtest would have acted on.
func studyOne(j job, params domain.BacktestParams) sideStats {
	out := sideStats{long: newSignalStats(), short: newSignalStats(), flat: newSignalStats()}
	candles := j.series[j.tf]
	set := strategies.DefaultSet()
	if params.Strategies != nil {
		set = *params.Strategies
	}

	for i := features.MinCandles; i < len(candles); i++ {
		current := candles[i]
		if current.OpenTime.Before(j.window.from) {
			continue
		}
		visible := visibleAt(j.series, current.CloseTime)
		primary := visible[j.tf]
		if len(primary) < features.MinCandles {
			continue
		}
		analyses := make(map[domain.Timeframe]domain.TimeframeAnalysis, len(visible))
		for tf, series := range visible {
			if len(series) >= features.MinCandles {
				analyses[tf] = features.AnalyzeTimeframe(tf, series)
			}
		}
		analysis, ok := analyses[j.tf]
		if !ok {
			continue
		}
		snapshot := features.BuildSnapshot(features.SnapshotInput{
			Symbol: j.symbol, Price: current.Close, Timeframes: analyses, Now: current.CloseTime,
		})
		decision := strategies.Evaluate(strategies.Input{
			Timeframe: j.tf, Analysis: analysis, Snapshot: snapshot, Candles: primary,
			Price: current.Close, Now: current.CloseTime,
			RoundTripCostPct: params.TakerFeePct.InexactFloat64()*2 + params.SlippagePct.InexactFloat64()*2,
		}, set)

		switch {
		case decision.Action == domain.RecommendationOpenLong && decision.Confidence >= params.MinConfidence:
			out.long.add(1, candles, i)
		case decision.Action == domain.RecommendationOpenShort && decision.Confidence >= params.MinConfidence:
			out.short.add(-1, candles, i)
		default:
			// The unconditional benchmark is scored long, so a positive number
			// here is simply the drift every rule has to beat.
			out.flat.add(1, candles, i)
		}
	}
	return out
}

// visibleAt is the same slice-by-close-time rule the engine uses: only candles
// that had already closed at the given moment are visible.
func visibleAt(series map[domain.Timeframe][]domain.Candle, at time.Time) map[domain.Timeframe][]domain.Candle {
	const limit = 500
	visible := make(map[domain.Timeframe][]domain.Candle, len(series))
	for tf, candles := range series {
		end := sort.Search(len(candles), func(i int) bool { return candles[i].CloseTime.After(at) })
		start := end - limit
		if start < 0 {
			start = 0
		}
		visible[tf] = candles[start:end]
	}
	return visible
}
