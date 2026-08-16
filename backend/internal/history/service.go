// Package history turns stored predictions, outcomes and real trades into the
// aggregate statistics shown in the UI and fed back into the LLM context.
//
// The three kinds of fact are kept strictly apart:
//   - model_prediction: what the model said,
//   - market_outcome:   what the market did afterwards,
//   - user_trade_outcome: what the user actually realised.
//
// Only the last two are evidence. The model is never told "you said LONG, so
// LONG was right".
package history

import (
	"context"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/shopspring/decimal"

	"github.com/crypto-market-advisor/advisor/internal/analysis/features"
	"github.com/crypto-market-advisor/advisor/internal/domain"
	"github.com/crypto-market-advisor/advisor/internal/logging"
	"github.com/crypto-market-advisor/advisor/internal/repository"
)

// Service computes historical statistics.
type Service struct {
	repos *repository.Repositories
	log   *slog.Logger
}

// NewService builds the history service.
func NewService(repos *repository.Repositories, logger *slog.Logger) *Service {
	return &Service{repos: repos, log: logging.For(logger, logging.CategoryAnalysis)}
}

// Performance summarises the recent track record for the LLM context.
func (s *Service) Performance(ctx context.Context, symbol string) (domain.HistoricalPerformance, error) {
	return s.PerformanceAt(ctx, symbol, time.Time{})
}

// PerformanceAt is Performance as it stood at a moment in time. A replay must
// pass the bar it is standing on: the track record of predictions made after
// that bar is exactly the kind of hindsight a backtest is supposed to exclude.
// The zero time means "everything known now", which is what the live cycle wants.
func (s *Service) PerformanceAt(ctx context.Context, symbol string, before time.Time) (domain.HistoricalPerformance, error) {
	filter := repository.HistoryFilter{Limit: 100}
	var cutoff *time.Time
	if !before.IsZero() {
		at := before.UTC()
		cutoff = &at
		filter.Before = cutoff
	}
	records, err := s.repos.Recommendations.RecordsWithOutcomes(ctx, filter)
	if err != nil {
		return domain.HistoricalPerformance{}, err
	}

	perf := domain.HistoricalPerformance{
		RegimeWinRate:  map[string]float64{},
		PatternWinRate: map[string]float64{},
	}

	var longWins, longTotal, shortWins, shortTotal int
	var symbolWins, symbolTotal int
	regimeStats := map[string][2]int{}

	for _, r := range records {
		if r.Outcome == nil || !r.Outcome.Finalized {
			continue
		}
		// A prediction made before the cutoff can still have been graded after
		// it. Its result was not knowable yet and must not count.
		if cutoff != nil && !r.Outcome.EvaluatedAt.Before(*cutoff) {
			continue
		}
		won := r.Outcome.Result == domain.ResultWin
		perf.SampleSize++

		switch r.Recommendation.Action {
		case domain.RecommendationOpenLong:
			longTotal++
			if won {
				longWins++
			}
		case domain.RecommendationOpenShort:
			shortTotal++
			if won {
				shortWins++
			}
		}
		if symbol != "" && r.Recommendation.Symbol == symbol {
			symbolTotal++
			if won {
				symbolWins++
			}
		}
		if regime := string(r.Recommendation.MarketRegime); regime != "" {
			stat := regimeStats[regime]
			stat[1]++
			if won {
				stat[0]++
			}
			regimeStats[regime] = stat
		}
	}

	perf.LongWinRate = rate(longWins, longTotal)
	perf.ShortWinRate = rate(shortWins, shortTotal)
	perf.SymbolWinRate = rate(symbolWins, symbolTotal)
	for regime, stat := range regimeStats {
		if stat[1] >= 3 {
			perf.RegimeWinRate[regime] = round2(float64(stat[0]) / float64(stat[1]))
		}
	}

	trades, err := s.repos.Positions.ClosedTrades(ctx, nil, cutoff, 200)
	if err == nil && len(trades) > 0 {
		var sum float64
		var count int
		for _, t := range trades {
			if t.NetPnLPct != nil {
				sum += *t.NetPnLPct
				count++
			}
		}
		if count > 0 {
			avg := round2(sum / float64(count))
			perf.AvgRealizedPnLPct = &avg
		}
	}

	if perf.SampleSize < 10 {
		perf.Note = "small sample: these rates are not yet statistically meaningful"
	}
	return perf, nil
}

// SimilarCases finds historically similar situations and attaches their real
// outcomes. Only runs strictly older than `before` are considered, which is
// what keeps backtests free of look-ahead.
func (s *Service) SimilarCases(ctx context.Context, vector []float64, symbol string, limit int, before time.Time) ([]domain.SimilarCase, error) {
	if len(vector) == 0 || limit <= 0 {
		return nil, nil
	}
	vectors, err := s.repos.Analysis.Vectors(ctx, 2000, before)
	if err != nil {
		return nil, err
	}
	if len(vectors) == 0 {
		return nil, nil
	}

	type scored struct {
		row        repository.VectorRow
		similarity float64
	}
	candidates := make([]scored, 0, len(vectors))
	for _, v := range vectors {
		sim := features.Similarity(vector, v.Vector)
		// Same-symbol history is more informative than a different market's.
		if v.Symbol == symbol {
			sim = math.Min(1, sim*1.05)
		}
		candidates = append(candidates, scored{row: v, similarity: sim})
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].similarity > candidates[j].similarity })

	if len(candidates) > limit*4 {
		candidates = candidates[:limit*4]
	}

	records, err := s.repos.Recommendations.RecordsWithOutcomes(ctx, repository.HistoryFilter{Limit: 1000})
	if err != nil {
		return nil, err
	}
	byRun := make(map[string]repository.RecordWithOutcome, len(records))
	for _, r := range records {
		if r.Recommendation.AnalysisRunID != nil {
			byRun[r.Recommendation.AnalysisRunID.String()] = r
		}
	}

	out := make([]domain.SimilarCase, 0, limit)
	for _, c := range candidates {
		rec, ok := byRun[c.row.RunID.String()]
		if !ok {
			continue // an analysis without a recommendation carries no outcome
		}
		sc := domain.SimilarCase{
			Similarity:     round3(c.similarity),
			Timestamp:      c.row.Timestamp,
			Symbol:         c.row.Symbol,
			Recommendation: rec.Recommendation.Action,
			Confidence:     rec.Recommendation.Confidence,
			FeaturesSummary: map[string]any{
				"regime": string(rec.Recommendation.MarketRegime),
			},
		}
		if rec.Decision != nil {
			sc.UserOpened = rec.Decision.Decision == domain.DecisionOpened
		}
		if rec.Outcome != nil && rec.Outcome.Finalized {
			sc.MarketOutcome = &domain.CaseOutcome{
				MFEPct: rec.Outcome.MFEPct,
				MAEPct: rec.Outcome.MAEPct,
				Status: string(rec.Outcome.Status),
			}
		}
		out = append(out, sc)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// Statistics is the dashboard aggregate.
type Statistics struct {
	GeneratedAt time.Time `json:"generated_at"`
	Window      string    `json:"window"`

	Predictions      int            `json:"predictions"`
	ActionCounts     map[string]int `json:"action_counts"`
	PositionsOpened  int            `json:"positions_opened"`
	PositionsClosed  int            `json:"positions_closed"`
	OutcomesResolved int            `json:"outcomes_resolved"`
	AmbiguousOutcome int            `json:"ambiguous_outcomes"`

	WinRate      float64  `json:"win_rate"`
	LossRate     float64  `json:"loss_rate"`
	ProfitFactor *float64 `json:"profit_factor,omitempty"`
	Expectancy   float64  `json:"expectancy"`
	AvgPnL       float64  `json:"average_pnl"`
	MedianPnL    float64  `json:"median_pnl"`
	MaxDrawdown  float64  `json:"max_drawdown"`
	AvgHoldingMs float64  `json:"average_holding_minutes"`
	AvgMFE       float64  `json:"average_mfe_pct"`
	AvgMAE       float64  `json:"average_mae_pct"`
	RealizedPnL  string   `json:"realized_pnl"`

	BySymbol     []Bucket `json:"by_symbol"`
	ByDirection  []Bucket `json:"by_direction"`
	ByRegime     []Bucket `json:"by_regime"`
	ByConfidence []Bucket `json:"by_confidence"`
	ByLeverage   []Bucket `json:"by_leverage"`
	Calibration  []Bucket `json:"calibration"`
}

// Bucket is one statistics slice.
type Bucket struct {
	Key           string   `json:"key"`
	Count         int      `json:"count"`
	Wins          int      `json:"wins"`
	Losses        int      `json:"losses"`
	WinRate       float64  `json:"win_rate"`
	AvgPnL        float64  `json:"average_pnl,omitempty"`
	ExpectedRate  *float64 `json:"expected_rate,omitempty"`
	CalibrationGa *float64 `json:"calibration_gap,omitempty"`
}

// Compute builds the full statistics view for the given window.
func (s *Service) Compute(ctx context.Context, since *time.Time) (Statistics, error) {
	stats := emptyStatistics()
	if since != nil {
		stats.Window = since.UTC().Format(time.RFC3339)
	}

	records, err := s.repos.Recommendations.RecordsWithOutcomes(ctx, repository.HistoryFilter{Since: since, Limit: 2000})
	if err != nil {
		return stats, err
	}

	regimeBuckets := map[string]*Bucket{}
	confidenceBuckets := map[string]*Bucket{}
	calibration := map[string]*Bucket{}

	for _, r := range records {
		stats.Predictions++
		stats.ActionCounts[string(r.Recommendation.Action)]++

		if r.Decision != nil && r.Decision.Decision == domain.DecisionOpened {
			stats.PositionsOpened++
		}
		if r.Outcome == nil || !r.Outcome.Finalized {
			continue
		}
		stats.OutcomesResolved++
		if r.Outcome.Ambiguous {
			stats.AmbiguousOutcome++
		}
		won := r.Outcome.Result == domain.ResultWin

		if regime := string(r.Recommendation.MarketRegime); regime != "" {
			addToBucket(regimeBuckets, regime, won, 0)
		}
		bucket := confidenceBucket(r.Recommendation.Confidence)
		addToBucket(confidenceBuckets, bucket, won, 0)
		addToBucket(calibration, bucket, won, 0)

		if r.Outcome.MFEPct != nil {
			stats.AvgMFE += *r.Outcome.MFEPct
		}
		if r.Outcome.MAEPct != nil {
			stats.AvgMAE += *r.Outcome.MAEPct
		}
	}
	if stats.OutcomesResolved > 0 {
		stats.AvgMFE = round2(stats.AvgMFE / float64(stats.OutcomesResolved))
		stats.AvgMAE = round2(stats.AvgMAE / float64(stats.OutcomesResolved))
	}

	trades, err := s.repos.Positions.ClosedTrades(ctx, since, nil, 2000)
	if err != nil {
		return stats, err
	}
	stats.PositionsClosed = len(trades)
	s.applyTradeStatistics(&stats, trades)

	stats.ByRegime = sortBuckets(regimeBuckets)
	stats.ByConfidence = sortBuckets(confidenceBuckets)
	stats.Calibration = calibrationBuckets(calibration)
	return stats, nil
}

func emptyStatistics() Statistics {
	return Statistics{
		GeneratedAt:  time.Now().UTC(),
		Window:       "all",
		ActionCounts: map[string]int{},
		RealizedPnL:  "0.00",
		BySymbol:     []Bucket{},
		ByDirection:  []Bucket{},
		ByRegime:     []Bucket{},
		ByConfidence: []Bucket{},
		ByLeverage:   []Bucket{},
		Calibration:  []Bucket{},
	}
}

// applyTradeStatistics folds realised trades into the aggregate.
func (s *Service) applyTradeStatistics(stats *Statistics, trades []repository.ClosedTrade) {
	if len(trades) == 0 {
		return
	}
	symbolBuckets := map[string]*Bucket{}
	directionBuckets := map[string]*Bucket{}
	leverageBuckets := map[string]*Bucket{}

	var wins, losses int
	var grossProfit, grossLoss decimal.Decimal
	var total decimal.Decimal
	var pnls []float64
	var holdSum float64

	// Chronological order is needed for a meaningful drawdown.
	ordered := make([]repository.ClosedTrade, len(trades))
	copy(ordered, trades)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].ClosedAt.Before(ordered[j].ClosedAt) })

	var equity, peak, maxDrawdown float64
	for _, t := range ordered {
		net, err := decimal.NewFromString(t.NetPnL)
		if err != nil {
			continue
		}
		total = total.Add(net)
		value, _ := net.Float64()
		pnls = append(pnls, value)
		holdSum += float64(t.HoldMinutes)

		switch {
		case net.IsPositive():
			wins++
			grossProfit = grossProfit.Add(net)
		case net.IsNegative():
			losses++
			grossLoss = grossLoss.Add(net.Abs())
		}

		won := net.IsPositive()
		addToBucket(symbolBuckets, t.Symbol, won, value)
		addToBucket(directionBuckets, string(t.Direction), won, value)
		addToBucket(leverageBuckets, leverageBucket(t.Leverage), won, value)

		equity += value
		if equity > peak {
			peak = equity
		}
		if drawdown := peak - equity; drawdown > maxDrawdown {
			maxDrawdown = drawdown
		}
	}

	resolved := wins + losses
	if resolved > 0 {
		stats.WinRate = round2(float64(wins) / float64(resolved))
		stats.LossRate = round2(float64(losses) / float64(resolved))
	}
	if grossLoss.IsPositive() {
		pf, _ := grossProfit.Div(grossLoss).Float64()
		pf = round2(pf)
		stats.ProfitFactor = &pf
	}
	if len(pnls) > 0 {
		var sum float64
		for _, v := range pnls {
			sum += v
		}
		stats.AvgPnL = round2(sum / float64(len(pnls)))
		stats.MedianPnL = round2(median(pnls))
		stats.Expectancy = stats.AvgPnL
		stats.AvgHoldingMs = round2(holdSum / float64(len(pnls)))
	}
	stats.MaxDrawdown = round2(maxDrawdown)
	stats.RealizedPnL = total.StringFixed(2)

	stats.BySymbol = sortBuckets(symbolBuckets)
	stats.ByDirection = sortBuckets(directionBuckets)
	stats.ByLeverage = sortBuckets(leverageBuckets)
}

func addToBucket(buckets map[string]*Bucket, key string, won bool, pnl float64) {
	b, ok := buckets[key]
	if !ok {
		b = &Bucket{Key: key}
		buckets[key] = b
	}
	b.Count++
	if won {
		b.Wins++
	} else {
		b.Losses++
	}
	b.AvgPnL += pnl
}

func sortBuckets(buckets map[string]*Bucket) []Bucket {
	out := make([]Bucket, 0, len(buckets))
	for _, b := range buckets {
		if b.Count > 0 {
			b.WinRate = round2(float64(b.Wins) / float64(b.Count))
			b.AvgPnL = round2(b.AvgPnL / float64(b.Count))
		}
		out = append(out, *b)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// calibrationBuckets compares stated confidence with the realised success rate.
func calibrationBuckets(buckets map[string]*Bucket) []Bucket {
	out := sortBuckets(buckets)
	for i := range out {
		expected := expectedRateFor(out[i].Key)
		if expected == nil {
			continue
		}
		out[i].ExpectedRate = expected
		gap := round2(out[i].WinRate - *expected)
		out[i].CalibrationGa = &gap
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// expectedRateFor maps a confidence bucket to the success rate it implies.
func expectedRateFor(bucket string) *float64 {
	mid := map[string]float64{
		"0-50": 0.35, "50-60": 0.55, "60-70": 0.65, "70-80": 0.75, "80-90": 0.85, "90-100": 0.95,
	}
	v, ok := mid[bucket]
	if !ok {
		return nil
	}
	return &v
}

func confidenceBucket(confidence int) string {
	switch {
	case confidence < 50:
		return "0-50"
	case confidence < 60:
		return "50-60"
	case confidence < 70:
		return "60-70"
	case confidence < 80:
		return "70-80"
	case confidence < 90:
		return "80-90"
	default:
		return "90-100"
	}
}

func leverageBucket(leverage string) string {
	v, err := strconv.ParseFloat(leverage, 64)
	if err != nil {
		return "unknown"
	}
	switch {
	case v <= 5:
		return "<=5x"
	case v <= 10:
		return "6-10x"
	case v <= 20:
		return "11-20x"
	case v <= 35:
		return "21-35x"
	default:
		return ">35x"
	}
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := make([]float64, len(values))
	copy(sorted, values)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

func rate(wins, total int) *float64 {
	if total == 0 {
		return nil
	}
	v := round2(float64(wins) / float64(total))
	return &v
}

func round2(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*100) / 100
}

func round3(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*1000) / 1000
}
