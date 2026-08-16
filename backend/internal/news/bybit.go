package news

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

// BybitAnnouncementsProvider reads the public V5 announcements endpoint. It
// intentionally accepts no API key because this endpoint is public.
type BybitAnnouncementsProvider struct {
	client           *http.Client
	guard            URLGuard
	maxResponseBytes int64
	maxRetries       int
	retryBaseWait    time.Duration
	maxPages         int
}

// NewBybitAnnouncementsProvider builds the official public announcements provider.
func NewBybitAnnouncementsProvider(client *http.Client, guard URLGuard, maxResponseBytes int64, maxRetries int, retryBaseWait time.Duration) *BybitAnnouncementsProvider {
	return &BybitAnnouncementsProvider{
		client: client, guard: guard, maxResponseBytes: maxResponseBytes,
		maxRetries: maxRetries, retryBaseWait: retryBaseWait, maxPages: 10,
	}
}

// Name identifies the provider in logs and per-source status.
func (p *BybitAnnouncementsProvider) Name() string { return "bybit_announcements" }

// Fetch reads announcements published after since, honouring conditional GET.
func (p *BybitAnnouncementsProvider) Fetch(ctx context.Context, source domain.NewsSource, since time.Time) (domain.NewsFetchResult, error) {
	if err := p.guard.Validate(ctx, source.URL); err != nil {
		return domain.NewsFetchResult{}, fmt.Errorf("reject bybit source: %w", err)
	}
	all := make([]domain.RawNewsItem, 0, 40)
	result := domain.NewsFetchResult{FetchedAt: time.Now().UTC()}
	for page := 1; page <= p.maxPages; page++ {
		items, etag, lastModified, notModified, err := p.fetchPage(ctx, source, page)
		if err != nil {
			return domain.NewsFetchResult{}, err
		}
		if page == 1 {
			result.ETag, result.LastModified, result.NotModified = etag, lastModified, notModified
		}
		if notModified {
			return result, nil
		}
		if len(items) == 0 {
			break
		}
		oldestReached := false
		for _, item := range items {
			if !since.IsZero() && item.PublishedAt.Before(since) {
				oldestReached = true
				continue
			}
			all = append(all, item)
		}
		if oldestReached || len(items) < 20 {
			break
		}
	}
	result.Items = all
	return result, nil
}

func (p *BybitAnnouncementsProvider) fetchPage(ctx context.Context, source domain.NewsSource, page int) ([]domain.RawNewsItem, string, string, bool, error) {
	endpoint, err := url.Parse(source.URL)
	if err != nil {
		return nil, "", "", false, fmt.Errorf("parse bybit endpoint: %w", err)
	}
	query := endpoint.Query()
	if query.Get("locale") == "" {
		query.Set("locale", "en-US")
	}
	query.Set("page", strconv.Itoa(page))
	query.Set("limit", "20")
	endpoint.RawQuery = query.Encode()

	var lastErr error
	for attempt := 0; attempt <= p.maxRetries; attempt++ {
		items, etag, modified, notModified, retry, err := p.fetchPageOnce(ctx, source, endpoint.String())
		if err == nil {
			return items, etag, modified, notModified, nil
		}
		lastErr = err
		if !retry || attempt == p.maxRetries {
			break
		}
		timer := time.NewTimer(p.retryBaseWait * time.Duration(1<<attempt))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, "", "", false, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, "", "", false, lastErr
}

func (p *BybitAnnouncementsProvider) fetchPageOnce(ctx context.Context, source domain.NewsSource, endpoint string) ([]domain.RawNewsItem, string, string, bool, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, "", "", false, false, fmt.Errorf("create bybit request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "CryptoMarketAdvisor/2.0 (+local-news-ingestion)")
	if source.ETag != "" {
		req.Header.Set("If-None-Match", source.ETag)
	}
	if source.LastModified != "" {
		req.Header.Set("If-Modified-Since", source.LastModified)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, "", "", false, true, fmt.Errorf("fetch bybit announcements: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotModified {
		return nil, source.ETag, source.LastModified, true, false, nil
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, "", "", false, true, fmt.Errorf("bybit returned retryable status %d", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, "", "", false, false, fmt.Errorf("bybit returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, p.maxResponseBytes+1))
	if err != nil {
		return nil, "", "", false, true, fmt.Errorf("read bybit response: %w", err)
	}
	if int64(len(body)) > p.maxResponseBytes {
		return nil, "", "", false, false, fmt.Errorf("bybit response exceeds %d-byte limit", p.maxResponseBytes)
	}
	var envelope struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
		Result  struct {
			List []struct {
				Title          string   `json:"title"`
				Description    string   `json:"description"`
				URL            string   `json:"url"`
				DateTimestamp  int64    `json:"dateTimestamp"`
				StartTimestamp int64    `json:"startDataTimestamp"`
				Tags           []string `json:"tags"`
				Type           struct {
					Title string `json:"title"`
					Key   string `json:"key"`
				} `json:"type"`
			} `json:"list"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, "", "", false, false, fmt.Errorf("decode bybit response: %w", err)
	}
	if envelope.RetCode != 0 {
		return nil, "", "", false, false, fmt.Errorf("bybit API error %d: %s", envelope.RetCode, envelope.RetMsg)
	}
	language := normalizedLanguage(endpointLocale(endpoint))
	items := make([]domain.RawNewsItem, 0, len(envelope.Result.List))
	for _, entry := range envelope.Result.List {
		published := time.UnixMilli(entry.DateTimestamp).UTC()
		metadata := map[string]any{"type": entry.Type.Key, "type_title": entry.Type.Title, "tags": entry.Tags}
		if entry.StartTimestamp > 0 {
			metadata["event_start_at"] = time.UnixMilli(entry.StartTimestamp).UTC().Format(time.RFC3339)
		}
		items = append(items, domain.RawNewsItem{
			ExternalID:  firstNonEmpty(strings.TrimSpace(entry.URL), TitleHash(NormalizeTitle(entry.Title))+":"+strconv.FormatInt(entry.DateTimestamp, 10)),
			URL:         strings.TrimSpace(entry.URL),
			Title:       cleanFeedText(entry.Title, 500),
			Summary:     cleanFeedText(entry.Description, 4000),
			Language:    language,
			PublishedAt: published,
			Metadata:    metadata,
		})
	}
	return items, resp.Header.Get("ETag"), resp.Header.Get("Last-Modified"), false, false, nil
}

func endpointLocale(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "en-US"
	}
	return firstNonEmpty(u.Query().Get("locale"), "en-US")
}
