package news

import (
	"math"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

var protectedTokenPattern = regexp.MustCompile(`(?:\$[A-Z0-9]{2,}|[A-Z][A-Z0-9]{1,})`)

var similarityStopWords = map[string]bool{
	"a": true, "an": true, "and": true, "as": true, "at": true, "for": true,
	"from": true, "in": true, "of": true, "on": true, "the": true, "to": true,
	"with": true, "says": true, "announces": true, "announcement": true,
}

// EventSimilarity combines cheap lexical similarity with bounded context
// bonuses. Low lexical overlap can never merge merely because assets match.
func EventSimilarity(titleA, titleB string, assetsA, assetsB []int64, categoriesA, categoriesB []domain.NewsCategory, distance, window time.Duration) float64 {
	protectedA, protectedB := protectedTokens(titleA), protectedTokens(titleB)
	if len(protectedA) > 0 && len(protectedB) > 0 && !intersectsString(protectedA, protectedB) {
		return 0
	}
	tokensA, tokensB := similarityTokens(titleA), similarityTokens(titleB)
	jaccard := setJaccard(tokensA, tokensB)
	dice := bigramDice(strings.Join(tokensA, " "), strings.Join(tokensB, " "))
	lexical := 0.7*jaccard + 0.3*dice
	if lexical < 0.42 {
		return lexical
	}
	score := 0.85 * lexical
	if intersectsInt64(assetsA, assetsB) {
		score += 0.07
	}
	if intersectsCategory(categoriesA, categoriesB) {
		score += 0.04
	}
	if window > 0 {
		proximity := 1 - math.Min(math.Abs(float64(distance))/float64(window), 1)
		score += 0.04 * proximity
	}
	return clamp01(score)
}

func protectedTokens(title string) []string {
	matches := protectedTokenPattern.FindAllString(title, -1)
	seen := make(map[string]bool, len(matches))
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		match = strings.TrimPrefix(match, "$")
		if match == "USDT" || match == "USD" || match == "CEO" || seen[match] {
			continue
		}
		seen[match] = true
		out = append(out, match)
	}
	return out
}

func similarityTokens(value string) []string {
	value = NormalizeTitle(value)
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '$'
	})
	seen := make(map[string]bool, len(fields))
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if len([]rune(field)) < 2 || similarityStopWords[field] || seen[field] {
			continue
		}
		seen[field] = true
		out = append(out, field)
	}
	return out
}

func setJaccard(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	set := make(map[string]bool, len(a))
	for _, value := range a {
		set[value] = true
	}
	intersection := 0
	for _, value := range b {
		if set[value] {
			intersection++
		} else {
			set[value] = true
		}
	}
	return float64(intersection) / float64(len(set))
}

func bigramDice(a, b string) float64 {
	aRunes, bRunes := []rune(a), []rune(b)
	if len(aRunes) < 2 || len(bRunes) < 2 {
		if a == b && a != "" {
			return 1
		}
		return 0
	}
	counts := make(map[string]int, len(aRunes)-1)
	for i := 0; i < len(aRunes)-1; i++ {
		counts[string(aRunes[i:i+2])]++
	}
	intersection := 0
	for i := 0; i < len(bRunes)-1; i++ {
		key := string(bRunes[i : i+2])
		if counts[key] > 0 {
			intersection++
			counts[key]--
		}
	}
	return 2 * float64(intersection) / float64((len(aRunes)-1)+(len(bRunes)-1))
}

func intersectsInt64(a, b []int64) bool {
	set := make(map[int64]bool, len(a))
	for _, value := range a {
		set[value] = true
	}
	for _, value := range b {
		if set[value] {
			return true
		}
	}
	return false
}

func intersectsCategory(a, b []domain.NewsCategory) bool {
	set := make(map[domain.NewsCategory]bool, len(a))
	for _, value := range a {
		set[value] = true
	}
	for _, value := range b {
		if set[value] {
			return true
		}
	}
	return false
}

func intersectsString(a, b []string) bool {
	set := make(map[string]bool, len(a))
	for _, value := range a {
		set[value] = true
	}
	for _, value := range b {
		if set[value] {
			return true
		}
	}
	return false
}
