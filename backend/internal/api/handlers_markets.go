package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/crypto-market-advisor/advisor/internal/domain"
	"github.com/crypto-market-advisor/advisor/internal/repository"
	"github.com/crypto-market-advisor/advisor/internal/scheduler"
)

// MarketDTO is the API view of a tracked asset.
type MarketDTO struct {
	ID                   int64      `json:"id"`
	Symbol               string     `json:"symbol"`
	CoinGeckoID          string     `json:"coingecko_id"`
	DisplayName          string     `json:"display_name"`
	BybitSymbol          string     `json:"bybit_symbol"`
	Enabled              bool       `json:"enabled"`
	ManuallyAdded        bool       `json:"manually_added"`
	Pinned               bool       `json:"pinned"`
	ExcludedFromAutoList bool       `json:"excluded_from_auto_list"`
	MarketCapRank        *int       `json:"market_cap_rank,omitempty"`
	Price                *float64   `json:"price,omitempty"`
	Change24hPct         *float64   `json:"price_change_24h_pct,omitempty"`
	Volume24h            *float64   `json:"volume_24h,omitempty"`
	MarketCap            *float64   `json:"market_cap,omitempty"`
	UpdatedAt            *time.Time `json:"market_updated_at,omitempty"`

	Regime         *string    `json:"market_regime,omitempty"`
	RSI            *float64   `json:"rsi,omitempty"`
	Trend          *string    `json:"trend,omitempty"`
	LastAction     *string    `json:"last_action,omitempty"`
	LastConfidence *int       `json:"last_confidence,omitempty"`
	LastSignalAt   *time.Time `json:"last_signal_at,omitempty"`
}

func assetToDTO(a domain.Asset) MarketDTO {
	return MarketDTO{
		ID: a.ID, Symbol: a.Symbol, CoinGeckoID: a.CoinGeckoID, DisplayName: a.DisplayName,
		BybitSymbol: a.BybitSymbol, Enabled: a.Enabled, ManuallyAdded: a.ManuallyAdded,
		Pinned: a.Pinned, ExcludedFromAutoList: a.ExcludedFromAutoList, MarketCapRank: a.MarketCapRank,
	}
}

func (s *Server) handleListMarkets(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	onlyEnabled := r.URL.Query().Get("enabled") == "true"
	assets, err := s.deps.Repos.Assets.List(ctx, onlyEnabled)
	if err != nil {
		WriteError(w, r, s.log, ErrInternal("failed to list markets").WithCause(err))
		return
	}

	prices, err := s.deps.Repos.Market.LatestForAll(ctx)
	if err != nil {
		s.log.Warn("load market snapshots failed", "error", err)
	}
	latestRecs, err := s.deps.Repos.Recommendations.LatestPerAsset(ctx)
	if err != nil {
		s.log.Warn("load latest recommendations failed", "error", err)
	}

	items := make([]MarketDTO, 0, len(assets))
	for _, a := range assets {
		dto := assetToDTO(a)
		if info, ok := prices[a.ID]; ok {
			price, change, volume, cap := info.Price, info.PriceChange24hPct, info.Volume24h, info.MarketCap
			fetched := info.FetchedAt
			dto.Price, dto.Change24hPct, dto.Volume24h, dto.MarketCap = &price, &change, &volume, &cap
			dto.UpdatedAt = &fetched
		}
		if rec, ok := latestRecs[a.ID]; ok {
			action, confidence, at := string(rec.Action), rec.Confidence, rec.CreatedAt
			regime := string(rec.MarketRegime)
			dto.LastAction, dto.LastConfidence, dto.LastSignalAt = &action, &confidence, &at
			if regime != "" {
				dto.Regime = &regime
			}
		}
		if run, err := s.deps.Repos.Analysis.Latest(ctx, a.ID); err == nil {
			if tf, ok := run.Snapshot.Timeframes[domain.TF1h]; ok {
				if tf.Indicators.RSI != nil {
					rsi := *tf.Indicators.RSI
					dto.RSI = &rsi
				}
				if tf.Indicators.TrendStrength != "" {
					trend := string(tf.Bias) + "/" + tf.Indicators.TrendStrength
					dto.Trend = &trend
				}
			}
			if dto.Regime == nil && run.Regime != "" {
				regime := string(run.Regime)
				dto.Regime = &regime
			}
		}
		items = append(items, dto)
	}

	WriteJSON(w, http.StatusOK, Page[MarketDTO]{Items: items, Total: len(items), Limit: len(items)})
}

// CreateMarketRequest adds an asset manually.
type CreateMarketRequest struct {
	CoinGeckoID string `json:"coingecko_id"`
	Symbol      string `json:"symbol"`
	DisplayName string `json:"display_name"`
	BybitSymbol string `json:"bybit_symbol"`
	Pinned      bool   `json:"pinned"`
}

func (s *Server) handleCreateMarket(w http.ResponseWriter, r *http.Request) {
	var req CreateMarketRequest
	if err := decodeJSON(r, &req); err != nil {
		WriteError(w, r, s.log, err)
		return
	}
	req.CoinGeckoID = strings.TrimSpace(strings.ToLower(req.CoinGeckoID))
	req.Symbol = strings.TrimSpace(strings.ToUpper(req.Symbol))
	if req.CoinGeckoID == "" || req.Symbol == "" {
		WriteError(w, r, s.log, ErrValidation("coingecko_id and symbol are required"))
		return
	}
	if req.DisplayName == "" {
		req.DisplayName = req.Symbol
	}
	if req.BybitSymbol == "" {
		req.BybitSymbol = req.Symbol + "USDT"
	}

	ctx, cancel := contextWithTimeout(r, 60*time.Second)
	defer cancel()

	asset, err := s.deps.Repos.Assets.Create(ctx, domain.Asset{
		CoinGeckoID:   req.CoinGeckoID,
		Symbol:        req.Symbol,
		DisplayName:   req.DisplayName,
		BybitSymbol:   req.BybitSymbol,
		Enabled:       true,
		ManuallyAdded: true,
		Pinned:        req.Pinned,
	})
	if err != nil {
		WriteError(w, r, s.log, ErrConflict("asset could not be created; the symbol or provider id may already exist").WithCause(err))
		return
	}

	// Populate history immediately so the new asset is usable without waiting
	// for the next scheduled backfill.
	go func() {
		bg, cancel := contextWithTimeoutBackground(5 * time.Minute)
		defer cancel()
		if err := s.deps.Scheduler.Backfill(bg, asset); err != nil {
			s.log.Warn("backfill for new asset failed", "symbol", asset.Symbol, "error", err)
		}
	}()

	WriteJSON(w, http.StatusCreated, assetToDTO(asset))
}

// UpdateMarketRequest changes user-controlled flags.
type UpdateMarketRequest struct {
	Enabled              *bool   `json:"enabled"`
	Pinned               *bool   `json:"pinned"`
	ExcludedFromAutoList *bool   `json:"excluded_from_auto_list"`
	BybitSymbol          *string `json:"bybit_symbol"`
	DisplayName          *string `json:"display_name"`
}

func (s *Server) handleUpdateMarket(w http.ResponseWriter, r *http.Request) {
	var req UpdateMarketRequest
	if err := decodeJSON(r, &req); err != nil {
		WriteError(w, r, s.log, err)
		return
	}

	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	asset, err := s.deps.Repos.Assets.GetBySymbol(ctx, chi.URLParam(r, "symbol"))
	if err != nil {
		WriteError(w, r, s.log, notFoundOr(err, "market not found"))
		return
	}

	updated, err := s.deps.Repos.Assets.UpdateFlags(ctx, asset.ID, repository.AssetFlags{
		Enabled:              req.Enabled,
		Pinned:               req.Pinned,
		ExcludedFromAutoList: req.ExcludedFromAutoList,
		BybitSymbol:          req.BybitSymbol,
		DisplayName:          req.DisplayName,
	})
	if err != nil {
		WriteError(w, r, s.log, notFoundOr(err, "market not found"))
		return
	}
	WriteJSON(w, http.StatusOK, assetToDTO(updated))
}

func (s *Server) handleDeleteMarket(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	asset, err := s.deps.Repos.Assets.GetBySymbol(ctx, chi.URLParam(r, "symbol"))
	if err != nil {
		WriteError(w, r, s.log, notFoundOr(err, "market not found"))
		return
	}
	if err := s.deps.Repos.Assets.Delete(ctx, asset.ID); err != nil {
		WriteError(w, r, s.log, notFoundOr(err, "market not found"))
		return
	}
	WriteJSON(w, http.StatusNoContent, nil)
}

func (s *Server) handleGetMarket(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	asset, err := s.deps.Repos.Assets.GetBySymbol(ctx, chi.URLParam(r, "symbol"))
	if err != nil {
		WriteError(w, r, s.log, notFoundOr(err, "market not found"))
		return
	}
	dto := assetToDTO(asset)
	if info, err := s.deps.Repos.Market.Latest(ctx, asset.ID); err == nil {
		price, change, volume, cap := info.Price, info.PriceChange24hPct, info.Volume24h, info.MarketCap
		fetched := info.FetchedAt
		dto.Price, dto.Change24hPct, dto.Volume24h, dto.MarketCap = &price, &change, &volume, &cap
		dto.UpdatedAt = &fetched
	}
	WriteJSON(w, http.StatusOK, dto)
}

// AnalysisDTO is the API view of one analysis run.
type AnalysisDTO struct {
	ID                 string                   `json:"id"`
	Symbol             string                   `json:"symbol"`
	AnalysisTimestamp  time.Time                `json:"analysis_timestamp"`
	LatestClosedCandle *time.Time               `json:"latest_closed_candle_timestamp,omitempty"`
	Price              float64                  `json:"price"`
	Snapshot           domain.FeatureSnapshot   `json:"features_snapshot"`
	DurationMS         int                      `json:"duration_ms"`
	TriggeredBy        string                   `json:"triggered_by"`
	StrategyDecision   *domain.StrategyDecision `json:"strategy_decision,omitempty"`
}

func (s *Server) handleGetAnalysis(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	asset, err := s.deps.Repos.Assets.GetBySymbol(ctx, chi.URLParam(r, "symbol"))
	if err != nil {
		WriteError(w, r, s.log, notFoundOr(err, "market not found"))
		return
	}
	run, err := s.deps.Repos.Analysis.Latest(ctx, asset.ID)
	if err != nil {
		WriteError(w, r, s.log, notFoundOr(err, "no analysis available for this market yet"))
		return
	}

	WriteJSON(w, http.StatusOK, AnalysisDTO{
		ID: run.ID.String(), Symbol: run.Symbol, AnalysisTimestamp: run.AnalysisTimestamp,
		LatestClosedCandle: run.LatestClosedCandle, Price: run.Price, Snapshot: run.Snapshot,
		DurationMS: run.DurationMS, TriggeredBy: run.TriggeredBy,
		StrategyDecision: run.StrategyDecision,
	})
}

func (s *Server) handleGetCandles(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	asset, err := s.deps.Repos.Assets.GetBySymbol(ctx, chi.URLParam(r, "symbol"))
	if err != nil {
		WriteError(w, r, s.log, notFoundOr(err, "market not found"))
		return
	}

	tf, err := domain.ParseTimeframe(defaultString(r.URL.Query().Get("timeframe"), "1h"))
	if err != nil {
		WriteError(w, r, s.log, ErrBadRequest(err.Error()))
		return
	}
	// Each timeframe has a bounded retention window. Ten thousand bars covers
	// the complete configured window even for 5m/15m/1h, so the chart does not
	// appear to end merely because of an API pagination limit.
	limit := 10_000
	if v := r.URL.Query().Get("limit"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 && parsed <= 10_000 {
			limit = parsed
		}
	}

	// An explicit window is what a backtest report needs: it must show the
	// period that was replayed, not the most recent bars.
	var candles []domain.Candle
	from, to, windowed, err := parseCandleWindow(r)
	if err != nil {
		WriteError(w, r, s.log, err)
		return
	}
	if windowed {
		candles, err = s.deps.Repos.Candles.Range(ctx, asset.ID, tf, from, to)
		if err == nil && len(candles) > limit {
			candles = candles[len(candles)-limit:]
		}
	} else {
		candles, err = s.deps.Repos.Candles.Latest(ctx, asset.ID, tf, limit, false)
	}
	if err != nil {
		WriteError(w, r, s.log, ErrInternal("failed to load candles").WithCause(err))
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"symbol":    asset.Symbol,
		"timeframe": tf,
		"candles":   candles,
	})
}

// parseCandleWindow reads the optional from/to query parameters. Both must be
// present for a windowed request; anything else falls back to the latest bars.
func parseCandleWindow(r *http.Request) (time.Time, time.Time, bool, error) {
	rawFrom, rawTo := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	if rawFrom == "" || rawTo == "" {
		return time.Time{}, time.Time{}, false, nil
	}
	from, err := time.Parse(time.RFC3339, rawFrom)
	if err != nil {
		return time.Time{}, time.Time{}, false, ErrBadRequest("from must be an RFC3339 timestamp")
	}
	to, err := time.Parse(time.RFC3339, rawTo)
	if err != nil {
		return time.Time{}, time.Time{}, false, ErrBadRequest("to must be an RFC3339 timestamp")
	}
	if !to.After(from) {
		return time.Time{}, time.Time{}, false, ErrBadRequest("to must be after from")
	}
	return from.UTC(), to.UTC(), true, nil
}

func (s *Server) handleAnalyzeNow(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 5*time.Minute)
	defer cancel()

	asset, err := s.deps.Repos.Assets.GetBySymbol(ctx, chi.URLParam(r, "symbol"))
	if err != nil {
		WriteError(w, r, s.log, notFoundOr(err, "market not found"))
		return
	}

	result, err := s.deps.Scheduler.AnalyzeNow(ctx, asset)
	if err != nil {
		if errors.Is(err, scheduler.ErrCooldown) {
			WriteError(w, r, s.log, ErrRateLimited("analysis for this symbol was requested too recently"))
			return
		}
		WriteError(w, r, s.log, ErrInternal("analysis failed").WithCause(err))
		return
	}

	response := map[string]any{
		"analysis_id":  result.Run.ID.String(),
		"symbol":       result.Run.Symbol,
		"llm_skipped":  result.LLMSkipped,
		"data_quality": result.Run.DataQuality,
	}
	if result.LLMError != "" {
		response["llm_error"] = result.LLMError
	}
	if result.Recommendation != nil {
		response["recommendation"] = recommendationToDTO(*result.Recommendation, nil, nil, s.deps.Config.Analysis.StaleAfter)
	}
	WriteJSON(w, http.StatusOK, response)
}

func (s *Server) handleRefreshUniverse(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 2*time.Minute)
	defer cancel()

	if err := s.deps.Market.RefreshUniverse(ctx); err != nil {
		WriteError(w, r, s.log, ErrUpstream("universe refresh failed").WithCause(err))
		return
	}
	WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func defaultString(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}
