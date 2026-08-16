-- News Intelligence schema. Provider payload details may live in JSONB, but
-- searchable entities and relations stay normalized.

CREATE TABLE IF NOT EXISTS news_sources (
    id                 UUID PRIMARY KEY,
    name               TEXT        NOT NULL,
    url                TEXT        NOT NULL,
    canonical_url      TEXT        NOT NULL UNIQUE,
    provider           TEXT        NOT NULL CHECK (provider IN ('rss', 'atom', 'bybit', 'gdelt')),
    priority           SMALLINT    NOT NULL DEFAULT 50 CHECK (priority BETWEEN 0 AND 100),
    enabled            BOOLEAN     NOT NULL DEFAULT TRUE,
    system             BOOLEAN     NOT NULL DEFAULT FALSE,
    status             TEXT        NOT NULL DEFAULT 'offline'
                                  CHECK (status IN ('online', 'degraded', 'offline', 'disabled')),
    etag               TEXT        NOT NULL DEFAULT '',
    last_modified      TEXT        NOT NULL DEFAULT '',
    last_attempt_at    TIMESTAMPTZ,
    last_success_at    TIMESTAMPTZ,
    last_error         TEXT        NOT NULL DEFAULT '',
    consecutive_errors INTEGER     NOT NULL DEFAULT 0 CHECK (consecutive_errors >= 0),
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_news_sources_enabled_priority
    ON news_sources (enabled, priority DESC, name) WHERE enabled;
CREATE INDEX IF NOT EXISTS idx_news_sources_status
    ON news_sources (status, last_success_at DESC);

CREATE TABLE IF NOT EXISTS news_clusters (
    id                  UUID PRIMARY KEY,
    canonical_title     TEXT             NOT NULL,
    canonical_source_id UUID REFERENCES news_sources (id) ON DELETE SET NULL,
    first_published_at  TIMESTAMPTZ      NOT NULL,
    first_seen_at       TIMESTAMPTZ      NOT NULL,
    last_seen_at        TIMESTAMPTZ      NOT NULL,
    importance          DOUBLE PRECISION NOT NULL DEFAULT 0
                                      CHECK (importance BETWEEN 0 AND 1),
    freshness           DOUBLE PRECISION NOT NULL DEFAULT 0
                                      CHECK (freshness BETWEEN 0 AND 1),
    critical            BOOLEAN          NOT NULL DEFAULT FALSE,
    source_count        INTEGER          NOT NULL DEFAULT 1 CHECK (source_count >= 1),
    created_at          TIMESTAMPTZ      NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ      NOT NULL DEFAULT NOW(),
    CHECK (first_seen_at <= last_seen_at)
);

CREATE INDEX IF NOT EXISTS idx_news_clusters_feed
    ON news_clusters (last_seen_at DESC, importance DESC);
CREATE INDEX IF NOT EXISTS idx_news_clusters_critical
    ON news_clusters (last_seen_at DESC) WHERE critical;
CREATE INDEX IF NOT EXISTS idx_news_clusters_canonical_source
    ON news_clusters (canonical_source_id) WHERE canonical_source_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS news_items (
    id               UUID PRIMARY KEY,
    source_id        UUID        NOT NULL REFERENCES news_sources (id) ON DELETE CASCADE,
    cluster_id       UUID        REFERENCES news_clusters (id) ON DELETE SET NULL,
    external_id      TEXT        NOT NULL DEFAULT '',
    url              TEXT        NOT NULL,
    canonical_url    TEXT        NOT NULL,
    title            TEXT        NOT NULL,
    normalized_title TEXT        NOT NULL,
    title_hash       TEXT        NOT NULL,
    summary          TEXT        NOT NULL DEFAULT '',
    language         TEXT        NOT NULL DEFAULT 'und',
    published_at     TIMESTAMPTZ NOT NULL,
    first_seen_at    TIMESTAMPTZ NOT NULL,
    last_seen_at     TIMESTAMPTZ NOT NULL,
    raw_metadata     JSONB       NOT NULL DEFAULT '{}'::JSONB,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (first_seen_at <= last_seen_at)
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_news_items_source_external_id
    ON news_items (source_id, external_id) WHERE external_id <> '';
CREATE UNIQUE INDEX IF NOT EXISTS uq_news_items_canonical_url
    ON news_items (canonical_url) WHERE canonical_url <> '';
CREATE INDEX IF NOT EXISTS idx_news_items_source_published
    ON news_items (source_id, published_at DESC);
CREATE INDEX IF NOT EXISTS idx_news_items_cluster
    ON news_items (cluster_id, published_at DESC) WHERE cluster_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_news_items_first_seen
    ON news_items (first_seen_at DESC);
CREATE INDEX IF NOT EXISTS idx_news_items_title_hash
    ON news_items (title_hash, published_at DESC);

CREATE TABLE IF NOT EXISTS news_item_assets (
    item_id    UUID             NOT NULL REFERENCES news_items (id) ON DELETE CASCADE,
    asset_id   BIGINT           NOT NULL REFERENCES assets (id) ON DELETE CASCADE,
    confidence DOUBLE PRECISION NOT NULL DEFAULT 1 CHECK (confidence BETWEEN 0 AND 1),
    matched_by TEXT             NOT NULL DEFAULT 'rule',
    PRIMARY KEY (item_id, asset_id)
);

CREATE INDEX IF NOT EXISTS idx_news_item_assets_asset
    ON news_item_assets (asset_id, item_id);

CREATE TABLE IF NOT EXISTS news_cluster_assets (
    cluster_id UUID             NOT NULL REFERENCES news_clusters (id) ON DELETE CASCADE,
    asset_id   BIGINT           NOT NULL REFERENCES assets (id) ON DELETE CASCADE,
    confidence DOUBLE PRECISION NOT NULL DEFAULT 1 CHECK (confidence BETWEEN 0 AND 1),
    PRIMARY KEY (cluster_id, asset_id)
);

CREATE INDEX IF NOT EXISTS idx_news_cluster_assets_asset
    ON news_cluster_assets (asset_id, cluster_id);

CREATE TABLE IF NOT EXISTS news_cluster_categories (
    cluster_id UUID NOT NULL REFERENCES news_clusters (id) ON DELETE CASCADE,
    category   TEXT NOT NULL CHECK (category IN (
        'regulation', 'listing', 'delisting', 'security', 'protocol',
        'macro', 'exchange', 'partnership', 'other'
    )),
    confidence DOUBLE PRECISION NOT NULL DEFAULT 1 CHECK (confidence BETWEEN 0 AND 1),
    PRIMARY KEY (cluster_id, category)
);

CREATE INDEX IF NOT EXISTS idx_news_cluster_categories_category
    ON news_cluster_categories (category, cluster_id);

CREATE TABLE IF NOT EXISTS news_market_reactions (
    cluster_id       UUID             NOT NULL REFERENCES news_clusters (id) ON DELETE CASCADE,
    asset_id         BIGINT           NOT NULL REFERENCES assets (id) ON DELETE CASCADE,
    baseline_time    TIMESTAMPTZ      NOT NULL,
    baseline_price   DOUBLE PRECISION NOT NULL CHECK (baseline_price > 0),
    return_5m_pct    DOUBLE PRECISION,
    return_15m_pct   DOUBLE PRECISION,
    return_1h_pct    DOUBLE PRECISION,
    return_4h_pct    DOUBLE PRECISION,
    return_24h_pct   DOUBLE PRECISION,
    max_up_pct       DOUBLE PRECISION,
    max_down_pct     DOUBLE PRECISION,
    observed_through TIMESTAMPTZ,
    created_at       TIMESTAMPTZ      NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ      NOT NULL DEFAULT NOW(),
    PRIMARY KEY (cluster_id, asset_id)
);

CREATE INDEX IF NOT EXISTS idx_news_market_reactions_asset_baseline
    ON news_market_reactions (asset_id, baseline_time DESC);

-- Public, no-key providers verified against their official endpoints. Sources
-- remain editable/disableable through the later source-management API.
INSERT INTO news_sources (id, name, url, canonical_url, provider, priority, enabled, system)
VALUES
    ('10000000-0000-4000-8000-000000000001', 'Bybit Announcements',
     'https://api.bybit.com/v5/announcements/index?locale=en-US',
     'https://api.bybit.com/v5/announcements/index?locale=en-US', 'bybit', 100, TRUE, TRUE),
    ('10000000-0000-4000-8000-000000000002', 'Ethereum Foundation',
     'https://blog.ethereum.org/feed.xml', 'https://blog.ethereum.org/feed.xml', 'atom', 90, TRUE, TRUE),
    ('10000000-0000-4000-8000-000000000003', 'CoinDesk',
     'https://www.coindesk.com/arc/outboundfeeds/rss/',
     'https://www.coindesk.com/arc/outboundfeeds/rss/', 'rss', 70, TRUE, TRUE),
    ('10000000-0000-4000-8000-000000000004', 'Cointelegraph',
     'https://cointelegraph.com/rss', 'https://cointelegraph.com/rss', 'rss', 60, TRUE, TRUE)
ON CONFLICT (id) DO NOTHING;
