package features

import (
	"encoding/json"
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

// syntheticSeries builds a deterministic but non-trivial candle series.
func syntheticSeries(n int, seed int64) []domain.Candle {
	rng := rand.New(rand.NewSource(seed))
	base := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	price := 100.0

	out := make([]domain.Candle, 0, n)
	for i := 0; i < n; i++ {
		drift := math.Sin(float64(i)/17) * 0.8
		noise := (rng.Float64() - 0.5) * 1.2
		open := price
		close := open + drift + noise
		high := math.Max(open, close) + rng.Float64()*0.5
		low := math.Min(open, close) - rng.Float64()*0.5
		out = append(out, domain.Candle{
			OpenTime:  base.Add(time.Duration(i) * time.Hour),
			CloseTime: base.Add(time.Duration(i+1) * time.Hour),
			Open:      open, High: high, Low: low, Close: close,
			Volume: 1000 + rng.Float64()*500,
			Closed: true,
			Source: domain.CandleSourceNative,
		})
		price = close
	}
	return out
}

// TestNoLookAheadInTimeframeAnalysis is the core leakage regression test:
// analysing the first k candles must produce exactly the same result whether or
// not the caller happens to hold later candles in memory.
func TestNoLookAheadInTimeframeAnalysis(t *testing.T) {
	full := syntheticSeries(400, 42)

	for _, cut := range []int{120, 200, 333} {
		prefix := full[:cut]

		// A defensive copy guards against a detector mutating its input, which
		// would be another way for future data to leak.
		prefixCopy := make([]domain.Candle, cut)
		copy(prefixCopy, prefix)

		a := AnalyzeTimeframe(domain.TF1h, prefixCopy)
		b := AnalyzeTimeframe(domain.TF1h, full[:cut])

		gotA, err := json.Marshal(a)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		gotB, err := json.Marshal(b)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if string(gotA) != string(gotB) {
			t.Fatalf("analysis at cut %d is not reproducible", cut)
		}

		// The analysis must never reference a candle beyond the cut.
		if a.LastClosedCandle.After(prefix[cut-1].CloseTime) {
			t.Fatalf("analysis at cut %d references a candle after the cut", cut)
		}
		for _, p := range a.Patterns {
			if p.CandleIndex > 0 {
				t.Fatalf("pattern %s points into the future (index %d)", p.Name, p.CandleIndex)
			}
		}
		for _, p := range a.ChartPatterns {
			if p.CandleIndex > 0 {
				t.Fatalf("chart pattern %s points into the future (index %d)", p.Name, p.CandleIndex)
			}
		}
		for _, s := range a.Structure.Swings {
			if s.Index+s.ConfirmedAfter > cut-1 {
				t.Fatalf("swing at %d was reported before it could be confirmed", s.Index)
			}
		}
	}
}

// TestAppendingCandlesDoesNotRewritePast verifies that adding new candles never
// changes what the analysis said about an earlier point in time.
func TestAppendingCandlesDoesNotRewritePast(t *testing.T) {
	full := syntheticSeries(300, 7)
	cut := 200

	before := AnalyzeTimeframe(domain.TF1h, full[:cut])
	_ = AnalyzeTimeframe(domain.TF1h, full) // simulate the later cycle
	after := AnalyzeTimeframe(domain.TF1h, full[:cut])

	x, _ := json.Marshal(before)
	y, _ := json.Marshal(after)
	if string(x) != string(y) {
		t.Fatal("analysis of a fixed window changed after a later analysis ran")
	}
}

func TestAnalyzeTimeframeHandlesShortSeries(t *testing.T) {
	short := syntheticSeries(10, 1)
	res := AnalyzeTimeframe(domain.TF5m, short)

	if res.Structure.State != domain.StructureUncertain {
		t.Fatalf("expected uncertain structure on short input, got %s", res.Structure.State)
	}
	if res.Regime.Primary != domain.RegimeUncertain {
		t.Fatalf("expected uncertain regime on short input, got %s", res.Regime.Primary)
	}
	if res.CandlesUsed != len(short) {
		t.Fatalf("candles used mismatch: %d", res.CandlesUsed)
	}
}

func TestBuildSnapshotAggregatesTimeframes(t *testing.T) {
	series := syntheticSeries(300, 11)
	tfs := map[domain.Timeframe]domain.TimeframeAnalysis{
		domain.TF15m: AnalyzeTimeframe(domain.TF15m, series),
		domain.TF1h:  AnalyzeTimeframe(domain.TF1h, series),
		domain.TF4h:  AnalyzeTimeframe(domain.TF4h, series),
	}
	snapshot := BuildSnapshot(SnapshotInput{
		Symbol:      "BTC",
		Price:       series[len(series)-1].Close,
		Timeframes:  tfs,
		Now:         time.Now(),
		DataQuality: domain.DataQuality{Status: domain.DataQualityOK},
	})

	if snapshot.SchemaVersion != domain.SchemaVersion {
		t.Fatalf("unexpected schema version %d", snapshot.SchemaVersion)
	}
	total := len(snapshot.TrendAlignment.Bullish) + len(snapshot.TrendAlignment.Bearish) + len(snapshot.TrendAlignment.Neutral)
	if total != len(tfs) {
		t.Fatalf("every timeframe must be classified, got %d of %d", total, len(tfs))
	}
	if snapshot.TrendAlignment.AlignmentScore < -1 || snapshot.TrendAlignment.AlignmentScore > 1 {
		t.Fatalf("alignment score out of range: %v", snapshot.TrendAlignment.AlignmentScore)
	}
	if snapshot.LatestClosedCandle.IsZero() {
		t.Fatal("latest closed candle must be set")
	}
}

func TestFeatureVectorIsStableAndBounded(t *testing.T) {
	series := syntheticSeries(300, 5)
	snapshot := BuildSnapshot(SnapshotInput{
		Symbol: "BTC",
		Price:  series[len(series)-1].Close,
		Timeframes: map[domain.Timeframe]domain.TimeframeAnalysis{
			domain.TF1h: AnalyzeTimeframe(domain.TF1h, series),
		},
		Now: time.Now(),
	})

	v1 := FeatureVector(snapshot)
	v2 := FeatureVector(snapshot)
	if len(v1) != len(v2) {
		t.Fatal("feature vector length must be deterministic")
	}
	for i := range v1 {
		if v1[i] != v2[i] {
			t.Fatalf("feature vector is not deterministic at %d", i)
		}
		if math.IsNaN(v1[i]) || math.IsInf(v1[i], 0) {
			t.Fatalf("feature vector contains a non-finite value at %d", i)
		}
		if v1[i] < -1 || v1[i] > 1 {
			t.Fatalf("feature vector value out of range at %d: %v", i, v1[i])
		}
	}
	if got := Similarity(v1, v2); got < 0.999 {
		t.Fatalf("identical vectors must be maximally similar, got %v", got)
	}
}
