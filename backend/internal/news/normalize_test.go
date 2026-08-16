package news

import "testing"

func TestNormalizeURLRemovesOnlyKnownTracking(t *testing.T) {
	got, err := NormalizeURL("HTTPS://Example.COM:443/article?id=42&utm_source=x&fbclid=y#comments")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://example.com/article?id=42"
	if got != want {
		t.Fatalf("NormalizeURL() = %q, want %q", got, want)
	}
}

func TestNormalizeTitlePreservesMeaningfulTokens(t *testing.T) {
	got := NormalizeTitle("  [BREAKING]   BTC ETF draws $100M!  ")
	want := "breaking] btc etf draws $100m"
	if got != want {
		t.Fatalf("NormalizeTitle() = %q, want %q", got, want)
	}
	if TitleHash(got) != TitleHash(NormalizeTitle("[BREAKING] BTC ETF draws $100M!")) {
		t.Fatal("equivalent titles must have the same hash")
	}
}
