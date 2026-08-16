// Package risk implements the deterministic risk engine. The model may suggest
// leverage and allocation, but this package has the final word: it recomputes
// the allowed range from volatility, stop distance, regime, confidence and data
// quality, and clamps the model's numbers into it.
package risk

import (
	"fmt"
	"math"
	"sync"

	"github.com/shopspring/decimal"

	"github.com/crypto-market-advisor/advisor/internal/config"
	"github.com/crypto-market-advisor/advisor/internal/domain"
)

// MaxLossOnMarginPct is the loss on margin the widest stop is allowed to imply.
// With leverage L and a stop distance of D percent, hitting the stop costs
// roughly L*D percent of margin; capping that keeps a single stop-out from
// erasing the position.
const MaxLossOnMarginPct = 35.0

// Assessment is the risk engine's verdict.
type Assessment struct {
	Leverage      domain.LeveragePlan
	AllocationPct decimal.Decimal
	RiskLevel     domain.RiskLevel
	Notes         []string
	StopDistance  *float64
	Rejected      bool
	RejectReason  string
}

// Input carries everything the engine evaluates.
type Input struct {
	Action         domain.RecommendationAction
	Confidence     int
	LLMLeverage    int
	LLMAllocation  decimal.Decimal
	LLMRiskLevel   domain.RiskLevel
	ReferencePrice float64
	StopLoss       []domain.PriceTarget
	Snapshot       domain.FeatureSnapshot
}

// Engine applies risk policy. Its configuration is swapped at runtime from the
// settings screen while analyses and backtests evaluate in the background, so
// every read goes through the lock.
type Engine struct {
	mu  sync.RWMutex
	cfg config.RiskConfig
}

// New builds the risk engine.
func New(cfg config.RiskConfig) *Engine { return &Engine{cfg: cfg} }

func (e *Engine) config() config.RiskConfig {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.cfg
}

// Evaluate computes the risk-adjusted plan.
func (e *Engine) Evaluate(in Input) Assessment {
	a := Assessment{
		Leverage: domain.LeveragePlan{
			LLMSuggested: in.LLMLeverage,
			RiskMaximum:  e.config().MaxLeverage,
			Recommended:  in.LLMLeverage,
		},
		AllocationPct: in.LLMAllocation,
		RiskLevel:     in.LLMRiskLevel,
	}

	if !in.Action.IsEntry() {
		// Nothing to size: management and no-action carry no new exposure.
		a.Leverage = domain.LeveragePlan{}
		a.AllocationPct = decimal.Zero
		return a
	}

	maxLev := float64(e.config().MaxLeverage)
	note := func(format string, args ...any) {
		a.Notes = append(a.Notes, fmt.Sprintf(format, args...))
	}
	if newsCap, newsNote := e.NewsLeverageCap(in.Snapshot); newsCap < int(maxLev) {
		maxLev = float64(newsCap)
		note("%s", newsNote)
	}

	// 1. Stop distance is the hard constraint: it bounds the loss per stop-out.
	if d, ok := stopDistancePct(in); ok {
		a.StopDistance = &d
		if d > 0 {
			byStop := MaxLossOnMarginPct / d
			if byStop < maxLev {
				note("stop distance %.2f%% caps leverage at %.1fx (max %.0f%% loss on margin)", d, byStop, MaxLossOnMarginPct)
				maxLev = byStop
			}
		}
	} else {
		note("no usable stop distance: leverage capped conservatively")
		maxLev = math.Min(maxLev, float64(e.config().MinLeverage)*2)
	}

	// 2. Volatility.
	if atrPct, ok := primaryATRPct(in.Snapshot); ok {
		switch {
		case atrPct >= e.config().ExtremeVolatilityATRPct:
			cap := math.Max(float64(e.config().MinLeverage), 8)
			note("extreme volatility (ATR %.2f%%) caps leverage at %.0fx", atrPct, cap)
			maxLev = math.Min(maxLev, cap)
		case atrPct >= e.config().HighVolatilityATRPct:
			cap := math.Max(float64(e.config().MinLeverage), 15)
			note("elevated volatility (ATR %.2f%%) caps leverage at %.0fx", atrPct, cap)
			maxLev = math.Min(maxLev, cap)
		}
	} else {
		note("ATR unavailable: leverage reduced for unknown volatility")
		maxLev *= 0.6
	}

	if p, ok := volatilityPercentile(in.Snapshot); ok && p >= 85 {
		note("volatility is in the %.0fth percentile of recent history", p)
		maxLev *= 0.8
	}

	// 3. Regime.
	switch in.Snapshot.AggregateRegime.Primary {
	case domain.RegimeUncertain:
		note("uncertain regime reduces allowed leverage")
		maxLev *= 0.6
	case domain.RegimeRange:
		note("range regime reduces allowed leverage")
		maxLev *= 0.8
	case domain.RegimeBreakout:
		note("breakout regime carries retest risk")
		maxLev *= 0.9
	}
	for _, tag := range in.Snapshot.AggregateRegime.Tags {
		if tag == domain.TagHighVolatility {
			maxLev *= 0.85
			note("high volatility tag reduces allowed leverage")
			break
		}
	}

	// 4. Confidence.
	switch {
	case in.Confidence < e.config().MinConfidence:
		note("confidence %d is below the %d threshold", in.Confidence, e.config().MinConfidence)
		maxLev = math.Min(maxLev, float64(e.config().MinLeverage))
	case in.Confidence < 70:
		maxLev = math.Min(maxLev, 20)
		note("moderate confidence caps leverage at 20x")
	}

	// 5. Multi-timeframe agreement.
	alignment := math.Abs(in.Snapshot.TrendAlignment.AlignmentScore)
	if alignment < 0.2 {
		note("timeframes disagree (alignment %.2f)", in.Snapshot.TrendAlignment.AlignmentScore)
		maxLev *= 0.8
	}

	// 6. An opposing level right in front of the trade.
	if blocked, distance := opposingLevelNearby(in); blocked {
		note("opposing level %.2f%% away limits the runway", distance)
		maxLev *= 0.85
	}

	// 7. Data quality.
	if in.Snapshot.DataQuality.Status != domain.DataQualityOK {
		note("data quality is %s", in.Snapshot.DataQuality.Status)
		maxLev *= 0.7
	}

	riskMax := int(math.Floor(maxLev))
	if riskMax < e.config().MinLeverage {
		riskMax = e.config().MinLeverage
	}
	if riskMax > e.config().MaxLeverage {
		riskMax = e.config().MaxLeverage
	}
	a.Leverage.RiskMaximum = riskMax

	recommended := in.LLMLeverage
	if recommended <= 0 {
		recommended = e.config().MinLeverage
	}
	if recommended > riskMax {
		note("model suggested %dx, risk-adjusted to %dx", in.LLMLeverage, riskMax)
		recommended = riskMax
	}
	if recommended < e.config().MinLeverage {
		recommended = e.config().MinLeverage
	}
	a.Leverage.Recommended = recommended
	a.Leverage.RiskReason = summarize(a.Notes)

	a.AllocationPct = e.allocation(in, recommended, riskMax, &a)
	a.RiskLevel = e.riskLevel(in, recommended, riskMax, a.Notes)
	return a
}

// NewsLeverageCap is a direction-neutral preflight rule. It never turns news
// into LONG/SHORT scoring; it only limits exposure around a fresh critical
// event, with a stricter cap when technical volatility is already elevated.
func (e *Engine) NewsLeverageCap(snapshot domain.FeatureSnapshot) (int, string) {
	critical := false
	maxAgeMinutes := int(e.config().CriticalNewsMaxAge.Minutes())
	for _, group := range [][]domain.NewsSnapshotItem{snapshot.NewsContext.AssetSpecific, snapshot.NewsContext.Global} {
		for _, item := range group {
			if item.Critical && item.AgeMinutes <= maxAgeMinutes {
				critical = true
				break
			}
		}
	}
	if !critical {
		return e.config().MaxLeverage, ""
	}
	highVolatility := false
	if atrPct, ok := primaryATRPct(snapshot); ok && atrPct >= e.config().HighVolatilityATRPct {
		highVolatility = true
	}
	for _, tag := range snapshot.AggregateRegime.Tags {
		if tag == domain.TagHighVolatility {
			highVolatility = true
		}
	}
	if highVolatility {
		return e.config().CriticalNewsHighVolMaxLeverage, "fresh critical news with elevated volatility caps leverage"
	}
	return e.config().CriticalNewsMaxLeverage, "fresh critical news caps leverage without implying direction"
}

// MinRiskSizedAllocationPct is the floor of a risk-derived size. Below this a
// position is too small to be worth the round trip, and the sizing rule can
// produce arbitrarily small numbers when the stop is very far away.
const MinRiskSizedAllocationPct = 0.5

// riskSizedAllocation turns "a stop-out may cost R% of the account" into a share
// of capital, given the leverage the position will carry and how far its stop
// sits.
//
// With margin M, leverage L and a stop D percent away, hitting the stop costs
// M*L*D/100. Setting that equal to R% of equity gives the allocation directly.
// The point is that a quiet asset and a volatile one then carry the same risk
// instead of the same size, which is what a fixed percentage gets wrong: it
// hands the biggest exposure to the instrument most likely to move against it.
func riskSizedAllocation(riskPerTradePct float64, leverage int, stopDistancePct float64) (decimal.Decimal, bool) {
	if riskPerTradePct <= 0 || leverage <= 0 || stopDistancePct <= 0 {
		return decimal.Zero, false
	}
	allocation := riskPerTradePct * 100 / (float64(leverage) * stopDistancePct)
	if math.IsNaN(allocation) || math.IsInf(allocation, 0) || allocation <= 0 {
		return decimal.Zero, false
	}
	return decimal.NewFromFloat(allocation), true
}

// allocation caps the advisory position size.
//
// leverage is the leverage the position will actually carry, not the one the
// model asked for: the risk of a stop-out is margin * leverage * distance, so
// sizing against a leverage the engine already refused would leave the trade
// carrying a fraction of the intended risk.
func (e *Engine) allocation(in Input, leverage, riskMax int, a *Assessment) decimal.Decimal {
	alloc := in.LLMAllocation
	if alloc.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero
	}
	// A configured risk budget replaces the requested size rather than capping
	// it: the whole point is that the size follows the stop, not the caller.
	if risk := e.config().RiskPerTradePct; risk > 0 {
		if distance, ok := stopDistancePct(in); ok {
			if sized, ok := riskSizedAllocation(risk, leverage, distance); ok {
				a.Notes = append(a.Notes, fmt.Sprintf(
					"size %s%% risks %.2f%% of capital at a %.2f%% stop", sized.StringFixed(2), risk, distance))
				alloc = sized
			}
		}
	}
	maxAlloc := e.config().MaxRecommendedAllocPct

	if alloc.GreaterThan(maxAlloc) {
		a.Notes = append(a.Notes, fmt.Sprintf("allocation reduced from %s%% to the configured maximum %s%%",
			alloc.StringFixed(1), maxAlloc.StringFixed(1)))
		alloc = maxAlloc
	}

	// Low confidence and low allowed leverage both argue for a smaller size.
	if in.Confidence < 65 {
		scaled := alloc.Mul(decimal.NewFromFloat(0.7)).Round(2)
		a.Notes = append(a.Notes, fmt.Sprintf("allocation scaled to %s%% for confidence %d", scaled.StringFixed(1), in.Confidence))
		alloc = scaled
	}
	if riskMax <= e.config().MinLeverage {
		scaled := alloc.Mul(decimal.NewFromFloat(0.8)).Round(2)
		a.Notes = append(a.Notes, "allocation scaled down because allowed leverage is at the floor")
		alloc = scaled
	}
	if in.Snapshot.DataQuality.Status != domain.DataQualityOK {
		alloc = alloc.Mul(decimal.NewFromFloat(0.7)).Round(2)
	}
	if e.config().RiskPerTradePct > 0 && alloc.LessThan(decimal.NewFromFloat(MinRiskSizedAllocationPct)) {
		alloc = decimal.NewFromFloat(MinRiskSizedAllocationPct)
	}
	return alloc.Round(2)
}

// riskLevel recomputes the qualitative risk from the final numbers rather than
// trusting the model's own label.
//
// The severity of the engine's own intervention counts as evidence: if the
// allowed leverage had to be cut far below the configured ceiling, or several
// constraints fired at once, the setup is not a "low risk" one no matter how
// tame the raw ATR looks.
func (e *Engine) riskLevel(in Input, leverage, riskMax int, notes []string) domain.RiskLevel {
	score := 0.0

	if atrPct, ok := primaryATRPct(in.Snapshot); ok {
		score += math.Min(2, atrPct/e.config().HighVolatilityATRPct)
	} else {
		score++
	}
	if p, ok := volatilityPercentile(in.Snapshot); ok && p >= 85 {
		score += 1 + (p-85)/30 // 85th percentile and up is genuinely stretched
	}

	score += float64(leverage) / float64(e.config().MaxLeverage) * 2

	// How hard the engine had to clamp, relative to the configured ceiling.
	if e.config().MaxLeverage > e.config().MinLeverage {
		cut := 1 - float64(riskMax-e.config().MinLeverage)/float64(e.config().MaxLeverage-e.config().MinLeverage)
		score += math.Max(0, cut) * 1.5
	}
	if len(notes) >= 4 {
		score += 0.5
	}

	if in.Confidence < 60 {
		score++
	}
	if in.Snapshot.DataQuality.Status != domain.DataQualityOK {
		score += 0.5
	}
	score += in.Snapshot.AggregateScores.VolatilityRisk

	switch {
	case score >= 5:
		return domain.RiskExtreme
	case score >= 3.4:
		return domain.RiskHigh
	case score >= 1.8:
		return domain.RiskMedium
	default:
		return domain.RiskLow
	}
}

// stopDistancePct returns the distance to the furthest stop, in percent.
func stopDistancePct(in Input) (float64, bool) {
	if in.ReferencePrice <= 0 || len(in.StopLoss) == 0 {
		return 0, false
	}
	worst := 0.0
	for _, sl := range in.StopLoss {
		d := math.Abs(sl.Price-in.ReferencePrice) / in.ReferencePrice * 100
		if d > worst {
			worst = d
		}
	}
	if worst <= 0 {
		return 0, false
	}
	return worst, true
}

// primaryATRPct prefers the 1h reading and falls back to nearby timeframes.
func primaryATRPct(s domain.FeatureSnapshot) (float64, bool) {
	for _, tf := range []domain.Timeframe{domain.TF1h, domain.TF15m, domain.TF4h, domain.TF5m, domain.TF1d} {
		if a, ok := s.Timeframes[tf]; ok && a.Indicators.ATRPct != nil {
			return *a.Indicators.ATRPct, true
		}
	}
	return 0, false
}

func volatilityPercentile(s domain.FeatureSnapshot) (float64, bool) {
	for _, tf := range []domain.Timeframe{domain.TF1h, domain.TF4h, domain.TF15m} {
		if a, ok := s.Timeframes[tf]; ok {
			if a.Indicators.ATRPercentile != nil {
				return *a.Indicators.ATRPercentile, true
			}
			if a.Indicators.VolPercentile != nil {
				return *a.Indicators.VolPercentile, true
			}
		}
	}
	return 0, false
}

// opposingLevelNearby reports a support/resistance level standing in the way of
// the trade within one percent of price.
func opposingLevelNearby(in Input) (bool, float64) {
	direction, ok := in.Action.Direction()
	if !ok || in.ReferencePrice <= 0 {
		return false, 0
	}
	want := domain.LevelResistance
	if direction == domain.DirectionShort {
		want = domain.LevelSupport
	}

	nearest := math.Inf(1)
	for _, l := range in.Snapshot.KeyLevels {
		if l.Type != want || l.Strength < 0.4 {
			continue
		}
		d := math.Abs(l.DistancePct)
		if direction == domain.DirectionLong && l.Price <= in.ReferencePrice {
			continue
		}
		if direction == domain.DirectionShort && l.Price >= in.ReferencePrice {
			continue
		}
		if d < nearest {
			nearest = d
		}
	}
	if math.IsInf(nearest, 1) || nearest > 1.0 {
		return false, 0
	}
	return true, nearest
}

func summarize(notes []string) string {
	if len(notes) == 0 {
		return "no risk adjustments applied"
	}
	if len(notes) == 1 {
		return notes[0]
	}
	return fmt.Sprintf("%s (+%d more constraints)", notes[0], len(notes)-1)
}

// SetConfig swaps the risk policy at runtime when the user edits settings.
func (e *Engine) SetConfig(cfg config.RiskConfig) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cfg = cfg
}

// Config returns the active risk policy.
func (e *Engine) Config() config.RiskConfig { return e.config() }
