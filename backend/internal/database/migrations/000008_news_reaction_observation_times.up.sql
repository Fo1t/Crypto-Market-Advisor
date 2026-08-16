ALTER TABLE news_market_reactions
    ADD COLUMN IF NOT EXISTS return_5m_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS return_15m_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS return_1h_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS return_4h_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS return_24h_at TIMESTAMPTZ;

-- Existing v7 rows were calculated from exact 5m boundaries. Backfill their
-- observation timestamps conservatively from the baseline; new rows always
-- persist the actual candle close selected by the tracker.
UPDATE news_market_reactions SET return_5m_at = baseline_time + INTERVAL '5 minutes'
WHERE return_5m_pct IS NOT NULL AND return_5m_at IS NULL;

UPDATE news_market_reactions SET return_15m_at = baseline_time + INTERVAL '15 minutes'
WHERE return_15m_pct IS NOT NULL AND return_15m_at IS NULL;

UPDATE news_market_reactions SET return_1h_at = baseline_time + INTERVAL '1 hour'
WHERE return_1h_pct IS NOT NULL AND return_1h_at IS NULL;

UPDATE news_market_reactions SET return_4h_at = baseline_time + INTERVAL '4 hours'
WHERE return_4h_pct IS NOT NULL AND return_4h_at IS NULL;

UPDATE news_market_reactions SET return_24h_at = baseline_time + INTERVAL '24 hours'
WHERE return_24h_pct IS NOT NULL AND return_24h_at IS NULL;
