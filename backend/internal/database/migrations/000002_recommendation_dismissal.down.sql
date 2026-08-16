DROP INDEX IF EXISTS idx_recommendations_visibility_time;

ALTER TABLE recommendations
    DROP COLUMN IF EXISTS dismissed_at;
