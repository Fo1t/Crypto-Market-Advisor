package backtesting

import (
	"context"
	"encoding/json"
	"math"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/crypto-market-advisor/advisor/internal/config"
	"github.com/crypto-market-advisor/advisor/internal/domain"
	"github.com/crypto-market-advisor/advisor/internal/llm"
	"github.com/crypto-market-advisor/advisor/internal/logging"
)

// inferenceSink is an InferenceStore that keeps nothing: the LLM backtest tests
// care about the decisions, not the audit trail.
type inferenceSink struct {
	mu    sync.Mutex
	count int
}

func (s *inferenceSink) InsertInference(context.Context, domain.InferenceRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.count++
	return nil
}

func (s *inferenceSink) CachedInference(context.Context, string) (string, bool, error) {
	return "", false, nil
}

// llmTestEngine is the engine of testEngine wired to a model that always
// answers with the given completion.
func llmTestEngine(t *testing.T, answer string) *Engine {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": answer}}},
			"usage":   map[string]int{"prompt_tokens": 1200, "completion_tokens": 300},
		})
	}))
	t.Cleanup(srv.Close)

	engine := testEngine()
	logger := logging.New("error", "text")
	engine.llm = llm.NewService(llm.NewClient(config.LLMConfig{
		BaseURL: srv.URL + "/v1", Model: "test-model", Timeout: 5 * time.Second,
		Temperature: 0.2, MaxTokens: 800, ContextSize: 32768, MaxConcurrent: 1, Enabled: true,
	}, logger), &inferenceSink{}, logger)
	return engine
}

// priceSeries builds candles that oscillate around a level, so a fixed model
// answer stays valid against the reference price of every bar.
func priceSeries(n int, seed int64, start time.Time, level float64) []domain.Candle {
	rng := rand.New(rand.NewSource(seed))
	out := make([]domain.Candle, 0, n)
	price := level
	for i := 0; i < n; i++ {
		open := price
		close := level + math.Sin(float64(i)/17)*level*0.004 + (rng.Float64()-0.5)*level*0.002
		high := math.Max(open, close) * 1.0008
		low := math.Min(open, close) * 0.9992
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

func llmRun(from, to time.Time, params func(*domain.BacktestParams)) domain.BacktestRun {
	run := testRun(from, to)
	run.Mode = domain.BacktestLLM
	run.Params.Mode = domain.BacktestLLM
	run.Params.ExitMode = domain.ExitModeTrailingATR
	run.Params.TrailingATRMult = decimal.NewFromFloat(2.25)
	run.Params.MinConfidence = 0
	if params != nil {
		params(&run.Params)
	}
	return run
}

// TestLLMEntryOpensWithATRExit is the regression test for a run that produced
// OPEN_LONG answers and no trades at all: the LLM signal carried no ATR, and
// every ATR-based exit mode refused the entry after the fee had been charged.
func TestLLMEntryOpensWithATRExit(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	candles := priceSeries(400, 11, start, 100000)
	run := llmRun(start.Add(250*time.Hour), start.Add(400*time.Hour), nil)

	engine := llmTestEngine(t, llmOpenLong)
	trades, metrics, _, err := engine.simulateCandles(context.Background(), run, candles)
	if err != nil {
		t.Fatalf("simulation failed: %v", err)
	}
	if len(trades) == 0 {
		t.Fatalf("an OPEN_LONG answer must open a position, reasons: %v", metrics.DecisionReasons)
	}
	if metrics.DecisionReasons[string(domain.StrategyReasonEntry)] == 0 {
		t.Fatalf("an entry must be recorded as such, got %v", metrics.DecisionReasons)
	}
	for _, trade := range trades {
		if trade.Direction != domain.DirectionLong {
			t.Fatalf("expected long trades, got %s", trade.Direction)
		}
	}
}

// TestLLMNoActionIsReported keeps a quiet run explainable: a model that never
// asks for a trade must still say so in the metrics.
func TestLLMNoActionIsReported(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	candles := priceSeries(400, 12, start, 100000)
	run := llmRun(start.Add(250*time.Hour), start.Add(400*time.Hour), nil)

	engine := llmTestEngine(t, llmNoAction)
	trades, metrics, _, err := engine.simulateCandles(context.Background(), run, candles)
	if err != nil {
		t.Fatalf("simulation failed: %v", err)
	}
	if len(trades) != 0 {
		t.Fatalf("NO_ACTION must not trade, got %d trades", len(trades))
	}
	if metrics.DecisionReasons[string(reasonLLMNoEntry)] == 0 {
		t.Fatalf("NO_ACTION must be counted, got %v", metrics.DecisionReasons)
	}
}

// TestLLMPullbackEntryRestsAtALimit proves the ATR now reaches the entry logic
// too: with a pullback configured the fill happens below the signal bar close
// and pays the maker fee.
func TestLLMPullbackEntryRestsAtALimit(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	candles := priceSeries(400, 13, start, 100000)
	run := llmRun(start.Add(250*time.Hour), start.Add(400*time.Hour), func(p *domain.BacktestParams) {
		p.EntryPullbackATR = decimal.NewFromFloat(0.5)
		p.EntryValidBars = 3
	})

	engine := llmTestEngine(t, llmOpenLong)
	trades, metrics, _, err := engine.simulateCandles(context.Background(), run, candles)
	if err != nil {
		t.Fatalf("simulation failed: %v", err)
	}
	if len(trades) == 0 && metrics.UnfilledEntries == 0 {
		t.Fatalf("a pullback entry must either fill or expire, reasons: %v", metrics.DecisionReasons)
	}
	for _, trade := range trades {
		if len(trade.Executions) == 0 || trade.Executions[0].FeeType != domain.FeeMaker {
			t.Fatal("a resting entry that the market reached pays the maker fee")
		}
	}
}

// TestRefusedEntryChargesNoFee covers the accounting side of the same bug: an
// entry the exit mode cannot build must leave the balance untouched.
func TestRefusedEntryChargesNoFee(t *testing.T) {
	engine := testEngine()
	params := testRun(time.Now(), time.Now()).Params
	params.ExitMode = domain.ExitModeTrailingATR
	state := newSimState(params)
	before := state.equity()

	candle := domain.Candle{
		OpenTime: time.Now(), CloseTime: time.Now().Add(time.Hour),
		Open: 100, High: 101, Low: 99, Close: 100, Closed: true,
	}
	signal := &signalResult{
		direction: domain.DirectionLong, reference: 100, confidence: 70,
		leverage: 10, allocationPct: decimal.NewFromInt(5), atr: 0,
	}
	if position := engine.openTradeAt(signal, 100, domain.FeeTaker, candle, params, state, nil); position != nil {
		t.Fatal("a trailing-ATR run must not open a position without an ATR")
	}
	if !state.equity().Equal(before) {
		t.Fatalf("a refused entry must cost nothing, equity moved %s -> %s", before, state.equity())
	}
}

const llmOpenLong = `{
  "action": "OPEN_LONG",
  "confidence": 72,
  "risk_level": "medium",
  "recommended_allocation_pct": 5,
  "leverage": {"recommended": 10},
  "entry": {"type": "market", "current_price": 100000},
  "take_profit": [{"price": 102000, "close_pct": 100}],
  "stop_loss": [{"price": 98000, "close_pct": 100}],
  "translations": {
    "ru": {
      "summary": "Продолжение тренда подтверждается несколькими таймфреймами.",
      "leverage_reason": "Умеренная волатильность",
      "take_profit_reasons": ["Ближайшее сопротивление"],
      "stop_loss_reasons": ["Ниже подтверждённой поддержки"],
      "management_reasons": [],
      "signals_for": ["Восходящий тренд на часовом графике"],
      "signals_against": ["Сопротивление на четырёхчасовом графике"],
      "invalidation_conditions": ["Часовая свеча закроется ниже 98000"]
    },
    "en": {
      "summary": "Multiple timeframes confirm trend continuation.",
      "leverage_reason": "Moderate volatility",
      "take_profit_reasons": ["Nearest resistance"],
      "stop_loss_reasons": ["Below confirmed support"],
      "management_reasons": [],
      "signals_for": ["One-hour uptrend"],
      "signals_against": ["Four-hour resistance"],
      "invalidation_conditions": ["One-hour candle closes below 98000"]
    },
    "zh-CN": {
      "summary": "多个时间周期确认趋势延续。",
      "leverage_reason": "波动率适中",
      "take_profit_reasons": ["最近阻力位"],
      "stop_loss_reasons": ["确认支撑位下方"],
      "management_reasons": [],
      "signals_for": ["一小时图呈上升趋势"],
      "signals_against": ["四小时图存在阻力"],
      "invalidation_conditions": ["一小时蜡烛收于98000下方"]
    }
  }
}`

const llmNoAction = `{
  "action": "NO_ACTION",
  "confidence": 40,
  "risk_level": "medium",
  "translations": {
    "ru": {
      "summary": "Сигналы противоречивы, преимущества нет.",
      "signals_for": ["Цена держится выше поддержки"],
      "signals_against": ["Конфликт таймфреймов"],
      "take_profit_reasons": [],
      "stop_loss_reasons": [],
      "management_reasons": [],
      "invalidation_conditions": ["Пробой диапазона в любую сторону"]
    },
    "en": {
      "summary": "Signals conflict and there is no edge.",
      "signals_for": ["Price holds above support"],
      "signals_against": ["Timeframe conflict"],
      "take_profit_reasons": [],
      "stop_loss_reasons": [],
      "management_reasons": [],
      "invalidation_conditions": ["A break out of the range either way"]
    },
    "zh-CN": {
      "summary": "信号相互矛盾，没有优势。",
      "signals_for": ["价格守住支撑位"],
      "signals_against": ["时间周期冲突"],
      "take_profit_reasons": [],
      "stop_loss_reasons": [],
      "management_reasons": [],
      "invalidation_conditions": ["区间被向任一方向突破"]
    }
  }
}`

// TestEffectiveStopsFollowTheExitMode pins what the risk engine is sized
// against: the level the run will really exit at, not the one the signal
// happened to carry.
func TestEffectiveStopsFollowTheExitMode(t *testing.T) {
	signalStop := []domain.PriceTarget{{Price: 95, ClosePct: 100}}
	req := riskRequest{
		direction: domain.DirectionLong, price: 100, atr: 2, stops: signalStop,
	}

	params := domain.BacktestParams{ExitMode: domain.ExitModeSignal}
	if got := effectiveStops(req, 10, params); got[0].Price != 95 {
		t.Fatalf("signal mode keeps the signal's own stop, got %v", got)
	}

	params = domain.BacktestParams{
		ExitMode: domain.ExitModeTrailingATR, TrailingATRMult: decimal.NewFromFloat(2.5),
	}
	// 100 - 2 * 2.5: the widest the Chandelier stop will ever be.
	if got := effectiveStops(req, 10, params); got[0].Price != 95.0 {
		t.Fatalf("trailing mode must use the initial Chandelier level, got %v", got)
	}
	params.TrailingATRMult = decimal.NewFromFloat(1)
	if got := effectiveStops(req, 10, params); got[0].Price != 98 {
		t.Fatalf("a tighter trail is a nearer stop, got %v", got)
	}

	params = domain.BacktestParams{
		ExitMode:       domain.ExitModePnLLadder,
		StopLossLadder: []domain.PnLExitStep{{PnLPct: -30, ClosePct: 100}},
	}
	// 30% of margin at 10x is a 3% move.
	if got := effectiveStops(req, 10, params); math.Abs(got[0].Price-97) > 1e-9 {
		t.Fatalf("ladder mode must price the configured loss on margin, got %v", got)
	}

	// A trailing run with no ATR has nothing better than the signal's stop.
	req.atr = 0
	params = domain.BacktestParams{ExitMode: domain.ExitModeTrailingATR}
	if got := effectiveStops(req, 10, params); got[0].Price != 95 {
		t.Fatalf("without an ATR the signal's stop stands, got %v", got)
	}
}

// TestUnreachableModelEndsTheRun keeps a dead model from burning the whole run
// timeout on network errors: the replay stops and says why, instead of walking
// hundreds of bars that can produce no decision.
func TestUnreachableModelEndsTheRun(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	candles := priceSeries(400, 41, start, 100000)
	run := llmRun(start.Add(250*time.Hour), start.Add(400*time.Hour), nil)

	// A server that refuses every call the way an offline model would.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "connection refused", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)

	engine := testEngine()
	logger := logging.New("error", "text")
	engine.llm = llm.NewService(llm.NewClient(config.LLMConfig{
		BaseURL: srv.URL + "/v1", Model: "test-model", Timeout: time.Second,
		MaxTokens: 800, ContextSize: 32768, MaxConcurrent: 1, Enabled: true,
	}, logger), &inferenceSink{}, logger)

	_, _, _, err := engine.simulateCandles(context.Background(), run, candles)
	if err == nil {
		t.Fatal("a replay whose model never answers must fail, not finish empty")
	}
	if !strings.Contains(err.Error(), "in a row") {
		t.Fatalf("the error must say the model stopped answering, got %v", err)
	}
}
