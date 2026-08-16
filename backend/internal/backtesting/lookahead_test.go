package backtesting

import (
	"context"
	"encoding/json"
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/crypto-market-advisor/advisor/internal/config"
	"github.com/crypto-market-advisor/advisor/internal/domain"
	"github.com/crypto-market-advisor/advisor/internal/logging"
	"github.com/crypto-market-advisor/advisor/internal/risk"
)

func testEngine() *Engine {
	cfg := config.Config{
		Risk: config.RiskConfig{
			MinLeverage:             5,
			MaxLeverage:             50,
			MaxRecommendedAllocPct:  decimal.NewFromInt(15),
			HighVolatilityATRPct:    1.5,
			ExtremeVolatilityATRPct: 3.0,
			MinConfidence:           55,
		},
	}
	logger := logging.New("error", "text")
	return &Engine{
		repos:   nil,
		llm:     nil,
		risk:    risk.New(cfg.Risk),
		cfg:     cfg,
		log:     logger,
		running: map[uuid.UUID]context.CancelFunc{},
	}
}

// series builds a deterministic candle series with a trend and noise.
func series(n int, seed int64, start time.Time) []domain.Candle {
	rng := rand.New(rand.NewSource(seed))
	price := 100.0
	out := make([]domain.Candle, 0, n)

	for i := 0; i < n; i++ {
		drift := math.Sin(float64(i)/23) * 1.4
		noise := (rng.Float64() - 0.5) * 0.9
		open := price
		close := open + drift + noise
		high := math.Max(open, close) + rng.Float64()*0.6
		low := math.Min(open, close) - rng.Float64()*0.6

		out = append(out, domain.Candle{
			OpenTime:  start.Add(time.Duration(i) * time.Hour),
			CloseTime: start.Add(time.Duration(i+1) * time.Hour),
			Open:      open, High: high, Low: low, Close: close,
			Volume: 1000, Closed: true, Source: domain.CandleSourceNative,
		})
		price = close
	}
	return out
}

func testRun(from, to time.Time) domain.BacktestRun {
	return domain.BacktestRun{
		ID:        uuid.New(),
		Mode:      domain.BacktestTechnical,
		Symbol:    "BTC",
		Timeframe: domain.TF1h,
		DateFrom:  from,
		DateTo:    to,
		Params: domain.BacktestParams{
			Mode:           domain.BacktestTechnical,
			Symbol:         "BTC",
			Timeframe:      domain.TF1h,
			DateFrom:       from,
			DateTo:         to,
			InitialCapital: decimal.NewFromInt(10000),
			AllocationPct:  decimal.NewFromInt(5),
			Leverage:       decimal.NewFromInt(10),
			SlippagePct:    decimal.NewFromFloat(0.02),
			TakerFeePct:    decimal.NewFromFloat(0.055),
			MinConfidence:  55,
		},
	}
}

// TestNoLookAheadInBacktest is the regression test required by the spec: the
// trades produced over a period must not change when the data after that period
// changes. If any part of the pipeline peeked into the future, appending a
// wildly different tail would alter the earlier trades.
func TestNoLookAheadInBacktest(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	base := series(600, 99, start)

	shortRun := base[:400]

	// The same 400 candles, followed by a violent future the engine must not see.
	extended := make([]domain.Candle, 400, 600)
	copy(extended, base[:400])
	price := base[399].Close
	for i := 400; i < 600; i++ {
		price *= 1.05 // a runaway rally after the cut
		extended = append(extended, domain.Candle{
			OpenTime:  start.Add(time.Duration(i) * time.Hour),
			CloseTime: start.Add(time.Duration(i+1) * time.Hour),
			Open:      price * 0.99, High: price * 1.02, Low: price * 0.97, Close: price,
			Volume: 5000, Closed: true, Source: domain.CandleSourceNative,
		})
	}

	from := start.Add(300 * time.Hour)
	to := start.Add(400 * time.Hour)
	run := testRun(from, to)

	engine := testEngine()
	tradesA, _, _, err := engine.simulateCandles(context.Background(), run, shortRun)
	if err != nil {
		t.Fatalf("first simulation failed: %v", err)
	}
	tradesB, _, _, err := engine.simulateCandles(context.Background(), run, extended)
	if err != nil {
		t.Fatalf("second simulation failed: %v", err)
	}

	// Compare only trades that both runs could have finished: the shorter run
	// legitimately cannot resolve a trade that is still open when its data ends.
	cutoff := shortRun[len(shortRun)-1].CloseTime
	a := tradesInWindow(tradesA, from, to, cutoff)
	b := tradesInWindow(tradesB, from, to, cutoff)

	if len(a) != len(b) {
		t.Fatalf("future candles changed the number of trades: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if !sameTrade(a[i], b[i]) {
			x, _ := json.Marshal(a[i])
			y, _ := json.Marshal(b[i])
			t.Fatalf("future candles changed trade %d:\n%s\n%s", i, x, y)
		}
	}
}

func tradesInWindow(trades []domain.BacktestTrade, from, to, cutoff time.Time) []domain.BacktestTrade {
	var out []domain.BacktestTrade
	for _, t := range trades {
		if t.OpenedAt.Before(from) || t.OpenedAt.After(to) {
			continue
		}
		if t.ExitReason == "end_of_period" {
			continue // this exit legitimately depends on where the data stops
		}
		if t.ClosedAt == nil || t.ClosedAt.After(cutoff) {
			continue // the shorter run had no data left to resolve this trade
		}
		out = append(out, t)
	}
	return out
}

func sameTrade(a, b domain.BacktestTrade) bool {
	return a.OpenedAt.Equal(b.OpenedAt) &&
		a.Direction == b.Direction &&
		a.EntryPrice.Equal(b.EntryPrice) &&
		a.NetPnL.Equal(b.NetPnL) &&
		a.ExitReason == b.ExitReason
}

// TestEntryUsesOnlyClosedCandles verifies the entry price comes from the signal
// bar's close, never from a later bar.
func TestEntryUsesOnlyClosedCandles(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	candles := series(500, 7, start)

	from := start.Add(300 * time.Hour)
	to := start.Add(480 * time.Hour)
	run := testRun(from, to)

	trades, _, _, err := testEngine().simulateCandles(context.Background(), run, candles)
	if err != nil {
		t.Fatalf("simulation failed: %v", err)
	}
	if len(trades) == 0 {
		t.Skip("this series produced no trades; nothing to assert")
	}

	byTime := make(map[int64]domain.Candle, len(candles))
	for _, c := range candles {
		byTime[c.CloseTime.Unix()] = c
	}
	for _, tr := range trades {
		candle, ok := byTime[tr.OpenedAt.Unix()]
		if !ok {
			t.Fatalf("trade opened at %v does not match any candle close", tr.OpenedAt)
		}
		// Entry is the signal bar's close adjusted by slippage only.
		expected := candle.Close * (1 + float64(tr.Direction.Sign())*0.0002)
		if math.Abs(tr.EntryPrice.InexactFloat64()-expected) > 0.01 {
			t.Fatalf("entry %v does not match the signal bar close %v", tr.EntryPrice, expected)
		}
		if tr.ClosedAt != nil && !tr.ClosedAt.After(tr.OpenedAt) {
			t.Fatalf("a trade must close strictly after it opens: %v -> %v", tr.OpenedAt, tr.ClosedAt)
		}
	}
}

// TestAmbiguousCandleTakesThePessimisticExit checks that when one candle
// touches both levels the engine does not invent a favourable ordering.
func TestAmbiguousCandleTakesThePessimisticExit(t *testing.T) {
	engine := testEngine()
	entry := 100.0
	confidence := 70

	open := &openTrade{
		trade: domain.BacktestTrade{
			ID:         uuid.New(),
			Direction:  domain.DirectionLong,
			OpenedAt:   time.Now().UTC(),
			EntryPrice: decimal.NewFromFloat(entry),
			Quantity:   decimal.NewFromInt(1),
			Leverage:   decimal.NewFromInt(10),
			Confidence: &confidence,
		},
		takeProfit:    []domain.PriceTarget{{Price: 105, ClosePct: 100}},
		stopLoss:      []domain.PriceTarget{{Price: 95, ClosePct: 100}},
		entryPrice:    entry,
		originalQty:   decimal.NewFromInt(1),
		remainingQty:  decimal.NewFromInt(1),
		initialMargin: decimal.NewFromInt(10),
		nextFundingAt: time.Now().UTC().Add(24 * time.Hour),
	}

	// This candle reaches both 105 and 95.
	candle := domain.Candle{
		OpenTime: time.Now().UTC(), CloseTime: time.Now().UTC().Add(time.Hour),
		Open: 100, High: 106, Low: 94, Close: 99, Closed: true,
	}
	run := testRun(time.Now().UTC(), time.Now().UTC().Add(time.Hour))

	state := newSimState(run.Params)
	closed, trade := engine.updateOpenTrade(open, candle, run.Params, run, state, nil)
	if !closed {
		t.Fatal("a candle touching both levels must close the trade")
	}
	if trade.ExitReason != "stop_loss_ambiguous_candle" {
		t.Fatalf("the ambiguity must be recorded in the exit reason, got %q", trade.ExitReason)
	}
	if trade.NetPnL.IsPositive() {
		t.Fatalf("the pessimistic outcome must be taken, got a profit of %s", trade.NetPnL)
	}
}

func TestPartialTakeProfitsCloseOnlyTheirConfiguredShare(t *testing.T) {
	engine := testEngine()
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	run := testRun(start, start.Add(3*time.Hour))
	run.Params.MakerFeePct = decimal.NewFromFloat(0.02)
	run.Params.TakerFeePct = decimal.NewFromFloat(0.055)
	confidence := 70
	position := &openTrade{
		trade: domain.BacktestTrade{
			ID: uuid.New(), Direction: domain.DirectionLong, OpenedAt: start,
			EntryPrice: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1),
			Leverage: decimal.NewFromInt(10), Confidence: &confidence,
		},
		takeProfit: []domain.PriceTarget{{Price: 105, ClosePct: 50}, {Price: 110, ClosePct: 50}},
		stopLoss:   []domain.PriceTarget{{Price: 95, ClosePct: 100}},
		entryPrice: 100, originalQty: decimal.NewFromInt(1), remainingQty: decimal.NewFromInt(1),
		initialMargin: decimal.NewFromInt(10), nextFundingAt: start.Add(8 * time.Hour),
	}
	state := newSimState(run.Params)

	closed, _ := engine.updateOpenTrade(position, domain.Candle{
		OpenTime: start, CloseTime: start.Add(time.Hour), Open: 100, High: 106, Low: 99, Close: 105, Closed: true,
	}, run.Params, run, state, nil)
	if closed || !position.remainingQty.Equal(decimal.NewFromFloat(0.5)) {
		t.Fatalf("first 50%% TP must leave half open: closed=%v remaining=%s", closed, position.remainingQty)
	}

	closed, trade := engine.updateOpenTrade(position, domain.Candle{
		OpenTime: start.Add(time.Hour), CloseTime: start.Add(2 * time.Hour), Open: 105, High: 111, Low: 104, Close: 110, Closed: true,
	}, run.Params, run, state, nil)
	if !closed || len(trade.Executions) != 2 {
		t.Fatalf("second TP must finish the trade with two fills: closed=%v executions=%d", closed, len(trade.Executions))
	}
	if trade.Executions[0].ClosePct != 50 || trade.Executions[1].ClosePct != 50 {
		t.Fatalf("partial percentages were not preserved: %+v", trade.Executions)
	}
	if trade.Executions[0].FeeType != domain.FeeMaker || trade.Executions[1].FeeType != domain.FeeMaker {
		t.Fatalf("take-profit limit fills must use maker fees: %+v", trade.Executions)
	}
	if trade.ExitPrice == nil || !trade.ExitPrice.Equal(decimal.NewFromFloat(107.5)) {
		t.Fatalf("expected quantity-weighted exit 107.5, got %v", trade.ExitPrice)
	}
}

func TestFundingAndLiquidationAreApplied(t *testing.T) {
	engine := testEngine()
	start := time.Date(2024, 1, 1, 7, 0, 0, 0, time.UTC)
	run := testRun(start, start.Add(3*time.Hour))
	run.Params.FundingRatePct = decimal.NewFromFloat(0.01)
	run.Params.MaintenanceMarginPct = decimal.NewFromFloat(0.5)
	confidence := 70
	position := &openTrade{
		trade: domain.BacktestTrade{
			ID: uuid.New(), Direction: domain.DirectionLong, OpenedAt: start,
			EntryPrice: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1),
			Leverage: decimal.NewFromInt(10), Confidence: &confidence,
		},
		entryPrice: 100, originalQty: decimal.NewFromInt(1), remainingQty: decimal.NewFromInt(1),
		initialMargin: decimal.NewFromInt(10), nextFundingAt: start.Add(time.Hour),
	}
	state := newSimState(run.Params)

	closed, trade := engine.updateOpenTrade(position, domain.Candle{
		OpenTime: start, CloseTime: start.Add(2 * time.Hour), Open: 100, High: 101, Low: 90, Close: 91, Closed: true,
	}, run.Params, run, state, nil)
	if !closed || trade.ExitReason != "liquidation" {
		t.Fatalf("10x long must liquidate below its maintenance threshold: closed=%v reason=%s", closed, trade.ExitReason)
	}
	if !trade.Funding.IsNegative() {
		t.Fatalf("positive funding rate must charge a long, got %s", trade.Funding)
	}
	foundFunding := false
	for _, execution := range trade.Executions {
		foundFunding = foundFunding || execution.Kind == "funding"
	}
	if !foundFunding {
		t.Fatal("funding event must be retained in the execution audit")
	}
}

// longPosition builds a 10x long of one unit at 100 for exit-path tests.
func longPosition(start time.Time, stops []domain.PriceTarget) *openTrade {
	confidence := 70
	return &openTrade{
		trade: domain.BacktestTrade{
			ID: uuid.New(), Direction: domain.DirectionLong, OpenedAt: start,
			EntryPrice: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1),
			Leverage: decimal.NewFromInt(10), Confidence: &confidence,
		},
		stopLoss:   stops,
		entryPrice: 100, originalQty: decimal.NewFromInt(1), remainingQty: decimal.NewFromInt(1),
		initialMargin: decimal.NewFromInt(10), nextFundingAt: start.Add(24 * time.Hour),
	}
}

// TestStopLossExecutesBeforeLiquidation guards the ordering inside one candle:
// price has to pass the nearer stop first, so a protected trade must not be
// reported as liquidated just because the same bar reached far enough.
func TestStopLossExecutesBeforeLiquidation(t *testing.T) {
	engine := testEngine()
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	run := testRun(start, start.Add(time.Hour))
	run.Params.MaintenanceMarginPct = decimal.NewFromFloat(0.5)
	position := longPosition(start, []domain.PriceTarget{{Price: 95, ClosePct: 100}})
	state := newSimState(run.Params)

	// The 10x long liquidates near 90.5; this candle sweeps far below it.
	closed, trade := engine.updateOpenTrade(position, domain.Candle{
		OpenTime: start, CloseTime: start.Add(time.Hour), Open: 99, High: 99, Low: 85, Close: 88, Closed: true,
	}, run.Params, run, state, nil)

	if !closed || trade.ExitReason != "stop_loss" {
		t.Fatalf("the stop must execute before liquidation: closed=%v reason=%q", closed, trade.ExitReason)
	}
	if trade.GrossPnL.LessThan(decimal.NewFromInt(-6)) {
		t.Fatalf("loss must be bounded by the stop at 95, got %s", trade.GrossPnL)
	}
}

// TestStopFillsAtTheOpenAfterAGap keeps gapped exits honest: a stop is a market
// order and cannot get its level back once the candle opened beyond it.
func TestStopFillsAtTheOpenAfterAGap(t *testing.T) {
	engine := testEngine()
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	run := testRun(start, start.Add(time.Hour))
	position := longPosition(start, []domain.PriceTarget{{Price: 95, ClosePct: 100}})
	state := newSimState(run.Params)

	closed, trade := engine.updateOpenTrade(position, domain.Candle{
		OpenTime: start, CloseTime: start.Add(time.Hour), Open: 92, High: 93, Low: 91, Close: 92, Closed: true,
	}, run.Params, run, state, nil)

	if !closed || trade.ExitPrice == nil {
		t.Fatalf("the gapped stop must close the trade: closed=%v", closed)
	}
	if trade.ExitPrice.GreaterThan(decimal.NewFromFloat(92)) {
		t.Fatalf("a stop cannot fill above the gapped open of 92, got %s", trade.ExitPrice)
	}
}

// TestLiquidationLossIsCappedAtTheMargin checks that a gap through the
// liquidation price does not charge the trade more than it ever posted.
func TestLiquidationLossIsCappedAtTheMargin(t *testing.T) {
	engine := testEngine()
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	run := testRun(start, start.Add(time.Hour))
	run.Params.MaintenanceMarginPct = decimal.NewFromFloat(0.5)
	run.Params.SlippagePct = decimal.Zero
	run.Params.TakerFeePct = decimal.Zero
	position := longPosition(start, nil)
	state := newSimState(run.Params)

	closed, trade := engine.updateOpenTrade(position, domain.Candle{
		OpenTime: start, CloseTime: start.Add(time.Hour), Open: 70, High: 72, Low: 65, Close: 68, Closed: true,
	}, run.Params, run, state, nil)

	if !closed || trade.ExitReason != "liquidation" {
		t.Fatalf("expected a liquidation, got closed=%v reason=%q", closed, trade.ExitReason)
	}
	// One unit at 100 with 10x leverage posts 10 of margin.
	if trade.NetPnL.LessThan(decimal.NewFromInt(-10)) {
		t.Fatalf("a liquidation must not lose more than the posted margin, got %s", trade.NetPnL)
	}
	if trade.PnLPct > -99 {
		t.Fatalf("a liquidation must still write off the margin, got %.2f%%", trade.PnLPct)
	}
}

// TestPartialStopThenLiquidation covers the mixed case: the near stop takes its
// share and the rest of the position is still exposed to the same candle.
func TestPartialStopThenLiquidation(t *testing.T) {
	engine := testEngine()
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	run := testRun(start, start.Add(time.Hour))
	run.Params.MaintenanceMarginPct = decimal.NewFromFloat(0.5)
	position := longPosition(start, []domain.PriceTarget{{Price: 97, ClosePct: 50}})
	state := newSimState(run.Params)

	closed, trade := engine.updateOpenTrade(position, domain.Candle{
		OpenTime: start, CloseTime: start.Add(time.Hour), Open: 99, High: 99, Low: 85, Close: 86, Closed: true,
	}, run.Params, run, state, nil)

	if !closed || trade.ExitReason != "liquidation" {
		t.Fatalf("the remainder must liquidate: closed=%v reason=%q", closed, trade.ExitReason)
	}
	kinds := make([]string, 0, len(trade.Executions))
	for _, execution := range trade.Executions {
		kinds = append(kinds, execution.Kind)
	}
	if len(kinds) != 2 || kinds[0] != "stop_loss" || kinds[1] != "liquidation" {
		t.Fatalf("expected a stop_loss fill followed by a liquidation, got %v", kinds)
	}
	if !position.remainingQty.IsZero() {
		t.Fatalf("nothing may stay open after a liquidation, got %s", position.remainingQty)
	}
}

// TestPnLLadderClosesByReturnOnMargin covers the custom exit mode: the steps are
// stated as return on margin, so the price levels have to follow the leverage
// the position actually opened with.
func TestPnLLadderClosesByReturnOnMargin(t *testing.T) {
	engine := testEngine()
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	run := testRun(start, start.Add(4*time.Hour))
	run.Params.ExitMode = domain.ExitModePnLLadder
	run.Params.SlippagePct = decimal.Zero
	run.Params.MakerFeePct, run.Params.TakerFeePct = decimal.Zero, decimal.Zero
	run.Params.TakeProfitLadder = []domain.PnLExitStep{
		{PnLPct: 50, ClosePct: 50},
		{PnLPct: 75, ClosePct: 25},
		{PnLPct: 100, ClosePct: 25},
	}
	run.Params.StopLossLadder = []domain.PnLExitStep{{PnLPct: 50, ClosePct: 100}}

	signal := &signalResult{
		direction: domain.DirectionLong, confidence: 70, leverage: 10,
		allocationPct: decimal.NewFromInt(5),
		takeProfit:    []domain.PriceTarget{{Price: 999, ClosePct: 100}},
		stopLoss:      []domain.PriceTarget{{Price: 1, ClosePct: 100}},
	}
	entryCandle := domain.Candle{
		OpenTime: start, CloseTime: start.Add(time.Hour),
		Open: 100, High: 100, Low: 100, Close: 100, Closed: true,
	}
	state := newSimState(run.Params)
	position := engine.openTradeFrom(signal, entryCandle, run.Params, state, nil)
	if position == nil {
		t.Fatal("the position must open")
	}

	// At 10x, +50% on margin is a +5% price move and -50% is -5%.
	want := []float64{105, 107.5, 110}
	if len(position.takeProfit) != 3 {
		t.Fatalf("the ladder must replace the signal targets, got %+v", position.takeProfit)
	}
	for i, level := range position.takeProfit {
		if math.Abs(level.Price-want[i]) > 1e-9 {
			t.Fatalf("target %d: want %.4f, got %.4f", i, want[i], level.Price)
		}
	}
	if len(position.stopLoss) != 1 || math.Abs(position.stopLoss[0].Price-95) > 1e-9 {
		t.Fatalf("the stop step must sit at -50%% on margin, got %+v", position.stopLoss)
	}

	// First step: +50% on margin closes half.
	closed, _ := engine.updateOpenTrade(position, domain.Candle{
		OpenTime: start.Add(time.Hour), CloseTime: start.Add(2 * time.Hour),
		Open: 100, High: 105.5, Low: 100, Close: 105, Closed: true,
	}, run.Params, run, state, nil)
	half := position.originalQty.Div(decimal.NewFromInt(2))
	if closed || !position.remainingQty.Equal(half) {
		t.Fatalf("the +50%% step must close exactly half: closed=%v remaining=%s", closed, position.remainingQty)
	}

	// A candle reaching +100% takes the two remaining steps in order.
	closed, trade := engine.updateOpenTrade(position, domain.Candle{
		OpenTime: start.Add(2 * time.Hour), CloseTime: start.Add(3 * time.Hour),
		Open: 105, High: 110.5, Low: 105, Close: 110, Closed: true,
	}, run.Params, run, state, nil)
	if !closed {
		t.Fatalf("the last step must close the position, remaining=%s", position.remainingQty)
	}
	var exits []domain.BacktestExecution
	for _, execution := range trade.Executions {
		if execution.Kind != "entry" {
			exits = append(exits, execution)
		}
	}
	if len(exits) != 3 {
		t.Fatalf("expected three staged fills, got %d (%+v)", len(exits), trade.Executions)
	}
	shares := []float64{50, 25, 25}
	for i, execution := range exits {
		if math.Abs(execution.ClosePct-shares[i]) > 1e-6 {
			t.Fatalf("fill %d closed %.2f%%, want %.0f%%", i, execution.ClosePct, shares[i])
		}
	}
	// Without fees or slippage the trade realises the share-weighted average of
	// its steps: 0.5*50 + 0.25*75 + 0.25*100 = 68.75% on margin.
	if math.Abs(trade.PnLPct-68.75) > 0.01 {
		t.Fatalf("the staged ladder must realise +68.75%% on margin, got %.2f", trade.PnLPct)
	}
}

// TestBreakEvenStopProtectsAPartiallyBankedTrade covers the change the stored
// runs argued for: a trade that reached its first target and then reversed used
// to give back a full stop, which was where a large share of the losing trades
// came from.
func TestBreakEvenStopProtectsAPartiallyBankedTrade(t *testing.T) {
	engine := testEngine()
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	run := testRun(start, start.Add(3*time.Hour))
	run.Params.BreakEvenAfterTP = true
	run.Params.SlippagePct, run.Params.MakerFeePct, run.Params.TakerFeePct = decimal.Zero, decimal.Zero, decimal.Zero

	position := longPosition(start, []domain.PriceTarget{{Price: 95, ClosePct: 100}})
	position.takeProfit = []domain.PriceTarget{{Price: 105, ClosePct: 50}, {Price: 110, ClosePct: 50}}
	state := newSimState(run.Params)

	// First target fills: half is banked and the stop must jump to the entry.
	closed, _ := engine.updateOpenTrade(position, domain.Candle{
		OpenTime: start, CloseTime: start.Add(time.Hour), Open: 100, High: 106, Low: 99, Close: 105, Closed: true,
	}, run.Params, run, state, nil)
	if closed {
		t.Fatal("half the position must stay open after the first target")
	}
	if len(position.stopLoss) != 1 || position.stopLoss[0].Price != 100 {
		t.Fatalf("the stop must move to the entry price, got %+v", position.stopLoss)
	}

	// The reversal now costs nothing instead of a full stop.
	closed, trade := engine.updateOpenTrade(position, domain.Candle{
		OpenTime: start.Add(time.Hour), CloseTime: start.Add(2 * time.Hour), Open: 105, High: 105, Low: 94, Close: 96, Closed: true,
	}, run.Params, run, state, nil)
	if !closed {
		t.Fatal("the break-even stop must close the remainder")
	}
	if !trade.GrossPnL.Equal(decimal.NewFromFloat(2.5)) {
		t.Fatalf("expected only the banked half to count, got %s", trade.GrossPnL)
	}
}

func TestRiskPlanUsesRiskAdjustedAllocation(t *testing.T) {
	engine := testEngine()
	params := testRun(time.Now(), time.Now().Add(time.Hour)).Params
	params.AllocationPct = decimal.NewFromInt(30)
	snapshot := domain.FeatureSnapshot{DataQuality: domain.DataQuality{Status: domain.DataQualityDegraded}}
	_, allocation := engine.riskPlan(riskRequest{
		direction: domain.DirectionLong, confidence: 60, price: 100,
		stops:      []domain.PriceTarget{{Price: 95, ClosePct: 100}},
		allocation: params.AllocationPct,
	}, snapshot, params)
	if !allocation.LessThan(decimal.NewFromInt(15)) {
		t.Fatalf("risk-adjusted allocation must be below the configured 15%% cap, got %s", allocation)
	}
}

func TestMaxDrawdownKeepsPeakAtTheTimeOfDrawdown(t *testing.T) {
	state := newSimState(domain.BacktestParams{InitialCapital: decimal.NewFromInt(100)})
	state.recordEquity(decimal.NewFromInt(50))  // 50% drawdown from 100
	state.recordEquity(decimal.NewFromInt(200)) // later new peak
	state.recordEquity(decimal.NewFromInt(170)) // only 15% from new peak
	metrics := state.metrics(nil)
	if metrics.MaxDrawdownPct != 50 {
		t.Fatalf("expected historical max drawdown 50%%, got %.2f", metrics.MaxDrawdownPct)
	}
}

// TestBacktestReportsMissingData makes the reason for smaller positions
// visible: a timeframe without stored history degrades data quality, which the
// risk engine reacts to, so the run has to say so instead of looking healthy.
func TestBacktestReportsMissingData(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	candles := series(500, 11, start)
	run := testRun(start.Add(300*time.Hour), start.Add(480*time.Hour))

	_, metrics, _, err := testEngine().simulateSeries(context.Background(), run,
		SimulationInputs{Series: map[domain.Timeframe][]domain.Candle{
			domain.TF1h: candles,
			domain.TF4h: candles[:5], // nowhere near enough bars to analyse
		}}, nil)
	if err != nil {
		t.Fatalf("simulation failed: %v", err)
	}
	if metrics.DegradedSteps == 0 {
		t.Fatal("a timeframe without history must be counted as a degraded step")
	}
	found := false
	for _, issue := range metrics.DataIssues {
		found = found || issue == "timeframe_4h"
	}
	if !found {
		t.Fatalf("the missing timeframe must be named, got %v", metrics.DataIssues)
	}
}

func TestEstimateSteps(t *testing.T) {
	from := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	params := domain.BacktestParams{
		Timeframe: domain.TF1h,
		DateFrom:  from,
		DateTo:    from.Add(240 * time.Hour),
	}
	if got := EstimateSteps(params); got != 240 {
		t.Fatalf("expected 240 hourly steps, got %d", got)
	}

	params.AnalysisInterval = "4h"
	if got := EstimateSteps(params); got != 60 {
		t.Fatalf("expected 60 four-hourly steps, got %d", got)
	}

	params.DateTo = from
	if got := EstimateSteps(params); got != 0 {
		t.Fatalf("an empty window must estimate 0 steps, got %d", got)
	}
}

func TestMetricsOnEmptyRun(t *testing.T) {
	state := newSimState(domain.BacktestParams{InitialCapital: decimal.NewFromInt(1000)})
	m := state.metrics(nil)

	if m.Trades != 0 || m.WinRate != 0 || m.ProfitFactor != nil {
		t.Fatalf("an empty run must produce empty metrics: %+v", m)
	}
	if !m.FinalCapital.Equal(decimal.NewFromInt(1000)) {
		t.Fatalf("capital must be untouched, got %s", m.FinalCapital)
	}
}

// TestTrailingStopRidesTheMoveAndNeverRetreats covers the Chandelier exit: the
// stop follows the extreme reached since entry and never gives ground back.
func TestTrailingStopRidesTheMoveAndNeverRetreats(t *testing.T) {
	engine := testEngine()
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	run := testRun(start, start.Add(5*time.Hour))
	run.Params.ExitMode = domain.ExitModeTrailingATR
	run.Params.SlippagePct, run.Params.MakerFeePct, run.Params.TakerFeePct = decimal.Zero, decimal.Zero, decimal.Zero

	signal := &signalResult{
		direction: domain.DirectionLong, confidence: 70, leverage: 10,
		allocationPct: decimal.NewFromInt(5), atr: 2,
		stopLoss: []domain.PriceTarget{{Price: 90, ClosePct: 100}},
	}
	entry := domain.Candle{OpenTime: start, CloseTime: start.Add(time.Hour),
		Open: 100, High: 100, Low: 100, Close: 100, Closed: true}
	state := newSimState(run.Params)
	position := engine.openTradeFrom(signal, entry, run.Params, state, nil)
	if position == nil {
		t.Fatal("the position must open")
	}
	// The trailing distance is DefaultTrailingATRMult average ranges below the
	// entry, so with an ATR of 2 the first stop sits at 95.
	wantStop := 100 - DefaultTrailingATRMult*signal.atr
	if len(position.stopLoss) != 1 || math.Abs(position.stopLoss[0].Price-wantStop) > 1e-9 {
		t.Fatalf("expected the initial stop at %v, got %+v", wantStop, position.stopLoss)
	}
	if len(position.takeProfit) != 0 {
		t.Fatalf("a trailing exit has no target: %+v", position.takeProfit)
	}

	// Price runs to 110: the stop follows it up by the same distance.
	wantTrailed := 110 - DefaultTrailingATRMult*signal.atr
	engine.updateOpenTrade(position, domain.Candle{
		OpenTime: start.Add(time.Hour), CloseTime: start.Add(2 * time.Hour),
		Open: 100, High: 110, Low: 99, Close: 108, Closed: true,
	}, run.Params, run, state, nil)
	if math.Abs(position.stopLoss[0].Price-wantTrailed) > 1e-9 {
		t.Fatalf("the stop must follow the high to %v, got %+v", wantTrailed, position.stopLoss)
	}

	// A quieter bar that stays above the trailed stop must not push it back down.
	engine.updateOpenTrade(position, domain.Candle{
		OpenTime: start.Add(2 * time.Hour), CloseTime: start.Add(3 * time.Hour),
		Open: 108, High: 108, Low: wantTrailed + 1, Close: 108, Closed: true,
	}, run.Params, run, state, nil)
	if math.Abs(position.stopLoss[0].Price-wantTrailed) > 1e-9 {
		t.Fatalf("the stop must not retreat, got %+v", position.stopLoss)
	}

	// Crossing it closes the trade in profit rather than at the entry stop.
	closed, trade := engine.updateOpenTrade(position, domain.Candle{
		OpenTime: start.Add(3 * time.Hour), CloseTime: start.Add(4 * time.Hour),
		Open: 106, High: 106, Low: 100, Close: 101, Closed: true,
	}, run.Params, run, state, nil)
	if !closed || !trade.GrossPnL.IsPositive() {
		t.Fatalf("the trailing stop must bank the move: closed=%v pnl=%s", closed, trade.GrossPnL)
	}
}

// TestFundingUsesTheStoredRate covers the accounting change that made the
// harness honest: a position charges the rate the exchange actually published
// for that settlement, and falls back to the run's flat rate only where no
// history exists. With a trailing exit holding positions for days this is the
// difference between a plausible result and a flattering one.
func TestFundingUsesTheStoredRate(t *testing.T) {
	engine := testEngine()
	start := time.Date(2024, 1, 1, 4, 0, 0, 0, time.UTC)
	run := testRun(start, start.Add(24*time.Hour))
	run.Params.FundingRatePct = decimal.NewFromFloat(0.01) // the flat fallback

	newPosition := func() *openTrade {
		return &openTrade{
			trade: domain.BacktestTrade{
				Direction:  domain.DirectionLong,
				OpenedAt:   start,
				EntryPrice: decimal.NewFromInt(100),
				Quantity:   decimal.NewFromInt(10),
				Leverage:   decimal.NewFromInt(5),
			},
			entryPrice:    100,
			originalQty:   decimal.NewFromInt(10),
			remainingQty:  decimal.NewFromInt(10),
			nextFundingAt: nextFundingAfter(start),
		}
	}
	candle := domain.Candle{
		OpenTime: start, CloseTime: start.Add(9 * time.Hour),
		Open: 100, High: 100, Low: 100, Close: 100, Closed: true,
	}

	// One settlement falls inside the candle: 0.05% of a notional of 1000, paid
	// by the long, so the position is 0.5 worse off.
	settlement := nextFundingAfter(start)
	stored := fundingSchedule([]domain.FundingRate{{SettledAt: settlement, Rate: 0.0005}})

	withHistory := newPosition()
	engine.applyFunding(withHistory, candle, run.Params, newSimState(run.Params), stored)
	if got := withHistory.funding.InexactFloat64(); math.Abs(got+0.5) > 1e-9 {
		t.Fatalf("the stored rate must be charged, got %v", got)
	}

	// Without history the run's own flat rate applies instead: 0.01% of 1000.
	withoutHistory := newPosition()
	engine.applyFunding(withoutHistory, candle, run.Params, newSimState(run.Params), nil)
	if got := withoutHistory.funding.InexactFloat64(); math.Abs(got+0.1) > 1e-9 {
		t.Fatalf("the configured flat rate must apply without history, got %v", got)
	}
}

// TestMarketContextInBacktestIgnoresTheFuture keeps the market-wide filter under
// the same look-ahead rule as everything else: the state at a moment may only
// be read from benchmark candles that had already closed.
func TestMarketContextInBacktestIgnoresTheFuture(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	daily := make([]domain.Candle, 0, 400)
	price := 100.0
	for i := 0; i < 400; i++ {
		open := start.AddDate(0, 0, i)
		daily = append(daily, domain.Candle{
			OpenTime: open, CloseTime: open.AddDate(0, 0, 1),
			Open: price, High: price, Low: price, Close: price, Closed: true,
		})
		// A rising benchmark that collapses after the moment under test.
		if i < 300 {
			price += 1
		} else {
			price *= 0.9
		}
	}

	at := daily[299].CloseTime
	got := marketContextAt(daily, at, "BTC")
	if got.Trend != domain.MarketTrendUp {
		t.Fatalf("the state at %v must reflect the rally, not the later crash: %+v", at, got)
	}
	if !got.AsOf.Equal(at) {
		t.Fatalf("the state must be read from the candle that had just closed, got %v", got.AsOf)
	}
	if empty := marketContextAt(nil, at, "BTC"); empty.Known() {
		t.Fatalf("no benchmark series must produce an unknown state, got %+v", empty)
	}
}

// TestPullbackEntryRestsAndShiftsItsLevels covers the entry change: the order
// waits below the signal, pays the maker fee when the market comes to it, and
// carries its exit geometry down with it rather than keeping levels measured
// from a price the position never had.
func TestPullbackEntryRestsAndShiftsItsLevels(t *testing.T) {
	signal := &signalResult{
		direction: domain.DirectionLong, reference: 100, atr: 2,
		takeProfit: []domain.PriceTarget{{Price: 106, ClosePct: 100}},
		stopLoss:   []domain.PriceTarget{{Price: 97, ClosePct: 100}},
	}
	pending := pendingEntry{signal: signal, limit: 99, expiresAt: 5}

	if pending.fills(domain.Candle{High: 101, Low: 99.5, Close: 100}) {
		t.Fatal("a bar that never reached the order must not fill it")
	}
	if !pending.fills(domain.Candle{High: 101, Low: 98.5, Close: 100}) {
		t.Fatal("a bar that traded through the order must fill it")
	}

	shifted := pending.shifted()
	if shifted.reference != 99 {
		t.Fatalf("the fill becomes the new reference, got %v", shifted.reference)
	}
	// Everything moves by the same one point the entry did: the geometry the
	// strategy asked for is preserved, only measured from where it filled.
	if len(shifted.takeProfit) != 1 || math.Abs(shifted.takeProfit[0].Price-105) > 1e-9 {
		t.Fatalf("the target must move with the entry, got %+v", shifted.takeProfit)
	}
	if len(shifted.stopLoss) != 1 || math.Abs(shifted.stopLoss[0].Price-96) > 1e-9 {
		t.Fatalf("the stop must move with the entry, got %+v", shifted.stopLoss)
	}
	// The original is untouched: a signal may be examined without being consumed.
	if signal.takeProfit[0].Price != 106 || signal.reference != 100 {
		t.Fatalf("shifting must not mutate the signal: %+v", signal)
	}

	short := pendingEntry{signal: &signalResult{direction: domain.DirectionShort, reference: 100}, limit: 101}
	if short.fills(domain.Candle{High: 100.5, Low: 99}) {
		t.Fatal("a short entry waits above the signal, not below it")
	}
	if !short.fills(domain.Candle{High: 101.5, Low: 99}) {
		t.Fatal("a short entry must fill when price rises to it")
	}
}

// TestPullbackEntryPaysTheMakerFee pins the arithmetic half of the change: a
// resting order crosses no spread and is charged the cheaper side.
func TestPullbackEntryPaysTheMakerFee(t *testing.T) {
	engine := testEngine()
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	run := testRun(start, start.Add(24*time.Hour))
	run.Params.MakerFeePct = decimal.NewFromFloat(0.02)
	run.Params.TakerFeePct = decimal.NewFromFloat(0.055)

	signal := &signalResult{
		direction: domain.DirectionLong, reference: 100, atr: 2, leverage: 5,
		allocationPct: decimal.NewFromInt(5), confidence: 70,
		stopLoss: []domain.PriceTarget{{Price: 97, ClosePct: 100}},
	}
	candle := domain.Candle{OpenTime: start, CloseTime: start.Add(time.Hour),
		Open: 100, High: 100, Low: 99, Close: 100, Closed: true}

	market := engine.openTradeFrom(signal, candle, run.Params, newSimState(run.Params), nil)
	limit := engine.openTradeAt(signal, 99, domain.FeeMaker, candle, run.Params, newSimState(run.Params), nil)
	if market == nil || limit == nil {
		t.Fatal("both entries must open")
	}
	if !limit.fees.LessThan(market.fees) {
		t.Fatalf("the resting order must be cheaper: %s vs %s", limit.fees, market.fees)
	}
	if limit.executions[0].FeeType != domain.FeeMaker {
		t.Fatalf("the fill must be recorded as maker, got %s", limit.executions[0].FeeType)
	}
	// The market entry also pays the spread, the resting one does not.
	if math.Abs(limit.entryPrice-99) > 1e-9 {
		t.Fatalf("the resting order fills at its own level, got %v", limit.entryPrice)
	}
	if limit.entryPrice >= market.entryPrice {
		t.Fatalf("a long that waited must have entered lower: %v vs %v", limit.entryPrice, market.entryPrice)
	}
}
