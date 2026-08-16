package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

// ListDueReactions returns new cluster/asset pairs and unfinished rows whose
// next horizon is due. Obsolete enrichment versions are never evaluated.
func (r *NewsRepository) ListDueReactions(ctx context.Context, now time.Time, limit int) ([]domain.NewsReactionWork, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := r.pool.Query(ctx, `
		SELECT c.id, ca.asset_id, c.first_seen_at,
		       r.cluster_id IS NOT NULL,
		       r.baseline_time, r.baseline_price,
		       r.return_5m_pct, r.return_5m_at,
		       r.return_15m_pct, r.return_15m_at,
		       r.return_1h_pct, r.return_1h_at,
		       r.return_4h_pct, r.return_4h_at,
		       r.return_24h_pct, r.return_24h_at,
		       r.max_up_pct, r.max_down_pct, r.observed_through,
		       r.status, r.next_evaluation_at, r.completed_at,
		       r.last_error, r.evaluation_version
		FROM news_clusters c
		JOIN news_cluster_assets ca ON ca.cluster_id = c.id
		LEFT JOIN news_market_reactions r
		  ON r.cluster_id = c.id AND r.asset_id = ca.asset_id
		WHERE c.algorithm_version = 1
		  AND (r.cluster_id IS NULL OR (r.status = 'tracking' AND r.next_evaluation_at <= $1))
		ORDER BY COALESCE(r.next_evaluation_at, c.first_seen_at) ASC, c.id, ca.asset_id
		LIMIT $2`, now.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("list due news reactions: %w", err)
	}
	defer rows.Close()

	out := make([]domain.NewsReactionWork, 0, limit)
	for rows.Next() {
		var work domain.NewsReactionWork
		var exists bool
		var status *string
		var nextEvaluationAt *time.Time
		var lastError *string
		var evaluationVersion *int
		var reaction domain.NewsMarketReaction
		if err := rows.Scan(
			&work.ClusterID, &work.AssetID, &work.FirstSeenAt, &exists,
			&reaction.BaselineTime, &reaction.BaselinePrice,
			&reaction.Return5mPct, &reaction.Return5mAt,
			&reaction.Return15mPct, &reaction.Return15mAt,
			&reaction.Return1hPct, &reaction.Return1hAt,
			&reaction.Return4hPct, &reaction.Return4hAt,
			&reaction.Return24hPct, &reaction.Return24hAt,
			&reaction.MaxUpPct, &reaction.MaxDownPct, &reaction.ObservedThrough,
			&status, &nextEvaluationAt, &reaction.CompletedAt,
			&lastError, &evaluationVersion,
		); err != nil {
			return nil, fmt.Errorf("scan due news reaction: %w", err)
		}
		if exists {
			reaction.ClusterID = work.ClusterID
			reaction.AssetID = work.AssetID
			if status != nil {
				reaction.Status = domain.NewsReactionStatus(*status)
			}
			if nextEvaluationAt != nil {
				reaction.NextEvaluationAt = *nextEvaluationAt
			}
			if lastError != nil {
				reaction.LastError = *lastError
			}
			if evaluationVersion != nil {
				reaction.EvaluationVersion = *evaluationVersion
			}
			work.Reaction = &reaction
		}
		out = append(out, work)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate due news reactions: %w", err)
	}
	return out, nil
}

// FirstClosed5mCandle returns the first candle whose close was observable in
// [at, knownAt]. The upper bound is the no-future-leakage guard.
func (r *NewsRepository) FirstClosed5mCandle(ctx context.Context, assetID int64, at, knownAt time.Time) (domain.Candle, bool, error) {
	var candle domain.Candle
	var source string
	err := r.pool.QueryRow(ctx, `
		SELECT open_time, close_time, open, high, low, close, volume, turnover, closed, source, provider
		FROM ohlcv_candles
		WHERE asset_id = $1 AND timeframe = '5m' AND closed
		  AND close_time >= $2 AND close_time <= $3
		ORDER BY close_time ASC
		LIMIT 1`, assetID, at.UTC(), knownAt.UTC()).Scan(
		&candle.OpenTime, &candle.CloseTime, &candle.Open, &candle.High,
		&candle.Low, &candle.Close, &candle.Volume, &candle.Turnover, &candle.Closed, &source, &candle.Provider,
	)
	if err != nil {
		if errors.Is(mapNoRows(err), ErrNotFound) {
			return domain.Candle{}, false, nil
		}
		return domain.Candle{}, false, fmt.Errorf("find closed reaction candle: %w", err)
	}
	candle.Source = domain.CandleSource(source)
	return candle, true, nil
}

// ReactionExtremes calculates MFE/MAE on bars closed after baseline and no
// later than knownAt. observedThrough is the exact last bar included.
func (r *NewsRepository) ReactionExtremes(ctx context.Context, assetID int64, baselineTime, knownAt time.Time) (maxHigh, minLow *float64, observedThrough *time.Time, err error) {
	err = r.pool.QueryRow(ctx, `
		SELECT MAX(high), MIN(low), MAX(close_time)
		FROM ohlcv_candles
		WHERE asset_id = $1 AND timeframe = '5m' AND closed
		  AND close_time > $2 AND close_time <= $3`, assetID, baselineTime.UTC(), knownAt.UTC()).Scan(
		&maxHigh, &minLow, &observedThrough,
	)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("calculate news reaction extremes: %w", err)
	}
	return maxHigh, minLow, observedThrough, nil
}

// UpsertReaction persists partial progress atomically. Missing horizon values
// do not erase values recorded by an earlier pass.
func (r *NewsRepository) UpsertReaction(ctx context.Context, reaction domain.NewsMarketReaction) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO news_market_reactions (
			cluster_id, asset_id, baseline_time, baseline_price,
			return_5m_pct, return_5m_at, return_15m_pct, return_15m_at,
			return_1h_pct, return_1h_at, return_4h_pct, return_4h_at,
			return_24h_pct, return_24h_at, max_up_pct, max_down_pct,
			observed_through, status, next_evaluation_at, completed_at,
			last_error, evaluation_version
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)
		ON CONFLICT (cluster_id, asset_id) DO UPDATE SET
			baseline_time = COALESCE(EXCLUDED.baseline_time, news_market_reactions.baseline_time),
			baseline_price = COALESCE(EXCLUDED.baseline_price, news_market_reactions.baseline_price),
			return_5m_pct = COALESCE(EXCLUDED.return_5m_pct, news_market_reactions.return_5m_pct),
			return_5m_at = COALESCE(EXCLUDED.return_5m_at, news_market_reactions.return_5m_at),
			return_15m_pct = COALESCE(EXCLUDED.return_15m_pct, news_market_reactions.return_15m_pct),
			return_15m_at = COALESCE(EXCLUDED.return_15m_at, news_market_reactions.return_15m_at),
			return_1h_pct = COALESCE(EXCLUDED.return_1h_pct, news_market_reactions.return_1h_pct),
			return_1h_at = COALESCE(EXCLUDED.return_1h_at, news_market_reactions.return_1h_at),
			return_4h_pct = COALESCE(EXCLUDED.return_4h_pct, news_market_reactions.return_4h_pct),
			return_4h_at = COALESCE(EXCLUDED.return_4h_at, news_market_reactions.return_4h_at),
			return_24h_pct = COALESCE(EXCLUDED.return_24h_pct, news_market_reactions.return_24h_pct),
			return_24h_at = COALESCE(EXCLUDED.return_24h_at, news_market_reactions.return_24h_at),
			max_up_pct = COALESCE(EXCLUDED.max_up_pct, news_market_reactions.max_up_pct),
			max_down_pct = COALESCE(EXCLUDED.max_down_pct, news_market_reactions.max_down_pct),
			observed_through = COALESCE(EXCLUDED.observed_through, news_market_reactions.observed_through),
			status = EXCLUDED.status,
			next_evaluation_at = EXCLUDED.next_evaluation_at,
			completed_at = EXCLUDED.completed_at,
			last_error = EXCLUDED.last_error,
			evaluation_version = EXCLUDED.evaluation_version,
			updated_at = NOW()`,
		reaction.ClusterID, reaction.AssetID, reaction.BaselineTime, reaction.BaselinePrice,
		reaction.Return5mPct, reaction.Return5mAt,
		reaction.Return15mPct, reaction.Return15mAt,
		reaction.Return1hPct, reaction.Return1hAt,
		reaction.Return4hPct, reaction.Return4hAt,
		reaction.Return24hPct, reaction.Return24hAt, reaction.MaxUpPct,
		reaction.MaxDownPct, reaction.ObservedThrough, reaction.Status,
		reaction.NextEvaluationAt.UTC(), reaction.CompletedAt, reaction.LastError,
		reaction.EvaluationVersion,
	)
	if err != nil {
		return fmt.Errorf("upsert news market reaction: %w", err)
	}
	return nil
}

// ReactionHistory returns only samples knowable at knownAt. A 24h value cannot
// enter the aggregate before baseline_time+24h even if persisted prematurely.
func (r *NewsRepository) ReactionHistory(ctx context.Context, assetID int64, category domain.NewsCategory, knownAt time.Time, minSample int) (domain.NewsReactionHistory, error) {
	if minSample < 1 {
		minSample = 1
	}
	categoryFilter := ""
	args := []any{assetID, knownAt.UTC()}
	if category != "" {
		categoryFilter = `AND EXISTS (
			SELECT 1 FROM news_cluster_categories cc
			WHERE cc.cluster_id = r.cluster_id AND cc.category = $3)`
		args = append(args, category)
	}
	query := fmt.Sprintf(`
		SELECT
			COUNT(return_1h_pct) FILTER (WHERE return_1h_at <= $2),
			COUNT(return_24h_pct) FILTER (WHERE return_24h_at <= $2),
			AVG(return_1h_pct) FILTER (WHERE return_1h_at <= $2),
			AVG(return_24h_pct) FILTER (WHERE return_24h_at <= $2),
			100.0 * AVG(CASE WHEN return_1h_pct > 0 THEN 1.0 ELSE 0.0 END)
				FILTER (WHERE return_1h_pct IS NOT NULL AND return_1h_at <= $2)
		FROM news_market_reactions r
		JOIN news_clusters c ON c.id = r.cluster_id
		WHERE r.asset_id = $1 AND c.algorithm_version = 1
		  AND c.first_seen_at <= $2 AND r.baseline_time IS NOT NULL
		  %s`, categoryFilter)
	var history domain.NewsReactionHistory
	if err := r.pool.QueryRow(ctx, query, args...).Scan(
		&history.SampleSize, &history.SampleSize24h, &history.Return1hAvg,
		&history.Return24hAvg, &history.WinRate1h,
	); err != nil {
		return domain.NewsReactionHistory{}, fmt.Errorf("load news reaction history: %w", err)
	}
	if history.SampleSize < minSample {
		history.Status = "insufficient_history"
		history.Return1hAvg = nil
		history.Return24hAvg = nil
		history.WinRate1h = nil
	} else if history.SampleSize24h < minSample {
		history.Status = "partial_history"
		history.Return24hAvg = nil
	} else {
		history.Status = "ok"
	}
	return history, nil
}
