package main

import (
	"fmt"
	"math/rand"
	"sort"
	"time"
)

// Forward windows overlap: two neighbouring bars share almost all of the future
// they are scored on, so a table built from a hundred thousand bars does not
// carry a hundred thousand independent observations. Counting them as if it did
// is how an effect that is really one lucky quarter looks overwhelming.
//
// The block bootstrap answers the question honestly. The timeline is cut into
// calendar months; whole months are resampled with replacement, all assets
// together so that the correlation between them survives; and the statistic is
// recomputed on each synthetic history. What comes out is not a single number
// but a distribution, and the useful summary is how often the effect keeps its
// sign.

// blockKey identifies one month of one condition.
type blockKey struct {
	block  string
	regime string
	bucket int
}

// blockStats accumulates the forward return of one cell.
type blockStats struct {
	sum   float64
	count int
}

// bootstrapSpread resamples months and reports the distribution of the gap
// between one move bucket and the quiet baseline inside one regime.
func bootstrapSpread(cells map[blockKey]*blockStats, regime string, bucket, baseline, replications int, seed int64) {
	blocks := map[string]bool{}
	for key := range cells {
		if key.regime == regime {
			blocks[key.block] = true
		}
	}
	usable := make([]string, 0, len(blocks))
	for block := range blocks {
		a, b := cells[blockKey{block, regime, bucket}], cells[blockKey{block, regime, baseline}]
		if a != nil && b != nil && a.count > 0 && b.count > 0 {
			usable = append(usable, block)
		}
	}
	sort.Strings(usable)
	if len(usable) < 8 {
		fmt.Printf("%-14s %-14s недостаточно блоков (%d)\n", regime, moveBucketLabel(bucket), len(usable))
		return
	}

	dice := rand.New(rand.NewSource(seed))
	samples := make([]float64, 0, replications)
	for rep := 0; rep < replications; rep++ {
		var bucketSum, bucketN, baseSum, baseN float64
		for i := 0; i < len(usable); i++ {
			block := usable[dice.Intn(len(usable))]
			a := cells[blockKey{block, regime, bucket}]
			b := cells[blockKey{block, regime, baseline}]
			bucketSum += a.sum
			bucketN += float64(a.count)
			baseSum += b.sum
			baseN += float64(b.count)
		}
		if bucketN == 0 || baseN == 0 {
			continue
		}
		samples = append(samples, bucketSum/bucketN-baseSum/baseN)
	}
	if len(samples) == 0 {
		return
	}
	sort.Float64s(samples)

	positive := 0
	for _, v := range samples {
		if v > 0 {
			positive++
		}
	}
	fmt.Printf("%-14s %-14s %9.3f %9.3f %9.3f %10.0f%% %8d\n",
		regime, moveBucketLabel(bucket), median(samples),
		quantile(samples, 0.05), quantile(samples, 0.95),
		float64(positive)/float64(len(samples))*100, len(usable))
}

// collectBlocks walks the same bars the move study walks and files each one
// under the month it belongs to.
func collectBlocks(jobs []job, lookback, horizon int) map[blockKey]*blockStats {
	out := map[blockKey]*blockStats{}
	for _, j := range jobs {
		candles := j.series[j.tf]
		for i := lookback + 20; i < len(candles)-horizon; i++ {
			current := candles[i]
			if current.OpenTime.Before(j.window.from) {
				continue
			}
			window := candles[i-lookback-20 : i-lookback]
			span := 0.0
			for _, c := range window {
				span += c.High - c.Low
			}
			span /= float64(len(window))
			if span <= 0 || current.Close <= 0 {
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
			key := blockKey{
				block:  current.CloseTime.Format("2006-01"),
				regime: marketRegimeAt(j.benchmark, current.CloseTime),
				bucket: index,
			}
			if out[key] == nil {
				out[key] = &blockStats{}
			}
			out[key].sum += (candles[i+horizon].Close - current.Close) / current.Close * 100
			out[key].count++
		}
	}
	return out
}

// reportBootstrap prints the resampled distribution for every move bucket
// against the quiet baseline, inside each regime.
func reportBootstrap(jobs []job, lookback, horizon, replications int) {
	start := time.Now()
	cells := collectBlocks(jobs, lookback, horizon)
	fmt.Printf("\nблочный бутстрап: месячные блоки, %d повторов, горизонт %d баров (сбор %.1fs)\n",
		replications, horizon, time.Since(start).Seconds())
	fmt.Printf("%-14s %-14s %9s %9s %9s %11s %8s\n",
		"режим", "группа", "медиана", "5%", "95%", "доля>0", "блоков")

	const baseline = 3 // the quiet bucket, -1..1 average ranges
	for _, regime := range []string{"рынок растёт", "рынок падает"} {
		for bucket := 0; bucket <= len(moveBuckets); bucket++ {
			if bucket == baseline {
				continue
			}
			bootstrapSpread(cells, regime, bucket, baseline, replications, int64(bucket)*7919+1)
		}
	}
}
