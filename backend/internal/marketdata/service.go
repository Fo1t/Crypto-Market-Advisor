package marketdata

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/crypto-market-advisor/advisor/internal/config"
	"github.com/crypto-market-advisor/advisor/internal/domain"
	"github.com/crypto-market-advisor/advisor/internal/logging"
	"github.com/crypto-market-advisor/advisor/internal/marketdata/bybit"
	"github.com/crypto-market-advisor/advisor/internal/repository"
)

// Provider supplies the market metadata no exchange endpoint carries:
// capitalisation, its ranking, and therefore the automatic universe. Prices and
// candles come from the exchange itself - a fallback that quietly serves
// derived bars was more confusing than an asset that is honestly absent.
type Provider interface {
	Name() string
	Ping(ctx context.Context) error
	TopMarkets(ctx context.Context, limit int) ([]domain.MarketInfo, error)
	MarketsByIDs(ctx context.Context, ids []string) ([]domain.MarketInfo, error)
}

// BybitProvider is the public, unauthenticated exchange-data seam. Spot is the
// primary market; linear perpetual candles are used when no spot pair exists.
type BybitProvider interface {
	Name() string
	Ping(context.Context) error
	SpotTickers(context.Context) (map[string]domain.MarketInfo, error)
	SupportsSpotSymbol(context.Context, string) (bool, error)
	SupportsLinearSymbol(context.Context, string) (bool, error)
	Klines(context.Context, string, domain.Timeframe, time.Time, time.Time) ([]domain.Candle, error)
	LinearKlines(context.Context, string, domain.Timeframe, time.Time, time.Time) ([]domain.Candle, error)
	FundingHistory(context.Context, string, time.Time, time.Time) ([]domain.FundingRate, error)
}

// Status keys published to system_status.
const (
	StatusMarketData      = "market_data"
	StatusUniverseRefresh = "universe_refresh"
)

// Service ingests market data into the database.
type Service struct {
	provider Provider
	bybit    BybitProvider
	repos    *repository.Repositories
	cfg      config.CoinGeckoConfig
	analysis config.AnalysisConfig
	log      *slog.Logger

	mu          sync.RWMutex
	lastSuccess time.Time
	lastError   string
	healthy     bool

	// A manual history import is a single long-running job the user watches,
	// so it is kept apart from the health state it must not disturb.
	importMu     sync.Mutex
	importJob    *ImportProgress
	importCancel context.CancelFunc
}

// NewService builds the market data service.
func NewService(p Provider, bybit BybitProvider, repos *repository.Repositories, cfg config.CoinGeckoConfig, analysis config.AnalysisConfig, logger *slog.Logger) *Service {
	return &Service{
		provider: p,
		bybit:    bybit,
		repos:    repos,
		cfg:      cfg,
		analysis: analysis,
		log:      logging.For(logger, logging.CategoryMarketData),
	}
}

// HealthFreshness is how recently a successful call counts as proof of life.
// Within this window the health endpoint reports from the recorded state
// instead of issuing a ping, because the provider client is rate limited and a
// health check must never queue behind a backfill.
const HealthFreshness = 2 * time.Minute

// Health reports the provider state for the health endpoints.
func (s *Service) Health(ctx context.Context) (domain.ComponentStatus, string, time.Time) {
	s.mu.RLock()
	last, lastErr, healthy := s.lastSuccess, s.lastError, s.healthy
	s.mu.RUnlock()

	if healthy && time.Since(last) < HealthFreshness {
		return domain.ComponentOnline, "", last
	}

	pingCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	// Bybit carries the prices and candles, so its absence is what actually
	// stops the analysis; the metadata provider only decides which assets are
	// tracked and how they rank.
	if s.bybit != nil {
		if err := s.bybit.Ping(pingCtx); err != nil {
			if !last.IsZero() && time.Since(last) < 15*time.Minute {
				return domain.ComponentDegraded, "Bybit unavailable: " + err.Error(), last
			}
			return domain.ComponentOffline, "Bybit unavailable: " + err.Error(), last
		}
		if !healthy && lastErr != "" {
			return domain.ComponentDegraded, lastErr, last
		}
		return domain.ComponentOnline, "primary=bybit", last
	}
	if err := s.provider.Ping(pingCtx); err != nil {
		if !last.IsZero() && time.Since(last) < 15*time.Minute {
			// Data is still flowing; the probe itself was just starved or slow.
			return domain.ComponentDegraded, err.Error(), last
		}
		return domain.ComponentOffline, err.Error(), last
	}
	if !healthy && lastErr != "" {
		return domain.ComponentDegraded, lastErr, last
	}
	return domain.ComponentOnline, "", last
}

// LastSuccess reports when data was last ingested successfully.
func (s *Service) LastSuccess() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastSuccess
}

func (s *Service) recordSuccess(ctx context.Context) {
	now := time.Now().UTC()
	s.mu.Lock()
	s.lastSuccess, s.healthy, s.lastError = now, true, ""
	s.mu.Unlock()
	_ = s.repos.Status.Set(ctx, StatusMarketData, string(domain.ComponentOnline), "", now)
}

func (s *Service) recordFailure(ctx context.Context, err error) {
	s.mu.Lock()
	s.healthy, s.lastError = false, err.Error()
	s.mu.Unlock()
	_ = s.repos.Status.Set(ctx, StatusMarketData, string(domain.ComponentDegraded), err.Error(), time.Now().UTC())
}

func (s *Service) recordDegraded(ctx context.Context, message string) {
	now := time.Now().UTC()
	s.mu.Lock()
	s.lastSuccess, s.healthy, s.lastError = now, false, message
	s.mu.Unlock()
	_ = s.repos.Status.Set(ctx, StatusMarketData, string(domain.ComponentDegraded), message, now)
}

// stablecoins and wrapped/staked derivatives are excluded from the automatic
// universe: they are not independently tradable directional instruments.
var excludedSymbols = map[string]bool{
	"USDT": true, "USDC": true, "DAI": true, "TUSD": true, "FDUSD": true, "USDE": true,
	"PYUSD": true, "USDD": true, "USDS": true, "BUSD": true, "GUSD": true, "LUSD": true,
	"FRAX": true, "USD1": true, "RLUSD": true, "USDP": true, "EURC": true, "USDG": true,
	"WBTC": true, "WETH": true, "WBETH": true, "WEETH": true, "STETH": true, "WSTETH": true,
	"RETH": true, "CBETH": true, "CBBTC": true, "METH": true, "EZETH": true, "RSETH": true,
	"SOLVBTC": true, "LBTC": true, "BSC-USD": true, "SUSDE": true, "STBTC": true, "JITOSOL": true,
	"MSOL": true, "BNSOL": true, "WBNB": true, "WHYPE": true, "WSOL": true, "STETH.E": true,
}

var excludedNameParts = []string{
	"wrapped", "staked", "stablecoin", "bridged", "liquid staking", "restaked", "tether", "usd coin",
}

// IsTradableAsset reports whether an asset belongs in the automatic universe.
func IsTradableAsset(info domain.MarketInfo) bool {
	symbol := strings.ToUpper(info.Symbol)
	if excludedSymbols[symbol] {
		return false
	}
	name := strings.ToLower(info.Name)
	for _, part := range excludedNameParts {
		if strings.Contains(name, part) {
			return false
		}
	}
	return true
}

// RefreshUniverse syncs the automatic top-N list without ever discarding the
// user's manual choices: flags such as enabled/pinned/excluded are preserved,
// manually added assets are never removed, and excluded ones are never re-added.
func (s *Service) RefreshUniverse(ctx context.Context) error {
	// Fetch a wider slice than needed because stablecoins and wrapped tokens
	// will be filtered out of it.
	fetchLimit := s.cfg.UniverseSize * 3
	if fetchLimit > 250 {
		fetchLimit = 250
	}

	markets, err := s.provider.TopMarkets(ctx, fetchLimit)
	if err != nil {
		s.recordFailure(ctx, err)
		return fmt.Errorf("refresh universe: %w", err)
	}

	existing, err := s.repos.Assets.List(ctx, false)
	if err != nil {
		return fmt.Errorf("list assets: %w", err)
	}
	byCoinGeckoID := make(map[string]domain.Asset, len(existing))
	bySymbol := make(map[string]domain.Asset, len(existing))
	for _, a := range existing {
		byCoinGeckoID[a.CoinGeckoID] = a
		bySymbol[strings.ToUpper(a.Symbol)] = a
	}

	added, updated := 0, 0
	selected := 0
	for _, info := range markets {
		if selected >= s.cfg.UniverseSize {
			break
		}
		if !IsTradableAsset(info) {
			continue
		}
		// Capitalisation alone is not tradability: an asset the exchange does
		// not list has no candles here, and adding it would only produce a row
		// that can never be analysed.
		bybitSymbol := strings.ToUpper(info.Symbol) + "USDT"
		if !s.tradableOnExchange(ctx, bybitSymbol) {
			continue
		}
		selected++

		if current, ok := byCoinGeckoID[info.CoinGeckoID]; ok {
			if current.ExcludedFromAutoList {
				continue
			}
			rank := info.MarketCapRank
			if err := s.repos.Assets.UpdateRank(ctx, current.ID, rank, info.Name); err != nil {
				s.log.Warn("update asset rank failed", slog.String("symbol", current.Symbol), slog.String("error", err.Error()))
				continue
			}
			updated++
			continue
		}
		if _, ok := bySymbol[strings.ToUpper(info.Symbol)]; ok {
			continue // symbol already tracked under a different provider ID
		}

		rank := info.MarketCapRank
		asset := domain.Asset{
			CoinGeckoID:   info.CoinGeckoID,
			Symbol:        strings.ToUpper(info.Symbol),
			DisplayName:   info.Name,
			BybitSymbol:   bybitSymbol,
			Enabled:       true,
			MarketCapRank: &rank,
		}
		if _, err := s.repos.Assets.Create(ctx, asset); err != nil {
			s.log.Warn("create asset failed", slog.String("symbol", asset.Symbol), slog.String("error", err.Error()))
			continue
		}
		added++
	}

	s.log.Info("universe refreshed", slog.Int("added", added), slog.Int("updated", updated))
	_ = s.repos.Status.Set(ctx, StatusUniverseRefresh, string(domain.ComponentOnline),
		fmt.Sprintf("added=%d updated=%d", added, updated), time.Now().UTC())
	s.recordSuccess(ctx)
	return nil
}

// tradableOnExchange reports whether the exchange lists the pair at all. A
// failed lookup answers "yes": refusing an asset because one catalogue request
// timed out would silently shrink the universe on a flaky network.
func (s *Service) tradableOnExchange(ctx context.Context, symbol string) bool {
	if s.bybit == nil {
		return true
	}
	spot, err := s.bybit.SupportsSpotSymbol(ctx, symbol)
	if err != nil {
		s.log.Debug("spot catalogue lookup failed", slog.String("symbol", symbol), slog.String("error", err.Error()))
		return true
	}
	if spot {
		return true
	}
	linear, err := s.bybit.SupportsLinearSymbol(ctx, symbol)
	if err != nil {
		s.log.Debug("linear catalogue lookup failed", slog.String("symbol", symbol), slog.String("error", err.Error()))
		return true
	}
	return linear
}

// IngestPrices refreshes the market overview: prices from the exchange, and the
// capitalisation and ranking the exchange does not publish.
func (s *Service) IngestPrices(ctx context.Context) error {
	assets, err := s.repos.Assets.List(ctx, true)
	if err != nil {
		return fmt.Errorf("list assets: %w", err)
	}
	if len(assets) == 0 {
		return nil
	}

	ids := make([]string, 0, len(assets))
	for _, a := range assets {
		ids = append(ids, a.CoinGeckoID)
	}

	infos, metadataErr := s.provider.MarketsByIDs(ctx, ids)
	byCoinID := make(map[string]domain.MarketInfo, len(infos))
	for _, info := range infos {
		byCoinID[info.CoinGeckoID] = info
	}
	var bybitInfos map[string]domain.MarketInfo
	var bybitErr error
	if s.bybit != nil {
		bybitInfos, bybitErr = s.bybit.SpotTickers(ctx)
	}
	if metadataErr != nil && (s.bybit == nil || bybitErr != nil) {
		err := errors.Join(metadataErr, bybitErr)
		s.recordFailure(ctx, err)
		return fmt.Errorf("ingest prices: %w", err)
	}

	now := time.Now().UTC()
	stored := 0
	var missingOnExchange []string
	for _, asset := range assets {
		info, ok := byCoinID[asset.CoinGeckoID]
		if !ok {
			info = domain.MarketInfo{CoinGeckoID: asset.CoinGeckoID, Symbol: asset.Symbol, Name: asset.DisplayName, FetchedAt: now}
		}
		// The exchange owns the price; the metadata provider owns the
		// capitalisation and the ranking that go with it.
		if exchange, found := bybitInfos[strings.ToUpper(asset.BybitSymbol)]; found && exchange.Price > 0 {
			info.Price, info.PriceChange24hPct, info.Volume24h = exchange.Price, exchange.PriceChange24hPct, exchange.Volume24h
			info.High24h, info.Low24h, info.FetchedAt = exchange.High24h, exchange.Low24h, exchange.FetchedAt
		} else {
			missingOnExchange = append(missingOnExchange, asset.Symbol)
			continue
		}
		if info.Price <= 0 {
			continue
		}
		if err := s.repos.Market.Insert(ctx, asset.ID, info); err != nil {
			s.log.Warn("store market snapshot failed", slog.String("symbol", asset.Symbol), slog.String("error", err.Error()))
			continue
		}
		stored++
	}
	if stored == 0 {
		err := fmt.Errorf("no market prices were available")
		s.recordFailure(ctx, err)
		return err
	}
	switch {
	case bybitErr != nil:
		s.recordDegraded(ctx, "exchange tickers unavailable: "+bybitErr.Error())
	case len(missingOnExchange) > 0:
		s.recordDegraded(ctx, "no exchange price for "+strings.Join(missingOnExchange, ", "))
	case metadataErr != nil:
		s.recordDegraded(ctx, "market capitalisation unavailable: "+metadataErr.Error())
	default:
		s.recordSuccess(ctx)
	}
	return nil
}

// backfillTimeframes is every timeframe the analysis reads, slowest last so a
// failure part-way still leaves the fast context in place.
var backfillTimeframes = []domain.Timeframe{
	domain.TF1m, domain.TF5m, domain.TF15m, domain.TF1h, domain.TF4h, domain.TF1d,
}

// ErrNotTradable reports an asset the exchange does not list. It is a fact
// about the asset, not a failure of the ingest: nothing can be analysed or
// traded without exchange candles, and inventing them from sampled prices only
// produced bars that looked real and were not.
var ErrNotTradable = errors.New("asset has no tradable pair on the exchange")

// BackfillCandles (re)builds the candle history of one asset across timeframes.
func (s *Service) BackfillCandles(ctx context.Context, asset domain.Asset) error {
	if s.bybit == nil || asset.BybitSymbol == "" {
		return fmt.Errorf("%s: %w", asset.Symbol, ErrNotTradable)
	}

	stored := 0
	for _, tf := range backfillTimeframes {
		candles, err := s.bybitBackfill(ctx, asset, tf)
		if err != nil {
			if errors.Is(err, ErrNotTradable) {
				return fmt.Errorf("%s: %w", asset.Symbol, ErrNotTradable)
			}
			s.log.Warn("backfill timeframe failed",
				slog.String("symbol", asset.Symbol),
				slog.String("timeframe", string(tf)),
				slog.String("error", err.Error()))
			continue
		}
		if len(candles) == 0 {
			continue
		}
		if err := s.repos.Candles.UpsertMany(ctx, asset.ID, tf, candles); err != nil {
			return fmt.Errorf("store %s candles: %w", tf, err)
		}
		stored++
	}

	if stored == 0 {
		err := fmt.Errorf("no candles were stored for %s", asset.Symbol)
		s.recordFailure(ctx, err)
		return err
	}
	s.recordSuccess(ctx)
	return nil
}

// FundingHistoryWindow is how far back a first funding backfill reaches. It
// matches the daily candle history, so a backtest over the stored candles can
// charge the funding that actually applied.
const FundingHistoryWindow = 5 * 365 * 24 * time.Hour

// BackfillFunding stores the settled funding of one asset's perpetual.
//
// The endpoint is public and the data is one row every eight hours, so a full
// refresh of twenty assets is a few hundred rows a day - small enough to run on
// the same schedule as the universe refresh. A first run reaches back over the
// whole stored candle history; later runs continue from the newest stored
// settlement.
func (s *Service) BackfillFunding(ctx context.Context, asset domain.Asset) error {
	if s.bybit == nil || asset.BybitSymbol == "" {
		return nil
	}
	supported, err := s.bybit.SupportsLinearSymbol(ctx, asset.BybitSymbol)
	if err != nil {
		return fmt.Errorf("check linear pair: %w", err)
	}
	if !supported {
		// No perpetual means no funding; that is a fact about the asset rather
		// than a failure of the ingest.
		return nil
	}

	now := time.Now().UTC()
	from := now.Add(-FundingHistoryWindow)
	if last, ok, err := s.repos.Funding.LastSettledAt(ctx, asset.ID); err != nil {
		return err
	} else if ok {
		// One settlement of overlap, so a rate revised after the fact is picked up.
		from = last.Add(-bybit.FundingInterval)
	}
	if !now.After(from) {
		return nil
	}

	rates, err := s.bybit.FundingHistory(ctx, asset.BybitSymbol, from, now)
	if err != nil {
		return err
	}
	if len(rates) == 0 {
		return nil
	}
	if err := s.repos.Funding.UpsertMany(ctx, asset.ID, asset.BybitSymbol, rates); err != nil {
		return err
	}
	s.log.Debug("funding history stored",
		slog.String("symbol", asset.Symbol), slog.Int("settlements", len(rates)))
	return nil
}

// bybitHistoryWindow is how far back a first backfill of each timeframe reaches.
//
// The slow timeframes are where the deterministic engine actually trades, and
// evaluating a change needs independent periods to test it on: with two years of
// four-hour bars there is one holdout window, which is barely evidence. The
// public endpoint pages a thousand bars at a time and the real limit is when the
// pair listed, so asking for more costs a handful of requests once.
var bybitHistoryWindow = map[domain.Timeframe]time.Duration{
	domain.TF1m:  48 * time.Hour,
	domain.TF5m:  30 * 24 * time.Hour,
	domain.TF15m: 180 * 24 * time.Hour,
	domain.TF1h:  3 * 365 * 24 * time.Hour,
	domain.TF4h:  6 * 365 * 24 * time.Hour,
	domain.TF1d:  10 * 365 * 24 * time.Hour,
}

// klineFetcher resolves which public market actually carries the pair and
// returns the function that reads its bars. Spot is the primary market; the
// linear perpetual is used for assets the exchange lists only as a contract.
func (s *Service) klineFetcher(ctx context.Context, asset domain.Asset) (func(domain.Timeframe, time.Time, time.Time) ([]domain.Candle, error), error) {
	spotSupported, err := s.bybit.SupportsSpotSymbol(ctx, asset.BybitSymbol)
	if err != nil {
		return nil, err
	}
	if !spotSupported {
		linearSupported, err := s.bybit.SupportsLinearSymbol(ctx, asset.BybitSymbol)
		if err != nil {
			return nil, err
		}
		if !linearSupported {
			return nil, fmt.Errorf("neither the spot nor the linear instrument %s is trading: %w",
				asset.BybitSymbol, ErrNotTradable)
		}
	}
	return func(tf domain.Timeframe, from, to time.Time) ([]domain.Candle, error) {
		if spotSupported {
			return s.bybit.Klines(ctx, asset.BybitSymbol, tf, from, to)
		}
		return s.bybit.LinearKlines(ctx, asset.BybitSymbol, tf, from, to)
	}, nil
}

// bybitBackfill advances from the stored watermark with one overlap candle.
// A first run uses a bounded timeframe-specific history window.
func (s *Service) bybitBackfill(ctx context.Context, asset domain.Asset, tf domain.Timeframe) ([]domain.Candle, error) {
	fetchTF, err := s.klineFetcher(ctx, asset)
	if err != nil {
		return nil, err
	}
	fetch := func(from, to time.Time) ([]domain.Candle, error) { return fetchTF(tf, from, to) }

	now := time.Now().UTC()
	wanted := now.Add(-bybitHistoryWindow[tf])

	from := wanted
	if last, found, err := s.repos.Candles.LastOpenTimeForProvider(ctx, asset.ID, tf, "bybit"); err != nil {
		return nil, err
	} else if found {
		from = last.Add(-tf.Duration())
	}
	candles, err := fetch(from, now)
	if err != nil {
		return nil, err
	}

	// Advancing from the newest stored candle only ever reaches forward. When the
	// configured history window is widened - or an asset was first backfilled
	// under a narrower one - the older part has to be fetched separately, or the
	// extra history would only ever arrive by waiting for it to happen.
	if first, found, err := s.repos.Candles.FirstOpenTimeForProvider(ctx, asset.ID, tf, "bybit"); err != nil {
		return nil, err
	} else if found && first.Sub(wanted) > tf.Duration() {
		older, err := fetch(wanted, first.Add(tf.Duration()))
		if err != nil {
			return nil, err
		}
		if len(older) > 0 {
			s.log.Debug("extended candle history backwards",
				slog.String("symbol", asset.Symbol), slog.String("timeframe", string(tf)),
				slog.Int("candles", len(older)))
			candles = append(older, candles...)
		}
	}
	return ClosedOnly(candles), nil
}

// LoadClosedCandles returns the most recent closed candles for analysis.
func (s *Service) LoadClosedCandles(ctx context.Context, assetID int64, tf domain.Timeframe, limit int) ([]domain.Candle, error) {
	return s.repos.Candles.Latest(ctx, assetID, tf, limit, true)
}

// Prune drops data outside the retention window.
func (s *Service) Prune(ctx context.Context) error {
	return s.repos.Market.Prune(ctx, time.Now().UTC().Add(-30*24*time.Hour))
}
