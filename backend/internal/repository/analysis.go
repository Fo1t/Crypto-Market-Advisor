package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

// AnalysisRepository stores immutable analysis runs.
type AnalysisRepository struct{ pool *pgxpool.Pool }

// Insert persists one analysis run together with its full feature snapshot.
func (r *AnalysisRepository) Insert(ctx context.Context, run domain.AnalysisRun) error {
	if run.DataQuality.MissingFields == nil {
		run.DataQuality.MissingFields = []string{}
	}
	snapshot, err := json.Marshal(run.Snapshot)
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}
	scores, err := json.Marshal(run.Scores)
	if err != nil {
		return fmt.Errorf("marshal scores: %w", err)
	}

	var decision []byte
	if run.StrategyDecision != nil {
		if decision, err = json.Marshal(run.StrategyDecision); err != nil {
			return fmt.Errorf("marshal strategy decision: %w", err)
		}
	}

	_, err = r.pool.Exec(ctx, `
		INSERT INTO analysis_runs (id, asset_id, symbol, analysis_timestamp, latest_closed_candle_time,
			price, features_snapshot, feature_vector, signal_scores, market_regime, data_quality,
			missing_fields, duration_ms, triggered_by, strategy_decision)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		run.ID, run.AssetID, run.Symbol, run.AnalysisTimestamp.UTC(), run.LatestClosedCandle,
		run.Price, snapshot, run.FeatureVector, scores, string(run.Regime), string(run.DataQuality.Status),
		run.DataQuality.MissingFields, run.DurationMS, run.TriggeredBy, decision)
	if err != nil {
		return fmt.Errorf("insert analysis run: %w", err)
	}
	return nil
}

// Latest returns the newest analysis run for an asset.
func (r *AnalysisRepository) Latest(ctx context.Context, assetID int64) (domain.AnalysisRun, error) {
	return r.scanOne(ctx, `
		SELECT id, asset_id, symbol, analysis_timestamp, latest_closed_candle_time, price,
			features_snapshot, feature_vector, signal_scores, market_regime, data_quality,
			missing_fields, duration_ms, triggered_by, created_at, strategy_decision
		FROM analysis_runs WHERE asset_id = $1 ORDER BY analysis_timestamp DESC LIMIT 1`, assetID)
}

// Get returns one analysis run by ID.
func (r *AnalysisRepository) Get(ctx context.Context, id uuid.UUID) (domain.AnalysisRun, error) {
	return r.scanOne(ctx, `
		SELECT id, asset_id, symbol, analysis_timestamp, latest_closed_candle_time, price,
			features_snapshot, feature_vector, signal_scores, market_regime, data_quality,
			missing_fields, duration_ms, triggered_by, created_at, strategy_decision
		FROM analysis_runs WHERE id = $1`, id)
}

func (r *AnalysisRepository) scanOne(ctx context.Context, q string, args ...any) (domain.AnalysisRun, error) {
	var run domain.AnalysisRun
	var snapshot, scores, decision []byte
	var regime, quality *string

	err := r.pool.QueryRow(ctx, q, args...).Scan(&run.ID, &run.AssetID, &run.Symbol,
		&run.AnalysisTimestamp, &run.LatestClosedCandle, &run.Price, &snapshot, &run.FeatureVector,
		&scores, &regime, &quality, &run.DataQuality.MissingFields, &run.DurationMS,
		&run.TriggeredBy, &run.CreatedAt, &decision)
	if err != nil {
		return run, mapNoRows(err)
	}
	if err := json.Unmarshal(snapshot, &run.Snapshot); err != nil {
		return run, fmt.Errorf("unmarshal snapshot: %w", err)
	}
	if len(scores) > 0 {
		if err := json.Unmarshal(scores, &run.Scores); err != nil {
			return run, fmt.Errorf("unmarshal scores: %w", err)
		}
	}
	if regime != nil {
		run.Regime = domain.MarketRegime(*regime)
	}
	if quality != nil {
		run.DataQuality.Status = domain.DataQualityStatus(*quality)
	}
	if len(decision) > 0 {
		var parsed domain.StrategyDecision
		if err := json.Unmarshal(decision, &parsed); err != nil {
			return run, fmt.Errorf("unmarshal strategy decision: %w", err)
		}
		run.StrategyDecision = &parsed
	}
	return run, nil
}

// VectorRow is a lightweight projection used by nearest-neighbour search.
type VectorRow struct {
	RunID     uuid.UUID
	Symbol    string
	Timestamp time.Time
	Vector    []float64
}

// Vectors returns feature vectors for similarity search, newest first.
func (r *AnalysisRepository) Vectors(ctx context.Context, limit int, excludeAfter time.Time) ([]VectorRow, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, symbol, analysis_timestamp, feature_vector
		FROM analysis_runs
		WHERE feature_vector IS NOT NULL AND analysis_timestamp < $1
		ORDER BY analysis_timestamp DESC LIMIT $2`, excludeAfter.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("query vectors: %w", err)
	}
	defer rows.Close()

	var out []VectorRow
	for rows.Next() {
		var v VectorRow
		if err := rows.Scan(&v.RunID, &v.Symbol, &v.Timestamp, &v.Vector); err != nil {
			return nil, fmt.Errorf("scan vector: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Prune deletes analysis runs older than the retention window that have no
// recommendation attached.
func (r *AnalysisRepository) Prune(ctx context.Context, olderThan time.Time) error {
	_, err := r.pool.Exec(ctx, `
		DELETE FROM analysis_runs a
		WHERE a.analysis_timestamp < $1
		  AND NOT EXISTS (SELECT 1 FROM recommendations rec WHERE rec.analysis_run_id = a.id)`,
		olderThan.UTC())
	if err != nil {
		return fmt.Errorf("prune analysis runs: %w", err)
	}
	return nil
}
