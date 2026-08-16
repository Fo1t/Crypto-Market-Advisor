package news

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/crypto-market-advisor/advisor/internal/config"
	"github.com/crypto-market-advisor/advisor/internal/domain"
	"github.com/crypto-market-advisor/advisor/internal/logging"
)

// EnrichmentStore is the persistence the enrichment pass needs.
type EnrichmentStore interface {
	ListPendingItems(ctx context.Context, enrichmentVersion, limit int) ([]domain.NewsWorkItem, error)
	ListAssetAliases(ctx context.Context) (map[int64][]string, error)
	CandidateClusters(ctx context.Context, enrichmentVersion int, from, to time.Time, limit int) ([]domain.NewsClusterCandidate, error)
	AssignCluster(ctx context.Context, item domain.NewsWorkItem, clusterID uuid.UUID, create bool,
		enrichmentVersion int,
		assets []domain.NewsAssetMatch, categories []domain.NewsCategoryMatch,
		importance, freshness float64, critical bool) (created bool, err error)
	CleanupOrphanClusters(ctx context.Context, enrichmentVersion int) (int64, error)
}

// CurrentEnrichmentVersion is bumped whenever the clustering or tagging rules
// change, so older derived clusters are recomputed instead of silently mixed.
const CurrentEnrichmentVersion = 1

// AssetRegistry supplies the tracked assets news can be linked to.
type AssetRegistry interface {
	List(ctx context.Context, enabledOnly bool) ([]domain.Asset, error)
}

// EnrichmentStats reports what one enrichment pass changed.
type EnrichmentStats struct {
	ItemsProcessed  int
	ClustersCreated int
	ItemsMerged     int
	OrphansRemoved  int64
}

// Processor clusters and enriches stored news using deterministic local rules.
type Processor struct {
	cfg    config.NewsConfig
	store  EnrichmentStore
	assets AssetRegistry
	log    *slog.Logger
	now    func() time.Time
}

// NewProcessor builds the deterministic clustering and enrichment processor.
func NewProcessor(cfg config.NewsConfig, store EnrichmentStore, assets AssetRegistry, logger *slog.Logger) *Processor {
	return &Processor{
		cfg: cfg, store: store, assets: assets,
		log: logging.For(logger, logging.CategoryNews), now: func() time.Time { return time.Now().UTC() },
	}
}

// ProcessPending drains a bounded queue so a malformed or unexpectedly large
// backlog cannot monopolize the scheduler forever.
func (p *Processor) ProcessPending(ctx context.Context) (EnrichmentStats, error) {
	assets, err := p.assets.List(ctx, true)
	if err != nil {
		return EnrichmentStats{}, fmt.Errorf("list assets for news tagging: %w", err)
	}
	customAliases, err := p.store.ListAssetAliases(ctx)
	if err != nil {
		return EnrichmentStats{}, err
	}
	matcher := NewAssetMatcher(assets, customAliases)
	var stats EnrichmentStats
	const batchSize = 200
	const maxBatches = 25
	for batch := 0; batch < maxBatches; batch++ {
		items, err := p.store.ListPendingItems(ctx, CurrentEnrichmentVersion, batchSize)
		if err != nil {
			return stats, err
		}
		if len(items) == 0 {
			break
		}
		for _, item := range items {
			if ctx.Err() != nil {
				return stats, ctx.Err()
			}
			created, err := p.processItem(ctx, matcher, item)
			if err != nil {
				return stats, fmt.Errorf("enrich news item %s: %w", item.Item.ID, err)
			}
			stats.ItemsProcessed++
			if created {
				stats.ClustersCreated++
			} else {
				stats.ItemsMerged++
			}
		}
		if len(items) < batchSize {
			break
		}
	}
	stats.OrphansRemoved, err = p.store.CleanupOrphanClusters(ctx, CurrentEnrichmentVersion)
	if err != nil {
		return stats, err
	}
	p.log.Info("news enrichment finished",
		slog.Int("items", stats.ItemsProcessed),
		slog.Int("clusters_created", stats.ClustersCreated),
		slog.Int("items_merged", stats.ItemsMerged),
		slog.Int64("orphans_removed", stats.OrphansRemoved))
	return stats, nil
}

func (p *Processor) processItem(ctx context.Context, matcher *AssetMatcher, item domain.NewsWorkItem) (bool, error) {
	categories := ClassifyItem(item.Item)
	assetMatches := matcher.Match(item.Item)
	assetIDs := make([]int64, 0, len(assetMatches))
	for _, match := range assetMatches {
		assetIDs = append(assetIDs, match.AssetID)
	}
	categoryValues := make([]domain.NewsCategory, 0, len(categories))
	for _, match := range categories {
		categoryValues = append(categoryValues, match.Category)
	}

	window := p.cfg.ClusterTimeWindow
	candidates, err := p.store.CandidateClusters(
		ctx, CurrentEnrichmentVersion,
		item.Item.PublishedAt.Add(-window), item.Item.PublishedAt.Add(window), 500,
	)
	if err != nil {
		return false, err
	}
	bestScore := 0.0
	var best *domain.NewsClusterCandidate
	for i := range candidates {
		candidate := &candidates[i]
		score := EventSimilarity(
			item.Item.Title, candidate.Cluster.CanonicalTitle,
			assetIDs, candidate.AssetIDs, categoryValues, candidate.Categories,
			item.Item.PublishedAt.Sub(candidate.Cluster.FirstPublishedAt), window,
		)
		if score > bestScore {
			bestScore, best = score, candidate
		}
	}
	clusterID, create, sourceCount := uuid.New(), true, 1
	if best != nil && bestScore >= p.cfg.TitleSimilarityThreshold {
		clusterID, create = best.Cluster.ID, false
		sourceCount = best.Cluster.SourceCount + 1
	}
	now := p.now()
	importance := ImportanceScore(item.SourcePriority, sourceCount, categories, assetMatches)
	freshness := FreshnessScore(item.Item.PublishedAt, now)
	critical := IsCriticalEvent(item.Item, categories, assetMatches)
	return p.store.AssignCluster(
		ctx, item, clusterID, create, CurrentEnrichmentVersion, assetMatches, categories,
		importance, freshness, critical,
	)
}
