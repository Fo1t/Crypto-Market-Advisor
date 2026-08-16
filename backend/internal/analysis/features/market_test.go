package features

import (
	"testing"
	"time"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

func dailySeries(n int, start, step float64) []domain.Candle {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	out := make([]domain.Candle, 0, n)
	price := start
	for i := 0; i < n; i++ {
		out = append(out, domain.Candle{
			OpenTime:  base.AddDate(0, 0, i),
			CloseTime: base.AddDate(0, 0, i+1),
			Open:      price, High: price, Low: price, Close: price,
			Closed: true,
		})
		price += step
	}
	return out
}

// TestMarketContextNeedsEnoughHistory covers the rule that matters most: a
// short series produces an unknown state rather than a guess, because the
// filter that reads it lets an unknown context through and would otherwise act
// on an average computed from almost nothing.
func TestMarketContextNeedsEnoughHistory(t *testing.T) {
	short := MarketContextFrom("BTC", dailySeries(MarketContextEMAPeriod-1, 100, 1))
	if short.Known() {
		t.Fatalf("a series shorter than the average must stay unknown, got %+v", short)
	}
	if got := MarketContextFrom("BTC", nil); got.Known() {
		t.Fatalf("no candles must stay unknown, got %+v", got)
	}
}

// TestMarketContextClassifiesTheBenchmark checks both directions and the band
// around the average where neither side is claimed.
func TestMarketContextClassifiesTheBenchmark(t *testing.T) {
	rising := MarketContextFrom("BTC", dailySeries(400, 100, 1))
	if rising.Trend != domain.MarketTrendUp {
		t.Fatalf("a rising series must read as an uptrend, got %+v", rising)
	}
	if rising.PriceVsEMA200Pct == nil || *rising.PriceVsEMA200Pct <= MarketContextBandPct {
		t.Fatalf("the distance must be reported and outside the band: %+v", rising.PriceVsEMA200Pct)
	}

	falling := MarketContextFrom("BTC", dailySeries(400, 500, -1))
	if falling.Trend != domain.MarketTrendDown {
		t.Fatalf("a falling series must read as a downtrend, got %+v", falling)
	}

	flat := MarketContextFrom("BTC", dailySeries(400, 100, 0))
	if flat.Trend != domain.MarketTrendFlat {
		t.Fatalf("a flat series must claim no side, got %+v", flat)
	}
	if flat.AsOf.IsZero() {
		t.Fatal("the state must say which candle it was read from")
	}
}

// TestRelativeStrengthIsRiskAdjusted covers the reason the score divides by the
// asset's own volatility: without it the ranking is a ranking of volatility, and
// the loudest asset always wins.
func TestRelativeStrengthIsRiskAdjusted(t *testing.T) {
	steady := dailySeries(200, 100, 1) // +1 a day, no noise
	if _, ok := RelativeStrength(steady[:RelativeStrengthLookback-1]); ok {
		t.Fatal("a series shorter than the lookback cannot be scored")
	}
	calm, ok := RelativeStrength(steady)
	if !ok {
		t.Fatal("a long enough series must produce a score")
	}

	// The same total move, delivered in a much noisier way, has to score lower.
	noisy := dailySeries(200, 100, 1)
	for i := range noisy {
		if i%2 == 1 {
			noisy[i].Close += 15
		}
	}
	shaky, ok := RelativeStrength(noisy)
	if !ok {
		t.Fatal("the noisy series must also produce a score")
	}
	if shaky >= calm {
		t.Fatalf("a noisier path to the same place must score lower: %.3f vs %.3f", shaky, calm)
	}

	// A flat series has no risk-adjusted move to report at all.
	if _, ok := RelativeStrength(dailySeries(200, 100, 0)); ok {
		t.Fatal("a flat series has no deviation to divide by")
	}
}

// TestRankUniverseOrdersAndBounds pins the percentile mapping the filter reads.
func TestRankUniverseOrdersAndBounds(t *testing.T) {
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	ranks := RankUniverse(map[string]float64{"WEAK": -1, "MID": 0.5, "STRONG": 3}, now)

	if ranks["STRONG"].RankPct != 100 || ranks["WEAK"].RankPct != 0 {
		t.Fatalf("the extremes must map to 100 and 0: %+v", ranks)
	}
	if ranks["MID"].RankPct <= 0 || ranks["MID"].RankPct >= 100 {
		t.Fatalf("the middle must land strictly between them: %+v", ranks["MID"])
	}
	for symbol, context := range ranks {
		if context.Members != 3 || !context.Known() {
			t.Fatalf("%s must know how many peers it was compared against: %+v", symbol, context)
		}
	}
	// One asset has no peers, so there is nothing to rank it against.
	if got := RankUniverse(map[string]float64{"ONLY": 1}, now); len(got) != 0 {
		t.Fatalf("a universe of one must produce no ranking, got %+v", got)
	}
}

// TestDailyRankerIgnoresTheFuture is the look-ahead guard for the cross-
// sectional view: a rank at a moment may only use closes that had happened.
func TestDailyRankerIgnoresTheFuture(t *testing.T) {
	rising := dailySeries(200, 100, 1)
	falling := dailySeries(200, 300, -1)
	// After the moment under test the two swap places entirely.
	for i := 150; i < 200; i++ {
		rising[i].Close = rising[149].Close - float64(i-149)*5
		falling[i].Close = falling[149].Close + float64(i-149)*5
	}

	ranker := NewDailyRanker(map[string][]domain.Candle{"UP": rising, "DOWN": falling})
	at := rising[149].CloseTime

	up, down := ranker.RankAt("UP", at), ranker.RankAt("DOWN", at)
	if up.RankPct <= down.RankPct {
		t.Fatalf("at %v the rising asset must rank above the falling one: %.0f vs %.0f", at, up.RankPct, down.RankPct)
	}
	if !up.AsOf.Equal(at) {
		t.Fatalf("the rank must be dated by the close it was read from, got %v", up.AsOf)
	}
	// Before any close there is nothing to rank.
	if got := ranker.RankAt("UP", rising[0].CloseTime.Add(-time.Hour)); got.Known() {
		t.Fatalf("no closed candle means no ranking, got %+v", got)
	}
}
