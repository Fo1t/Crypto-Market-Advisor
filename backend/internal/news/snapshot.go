package news

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/crypto-market-advisor/advisor/internal/config"
	"github.com/crypto-market-advisor/advisor/internal/domain"
)

// SnapshotStore is the read-only persistence seam used by the analysis path.
type SnapshotStore interface {
	ListNewsSnapshotItems(context.Context, int64, time.Time, time.Time, int, int) ([]domain.NewsSnapshotItem, []domain.NewsSnapshotItem, error)
	ReactionHistory(context.Context, int64, domain.NewsCategory, time.Time, int) (domain.NewsReactionHistory, error)
	Stats(context.Context) (domain.NewsStats, error)
}

// SnapshotBuilder produces deterministic, bounded news context without any
// extra model inference. Failure is represented in-band and never prevents the
// technical analysis from completing.
type SnapshotBuilder struct {
	mu    sync.RWMutex
	cfg   config.NewsConfig
	store SnapshotStore
}

// SetConfig applies UI-edited news controls to subsequent analysis runs.
func (b *SnapshotBuilder) SetConfig(cfg config.NewsConfig) {
	b.mu.Lock()
	b.cfg = cfg
	b.mu.Unlock()
}

// NewSnapshotBuilder builds the deterministic news snapshot used by analyses.
func NewSnapshotBuilder(cfg config.NewsConfig, store SnapshotStore) *SnapshotBuilder {
	return &SnapshotBuilder{cfg: cfg, store: store}
}

// Build assembles the bounded news context that was knowable at knownAt.
func (b *SnapshotBuilder) Build(ctx context.Context, assetID int64, knownAt time.Time) (domain.NewsSnapshot, error) {
	b.mu.RLock()
	cfg := b.cfg
	b.mu.RUnlock()
	snapshot := domain.NewsSnapshot{
		Status:        domain.NewsContextUnavailable,
		LookbackHours: cfg.LLMLookback.Hours(),
		AssetSpecific: []domain.NewsSnapshotItem{},
		Global:        []domain.NewsSnapshotItem{},
		Historical: domain.NewsReactionHistory{
			Status: "insufficient_history",
		},
	}
	if !cfg.Enabled {
		snapshot.Status = domain.NewsContextDisabled
		snapshot.Historical.Status = "disabled"
		return snapshot, nil
	}
	if b.store == nil {
		snapshot.StatusDetail = "news store is unavailable"
		return snapshot, fmt.Errorf("news snapshot store is unavailable")
	}

	stats, err := b.store.Stats(ctx)
	if err != nil {
		snapshot.StatusDetail = "news status query failed"
		return snapshot, fmt.Errorf("load news status: %w", err)
	}
	snapshot.Status = statusFromStats(stats)

	knownAt = knownAt.UTC()
	assetItems, globalItems, itemsErr := b.store.ListNewsSnapshotItems(
		ctx, assetID, knownAt.Add(-cfg.LLMLookback), knownAt,
		cfg.LLMMaxAssetItems, cfg.LLMMaxGlobalItems,
	)
	if itemsErr != nil {
		snapshot.Status = domain.NewsContextUnavailable
		snapshot.StatusDetail = "news event query failed"
		return snapshot, fmt.Errorf("load news events: %w", itemsErr)
	}
	snapshot.AssetSpecific = assetItems
	snapshot.Global = globalItems

	history, historyErr := b.store.ReactionHistory(ctx, assetID, "", knownAt, cfg.HistoryMinSampleSize)
	if historyErr != nil {
		if snapshot.Status == domain.NewsContextOK {
			snapshot.Status = domain.NewsContextDegraded
		}
		snapshot.StatusDetail = "historical reaction query failed"
	} else {
		snapshot.Historical = history
	}

	if len(assetItems) == 0 && len(globalItems) == 0 && snapshot.Status == domain.NewsContextOK {
		snapshot.Status = domain.NewsContextAvailableButEmpty
	}
	if historyErr != nil {
		return snapshot, fmt.Errorf("load historical news reactions: %w", historyErr)
	}
	return snapshot, nil
}

func statusFromStats(stats domain.NewsStats) domain.NewsContextStatus {
	if stats.SourcesEnabled == 0 {
		return domain.NewsContextUnavailable
	}
	online := stats.SourcesByStatus[domain.NewsSourceOnline]
	degraded := stats.SourcesByStatus[domain.NewsSourceDegraded]
	offline := stats.SourcesByStatus[domain.NewsSourceOffline]
	if online == 0 {
		if stats.ItemsTotal > 0 {
			return domain.NewsContextDegraded
		}
		return domain.NewsContextUnavailable
	}
	if degraded > 0 || offline > 0 || online < stats.SourcesEnabled {
		return domain.NewsContextDegraded
	}
	return domain.NewsContextOK
}
