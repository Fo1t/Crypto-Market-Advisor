ALTER TABLE ohlcv_candles
    ADD COLUMN IF NOT EXISTS turnover DOUBLE PRECISION NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS provider TEXT NOT NULL DEFAULT 'coingecko';

CREATE INDEX IF NOT EXISTS idx_candles_provider
    ON ohlcv_candles (provider, asset_id, timeframe, open_time DESC);
