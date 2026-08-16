# Architecture

```
React UI  →  Go REST API  →  domain services  →  PostgreSQL
```

The frontend never touches the database and performs no trading maths of its own: it renders what
the backend computed and sends user actions back. The backend is the single source of truth for
analysis, positions and money.

## Layout

```
backend/
  cmd/
    server/              one binary, four modes: serve | api | worker | migrate
    lab/                 offline research harness (no database, no LLM, writes nothing)
  internal/
    api/                 REST layer, DTOs, middleware, one error format
    config/              typed configuration from the environment
    domain/              enums and domain models
    database/            pgx pool + embedded migrations
    repository/          hand-written SQL over pgx
    marketdata/          provider abstraction and candle assembly
      bybit/             public V5 market data: instruments, tickers, kline, funding
      coingecko/         market capitalisation, ranking, automatic universe
    news/                RSS/Atom + Bybit announcements, SSRF guard, clustering, scoring
    analysis/
      indicators/        ~25 deterministic indicators
      patterns/          ~45 candlestick and 20 chart formations
      marketstructure/   swings, HH/HL/LH/LL, BOS/CHoCH
      supportresistance/ level clustering
      divergences/       RSI/MACD/OBV, regular and hidden
      regime/            regime classifier and signal scoring
      strategies/        the deterministic policy: weighted votes and veto filters
      features/          snapshot assembly, multi-timeframe aggregation, feature vector
    llm/                 client, prompt, compact serializer, strict validation
    risk/                deterministic risk engine
    positions/, pnl/     position accounting in decimal
    recommendations/     cycle orchestration and outcome grading
    history/             statistics, similar cases, calibration
    backtesting/         both modes, execution model, metrics
    scheduler/           background cycles with bounded concurrency
    settings/            stored settings and live application
frontend/src/            React + TypeScript: pages, charts, i18n (ru/en/zh-CN)
docker/                  Dockerfiles and nginx.conf
```

## Decisions worth knowing

**The model never has the last word.** `internal/risk` recomputes the allowed leverage and position
size from stop distance, ATR and its percentile, regime, timeframe agreement, nearby opposing
levels, confidence and data quality. The UI shows both numbers — what the model asked for and what
was granted.

**Money is decimal, everywhere.** `NUMERIC(38,18)` in the database, `decimal.Decimal` in Go.
`float64` appears only in analytics, where a last-digit difference cannot corrupt an account.

**A prediction is immutable.** Rows in `recommendations` are never edited. The user's decision and
the market outcome live in separate tables, which is what keeps the statistics honest — a forecast
cannot be quietly improved after the fact.

**Position history is append-only.** Results are always derived from fills; the position row is a
cache of the current state, never the record itself.

**No sqlc.** The query set is small and partly dynamic, so hand-written SQL in
`internal/repository` keeps `go build ./...` the only build step. SQL exists nowhere else.

**No message broker, no vector database.** For a single-user local application a scheduler with a
semaphore and a deterministic nearest-neighbour search over normalised feature vectors do the job.
The interfaces leave room for more if the need ever appears.

**Untrusted model output.** The answer is parsed, semantically validated (enums, ranges, TP/SL
direction relative to the entry, close-percentage sums), repaired once at most, then clamped by the
risk engine. It is never evaluated as code.

## The analysis cycle

```
scheduler (every 5m, aligned to the candle close)
   ↓
market-data workers ── bounded by a semaphore
   ↓
indicators · patterns · structure · levels · divergences · regime   (per timeframe)
   ↓
multi-timeframe aggregation + trend alignment score
   ↓
news context + track record + similar historical cases
   ↓
compact snapshot → LLM inference queue (concurrency 1 by default)
   ↓
validation → risk engine → stored recommendation
```

The deterministic policy produces its own verdict on the same snapshot in the same cycle, stored
next to the model's, so the two can be compared on one history.

Analysis priority is: assets with open positions, then recent strong signals, then everything else.
Scarce GPU time goes where a decision is actually pending.

## Look-ahead defence

At simulated time T the analysis receives only candles with a close time ≤ T, and the forward
simulation of an open trade starts strictly on the next bar. News, the track record and similar
cases are cut off the same way — including outcome grading, so a prediction evaluated after T does
not count towards the statistics shown at T.

This is asserted by regression tests: appending a wildly different future to the data must not
change any earlier trade (`internal/backtesting/lookahead_test.go`,
`internal/analysis/features/lookahead_test.go`).

## REST API

```
GET    /api/health              /db  /market-data  /llm  /news
GET    /api/dashboard

GET    /api/markets                        POST /api/markets
GET    /api/markets/{symbol}               PATCH /api/markets/{symbol}
DELETE /api/markets/{symbol}               POST /api/markets/refresh
GET    /api/markets/{symbol}/analysis
GET    /api/markets/{symbol}/candles?timeframe=1h&limit=300
POST   /api/markets/{symbol}/analyze

GET    /api/recommendations                DELETE /api/recommendations
GET    /api/recommendations/{id}           DELETE /api/recommendations/{id}
POST   /api/recommendations/{id}/restore
POST   /api/recommendations/{id}/decision

GET    /api/news?q=&asset=&category=&critical=&min_importance=&sort=
GET    /api/news/{id}                      GET /api/news/stats
GET    /api/news/sources                   POST /api/news/sources
PATCH  /api/news/sources/{id}              DELETE /api/news/sources/{id}

GET    /api/positions                      POST /api/positions
GET    /api/positions/{id}                 DELETE /api/positions/{id}
POST   /api/positions/{id}/close           POST /api/positions/{id}/partial-close
POST   /api/positions/{id}/plan
POST   /api/positions/{id}/fee             POST /api/positions/{id}/funding

GET    /api/statistics?days=30

GET    /api/backtests                      POST /api/backtests
POST   /api/backtests/estimate             GET  /api/backtests/{id}
DELETE /api/backtests/{id}                 POST /api/backtests/{id}/cancel

GET    /api/settings                       PUT  /api/settings
```

Every error uses one shape:

```json
{ "error": { "code": "VALIDATION_FAILED", "message": "...", "request_id": "..." } }
```

Enums cross the API as stable machine keys (`OPEN_LONG`, `MANAGE_POSITION`, `strong_uptrend`, …);
the frontend translates them. Backend responses are DTOs, never database models.

## Database

Core tables: `assets`, `ohlcv_candles`, `funding_rates`, `market_snapshots`, `analysis_runs`,
`recommendations`, `recommendation_decisions`, `recommendation_outcomes`, `llm_inferences`,
`positions`, `position_fills`, `position_events`, `fee_events`, `funding_events`, `backtest_runs`,
`backtest_trades`, `news_*`, `app_settings`, `system_status`.

Schema changes are migrations only (`internal/database/migrations`, embedded in the binary and
applied on start unless `DATABASE_AUTO_MIGRATE=false`). Every migration ships with its `down` counterpart
and is expected to survive an `up → down → up` cycle on a clean database.
