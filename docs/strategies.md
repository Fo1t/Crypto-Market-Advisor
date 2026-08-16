# The deterministic policy

Alongside the model, the application runs a rule-based decision engine. It reads the analysis that
has already been computed — no extra indicator is calculated for it — and behaves identically in
the live cycle and in a backtest. Its verdict is stored next to the model's, which is what makes
"LLM versus rules" answerable on one history.

## How a decision is made

Strategies come in two kinds.

**Directional strategies** vote long or short with a strength between 0 and 1, multiplied by their
weight: EMA 50/200 trend, ADX with DI direction, MACD, market structure with BOS/CHoCH, chart and
candlestick patterns, divergences, RSI mean reversion, range breakout, momentum, volume
confirmation, position relative to VWAP, and the aggregate technical score.

Three classic systems ship implemented from their public descriptions: Donchian channel breakout
(the Turtle entry, 20 and 55 bars), Supertrend on ATR(10) with a multiplier of 3, and Larry
Connors' RSI(2) rule with an SMA200 filter. Only RSI(2) is enabled by default — on the daily grid it
improved both periods, while Donchian was neutral and Supertrend won one period and lost another.

**Filters** vote against a trade: an opposing support or resistance level, timeframe disagreement,
extreme or dead volatility, an unreadable regime, a fresh critical news event, and the trade-cost
floor.

```text
edge     = |sum of long weights − sum of short weights|
blocking = sum of the filter weights that oppose this particular side

enter if edge > threshold AND edge > blocking
```

Two strategies weighing 0.7 and 0.3 for a short will not open anything against a filter of weight
2: 1.0 < 2.0. Each filter also has a **hard veto** switch that forbids the entry regardless of
weights; by default only a fresh critical news event uses it.

Filter thresholds are relative to the instrument, not to absolute percentages: an opposing level
counts as an obstacle when it sits closer than three quarters of the average candle range (ATR), and
volatility is judged by the percentile of its own history. Absolute thresholds would fire on nearly
every 5m bar and turn into a permanent tax on the signal.

**Confidence** combines two things: how one-sided the vote was, and how much of the enabled policy
spoke at all. Both matter — a lone strategy nobody contradicted is not the same as half the set
pulling one way. On real ETH 5m data this spreads roughly 60–95 with a median near 67. It then
passes through the same thresholds and the same risk engine as a model recommendation.

Everything is editable under **Settings**: enabled state, weight, hard veto and the overall edge
threshold. Starting a backtest snapshots the current policy into the run's parameters, so an old run
stays reproducible after the settings change, and the report shows which strategies voted for each
trade. A negative weight inverts a strategy — that is how a counter-trend profile is expressed.

News never sets a direction. The market's reaction to an event is not deterministic, so news can
only restrict an entry.

## Shipped profiles

Four whole policies, each measured as a unit over the same two grids: seven 4h windows spanning five
years (three of them falling markets) and four separate years of daily bars, fourteen assets, with
each perpetual's actual funding charged every eight hours.

| Profile | PF 4h | PF 1d | Worst window | Trades |
|---|---|---|---|---|
| **Regime momentum + Supertrend** (default) | **1.27** | **1.56** | 0.54 | 1215 |
| Regime momentum only | 1.18 | 1.43 | 0.26 | 1012 |
| Core trio | 1.07 | 1.33 | 0.50 | 1555 |
| Broad ensemble (the earlier default) | 1.04 | 1.27 | 0.52 | 1886 |

Choosing a profile changes only the directional strategies and the edge threshold; the protective
vetoes (news, trade cost, trend and market gates) stay in place. Editing any weight by hand clears
the "profile selected" state, and the UI says so.

The default profile is built around a rule found by direct measurement: in a rising market a strong
price thrust yields +8.7 points of return above baseline ten days later, and that effect is positive
in 100% of block-bootstrap resamples. The broad ensemble remains available — its weakest daily
window is the mildest of the four.

## The defaults, and why

**Where the weights came from.** A grid of 216 runs (12 assets × three timeframes × three
thresholds × two independent periods, 47,593 trades). MACD, momentum, VWAP, candlestick patterns
and market structure were consistently useful; divergences were harmful on a large sample and were
given a low weight.

**Why long-only by default.** For every signal the realised return of the next 1, 3, 5, 10, 20 and
40 candles was measured separately for longs, shorts and bars with no signal. Across four
independent years of daily candles and three 4h windows, longs beat the no-signal baseline while
shorts were negative in almost every window — including the falling 2022–23 year, where a short with
any edge should have earned the most. Raising the confidence threshold made longs better and shorts
worse, which is how a rule with a negative edge behaves, not a weak one. Directions are switchable
in the strategy editor.

**Trend gate.** Forbids a trade against the slowest analysed timeframe (price on the wrong side of
its EMA200). As a weighted argument it changed nothing, so it ships as a hard veto — in that form it
raised profit factor and improved the worst daily period. A stricter variant additionally requiring
EMA50 and EMA200 to agree was tested and rejected: it cut the entry at the start of every new trend
(1.25 → 1.20 daily, 1.16 → 1.10 on 4h).

**Market gate.** Crypto assets move together, so an asset with a clean chart of its own still
swims against the tide while the benchmark (BTC by default, `ANALYSIS_BENCHMARK_SYMBOL`) is below
its daily EMA200. Per-symbol analysis cannot see this, and it accounted for nearly all of the
long-only loss in falling markets. It ships as a hard veto. With no benchmark data it stays silent
rather than blocking — a filter that fires on missing data would halt the system for no reason.

**Trade cost floor.** `cost_floor` refuses an entry when a realistic target (two average candle
ranges) is worth less than three round-trip costs at your fees and slippage. On 5m at 10x, a round
trip eats about 1.5% of margin; without this floor a trade does not pay for itself even when the
direction is right.

**Trailing exit instead of fixed targets.** A new run defaults to a Chandelier stop at 2.5 average
candle ranges. On the same history it beat every fixed geometry tested (1.25 versus 1.06–1.11 daily,
1.16 versus 1.03–1.08 on 4h) and proved least sensitive to its own parameter: any multiplier from 2
to 5 performs about the same. The "signal" geometry was widened too — stop 1.5 ATR, targets 3.0 and
6.0 ATR.

**Limit entry on a pullback.** Instead of buying the close of the signal candle, a new run rests a
limit order 0.5 average candle ranges below (for a long) and waits three bars; unfilled means the
signal is forgotten. Half the benefit is arithmetic: a limit order pays the maker fee and does not
cross the spread, which is 0.055% of notional on every trade. Exit levels shift with the actual
fill. On the stored history this turned all four independent years profitable at the account level.

**Actual funding.** Settlement history comes from Bybit's public
`/v5/market/funding/history` — no API key, one row per eight hours per asset. A trailing exit holds
positions for dozens of hours, so a long pays funding dozens of times, and assuming zero flattered
every result. With real funding, profit factor went 1.35 → 1.28 on daily and 1.20 → 1.16 on 4h: the
edge survives, the numbers are honest.

**Break-even stop after the first partial** is available as a checkbox but ships off: on stored runs
it also took out the trades that would have reached the second target, and the total was worse
(ETH 5m −15.2% versus −13.9%). Test it on your own data before enabling.

**Two switches ship off** because measurement did not support them: "account for market regime"
(strengthening trend votes in a trend and counter-trend votes in a range) and the extension filter
(do not join a move further than 1.2 ATR from EMA20). Both ideas are reasonable, both are one
checkbox away, and neither improved our history.

## What the history showed

Measured on real stored candles through `cmd/lab` with actual funding. "Before" is the profile the
project used earlier, "after" is the current defaults.

| Timeframe | Windows | Trades before/after | Profit factor before/after | Avg trade before/after | Avg drawdown before/after |
|---|---|---|---|---|---|
| 1d | 4 independent years, 14 assets | 1776 / 570 | 1.00 / **1.28** | −0.05% / **+7.06%** | 13.7% / **11.0%** |
| 4h | 3 windows, 15 assets | 4593 / 767 | 0.97 / **1.16** | −0.30% / **+2.11%** | 12.0% / **6.7%** |
| 1h | 2 windows, 15 assets | 10,145 / 545 | 0.84 / 0.89 | −0.68% / −0.67% | 16.1% / **3.2%** |
| 15m | 2 windows, 15 assets | 9030 / 0 | 0.62 / — | −0.91% / — | 13.1% / **0%** |

On 1d and 4h the system moved from loss to profit. The loss per run fell from −9.60% to −0.63% on
1h and from −11.84% to zero on 15m — but on 15m the market filter let no trade through at all in the
tested windows, so that is "stopped trading", not "became profitable"; in a different market trades
will appear there and, judging by 1h, still will not pay. That is why the deterministic decision is
taken on 4h (then 1d), with faster timeframes kept as context.

The worst period stays negative: a long-only policy cannot survive the falling 2022–23 year. The
loss shrank from −5.05% to −0.55% per run and the drawdown from 13.2% to 3.8%.

**What these numbers do not mean.** Buy and hold over the same windows returned +53.5% (1d) and
+32.2% (4h). A system trading 5% allocation at 5x carries far less exposure and does not beat the
market in absolute return. What improved is the quality of a trade, not portfolio performance.

**Position sizing matters more than any of it.** A per-symbol profit factor of 1.28 does not mean a
profitable portfolio: pooling every asset's trades into one account gave −34% with an 85% drawdown
in 2024–25, because crypto assets get stopped out together. With risk-based sizing
(`RISK_PER_TRADE_PCT`) the same year gives −2.5% with a 20% drawdown, and the money-weighted profit
factor improved in six windows out of seven. A control run with a fixed allocation of the same
average size does worse — so it is the risk equalisation doing the work, not the smaller size.

**Tested and rejected.** The strict trend filter with the EMA50/EMA200 condition (1.25 → 1.20
daily). Raising the `cost_floor` multiple from 3 to 16: on 1h it removes up to half the trades while
the loss per run stays the same (−0.59% versus −0.53%), meaning the removed trades were no worse
than the rest. Re-fitting the directional weights to the new regime: 1.23–1.31 spread under
one-at-a-time ablation, i.e. noise.

**How often it trades.** The signal is checked on every candle and, with a one-position limit, the
engine re-enters immediately after an exit. For rarer entries raise the minimum edge (1.8 by
default) and the confidence threshold, widen the analysis interval, or move to a slower timeframe.

---

Every figure on this page came from `cmd/lab` on stored candles — see
[backtesting.md](backtesting.md#offline-research-harness) for how to reproduce them. They describe
the past of a specific dataset. They are not a forecast.
