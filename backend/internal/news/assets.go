package news

import (
	"regexp"
	"sort"
	"strings"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

var ambiguousTickers = map[string]bool{
	"ONE": true, "LINK": true, "TON": true, "NEAR": true, "OP": true, "ARB": true,
}

var cryptoContextWords = []string{
	"crypto", "token", "coin", "blockchain", "network", "mainnet", "wallet",
	"exchange", "trading", "price", "listing", "delisting", "perpetual", "usdt",
}

type assetAlias struct {
	asset         domain.Asset
	value         string
	matchedBy     string
	confidence    float64
	caseSensitive bool
	contextual    bool
}

// AssetMatcher builds conservative aliases from the existing asset registry.
type AssetMatcher struct {
	aliases []assetAlias
}

// NewAssetMatcher builds the alias index used to link news to tracked assets.
func NewAssetMatcher(assets []domain.Asset, custom map[int64][]string) *AssetMatcher {
	aliases := make([]assetAlias, 0, len(assets)*5)
	for _, asset := range assets {
		symbol := strings.ToUpper(strings.TrimSpace(asset.Symbol))
		if symbol != "" {
			aliases = append(aliases, assetAlias{asset: asset, value: symbol, matchedBy: "symbol", confidence: 0.86, caseSensitive: true, contextual: ambiguousTickers[symbol]})
			aliases = append(aliases, assetAlias{asset: asset, value: "$" + symbol, matchedBy: "cashtag", confidence: 0.94, caseSensitive: false})
		}
		if display := strings.TrimSpace(asset.DisplayName); display != "" && !strings.EqualFold(display, symbol) {
			aliases = append(aliases, assetAlias{asset: asset, value: display, matchedBy: "display_name", confidence: 0.96})
		}
		if providerID := strings.ReplaceAll(strings.TrimSpace(asset.CoinGeckoID), "-", " "); len(providerID) >= 4 && !strings.EqualFold(providerID, asset.DisplayName) {
			aliases = append(aliases, assetAlias{asset: asset, value: providerID, matchedBy: "provider_id", confidence: 0.88})
		}
		if bybit := strings.ToUpper(strings.TrimSpace(asset.BybitSymbol)); bybit != "" {
			aliases = append(aliases, assetAlias{asset: asset, value: bybit, matchedBy: "bybit_symbol", confidence: 0.98, caseSensitive: true})
		}
		for _, alias := range custom[asset.ID] {
			if alias = strings.TrimSpace(alias); alias != "" {
				aliases = append(aliases, assetAlias{asset: asset, value: alias, matchedBy: "custom_alias", confidence: 0.92})
			}
		}
	}
	return &AssetMatcher{aliases: aliases}
}

// Match uses provider tags first and whole-token text matching second. A ticker
// is never searched as a raw substring.
func (m *AssetMatcher) Match(item domain.NewsItem) []domain.NewsAssetMatch {
	text := item.Title + " " + item.Summary
	lowerText := strings.ToLower(text)
	contextual := containsAny(lowerText, cryptoContextWords)
	tags := metadataTags(item.Metadata)
	best := make(map[int64]domain.NewsAssetMatch)
	for _, alias := range m.aliases {
		matched, confidence, matchedBy := false, alias.confidence, alias.matchedBy
		for _, tag := range tags {
			if strings.EqualFold(tag, alias.asset.Symbol) || strings.EqualFold(tag, alias.asset.BybitSymbol) {
				matched, confidence, matchedBy = true, 1, "provider_tag"
				break
			}
		}
		if !matched {
			if alias.contextual && !contextual {
				continue
			}
			matched = containsWhole(text, alias.value, alias.caseSensitive)
		}
		if matched && confidence > best[alias.asset.ID].Confidence {
			best[alias.asset.ID] = domain.NewsAssetMatch{
				AssetID: alias.asset.ID, Symbol: alias.asset.Symbol,
				Confidence: confidence, MatchedBy: matchedBy,
			}
		}
	}
	out := make([]domain.NewsAssetMatch, 0, len(best))
	for _, match := range best {
		out = append(out, match)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AssetID < out[j].AssetID })
	return out
}

func containsWhole(text, value string, caseSensitive bool) bool {
	if value == "" {
		return false
	}
	if !caseSensitive {
		text, value = strings.ToLower(text), strings.ToLower(value)
	}
	pattern := `(^|[^\pL\pN])` + regexp.QuoteMeta(value) + `([^\pL\pN]|$)`
	return regexp.MustCompile(pattern).FindStringIndex(text) != nil
}

func containsAny(text string, words []string) bool {
	for _, word := range words {
		if strings.Contains(text, word) {
			return true
		}
	}
	return false
}

func metadataTags(metadata map[string]any) []string {
	if metadata == nil {
		return nil
	}
	switch raw := metadata["tags"].(type) {
	case []string:
		return raw
	case []any:
		out := make([]string, 0, len(raw))
		for _, value := range raw {
			if text, ok := value.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}
