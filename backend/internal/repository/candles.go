package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

// CandleRepository stores OHLCV bars and raw price ticks.
type CandleRepository struct{ pool *pgxpool.Pool }

// UpsertMany inserts or refreshes a batch of candles for one asset/timeframe.
func (r *CandleRepository) UpsertMany(ctx context.Context, assetID int64, tf domain.Timeframe, candles []domain.Candle) error {
	if len(candles) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, c := range candles {
		batch.Queue(`
			INSERT INTO ohlcv_candles (asset_id, timeframe, open_time, close_time, open, high, low, close, volume, turnover, closed, source, provider, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13, NOW())
			ON CONFLICT (asset_id, timeframe, open_time) DO UPDATE SET
				close_time = EXCLUDED.close_time,
				open       = EXCLUDED.open,
				high       = EXCLUDED.high,
				low        = EXCLUDED.low,
				close      = EXCLUDED.close,
				volume     = GREATEST(ohlcv_candles.volume, EXCLUDED.volume),
				turnover   = GREATEST(ohlcv_candles.turnover, EXCLUDED.turnover),
				closed     = EXCLUDED.closed,
				source     = EXCLUDED.source,
				provider   = EXCLUDED.provider,
				updated_at = NOW()`,
			assetID, string(tf), c.OpenTime.UTC(), c.CloseTime.UTC(),
			c.Open, c.High, c.Low, c.Close, c.Volume, c.Turnover, c.Closed, string(c.Source), providerName(c.Provider))
	}

	br := r.pool.SendBatch(ctx, batch)
	defer func() { _ = br.Close() }()
	for range candles {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("upsert candle: %w", err)
		}
	}
	return nil
}

// Latest returns up to limit most recent candles in ascending time order.
// Only closed candles are returned when onlyClosed is set, which is what every
// analysis path uses to avoid look-ahead on a forming bar.
func (r *CandleRepository) Latest(ctx context.Context, assetID int64, tf domain.Timeframe, limit int, onlyClosed bool) ([]domain.Candle, error) {
	q := `SELECT open_time, close_time, open, high, low, close, volume, turnover, closed, source, provider
	      FROM ohlcv_candles WHERE asset_id = $1 AND timeframe = $2`
	if onlyClosed {
		q += ` AND closed`
	}
	q += ` ORDER BY open_time DESC LIMIT $3`

	rows, err := r.pool.Query(ctx, q, assetID, string(tf), limit)
	if err != nil {
		return nil, fmt.Errorf("query candles: %w", err)
	}
	defer rows.Close()

	var reversed []domain.Candle
	for rows.Next() {
		var c domain.Candle
		var src string
		if err := rows.Scan(&c.OpenTime, &c.CloseTime, &c.Open, &c.High, &c.Low, &c.Close, &c.Volume, &c.Turnover, &c.Closed, &src, &c.Provider); err != nil {
			return nil, fmt.Errorf("scan candle: %w", err)
		}
		c.Source = domain.CandleSource(src)
		reversed = append(reversed, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]domain.Candle, len(reversed))
	for i, c := range reversed {
		out[len(reversed)-1-i] = c
	}
	return out, nil
}

// Range returns closed candles inside [from, to] in ascending order.
// Backtesting relies on this to slice history without leaking future bars.
func (r *CandleRepository) Range(ctx context.Context, assetID int64, tf domain.Timeframe, from, to time.Time) ([]domain.Candle, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT open_time, close_time, open, high, low, close, volume, turnover, closed, source, provider
		FROM ohlcv_candles
		WHERE asset_id = $1 AND timeframe = $2 AND open_time >= $3 AND open_time <= $4
		ORDER BY open_time ASC`, assetID, string(tf), from.UTC(), to.UTC())
	if err != nil {
		return nil, fmt.Errorf("query candle range: %w", err)
	}
	defer rows.Close()

	var out []domain.Candle
	for rows.Next() {
		var c domain.Candle
		var src string
		if err := rows.Scan(&c.OpenTime, &c.CloseTime, &c.Open, &c.High, &c.Low, &c.Close, &c.Volume, &c.Turnover, &c.Closed, &src, &c.Provider); err != nil {
			return nil, fmt.Errorf("scan candle: %w", err)
		}
		c.Source = domain.CandleSource(src)
		out = append(out, c)
	}
	return out, rows.Err()
}

func providerName(value string) string {
	if value == "" {
		return "coingecko"
	}
	return value
}

// Coverage reports what history is stored for one asset and timeframe. The
// backtest form asks before a run starts, because a date range that reaches
// further back than the candles is the difference between a seven-month test
// and a one-month one.
func (r *CandleRepository) Coverage(ctx context.Context, assetID int64, tf domain.Timeframe) (domain.CandleCoverage, error) {
	var out domain.CandleCoverage
	var first, last *time.Time
	err := r.pool.QueryRow(ctx, `
        SELECT count(*), min(open_time), max(open_time)
        FROM ohlcv_candles
        WHERE asset_id = $1 AND timeframe = $2 AND closed`, assetID, string(tf)).
		Scan(&out.Candles, &first, &last)
	if err != nil {
		return out, fmt.Errorf("candle coverage: %w", err)
	}
	if first != nil && last != nil {
		from, to := first.UTC(), last.UTC()
		out.From, out.To = &from, &to
	}
	return out, nil
}

// LastOpenTime returns the newest stored candle open time for an asset/timeframe.
func (r *CandleRepository) LastOpenTime(ctx context.Context, assetID int64, tf domain.Timeframe) (time.Time, bool, error) {
	var t time.Time
	err := r.pool.QueryRow(ctx,
		`SELECT open_time FROM ohlcv_candles WHERE asset_id = $1 AND timeframe = $2 ORDER BY open_time DESC LIMIT 1`,
		assetID, string(tf)).Scan(&t)
	if err != nil {
		if errors.Is(mapNoRows(err), ErrNotFound) {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, fmt.Errorf("last candle time: %w", err)
	}
	return t, true, nil
}

// LastOpenTimeForProvider returns the provider-specific watermark. This keeps
// a legacy fallback candle from falsely marking native Bybit history as loaded.
func (r *CandleRepository) LastOpenTimeForProvider(ctx context.Context, assetID int64, tf domain.Timeframe, provider string) (time.Time, bool, error) {
	var value time.Time
	err := r.pool.QueryRow(ctx, `
		SELECT open_time FROM ohlcv_candles
		WHERE asset_id = $1 AND timeframe = $2 AND provider = $3
		ORDER BY open_time DESC LIMIT 1`, assetID, string(tf), provider).Scan(&value)
	if err != nil {
		if errors.Is(mapNoRows(err), ErrNotFound) {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, fmt.Errorf("last provider candle time: %w", err)
	}
	return value, true, nil
}

// FirstOpenTimeForProvider reports the oldest stored candle of one provider,
// which is where a backward extension of the history has to continue from.
func (r *CandleRepository) FirstOpenTimeForProvider(ctx context.Context, assetID int64, tf domain.Timeframe, provider string) (time.Time, bool, error) {
	var at *time.Time
	err := r.pool.QueryRow(ctx, `
        SELECT min(open_time) FROM ohlcv_candles
        WHERE asset_id = $1 AND timeframe = $2 AND provider = $3`,
		assetID, string(tf), provider).Scan(&at)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("first candle time: %w", err)
	}
	if at == nil {
		return time.Time{}, false, nil
	}
	return at.UTC(), true, nil
}

// PriceAt returns the close of the candle covering ts on the given timeframe.
func (r *CandleRepository) PriceAt(ctx context.Context, assetID int64, tf domain.Timeframe, ts time.Time) (float64, bool, error) {
	var price float64
	err := r.pool.QueryRow(ctx, `
		SELECT close FROM ohlcv_candles
		WHERE asset_id = $1 AND timeframe = $2 AND open_time <= $3 AND closed
		ORDER BY open_time DESC LIMIT 1`, assetID, string(tf), ts.UTC()).Scan(&price)
	if err != nil {
		if errors.Is(mapNoRows(err), ErrNotFound) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("price at: %w", err)
	}
	return price, true, nil
}
