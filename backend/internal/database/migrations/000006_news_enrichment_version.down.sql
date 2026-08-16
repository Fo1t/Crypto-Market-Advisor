DROP INDEX IF EXISTS idx_news_clusters_algorithm_window;
DROP INDEX IF EXISTS idx_news_items_pending_enrichment;

ALTER TABLE news_clusters DROP COLUMN IF EXISTS algorithm_version;
ALTER TABLE news_items DROP COLUMN IF EXISTS enrichment_version;

CREATE INDEX IF NOT EXISTS idx_news_items_unclustered
    ON news_items (first_seen_at ASC, id ASC) WHERE cluster_id IS NULL;
