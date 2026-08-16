package features

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

// timeframeWeights decide how much each timeframe contributes to the
// multi-timeframe verdict. Higher timeframes carry more weight because they
// set the context the lower ones trade inside.
var timeframeWeights = map[domain.Timeframe]float64{
	domain.TF1m:  0.4,
	domain.TF5m:  0.7,
	domain.TF15m: 1.0,
	domain.TF1h:  1.5,
	domain.TF4h:  2.0,
	domain.TF1d:  2.5,
}

// SnapshotInput carries everything needed to assemble a feature snapshot.
type SnapshotInput struct {
	Symbol          string
	Price           float64
	Market          domain.MarketInfo
	Timeframes      map[domain.Timeframe]domain.TimeframeAnalysis
	ActivePositions []domain.PositionContext
	Performance     domain.HistoricalPerformance
	SimilarCases    []domain.SimilarCase
	RecentCandles   []domain.Candle
	DataQuality     domain.DataQuality
	// MarketContext is the state of the benchmark, computed once per cycle and
	// shared by every symbol analysed in it.
	MarketContext domain.MarketContext
	// UniverseContext is where this symbol stands among the tracked assets.
	UniverseContext domain.UniverseContext
	Now             time.Time
}

// BuildSnapshot aggregates per-timeframe analyses into the final snapshot.
func BuildSnapshot(in SnapshotInput) domain.FeatureSnapshot {
	snapshot := domain.FeatureSnapshot{
		SchemaVersion:         domain.SchemaVersion,
		Timestamp:             in.Now.UTC(),
		Symbol:                in.Symbol,
		Price:                 in.Price,
		Market:                in.Market,
		Timeframes:            in.Timeframes,
		ActivePositions:       in.ActivePositions,
		HistoricalPerformance: in.Performance,
		SimilarCases:          in.SimilarCases,
		DataQuality:           in.DataQuality,
		RecentCandles:         in.RecentCandles,
	}

	snapshot.MarketContext = in.MarketContext
	snapshot.UniverseContext = in.UniverseContext
	snapshot.LatestClosedCandle = latestClosedCandle(in.Timeframes)
	snapshot.TrendAlignment = alignment(in.Timeframes)
	snapshot.AggregateScores = aggregateScores(in.Timeframes)
	snapshot.AggregateRegime = aggregateRegime(in.Timeframes)
	snapshot.KeyLevels = mergeLevels(in.Timeframes)
	return snapshot
}

func latestClosedCandle(tfs map[domain.Timeframe]domain.TimeframeAnalysis) time.Time {
	var latest time.Time
	for _, a := range tfs {
		if a.LastClosedCandle.After(latest) {
			latest = a.LastClosedCandle
		}
	}
	return latest
}

// alignment groups timeframes by bias and scores how much they agree.
func alignment(tfs map[domain.Timeframe]domain.TimeframeAnalysis) domain.TrendAlignment {
	out := domain.TrendAlignment{
		Bullish: []domain.Timeframe{},
		Bearish: []domain.Timeframe{},
		Neutral: []domain.Timeframe{},
	}
	var weighted, total float64

	for tf, a := range tfs {
		w := timeframeWeights[tf]
		if w == 0 {
			w = 1
		}
		total += w
		switch a.Bias {
		case domain.PatternBullish:
			out.Bullish = append(out.Bullish, tf)
			weighted += w
		case domain.PatternBearish:
			out.Bearish = append(out.Bearish, tf)
			weighted -= w
		default:
			out.Neutral = append(out.Neutral, tf)
		}
	}
	domain.SortTimeframes(out.Bullish)
	domain.SortTimeframes(out.Bearish)
	domain.SortTimeframes(out.Neutral)

	if total > 0 {
		out.AlignmentScore = math.Round(weighted/total*100) / 100
	}
	out.Conflicts = conflicts(tfs)
	return out
}

// conflicts names the disagreements a trader would actually care about: a fast
// timeframe pointing against a slow one.
func conflicts(tfs map[domain.Timeframe]domain.TimeframeAnalysis) []string {
	ordered := make([]domain.Timeframe, 0, len(tfs))
	for tf := range tfs {
		ordered = append(ordered, tf)
	}
	domain.SortTimeframes(ordered)

	var out []string
	for i := 0; i < len(ordered); i++ {
		for j := i + 1; j < len(ordered); j++ {
			fast, slow := tfs[ordered[i]], tfs[ordered[j]]
			if fast.Bias == domain.PatternNeutral || slow.Bias == domain.PatternNeutral {
				continue
			}
			if fast.Bias != slow.Bias && timeframeWeights[ordered[j]]/timeframeWeights[ordered[i]] >= 2 {
				out = append(out, fmt.Sprintf("%s %s vs %s %s", ordered[i], fast.Bias, ordered[j], slow.Bias))
			}
		}
	}
	if len(out) > 4 {
		out = out[:4]
	}
	return out
}

func aggregateScores(tfs map[domain.Timeframe]domain.TimeframeAnalysis) domain.SignalScores {
	var trend, momentum, patternScore, volRisk, net, total float64

	for tf, a := range tfs {
		w := timeframeWeights[tf]
		if w == 0 {
			w = 1
		}
		trend += a.Scores.Trend * w
		momentum += a.Scores.Momentum * w
		patternScore += a.Scores.Pattern * w
		volRisk += a.Scores.VolatilityRisk * w
		net += a.Scores.Net * w
		total += w
	}
	if total == 0 {
		return domain.SignalScores{DeterministicBias: "neutral"}
	}

	scores := domain.SignalScores{
		Trend:          round2(trend / total),
		Momentum:       round2(momentum / total),
		Pattern:        round2(patternScore / total),
		VolatilityRisk: round2(volRisk / total),
		Net:            round2(net / total),
	}
	scores.TechnicalBull = round2(math.Max(0, scores.Net))
	scores.TechnicalBear = round2(math.Max(0, -scores.Net))

	switch {
	case scores.Net >= 0.25:
		scores.DeterministicBias = "bullish"
	case scores.Net <= -0.25:
		scores.DeterministicBias = "bearish"
	default:
		scores.DeterministicBias = "neutral"
	}
	return scores
}

// aggregateRegime takes the 1h regime as the primary read when available,
// falling back to the slowest timeframe present, and unions the tags.
func aggregateRegime(tfs map[domain.Timeframe]domain.TimeframeAnalysis) domain.Regime {
	preferred := []domain.Timeframe{domain.TF1h, domain.TF4h, domain.TF15m, domain.TF1d, domain.TF5m, domain.TF1m}

	out := domain.Regime{Primary: domain.RegimeUncertain}
	for _, tf := range preferred {
		if a, ok := tfs[tf]; ok && a.Regime.Primary != "" {
			out.Primary = a.Regime.Primary
			out.Score = a.Regime.Score
			break
		}
	}

	seen := map[domain.RegimeTag]int{}
	for _, a := range tfs {
		for _, tag := range a.Regime.Tags {
			seen[tag]++
		}
	}
	for tag, count := range seen {
		// A tag needs to show up on more than one timeframe to be reported,
		// unless only one timeframe was analysed at all.
		if count >= 2 || len(tfs) == 1 {
			out.Tags = append(out.Tags, tag)
		}
	}
	sort.Slice(out.Tags, func(i, j int) bool { return out.Tags[i] < out.Tags[j] })
	return out
}

// mergeLevels collects the most relevant levels across timeframes.
func mergeLevels(tfs map[domain.Timeframe]domain.TimeframeAnalysis) []domain.Level {
	var all []domain.Level
	for tf, a := range tfs {
		w := timeframeWeights[tf]
		if w == 0 {
			w = 1
		}
		for _, l := range a.Levels {
			l.Timeframe = tf
			l.Strength = math.Min(1, l.Strength*(0.6+w/5))
			all = append(all, l)
		}
	}
	if len(all) == 0 {
		return nil
	}

	sort.SliceStable(all, func(i, j int) bool {
		return levelRelevance(all[i]) > levelRelevance(all[j])
	})

	// Deduplicate levels that land within 0.3% of an already selected one.
	var out []domain.Level
	for _, l := range all {
		duplicate := false
		for _, kept := range out {
			if kept.Price != 0 && math.Abs(kept.Price-l.Price)/kept.Price*100 < 0.3 {
				duplicate = true
				break
			}
		}
		if !duplicate {
			out = append(out, l)
		}
		if len(out) >= 10 {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Price < out[j].Price })
	return out
}

func levelRelevance(l domain.Level) float64 {
	return l.Strength / (1 + math.Abs(l.DistancePct)/4)
}

func round2(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*100) / 100
}

// FeatureVector projects a snapshot into a fixed-length normalised vector used
// by the nearest-neighbour search for similar historical cases.
//
// The layout is fixed and versioned by position: appending new dimensions at
// the end keeps older stored vectors comparable for their common prefix.
func FeatureVector(s domain.FeatureSnapshot) []float64 {
	tfOrder := []domain.Timeframe{domain.TF5m, domain.TF15m, domain.TF1h, domain.TF4h}
	vec := make([]float64, 0, 4*6+6)

	for _, tf := range tfOrder {
		a, ok := s.Timeframes[tf]
		if !ok {
			vec = append(vec, 0, 0, 0, 0, 0, 0)
			continue
		}
		vec = append(vec,
			norm(a.Indicators.RSI, 0, 100),
			signOf(a.Indicators.MACDHistogram),
			norm(a.Indicators.ADX, 0, 60),
			norm(a.Indicators.ATRPct, 0, 5),
			clamp(a.Scores.Trend, -1, 1),
			clamp(a.Scores.Momentum, -1, 1),
		)
	}

	vec = append(vec,
		clamp(s.TrendAlignment.AlignmentScore, -1, 1),
		clamp(s.AggregateScores.Net, -1, 1),
		clamp(s.AggregateScores.Pattern, -1, 1),
		clamp(s.AggregateScores.VolatilityRisk, 0, 1),
		regimeCode(s.AggregateRegime.Primary),
		structureCode(s.Timeframes),
	)
	return vec
}

func norm(v *float64, lo, hi float64) float64 {
	if v == nil || hi == lo {
		return 0
	}
	return clamp((*v-lo)/(hi-lo), 0, 1)
}

func signOf(v *float64) float64 {
	if v == nil {
		return 0
	}
	switch {
	case *v > 0:
		return 1
	case *v < 0:
		return -1
	default:
		return 0
	}
}

func clamp(v, lo, hi float64) float64 { //nolint:unparam // hi is 1 today; keeping the bound explicit documents the range
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Max(lo, math.Min(hi, v))
}

// regimeCode maps regimes onto a bearish..bullish axis in [-1,1].
func regimeCode(r domain.MarketRegime) float64 {
	switch r {
	case domain.RegimeStrongUptrend:
		return 1
	case domain.RegimeWeakUptrend:
		return 0.5
	case domain.RegimeBreakout:
		return 0.25
	case domain.RegimeRange, domain.RegimeCompression:
		return 0
	case domain.RegimeWeakDowntrend:
		return -0.5
	case domain.RegimeStrongDowntrend:
		return -1
	default:
		return 0
	}
}

func structureCode(tfs map[domain.Timeframe]domain.TimeframeAnalysis) float64 {
	a, ok := tfs[domain.TF1h]
	if !ok {
		return 0
	}
	switch a.Structure.State {
	case domain.StructureBullish:
		return 1
	case domain.StructureBearish:
		return -1
	case domain.StructureTransition:
		return 0.25
	default:
		return 0
	}
}

// Similarity returns the cosine-like similarity of two feature vectors in [0,1].
func Similarity(a, b []float64) float64 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	if n == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := 0; i < n; i++ {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	cos := dot / (math.Sqrt(na) * math.Sqrt(nb))
	return math.Round((cos+1)/2*1000) / 1000
}
