package main

import (
	"fmt"
	"math"
	"os"
	"sort"

	"github.com/crypto-market-advisor/advisor/internal/backtesting"
	"github.com/crypto-market-advisor/advisor/internal/domain"
)

// The control group answers a question the strategy's own numbers cannot: a
// long-only policy in a rising market shows a profit factor above one whether or
// not it predicts anything. What matters is the gap between the strategy and
// entries that are random but matched in every other respect - same asset, same
// period, same exit machinery, same sizing, same fees, same funding, and the
// same number of trades.
//
// If the strategy and the coin toss land on the same figure, the result is the
// market and the execution, not the signal.

// reportPlacebo repeats every job with random entries and prints the resulting
// distribution against the real figures.
func reportPlacebo(engine *backtesting.Engine, base domain.BacktestParams, jobs []job, real []Result, replications, workers int) {
	// The coin is weighted so that each replication opens about as many trades as
	// the real run did on the same series: comparing forty random trades with a
	// hundred real ones would compare sample sizes, not skill.
	chance := make([]float64, len(jobs))
	for i, r := range real {
		points := r.Metrics.AnalysisPoints
		if points <= 0 || r.Metrics.Trades == 0 {
			continue
		}
		chance[i] = math.Min(1, float64(r.Metrics.Trades)/float64(points))
	}

	// The strategy's own figure has to be pooled exactly the way a replication is
	// pooled. Averaging per-run profit factors instead would compare a mean of
	// ratios with a ratio of sums, and the first is systematically the larger of
	// the two - which would hand the strategy an edge it did not earn.
	type key struct{ variant, window string }
	realAgg := map[key]*aggregate{}
	for _, r := range real {
		k := key{r.Variant, r.Window}
		if realAgg[k] == nil {
			realAgg[k] = &aggregate{}
		}
		realAgg[k].add(r)
	}

	samples := map[key][]float64{}
	for rep := 0; rep < replications; rep++ {
		control := make([]job, 0, len(jobs))
		for i, j := range jobs {
			if chance[i] <= 0 {
				continue
			}
			j.randomChance = chance[i]
			j.randomSeed = int64(rep)*1_000_003 + int64(i)
			control = append(control, j)
		}
		if len(control) == 0 {
			continue
		}
		out := runJobs(engine, base, control, workers)

		// One replication is pooled the same way the real table pools its runs, so
		// the two figures are comparable.
		agg := map[key]*aggregate{}
		for _, r := range out {
			k := key{r.Variant, r.Window}
			if agg[k] == nil {
				agg[k] = &aggregate{}
			}
			agg[k].add(r)
		}
		for k, a := range agg {
			samples[k] = append(samples[k], a.profitFactor())
		}
		fmt.Fprintf(os.Stderr, "\rконтрольных повторов: %d/%d", rep+1, replications)
	}
	fmt.Fprintln(os.Stderr)

	fmt.Println()
	fmt.Println("контроль: случайные входы, всё остальное совпадает")
	fmt.Println("variant\twindow\tPF стратегии\tPF случайных: медиана\t5%\t95%\tразница\tдоля повторов хуже стратегии")
	keys := make([]key, 0, len(samples))
	for k := range samples {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].variant != keys[j].variant {
			return keys[i].variant < keys[j].variant
		}
		return keys[i].window < keys[j].window
	})
	for _, k := range keys {
		values := append([]float64(nil), samples[k]...)
		sort.Float64s(values)
		strategy := 0.0
		if a := realAgg[k]; a != nil {
			strategy = a.profitFactor()
		}
		beaten := 0
		for _, v := range values {
			if v < strategy {
				beaten++
			}
		}
		fmt.Printf("%s\t%s\t%.3f\t%.3f\t%.3f\t%.3f\t%+.3f\t%.0f%%\n",
			k.variant, k.window, strategy, median(values),
			quantile(values, 0.05), quantile(values, 0.95),
			strategy-median(values), float64(beaten)/float64(len(values))*100)
	}
}

// aggregate pools the trades of one replication into a single profit factor,
// weighting every trade by its return on margin exactly as the main table does.
type aggregate struct{ win, loss float64 }

func (a *aggregate) add(r Result) {
	for _, value := range r.Returns {
		if value >= 0 {
			a.win += value
		} else {
			a.loss += -value
		}
	}
}

func (a *aggregate) profitFactor() float64 {
	if a.loss <= 0 {
		return math.Inf(1)
	}
	return a.win / a.loss
}

func quantile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(q * float64(len(sorted)-1))
	return sorted[i]
}
