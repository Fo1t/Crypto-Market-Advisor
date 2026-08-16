ALTER TABLE recommendations
    ADD COLUMN IF NOT EXISTS dismissed_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_recommendations_visibility_time
    ON recommendations (dismissed_at, created_at DESC);
