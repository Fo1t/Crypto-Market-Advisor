package news

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

type categoryRule struct {
	category   domain.NewsCategory
	confidence float64
	keywords   []string
}

var categoryRules = []categoryRule{
	{domain.NewsCategoryHack, 0.95, []string{" hacked", "hack ", "hack:", "breach", "stolen funds", "cyberattack"}},
	{domain.NewsCategoryExploit, 0.94, []string{"exploit", "vulnerability", "drained", "attack vector"}},
	{domain.NewsCategorySecurity, 0.82, []string{"security incident", "security breach", "compromised", "phishing", "malware"}},
	{domain.NewsCategoryTradingSuspension, 0.96, []string{"trading suspension", "suspend trading", "suspends trading", "suspended trading", "trading halted", "halt deposits", "halt withdrawals"}},
	{domain.NewsCategoryDelisting, 0.96, []string{"delist", "removal of trading", "remove trading pair", "contract delisting"}},
	{domain.NewsCategoryListing, 0.91, []string{"new listing", "will list", "listed on", "new trading pair", "listing of"}},
	{domain.NewsCategoryExchange, 0.78, []string{"exchange", "spot trading", "perpetual contract", "launchpool", "bybit"}},
	{domain.NewsCategoryLegal, 0.90, []string{"lawsuit", "court", "charged", "settlement", "indictment", "legal action"}},
	{domain.NewsCategoryRegulation, 0.88, []string{"regulation", "regulator", "sec ", "cftc", "compliance", "government ban", "regulatory approval"}},
	{domain.NewsCategoryNetworkOutage, 0.96, []string{"network outage", "network halt", "chain halted", "stopped producing blocks", "consensus failure"}},
	{domain.NewsCategoryNetworkUpgrade, 0.90, []string{"network upgrade", "protocol upgrade", "hard fork", "mainnet launch", "testnet launch"}},
	{domain.NewsCategoryProtocol, 0.75, []string{"protocol", "mainnet", "testnet", "validator", "blockchain network"}},
	{domain.NewsCategoryETF, 0.94, []string{" etf", "exchange-traded fund"}},
	{domain.NewsCategoryInstitutional, 0.80, []string{"institutional", "treasury reserve", "asset manager", "institutional adoption"}},
	{domain.NewsCategoryMacro, 0.83, []string{"federal reserve", "interest rate", "inflation", "jobs report", "central bank", "tariff", "gdp"}},
	{domain.NewsCategoryMining, 0.85, []string{"bitcoin mining", "crypto mining", "miner revenue", "mining difficulty", "hashrate"}},
	{domain.NewsCategoryStablecoin, 0.88, []string{"stablecoin", "depeg", "usdt", "usdc"}},
	{domain.NewsCategoryDeFi, 0.82, []string{"defi", "decentralized finance", "liquidity pool", "yield farming"}},
	{domain.NewsCategoryTokenomics, 0.84, []string{"token unlock", "token burn", "tokenomics", "vesting", "supply reduction"}},
	{domain.NewsCategoryPartnership, 0.78, []string{"partnership", "partners with", "collaboration", "strategic alliance"}},
	{domain.NewsCategoryMarket, 0.65, []string{"price", "market", "rally", "sell-off", "liquidation", "trading volume"}},
}

// ClassifyItem applies deterministic, non-LLM rules to provider metadata,
// title and summary. Multiple categories are intentional (e.g. exchange +
// delisting), each with an independently explainable confidence.
func ClassifyItem(item domain.NewsItem) []domain.NewsCategoryMatch {
	text := " " + strings.ToLower(item.Title+" "+item.Summary) + " "
	scores := make(map[domain.NewsCategory]float64)
	for _, rule := range categoryRules {
		for _, keyword := range rule.keywords {
			if strings.Contains(text, keyword) {
				if rule.confidence > scores[rule.category] {
					scores[rule.category] = rule.confidence
				}
				break
			}
		}
	}
	metadataType := strings.ToLower(metadataString(item.Metadata, "type"))
	switch {
	case strings.Contains(metadataType, "delist"):
		scores[domain.NewsCategoryExchange] = maxFloat(scores[domain.NewsCategoryExchange], 0.95)
		scores[domain.NewsCategoryDelisting] = maxFloat(scores[domain.NewsCategoryDelisting], 0.99)
	case strings.Contains(metadataType, "new_crypto"), strings.Contains(metadataType, "listing"):
		scores[domain.NewsCategoryExchange] = maxFloat(scores[domain.NewsCategoryExchange], 0.95)
		scores[domain.NewsCategoryListing] = maxFloat(scores[domain.NewsCategoryListing], 0.98)
	case strings.Contains(metadataType, "maintenance"):
		scores[domain.NewsCategoryExchange] = maxFloat(scores[domain.NewsCategoryExchange], 0.88)
	}
	if scores[domain.NewsCategoryHack] > 0 || scores[domain.NewsCategoryExploit] > 0 {
		scores[domain.NewsCategorySecurity] = maxFloat(scores[domain.NewsCategorySecurity], 0.92)
	}
	if len(scores) == 0 {
		scores[domain.NewsCategoryOther] = 0.5
	}
	out := make([]domain.NewsCategoryMatch, 0, len(scores))
	for category, confidence := range scores {
		out = append(out, domain.NewsCategoryMatch{Category: category, Confidence: confidence})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Confidence == out[j].Confidence {
			return out[i].Category < out[j].Category
		}
		return out[i].Confidence > out[j].Confidence
	})
	return out
}

// FreshnessScore uses a 12-hour half-life and never treats a slightly future
// provider timestamp as more than fully fresh.
func FreshnessScore(publishedAt, now time.Time) float64 {
	age := now.Sub(publishedAt).Hours()
	if age <= 0 {
		return 1
	}
	return clamp01(math.Exp(-math.Ln2 * age / 12))
}

var categoryImportance = map[domain.NewsCategory]float64{
	domain.NewsCategoryHack: 0.36, domain.NewsCategoryExploit: 0.34,
	domain.NewsCategoryNetworkOutage: 0.36, domain.NewsCategoryTradingSuspension: 0.34,
	domain.NewsCategoryDelisting: 0.32, domain.NewsCategoryETF: 0.30,
	domain.NewsCategoryRegulation: 0.28, domain.NewsCategoryLegal: 0.27,
	domain.NewsCategoryStablecoin: 0.27, domain.NewsCategoryNetworkUpgrade: 0.24,
	domain.NewsCategoryInstitutional: 0.22, domain.NewsCategoryTokenomics: 0.20,
	domain.NewsCategoryListing: 0.18, domain.NewsCategoryMacro: 0.18,
	domain.NewsCategoryProtocol: 0.15, domain.NewsCategoryExchange: 0.14,
	domain.NewsCategoryPartnership: 0.12, domain.NewsCategoryDeFi: 0.12,
	domain.NewsCategoryMining: 0.11, domain.NewsCategoryMarket: 0.08,
	domain.NewsCategoryOther: 0.03,
}

// ImportanceScore estimates potential event impact, not headline sentiment.
func ImportanceScore(sourcePriority, sourceCount int, categories []domain.NewsCategoryMatch, assets []domain.NewsAssetMatch) float64 {
	score := 0.10 + 0.22*clamp01(float64(sourcePriority)/100)
	if sourcePriority >= 90 {
		score += 0.10
	}
	categoryWeight := 0.0
	for _, category := range categories {
		categoryWeight = maxFloat(categoryWeight, categoryImportance[category.Category]*category.Confidence)
	}
	score += categoryWeight
	if len(assets) > 0 {
		score += 0.10 + math.Min(float64(len(assets)-1)*0.025, 0.075)
	}
	if sourceCount > 1 {
		score += math.Min(float64(sourceCount-1)*0.03, 0.12)
	}
	return clamp01(score)
}

// IsCriticalEvent is a risk/context flag, never an automatic trading command.
func IsCriticalEvent(item domain.NewsItem, categories []domain.NewsCategoryMatch, assets []domain.NewsAssetMatch) bool {
	has := make(map[domain.NewsCategory]bool, len(categories))
	for _, category := range categories {
		has[category.Category] = true
	}
	if has[domain.NewsCategoryNetworkOutage] || has[domain.NewsCategoryTradingSuspension] {
		return true
	}
	if len(assets) > 0 && has[domain.NewsCategoryDelisting] {
		return true
	}
	text := strings.ToLower(item.Title + " " + item.Summary)
	return (has[domain.NewsCategoryHack] || has[domain.NewsCategoryExploit]) &&
		(strings.Contains(text, "exchange") || strings.Contains(text, "protocol") || strings.Contains(text, "million") || strings.Contains(text, "billion"))
}

func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, _ := metadata[key].(string)
	return value
}

func clamp01(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
