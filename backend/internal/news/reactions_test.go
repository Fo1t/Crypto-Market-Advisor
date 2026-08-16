package news

import (
	"context"
	"io"
	"log/slog"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/crypto-market-advisor/advisor/internal/config"
	"github.com/crypto-market-advisor/advisor/internal/domain"
)

type reactionQuery struct {
	At      time.Time
	KnownAt time.Time
}

type fakeReactionStore struct {
	work    []domain.NewsReactionWork
	candles []domain.Candle
	queries []reactionQuery
	saved   []domain.NewsMarketReaction
}

func (f *fakeReactionStore) ListDueReactions(_ context.Context, _ time.Time, _ int) ([]domain.NewsReactionWork, error) {
	return f.work, nil
}

func (f *fakeReactionStore) FirstClosed5mCandle(_ context.Context, _ int64, at, knownAt time.Time) (domain.Candle, bool, error) {
	f.queries = append(f.queries, reactionQuery{At: at, KnownAt: knownAt})
	for _, candle := range f.candles {
		if candle.Closed && !candle.CloseTime.Before(at) && !candle.CloseTime.After(knownAt) {
			return candle, true, nil
		}
	}
	return domain.Candle{}, false, nil
}

func (f *fakeReactionStore) ReactionExtremes(_ context.Context, _ int64, baselineTime, knownAt time.Time) (*float64, *float64, *time.Time, error) {
	var maxHigh, minLow *float64
	var through *time.Time
	for _, candle := range f.candles {
		if !candle.Closed || !candle.CloseTime.After(baselineTime) || candle.CloseTime.After(knownAt) {
			continue
		}
		if maxHigh == nil || candle.High > *maxHigh {
			value := candle.High
			maxHigh = &value
		}
		if minLow == nil || candle.Low < *minLow {
			value := candle.Low
			minLow = &value
		}
		value := candle.CloseTime
		through = &value
	}
	return maxHigh, minLow, through, nil
}

func (f *fakeReactionStore) UpsertReaction(_ context.Context, reaction domain.NewsMarketReaction) error {
	f.saved = append(f.saved, reaction)
	return nil
}

func newTestReactionTracker(store ReactionStore, now time.Time) *ReactionTracker {
	tracker := NewReactionTracker(config.NewsConfig{
		ReactionInterval:      5 * time.Minute,
		ReactionBaselineGrace: 2 * time.Hour,
	}, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	tracker.now = func() time.Time { return now }
	return tracker
}

func reactionCandle(closeTime time.Time, close, high, low float64) domain.Candle {
	return domain.Candle{
		OpenTime: closeTime.Add(-5 * time.Minute), CloseTime: closeTime,
		Open: close, High: high, Low: low, Close: close, Closed: true,
		Source: domain.CandleSourceNative,
	}
}

func TestReactionTrackerDoesNotReadFutureHorizons(t *testing.T) {
	t0 := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	now := t0.Add(10 * time.Minute)
	store := &fakeReactionStore{
		work: []domain.NewsReactionWork{{ClusterID: uuid.New(), AssetID: 7, FirstSeenAt: t0}},
		candles: []domain.Candle{
			reactionCandle(t0.Add(5*time.Minute), 100, 101, 99),
			reactionCandle(t0.Add(10*time.Minute), 105, 106, 98),
			reactionCandle(t0.Add(20*time.Minute), 120, 121, 104), // future at analysis time
		},
	}
	tracker := newTestReactionTracker(store, now)
	stats, err := tracker.ProcessDue(context.Background(), 10)
	if err != nil {
		t.Fatalf("process due: %v", err)
	}
	if stats.Updated != 1 || len(store.saved) != 1 {
		t.Fatalf("unexpected stats=%+v saved=%d", stats, len(store.saved))
	}
	reaction := store.saved[0]
	if reaction.Return5mPct == nil || math.Abs(*reaction.Return5mPct-5) > 1e-9 {
		t.Fatalf("5m return=%v, want 5", reaction.Return5mPct)
	}
	if reaction.Return15mPct != nil || reaction.Return1hPct != nil || reaction.Return24hPct != nil {
		t.Fatalf("future horizons leaked into result: %+v", reaction)
	}
	if reaction.MaxUpPct == nil || math.Abs(*reaction.MaxUpPct-6) > 1e-9 || reaction.MaxDownPct == nil || math.Abs(*reaction.MaxDownPct+2) > 1e-9 {
		t.Fatalf("unexpected MFE/MAE: up=%v down=%v", reaction.MaxUpPct, reaction.MaxDownPct)
	}
	for _, query := range store.queries {
		if query.KnownAt.After(now) {
			t.Fatalf("query crossed analysis time: knownAt=%s now=%s", query.KnownAt, now)
		}
	}
	if !reaction.NextEvaluationAt.Equal(t0.Add(20 * time.Minute)) {
		t.Fatalf("next evaluation=%s", reaction.NextEvaluationAt)
	}
}

func TestReactionTrackerCompletesAllHorizons(t *testing.T) {
	t0 := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	baselineTime := t0.Add(5 * time.Minute)
	store := &fakeReactionStore{
		work: []domain.NewsReactionWork{{ClusterID: uuid.New(), AssetID: 1, FirstSeenAt: t0}},
		candles: []domain.Candle{
			reactionCandle(baselineTime, 100, 100, 100),
			reactionCandle(baselineTime.Add(5*time.Minute), 101, 102, 99),
			reactionCandle(baselineTime.Add(15*time.Minute), 102, 103, 98),
			reactionCandle(baselineTime.Add(time.Hour), 103, 104, 97),
			reactionCandle(baselineTime.Add(4*time.Hour), 104, 105, 96),
			reactionCandle(baselineTime.Add(24*time.Hour), 110, 112, 95),
		},
	}
	tracker := newTestReactionTracker(store, baselineTime.Add(24*time.Hour))
	stats, err := tracker.ProcessDue(context.Background(), 10)
	if err != nil {
		t.Fatalf("process due: %v", err)
	}
	reaction := store.saved[0]
	if stats.Completed != 1 || reaction.Status != domain.NewsReactionComplete {
		t.Fatalf("reaction not complete: stats=%+v reaction=%+v", stats, reaction)
	}
	if reaction.Return24hPct == nil || math.Abs(*reaction.Return24hPct-10) > 1e-9 {
		t.Fatalf("24h return=%v, want 10", reaction.Return24hPct)
	}
	if reaction.ObservedThrough == nil || !reaction.ObservedThrough.Equal(baselineTime.Add(24*time.Hour)) {
		t.Fatalf("observed through=%v", reaction.ObservedThrough)
	}
}

func TestReactionTrackerMarksMissingBaselineExplicitly(t *testing.T) {
	t0 := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	store := &fakeReactionStore{
		work: []domain.NewsReactionWork{{ClusterID: uuid.New(), AssetID: 1, FirstSeenAt: t0}},
	}
	tracker := newTestReactionTracker(store, t0.Add(2*time.Hour))
	stats, err := tracker.ProcessDue(context.Background(), 10)
	if err != nil {
		t.Fatalf("process due: %v", err)
	}
	reaction := store.saved[0]
	if stats.InsufficientData != 1 || reaction.Status != domain.NewsReactionInsufficientData {
		t.Fatalf("missing baseline must be explicit: stats=%+v reaction=%+v", stats, reaction)
	}
	if reaction.BaselineTime != nil || reaction.LastError == "" || reaction.CompletedAt == nil {
		t.Fatalf("unexpected insufficient-data row: %+v", reaction)
	}
}
