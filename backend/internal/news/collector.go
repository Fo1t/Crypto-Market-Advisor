package news

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/crypto-market-advisor/advisor/internal/config"
	"github.com/crypto-market-advisor/advisor/internal/domain"
	"github.com/crypto-market-advisor/advisor/internal/logging"
)

// NewsStore is the narrow persistence contract used by collection workers.
type NewsStore interface {
	ListSources(ctx context.Context) ([]domain.NewsSource, error)
	UpsertItems(ctx context.Context, items []domain.NewsItem) (inserted int, existing int, err error)
	RecordFetchSuccess(ctx context.Context, id uuid.UUID, at time.Time, etag, lastModified string) error
	RecordFetchFailure(ctx context.Context, id uuid.UUID, at time.Time, message string) error
}

// CollectionStats summarizes one fault-isolated pass over enabled sources.
type CollectionStats struct {
	SourcesAttempted int
	SourcesSucceeded int
	SourcesFailed    int
	ItemsReceived    int
	ItemsInserted    int
	ItemsExisting    int
	ItemsRejected    int
	ItemsClustered   int
	ClustersCreated  int
}

// Collector coordinates providers and persistence without invoking the LLM.
type Collector struct {
	cfgMu     sync.RWMutex
	cfg       config.NewsConfig
	store     NewsStore
	providers map[domain.NewsProvider]Provider
	log       *slog.Logger
	now       func() time.Time
	enricher  interface {
		ProcessPending(context.Context) (EnrichmentStats, error)
	}
}

// SetConfig applies live collection controls.
func (c *Collector) SetConfig(cfg config.NewsConfig) {
	c.cfgMu.Lock()
	c.cfg = cfg
	c.cfgMu.Unlock()
}

func (c *Collector) config() config.NewsConfig {
	c.cfgMu.RLock()
	defer c.cfgMu.RUnlock()
	return c.cfg
}

// SetEnricher attaches the deterministic post-ingestion processor.
func (c *Collector) SetEnricher(enricher interface {
	ProcessPending(context.Context) (EnrichmentStats, error)
}) {
	c.enricher = enricher
}

// NewCollector wires the enabled providers into one bounded ingestion cycle.
func NewCollector(cfg config.NewsConfig, store NewsStore, rss Provider, bybit Provider, logger *slog.Logger) *Collector {
	providers := map[domain.NewsProvider]Provider{
		domain.NewsProviderRSS:  rss,
		domain.NewsProviderAtom: rss,
	}
	if bybit != nil {
		providers[domain.NewsProviderBybit] = bybit
	}
	return &Collector{
		cfg: cfg, store: store, providers: providers,
		log: logging.For(logger, logging.CategoryNews), now: func() time.Time { return time.Now().UTC() },
	}
}

// Collect processes sources concurrently while containing every source error.
// The returned error joins failures for observability; successful sources are
// still committed and counted.
func (c *Collector) Collect(ctx context.Context) (CollectionStats, error) {
	cfg := c.config()
	if !cfg.Enabled {
		return CollectionStats{}, nil
	}
	sources, err := c.store.ListSources(ctx)
	if err != nil {
		return CollectionStats{}, fmt.Errorf("list news sources: %w", err)
	}
	limit := cfg.FetchConcurrency
	if limit < 1 {
		limit = 1
	}
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var stats CollectionStats
	var failures []error

	for _, source := range sources {
		if !source.Enabled || (source.Provider == domain.NewsProviderBybit && !cfg.BybitEnabled) {
			continue
		}
		source := source
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			mu.Lock()
			stats.SourcesAttempted++
			mu.Unlock()
			partial, err := c.collectSource(ctx, source)
			mu.Lock()
			stats.ItemsReceived += partial.ItemsReceived
			stats.ItemsInserted += partial.ItemsInserted
			stats.ItemsExisting += partial.ItemsExisting
			stats.ItemsRejected += partial.ItemsRejected
			if err != nil {
				stats.SourcesFailed++
				failures = append(failures, fmt.Errorf("%s: %w", source.Name, err))
			} else {
				stats.SourcesSucceeded++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	if c.enricher != nil {
		enrichment, err := c.enricher.ProcessPending(ctx)
		stats.ItemsClustered = enrichment.ItemsProcessed
		stats.ClustersCreated = enrichment.ClustersCreated
		if err != nil {
			failures = append(failures, fmt.Errorf("news enrichment: %w", err))
		}
	}
	return stats, errors.Join(failures...)
}

func (c *Collector) collectSource(ctx context.Context, source domain.NewsSource) (CollectionStats, error) {
	provider := c.providers[source.Provider]
	if provider == nil {
		err := fmt.Errorf("unsupported news provider %q", source.Provider)
		_ = c.store.RecordFetchFailure(ctx, source.ID, c.now(), err.Error())
		return CollectionStats{}, err
	}
	now := c.now()
	since := now.Add(-7 * 24 * time.Hour)
	if source.LastSuccessAt != nil {
		// Deliberate overlap catches late edits and timestamp jitter; exact dedup
		// keeps the pass idempotent.
		since = source.LastSuccessAt.Add(-15 * time.Minute)
	}
	result, err := provider.Fetch(ctx, source, since)
	if err != nil {
		if stateErr := c.store.RecordFetchFailure(ctx, source.ID, now, err.Error()); stateErr != nil {
			err = errors.Join(err, stateErr)
		}
		return CollectionStats{}, err
	}
	stats := CollectionStats{ItemsReceived: len(result.Items)}
	items := make([]domain.NewsItem, 0, len(result.Items))
	for _, raw := range result.Items {
		item, err := normalizeRawItem(source, raw, now)
		if err != nil {
			stats.ItemsRejected++
			c.log.Warn("news item rejected", slog.String("source", source.Name), slog.String("error", err.Error()))
			continue
		}
		items = append(items, item)
	}
	if len(items) > 0 {
		stats.ItemsInserted, stats.ItemsExisting, err = c.store.UpsertItems(ctx, items)
		if err != nil {
			_ = c.store.RecordFetchFailure(ctx, source.ID, now, err.Error())
			return stats, err
		}
	}
	if err := c.store.RecordFetchSuccess(ctx, source.ID, now, result.ETag, result.LastModified); err != nil {
		return stats, err
	}
	return stats, nil
}

func normalizeRawItem(source domain.NewsSource, raw domain.RawNewsItem, seenAt time.Time) (domain.NewsItem, error) {
	title := cleanFeedText(raw.Title, 500)
	normalizedTitle := NormalizeTitle(title)
	if normalizedTitle == "" {
		return domain.NewsItem{}, errors.New("empty news title")
	}
	articleURL, err := resolveArticleURL(source.URL, raw.URL)
	if err != nil {
		return domain.NewsItem{}, err
	}
	canonicalURL, err := NormalizeURL(articleURL)
	if err != nil {
		return domain.NewsItem{}, fmt.Errorf("normalize article url: %w", err)
	}
	if raw.PublishedAt.IsZero() {
		return domain.NewsItem{}, errors.New("missing publication time")
	}
	language := normalizedLanguage(raw.Language)
	return domain.NewsItem{
		ID: uuid.New(), SourceID: source.ID, ExternalID: strings.TrimSpace(raw.ExternalID),
		URL: articleURL, CanonicalURL: canonicalURL, Title: title,
		NormalizedTitle: normalizedTitle, TitleHash: TitleHash(normalizedTitle),
		Summary: cleanFeedText(raw.Summary, 4000), Language: language,
		PublishedAt: raw.PublishedAt.UTC(), FirstSeenAt: seenAt, LastSeenAt: seenAt,
		Metadata: raw.Metadata,
	}, nil
}

func resolveArticleURL(sourceURL, articleURL string) (string, error) {
	base, err := url.Parse(sourceURL)
	if err != nil {
		return "", fmt.Errorf("parse source url: %w", err)
	}
	reference, err := url.Parse(strings.TrimSpace(articleURL))
	if err != nil {
		return "", fmt.Errorf("parse article url: %w", err)
	}
	resolved := base.ResolveReference(reference)
	if resolved.Scheme != "http" && resolved.Scheme != "https" {
		return "", fmt.Errorf("unsupported article scheme %q", resolved.Scheme)
	}
	if resolved.Hostname() == "" {
		return "", errors.New("article url host is required")
	}
	return resolved.String(), nil
}
