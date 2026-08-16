-- Enrichment support: the full stable category taxonomy, configurable asset
-- aliases, and the hot-path index for pending clustering work.

ALTER TABLE news_cluster_categories
    DROP CONSTRAINT IF EXISTS news_cluster_categories_category_check;
ALTER TABLE news_cluster_categories
    ADD CONSTRAINT news_cluster_categories_category_check CHECK (category IN (
        'market', 'regulation', 'legal', 'security', 'exploit', 'hack',
        'exchange', 'listing', 'delisting', 'trading_suspension',
        'protocol', 'network_upgrade', 'network_outage', 'etf',
        'institutional', 'macro', 'mining', 'stablecoin', 'defi',
        'tokenomics', 'partnership', 'other'
    ));

CREATE TABLE IF NOT EXISTS news_asset_aliases (
    id             BIGSERIAL PRIMARY KEY,
    asset_id       BIGINT      NOT NULL REFERENCES assets (id) ON DELETE CASCADE,
    alias          TEXT        NOT NULL,
    case_sensitive BOOLEAN     NOT NULL DEFAULT FALSE,
    enabled        BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (BTRIM(alias) <> '')
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_news_asset_aliases_asset_alias
    ON news_asset_aliases (asset_id, LOWER(alias));
CREATE INDEX IF NOT EXISTS idx_news_asset_aliases_enabled
    ON news_asset_aliases (asset_id, alias) WHERE enabled;

CREATE INDEX IF NOT EXISTS idx_news_items_unclustered
    ON news_items (first_seen_at ASC, id ASC) WHERE cluster_id IS NULL;
