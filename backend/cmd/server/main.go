// Command server is the single binary of the Crypto Market Advisor backend.
//
// Modes:
//
//	server serve    API + background scheduler in one process (default)
//	server api      API only
//	server worker   background scheduler only
//	server migrate  apply database migrations and exit
//
// The same image is used for every mode; there is no second codebase.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/crypto-market-advisor/advisor/internal/api"
	"github.com/crypto-market-advisor/advisor/internal/backtesting"
	"github.com/crypto-market-advisor/advisor/internal/config"
	"github.com/crypto-market-advisor/advisor/internal/database"
	"github.com/crypto-market-advisor/advisor/internal/history"
	"github.com/crypto-market-advisor/advisor/internal/llm"
	"github.com/crypto-market-advisor/advisor/internal/logging"
	"github.com/crypto-market-advisor/advisor/internal/marketdata"
	bybitmarket "github.com/crypto-market-advisor/advisor/internal/marketdata/bybit"
	"github.com/crypto-market-advisor/advisor/internal/marketdata/coingecko"
	newsintelligence "github.com/crypto-market-advisor/advisor/internal/news"
	"github.com/crypto-market-advisor/advisor/internal/positions"
	"github.com/crypto-market-advisor/advisor/internal/recommendations"
	"github.com/crypto-market-advisor/advisor/internal/repository"
	"github.com/crypto-market-advisor/advisor/internal/risk"
	"github.com/crypto-market-advisor/advisor/internal/scheduler"
	"github.com/crypto-market-advisor/advisor/internal/settings"
)

func main() {
	mode := "serve"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}

	if err := run(mode); err != nil {
		slog.Default().Error("fatal error", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

func run(mode string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	logger := logging.New(cfg.Logging.Level, cfg.Logging.Format)
	slog.SetDefault(logger)

	log := logging.For(logger, logging.CategorySystem)
	log.Info("starting", slog.String("mode", mode), slog.String("env", cfg.App.Env))

	if mode == "migrate" {
		return database.Migrate(cfg.Database.URL, logger)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := database.ConnectWithRetry(ctx, cfg.Database, logger, 30, 2*time.Second)
	if err != nil {
		return err
	}
	defer db.Close()

	if cfg.Database.AutoMigrate {
		if err := database.Migrate(cfg.Database.URL, logger); err != nil {
			return err
		}
	}

	app, err := buildApp(ctx, cfg, db, logger)
	if err != nil {
		return err
	}

	var wg sync.WaitGroup
	if mode == "serve" || mode == "worker" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			app.scheduler.Run(ctx)
		}()
	}

	if mode == "serve" || mode == "api" {
		if err := serveHTTP(ctx, cfg, app, logger); err != nil {
			return err
		}
	} else {
		<-ctx.Done()
	}

	wg.Wait()
	log.Info("shutdown complete")
	return nil
}

// application holds the wired services.
type application struct {
	repos     *repository.Repositories
	market    *marketdata.Service
	positions *positions.Service
	history   *history.Service
	analysis  *recommendations.Service
	scheduler *scheduler.Scheduler
	backtests *backtesting.Engine
	settings  *settings.Service
	llm       *llm.Service
	db        *database.DB
}

func buildApp(ctx context.Context, cfg *config.Config, db *database.DB, logger *slog.Logger) (*application, error) {
	repos := repository.New(db.Pool)

	// A backtest cannot survive a restart, so any run still marked active
	// belongs to a previous process and is retired here.
	if retired, err := repos.Backtests.CleanupInterrupted(ctx); err != nil {
		logging.For(logger, logging.CategoryBacktest).Warn("could not clean up interrupted backtests",
			slog.String("error", err.Error()))
	} else if retired > 0 {
		logging.For(logger, logging.CategoryBacktest).Info("retired interrupted backtest runs",
			slog.Int64("count", retired))
	}

	provider := coingecko.New(cfg.CoinGecko, logger)
	var bybitProvider marketdata.BybitProvider
	if cfg.Bybit.Enabled {
		bybitProvider = bybitmarket.New(cfg.Bybit, logger)
	}
	marketSvc := marketdata.NewService(provider, bybitProvider, repos, cfg.CoinGecko, cfg.Analysis, logger)

	llmClient := llm.NewClient(cfg.LLM, logger)
	llmSvc := llm.NewService(llmClient, repos.Recommendations, logger)

	riskEngine := risk.New(cfg.Risk)
	positionsSvc := positions.NewService(repos, cfg.Fees, logger)
	historySvc := history.NewService(repos, logger)
	analysisSvc := recommendations.NewService(repos, marketSvc, llmSvc, riskEngine, positionsSvc, historySvc, *cfg, logger)
	newsGuard := newsintelligence.URLGuard{AllowPrivate: cfg.News.AllowPrivateFeeds}
	newsHTTPClient := newsintelligence.NewSafeHTTPClient(cfg.News.HTTPTimeout, newsGuard)
	rssProvider := newsintelligence.NewRSSProvider(
		newsHTTPClient, newsGuard, cfg.News.MaxResponseBytes, cfg.News.MaxRetries, cfg.News.RetryBaseWait,
	)
	bybitNewsProvider := newsintelligence.NewBybitAnnouncementsProvider(
		newsHTTPClient, newsGuard, cfg.News.MaxResponseBytes, cfg.News.MaxRetries, cfg.News.RetryBaseWait,
	)
	newsCollector := newsintelligence.NewCollector(cfg.News, repos.News, rssProvider, bybitNewsProvider, logger)
	newsProcessor := newsintelligence.NewProcessor(cfg.News, repos.News, repos.Assets, logger)
	newsCollector.SetEnricher(newsProcessor)
	newsReactionTracker := newsintelligence.NewReactionTracker(cfg.News, repos.News, logger)
	schedulerSvc := scheduler.New(*cfg, repos, marketSvc, analysisSvc, newsCollector, newsReactionTracker, logger)
	backtestEngine := backtesting.NewEngine(repos, llmSvc, riskEngine, historySvc, *cfg, logger)

	settingsSvc := settings.NewService(repos, *cfg, logger)
	settingsSvc.Register(&liveConfig{
		positions: positionsSvc,
		backtests: backtestEngine,
		risk:      riskEngine,
		llmClient: llmClient,
		settings:  settingsSvc,
		analysis:  analysisSvc,
		scheduler: schedulerSvc,
		log:       logging.For(logger, logging.CategorySystem),
	})
	if err := settingsSvc.Load(ctx); err != nil {
		return nil, err
	}

	return &application{
		repos: repos, market: marketSvc, positions: positionsSvc, history: historySvc,
		analysis: analysisSvc, scheduler: schedulerSvc, backtests: backtestEngine,
		settings: settingsSvc, llm: llmSvc, db: db,
	}, nil
}

// liveConfig applies edited settings to the running services, so the Settings
// screen changes behaviour without a restart.
type liveConfig struct {
	positions *positions.Service
	backtests *backtesting.Engine
	risk      *risk.Engine
	llmClient *llm.Client
	settings  *settings.Service
	analysis  *recommendations.Service
	scheduler *scheduler.Scheduler
	log       *slog.Logger
}

// ApplySettings implements settings.Applier.
func (l *liveConfig) ApplySettings(_ settings.Settings) error {
	l.positions.SetFees(l.settings.FeesConfig())
	l.risk.SetConfig(l.settings.RiskConfig())
	l.analysis.SetRiskConfig(l.settings.RiskConfig())
	analysisCfg := l.settings.AnalysisConfig()
	l.analysis.SetAnalysisConfig(analysisCfg)
	l.scheduler.SetAnalysisConfig(analysisCfg)
	strategySet := l.settings.StrategySet()
	l.analysis.SetStrategySet(strategySet)
	l.llmClient.SetConfig(l.settings.LLMConfig())
	newsCfg := l.settings.NewsConfig()
	if l.backtests != nil {
		l.backtests.SetAnalysisConfig(analysisCfg)
		l.backtests.SetStrategySet(strategySet)
		l.backtests.SetNewsConfig(newsCfg)
	}
	l.analysis.SetNewsConfig(newsCfg)
	l.scheduler.SetNewsConfig(newsCfg)
	l.log.Info("runtime configuration applied")
	return nil
}

func serveHTTP(ctx context.Context, cfg *config.Config, app *application, logger *slog.Logger) error {
	server := api.NewServer(api.Deps{
		Config: *cfg, Logger: logger, DB: app.db, Repos: app.repos,
		Market: app.market, Positions: app.positions, History: app.history,
		Scheduler: app.scheduler, Backtests: app.backtests, Settings: app.settings, LLM: app.llm,
	})

	httpServer := &http.Server{
		Addr:         cfg.HTTP.Addr(),
		Handler:      server.Handler(),
		ReadTimeout:  cfg.HTTP.ReadTimeout,
		WriteTimeout: cfg.HTTP.WriteTimeout,
		IdleTimeout:  cfg.HTTP.IdleTimeout,
	}

	log := logging.For(logger, logging.CategorySystem)
	errCh := make(chan error, 1)

	go func() {
		log.Info("http server listening", slog.String("addr", cfg.HTTP.Addr()))
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
		log.Info("shutting down http server")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.App.ShutdownTimeout)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("http shutdown: %w", err)
	}
	return nil
}
