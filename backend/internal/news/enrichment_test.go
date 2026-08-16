package news

import (
	"testing"
	"time"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

func TestAssetMatcherAvoidsTickerSubstrings(t *testing.T) {
	matcher := NewAssetMatcher([]domain.Asset{
		{ID: 1, Symbol: "BTC", DisplayName: "Bitcoin", CoinGeckoID: "bitcoin", BybitSymbol: "BTCUSDT"},
		{ID: 2, Symbol: "LINK", DisplayName: "Chainlink", CoinGeckoID: "chainlink", BybitSymbol: "LINKUSDT"},
	}, nil)

	matches := matcher.Match(domain.NewsItem{Title: "A new link between payment networks"})
	if len(matches) != 0 {
		t.Fatalf("ordinary lowercase word produced ticker match: %+v", matches)
	}
	matches = matcher.Match(domain.NewsItem{Title: "LINK price rises after Chainlink upgrade"})
	if len(matches) != 1 || matches[0].AssetID != 2 {
		t.Fatalf("contextual LINK was not matched: %+v", matches)
	}
	matches = matcher.Match(domain.NewsItem{Title: "Bitcoin adoption grows"})
	if len(matches) != 1 || matches[0].AssetID != 1 || matches[0].MatchedBy != "display_name" {
		t.Fatalf("full asset name was not matched: %+v", matches)
	}
	matches = matcher.Match(domain.NewsItem{Title: "New pair", Metadata: map[string]any{"tags": []any{"BTC"}}})
	if len(matches) != 1 || matches[0].Confidence != 1 {
		t.Fatalf("provider tag was not preferred: %+v", matches)
	}
}

func TestClassifyImportanceFreshnessAndCritical(t *testing.T) {
	item := domain.NewsItem{Title: "Exchange suspends trading after protocol exploit", Summary: "Millions stolen"}
	categories := ClassifyItem(item)
	if !hasCategory(categories, domain.NewsCategoryExploit) ||
		!hasCategory(categories, domain.NewsCategorySecurity) ||
		!hasCategory(categories, domain.NewsCategoryTradingSuspension) {
		t.Fatalf("missing security categories: %+v", categories)
	}
	assets := []domain.NewsAssetMatch{{AssetID: 1, Symbol: "BTC", Confidence: 0.9}}
	if !IsCriticalEvent(item, categories, assets) {
		t.Fatal("trading suspension/exploit should be critical context")
	}
	importance := ImportanceScore(100, 3, categories, assets)
	if importance < 0.8 || importance > 1 {
		t.Fatalf("unexpected importance: %v", importance)
	}
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	if fresh := FreshnessScore(now.Add(-12*time.Hour), now); fresh < 0.49 || fresh > 0.51 {
		t.Fatalf("12-hour half-life broken: %v", fresh)
	}
}

func TestEventSimilarityNeedsLexicalEvidence(t *testing.T) {
	window := 6 * time.Hour
	related := EventSimilarity(
		"SEC approves spot Bitcoin ETF applications",
		"Spot Bitcoin ETF applications approved by SEC",
		[]int64{1}, []int64{1},
		[]domain.NewsCategory{domain.NewsCategoryETF}, []domain.NewsCategory{domain.NewsCategoryETF},
		20*time.Minute, window,
	)
	if related < 0.72 {
		t.Fatalf("related headlines score too low: %v", related)
	}
	unrelated := EventSimilarity(
		"Bitcoin mining difficulty reaches record high",
		"Bitcoin ETF records daily inflows",
		[]int64{1}, []int64{1},
		[]domain.NewsCategory{domain.NewsCategoryMining}, []domain.NewsCategory{domain.NewsCategoryETF},
		10*time.Minute, window,
	)
	if unrelated >= 0.72 {
		t.Fatalf("asset/time bonuses merged unrelated events: %v", unrelated)
	}
	differentListings := EventSimilarity(
		"New listing: NAVERUSDT TradFi Perpetual Contract, with up to 25x leverage",
		"New listing: SAMSUNGEMUSDT TradFi Perpetual Contract, with up to 25x leverage",
		nil, nil,
		[]domain.NewsCategory{domain.NewsCategoryListing}, []domain.NewsCategory{domain.NewsCategoryListing},
		5*time.Minute, window,
	)
	if differentListings != 0 {
		t.Fatalf("different protected instrument identifiers merged: %v", differentListings)
	}
}

func hasCategory(matches []domain.NewsCategoryMatch, category domain.NewsCategory) bool {
	for _, match := range matches {
		if match.Category == category {
			return true
		}
	}
	return false
}
