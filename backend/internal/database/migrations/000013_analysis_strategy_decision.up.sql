ALTER TABLE analysis_runs
    ADD COLUMN IF NOT EXISTS strategy_decision JSONB;
