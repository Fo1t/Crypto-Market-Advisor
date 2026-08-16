package news

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

func TestBybitAnnouncementsProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("locale") != "ru-RU" || r.URL.Query().Get("page") != "1" || r.URL.Query().Get("limit") != "20" {
			t.Errorf("unexpected query: %s", r.URL.RawQuery)
		}
		w.Header().Set("ETag", `"ann-v1"`)
		_, _ = fmt.Fprint(w, `{
			"retCode":0,"retMsg":"OK","result":{"list":[{
				"title":"New BTC listing","description":"<p>Trading starts soon</p>",
				"type":{"title":"New Listings","key":"new_crypto"},
				"tags":["BTC"],"url":"https://announcements.bybit.com/article/1",
				"dateTimestamp":1786701900000,"startDataTimestamp":1786705200000
			}]}}
		`)
	}))
	defer server.Close()
	guard := URLGuard{AllowPrivate: true}
	provider := NewBybitAnnouncementsProvider(NewSafeHTTPClient(2*time.Second, guard), guard, 64*1024, 0, time.Millisecond)
	result, err := provider.Fetch(context.Background(), domain.NewsSource{URL: server.URL + "?locale=ru-RU"}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].Language != "ru-ru" || result.Items[0].Summary != "Trading starts soon" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result.Items[0].Metadata["type"] != "new_crypto" || result.ETag != `"ann-v1"` {
		t.Fatalf("metadata/cache headers lost: %#v", result)
	}
}

func TestBybitAnnouncementsProviderRejectsAPIErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"retCode":10001,"retMsg":"bad locale","result":{"list":[]}}`)
	}))
	defer server.Close()
	guard := URLGuard{AllowPrivate: true}
	provider := NewBybitAnnouncementsProvider(NewSafeHTTPClient(time.Second, guard), guard, 64*1024, 0, time.Millisecond)
	if _, err := provider.Fetch(context.Background(), domain.NewsSource{URL: server.URL}, time.Time{}); err == nil {
		t.Fatal("expected Bybit API error")
	}
}
