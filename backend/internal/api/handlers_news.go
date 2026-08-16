package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/crypto-market-advisor/advisor/internal/config"
	"github.com/crypto-market-advisor/advisor/internal/domain"
	newsintelligence "github.com/crypto-market-advisor/advisor/internal/news"
	"github.com/crypto-market-advisor/advisor/internal/repository"
)

// NewsSourceDTO is the API view of a configured news source.
type NewsSourceDTO struct {
	ID                uuid.UUID               `json:"id"`
	Name              string                  `json:"name"`
	URL               string                  `json:"url"`
	Provider          domain.NewsProvider     `json:"provider"`
	Priority          int                     `json:"priority"`
	Enabled           bool                    `json:"enabled"`
	System            bool                    `json:"system"`
	Status            domain.NewsSourceStatus `json:"status"`
	LastAttemptAt     *time.Time              `json:"last_attempt_at,omitempty"`
	LastSuccessAt     *time.Time              `json:"last_success_at,omitempty"`
	LastError         string                  `json:"last_error,omitempty"`
	ConsecutiveErrors int                     `json:"consecutive_errors"`
}

func newsSourceDTO(source domain.NewsSource) NewsSourceDTO {
	return NewsSourceDTO{
		ID: source.ID, Name: source.Name, URL: source.URL, Provider: source.Provider,
		Priority: source.Priority, Enabled: source.Enabled, System: source.System,
		Status: source.Status, LastAttemptAt: source.LastAttemptAt,
		LastSuccessAt: source.LastSuccessAt, LastError: source.LastError,
		ConsecutiveErrors: source.ConsecutiveErrors,
	}
}

func (s *Server) handleListNews(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()
	limit, offset := pagination(r, 50)
	filter := repository.NewsListFilter{
		Query:       strings.TrimSpace(r.URL.Query().Get("q")),
		AssetSymbol: strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("asset"))),
		Category:    strings.ToLower(strings.TrimSpace(r.URL.Query().Get("category"))),
		Sort:        strings.ToLower(strings.TrimSpace(r.URL.Query().Get("sort"))),
		Since:       parseSince(r), Limit: limit, Offset: offset,
	}
	if filter.Category != "" && !domain.NewsCategory(filter.Category).Valid() {
		WriteError(w, r, s.log, ErrValidation("invalid news category"))
		return
	}
	if filter.Sort == "" {
		filter.Sort = "latest"
	}
	if filter.Sort != "latest" && filter.Sort != "importance" {
		WriteError(w, r, s.log, ErrValidation("sort must be latest or importance"))
		return
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("source_id")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			WriteError(w, r, s.log, ErrValidation("source_id must be a UUID"))
			return
		}
		filter.SourceID = &id
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("critical")); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			WriteError(w, r, s.log, ErrValidation("critical must be true or false"))
			return
		}
		filter.Critical = &value
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("min_importance")); raw != "" {
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil || value < 0 || value > 1 {
			WriteError(w, r, s.log, ErrValidation("min_importance must be between 0 and 1"))
			return
		}
		filter.MinImportance = &value
	}
	items, total, err := s.deps.Repos.News.ListClusters(ctx, filter)
	if err != nil {
		WriteError(w, r, s.log, ErrInternal("failed to list news").WithCause(err))
		return
	}
	now := time.Now().UTC()
	for index := range items {
		items[index].Freshness = newsintelligence.FreshnessScore(items[index].FirstPublishedAt, now)
	}
	WriteJSON(w, http.StatusOK, Page[domain.NewsClusterView]{Items: items, Total: total, Limit: limit, Offset: offset})
}

func (s *Server) handleGetNews(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(r, "id")
	if err != nil {
		WriteError(w, r, s.log, err)
		return
	}
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()
	view, err := s.deps.Repos.News.GetCluster(ctx, id)
	if err != nil {
		WriteError(w, r, s.log, notFoundOr(err, "news event not found"))
		return
	}
	view.Freshness = newsintelligence.FreshnessScore(view.FirstPublishedAt, time.Now().UTC())
	WriteJSON(w, http.StatusOK, view)
}

func (s *Server) handleListNewsSources(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()
	sources, err := s.deps.Repos.News.ListSources(ctx)
	if err != nil {
		WriteError(w, r, s.log, ErrInternal("failed to list news sources").WithCause(err))
		return
	}
	out := make([]NewsSourceDTO, 0, len(sources))
	for _, source := range sources {
		out = append(out, newsSourceDTO(source))
	}
	WriteJSON(w, http.StatusOK, out)
}

type createNewsSourceRequest struct {
	Name     string              `json:"name"`
	URL      string              `json:"url"`
	Provider domain.NewsProvider `json:"provider"`
	Priority int                 `json:"priority"`
	Enabled  *bool               `json:"enabled,omitempty"`
}

func (s *Server) handleCreateNewsSource(w http.ResponseWriter, r *http.Request) {
	var request createNewsSourceRequest
	if err := decodeJSON(r, &request); err != nil {
		WriteError(w, r, s.log, err)
		return
	}
	request.Name = strings.TrimSpace(request.Name)
	request.URL = strings.TrimSpace(request.URL)
	if request.Name == "" || len([]rune(request.Name)) > 120 {
		WriteError(w, r, s.log, ErrValidation("name is required and must not exceed 120 characters"))
		return
	}
	if request.Provider != domain.NewsProviderRSS && request.Provider != domain.NewsProviderAtom {
		WriteError(w, r, s.log, ErrValidation("custom source provider must be rss or atom"))
		return
	}
	if request.Priority < 0 || request.Priority > 100 {
		WriteError(w, r, s.log, ErrValidation("priority must be between 0 and 100"))
		return
	}
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()
	canonicalURL, err := validateNewsSourceURL(ctx, s.deps.Config, request.URL)
	if err != nil {
		WriteError(w, r, s.log, ErrValidation(err.Error()))
		return
	}
	enabled := true
	if request.Enabled != nil {
		enabled = *request.Enabled
	}
	source, err := s.deps.Repos.News.UpsertSource(ctx, domain.NewsSource{
		Name: request.Name, URL: request.URL, CanonicalURL: canonicalURL,
		Provider: request.Provider, Priority: request.Priority, Enabled: enabled,
	})
	if err != nil {
		WriteError(w, r, s.log, ErrInternal("failed to create news source").WithCause(err))
		return
	}
	WriteJSON(w, http.StatusCreated, newsSourceDTO(source))
}

type updateNewsSourceRequest struct {
	Name     *string              `json:"name,omitempty"`
	URL      *string              `json:"url,omitempty"`
	Provider *domain.NewsProvider `json:"provider,omitempty"`
	Priority *int                 `json:"priority,omitempty"`
	Enabled  *bool                `json:"enabled,omitempty"`
}

func (s *Server) handleUpdateNewsSource(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(r, "id")
	if err != nil {
		WriteError(w, r, s.log, err)
		return
	}
	var request updateNewsSourceRequest
	if err := decodeJSON(r, &request); err != nil {
		WriteError(w, r, s.log, err)
		return
	}
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()
	source, err := s.deps.Repos.News.GetSource(ctx, id)
	if err != nil {
		WriteError(w, r, s.log, notFoundOr(err, "news source not found"))
		return
	}
	if request.Name != nil {
		source.Name = strings.TrimSpace(*request.Name)
	}
	if request.Priority != nil {
		source.Priority = *request.Priority
	}
	if request.Enabled != nil {
		source.Enabled = *request.Enabled
	}
	if source.Name == "" || len([]rune(source.Name)) > 120 || source.Priority < 0 || source.Priority > 100 {
		WriteError(w, r, s.log, ErrValidation("invalid source name or priority"))
		return
	}
	if request.URL != nil || request.Provider != nil {
		if source.System {
			WriteError(w, r, s.log, ErrValidation("system source URL and provider are immutable"))
			return
		}
		if request.URL != nil {
			source.URL = strings.TrimSpace(*request.URL)
		}
		if request.Provider != nil {
			source.Provider = *request.Provider
		}
		if source.Provider != domain.NewsProviderRSS && source.Provider != domain.NewsProviderAtom {
			WriteError(w, r, s.log, ErrValidation("custom source provider must be rss or atom"))
			return
		}
		source.CanonicalURL, err = validateNewsSourceURL(ctx, s.deps.Config, source.URL)
		if err != nil {
			WriteError(w, r, s.log, ErrValidation(err.Error()))
			return
		}
	}
	updated, err := s.deps.Repos.News.UpdateSource(ctx, source)
	if err != nil {
		WriteError(w, r, s.log, notFoundOr(err, "failed to update news source"))
		return
	}
	WriteJSON(w, http.StatusOK, newsSourceDTO(updated))
}

func (s *Server) handleDisableNewsSource(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(r, "id")
	if err != nil {
		WriteError(w, r, s.log, err)
		return
	}
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()
	source, err := s.deps.Repos.News.GetSource(ctx, id)
	if err != nil {
		WriteError(w, r, s.log, notFoundOr(err, "news source not found"))
		return
	}
	source.Enabled = false
	if _, err := s.deps.Repos.News.UpdateSource(ctx, source); err != nil {
		WriteError(w, r, s.log, ErrInternal("failed to disable news source").WithCause(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleNewsStats(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()
	stats, err := s.deps.Repos.News.Stats(ctx)
	if err != nil {
		WriteError(w, r, s.log, ErrInternal("failed to load news stats").WithCause(err))
		return
	}
	WriteJSON(w, http.StatusOK, stats)
}

func (s *Server) handleHealthNews(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()
	newsEnabled := s.deps.Config.News.Enabled
	if s.deps.Settings != nil {
		newsEnabled = s.deps.Settings.Current().News.Enabled
	}
	if !newsEnabled {
		WriteJSON(w, http.StatusOK, map[string]any{"status": domain.NewsContextDisabled})
		return
	}
	stats, err := s.deps.Repos.News.Stats(ctx)
	if err != nil {
		WriteError(w, r, s.log, ErrInternal("failed to load news health").WithCause(err))
		return
	}
	status := domain.NewsContextOK
	online := stats.SourcesByStatus[domain.NewsSourceOnline]
	if online == 0 {
		status = domain.NewsContextUnavailable
	} else if online < stats.SourcesEnabled {
		status = domain.NewsContextDegraded
	} else if stats.ItemsTotal == 0 {
		status = domain.NewsContextAvailableButEmpty
	}
	WriteJSON(w, http.StatusOK, map[string]any{"status": status, "stats": stats})
}

func validateNewsSourceURL(ctx context.Context, cfg config.Config, raw string) (string, error) {
	canonical, err := newsintelligence.NormalizeURL(raw)
	if err != nil {
		return "", err
	}
	guard := newsintelligence.URLGuard{AllowPrivate: cfg.News.AllowPrivateFeeds}
	if err := guard.Validate(ctx, canonical); err != nil {
		return "", err
	}
	return canonical, nil
}
