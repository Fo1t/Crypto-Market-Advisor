package bybit

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crypto-market-advisor/advisor/internal/config"
	"github.com/crypto-market-advisor/advisor/internal/domain"
)

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return New(config.BybitMarketConfig{BaseURL: server.URL, Timeout: time.Second, RateLimitRPM: 1000}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestSpotTickersNormalizesQuoteTurnover(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"retCode":0,"retMsg":"OK","result":{"list":[{"symbol":"BTCUSDT","lastPrice":"65000","price24hPcnt":"0.0125","volume24h":"100","turnover24h":"6500000","highPrice24h":"66000","lowPrice24h":"63000"}]}}`)
	})
	items, err := client.SpotTickers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	btc := items["BTCUSDT"]
	if btc.Price != 65000 || btc.PriceChange24hPct != 1.25 || btc.Volume24h != 6500000 {
		t.Fatalf("unexpected ticker: %+v", btc)
	}
}

func TestKlinesSortsAndPreservesBaseVolumeAndQuoteTurnover(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Minute)
	older := now.Add(-10 * time.Minute)
	newer := now.Add(-5 * time.Minute)
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"retCode":0,"retMsg":"OK","result":{"list":[["%d","101","104","100","103","12","1234"],["%d","100","102","99","101","10","1005"]]}}`, newer.UnixMilli(), older.UnixMilli())
	})
	candles, err := client.Klines(context.Background(), "BTCUSDT", domain.TF5m, older.Add(-time.Minute), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(candles) != 2 {
		t.Fatalf("expected 2 closed candles, got %d", len(candles))
	}
	if !candles[0].OpenTime.Equal(older) || candles[0].Volume != 10 || candles[0].Turnover != 1005 || candles[0].Provider != "bybit" {
		t.Fatalf("unexpected first candle: %+v", candles[0])
	}
}

func TestLinearInstrumentAndKlinesUseLinearCategory(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Minute)
	open := now.Add(-5 * time.Minute)
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("category") != "linear" {
			t.Errorf("expected linear category, got %q", r.URL.Query().Get("category"))
		}
		switch r.URL.Path {
		case "/v5/market/instruments-info":
			_, _ = fmt.Fprint(w, `{"retCode":0,"retMsg":"OK","result":{"list":[{"symbol":"XMRUSDT","status":"Trading"}],"nextPageCursor":""}}`)
		case "/v5/market/kline":
			_, _ = fmt.Fprintf(w, `{"retCode":0,"retMsg":"OK","result":{"list":[["%d","100","102","99","101","7","707"]]}}`, open.UnixMilli())
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})

	supported, err := client.SupportsLinearSymbol(context.Background(), "xmrusdt")
	if err != nil || !supported {
		t.Fatalf("expected XMRUSDT linear support, supported=%v err=%v", supported, err)
	}
	candles, err := client.LinearKlines(context.Background(), "XMRUSDT", domain.TF5m, open.Add(-time.Minute), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(candles) != 1 || candles[0].Volume != 7 || candles[0].Turnover != 707 || candles[0].Provider != "bybit" {
		t.Fatalf("unexpected linear candle: %+v", candles)
	}
}
