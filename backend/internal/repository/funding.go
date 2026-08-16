package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

// FundingRepository stores the settled funding history of the perpetuals behind
// the tracked assets.
type FundingRepository struct{ pool *pgxpool.Pool }

// UpsertMany stores a batch of settlements. A settlement that is already known
// is overwritten rather than duplicated, so a re-fetch of an overlapping window
// is harmless.
func (r *FundingRepository) UpsertMany(ctx context.Context, assetID int64, symbol string, rates []domain.FundingRate) error {
	if len(rates) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, rate := range rates {
		batch.Queue(`
            INSERT INTO funding_rates (asset_id, symbol, settled_at, rate, provider, updated_at)
            VALUES ($1, $2, $3, $4, 'bybit', now())
            ON CONFLICT (asset_id, settled_at)
            DO UPDATE SET rate = EXCLUDED.rate, symbol = EXCLUDED.symbol, updated_at = now()`,
			assetID, symbol, rate.SettledAt.UTC(), rate.Rate)
	}
	results := r.pool.SendBatch(ctx, batch)
	defer func() { _ = results.Close() }()
	for range rates {
		if _, err := results.Exec(); err != nil {
			return fmt.Errorf("upsert funding rates: %w", err)
		}
	}
	return nil
}

// Range returns the settlements of one asset inside a window, oldest first.
func (r *FundingRepository) Range(ctx context.Context, assetID int64, from, to time.Time) ([]domain.FundingRate, error) {
	rows, err := r.pool.Query(ctx, `
        SELECT settled_at, rate
        FROM funding_rates
        WHERE asset_id = $1 AND settled_at >= $2 AND settled_at <= $3
        ORDER BY settled_at`, assetID, from.UTC(), to.UTC())
	if err != nil {
		return nil, fmt.Errorf("load funding rates: %w", err)
	}
	defer rows.Close()

	var out []domain.FundingRate
	for rows.Next() {
		var rate domain.FundingRate
		if err := rows.Scan(&rate.SettledAt, &rate.Rate); err != nil {
			return nil, err
		}
		rate.SettledAt = rate.SettledAt.UTC()
		out = append(out, rate)
	}
	return out, rows.Err()
}

// LastSettledAt reports the newest stored settlement, which is where an
// incremental refresh continues from.
func (r *FundingRepository) LastSettledAt(ctx context.Context, assetID int64) (time.Time, bool, error) {
	var at *time.Time
	err := r.pool.QueryRow(ctx,
		`SELECT max(settled_at) FROM funding_rates WHERE asset_id = $1`, assetID).Scan(&at)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("last funding settlement: %w", err)
	}
	if at == nil {
		return time.Time{}, false, nil
	}
	return at.UTC(), true, nil
}
