ALTER TABLE recommendations
    ADD COLUMN IF NOT EXISTS news_assessment JSONB;
