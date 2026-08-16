-- Price ticks existed to build one-minute candles for assets the exchange does
-- not list. Those candles carried no volume and only ever approximated a bar,
-- so both the fallback and its input are gone: every candle now comes from the
-- exchange, or the asset is honestly reported as having none.
DROP TABLE IF EXISTS price_ticks;
