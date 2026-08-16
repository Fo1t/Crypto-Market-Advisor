DROP INDEX IF EXISTS idx_news_items_unclustered;
DROP TABLE IF EXISTS news_asset_aliases;

DELETE FROM news_cluster_categories
WHERE category NOT IN (
    'regulation', 'listing', 'delisting', 'security', 'protocol',
    'macro', 'exchange', 'partnership', 'other'
);

ALTER TABLE news_cluster_categories
    DROP CONSTRAINT IF EXISTS news_cluster_categories_category_check;
ALTER TABLE news_cluster_categories
    ADD CONSTRAINT news_cluster_categories_category_check CHECK (category IN (
        'regulation', 'listing', 'delisting', 'security', 'protocol',
        'macro', 'exchange', 'partnership', 'other'
    ));
