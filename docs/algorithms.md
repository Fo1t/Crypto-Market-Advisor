# How the analysis works

**English** · [Русский](algorithms.ru.md) · [简体中文](algorithms.zh-CN.md)

A surface-level tour of every algorithm between a downloaded candle and an advisory on the screen.
It is meant to make the pipeline legible, not to replace the code: each section names the package
that owns it. The deterministic policy has its own deep dive in [strategies.md](strategies.md), and
the replay engine in [backtesting.md](backtesting.md).

```
candles ─► indicators ─► patterns ─► structure ─► levels ─► divergences ─► regime
                                                                             │
                                    per timeframe, then aggregated ──────────┘
                                                                             ▼
                          feature snapshot + news + track record + similar past cases
                                        │                        │
                          deterministic vote            compact snapshot ─► LLM ─► validation
                                        └──────────► risk engine ◄──────────┘
                                                          ▼
                                                      advisory
```

Everything before the LLM is deterministic: same candles in, same numbers out, unit-tested. The
model interprets those numbers and never produces one of its own.

---

## 1. Indicators

`internal/analysis/indicators` — around 25 classic indicators, all computed from closed bars only,
each returning a full series so a change over time can be read rather than just a snapshot.

| Family | What is computed |
|---|---|
| Trend | SMA/EMA/WMA (several lengths), MACD, ADX with +DI/−DI, Aroon, moving-average slope |
| Momentum | RSI, Stochastic, Stochastic RSI, CCI, Williams %R, ROC, Momentum |
| Volatility | ATR and ATR as a percentage of price, its percentile against its own history, Bollinger bands and width, Keltner channels, realised volatility |
| Volume | OBV, MFI, CMF, rolling and daily-anchored VWAP, relative volume |
| Statistics | Percentile, slope, standard deviation, mean — the helpers the rules above are built on |

The percentile of ATR matters more than ATR itself: "volatile" only means anything relative to how
this asset usually behaves.

## 2. Patterns

`internal/analysis/patterns` — about 45 candlestick formations (engulfing, hammer, stars, harami,
marubozu, …) and 20 chart formations (double tops and bottoms, head and shoulders, triangles,
wedges, flags, channels, breakouts and their retests).

Every hit carries its direction, a strength, and its **age in candles**. Age is what makes a
pattern usable: a breakout three bars old is a different fact from the same breakout forty bars ago,
and only the fresh one is allowed to move the regime.

## 3. Market structure

`internal/analysis/marketstructure` — fractal pivots with a half-width of 3 bars, labelled
HH/HL/LH/LL, from which the state (bullish, bearish, range, transition) and the events
**BOS** (break of structure) and **CHoCH** (change of character) are derived.

**Anti-repainting rule:** a pivot at bar *i* is only reported once `depth` bars after it have closed.
A swing that would be re-drawn later is not a swing yet — this is the same discipline that keeps
backtests honest.

## 4. Support and resistance

`internal/analysis/supportresistance` — candidate prices from swing points, calendar
highs/lows and round numbers are clustered together when they sit within 0.35 % of each other. A
cluster's strength grows with the number of touches and falls with age; the eight strongest levels
survive, and the nearest support below and resistance above the current price are singled out,
because those are the two the plan actually has to respect.

## 5. Divergences

`internal/analysis/divergences` — regular and hidden divergences between price pivots and RSI, MACD
and OBV. Pivot pairs are only compared within a 120-bar lookback, at least 4 and at most 60 bars
apart, and only when the price difference between them exceeds 0.15 % — three filters that exist
because divergence detection without them finds one everywhere.

## 6. Regime

`internal/analysis/regime` — rule-based, no model involved, which is what makes it usable as a
baseline to compare the LLM against.

```
ADX ≥ 40 and +DI > −DI  →  strong uptrend        ADX ≥ 40 and −DI > +DI  →  strong downtrend
ADX ≥ 25 and +DI > −DI  →  weak uptrend          ADX ≥ 25 and −DI > +DI  →  weak downtrend
ADX < 20                →  range                 fresh breakout pattern  →  breakout
```

When ADX is mute, market structure gets to promote a range into a directional read. On top of the
primary label the classifier attaches tags — high/low volatility, strong/weak momentum,
overbought/oversold, volume spike or drought, near support/resistance, squeeze, expanding ranges —
and a confidence derived from ADX and ATR.

The same package computes the **deterministic score**: trend, momentum, patterns and volatility
risk each land in [−1, 1] and are folded into a bull/bear split. It is the number the UI shows next
to the model's opinion.

## 7. News: a constraint, never a direction

`internal/news`. RSS/Atom feeds and Bybit announcements are fetched with conditional GET, response
size limits and SSRF protection, then normalised, de-duplicated and clustered: titles are compared
with a lexical similarity (Jaccard over tokens plus bigram Dice) that is bounded by protected
tokens, so two stories can never merge merely because they mention the same asset. Keyword rules
classify each cluster (hack, exploit, delisting, trading suspension, listing, regulation, …) with a
per-rule confidence, and an importance score comes out of category, source priority, freshness and
how many independent sources carried it.

**A news item never argues for a side.** The market's reaction to an event is not deterministic, so
a fresh critical event can only *limit exposure* — cap leverage, or veto an entry outright. The
observed reaction history (how price actually moved after similar past events) is context for the
model and for the statistics, not a direction generator.

## 8. The feature snapshot

`internal/analysis/features` — every timeframe is analysed independently, then aggregated: a trend
alignment score across timeframes, the market overview, data quality (`ok`, `degraded`, `unusable`
depending on how much is missing and how stale it is), and a normalised **feature vector** used for
similarity search.

`internal/history` finds **similar past cases** by cosine similarity over those vectors, rescaled to
[0, 1], with a 5 % bonus for the same symbol — a previous BTC situation says more about BTC than an
equally similar SOL one. Only runs strictly older than the moment being analysed are considered,
and each case carries its real outcome, not its prediction.

## 9. The deterministic vote

`internal/analysis/strategies` — the policy that produces a verdict *without* the model, on the same
snapshot, in the same cycle, stored next to the model's so "LLM vs rules" is answerable on one
history.

Around sixteen directional strategies (composite score, higher-timeframe trend, Donchian breakout,
Supertrend, Connors RSI(2) reversion, regime momentum, EMA trend, ADX/DI, …) vote long or short with
a strength in [0, 1] multiplied by their weight. Ten filters (cost floor, extension guard, trend and
market gates, relative strength, opposing level, timeframe conflict, volatility guard, regime guard,
critical news) vote *against* a trade, and a filter marked **hard veto** blocks the entry regardless
of the weights. An entry is opened only when the directional margin beats both the threshold
(1.8 by default) and the total weight of the filters. Weights can be regime-adaptive: trend-following
opinions count for more while a trend exists, mean-reverting ones inside a range.

The shipped weights were fitted on ~47 600 simulated trades across 12 assets, three timeframes and
two independent periods — one market and five years, so a starting point rather than truth. See
[strategies.md](strategies.md).

## 10. The model layer

`internal/llm` — the full snapshot is always persisted, but only a **compact projection** of it
travels to the model, because the context is finite. The answer must satisfy a strict schema:
enums, ranges, take-profit and stop-loss on the correct side of the entry, close percentages that
sum correctly, an allocation and leverage within the configured bounds. Every problem found is
collected so a single repair retry can address all of them at once; if the repair also fails, the
answer is stored as an inference error and no recommendation is produced. A missing or unreachable
model does not stop anything else — data collection and the whole technical analysis keep running.

## 11. The risk engine

`internal/risk` — deterministic, and it has the last word. From volatility and its percentile, stop
distance, regime, timeframe agreement, nearby opposing levels, stated confidence and data quality it
recomputes the permitted leverage and the position size, then clamps the model's numbers into that
range. The UI shows both figures, the model's suggestion and what was granted.

Two rules carry most of the weight:

* **A stop-out may not erase the position.** With leverage L and a stop D percent away, hitting the
  stop costs roughly L·D percent of margin; that product is capped at 35 %.
* **Risk per trade drives the size.** "A stop-out may cost R % of capital" plus the leverage and the
  stop distance gives the allocation directly, so a quiet asset and a volatile one carry the same
  risk rather than the same size.

A fresh critical news event applies a direction-neutral leverage cap on top, stricter when technical
volatility is already high.

## 12. Grading and statistics

`internal/recommendations/outcomes.go` and `internal/history`. Twenty-four hours after a
recommendation its market outcome is finalised — which level was reached first, the maximum
favourable and adverse excursion, and whether the case is ambiguous because both were touched inside
the same bar. Three kinds of fact are kept strictly apart:

| Kind | What it is |
|---|---|
| `model_prediction` | what the model said |
| `market_outcome` | what the market then did |
| `user_trade_outcome` | what you actually realised |

Only the last two are evidence. The model is never told "you said LONG, so LONG was right".
Predictions are immutable; decisions and outcomes live in their own tables. The statistics screen
derives win rate, profit factor, expectancy, MFE/MAE, drawdown and holding time from them — plus
**calibration**, which asks the only question that matters about stated confidence: when the model
says 90 %, is it right 90 % of the time?

## 13. Replay

`internal/backtesting` — the same analysis path, driven by stored candles. At simulated time T it
receives only bars closed at or before T, an open trade is simulated from the next bar onwards, and
news, the track record and similar cases are cut off the same way, including outcome grading. This
is asserted by regression tests: appending a wildly different future to the data must not change any
earlier trade. Fees, funding, slippage, partial fills, liquidation and maintenance margin are all
simulated. Details in [backtesting.md](backtesting.md).
