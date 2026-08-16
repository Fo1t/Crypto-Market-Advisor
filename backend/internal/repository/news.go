package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

// NewsRepository persists news sources and their fetch state. Item/cluster
// ingestion is added as one transaction in the next pipeline milestone.
type NewsRepository struct {
	pool *pgxpool.Pool
}

// ListSources returns stable priority ordering for source management and the
// scheduler. Disabled sources are included so they can be re-enabled.
func (r *NewsRepository) ListSources(ctx context.Context) ([]domain.NewsSource, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, name, url, canonical_url, provider, priority, enabled, system,
		       status, etag, last_modified, last_attempt_at, last_success_at,
		       last_error, consecutive_errors, created_at, updated_at
		FROM news_sources
		ORDER BY priority DESC, name ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list news sources: %w", err)
	}
	defer rows.Close()

	out := make([]domain.NewsSource, 0)
	for rows.Next() {
		source, err := scanNewsSource(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, source)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate news sources: %w", err)
	}
	return out, nil
}

// GetSource returns one source by its stable identifier.
func (r *NewsRepository) GetSource(ctx context.Context, id uuid.UUID) (domain.NewsSource, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, name, url, canonical_url, provider, priority, enabled, system,
		       status, etag, last_modified, last_attempt_at, last_success_at,
		       last_error, consecutive_errors, created_at, updated_at
		FROM news_sources WHERE id = $1`, id)
	source, err := scanNewsSource(row.Scan)
	if err != nil {
		return domain.NewsSource{}, mapNoRows(err)
	}
	return source, nil
}

// UpsertSource creates or updates a source while preserving its fetch state.
// canonical_url is the conflict key because the same feed must not be polled
// twice under different display names.
func (r *NewsRepository) UpsertSource(ctx context.Context, source domain.NewsSource) (domain.NewsSource, error) {
	if source.ID == uuid.Nil {
		source.ID = uuid.New()
	}
	if source.Status == "" {
		source.Status = domain.NewsSourceOffline
	}
	row := r.pool.QueryRow(ctx, `
		INSERT INTO news_sources (
			id, name, url, canonical_url, provider, priority, enabled, system, status
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (canonical_url) DO UPDATE SET
			name = EXCLUDED.name,
			url = EXCLUDED.url,
			provider = EXCLUDED.provider,
			priority = EXCLUDED.priority,
			enabled = EXCLUDED.enabled,
			status = CASE WHEN EXCLUDED.enabled THEN news_sources.status ELSE 'disabled' END,
			updated_at = NOW()
		RETURNING id, name, url, canonical_url, provider, priority, enabled, system,
		          status, etag, last_modified, last_attempt_at, last_success_at,
		          last_error, consecutive_errors, created_at, updated_at`,
		source.ID, source.Name, source.URL, source.CanonicalURL, source.Provider,
		source.Priority, source.Enabled, source.System, source.Status)
	stored, err := scanNewsSource(row.Scan)
	if err != nil {
		return domain.NewsSource{}, fmt.Errorf("upsert news source: %w", err)
	}
	return stored, nil
}

// UpdateSource updates mutable source configuration without touching cache or
// health history. System-source restrictions are enforced by the API/service.
func (r *NewsRepository) UpdateSource(ctx context.Context, source domain.NewsSource) (domain.NewsSource, error) {
	row := r.pool.QueryRow(ctx, `
		UPDATE news_sources SET
			name = $2, url = $3, canonical_url = $4, provider = $5,
			priority = $6, enabled = $7,
			status = CASE WHEN $7 THEN CASE WHEN status = 'disabled' THEN 'offline' ELSE status END ELSE 'disabled' END,
			updated_at = NOW()
		WHERE id = $1
		RETURNING id, name, url, canonical_url, provider, priority, enabled, system,
		          status, etag, last_modified, last_attempt_at, last_success_at,
		          last_error, consecutive_errors, created_at, updated_at`,
		source.ID, source.Name, source.URL, source.CanonicalURL, source.Provider,
		source.Priority, source.Enabled)
	stored, err := scanNewsSource(row.Scan)
	if err != nil {
		return domain.NewsSource{}, mapNoRows(err)
	}
	return stored, nil
}

// RecordFetchSuccess atomically stores conditional-GET metadata and marks the
// source healthy. A 304 response is still a successful fetch.
func (r *NewsRepository) RecordFetchSuccess(ctx context.Context, id uuid.UUID, at time.Time, etag, lastModified string) error {
	result, err := r.pool.Exec(ctx, `
		UPDATE news_sources SET
			status = CASE WHEN enabled THEN 'online' ELSE 'disabled' END,
			etag = $2,
			last_modified = $3,
			last_attempt_at = $4,
			last_success_at = $4,
			last_error = '',
			consecutive_errors = 0,
			updated_at = NOW()
		WHERE id = $1`, id, etag, lastModified, at)
	if err != nil {
		return fmt.Errorf("record news fetch success: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordFetchFailure keeps the source and its cache metadata, increments the
// error counter, and derives a stable degraded/offline status.
func (r *NewsRepository) RecordFetchFailure(ctx context.Context, id uuid.UUID, at time.Time, message string) error {
	result, err := r.pool.Exec(ctx, `
		UPDATE news_sources SET
			status = CASE
				WHEN NOT enabled THEN 'disabled'
				WHEN consecutive_errors + 1 >= 3 THEN 'offline'
				ELSE 'degraded'
			END,
			last_attempt_at = $2,
			last_error = LEFT($3, 2000),
			consecutive_errors = consecutive_errors + 1,
			updated_at = NOW()
		WHERE id = $1`, id, at, message)
	if err != nil {
		return fmt.Errorf("record news fetch failure: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UpsertItems persists a normalized batch atomically. It returns the number of
// newly inserted and already-known materials. Exact duplicate checks cover the
// provider ID, canonical URL, and identical title inside a narrow time window.
func (r *NewsRepository) UpsertItems(ctx context.Context, items []domain.NewsItem) (int, int, error) {
	inserted, existing := 0, 0
	err := inTx(ctx, r.pool, func(tx pgx.Tx) error {
		for _, item := range items {
			metadata, err := json.Marshal(item.Metadata)
			if err != nil {
				return fmt.Errorf("encode news metadata: %w", err)
			}

			var existingID uuid.UUID
			err = tx.QueryRow(ctx, `
				SELECT id FROM news_items
				WHERE (source_id = $1 AND $2 <> '' AND external_id = $2)
				   OR ($3 <> '' AND canonical_url = $3)
				   OR (title_hash = $4 AND published_at BETWEEN $5::timestamptz - INTERVAL '6 hours' AND $5::timestamptz + INTERVAL '6 hours')
				ORDER BY first_seen_at ASC
				LIMIT 1
				FOR UPDATE`, item.SourceID, item.ExternalID, item.CanonicalURL, item.TitleHash, item.PublishedAt).Scan(&existingID)
			if err == nil {
				if _, err := tx.Exec(ctx, `
					UPDATE news_items SET last_seen_at = GREATEST(last_seen_at, $2), updated_at = NOW()
					WHERE id = $1`, existingID, item.LastSeenAt); err != nil {
					return fmt.Errorf("touch existing news item: %w", err)
				}
				existing++
				continue
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("find exact news duplicate: %w", err)
			}

			result, err := tx.Exec(ctx, `
				INSERT INTO news_items (
					id, source_id, cluster_id, external_id, url, canonical_url,
					title, normalized_title, title_hash, summary, language,
					published_at, first_seen_at, last_seen_at, raw_metadata
				) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
				ON CONFLICT DO NOTHING`,
				item.ID, item.SourceID, item.ClusterID, item.ExternalID, item.URL,
				item.CanonicalURL, item.Title, item.NormalizedTitle, item.TitleHash,
				item.Summary, item.Language, item.PublishedAt, item.FirstSeenAt,
				item.LastSeenAt, metadata)
			if err != nil {
				return fmt.Errorf("insert news item: %w", err)
			}
			if result.RowsAffected() == 1 {
				inserted++
			} else {
				existing++
			}
		}
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return inserted, existing, nil
}

// ListPendingItems returns the oldest unclustered items first. The partial
// idx_news_items_unclustered index keeps this queue scan small.
func (r *NewsRepository) ListPendingItems(ctx context.Context, enrichmentVersion, limit int) ([]domain.NewsWorkItem, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := r.pool.Query(ctx, `
		SELECT i.id, i.source_id, i.cluster_id, i.external_id, i.url,
		       i.canonical_url, i.title, i.normalized_title, i.title_hash,
		       i.summary, i.language, i.published_at, i.first_seen_at,
		       i.last_seen_at, i.raw_metadata, s.priority, s.system
		FROM news_items i
		JOIN news_sources s ON s.id = i.source_id
		WHERE i.enrichment_version < $1
		ORDER BY i.enrichment_version ASC, i.first_seen_at ASC, i.id ASC
		LIMIT $2`, enrichmentVersion, limit)
	if err != nil {
		return nil, fmt.Errorf("list pending news items: %w", err)
	}
	defer rows.Close()
	out := make([]domain.NewsWorkItem, 0, limit)
	for rows.Next() {
		var work domain.NewsWorkItem
		var metadata []byte
		if err := rows.Scan(
			&work.Item.ID, &work.Item.SourceID, &work.Item.ClusterID,
			&work.Item.ExternalID, &work.Item.URL, &work.Item.CanonicalURL,
			&work.Item.Title, &work.Item.NormalizedTitle, &work.Item.TitleHash,
			&work.Item.Summary, &work.Item.Language, &work.Item.PublishedAt,
			&work.Item.FirstSeenAt, &work.Item.LastSeenAt, &metadata,
			&work.SourcePriority, &work.SourceSystem,
		); err != nil {
			return nil, fmt.Errorf("scan pending news item: %w", err)
		}
		if err := json.Unmarshal(metadata, &work.Item.Metadata); err != nil {
			return nil, fmt.Errorf("decode pending news metadata: %w", err)
		}
		out = append(out, work)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending news items: %w", err)
	}
	return out, nil
}

// ListAssetAliases returns enabled user aliases grouped by asset.
func (r *NewsRepository) ListAssetAliases(ctx context.Context) (map[int64][]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT asset_id, alias FROM news_asset_aliases
		WHERE enabled
		ORDER BY asset_id, id`)
	if err != nil {
		return nil, fmt.Errorf("list news asset aliases: %w", err)
	}
	defer rows.Close()
	out := make(map[int64][]string)
	for rows.Next() {
		var assetID int64
		var alias string
		if err := rows.Scan(&assetID, &alias); err != nil {
			return nil, fmt.Errorf("scan news asset alias: %w", err)
		}
		out[assetID] = append(out[assetID], alias)
	}
	return out, rows.Err()
}

// CandidateClusters returns recent clusters with the relations needed by the
// in-process similarity function. Equality filters are absent here, so the
// leading last_seen_at column of idx_news_clusters_feed serves the range scan.
func (r *NewsRepository) CandidateClusters(ctx context.Context, enrichmentVersion int, from, to time.Time, limit int) ([]domain.NewsClusterCandidate, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	rows, err := r.pool.Query(ctx, `
		SELECT c.id, c.canonical_title, c.canonical_source_id,
		       c.first_published_at, c.first_seen_at, c.last_seen_at,
		       c.importance, c.freshness, c.critical, c.source_count,
		       COALESCE(array_agg(DISTINCT ca.asset_id) FILTER (WHERE ca.asset_id IS NOT NULL), '{}'::bigint[]),
		       COALESCE(array_agg(DISTINCT cc.category) FILTER (WHERE cc.category IS NOT NULL), '{}'::text[])
		FROM news_clusters c
		LEFT JOIN news_cluster_assets ca ON ca.cluster_id = c.id
		LEFT JOIN news_cluster_categories cc ON cc.cluster_id = c.id
		WHERE c.algorithm_version = $1 AND c.last_seen_at >= $2 AND c.first_published_at <= $3
		GROUP BY c.id
		ORDER BY c.last_seen_at DESC, c.importance DESC
		LIMIT $4`, enrichmentVersion, from, to, limit)
	if err != nil {
		return nil, fmt.Errorf("list news cluster candidates: %w", err)
	}
	defer rows.Close()
	out := make([]domain.NewsClusterCandidate, 0, limit)
	for rows.Next() {
		var candidate domain.NewsClusterCandidate
		var categories []string
		if err := rows.Scan(
			&candidate.Cluster.ID, &candidate.Cluster.CanonicalTitle,
			&candidate.Cluster.CanonicalSourceID, &candidate.Cluster.FirstPublishedAt,
			&candidate.Cluster.FirstSeenAt, &candidate.Cluster.LastSeenAt,
			&candidate.Cluster.Importance, &candidate.Cluster.Freshness,
			&candidate.Cluster.Critical, &candidate.Cluster.SourceCount,
			&candidate.AssetIDs, &categories,
		); err != nil {
			return nil, fmt.Errorf("scan news cluster candidate: %w", err)
		}
		for _, category := range categories {
			candidate.Categories = append(candidate.Categories, domain.NewsCategory(category))
		}
		out = append(out, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate news cluster candidates: %w", err)
	}
	return out, nil
}

// AssignCluster attaches an item and enrichment relations under a transaction-
// level advisory lock. It prevents concurrent duplicate assignment of the same
// item; candidate selection intentionally remains a single-worker operation.
func (r *NewsRepository) AssignCluster(
	ctx context.Context,
	item domain.NewsWorkItem,
	clusterID uuid.UUID,
	create bool,
	enrichmentVersion int,
	assets []domain.NewsAssetMatch,
	categories []domain.NewsCategoryMatch,
	importance, freshness float64,
	critical bool,
) (bool, error) {
	created := false
	err := inTx(ctx, r.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('news_clustering'))`); err != nil {
			return fmt.Errorf("lock news clustering: %w", err)
		}
		var currentVersion int
		if err := tx.QueryRow(ctx, `SELECT enrichment_version FROM news_items WHERE id = $1 FOR UPDATE`, item.Item.ID).Scan(&currentVersion); err != nil {
			return mapNoRows(err)
		}
		if currentVersion >= enrichmentVersion {
			return nil
		}
		if create {
			_, err := tx.Exec(ctx, `
				INSERT INTO news_clusters (
					id, canonical_title, canonical_source_id, first_published_at,
					first_seen_at, last_seen_at, importance, freshness, critical,
					source_count, algorithm_version
				) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,1,$10)`,
				clusterID, item.Item.Title, item.Item.SourceID, item.Item.PublishedAt,
				item.Item.FirstSeenAt, item.Item.LastSeenAt, importance, freshness, critical,
				enrichmentVersion)
			if err != nil {
				return fmt.Errorf("create news cluster: %w", err)
			}
			created = true
		}
		if _, err := tx.Exec(ctx, `
			UPDATE news_items SET cluster_id = $2, enrichment_version = $3, updated_at = NOW()
			WHERE id = $1`, item.Item.ID, clusterID, enrichmentVersion); err != nil {
			return fmt.Errorf("attach news item to cluster: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM news_item_assets WHERE item_id = $1`, item.Item.ID); err != nil {
			return fmt.Errorf("reset news item assets: %w", err)
		}
		for _, asset := range assets {
			if _, err := tx.Exec(ctx, `
				INSERT INTO news_item_assets (item_id, asset_id, confidence, matched_by)
				VALUES ($1,$2,$3,$4)
				ON CONFLICT (item_id, asset_id) DO UPDATE SET
					confidence = GREATEST(news_item_assets.confidence, EXCLUDED.confidence),
					matched_by = CASE WHEN EXCLUDED.confidence > news_item_assets.confidence THEN EXCLUDED.matched_by ELSE news_item_assets.matched_by END`,
				item.Item.ID, asset.AssetID, asset.Confidence, asset.MatchedBy); err != nil {
				return fmt.Errorf("attach news item asset: %w", err)
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO news_cluster_assets (cluster_id, asset_id, confidence)
				VALUES ($1,$2,$3)
				ON CONFLICT (cluster_id, asset_id) DO UPDATE SET
					confidence = GREATEST(news_cluster_assets.confidence, EXCLUDED.confidence)`,
				clusterID, asset.AssetID, asset.Confidence); err != nil {
				return fmt.Errorf("attach news cluster asset: %w", err)
			}
		}
		for _, category := range categories {
			if _, err := tx.Exec(ctx, `
				INSERT INTO news_cluster_categories (cluster_id, category, confidence)
				VALUES ($1,$2,$3)
				ON CONFLICT (cluster_id, category) DO UPDATE SET
					confidence = GREATEST(news_cluster_categories.confidence, EXCLUDED.confidence)`,
				clusterID, category.Category, category.Confidence); err != nil {
				return fmt.Errorf("attach news cluster category: %w", err)
			}
		}
		if !create {
			if _, err := tx.Exec(ctx, `
				UPDATE news_clusters c SET
					canonical_title = CASE WHEN $2 > COALESCE((SELECT priority FROM news_sources WHERE id = c.canonical_source_id), -1)
					                       THEN $3 ELSE c.canonical_title END,
					canonical_source_id = CASE WHEN $2 > COALESCE((SELECT priority FROM news_sources WHERE id = c.canonical_source_id), -1)
					                           THEN $4 ELSE c.canonical_source_id END,
					first_published_at = LEAST(c.first_published_at, $5),
					first_seen_at = LEAST(c.first_seen_at, $6),
					last_seen_at = GREATEST(c.last_seen_at, $7),
					importance = GREATEST(c.importance, $8),
					freshness = GREATEST(c.freshness, $9),
					critical = c.critical OR $10,
					source_count = (SELECT COUNT(DISTINCT source_id) FROM news_items WHERE cluster_id = c.id),
					updated_at = NOW()
				WHERE c.id = $1`, clusterID, item.SourcePriority, item.Item.Title,
				item.Item.SourceID, item.Item.PublishedAt, item.Item.FirstSeenAt,
				item.Item.LastSeenAt, importance, freshness, critical); err != nil {
				return fmt.Errorf("update news cluster: %w", err)
			}
		}
		return nil
	})
	return created, err
}

// CleanupOrphanClusters removes derived clusters left behind after a versioned
// re-enrichment pass. Raw news_items and first_seen_at are never deleted.
func (r *NewsRepository) CleanupOrphanClusters(ctx context.Context, enrichmentVersion int) (int64, error) {
	result, err := r.pool.Exec(ctx, `
		DELETE FROM news_clusters c
		WHERE c.algorithm_version < $1
		  AND NOT EXISTS (SELECT 1 FROM news_items i WHERE i.cluster_id = c.id)`, enrichmentVersion)
	if err != nil {
		return 0, fmt.Errorf("cleanup orphan news clusters: %w", err)
	}
	return result.RowsAffected(), nil
}

// NewsListFilter narrows a cluster listing.
type NewsListFilter struct {
	ClusterID     *uuid.UUID
	Query         string
	AssetSymbol   string
	Category      string
	SourceID      *uuid.UUID
	Critical      *bool
	MinImportance *float64
	Since         *time.Time
	Until         *time.Time
	KnownAt       *time.Time
	Sort          string
	Limit         int
	Offset        int
}

// ListClusters returns the normalized event feed, not raw duplicate articles.
func (r *NewsRepository) ListClusters(ctx context.Context, filter NewsListFilter) ([]domain.NewsClusterView, int, error) {
	where := []string{"c.algorithm_version = 1"}
	args := []any{}
	add := func(format string, value any) {
		args = append(args, value)
		where = append(where, fmt.Sprintf(format, len(args)))
	}
	if filter.ClusterID != nil {
		add("c.id = $%d", *filter.ClusterID)
	}
	if filter.Query != "" {
		args = append(args, filter.Query)
		placeholder := len(args)
		where = append(where, fmt.Sprintf("(c.canonical_title ILIKE '%%' || $%d || '%%' OR EXISTS (SELECT 1 FROM news_items qi WHERE qi.cluster_id = c.id AND qi.summary ILIKE '%%' || $%d || '%%'))", placeholder, placeholder))
	}
	if filter.AssetSymbol != "" {
		add("EXISTS (SELECT 1 FROM news_cluster_assets qa JOIN assets qaa ON qaa.id = qa.asset_id WHERE qa.cluster_id = c.id AND UPPER(qaa.symbol) = UPPER($%d))", filter.AssetSymbol)
	}
	if filter.Category != "" {
		add("EXISTS (SELECT 1 FROM news_cluster_categories qc WHERE qc.cluster_id = c.id AND qc.category = $%d)", filter.Category)
	}
	if filter.SourceID != nil {
		add("EXISTS (SELECT 1 FROM news_items qs WHERE qs.cluster_id = c.id AND qs.source_id = $%d)", *filter.SourceID)
	}
	if filter.Critical != nil {
		add("c.critical = $%d", *filter.Critical)
	}
	if filter.MinImportance != nil {
		add("c.importance >= $%d", *filter.MinImportance)
	}
	if filter.Since != nil {
		add("c.first_seen_at >= $%d", filter.Since.UTC())
	}
	if filter.Until != nil {
		add("c.first_seen_at <= $%d", filter.Until.UTC())
	}
	if filter.KnownAt != nil {
		add("c.first_seen_at <= $%d", filter.KnownAt.UTC())
	}
	clause := strings.Join(where, " AND ")
	var total int
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM news_clusters c WHERE `+clause, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count news clusters: %w", err)
	}
	limit := filter.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	order := "c.first_seen_at DESC, c.id DESC"
	if filter.Sort == "importance" {
		order = "c.importance DESC, c.first_seen_at DESC, c.id DESC"
	}
	args = append(args, limit, filter.Offset)
	query := fmt.Sprintf(`
		SELECT c.id, c.canonical_title, COALESCE(ci.canonical_url, ''),
		       COALESCE(ci.summary, ''), COALESCE(ci.language, 'und'),
		       c.first_published_at, c.first_seen_at, c.last_seen_at,
		       c.importance, c.freshness, c.critical, c.source_count,
		       (SELECT COUNT(*) FROM news_items pi WHERE pi.cluster_id = c.id),
		       COALESCE((SELECT jsonb_agg(jsonb_build_object(
		           'id', a.id, 'symbol', a.symbol, 'name', a.display_name, 'confidence', ca.confidence
		       ) ORDER BY ca.confidence DESC, a.symbol) FROM news_cluster_assets ca
		          JOIN assets a ON a.id = ca.asset_id WHERE ca.cluster_id = c.id), '[]'::jsonb),
		       COALESCE((SELECT jsonb_agg(jsonb_build_object(
		           'category', cc.category, 'confidence', cc.confidence
		       ) ORDER BY cc.confidence DESC, cc.category) FROM news_cluster_categories cc
		          WHERE cc.cluster_id = c.id), '[]'::jsonb),
		       COALESCE((SELECT jsonb_agg(source_row.obj ORDER BY source_row.priority DESC, source_row.name)
		          FROM (SELECT DISTINCT s.id, s.priority, s.name,
		               jsonb_build_object('id', s.id, 'name', s.name, 'priority', s.priority, 'system', s.system) obj
		                FROM news_items si JOIN news_sources s ON s.id = si.source_id
		                WHERE si.cluster_id = c.id) source_row), '[]'::jsonb),
		       COALESCE((SELECT jsonb_agg(jsonb_build_object(
		           'asset_id', r.asset_id, 'symbol', ra.symbol,
		           'baseline_time', r.baseline_time, 'baseline_price', r.baseline_price,
		           'return_5m_pct', r.return_5m_pct, 'return_15m_pct', r.return_15m_pct,
		           'return_1h_pct', r.return_1h_pct, 'return_4h_pct', r.return_4h_pct,
		           'return_24h_pct', r.return_24h_pct,
		           'max_up_move_pct', r.max_up_pct, 'max_down_move_pct', r.max_down_pct,
		           'observed_through', r.observed_through, 'status', r.status
		       ) ORDER BY ra.symbol) FROM news_market_reactions r
		         JOIN assets ra ON ra.id = r.asset_id WHERE r.cluster_id = c.id), '[]'::jsonb)
		FROM news_clusters c
		LEFT JOIN LATERAL (
			SELECT i.canonical_url, i.summary, i.language
			FROM news_items i WHERE i.cluster_id = c.id
			ORDER BY (i.source_id = c.canonical_source_id) DESC, i.first_seen_at ASC LIMIT 1
		) ci ON TRUE
		WHERE %s ORDER BY %s LIMIT $%d OFFSET $%d`, clause, order, len(args)-1, len(args))
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list news clusters: %w", err)
	}
	defer rows.Close()
	out := make([]domain.NewsClusterView, 0, limit)
	for rows.Next() {
		var view domain.NewsClusterView
		var assetsJSON, categoriesJSON, sourcesJSON, reactionsJSON []byte
		if err := rows.Scan(
			&view.ID, &view.CanonicalTitle, &view.CanonicalURL,
			&view.CanonicalSummary, &view.Language, &view.FirstPublishedAt,
			&view.FirstSeenAt, &view.LastSeenAt, &view.Importance, &view.Freshness,
			&view.Critical, &view.SourceCount, &view.PublicationCount,
			&assetsJSON, &categoriesJSON, &sourcesJSON, &reactionsJSON,
		); err != nil {
			return nil, 0, fmt.Errorf("scan news cluster: %w", err)
		}
		if err := json.Unmarshal(assetsJSON, &view.Assets); err != nil {
			return nil, 0, fmt.Errorf("decode news cluster assets: %w", err)
		}
		if err := json.Unmarshal(categoriesJSON, &view.Categories); err != nil {
			return nil, 0, fmt.Errorf("decode news cluster categories: %w", err)
		}
		if err := json.Unmarshal(sourcesJSON, &view.Sources); err != nil {
			return nil, 0, fmt.Errorf("decode news cluster sources: %w", err)
		}
		if err := json.Unmarshal(reactionsJSON, &view.Reactions); err != nil {
			return nil, 0, fmt.Errorf("decode news cluster reactions: %w", err)
		}
		out = append(out, view)
	}
	return out, total, rows.Err()
}

// GetCluster returns one cluster with its publications and reactions.
func (r *NewsRepository) GetCluster(ctx context.Context, id uuid.UUID) (domain.NewsClusterView, error) {
	items, _, err := r.ListClusters(ctx, NewsListFilter{ClusterID: &id, Limit: 1})
	if err != nil {
		return domain.NewsClusterView{}, err
	}
	if len(items) == 0 {
		return domain.NewsClusterView{}, ErrNotFound
	}
	view := items[0]
	rows, err := r.pool.Query(ctx, `
		SELECT i.id, s.id, s.name, s.priority, s.system, i.canonical_url,
		       i.title, i.summary, i.language, i.published_at, i.first_seen_at
		FROM news_items i JOIN news_sources s ON s.id = i.source_id
		WHERE i.cluster_id = $1 ORDER BY s.priority DESC, i.published_at ASC`, id)
	if err != nil {
		return domain.NewsClusterView{}, fmt.Errorf("list news publications: %w", err)
	}
	defer rows.Close()
	view.Publications = make([]domain.NewsPublication, 0, view.PublicationCount)
	for rows.Next() {
		var publication domain.NewsPublication
		if err := rows.Scan(
			&publication.ID, &publication.Source.ID, &publication.Source.Name,
			&publication.Source.Priority, &publication.Source.System,
			&publication.URL, &publication.Title, &publication.Summary,
			&publication.Language, &publication.PublishedAt, &publication.FirstSeenAt,
		); err != nil {
			return domain.NewsClusterView{}, fmt.Errorf("scan news publication: %w", err)
		}
		view.Publications = append(view.Publications, publication)
	}
	return view, rows.Err()
}

// Stats aggregates source health and ingestion counters for the health check.
func (r *NewsRepository) Stats(ctx context.Context) (domain.NewsStats, error) {
	stats := domain.NewsStats{SourcesByStatus: map[domain.NewsSourceStatus]int{}}
	if err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*), COUNT(*) FILTER (WHERE enabled),
		       (SELECT COUNT(*) FROM news_items),
		       (SELECT COUNT(*) FROM news_clusters WHERE algorithm_version = 1),
		       (SELECT COUNT(*) FROM news_clusters WHERE algorithm_version = 1 AND critical),
		       (SELECT MAX(last_seen_at) FROM news_clusters WHERE algorithm_version = 1)
		FROM news_sources`).Scan(
		&stats.SourcesTotal, &stats.SourcesEnabled, &stats.ItemsTotal,
		&stats.ClustersTotal, &stats.CriticalTotal, &stats.LastSeenAt,
	); err != nil {
		return domain.NewsStats{}, fmt.Errorf("load news stats: %w", err)
	}
	rows, err := r.pool.Query(ctx, `SELECT status, COUNT(*) FROM news_sources GROUP BY status`)
	if err != nil {
		return domain.NewsStats{}, fmt.Errorf("load news source statuses: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var status domain.NewsSourceStatus
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return domain.NewsStats{}, err
		}
		stats.SourcesByStatus[status] = count
	}
	return stats, rows.Err()
}

type scanNewsSourceFunc func(dest ...any) error

func scanNewsSource(scan scanNewsSourceFunc) (domain.NewsSource, error) {
	var source domain.NewsSource
	if err := scan(
		&source.ID, &source.Name, &source.URL, &source.CanonicalURL,
		&source.Provider, &source.Priority, &source.Enabled, &source.System,
		&source.Status, &source.ETag, &source.LastModified, &source.LastAttemptAt,
		&source.LastSuccessAt, &source.LastError, &source.ConsecutiveErrors,
		&source.CreatedAt, &source.UpdatedAt,
	); err != nil {
		return domain.NewsSource{}, fmt.Errorf("scan news source: %w", err)
	}
	return source, nil
}
