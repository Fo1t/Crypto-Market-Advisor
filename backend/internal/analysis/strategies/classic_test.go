package strategies

import (
	"testing"
	"time"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

// synthetic builds a candle series from closing prices, with a small range
// around each close so the true range is defined.
func synthetic(closes []float64) []domain.Candle {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	out := make([]domain.Candle, 0, len(closes))
	previous := closes[0]
	for i, close := range closes {
		high, low := max(previous, close)*1.001, min(previous, close)*0.999
		out = append(out, domain.Candle{
			OpenTime:  start.Add(time.Duration(i) * time.Hour),
			CloseTime: start.Add(time.Duration(i+1) * time.Hour),
			Open:      previous, High: high, Low: low, Close: close, Closed: true,
		})
		previous = close
	}
	return out
}

func TestDonchianBreakoutFollowsTheChannel(t *testing.T) {
	flat := make([]float64, 60)
	for i := range flat {
		flat[i] = 100
	}
	// A close above every high of the preceding window is a breakout.
	up := donchianBreakout(Input{Candles: synthetic(append(append([]float64{}, flat...), 105))})
	if up.direction != domain.PatternBullish || up.strength < 0.7 {
		t.Fatalf("a break above the channel must vote long: %+v", up)
	}
	down := donchianBreakout(Input{Candles: synthetic(append(append([]float64{}, flat...), 95))})
	if down.direction != domain.PatternBearish {
		t.Fatalf("a break below the channel must vote short: %+v", down)
	}
	inside := donchianBreakout(Input{Candles: synthetic(append(append([]float64{}, flat...), 100))})
	if inside.direction != "" {
		t.Fatalf("price inside the channel is not a breakout: %+v", inside)
	}
}

func TestSuperTrendFollowsTheDirection(t *testing.T) {
	rising := make([]float64, 60)
	falling := make([]float64, 60)
	for i := range rising {
		rising[i] = 100 + float64(i)
		falling[i] = 160 - float64(i)
	}
	up := superTrend(Input{Candles: synthetic(rising)})
	if up.direction != domain.PatternBullish {
		t.Fatalf("a rising series must read as an uptrend: %+v", up)
	}
	down := superTrend(Input{Candles: synthetic(falling)})
	if down.direction != domain.PatternBearish {
		t.Fatalf("a falling series must read as a downtrend: %+v", down)
	}
}

func TestRSI2OnlyTradesWithTheLongTrend(t *testing.T) {
	// A long uptrend, then a sharp three-bar dip: the published rule buys that.
	closes := make([]float64, 0, 260)
	for i := 0; i < 250; i++ {
		closes = append(closes, 100+float64(i)*0.5)
	}
	closes = append(closes, closes[len(closes)-1]*0.97, closes[len(closes)-1]*0.95, closes[len(closes)-1]*0.94)
	got := rsi2Reversion(Input{Candles: synthetic(closes)})
	if got.direction != domain.PatternBullish {
		t.Fatalf("an oversold dip above the long average must vote long: %+v", got)
	}

	// The same dip below the long average is explicitly not a trade.
	down := make([]float64, 0, 260)
	for i := 0; i < 250; i++ {
		down = append(down, 200-float64(i)*0.5)
	}
	down = append(down, down[len(down)-1]*0.97, down[len(down)-1]*0.95, down[len(down)-1]*0.94)
	if got := rsi2Reversion(Input{Candles: synthetic(down)}); got.direction == domain.PatternBullish {
		t.Fatalf("the rule must not buy a dip below the long average: %+v", got)
	}
}
