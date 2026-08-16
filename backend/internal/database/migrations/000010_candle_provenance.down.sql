DROP INDEX IF EXISTS idx_candles_provider;
ALTER TABLE ohlcv_candles DROP COLUMN IF EXISTS provider;
ALTER TABLE ohlcv_candles DROP COLUMN IF EXISTS turnover;
