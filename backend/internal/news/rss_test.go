package news

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

func TestParseRSSAndAtom(t *testing.T) {
	since := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	rss := `<?xml version="1.0"?><rss version="2.0"><channel><language>en</language>
		<item><guid>a-1</guid><link>https://example.com/a</link><title>BTC &amp; ETH</title>
		<description><![CDATA[<p>Market update</p>]]></description><pubDate>Fri, 14 Aug 2026 10:05:00 +0000</pubDate></item>
		<item><guid>old</guid><link>https://example.com/old</link><title>Old</title>
		<pubDate>Fri, 14 Aug 2026 09:00:00 +0000</pubDate></item></channel></rss>`
	items, err := parseFeed([]byte(rss), since)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ExternalID != "a-1" || items[0].Summary != "Market update" {
		t.Fatalf("unexpected RSS items: %#v", items)
	}

	atom := `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom" xml:lang="en-US">
		<entry><id>tag:example,2026:1</id><title>Protocol update</title><summary>Ready</summary>
		<link rel="alternate" href="https://example.com/protocol"/><published>2026-08-14T10:06:00Z</published></entry></feed>`
	items, err = parseFeed([]byte(atom), since)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].URL != "https://example.com/protocol" || items[0].Language != "en-us" {
		t.Fatalf("unexpected Atom items: %#v", items)
	}
}

func TestRSSProviderConditionalGetAndResponseLimit(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path == "/oversize" {
			_, _ = fmt.Fprint(w, strings.Repeat("x", 257))
			return
		}
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		_, _ = fmt.Fprint(w, `<?xml version="1.0"?><rss><channel><item><guid>1</guid><link>https://example.com/1</link><title>One</title><pubDate>Fri, 14 Aug 2026 10:05:00 +0000</pubDate></item></channel></rss>`)
	}))
	defer server.Close()

	guard := URLGuard{AllowPrivate: true}
	client := NewSafeHTTPClient(2*time.Second, guard)
	provider := NewRSSProvider(client, guard, 256, 0, time.Millisecond)
	source := domain.NewsSource{URL: server.URL + "/feed"}
	result, err := provider.Fetch(context.Background(), source, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.ETag != `"v1"` {
		t.Fatalf("unexpected fetch result: %#v", result)
	}
	source.ETag = result.ETag
	result, err = provider.Fetch(context.Background(), source, time.Time{})
	if err != nil || !result.NotModified {
		t.Fatalf("conditional fetch = %#v, %v", result, err)
	}

	source.URL = server.URL + "/oversize"
	if _, err := provider.Fetch(context.Background(), source, time.Time{}); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected response-limit error, got %v", err)
	}
	if requests.Load() != 3 {
		t.Fatalf("request count = %d, want 3", requests.Load())
	}
}

func TestRSSProviderRetriesServerErrors(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = fmt.Fprint(w, `<?xml version="1.0"?><rss><channel></channel></rss>`)
	}))
	defer server.Close()
	guard := URLGuard{AllowPrivate: true}
	provider := NewRSSProvider(NewSafeHTTPClient(2*time.Second, guard), guard, 1024, 1, time.Millisecond)
	if _, err := provider.Fetch(context.Background(), domain.NewsSource{URL: server.URL}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 2 {
		t.Fatalf("attempt count = %d, want 2", attempts.Load())
	}
}
