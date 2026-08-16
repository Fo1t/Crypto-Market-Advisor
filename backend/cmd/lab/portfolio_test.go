package main

import (
	"math"
	"testing"
	"time"
)

func at(day int) time.Time {
	return time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, day)
}

// TestPortfolioAcceptsWhatFitsAndCompounds pins the arithmetic: with a free slot
// every trade is taken, each posts the configured share of the equity it finds,
// and the result compounds.
func TestPortfolioAcceptsWhatFitsAndCompounds(t *testing.T) {
	trades := []portfolioTrade{
		{symbol: "A", openedAt: at(0), closedAt: at(1), pnlPct: 100}, // +100% of margin
		{symbol: "B", openedAt: at(2), closedAt: at(3), pnlPct: 100},
	}
	got := composePortfolio(trades, 5, 10, at(0), at(10), 0, 0)

	if got.accepted != 2 || got.rejected != 0 {
		t.Fatalf("both trades fit into five slots: %+v", got)
	}
	// 10000 -> +10% of 10000 = 11000 -> +10% of 11000 = 12100.
	if math.Abs(got.returnPct-21) > 1e-9 {
		t.Fatalf("expected 21%% compounded, got %.4f", got.returnPct)
	}
	if got.wins != 2 || got.maxDrawdown != 0 {
		t.Fatalf("two winners and no drawdown expected: %+v", got)
	}
}

// TestPortfolioRejectsWhenTheBookIsFull covers the constraint the whole exercise
// exists for: a signal that arrives while every slot is taken is not traded.
func TestPortfolioRejectsWhenTheBookIsFull(t *testing.T) {
	trades := []portfolioTrade{
		{symbol: "A", openedAt: at(0), closedAt: at(5), pnlPct: 10},
		{symbol: "B", openedAt: at(1), closedAt: at(2), pnlPct: 10}, // no free slot
		{symbol: "C", openedAt: at(6), closedAt: at(7), pnlPct: 10}, // the first has closed
	}
	got := composePortfolio(trades, 1, 10, at(0), at(10), 0, 0)

	if got.accepted != 2 || got.rejected != 1 {
		t.Fatalf("one trade must be refused for lack of a slot: %+v", got)
	}
	// Exposure: five days for the first trade and one for the third, out of ten.
	if math.Abs(got.exposurePct-60) > 1e-6 {
		t.Fatalf("expected 60%% exposure, got %.4f", got.exposurePct)
	}
	if math.Abs(got.avgOpen-0.6) > 1e-6 {
		t.Fatalf("expected 0.6 positions on average, got %.4f", got.avgOpen)
	}
}

// TestPortfolioSettlesInTimeOrder is the property the composed equity curve
// depends on: a trade opened later but closed earlier must settle first, or the
// drawdown is measured against a curve that never existed.
func TestPortfolioSettlesInTimeOrder(t *testing.T) {
	trades := []portfolioTrade{
		{symbol: "long", openedAt: at(0), closedAt: at(9), pnlPct: 100},
		{symbol: "short", openedAt: at(1), closedAt: at(2), pnlPct: -50},
	}
	got := composePortfolio(trades, 5, 10, at(0), at(10), 0, 0)

	// The loser settles first: 10000 - 50% of 1000 = 9500, a 5% drawdown from the
	// peak. Then the winner adds 100% of the 1000 it posted at the start.
	if math.Abs(got.maxDrawdown-5) > 1e-9 {
		t.Fatalf("expected a 5%% drawdown from settling the loser first, got %.4f", got.maxDrawdown)
	}
	if math.Abs(got.returnPct-5) > 1e-9 {
		t.Fatalf("expected +5%% overall, got %.4f", got.returnPct)
	}
	if math.Abs(got.profitFactor-2) > 1e-9 {
		t.Fatalf("expected a profit factor of 2, got %.4f", got.profitFactor)
	}
}

// TestPortfolioMatchesTheUnweightedSumWhenNothingCompounds is the bridge back to
// the per-symbol table: with a tiny allocation the compounding term vanishes and
// the composed return has to agree with the plain sum of the trade returns.
func TestPortfolioMatchesTheUnweightedSumWhenNothingCompounds(t *testing.T) {
	var trades []portfolioTrade
	sum := 0.0
	for i := 0; i < 50; i++ {
		pnl := float64(i%7) - 3 // a mix of winners and losers
		trades = append(trades, portfolioTrade{
			symbol: "S", openedAt: at(i), closedAt: at(i).Add(time.Hour), pnlPct: pnl,
		})
		sum += pnl
	}
	const allocation = 0.01
	got := composePortfolio(trades, 50, allocation, at(0), at(60), 0, 0)

	want := sum * allocation / 100
	if math.Abs(got.returnPct-want) > 0.01 {
		t.Fatalf("composed %.4f%% but the trades sum to %.4f%%", got.returnPct, want)
	}
}

// TestEquityGuardSuspendsWhileTheAccountIsBelowItsAverage covers the rule that
// judges the strategy rather than the market: after a loss deep enough to pull
// the account under its own recent average, the next entry is refused.
func TestEquityGuardSuspendsWhileTheAccountIsBelowItsAverage(t *testing.T) {
	trades := []portfolioTrade{
		// A long quiet stretch establishes the average.
		{symbol: "A", openedAt: at(0), closedAt: at(1), pnlPct: 0, allocationPct: 10},
		// Then a heavy loss, far enough in that the window is fully populated.
		{symbol: "B", openedAt: at(60), closedAt: at(61), pnlPct: -90, allocationPct: 10},
		// The next signal arrives while the account is still below its average.
		{symbol: "C", openedAt: at(65), closedAt: at(66), pnlPct: 50, allocationPct: 10},
	}

	free := composePortfolio(trades, 5, 0, at(0), at(120), 0, 0)
	if free.accepted != 3 || free.suspended != 0 {
		t.Fatalf("without the guard every trade is taken: %+v", free)
	}

	guarded := composePortfolio(trades, 5, 0, at(0), at(120), 0, 30*24*time.Hour)
	if guarded.suspended != 1 || guarded.accepted != 2 {
		t.Fatalf("the entry after the loss must be suspended: %+v", guarded)
	}
	// Suspension is counted as a refusal, not as a trade that never existed.
	if guarded.accepted+guarded.rejected != len(trades) {
		t.Fatalf("every signal must be accounted for: %+v", guarded)
	}
}

// TestEquityGuardNeedsHistoryBeforeItJudges keeps the filter from blocking the
// first trades of a run, when there is no curve to compare against yet.
func TestEquityGuardNeedsHistoryBeforeItJudges(t *testing.T) {
	trades := []portfolioTrade{
		{symbol: "A", openedAt: at(1), closedAt: at(2), pnlPct: -50, allocationPct: 10},
		{symbol: "B", openedAt: at(3), closedAt: at(4), pnlPct: -50, allocationPct: 10},
	}
	got := composePortfolio(trades, 5, 0, at(0), at(60), 0, 30*24*time.Hour)
	if got.suspended != 0 || got.accepted != 2 {
		t.Fatalf("with less history than the window the guard must stay quiet: %+v", got)
	}
}
