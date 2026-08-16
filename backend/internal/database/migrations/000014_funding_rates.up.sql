-- Settled funding rates of the linear perpetual behind each asset.
--
-- They come from Bybit's public funding history endpoint, which needs no API
-- key and exposes nothing about an account. A backtest that ignores funding
-- flatters every position held for days, which is exactly what the trailing
-- exit produces, so the history is stored rather than assumed.
CREATE TABLE IF NOT EXISTS funding_rates (
    asset_id   BIGINT      NOT NULL REFERENCES assets (id) ON DELETE CASCADE,
    symbol     TEXT        NOT NULL,
    settled_at TIMESTAMPTZ NOT NULL,
    -- Fraction of notional per settlement, as the exchange publishes it.
    rate       DOUBLE PRECISION NOT NULL,
    provider   TEXT        NOT NULL DEFAULT 'bybit',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (asset_id, settled_at)
);

CREATE INDEX IF NOT EXISTS idx_funding_rates_lookup
    ON funding_rates (asset_id, settled_at DESC);
