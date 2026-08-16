package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

// ListNewsSnapshotItems returns separately ranked asset-specific and global
// events. Critical events sort first, so applying configured limits can never
// silently discard a fresh critical item in favour of a routine headline.
func (r *NewsRepository) ListNewsSnapshotItems(
	ctx context.Context,
	assetID int64,
	since, knownAt time.Time,
	maxAsset, maxGlobal int,
) ([]domain.NewsSnapshotItem, []domain.NewsSnapshotItem, error) {
	assetItems, err := r.listNewsSnapshotGroup(ctx, assetID, since, knownAt, maxAsset, false)
	if err != nil {
		return nil, nil, err
	}
	globalItems, err := r.listNewsSnapshotGroup(ctx, assetID, since, knownAt, maxGlobal, true)
	if err != nil {
		return nil, nil, err
	}
	return assetItems, globalItems, nil
}

func (r *NewsRepository) listNewsSnapshotGroup(
	ctx context.Context,
	assetID int64,
	since, knownAt time.Time,
	limit int,
	global bool,
) ([]domain.NewsSnapshotItem, error) {
	if limit <= 0 {
		return []domain.NewsSnapshotItem{}, nil
	}
	if limit > 50 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx, `
		SELECT c.id,
		       GREATEST(0, FLOOR(EXTRACT(EPOCH FROM ($3::timestamptz - c.first_seen_at)) / 60))::int,
		       COALESCE(s.name, ''),
		       CASE WHEN s.provider = 'bybit' THEN 'exchange_official'
		            WHEN s.canonical_url ILIKE '%ethereum.org/%' THEN 'project_official'
		            ELSE 'media' END,
		       c.canonical_title, LEFT(COALESCE(ci.summary, ''), 500),
		       COALESCE((SELECT jsonb_agg(a.symbol ORDER BY a.symbol)
		         FROM news_cluster_assets all_ca JOIN assets a ON a.id = all_ca.asset_id
		         WHERE all_ca.cluster_id = c.id), '[]'::jsonb),
		       COALESCE((SELECT jsonb_agg(cc.category ORDER BY cc.confidence DESC, cc.category)
		         FROM news_cluster_categories cc WHERE cc.cluster_id = c.id), '[]'::jsonb),
		       c.importance,
		       GREATEST(0, 1 - EXTRACT(EPOCH FROM ($3::timestamptz - c.first_seen_at)) /
		         GREATEST(1, EXTRACT(EPOCH FROM ($3::timestamptz - $2::timestamptz)))),
		       c.critical, c.source_count,
		       r.baseline_time IS NOT NULL AND r.baseline_time <= $3,
		       CASE WHEN r.return_5m_at <= $3 THEN r.return_5m_pct END,
		       CASE WHEN r.return_15m_at <= $3 THEN r.return_15m_pct END,
		       CASE WHEN r.return_1h_at <= $3 THEN r.return_1h_pct END,
		       CASE WHEN r.return_4h_at <= $3 THEN r.return_4h_pct END,
		       CASE WHEN r.return_24h_at <= $3 THEN r.return_24h_pct END,
		       CASE WHEN r.observed_through <= $3 THEN r.max_up_pct END,
		       CASE WHEN r.observed_through <= $3 THEN r.max_down_pct END,
		       CASE WHEN r.observed_through <= $3 THEN r.observed_through END
		FROM news_clusters c
		LEFT JOIN news_sources s ON s.id = c.canonical_source_id
		LEFT JOIN LATERAL (
			SELECT i.summary FROM news_items i
			WHERE i.cluster_id = c.id
			ORDER BY (i.source_id = c.canonical_source_id) DESC, i.first_seen_at ASC LIMIT 1
		) ci ON TRUE
		LEFT JOIN news_market_reactions r ON r.cluster_id = c.id AND r.asset_id = $1
		WHERE c.algorithm_version = 1
		  AND c.first_seen_at >= $2 AND c.first_seen_at <= $3
		  AND CASE WHEN $4::boolean THEN
		      NOT EXISTS (SELECT 1 FROM news_cluster_assets own_ca
		                  WHERE own_ca.cluster_id = c.id AND own_ca.asset_id = $1)
		      AND (
		        NOT EXISTS (SELECT 1 FROM news_cluster_assets any_ca WHERE any_ca.cluster_id = c.id)
		        OR EXISTS (SELECT 1 FROM news_cluster_categories gcc
		                   WHERE gcc.cluster_id = c.id AND gcc.category IN
		                     ('market','macro','regulation','stablecoin','security','hack','exploit','exchange'))
		      )
		    ELSE EXISTS (SELECT 1 FROM news_cluster_assets own_ca
		                 WHERE own_ca.cluster_id = c.id AND own_ca.asset_id = $1)
		  END
		ORDER BY c.critical DESC,
		  (c.importance * 0.45
		   + GREATEST(0, 1 - EXTRACT(EPOCH FROM ($3::timestamptz - c.first_seen_at)) /
		       GREATEST(1, EXTRACT(EPOCH FROM ($3::timestamptz - $2::timestamptz)))) * 0.25
		   + CASE WHEN s.provider = 'bybit' OR s.canonical_url ILIKE '%ethereum.org/%' THEN 0.10 ELSE 0 END
		   + LEAST(c.source_count, 5) / 5.0 * 0.10
		   + LEAST(COALESCE(s.priority, 0), 100) / 100.0 * 0.10) DESC,
		  c.first_seen_at DESC, c.id`, assetID, since.UTC(), knownAt.UTC(), global)
	if err != nil {
		return nil, fmt.Errorf("list news snapshot items: %w", err)
	}
	defer rows.Close()

	out := make([]domain.NewsSnapshotItem, 0, limit)
	for rows.Next() {
		var item domain.NewsSnapshotItem
		var assetsJSON, categoriesJSON []byte
		var hasReaction bool
		var reaction domain.NewsReactionSoFar
		if err := rows.Scan(
			&item.ClusterID, &item.AgeMinutes, &item.CanonicalSource,
			&item.SourceType, &item.Title, &item.Summary,
			&assetsJSON, &categoriesJSON, &item.Importance, &item.Freshness,
			&item.Critical, &item.SourceCount, &hasReaction,
			&reaction.Return5mPct, &reaction.Return15mPct, &reaction.Return1hPct,
			&reaction.Return4hPct, &reaction.Return24hPct,
			&reaction.MaxUpPct, &reaction.MaxDownPct, &reaction.ObservedThrough,
		); err != nil {
			return nil, fmt.Errorf("scan news snapshot item: %w", err)
		}
		if err := json.Unmarshal(assetsJSON, &item.Assets); err != nil {
			return nil, fmt.Errorf("decode news snapshot assets: %w", err)
		}
		if err := json.Unmarshal(categoriesJSON, &item.Categories); err != nil {
			return nil, fmt.Errorf("decode news snapshot categories: %w", err)
		}
		if hasReaction {
			reaction.ElapsedMinutes = item.AgeMinutes
			reaction.LatestReturnPct = latestNewsReturn(reaction)
			item.Reaction = &reaction
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate news snapshot items: %w", err)
	}
	return limitSnapshotItems(out, limit), nil
}

func limitSnapshotItems(items []domain.NewsSnapshotItem, limit int) []domain.NewsSnapshotItem {
	critical := make([]domain.NewsSnapshotItem, 0)
	nonCritical := make([]domain.NewsSnapshotItem, 0)
	for _, item := range items {
		if item.Critical {
			critical = append(critical, item)
		} else {
			nonCritical = append(nonCritical, item)
		}
	}
	out := critical
	remaining := limit - len(out)
	if remaining > len(nonCritical) {
		remaining = len(nonCritical)
	}
	if remaining > 0 {
		out = append(out, nonCritical[:remaining]...)
	}
	return out
}

func latestNewsReturn(reaction domain.NewsReactionSoFar) *float64 {
	for _, value := range []*float64{
		reaction.Return24hPct, reaction.Return4hPct, reaction.Return1hPct,
		reaction.Return15mPct, reaction.Return5mPct,
	} {
		if value != nil {
			return value
		}
	}
	return nil
}
