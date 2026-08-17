// Package coingecko implements the market metadata provider: capitalisation,
// its ranking, and therefore the automatic asset universe.
//
// Two endpoints are called, both public and both usable without a key:
//   - /ping             liveness, for the health endpoint
//   - /coins/markets    list + market cap + rank + 24h stats
//
// It is deliberately not a candle source. The free tier exposes no native
// minute-level OHLC, and a series reconstructed from its sampled prices looks
// real without being real; prices and candles come from the exchange instead,
// and an asset the exchange does not list is reported as untradable. See
// docs/data-sources.md for the full division of labour.
package coingecko

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/crypto-market-advisor/advisor/internal/config"
	"github.com/crypto-market-advisor/advisor/internal/domain"
	"github.com/crypto-market-advisor/advisor/internal/httpx"
	"github.com/crypto-market-advisor/advisor/internal/logging"
)

// Client talks to the CoinGecko REST API.
type Client struct {
	cfg   config.CoinGeckoConfig
	http  *httpx.Client
	log   *slog.Logger
	cache *cache
}

// New builds a CoinGecko client.
func New(cfg config.CoinGeckoConfig, logger *slog.Logger) *Client {
	log := logging.For(logger, logging.CategoryMarketData).With(slog.String("provider", "coingecko"))
	return &Client{
		cfg: cfg,
		http: httpx.New(httpx.Options{
			Timeout:       cfg.Timeout,
			MaxRetries:    cfg.MaxRetries,
			RetryBaseWait: cfg.RetryBaseWait,
			RateLimitRPM:  cfg.RateLimitRPM,
			UserAgent:     "crypto-market-advisor/1.0",
			Logger:        log,
		}),
		log:   log,
		cache: newCache(cfg.CacheTTL),
	}
}

// Name identifies the provider in health output.
func (c *Client) Name() string { return "coingecko" }

func (c *Client) headers() map[string]string {
	if c.cfg.APIKey == "" {
		return nil
	}
	header := c.cfg.APIKeyHeader
	if header == "" {
		header = "x-cg-demo-api-key"
	}
	return map[string]string{header: c.cfg.APIKey}
}

func (c *Client) url(path string, query url.Values) string {
	u := c.cfg.BaseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	return u
}

// Ping checks connectivity.
func (c *Client) Ping(ctx context.Context) error {
	var out struct {
		GeckoSays string `json:"gecko_says"`
	}
	if err := c.http.GetJSON(ctx, c.url("/ping", nil), c.headers(), &out); err != nil {
		return fmt.Errorf("coingecko ping: %w", err)
	}
	return nil
}

// marketRow mirrors the /coins/markets response entry.
type marketRow struct {
	ID                    string   `json:"id"`
	Symbol                string   `json:"symbol"`
	Name                  string   `json:"name"`
	CurrentPrice          float64  `json:"current_price"`
	MarketCap             float64  `json:"market_cap"`
	MarketCapRank         int      `json:"market_cap_rank"`
	TotalVolume           float64  `json:"total_volume"`
	High24h               *float64 `json:"high_24h"`
	Low24h                *float64 `json:"low_24h"`
	PriceChangePct24h     *float64 `json:"price_change_percentage_24h"`
	PriceChangePct1hCurr  *float64 `json:"price_change_percentage_1h_in_currency"`
	PriceChangePct24hCurr *float64 `json:"price_change_percentage_24h_in_currency"`
	PriceChangePct7dCurr  *float64 `json:"price_change_percentage_7d_in_currency"`
}

// TopMarkets returns the top assets by market capitalisation.
func (c *Client) TopMarkets(ctx context.Context, limit int) ([]domain.MarketInfo, error) {
	if limit <= 0 {
		limit = 20
	}
	perPage := limit
	if perPage > 250 {
		perPage = 250
	}
	q := url.Values{}
	q.Set("vs_currency", c.cfg.VSCurrency)
	q.Set("order", "market_cap_desc")
	q.Set("per_page", fmt.Sprint(perPage))
	q.Set("page", "1")
	q.Set("sparkline", "false")
	q.Set("price_change_percentage", "1h,24h,7d")

	return c.fetchMarkets(ctx, q, "top_markets")
}

// MarketsByIDs returns market data for specific CoinGecko IDs.
func (c *Client) MarketsByIDs(ctx context.Context, ids []string) ([]domain.MarketInfo, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	q := url.Values{}
	q.Set("vs_currency", c.cfg.VSCurrency)
	q.Set("ids", strings.Join(ids, ","))
	q.Set("sparkline", "false")
	q.Set("price_change_percentage", "1h,24h,7d")
	q.Set("per_page", "250")

	return c.fetchMarkets(ctx, q, "markets:"+strings.Join(ids, ","))
}

func (c *Client) fetchMarkets(ctx context.Context, q url.Values, cacheKey string) ([]domain.MarketInfo, error) {
	if cached, ok := c.cache.get(cacheKey); ok {
		if infos, ok := cached.([]domain.MarketInfo); ok {
			return infos, nil
		}
	}

	var rows []marketRow
	if err := c.http.GetJSON(ctx, c.url("/coins/markets", q), c.headers(), &rows); err != nil {
		return nil, fmt.Errorf("coingecko markets: %w", err)
	}

	now := time.Now().UTC()
	out := make([]domain.MarketInfo, 0, len(rows))
	for _, r := range rows {
		info := domain.MarketInfo{
			CoinGeckoID:   r.ID,
			Symbol:        strings.ToUpper(r.Symbol),
			Name:          r.Name,
			Price:         r.CurrentPrice,
			MarketCap:     r.MarketCap,
			MarketCapRank: r.MarketCapRank,
			Volume24h:     r.TotalVolume,
			High24h:       r.High24h,
			Low24h:        r.Low24h,
			FetchedAt:     now,
		}
		switch {
		case r.PriceChangePct24hCurr != nil:
			info.PriceChange24hPct = *r.PriceChangePct24hCurr
		case r.PriceChangePct24h != nil:
			info.PriceChange24hPct = *r.PriceChangePct24h
		}
		info.PriceChange1hPct = r.PriceChangePct1hCurr
		info.PriceChange7dPct = r.PriceChangePct7dCurr
		out = append(out, info)
	}

	c.cache.set(cacheKey, out)
	return out, nil
}

// cache is a tiny TTL cache guarding the rate limit budget.
type cache struct {
	mu    sync.RWMutex
	ttl   time.Duration
	items map[string]cacheItem
}

type cacheItem struct {
	value     any
	expiresAt time.Time
}

func newCache(ttl time.Duration) *cache {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	return &cache{ttl: ttl, items: map[string]cacheItem{}}
}

func (c *cache) get(key string) (any, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	item, ok := c.items[key]
	if !ok || time.Now().After(item.expiresAt) {
		return nil, false
	}
	return item.value, true
}

func (c *cache) set(key string, value any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.items) > 500 {
		c.items = map[string]cacheItem{}
	}
	c.items[key] = cacheItem{value: value, expiresAt: time.Now().Add(c.ttl)}
}
