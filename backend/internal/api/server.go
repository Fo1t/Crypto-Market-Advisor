package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/crypto-market-advisor/advisor/internal/backtesting"
	"github.com/crypto-market-advisor/advisor/internal/config"
	"github.com/crypto-market-advisor/advisor/internal/database"
	"github.com/crypto-market-advisor/advisor/internal/history"
	"github.com/crypto-market-advisor/advisor/internal/llm"
	"github.com/crypto-market-advisor/advisor/internal/marketdata"
	"github.com/crypto-market-advisor/advisor/internal/positions"
	"github.com/crypto-market-advisor/advisor/internal/repository"
	"github.com/crypto-market-advisor/advisor/internal/scheduler"
	"github.com/crypto-market-advisor/advisor/internal/settings"
)

// Deps carries every service the HTTP layer needs.
type Deps struct {
	Config    config.Config
	Logger    *slog.Logger
	DB        *database.DB
	Repos     *repository.Repositories
	Market    *marketdata.Service
	Positions *positions.Service
	History   *history.Service
	Scheduler *scheduler.Scheduler
	Backtests *backtesting.Engine
	Settings  *settings.Service
	LLM       *llm.Service
}

// Server exposes the REST API.
type Server struct {
	deps   Deps
	log    *slog.Logger
	router chi.Router
}

// NewServer builds the API server with its routes.
func NewServer(deps Deps) *Server {
	s := &Server{deps: deps, log: deps.Logger}
	s.router = s.routes()
	return s
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler { return s.router }

func (s *Server) routes() chi.Router {
	r := chi.NewRouter()
	r.Use(RequestIDMiddleware)
	r.Use(RecoverMiddleware(s.log))
	r.Use(LoggingMiddleware(s.log))
	r.Use(CORSMiddleware(s.deps.Config.HTTP.CORSAllowOrigin))

	r.Route("/api", func(api chi.Router) {
		api.Route("/health", func(h chi.Router) {
			h.Get("/", s.handleHealth)
			h.Get("/db", s.handleHealthDB)
			h.Get("/market-data", s.handleHealthMarketData)
			h.Get("/llm", s.handleHealthLLM)
			h.Get("/news", s.handleHealthNews)
		})

		api.Route("/news", func(n chi.Router) {
			n.Get("/", s.handleListNews)
			n.Get("/stats", s.handleNewsStats)
			n.Get("/sources", s.handleListNewsSources)
			n.Post("/sources", s.handleCreateNewsSource)
			n.Patch("/sources/{id}", s.handleUpdateNewsSource)
			n.Delete("/sources/{id}", s.handleDisableNewsSource)
			n.Get("/{id}", s.handleGetNews)
		})

		api.Route("/markets", func(m chi.Router) {
			m.Get("/", s.handleListMarkets)
			m.Post("/", s.handleCreateMarket)
			m.Post("/refresh", s.handleRefreshUniverse)
			m.Get("/{symbol}", s.handleGetMarket)
			m.Patch("/{symbol}", s.handleUpdateMarket)
			m.Delete("/{symbol}", s.handleDeleteMarket)
			m.Get("/{symbol}/analysis", s.handleGetAnalysis)
			m.Get("/{symbol}/candles", s.handleGetCandles)
			m.Post("/{symbol}/analyze", s.handleAnalyzeNow)
		})

		api.Route("/recommendations", func(rec chi.Router) {
			rec.Get("/", s.handleListRecommendations)
			rec.Delete("/", s.handleDismissAllRecommendations)
			rec.Get("/{id}", s.handleGetRecommendation)
			rec.Delete("/{id}", s.handleDismissRecommendation)
			rec.Post("/{id}/restore", s.handleRestoreRecommendation)
			rec.Post("/{id}/decision", s.handleRecommendationDecision)
		})

		api.Route("/positions", func(p chi.Router) {
			p.Get("/", s.handleListPositions)
			p.Post("/", s.handleCreatePosition)
			p.Get("/{id}", s.handleGetPosition)
			p.Delete("/{id}", s.handleDeletePosition)
			p.Post("/{id}/close", s.handleClosePosition)
			p.Post("/{id}/partial-close", s.handlePartialClose)
			p.Post("/{id}/plan", s.handleUpdatePlan)
			p.Post("/{id}/fee", s.handleAddFee)
			p.Post("/{id}/funding", s.handleAddFunding)
		})

		api.Get("/statistics", s.handleStatistics)
		api.Get("/dashboard", s.handleDashboard)

		api.Route("/backtests", func(b chi.Router) {
			b.Get("/", s.handleListBacktests)
			b.Post("/", s.handleCreateBacktest)
			b.Post("/estimate", s.handleEstimateBacktest)
			b.Post("/hide", s.handleHideBacktests)
			b.Get("/hidden", s.handleBacktestHidden)
			b.Post("/purge", s.handlePurgeBacktests)
			b.Get("/{id}", s.handleGetBacktest)
			b.Delete("/{id}", s.handleDeleteBacktest)
			b.Post("/{id}/cancel", s.handleCancelBacktest)
		})

		api.Get("/strategies", s.handleGetStrategies)
		api.Get("/settings", s.handleGetSettings)
		api.Put("/settings", s.handleUpdateSettings)
	})

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, r, s.log, ErrNotFound("endpoint not found"))
	})
	return r
}

// decodeJSON reads and validates a JSON request body.
func decodeJSON(r *http.Request, v any) error {
	if r.Body == nil {
		return ErrBadRequest("request body is required")
	}
	decoder := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4<<20))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(v); err != nil {
		return ErrBadRequest("invalid JSON body: " + err.Error()).WithCause(err)
	}
	return nil
}

// pagination extracts limit/offset query parameters.
func pagination(r *http.Request, defaultLimit int) (limit, offset int) {
	limit = defaultLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 && parsed <= 500 {
			limit = parsed
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed >= 0 {
			offset = parsed
		}
	}
	return limit, offset
}

// parseUUID reads a UUID path parameter.
func parseUUID(r *http.Request, param string) (uuid.UUID, error) { //nolint:unparam // every route uses "id" today; the name stays explicit
	raw := chi.URLParam(r, param)
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, ErrBadRequest(fmt.Sprintf("%s must be a UUID", param)).WithCause(err)
	}
	return id, nil
}

func parseSince(r *http.Request) *time.Time {
	raw := r.URL.Query().Get("since")
	if raw == "" {
		if days := r.URL.Query().Get("days"); days != "" {
			if n, err := strconv.Atoi(days); err == nil && n > 0 {
				t := time.Now().UTC().AddDate(0, 0, -n)
				return &t
			}
		}
		return nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		utc := t.UTC()
		return &utc
	}
	return nil
}

// Page is the envelope for paginated listings.
type Page[T any] struct {
	Items  []T `json:"items"`
	Total  int `json:"total"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

// notFoundOr converts a repository miss into a 404 and anything else into a 500.
func notFoundOr(err error, message string) error {
	if err == nil {
		return nil
	}
	if isNotFound(err) {
		return ErrNotFound(message)
	}
	return ErrInternal(message).WithCause(err)
}

func isNotFound(err error) bool {
	return errors.Is(err, repository.ErrNotFound)
}

// contextWithTimeout gives handlers a bounded lifetime.
func contextWithTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}
