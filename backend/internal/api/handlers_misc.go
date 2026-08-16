package api

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/crypto-market-advisor/advisor/internal/analysis/strategies"
	"github.com/crypto-market-advisor/advisor/internal/backtesting"
	"github.com/crypto-market-advisor/advisor/internal/domain"
	"github.com/crypto-market-advisor/advisor/internal/repository"
	"github.com/crypto-market-advisor/advisor/internal/settings"
)

// contextWithTimeoutBackground creates a detached context for work that must
// outlive the request, such as backfilling a newly added asset.
func contextWithTimeoutBackground(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

func (s *Server) respondWithPosition(w http.ResponseWriter, r *http.Request, ctx context.Context, id uuid.UUID) {
	view, err := s.deps.Positions.View(ctx, id)
	if err != nil {
		WriteError(w, r, s.log, notFoundOr(err, "position not found"))
		return
	}
	WriteJSON(w, http.StatusOK, view)
}

// ComponentHealth is the status of one dependency.
type ComponentHealth struct {
	Name       string                 `json:"name"`
	Status     domain.ComponentStatus `json:"status"`
	Message    string                 `json:"message,omitempty"`
	CheckedAt  time.Time              `json:"checked_at"`
	LastOK     *time.Time             `json:"last_ok,omitempty"`
	LLMContext *LLMContextHealth      `json:"llm_context,omitempty"`
}

// LLMContextHealth reports how close recent prompts came to the model context
// window. llama.cpp rejects an oversized request itself, so the UI needs the
// measured footprint to warn while there is still headroom.
type LLMContextHealth struct {
	ContextSize      int `json:"context_size"`
	MaxOutputTokens  int `json:"max_output_tokens"`
	LastPromptTokens int `json:"last_prompt_tokens"`
	PeakPromptTokens int `json:"peak_prompt_tokens"`
	// UsedPct is the peak prompt plus the reserved response over the context,
	// which is the ratio that actually has to stay below 100.
	UsedPct     float64    `json:"used_pct"`
	LastUsedPct float64    `json:"last_used_pct"`
	WarnPct     float64    `json:"warn_pct"`
	CriticalPct float64    `json:"critical_pct"`
	Level       string     `json:"level"`
	Samples     int        `json:"samples"`
	ObservedAt  *time.Time `json:"observed_at,omitempty"`
}

// HealthResponse aggregates every component plus operational timestamps.
type HealthResponse struct {
	Status     domain.ComponentStatus `json:"status"`
	Components []ComponentHealth      `json:"components"`
	Timestamps map[string]time.Time   `json:"timestamps,omitempty"`
	Scheduler  any                    `json:"scheduler,omitempty"`
	Version    string                 `json:"version"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	components := []ComponentHealth{
		s.dbHealth(ctx),
		s.marketHealth(ctx),
		s.llmHealth(ctx),
		s.newsHealth(ctx),
	}

	overall := domain.ComponentOnline
	for _, c := range components {
		// The database is the only hard dependency; the rest degrade gracefully.
		if c.Name == "database" && c.Status != domain.ComponentOnline {
			overall = domain.ComponentOffline
			break
		}
		if c.Status == domain.ComponentOffline || c.Status == domain.ComponentDegraded {
			if overall == domain.ComponentOnline {
				overall = domain.ComponentDegraded
			}
		}
	}

	response := HealthResponse{Status: overall, Components: components, Version: "1.0.0"}
	if entries, err := s.deps.Repos.Status.All(ctx); err == nil {
		response.Timestamps = map[string]time.Time{}
		for _, e := range entries {
			response.Timestamps[e.Key] = e.OccurredAt
		}
	}
	if s.deps.Scheduler != nil {
		response.Scheduler = s.deps.Scheduler.Observability()
	}

	status := http.StatusOK
	if overall == domain.ComponentOffline {
		status = http.StatusServiceUnavailable
	}
	WriteJSON(w, status, response)
}

func (s *Server) newsHealth(ctx context.Context) ComponentHealth {
	health := ComponentHealth{Name: "news", CheckedAt: time.Now().UTC()}
	newsEnabled := s.deps.Config.News.Enabled
	if s.deps.Settings != nil {
		newsEnabled = s.deps.Settings.Current().News.Enabled
	}
	if !newsEnabled {
		health.Status = domain.ComponentDisabled
		health.Message = "news intelligence is disabled"
		return health
	}
	stats, err := s.deps.Repos.News.Stats(ctx)
	if err != nil {
		health.Status = domain.ComponentOffline
		health.Message = err.Error()
		return health
	}
	if stats.LastSeenAt != nil {
		health.LastOK = stats.LastSeenAt
	}
	online := stats.SourcesByStatus[domain.NewsSourceOnline]
	switch {
	case online == 0:
		health.Status = domain.ComponentOffline
		health.Message = "no news sources are online"
	case online < stats.SourcesEnabled:
		health.Status = domain.ComponentDegraded
		health.Message = "one or more news sources are degraded or offline"
	case stats.ItemsTotal == 0:
		health.Status = domain.ComponentOnline
		health.Message = "sources are available but no items have been received"
	default:
		health.Status = domain.ComponentOnline
	}
	return health
}

func (s *Server) dbHealth(ctx context.Context) ComponentHealth {
	health := ComponentHealth{Name: "database", Status: domain.ComponentOnline, CheckedAt: time.Now().UTC()}
	if err := s.deps.DB.Ping(ctx); err != nil {
		health.Status = domain.ComponentOffline
		health.Message = err.Error()
	}
	return health
}

func (s *Server) marketHealth(ctx context.Context) ComponentHealth {
	status, message, lastOK := s.deps.Market.Health(ctx)
	health := ComponentHealth{
		Name: "market_data", Status: status, Message: message, CheckedAt: time.Now().UTC(),
	}
	if !lastOK.IsZero() {
		health.LastOK = &lastOK
	}
	return health
}

func (s *Server) llmHealth(ctx context.Context) ComponentHealth {
	health := ComponentHealth{Name: "llm", CheckedAt: time.Now().UTC()}
	client := s.deps.LLM.Client()

	if !client.Enabled() {
		health.Status = domain.ComponentDisabled
		health.Message = "llm integration is disabled"
		return health
	}
	lastOK, lastErr := client.LastState()
	if !lastOK.IsZero() {
		health.LastOK = &lastOK
	}
	if err := client.Ping(ctx); err != nil {
		health.Status = domain.ComponentOffline
		health.Message = err.Error()
		return health
	}
	health.Status = domain.ComponentOnline
	if lastErr != "" {
		health.Message = "last error: " + lastErr
	}
	health.LLMContext = s.llmContextHealth(ctx)
	return health
}

// llmContextHealth turns the measured token usage of recent inferences into a
// warning level. It returns nil when nothing has been measured yet, so the UI
// never invents a usage figure.
func (s *Server) llmContextHealth(ctx context.Context) *LLMContextHealth {
	cfg := s.deps.LLM.Client().Config()
	if cfg.ContextSize <= 0 {
		return nil
	}
	usage, err := s.deps.Repos.Recommendations.InferenceContextUsage(ctx, 50)
	if err != nil || usage.Samples == 0 {
		return nil
	}

	warn, critical := cfg.ContextWarnPct, cfg.ContextCriticalPct
	if warn <= 0 {
		warn = 75
	}
	if critical <= warn {
		critical = warn + 15
	}
	out := &LLMContextHealth{
		ContextSize:      cfg.ContextSize,
		MaxOutputTokens:  cfg.MaxTokens,
		LastPromptTokens: usage.LastPromptTokens,
		PeakPromptTokens: usage.PeakPromptTokens,
		WarnPct:          warn,
		CriticalPct:      critical,
		Samples:          usage.Samples,
		ObservedAt:       usage.LastAt,
	}
	share := func(prompt int) float64 {
		return math.Round(float64(prompt+cfg.MaxTokens)/float64(cfg.ContextSize)*1000) / 10
	}
	out.UsedPct = share(usage.PeakPromptTokens)
	out.LastUsedPct = share(usage.LastPromptTokens)

	switch {
	case out.UsedPct >= critical:
		out.Level = "critical"
	case out.UsedPct >= warn:
		out.Level = "warning"
	default:
		out.Level = "ok"
	}
	return out
}

func (s *Server) handleHealthDB(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()
	s.writeComponent(w, s.dbHealth(ctx))
}

func (s *Server) handleHealthMarketData(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()
	s.writeComponent(w, s.marketHealth(ctx))
}

func (s *Server) handleHealthLLM(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()
	s.writeComponent(w, s.llmHealth(ctx))
}

func (s *Server) writeComponent(w http.ResponseWriter, health ComponentHealth) {
	status := http.StatusOK
	if health.Status == domain.ComponentOffline {
		status = http.StatusServiceUnavailable
	}
	WriteJSON(w, status, health)
}

func (s *Server) handleStatistics(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()

	stats, err := s.deps.History.Compute(ctx, parseSince(r))
	if err != nil {
		WriteError(w, r, s.log, ErrInternal("failed to compute statistics").WithCause(err))
		return
	}
	WriteJSON(w, http.StatusOK, stats)
}

// DashboardResponse is everything the landing page needs in one request.
type DashboardResponse struct {
	Markets         []MarketDTO           `json:"markets"`
	Recommendations []RecommendationDTO   `json:"recent_recommendations"`
	Positions       []domain.PositionView `json:"open_positions"`
	Performance     any                   `json:"performance"`
	FeesConfigured  bool                  `json:"fees_configured"`
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()

	response := DashboardResponse{FeesConfigured: s.deps.Positions.Fees().Configured}

	assets, err := s.deps.Repos.Assets.List(ctx, true)
	if err != nil {
		WriteError(w, r, s.log, ErrInternal("failed to load markets").WithCause(err))
		return
	}
	prices, _ := s.deps.Repos.Market.LatestForAll(ctx)
	latestRecs, _ := s.deps.Repos.Recommendations.LatestPerAsset(ctx)

	for _, a := range assets {
		dto := assetToDTO(a)
		if info, ok := prices[a.ID]; ok {
			price, change := info.Price, info.PriceChange24hPct
			dto.Price, dto.Change24hPct = &price, &change
		}
		if rec, ok := latestRecs[a.ID]; ok {
			action, confidence, at := string(rec.Action), rec.Confidence, rec.CreatedAt
			regime := string(rec.MarketRegime)
			dto.LastAction, dto.LastConfidence, dto.LastSignalAt = &action, &confidence, &at
			if regime != "" {
				dto.Regime = &regime
			}
		}
		response.Markets = append(response.Markets, dto)
		if len(response.Markets) >= 12 {
			break
		}
	}

	recs, _, err := s.deps.Repos.Recommendations.List(ctx, repositoryListFilter(8))
	if err == nil {
		ids := make([]uuid.UUID, 0, len(recs))
		for _, rec := range recs {
			ids = append(ids, rec.ID)
		}
		decisions, outcomes := s.decisionsAndOutcomes(ctx, ids)
		for _, rec := range recs {
			response.Recommendations = append(response.Recommendations, s.decorate(rec, decisions, outcomes))
		}
	}

	if views, err := s.deps.Positions.List(ctx, true); err == nil {
		response.Positions = views
	}

	since := time.Now().UTC().AddDate(0, 0, -30)
	if stats, err := s.deps.History.Compute(ctx, &since); err == nil {
		response.Performance = stats
	}

	if response.Markets == nil {
		response.Markets = []MarketDTO{}
	}
	if response.Recommendations == nil {
		response.Recommendations = []RecommendationDTO{}
	}
	if response.Positions == nil {
		response.Positions = []domain.PositionView{}
	}
	WriteJSON(w, http.StatusOK, response)
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, http.StatusOK, s.deps.Settings.Current())
}

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var next settings.Settings
	if err := decodeJSON(r, &next); err != nil {
		WriteError(w, r, s.log, err)
		return
	}

	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	updated, err := s.deps.Settings.Update(ctx, next)
	if err != nil {
		WriteError(w, r, s.log, ErrValidation(err.Error()))
		return
	}
	WriteJSON(w, http.StatusOK, updated)
}

// StrategyDTO describes one available strategy. The UI translates it by ID and
// uses the kind to group directional opinions apart from filters.
type StrategyDTO struct {
	ID            string              `json:"id"`
	Kind          domain.StrategyKind `json:"kind"`
	DefaultWeight float64             `json:"default_weight"`
	DefaultOn     bool                `json:"default_enabled"`
	DefaultVeto   bool                `json:"default_hard_veto"`
}

func (s *Server) handleGetStrategies(w http.ResponseWriter, r *http.Request) {
	catalog := strategies.Catalog()
	out := make([]StrategyDTO, 0, len(catalog))
	for _, def := range catalog {
		out = append(out, StrategyDTO{
			ID: def.ID, Kind: def.Kind, DefaultWeight: def.Weight,
			DefaultOn: def.Enabled, DefaultVeto: def.HardVeto,
		})
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"items":              out,
		"default_min_signal": strategies.DefaultMinSignal,
		// The presets carry the figures they were measured on, so the settings
		// screen can offer a choice with evidence rather than a list of names.
		"presets": strategies.Presets(),
	})
}

// BacktestRequest starts a new run.
type BacktestRequest struct {
	Mode                 string `json:"mode"`
	Symbol               string `json:"symbol"`
	Timeframe            string `json:"timeframe"`
	DateFrom             string `json:"date_from"`
	DateTo               string `json:"date_to"`
	AnalysisInterval     string `json:"analysis_interval"`
	InitialCapital       string `json:"initial_capital"`
	AllocationPct        string `json:"allocation_pct"`
	Leverage             string `json:"leverage"`
	SlippagePct          string `json:"slippage_pct"`
	FundingRatePct       string `json:"funding_rate_pct"`
	MaintenanceMarginPct string `json:"maintenance_margin_pct"`
	MaxOpenPositions     int    `json:"max_open_positions"`
	MinConfidence        int    `json:"min_confidence"`
	InferencePauseMS     int    `json:"inference_pause_ms"`
	UseCache             bool   `json:"use_cache"`
	Confirm              bool   `json:"confirm"`

	BreakEvenAfterTP *bool                `json:"break_even_after_tp"`
	ExitMode         string               `json:"exit_mode"`
	TrailingATRMult  string               `json:"trailing_atr_mult"`
	TakeProfitLadder []domain.PnLExitStep `json:"take_profit_ladder"`
	StopLossLadder   []domain.PnLExitStep `json:"stop_loss_ladder"`
	// Strategies overrides the saved policy for this run only, which is what
	// comparing two weight profiles over one period needs.
	Strategies *domain.StrategySet `json:"strategies"`
}

// validateLadder checks one side of a return-on-margin exit ladder.
func validateLadder(steps []domain.PnLExitStep, name string) error {
	total := 0.0
	for i, step := range steps {
		if step.PnLPct <= 0 || step.PnLPct > 10_000 {
			return ErrValidation(fmt.Sprintf("%s[%d].pnl_pct must be between 0 and 10000", name, i))
		}
		if step.ClosePct <= 0 || step.ClosePct > 100 {
			return ErrValidation(fmt.Sprintf("%s[%d].close_pct must be between 0 and 100", name, i))
		}
		total += step.ClosePct
	}
	if total > 100.0001 {
		return ErrValidation(fmt.Sprintf("%s closes %.0f%% of the position in total, which is more than it holds", name, total))
	}
	return nil
}

func (s *Server) parseBacktest(req BacktestRequest) (domain.BacktestParams, error) {
	params := domain.BacktestParams{
		Mode:             domain.BacktestMode(strings.ToLower(defaultString(req.Mode, "technical"))),
		Symbol:           strings.ToUpper(strings.TrimSpace(req.Symbol)),
		AnalysisInterval: strings.TrimSpace(req.AnalysisInterval),
		MinConfidence:    req.MinConfidence,
		UseCache:         req.UseCache,
		Confirm:          req.Confirm,
	}
	if params.Mode != domain.BacktestTechnical && params.Mode != domain.BacktestLLM {
		return params, ErrValidation("mode must be technical or llm")
	}

	tf, err := domain.ParseTimeframe(defaultString(req.Timeframe, "1h"))
	if err != nil {
		return params, ErrValidation(err.Error())
	}
	params.Timeframe = tf

	from, err := time.Parse(time.RFC3339, req.DateFrom)
	if err != nil {
		return params, ErrValidation("date_from must be an RFC3339 timestamp")
	}
	to, err := time.Parse(time.RFC3339, req.DateTo)
	if err != nil {
		return params, ErrValidation("date_to must be an RFC3339 timestamp")
	}
	if !to.After(from) {
		return params, ErrValidation("date_to must be after date_from")
	}
	params.DateFrom, params.DateTo = from.UTC(), to.UTC()

	fees := s.deps.Positions.Fees()
	params.MakerFeePct, params.TakerFeePct = fees.MakerPct, fees.TakerPct
	params.SlippagePct = s.deps.Config.Backtest.DefaultSlippage
	params.FundingRatePct = s.deps.Config.Backtest.DefaultFundingRate
	params.MaintenanceMarginPct = s.deps.Config.Backtest.DefaultMaintenanceMargin
	params.MaxOpenPositions = s.deps.Config.Backtest.DefaultMaxOpenPositions
	params.InferencePause = s.deps.Config.Backtest.DefaultInferencePause
	if params.MaxOpenPositions <= 0 {
		params.MaxOpenPositions = 1
	}
	params.InitialCapital = decimal.NewFromInt(10000)
	params.AllocationPct = decimal.NewFromInt(5)
	params.Leverage = decimal.NewFromInt(int64(s.deps.Config.Risk.MinLeverage))

	for _, field := range []struct {
		raw           string
		dst           *decimal.Decimal
		name          string
		allowNegative bool
	}{
		{req.InitialCapital, &params.InitialCapital, "initial_capital", false},
		{req.AllocationPct, &params.AllocationPct, "allocation_pct", false},
		{req.Leverage, &params.Leverage, "leverage", false},
		{req.SlippagePct, &params.SlippagePct, "slippage_pct", false},
		{req.FundingRatePct, &params.FundingRatePct, "funding_rate_pct", true},
		{req.MaintenanceMarginPct, &params.MaintenanceMarginPct, "maintenance_margin_pct", false},
	} {
		if strings.TrimSpace(field.raw) == "" {
			continue
		}
		value, err := decimal.NewFromString(strings.TrimSpace(field.raw))
		if err != nil || (!field.allowNegative && value.IsNegative()) {
			return params, ErrValidation(field.name + " must be a non-negative number")
		}
		*field.dst = value
	}
	if params.AllocationPct.GreaterThan(decimal.NewFromInt(100)) {
		return params, ErrValidation("allocation_pct must not exceed 100")
	}
	if params.MaintenanceMarginPct.GreaterThanOrEqual(decimal.NewFromInt(100)) {
		return params, ErrValidation("maintenance_margin_pct must be below 100")
	}
	if params.FundingRatePct.Abs().GreaterThan(decimal.NewFromInt(5)) {
		return params, ErrValidation("funding_rate_pct must be between -5 and 5")
	}
	if req.MaxOpenPositions > 0 {
		params.MaxOpenPositions = req.MaxOpenPositions
	}
	if req.InferencePauseMS != 0 {
		// A pause only lengthens the run, so the useful bound is generous: five
		// seconds between requests is already a very cautious duty cycle.
		if req.InferencePauseMS < 0 || req.InferencePauseMS > 60_000 {
			return params, ErrValidation("inference_pause_ms must be between 0 and 60000")
		}
		params.InferencePause = time.Duration(req.InferencePauseMS) * time.Millisecond
	}
	if params.MaxOpenPositions < 1 || params.MaxOpenPositions > 20 {
		return params, ErrValidation("max_open_positions must be between 1 and 20")
	}

	// The deterministic policy is snapshotted into the run, so its result stays
	// reproducible after the settings change. A request may override it for this
	// run alone, without touching what the live analysis uses.
	switch {
	case req.Strategies != nil:
		if err := strategies.Validate(*req.Strategies); err != nil {
			return params, ErrValidation(err.Error())
		}
		set := strategies.Normalize(*req.Strategies)
		params.Strategies = &set
	case s.deps.Settings != nil:
		set := s.deps.Settings.StrategySet()
		params.Strategies = &set
	}

	// Off by default: protecting a partially banked trade sounds prudent, but on
	// the stored runs it consistently cost more than it saved, because the stop
	// also removed the trades that went on to the second target.
	if req.BreakEvenAfterTP != nil {
		params.BreakEvenAfterTP = *req.BreakEvenAfterTP
	}

	// A new run waits for a pullback and rests a limit order rather than paying
	// the spread and the taker fee at the market. Both halves of that are worth
	// something, and the measurements are in DefaultEntryPullbackATR.
	params.EntryPullbackATR = decimal.NewFromFloat(backtesting.DefaultEntryPullbackATR)
	params.EntryValidBars = backtesting.DefaultEntryValidBars

	// A new run trails by default. Across four years of daily bars and three
	// windows of four-hour bars the Chandelier exit beat every fixed-target
	// geometry tested - 1.25 against 1.06-1.11 on daily bars, 1.16 against
	// 1.03-1.08 on four-hour bars - and it was the least sensitive to its own
	// parameter: every multiplier between 2 and 5 landed within a few percent.
	params.ExitMode = domain.BacktestExitMode(strings.ToLower(defaultString(req.ExitMode, string(domain.ExitModeTrailingATR))))
	if !params.ExitMode.Valid() {
		return params, ErrValidation("exit_mode must be signal, pnl_ladder or trailing_atr")
	}
	if params.ExitMode == domain.ExitModeTrailingATR {
		params.TrailingATRMult = decimal.NewFromFloat(backtesting.DefaultTrailingATRMult)
		if raw := strings.TrimSpace(req.TrailingATRMult); raw != "" {
			value, err := decimal.NewFromString(raw)
			if err != nil || value.LessThanOrEqual(decimal.Zero) || value.GreaterThan(decimal.NewFromInt(20)) {
				return params, ErrValidation("trailing_atr_mult must be between 0 and 20")
			}
			params.TrailingATRMult = value
		}
	}
	if params.ExitMode == domain.ExitModePnLLadder {
		params.TakeProfitLadder, params.StopLossLadder = req.TakeProfitLadder, req.StopLossLadder
		if len(params.TakeProfitLadder)+len(params.StopLossLadder) == 0 {
			return params, ErrValidation("a pnl_ladder run needs at least one take-profit or stop-loss step")
		}
		if err := validateLadder(params.TakeProfitLadder, "take_profit_ladder"); err != nil {
			return params, err
		}
		if err := validateLadder(params.StopLossLadder, "stop_loss_ladder"); err != nil {
			return params, err
		}
	}
	return params, nil
}

func (s *Server) handleEstimateBacktest(w http.ResponseWriter, r *http.Request) {
	var req BacktestRequest
	if err := decodeJSON(r, &req); err != nil {
		WriteError(w, r, s.log, err)
		return
	}
	params, err := s.parseBacktest(req)
	if err != nil {
		WriteError(w, r, s.log, err)
		return
	}
	steps := backtesting.EstimateSteps(params)

	// The estimate counts the requested range. What the replay can actually see
	// is bounded by the stored candles, and telling the user that before the run
	// costs one query and saves a wasted test.
	response := map[string]any{
		"estimated_inference_count": steps,
		"inference_pause_ms":        int(params.InferencePause / time.Millisecond),
		"mode":                      params.Mode,
		"requires_confirmation":     params.Mode == domain.BacktestLLM,
		"max_inferences":            s.deps.Config.Backtest.MaxInferences,
	}
	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()
	if asset, err := s.deps.Repos.Assets.GetBySymbol(ctx, params.Symbol); err == nil {
		if coverage, err := s.deps.Repos.Candles.Coverage(ctx, asset.ID, params.Timeframe); err == nil {
			response["coverage"] = coverage
		}
	}
	WriteJSON(w, http.StatusOK, response)
}

func (s *Server) handleCreateBacktest(w http.ResponseWriter, r *http.Request) {
	var req BacktestRequest
	if err := decodeJSON(r, &req); err != nil {
		WriteError(w, r, s.log, err)
		return
	}
	params, err := s.parseBacktest(req)
	if err != nil {
		WriteError(w, r, s.log, err)
		return
	}

	ctx, cancel := contextWithTimeout(r, 20*time.Second)
	defer cancel()

	asset, err := s.deps.Repos.Assets.GetBySymbol(ctx, params.Symbol)
	if err != nil {
		WriteError(w, r, s.log, notFoundOr(err, "unknown symbol"))
		return
	}

	steps := backtesting.EstimateSteps(params)
	if params.Mode == domain.BacktestLLM {
		if !params.Confirm {
			WriteError(w, r, s.log, ErrValidation("an LLM backtest needs explicit confirmation; call /api/backtests/estimate first"))
			return
		}
		if steps > s.deps.Config.Backtest.MaxInferences {
			WriteError(w, r, s.log, ErrValidation("estimated inference count exceeds BACKTEST_MAX_INFERENCES"))
			return
		}
		if !s.deps.LLM.Enabled() {
			WriteError(w, r, s.log, ErrUpstream("the LLM is disabled, an LLM backtest cannot run"))
			return
		}
	}

	run := domain.BacktestRun{
		ID: uuid.New(), Mode: params.Mode, Symbol: asset.Symbol, AssetID: &asset.ID,
		Timeframe: params.Timeframe, DateFrom: params.DateFrom, DateTo: params.DateTo,
		Interval: params.AnalysisInterval, Status: domain.BacktestPending,
		Params: params, EstimatedSteps: steps, CreatedAt: time.Now().UTC(),
	}
	if err := s.deps.Repos.Backtests.Create(ctx, run); err != nil {
		WriteError(w, r, s.log, ErrInternal("failed to create the backtest run").WithCause(err))
		return
	}

	// Runs are executed in the background; the client polls the run endpoint.
	// The ceiling has to survive the slowest run the limits still allow: an LLM
	// replay of BACKTEST_MAX_INFERENCES steps at several seconds each runs for
	// hours, and a timeout mid-replay discards every inference it had paid for.
	go func() {
		bg, cancel := contextWithTimeoutBackground(s.deps.Config.Backtest.RunTimeout)
		defer cancel()
		if err := s.deps.Backtests.Run(bg, run, asset); err != nil {
			s.log.Warn("backtest failed", "id", run.ID.String(), "error", err)
		}
	}()

	WriteJSON(w, http.StatusAccepted, map[string]any{
		"id":              run.ID.String(),
		"status":          run.Status,
		"estimated_steps": steps,
	})
}

func (s *Server) handleListBacktests(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	filter, err := backtestFilter(r)
	if err != nil {
		WriteError(w, r, s.log, err)
		return
	}
	limit, offset := pagination(r, 25)
	runs, total, err := s.deps.Repos.Backtests.List(ctx, filter, limit, offset)
	if err != nil {
		WriteError(w, r, s.log, ErrInternal("failed to list backtests").WithCause(err))
		return
	}
	if runs == nil {
		runs = []domain.BacktestRun{}
	}
	WriteJSON(w, http.StatusOK, Page[domain.BacktestRun]{Items: runs, Total: total, Limit: limit, Offset: offset})
}

// backtestFilter reads the optional narrowing of the run list. Unknown values
// are rejected rather than ignored: a filter that silently matches everything is
// indistinguishable from a filter that works, and the bulk hide uses the same
// one.
func backtestFilter(r *http.Request) (repository.BacktestFilter, error) {
	query := r.URL.Query()
	filter := repository.BacktestFilter{
		Mode:      strings.ToLower(strings.TrimSpace(query.Get("mode"))),
		Symbol:    strings.ToUpper(strings.TrimSpace(query.Get("symbol"))),
		Status:    strings.ToLower(strings.TrimSpace(query.Get("status"))),
		Timeframe: strings.TrimSpace(query.Get("timeframe")),
	}
	if filter.Mode != "" && domain.BacktestMode(filter.Mode) != domain.BacktestTechnical &&
		domain.BacktestMode(filter.Mode) != domain.BacktestLLM {
		return filter, ErrValidation("mode must be technical or llm")
	}
	if filter.Timeframe != "" {
		if _, err := domain.ParseTimeframe(filter.Timeframe); err != nil {
			return filter, ErrValidation(err.Error())
		}
	}
	switch domain.BacktestStatus(filter.Status) {
	case "", domain.BacktestPending, domain.BacktestRunning, domain.BacktestCompleted,
		domain.BacktestFailed, domain.BacktestCanceled:
	default:
		return filter, ErrValidation("unknown backtest status " + filter.Status)
	}
	return filter, nil
}

// handleHideBacktests hides every finished run the current filter selects, which
// is how a list that has accumulated dozens of experiments is cleared without
// losing any of them: the rows stay in the database with their trades.
func (s *Server) handleHideBacktests(w http.ResponseWriter, r *http.Request) {
	filter, err := backtestFilter(r)
	if err != nil {
		WriteError(w, r, s.log, err)
		return
	}
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	hidden, err := s.deps.Repos.Backtests.SoftDeleteMatching(ctx, filter)
	if err != nil {
		WriteError(w, r, s.log, ErrInternal("failed to hide backtests").WithCause(err))
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"hidden": hidden})
}

// handleBacktestHidden reports what a purge would remove.
func (s *Server) handleBacktestHidden(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	stats, err := s.deps.Repos.Backtests.HiddenSummary(ctx)
	if err != nil {
		WriteError(w, r, s.log, ErrInternal("failed to summarise hidden backtests").WithCause(err))
		return
	}
	WriteJSON(w, http.StatusOK, stats)
}

// handlePurgeBacktests permanently removes the hidden runs and their trades.
func (s *Server) handlePurgeBacktests(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 60*time.Second)
	defer cancel()

	removed, err := s.deps.Repos.Backtests.Purge(ctx)
	if err != nil {
		WriteError(w, r, s.log, ErrInternal("failed to purge backtests").WithCause(err))
		return
	}
	s.log.Info("hidden backtests purged", slog.Int("runs", removed))
	WriteJSON(w, http.StatusOK, map[string]any{"removed": removed})
}

func (s *Server) handleGetBacktest(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(r, "id")
	if err != nil {
		WriteError(w, r, s.log, err)
		return
	}
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	run, err := s.deps.Repos.Backtests.Get(ctx, id)
	if err != nil {
		WriteError(w, r, s.log, notFoundOr(err, "backtest not found"))
		return
	}
	trades, err := s.deps.Repos.Backtests.Trades(ctx, id)
	if err != nil {
		WriteError(w, r, s.log, ErrInternal("failed to load backtest trades").WithCause(err))
		return
	}
	if trades == nil {
		trades = []domain.BacktestTrade{}
	}
	// The equity curve is only loaded for a single run: the listing does not
	// need it and it is by far the largest part of the payload.
	curve, err := s.deps.Repos.Backtests.EquityCurve(ctx, id)
	if err != nil {
		WriteError(w, r, s.log, ErrInternal("failed to load the equity curve").WithCause(err))
		return
	}
	if curve == nil {
		curve = []domain.EquityPoint{}
	}
	WriteJSON(w, http.StatusOK, map[string]any{"run": run, "trades": trades, "equity_curve": curve})
}

func (s *Server) handleDeleteBacktest(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(r, "id")
	if err != nil {
		WriteError(w, r, s.log, err)
		return
	}
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	run, err := s.deps.Repos.Backtests.Get(ctx, id)
	if err != nil {
		WriteError(w, r, s.log, notFoundOr(err, "backtest not found"))
		return
	}
	if run.Status == domain.BacktestPending || run.Status == domain.BacktestRunning {
		WriteError(w, r, s.log, ErrConflict("cancel the running backtest before deleting its result"))
		return
	}
	if err := s.deps.Repos.Backtests.SoftDelete(ctx, id); err != nil {
		WriteError(w, r, s.log, notFoundOr(err, "backtest not found"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCancelBacktest(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(r, "id")
	if err != nil {
		WriteError(w, r, s.log, err)
		return
	}
	if !s.deps.Backtests.Cancel(id) {
		WriteError(w, r, s.log, ErrNotFound("no running backtest with this id"))
		return
	}
	WriteJSON(w, http.StatusOK, map[string]string{"status": "canceling"})
}
