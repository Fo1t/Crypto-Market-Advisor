package indicators

import (
	"math"
	"testing"
)

// wilderCloses is the reference close series from Wilder's RSI example, the
// same series used by most published RSI worked examples.
var wilderCloses = []float64{
	44.34, 44.09, 44.15, 43.61, 44.33, 44.83, 45.10, 45.42, 45.84, 46.08,
	45.89, 46.03, 45.61, 46.28, 46.28, 46.00, 46.03, 46.41, 46.22, 45.64,
	46.21, 46.25, 45.71, 46.45, 45.78, 45.35, 44.03, 44.18, 44.22, 44.57,
	43.42, 42.66, 43.13,
}

func assertClose(t *testing.T, got, want, tol float64, msg string) {
	t.Helper()
	if math.IsNaN(got) {
		t.Fatalf("%s: got NaN, want %v", msg, want)
	}
	if math.Abs(got-want) > tol {
		t.Fatalf("%s: got %v, want %v (tolerance %v)", msg, got, want, tol)
	}
}

func TestSMA(t *testing.T) {
	values := []float64{1, 2, 3, 4, 5, 6}
	got := SMA(values, 3)

	if !math.IsNaN(got[0]) || !math.IsNaN(got[1]) {
		t.Fatalf("expected leading NaNs, got %v", got[:2])
	}
	assertClose(t, got[2], 2, 1e-9, "sma[2]")
	assertClose(t, got[5], 5, 1e-9, "sma[5]")
}

func TestEMASeededWithSMA(t *testing.T) {
	values := []float64{1, 2, 3, 4, 5}
	got := EMA(values, 3)

	assertClose(t, got[2], 2, 1e-9, "ema seed")
	// k = 0.5; ema[3] = (4-2)*0.5+2 = 3
	assertClose(t, got[3], 3, 1e-9, "ema[3]")
	assertClose(t, got[4], 4, 1e-9, "ema[4]")
}

func TestWMA(t *testing.T) {
	values := []float64{1, 2, 3, 4}
	got := WMA(values, 3)
	// (1*1 + 2*2 + 3*3) / 6 = 14/6
	assertClose(t, got[2], 14.0/6.0, 1e-9, "wma[2]")
	assertClose(t, got[3], (2*1+3*2+4*3)/6.0, 1e-9, "wma[3]")
}

func TestRSIMatchesWilderReference(t *testing.T) {
	rsi := RSI(wilderCloses, 14)

	if !math.IsNaN(rsi[13]) {
		t.Fatalf("rsi must be undefined before index 14, got %v", rsi[13])
	}
	expected := map[int]float64{
		14: 70.53,
		15: 66.32,
		16: 66.55,
		17: 69.41,
		18: 66.36,
		19: 57.97,
		24: 54.68,
		30: 37.30,
		32: 37.77,
	}
	for idx, want := range expected {
		assertClose(t, rsi[idx], want, 0.35, "rsi index")
	}
	for _, v := range rsi[14:] {
		if v < 0 || v > 100 {
			t.Fatalf("rsi out of range: %v", v)
		}
	}
}

func TestRSIAllGainsIs100(t *testing.T) {
	closes := make([]float64, 30)
	for i := range closes {
		closes[i] = float64(100 + i)
	}
	rsi := RSI(closes, 14)
	assertClose(t, rsi[len(rsi)-1], 100, 1e-9, "rsi all gains")
}

func TestTrueRangeAndATR(t *testing.T) {
	highs := []float64{10, 11, 12, 13, 14}
	lows := []float64{9, 10, 11, 12, 13}
	closes := []float64{9.5, 10.5, 11.5, 12.5, 13.5}

	tr := TrueRange(highs, lows, closes)
	assertClose(t, tr[0], 1, 1e-9, "tr[0]")
	// max(11-10, |11-9.5|, |10-9.5|) = 1.5
	assertClose(t, tr[1], 1.5, 1e-9, "tr[1]")

	atr := ATR(highs, lows, closes, 3)
	// seed = mean(tr[0..2]) = (1 + 1.5 + 1.5)/3
	assertClose(t, atr[2], (1+1.5+1.5)/3, 1e-9, "atr seed")
}

func TestMACDStructure(t *testing.T) {
	closes := make([]float64, 100)
	for i := range closes {
		closes[i] = 100 + math.Sin(float64(i)/5)*10
	}
	res := MACD(closes, 12, 26, 9)

	if len(res.MACD) != len(closes) || len(res.Signal) != len(closes) || len(res.Histogram) != len(closes) {
		t.Fatal("MACD series must be aligned with input")
	}
	if !math.IsNaN(res.MACD[24]) {
		t.Fatalf("macd must be undefined before slow EMA is seeded, got %v", res.MACD[24])
	}
	if math.IsNaN(res.MACD[25]) {
		t.Fatal("macd must be defined once the slow EMA is seeded")
	}
	last := len(closes) - 1
	assertClose(t, res.Histogram[last], res.MACD[last]-res.Signal[last], 1e-9, "histogram identity")
}

func TestMACDConstantSeriesIsZero(t *testing.T) {
	closes := make([]float64, 60)
	for i := range closes {
		closes[i] = 42
	}
	res := MACD(closes, 12, 26, 9)
	last := len(closes) - 1
	assertClose(t, res.MACD[last], 0, 1e-9, "macd of constant series")
	assertClose(t, res.Histogram[last], 0, 1e-9, "histogram of constant series")
}

func TestBollingerBands(t *testing.T) {
	closes := []float64{2, 4, 4, 4, 5, 5, 7, 9}
	res := Bollinger(closes, 8, 2)
	last := len(closes) - 1

	// mean = 5, population sd = 2
	assertClose(t, res.Middle[last], 5, 1e-9, "bb middle")
	assertClose(t, res.Upper[last], 9, 1e-9, "bb upper")
	assertClose(t, res.Lower[last], 1, 1e-9, "bb lower")
	assertClose(t, res.PercentB[last], 1, 1e-9, "bb %b")
}

func TestADXTrendingMarketIsStrong(t *testing.T) {
	n := 80
	highs := make([]float64, n)
	lows := make([]float64, n)
	closes := make([]float64, n)
	for i := 0; i < n; i++ {
		base := 100 + float64(i)
		highs[i] = base + 1
		lows[i] = base - 1
		closes[i] = base + 0.5
	}
	res := ADX(highs, lows, closes, 14)

	adx, ok := Last(res.ADX)
	if !ok {
		t.Fatal("ADX must be defined for a long trending series")
	}
	if adx < 40 {
		t.Fatalf("a pure uptrend must produce a high ADX, got %v", adx)
	}
	plus, _ := Last(res.PlusDI)
	minus, _ := Last(res.MinusDI)
	if plus <= minus {
		t.Fatalf("+DI must dominate in an uptrend: +DI=%v -DI=%v", plus, minus)
	}
}

func TestStochasticRange(t *testing.T) {
	highs := []float64{10, 11, 12, 13, 14, 15}
	lows := []float64{5, 6, 7, 8, 9, 10}
	closes := []float64{10, 11, 12, 13, 14, 15}

	k, _ := Stochastic(highs, lows, closes, 5, 1, 3)
	assertClose(t, k[5], 100, 1e-9, "stoch at range high")
}

func TestOBV(t *testing.T) {
	closes := []float64{10, 11, 10, 10, 12}
	volumes := []float64{100, 200, 150, 50, 300}
	obv := OBV(closes, volumes)

	assertClose(t, obv[0], 0, 1e-9, "obv[0]")
	assertClose(t, obv[1], 200, 1e-9, "obv[1]")
	assertClose(t, obv[2], 50, 1e-9, "obv[2]")
	assertClose(t, obv[3], 50, 1e-9, "obv unchanged on flat close")
	assertClose(t, obv[4], 350, 1e-9, "obv[4]")
}

func TestVWAPWeighting(t *testing.T) {
	highs := []float64{10, 20}
	lows := []float64{10, 20}
	closes := []float64{10, 20}
	volumes := []float64{1, 3}

	vwap := RollingVWAP(highs, lows, closes, volumes, 2)
	assertClose(t, vwap[1], (10.0*1+20.0*3)/4.0, 1e-9, "rolling vwap")
}

func TestMFIBounds(t *testing.T) {
	n := 40
	highs := make([]float64, n)
	lows := make([]float64, n)
	closes := make([]float64, n)
	volumes := make([]float64, n)
	for i := 0; i < n; i++ {
		highs[i] = 100 + float64(i) + 1
		lows[i] = 100 + float64(i) - 1
		closes[i] = 100 + float64(i)
		volumes[i] = 1000
	}
	mfi := MFI(highs, lows, closes, volumes, 14)
	v, ok := Last(mfi)
	if !ok {
		t.Fatal("MFI must be defined")
	}
	assertClose(t, v, 100, 1e-9, "mfi with only positive flow")
}

func TestPercentile(t *testing.T) {
	series := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	got := Percentile(series, 5.5, 10)
	assertClose(t, got, 50, 1e-9, "percentile")
}

func TestSlopeDirection(t *testing.T) {
	up := []float64{1, 2, 3, 4, 5}
	if s := Slope(up, 5); s <= 0 {
		t.Fatalf("expected positive slope, got %v", s)
	}
	down := []float64{5, 4, 3, 2, 1}
	if s := Slope(down, 5); s >= 0 {
		t.Fatalf("expected negative slope, got %v", s)
	}
}

func TestIndicatorsHandleShortInput(t *testing.T) {
	short := []float64{1, 2}
	if v, ok := Last(RSI(short, 14)); ok {
		t.Fatalf("RSI must be undefined for short input, got %v", v)
	}
	if v, ok := Last(SMA(short, 50)); ok {
		t.Fatalf("SMA must be undefined for short input, got %v", v)
	}
	if v, ok := Last(ATR(short, short, short, 14)); ok {
		t.Fatalf("ATR must be undefined for short input, got %v", v)
	}
}
