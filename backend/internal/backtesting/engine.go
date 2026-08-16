// Package backtesting replays historical candles through the same analysis
// code the live system uses.
//
// The single most important property here is the absence of look-ahead: at
// simulated time T the engine only ever passes candles[:T] into the analysis,
// and the forward simulation of an open trade starts strictly after the entry
// bar. The regression test in this package asserts exactly that.
package backtesting

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/crypto-market-advisor/advisor/internal/analysis/features"
	"github.com/crypto-market-advisor/advisor/internal/analysis/strategies"
	"github.com/crypto-market-advisor/advisor/internal/config"
	"github.com/crypto-market-advisor/advisor/internal/domain"
	"github.com/crypto-market-advisor/advisor/internal/llm"
	"github.com/crypto-market-advisor/advisor/internal/logging"
	"github.com/crypto-market-advisor/advisor/internal/news"
	"github.com/crypto-market-advisor/advisor/internal/repository"
	"github.com/crypto-market-advisor/advisor/internal/risk"
)

// WarmupBars is how much history the analysis needs before the first signal.
const WarmupBars = 250

// MaxConsecutiveLLMFailures is how many failed inferences in a row end an LLM
// replay. One failure is a hiccup worth stepping over; a handful in a row is a
// model that is down, and continuing only spends the run's remaining hours on
// timeouts that produce no decisions.
const MaxConsecutiveLLMFailures = 5

// Engine executes backtest runs.
type Engine struct {
	repos   *repository.Repositories
	llm     *llm.Service
	risk    *risk.Engine
	history HistoryProvider
	cfg     config.Config
	log     *slog.Logger

	mu      sync.Mutex
	running map[uuid.UUID]context.CancelFunc

	cfgMu      sync.RWMutex
	strategies domain.StrategySet
}

// SetStrategySet applies the UI-edited deterministic policy. A run started
// later snapshots it into its own parameters.
func (e *Engine) SetStrategySet(set domain.StrategySet) {
	e.cfgMu.Lock()
	e.strategies = set
	e.cfgMu.Unlock()
}

// strategySet prefers the policy stored with the run, so re-running an old
// backtest reproduces it even after the settings changed.
func (e *Engine) strategySet(run domain.BacktestRun) domain.StrategySet {
	if run.Params.Strategies != nil && len(run.Params.Strategies.Items) > 0 {
		return *run.Params.Strategies
	}
	e.cfgMu.RLock()
	defer e.cfgMu.RUnlock()
	if len(e.strategies.Items) == 0 {
		return strategies.DefaultSet()
	}
	return e.strategies
}

// SetAnalysisConfig applies UI-edited analysis controls so a backtest replays
// the same timeframes the live cycle currently uses.
func (e *Engine) SetAnalysisConfig(cfg config.AnalysisConfig) {
	e.cfgMu.Lock()
	e.cfg.Analysis = cfg
	e.cfgMu.Unlock()
}

// SetNewsConfig applies UI-edited news controls, so a replay reads the same
// lookback and limits the live analysis currently uses.
func (e *Engine) SetNewsConfig(cfg config.NewsConfig) {
	e.cfgMu.Lock()
	e.cfg.News = cfg
	e.cfgMu.Unlock()
}

func (e *Engine) analysisConfig() config.AnalysisConfig {
	e.cfgMu.RLock()
	defer e.cfgMu.RUnlock()
	return e.cfg.Analysis
}

// NewEngine builds the backtesting engine. historySvc may be nil, which leaves
// an LLM replay without the retrieval context the live prompt carries.
func NewEngine(repos *repository.Repositories, llmSvc *llm.Service, riskEngine *risk.Engine, historySvc HistoryProvider, cfg config.Config, logger *slog.Logger) *Engine {
	return &Engine{
		repos: repos, llm: llmSvc, risk: riskEngine, history: historySvc, cfg: cfg,
		log:     logging.For(logger, logging.CategoryBacktest),
		running: map[uuid.UUID]context.CancelFunc{},
	}
}

// EstimateSteps returns how many analysis points a run would evaluate, which
// the UI shows before an LLM backtest is allowed to start.
func EstimateSteps(params domain.BacktestParams) int {
	tf := params.Timeframe
	if !tf.Valid() {
		return 0
	}
	step := tf.Duration()
	if params.AnalysisInterval != "" {
		if d, err := time.ParseDuration(params.AnalysisInterval); err == nil && d > step {
			step = d
		}
	}
	span := params.DateTo.Sub(params.DateFrom)
	if span <= 0 || step <= 0 {
		return 0
	}
	return int(span / step)
}

// Run executes a backtest and stores its trades and metrics.
func (e *Engine) Run(ctx context.Context, run domain.BacktestRun, asset domain.Asset) error {
	runCtx, cancel := context.WithCancel(ctx)
	e.mu.Lock()
	e.running[run.ID] = cancel
	e.mu.Unlock()
	defer func() {
		cancel()
		e.mu.Lock()
		delete(e.running, run.ID)
		e.mu.Unlock()
	}()

	if err := e.repos.Backtests.SetStatus(runCtx, run.ID, domain.BacktestRunning, nil, ""); err != nil {
		return err
	}

	trades, metrics, curve, err := e.simulate(runCtx, run, asset)
	if err != nil {
		// Persist the failure even if the caller's context died mid-run.
		failCtx, failCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer failCancel()
		status := domain.BacktestFailed
		if errors.Is(err, context.Canceled) {
			status = domain.BacktestCanceled
		}
		_ = e.repos.Backtests.SetStatus(failCtx, run.ID, status, nil, err.Error())
		return err
	}

	if err := e.repos.Backtests.InsertTrades(runCtx, trades); err != nil {
		return fmt.Errorf("store backtest trades: %w", err)
	}
	if err := e.repos.Backtests.SaveEquityCurve(runCtx, run.ID, curve); err != nil {
		return fmt.Errorf("store backtest equity curve: %w", err)
	}
	if err := e.repos.Backtests.SetStatus(runCtx, run.ID, domain.BacktestCompleted, &metrics, ""); err != nil {
		return err
	}
	// The estimate counts the requested date range; the replay only covers the
	// part the stored candles reach. Reporting the estimate here would show a
	// full progress bar for a run that silently replayed a fraction of it.
	_ = e.repos.Backtests.UpdateProgress(runCtx, run.ID, metrics.AnalysisPoints)

	e.log.Info("backtest finished",
		slog.String("symbol", run.Symbol),
		slog.String("mode", string(run.Mode)),
		slog.Int("trades", metrics.Trades),
		slog.Float64("return_pct", metrics.TotalReturnPct))
	return nil
}

// Cancel stops a running backtest.
func (e *Engine) Cancel(id uuid.UUID) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	cancel, ok := e.running[id]
	if ok {
		cancel()
	}
	return ok
}

// simulate is the core replay loop.
func (e *Engine) simulate(ctx context.Context, run domain.BacktestRun, asset domain.Asset) ([]domain.BacktestTrade, domain.BacktestMetrics, []domain.EquityPoint, error) {
	analysisCfg := e.analysisConfig()
	timeframes := []domain.Timeframe{run.Timeframe}
	if configured, err := domain.ParseTimeframes(analysisCfg.Timeframes); err == nil && len(configured) > 0 {
		timeframes = configured
		found := false
		for _, tf := range timeframes {
			found = found || tf == run.Timeframe
		}
		if !found {
			timeframes = append(timeframes, run.Timeframe)
		}
	}

	series := make(map[domain.Timeframe][]domain.Candle, len(timeframes))
	for _, tf := range timeframes {
		warmup := WarmupBars
		if analysisCfg.CandleHistoryLimit > warmup {
			warmup = analysisCfg.CandleHistoryLimit
		}
		from := run.Params.DateFrom.Add(-tf.Duration() * time.Duration(warmup))
		candles, err := e.repos.Candles.Range(ctx, asset.ID, tf, from, run.Params.DateTo)
		if err != nil {
			return nil, domain.BacktestMetrics{}, nil, fmt.Errorf("load %s candles: %w", tf, err)
		}
		series[tf] = closedThrough(candles, run.Params.DateTo)
	}
	primary := series[run.Timeframe]
	if len(primary) < features.MinCandles+5 {
		return nil, domain.BacktestMetrics{}, nil, fmt.Errorf("not enough stored candles for %s %s: have %d", run.Symbol, run.Timeframe, len(primary))
	}

	// An LLM step costs seconds, so its progress is written on every step; a
	// technical step costs microseconds and only needs an occasional write.
	stride := 25
	if run.Mode == domain.BacktestLLM {
		stride = 1
	}
	progress := func(completed int) {
		if completed%stride == 0 {
			_ = e.repos.Backtests.UpdateProgress(ctx, run.ID, completed)
		}
	}
	benchmark, err := e.benchmarkSeries(ctx, run)
	if err != nil {
		return nil, domain.BacktestMetrics{}, nil, err
	}
	funding, err := e.fundingSeries(ctx, run, asset)
	if err != nil {
		return nil, domain.BacktestMetrics{}, nil, err
	}
	universe, err := e.universeRanker(ctx, run)
	if err != nil {
		return nil, domain.BacktestMetrics{}, nil, err
	}
	return e.simulateSeries(ctx, run, SimulationInputs{
		Series: series, Benchmark: benchmark, funding: funding, Universe: universe,
		News: e.newsContext(asset), History: e.history,
	}, progress)
}

// newsContext wires the same snapshot builder the live analysis uses, bound to
// the asset under test. It returns nil when there is no store to read from, so
// the offline harness keeps running without a database.
func (e *Engine) newsContext(asset domain.Asset) NewsContextProvider {
	if e.repos == nil || e.repos.News == nil {
		return nil
	}
	e.cfgMu.RLock()
	newsCfg := e.cfg.News
	e.cfgMu.RUnlock()
	if !newsCfg.Enabled {
		return nil
	}
	return &replayNews{
		builder: news.NewSnapshotBuilder(newsCfg, e.repos.News),
		assetID: asset.ID,
		log:     e.log,
	}
}

// replayNews answers with the news context of a replayed bar. A failure is
// logged once and then reported in-band, exactly as it is live: missing news
// degrades a decision, it does not abort the replay.
type replayNews struct {
	builder *news.SnapshotBuilder
	assetID int64
	log     *slog.Logger
	failed  bool
}

func (r *replayNews) NewsContextAt(ctx context.Context, at time.Time) domain.NewsSnapshot {
	snapshot, err := r.builder.Build(ctx, r.assetID, at)
	if err != nil && !r.failed {
		r.failed = true
		r.log.Debug("news context unavailable during replay", slog.String("error", err.Error()))
	}
	return snapshot
}

// benchmarkSeries loads the daily candles of the asset the market-wide filter
// judges the tide by. A missing benchmark is not an error: the filter treats an
// unknown context as no argument, so the run simply proceeds without it.
func (e *Engine) benchmarkSeries(ctx context.Context, run domain.BacktestRun) ([]domain.Candle, error) {
	symbol := e.analysisConfig().BenchmarkSymbol
	if symbol == "" || e.repos == nil {
		return nil, nil
	}
	asset, err := e.repos.Assets.GetBySymbol(ctx, symbol)
	if err != nil {
		e.log.Debug("benchmark asset unavailable", slog.String("symbol", symbol))
		//nolint:nilerr // a missing benchmark degrades the run, it does not fail it
		return nil, nil
	}
	// The average needs its own warmup before the first bar of the window.
	from := run.Params.DateFrom.Add(-time.Duration(features.MarketContextEMAPeriod+50) * 24 * time.Hour)
	candles, err := e.repos.Candles.Range(ctx, asset.ID, domain.TF1d, from, run.Params.DateTo)
	if err != nil {
		return nil, fmt.Errorf("load benchmark candles: %w", err)
	}
	return closedThrough(candles, run.Params.DateTo), nil
}

// universeRanker builds the cross-sectional ranking from the daily candles of
// every tracked asset. A universe that cannot be read is not an error: the
// filters treat a missing ranking as no argument, exactly as they do a missing
// benchmark.
func (e *Engine) universeRanker(ctx context.Context, run domain.BacktestRun) (UniverseRanker, error) {
	if e.repos == nil {
		return nil, nil
	}
	// Ranking the universe means loading the daily history of every tracked
	// asset. Nothing reads the result unless the cross-sectional filter is on, so
	// a run that does not use it should not pay for it.
	if cfg, ok := e.strategySet(run).Find(strategies.IDRelStrengthGate); !ok || !cfg.Enabled {
		return nil, nil
	}
	assets, err := e.repos.Assets.List(ctx, true)
	if err != nil {
		e.log.Debug("universe unavailable for ranking", slog.String("error", err.Error()))
		return nil, nil
	}
	from := run.Params.DateFrom.Add(-time.Duration(features.RelativeStrengthLookback+30) * 24 * time.Hour)
	universe := make(map[string][]domain.Candle, len(assets))
	for _, asset := range assets {
		candles, err := e.repos.Candles.Range(ctx, asset.ID, domain.TF1d, from, run.Params.DateTo)
		if err != nil {
			return nil, fmt.Errorf("load %s daily candles for ranking: %w", asset.Symbol, err)
		}
		if closed := closedThrough(candles, run.Params.DateTo); len(closed) > 0 {
			universe[asset.Symbol] = closed
		}
	}
	if len(universe) < 2 {
		return nil, nil
	}
	return features.NewDailyRanker(universe), nil
}

// marketContextAt reads the benchmark state as it stood at the given moment.
// Only candles that had already closed are visible, which is what keeps the
// market-wide filter free of look-ahead.
func marketContextAt(benchmark []domain.Candle, at time.Time, symbol string) domain.MarketContext {
	if len(benchmark) == 0 {
		return domain.MarketContext{}
	}
	end := sort.Search(len(benchmark), func(i int) bool { return benchmark[i].CloseTime.After(at) })
	return features.MarketContextFrom(symbol, benchmark[:end])
}

// simulateCandles replays a candle series. It is separated from loading so the
// look-ahead regression test can drive it with synthetic data.
func (e *Engine) simulateCandles(ctx context.Context, run domain.BacktestRun, candles []domain.Candle) ([]domain.BacktestTrade, domain.BacktestMetrics, []domain.EquityPoint, error) {
	return e.simulateSeries(ctx, run, SimulationInputs{Series: map[domain.Timeframe][]domain.Candle{run.Timeframe: candles}}, nil)
}

// Simulate replays an already loaded candle series and returns the result
// without persisting anything. It is the entry point for offline research: a
// parameter sweep runs thousands of replays, and none of them belongs in the
// backtest_runs table. The code path is the same one a stored run takes, so a
// result found offline reproduces in the UI.
func (e *Engine) Simulate(ctx context.Context, run domain.BacktestRun, in SimulationInputs) ([]domain.BacktestTrade, domain.BacktestMetrics, []domain.EquityPoint, error) {
	return e.simulateSeries(ctx, run, in.withSchedule(), nil)
}

// SimulationInputs is everything a replay reads besides the run itself. It is a
// struct rather than a parameter list because the context a decision needs keeps
// growing - the benchmark, the funding history, the standing among peers - and
// each addition would otherwise change the signature for every caller.
type SimulationInputs struct {
	// Series is the candle history per timeframe for the traded symbol.
	Series map[domain.Timeframe][]domain.Candle
	// Benchmark is the daily history of the market proxy.
	Benchmark []domain.Candle
	// Funding is the settled funding history of the traded perpetual.
	Funding []domain.FundingRate
	// Universe reports where the symbol stood among its peers. Nil means the
	// comparison was unavailable, which the filters treat as no argument.
	Universe UniverseRanker
	// News supplies the news context that was knowable at each replayed moment.
	// Nil leaves the snapshot without one, which is what the offline harness and
	// the unit tests run with; a live run passes the stored history so the news
	// filter and the news leverage cap behave as they do in production.
	News NewsContextProvider
	// History supplies the track record and the similar past cases the live
	// analysis shows the model. Nil omits both, which is what a deterministic
	// replay needs: the rules never read them.
	History HistoryProvider

	// RandomEntryChance replaces the policy with a coin toss: at every analysis
	// point a long is opened with this probability, and the strategies are not
	// consulted at all. Everything downstream - the exit machinery, the sizing,
	// the fees, the funding - stays exactly as it is.
	//
	// This is the control group. A long-only policy in a rising market shows a
	// profit factor above one whether or not it predicts anything, so the number
	// that matters is not the strategy's own figure but the gap between it and
	// random entries matched on asset, period and holding machinery.
	RandomEntryChance float64
	// RandomEntrySeed makes one replication reproducible.
	RandomEntrySeed int64

	funding map[int64]float64
}

// UniverseRanker reports the standing of one symbol among the tracked assets at
// a moment in time.
type UniverseRanker interface {
	RankAt(symbol string, at time.Time) domain.UniverseContext
}

// NewsContextProvider returns the news context of one replayed moment. It must
// only report what was published by then: the whole value of replaying news is
// lost if the snapshot of an old bar knows what came after it.
type NewsContextProvider interface {
	NewsContextAt(ctx context.Context, at time.Time) domain.NewsSnapshot
}

// HistoryProvider supplies the track record and the comparable past situations
// the live analysis puts in front of the model. Both are bounded by the replayed
// moment: a decision may only see predictions that were already graded by then.
type HistoryProvider interface {
	PerformanceAt(ctx context.Context, symbol string, at time.Time) (domain.HistoricalPerformance, error)
	SimilarCases(ctx context.Context, vector []float64, symbol string, limit int, before time.Time) ([]domain.SimilarCase, error)
}

func (in SimulationInputs) withSchedule() SimulationInputs {
	in.funding = fundingSchedule(in.Funding)
	return in
}

// EquityCurveMaxPoints bounds what is stored for the chart: a year of 5m bars
// would otherwise be a hundred thousand samples nobody can see.
const EquityCurveMaxPoints = 1500

func (e *Engine) simulateSeries(ctx context.Context, run domain.BacktestRun, in SimulationInputs, progress func(int)) ([]domain.BacktestTrade, domain.BacktestMetrics, []domain.EquityPoint, error) {
	params := run.Params
	tf := run.Timeframe
	series, benchmark, funding := in.Series, in.Benchmark, in.funding
	candles := series[tf]

	stepBars := 1
	if params.AnalysisInterval != "" {
		if d, err := time.ParseDuration(params.AnalysisInterval); err == nil && d > tf.Duration() {
			stepBars = int(d / tf.Duration())
		}
	}

	state := newSimState(params)
	var dice *rand.Rand
	if in.RandomEntryChance > 0 {
		dice = rand.New(rand.NewSource(in.RandomEntrySeed))
	}
	var trades []domain.BacktestTrade
	var open []*openTrade
	var pending *pendingEntry
	pullback := params.EntryPullbackATR.InexactFloat64()
	validBars := params.EntryValidBars
	if validBars <= 0 {
		validBars = DefaultEntryValidBars
	}
	lastSignalIndex := -stepBars
	completed := 0
	consecutiveFailures := 0
	maxOpen := params.MaxOpenPositions
	if maxOpen <= 0 {
		maxOpen = 1
	}

	for i := features.MinCandles; i < len(candles); i++ {
		if ctx.Err() != nil {
			return nil, domain.BacktestMetrics{}, nil, ctx.Err()
		}
		current := candles[i]
		if current.OpenTime.Before(params.DateFrom) {
			continue
		}

		// Existing positions receive funding and exits before a new signal is
		// considered. Multiple positions are independent and share free margin.
		if len(open) > 0 {
			// Record both intrabar extremes before mutating the positions. This is
			// conservative when OHLC does not reveal the high/low ordering and
			// prevents close-only equity from understating drawdown.
			state.markToMarket(current.Low, open, params.TakerFeePct)
			state.markToMarket(current.High, open, params.TakerFeePct)
		}
		survivors := open[:0]
		for _, position := range open {
			closed, trade := e.updateOpenTrade(position, current, params, run, state, funding)
			if closed {
				trades = append(trades, trade)
			} else {
				survivors = append(survivors, position)
			}
		}
		open = survivors

		// Equity is re-marked at the close after the exits of this bar, so a new
		// position is sized from the capital that actually remains.
		state.markToMarket(current.Close, open, params.TakerFeePct)
		state.recordEquityPoint(current.CloseTime)

		// A resting entry is checked before anything else this bar decides: the
		// order was placed on an earlier bar, so this one can only fill it.
		if pending != nil {
			switch {
			case i > pending.expiresAt:
				state.unfilledEntries++
				state.recordReason(reasonEntryUnfilled)
				pending = nil
			case len(open) >= maxOpen:
				pending = nil
			case pending.fills(current):
				if position := e.openTradeAt(pending.shifted(), pending.limit, domain.FeeMaker,
					current, params, state, open); position != nil {
					open = append(open, position)
					state.markToMarket(current.Close, open, params.TakerFeePct)
				}
				pending = nil
			}
		}

		if i-lastSignalIndex < stepBars {
			continue
		}
		lastSignalIndex = i

		// Every analysis point counts as progress, including the ones that need
		// no decision, so a long LLM run advances at a predictable pace instead
		// of stalling whenever a position is open.
		completed++
		state.analysisPoints++
		state.recordBar(current.CloseTime)
		if progress != nil {
			progress(completed)
		}

		// A full position book cannot act on a signal, so no analysis (and in
		// LLM mode no inference) is spent on it.
		if len(open) >= maxOpen {
			continue
		}

		// Only the closed candles up to and including the current one are
		// visible. This slice is the entire look-ahead defence.
		if dice != nil {
			// The control group needs the same exit plan the real trades get, so
			// the signal is built from the same analysis - only the decision to
			// enter is replaced by chance.
			if dice.Float64() < in.RandomEntryChance {
				if signal := e.randomEntrySignal(run, series, current); signal != nil {
					if position := e.openTradeFrom(signal, current, params, state, open); position != nil {
						open = append(open, position)
						state.markToMarket(current.Close, open, params.TakerFeePct)
					}
				}
			}
			state.recordReason("random_entry")
			continue
		}

		visible := visibleSeriesAt(series, current.CloseTime, e.analysisConfig().CandleHistoryLimit)
		market := marketContextAt(benchmark, current.CloseTime, e.analysisConfig().BenchmarkSymbol)
		universe := domain.UniverseContext{}
		if in.Universe != nil {
			universe = in.Universe.RankAt(run.Symbol, current.CloseTime)
		}
		decision, err := e.signal(ctx, run, tf, visible, signalContext{
			market: market, universe: universe, news: in.News, history: in.History, open: open,
		})
		state.inferences += decision.inferenceCost
		state.cacheHits += decision.cacheHit
		state.recordQuality(decision.quality)
		state.recordReason(decision.reason)

		// Only a request that actually reached the model is worth waiting after:
		// a cached answer never touched the GPU, and pausing on it would make a
		// re-run of an identical period as slow as the first one.
		if decision.inferenceCost > 0 && params.InferencePause > 0 {
			timer := time.NewTimer(params.InferencePause)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, domain.BacktestMetrics{}, nil, ctx.Err()
			case <-timer.C:
			}
		}
		if err != nil {
			e.log.Debug("signal generation failed", slog.String("error", err.Error()))
			// A model that has stopped answering will not start again on the next
			// bar, and every further step pays another timeout for the same
			// nothing. Failing the run says what happened while the inferences
			// already spent are still worth something.
			consecutiveFailures++
			if run.Mode == domain.BacktestLLM && consecutiveFailures >= MaxConsecutiveLLMFailures {
				return nil, domain.BacktestMetrics{}, nil, fmt.Errorf(
					"the model failed %d times in a row, last error: %w", consecutiveFailures, err)
			}
			continue
		}
		consecutiveFailures = 0
		if decision.signal != nil {
			if pullback > 0 && decision.signal.atr > 0 {
				// The order rests where the strategy would rather have bought,
				// and only a later bar can reach it.
				sign := float64(decision.signal.direction.Sign())
				limit := decision.signal.reference - sign*pullback*decision.signal.atr
				if limit > 0 {
					pending = &pendingEntry{signal: decision.signal, limit: limit, expiresAt: i + validBars}
				}
				continue
			}
			if position := e.openTradeFrom(decision.signal, current, params, state, open); position != nil {
				open = append(open, position)
				state.markToMarket(current.Close, open, params.TakerFeePct)
			}
		}
	}

	// Positions still open at the end are closed at the last available price.
	if len(open) > 0 {
		last := candles[len(candles)-1]
		for _, position := range open {
			trades = append(trades, e.forceClose(position, last, params, run, state, funding, "end_of_period"))
		}
		state.markToMarket(last.Close, nil, params.TakerFeePct)
		state.recordEquityPoint(last.CloseTime)
	}
	if progress != nil {
		progress(completed)
	}

	metrics := state.metrics(trades)
	return trades, metrics, state.equityCurve(EquityCurveMaxPoints), nil
}

func visibleSeriesAt(series map[domain.Timeframe][]domain.Candle, at time.Time, limit int) map[domain.Timeframe][]domain.Candle {
	if limit <= 0 {
		limit = 500
	}
	visible := make(map[domain.Timeframe][]domain.Candle, len(series))
	for tf, candles := range series {
		end := sort.Search(len(candles), func(i int) bool { return candles[i].CloseTime.After(at) })
		start := end - limit
		if start < 0 {
			start = 0
		}
		visible[tf] = candles[start:end]
	}
	return visible
}

// signalResult is one decision taken at a simulated point in time.
type signalResult struct {
	direction domain.Direction
	// reference is the price the exit levels were computed from. A pullback
	// entry fills lower, and the levels move with it.
	reference     float64
	confidence    int
	leverage      int
	allocationPct decimal.Decimal
	takeProfit    []domain.PriceTarget
	stopLoss      []domain.PriceTarget
	votes         []domain.StrategyVote
	atr           float64
}

type signalDecision struct {
	signal *signalResult
	// reason is why the deterministic policy did or did not ask for a trade. It
	// is what turns a run with no trades from a mystery into a report.
	reason        domain.StrategyDecisionReason
	quality       domain.DataQuality
	inferenceCost int
	cacheHit      int
}

// signalContext is the surrounding information one decision point sees besides
// the candles of its own symbol.
type signalContext struct {
	market   domain.MarketContext
	universe domain.UniverseContext
	news     NewsContextProvider
	history  HistoryProvider
	open     []*openTrade
}

// signal produces a decision either from the deterministic scores or from the LLM.
func (e *Engine) signal(ctx context.Context, run domain.BacktestRun, tf domain.Timeframe, visible map[domain.Timeframe][]domain.Candle, sctx signalContext) (signalDecision, error) {
	primary := visible[tf]
	if len(primary) < features.MinCandles {
		return signalDecision{}, nil
	}
	last := primary[len(primary)-1]
	analyses := make(map[domain.Timeframe]domain.TimeframeAnalysis, len(visible))
	quality := domain.DataQuality{Status: domain.DataQualityOK, MissingFields: []string{}, Notes: []string{}}
	for timeframe, candles := range visible {
		if len(candles) < features.MinCandles {
			quality.AddMissing(fmt.Sprintf("timeframe_%s", timeframe))
			continue
		}
		analyses[timeframe] = features.AnalyzeTimeframe(timeframe, candles)
		if missingCandleVolume(candles) {
			quality.AddMissing(fmt.Sprintf("volume_%s", timeframe))
		}
		if hasCandleGaps(candles, timeframe) {
			quality.AddMissing(fmt.Sprintf("timeframe_%s_gaps", timeframe))
		}
	}
	analysis, ok := analyses[tf]
	if !ok {
		return signalDecision{}, nil
	}

	snapshot := features.BuildSnapshot(features.SnapshotInput{
		Symbol:          run.Symbol,
		Price:           last.Close,
		Timeframes:      analyses,
		ActivePositions: positionContexts(sctx.open, last.CloseTime, last.Close),
		RecentCandles:   tail(primary, 30),
		MarketContext:   sctx.market,
		UniverseContext: sctx.universe,
		DataQuality:     quality,
		Now:             last.CloseTime,
	})
	// The news context is attached the same way the live cycle attaches it, and
	// bounded by the bar being replayed rather than by now.
	if sctx.news != nil {
		snapshot.NewsContext = sctx.news.NewsContextAt(ctx, last.CloseTime)
	}
	// The track record and the comparable past cases are what the live prompt
	// carries and the rules ignore, so they are only assembled for a run that
	// will actually ask the model.
	if sctx.history != nil && run.Mode == domain.BacktestLLM {
		e.attachHistory(ctx, sctx.history, &snapshot, run.Symbol, last.CloseTime)
	}

	if run.Mode == domain.BacktestTechnical {
		signal, reason := e.technicalSignal(run, analysis, snapshot, primary, last)
		return signalDecision{signal: signal, reason: reason, quality: quality}, nil
	}
	decision, err := e.llmSignal(ctx, run, analysis, snapshot, last)
	decision.quality = quality
	return decision, err
}

// attachHistory adds the same two retrieval blocks the live prompt carries. A
// failure of either is logged and left empty: the model is told what it has, and
// a replay that cannot read its own past still produces a decision.
func (e *Engine) attachHistory(ctx context.Context, provider HistoryProvider, snapshot *domain.FeatureSnapshot, symbol string, at time.Time) {
	performance, err := provider.PerformanceAt(ctx, symbol, at)
	if err != nil {
		e.log.Debug("replay performance context unavailable", slog.String("error", err.Error()))
	} else {
		snapshot.HistoricalPerformance = performance
	}

	similar, err := provider.SimilarCases(ctx, features.FeatureVector(*snapshot), symbol, SimilarCaseLimit, at)
	if err != nil {
		e.log.Debug("replay similar-case lookup failed", slog.String("error", err.Error()))
		return
	}
	snapshot.SimilarCases = similar
}

// SimilarCaseLimit is how many comparable past situations one prompt carries.
// It matches the live analysis, because a replay that shows the model a
// different amount of context is not replaying the live decision.
const SimilarCaseLimit = 5

// technicalSignal is the pure rule-based baseline: no model involved.
func (e *Engine) technicalSignal(run domain.BacktestRun, analysis domain.TimeframeAnalysis, snapshot domain.FeatureSnapshot, candles []domain.Candle, last domain.Candle) (*signalResult, domain.StrategyDecisionReason) {
	decision := strategies.Evaluate(strategies.Input{
		Timeframe:               run.Timeframe,
		Analysis:                analysis,
		Snapshot:                snapshot,
		Candles:                 candles,
		Price:                   last.Close,
		Now:                     last.CloseTime,
		CriticalNewsMaxAge:      e.risk.Config().CriticalNewsMaxAge,
		RoundTripCostPct:        roundTripCost(run.Params),
		CostFloorMultiple:       run.Params.CostFloorMultiple.InexactFloat64(),
		MinRelativeStrengthPct:  run.Params.MinRelativeStrengthPct.InexactFloat64(),
		MarketGateLongBufferPct: run.Params.MarketGateLongBufferPct.InexactFloat64(),
		MarketGateAllowFallingAverage: run.Params.MarketGateAllowFallingAverage != nil &&
			*run.Params.MarketGateAllowFallingAverage,
	}, e.strategySet(run))

	if !decision.IsEntry() {
		return nil, decision.Reason
	}
	if decision.Confidence < run.Params.MinConfidence {
		return nil, reasonBelowMinConfidence
	}
	direction := decision.Direction
	confidence := decision.Confidence

	atr := 0.0
	if analysis.Indicators.ATR != nil {
		atr = *analysis.Indicators.ATR
	}
	if atr <= 0 {
		return nil, reasonNoATR
	}

	plan := atrPlan(run.Params)
	entry := last.Close
	sign := float64(direction.Sign())
	stop := entry - sign*plan.stop*atr
	target1 := entry + sign*plan.target1*atr
	target2 := entry + sign*plan.target2*atr

	takeProfit := []domain.PriceTarget{
		{Price: target1, ClosePct: plan.target1Close},
		{Price: target2, ClosePct: 100 - plan.target1Close},
	}
	if plan.target1Close >= 100 {
		takeProfit = takeProfit[:1]
	}
	signal := &signalResult{
		direction:     direction,
		reference:     entry,
		confidence:    confidence,
		allocationPct: run.Params.AllocationPct,
		takeProfit:    takeProfit,
		stopLoss:      []domain.PriceTarget{{Price: stop, ClosePct: 100}},
		votes:         decision.Votes,
		atr:           atr,
	}
	signal.leverage, signal.allocationPct = e.riskPlan(riskRequest{
		direction:  direction,
		confidence: confidence,
		price:      entry,
		stops:      signal.stopLoss,
		atr:        atr,
		allocation: signal.allocationPct,
	}, snapshot, run.Params)
	return signal, decision.Reason
}

// Reasons the replay records beyond the policy's own verdicts.
const (
	reasonBelowMinConfidence domain.StrategyDecisionReason = "below_min_confidence"
	reasonNoATR              domain.StrategyDecisionReason = "no_atr"
	reasonEntryUnfilled      domain.StrategyDecisionReason = "entry_never_filled"
	// The model answered, and the answer was not a trade.
	reasonLLMNoEntry domain.StrategyDecisionReason = "llm_no_entry"
	// The model answered, but validation refused the answer.
	reasonLLMRejected domain.StrategyDecisionReason = "llm_rejected"
	// The model could not be reached at all.
	reasonLLMFailed domain.StrategyDecisionReason = "llm_failed"
)

// requiresATR reports whether this run places any of its prices in ATR units -
// a trailing stop, or an entry resting a fraction of a range below the signal.
// Such a run cannot honour the signal without an ATR, and finding that out
// before any money moves is what keeps a refused entry free of charge.
func requiresATR(params domain.BacktestParams) bool {
	return params.ExitMode == domain.ExitModeTrailingATR ||
		params.EntryPullbackATR.GreaterThan(decimal.Zero)
}

// DefaultEntryValidBars is how many bars a pullback entry waits before the
// signal is considered stale. A signal that has not been reached in a few bars
// is describing a market that has moved on.
const DefaultEntryValidBars = 3

// DefaultEntryPullbackATR is how far below the signal a long rests its entry,
// in average bar ranges.
//
// Half a range is where the measurements land and the reason is mechanical
// rather than statistical. Part of the gain is arithmetic and certain: a resting
// order pays the maker fee instead of the taker fee and crosses no spread, which
// on the shipped fee profile is 0.055% of notional saved on every trade. The
// rest is the better entry itself. Too far and the order only fills when the
// move has already failed - at one full range the daily profit factor falls back
// from 1.29 to 1.26 and the four-hour one from 1.19 to 1.04.
//
// Replayed over four separate years of daily bars this made every one of them
// profitable at the account level for the first time, and lifted the worst
// window from a profit factor of 0.73 to 1.04.
const DefaultEntryPullbackATR = 0.5

// pendingEntry is a signal waiting for the market to come back to it.
type pendingEntry struct {
	signal    *signalResult
	limit     float64
	expiresAt int
}

// fills reports whether the bar reached the resting order. A long waits below
// the signal price, a short above it.
func (p pendingEntry) fills(candle domain.Candle) bool {
	if p.signal.direction == domain.DirectionLong {
		return candle.Low <= p.limit
	}
	return candle.High >= p.limit
}

// shifted returns the signal with every exit level moved by the difference
// between the fill and the price the levels were derived from.
//
// The ATR plan is entirely relative - a stop one and a half ranges away, targets
// at three and six - so translating it keeps exactly the geometry the strategy
// asked for, now measured from where the position actually opened.
func (p pendingEntry) shifted() *signalResult {
	shift := p.limit - p.signal.reference
	if shift == 0 {
		return p.signal
	}
	out := *p.signal
	out.reference = p.limit
	out.takeProfit = shiftTargets(p.signal.takeProfit, shift)
	out.stopLoss = shiftTargets(p.signal.stopLoss, shift)
	return &out
}

func shiftTargets(levels []domain.PriceTarget, shift float64) []domain.PriceTarget {
	if len(levels) == 0 {
		return nil
	}
	out := make([]domain.PriceTarget, 0, len(levels))
	for _, level := range levels {
		level.Price += shift
		if level.Price > 0 {
			out = append(out, level)
		}
	}
	return out
}

// DefaultTrailingATRMult is how far behind the extreme a Chandelier stop sits
// when the caller does not say. Anything from 2 to 5 performed the same on the
// tested history, so the middle of that plateau is what ships.
const DefaultTrailingATRMult = 2.5

// randomEntrySignal builds the same trade plan a real signal would carry, with a
// fixed confidence so the risk engine treats every control trade alike.
func (e *Engine) randomEntrySignal(run domain.BacktestRun, series map[domain.Timeframe][]domain.Candle, current domain.Candle) *signalResult {
	visible := visibleSeriesAt(series, current.CloseTime, e.analysisConfig().CandleHistoryLimit)
	candles := visible[run.Timeframe]
	if len(candles) < features.MinCandles {
		return nil
	}
	analysis := features.AnalyzeTimeframe(run.Timeframe, candles)
	snapshot := features.BuildSnapshot(features.SnapshotInput{
		Symbol: run.Symbol, Price: current.Close, Now: current.CloseTime,
		Timeframes: map[domain.Timeframe]domain.TimeframeAnalysis{run.Timeframe: analysis},
	})

	atr := 0.0
	if analysis.Indicators.ATR != nil {
		atr = *analysis.Indicators.ATR
	}
	if atr <= 0 {
		return nil
	}
	plan := atrPlan(run.Params)
	entry := current.Close
	signal := &signalResult{
		direction: domain.DirectionLong, reference: entry, confidence: 70, atr: atr,
		allocationPct: run.Params.AllocationPct,
		takeProfit: []domain.PriceTarget{
			{Price: entry + plan.target1*atr, ClosePct: plan.target1Close},
			{Price: entry + plan.target2*atr, ClosePct: 100 - plan.target1Close},
		},
		stopLoss: []domain.PriceTarget{{Price: entry - plan.stop*atr, ClosePct: 100}},
	}
	signal.leverage, signal.allocationPct = e.riskPlan(riskRequest{
		direction: domain.DirectionLong, confidence: 70, price: entry,
		stops: signal.stopLoss, atr: atr, allocation: signal.allocationPct,
	}, snapshot, run.Params)
	return signal
}

// exitPlan is the ATR geometry of a deterministic trade.
type exitPlan struct {
	stop, target1, target2 float64
	target1Close           float64
}

// DefaultATRPlan is the shipped payoff profile of a fixed-target trade: a stop
// one and a half average bar ranges away, a first target at twice that distance
// and a runner at four times it.
//
// The previous profile stopped out at 1.1 ATR and aimed at 2.0. Replayed over
// four separate years of daily bars and three windows of four-hour bars it was
// the weakest geometry of every pair tested: the tight stop removed the trades
// that later paid, and the near target capped the ones that survived. Widening
// both raised the pooled profit factor on daily bars from 1.07 to 1.08 and on
// four-hour bars from 1.03 to 1.05 - a small gain, and the reason the trailing
// exit rather than this one is what a new run defaults to.
var DefaultATRPlan = struct{ Stop, Target1, Target2, Target1ClosePct float64 }{
	Stop: 1.5, Target1: 3.0, Target2: 6.0, Target1ClosePct: 50,
}

// atrPlan reads the run's geometry, falling back to the shipped profile for
// every value the caller left unset.
func atrPlan(params domain.BacktestParams) exitPlan {
	plan := exitPlan{
		stop:         DefaultATRPlan.Stop,
		target1:      DefaultATRPlan.Target1,
		target2:      DefaultATRPlan.Target2,
		target1Close: DefaultATRPlan.Target1ClosePct,
	}
	if v := params.ATRStopMult.InexactFloat64(); v > 0 {
		plan.stop = v
	}
	if v := params.ATRTarget1Mult.InexactFloat64(); v > 0 {
		plan.target1 = v
	}
	if v := params.ATRTarget2Mult.InexactFloat64(); v > 0 {
		plan.target2 = v
	}
	if v := params.ATRTarget1ClosePct.InexactFloat64(); v > 0 {
		plan.target1Close = math.Min(v, 100)
	}
	// A runner that sits below the first target is not a ladder; collapsing it
	// into a single exit is what the caller meant.
	if plan.target2 <= plan.target1 {
		plan.target1Close = 100
	}
	return plan
}

// llmSignal asks the model, reusing cached answers for identical situations.
func (e *Engine) llmSignal(ctx context.Context, run domain.BacktestRun, analysis domain.TimeframeAnalysis, snapshot domain.FeatureSnapshot, last domain.Candle) (signalDecision, error) {
	if !e.llm.Enabled() {
		return signalDecision{}, fmt.Errorf("llm backtest requested but the LLM is disabled")
	}

	// The live risk policy, including edits made in the settings screen, bounds
	// the model exactly as it would during a scheduled analysis.
	riskCfg := e.risk.Config()
	decision := signalDecision{inferenceCost: 1}
	runID := run.ID
	result, err := e.llm.Analyze(ctx, llm.Request{
		Snapshot:      snapshot,
		BacktestRunID: &runID,
		UseCache:      run.Params.UseCache,
		MaxAllocation: riskCfg.MaxRecommendedAllocPct.InexactFloat64(),
		Validation: llm.ValidationContext{
			Symbol:           run.Symbol,
			ReferencePrice:   last.Close,
			MinLeverage:      riskCfg.MinLeverage,
			MaxLeverage:      riskCfg.MaxLeverage,
			MaxAllocationPct: riskCfg.MaxRecommendedAllocPct.InexactFloat64(),
		},
	})
	if err != nil {
		decision.reason = reasonLLMFailed
		return decision, err
	}
	if result.Record.Status == domain.InferenceCached {
		decision.cacheHit = 1
		decision.inferenceCost = 0
	}
	v := result.Validated
	switch {
	case v == nil:
		decision.reason = reasonLLMRejected
		return decision, nil
	case !v.Action.IsEntry():
		decision.reason = reasonLLMNoEntry
		return decision, nil
	case v.Confidence < run.Params.MinConfidence:
		decision.reason = reasonBelowMinConfidence
		return decision, nil
	}
	direction, _ := v.Action.Direction()

	// The exit machinery is measured in ranges, not prices: a trailing stop, a
	// pullback entry and the level shift that follows a fill all need the ATR
	// and the price the model's levels were quoted from. Without them an LLM
	// trade is silently unopenable in every ATR-based exit mode.
	atr := 0.0
	if analysis.Indicators.ATR != nil {
		atr = *analysis.Indicators.ATR
	}
	if atr <= 0 && requiresATR(run.Params) {
		decision.reason = reasonNoATR
		return decision, nil
	}

	signal := &signalResult{
		direction:     direction,
		reference:     last.Close,
		confidence:    v.Confidence,
		allocationPct: v.AllocationPct,
		takeProfit:    v.TakeProfit,
		stopLoss:      v.StopLoss,
		atr:           atr,
	}
	if signal.allocationPct.IsZero() {
		signal.allocationPct = run.Params.AllocationPct
	}
	// The model's own leverage and risk level go into the risk engine exactly as
	// they do live, so a backtest reproduces the sizing the user would have seen.
	signal.leverage, signal.allocationPct = e.riskPlan(riskRequest{
		direction:  direction,
		confidence: v.Confidence,
		leverage:   v.Leverage,
		riskLevel:  v.RiskLevel,
		price:      last.Close,
		stops:      v.StopLoss,
		atr:        atr,
		allocation: signal.allocationPct,
	}, snapshot, run.Params)
	decision.signal = signal
	decision.reason = domain.StrategyReasonEntry
	return decision, nil
}

// riskRequest is what a signal asks for before the risk engine trims it.
type riskRequest struct {
	direction  domain.Direction
	confidence int
	leverage   int // 0 falls back to the leverage configured for the run
	riskLevel  domain.RiskLevel
	price      float64
	stops      []domain.PriceTarget
	atr        float64
	allocation decimal.Decimal
}

// riskPlan applies both leverage and allocation from the same deterministic
// risk assessment used by the live recommendation path.
func (e *Engine) riskPlan(req riskRequest, snapshot domain.FeatureSnapshot, params domain.BacktestParams) (int, decimal.Decimal) {
	action := domain.RecommendationOpenLong
	if req.direction == domain.DirectionShort {
		action = domain.RecommendationOpenShort
	}
	requested := req.leverage
	if requested <= 0 {
		requested = int(params.Leverage.IntPart())
	}
	if requested <= 0 {
		requested = e.risk.Config().MinLeverage
	}

	assessment := e.risk.Evaluate(risk.Input{
		Action:         action,
		Confidence:     req.confidence,
		LLMLeverage:    requested,
		LLMAllocation:  req.allocation,
		LLMRiskLevel:   req.riskLevel,
		ReferencePrice: req.price,
		StopLoss:       effectiveStops(req, requested, params),
		Snapshot:       snapshot,
	})
	return assessment.Leverage.Recommended, assessment.AllocationPct
}

// effectiveStops is where the position will actually be stopped out under this
// run's exit mode, which is not always where the signal put its stop.
//
// Both things the risk engine derives from a stop - the leverage cap and the
// risk-sized allocation - are only meaningful against the level that will really
// be there. A trailing run replaces the signal's stop with a Chandelier level,
// and a P&L ladder replaces it with a fixed loss on margin; sizing either of
// them against a stop the engine is about to discard misstates the risk of the
// trade by however far the two levels differ.
func effectiveStops(req riskRequest, leverage int, params domain.BacktestParams) []domain.PriceTarget {
	switch params.ExitMode {
	case domain.ExitModeTrailingATR:
		if req.atr <= 0 || req.price <= 0 {
			return req.stops
		}
		mult := params.TrailingATRMult.InexactFloat64()
		if mult <= 0 {
			mult = DefaultTrailingATRMult
		}
		// The initial Chandelier level is the widest this stop will ever be: it
		// only ratchets towards price from here.
		price := req.price - float64(req.direction.Sign())*req.atr*mult
		if price <= 0 {
			return req.stops
		}
		return []domain.PriceTarget{{Price: price, ClosePct: 100}}
	case domain.ExitModePnLLadder:
		if ladder := ladderTargets(params.StopLossLadder, req.price, req.direction, leverage, false); len(ladder) > 0 {
			return ladder
		}
	}
	return req.stops
}

// openTrade is a position under simulation.
type openTrade struct {
	trade         domain.BacktestTrade
	takeProfit    []domain.PriceTarget
	stopLoss      []domain.PriceTarget
	entryPrice    float64
	originalQty   decimal.Decimal
	remainingQty  decimal.Decimal
	initialMargin decimal.Decimal
	grossPnL      decimal.Decimal
	fees          decimal.Decimal
	funding       decimal.Decimal
	exitNotional  decimal.Decimal
	exitedQty     decimal.Decimal
	executions    []domain.BacktestExecution
	nextFundingAt time.Time
	breakEven     bool
	trailATR      float64
	peak          float64
	mfe, mae      float64
}

func (e *Engine) openTradeFrom(signal *signalResult, candle domain.Candle, params domain.BacktestParams, state *simState, positions []*openTrade) *openTrade {
	slippage := params.SlippagePct.InexactFloat64() / 100
	sign := float64(signal.direction.Sign())
	entry := candle.Close * (1 + sign*slippage)
	return e.openTradeAt(signal, entry, domain.FeeTaker, candle, params, state, positions)
}

// openTradeAt opens a position at an explicit fill price.
//
// A market entry pays the taker fee and the slippage of crossing the spread; a
// resting limit order that the market came to pays the maker fee and no
// slippage, because it did not move to meet anything. Keeping the two apart is
// the whole point of a pullback entry: the better price is only half of what it
// saves.
func (e *Engine) openTradeAt(signal *signalResult, entry float64, feeType domain.FeeType, candle domain.Candle, params domain.BacktestParams, state *simState, positions []*openTrade) *openTrade {
	if entry <= 0 {
		return nil
	}
	sign := float64(signal.direction.Sign())

	capital := state.equity()
	allocation := signal.allocationPct
	if allocation.LessThanOrEqual(decimal.Zero) {
		return nil
	}
	margin := capital.Mul(allocation).Div(decimal.NewFromInt(100))
	notional := margin.Mul(decimal.NewFromInt(int64(signal.leverage)))
	quantity := notional.Div(decimal.NewFromFloat(entry))
	if quantity.LessThanOrEqual(decimal.Zero) {
		return nil
	}

	// The exit plan is settled before any money moves: a mode that cannot build
	// one refuses the entry, and a refused entry must not cost a fee.
	takeProfit, stopLoss := signal.takeProfit, signal.stopLoss
	trailATR := 0.0
	switch params.ExitMode {
	case domain.ExitModePnLLadder:
		takeProfit = ladderTargets(params.TakeProfitLadder, entry, signal.direction, signal.leverage, true)
		stopLoss = ladderTargets(params.StopLossLadder, entry, signal.direction, signal.leverage, false)
	case domain.ExitModeTrailingATR:
		// A Chandelier exit has no target: the position rides until the trailing
		// stop is crossed, which is the whole point of the mode.
		mult := params.TrailingATRMult.InexactFloat64()
		if mult <= 0 {
			mult = DefaultTrailingATRMult
		}
		if signal.atr <= 0 {
			return nil
		}
		trailATR = signal.atr * mult
		takeProfit = nil
		stopLoss = []domain.PriceTarget{{Price: entry - sign*trailATR, ClosePct: 100}}
	}

	fee := feeFor(notional, params, feeType)
	if margin.Add(fee).GreaterThan(state.availableMargin(positions)) {
		return nil
	}
	state.cash = state.cash.Sub(fee)
	execution := domain.BacktestExecution{
		Kind: "entry", ExecutedAt: candle.CloseTime, Price: decimal.NewFromFloat(entry),
		Quantity: quantity, Fee: fee, FeeType: feeType,
	}

	return &openTrade{
		trade: domain.BacktestTrade{
			ID:            uuid.New(),
			RunID:         uuid.Nil, // assigned when persisted
			Symbol:        "",
			Direction:     signal.direction,
			OpenedAt:      candle.CloseTime,
			EntryPrice:    decimal.NewFromFloat(entry),
			Quantity:      quantity,
			Leverage:      decimal.NewFromInt(int64(signal.leverage)),
			AllocationPct: allocation,
			Fees:          fee,
			Confidence:    &signal.confidence,
			StrategyVotes: signal.votes,
		},
		takeProfit:    takeProfit,
		stopLoss:      stopLoss,
		entryPrice:    entry,
		originalQty:   quantity,
		remainingQty:  quantity,
		initialMargin: margin,
		fees:          fee,
		executions:    []domain.BacktestExecution{execution},
		nextFundingAt: nextFundingAfter(candle.CloseTime),
		trailATR:      trailATR,
		peak:          entry,
	}
}

// roundTripCost is what opening and closing one position costs in percent of
// notional under this run's parameters.
func roundTripCost(params domain.BacktestParams) float64 {
	taker := params.TakerFeePct.InexactFloat64()
	maker := params.MakerFeePct.InexactFloat64()
	slippage := params.SlippagePct.InexactFloat64()
	// Entry is a market order; the exit is a limit target at best and a market
	// stop at worst, so the cheaper of the two fees is used for it.
	exit := taker
	if maker > 0 && maker < exit {
		exit = maker
	}
	return taker + exit + 2*slippage
}

// ladderTargets turns "close X% of the position at Y% return on margin" into the
// price levels the execution engine already understands, using the leverage the
// position actually opened with. The step therefore means the same thing after
// the risk engine has adjusted leverage, and everything downstream - partial
// fills, fees, gaps, ordering inside a candle - keeps working unchanged.
func ladderTargets(steps []domain.PnLExitStep, entry float64, direction domain.Direction, leverage int, profit bool) []domain.PriceTarget {
	if len(steps) == 0 || leverage <= 0 || entry <= 0 {
		return nil
	}
	sign := float64(direction.Sign())
	if !profit {
		sign = -sign
	}

	out := make([]domain.PriceTarget, 0, len(steps))
	for _, step := range steps {
		move := math.Abs(step.PnLPct) / float64(leverage) / 100
		// A losing step cannot be further away than the entire notional; the
		// position is bankrupt well before that and liquidation handles it.
		if move <= 0 || (!profit && move >= 1) || step.ClosePct <= 0 {
			continue
		}
		price := entry * (1 + sign*move)
		if price <= 0 {
			continue
		}
		out = append(out, domain.PriceTarget{Price: price, ClosePct: step.ClosePct})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// updateOpenTrade applies one candle to an open trade.
func (e *Engine) updateOpenTrade(open *openTrade, candle domain.Candle, params domain.BacktestParams, run domain.BacktestRun, state *simState, funding map[int64]float64) (bool, domain.BacktestTrade) {
	sign := float64(open.trade.Direction.Sign())
	up := (candle.High - open.entryPrice) / open.entryPrice * 100 * sign
	down := (candle.Low - open.entryPrice) / open.entryPrice * 100 * sign
	open.mfe = math.Max(open.mfe, math.Max(up, down))
	open.mae = math.Min(open.mae, math.Min(up, down))

	e.applyFunding(open, candle, params, state, funding)

	liquidation, hasLiquidation := liquidationPrice(open, params)
	liquidated := hasLiquidation && priceTouched(candle, liquidation, open.trade.Direction, false)

	// Price has to travel through the nearer level first. A stop between the
	// entry and the liquidation price therefore executes before the position
	// can ever reach liquidation, no matter how far the candle extends.
	if liquidated && !stopPrecedes(open, candle, liquidation) {
		e.executeExit(open, liquidationFill(open, candle, liquidation), 100,
			candle.CloseTime, params, domain.FeeTaker, "liquidation", state)
		return true, e.finalizeTrade(open, run, "liquidation", candle.CloseTime)
	}

	tpTouched := anyTargetTouched(candle, open.takeProfit, open.trade.Direction, true)
	slTouched := anyTargetTouched(candle, open.stopLoss, open.trade.Direction, false)
	reason := ""
	switch {
	case tpTouched && slTouched:
		open.stopLoss = e.executeTouchedTargets(open, candle, open.stopLoss, false, params, state, "stop_loss_ambiguous_candle")
		reason = "stop_loss_ambiguous_candle"
	case slTouched:
		open.stopLoss = e.executeTouchedTargets(open, candle, open.stopLoss, false, params, state, "stop_loss")
		reason = "stop_loss"
	case tpTouched:
		open.takeProfit = e.executeTouchedTargets(open, candle, open.takeProfit, true, params, state, "take_profit")
		reason = "take_profit"
		// Once part of the position is banked, the rest is protected at the entry
		// price. Without this a trade that reached its first target and then
		// reversed still gave back a full stop, which is where a large share of
		// the losing trades in the stored runs came from.
		if params.BreakEvenAfterTP && open.remainingQty.IsPositive() && !open.breakEven {
			open.breakEven = true
			open.stopLoss = []domain.PriceTarget{{Price: open.entryPrice, ClosePct: 100}}
		}
	}
	if open.remainingQty.IsZero() {
		return true, e.finalizeTrade(open, run, reason, candle.CloseTime)
	}
	// The trailing stop only takes this bar's extreme into account once the bar
	// is over. Moving it first would let the same candle both set the level and
	// trigger it, which is look-ahead inside the bar.
	e.trailStop(open, candle)

	// What the partial stops left open is still exposed to the same candle.
	if liquidated {
		e.executeExit(open, liquidationFill(open, candle, liquidation), 100,
			candle.CloseTime, params, domain.FeeTaker, "liquidation", state)
		return true, e.finalizeTrade(open, run, "liquidation", candle.CloseTime)
	}
	return false, domain.BacktestTrade{}
}

// trailStop moves a Chandelier stop up behind the extreme reached since entry.
// The stop never retreats: that is what separates a trailing stop from a stop
// that simply follows price around. It is applied after the exits of the bar,
// so a level can only trigger on a later candle than the one that set it.
func (e *Engine) trailStop(open *openTrade, candle domain.Candle) {
	if open.trailATR <= 0 {
		return
	}
	if open.trade.Direction == domain.DirectionLong {
		open.peak = math.Max(open.peak, candle.High)
		level := open.peak - open.trailATR
		if len(open.stopLoss) == 0 || level > open.stopLoss[0].Price {
			open.stopLoss = []domain.PriceTarget{{Price: level, ClosePct: 100}}
		}
		return
	}
	if open.peak == 0 {
		open.peak = candle.Low
	}
	open.peak = math.Min(open.peak, candle.Low)
	level := open.peak + open.trailATR
	if len(open.stopLoss) == 0 || level < open.stopLoss[0].Price {
		open.stopLoss = []domain.PriceTarget{{Price: level, ClosePct: 100}}
	}
}

// stopPrecedes reports whether a stop level that this candle also reached sits
// between the entry and the given adverse price.
func stopPrecedes(open *openTrade, candle domain.Candle, price float64) bool {
	for _, level := range open.stopLoss {
		if !priceTouched(candle, level.Price, open.trade.Direction, false) {
			continue
		}
		if open.trade.Direction == domain.DirectionLong && level.Price > price {
			return true
		}
		if open.trade.Direction == domain.DirectionShort && level.Price < price {
			return true
		}
	}
	return false
}

func (e *Engine) executeTouchedTargets(open *openTrade, candle domain.Candle, levels []domain.PriceTarget, takeProfit bool, params domain.BacktestParams, state *simState, reason string) []domain.PriceTarget {
	ordered := append([]domain.PriceTarget(nil), levels...)
	sort.SliceStable(ordered, func(i, j int) bool {
		ascending := (open.trade.Direction == domain.DirectionLong && takeProfit) || (open.trade.Direction == domain.DirectionShort && !takeProfit)
		if ascending {
			return ordered[i].Price < ordered[j].Price
		}
		return ordered[i].Price > ordered[j].Price
	})
	remaining := make([]domain.PriceTarget, 0, len(ordered))
	feeType := domain.FeeTaker
	if takeProfit {
		feeType = domain.FeeMaker
	}
	for _, level := range ordered {
		if !priceTouched(candle, level.Price, open.trade.Direction, takeProfit) {
			remaining = append(remaining, level)
			continue
		}
		closePct := level.ClosePct
		if closePct <= 0 {
			closePct = 100
		}
		// A take profit is a resting limit order and fills at its level; a stop
		// becomes a market order and cannot beat a candle that gapped past it.
		fill := level.Price
		if !takeProfit {
			fill = stopFill(level.Price, candle, open.trade.Direction)
		}
		e.executeExit(open, fill, closePct, candle.CloseTime, params, feeType, reason, state)
		if open.remainingQty.IsZero() {
			break
		}
	}
	return remaining
}

func (e *Engine) executeExit(open *openTrade, exitPrice float64, closePct float64, at time.Time, params domain.BacktestParams, feeType domain.FeeType, reason string, state *simState) {
	qty := open.originalQty.Mul(decimal.NewFromFloat(closePct)).Div(decimal.NewFromInt(100))
	if closePct >= 100 || qty.GreaterThan(open.remainingQty) {
		qty = open.remainingQty
	}
	if qty.LessThanOrEqual(decimal.Zero) {
		return
	}
	slippage := params.SlippagePct.InexactFloat64() / 100
	sign := float64(open.trade.Direction.Sign())
	fill := exitPrice
	if feeType != domain.FeeMaker {
		fill *= 1 - sign*slippage
	}

	exit := decimal.NewFromFloat(fill)
	gross := exit.Sub(open.trade.EntryPrice).Mul(qty).Mul(decimal.NewFromInt(int64(open.trade.Direction.Sign())))
	exitFee := feeFor(exit.Mul(qty), params, feeType)
	state.cash = state.cash.Add(gross).Sub(exitFee)
	open.grossPnL = open.grossPnL.Add(gross)
	open.fees = open.fees.Add(exitFee)
	open.exitNotional = open.exitNotional.Add(exit.Mul(qty))
	open.exitedQty = open.exitedQty.Add(qty)
	open.remainingQty = open.remainingQty.Sub(qty)
	if open.remainingQty.IsNegative() {
		open.remainingQty = decimal.Zero
	}
	actualPct, _ := qty.Div(open.originalQty).Mul(decimal.NewFromInt(100)).Float64()
	open.executions = append(open.executions, domain.BacktestExecution{
		Kind: reason, ExecutedAt: at, Price: exit, Quantity: qty, ClosePct: actualPct,
		GrossPnL: gross.Round(10), Fee: exitFee.Round(10), FeeType: feeType,
	})
}

func (e *Engine) finalizeTrade(open *openTrade, run domain.BacktestRun, reason string, at time.Time) domain.BacktestTrade {
	trade := open.trade
	trade.Symbol = run.Symbol
	trade.RunID = run.ID
	trade.ClosedAt = &at
	if open.exitedQty.IsPositive() {
		exit := open.exitNotional.Div(open.exitedQty)
		trade.ExitPrice = &exit
	}
	trade.GrossPnL = open.grossPnL.Round(10)
	trade.Fees = open.fees.Round(10)
	trade.Funding = open.funding.Round(10)
	trade.NetPnL = trade.GrossPnL.Sub(trade.Fees).Add(trade.Funding).Round(10)
	trade.ExitReason = reason
	trade.Executions = append([]domain.BacktestExecution(nil), open.executions...)

	mfe, mae := open.mfe, open.mae
	trade.MFEPct, trade.MAEPct = &mfe, &mae

	margin := trade.EntryPrice.Mul(open.originalQty).Div(trade.Leverage)
	if margin.IsPositive() {
		pct, _ := trade.NetPnL.Div(margin).Mul(decimal.NewFromInt(100)).Float64()
		trade.PnLPct = math.Round(pct*100) / 100
	}
	return trade
}

func (e *Engine) forceClose(open *openTrade, candle domain.Candle, params domain.BacktestParams, run domain.BacktestRun, state *simState, funding map[int64]float64, reason string) domain.BacktestTrade {
	e.applyFunding(open, candle, params, state, funding)
	e.executeExit(open, candle.Close, 100, candle.CloseTime, params, domain.FeeTaker, reason, state)
	return e.finalizeTrade(open, run, reason, candle.CloseTime)
}

func anyTargetTouched(candle domain.Candle, levels []domain.PriceTarget, direction domain.Direction, isTakeProfit bool) bool {
	for _, level := range levels {
		if priceTouched(candle, level.Price, direction, isTakeProfit) {
			return true
		}
	}
	return false
}

func priceTouched(candle domain.Candle, price float64, direction domain.Direction, takeProfit bool) bool {
	switch {
	case direction == domain.DirectionLong && takeProfit:
		return candle.High >= price
	case direction == domain.DirectionLong:
		return candle.Low <= price
	case takeProfit:
		return candle.Low <= price
	default:
		return candle.High >= price
	}
}

func feeFor(notional decimal.Decimal, params domain.BacktestParams, feeType domain.FeeType) decimal.Decimal {
	rate := params.TakerFeePct
	if feeType == domain.FeeMaker {
		rate = params.MakerFeePct
	}
	return notional.Mul(rate).Div(decimal.NewFromInt(100))
}

// stopFill is the first price a triggered stop can realistically get: its own
// level, or the candle open when the market gapped straight through it.
func stopFill(level float64, candle domain.Candle, direction domain.Direction) float64 {
	if candle.Open <= 0 {
		return level
	}
	if direction == domain.DirectionLong {
		return math.Min(level, candle.Open)
	}
	return math.Max(level, candle.Open)
}

// liquidationFill applies the same gap rule and then caps the fill at the
// bankruptcy price: past that point the loss is no longer the trader's, so the
// simulation must not charge more than the posted margin (slippage aside).
func liquidationFill(open *openTrade, candle domain.Candle, liquidation float64) float64 {
	fill := stopFill(liquidation, candle, open.trade.Direction)
	leverage := open.trade.Leverage.InexactFloat64()
	if leverage <= 0 {
		return fill
	}
	if open.trade.Direction == domain.DirectionLong {
		return math.Max(fill, open.entryPrice*(1-1/leverage))
	}
	return math.Min(fill, open.entryPrice*(1+1/leverage))
}

func liquidationPrice(open *openTrade, params domain.BacktestParams) (float64, bool) {
	leverage := open.trade.Leverage.InexactFloat64()
	if leverage <= 0 {
		return 0, false
	}
	distance := 1/leverage - params.MaintenanceMarginPct.InexactFloat64()/100
	if distance < 0 {
		distance = 0
	}
	if open.trade.Direction == domain.DirectionLong {
		return open.entryPrice * (1 - distance), true
	}
	return open.entryPrice * (1 + distance), true
}

// applyFunding charges every settlement the position lived through.
//
// The rate is the one the exchange actually published for that moment when the
// history is available, and the configured flat rate only where it is not. That
// distinction matters more than it looks: the trailing exit holds positions for
// days, so a long pays dozens of settlements, and assuming zero flatters every
// result the harness produces.
func (e *Engine) applyFunding(open *openTrade, candle domain.Candle, params domain.BacktestParams, state *simState, funding map[int64]float64) {
	for !open.nextFundingAt.After(candle.CloseTime) {
		rate := params.FundingRatePct
		if actual, ok := funding[open.nextFundingAt.Unix()]; ok {
			rate = decimal.NewFromFloat(actual)
		}
		if !rate.IsZero() && open.remainingQty.IsPositive() {
			mark := decimal.NewFromFloat(candle.Open)
			notional := mark.Mul(open.remainingQty)
			amount := notional.Mul(rate).Div(decimal.NewFromInt(100)).Mul(decimal.NewFromInt(int64(-open.trade.Direction.Sign())))
			open.funding = open.funding.Add(amount)
			state.cash = state.cash.Add(amount)
			open.executions = append(open.executions, domain.BacktestExecution{
				Kind: "funding", ExecutedAt: open.nextFundingAt, Price: mark,
				Quantity: open.remainingQty, Funding: amount.Round(10),
			})
		}
		open.nextFundingAt = open.nextFundingAt.Add(8 * time.Hour)
	}
}

// fundingSchedule indexes settlements by the second they happened, which is how
// the simulation looks them up as it walks the eight-hour funding clock.
func fundingSchedule(rates []domain.FundingRate) map[int64]float64 {
	if len(rates) == 0 {
		return nil
	}
	out := make(map[int64]float64, len(rates))
	for _, rate := range rates {
		out[rate.SettledAt.UTC().Unix()] = rate.Pct()
	}
	return out
}

// fundingSeries loads the settled funding of the traded perpetual. A missing
// history is not an error: the run falls back to the flat rate in its own
// parameters, which is what it did before the history existed.
func (e *Engine) fundingSeries(ctx context.Context, run domain.BacktestRun, asset domain.Asset) (map[int64]float64, error) {
	if e.repos == nil {
		return nil, nil
	}
	rates, err := e.repos.Funding.Range(ctx, asset.ID, run.Params.DateFrom, run.Params.DateTo)
	if err != nil {
		return nil, fmt.Errorf("load funding history: %w", err)
	}
	if len(rates) == 0 {
		e.log.Debug("no stored funding history; using the configured flat rate",
			slog.String("symbol", run.Symbol))
	}
	return fundingSchedule(rates), nil
}

func nextFundingAfter(at time.Time) time.Time {
	at = at.UTC()
	day := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC)
	for _, hour := range []time.Duration{0, 8 * time.Hour, 16 * time.Hour, 24 * time.Hour} {
		candidate := day.Add(hour)
		if candidate.After(at) {
			return candidate
		}
	}
	return day.Add(24 * time.Hour)
}

func closedThrough(candles []domain.Candle, to time.Time) []domain.Candle {
	out := make([]domain.Candle, 0, len(candles))
	for _, c := range candles {
		if c.Closed && !c.CloseTime.After(to) {
			out = append(out, c)
		}
	}
	return out
}

func missingCandleVolume(candles []domain.Candle) bool {
	for _, candle := range candles {
		if candle.Volume > 0 {
			return false
		}
	}
	return len(candles) > 0
}

func hasCandleGaps(candles []domain.Candle, tf domain.Timeframe) bool {
	for i := 1; i < len(candles); i++ {
		if candles[i].OpenTime.Sub(candles[i-1].OpenTime) > tf.Duration() {
			return true
		}
	}
	return false
}

func tail(candles []domain.Candle, limit int) []domain.Candle {
	if len(candles) <= limit {
		return candles
	}
	return candles[len(candles)-limit:]
}

func positionContexts(positions []*openTrade, at time.Time, mark float64) []domain.PositionContext {
	out := make([]domain.PositionContext, 0, len(positions))
	for _, position := range positions {
		remainingPct, _ := position.remainingQty.Div(position.originalQty).Mul(decimal.NewFromInt(100)).Float64()
		unrealized := (mark - position.entryPrice) / position.entryPrice * 100 * float64(position.trade.Direction.Sign())
		ctx := domain.PositionContext{
			PositionID: position.trade.ID, Direction: position.trade.Direction,
			EntryPrice: position.entryPrice, Leverage: position.trade.Leverage.InexactFloat64(),
			RemainingPct: remainingPct, UnrealizedPct: unrealized,
			AgeMinutes: int(at.Sub(position.trade.OpenedAt).Minutes()), SizeKnown: true,
		}
		for _, level := range position.stopLoss {
			ctx.CurrentStops = append(ctx.CurrentStops, level.Price)
		}
		for _, level := range position.takeProfit {
			ctx.CurrentTargets = append(ctx.CurrentTargets, level.Price)
		}
		out = append(out, ctx)
	}
	return out
}
