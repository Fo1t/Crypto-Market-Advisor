# Data sources: Bybit and CoinGecko

**English** · [Русский](data-sources.ru.md) · [简体中文](data-sources.zh-CN.md)

Two providers, one rule: **the exchange owns prices, the metadata provider owns capitalisation.**
Neither is asked for what the other is authoritative about, and nothing is ever reconstructed from
the wrong one. Every endpoint below is public — no account, no API key, no signature. The
application has no way to place an order, because it never authenticates.

| | Bybit | CoinGecko |
|---|---|---|
| Answers | What does this pair cost, what did it do, when did it settle funding? | How large is this asset, and how does it rank against the others? |
| Drives | Prices, candles, funding, the news announcements feed | The automatic top-N universe, market cap column |
| Is it a candle source? | Yes — the only one | No, deliberately |
| Missing it means | No analysis is possible | The universe stops updating; analysis continues |

Code: `backend/internal/marketdata/bybit`, `backend/internal/marketdata/coingecko`, tied together in
`backend/internal/marketdata/service.go`.

---

## Bybit (public V5 market API)

Base URL `https://api.bybit.com` (`BYBIT_MARKET_BASE_URL`). Four market endpoints, plus one for
announcements used by the news pipeline.

| Endpoint | Called by | What it is used for |
|---|---|---|
| `GET /v5/market/instruments-info` | `SupportsSpotSymbol`, `SupportsLinearSymbol`, `Ping` | Does the exchange list this pair, and is it actually `Trading`? |
| `GET /v5/market/tickers?category=spot` | `SpotTickers` | Last price, 24h change, 24h turnover, high/low for every tracked symbol |
| `GET /v5/market/kline` | `Klines`, `LinearKlines` | The OHLCV history every indicator, chart and backtest reads |
| `GET /v5/market/funding/history` | `FundingHistory` | Settled funding of a perpetual, so a backtest charges what really applied |
| Announcements | `internal/news/bybit.go` | Listings, delistings, trading suspensions (see [the news pipeline](algorithms.md#7-news-a-constraint-never-a-direction)) |

### Which market a symbol is read from

An asset is tracked under a Bybit symbol (`BTC` → `BTCUSDT`). Before any candle request the
instrument catalogue decides where the bars come from:

```
spot lists the symbol and it is Trading   →  category=spot     (the normal case)
otherwise, linear lists it and is Trading →  category=linear   (perpetual-only assets)
neither                                   →  ErrNotTradable — the asset is reported, not faked
```

The full catalogue of each category is paged (1000 rows per page, cursor-based, capped at 10 pages)
and **cached for one hour**, so this check costs nothing per symbol. A catalogue request that fails
answers "yes": refusing an asset because one lookup timed out would silently shrink the universe on
a flaky network.

### How candles are fetched

`GET /v5/market/kline` returns at most 1000 bars per call, newest first. The client walks the
window **backwards**: it asks for `[from, end]`, moves `end` to just before the oldest bar it
received, and repeats — up to 100 pages, so at most ~100 000 bars per timeframe and call. Duplicate
open times are de-duplicated by timestamp and the result is sorted ascending. Timeframes map to
Bybit intervals as `1m→1`, `5m→5`, `15m→15`, `1h→60`, `4h→240`, `1d→D`.

A forming bar is dropped before anything is stored (`ClosedOnly`). Only settled candles ever reach
the database, which is what keeps an indicator from reacting to a bar that has not finished.

**The scheduled backfill** (`BackfillCandles`, run at start-up and after every daily universe
refresh) advances from the newest stored bar of each timeframe, re-fetching one bar of overlap so a
revised candle is picked up. On a first run it reaches back a fixed, timeframe-specific window:

| Timeframe | 1m | 5m | 15m | 1h | 4h | 1d |
|---|---|---|---|---|---|---|
| First-run history | 48 h | 30 d | 180 d | 3 y | 6 y | 10 y |

If that window is later widened, the older part is fetched separately — moving forward from the
newest bar would otherwise never reach it.

**The manual import** (Settings → *Historical data*) is the other direction: you name assets, a set
of timeframes and a period, and exactly that period is downloaded. It is the right tool for a
history longer than the automatic window, and for repairing a stretch that arrived with gaps.
Candles are upserted, so importing the same period twice changes nothing but the `updated_at`
column. One job runs at a time and it runs in the backend — closing the tab does not abandon it.
REST: `POST /api/markets/import`, `GET /api/markets/import`, `POST /api/markets/import/cancel`.

### Funding

`BackfillFunding` stores the settled funding of each asset's linear perpetual: one row every eight
hours, paged backwards 200 settlements at a time. A first run reaches back five years, so a backtest
over the stored candles can charge the funding that actually applied; later runs continue from the
newest stored settlement with one interval of overlap. Assets with no perpetual simply have no
funding — a fact about the asset, not a failure.

### Prices

`IngestPrices` runs on its own cadence (`MARKET_DATA_INTERVAL`, one minute by default) and takes
**one** ticker request for all tracked symbols, so every row of the market overview shares a single
exchange timestamp. Price, 24h change, 24h turnover and the high/low come from Bybit; the market
capitalisation and rank are merged in from CoinGecko. An asset the exchange has no price for is
skipped and named in the status message rather than being shown with a stale number.

### Rate limits, retries and health

The client is wrapped in `internal/httpx`: 300 requests/minute by default
(`BYBIT_MARKET_RATE_LIMIT_RPM`), a 15 s timeout, 2 retries with exponential backoff and jitter, and
`Retry-After` honoured after a 429 — the limiter itself is paused, so a retry storm cannot form.

Health (`GET /api/health/market-data`) reports from recorded state while a successful call is less
than two minutes old, and only then issues a ping. Bybit's absence is what actually stops the
analysis, so it is the component the probe checks; if data was flowing within the last 15 minutes
the status is `degraded` rather than `offline` — a starved probe is not an outage.

---

## CoinGecko (public REST API)

Base URL `https://api.coingecko.com/api/v3` (`COINGECKO_BASE_URL`). Two endpoints are called, both
without a key by default; a demo key can be supplied with `COINGECKO_API_KEY` and is sent as
`x-cg-demo-api-key`.

| Endpoint | Called by | What it is used for |
|---|---|---|
| `GET /ping` | `Ping` | Liveness for `GET /api/health/market-data` |
| `GET /coins/markets` | `TopMarkets`, `MarketsByIDs` | Capitalisation, its ranking, and therefore the automatic universe |

Responses are cached for 45 s (`COINGECKO_CACHE_TTL`) and the client is limited to 25
requests/minute (`COINGECKO_RATE_LIMIT_RPM`), which is what the free tier tolerates.

### What the universe refresh actually does

`RefreshUniverse` runs at start-up and once a day (`MARKET_UNIVERSE_REFRESH`):

1. Ask CoinGecko for the top `MARKET_UNIVERSE_SIZE × 3` assets by capitalisation (capped at 250) —
   deliberately more than needed, because most of the list is about to be filtered out.
2. Drop what is not an independently tradable directional instrument: stablecoins, wrapped, staked,
   restaked and bridged derivatives, by symbol and by name.
3. Drop what Bybit does not list. Capitalisation is not tradability: an asset with no exchange pair
   would only ever be a row that can never be analysed.
4. Keep the first `MARKET_UNIVERSE_SIZE` survivors (20 by default), creating the ones that are new
   and updating the rank and display name of the ones already tracked.

**Your choices always win.** Assets you added by hand are never removed, assets you excluded are
never re-added, and the enabled/pinned/excluded flags survive every refresh.

### Why it is not a candle source

CoinGecko's free tier has no native minute-level OHLC; a candle series built from its sampled
prices looks real and is not. The project used to do exactly that and stopped: an asset Bybit does
not list is now reported as untradable instead of being analysed on approximated bars. Every stored
candle records the provider it came from (`ohlcv_candles.provider`) and how it was produced
(`source`), so an old derived bar can always be told from a native one.

---

## What runs when

| Loop | Cadence | Talks to |
|---|---|---|
| Price ingest | `MARKET_DATA_INTERVAL` (1 min) | Bybit tickers + CoinGecko markets |
| Analysis cycle | `ANALYSIS_INTERVAL` (5 min, aligned to the candle close) | Database only |
| Universe refresh + full backfill | `MARKET_UNIVERSE_REFRESH` (24 h) | CoinGecko, then Bybit catalogue/kline/funding |
| News collection | `NEWS_FETCH_INTERVAL` | Bybit announcements + RSS/Atom |
| Manual import | On request | Bybit kline |

## When a provider is down

| Situation | What happens |
|---|---|
| Bybit unreachable | No prices, no new candles. `market_data` goes `degraded`, then `offline` after 15 minutes without data. Analysis of stale data is marked `degraded`/`unusable` rather than being passed off as fresh. |
| CoinGecko unreachable | Prices keep flowing from Bybit; the market cap column stops updating and the universe refresh fails with a logged warning. Nothing else is affected. |
| A pair is delisted | The catalogue reports it as not `Trading`, the backfill returns `ErrNotTradable`, and the asset keeps its stored history without gaining new bars. |
| A 429 | The limiter pauses for `Retry-After`, the request is retried with backoff, and the failure is reported as `degraded` rather than as missing data. |

## Configuration

Everything above is tunable from `.env`; see [configuration.md](configuration.md) for the full
reference.

```env
BYBIT_MARKET_DATA_ENABLED=true
BYBIT_MARKET_BASE_URL=https://api.bybit.com
BYBIT_MARKET_RATE_LIMIT_RPM=300
BYBIT_MARKET_TIMEOUT=15s

COINGECKO_BASE_URL=https://api.coingecko.com/api/v3
COINGECKO_API_KEY=                 # optional demo key
COINGECKO_RATE_LIMIT_RPM=25
COINGECKO_CACHE_TTL=45s
MARKET_UNIVERSE_SIZE=20
MARKET_UNIVERSE_REFRESH=24h
```
