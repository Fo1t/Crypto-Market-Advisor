-- Ties every stored inference to the backtest run that paid for it. Without it
-- a replay's answers can only be found by timestamp, which stops working as soon
-- as two runs overlap or a live analysis happens in between.
ALTER TABLE llm_inferences
    ADD COLUMN IF NOT EXISTS backtest_run_id UUID REFERENCES backtest_runs (id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_llm_inferences_backtest_run
    ON llm_inferences (backtest_run_id, created_at)
    WHERE backtest_run_id IS NOT NULL;
