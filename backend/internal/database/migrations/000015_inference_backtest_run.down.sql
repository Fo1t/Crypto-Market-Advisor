DROP INDEX IF EXISTS idx_llm_inferences_backtest_run;

ALTER TABLE llm_inferences
    DROP COLUMN IF EXISTS backtest_run_id;
