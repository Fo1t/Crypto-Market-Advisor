-- Reaction rows are durable work items as well as results. Nullable baseline
-- fields let us record an explicit insufficient-data outcome instead of
-- silently losing events that have no candle history.
ALTER TABLE news_market_reactions
    ALTER COLUMN baseline_time DROP NOT NULL,
    ALTER COLUMN baseline_price DROP NOT NULL,
    ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'tracking'
        CHECK (status IN ('tracking', 'complete', 'insufficient_data')),
    ADD COLUMN IF NOT EXISTS next_evaluation_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ADD COLUMN IF NOT EXISTS completed_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS last_error TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS evaluation_version INTEGER NOT NULL DEFAULT 1
        CHECK (evaluation_version > 0);

-- Baseline and horizon lookups filter by asset, timeframe, close timestamp and
-- closed=true. This index matches that access pattern and excludes forming bars.
CREATE INDEX IF NOT EXISTS idx_candles_closed_asset_tf_close
    ON ohlcv_candles (asset_id, timeframe, close_time ASC)
    WHERE closed;

-- The scheduler only scans due, unfinished reactions.
CREATE INDEX IF NOT EXISTS idx_news_market_reactions_due
    ON news_market_reactions (next_evaluation_at, cluster_id, asset_id)
    WHERE status = 'tracking';
