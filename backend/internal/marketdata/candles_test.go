package marketdata

import (
	"testing"
	"time"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

func ts(minutes int) time.Time {
	return time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(minutes) * time.Minute)
}

func TestClosedOnlyFiltersFormingCandle(t *testing.T) {
	candles := []domain.Candle{
		{OpenTime: ts(0), Closed: true},
		{OpenTime: ts(5), Closed: true},
		{OpenTime: ts(10), Closed: false},
	}
	got := ClosedOnly(candles)
	if len(got) != 2 {
		t.Fatalf("expected the forming candle to be dropped, got %d candles", len(got))
	}
}

func TestBackfillCoversEveryAnalysedTimeframe(t *testing.T) {
	want := []domain.Timeframe{domain.TF1m, domain.TF5m, domain.TF15m, domain.TF1h, domain.TF4h, domain.TF1d}
	for _, tf := range want {
		found := false
		for _, planned := range backfillTimeframes {
			found = found || planned == tf
		}
		if !found {
			t.Fatalf("the backfill must cover %s: every candle now comes from the exchange, so a gap here is a gap in the analysis", tf)
		}
		if bybitHistoryWindow[tf] <= 0 {
			t.Fatalf("no history window configured for %s", tf)
		}
	}
	if bybitHistoryWindow[domain.TF1m] < 48*time.Hour {
		t.Fatalf("native 1m history window is too short: %s", bybitHistoryWindow[domain.TF1m])
	}
}

func TestIsTradableAssetFiltersStablecoinsAndWrappers(t *testing.T) {
	cases := []struct {
		symbol, name string
		want         bool
	}{
		{"BTC", "Bitcoin", true},
		{"ETH", "Ethereum", true},
		{"SOL", "Solana", true},
		{"USDT", "Tether", false},
		{"USDC", "USD Coin", false},
		{"WBTC", "Wrapped Bitcoin", false},
		{"STETH", "Lido Staked Ether", false},
		{"DAI", "Dai", false},
	}
	for _, c := range cases {
		got := IsTradableAsset(domain.MarketInfo{Symbol: c.symbol, Name: c.name})
		if got != c.want {
			t.Fatalf("%s (%s): expected tradable=%v, got %v", c.symbol, c.name, c.want, got)
		}
	}
}
