-- Core schema for Crypto Market Advisor.
-- Money values use NUMERIC(38,18); analytical values use double precision.

CREATE TABLE IF NOT EXISTS assets (
    id                       BIGSERIAL PRIMARY KEY,
    coingecko_id             TEXT        NOT NULL UNIQUE,
    symbol                   TEXT        NOT NULL UNIQUE,
    display_name             TEXT        NOT NULL,
    bybit_symbol             TEXT        NOT NULL DEFAULT '',
    enabled                  BOOLEAN     NOT NULL DEFAULT TRUE,
    manually_added           BOOLEAN     NOT NULL DEFAULT FALSE,
    pinned                   BOOLEAN     NOT NULL DEFAULT FALSE,
    excluded_from_auto_list  BOOLEAN     NOT NULL DEFAULT FALSE,
    market_cap_rank          INTEGER,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_assets_enabled ON assets (enabled) WHERE enabled;

-- OHLCV candles. `source` records whether the bar is native provider data,
-- aggregated from a finer timeframe, or synthesised from polled prices.
CREATE TABLE IF NOT EXISTS ohlcv_candles (
    asset_id    BIGINT      NOT NULL REFERENCES assets (id) ON DELETE CASCADE,
    timeframe   TEXT        NOT NULL,
    open_time   TIMESTAMPTZ NOT NULL,
    close_time  TIMESTAMPTZ NOT NULL,
    open        DOUBLE PRECISION NOT NULL,
    high        DOUBLE PRECISION NOT NULL,
    low         DOUBLE PRECISION NOT NULL,
    close       DOUBLE PRECISION NOT NULL,
    volume      DOUBLE PRECISION NOT NULL DEFAULT 0,
    closed      BOOLEAN     NOT NULL DEFAULT TRUE,
    source      TEXT        NOT NULL DEFAULT 'native',
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (asset_id, timeframe, open_time)
);

CREATE INDEX IF NOT EXISTS idx_candles_lookup ON ohlcv_candles (asset_id, timeframe, open_time DESC);

-- Sampled prices used to build the fastest timeframes.
CREATE TABLE IF NOT EXISTS price_ticks (
    asset_id  BIGINT           NOT NULL REFERENCES assets (id) ON DELETE CASCADE,
    ts        TIMESTAMPTZ      NOT NULL,
    price     DOUBLE PRECISION NOT NULL,
    volume    DOUBLE PRECISION NOT NULL DEFAULT 0,
    PRIMARY KEY (asset_id, ts)
);

CREATE TABLE IF NOT EXISTS market_snapshots (
    id                   BIGSERIAL PRIMARY KEY,
    asset_id             BIGINT           NOT NULL REFERENCES assets (id) ON DELETE CASCADE,
    captured_at          TIMESTAMPTZ      NOT NULL DEFAULT NOW(),
    price                DOUBLE PRECISION NOT NULL,
    market_cap           DOUBLE PRECISION,
    volume_24h           DOUBLE PRECISION,
    price_change_24h_pct DOUBLE PRECISION,
    price_change_1h_pct  DOUBLE PRECISION,
    price_change_7d_pct  DOUBLE PRECISION,
    high_24h             DOUBLE PRECISION,
    low_24h              DOUBLE PRECISION,
    raw                  JSONB
);

CREATE INDEX IF NOT EXISTS idx_market_snapshots_asset ON market_snapshots (asset_id, captured_at DESC);

-- One row per analysis cycle for one symbol. Immutable once written.
CREATE TABLE IF NOT EXISTS analysis_runs (
    id                          UUID PRIMARY KEY,
    asset_id                    BIGINT      NOT NULL REFERENCES assets (id) ON DELETE CASCADE,
    symbol                      TEXT        NOT NULL,
    analysis_timestamp          TIMESTAMPTZ NOT NULL,
    latest_closed_candle_time   TIMESTAMPTZ,
    price                       DOUBLE PRECISION NOT NULL,
    features_snapshot           JSONB       NOT NULL,
    feature_vector              DOUBLE PRECISION[],
    signal_scores               JSONB       NOT NULL DEFAULT '{}'::JSONB,
    market_regime               TEXT,
    data_quality                TEXT        NOT NULL DEFAULT 'ok',
    missing_fields              TEXT[]      NOT NULL DEFAULT ARRAY[]::TEXT[],
    duration_ms                 INTEGER     NOT NULL DEFAULT 0,
    triggered_by                TEXT        NOT NULL DEFAULT 'scheduler',
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_analysis_runs_asset_time ON analysis_runs (asset_id, analysis_timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_analysis_runs_time ON analysis_runs (analysis_timestamp DESC);

-- Predictions are immutable: user decisions and outcomes live in their own tables.
CREATE TABLE IF NOT EXISTS recommendations (
    id                    UUID PRIMARY KEY,
    analysis_run_id       UUID        REFERENCES analysis_runs (id) ON DELETE SET NULL,
    asset_id              BIGINT      NOT NULL REFERENCES assets (id) ON DELETE CASCADE,
    symbol                TEXT        NOT NULL,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    action                TEXT        NOT NULL,
    confidence            INTEGER     NOT NULL,
    risk_level            TEXT        NOT NULL,
    summary               TEXT        NOT NULL DEFAULT '',
    reference_price       NUMERIC(38,18) NOT NULL,
    allocation_pct        NUMERIC(38,18) NOT NULL DEFAULT 0,
    llm_leverage          INTEGER     NOT NULL DEFAULT 0,
    risk_max_leverage     INTEGER     NOT NULL DEFAULT 0,
    final_leverage        INTEGER     NOT NULL DEFAULT 0,
    leverage_reason       TEXT        NOT NULL DEFAULT '',
    entry                 JSONB,
    take_profit           JSONB       NOT NULL DEFAULT '[]'::JSONB,
    stop_loss             JSONB       NOT NULL DEFAULT '[]'::JSONB,
    management            JSONB,
    signals_for           TEXT[]      NOT NULL DEFAULT ARRAY[]::TEXT[],
    signals_against       TEXT[]      NOT NULL DEFAULT ARRAY[]::TEXT[],
    invalidation          TEXT[]      NOT NULL DEFAULT ARRAY[]::TEXT[],
    model_name            TEXT        NOT NULL DEFAULT '',
    prompt_version        TEXT        NOT NULL DEFAULT '',
    schema_version        INTEGER     NOT NULL DEFAULT 1,
    risk_engine_output    JSONB,
    validated_output      JSONB,
    market_regime         TEXT,
    data_quality          TEXT        NOT NULL DEFAULT 'ok'
);

CREATE INDEX IF NOT EXISTS idx_recommendations_asset_time ON recommendations (asset_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_recommendations_time ON recommendations (created_at DESC);
CREATE INDEX IF NOT EXISTS idx_recommendations_action ON recommendations (action);

-- What the user did with a recommendation, kept apart from the prediction itself.
CREATE TABLE IF NOT EXISTS recommendation_decisions (
    recommendation_id  UUID PRIMARY KEY REFERENCES recommendations (id) ON DELETE CASCADE,
    decision           TEXT        NOT NULL,
    linked_position_id UUID,
    decided_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    note               TEXT        NOT NULL DEFAULT ''
);

-- What the market actually did afterwards. Never merged into recommendations.
CREATE TABLE IF NOT EXISTS recommendation_outcomes (
    recommendation_id      UUID PRIMARY KEY REFERENCES recommendations (id) ON DELETE CASCADE,
    evaluated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finalized              BOOLEAN     NOT NULL DEFAULT FALSE,
    price_after_5m         DOUBLE PRECISION,
    price_after_15m        DOUBLE PRECISION,
    price_after_1h         DOUBLE PRECISION,
    price_after_4h         DOUBLE PRECISION,
    price_after_24h        DOUBLE PRECISION,
    mfe_pct                DOUBLE PRECISION,
    mae_pct                DOUBLE PRECISION,
    first_tp_hit_index     INTEGER,
    first_sl_hit_index     INTEGER,
    status                 TEXT        NOT NULL DEFAULT 'pending',
    ambiguous              BOOLEAN     NOT NULL DEFAULT FALSE,
    ambiguity_reason       TEXT        NOT NULL DEFAULT '',
    result                 TEXT
);

CREATE INDEX IF NOT EXISTS idx_outcomes_status ON recommendation_outcomes (status, finalized);

-- Every inference attempt, successful or not.
CREATE TABLE IF NOT EXISTS llm_inferences (
    id                UUID PRIMARY KEY,
    recommendation_id UUID        REFERENCES recommendations (id) ON DELETE SET NULL,
    analysis_run_id   UUID        REFERENCES analysis_runs (id) ON DELETE SET NULL,
    symbol            TEXT        NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    model_name        TEXT        NOT NULL DEFAULT '',
    prompt_version    TEXT        NOT NULL DEFAULT '',
    schema_version    INTEGER     NOT NULL DEFAULT 1,
    cache_key         TEXT,
    llm_input         JSONB,
    llm_raw_output    TEXT,
    parsed_output     JSONB,
    status            TEXT        NOT NULL,
    error_message     TEXT        NOT NULL DEFAULT '',
    repair_attempted  BOOLEAN     NOT NULL DEFAULT FALSE,
    latency_ms        INTEGER     NOT NULL DEFAULT 0,
    prompt_tokens     INTEGER,
    completion_tokens INTEGER
);

CREATE INDEX IF NOT EXISTS idx_llm_inferences_cache ON llm_inferences (cache_key) WHERE cache_key IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_llm_inferences_time ON llm_inferences (created_at DESC);

-- User positions. Aggregate numbers are derived from fills; the row keeps the
-- current cached state for fast reads but the fills remain the source of truth.
CREATE TABLE IF NOT EXISTS positions (
    id                 UUID PRIMARY KEY,
    asset_id           BIGINT      NOT NULL REFERENCES assets (id) ON DELETE CASCADE,
    symbol             TEXT        NOT NULL,
    direction          TEXT        NOT NULL,
    status             TEXT        NOT NULL DEFAULT 'OPEN',
    entry_price        NUMERIC(38,18) NOT NULL,
    leverage           NUMERIC(38,18) NOT NULL,
    initial_quantity   NUMERIC(38,18),
    remaining_quantity NUMERIC(38,18),
    initial_notional   NUMERIC(38,18),
    initial_margin     NUMERIC(38,18),
    size_known         BOOLEAN     NOT NULL DEFAULT TRUE,
    opened_at          TIMESTAMPTZ NOT NULL,
    closed_at          TIMESTAMPTZ,
    recommendation_id  UUID        REFERENCES recommendations (id) ON DELETE SET NULL,
    fee_type           TEXT        NOT NULL DEFAULT 'taker',
    original_plan      JSONB,
    current_plan       JSONB,
    note               TEXT        NOT NULL DEFAULT '',
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_positions_status ON positions (status);
CREATE INDEX IF NOT EXISTS idx_positions_asset ON positions (asset_id, opened_at DESC);

-- Append-only executions. Opening and every (partial) close is a fill.
CREATE TABLE IF NOT EXISTS position_fills (
    id            UUID PRIMARY KEY,
    position_id   UUID        NOT NULL REFERENCES positions (id) ON DELETE CASCADE,
    kind          TEXT        NOT NULL,
    quantity      NUMERIC(38,18),
    close_pct     NUMERIC(38,18),
    price         NUMERIC(38,18) NOT NULL,
    fee           NUMERIC(38,18) NOT NULL DEFAULT 0,
    fee_type      TEXT        NOT NULL DEFAULT 'taker',
    fee_estimated BOOLEAN     NOT NULL DEFAULT TRUE,
    realized_pnl  NUMERIC(38,18) NOT NULL DEFAULT 0,
    executed_at   TIMESTAMPTZ NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    note          TEXT        NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_fills_position ON position_fills (position_id, executed_at);

-- Full audit trail of everything that happened to a position.
CREATE TABLE IF NOT EXISTS position_events (
    id          BIGSERIAL PRIMARY KEY,
    position_id UUID        NOT NULL REFERENCES positions (id) ON DELETE CASCADE,
    event_type  TEXT        NOT NULL,
    payload     JSONB       NOT NULL DEFAULT '{}'::JSONB,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_position_events_position ON position_events (position_id, occurred_at);

-- Standalone fees the user records manually (not tied to a fill).
CREATE TABLE IF NOT EXISTS fee_events (
    id          UUID PRIMARY KEY,
    position_id UUID        NOT NULL REFERENCES positions (id) ON DELETE CASCADE,
    amount      NUMERIC(38,18) NOT NULL,
    fee_type    TEXT        NOT NULL DEFAULT 'custom',
    occurred_at TIMESTAMPTZ NOT NULL,
    note        TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_fee_events_position ON fee_events (position_id, occurred_at);

-- Funding is entered manually: the app has no exchange integration.
CREATE TABLE IF NOT EXISTS funding_events (
    id          UUID PRIMARY KEY,
    position_id UUID        NOT NULL REFERENCES positions (id) ON DELETE CASCADE,
    amount      NUMERIC(38,18) NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    note        TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_funding_events_position ON funding_events (position_id, occurred_at);

CREATE TABLE IF NOT EXISTS backtest_runs (
    id                UUID PRIMARY KEY,
    mode              TEXT        NOT NULL,
    symbol            TEXT        NOT NULL,
    asset_id          BIGINT      REFERENCES assets (id) ON DELETE SET NULL,
    timeframe         TEXT        NOT NULL,
    date_from         TIMESTAMPTZ NOT NULL,
    date_to           TIMESTAMPTZ NOT NULL,
    analysis_interval TEXT        NOT NULL DEFAULT '',
    status            TEXT        NOT NULL DEFAULT 'pending',
    params            JSONB       NOT NULL DEFAULT '{}'::JSONB,
    metrics           JSONB,
    estimated_steps   INTEGER     NOT NULL DEFAULT 0,
    completed_steps   INTEGER     NOT NULL DEFAULT 0,
    error_message     TEXT        NOT NULL DEFAULT '',
    started_at        TIMESTAMPTZ,
    finished_at       TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_backtest_runs_time ON backtest_runs (created_at DESC);

CREATE TABLE IF NOT EXISTS backtest_trades (
    id             UUID PRIMARY KEY,
    run_id         UUID        NOT NULL REFERENCES backtest_runs (id) ON DELETE CASCADE,
    symbol         TEXT        NOT NULL,
    direction      TEXT        NOT NULL,
    opened_at      TIMESTAMPTZ NOT NULL,
    closed_at      TIMESTAMPTZ,
    entry_price    NUMERIC(38,18) NOT NULL,
    exit_price     NUMERIC(38,18),
    quantity       NUMERIC(38,18) NOT NULL DEFAULT 0,
    leverage       NUMERIC(38,18) NOT NULL DEFAULT 0,
    allocation_pct NUMERIC(38,18) NOT NULL DEFAULT 0,
    gross_pnl      NUMERIC(38,18) NOT NULL DEFAULT 0,
    fees           NUMERIC(38,18) NOT NULL DEFAULT 0,
    funding        NUMERIC(38,18) NOT NULL DEFAULT 0,
    net_pnl        NUMERIC(38,18) NOT NULL DEFAULT 0,
    pnl_pct        DOUBLE PRECISION NOT NULL DEFAULT 0,
    mfe_pct        DOUBLE PRECISION,
    mae_pct        DOUBLE PRECISION,
    exit_reason    TEXT        NOT NULL DEFAULT '',
    confidence     INTEGER,
    details        JSONB
);

CREATE INDEX IF NOT EXISTS idx_backtest_trades_run ON backtest_trades (run_id, opened_at);

CREATE TABLE IF NOT EXISTS app_settings (
    key        TEXT PRIMARY KEY,
    value      JSONB       NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Operational timestamps surfaced by the observability panel.
CREATE TABLE IF NOT EXISTS system_status (
    key         TEXT PRIMARY KEY,
    status      TEXT        NOT NULL DEFAULT 'unknown',
    message     TEXT        NOT NULL DEFAULT '',
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
