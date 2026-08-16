# Backtesting

Two modes, one engine:

* **Technical** — no model involved: indicators, patterns, the deterministic policy and the risk
  engine. Fast enough to sweep years.
* **LLM** — one inference per analysis step. The form requires an estimate first ("Estimate" shows
  the expected inference count) and only starts after confirmation, and only if the estimate stays
  under `BACKTEST_MAX_INFERENCES`.

Inference results are cached by the hash of `prompt_version + model + snapshot`, so replaying the
same period does not touch the GPU again.

## The look-ahead guarantee

At time T the engine sees only candles with a close time ≤ T, and the forward simulation of an open
trade starts strictly on the next bar. The news context, the track record and the similar historical
cases are cut off the same way — a prediction graded after T does not appear in the summary shown
at T.

This is a regression test, not a promise: appending a different future to the data must not change
any earlier trade (`internal/backtesting/lookahead_test.go`,
`internal/analysis/features/lookahead_test.go`).

When a single candle touches both the take profit and the stop, OHLC does not reveal the order. The
engine does not invent one: a backtest takes the pessimistic outcome and labels it
`stop_loss_ambiguous_candle`, and recommendation grading marks the outcome `ambiguous` (or resolves
it on a finer timeframe when that history exists).

## Execution model

Simulated: partial take profits and stops, maker fees for resting limit exits, taker fees for
entries, stops and liquidation, configurable slippage, funding every eight hours, maintenance margin
and several concurrent positions.

Within one candle, price reaches the nearer level first: a stop between the entry and the
liquidation price executes before liquidation can ever happen. A stop is a market order, so on a gap
it fills at the candle open rather than at its own level; a liquidation loss is capped at the
bankruptcy price, i.e. the posted margin. A limit take profit fills at its level and pays the maker
fee.

Analysis at every step is multi-timeframe and uses the same risk settings as the live cycle,
including the allocation reduction. When part of the history is missing (1m candles are only kept
for the last day, for instance), the step is honestly marked degraded: the run metrics report how
many steps were degraded and which features were missing, and on those steps the risk engine lowers
leverage and size exactly as it would live.

## Exit modes

* **Signal levels** — TP/SL from the signal itself: ATR-derived in technical mode
  (`SL = 1.5 ATR`, TP `3.0 ATR` and `6.0 ATR`), the model's own plan in LLM mode.
* **P&L ladder** — fixed steps in return on margin, identical for any signal. `50:50, 75:25, 100:25`
  closes half at +50% on margin, then 25% at +75%, then the rest at +100%; stops are written the
  same way with positive thresholds (`50:100` closes everything at −50%). Thresholds are converted
  into prices at entry using the position's actual leverage, so a step means the same thing after
  the risk engine adjusted it. Percentages are on margin and before costs — fees and funding are
  charged on top.
* **Trailing ATR (Chandelier)** — no target at all: the stop follows the extreme reached since entry
  at a distance of N average candle ranges. This is the only mode that gave a profit factor above 1
  in both independent periods on 4h and 1d in our tests.

Leverage above roughly 10x is pointless to change: the form requests it, the risk engine decides,
and a request of 25x or 50x still ends up at 5–12x. Leverage alone creates no edge anyway — it
scales profit, loss and fees alike, leaving profit factor untouched.

## The run report

* An equity curve sampled at every candle close, including unrealised P&L, with drawdown from the
  running peak; stored with the run, downsampled to 1500 points.
* A candlestick chart of exactly the run period with markers for every execution: entry, each
  partial close with its share, and liquidation.
* Distributions: what closed the trades, and how P&L was spread.
* An expandable execution list per trade. The trade row shows the volume-weighted exit price; the
  individual steps live inside it, which matters for the P&L ladder and multi-level take profits.
* `decision_reasons`: why each analysis point did or did not become a trade — entry,
  `below_min_signal`, `hard_veto`, `entry_never_filled`, `no_atr`, and for LLM runs `llm_no_entry`
  (the model saw no trade), `llm_rejected` (validation refused the answer) and `llm_failed` (the
  model could not be reached). A run with no trades is a report, not a mystery.

The "Copy" button carries a finished run's parameters into the form of a new one, so an experiment
can be repeated with a single value changed.

A finished, failed or cancelled run can be deleted from the UI. That is a soft delete: the run, its
parameters, metrics and trades stay in PostgreSQL for audit but no longer appear in the normal list
and detail endpoints. An active run has to be cancelled first.

## LLM runs

* Every inference is tagged with the run that paid for it (`llm_inferences.backtest_run_id`), so all
  answers of one run can be pulled out with a single query:

  ```sql
  SELECT created_at, status, latency_ms,
         parsed_output->>'Action'     AS action,
         parsed_output->>'Confidence' AS confidence
  FROM llm_inferences
  WHERE backtest_run_id = '<run id>'
  ORDER BY created_at;
  ```

* `BACKTEST_INFERENCE_PAUSE` adds a cool-down after every request that actually reached the model,
  so a long replay does not hold the GPU at full load from start to finish. Cached answers are not
  delayed — otherwise a re-run of the same period would be as slow as the first. Cancelling a run
  takes effect during the pause immediately.
* One run is bounded by `BACKTEST_RUN_TIMEOUT` (12 hours by default). A timeout in the middle of a
  replay throws away every inference already paid for, so keep the ceiling above the worst case you
  intend to run.
* If the model stops answering, the run fails after five consecutive errors instead of spending the
  remaining hours on network timeouts.
* The replay assembles the same context the live cycle does — news, track record, similar cases —
  bounded by the bar being replayed. A technical run reads news (the `critical_news` filter uses it)
  but not the track record: the rules never look at it.

## Offline research harness

`cmd/lab` replays stored candles through the same engine without a database, without the LLM and
without writing anything: hundreds of runs finish in seconds and are pooled into one table. Every
figure in [strategies.md](strategies.md) came from it.

```bash
# export candles from a running database, one file per timeframe
for tf in 1d 4h 1h 15m 5m; do
  docker compose exec -T postgres psql -U advisor -d advisor -tA -c "
    COPY (SELECT a.symbol, c.open_time, c.close_time, c.open, c.high, c.low, c.close, c.volume
          FROM ohlcv_candles c JOIN assets a ON a.id=c.asset_id
          WHERE c.timeframe='$tf' AND c.closed
          ORDER BY a.symbol, c.open_time) TO STDOUT WITH (FORMAT csv)" > ./candles/$tf.csv
done

cd backend

# compare two weight profiles over independent years
go run ./cmd/lab -data ../candles -tf 1d -symbols BTC,ETH,SOL \
  -periods "Y23=2023-06-01:2024-06-01,Y24=2024-06-01:2025-06-01" \
  -variants before.json,after.json

# portfolio mode: pool every symbol into one account with limited slots,
# and compare against buy and hold
go run ./cmd/lab -data ../candles -funding ../funding -tf 1d \
  -risk-per-trade 0.75 -portfolio 3,5 \
  -periods "Y24=2024-06-01:2025-06-01" -variants after.json

# with real funding: a directory of <symbol>.csv files, columns settled_at,rate
go run ./cmd/lab -data ../candles -funding ../funding -tf 4h \
  -periods "A=2025-06-01:2026-01-01" -variants after.json

# does the entry predict anything at all: realised return of the next N candles
# after each signal, against bars with no signal
go run ./cmd/lab -data ../candles -tf 4h -periods "A=2025-06-01:2026-01-01" -signal-study
```

A variant file describes only what changes: `sides`, `strategies`, `exit_mode`,
`trailing_atr_mult`, `atr_stop_mult`, `atr_target1_mult`, `atr_target2_mult`, `min_confidence`,
`leverage`, `allocation_pct`. The flags `-benchmark` and `-cost-multiple` set the benchmark asset
and the payback threshold. Everything else comes from the application defaults, so an empty `{}` is
exactly what a user gets.

Useful flags beyond the basics: `-placebo` repeats a run with random entries matched on asset,
period, exit machinery and trade count; `-bootstrap` resamples calendar months with replacement to
test whether an effect survives; `-equity-filter` suspends new entries while the pooled account is
below its own average.
