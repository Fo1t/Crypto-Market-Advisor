package news

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/crypto-market-advisor/advisor/internal/config"
	"github.com/crypto-market-advisor/advisor/internal/domain"
)

type stubProvider struct {
	result domain.NewsFetchResult
	err    error
}

func (p stubProvider) Name() string { return "stub" }
func (p stubProvider) Fetch(context.Context, domain.NewsSource, time.Time) (domain.NewsFetchResult, error) {
	return p.result, p.err
}

type stubNewsStore struct {
	mu       sync.Mutex
	sources  []domain.NewsSource
	items    []domain.NewsItem
	success  []uuid.UUID
	failures []uuid.UUID
}

func (s *stubNewsStore) ListSources(context.Context) ([]domain.NewsSource, error) {
	return s.sources, nil
}
func (s *stubNewsStore) UpsertItems(_ context.Context, items []domain.NewsItem) (int, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = append(s.items, items...)
	return len(items), 0, nil
}
func (s *stubNewsStore) RecordFetchSuccess(_ context.Context, id uuid.UUID, _ time.Time, _, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.success = append(s.success, id)
	return nil
}
func (s *stubNewsStore) RecordFetchFailure(_ context.Context, id uuid.UUID, _ time.Time, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures = append(s.failures, id)
	return nil
}

func TestCollectorContainsSourceFailuresAndNormalizesItems(t *testing.T) {
	rssID, bybitID := uuid.New(), uuid.New()
	store := &stubNewsStore{sources: []domain.NewsSource{
		{ID: rssID, Name: "Feed", URL: "https://example.com/news/feed.xml", Provider: domain.NewsProviderRSS, Enabled: true},
		{ID: bybitID, Name: "Bybit", URL: "https://api.bybit.com/v5/announcements/index", Provider: domain.NewsProviderBybit, Enabled: true},
	}}
	rss := stubProvider{result: domain.NewsFetchResult{Items: []domain.RawNewsItem{
		{ExternalID: "1", URL: "../article?id=1&utm_source=feed", Title: "  BTC  update! ", PublishedAt: time.Now().UTC()},
	}}}
	bybit := stubProvider{err: errors.New("temporary upstream failure")}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	collector := NewCollector(config.NewsConfig{Enabled: true, FetchConcurrency: 2, BybitEnabled: true}, store, rss, bybit, logger)

	stats, err := collector.Collect(context.Background())
	if err == nil {
		t.Fatal("partial source failure must be observable")
	}
	if stats.SourcesSucceeded != 1 || stats.SourcesFailed != 1 || stats.ItemsInserted != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if len(store.items) != 1 || store.items[0].CanonicalURL != "https://example.com/article?id=1" || store.items[0].NormalizedTitle != "btc update" {
		t.Fatalf("item was not normalized: %#v", store.items)
	}
	if len(store.success) != 1 || store.success[0] != rssID || len(store.failures) != 1 || store.failures[0] != bybitID {
		t.Fatalf("source states not isolated: success=%v failures=%v", store.success, store.failures)
	}
}
