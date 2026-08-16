package strategies

import (
	"fmt"
	"math"
	"time"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

// Input is everything a strategy may read. The primary timeframe carries the
// per-timeframe analysis; cross-timeframe facts, levels and news come from the
// snapshot the same analysis cycle produced.
type Input struct {
	Timeframe domain.Timeframe
	Analysis  domain.TimeframeAnalysis
	Snapshot  domain.FeatureSnapshot
	// Candles is the closed history visible at this moment, oldest first. It
	// lets a strategy compute a series the shared analysis does not carry, such
	// as a Donchian channel or a two-period RSI.
	Candles []domain.Candle
	Price   float64
	Now     time.Time
	// CriticalNewsMaxAge bounds what still counts as fresh critical news.
	CriticalNewsMaxAge time.Duration
	// RoundTripCostPct is what opening and closing one position costs in percent
	// of notional: both fees plus the expected slippage.
	RoundTripCostPct float64
	// CostFloorMultiple is how many round trips the realistic target has to be
	// worth before the trade is allowed. Zero means DefaultCostFloorMultiple.
	CostFloorMultiple float64
	// MinRelativeStrengthPct is the percentile an asset must reach among the
	// tracked universe before it is worth one of the few available slots. Zero
	// means DefaultMinRelativeStrengthPct.
	MinRelativeStrengthPct float64
	// MarketGateLongBufferPct is the buffer a long demands: the benchmark has to
	// stand at least this far above its own long average, not merely above it.
	// Zero asks for no buffer, which is the shipped behaviour.
	MarketGateLongBufferPct float64
	// MarketGateAllowFallingAverage lifts the demand that the benchmark average
	// itself be rising. The demand is on by default: price above a falling
	// average is a rally inside a downtrend, and trading those is what the
	// policy lost most of its money to.
	MarketGateAllowFallingAverage bool
}

// opinion is the raw result of one strategy before weighting.
type opinion struct {
	direction domain.PatternDirection
	blocks    domain.StrategyBlocks
	strength  float64
	detail    string
}

var noOpinion = opinion{}

// Evaluate folds the enabled strategies into one decision.
//
// Directional weight competes with blocking weight: a side is taken only when
// its net weighted margin clears both the configured minimum and everything
// arguing against it. A filter marked as a hard veto stops the entry outright,
// which is what a fresh critical news event needs.
func Evaluate(in Input, set domain.StrategySet) domain.StrategyDecision {
	set = Normalize(set)
	decision := domain.StrategyDecision{
		Action:      domain.RecommendationNoAction,
		MinSignal:   set.MinSignal,
		Timeframe:   in.Timeframe,
		EvaluatedAt: in.Now.UTC(),
		Reason:      domain.StrategyReasonNoStrategies,
	}

	var directionalWeight float64
	filters := make([]domain.StrategyVote, 0, 4)

	for _, cfg := range set.Items {
		if !cfg.Enabled || cfg.Weight == 0 {
			continue
		}
		result := evaluateOne(cfg.ID, in)
		kind := kindOf(cfg.ID)

		if kind == domain.StrategyFilter {
			// A filter argues against a trade; arguing "less than nothing" has no
			// meaning, so its weight is taken as a magnitude.
			weight := math.Abs(cfg.Weight)
			if result.blocks == domain.BlocksNothing || result.strength <= 0 {
				continue
			}
			filters = append(filters, domain.StrategyVote{
				ID: cfg.ID, Kind: kind, Blocks: result.blocks, Strength: round2(result.strength),
				Weight: weight, Score: round3(weight * result.strength),
				HardVeto: cfg.HardVeto, Detail: result.detail,
			})
			continue
		}

		// A negative weight fades the strategy: its vote counts for the opposite
		// side. That is what makes a counter-trend profile expressible at all.
		style := styleOf(cfg.ID)
		weight := cfg.Weight * regimeMultiplier(set.RegimeAdaptive, style, in)
		directionalWeight += math.Abs(weight)
		if result.direction == domain.PatternNeutral || result.direction == "" || result.strength <= 0 {
			continue
		}
		score := weight * result.strength
		if result.direction == domain.PatternBearish {
			score = -score
		}
		vote := domain.StrategyVote{
			ID: cfg.ID, Kind: kind, Style: style, Direction: result.direction,
			Strength: round2(result.strength), Weight: round3(weight),
			Score: round3(score), Detail: result.detail,
		}
		decision.Votes = append(decision.Votes, vote)
		if score > 0 {
			decision.LongScore += score
		} else {
			decision.ShortScore += -score
		}
	}

	decision.LongScore = round3(decision.LongScore)
	decision.ShortScore = round3(decision.ShortScore)
	decision.NetScore = round3(decision.LongScore - decision.ShortScore)
	if directionalWeight == 0 && len(filters) == 0 {
		return decision
	}

	direction := domain.DirectionLong
	if decision.NetScore < 0 {
		direction = domain.DirectionShort
	}
	// Only the filters that argue against the side under consideration count.
	blocking := 0.0
	vetoed := false
	for _, filter := range filters {
		if !filterApplies(filter.Blocks, direction) {
			continue
		}
		blocking += filter.Score
		vetoed = vetoed || filter.HardVeto
	}
	decision.BlockScore = round3(blocking)
	decision.Votes = append(decision.Votes, filters...)

	net := math.Abs(decision.NetScore)
	switch {
	case net == 0:
		decision.Reason = domain.StrategyReasonNoDirection
		return decision
	case !set.Sides.Allows(direction):
		decision.Reason = domain.StrategyReasonSideDisabled
		return decision
	case vetoed:
		decision.Reason = domain.StrategyReasonVetoed
		return decision
	case net < set.MinSignal:
		decision.Reason = domain.StrategyReasonBelowMinSignal
		return decision
	case net <= decision.BlockScore:
		decision.Reason = domain.StrategyReasonBlocked
		return decision
	}

	decision.Direction = direction
	decision.Action = domain.RecommendationOpenLong
	if direction == domain.DirectionShort {
		decision.Action = domain.RecommendationOpenShort
	}
	decision.Reason = domain.StrategyReasonEntry
	decision.Confidence = confidenceOf(
		math.Max(decision.LongScore, decision.ShortScore),
		math.Min(decision.LongScore, decision.ShortScore),
		directionalWeight,
	)
	return decision
}

// regimeMultiplier scales a vote by how well its style suits the market the
// deterministic classifier reports.
//
// The ensemble otherwise averages a trend follower and a mean reverter with the
// same authority in every market, which is how contradictory voices cancel into
// noise. A trend that exists is an argument for the followers; a range is an
// argument for the faders. Nothing is silenced - the losing style keeps a
// reduced voice, because the classifier is itself an estimate.
func regimeMultiplier(enabled bool, style domain.StrategyStyle, in Input) float64 {
	if !enabled || style == domain.StyleNeutral {
		return 1
	}
	const boost, damp = 1.15, 0.85

	trending, ranging := false, false
	switch in.Analysis.Regime.Primary {
	case domain.RegimeStrongUptrend, domain.RegimeStrongDowntrend, domain.RegimeBreakout:
		trending = true
	case domain.RegimeRange, domain.RegimeCompression:
		ranging = true
	}
	// ADX is the second opinion: a weak trend label with a strong ADX still
	// counts as a trend, and the reverse holds too.
	if adx, ok := ptr(in.Analysis.Indicators.ADX); ok {
		switch {
		case adx >= 25:
			trending, ranging = true, false
		case adx < 18:
			ranging, trending = true, false
		}
	}

	switch {
	case trending && style == domain.StyleTrend:
		return boost
	case trending && style == domain.StyleReversion:
		return damp
	case ranging && style == domain.StyleTrend:
		return damp
	case ranging && style == domain.StyleReversion:
		return boost
	default:
		return 1
	}
}

// filterApplies reports whether a filter argues against the given side.
func filterApplies(blocks domain.StrategyBlocks, direction domain.Direction) bool {
	switch blocks {
	case domain.BlocksBoth:
		return true
	case domain.BlocksLong:
		return direction == domain.DirectionLong
	case domain.BlocksShort:
		return direction == domain.DirectionShort
	default:
		return false
	}
}

// confidenceOf maps the vote onto the 0-100 scale the rest of the system speaks.
//
// Two different things make a deterministic signal trustworthy: how one-sided
// the directional vote was, and how much of the enabled policy spoke at all -
// many strategies stay silent on any given bar, and silence is not dissent. The
// filters are not subtracted here: they have already acted as a gate, and
// counting them twice pushed every entry into the 50-60 band, where the
// configured minimum confidence then rejected practically everything.
func confidenceOf(winning, losing, totalWeight float64) int {
	if winning <= 0 {
		return 50
	}
	agreement := winning / (winning + losing) // 0.5 when evenly split, 1 when unopposed
	coverage := 0.0
	if totalWeight > 0 {
		coverage = winning / totalWeight
	}
	// Both factors have to be present: a lone strategy nobody contradicts is not
	// the same as half the policy pulling the same way, so they multiply rather
	// than average.
	score := clamp((agreement-0.5)*2, 0, 1) * clamp(coverage/0.5, 0, 1)
	return int(math.Round(50 + 45*score))
}

// evaluateOne dispatches to the individual strategy implementations.
func evaluateOne(id string, in Input) opinion {
	switch id {
	case IDCompositeScore:
		return compositeScore(in)
	case IDHigherTimeframe:
		return higherTimeframe(in)
	case IDDonchianBreakout:
		return donchianBreakout(in)
	case IDSuperTrend:
		return superTrend(in)
	case IDRSI2Reversion:
		return rsi2Reversion(in)
	case IDRegimeMomentum:
		return regimeMomentum(in)
	case IDCapitulationLong:
		return capitulationLong(in)
	case IDBearRallyFade:
		return bearRallyFade(in)
	case IDEMATrend:
		return emaTrend(in)
	case IDADXDirectional:
		return adxDirectional(in)
	case IDMACD:
		return macdStrategy(in)
	case IDMarketStructure:
		return marketStructure(in)
	case IDChartPatterns:
		return chartPatterns(in)
	case IDCandlePatterns:
		return candlePatterns(in)
	case IDDivergences:
		return divergences(in)
	case IDRSIReversion:
		return rsiReversion(in)
	case IDBreakout:
		return breakout(in)
	case IDMomentum:
		return momentum(in)
	case IDVolumeConfirmation:
		return volumeConfirmation(in)
	case IDVWAP:
		return vwapPosition(in)

	case IDOpposingLevel:
		return opposingLevel(in)
	case IDTimeframeConflict:
		return timeframeConflict(in)
	case IDVolatilityGuard:
		return volatilityGuard(in)
	case IDRegimeGuard:
		return regimeGuard(in)
	case IDCriticalNews:
		return criticalNews(in)
	case IDCostFloor:
		return costFloor(in)
	case IDExtensionGuard:
		return extensionGuard(in)
	case IDTrendGate:
		return trendGate(in)
	case IDMarketGate:
		return marketGate(in)
	case IDRelStrengthGate:
		return relativeStrengthGate(in)
	default:
		return noOpinion
	}
}

// --- directional strategies -------------------------------------------------

// compositeScore reuses the existing deterministic score, so the behaviour the
// project had before strategies existed stays available as one opinion.
func compositeScore(in Input) opinion {
	net := in.Analysis.Scores.Net
	if math.Abs(net) < 0.15 {
		return noOpinion
	}
	return opinion{
		direction: directionOf(net),
		strength:  clamp(math.Abs(net), 0, 1),
		detail:    fmt.Sprintf("net %.2f", net),
	}
}

// higherTimeframeOrder is the ladder a strategy climbs to find context.
var higherTimeframeOrder = []domain.Timeframe{
	domain.TF1m, domain.TF5m, domain.TF15m, domain.TF1h, domain.TF4h, domain.TF1d,
}

// higherTimeframe votes with the trend of the next slower timeframe that was
// analysed. Trading against the larger picture is the classic way to be right
// about the direction and still lose, and the stored runs show the trades taken
// while the timeframes disagreed were the worst ones.
func higherTimeframe(in Input) opinion {
	index := -1
	for i, tf := range higherTimeframeOrder {
		if tf == in.Timeframe {
			index = i
			break
		}
	}
	if index < 0 {
		return noOpinion
	}
	for _, tf := range higherTimeframeOrder[index+1:] {
		higher, ok := in.Snapshot.Timeframes[tf]
		if !ok {
			continue
		}
		net := higher.Scores.Net
		if math.Abs(net) < 0.15 {
			return noOpinion
		}
		return opinion{
			direction: directionOf(net),
			strength:  clamp(math.Abs(net), 0, 1),
			detail:    fmt.Sprintf("%s net %.2f", tf, net),
		}
	}
	return noOpinion
}

// emaTrend follows the classic stack: price above both moving averages with the
// fast one above the slow is a trend, the mirror image is the opposite.
func emaTrend(in Input) opinion {
	fast, okFast := ptr(in.Analysis.Indicators.PriceVsEMA50Pct)
	slow, okSlow := ptr(in.Analysis.Indicators.PriceVsEMA200Pct)
	if !okFast && !okSlow {
		return noOpinion
	}

	score, weight := 0.0, 0.0
	if okSlow {
		score += clamp(slow/4, -1, 1) * 1.2
		weight += 1.2
	}
	if okFast {
		score += clamp(fast/2.5, -1, 1)
		weight++
	}
	// The relationship between the two averages is the trend itself.
	ema := in.Analysis.Indicators.EMA
	e50, ok50 := ema["50"]
	e200, ok200 := ema["200"]
	if ok50 && ok200 && e200 > 0 {
		gap := (e50 - e200) / e200 * 100
		score += clamp(gap/2, -1, 1) * 1.3
		weight += 1.3
	}
	if weight == 0 {
		return noOpinion
	}
	value := clamp(score/weight, -1, 1)
	if math.Abs(value) < 0.15 {
		return noOpinion
	}
	return opinion{
		direction: directionOf(value),
		strength:  math.Abs(value),
		detail:    fmt.Sprintf("EMA50 %+.2f%% · EMA200 %+.2f%%", fast, slow),
	}
}

// adxDirectional trades only when ADX says a trend actually exists.
func adxDirectional(in Input) opinion {
	adx, ok := ptr(in.Analysis.Indicators.ADX)
	plus, okPlus := ptr(in.Analysis.Indicators.PlusDI)
	minus, okMinus := ptr(in.Analysis.Indicators.MinusDI)
	if !ok || !okPlus || !okMinus || adx < 20 {
		return noOpinion
	}
	spread := math.Abs(plus-minus) / math.Max(1, plus+minus)
	strength := clamp(math.Min(1, adx/40)*(0.4+0.6*spread), 0, 1)
	if strength < 0.15 {
		return noOpinion
	}
	direction := domain.PatternBullish
	if minus > plus {
		direction = domain.PatternBearish
	}
	return opinion{
		direction: direction,
		strength:  strength,
		detail:    fmt.Sprintf("ADX %.1f · +DI %.1f / -DI %.1f", adx, plus, minus),
	}
}

// macdStrategy combines the histogram with the side of the signal line.
func macdStrategy(in Input) opinion {
	hist, okHist := ptr(in.Analysis.Indicators.MACDHistogram)
	macd, okMACD := ptr(in.Analysis.Indicators.MACD)
	signal, okSignal := ptr(in.Analysis.Indicators.MACDSignal)
	if !okHist && (!okMACD || !okSignal) {
		return noOpinion
	}

	score, weight := 0.0, 0.0
	if okHist && in.Price > 0 {
		// Scale the histogram by price so the strength means the same thing on
		// a four-digit and a four-decimal instrument.
		score += clamp(hist/in.Price*2000, -1, 1)
		weight++
	}
	if okMACD && okSignal {
		if macd > signal {
			score += 0.8
		} else {
			score -= 0.8
		}
		weight += 0.8
	}
	if weight == 0 {
		return noOpinion
	}
	value := clamp(score/weight, -1, 1)
	if math.Abs(value) < 0.2 {
		return noOpinion
	}
	return opinion{
		direction: directionOf(value),
		strength:  math.Abs(value),
		detail:    fmt.Sprintf("hist %+.4f", hist),
	}
}

// marketStructure reads HH/HL against LH/LL and any recent break of structure.
func marketStructure(in Input) opinion {
	structure := in.Analysis.Structure
	base := 0.0
	switch structure.State {
	case domain.StructureBullish:
		base = 0.7
	case domain.StructureBearish:
		base = -0.7
	case domain.StructureRange, domain.StructureTransition, domain.StructureUncertain:
		base = 0
	}

	event := ""
	for _, e := range structure.Events {
		if e.AgeCandles > 10 {
			continue
		}
		decay := 1 / (1 + float64(e.AgeCandles)/5)
		delta := 0.4 * decay
		if e.Direction == domain.DirectionShort {
			delta = -delta
		}
		base += delta
		event = fmt.Sprintf(" · %s %d bars ago", e.Type, e.AgeCandles)
		break
	}
	value := clamp(base, -1, 1)
	if math.Abs(value) < 0.2 {
		return noOpinion
	}
	return opinion{
		direction: directionOf(value),
		strength:  math.Abs(value),
		detail:    fmt.Sprintf("%s%s", structure.State, event),
	}
}

// chartPatterns aggregates the detected chart formations by strength and age.
func chartPatterns(in Input) opinion {
	return patternOpinion(in.Analysis.ChartPatterns, 30, 10)
}

// candlePatterns only trusts formations that are still on screen.
func candlePatterns(in Input) opinion {
	return patternOpinion(in.Analysis.Patterns, 3, 2)
}

func patternOpinion(patterns []domain.Pattern, maxAge int, halfLife float64) opinion {
	score, weight := 0.0, 0.0
	best := ""
	for _, p := range patterns {
		if p.AgeCandles > maxAge || p.Direction == domain.PatternNeutral {
			continue
		}
		decay := 1 / (1 + float64(p.AgeCandles)/halfLife)
		value := p.Strength * decay
		if p.Direction == domain.PatternBearish {
			value = -value
		}
		score += value
		weight++
		if best == "" {
			best = p.Name
		}
	}
	if weight == 0 {
		return noOpinion
	}
	value := clamp(score/weight, -1, 1)
	if math.Abs(value) < 0.15 {
		return noOpinion
	}
	return opinion{
		direction: directionOf(value),
		strength:  math.Abs(value),
		detail:    fmt.Sprintf("%s ×%d", best, int(weight)),
	}
}

// divergences votes for the direction the divergence implies.
func divergences(in Input) opinion {
	score, weight := 0.0, 0.0
	name := ""
	for _, d := range in.Analysis.Divergences {
		if d.AgeCandles > 15 || d.Direction == domain.PatternNeutral {
			continue
		}
		decay := 1 / (1 + float64(d.AgeCandles)/8)
		value := d.Strength * decay
		if d.Direction == domain.PatternBearish {
			value = -value
		}
		score += value
		weight++
		if name == "" {
			name = fmt.Sprintf("%s %s", d.Indicator, d.Type)
		}
	}
	if weight == 0 {
		return noOpinion
	}
	value := clamp(score/weight, -1, 1)
	if math.Abs(value) < 0.15 {
		return noOpinion
	}
	return opinion{direction: directionOf(value), strength: math.Abs(value), detail: name}
}

// rsiReversion is the counter-trend voice: stretched momentum against the
// Bollinger band argues for a move back towards the mean.
func rsiReversion(in Input) opinion {
	rsi, ok := ptr(in.Analysis.Indicators.RSI)
	if !ok {
		return noOpinion
	}
	percentB, okB := ptr(in.Analysis.Indicators.BBPercentB)

	var score float64
	switch {
	case rsi <= 30:
		score = (30 - rsi) / 30
	case rsi >= 70:
		score = -(rsi - 70) / 30
	default:
		return noOpinion
	}
	if okB {
		// Outside the band strengthens the case, inside it weakens it.
		switch {
		case percentB < 0 && score > 0:
			score = math.Min(1, score*1.4)
		case percentB > 1 && score < 0:
			score = math.Max(-1, score*1.4)
		default:
			score *= 0.7
		}
	}
	value := clamp(score, -1, 1)
	if math.Abs(value) < 0.15 {
		return noOpinion
	}
	return opinion{
		direction: directionOf(value),
		strength:  math.Abs(value),
		detail:    fmt.Sprintf("RSI %.1f", rsi),
	}
}

// breakout follows a confirmed break out of compression, preferring one that
// volume agrees with.
func breakout(in Input) opinion {
	regime := in.Analysis.Regime
	if regime.Primary != domain.RegimeBreakout && !hasTag(regime.Tags, domain.TagExpandingRanges) {
		return noOpinion
	}
	high, okHigh := ptr(in.Analysis.Indicators.DistFromHighPct)
	low, okLow := ptr(in.Analysis.Indicators.DistFromLowPct)
	if !okHigh || !okLow {
		return noOpinion
	}

	// Near the top of the recent range is an upward break, near the bottom a
	// downward one; the middle of the range is not a breakout at all.
	var value float64
	switch {
	case math.Abs(high) <= 0.5:
		value = 0.8
	case math.Abs(low) <= 0.5:
		value = -0.8
	default:
		return noOpinion
	}
	if rel, ok := ptr(in.Analysis.Indicators.RelativeVolume); ok {
		switch {
		case rel >= 1.5:
			value *= 1.2
		case rel < 0.8:
			value *= 0.6
		}
	}
	value = clamp(value, -1, 1)
	return opinion{
		direction: directionOf(value),
		strength:  math.Abs(value),
		detail:    fmt.Sprintf("%s · high %+.2f%% / low %+.2f%%", regime.Primary, high, low),
	}
}

// momentum is the fast oscillator group.
func momentum(in Input) opinion {
	score, weight := 0.0, 0.0
	ind := in.Analysis.Indicators
	if v, ok := ptr(ind.ROC); ok {
		score += clamp(v/3, -1, 1)
		weight++
	}
	if v, ok := ptr(ind.CCI); ok {
		score += clamp(v/150, -1, 1) * 0.8
		weight += 0.8
	}
	if v, ok := ptr(ind.StochK); ok {
		score += clamp((v-50)/40, -1, 1) * 0.6
		weight += 0.6
	}
	if v, ok := ptr(ind.RSISlope); ok {
		score += clamp(v/3, -1, 1) * 0.5
		weight += 0.5
	}
	if weight == 0 {
		return noOpinion
	}
	value := clamp(score/weight, -1, 1)
	if math.Abs(value) < 0.2 {
		return noOpinion
	}
	return opinion{direction: directionOf(value), strength: math.Abs(value), detail: "ROC/CCI/Stoch"}
}

// volumeConfirmation lets money flow agree or disagree with price.
func volumeConfirmation(in Input) opinion {
	ind := in.Analysis.Indicators
	score, weight := 0.0, 0.0
	if v, ok := ptr(ind.OBVSlope); ok {
		score += clamp(v, -1, 1)
		weight++
	}
	if v, ok := ptr(ind.CMF); ok {
		score += clamp(v/0.2, -1, 1) * 0.8
		weight += 0.8
	}
	if v, ok := ptr(ind.MFI); ok {
		score += clamp((v-50)/30, -1, 1) * 0.6
		weight += 0.6
	}
	if weight == 0 {
		return noOpinion
	}
	value := clamp(score/weight, -1, 1)
	if math.Abs(value) < 0.2 {
		return noOpinion
	}
	return opinion{direction: directionOf(value), strength: math.Abs(value), detail: "OBV/CMF/MFI"}
}

// vwapPosition treats the session average as the fair value line.
func vwapPosition(in Input) opinion {
	v, ok := ptr(in.Analysis.Indicators.PriceVsVWAPPct)
	if !ok {
		return noOpinion
	}
	value := clamp(v/1.5, -1, 1)
	if math.Abs(value) < 0.2 {
		return noOpinion
	}
	return opinion{
		direction: directionOf(value),
		strength:  math.Abs(value),
		detail:    fmt.Sprintf("VWAP %+.2f%%", v),
	}
}

// --- filters ----------------------------------------------------------------

// opposingLevel argues against buying straight into resistance and against
// selling straight into support.
func opposingLevel(in Input) opinion {
	if in.Price <= 0 {
		return noOpinion
	}
	levels := in.Snapshot.KeyLevels
	if len(levels) == 0 {
		levels = in.Analysis.Levels
	}

	nearestUp, nearestDown := math.Inf(1), math.Inf(1)
	for _, level := range levels {
		if level.Strength < 0.4 {
			continue
		}
		distance := math.Abs(level.DistancePct)
		if level.Type == domain.LevelResistance && level.Price > in.Price {
			nearestUp = math.Min(nearestUp, distance)
		}
		if level.Type == domain.LevelSupport && level.Price < in.Price {
			nearestDown = math.Min(nearestDown, distance)
		}
	}

	// A fixed percentage is meaningless across timeframes: on 5m bars a level one
	// percent away is not "in the way" at all. The reach follows the average range
	// of a bar instead, so the filter stays selective everywhere.
	reach := 1.0
	if atrPct, ok := ptr(in.Analysis.Indicators.ATRPct); ok && atrPct > 0 {
		reach = clamp(atrPct*0.75, 0.1, 1.5)
	}
	blocksLong, blocksShort := nearestUp <= reach, nearestDown <= reach
	switch {
	case blocksLong && blocksShort:
		return opinion{
			blocks: domain.BlocksBoth, strength: 1,
			detail: fmt.Sprintf("R %.2f%% / S %.2f%%", nearestUp, nearestDown),
		}
	case blocksLong:
		return opinion{
			blocks: domain.BlocksLong, strength: clamp(1-nearestUp/reach, 0.3, 1),
			detail: fmt.Sprintf("resistance %.2f%%", nearestUp),
		}
	case blocksShort:
		return opinion{
			blocks: domain.BlocksShort, strength: clamp(1-nearestDown/reach, 0.3, 1),
			detail: fmt.Sprintf("support %.2f%%", nearestDown),
		}
	default:
		return noOpinion
	}
}

// timeframeConflict argues against trading while the timeframes disagree.
func timeframeConflict(in Input) opinion {
	alignment := in.Snapshot.TrendAlignment
	// One timeframe cannot disagree with itself.
	if len(alignment.Bullish)+len(alignment.Bearish)+len(alignment.Neutral) < 2 {
		return noOpinion
	}
	const conflict = 0.15
	score := math.Abs(alignment.AlignmentScore)
	if score >= conflict {
		return noOpinion
	}
	return opinion{
		blocks:   domain.BlocksBoth,
		strength: clamp(1-score/conflict, 0.3, 1),
		detail:   fmt.Sprintf("alignment %.2f", alignment.AlignmentScore),
	}
}

// volatilityGuard argues against both extremes: a market that moves too much to
// place a sane stop, and one that barely moves at all.
//
// The comparison is against this instrument's own recent history rather than an
// absolute percentage, because a 0.2% average range is dead on a daily chart and
// perfectly normal on a five-minute one.
func volatilityGuard(in Input) opinion {
	if percentile, ok := ptr(in.Analysis.Indicators.ATRPercentile); ok {
		switch {
		case percentile >= 95:
			return opinion{
				blocks: domain.BlocksBoth, strength: clamp((percentile-95)/5, 0.4, 1),
				detail: fmt.Sprintf("ATR p%.0f", percentile),
			}
		case percentile <= 3:
			return opinion{
				blocks: domain.BlocksBoth, strength: 0.5,
				detail: fmt.Sprintf("ATR p%.0f", percentile),
			}
		default:
			return noOpinion
		}
	}

	// Without a percentile only an obviously broken market is rejected.
	atrPct, ok := ptr(in.Analysis.Indicators.ATRPct)
	if !ok || (atrPct < 8 && atrPct > 0.01) {
		return noOpinion
	}
	return opinion{
		blocks: domain.BlocksBoth, strength: 0.6,
		detail: fmt.Sprintf("ATR %.2f%%", atrPct),
	}
}

// regimeGuard argues against trading a market the classifier cannot read.
func regimeGuard(in Input) opinion {
	switch in.Analysis.Regime.Primary {
	case domain.RegimeUncertain:
		return opinion{blocks: domain.BlocksBoth, strength: 0.8, detail: "uncertain"}
	case domain.RegimeCompression:
		return opinion{blocks: domain.BlocksBoth, strength: 0.5, detail: "compression"}
	default:
		return noOpinion
	}
}

// DefaultCostFloorMultiple is how many round trips a trade has to be able to
// earn before it is worth taking.
//
// Raising it was tried as a way to stop the hourly timeframe losing money and
// did not work: swept from 3 to 16 over the stored history it removes up to
// half the hourly trades while leaving the loss per run where it was (-0.59% to
// -0.53%), and the slow timeframes never notice it at all. The trades it
// removes are no worse than the ones it keeps, so the filter is a floor against
// obviously unpayable trades rather than a selectivity tool. The knob stays
// configurable because a user on a worse fee tier needs a higher floor.
const DefaultCostFloorMultiple = 3.0

// costFloor rejects a trade whose realistic target cannot pay for its own round
// trip. In the stored runs the fees of a five-minute strategy came to roughly
// two thirds of its average loss: at that point the entry quality stops
// mattering, because the trade cannot win even when it is right.
//
// The yardstick is the distance the technical plan aims for, two average bar
// ranges, against three times the cost of entering and leaving.
func costFloor(in Input) opinion {
	cost := in.RoundTripCostPct
	if cost <= 0 {
		return noOpinion
	}
	atrPct, ok := ptr(in.Analysis.Indicators.ATRPct)
	if !ok || atrPct <= 0 {
		return noOpinion
	}
	multiple := in.CostFloorMultiple
	if multiple <= 0 {
		multiple = DefaultCostFloorMultiple
	}
	target := 2 * atrPct
	required := multiple * cost
	if target >= required {
		return noOpinion
	}
	return opinion{
		blocks:   domain.BlocksBoth,
		strength: clamp(1-target/required, 0.3, 1),
		detail:   fmt.Sprintf("target %.2f%% vs cost %.2f%%", target, cost),
	}
}

// extensionGuard argues against joining a move that has already travelled too
// far from its own short average.
//
// Across the stored runs the trades that reached their target barely went
// against the entry at all, while the losers moved against it immediately: the
// entries were late rather than wrong. Distance is measured in average bar
// ranges, so the same rule holds on every timeframe.
func extensionGuard(in Input) opinion {
	atr, okATR := ptr(in.Analysis.Indicators.ATR)
	if !okATR || atr <= 0 || in.Price <= 0 {
		return noOpinion
	}
	anchor, ok := in.Analysis.Indicators.EMA["20"]
	if !ok || anchor <= 0 {
		if anchor, ok = in.Analysis.Indicators.SMA["20"]; !ok || anchor <= 0 {
			return noOpinion
		}
	}

	const reach = 1.2 // average bar ranges
	extension := (in.Price - anchor) / atr
	if math.Abs(extension) < reach {
		return noOpinion
	}
	blocks := domain.BlocksLong
	if extension < 0 {
		blocks = domain.BlocksShort
	}
	return opinion{
		blocks:   blocks,
		strength: clamp((math.Abs(extension)-reach)/reach, 0.4, 1),
		detail:   fmt.Sprintf("%.1f ATR from EMA20", extension),
	}
}

// trendGate argues against taking a position against the slowest timeframe on
// screen, measured as price against its own long moving average.
//
// Unlike timeframeConflict, which asks whether the timeframes agree with each
// other, this asks whether the trade agrees with the largest picture available.
// A counter-trend entry is not forbidden outright - the weight decides how much
// the argument is worth - but it has to be paid for.
func trendGate(in Input) opinion {
	analysis, timeframe, ok := slowestAnalysis(in)
	if !ok {
		return noOpinion
	}
	value, ok := ptr(analysis.Indicators.PriceVsEMA200Pct)
	if !ok {
		if value, ok = ptr(analysis.Indicators.PriceVsEMA50Pct); !ok {
			return noOpinion
		}
	}
	// Sitting on the average is not a trend in either direction. The band is
	// stated in average bar ranges so it means the same on every timeframe.
	band := 1.0
	if atrPct, okATR := ptr(analysis.Indicators.ATRPct); okATR && atrPct > 0 {
		band = clamp(atrPct, 0.2, 3)
	}
	// Adding the order of the averages as a second condition - refusing both
	// sides while price and the EMA50/EMA200 cross disagree - was tried and
	// rejected: it cut the pooled profit factor from 1.25 to 1.20 on daily bars
	// and from 1.16 to 1.10 on four-hour bars, because the trades it removed
	// were the early part of every new trend.
	if math.Abs(value) < band {
		return noOpinion
	}
	blocks := domain.BlocksShort
	if value < 0 {
		blocks = domain.BlocksLong
	}
	return opinion{
		blocks:   blocks,
		strength: clamp(math.Abs(value)/(4*band), 0.3, 1),
		detail:   fmt.Sprintf("%s price %+.2f%% vs EMA200", timeframe, value),
	}
}

// marketGate argues against trading with the market as a whole, not just with
// this instrument. Crypto assets move together: an asset with a clean chart of
// its own is still swimming against the tide while the benchmark is below its
// own long daily average, and that is exactly the picture a per-symbol analysis
// cannot see.
//
// An unknown benchmark never blocks anything. Missing context has to let a
// trade through, because a filter that fires on absent data would silently stop
// the system whenever the benchmark series is short.
// marketGateBandPct is the dead zone around the benchmark average: sitting on it
// says nothing about direction. It mirrors features.MarketContextBandPct, which
// this package cannot import without a cycle.
const marketGateBandPct = 1.0

func marketGate(in Input) opinion {
	context := in.Snapshot.MarketContext
	if !context.Known() {
		return noOpinion
	}
	distance := 0.0
	if context.PriceVsEMA200Pct != nil {
		distance = *context.PriceVsEMA200Pct
	}
	detail := fmt.Sprintf("%s %+.1f%% vs EMA200", context.Benchmark, distance)

	// The average itself is the second condition. Price above a falling average
	// is a rally inside a downtrend, and treating that as an uptrend is what let
	// the policy trade through the bear markets it was meant to sit out.
	rising, falling := false, false
	if slope := context.EMA200SlopePct; slope != nil {
		rising, falling = *slope > 0, *slope < 0
		detail = fmt.Sprintf("%s, average %+.1f%%/mo", detail, *slope)
	}

	// A long is refused when the benchmark is below its average, when the buffer
	// it asks for is not there, or when the average itself is falling. The three
	// say different things: the first is a downtrend, the second a market that
	// has only just crossed back over, the third a rally inside a decline.
	switch {
	case distance <= -marketGateBandPct,
		in.MarketGateLongBufferPct > 0 && distance < in.MarketGateLongBufferPct,
		!in.MarketGateAllowFallingAverage && falling:
		return opinion{
			blocks:   domain.BlocksLong,
			strength: clamp(math.Abs(distance)/15, 0.5, 1),
			detail:   detail,
		}
	case distance >= marketGateBandPct && (in.MarketGateAllowFallingAverage || rising):
		return opinion{
			blocks:   domain.BlocksShort,
			strength: clamp(math.Abs(distance)/15, 0.5, 1),
			detail:   detail,
		}
	default:
		return noOpinion
	}
}

// DefaultMinRelativeStrengthPct is the percentile an asset has to reach before
// it is worth trading rather than one of its peers.
const DefaultMinRelativeStrengthPct = 50.0

// relativeStrengthGate argues against spending a position on an asset that is
// lagging the ones it competes with.
//
// Every filter before this one asks whether the trade is good in isolation. Only
// a handful of positions can be open at once, though, so the question that
// decides the account is which of the candidates deserves the slot. An asset in
// the bottom half of its own universe by risk-adjusted momentum is, by
// construction, not one of them.
//
// An unranked universe never blocks anything: a single tracked asset has no
// peers to lose to.
func relativeStrengthGate(in Input) opinion {
	context := in.Snapshot.UniverseContext
	if !context.Known() {
		return noOpinion
	}
	threshold := in.MinRelativeStrengthPct
	if threshold <= 0 {
		threshold = DefaultMinRelativeStrengthPct
	}
	// The mirror holds for the short side: shorting the strongest asset on screen
	// is the same mistake in the other direction. A threshold above the middle
	// makes the two demands overlap, and an asset caught by both is one neither
	// side wants - saying so explicitly keeps the filter from appearing to permit
	// the side it simply did not check.
	detail := fmt.Sprintf("rank %.0f of %d assets", context.RankPct, context.Members)
	blocksLong := context.RankPct < threshold
	blocksShort := context.RankPct > 100-threshold
	switch {
	case blocksLong && blocksShort:
		return opinion{blocks: domain.BlocksBoth, strength: 0.5, detail: detail}
	case blocksLong:
		return opinion{
			blocks:   domain.BlocksLong,
			strength: clamp((threshold-context.RankPct)/threshold, 0.3, 1),
			detail:   detail,
		}
	case blocksShort:
		return opinion{
			blocks:   domain.BlocksShort,
			strength: clamp((context.RankPct-(100-threshold))/threshold, 0.3, 1),
			detail:   detail,
		}
	default:
		return noOpinion
	}
}

// slowestAnalysis returns the analysis of the slowest timeframe available, which
// is the largest picture the current cycle actually looked at. It never returns
// a timeframe faster than the one being traded.
func slowestAnalysis(in Input) (domain.TimeframeAnalysis, domain.Timeframe, bool) {
	index := -1
	for i, tf := range higherTimeframeOrder {
		if tf == in.Timeframe {
			index = i
			break
		}
	}
	if index >= 0 {
		for i := len(higherTimeframeOrder) - 1; i > index; i-- {
			tf := higherTimeframeOrder[i]
			if analysis, ok := in.Snapshot.Timeframes[tf]; ok {
				return analysis, tf, true
			}
		}
	}
	if in.Analysis.Timeframe == "" {
		return domain.TimeframeAnalysis{}, "", false
	}
	return in.Analysis, in.Timeframe, true
}

// criticalNews argues against opening around a fresh critical event. It never
// implies a direction: the reaction to news is not deterministic.
func criticalNews(in Input) opinion {
	maxAge := in.CriticalNewsMaxAge
	if maxAge <= 0 {
		maxAge = 2 * time.Hour
	}
	maxAgeMinutes := int(maxAge.Minutes())

	for _, group := range [][]domain.NewsSnapshotItem{
		in.Snapshot.NewsContext.AssetSpecific,
		in.Snapshot.NewsContext.Global,
	} {
		for _, item := range group {
			if item.Critical && item.AgeMinutes <= maxAgeMinutes {
				return opinion{
					blocks:   domain.BlocksBoth,
					strength: 1,
					detail:   fmt.Sprintf("critical %dm ago", item.AgeMinutes),
				}
			}
		}
	}
	return noOpinion
}

// --- helpers ----------------------------------------------------------------

func ptr(v *float64) (float64, bool) {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) {
		return 0, false
	}
	return *v, true
}

func directionOf(value float64) domain.PatternDirection {
	if value >= 0 {
		return domain.PatternBullish
	}
	return domain.PatternBearish
}

func hasTag(tags []domain.RegimeTag, want domain.RegimeTag) bool {
	for _, tag := range tags {
		if tag == want {
			return true
		}
	}
	return false
}

// clamp bounds a value into [min, max].
func clamp(v, min, max float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	return math.Max(min, math.Min(max, v))
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
func round3(v float64) float64 { return math.Round(v*1000) / 1000 }
