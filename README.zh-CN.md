<div align="center">

# Crypto Market Advisor

**本地自托管的加密永续合约市场分析工具 —— 不是交易机器人。**

它读取市场数据，自行完成全部计算，把算好的特征交给本地大模型解读，
再让确定性风控引擎复核，最后给你一条建议。下单由你完成，
它负责记账，并对自己过去的判断打分。

[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8.svg?logo=go&logoColor=white)](https://go.dev)
[![React](https://img.shields.io/badge/React-18-61DAFB.svg?logo=react&logoColor=white)](https://react.dev)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-16-4169E1.svg?logo=postgresql&logoColor=white)](https://www.postgresql.org)
[![数据留在本地](https://img.shields.io/badge/数据-留在本地-success.svg)](#隐私与安全)

[English](README.md) · [Русский](README.ru.md) · **简体中文**

</div>

---

## 它做什么

```
行情数据 + 公开新闻  →  本地归一化与去重
        ↓
指标 · K线与图形形态 · 市场结构 · 支撑阻力 · 背离 · 市场状态
        ↓
历史背景：过往判断的成绩、相似的历史情形
        ↓
紧凑特征快照  →  本地大模型  →  严格的结构校验
        ↓
确定性风控引擎（杠杆与仓位的最终决定权在它手里）
        ↓
建议  →  你的决定  →  手动记录持仓  →  实际结果
        ↓
汇入下一次分析的统计数据
```

大模型只负责解读。它不自行获取价格，不计算任何指标，也不下单——它看到的每个数字都由后端算好。

## 它不做什么

* **不会**开仓、平仓或修改持仓。
* **不会**向任何交易所发送订单。
* **不需要**交易所私有 API 密钥，只访问公开接口。
* **不会**编造数据。缺失的输入会被标记为 `degraded`，被拒绝的模型回复会作为错误保存，而不会当作信号展示。

交易由你手动完成。应用记录你的真实操作，并衡量它的建议究竟有多好。

---

## 核心特性

| | |
|---|---|
| **完全本地运行** | Postgres、后端、界面以及（可选的）模型都跑在你自己的机器上。没有账号，没有遥测，没有第三方服务掌握你的持仓。 |
| **计算全在后端** | 约 25 个指标、约 45 种 K 线形态与 20 种图形形态、含 BOS/CHoCH 的市场结构、聚类后的支撑阻力、RSI/MACD/OBV 背离，以及市场状态分类器——全部确定性实现并有单元测试。 |
| **模型说了不算** | 独立的风控引擎依据波动率、止损距离、市场状态、置信度和数据质量重新计算允许的杠杆与仓位。界面同时显示模型的建议值与风控调整后的结果。 |
| **严格校验回复** | 校验枚举、取值范围、止盈止损方向以及平仓比例之和。不合规的回复只有一次修复重试，之后作为推理错误存档。 |
| **有可比较的基准** | 带硬否决过滤器的加权策略在每个周期给出自己的结论，与模型结论并排保存——"大模型 vs 规则"可以在同一段历史上验证。 |
| **诚实的回测** | 在时刻 T，引擎只能看到收盘时间 ≤ T 的 K 线，这一点由回归测试保证。手续费、资金费率、滑点、部分成交、强平与维持保证金都会被模拟。 |
| **只追加的账目** | 资金全程使用 `decimal`。持仓结果由成交明细推导，绝不覆盖；预测在结果揭晓后不可修改。 |
| **三语界面** | 英语（默认）、俄语和简体中文，模型自己的文字说明同样如此：一次推理生成一条建议的三种语言版本。 |

---

## 快速开始

**需要：** 带 Compose v2 的 Docker。使用内置模型还需要 NVIDIA 显卡与 Container Toolkit。
Go 1.25+ 和 Node 22+ 仅在本地开发时需要。

```bash
git clone https://github.com/crypto-market-advisor/advisor.git
cd advisor
cp .env.example .env
# 编辑 .env —— 至少填入你的手续费费率，详见 docs/configuration.md

docker compose up -d                  # frontend + backend + postgres
# 或者同时启动本地 llama.cpp：
docker compose --profile llm up -d
```

界面在 <http://localhost:3000>，健康检查在 <http://localhost:8080/api/health>。

首次启动时，应用会自动执行数据库迁移、创建默认设置、从 CoinGecko 拉取市值前 20 的可交易资产
（排除稳定币与 wrapped/staked 重复品种）、回补 K 线历史、开始采集公开的 RSS/Atom 与 Bybit 公告，
并在页面顶部显示各依赖组件的状态。

**没有模型也能用。** 大模型关闭或不可用时，数据采集与全部技术分析照常运行，顶部会给出提示，只是不再产生建议。
应用永远不会展示编造的信号。

快捷命令：`make up`、`make up-llm`、`make down`、`make logs`、`make migrate`、`make test`、
`make lint`、`make frontend-test`。每条都对应一句 `docker compose` 或 `go`/`npm` 命令，
[Makefile](Makefile) 中都有列出，方便没有 `make` 的用户。

---

## 日常使用

| 需求 | 位置 |
|---|---|
| 添加或停用资产 | **行情 → 添加资产**（CoinGecko id + 代号）。手动改动不会被每日的前 20 名刷新覆盖。 |
| 立即运行一次分析 | **行情 → 立即分析**，受 `ANALYSIS_MANUAL_COOLDOWN` 限流。 |
| 记录一笔真实交易 | 在建议卡片或资产页面点击"我已开仓"。仓位规模可用数量、名义价值或保证金任一填写，其余自动推导。 |
| 管理持仓 | 部分平仓（占剩余仓位的 25/50/75/100%）、全部平仓、修改止盈止损、手动录入手续费与资金费率。每步操作都会追加到不可变的历史中。 |
| 检验模型 | **历史**与**统计**：胜率、盈利因子、期望值、MFE/MAE、回撤，以及置信度校准——声称的 90% 是否真有 90% 成立。 |
| 验证想法 | **回测**：确定性模式适合长周期，大模型模式适合逐个决策对比。 |

建议不会被真正删除："删除"是软隐藏，预测、推理记录、你的决定与最终结果都保留在数据库和统计中。

---

## 一分钟配置

全部配置集中在 [`.env`](.env.example)，按组分类并带注释。第一天只有三件事要紧：

```env
# 1. 手续费——故意留空。凭空填一个费率会悄悄扭曲你的盈亏。
DEFAULT_MAKER_FEE_PCT=
DEFAULT_TAKER_FEE_PCT=

# 2. 模型在哪里。内置的 llama.cpp，或任何兼容 OpenAI 的服务。
LLM_BASE_URL=http://llm:8080/v1
LLM_MODEL=Qwen3-8B
LLM_MAX_CONCURRENT_REQUESTS=1

# 3. 一次止损最多可以损失本金的百分之几。
RISK_PER_TRADE_PCT=0.75
```

两个费率都未填写时，手续费按未知处理，界面会给出提示，所有受影响的金额都会标注为近似值。
在界面中修改的设置保存在数据库中，其优先级高于 `.env`，因此重启不会回退你的改动；
若要改回环境变量，用 `SETTINGS_FROM_ENV=true` 启动一次即可。

完整参考：**[docs/configuration.md](docs/configuration.md)**（文档为英文）。

---

## 文档

| 文档 | 内容 |
|---|---|
| [docs/configuration.md](docs/configuration.md) | 全部环境变量、设置优先级、手续费、本地大模型及其上下文预算。 |
| [docs/architecture.md](docs/architecture.md) | 模块结构、值得了解的设计取舍，以及 REST API。 |
| [docs/strategies.md](docs/strategies.md) | 确定性策略体系：策略、过滤器、硬否决、内置组合，以及默认值背后的实测数据。 |
| [docs/backtesting.md](docs/backtesting.md) | 两种回测模式、撮合模型、无未来函数的保证，以及离线研究工具。 |
| [docs/development.md](docs/development.md) | 构建、测试、静态检查、集成测试与数据库迁移。 |
| [docs/troubleshooting.md](docs/troubleshooting.md) | 用户真正会遇到的故障，以及每种故障意味着什么。 |

---

## 数据来源

仅使用公开接口，全程无需鉴权：

* **Bybit V5 Market Data** —— 现货合约目录、行情、带成交额的原生 OHLCV，以及已结算的资金费率历史。
  价格与 K 线的主要来源。
* **CoinGecko** —— 提供交易所接口没有的市值与市值排名，据此生成自动的前 20 名单。
  它不是 K 线来源：Bybit 未上架的资产会被标记为不可交易，而不是用近似的 K 线去分析。
* **新闻** —— Bybit 公告加上 RSS/Atom 源（默认为 Ethereum Foundation、CoinDesk、Cointelegraph）。
  采用条件式 GET、响应大小限制，并在重定向与建立连接时防护 SSRF。

新闻永远不指示方向：市场对事件的反应并不确定，因此一条新出现的重大事件只能限制敞口，不能作为做多或做空的理由。

## 隐私与安全

* 没有交易所私钥、不下单、不访问账户——这是设计使然，而非配置选项。
* 密钥保存在 `.env` 中（已被 git 忽略）；浏览器存储中不写入任何敏感信息。
* 模型输出被视为不可信输入：解析、校验、由风控引擎收敛，绝不作为代码执行。

---

## 参与贡献

欢迎提交 issue 与 pull request —— 工作流程、不可让步的原则（无未来函数、资金用 decimal、预测不可修改）
以及检查步骤都写在 [CONTRIBUTING.md](CONTRIBUTING.md) 中。安全问题请见 [SECURITY.md](SECURITY.md)。

## 免责声明

这是一个分析工具。它的预测不保证价格走势，过去的测量结果不会延续到未来，
每一笔交易都是你自己的决定与风险。本项目不构成投资建议。

## 许可证

[Apache License 2.0](LICENSE)。
