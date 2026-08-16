package llm

import (
	"encoding/json"
	"fmt"
	"math"
	"unicode"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

// Payload is the compact projection of a feature snapshot sent to the model.
// The full snapshot is always persisted; only this reduced view travels to the
// LLM, because an 8k context cannot hold the complete analysis.
type Payload struct {
	SchemaVersion int                          `json:"schema_version"`
	Timestamp     string                       `json:"timestamp"`
	Symbol        string                       `json:"symbol"`
	Price         float64                      `json:"price"`
	Market        compactMarket                `json:"market"`
	Timeframes    map[string]compactTF         `json:"timeframes"`
	Alignment     compactAlignment             `json:"trend_alignment"`
	Regime        compactRegime                `json:"market_regime"`
	Scores        domain.SignalScores          `json:"deterministic_scores"`
	Levels        []compactLevel               `json:"support_resistance,omitempty"`
	Positions     []domain.PositionContext     `json:"active_user_positions"`
	Performance   domain.HistoricalPerformance `json:"historical_performance"`
	SimilarCases  []compactCase                `json:"similar_historical_cases,omitempty"`
	News          domain.NewsSnapshot          `json:"news_context"`
	RecentCandles [][]float64                  `json:"recent_candles_ohlc,omitempty"`
	DataQuality   domain.DataQuality           `json:"data_quality"`
}

type compactMarket struct {
	MarketCap         float64  `json:"market_cap,omitempty"`
	Volume24h         float64  `json:"volume_24h,omitempty"`
	PriceChange24hPct float64  `json:"price_change_24h_pct"`
	PriceChange1hPct  *float64 `json:"price_change_1h_pct,omitempty"`
	PriceChange7dPct  *float64 `json:"price_change_7d_pct,omitempty"`
}

type compactTF struct {
	Close      float64            `json:"close"`
	Indicators map[string]any     `json:"indicators"`
	Structure  string             `json:"structure"`
	Regime     string             `json:"regime"`
	Bias       string             `json:"bias"`
	Patterns   []string           `json:"patterns,omitempty"`
	Chart      []string           `json:"chart_patterns,omitempty"`
	Divergence []string           `json:"divergences,omitempty"`
	Scores     map[string]float64 `json:"scores,omitempty"`
}

type compactAlignment struct {
	Bullish   []string `json:"bullish"`
	Bearish   []string `json:"bearish"`
	Neutral   []string `json:"neutral"`
	Score     float64  `json:"alignment_score"`
	Conflicts []string `json:"conflicts,omitempty"`
}

type compactRegime struct {
	Primary string   `json:"primary"`
	Tags    []string `json:"tags,omitempty"`
}

type compactLevel struct {
	Price       float64 `json:"price"`
	Type        string  `json:"type"`
	Strength    float64 `json:"strength"`
	DistancePct float64 `json:"distance_pct"`
	Touches     int     `json:"touches"`
}

type compactCase struct {
	Similarity     float64        `json:"similarity"`
	Timestamp      string         `json:"timestamp"`
	Recommendation string         `json:"recommendation"`
	Confidence     int            `json:"confidence"`
	UserOpened     bool           `json:"user_opened"`
	Features       map[string]any `json:"features_summary,omitempty"`
	MarketOutcome  any            `json:"market_outcome,omitempty"`
	TradeOutcome   any            `json:"user_trade_outcome,omitempty"`
}

// BuildOptions controls compaction.
type BuildOptions struct {
	// MaxTokens is the budget for the user message.
	MaxTokens int
	// TailCandles is how many raw candles are attached as extra context.
	TailCandles int
	// MaxSimilarCases caps the retrieved historical cases.
	MaxSimilarCases int
}

// DefaultBuildOptions returns the preferred snapshot budget. The service caps
// it further using the configured model context and response reservation.
func DefaultBuildOptions() BuildOptions {
	return BuildOptions{MaxTokens: 4200, TailCandles: 20, MaxSimilarCases: 5}
}

// trimStep is one reduction applied when the payload does not fit.
type trimStep struct {
	note  string
	apply func(p *Payload)
}

// Build compacts a snapshot into a payload that fits the token budget.
// Reductions are applied in priority order and every reduction is reported, so
// the model can be told what was left out instead of silently losing context.
func Build(snapshot domain.FeatureSnapshot, opts BuildOptions) (Payload, string, []string, error) {
	if opts.MaxTokens <= 0 {
		opts = DefaultBuildOptions()
	}
	payload := compact(snapshot, opts)

	steps := []trimStep{
		{"similar historical cases reduced to 3", func(p *Payload) { p.SimilarCases = head(p.SimilarCases, 3) }},
		{"news summaries removed; titles and metrics retained", func(p *Payload) { trimNewsSummaries(p) }},
		{"non-critical news reduced; all critical events retained", func(p *Payload) { trimNonCriticalNews(p) }},
		{"raw candle tail shortened", func(p *Payload) { p.RecentCandles = headCandles(p.RecentCandles, 10) }},
		{"per-timeframe pattern lists shortened", func(p *Payload) { trimPatterns(p, 2) }},
		{"similar historical cases removed", func(p *Payload) { p.SimilarCases = nil }},
		{"raw candle tail removed", func(p *Payload) { p.RecentCandles = nil }},
		{"secondary indicators removed", func(p *Payload) { reduceIndicators(p) }},
		{"1m timeframe removed", func(p *Payload) { delete(p.Timeframes, string(domain.TF1m)) }},
		{"support/resistance list shortened", func(p *Payload) { p.Levels = head(p.Levels, 4) }},
		{"5m timeframe removed", func(p *Payload) { delete(p.Timeframes, string(domain.TF5m)) }},
		{"pattern and divergence details removed", func(p *Payload) { trimPatterns(p, 0) }},
		{"support/resistance list removed", func(p *Payload) { p.Levels = nil }},
		{"1d timeframe removed", func(p *Payload) { delete(p.Timeframes, string(domain.TF1d)) }},
		{"data-quality detail shortened", func(p *Payload) {
			p.DataQuality.MissingFields = head(p.DataQuality.MissingFields, 6)
			p.DataQuality.Notes = head(p.DataQuality.Notes, 3)
		}},
	}

	var applied []string
	encoded, err := encode(payload)
	if err != nil {
		return payload, "", nil, err
	}

	for _, step := range steps {
		if EstimateTokens(encoded) <= opts.MaxTokens {
			break
		}
		step.apply(&payload)
		applied = append(applied, step.note)

		encoded, err = encode(payload)
		if err != nil {
			return payload, "", applied, err
		}
	}

	if EstimateTokens(encoded) > opts.MaxTokens {
		// Everything optional is already gone. The payload is still valid JSON;
		// it is never cut mid-structure, so the model receives a complete but
		// minimal snapshot.
		applied = append(applied, "payload still exceeds the token budget after all reductions")
	}
	if len(applied) > 0 {
		payload.DataQuality.AddNote("context reduced to fit the configured LLM window; optional detail may be omitted")
		encoded, err = encode(payload)
		if err != nil {
			return payload, "", applied, err
		}
	}
	if estimated := EstimateTokens(encoded); estimated > opts.MaxTokens {
		return payload, "", applied, fmt.Errorf(
			"minimal LLM payload requires about %d tokens but the available budget is %d",
			estimated, opts.MaxTokens,
		)
	}
	return payload, encoded, applied, nil
}

func encode(p Payload) (string, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("encode llm payload: %w", err)
	}
	return string(raw), nil
}

// EstimateTokens is a deliberately conservative tokenizer-independent estimate.
// JSON punctuation and short numeric fragments use more tokens than ordinary
// prose, while CJK characters can approach one token per rune. Exact tokenizers
// remain model-specific, so the request budget also keeps a separate safety
// reserve before reaching the configured context limit.
func EstimateTokens(s string) int {
	ascii, nonASCII := 0, 0
	for _, r := range s {
		if r <= unicode.MaxASCII {
			ascii++
		} else {
			nonASCII++
		}
	}
	return int(math.Ceil(float64(ascii)/2.4)) + nonASCII
}

func compact(s domain.FeatureSnapshot, opts BuildOptions) Payload {
	p := Payload{
		SchemaVersion: s.SchemaVersion,
		Timestamp:     s.Timestamp.UTC().Format("2006-01-02T15:04:05Z"),
		Symbol:        s.Symbol,
		Price:         s.Price,
		Market: compactMarket{
			MarketCap:         s.Market.MarketCap,
			Volume24h:         s.Market.Volume24h,
			PriceChange24hPct: s.Market.PriceChange24hPct,
			PriceChange1hPct:  s.Market.PriceChange1hPct,
			PriceChange7dPct:  s.Market.PriceChange7dPct,
		},
		Timeframes:  map[string]compactTF{},
		Scores:      s.AggregateScores,
		Positions:   s.ActivePositions,
		Performance: s.HistoricalPerformance,
		News:        s.NewsContext,
		DataQuality: s.DataQuality,
	}
	if p.Positions == nil {
		p.Positions = []domain.PositionContext{}
	}

	for tf, a := range s.Timeframes {
		p.Timeframes[string(tf)] = compactTF{
			Close:      a.Close,
			Indicators: compactIndicators(a.Indicators),
			Structure:  a.Structure.Description,
			Regime:     string(a.Regime.Primary),
			Bias:       string(a.Bias),
			Patterns:   patternNames(a.Patterns, 3),
			Chart:      patternNames(a.ChartPatterns, 3),
			Divergence: divergenceNames(a.Divergences, 3),
			Scores: map[string]float64{
				"trend": a.Scores.Trend, "momentum": a.Scores.Momentum, "pattern": a.Scores.Pattern,
			},
		}
	}

	p.Alignment = compactAlignment{
		Bullish:   tfNames(s.TrendAlignment.Bullish),
		Bearish:   tfNames(s.TrendAlignment.Bearish),
		Neutral:   tfNames(s.TrendAlignment.Neutral),
		Score:     s.TrendAlignment.AlignmentScore,
		Conflicts: s.TrendAlignment.Conflicts,
	}
	p.Regime = compactRegime{Primary: string(s.AggregateRegime.Primary)}
	for _, t := range s.AggregateRegime.Tags {
		p.Regime.Tags = append(p.Regime.Tags, string(t))
	}

	for _, l := range s.KeyLevels {
		p.Levels = append(p.Levels, compactLevel{
			Price: l.Price, Type: string(l.Type), Strength: round2(l.Strength),
			DistancePct: round2(l.DistancePct), Touches: l.Touches,
		})
	}

	for _, c := range head(s.SimilarCases, opts.MaxSimilarCases) {
		cc := compactCase{
			Similarity:     c.Similarity,
			Timestamp:      c.Timestamp.UTC().Format("2006-01-02T15:04Z"),
			Recommendation: string(c.Recommendation),
			Confidence:     c.Confidence,
			UserOpened:     c.UserOpened,
			Features:       c.FeaturesSummary,
		}
		if c.MarketOutcome != nil {
			cc.MarketOutcome = c.MarketOutcome
		}
		if c.TradeOutcome != nil {
			cc.TradeOutcome = c.TradeOutcome
		}
		p.SimilarCases = append(p.SimilarCases, cc)
	}

	tail := head(reverseCandles(s.RecentCandles), opts.TailCandles)
	for i := len(tail) - 1; i >= 0; i-- {
		c := tail[i]
		p.RecentCandles = append(p.RecentCandles, []float64{
			round2(c.Open), round2(c.High), round2(c.Low), round2(c.Close),
		})
	}
	return p
}

func trimNewsSummaries(p *Payload) {
	for index := range p.News.AssetSpecific {
		p.News.AssetSpecific[index].Summary = ""
	}
	for index := range p.News.Global {
		p.News.Global[index].Summary = ""
	}
}

func trimNonCriticalNews(p *Payload) {
	p.News.AssetSpecific = retainCriticalAndHead(p.News.AssetSpecific, 3)
	p.News.Global = retainCriticalAndHead(p.News.Global, 2)
}

func retainCriticalAndHead(items []domain.NewsSnapshotItem, nonCriticalLimit int) []domain.NewsSnapshotItem {
	out := make([]domain.NewsSnapshotItem, 0, len(items))
	nonCritical := 0
	for _, item := range items {
		if item.Critical {
			out = append(out, item)
			continue
		}
		if nonCritical < nonCriticalLimit {
			out = append(out, item)
			nonCritical++
		}
	}
	return out
}

// compactIndicators keeps the fields that carry decision-relevant information.
func compactIndicators(ind domain.Indicators) map[string]any {
	out := map[string]any{}
	put := func(key string, v *float64) {
		if v != nil {
			out[key] = *v
		}
	}
	putStr := func(key, v string) {
		if v != "" {
			out[key] = v
		}
	}

	put("rsi", ind.RSI)
	putStr("rsi_state", ind.RSIState)
	put("rsi_slope", ind.RSISlope)
	put("stoch_k", ind.StochK)
	put("macd_hist", ind.MACDHistogram)
	putStr("macd_state", ind.MACDState)
	put("adx", ind.ADX)
	putStr("trend_strength", ind.TrendStrength)
	put("plus_di", ind.PlusDI)
	put("minus_di", ind.MinusDI)
	put("atr_pct", ind.ATRPct)
	put("atr_percentile", ind.ATRPercentile)
	put("bb_width", ind.BBWidth)
	put("bb_percent_b", ind.BBPercentB)
	put("realized_volatility", ind.RealizedVol)
	put("price_vs_ema_50_pct", ind.PriceVsEMA50Pct)
	put("price_vs_ema_200_pct", ind.PriceVsEMA200Pct)
	put("distance_from_high_pct", ind.DistFromHighPct)
	put("distance_from_low_pct", ind.DistFromLowPct)
	put("relative_volume", ind.RelativeVolume)
	put("mfi", ind.MFI)
	put("cmf", ind.CMF)
	put("price_vs_vwap_pct", ind.PriceVsVWAPPct)

	if len(ind.EMA) > 0 {
		ema := map[string]float64{}
		for _, k := range []string{"20", "50", "200"} {
			if v, ok := ind.EMA[k]; ok {
				ema[k] = v
			}
		}
		if len(ema) > 0 {
			out["ema"] = ema
		}
	}
	return out
}

// reduceIndicators strips the second-tier indicators when context runs short.
func reduceIndicators(p *Payload) {
	keep := map[string]bool{
		"rsi": true, "rsi_state": true, "macd_state": true, "adx": true,
		"trend_strength": true, "atr_pct": true, "price_vs_ema_200_pct": true,
		"bb_percent_b": true,
	}
	for tf, data := range p.Timeframes {
		reduced := map[string]any{}
		for k, v := range data.Indicators {
			if keep[k] {
				reduced[k] = v
			}
		}
		data.Indicators = reduced
		p.Timeframes[tf] = data
	}
}

func trimPatterns(p *Payload, limit int) {
	for tf, data := range p.Timeframes {
		if limit <= 0 {
			data.Patterns = nil
			data.Chart = nil
			data.Divergence = nil
		} else {
			data.Patterns = head(data.Patterns, limit)
			data.Chart = head(data.Chart, limit)
			data.Divergence = head(data.Divergence, limit)
		}
		p.Timeframes[tf] = data
	}
}

func patternNames(patterns []domain.Pattern, limit int) []string {
	out := make([]string, 0, limit)
	for _, p := range patterns {
		if len(out) >= limit {
			break
		}
		out = append(out, fmt.Sprintf("%s(%s,%.2f,age=%d)", p.Name, p.Direction, p.Strength, p.AgeCandles))
	}
	return out
}

func divergenceNames(divs []domain.Divergence, limit int) []string {
	out := make([]string, 0, limit)
	for _, d := range divs {
		if len(out) >= limit {
			break
		}
		out = append(out, fmt.Sprintf("%s_%s_%s(%.2f,age=%d)", d.Indicator, d.Type, d.Direction, d.Strength, d.AgeCandles))
	}
	return out
}

func tfNames(tfs []domain.Timeframe) []string {
	out := make([]string, 0, len(tfs))
	for _, tf := range tfs {
		out = append(out, string(tf))
	}
	return out
}

func head[T any](items []T, n int) []T {
	if n <= 0 || len(items) <= n {
		if n <= 0 {
			return nil
		}
		return items
	}
	return items[:n]
}

func headCandles(items [][]float64, n int) [][]float64 { return head(items, n) }

func reverseCandles(candles []domain.Candle) []domain.Candle {
	out := make([]domain.Candle, len(candles))
	for i, c := range candles {
		out[len(candles)-1-i] = c
	}
	return out
}

func round2(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*100) / 100
}
