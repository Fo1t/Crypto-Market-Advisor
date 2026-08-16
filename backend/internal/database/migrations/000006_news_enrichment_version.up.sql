-- Version deterministic enrichment so rule changes can safely reprocess stored
-- raw items without deleting them or losing first_seen_at.
ALTER TABLE news_items
    ADD COLUMN IF NOT EXISTS enrichment_version INTEGER NOT NULL DEFAULT 0
    CHECK (enrichment_version >= 0);
ALTER TABLE news_clusters
    ADD COLUMN IF NOT EXISTS algorithm_version INTEGER NOT NULL DEFAULT 0
    CHECK (algorithm_version >= 0);

DROP INDEX IF EXISTS idx_news_items_unclustered;
CREATE INDEX idx_news_items_pending_enrichment
    ON news_items (enrichment_version ASC, first_seen_at ASC, id ASC)
    WHERE enrichment_version < 1;

CREATE INDEX idx_news_clusters_algorithm_window
    ON news_clusters (algorithm_version, last_seen_at DESC, importance DESC);
