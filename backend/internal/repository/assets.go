package repository

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

// AssetRepository stores the tracked cryptocurrency universe.
type AssetRepository struct{ pool *pgxpool.Pool }

const assetColumns = `id, coingecko_id, symbol, display_name, bybit_symbol, enabled,
	manually_added, pinned, excluded_from_auto_list, market_cap_rank, created_at, updated_at`

func scanAsset(row pgx.Row) (domain.Asset, error) {
	var a domain.Asset
	err := row.Scan(&a.ID, &a.CoinGeckoID, &a.Symbol, &a.DisplayName, &a.BybitSymbol, &a.Enabled,
		&a.ManuallyAdded, &a.Pinned, &a.ExcludedFromAutoList, &a.MarketCapRank, &a.CreatedAt, &a.UpdatedAt)
	return a, err
}

// List returns assets, optionally only the enabled ones.
func (r *AssetRepository) List(ctx context.Context, onlyEnabled bool) ([]domain.Asset, error) {
	q := `SELECT ` + assetColumns + ` FROM assets`
	if onlyEnabled {
		q += ` WHERE enabled`
	}
	q += ` ORDER BY COALESCE(market_cap_rank, 100000), symbol`

	rows, err := r.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list assets: %w", err)
	}
	defer rows.Close()

	var out []domain.Asset
	for rows.Next() {
		a, err := scanAsset(rows)
		if err != nil {
			return nil, fmt.Errorf("scan asset: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetByID looks up one asset.
func (r *AssetRepository) GetByID(ctx context.Context, id int64) (domain.Asset, error) {
	a, err := scanAsset(r.pool.QueryRow(ctx, `SELECT `+assetColumns+` FROM assets WHERE id = $1`, id))
	if err != nil {
		return a, mapNoRows(err)
	}
	return a, nil
}

// GetBySymbol looks up one asset by its trading symbol (case-insensitive).
func (r *AssetRepository) GetBySymbol(ctx context.Context, symbol string) (domain.Asset, error) {
	a, err := scanAsset(r.pool.QueryRow(ctx,
		`SELECT `+assetColumns+` FROM assets WHERE UPPER(symbol) = UPPER($1)`, symbol))
	if err != nil {
		return a, mapNoRows(err)
	}
	return a, nil
}

// GetByCoinGeckoID looks up one asset by provider identifier.
func (r *AssetRepository) GetByCoinGeckoID(ctx context.Context, id string) (domain.Asset, error) {
	a, err := scanAsset(r.pool.QueryRow(ctx,
		`SELECT `+assetColumns+` FROM assets WHERE coingecko_id = $1`, id))
	if err != nil {
		return a, mapNoRows(err)
	}
	return a, nil
}

// Create inserts a new asset.
func (r *AssetRepository) Create(ctx context.Context, a domain.Asset) (domain.Asset, error) {
	created, err := scanAsset(r.pool.QueryRow(ctx, `
		INSERT INTO assets (coingecko_id, symbol, display_name, bybit_symbol, enabled,
			manually_added, pinned, excluded_from_auto_list, market_cap_rank)
		VALUES ($1, UPPER($2), $3, UPPER($4), $5, $6, $7, $8, $9)
		RETURNING `+assetColumns,
		a.CoinGeckoID, a.Symbol, a.DisplayName, a.BybitSymbol, a.Enabled,
		a.ManuallyAdded, a.Pinned, a.ExcludedFromAutoList, a.MarketCapRank))
	if err != nil {
		return created, fmt.Errorf("create asset: %w", err)
	}
	return created, nil
}

// AssetFlags carries the user-controlled flags that automatic refresh must never clobber.
type AssetFlags struct {
	Enabled              *bool
	Pinned               *bool
	ExcludedFromAutoList *bool
	BybitSymbol          *string
	DisplayName          *string
}

// UpdateFlags applies a partial update of user-controlled fields.
func (r *AssetRepository) UpdateFlags(ctx context.Context, id int64, f AssetFlags) (domain.Asset, error) {
	sets := []string{"updated_at = NOW()"}
	args := []any{id}
	add := func(expr string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf("%s = $%d", expr, len(args)))
	}
	if f.Enabled != nil {
		add("enabled", *f.Enabled)
	}
	if f.Pinned != nil {
		add("pinned", *f.Pinned)
	}
	if f.ExcludedFromAutoList != nil {
		add("excluded_from_auto_list", *f.ExcludedFromAutoList)
	}
	if f.BybitSymbol != nil {
		add("bybit_symbol", strings.ToUpper(*f.BybitSymbol))
	}
	if f.DisplayName != nil {
		add("display_name", *f.DisplayName)
	}

	q := `UPDATE assets SET ` + strings.Join(sets, ", ") + ` WHERE id = $1 RETURNING ` + assetColumns
	a, err := scanAsset(r.pool.QueryRow(ctx, q, args...))
	if err != nil {
		return a, mapNoRows(err)
	}
	return a, nil
}

// UpdateRank refreshes only the market cap rank, leaving user flags untouched.
func (r *AssetRepository) UpdateRank(ctx context.Context, id int64, rank int, displayName string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE assets SET market_cap_rank = $2, display_name = $3, updated_at = NOW() WHERE id = $1`,
		id, rank, displayName)
	if err != nil {
		return fmt.Errorf("update asset rank: %w", err)
	}
	return nil
}

// Delete removes an asset and all data hanging off it.
func (r *AssetRepository) Delete(ctx context.Context, id int64) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM assets WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete asset: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Count returns the number of stored assets.
func (r *AssetRepository) Count(ctx context.Context) (int, error) {
	var n int
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM assets`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count assets: %w", err)
	}
	return n, nil
}
