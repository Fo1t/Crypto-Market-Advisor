package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

// MarketRepository stores market overview snapshots.
type MarketRepository struct{ pool *pgxpool.Pool }

// Insert stores one market snapshot.
func (r *MarketRepository) Insert(ctx context.Context, assetID int64, info domain.MarketInfo) error {
	raw, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("marshal market info: %w", err)
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO market_snapshots (asset_id, captured_at, price, market_cap, volume_24h,
			price_change_24h_pct, price_change_1h_pct, price_change_7d_pct, high_24h, low_24h, raw)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		assetID, info.FetchedAt.UTC(), info.Price, info.MarketCap, info.Volume24h,
		info.PriceChange24hPct, info.PriceChange1hPct, info.PriceChange7dPct, info.High24h, info.Low24h, raw)
	if err != nil {
		return fmt.Errorf("insert market snapshot: %w", err)
	}
	return nil
}

// Latest returns the most recent snapshot for an asset.
func (r *MarketRepository) Latest(ctx context.Context, assetID int64) (domain.MarketInfo, error) {
	var raw []byte
	err := r.pool.QueryRow(ctx,
		`SELECT raw FROM market_snapshots WHERE asset_id = $1 ORDER BY captured_at DESC LIMIT 1`, assetID).Scan(&raw)
	if err != nil {
		return domain.MarketInfo{}, mapNoRows(err)
	}
	var info domain.MarketInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return info, fmt.Errorf("unmarshal market info: %w", err)
	}
	return info, nil
}

// LatestForAll returns the newest snapshot per asset, keyed by asset ID.
func (r *MarketRepository) LatestForAll(ctx context.Context) (map[int64]domain.MarketInfo, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT ON (asset_id) asset_id, raw
		FROM market_snapshots
		ORDER BY asset_id, captured_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("query latest snapshots: %w", err)
	}
	defer rows.Close()

	out := make(map[int64]domain.MarketInfo)
	for rows.Next() {
		var id int64
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, fmt.Errorf("scan snapshot: %w", err)
		}
		var info domain.MarketInfo
		if err := json.Unmarshal(raw, &info); err != nil {
			continue
		}
		out[id] = info
	}
	return out, rows.Err()
}

// Prune deletes snapshots older than the retention window.
func (r *MarketRepository) Prune(ctx context.Context, olderThan time.Time) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM market_snapshots WHERE captured_at < $1`, olderThan.UTC())
	if err != nil {
		return fmt.Errorf("prune market snapshots: %w", err)
	}
	return nil
}

// StatusRepository stores operational timestamps for the observability panel.
type StatusRepository struct{ pool *pgxpool.Pool }

// StatusEntry is one operational fact, e.g. "last successful analysis".
type StatusEntry struct {
	Key        string    `json:"key"`
	Status     string    `json:"status"`
	Message    string    `json:"message"`
	OccurredAt time.Time `json:"occurred_at"`
}

// Set upserts one status entry.
func (r *StatusRepository) Set(ctx context.Context, key, status, message string, at time.Time) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO system_status (key, status, message, occurred_at) VALUES ($1,$2,$3,$4)
		ON CONFLICT (key) DO UPDATE SET status = EXCLUDED.status, message = EXCLUDED.message, occurred_at = EXCLUDED.occurred_at`,
		key, status, message, at.UTC())
	if err != nil {
		return fmt.Errorf("set status: %w", err)
	}
	return nil
}

// All returns every status entry.
func (r *StatusRepository) All(ctx context.Context) ([]StatusEntry, error) {
	rows, err := r.pool.Query(ctx, `SELECT key, status, message, occurred_at FROM system_status ORDER BY key`)
	if err != nil {
		return nil, fmt.Errorf("query status: %w", err)
	}
	defer rows.Close()

	var out []StatusEntry
	for rows.Next() {
		var e StatusEntry
		if err := rows.Scan(&e.Key, &e.Status, &e.Message, &e.OccurredAt); err != nil {
			return nil, fmt.Errorf("scan status: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// SettingsRepository persists UI-editable settings as JSON documents.
type SettingsRepository struct{ pool *pgxpool.Pool }

// Get reads one settings document into v.
func (r *SettingsRepository) Get(ctx context.Context, key string, v any) (bool, error) {
	var raw []byte
	err := r.pool.QueryRow(ctx, `SELECT value FROM app_settings WHERE key = $1`, key).Scan(&raw)
	if err != nil {
		if errors.Is(mapNoRows(err), ErrNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("get settings: %w", err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return false, fmt.Errorf("unmarshal settings: %w", err)
	}
	return true, nil
}

// Put writes one settings document.
func (r *SettingsRepository) Put(ctx context.Context, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO app_settings (key, value, updated_at) VALUES ($1,$2,NOW())
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()`, key, raw)
	if err != nil {
		return fmt.Errorf("put settings: %w", err)
	}
	return nil
}
