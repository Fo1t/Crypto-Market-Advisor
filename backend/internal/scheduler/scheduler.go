// Package scheduler runs the background work: market data ingestion, the
// periodic analysis cycle, universe refresh and outcome evaluation.
//
// Everything here runs inside the Go process. The browser is never the source
// of scheduling, and no external job broker is involved: for a local
// single-user tool a goroutine with a ticker is the right amount of machinery.
package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/crypto-market-advisor/advisor/internal/config"
	"github.com/crypto-market-advisor/advisor/internal/domain"
	"github.com/crypto-market-advisor/advisor/internal/logging"
	"github.com/crypto-market-advisor/advisor/internal/marketdata"
	"github.com/crypto-market-advisor/advisor/internal/news"
	"github.com/crypto-market-advisor/advisor/internal/recommendations"
	"github.com/crypto-market-advisor/advisor/internal/repository"
)

// ErrCooldown is returned when a manual analysis is requested too soon.
var ErrCooldown = errors.New("analysis for this symbol was requested too recently")

// Status keys published to system_status.
const (
	StatusNextAnalysis = "next_analysis"
	StatusScheduler    = "scheduler"
)

// Scheduler owns all background loops.
type Scheduler struct {
	cfg       config.Config
	repos     *repository.Repositories
	market    *marketdata.Service
	analysis  *recommendations.Service
	news      *news.Collector
	reactions *news.ReactionTracker
	log       *slog.Logger

	sem *semaphore.Weighted

	mu           sync.Mutex
	newsMu       sync.RWMutex
	newsCfg      config.NewsConfig
	analysisMu   sync.RWMutex
	analysisCfg  config.AnalysisConfig
	lastManual   map[string]time.Time
	lastCycle    time.Time
	nextCycle    time.Time
	cycleRunning bool
}

// New builds the scheduler.
func New(cfg config.Config, repos *repository.Repositories, market *marketdata.Service, analysis *recommendations.Service, newsCollector *news.Collector, reactionTracker *news.ReactionTracker, logger *slog.Logger) *Scheduler {
	workers := cfg.Analysis.MaxConcurrentSymbols
	if workers < 1 {
		workers = 1
	}
	return &Scheduler{
		cfg: cfg, repos: repos, market: market, analysis: analysis, news: newsCollector, reactions: reactionTracker,
		newsCfg:     cfg.News,
		analysisCfg: cfg.Analysis,
		log:         logging.For(logger, logging.CategoryScheduler),
		sem:         semaphore.NewWeighted(int64(workers)),
		lastManual:  map[string]time.Time{},
	}
}

// SetNewsConfig applies UI-edited news controls. The loops remain alive while
// disabled so collection can later be enabled without restarting the process.
func (s *Scheduler) SetNewsConfig(cfg config.NewsConfig) {
	s.newsMu.Lock()
	s.newsCfg = cfg
	s.newsMu.Unlock()
	if s.news != nil {
		s.news.SetConfig(cfg)
	}
	if s.reactions != nil {
		s.reactions.SetConfig(cfg)
	}
}

func (s *Scheduler) currentNewsConfig() config.NewsConfig {
	s.newsMu.RLock()
	defer s.newsMu.RUnlock()
	return s.newsCfg
}

// SetAnalysisConfig applies the UI-edited analysis controls. Like the news
// loops, the analysis loops stay alive while disabled: the "background
// analysis" switch has to take effect in both directions without a restart.
func (s *Scheduler) SetAnalysisConfig(cfg config.AnalysisConfig) {
	s.analysisMu.Lock()
	s.analysisCfg = cfg
	s.analysisMu.Unlock()
}

func (s *Scheduler) currentAnalysisConfig() config.AnalysisConfig {
	s.analysisMu.RLock()
	defer s.analysisMu.RUnlock()
	return s.analysisCfg
}

// analysisEnabled reports whether background analysis is currently switched on.
func (s *Scheduler) analysisEnabled() bool { return s.currentAnalysisConfig().Enabled }

// Run starts every loop and blocks until the context is cancelled.
func (s *Scheduler) Run(ctx context.Context) {
	s.log.Info("scheduler starting",
		slog.Duration("analysis_interval", s.cfg.Analysis.Interval),
		slog.Duration("market_data_interval", s.cfg.Analysis.MarketDataInterval),
		slog.Duration("news_interval", s.cfg.News.FetchInterval))

	var wg sync.WaitGroup
	type loopSpec struct {
		name string
		fn   func(context.Context)
	}
	loops := make([]loopSpec, 0, 8)
	loops = append(loops,
		loopSpec{"bootstrap", s.bootstrap},
		loopSpec{"market_data", s.marketDataLoop},
		loopSpec{"analysis", s.analysisLoop},
		loopSpec{"universe", s.universeLoop},
		loopSpec{"outcomes", s.outcomeLoop},
		loopSpec{"maintenance", s.maintenanceLoop},
	)
	if !s.analysisEnabled() {
		s.log.Warn("background analysis is switched off; market data and news keep running, no analysis cycle is started")
	}
	if s.news != nil {
		loops = append(loops, loopSpec{"news", s.newsLoop})
	}
	if s.reactions != nil {
		loops = append(loops, loopSpec{"news_reactions", s.newsReactionLoop})
	}
	for _, loop := range loops {
		wg.Add(1)
		go func(name string, fn func(context.Context)) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					s.log.Error("scheduler loop panicked", slog.String("loop", name), slog.Any("panic", r))
				}
			}()
			fn(ctx)
		}(loop.name, loop.fn)
	}

	wg.Wait()
	s.log.Info("scheduler stopped")
}

func (s *Scheduler) newsReactionLoop(ctx context.Context) {
	for {
		cfg := s.currentNewsConfig()
		if cfg.Enabled {
			s.evaluateNewsReactions(ctx)
		}
		interval := cfg.ReactionInterval
		if interval < time.Minute {
			interval = time.Minute
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *Scheduler) evaluateNewsReactions(ctx context.Context) {
	stats, err := s.reactions.ProcessDue(ctx, 500)
	if err != nil {
		s.log.Warn("news reaction evaluation completed with failures",
			slog.Int("due", stats.Due), slog.Int("updated", stats.Updated),
			slog.String("error", err.Error()))
		return
	}
	if stats.Due > 0 {
		s.log.Info("news reactions evaluated",
			slog.Int("due", stats.Due), slog.Int("updated", stats.Updated),
			slog.Int("completed", stats.Completed),
			slog.Int("insufficient_data", stats.InsufficientData))
	}
}

func (s *Scheduler) newsLoop(ctx context.Context) {
	for {
		cfg := s.currentNewsConfig()
		if cfg.Enabled {
			s.collectNews(ctx)
		}
		interval := cfg.FetchInterval
		if interval < time.Minute {
			interval = time.Minute
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *Scheduler) collectNews(ctx context.Context) {
	stats, err := s.news.Collect(ctx)
	if err != nil {
		s.log.Warn("news collection completed with source failures",
			slog.Int("sources_succeeded", stats.SourcesSucceeded),
			slog.Int("sources_failed", stats.SourcesFailed),
			slog.Int("items_inserted", stats.ItemsInserted),
			slog.String("error", err.Error()))
		return
	}
	s.log.Info("news collection finished",
		slog.Int("sources", stats.SourcesSucceeded),
		slog.Int("items_received", stats.ItemsReceived),
		slog.Int("items_inserted", stats.ItemsInserted),
		slog.Int("items_existing", stats.ItemsExisting),
		slog.Int("items_clustered", stats.ItemsClustered),
		slog.Int("clusters_created", stats.ClustersCreated))
}

// bootstrap performs the first-run work: universe, market data, backfill.
func (s *Scheduler) bootstrap(ctx context.Context) {
	count, err := s.repos.Assets.Count(ctx)
	if err != nil {
		s.log.Error("count assets failed", slog.String("error", err.Error()))
		return
	}
	if count == 0 {
		s.log.Info("empty universe, fetching the default top list")
		if err := s.market.RefreshUniverse(ctx); err != nil {
			s.log.Error("initial universe refresh failed", slog.String("error", err.Error()))
		}
	}

	if err := s.market.IngestPrices(ctx); err != nil {
		s.log.Warn("initial price ingest failed", slog.String("error", err.Error()))
	}
	s.backfillAll(ctx)
}

// backfillAll rebuilds candle history for every enabled asset.
func (s *Scheduler) backfillAll(ctx context.Context) {
	assets, err := s.repos.Assets.List(ctx, true)
	if err != nil {
		s.log.Error("list assets failed", slog.String("error", err.Error()))
		return
	}
	for _, asset := range assets {
		if ctx.Err() != nil {
			return
		}
		if err := s.market.BackfillCandles(ctx, asset); err != nil {
			s.log.Warn("backfill failed",
				slog.String("symbol", asset.Symbol),
				slog.String("error", err.Error()))
		}
		// Funding is one public row every eight hours per asset, so it rides
		// along with the candle backfill instead of needing a loop of its own.
		if err := s.market.BackfillFunding(ctx, asset); err != nil {
			s.log.Warn("funding backfill failed",
				slog.String("symbol", asset.Symbol),
				slog.String("error", err.Error()))
		}
	}
	s.log.Info("candle backfill finished", slog.Int("assets", len(assets)))
}

// marketDataLoop refreshes prices on its own cadence, faster than analysis.
func (s *Scheduler) marketDataLoop(ctx context.Context) {
	ticker := time.NewTicker(s.cfg.Analysis.MarketDataInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.market.IngestPrices(ctx); err != nil {
				s.log.Warn("price ingest failed", slog.String("error", err.Error()))
			}
		}
	}
}

// analysisLoop aligns the analysis cycle with candle closes.
func (s *Scheduler) analysisLoop(ctx context.Context) {
	for {
		// Both the interval and the on/off switch are read fresh every pass, so
		// edits in the settings screen apply from the next boundary onward.
		interval := s.currentAnalysisConfig().Interval
		wait := untilNextBoundary(time.Now().UTC(), interval)
		s.setNext(time.Now().UTC().Add(wait))

		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}

		// A short grace period lets the just-closed candle land in storage
		// before the analysis reads it.
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}

		// The switch is honoured here rather than by starting or not starting
		// the loop, so turning it off stops the next cycle immediately and
		// turning it back on needs no restart. Market data, news and outcome
		// bookkeeping deliberately keep running either way.
		if !s.analysisEnabled() {
			continue
		}
		s.RunCycle(ctx)
	}
}

// untilNextBoundary returns the delay until the next interval boundary, so a
// 5-minute cycle fires at :00, :05, :10 rather than at an arbitrary offset.
func untilNextBoundary(now time.Time, interval time.Duration) time.Duration {
	if interval <= 0 {
		return time.Minute
	}
	next := now.Truncate(interval).Add(interval)
	return next.Sub(now)
}

// RunCycle analyses every enabled asset once, in priority order.
func (s *Scheduler) RunCycle(ctx context.Context) {
	s.mu.Lock()
	if s.cycleRunning {
		s.mu.Unlock()
		s.log.Warn("skipping cycle: the previous one is still running")
		return
	}
	s.cycleRunning = true
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.cycleRunning = false
		s.lastCycle = time.Now().UTC()
		s.mu.Unlock()
	}()

	assets, err := s.prioritise(ctx)
	if err != nil {
		s.log.Error("prioritise assets failed", slog.String("error", err.Error()))
		return
	}
	if len(assets) == 0 {
		return
	}

	start := time.Now()
	var wg sync.WaitGroup

	for _, asset := range assets {
		if ctx.Err() != nil {
			break
		}
		if err := s.sem.Acquire(ctx, 1); err != nil {
			break
		}
		wg.Add(1)
		go func(a domain.Asset) {
			defer wg.Done()
			defer s.sem.Release(1)

			if _, err := s.analysis.Analyze(ctx, a, "scheduler"); err != nil {
				s.log.Warn("analysis failed", slog.String("symbol", a.Symbol), slog.String("error", err.Error()))
			}
		}(asset)
	}
	wg.Wait()

	s.log.Info("analysis cycle finished",
		slog.Int("assets", len(assets)),
		slog.Duration("duration", time.Since(start)))
	_ = s.repos.Status.Set(ctx, StatusScheduler, string(domain.ComponentOnline), "cycle finished", time.Now().UTC())
}

// prioritise orders assets so that the scarce LLM capacity goes to what matters:
// symbols with open positions first, then recent strong signals, then the rest.
func (s *Scheduler) prioritise(ctx context.Context) ([]domain.Asset, error) {
	assets, err := s.repos.Assets.List(ctx, true)
	if err != nil {
		return nil, err
	}
	positions, err := s.repos.Positions.List(ctx, true, nil)
	if err != nil {
		return nil, err
	}
	withPosition := make(map[int64]bool, len(positions))
	for _, p := range positions {
		withPosition[p.AssetID] = true
	}

	latest, err := s.repos.Recommendations.LatestPerAsset(ctx)
	if err != nil {
		s.log.Warn("load latest recommendations failed", slog.String("error", err.Error()))
	}

	var priority1, priority2, priority3 []domain.Asset
	for _, a := range assets {
		switch {
		case withPosition[a.ID]:
			priority1 = append(priority1, a)
		case isStrongSignal(latest[a.ID]):
			priority2 = append(priority2, a)
		default:
			priority3 = append(priority3, a)
		}
	}
	return append(append(priority1, priority2...), priority3...), nil
}

func isStrongSignal(rec domain.Recommendation) bool {
	if rec.ID.String() == "00000000-0000-0000-0000-000000000000" {
		return false
	}
	if time.Since(rec.CreatedAt) > 2*time.Hour {
		return false
	}
	return rec.Action.IsEntry() && rec.Confidence >= 65
}

// universeLoop refreshes the tracked asset list once a day.
func (s *Scheduler) universeLoop(ctx context.Context) {
	ticker := time.NewTicker(s.cfg.Analysis.UniverseRefresh)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.market.RefreshUniverse(ctx); err != nil {
				s.log.Warn("universe refresh failed", slog.String("error", err.Error()))
				continue
			}
			s.backfillAll(ctx)
		}
	}
}

// outcomeLoop keeps recommendation outcomes up to date.
func (s *Scheduler) outcomeLoop(ctx context.Context) {
	ticker := time.NewTicker(s.cfg.Analysis.OutcomeInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			updated, err := s.analysis.EvaluateOutcomes(ctx, 200)
			if err != nil {
				s.log.Warn("outcome evaluation failed", slog.String("error", err.Error()))
				continue
			}
			if updated > 0 {
				s.log.Info("outcomes updated", slog.Int("count", updated))
			}
		}
	}
}

// maintenanceLoop prunes old rows and refreshes deep candle history.
func (s *Scheduler) maintenanceLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.market.Prune(ctx); err != nil {
				s.log.Warn("prune failed", slog.String("error", err.Error()))
			}
			s.backfillAll(ctx)
		}
	}
}

// AnalyzeNow runs an on-demand analysis for one asset, rate limited per symbol.
func (s *Scheduler) AnalyzeNow(ctx context.Context, asset domain.Asset) (*recommendations.Result, error) {
	s.mu.Lock()
	last, ok := s.lastManual[asset.Symbol]
	if ok && time.Since(last) < s.cfg.Analysis.ManualCooldown {
		s.mu.Unlock()
		return nil, ErrCooldown
	}
	s.lastManual[asset.Symbol] = time.Now()
	s.mu.Unlock()

	if err := s.sem.Acquire(ctx, 1); err != nil {
		return nil, err
	}
	defer s.sem.Release(1)

	return s.analysis.Analyze(ctx, asset, "manual")
}

// Backfill exposes a manual candle backfill for a newly added asset.
func (s *Scheduler) Backfill(ctx context.Context, asset domain.Asset) error {
	if err := s.market.BackfillCandles(ctx, asset); err != nil {
		return err
	}
	if err := s.market.BackfillFunding(ctx, asset); err != nil {
		s.log.Warn("funding backfill failed",
			slog.String("symbol", asset.Symbol), slog.String("error", err.Error()))
	}
	return nil
}

// Observability reports the scheduler timings shown in the UI.
type Observability struct {
	LastCycle   *time.Time `json:"last_analysis_cycle,omitempty"`
	NextCycle   *time.Time `json:"next_analysis_cycle,omitempty"`
	CycleActive bool       `json:"cycle_running"`
}

// Observability returns the current scheduler state.
func (s *Scheduler) Observability() Observability {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := Observability{CycleActive: s.cycleRunning}
	if !s.lastCycle.IsZero() {
		t := s.lastCycle
		out.LastCycle = &t
	}
	if !s.nextCycle.IsZero() {
		t := s.nextCycle
		out.NextCycle = &t
	}
	return out
}

func (s *Scheduler) setNext(t time.Time) {
	s.mu.Lock()
	s.nextCycle = t
	s.mu.Unlock()
}
