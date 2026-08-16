package news

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/crypto-market-advisor/advisor/internal/config"
	"github.com/crypto-market-advisor/advisor/internal/domain"
)

type fakeSnapshotStore struct {
	stats      domain.NewsStats
	asset      []domain.NewsSnapshotItem
	global     []domain.NewsSnapshotItem
	history    domain.NewsReactionHistory
	statsErr   error
	itemsErr   error
	historyErr error
}

func (f *fakeSnapshotStore) Stats(context.Context) (domain.NewsStats, error) {
	return f.stats, f.statsErr
}

func (f *fakeSnapshotStore) ListNewsSnapshotItems(context.Context, int64, time.Time, time.Time, int, int) ([]domain.NewsSnapshotItem, []domain.NewsSnapshotItem, error) {
	return f.asset, f.global, f.itemsErr
}

func (f *fakeSnapshotStore) ReactionHistory(context.Context, int64, domain.NewsCategory, time.Time, int) (domain.NewsReactionHistory, error) {
	return f.history, f.historyErr
}

func snapshotConfig() config.NewsConfig {
	return config.NewsConfig{
		Enabled: true, LLMLookback: 12 * time.Hour,
		LLMMaxAssetItems: 8, LLMMaxGlobalItems: 5,
		HistoryMinSampleSize: 10,
	}
}

func TestSnapshotStatusDistinguishesEmptyFromUnavailable(t *testing.T) {
	online := &fakeSnapshotStore{stats: domain.NewsStats{
		SourcesEnabled:  1,
		SourcesByStatus: map[domain.NewsSourceStatus]int{domain.NewsSourceOnline: 1},
	}, history: domain.NewsReactionHistory{Status: "insufficient_history"}}
	snapshot, err := NewSnapshotBuilder(snapshotConfig(), online).Build(context.Background(), 1, time.Now())
	if err != nil {
		t.Fatalf("healthy empty snapshot: %v", err)
	}
	if snapshot.Status != domain.NewsContextAvailableButEmpty {
		t.Fatalf("healthy empty status=%s", snapshot.Status)
	}

	broken := &fakeSnapshotStore{statsErr: errors.New("database unavailable")}
	snapshot, err = NewSnapshotBuilder(snapshotConfig(), broken).Build(context.Background(), 1, time.Now())
	if err == nil || snapshot.Status != domain.NewsContextUnavailable {
		t.Fatalf("outage must stay distinguishable: status=%s err=%v", snapshot.Status, err)
	}
}

func TestSnapshotKeepsNewsFailureSeparateFromTechnicalQuality(t *testing.T) {
	store := &fakeSnapshotStore{
		stats: domain.NewsStats{
			SourcesEnabled: 2, ItemsTotal: 5,
			SourcesByStatus: map[domain.NewsSourceStatus]int{
				domain.NewsSourceOnline: 1, domain.NewsSourceOffline: 1,
			},
		},
		asset:      []domain.NewsSnapshotItem{{Title: "asset event", Critical: true}},
		historyErr: errors.New("history temporarily unavailable"),
	}
	snapshot, err := NewSnapshotBuilder(snapshotConfig(), store).Build(context.Background(), 1, time.Now())
	if err == nil {
		t.Fatal("history degradation should be observable")
	}
	if snapshot.Status != domain.NewsContextDegraded || len(snapshot.AssetSpecific) != 1 {
		t.Fatalf("cached news should remain usable in degraded mode: %+v", snapshot)
	}
}

func TestDisabledSnapshotIsExplicitAndNonNil(t *testing.T) {
	cfg := snapshotConfig()
	cfg.Enabled = false
	snapshot, err := NewSnapshotBuilder(cfg, nil).Build(context.Background(), 1, time.Now())
	if err != nil || snapshot.Status != domain.NewsContextDisabled {
		t.Fatalf("disabled snapshot: %+v err=%v", snapshot, err)
	}
	if snapshot.AssetSpecific == nil || snapshot.Global == nil {
		t.Fatal("disabled snapshot arrays must serialize as [] rather than null")
	}
}
