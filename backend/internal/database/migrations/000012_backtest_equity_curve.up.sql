ALTER TABLE backtest_runs
    ADD COLUMN IF NOT EXISTS equity_curve JSONB;
