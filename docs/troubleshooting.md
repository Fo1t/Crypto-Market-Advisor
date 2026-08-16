# Troubleshooting

**`docker compose up` fails on postgres.** Port 5432 is taken by a local PostgreSQL. Change
`POSTGRES_PORT` in `.env`.

**HTTP 429 from CoinGecko in the logs.** The free tier is strict. Lower `COINGECKO_RATE_LIMIT_RPM`
(10 is safe) or add `COINGECKO_API_KEY`. One failing fetch never stops the application: the affected
data is marked `degraded` and the cycle continues.

**`data_quality: degraded` with a timeframe in `missing_fields`.** That timeframe has fewer stored
candles than the analysis needs. Either the backfill has not caught up yet (1m history is kept for
about two days), or the exchange returned less history for that pair. The asset page shows the
provider mix, so a gap is never presented as a successful sync.

**An asset shows no candles at all.** Bybit lists neither a spot nor a linear pair for it, so there
is nothing to analyse — market capitalisation alone does not make an instrument tradable. The
automatic universe now skips such assets; one added by hand stays in the list and reports the
failure. Remove it, or pick a symbol the exchange actually lists.

**LLM offline.** Check `curl http://localhost:8081/v1/models` for the bundled profile, or whatever
`LLM_BASE_URL` points at. From inside a container, a server on the host OS is reachable as
`host.docker.internal`. While the model is down, market data and technical analysis keep working —
only recommendations stop.

**No recommendations appear.** With the model off or unreachable, that is the intended behaviour:
the application does not show invented signals. Technical analysis remains available on the asset
page. If the model is online and still nothing appears, open the run in **History** — the
deterministic verdict is stored on every cycle and says which rule refused.

**The model answers with something that is not JSON.** The backend makes exactly one repair attempt,
restating the specific validation problems. If that also fails, an inference error is stored and
nothing is shown to you. The `llm_inferences` table has the raw answer and the reason.

**A backtest finished with no trades.** Read `decision_reasons` in the run metrics: it counts why
each analysis point did not become a trade — `hard_veto`, `below_min_signal`, `entry_never_filled`,
`no_atr`, or, for LLM runs, `llm_no_entry`, `llm_rejected`, `llm_failed`.

**A long LLM backtest ends as failed.** Either it hit `BACKTEST_RUN_TIMEOUT` (12 hours by default)
or the model stopped answering and the run was ended after five consecutive failures. The error
message on the run says which.

**I changed `.env` but the application uses the old settings.** Settings edited in the UI are stored
in the database and take precedence. Start once with `SETTINGS_FROM_ENV=true` to re-seed them from
the environment.

**Fees show as approximate.** Both `DEFAULT_MAKER_FEE_PCT` and `DEFAULT_TAKER_FEE_PCT` are empty by
design. Set your own tier in `.env` or under **Settings → Exchange and fees**; see
[configuration.md](configuration.md#fees).

**An exotic asset showed up in the top-20.** The automatic universe filters stablecoins and
wrapped/staked duplicates by symbol and name, but tokenised products can slip through. Disable it by
hand on the **Markets** page — manual flags survive the daily refresh.

**GPU not visible to the `llm` container.** Verify the passthrough independently:

```bash
docker run --rm --gpus all nvidia/cuda:12.4.0-base-ubuntu22.04 nvidia-smi
```

If that fails, the problem is the NVIDIA Container Toolkit, not this application.

**The model refuses a request as too long.** Check the context badge in the status bar. Raise
`LLM_CONTEXT_SIZE` (and restart the `llm` container — `--ctx-size` is applied at load), or cap the
snapshot with `LLM_SNAPSHOT_MAX_TOKENS`. The value in `.env` and in the UI must match the value the
server actually runs with.
