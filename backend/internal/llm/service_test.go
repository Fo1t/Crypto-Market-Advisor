package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/crypto-market-advisor/advisor/internal/config"
	"github.com/crypto-market-advisor/advisor/internal/domain"
	"github.com/crypto-market-advisor/advisor/internal/logging"
)

// memoryStore is an in-memory InferenceStore for tests.
type memoryStore struct {
	mu      sync.Mutex
	records []domain.InferenceRecord
	cache   map[string]string
}

func newMemoryStore() *memoryStore {
	return &memoryStore{cache: map[string]string{}}
}

func (m *memoryStore) InsertInference(_ context.Context, rec domain.InferenceRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = append(m.records, rec)
	if rec.CacheKey != nil && rec.RawOutput != "" && rec.Status == domain.InferenceOK {
		m.cache[*rec.CacheKey] = rec.RawOutput
	}
	return nil
}

func (m *memoryStore) CachedInference(_ context.Context, key string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	raw, ok := m.cache[key]
	return raw, ok, nil
}

func (m *memoryStore) last() domain.InferenceRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.records) == 0 {
		return domain.InferenceRecord{}
	}
	return m.records[len(m.records)-1]
}

// mockServer serves a scripted sequence of completions.
func mockServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, call int)) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	call := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		call++
		current := call
		mu.Unlock()
		handler(w, r, current)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func writeCompletion(w http.ResponseWriter, content string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": content}}},
		"usage":   map[string]int{"prompt_tokens": 1200, "completion_tokens": 300},
	})
}

func newTestService(t *testing.T, baseURL string, timeout time.Duration, store InferenceStore) *Service {
	t.Helper()
	cfg := config.LLMConfig{
		BaseURL: baseURL, Model: "test-model", Timeout: timeout, Temperature: 0.2,
		MaxTokens: 800, ContextSize: 8192, MaxConcurrent: 1, Enabled: true, PromptVersion: PromptVersionV1,
	}
	logger := logging.New("error", "text")
	return NewService(NewClient(cfg, logger), store, logger)
}

func testRequest() Request {
	return Request{
		Snapshot: domain.FeatureSnapshot{
			SchemaVersion: domain.SchemaVersion,
			Timestamp:     time.Now().UTC(),
			Symbol:        "BTC",
			Price:         100000,
			Timeframes:    map[domain.Timeframe]domain.TimeframeAnalysis{},
			DataQuality:   domain.DataQuality{Status: domain.DataQualityOK},
		},
		Validation:    baseContext(),
		MaxAllocation: 15,
	}
}

func TestServiceReturnsValidatedAnswer(t *testing.T) {
	srv := mockServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		writeCompletion(w, validMultilingual)
	})
	store := newMemoryStore()
	svc := newTestService(t, srv.URL+"/v1", 5*time.Second, store)

	res, err := svc.Analyze(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Validated == nil || res.Validated.Action != domain.RecommendationOpenLong {
		t.Fatal("expected a validated OPEN_LONG answer")
	}
	if got := store.last().Status; got != domain.InferenceOK {
		t.Fatalf("expected status ok, got %s", got)
	}
}

func TestServiceRepairsInvalidAnswerOnce(t *testing.T) {
	broken := strings.Replace(validMultilingual, `"recommended": 10`, `"recommended": 100`, 1)
	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request, call int) {
		if call == 1 {
			writeCompletion(w, broken)
			return
		}
		// The repair turn must carry the rejected output and problem list without
		// repeating the much larger market snapshot.
		body, _ := json.Marshal(map[string]any{})
		_ = body
		var req struct {
			Messages []Message `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if len(req.Messages) != 2 {
			t.Errorf("expected 2 messages in the repair turn, got %d", len(req.Messages))
		}
		if !strings.Contains(req.Messages[1].Content, broken) {
			t.Error("repair prompt must include the rejected output")
		}
		if !strings.Contains(req.Messages[1].Content, "leverage.recommended must be between") {
			t.Errorf("repair prompt must state the validation problem, got %q", req.Messages[1].Content)
		}
		writeCompletion(w, validMultilingual)
	})
	store := newMemoryStore()
	svc := newTestService(t, srv.URL+"/v1", 5*time.Second, store)

	res, err := svc.Analyze(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Validated == nil {
		t.Fatal("expected a validated answer after repair")
	}
	rec := store.last()
	if !rec.RepairAttempted {
		t.Fatal("the record must note that a repair was attempted")
	}
	if rec.Status != domain.InferenceRepaired {
		t.Fatalf("expected status repaired, got %s", rec.Status)
	}
}

func TestServiceGivesUpAfterOneRepair(t *testing.T) {
	broken := strings.Replace(validMultilingual, `"recommended": 10`, `"recommended": 100`, 1)
	calls := 0
	srv := mockServer(t, func(w http.ResponseWriter, _ *http.Request, call int) {
		calls = call
		writeCompletion(w, broken)
	})
	store := newMemoryStore()
	svc := newTestService(t, srv.URL+"/v1", 5*time.Second, store)

	res, err := svc.Analyze(context.Background(), testRequest())
	if err == nil {
		t.Fatal("expected an error when the model stays invalid")
	}
	if res.Validated != nil {
		t.Fatal("an invalid answer must never surface as a recommendation")
	}
	if calls != 2 {
		t.Fatalf("expected exactly one repair attempt (2 calls), got %d", calls)
	}
	if got := store.last().Status; got != domain.InferenceInvalid {
		t.Fatalf("expected status invalid_response, got %s", got)
	}
}

func TestServiceHandlesEmptyAnswer(t *testing.T) {
	srv := mockServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		writeCompletion(w, "   ")
	})
	store := newMemoryStore()
	svc := newTestService(t, srv.URL+"/v1", 5*time.Second, store)

	_, err := svc.Analyze(context.Background(), testRequest())
	if !errors.Is(err, ErrEmptyCompletion) {
		t.Fatalf("expected ErrEmptyCompletion, got %v", err)
	}
	if got := store.last().Status; got != domain.InferenceEmpty {
		t.Fatalf("expected status empty_response, got %s", got)
	}
}

func TestServiceHandlesTimeout(t *testing.T) {
	srv := mockServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		time.Sleep(300 * time.Millisecond)
		writeCompletion(w, validMultilingual)
	})
	store := newMemoryStore()
	svc := newTestService(t, srv.URL+"/v1", 50*time.Millisecond, store)

	_, err := svc.Analyze(context.Background(), testRequest())
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if got := store.last().Status; got == domain.InferenceOK {
		t.Fatal("a timed out call must not be recorded as successful")
	}
}

func TestServiceHandlesUpstreamError(t *testing.T) {
	srv := mockServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"model not loaded"}}`))
	})
	store := newMemoryStore()
	svc := newTestService(t, srv.URL+"/v1", 5*time.Second, store)

	_, err := svc.Analyze(context.Background(), testRequest())
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("expected an upstream error, got %v", err)
	}
	if got := store.last().Status; got != domain.InferenceTransportError {
		t.Fatalf("expected transport_error, got %s", got)
	}
}

func TestDisabledLLMIsReportedNotFaked(t *testing.T) {
	cfg := config.LLMConfig{BaseURL: "http://127.0.0.1:1", Model: "m", Timeout: time.Second, MaxConcurrent: 1, Enabled: false}
	logger := logging.New("error", "text")
	store := newMemoryStore()
	svc := NewService(NewClient(cfg, logger), store, logger)

	res, err := svc.Analyze(context.Background(), testRequest())
	if !errors.Is(err, ErrDisabled) {
		t.Fatalf("expected ErrDisabled, got %v", err)
	}
	if res.Validated != nil {
		t.Fatal("a disabled LLM must never produce a recommendation")
	}
}

func TestCacheAvoidsSecondInference(t *testing.T) {
	calls := 0
	srv := mockServer(t, func(w http.ResponseWriter, _ *http.Request, call int) {
		calls = call
		writeCompletion(w, validMultilingual)
	})
	store := newMemoryStore()
	svc := newTestService(t, srv.URL+"/v1", 5*time.Second, store)

	req := testRequest()
	req.UseCache = true

	if _, err := svc.Analyze(context.Background(), req); err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	res, err := svc.Analyze(context.Background(), req)
	if err != nil {
		t.Fatalf("second call failed: %v", err)
	}
	if calls != 1 {
		t.Fatalf("the identical snapshot must be served from cache, got %d upstream calls", calls)
	}
	if res.Record.Status != domain.InferenceCached {
		t.Fatalf("expected a cached record, got %s", res.Record.Status)
	}
}

func TestConcurrencyIsCappedAtOne(t *testing.T) {
	var mu sync.Mutex
	inFlight, maxInFlight := 0, 0

	srv := mockServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		mu.Lock()
		inFlight++
		if inFlight > maxInFlight {
			maxInFlight = inFlight
		}
		mu.Unlock()

		time.Sleep(40 * time.Millisecond)

		mu.Lock()
		inFlight--
		mu.Unlock()
		writeCompletion(w, validMultilingual)
	})
	svc := newTestService(t, srv.URL+"/v1", 5*time.Second, newMemoryStore())

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = svc.Analyze(context.Background(), testRequest())
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if maxInFlight > 1 {
		t.Fatalf("LLM_MAX_CONCURRENT_REQUESTS=1 must serialise inference, saw %d in flight", maxInFlight)
	}
}

func TestPayloadFitsTokenBudget(t *testing.T) {
	snapshot := domain.FeatureSnapshot{
		SchemaVersion: domain.SchemaVersion,
		Timestamp:     time.Now().UTC(),
		Symbol:        "BTC",
		Price:         100000,
		Timeframes:    map[domain.Timeframe]domain.TimeframeAnalysis{},
		DataQuality:   domain.DataQuality{Status: domain.DataQualityOK},
	}
	// Fill every timeframe with a realistic amount of detail.
	for _, tf := range domain.AllTimeframes {
		rsi, adx, atr := 55.5, 27.3, 1.42
		analysis := domain.TimeframeAnalysis{
			Timeframe:  tf,
			Close:      100000,
			Indicators: domain.Indicators{RSI: &rsi, ADX: &adx, ATRPct: &atr, EMA: map[string]float64{"20": 1, "50": 2, "200": 3}},
			Structure:  domain.MarketStructure{State: domain.StructureBullish, Description: "bullish (HH -> HL -> HH)"},
			Regime:     domain.Regime{Primary: domain.RegimeWeakUptrend},
			Bias:       domain.PatternBullish,
		}
		for i := 0; i < 8; i++ {
			analysis.Patterns = append(analysis.Patterns, domain.Pattern{Name: "bullish_engulfing", Direction: domain.PatternBullish, Strength: 0.8})
			analysis.ChartPatterns = append(analysis.ChartPatterns, domain.Pattern{Name: "ascending_triangle", Direction: domain.PatternBullish, Strength: 0.7})
		}
		snapshot.Timeframes[tf] = analysis
	}
	for i := 0; i < 40; i++ {
		snapshot.SimilarCases = append(snapshot.SimilarCases, domain.SimilarCase{
			Similarity: 0.9, Symbol: "BTC", Recommendation: domain.RecommendationOpenLong, Confidence: 70,
			FeaturesSummary: map[string]any{"rsi": 55, "adx": 27, "regime": "weak_uptrend"},
		})
		snapshot.RecentCandles = append(snapshot.RecentCandles, domain.Candle{Open: 1, High: 2, Low: 0.5, Close: 1.5})
	}

	opts := BuildOptions{MaxTokens: 1500, TailCandles: 20, MaxSimilarCases: 5}
	_, encoded, trims, err := Build(snapshot, opts)
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	if len(trims) == 0 {
		t.Fatal("an oversized snapshot must report which parts were trimmed")
	}
	if EstimateTokens(encoded) > opts.MaxTokens {
		t.Fatalf("payload still exceeds the budget: %d tokens", EstimateTokens(encoded))
	}

	// The payload must remain complete JSON, never cut mid-structure.
	var probe map[string]any
	if err := json.Unmarshal([]byte(encoded), &probe); err != nil {
		t.Fatalf("trimmed payload is not valid JSON: %v", err)
	}
	if probe["symbol"] != "BTC" {
		t.Fatal("trimming must never drop the identifying fields")
	}
}

func TestContextBudgetReservesSystemPromptAndResponse(t *testing.T) {
	cfg := config.LLMConfig{ContextSize: 8192, MaxTokens: 1800}
	systemPrompt := SystemPrompt(PromptVersionV3, 5, 50, 15)
	opts, err := buildOptionsForContext(DefaultBuildOptions(), cfg, systemPrompt)
	if err != nil {
		t.Fatalf("build context budget: %v", err)
	}
	total := EstimateTokens(systemPrompt) + EstimateTokens(userSnapshotPrefix) + opts.MaxTokens + cfg.MaxTokens + safetyTokens(cfg.ContextSize)
	if total > cfg.ContextSize {
		t.Fatalf("budget exceeds context: %d > %d", total, cfg.ContextSize)
	}
	if opts.MaxTokens >= DefaultBuildOptions().MaxTokens {
		t.Fatalf("8k context should reduce the snapshot budget, got %d", opts.MaxTokens)
	}
}

// TestSnapshotBudgetGrowsWithTheContextWindow is the regression guard for the
// bug where a larger context still produced a trimmed snapshot: enlarging the
// window has to enlarge the snapshot the model actually sees.
func TestSnapshotBudgetGrowsWithTheContextWindow(t *testing.T) {
	systemPrompt := SystemPrompt(PromptVersionV3, 5, 50, 15)
	small, err := buildOptionsForContext(DefaultBuildOptions(),
		config.LLMConfig{ContextSize: 8192, MaxTokens: 1800}, systemPrompt)
	if err != nil {
		t.Fatalf("8k budget: %v", err)
	}
	large, err := buildOptionsForContext(DefaultBuildOptions(),
		config.LLMConfig{ContextSize: 16384, MaxTokens: 1800}, systemPrompt)
	if err != nil {
		t.Fatalf("16k budget: %v", err)
	}
	if large.MaxTokens <= small.MaxTokens {
		t.Fatalf("a doubled context must raise the snapshot budget: %d vs %d", large.MaxTokens, small.MaxTokens)
	}
	if large.MaxTokens <= DefaultBuildOptions().MaxTokens {
		t.Fatalf("a 16k window must exceed the fallback budget, got %d", large.MaxTokens)
	}

	// An explicit cap still wins, for users who prefer shorter prompts.
	capped, err := buildOptionsForContext(DefaultBuildOptions(),
		config.LLMConfig{ContextSize: 16384, MaxTokens: 1800, SnapshotMaxTokens: 3000}, systemPrompt)
	if err != nil {
		t.Fatalf("capped budget: %v", err)
	}
	if capped.MaxTokens != 3000 {
		t.Fatalf("LLM_SNAPSHOT_MAX_TOKENS must cap the budget, got %d", capped.MaxTokens)
	}
}

func TestTokenEstimateAccountsForCJK(t *testing.T) {
	if got := EstimateTokens("重要风险提示"); got < 6 {
		t.Fatalf("CJK estimate must be at least one token per rune, got %d", got)
	}
}

func TestNewsTrimmingNeverDropsCriticalEvents(t *testing.T) {
	criticalID := uuid.New()
	payload := Payload{News: domain.NewsSnapshot{
		AssetSpecific: []domain.NewsSnapshotItem{
			{ClusterID: uuid.New(), Title: "routine one"},
			{ClusterID: criticalID, Title: "critical", Critical: true},
			{ClusterID: uuid.New(), Title: "routine two"},
			{ClusterID: uuid.New(), Title: "routine three"},
			{ClusterID: uuid.New(), Title: "routine four"},
		},
	}}
	trimNonCriticalNews(&payload)
	found := false
	for _, item := range payload.News.AssetSpecific {
		if item.ClusterID == criticalID {
			found = true
		}
	}
	if !found {
		t.Fatal("critical news must survive context trimming")
	}
}
