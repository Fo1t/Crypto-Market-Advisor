# 数据源：Bybit 与 CoinGecko

[English](data-sources.md) · [Русский](data-sources.ru.md) · **简体中文**

两个数据源，一条规则：**价格归交易所，市值归元数据提供方。** 不会向其中一方索取属于另一方权威范围的
数据，也绝不用错误的来源去重建它。下面列出的所有接口都是公开的——无需账号、无需密钥、无需签名。
本应用根本无法下单，因为它从不进行身份认证。

| | Bybit | CoinGecko |
|---|---|---|
| 回答什么问题 | 这个交易对现在多少钱、走过什么行情、资金费何时结算？ | 这个资产有多大、在整体排名中处于什么位置？ |
| 决定什么 | 价格、K线、资金费率、公告流 | 自动的前 N 名资产池、市值列 |
| 是K线来源吗？ | 是——唯一的来源 | 不是，这是刻意的 |
| 若不可用 | 无法进行分析 | 资产池停止更新，分析照常运行 |

代码：`backend/internal/marketdata/bybit`、`backend/internal/marketdata/coingecko`，在
`backend/internal/marketdata/service.go` 中衔接。

---

## Bybit（公开 V5 行情接口）

基础地址 `https://api.bybit.com`（`BYBIT_MARKET_BASE_URL`）。四个行情接口，外加新闻管线使用的公告流。

| 接口 | 调用方 | 用途 |
|---|---|---|
| `GET /v5/market/instruments-info` | `SupportsSpotSymbol`、`SupportsLinearSymbol`、`Ping` | 交易所是否上架该交易对，当前是否处于 `Trading` |
| `GET /v5/market/tickers?category=spot` | `SpotTickers` | 所有跟踪交易对的最新价、24小时涨跌、成交额、最高/最低价 |
| `GET /v5/market/kline` | `Klines`、`LinearKlines` | 指标、图表与回测读取的 OHLCV 历史 |
| `GET /v5/market/funding/history` | `FundingHistory` | 永续合约已结算的资金费，让回测按真实发生的费率计费 |
| 公告 | `internal/news/bybit.go` | 上币、下架、暂停交易（见[新闻管线](algorithms.zh-CN.md#7-新闻只是约束从不指示方向)） |

### 一个交易对的数据来自哪个市场

资产以 Bybit 交易对形式跟踪（`BTC` → `BTCUSDT`）。任何K线请求之前，先由合约目录决定数据来源：

```
现货有该交易对且状态为 Trading   →  category=spot     （常规情况）
否则线性合约有且状态为 Trading   →  category=linear   （只有永续的资产）
两者都没有                       →  ErrNotTradable —— 如实报告，而不是伪造数据
```

每个类别的完整目录按页读取（每页 1000 条，游标分页，最多 10 页）并**缓存一小时**，因此该检查对单个
交易对几乎不产生成本。目录请求失败时按“是”处理：因为一次查询超时就剔除某个资产，会在网络不稳定时
悄悄缩小资产池。

### K线是怎么取回来的

`GET /v5/market/kline` 每次最多返回 1000 根K线，从最新开始。客户端**向后**遍历时间窗：请求
`[from, end]`，把 `end` 移到本次收到的最老K线之前，再次请求——最多 100 页，即每个周期每次调用最多约
100 000 根。重复的开盘时间按时间戳去重，结果按时间升序排序。周期与 Bybit interval 的对应关系为
`1m→1`、`5m→5`、`15m→15`、`1h→60`、`4h→240`、`1d→D`。

正在形成的K线在写入前被丢弃（`ClosedOnly`）。只有已收盘的K线才会进入数据库——正是这一点让指标不会对
尚未结束的一根K线做出反应。

**定时补齐**（`BackfillCandles`，启动时以及每次每日资产池刷新后运行）从每个周期已存最新一根K线继续
向前，并重取一根重叠K线，以便捕捉被修正的数据。首次运行会按周期各自固定的时间窗回溯：

| 周期 | 1m | 5m | 15m | 1h | 4h | 1d |
|---|---|---|---|---|---|---|
| 首次运行的历史 | 48 小时 | 30 天 | 180 天 | 3 年 | 6 年 | 10 年 |

如果之后把该时间窗调大，更早的那部分需要单独抓取——从最新K线向前推进永远够不到它。

**手动导入**（设置 → *历史数据*）方向相反：你指定资产、一组周期和一个时间段，就下载这一段。它适合
两件事：取回比自动时间窗更长的历史，以及修复当初带缺口的时段。K线按主键 upsert，所以同一时间段导入
两次只会改变 `updated_at` 列。同一时刻只运行一个任务，且任务运行在后端——关闭页面不会中断它。
REST：`POST /api/markets/import`、`GET /api/markets/import`、`POST /api/markets/import/cancel`。

### 资金费率

`BackfillFunding` 保存每个资产线性永续的已结算资金费：每八小时一条，按每页 200 条向后翻页。首次运行
回溯五年，使得基于已存K线的回测能按真实生效的费率计费；后续运行从已存最新一条结算继续，并保留一个
结算间隔的重叠。没有永续合约的资产自然就没有资金费——这是资产的事实，而非抓取失败。

### 价格

`IngestPrices` 按自己的节奏运行（`MARKET_DATA_INTERVAL`，默认一分钟），并且对所有跟踪交易对只发出
**一次** ticker 请求，因此行情总览的每一行共享同一个交易所时间戳。价格、24小时涨跌、成交额与最高/最低
价来自 Bybit；市值与排名由 CoinGecko 合并进来。交易所没有价格的资产会被跳过，并在状态信息中点名，而不是
带着过期数字继续展示。

### 频率限制、重试与健康状态

客户端封装在 `internal/httpx` 中：默认每分钟 300 次请求（`BYBIT_MARKET_RATE_LIMIT_RPM`）、15 秒超时、
2 次带指数退避与抖动的重试，遇到 429 时遵守 `Retry-After`——被暂停的是限流器本身，因此不会形成重试风暴。

健康检查（`GET /api/health/market-data`）在最近一次成功调用不足两分钟时直接读取记录的状态，之后才真正
发起 ping。真正会让分析停摆的是 Bybit 缺席，所以探测的是它；如果最近 15 分钟内仍有数据流入，状态为
`degraded` 而不是 `offline`——一次被拖慢的探测并不等于故障。

---

## CoinGecko（公开 REST API）

基础地址 `https://api.coingecko.com/api/v3`（`COINGECKO_BASE_URL`）。只调用两个接口，默认不带密钥；
可通过 `COINGECKO_API_KEY` 提供 demo 密钥，以 `x-cg-demo-api-key` 请求头发送。

| 接口 | 调用方 | 用途 |
|---|---|---|
| `GET /ping` | `Ping` | 为 `GET /api/health/market-data` 提供存活检测 |
| `GET /coins/markets` | `TopMarkets`、`MarketsByIDs` | 市值及其排名，并由此得到自动资产池 |

响应缓存 45 秒（`COINGECKO_CACHE_TTL`），客户端限速每分钟 25 次请求（`COINGECKO_RATE_LIMIT_RPM`），
这是免费额度能够承受的水平。

### 资产池刷新到底做了什么

`RefreshUniverse` 在启动时以及每天运行一次（`MARKET_UNIVERSE_REFRESH`）：

1. 向 CoinGecko 请求市值前 `MARKET_UNIVERSE_SIZE × 3` 的资产（上限 250）——刻意多取，因为其中大部分
   马上会被过滤掉。
2. 剔除不是独立可交易方向性标的的品种：稳定币，以及按符号和名称识别出的封装、质押、再质押与跨链
   衍生资产。
3. 剔除 Bybit 没有上架的品种。市值不等于可交易性：没有交易对的资产只会成为一行永远无法分析的记录。
4. 保留前 `MARKET_UNIVERSE_SIZE` 个幸存者（默认 20 个）：新的创建，已跟踪的更新排名与显示名称。

**你的选择永远优先。** 手动添加的资产不会被移除，被排除的资产不会被重新加入，启用/置顶/排除这些标记
在每次刷新后都会保留。

### 为什么它不是K线来源

CoinGecko 免费额度没有原生的分钟级 OHLC；用其采样价格拼出的K线看起来像真的，其实不是。项目过去正是
这么做的，后来停止了：Bybit 没有上架的资产现在会如实标记为不可交易，而不是拿近似K线去分析。每根存入
的K线都记录了来源提供方（`ohlcv_candles.provider`）与生成方式（`source`），因此旧的派生K线始终能与
原生K线区分开。

---

## 各任务的运行节奏

| 循环 | 频率 | 访问对象 |
|---|---|---|
| 价格抓取 | `MARKET_DATA_INTERVAL`（1 分钟） | Bybit tickers + CoinGecko markets |
| 分析周期 | `ANALYSIS_INTERVAL`（5 分钟，对齐K线收盘） | 仅数据库 |
| 资产池刷新 + 全量补齐 | `MARKET_UNIVERSE_REFRESH`（24 小时） | 先 CoinGecko，再 Bybit 目录/K线/资金费 |
| 新闻采集 | `NEWS_FETCH_INTERVAL` | Bybit 公告 + RSS/Atom |
| 手动导入 | 按需 | Bybit K线 |

## 数据源不可用时会发生什么

| 情况 | 行为 |
|---|---|
| Bybit 不可达 | 没有价格，也没有新K线。`market_data` 变为 `degraded`，若 15 分钟没有数据则转为 `offline`。基于过期数据的分析会标记为 `degraded`/`unusable`，不会冒充为新鲜结果。 |
| CoinGecko 不可达 | 价格仍从 Bybit 流入；市值列停止更新，资产池刷新失败并在日志中告警。其他部分不受影响。 |
| 交易对被下架 | 目录显示其状态不是 `Trading`，补齐返回 `ErrNotTradable`，资产保留既有历史但不再获得新K线。 |
| 收到 429 | 限流器按 `Retry-After` 暂停，请求带退避重试，故障体现为 `degraded` 而不是数据缺失。 |

## 配置

以上全部可在 `.env` 中调整；完整参考见 [configuration.md](configuration.md)。

```env
BYBIT_MARKET_DATA_ENABLED=true
BYBIT_MARKET_BASE_URL=https://api.bybit.com
BYBIT_MARKET_RATE_LIMIT_RPM=300
BYBIT_MARKET_TIMEOUT=15s

COINGECKO_BASE_URL=https://api.coingecko.com/api/v3
COINGECKO_API_KEY=                 # 可选的 demo 密钥
COINGECKO_RATE_LIMIT_RPM=25
COINGECKO_CACHE_TTL=45s
MARKET_UNIVERSE_SIZE=20
MARKET_UNIVERSE_REFRESH=24h
```
