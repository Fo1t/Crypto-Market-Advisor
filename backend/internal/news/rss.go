package news

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

var htmlTagPattern = regexp.MustCompile(`<[^>]*>`)

// RSSProvider parses RSS 2.x and Atom feeds behind the same contract.
type RSSProvider struct {
	client           *http.Client
	guard            URLGuard
	maxResponseBytes int64
	maxRetries       int
	retryBaseWait    time.Duration
}

// NewRSSProvider builds the universal RSS/Atom provider.
func NewRSSProvider(client *http.Client, guard URLGuard, maxResponseBytes int64, maxRetries int, retryBaseWait time.Duration) *RSSProvider {
	return &RSSProvider{
		client: client, guard: guard, maxResponseBytes: maxResponseBytes,
		maxRetries: maxRetries, retryBaseWait: retryBaseWait,
	}
}

// Name identifies the provider in logs and per-source status.
func (p *RSSProvider) Name() string { return "rss_atom" }

// Fetch reads a feed, applying SSRF protection, conditional GET and retries.
func (p *RSSProvider) Fetch(ctx context.Context, source domain.NewsSource, since time.Time) (domain.NewsFetchResult, error) {
	if err := p.guard.Validate(ctx, source.URL); err != nil {
		return domain.NewsFetchResult{}, fmt.Errorf("reject news source: %w", err)
	}
	var lastErr error
	for attempt := 0; attempt <= p.maxRetries; attempt++ {
		result, retry, err := p.fetchOnce(ctx, source, since)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if !retry || attempt == p.maxRetries {
			break
		}
		wait := p.retryBaseWait * time.Duration(1<<attempt)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return domain.NewsFetchResult{}, ctx.Err()
		case <-timer.C:
		}
	}
	return domain.NewsFetchResult{}, lastErr
}

func (p *RSSProvider) fetchOnce(ctx context.Context, source domain.NewsSource, since time.Time) (domain.NewsFetchResult, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source.URL, nil)
	if err != nil {
		return domain.NewsFetchResult{}, false, fmt.Errorf("create feed request: %w", err)
	}
	req.Header.Set("Accept", "application/atom+xml, application/rss+xml, application/xml, text/xml;q=0.9")
	req.Header.Set("User-Agent", "CryptoMarketAdvisor/2.0 (+local-news-ingestion)")
	if source.ETag != "" {
		req.Header.Set("If-None-Match", source.ETag)
	}
	if source.LastModified != "" {
		req.Header.Set("If-Modified-Since", source.LastModified)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return domain.NewsFetchResult{}, true, fmt.Errorf("fetch feed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	fetchedAt := time.Now().UTC()
	if resp.StatusCode == http.StatusNotModified {
		return domain.NewsFetchResult{
			NotModified: true, ETag: source.ETag,
			LastModified: source.LastModified, FetchedAt: fetchedAt,
		}, false, nil
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return domain.NewsFetchResult{}, true, fmt.Errorf("feed returned retryable status %d", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return domain.NewsFetchResult{}, false, fmt.Errorf("feed returned status %d", resp.StatusCode)
	}

	limited := io.LimitReader(resp.Body, p.maxResponseBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return domain.NewsFetchResult{}, true, fmt.Errorf("read feed: %w", err)
	}
	if int64(len(body)) > p.maxResponseBytes {
		return domain.NewsFetchResult{}, false, fmt.Errorf("feed exceeds %d-byte limit", p.maxResponseBytes)
	}
	items, err := parseFeed(body, since)
	if err != nil {
		return domain.NewsFetchResult{}, false, err
	}
	return domain.NewsFetchResult{
		Items: items, ETag: resp.Header.Get("ETag"),
		LastModified: resp.Header.Get("Last-Modified"), FetchedAt: fetchedAt,
	}, false, nil
}

type rssDocument struct {
	Channel struct {
		Language string     `xml:"language"`
		Items    []rssEntry `xml:"item"`
	} `xml:"channel"`
}

type rssEntry struct {
	GUID        string `xml:"guid"`
	Link        string `xml:"link"`
	Title       string `xml:"title"`
	Description string `xml:"description"`
	PubDate     string `xml:"pubDate"`
	Date        string `xml:"date"`
}

type atomDocument struct {
	Language string      `xml:"lang,attr"`
	Entries  []atomEntry `xml:"entry"`
}

type atomEntry struct {
	ID        string     `xml:"id"`
	Title     string     `xml:"title"`
	Summary   string     `xml:"summary"`
	Content   string     `xml:"content"`
	Published string     `xml:"published"`
	Updated   string     `xml:"updated"`
	Links     []atomLink `xml:"link"`
}

type atomLink struct {
	Rel  string `xml:"rel,attr"`
	Href string `xml:"href,attr"`
}

func parseFeed(body []byte, since time.Time) ([]domain.RawNewsItem, error) {
	var root struct{ XMLName xml.Name }
	if err := xml.Unmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("decode feed root: %w", err)
	}
	switch strings.ToLower(root.XMLName.Local) {
	case "rss", "rdf":
		var feed rssDocument
		if err := xml.Unmarshal(body, &feed); err != nil {
			return nil, fmt.Errorf("decode rss feed: %w", err)
		}
		items := make([]domain.RawNewsItem, 0, len(feed.Channel.Items))
		for _, entry := range feed.Channel.Items {
			published, err := parseFeedTime(firstNonEmpty(entry.PubDate, entry.Date))
			if err != nil || (!since.IsZero() && published.Before(since)) {
				continue
			}
			items = append(items, domain.RawNewsItem{
				ExternalID: firstNonEmpty(strings.TrimSpace(entry.GUID), strings.TrimSpace(entry.Link)),
				URL:        strings.TrimSpace(entry.Link), Title: cleanFeedText(entry.Title, 500),
				Summary: cleanFeedText(entry.Description, 4000), Language: normalizedLanguage(feed.Channel.Language),
				PublishedAt: published,
			})
		}
		return items, nil
	case "feed":
		var feed atomDocument
		if err := xml.Unmarshal(body, &feed); err != nil {
			return nil, fmt.Errorf("decode atom feed: %w", err)
		}
		items := make([]domain.RawNewsItem, 0, len(feed.Entries))
		for _, entry := range feed.Entries {
			published, err := parseFeedTime(firstNonEmpty(entry.Published, entry.Updated))
			if err != nil || (!since.IsZero() && published.Before(since)) {
				continue
			}
			link := atomEntryLink(entry.Links)
			items = append(items, domain.RawNewsItem{
				ExternalID: firstNonEmpty(strings.TrimSpace(entry.ID), link), URL: link,
				Title: cleanFeedText(entry.Title, 500), Summary: cleanFeedText(firstNonEmpty(entry.Summary, entry.Content), 4000),
				Language: normalizedLanguage(feed.Language), PublishedAt: published,
			})
		}
		return items, nil
	default:
		return nil, fmt.Errorf("unsupported feed root %q", root.XMLName.Local)
	}
}

func parseFeedTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, errors.New("feed entry has no publication time")
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC822} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported feed time %q", value)
}

func atomEntryLink(links []atomLink) string {
	for _, link := range links {
		if link.Rel == "alternate" && link.Href != "" {
			return strings.TrimSpace(link.Href)
		}
	}
	for _, link := range links {
		if link.Href != "" {
			return strings.TrimSpace(link.Href)
		}
	}
	return ""
}

func cleanFeedText(value string, maxRunes int) string {
	value = html.UnescapeString(htmlTagPattern.ReplaceAllString(value, " "))
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > maxRunes {
		runes = runes[:maxRunes]
	}
	return string(runes)
}

func normalizedLanguage(language string) string {
	language = strings.TrimSpace(strings.ToLower(language))
	if language == "" {
		return "und"
	}
	return language
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
