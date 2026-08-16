// Command lab is the offline research harness for the deterministic strategy
// engine.
//
// It replays stored candles through exactly the same backtesting engine the
// application uses, but without a database, without the LLM and without writing
// anything: a sweep of hundreds of runs finishes in seconds and produces one
// pooled table. That is what makes "does this change actually help" answerable
// on more than a single symbol and a single period.
//
// Candles come from CSV files exported from the application database, one file
// per timeframe, columns: symbol,open_time,close_time,open,high,low,close,volume.
//
//	go run ./cmd/lab -data ./candles -tf 1h -symbols BTC,ETH \
//	    -periods "IS=2025-08-15:2026-02-15,OOS=2026-02-15:2026-08-15" \
//	    -variants baseline.json,candidate.json
package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/crypto-market-advisor/advisor/internal/analysis/features"
	"github.com/crypto-market-advisor/advisor/internal/analysis/strategies"
	"github.com/crypto-market-advisor/advisor/internal/backtesting"
	"github.com/crypto-market-advisor/advisor/internal/config"
	"github.com/crypto-market-advisor/advisor/internal/domain"
	"github.com/crypto-market-advisor/advisor/internal/risk"
)

func main() {
	var (
		dataDir  = flag.String("data", "./candles", "directory with <timeframe>.csv candle exports")
		tfList   = flag.String("tf", "1h", "comma separated timeframes to run")
		symList  = flag.String("symbols", "BTC,ETH,BNB,XRP,SOL,DOGE,ADA,LINK,BCH,XLM,TRX,XMR", "comma separated symbols")
		periods  = flag.String("periods", "", "named windows, e.g. IS=2025-01-01:2025-07-01,OOS=2025-07-01:2026-01-01")
		variants = flag.String("variants", "", "comma separated variant JSON files; empty means the shipped defaults")
		context_ = flag.String("context", "higher", "which timeframes the analysis sees: primary|higher")
		workers  = flag.Int("workers", 0, "parallel runs; 0 uses the number of CPUs")
		alloc    = flag.Float64("allocation", 5, "allocation percent per trade")
		leverage = flag.Int("leverage", 5, "requested leverage before the risk engine trims it")
		minConf  = flag.Int("min-confidence", 0, "reject signals below this confidence")
		maker    = flag.Float64("maker-fee", 0.02, "maker fee percent")
		taker    = flag.Float64("taker-fee", 0.055, "taker fee percent")
		slippage = flag.Float64("slippage", 0.02, "slippage percent per side")
		perRun   = flag.Bool("per-run", false, "print every individual run as well as the pooled table")
		verbose  = flag.Bool("verbose", false, "add exit-reason and long/short breakdown under every pooled line")
		jsonOut  = flag.String("json", "", "also write the full result set to this JSON file")
		boot     = flag.Int("bootstrap", 0, "resample calendar months with replacement this many times to test the move study for significance")
		bootHor  = flag.Int("bootstrap-horizon", 10, "forward window, in bars, the bootstrap measures")
		placebo  = flag.Int("placebo", 0, "after the real run, repeat it this many times with random entries matched on asset, period, exit machinery and trade count")
		study    = flag.Bool("signal-study", false, "score forward returns after every signal instead of simulating trades")
		fundStud = flag.Bool("funding-study", false, "score forward returns by the percentile of the prevailing funding rate")
		moveStud = flag.Int("move-study", 0, "score forward returns by the trailing move over this many bars, measured in average bar ranges")
		gateDist = flag.Float64("gate-buffer", 0, "buffer a long demands above the benchmark average, in percent; 0 asks for none")
		gateFall = flag.Bool("gate-allow-falling", false, "lift the demand that the benchmark average itself be rising")
		horizon  = flag.String("horizons", "", "comma separated forward windows in bars for the studies; empty uses the defaults")
		aggr     = flag.Int("aggregate", 0, "merge every N bars of the traded timeframe into one before replaying; probes a slower clock without inventing a new timeframe")
		pullback = flag.Float64("entry-pullback", -1, "resting limit entry this many ATR below the signal; 0 enters at the market, negative uses the shipped default")
		validFor = flag.Int("entry-valid-bars", 0, "how many bars the resting entry waits; 0 uses the shipped default")
		slots    = flag.String("portfolio", "", "compose the per-symbol trades into one account; comma separated slot counts, e.g. 2,3,5")
		pAlloc   = flag.String("portfolio-alloc", "", "comma separated allocation percentages for the composed account; empty uses -allocation")
		riskPer  = flag.Float64("risk-per-trade", 0, "share of equity a stop-out may cost; 0 keeps the fixed allocation")
		minRS    = flag.Float64("min-strength", 0, "percentile an asset must reach among its peers; 0 uses the shipped default")
		eqGuard  = flag.Int("equity-filter", 0, "suspend new entries while the composed account is below its own average over this many days; 0 disables")
		bench    = flag.String("benchmark", "BTC", "symbol whose daily trend stands for the market as a whole; empty disables the market filter")
		costMult = flag.Float64("cost-multiple", 0, "how many round trips a target must be worth; 0 uses the shipped default")
		fundingD = flag.String("funding", "", "directory with <symbol>.csv funding exports (settled_at,rate); empty charges the flat rate")
	)
	flag.Parse()

	timeframes, err := domain.ParseTimeframes(splitList(*tfList))
	if err != nil {
		log.Fatalf("timeframes: %v", err)
	}
	symbols := splitList(*symList)
	windows, err := parseWindows(*periods)
	if err != nil {
		log.Fatalf("periods: %v", err)
	}
	variantSet, err := loadVariants(splitList(*variants))
	if err != nil {
		log.Fatalf("variants: %v", err)
	}
	labVariants = variantSet

	store, err := loadCandles(*dataDir, timeframes, *context_)
	if err != nil {
		log.Fatalf("candles: %v", err)
	}
	// The benchmark is judged on daily bars whatever the run trades, so its
	// series is loaded even when no run asks for that timeframe.
	if *bench != "" {
		daily, err := loadCandles(*dataDir, []domain.Timeframe{domain.TF1d}, "primary")
		if err != nil {
			log.Fatalf("benchmark candles: %v", err)
		}
		for k, v := range daily {
			if _, ok := store[k]; !ok {
				store[k] = v
			}
		}
	}

	labBenchmark = strings.ToUpper(strings.TrimSpace(*bench))
	// The cross-sectional ranking is built from the daily history of every symbol
	// the run knows about, which is the same universe a live cycle would compare.
	if daily := dailyUniverse(store, symbols); len(daily) >= 2 {
		labRanker = features.NewDailyRanker(daily)
	}
	labRiskPerTrade = *riskPer
	engine := backtesting.NewEngine(nil, nil, riskEngine(), nil, labConfig(), noopLogger())
	base := baseParams(*alloc, *leverage, *minConf, *maker, *taker, *slippage)
	base.CostFloorMultiple = decimal.NewFromFloat(*costMult)
	base.MinRelativeStrengthPct = decimal.NewFromFloat(*minRS)
	// A negative flag means "whatever a new run in the UI would get", so the
	// harness measures the shipped behaviour unless asked otherwise.
	if *pullback < 0 {
		base.EntryPullbackATR = decimal.NewFromFloat(backtesting.DefaultEntryPullbackATR)
	} else {
		base.EntryPullbackATR = decimal.NewFromFloat(*pullback)
	}
	base.EntryValidBars = *validFor
	base.MarketGateLongBufferPct = decimal.NewFromFloat(*gateDist)
	base.MarketGateAllowFallingAverage = gateFall

	funding, err := loadFunding(*fundingD)
	if err != nil {
		log.Fatalf("funding: %v", err)
	}
	if *fundingD != "" && len(funding) == 0 {
		log.Fatalf("funding: no settlements found in %s", *fundingD)
	}

	if *aggr > 1 {
		store = aggregateStore(store, timeframes, *aggr)
	}

	jobs := buildJobs(symbols, timeframes, windows, variantSet, store, *context_, labBenchmark, funding)
	if len(jobs) == 0 {
		log.Fatal("nothing to run: no symbol has candles for the requested timeframes and periods")
	}
	fmt.Fprintf(os.Stderr, "running %d simulations...\n", len(jobs))

	if *study {
		studySignals(jobs, base, *workers)
		return
	}
	if *fundStud {
		studyFunding(jobs, *workers)
		return
	}
	if custom, err := numberList(*horizon); err != nil {
		log.Fatalf("horizons: %v", err)
	} else if len(custom) > 0 {
		horizons = horizons[:0]
		for _, h := range custom {
			horizons = append(horizons, int(h))
		}
	}
	if *moveStud > 0 {
		studyMoves(jobs, *workers, *moveStud)
		if *boot > 0 {
			reportBootstrap(jobs, *moveStud, *bootHor, *boot)
		}
		return
	}

	results := runJobs(engine, base, jobs, *workers)
	report(results, variantSet, windows, timeframes, *perRun, *verbose)

	if *placebo > 0 {
		reportPlacebo(engine, base, jobs, results, *placebo, *workers)
	}
	slotList, err := numberList(*slots)
	if err != nil {
		log.Fatalf("portfolio: %v", err)
	}
	allocList, err := numberList(*pAlloc)
	if err != nil {
		log.Fatalf("portfolio-alloc: %v", err)
	}
	if len(allocList) == 0 {
		// With a risk budget configured the sizes differ per trade, and averaging
		// them into one constant would erase exactly what is being measured.
		if labRiskPerTrade > 0 {
			allocList = []float64{0}
		} else {
			allocList = []float64{*alloc}
		}
	}
	for _, n := range slotList {
		for _, a := range allocList {
			reportPortfolio(results, variantSet, windows, int(n), a,
				time.Duration(*eqGuard)*24*time.Hour)
		}
	}

	if *jsonOut != "" {
		if err := writeJSON(*jsonOut, results); err != nil {
			log.Fatalf("write json: %v", err)
		}
	}
}

// --- variants ---------------------------------------------------------------

// Variant is one configuration under test. Everything that is not set falls
// back to the shipped defaults, so a file only has to state what it changes.
type Variant struct {
	Name       string              `json:"name"`
	Strategies *domain.StrategySet `json:"strategies,omitempty"`

	// Execution overrides. A nil pointer keeps the harness default.
	ExitMode         string               `json:"exit_mode,omitempty"`
	TrailingATRMult  *float64             `json:"trailing_atr_mult,omitempty"`
	TakeProfitLadder []domain.PnLExitStep `json:"take_profit_ladder,omitempty"`
	StopLossLadder   []domain.PnLExitStep `json:"stop_loss_ladder,omitempty"`
	BreakEvenAfterTP *bool                `json:"break_even_after_tp,omitempty"`
	MinConfidence    *int                 `json:"min_confidence,omitempty"`
	MaxOpenPositions *int                 `json:"max_open_positions,omitempty"`
	Leverage         *int                 `json:"leverage,omitempty"`
	AllocationPct    *float64             `json:"allocation_pct,omitempty"`

	Sides string `json:"sides,omitempty"`

	ATRStopMult        *float64 `json:"atr_stop_mult,omitempty"`
	ATRTarget1Mult     *float64 `json:"atr_target1_mult,omitempty"`
	ATRTarget2Mult     *float64 `json:"atr_target2_mult,omitempty"`
	ATRTarget1ClosePct *float64 `json:"atr_target1_close_pct,omitempty"`
}

func loadVariants(paths []string) ([]Variant, error) {
	if len(paths) == 0 {
		set := strategies.DefaultSet()
		return []Variant{{Name: "defaults", Strategies: &set}}, nil
	}
	out := make([]Variant, 0, len(paths))
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var v Variant
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if v.Name == "" {
			v.Name = strings.TrimSuffix(filepath.Base(path), ".json")
		}
		if v.Strategies == nil {
			set := strategies.DefaultSet()
			v.Strategies = &set
		} else {
			set := strategies.Normalize(*v.Strategies)
			if err := strategies.Validate(set); err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			v.Strategies = &set
		}
		out = append(out, v)
	}
	return out, nil
}

// apply folds a variant's overrides into the shared parameter template.
func (v Variant) apply(params domain.BacktestParams) domain.BacktestParams {
	params.Strategies = v.Strategies
	if v.Sides != "" && v.Strategies != nil {
		set := *v.Strategies
		set.Sides = domain.StrategySides(v.Sides)
		params.Strategies = &set
	}
	if v.ExitMode != "" {
		params.ExitMode = domain.BacktestExitMode(v.ExitMode)
	}
	if v.TrailingATRMult != nil {
		params.TrailingATRMult = decimal.NewFromFloat(*v.TrailingATRMult)
	}
	if len(v.TakeProfitLadder) > 0 {
		params.TakeProfitLadder = v.TakeProfitLadder
	}
	if len(v.StopLossLadder) > 0 {
		params.StopLossLadder = v.StopLossLadder
	}
	if v.BreakEvenAfterTP != nil {
		params.BreakEvenAfterTP = *v.BreakEvenAfterTP
	}
	if v.MinConfidence != nil {
		params.MinConfidence = *v.MinConfidence
	}
	if v.MaxOpenPositions != nil {
		params.MaxOpenPositions = *v.MaxOpenPositions
	}
	if v.Leverage != nil {
		params.Leverage = decimal.NewFromInt(int64(*v.Leverage))
	}
	if v.AllocationPct != nil {
		params.AllocationPct = decimal.NewFromFloat(*v.AllocationPct)
	}
	for _, field := range []struct {
		value *float64
		dst   *decimal.Decimal
	}{
		{v.ATRStopMult, &params.ATRStopMult},
		{v.ATRTarget1Mult, &params.ATRTarget1Mult},
		{v.ATRTarget2Mult, &params.ATRTarget2Mult},
		{v.ATRTarget1ClosePct, &params.ATRTarget1ClosePct},
	} {
		if field.value != nil {
			*field.dst = decimal.NewFromFloat(*field.value)
		}
	}
	return params
}

// --- candle store -----------------------------------------------------------

type key struct {
	symbol string
	tf     domain.Timeframe
}

type store map[key][]domain.Candle

func loadCandles(dir string, requested []domain.Timeframe, contextMode string) (store, error) {
	needed := map[domain.Timeframe]bool{}
	for _, tf := range requested {
		needed[tf] = true
		if contextMode == "higher" {
			for _, higher := range higherThan(tf) {
				needed[higher] = true
			}
		}
	}

	out := store{}
	for tf := range needed {
		path := filepath.Join(dir, string(tf)+".csv")
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		reader := csv.NewReader(file)
		reader.FieldsPerRecord = 8
		reader.ReuseRecord = true
		for {
			record, err := reader.Read()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				_ = file.Close()
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			candle, symbol, err := parseCandle(record)
			if err != nil {
				_ = file.Close()
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			k := key{symbol: symbol, tf: tf}
			out[k] = append(out[k], candle)
		}
		_ = file.Close()
	}
	for k := range out {
		sort.Slice(out[k], func(i, j int) bool { return out[k][i].OpenTime.Before(out[k][j].OpenTime) })
	}
	return out, nil
}

func parseCandle(record []string) (domain.Candle, string, error) {
	openTime, err := parseTime(record[1])
	if err != nil {
		return domain.Candle{}, "", err
	}
	closeTime, err := parseTime(record[2])
	if err != nil {
		return domain.Candle{}, "", err
	}
	values := make([]float64, 5)
	for i := 0; i < 5; i++ {
		v, err := strconv.ParseFloat(record[3+i], 64)
		if err != nil {
			return domain.Candle{}, "", err
		}
		values[i] = v
	}
	return domain.Candle{
		OpenTime: openTime, CloseTime: closeTime,
		Open: values[0], High: values[1], Low: values[2], Close: values[3], Volume: values[4],
		Closed: true, Source: domain.CandleSourceNative, Provider: "lab",
	}, record[0], nil
}

func parseTime(s string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02 15:04:05-07", time.RFC3339, "2006-01-02 15:04:05Z07:00"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised timestamp %q", s)
}

// higherThan lists the timeframes above the given one, which is the context a
// live analysis would also have had.
func higherThan(tf domain.Timeframe) []domain.Timeframe {
	ladder := []domain.Timeframe{domain.TF1m, domain.TF5m, domain.TF15m, domain.TF1h, domain.TF4h, domain.TF1d}
	for i, candidate := range ladder {
		if candidate == tf {
			return ladder[i+1:]
		}
	}
	return nil
}

// --- jobs -------------------------------------------------------------------

type window struct {
	name     string
	from, to time.Time
}

type job struct {
	variant int
	window  window
	symbol  string
	tf      domain.Timeframe
	series  map[domain.Timeframe][]domain.Candle
	// benchmark is the daily series of the market proxy, uncut: the engine only
	// ever looks at the part that had closed at the moment being simulated.
	benchmark []domain.Candle
	// funding is the settled funding history of this symbol's perpetual.
	funding []domain.FundingRate
	// randomChance and randomSeed turn this job into a control replication.
	randomChance float64
	randomSeed   int64
}

// Result is one completed simulation.
type Result struct {
	Variant   string                 `json:"variant"`
	Window    string                 `json:"window"`
	Symbol    string                 `json:"symbol"`
	Timeframe domain.Timeframe       `json:"timeframe"`
	Metrics   domain.BacktestMetrics `json:"metrics"`
	// BuyHoldPct is what simply holding the asset over the same window returned.
	// Without it a long-biased system cannot be told apart from the market.
	BuyHoldPct float64   `json:"buy_hold_pct"`
	Returns    []float64 `json:"-"`
	LongRet    []float64 `json:"-"`
	ShortRet   []float64 `json:"-"`
	// Trades carries what the portfolio composer needs: when each trade was on
	// and what it returned on its own margin.
	Trades []portfolioTrade `json:"-"`
	// Exits counts how each trade ended. A profile that looks the same in the
	// summary can be reaching its target twice as often and giving it back, and
	// only this breakdown shows that.
	Exits map[string]int `json:"exits,omitempty"`
	Err   string         `json:"error,omitempty"`
}

func parseWindows(spec string) ([]window, error) {
	if strings.TrimSpace(spec) == "" {
		return nil, fmt.Errorf("at least one period is required, e.g. IS=2025-01-01:2026-01-01")
	}
	var out []window
	for _, item := range splitList(spec) {
		name, rest, ok := strings.Cut(item, "=")
		if !ok {
			return nil, fmt.Errorf("period %q must look like NAME=FROM:TO", item)
		}
		fromStr, toStr, ok := strings.Cut(rest, ":")
		if !ok {
			return nil, fmt.Errorf("period %q must look like NAME=FROM:TO", item)
		}
		from, err := time.Parse("2006-01-02", fromStr)
		if err != nil {
			return nil, err
		}
		to, err := time.Parse("2006-01-02", toStr)
		if err != nil {
			return nil, err
		}
		if !to.After(from) {
			return nil, fmt.Errorf("period %q ends before it starts", name)
		}
		out = append(out, window{name: name, from: from.UTC(), to: to.UTC()})
	}
	return out, nil
}

// buildJobs pairs every requested combination with the candles it needs. A
// combination without enough history is skipped rather than reported as a run
// with zero trades, because those two are not the same finding.
func buildJobs(symbols []string, timeframes []domain.Timeframe, windows []window, variants []Variant, data store, contextMode, benchmark string, funding map[string][]domain.FundingRate) []job {
	var jobs []job
	// The whole benchmark history is handed over: the engine cuts it at each
	// simulated moment, which is where the look-ahead defence belongs.
	benchmarkSeries := data[key{symbol: benchmark, tf: domain.TF1d}]
	for vi := range variants {
		for _, w := range windows {
			for _, symbol := range symbols {
				for _, tf := range timeframes {
					primary := data[key{symbol: symbol, tf: tf}]
					warmup := backtesting.WarmupBars
					sliced := sliceWithWarmup(primary, w.from, w.to, warmup)
					if len(sliced) < warmup+20 {
						continue
					}
					series := map[domain.Timeframe][]domain.Candle{tf: sliced}
					if contextMode == "higher" {
						for _, higher := range higherThan(tf) {
							candles := data[key{symbol: symbol, tf: higher}]
							hs := sliceWithWarmup(candles, w.from, w.to, warmup)
							if len(hs) >= 60 {
								series[higher] = hs
							}
						}
					}
					jobs = append(jobs, job{
						variant: vi, window: w, symbol: symbol, tf: tf,
						series: series, benchmark: benchmarkSeries, funding: funding[symbol],
					})
				}
			}
		}
	}
	return jobs
}

// sliceWithWarmup returns the candles of the window plus the history the
// indicators need before it, so the first bar of the window is already analysable.
func sliceWithWarmup(candles []domain.Candle, from, to time.Time, warmup int) []domain.Candle {
	start := sort.Search(len(candles), func(i int) bool { return !candles[i].OpenTime.Before(from) })
	end := sort.Search(len(candles), func(i int) bool { return candles[i].CloseTime.After(to) })
	if start-warmup > 0 {
		start -= warmup
	} else {
		start = 0
	}
	if end <= start {
		return nil
	}
	return candles[start:end]
}

func runJobs(engine *backtesting.Engine, base domain.BacktestParams, jobs []job, workers int) []Result {
	if workers <= 0 {
		workers = maxParallel()
	}
	results := make([]Result, len(jobs))
	queue := make(chan int)
	var wg sync.WaitGroup
	var done int64
	var mu sync.Mutex

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range queue {
				results[index] = runOne(engine, base, jobs[index])
				mu.Lock()
				done++
				if done%50 == 0 {
					fmt.Fprintf(os.Stderr, "\r%d/%d", done, len(jobs))
				}
				mu.Unlock()
			}
		}()
	}
	for i := range jobs {
		queue <- i
	}
	close(queue)
	wg.Wait()
	fmt.Fprintf(os.Stderr, "\r%d/%d done\n", len(jobs), len(jobs))
	return results
}

func runOne(engine *backtesting.Engine, base domain.BacktestParams, j job) Result {
	variant := labVariants[j.variant]
	params := variant.apply(base)
	params.Symbol = j.symbol
	params.Timeframe = j.tf
	params.DateFrom = j.window.from
	params.DateTo = j.window.to

	run := domain.BacktestRun{
		ID: uuid.New(), Mode: domain.BacktestTechnical, Symbol: j.symbol,
		Timeframe: j.tf, DateFrom: j.window.from, DateTo: j.window.to, Params: params,
	}
	result := Result{
		Variant: variant.Name, Window: j.window.name, Symbol: j.symbol, Timeframe: j.tf,
		BuyHoldPct: buyHold(j.series[j.tf], j.window.from, j.window.to),
	}
	inputs := backtesting.SimulationInputs{
		Series: j.series, Benchmark: j.benchmark, Funding: j.funding, Universe: labRanker,
		RandomEntryChance: j.randomChance, RandomEntrySeed: j.randomSeed,
	}
	trades, metrics, _, err := engine.Simulate(context.Background(), run, inputs)
	if err != nil {
		result.Err = err.Error()
		return result
	}
	result.Metrics = metrics
	result.Returns = make([]float64, 0, len(trades))
	result.Exits = map[string]int{}
	for _, trade := range trades {
		result.Returns = append(result.Returns, trade.PnLPct)
		result.Exits[trade.ExitReason]++
		if trade.ClosedAt != nil {
			result.Trades = append(result.Trades, portfolioTrade{
				symbol: j.symbol, openedAt: trade.OpenedAt, closedAt: *trade.ClosedAt,
				pnlPct: trade.PnLPct, allocationPct: trade.AllocationPct.InexactFloat64(),
			})
		}
		if trade.Direction == domain.DirectionLong {
			result.LongRet = append(result.LongRet, trade.PnLPct)
		} else {
			result.ShortRet = append(result.ShortRet, trade.PnLPct)
		}
	}
	return result
}

// labVariants is the parsed variant list. The worker function needs it and the
// job only carries an index, which keeps the queue cheap.
var labVariants []Variant

// --- reporting --------------------------------------------------------------

type pooled struct {
	runs      int
	trades    int
	wins      int
	grossWin  float64
	grossLoss float64
	returns   []float64
	runReturn []float64
	buyHold   []float64
	maxDD     []float64
	longs     int
	shorts    int
	holdSum   float64
	exits     map[string]int
	longRet   []float64
	shortRet  []float64
}

func (p *pooled) add(r Result) {
	if p.exits == nil {
		p.exits = map[string]int{}
	}
	for reason, count := range r.Exits {
		p.exits[reason] += count
	}
	p.runs++
	p.trades += r.Metrics.Trades
	p.wins += r.Metrics.Wins
	p.longs += r.Metrics.LongTrades
	p.shorts += r.Metrics.ShortTrades
	p.holdSum += r.Metrics.AvgHoldingMinute * float64(r.Metrics.Trades)
	p.runReturn = append(p.runReturn, r.Metrics.TotalReturnPct)
	p.buyHold = append(p.buyHold, r.BuyHoldPct)
	p.maxDD = append(p.maxDD, r.Metrics.MaxDrawdownPct)
	p.longRet = append(p.longRet, r.LongRet...)
	p.shortRet = append(p.shortRet, r.ShortRet...)
	for _, value := range r.Returns {
		p.returns = append(p.returns, value)
		if value >= 0 {
			p.grossWin += value
		} else {
			p.grossLoss += -value
		}
	}
}

// detail is the diagnostic line: how the trades ended and how the two sides
// performed. It answers "why" once the summary has said "worse".
func (p pooled) detail() string {
	reasons := make([]string, 0, len(p.exits))
	for reason := range p.exits {
		reasons = append(reasons, reason)
	}
	sort.Slice(reasons, func(i, j int) bool { return p.exits[reasons[i]] > p.exits[reasons[j]] })
	parts := make([]string, 0, len(reasons)+2)
	parts = append(parts, fmt.Sprintf("long %.3f%% x%d", mean(p.longRet), len(p.longRet)))
	parts = append(parts, fmt.Sprintf("short %.3f%% x%d", mean(p.shortRet), len(p.shortRet)))
	for _, reason := range reasons {
		parts = append(parts, fmt.Sprintf("%s %d", reason, p.exits[reason]))
	}
	return strings.Join(parts, " · ")
}

func (p pooled) profitFactor() float64 {
	if p.grossLoss <= 0 {
		return math.Inf(1)
	}
	return p.grossWin / p.grossLoss
}

func (p pooled) expectancy() float64 {
	if len(p.returns) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range p.returns {
		sum += v
	}
	return sum / float64(len(p.returns))
}

func (p pooled) winRate() float64 {
	if p.trades == 0 {
		return 0
	}
	return float64(p.wins) / float64(p.trades) * 100
}

func report(results []Result, variants []Variant, windows []window, timeframes []domain.Timeframe, perRun, verbose bool) {
	if perRun {
		fmt.Println("variant\twindow\tsymbol\ttf\ttrades\twin%\tPF\texpectancy%\treturn%\tmaxDD%")
		for _, r := range results {
			pf := 0.0
			if r.Metrics.ProfitFactor != nil {
				pf = *r.Metrics.ProfitFactor
			}
			fmt.Printf("%s\t%s\t%s\t%s\t%d\t%.1f\t%.2f\t%.2f\t%.2f\t%.2f\n",
				r.Variant, r.Window, r.Symbol, r.Timeframe, r.Metrics.Trades,
				r.Metrics.WinRate*100, pf, r.Metrics.Expectancy, r.Metrics.TotalReturnPct, r.Metrics.MaxDrawdownPct)
		}
		fmt.Println()
	}

	fmt.Println("variant\twindow\ttf\truns\ttrades\twin%\tPF\texpect%\tavgRet%\tmedRet%\tprofitable\tavgDD%\tlong/short\tavgHoldH\tbuyHold%")
	for _, variant := range variants {
		for _, w := range windows {
			for _, tf := range timeframes {
				var p pooled
				for _, r := range results {
					if r.Variant == variant.Name && r.Window == w.name && r.Timeframe == tf {
						p.add(r)
					}
				}
				if p.runs == 0 {
					continue
				}
				printPooled(variant.Name, w.name, string(tf), p, verbose)
			}
		}
		for _, w := range windows {
			var p pooled
			for _, r := range results {
				if r.Variant == variant.Name && r.Window == w.name {
					p.add(r)
				}
			}
			if p.runs > 0 && len(timeframes) > 1 {
				printPooled(variant.Name, w.name, "ALL", p, verbose)
			}
		}
		// The everything line is what decides whether a change earns its place: a
		// profile that only wins on one period or one timeframe has not, and the
		// worst-window column says how bad the slice it loses on gets.
		var all pooled
		worst := math.Inf(1)
		for _, w := range windows {
			var p pooled
			for _, r := range results {
				if r.Variant == variant.Name && r.Window == w.name {
					p.add(r)
					all.add(r)
				}
			}
			if p.runs > 0 {
				worst = math.Min(worst, p.profitFactor())
			}
		}
		if all.runs > 0 && len(windows) > 1 {
			printPooled(variant.Name, fmt.Sprintf("ALL(worstPF %.2f)", worst), "ALL", all, verbose)
		}
	}
}

func printPooled(variant, windowName, tf string, p pooled, verbose bool) {
	profitable := 0
	for _, r := range p.runReturn {
		if r > 0 {
			profitable++
		}
	}
	avgHold := 0.0
	if p.trades > 0 {
		avgHold = p.holdSum / float64(p.trades) / 60
	}
	fmt.Printf("%s\t%s\t%s\t%d\t%d\t%.1f\t%.2f\t%.3f\t%.2f\t%.2f\t%d/%d\t%.2f\t%d/%d\t%.1f\t%.2f\n",
		variant, windowName, tf, p.runs, p.trades, p.winRate(), p.profitFactor(), p.expectancy(),
		mean(p.runReturn), median(p.runReturn), profitable, p.runs, mean(p.maxDD), p.longs, p.shorts, avgHold,
		mean(p.buyHold))
	if verbose {
		fmt.Printf("        %s\n", p.detail())
	}
}

func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

func writeJSON(path string, results []Result) error {
	raw, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

// --- wiring -----------------------------------------------------------------

func baseParams(alloc float64, leverage, minConf int, maker, taker, slippage float64) domain.BacktestParams {
	return domain.BacktestParams{
		Mode:                 domain.BacktestTechnical,
		InitialCapital:       decimal.NewFromInt(10000),
		AllocationPct:        decimal.NewFromFloat(alloc),
		Leverage:             decimal.NewFromInt(int64(leverage)),
		SlippagePct:          decimal.NewFromFloat(slippage),
		MakerFeePct:          decimal.NewFromFloat(maker),
		TakerFeePct:          decimal.NewFromFloat(taker),
		FundingRatePct:       decimal.Zero,
		MaintenanceMarginPct: decimal.Zero,
		MaxOpenPositions:     1,
		MinConfidence:        minConf,
		// The harness starts from the same exit a new run in the UI gets, so a
		// variant that says nothing about exits measures the shipped behaviour.
		ExitMode:        domain.ExitModeTrailingATR,
		TrailingATRMult: decimal.NewFromFloat(backtesting.DefaultTrailingATRMult),
	}
}

// labRiskPerTrade mirrors the -risk-per-trade flag into the risk policy the
// harness hands to the engine.
var labRiskPerTrade float64

func riskEngine() *risk.Engine {
	return risk.New(config.RiskConfig{
		RiskPerTradePct:                labRiskPerTrade,
		MinLeverage:                    5,
		MaxLeverage:                    50,
		MaxRecommendedAllocPct:         decimal.NewFromInt(15),
		HighVolatilityATRPct:           1.5,
		ExtremeVolatilityATRPct:        3,
		MinConfidence:                  55,
		CriticalNewsMaxLeverage:        15,
		CriticalNewsHighVolMaxLeverage: 8,
		CriticalNewsMaxAge:             2 * time.Hour,
	})
}

// labBenchmark is the market proxy the harness hands to the engine, and
// labRanker the cross-sectional standing of every symbol in the universe.
var (
	labBenchmark string
	labRanker    *features.DailyRanker
)

// aggregateStore merges every group bars of the requested timeframes into one.
//
// It is a probe, not a feature: the whole history says the engine does better on
// slower bars (15m 0.62, 1h 0.89, 4h 1.16, 1d 1.28), and before adding a real
// weekly timeframe to the domain, the exchange client and the UI it is worth
// knowing whether the trend actually continues. Merging daily bars into weekly
// ones answers that with a flag.
func aggregateStore(data store, timeframes []domain.Timeframe, group int) store {
	out := store{}
	for k, candles := range data {
		wanted := false
		for _, tf := range timeframes {
			wanted = wanted || k.tf == tf
		}
		if !wanted {
			out[k] = candles
			continue
		}
		out[k] = aggregateCandles(candles, group)
	}
	return out
}

// aggregateCandles merges consecutive bars, keeping the first open, the last
// close and the extremes between them. A trailing partial group is dropped: a
// bar that has not finished forming is not a closed bar.
func aggregateCandles(candles []domain.Candle, group int) []domain.Candle {
	if group < 2 || len(candles) < group {
		return candles
	}
	out := make([]domain.Candle, 0, len(candles)/group)
	for i := 0; i+group <= len(candles); i += group {
		window := candles[i : i+group]
		merged := window[0]
		merged.CloseTime = window[len(window)-1].CloseTime
		merged.Close = window[len(window)-1].Close
		for _, candle := range window[1:] {
			merged.High = math.Max(merged.High, candle.High)
			merged.Low = math.Min(merged.Low, candle.Low)
			merged.Volume += candle.Volume
			merged.Turnover += candle.Turnover
		}
		out = append(out, merged)
	}
	return out
}

// dailyUniverse collects the daily series of the requested symbols, which is
// what the relative-strength ranking is computed from whatever timeframe the
// run itself trades.
func dailyUniverse(data store, symbols []string) map[string][]domain.Candle {
	out := make(map[string][]domain.Candle, len(symbols))
	for _, symbol := range symbols {
		if candles := data[key{symbol: symbol, tf: domain.TF1d}]; len(candles) > 0 {
			out[symbol] = candles
		}
	}
	return out
}

func labConfig() config.Config {
	return config.Config{Analysis: config.AnalysisConfig{
		Timeframes:         []string{},
		CandleHistoryLimit: 500,
		BenchmarkSymbol:    labBenchmark,
	}}
}

// noopLogger keeps the engine's debug output out of the research table.
func noopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func maxParallel() int {
	if n := runtime.NumCPU(); n > 1 {
		return n
	}
	return 1
}

// loadFunding reads settled funding histories, one CSV per symbol with the
// columns settled_at,rate, where the rate is the exchange's own fraction of
// notional per settlement.
func loadFunding(dir string) (map[string][]domain.FundingRate, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := map[string][]domain.FundingRate{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".csv") {
			continue
		}
		symbol := strings.TrimSuffix(entry.Name(), ".csv")
		file, err := os.Open(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		reader := csv.NewReader(file)
		reader.FieldsPerRecord = 2
		for {
			record, err := reader.Read()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				_ = file.Close()
				return nil, fmt.Errorf("%s: %w", entry.Name(), err)
			}
			at, err := parseTime(record[0])
			if err != nil {
				_ = file.Close()
				return nil, fmt.Errorf("%s: %w", entry.Name(), err)
			}
			rate, err := strconv.ParseFloat(record[1], 64)
			if err != nil {
				_ = file.Close()
				return nil, fmt.Errorf("%s: %w", entry.Name(), err)
			}
			out[symbol] = append(out[symbol], domain.FundingRate{SettledAt: at, Rate: rate})
		}
		_ = file.Close()
		sort.Slice(out[symbol], func(i, j int) bool {
			return out[symbol][i].SettledAt.Before(out[symbol][j].SettledAt)
		})
	}
	return out, nil
}

// buyHold is the return of holding the asset for the whole window, which is the
// benchmark a long-biased policy has to beat before it can claim any skill.
func buyHold(candles []domain.Candle, from, to time.Time) float64 {
	start := sort.Search(len(candles), func(i int) bool { return !candles[i].OpenTime.Before(from) })
	end := sort.Search(len(candles), func(i int) bool { return candles[i].CloseTime.After(to) })
	if start >= end || start >= len(candles) || end > len(candles) {
		return 0
	}
	first, last := candles[start].Close, candles[end-1].Close
	if first <= 0 {
		return 0
	}
	return (last - first) / first * 100
}

// numberList parses a comma separated list of numbers, which is how the
// portfolio grid is requested on the command line.
func numberList(raw string) ([]float64, error) {
	var out []float64
	for _, item := range splitList(raw) {
		value, err := strconv.ParseFloat(item, 64)
		if err != nil {
			return nil, fmt.Errorf("%q is not a number", item)
		}
		out = append(out, value)
	}
	return out, nil
}

func splitList(s string) []string {
	var out []string
	for _, item := range strings.Split(s, ",") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
