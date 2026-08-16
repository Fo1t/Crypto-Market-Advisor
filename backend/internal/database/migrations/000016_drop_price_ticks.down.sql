-- Recreates the table exactly as migration 000001 defined it. The observations
-- themselves are not restored: they were a 48-hour rolling sample.
CREATE TABLE IF NOT EXISTS price_ticks (
    asset_id  BIGINT           NOT NULL REFERENCES assets (id) ON DELETE CASCADE,
    ts        TIMESTAMPTZ      NOT NULL,
    price     DOUBLE PRECISION NOT NULL,
    volume    DOUBLE PRECISION NOT NULL DEFAULT 0,
    PRIMARY KEY (asset_id, ts)
);
