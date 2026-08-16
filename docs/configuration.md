# Configuration

Every setting has a commented entry in [`.env.example`](../.env.example), grouped as Database,
HTTP, Bybit Market Data, CoinGecko, News, Analysis, LLM, Risk management, Fees, Backtesting and
Logging. This page covers the parts that need more than one line of explanation.

## Settings precedence

Anything you change in the **Settings** screen is stored in the database and takes precedence over
`.env`, so a restart never silently reverts a deliberate change. To go back to the environment,
start once with:

```env
SETTINGS_FROM_ENV=true
```

The stored settings are then re-seeded from the environment. LLM, risk, news and fee settings apply
live — no restart needed.

## Fees

The fee rates ship **empty on purpose**. The official Bybit fee schedule cannot be verified
programmatically (it answers HTTP 403 to automated requests), and a guessed rate would quietly
distort your P&L in a tool whose whole point is honest accounting.

Take your own tier from
<https://www.bybit.com/en/help-center/article/Trading-Fee-Structure>
and set:

```env
DEFAULT_MAKER_FEE_PCT=
DEFAULT_TAKER_FEE_PCT=
```

or fill them in under **Settings → Exchange and fees**. While both are unset:

* fees are treated as unknown and counted as zero,
* the UI shows a warning,
* every affected figure is flagged as approximate.

The actual fee of a specific fill can always be entered by hand and takes precedence over the
computed one. Funding is entered manually as well — positive means you received it, negative means
you paid. (Backtests use the exchange's published funding history; only your own live positions
need manual entry, because the application has no account access.)

## The local LLM

Any OpenAI-compatible server works: llama.cpp, vLLM, Ollama in OpenAI mode, and so on.

The Settings screen offers four presets — **Local**, **ChatGPT**, **Claude** and **Custom** — which
fill in the endpoint, a current model and sane context and concurrency values; every field stays
editable afterwards, and the selected preset is derived from the endpoint rather than stored. The
two hosted presets need `LLM_API_KEY` in `.env`: keys are never entered or kept in the UI. Note what
sending a snapshot to a hosted endpoint means — see [Privacy](../README.md#privacy-and-safety) —
and that Anthropic describes its OpenAI-compatible layer as a way to evaluate its models rather
than a production interface.

### Option 1 — the bundled llama.cpp

```bash
mkdir -p models
# put a GGUF file in it, e.g. Qwen_Qwen3-8B-Q5_K_M.gguf
```

```env
LLM_MODEL_DIR=./models
LLM_MODEL_FILE=Qwen_Qwen3-8B-Q5_K_M.gguf
LLM_BASE_URL=http://llm:8080/v1
LLM_MODEL=Qwen3-8B
LLM_MAX_TOKENS=1800
LLM_CONTEXT_SIZE=16384
LLM_GPU_LAYERS=999
```

```bash
docker compose --profile llm up -d
```

Qwen3-8B at Q5_K_M needs roughly 6–7 GB of VRAM. Verify GPU passthrough first:

```bash
docker run --rm --gpus all nvidia/cuda:12.4.0-base-ubuntu22.04 nvidia-smi
```

### Option 2 — a model you already run

Leave the `llm` profile down and point the backend at your server:

```env
LLM_BASE_URL=http://host.docker.internal:8080/v1
```

`host.docker.internal` is already mapped for the backend service in the compose file.

Check either setup with:

```bash
curl http://localhost:8080/api/health/llm
# {"name":"llm","status":"online",...}
```

### Context budget

`LLM_MAX_TOKENS` caps only the generated answer. The overall limit for
`system prompt + market snapshot + answer` is `LLM_CONTEXT_SIZE`. The backend reserves room for the
prompt, the answer and a safety margin, then spends what remains on the snapshot, trimming only
optional detail — low-value historical cases first, then less significant indicators, then the tail
of raw candles. It never truncates JSON mid-structure.

Because the snapshot budget is derived from the window, raising `LLM_CONTEXT_SIZE` genuinely gives
the model more data rather than more empty space. To shorten prompts for speed, cap the snapshot
explicitly with `LLM_SNAPSHOT_MAX_TOKENS`. Both values are editable in the UI.

If you change the context size of the bundled llama.cpp, the value in `.env` and in the UI must
match, and the `llm` container has to be restarted — `--ctx-size` is applied when the model loads.

**Context usage indicator.** The backend stores the token usage the server itself reports for every
inference and shows the peak of the last 50, plus the reserved answer, as a percentage of the
window: `LLM context: 60%`. A healthy run already sits around 70–85%, because the snapshot fills
what is available, so the warning thresholds mark the point where a refusal becomes likely:

```env
LLM_CONTEXT_WARN_PCT=90
LLM_CONTEXT_CRITICAL_PCT=97
```

Until at least one inference with known usage exists, the badge is hidden — no invented numbers.

### Concurrency

One local GPU serves one request at a time:

```env
LLM_MAX_CONCURRENT_REQUESTS=1
```

The same value sets both the backend semaphore and the number of llama.cpp slots. The inference
queue is prioritised: assets with open positions first, then recent strong signals, then the rest.

## Analysis

The cycle runs every five minutes by default, independently of any open browser tab, and analyses
`1m/5m/15m/1h/4h/1d` per asset before aggregating them into one multi-timeframe snapshot.

```env
ANALYSIS_INTERVAL=5m
ANALYSIS_TIMEFRAMES=1m,5m,15m,1h,4h,1d
ANALYSIS_BENCHMARK_SYMBOL=BTC     # what "the market as a whole" means for the market filter
ANALYSIS_MANUAL_COOLDOWN=1m       # anti-spam for the "Analyse now" button
```

## Risk

```env
RISK_MIN_LEVERAGE=5
RISK_MAX_LEVERAGE=50
MAX_RECOMMENDED_ALLOCATION_PCT=15
RISK_PER_TRADE_PCT=0.75           # what a stop-out may cost, in percent of capital; 0 = fixed size
```

`RISK_PER_TRADE_PCT` replaces the requested position size rather than capping it:
`size = risk × 100 / (leverage × stop_distance_%)`, computed from the leverage the position will
actually carry after the risk engine has trimmed it. A quiet asset and a volatile one then take the
same risk instead of the same size. See [strategies.md](strategies.md) for what that changed at the
account level.

## Backtesting

```env
BACKTEST_MAX_INFERENCES=2000      # hard ceiling for one LLM run
BACKTEST_CACHE_ENABLED=true       # reuse identical situations instead of re-running the GPU
BACKTEST_INFERENCE_PAUSE=1s       # cool-down after each request that reached the model
BACKTEST_RUN_TIMEOUT=12h          # a run cancelled by this is stored as failed
BACKTEST_DEFAULT_FUNDING_RATE_PCT=0
BACKTEST_DEFAULT_MAINTENANCE_MARGIN_PCT=0
```

Set the funding rate and maintenance margin for the contract tier you are testing. Zero means no
funding and liquidation at the bankruptcy price — the application does not invent exchange-specific
values. Settled funding is downloaded from Bybit's public history and used whenever it covers the
period; the flat rate is only the fallback.

## Rate limits

The free CoinGecko tier is stricter than the paid ones. If HTTP 429 shows up in the logs, lower
`COINGECKO_RATE_LIMIT_RPM` (10 is safe) or add `COINGECKO_API_KEY`.
