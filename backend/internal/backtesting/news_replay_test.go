package backtesting

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/crypto-market-advisor/advisor/internal/analysis/features"
	"github.com/crypto-market-advisor/advisor/internal/domain"
)

// recordingNews answers with a critical event from criticalFrom onwards and
// remembers every moment it was asked about.
type recordingNews struct {
	criticalFrom time.Time
	asked        []time.Time
}

func (r *recordingNews) NewsContextAt(_ context.Context, at time.Time) domain.NewsSnapshot {
	r.asked = append(r.asked, at)
	snapshot := domain.NewsSnapshot{
		Status: domain.NewsContextAvailableButEmpty, LookbackHours: 24,
		AssetSpecific: []domain.NewsSnapshotItem{}, Global: []domain.NewsSnapshotItem{},
	}
	if at.Before(r.criticalFrom) {
		return snapshot
	}
	snapshot.Status = domain.NewsContextOK
	snapshot.AssetSpecific = []domain.NewsSnapshotItem{{
		ClusterID: uuid.New(), AgeMinutes: 10, Title: "exchange halt",
		Critical: true, Importance: 1, Freshness: 1, SourceCount: 3,
	}}
	return snapshot
}

// TestReplayedNewsIsBoundedByTheBar is the look-ahead guard for the news
// context: a decision may only ever be asked about the bar it is standing on.
func TestReplayedNewsIsBoundedByTheBar(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	candles := series(400, 21, start)
	run := testRun(start.Add(260*time.Hour), start.Add(400*time.Hour))

	feed := &recordingNews{criticalFrom: start.Add(10000 * time.Hour)} // never critical
	engine := testEngine()
	if _, _, _, err := engine.simulateSeries(context.Background(), run, SimulationInputs{
		Series: map[domain.Timeframe][]domain.Candle{run.Timeframe: candles},
		News:   feed,
	}, nil); err != nil {
		t.Fatalf("simulation failed: %v", err)
	}

	if len(feed.asked) == 0 {
		t.Fatal("the replay must ask for the news context of its decision points")
	}
	closeTimes := make(map[time.Time]bool, len(candles))
	for _, candle := range candles {
		closeTimes[candle.CloseTime] = true
	}
	for _, at := range feed.asked {
		if !closeTimes[at] {
			t.Fatalf("news was requested for %s, which is not a replayed bar close", at)
		}
		if at.After(run.Params.DateTo) {
			t.Fatalf("news was requested past the end of the run: %s", at)
		}
	}
}

// TestCriticalNewsVetoesADecisionInReplay proves the wiring reaches the policy:
// a bar the rules had an ordinary verdict on becomes a hard veto once the
// replayed news context carries a fresh critical event.
func TestCriticalNewsVetoesADecisionInReplay(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	candles := series(400, 99, start)
	run := testRun(start.Add(300*time.Hour), start.Add(400*time.Hour))
	engine := testEngine()

	quiet := &recordingNews{criticalFrom: start.Add(10000 * time.Hour)}
	loud := &recordingNews{criticalFrom: start}

	checked := 0
	for i := features.MinCandles; i < len(candles); i += 17 {
		visible := map[domain.Timeframe][]domain.Candle{run.Timeframe: candles[:i]}

		before, err := engine.signal(context.Background(), run, run.Timeframe, visible, signalContext{news: quiet})
		if err != nil {
			t.Fatalf("quiet signal failed: %v", err)
		}
		// Only bars the policy actually scored can show the difference: a
		// disabled side never reaches the filters at all.
		if before.reason != domain.StrategyReasonBelowMinSignal {
			continue
		}

		after, err := engine.signal(context.Background(), run, run.Timeframe, visible, signalContext{news: loud})
		if err != nil {
			t.Fatalf("loud signal failed: %v", err)
		}
		if after.reason != domain.StrategyReasonVetoed {
			t.Fatalf("a fresh critical event must veto the bar, got %q (was %q)", after.reason, before.reason)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no bar was left for the news filter to change")
	}
}

// recordingHistory answers with empty context and remembers what it was asked.
type recordingHistory struct {
	asked []time.Time
}

func (h *recordingHistory) PerformanceAt(_ context.Context, _ string, at time.Time) (domain.HistoricalPerformance, error) {
	h.asked = append(h.asked, at)
	return domain.HistoricalPerformance{SampleSize: 7}, nil
}

func (h *recordingHistory) SimilarCases(_ context.Context, _ []float64, _ string, _ int, before time.Time) ([]domain.SimilarCase, error) {
	h.asked = append(h.asked, before)
	return []domain.SimilarCase{{Similarity: 0.9}}, nil
}

// TestReplayedHistoryIsBoundedByTheBar keeps the retrieval context on the right
// side of the replayed moment: the track record a decision reads must be the one
// that existed then, never the finished record of the whole period.
func TestReplayedHistoryIsBoundedByTheBar(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	candles := series(400, 31, start)
	run := llmRun(start.Add(300*time.Hour), start.Add(400*time.Hour), nil)

	feed := &recordingHistory{}
	engine := llmTestEngine(t, llmNoAction)
	if _, _, _, err := engine.simulateSeries(context.Background(), run, SimulationInputs{
		Series:  map[domain.Timeframe][]domain.Candle{run.Timeframe: candles},
		History: feed,
	}, nil); err != nil {
		t.Fatalf("simulation failed: %v", err)
	}

	if len(feed.asked) == 0 {
		t.Fatal("an LLM replay must read the retrieval context of its decision points")
	}
	closeTimes := make(map[time.Time]bool, len(candles))
	for _, candle := range candles {
		closeTimes[candle.CloseTime] = true
	}
	for _, at := range feed.asked {
		if !closeTimes[at] {
			t.Fatalf("history was requested for %s, which is not a replayed bar close", at)
		}
	}
}

// TestTechnicalReplaySkipsRetrieval keeps the deterministic baseline free of
// work it never reads: the rules do not look at the track record, and asking the
// database for it on every bar would only slow the sweep down.
func TestTechnicalReplaySkipsRetrieval(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	candles := series(400, 32, start)
	run := testRun(start.Add(300*time.Hour), start.Add(400*time.Hour))

	feed := &recordingHistory{}
	if _, _, _, err := testEngine().simulateSeries(context.Background(), run, SimulationInputs{
		Series:  map[domain.Timeframe][]domain.Candle{run.Timeframe: candles},
		History: feed,
	}, nil); err != nil {
		t.Fatalf("simulation failed: %v", err)
	}
	if len(feed.asked) != 0 {
		t.Fatalf("a technical replay must not read retrieval context, asked %d times", len(feed.asked))
	}
}
