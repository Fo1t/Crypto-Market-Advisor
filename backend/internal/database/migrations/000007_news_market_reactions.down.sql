DROP INDEX IF EXISTS idx_news_market_reactions_due;
DROP INDEX IF EXISTS idx_candles_closed_asset_tf_close;

-- The old schema cannot represent missing baselines.
DELETE FROM news_market_reactions WHERE baseline_time IS NULL OR baseline_price IS NULL;

ALTER TABLE news_market_reactions
    ALTER COLUMN baseline_time SET NOT NULL,
    ALTER COLUMN baseline_price SET NOT NULL,
    DROP COLUMN IF EXISTS evaluation_version,
    DROP COLUMN IF EXISTS last_error,
    DROP COLUMN IF EXISTS completed_at,
    DROP COLUMN IF EXISTS next_evaluation_at,
    DROP COLUMN IF EXISTS status;
