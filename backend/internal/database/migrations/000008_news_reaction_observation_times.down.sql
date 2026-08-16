ALTER TABLE news_market_reactions
    DROP COLUMN IF EXISTS return_24h_at,
    DROP COLUMN IF EXISTS return_4h_at,
    DROP COLUMN IF EXISTS return_1h_at,
    DROP COLUMN IF EXISTS return_15m_at,
    DROP COLUMN IF EXISTS return_5m_at;
