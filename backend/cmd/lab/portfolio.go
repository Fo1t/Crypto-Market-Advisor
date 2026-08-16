package main

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// A per-symbol backtest answers "is this trade any good". It cannot answer
// "would this have made money", because it gives every symbol its own capital
// and lets all fifteen of them trade at once with no shared limit. Composing the
// same trades into one account with a bounded number of slots is what makes the
// result comparable to holding the market, and it is the measurement any asset
// selection rule needs: selecting from a universe is meaningless while every
// candidate is traded anyway.
//
// The composition is an approximation in one respect, stated here rather than
// hidden: a trade the portfolio refuses for lack of a free slot still occupied
// its symbol in the per-symbol replay, so the next trade of that symbol keeps
// its original timing instead of starting earlier. The distortion is bounded by
// how often the book is full, which the report prints.

// portfolioTrade is one simulated trade as the composer needs it.
type portfolioTrade struct {
	symbol   string
	openedAt time.Time
	closedAt time.Time
	// pnlPct is the net return on the margin the trade posted, which is
	// independent of account size and therefore composable.
	pnlPct float64
	// allocationPct is the share of equity this trade posted as margin, as the
	// risk engine decided it. Risk-based sizing only becomes visible in the
	// composed account when each trade brings its own share rather than all of
	// them sharing one constant.
	allocationPct float64
}

// equityGuard suspends new entries while the account is below its own recent
// average.
//
// Every filter so far judges the market. This one judges the strategy: a system
// that is losing is either in a regime it does not suit or is simply wrong at
// the moment, and in both cases the cheapest response is to stop adding
// exposure until its own curve recovers. It has no per-asset threshold to tune,
// which is exactly what the cross-sectional attempt failed on.
type equityGuard struct {
	window  time.Duration
	samples []equitySample
}

type equitySample struct {
	at     time.Time
	equity float64
}

// record marks the account value from this moment on.
func (g *equityGuard) record(at time.Time, equity float64) {
	if g == nil || g.window <= 0 {
		return
	}
	g.samples = append(g.samples, equitySample{at: at, equity: equity})
}

// allows reports whether a new position may be opened.
//
// The comparison is against the time-weighted average of the account over the
// window, not the average of the samples: settlements arrive in clusters, and a
// plain mean would let a busy afternoon outvote a quiet fortnight.
func (g *equityGuard) allows(at time.Time, equity float64) bool {
	if g == nil || g.window <= 0 || len(g.samples) == 0 {
		return true
	}
	from := at.Add(-g.window)
	if g.samples[0].at.After(from) {
		// Not enough history to judge the strategy yet.
		return true
	}

	var area, span float64
	previous := from
	value := g.samples[0].equity
	for _, sample := range g.samples {
		if !sample.at.After(from) {
			value = sample.equity
			continue
		}
		if sample.at.After(at) {
			break
		}
		elapsed := sample.at.Sub(previous).Seconds()
		area += value * elapsed
		span += elapsed
		previous, value = sample.at, sample.equity
	}
	if elapsed := at.Sub(previous).Seconds(); elapsed > 0 {
		area += value * elapsed
		span += elapsed
	}
	if span <= 0 {
		return true
	}
	return equity >= area/span
}

// portfolioResult is one composed account.
type portfolioResult struct {
	variant  string
	window   string
	slots    int
	accepted int
	rejected int
	// suspended counts entries refused because the account itself was below its
	// own average, as opposed to refused for lack of a free slot.
	suspended int
	wins      int

	returnPct    float64
	maxDrawdown  float64
	profitFactor float64
	// exposurePct is the share of the window during which at least one position
	// was open, and avgOpen the average number of positions held.
	exposurePct float64
	avgOpen     float64
	buyHoldPct  float64
}

// composePortfolio replays the given trades into a single account.
//
// allocationPct is the share of current equity posted as margin per position.
// A value at or below zero means every trade uses the share the risk engine
// gave it, which is what measuring a sizing rule requires; a positive value
// overrides them all with one constant, which is what comparing two fixed sizes
// requires.
func composePortfolio(trades []portfolioTrade, slots int, allocationPct float64, from, to time.Time, buyHold float64, guardWindow time.Duration) portfolioResult {
	out := portfolioResult{slots: slots, buyHoldPct: buyHold}
	if len(trades) == 0 || slots <= 0 {
		return out
	}

	ordered := append([]portfolioTrade(nil), trades...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].openedAt.Equal(ordered[j].openedAt) {
			return ordered[i].symbol < ordered[j].symbol
		}
		return ordered[i].openedAt.Before(ordered[j].openedAt)
	})

	const startingEquity = 10000.0
	equity, peak := startingEquity, startingEquity
	guard := &equityGuard{window: guardWindow}
	guard.record(from, equity)
	var grossWin, grossLoss float64

	// open holds the accepted positions that have not been closed yet, together
	// with the money each one will settle for.
	type openPosition struct {
		closesAt time.Time
		result   float64
	}
	var open []openPosition

	// exposure is accumulated as position-time so that both the share of the
	// window spent in the market and the average book size come out of one pass.
	var positionSeconds float64
	var busySeconds float64
	lastEvent := from

	settleUntil := func(at time.Time) {
		for {
			next := -1
			for i, position := range open {
				if !position.closesAt.After(at) && (next < 0 || position.closesAt.Before(open[next].closesAt)) {
					next = i
				}
			}
			if next < 0 {
				return
			}
			closing := open[next]
			elapsed := closing.closesAt.Sub(lastEvent).Seconds()
			if elapsed > 0 {
				positionSeconds += elapsed * float64(len(open))
				busySeconds += elapsed
			}
			lastEvent = closing.closesAt

			equity += closing.result
			guard.record(closing.closesAt, equity)
			if closing.result >= 0 {
				grossWin += closing.result
				out.wins++
			} else {
				grossLoss += -closing.result
			}
			if equity > peak {
				peak = equity
			} else if peak > 0 {
				if dd := (peak - equity) / peak * 100; dd > out.maxDrawdown {
					out.maxDrawdown = dd
				}
			}
			open = append(open[:next], open[next+1:]...)
		}
	}

	for _, trade := range ordered {
		settleUntil(trade.openedAt)

		elapsed := trade.openedAt.Sub(lastEvent).Seconds()
		if elapsed > 0 {
			positionSeconds += elapsed * float64(len(open))
			if len(open) > 0 {
				busySeconds += elapsed
			}
		}
		lastEvent = trade.openedAt

		if len(open) >= slots {
			out.rejected++
			continue
		}
		if !guard.allows(trade.openedAt, equity) {
			out.rejected++
			out.suspended++
			continue
		}
		share := allocationPct
		if share <= 0 {
			share = trade.allocationPct
		}
		margin := equity * share / 100
		if margin <= 0 {
			out.rejected++
			continue
		}
		out.accepted++
		open = append(open, openPosition{
			closesAt: trade.closedAt,
			result:   margin * trade.pnlPct / 100,
		})
	}
	settleUntil(to)

	// Anything still open at the end settles at its own close time, which may lie
	// past the window when the last trade ran over the edge.
	for len(open) > 0 {
		latest := open[0].closesAt
		for _, position := range open {
			if position.closesAt.After(latest) {
				latest = position.closesAt
			}
		}
		settleUntil(latest)
	}

	out.returnPct = (equity - startingEquity) / startingEquity * 100
	if grossLoss > 0 {
		out.profitFactor = grossWin / grossLoss
	} else if grossWin > 0 {
		out.profitFactor = math.Inf(1)
	}

	span := to.Sub(from).Seconds()
	if span > 0 {
		out.exposurePct = busySeconds / span * 100
		out.avgOpen = positionSeconds / span
	}
	return out
}

// reportPortfolio composes one account per variant and window and prints the
// comparison against holding the same universe.
func reportPortfolio(results []Result, variants []Variant, windows []window, slots int, allocationPct float64, guardWindow time.Duration) {
	fmt.Println()
	sizing := fmt.Sprintf("%.1f%% of equity per position", allocationPct)
	if allocationPct <= 0 {
		sizing = "size from the risk engine"
	}
	guard := "no equity filter"
	if guardWindow > 0 {
		guard = fmt.Sprintf("entries suspended below the %.0f-day average of the account", guardWindow.Hours()/24)
	}
	fmt.Printf("portfolio: %d slots, %s, %s\n", slots, sizing, guard)
	fmt.Println("variant\twindow\ttaken/seen\tsuspended\twin%\tPF\treturn%\tmaxDD%\treturn/DD\texposure%\tavgOpen\tbuyHold%")

	for _, variant := range variants {
		for _, w := range windows {
			var trades []portfolioTrade
			var holds []float64
			for _, r := range results {
				if r.Variant != variant.Name || r.Window != w.name {
					continue
				}
				trades = append(trades, r.Trades...)
				holds = append(holds, r.BuyHoldPct)
			}
			if len(trades) == 0 {
				continue
			}
			composed := composePortfolio(trades, slots, allocationPct, w.from, w.to, mean(holds), guardWindow)
			composed.variant, composed.window = variant.Name, w.name
			printPortfolio(composed)
		}
	}
}

func printPortfolio(p portfolioResult) {
	ratio := 0.0
	if p.maxDrawdown > 0 {
		ratio = p.returnPct / p.maxDrawdown
	}
	winRate := 0.0
	if p.accepted > 0 {
		winRate = float64(p.wins) / float64(p.accepted) * 100
	}
	fmt.Printf("%s\t%s\t%d/%d\t%d\t%.1f\t%.2f\t%.2f\t%.2f\t%.2f\t%.1f\t%.2f\t%.2f\n",
		p.variant, p.window, p.accepted, p.accepted+p.rejected, p.suspended, winRate, p.profitFactor,
		p.returnPct, p.maxDrawdown, ratio, p.exposurePct, p.avgOpen, p.buyHoldPct)
}
