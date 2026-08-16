DROP INDEX IF EXISTS idx_backtest_runs_visible_time;

ALTER TABLE backtest_runs
    DROP COLUMN IF EXISTS deleted_at;
