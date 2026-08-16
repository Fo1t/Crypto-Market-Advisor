// Package bybit implements the public V5 spot market-data API. These
// endpoints deliberately require neither an account nor API credentials.
package bybit

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/crypto-market-advisor/advisor/internal/config"
	"github.com/crypto-market-advisor/advisor/internal/domain"
	"github.com/crypto-market-advisor/advisor/internal/httpx"
	"github.com/crypto-market-advisor/advisor/internal/logging"
)

// Client is the public Bybit V5 market data client. It needs no API key and
// only ever reads market endpoints.
type Client struct {
	cfg               config.BybitMarketConfig
	http              *httpx.Client
	spotInstruments   instrumentCatalog
	linearInstruments instrumentCatalog
}

type instrumentCatalog struct {
	sync.RWMutex
	values    map[string]bool
	expiresAt time.Time
}

// SupportsSpotSymbol validates the configured mapping against the exchange's
// public instruments catalog. The complete spot catalog is cached for an hour.
func (c *Client) SupportsSpotSymbol(ctx context.Context, symbol string) (bool, error) {
	return c.supportsSymbol(ctx, symbol, "spot", &c.spotInstruments)
}

// SupportsLinearSymbol checks the public USDT perpetual catalog. Linear data
// is a secondary exchange-native source for assets which have no spot market.
func (c *Client) SupportsLinearSymbol(ctx context.Context, symbol string) (bool, error) {
	return c.supportsSymbol(ctx, symbol, "linear", &c.linearInstruments)
}

func (c *Client) supportsSymbol(ctx context.Context, symbol, category string, catalog *instrumentCatalog) (bool, error) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	catalog.RLock()
	if time.Now().Before(catalog.expiresAt) {
		value := catalog.values[symbol]
		catalog.RUnlock()
		return value, nil
	}
	catalog.RUnlock()
	type row struct {
		Symbol string `json:"symbol"`
		Status string `json:"status"`
	}
	values := make(map[string]bool)
	cursor := ""
	for page := 0; page < 10; page++ {
		var out envelope[struct {
			List           []row  `json:"list"`
			NextPageCursor string `json:"nextPageCursor"`
		}]
		q := url.Values{"category": {category}, "limit": {"1000"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		if err := c.get(ctx, "/v5/market/instruments-info", q, &out); err != nil {
			return false, fmt.Errorf("bybit %s instruments: %w", category, err)
		}
		if out.RetCode != 0 {
			return false, fmt.Errorf("bybit retCode %d: %s", out.RetCode, out.RetMsg)
		}
		for _, instrument := range out.Result.List {
			values[instrument.Symbol] = instrument.Status == "Trading"
		}
		if out.Result.NextPageCursor == "" || out.Result.NextPageCursor == cursor {
			break
		}
		cursor = out.Result.NextPageCursor
	}
	catalog.Lock()
	catalog.values, catalog.expiresAt = values, time.Now().Add(time.Hour)
	catalog.Unlock()
	return values[symbol], nil
}

// New builds a Bybit market data client with retries and rate limiting.
func New(cfg config.BybitMarketConfig, logger *slog.Logger) *Client {
	log := logging.For(logger, logging.CategoryMarketData).With(slog.String("provider", "bybit"))
	return &Client{cfg: cfg, http: httpx.New(httpx.Options{
		Timeout: cfg.Timeout, MaxRetries: cfg.MaxRetries, RetryBaseWait: cfg.RetryBaseWait,
		RateLimitRPM: cfg.RateLimitRPM, UserAgent: "crypto-market-advisor/1.0", Logger: log,
	})}
}

// Name identifies the provider in candle provenance and health output.
func (c *Client) Name() string { return "bybit" }

type envelope[T any] struct {
	RetCode int    `json:"retCode"`
	RetMsg  string `json:"retMsg"`
	Result  T      `json:"result"`
}

func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	endpoint := c.cfg.BaseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	if err := c.http.GetJSON(ctx, endpoint, nil, out); err != nil {
		return err
	}
	return nil
}

// Ping performs the cheapest public request that proves the API is reachable.
func (c *Client) Ping(ctx context.Context) error {
	q := url.Values{"category": {"spot"}, "symbol": {"BTCUSDT"}}
	var out envelope[struct {
		List []struct {
			Symbol string `json:"symbol"`
		} `json:"list"`
	}]
	if err := c.get(ctx, "/v5/market/instruments-info", q, &out); err != nil {
		return fmt.Errorf("bybit instruments: %w", err)
	}
	if out.RetCode != 0 {
		return fmt.Errorf("bybit retCode %d: %s", out.RetCode, out.RetMsg)
	}
	if len(out.Result.List) != 1 || out.Result.List[0].Symbol != "BTCUSDT" {
		return fmt.Errorf("bybit BTCUSDT instrument unavailable")
	}
	return nil
}

// SpotTickers fetches the public spot ticker list once so all tracked symbols
// share one request and one exchange timestamp.
func (c *Client) SpotTickers(ctx context.Context) (map[string]domain.MarketInfo, error) {
	type row struct {
		Symbol       string `json:"symbol"`
		LastPrice    string `json:"lastPrice"`
		Price24hPcnt string `json:"price24hPcnt"`
		Volume24h    string `json:"volume24h"`
		Turnover24h  string `json:"turnover24h"`
		HighPrice24h string `json:"highPrice24h"`
		LowPrice24h  string `json:"lowPrice24h"`
	}
	var out envelope[struct {
		List []row `json:"list"`
	}]
	if err := c.get(ctx, "/v5/market/tickers", url.Values{"category": {"spot"}}, &out); err != nil {
		return nil, fmt.Errorf("bybit tickers: %w", err)
	}
	if out.RetCode != 0 {
		return nil, fmt.Errorf("bybit retCode %d: %s", out.RetCode, out.RetMsg)
	}
	result := make(map[string]domain.MarketInfo, len(out.Result.List))
	for _, item := range out.Result.List {
		last, err := parse(item.LastPrice)
		if err != nil || last <= 0 {
			continue
		}
		change, _ := parse(item.Price24hPcnt)
		volume, _ := parse(item.Volume24h)
		turnover, _ := parse(item.Turnover24h)
		high, _ := parse(item.HighPrice24h)
		low, _ := parse(item.LowPrice24h)
		_ = volume // spot base volume is not mixed with the quote-denominated market overview
		result[item.Symbol] = domain.MarketInfo{Symbol: item.Symbol, Price: last, PriceChange24hPct: change * 100, Volume24h: turnover, High24h: &high, Low24h: &low, FetchedAt: time.Now().UTC()}
	}
	return result, nil
}

var intervals = map[domain.Timeframe]string{domain.TF1m: "1", domain.TF5m: "5", domain.TF15m: "15", domain.TF1h: "60", domain.TF4h: "240", domain.TF1d: "D"}

// Klines returns every public spot bar in [from,to], paging backward over the
// reverse-chronological V5 response and excluding a still-forming candle.
func (c *Client) Klines(ctx context.Context, symbol string, tf domain.Timeframe, from, to time.Time) ([]domain.Candle, error) {
	return c.klines(ctx, "spot", symbol, tf, from, to)
}

// LinearKlines returns public linear perpetual bars for assets without a spot
// pair. Both market categories use the same Bybit OHLCV response contract.
func (c *Client) LinearKlines(ctx context.Context, symbol string, tf domain.Timeframe, from, to time.Time) ([]domain.Candle, error) {
	return c.klines(ctx, "linear", symbol, tf, from, to)
}

func (c *Client) klines(ctx context.Context, category, symbol string, tf domain.Timeframe, from, to time.Time) ([]domain.Candle, error) {
	interval, ok := intervals[tf]
	if !ok {
		return nil, fmt.Errorf("unsupported Bybit timeframe %s", tf)
	}
	from, to = from.UTC(), to.UTC()
	byTime := map[int64]domain.Candle{}
	end := to
	for page := 0; page < 100; page++ {
		q := url.Values{"category": {category}, "symbol": {strings.ToUpper(symbol)}, "interval": {interval}, "start": {strconv.FormatInt(from.UnixMilli(), 10)}, "end": {strconv.FormatInt(end.UnixMilli(), 10)}, "limit": {"1000"}}
		var out envelope[struct {
			List [][]string `json:"list"`
		}]
		if err := c.get(ctx, "/v5/market/kline", q, &out); err != nil {
			return nil, fmt.Errorf("bybit kline %s %s: %w", symbol, tf, err)
		}
		if out.RetCode != 0 {
			return nil, fmt.Errorf("bybit retCode %d for %s: %s", out.RetCode, symbol, out.RetMsg)
		}
		if len(out.Result.List) == 0 {
			break
		}
		oldest := end
		for _, row := range out.Result.List {
			candle, err := parseCandle(row, tf)
			if err != nil {
				continue
			}
			if candle.OpenTime.Before(from) || candle.OpenTime.After(to) {
				continue
			}
			byTime[candle.OpenTime.UnixMilli()] = candle
			if candle.OpenTime.Before(oldest) {
				oldest = candle.OpenTime
			}
		}
		if len(out.Result.List) < 1000 || !oldest.After(from) {
			break
		}
		end = oldest.Add(-time.Millisecond)
	}
	result := make([]domain.Candle, 0, len(byTime))
	for _, candle := range byTime {
		result = append(result, candle)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].OpenTime.Before(result[j].OpenTime) })
	return result, nil
}

func parseCandle(row []string, tf domain.Timeframe) (domain.Candle, error) {
	if len(row) < 7 {
		return domain.Candle{}, fmt.Errorf("short kline row")
	}
	startMS, err := strconv.ParseInt(row[0], 10, 64)
	if err != nil {
		return domain.Candle{}, err
	}
	values := make([]float64, 6)
	for i := range values {
		values[i], err = parse(row[i+1])
		if err != nil {
			return domain.Candle{}, err
		}
	}
	openTime := time.UnixMilli(startMS).UTC()
	closeTime := openTime.Add(tf.Duration())
	return domain.Candle{OpenTime: openTime, CloseTime: closeTime, Open: values[0], High: values[1], Low: values[2], Close: values[3], Volume: values[4], Turnover: values[5], Closed: !closeTime.After(time.Now().UTC()), Source: domain.CandleSourceNative, Provider: "bybit"}, nil
}

func parse(value string) (float64, error) { return strconv.ParseFloat(value, 64) }

// FundingInterval is how often a linear perpetual settles funding on Bybit.
// The public history endpoint returns one entry per settlement, and the value
// is used to page backwards through it.
const FundingInterval = 8 * time.Hour

// FundingHistory returns the settled funding rates of one linear perpetual in
// the given window, oldest first.
//
// This is a public endpoint: it needs no API key and says nothing about any
// account. The rate is returned as a fraction of notional per settlement, which
// is how the exchange states it; the caller converts to whatever unit it needs.
func (c *Client) FundingHistory(ctx context.Context, symbol string, from, to time.Time) ([]domain.FundingRate, error) {
	from, to = from.UTC(), to.UTC()
	if !to.After(from) {
		return nil, nil
	}
	byTime := map[int64]domain.FundingRate{}
	end := to

	// Each page returns the newest 200 settlements before end, so the window is
	// walked backwards. The page cap bounds a request that would otherwise run
	// for years of history in one call.
	for page := 0; page < 60; page++ {
		q := url.Values{
			"category":  {"linear"},
			"symbol":    {strings.ToUpper(symbol)},
			"startTime": {strconv.FormatInt(from.UnixMilli(), 10)},
			"endTime":   {strconv.FormatInt(end.UnixMilli(), 10)},
			"limit":     {"200"},
		}
		var out envelope[struct {
			List []struct {
				Symbol      string `json:"symbol"`
				FundingRate string `json:"fundingRate"`
				Timestamp   string `json:"fundingRateTimestamp"`
			} `json:"list"`
		}]
		if err := c.get(ctx, "/v5/market/funding/history", q, &out); err != nil {
			return nil, fmt.Errorf("bybit funding history %s: %w", symbol, err)
		}
		if out.RetCode != 0 {
			return nil, fmt.Errorf("bybit retCode %d for %s funding: %s", out.RetCode, symbol, out.RetMsg)
		}
		if len(out.Result.List) == 0 {
			break
		}

		oldest := end
		for _, row := range out.Result.List {
			ms, err := strconv.ParseInt(row.Timestamp, 10, 64)
			if err != nil {
				continue
			}
			rate, err := parse(row.FundingRate)
			if err != nil {
				continue
			}
			at := time.UnixMilli(ms).UTC()
			if at.Before(from) || at.After(to) {
				continue
			}
			byTime[ms] = domain.FundingRate{SettledAt: at, Rate: rate}
			if at.Before(oldest) {
				oldest = at
			}
		}
		if len(out.Result.List) < 200 || !oldest.After(from) {
			break
		}
		end = oldest.Add(-time.Millisecond)
	}

	result := make([]domain.FundingRate, 0, len(byTime))
	for _, rate := range byTime {
		result = append(result, rate)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].SettledAt.Before(result[j].SettledAt) })
	return result, nil
}
