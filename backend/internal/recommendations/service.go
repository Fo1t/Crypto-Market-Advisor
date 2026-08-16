// Package recommendations orchestrates one full analysis cycle for a symbol:
// market data -> deterministic analysis -> historical context -> LLM -> risk
// engine -> persisted advisory.
package recommendations

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/crypto-market-advisor/advisor/internal/analysis/features"
	"github.com/crypto-market-advisor/advisor/internal/analysis/strategies"
	"github.com/crypto-market-advisor/advisor/internal/config"
	"github.com/crypto-market-advisor/advisor/internal/domain"
	"github.com/crypto-market-advisor/advisor/internal/history"
	"github.com/crypto-market-advisor/advisor/internal/llm"
	"github.com/crypto-market-advisor/advisor/internal/logging"
	"github.com/crypto-market-advisor/advisor/internal/marketdata"
	newsintelligence "github.com/crypto-market-advisor/advisor/internal/news"
	"github.com/crypto-market-advisor/advisor/internal/positions"
	"github.com/crypto-market-advisor/advisor/internal/repository"
	"github.com/crypto-market-advisor/advisor/internal/risk"
)

// Status keys published to system_status.
const (
	StatusLastAnalysis  = "last_analysis"
	StatusLastInference = "last_inference"
)

// Service runs analysis cycles.
type Service struct {
	cfgMu      sync.RWMutex
	repos      *repository.Repositories
	market     *marketdata.Service
	llm        *llm.Service
	risk       *risk.Engine
	positions  *positions.Service
	history    *history.Service
	news       *newsintelligence.SnapshotBuilder
	strategies domain.StrategySet
	cfg        config.Config
	log        *slog.Logger

	// The cross-sectional ranking is one answer for the whole universe, so it is
	// computed once per cycle and shared by every symbol analysed in it.
	universeMu    sync.Mutex
	universeRanks map[string]domain.UniverseContext
	universeAt    time.Time
}

// SetNewsConfig applies UI-edited context limits to future analyses.
func (s *Service) SetNewsConfig(cfg config.NewsConfig) { s.news.SetConfig(cfg) }

// SetRiskConfig keeps LLM preflight bounds aligned with the live risk engine.
func (s *Service) SetRiskConfig(cfg config.RiskConfig) {
	s.cfgMu.Lock()
	s.cfg.Risk = cfg
	s.cfgMu.Unlock()
}

func (s *Service) riskConfig() config.RiskConfig {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg.Risk
}

// SetStrategySet applies the UI-edited deterministic policy to the next cycle.
func (s *Service) SetStrategySet(set domain.StrategySet) {
	s.cfgMu.Lock()
	s.strategies = set
	s.cfgMu.Unlock()
}

func (s *Service) strategySet() domain.StrategySet {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	if len(s.strategies.Items) == 0 {
		return strategies.DefaultSet()
	}
	return s.strategies
}

// SetAnalysisConfig applies UI-edited analysis controls, so an edited timeframe
// list reaches the next cycle without a restart.
func (s *Service) SetAnalysisConfig(cfg config.AnalysisConfig) {
	s.cfgMu.Lock()
	s.cfg.Analysis = cfg
	s.cfgMu.Unlock()
}

func (s *Service) analysisConfig() config.AnalysisConfig {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg.Analysis
}

// NewService wires the analysis pipeline.
func NewService(
	repos *repository.Repositories,
	market *marketdata.Service,
	llmSvc *llm.Service,
	riskEngine *risk.Engine,
	positionsSvc *positions.Service,
	historySvc *history.Service,
	cfg config.Config,
	logger *slog.Logger,
) *Service {
	return &Service{
		repos: repos, market: market, llm: llmSvc, risk: riskEngine,
		positions: positionsSvc, history: historySvc,
		news: newsintelligence.NewSnapshotBuilder(cfg.News, repos.News), cfg: cfg,
		log: logging.For(logger, logging.CategoryAnalysis),
	}
}

// Result is the outcome of one analysis cycle.
type Result struct {
	Run            domain.AnalysisRun
	Recommendation *domain.Recommendation
	LLMSkipped     bool
	LLMError       string
}

// Analyze runs the complete cycle for one asset.
// A failure of the LLM stage still produces (and persists) a stored analysis
// run, so the deterministic analysis remains available in the UI.
func (s *Service) Analyze(ctx context.Context, asset domain.Asset, trigger string) (*Result, error) {
	start := time.Now()

	run, snapshot, err := s.buildAnalysis(ctx, asset, trigger)
	if err != nil {
		return nil, err
	}
	run.DurationMS = int(time.Since(start).Milliseconds())

	if err := s.repos.Analysis.Insert(ctx, run); err != nil {
		return nil, fmt.Errorf("store analysis run: %w", err)
	}
	_ = s.repos.Status.Set(ctx, StatusLastAnalysis, string(domain.ComponentOnline), asset.Symbol, time.Now().UTC())

	result := &Result{Run: run}

	if !s.llm.Enabled() {
		result.LLMSkipped = true
		return result, nil
	}

	openIDs, err := s.openPositionIDs(ctx, asset.ID)
	if err != nil {
		s.log.Warn("load open positions failed", slog.String("error", err.Error()))
	}

	runID := run.ID
	riskCfg := s.riskConfig()
	preflightMaxLeverage, _ := s.risk.NewsLeverageCap(snapshot)
	inference, err := s.llm.Analyze(ctx, llm.Request{
		Snapshot:      snapshot,
		AnalysisRunID: &runID,
		MaxAllocation: riskCfg.MaxRecommendedAllocPct.InexactFloat64(),
		Validation: llm.ValidationContext{
			Symbol:           asset.Symbol,
			ReferencePrice:   snapshot.Price,
			MinLeverage:      riskCfg.MinLeverage,
			MaxLeverage:      preflightMaxLeverage,
			MaxAllocationPct: riskCfg.MaxRecommendedAllocPct.InexactFloat64(),
			OpenPositionIDs:  openIDs,
			NewsClusterIDs:   newsClusterIDs(snapshot.NewsContext),
		},
	})
	if err != nil {
		result.LLMError = err.Error()
		if !errors.Is(err, llm.ErrDisabled) {
			s.log.Warn("inference failed",
				slog.String("symbol", asset.Symbol),
				slog.String("error", err.Error()))
		}
		return result, nil
	}
	_ = s.repos.Status.Set(ctx, StatusLastInference, string(domain.ComponentOnline), asset.Symbol, time.Now().UTC())

	rec, err := s.buildRecommendation(ctx, asset, run, snapshot, inference)
	if err != nil {
		return result, err
	}
	result.Recommendation = rec
	return result, nil
}

// buildAnalysis performs the deterministic half of the cycle.
func (s *Service) buildAnalysis(ctx context.Context, asset domain.Asset, trigger string) (domain.AnalysisRun, domain.FeatureSnapshot, error) {
	analysisCfg := s.analysisConfig()
	timeframes, err := domain.ParseTimeframes(analysisCfg.Timeframes)
	if err != nil {
		return domain.AnalysisRun{}, domain.FeatureSnapshot{}, fmt.Errorf("configured timeframes: %w", err)
	}

	quality := domain.DataQuality{
		Status:        domain.DataQualityOK,
		MissingFields: []string{},
		Notes:         []string{},
	}
	analyses := make(map[domain.Timeframe]domain.TimeframeAnalysis, len(timeframes))
	var recentCandles []domain.Candle
	windows := make(map[domain.Timeframe][]domain.Candle, len(timeframes))

	for _, tf := range timeframes {
		candles, err := s.market.LoadClosedCandles(ctx, asset.ID, tf, analysisCfg.CandleHistoryLimit)
		if err != nil {
			return domain.AnalysisRun{}, domain.FeatureSnapshot{}, fmt.Errorf("load %s candles: %w", tf, err)
		}
		if len(candles) < features.MinCandles {
			quality.AddMissing(fmt.Sprintf("timeframe_%s", tf))
			continue
		}
		analyses[tf] = features.AnalyzeTimeframe(tf, candles)
		windows[tf] = candles

		if tf == domain.TF15m || (len(recentCandles) == 0 && tf == domain.TF1h) {
			recentCandles = tailCandles(candles, 30)
		}
	}
	if len(analyses) == 0 {
		quality.Status = domain.DataQualityUnusable
	}

	info, err := s.repos.Market.Latest(ctx, asset.ID)
	if err != nil {
		if !errors.Is(err, repository.ErrNotFound) {
			return domain.AnalysisRun{}, domain.FeatureSnapshot{}, fmt.Errorf("load market info: %w", err)
		}
		quality.AddMissing("market_overview")
	}
	price := info.Price
	if price <= 0 {
		price = latestClose(analyses)
		if price <= 0 {
			return domain.AnalysisRun{}, domain.FeatureSnapshot{}, errors.New("no price available for analysis")
		}
		quality.AddMissing("current_price")
	}
	if !info.FetchedAt.IsZero() && time.Since(info.FetchedAt) > 15*time.Minute {
		quality.AddNote("market overview is older than 15 minutes")
		if quality.Status == domain.DataQualityOK {
			quality.Status = domain.DataQualityDegraded
		}
	}

	positionContexts, err := s.positions.OpenContexts(ctx, asset.ID)
	if err != nil {
		s.log.Warn("load position contexts failed", slog.String("error", err.Error()))
	}

	now := time.Now().UTC()
	performance, err := s.history.Performance(ctx, asset.Symbol)
	if err != nil {
		s.log.Warn("load performance failed", slog.String("error", err.Error()))
	}

	market := s.marketContext(ctx, asset)
	universe := s.universeContext(ctx, asset)
	if !market.Known() && analysisCfg.BenchmarkSymbol != "" {
		quality.AddMissing("market_context")
	}

	snapshot := features.BuildSnapshot(features.SnapshotInput{
		Symbol:          asset.Symbol,
		Price:           price,
		Market:          info,
		Timeframes:      analyses,
		ActivePositions: positionContexts,
		Performance:     performance,
		RecentCandles:   recentCandles,
		MarketContext:   market,
		UniverseContext: universe,
		DataQuality:     quality,
		Now:             now,
	})

	vector := features.FeatureVector(snapshot)
	similar, err := s.history.SimilarCases(ctx, vector, asset.Symbol, 5, now)
	if err != nil {
		s.log.Warn("similar case lookup failed", slog.String("error", err.Error()))
	}
	snapshot.SimilarCases = similar
	newsContext, newsErr := s.news.Build(ctx, asset.ID, now)
	if newsErr != nil {
		s.log.Warn("news snapshot degraded", slog.String("error", newsErr.Error()))
	}
	snapshot.NewsContext = newsContext

	// The deterministic verdict is produced on every cycle, with or without the
	// LLM, and stored next to it so the two can be compared on one history.
	primary := primaryTimeframe(analyses, timeframes)
	decision := strategies.Evaluate(strategies.Input{
		Timeframe:          primary,
		Analysis:           analyses[primary],
		Snapshot:           snapshot,
		Candles:            windows[primary],
		Price:              price,
		Now:                now,
		CriticalNewsMaxAge: s.riskConfig().CriticalNewsMaxAge,
		RoundTripCostPct:   s.roundTripCost(),
	}, s.strategySet())

	run := domain.AnalysisRun{
		ID:                uuid.New(),
		StrategyDecision:  &decision,
		AssetID:           asset.ID,
		Symbol:            asset.Symbol,
		AnalysisTimestamp: now,
		Price:             price,
		Snapshot:          snapshot,
		FeatureVector:     vector,
		Scores:            snapshot.AggregateScores,
		Regime:            snapshot.AggregateRegime.Primary,
		DataQuality:       snapshot.DataQuality,
		TriggeredBy:       trigger,
	}
	if !snapshot.LatestClosedCandle.IsZero() {
		latest := snapshot.LatestClosedCandle
		run.LatestClosedCandle = &latest
	}
	return run, snapshot, nil
}

// roundTripCost is what one full position costs in fees and slippage under the
// exchange settings the user configured.
// marketContext reads the state of the benchmark asset, which stands for the
// market as a whole. Every symbol analysed in this cycle sees the same value.
//
// A failure here is not a failure of the analysis: an unknown context lets the
// market-wide filter through rather than blocking on missing data, so the cycle
// continues with a note in the data quality instead of an error.
func (s *Service) marketContext(ctx context.Context, asset domain.Asset) domain.MarketContext {
	symbol := s.analysisConfig().BenchmarkSymbol
	if symbol == "" {
		return domain.MarketContext{}
	}
	benchmark := asset
	if !strings.EqualFold(asset.Symbol, symbol) {
		found, err := s.repos.Assets.GetBySymbol(ctx, symbol)
		if err != nil {
			s.log.Warn("benchmark asset unavailable",
				slog.String("symbol", symbol), slog.String("error", err.Error()))
			return domain.MarketContext{}
		}
		benchmark = found
	}

	candles, err := s.market.LoadClosedCandles(ctx, benchmark.ID, domain.TF1d, features.MarketContextEMAPeriod+50)
	if err != nil {
		s.log.Warn("benchmark candles unavailable",
			slog.String("symbol", symbol), slog.String("error", err.Error()))
		return domain.MarketContext{}
	}
	return features.MarketContextFrom(benchmark.Symbol, candles)
}

// universeContext reports where this asset stands among the tracked ones.
//
// It is only computed when the cross-sectional filter is actually enabled: the
// ranking needs the daily history of every tracked asset, and a policy that
// ignores the result should not pay for it on every cycle. The answer is cached
// for the length of one analysis cycle, so twenty symbols analysed together rank
// the universe once rather than twenty times.
func (s *Service) universeContext(ctx context.Context, asset domain.Asset) domain.UniverseContext {
	if cfg, ok := s.strategySet().Find(strategies.IDRelStrengthGate); !ok || !cfg.Enabled {
		return domain.UniverseContext{}
	}

	s.universeMu.Lock()
	defer s.universeMu.Unlock()
	if time.Since(s.universeAt) < s.analysisConfig().Interval && s.universeRanks != nil {
		return s.universeRanks[asset.Symbol]
	}

	assets, err := s.repos.Assets.List(ctx, true)
	if err != nil {
		s.log.Warn("universe unavailable for ranking", slog.String("error", err.Error()))
		return domain.UniverseContext{}
	}
	scores := make(map[string]float64, len(assets))
	for _, item := range assets {
		candles, err := s.market.LoadClosedCandles(ctx, item.ID, domain.TF1d, features.RelativeStrengthLookback+5)
		if err != nil {
			continue
		}
		if score, ok := features.RelativeStrength(candles); ok {
			scores[item.Symbol] = score
		}
	}
	now := time.Now().UTC()
	s.universeRanks = features.RankUniverse(scores, now)
	s.universeAt = now
	return s.universeRanks[asset.Symbol]
}

func (s *Service) roundTripCost() float64 {
	fees := s.positions.Fees()
	taker := fees.TakerPct.InexactFloat64()
	exit := taker
	if maker := fees.MakerPct.InexactFloat64(); maker > 0 && maker < exit {
		exit = maker
	}
	return taker + exit + 2*fees.SlippagePct.InexactFloat64()
}

// primaryTimeframe picks the timeframe the deterministic decision is taken on.
//
// The order is what the history says the engine can actually trade rather than
// how often a chart updates. Replayed over the stored candles the deterministic
// policy reached a pooled profit factor of 1.16 on four-hour bars and 1.25 on
// daily ones, but only 0.90 on hourly and 0.60 on fifteen-minute bars: on the
// fast timeframes the average move it aims for no longer pays for the round
// trip. The faster series stay in the snapshot as context; they are simply not
// where the decision is taken unless nothing slower was analysed.
func primaryTimeframe(analyses map[domain.Timeframe]domain.TimeframeAnalysis, configured []domain.Timeframe) domain.Timeframe {
	for _, tf := range []domain.Timeframe{domain.TF4h, domain.TF1d, domain.TF1h, domain.TF15m, domain.TF5m, domain.TF1m} {
		if _, ok := analyses[tf]; ok {
			return tf
		}
	}
	if len(configured) > 0 {
		return configured[len(configured)-1]
	}
	return domain.TF1h
}

// buildRecommendation applies the risk engine and stores the final advisory.
func (s *Service) buildRecommendation(ctx context.Context, asset domain.Asset, run domain.AnalysisRun, snapshot domain.FeatureSnapshot, inference *llm.Result) (*domain.Recommendation, error) {
	v := inference.Validated
	v.Translations = withoutKnownVolumeLimitation(v.Translations)
	assessment := s.risk.Evaluate(risk.Input{
		Action:         v.Action,
		Confidence:     v.Confidence,
		LLMLeverage:    v.Leverage,
		LLMAllocation:  v.AllocationPct,
		LLMRiskLevel:   v.RiskLevel,
		ReferencePrice: snapshot.Price,
		StopLoss:       v.StopLoss,
		Snapshot:       snapshot,
	})
	// The row's own narrative columns are the fallback the UI uses when a
	// recommendation has no translation for the chosen language, so they hold
	// the default language rather than any particular user's.
	primary := v.Translations[domain.DefaultLanguage]
	assessment.Leverage.Reason = primary.LeverageReason

	runID := run.ID
	rec := domain.Recommendation{
		ID:              uuid.New(),
		AnalysisRunID:   &runID,
		AssetID:         asset.ID,
		Symbol:          asset.Symbol,
		CreatedAt:       time.Now().UTC(),
		Action:          v.Action,
		Confidence:      v.Confidence,
		RiskLevel:       assessment.RiskLevel,
		Summary:         primary.Summary,
		ReferencePrice:  decimal.NewFromFloat(snapshot.Price),
		AllocationPct:   assessment.AllocationPct,
		Leverage:        assessment.Leverage,
		Entry:           v.Entry,
		TakeProfit:      targetsWithReasons(v.TakeProfit, primary.TakeProfitReasons),
		StopLoss:        targetsWithReasons(v.StopLoss, primary.StopLossReasons),
		Management:      managementWithReasons(v.Management, primary.ManagementReasons),
		SignalsFor:      primary.SignalsFor,
		SignalsAgainst:  primary.SignalsAgainst,
		Invalidation:    primary.Invalidation,
		ModelName:       inference.Record.ModelName,
		PromptVersion:   inference.Record.PromptVersion,
		SchemaVersion:   inference.Record.SchemaVersion,
		MarketRegime:    snapshot.AggregateRegime.Primary,
		DataQuality:     snapshot.DataQuality.Status,
		RiskEngineNotes: assessment.Notes,
		Translations:    v.Translations,
		NewsAssessment:  v.NewsAssessment,
	}
	if rec.SignalsFor == nil {
		rec.SignalsFor = []string{}
	}
	if rec.SignalsAgainst == nil {
		rec.SignalsAgainst = []string{}
	}
	if rec.Invalidation == nil {
		rec.Invalidation = []string{}
	}
	if rec.TakeProfit == nil {
		rec.TakeProfit = []domain.PriceTarget{}
	}
	if rec.StopLoss == nil {
		rec.StopLoss = []domain.PriceTarget{}
	}

	if err := s.repos.Recommendations.Insert(ctx, rec, v); err != nil {
		return nil, fmt.Errorf("store recommendation: %w", err)
	}
	s.log.Info("recommendation stored",
		slog.String("symbol", rec.Symbol),
		slog.String("action", string(rec.Action)),
		slog.Int("confidence", rec.Confidence),
		slog.Int("llm_leverage", rec.Leverage.LLMSuggested),
		slog.Int("final_leverage", rec.Leverage.Recommended))
	return &rec, nil
}

func newsClusterIDs(snapshot domain.NewsSnapshot) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(snapshot.AssetSpecific)+len(snapshot.Global))
	seen := map[uuid.UUID]bool{}
	for _, items := range [][]domain.NewsSnapshotItem{snapshot.AssetSpecific, snapshot.Global} {
		for _, item := range items {
			if !seen[item.ClusterID] {
				ids = append(ids, item.ClusterID)
				seen[item.ClusterID] = true
			}
		}
	}
	return ids
}

func withoutKnownVolumeLimitation(in map[string]domain.RecommendationNarrative) map[string]domain.RecommendationNarrative {
	out := make(map[string]domain.RecommendationNarrative, len(in))
	for language, narrative := range in {
		filtered := make([]string, 0, len(narrative.SignalsAgainst))
		for _, signal := range narrative.SignalsAgainst {
			if isKnownVolumeLimitation(language, signal) {
				continue
			}
			filtered = append(filtered, signal)
		}
		narrative.SignalsAgainst = filtered
		out[language] = narrative
	}
	return out
}

func isKnownVolumeLimitation(language, signal string) bool {
	lower := strings.ToLower(signal)
	switch language {
	case "ru":
		return (strings.Contains(lower, "объем") || strings.Contains(lower, "объём")) &&
			(strings.Contains(lower, "нет ") || strings.Contains(lower, "недоста") ||
				strings.Contains(lower, "отсутств") || strings.Contains(lower, "недоступ"))
	case "zh-CN":
		return strings.Contains(lower, "成交量") &&
			(strings.Contains(lower, "缺") || strings.Contains(lower, "不可用") || strings.Contains(lower, "没有"))
	default:
		return strings.Contains(lower, "volume") &&
			(strings.Contains(lower, "missing") || strings.Contains(lower, "unavailable") || strings.Contains(lower, "no "))
	}
}

func targetsWithReasons(targets []domain.PriceTarget, reasons []string) []domain.PriceTarget {
	out := append([]domain.PriceTarget(nil), targets...)
	for index := range out {
		if index < len(reasons) {
			out[index].Reason = reasons[index]
		}
	}
	return out
}

func managementWithReasons(plan *domain.ManagementPlan, reasons []string) *domain.ManagementPlan {
	if plan == nil {
		return nil
	}
	out := &domain.ManagementPlan{PositionID: plan.PositionID, Actions: append([]domain.ManagementAction(nil), plan.Actions...)}
	for index := range out.Actions {
		if index < len(reasons) {
			out.Actions[index].Reason = reasons[index]
		}
	}
	return out
}

func (s *Service) openPositionIDs(ctx context.Context, assetID int64) ([]uuid.UUID, error) {
	list, err := s.repos.Positions.List(ctx, true, &assetID)
	if err != nil {
		return nil, err
	}
	out := make([]uuid.UUID, 0, len(list))
	for _, p := range list {
		out = append(out, p.ID)
	}
	return out, nil
}

func latestClose(analyses map[domain.Timeframe]domain.TimeframeAnalysis) float64 {
	for _, tf := range []domain.Timeframe{domain.TF1m, domain.TF5m, domain.TF15m, domain.TF1h, domain.TF4h, domain.TF1d} {
		if a, ok := analyses[tf]; ok && a.Close > 0 {
			return a.Close
		}
	}
	return 0
}

func tailCandles(candles []domain.Candle, n int) []domain.Candle {
	if len(candles) <= n {
		return candles
	}
	return candles[len(candles)-n:]
}
