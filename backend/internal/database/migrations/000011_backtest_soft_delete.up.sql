ALTER TABLE backtest_runs
    ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_backtest_runs_visible_time
    ON backtest_runs (created_at DESC)
    WHERE deleted_at IS NULL;
