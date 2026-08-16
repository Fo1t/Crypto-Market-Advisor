package news

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

var trackingQueryKeys = map[string]struct{}{
	"fbclid": {}, "gclid": {}, "dclid": {}, "msclkid": {},
	"mc_cid": {}, "mc_eid": {}, "igshid": {}, "ref_src": {},
}

// NormalizeURL removes fragments and known analytics parameters while
// retaining query parameters that may identify the actual article.
func NormalizeURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	if (u.Scheme == "https" && u.Port() == "443") || (u.Scheme == "http" && u.Port() == "80") {
		u.Host = u.Hostname()
	}
	query := u.Query()
	for key := range query {
		lower := strings.ToLower(key)
		_, known := trackingQueryKeys[lower]
		if known || strings.HasPrefix(lower, "utm_") {
			query.Del(key)
		}
	}
	u.RawQuery = query.Encode()
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String(), nil
}

// NormalizeTitle performs conservative Unicode and whitespace normalization.
// It preserves meaningful symbols such as '$' and digits used in tickers and
// monetary amounts.
func NormalizeTitle(title string) string {
	title = norm.NFKC.String(title)
	title = strings.ToLower(strings.Join(strings.Fields(title), " "))
	return strings.TrimFunc(title, func(r rune) bool {
		return unicode.IsPunct(r) && r != '$'
	})
}

// TitleHash is the stable exact-dedup fingerprint of a normalized title.
func TitleHash(normalizedTitle string) string {
	sum := sha256.Sum256([]byte(normalizedTitle))
	return hex.EncodeToString(sum[:])
}
