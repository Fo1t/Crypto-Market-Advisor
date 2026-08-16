package llm

import (
	"fmt"
	"math"
	"strings"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

// ValidationError collects every problem found in a model answer so that a
// single repair attempt can address all of them at once.
type ValidationError struct {
	Problems []string
}

func (e *ValidationError) Error() string {
	return "invalid model response: " + strings.Join(e.Problems, "; ")
}

// Add records a problem.
func (e *ValidationError) Add(format string, args ...any) {
	e.Problems = append(e.Problems, fmt.Sprintf(format, args...))
}

// HasProblems reports whether anything was rejected.
func (e *ValidationError) HasProblems() bool { return len(e.Problems) > 0 }

// ValidationContext carries the facts a response is checked against.
type ValidationContext struct {
	Symbol           string
	ReferencePrice   float64
	MinLeverage      int
	MaxLeverage      int
	MaxAllocationPct float64
	OpenPositionIDs  []uuid.UUID
	NewsClusterIDs   []uuid.UUID
	// CloseTolerancePct allows TP/SL fractions to sum to slightly off 100
	// before the answer is rejected; the result is then normalised.
	CloseTolerancePct float64
}

// Validated is a model answer that passed every semantic check.
type Validated struct {
	Action         domain.RecommendationAction
	Confidence     int
	RiskLevel      domain.RiskLevel
	Summary        string
	AllocationPct  decimal.Decimal
	Leverage       int
	LeverageReason string
	Entry          *domain.EntryPlan
	TakeProfit     []domain.PriceTarget
	StopLoss       []domain.PriceTarget
	Management     *domain.ManagementPlan
	SignalsFor     []string
	SignalsAgainst []string
	Invalidation   []string
	Translations   map[string]domain.RecommendationNarrative
	NewsAssessment *domain.NewsAssessment
}

// Validate performs full semantic validation of a parsed model answer.
func Validate(resp *Response, ctx ValidationContext) (*Validated, error) {
	verr := &ValidationError{}
	if resp == nil {
		verr.Add("empty response")
		return nil, verr
	}
	if ctx.CloseTolerancePct <= 0 {
		ctx.CloseTolerancePct = 5
	}

	translations := normalizeTranslations(resp.Translations)
	canonical := domain.RecommendationNarrative{
		Summary:        strings.TrimSpace(resp.Summary),
		SignalsFor:     cleanStrings(resp.SignalsFor),
		SignalsAgainst: cleanStrings(resp.SignalsAgainst),
		Invalidation:   cleanStrings(resp.Invalidation),
	}
	if translated, ok := translations["en"]; ok {
		canonical = translated
	} else if translated, ok := translations["ru"]; ok {
		canonical = translated
	}
	out := &Validated{
		Summary:        canonical.Summary,
		SignalsFor:     canonical.SignalsFor,
		SignalsAgainst: canonical.SignalsAgainst,
		Invalidation:   canonical.Invalidation,
		Translations:   translations,
	}

	action, err := domain.ParseAction(strings.ToUpper(strings.TrimSpace(resp.Action)))
	if err != nil {
		verr.Add("action must be one of OPEN_LONG, OPEN_SHORT, NO_ACTION, MANAGE_POSITION (got %q)", resp.Action)
	}
	out.Action = action

	out.Confidence = validateConfidence(resp.Confidence, verr)
	out.RiskLevel = validateRiskLevel(resp.RiskLevel, verr)
	out.AllocationPct = validateAllocation(resp.RecommendedAllocationPc, action, verr)
	out.Leverage, out.LeverageReason = validateLeverage(resp.Leverage, action, ctx, verr)
	if canonical.LeverageReason != "" {
		out.LeverageReason = canonical.LeverageReason
	}

	if action.IsEntry() {
		validateEntryAction(resp, ctx, out, verr)
	}
	if action == domain.RecommendationManage {
		out.Management = validateManagement(resp.Management, ctx, verr)
	}
	if action == domain.RecommendationNoAction {
		// A "do nothing" answer must not smuggle in a trade plan.
		out.TakeProfit, out.StopLoss, out.Entry = nil, nil, nil
	}
	applyNarrativeReasons(out, canonical)
	out.NewsAssessment = validateNewsAssessment(resp.NewsAssessment, ctx, verr)

	if out.Summary == "" {
		verr.Add("summary must not be empty")
	}
	if verr.HasProblems() {
		return nil, verr
	}
	return out, nil
}

func validateNewsAssessment(raw *NewsAssessmentJSON, ctx ValidationContext, verr *ValidationError) *domain.NewsAssessment {
	if raw == nil {
		return nil
	}
	assessment := &domain.NewsAssessment{
		OverallSentiment: strings.ToUpper(strings.TrimSpace(raw.OverallSentiment)),
		Impact:           strings.ToUpper(strings.TrimSpace(raw.Impact)),
		TimeHorizon:      strings.ToUpper(strings.TrimSpace(raw.TimeHorizon)),
		Reasons:          map[string]string{},
	}
	if !oneOf(assessment.OverallSentiment, "STRONGLY_BULLISH", "BULLISH", "NEUTRAL", "BEARISH", "STRONGLY_BEARISH", "MIXED") {
		verr.Add("news_assessment.overall_sentiment is invalid")
	}
	if !oneOf(assessment.Impact, "NONE", "LOW", "MEDIUM", "HIGH", "EXTREME") {
		verr.Add("news_assessment.impact is invalid")
	}
	if !oneOf(assessment.TimeHorizon, "IMMEDIATE", "SHORT_TERM", "MEDIUM_TERM", "UNCERTAIN") {
		verr.Add("news_assessment.time_horizon is invalid")
	}
	if raw.Confidence == nil || !finite(*raw.Confidence) || *raw.Confidence < 0 || *raw.Confidence > 100 {
		verr.Add("news_assessment.confidence must be between 0 and 100")
	} else {
		assessment.Confidence = int(math.Round(*raw.Confidence))
	}
	allowed := make(map[uuid.UUID]bool, len(ctx.NewsClusterIDs))
	for _, id := range ctx.NewsClusterIDs {
		allowed[id] = true
	}
	seen := map[uuid.UUID]bool{}
	for _, rawID := range raw.ImportantClusters {
		id, err := uuid.Parse(strings.TrimSpace(rawID))
		if err != nil {
			verr.Add("news_assessment.important_clusters contains invalid uuid %q", rawID)
			continue
		}
		if !allowed[id] {
			verr.Add("news_assessment.important_clusters contains cluster absent from input: %s", id)
			continue
		}
		if !seen[id] {
			assessment.ImportantClusters = append(assessment.ImportantClusters, id)
			seen[id] = true
		}
	}
	for _, language := range domain.SupportedLanguages() {
		reason := strings.TrimSpace(raw.Reasons[language])
		if reason == "" {
			verr.Add("news_assessment.reasons.%s is required", language)
			continue
		}
		assessment.Reasons[language] = reason
	}
	return assessment
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func normalizeTranslations(in map[string]NarrativeJSON) map[string]domain.RecommendationNarrative {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]domain.RecommendationNarrative, len(in))
	for language, narrative := range in {
		out[language] = domain.RecommendationNarrative{
			Summary:           strings.TrimSpace(narrative.Summary),
			LeverageReason:    strings.TrimSpace(narrative.LeverageReason),
			TakeProfitReasons: cleanStrings(narrative.TakeProfitReasons),
			StopLossReasons:   cleanStrings(narrative.StopLossReasons),
			ManagementReasons: cleanStrings(narrative.ManagementReasons),
			SignalsFor:        cleanStrings(narrative.SignalsFor),
			SignalsAgainst:    cleanStrings(narrative.SignalsAgainst),
			Invalidation:      cleanStrings(narrative.Invalidation),
		}
	}
	return out
}

func applyNarrativeReasons(out *Validated, narrative domain.RecommendationNarrative) {
	for index := range out.TakeProfit {
		if index < len(narrative.TakeProfitReasons) {
			out.TakeProfit[index].Reason = narrative.TakeProfitReasons[index]
		}
	}
	for index := range out.StopLoss {
		if index < len(narrative.StopLossReasons) {
			out.StopLoss[index].Reason = narrative.StopLossReasons[index]
		}
	}
	if out.Management != nil {
		for index := range out.Management.Actions {
			if index < len(narrative.ManagementReasons) {
				out.Management.Actions[index].Reason = narrative.ManagementReasons[index]
			}
		}
	}
}

func validateConfidence(v *float64, verr *ValidationError) int {
	if v == nil {
		verr.Add("confidence is required")
		return 0
	}
	if !finite(*v) {
		verr.Add("confidence must be a finite number")
		return 0
	}
	if *v < 0 || *v > 100 {
		verr.Add("confidence must be between 0 and 100 (got %v)", *v)
		return 0
	}
	return int(math.Round(*v))
}

func validateRiskLevel(raw string, verr *ValidationError) domain.RiskLevel {
	level := domain.RiskLevel(strings.ToLower(strings.TrimSpace(raw)))
	if !level.Valid() {
		verr.Add("risk_level must be one of %s (got %q)", domain.AllowedRiskLevels(), raw)
		return domain.RiskUnknown
	}
	return level
}

func validateAllocation(v *float64, action domain.RecommendationAction, verr *ValidationError) decimal.Decimal {
	if v == nil {
		if action.IsEntry() {
			verr.Add("recommended_allocation_pct is required for entry actions")
		}
		return decimal.Zero
	}
	if !finite(*v) {
		verr.Add("recommended_allocation_pct must be a finite number")
		return decimal.Zero
	}
	if *v < 0 || *v > 100 {
		verr.Add("recommended_allocation_pct must be between 0 and 100 (got %v)", *v)
		return decimal.Zero
	}
	if action.IsEntry() && *v <= 0 {
		verr.Add("an entry action needs a positive allocation")
		return decimal.Zero
	}
	return decimal.NewFromFloat(*v).Round(2)
}

func validateLeverage(l *LeverageJSON, action domain.RecommendationAction, ctx ValidationContext, verr *ValidationError) (int, string) {
	if l == nil || l.Recommended == nil {
		if action.IsEntry() {
			verr.Add("leverage.recommended is required for entry actions")
		}
		return 0, ""
	}
	v := *l.Recommended
	if !finite(v) {
		verr.Add("leverage.recommended must be a finite number")
		return 0, ""
	}
	if v < float64(ctx.MinLeverage) || v > float64(ctx.MaxLeverage) {
		verr.Add("leverage.recommended must be between %d and %d (got %v)", ctx.MinLeverage, ctx.MaxLeverage, v)
		return 0, ""
	}
	return int(math.Round(v)), strings.TrimSpace(l.Reason)
}

func validateEntryAction(resp *Response, ctx ValidationContext, out *Validated, verr *ValidationError) {
	direction, _ := out.Action.Direction()
	reference := ctx.ReferencePrice

	out.Entry = validateEntry(resp.Entry, ctx, verr)
	if out.Entry != nil {
		// The entry zone, when given, is the reference for TP/SL direction.
		switch {
		case direction == domain.DirectionLong && out.Entry.PreferredMax != nil:
			reference = *out.Entry.PreferredMax
		case direction == domain.DirectionShort && out.Entry.PreferredMin != nil:
			reference = *out.Entry.PreferredMin
		}
	}

	out.TakeProfit = validateTargets(resp.TakeProfit, "take_profit", direction, reference, ctx, true, verr)
	out.StopLoss = validateTargets(resp.StopLoss, "stop_loss", direction, reference, ctx, false, verr)

	if len(out.TakeProfit) == 0 {
		verr.Add("at least one take_profit level is required for an entry action")
	}
	if len(out.StopLoss) == 0 {
		verr.Add("at least one stop_loss level is required for an entry action")
	}
}

func validateEntry(e *EntryJSON, ctx ValidationContext, verr *ValidationError) *domain.EntryPlan {
	if e == nil {
		return &domain.EntryPlan{Type: "market", CurrentPrice: ctx.ReferencePrice}
	}
	plan := &domain.EntryPlan{
		Type:         strings.ToLower(strings.TrimSpace(e.Type)),
		CurrentPrice: ctx.ReferencePrice,
	}
	if plan.Type == "" {
		plan.Type = "market"
	}

	minOK := e.PreferredMin != nil && finite(*e.PreferredMin) && *e.PreferredMin > 0
	maxOK := e.PreferredMax != nil && finite(*e.PreferredMax) && *e.PreferredMax > 0

	if e.PreferredMin != nil && !minOK {
		verr.Add("entry.preferred_min must be a positive finite price")
	}
	if e.PreferredMax != nil && !maxOK {
		verr.Add("entry.preferred_max must be a positive finite price")
	}
	if minOK && maxOK && *e.PreferredMin > *e.PreferredMax {
		verr.Add("entry.preferred_min must not exceed entry.preferred_max")
		return plan
	}
	if minOK {
		plan.PreferredMin = e.PreferredMin
	}
	if maxOK {
		plan.PreferredMax = e.PreferredMax
	}

	// An entry zone that sits far away from the market is not actionable.
	if ctx.ReferencePrice > 0 {
		for _, p := range []*float64{plan.PreferredMin, plan.PreferredMax} {
			if p == nil {
				continue
			}
			if math.Abs(*p-ctx.ReferencePrice)/ctx.ReferencePrice > 0.15 {
				verr.Add("entry zone %.4f is more than 15%% away from the current price %.4f", *p, ctx.ReferencePrice)
			}
		}
	}
	return plan
}

// validateTargets checks price direction, fraction bounds and the fraction sum.
func validateTargets(targets []TargetJSON, field string, direction domain.Direction, reference float64, ctx ValidationContext, isTakeProfit bool, verr *ValidationError) []domain.PriceTarget {
	if len(targets) == 0 {
		return nil
	}
	if len(targets) > 6 {
		verr.Add("%s must contain at most 6 levels (got %d)", field, len(targets))
		return nil
	}

	out := make([]domain.PriceTarget, 0, len(targets))
	var sum float64
	valid := true

	for i, t := range targets {
		if t.Price == nil || !finite(*t.Price) || *t.Price <= 0 {
			verr.Add("%s[%d].price must be a positive finite number", field, i)
			valid = false
			continue
		}
		if t.ClosePct == nil || !finite(*t.ClosePct) {
			verr.Add("%s[%d].close_pct is required", field, i)
			valid = false
			continue
		}
		if *t.ClosePct <= 0 || *t.ClosePct > 100 {
			verr.Add("%s[%d].close_pct must be in (0,100] (got %v)", field, i, *t.ClosePct)
			valid = false
			continue
		}
		if reference > 0 && !directionOK(*t.Price, reference, direction, isTakeProfit) {
			verr.Add("%s[%d] price %.4f is on the wrong side of the reference price %.4f for a %s position",
				field, i, *t.Price, reference, direction)
			valid = false
			continue
		}
		sum += *t.ClosePct
		out = append(out, domain.PriceTarget{
			Price:    *t.Price,
			ClosePct: *t.ClosePct,
			Reason:   strings.TrimSpace(t.Reason),
		})
	}
	if !valid {
		return nil
	}

	if math.Abs(sum-100) > ctx.CloseTolerancePct {
		verr.Add("%s close_pct values must sum to 100 (got %.2f)", field, sum)
		return nil
	}
	// Normalise small rounding drift so downstream accounting stays exact.
	if sum > 0 && sum != 100 {
		for i := range out {
			out[i].ClosePct = math.Round(out[i].ClosePct/sum*100*100) / 100
		}
	}
	sortTargets(out, direction, isTakeProfit)
	return out
}

// directionOK enforces TP above / SL below the reference for longs, and the
// mirror image for shorts.
func directionOK(price, reference float64, direction domain.Direction, isTakeProfit bool) bool {
	switch {
	case direction == domain.DirectionLong && isTakeProfit:
		return price > reference
	case direction == domain.DirectionLong && !isTakeProfit:
		return price < reference
	case direction == domain.DirectionShort && isTakeProfit:
		return price < reference
	default:
		return price > reference
	}
}

// sortTargets orders levels the way they will be hit.
func sortTargets(targets []domain.PriceTarget, direction domain.Direction, isTakeProfit bool) {
	ascending := (direction == domain.DirectionLong) == isTakeProfit
	for i := 1; i < len(targets); i++ {
		for j := i; j > 0; j-- {
			swap := targets[j-1].Price > targets[j].Price
			if !ascending {
				swap = targets[j-1].Price < targets[j].Price
			}
			if !swap {
				break
			}
			targets[j-1], targets[j] = targets[j], targets[j-1]
		}
	}
}

func validateManagement(m *ManageJSON, ctx ValidationContext, verr *ValidationError) *domain.ManagementPlan {
	if m == nil {
		verr.Add("management is required when action is MANAGE_POSITION")
		return nil
	}
	id, err := uuid.Parse(strings.TrimSpace(m.PositionID))
	if err != nil {
		verr.Add("management.position_id must be a valid position UUID (got %q)", m.PositionID)
		return nil
	}
	known := false
	for _, open := range ctx.OpenPositionIDs {
		if open == id {
			known = true
			break
		}
	}
	if !known {
		verr.Add("management.position_id %s does not match any open position", id)
		return nil
	}
	if len(m.Actions) == 0 {
		verr.Add("management.actions must not be empty")
		return nil
	}

	plan := &domain.ManagementPlan{PositionID: id}
	for i, a := range m.Actions {
		actionType := domain.ManagementActionType(strings.ToUpper(strings.TrimSpace(a.Type)))
		if !actionType.Valid() {
			verr.Add("management.actions[%d].type is not a known management action (got %q)", i, a.Type)
			continue
		}
		action := domain.ManagementAction{Type: actionType, Reason: strings.TrimSpace(a.Reason)}

		switch actionType {
		case domain.ManagementMoveStopLoss:
			if a.NewStopLoss == nil || !finite(*a.NewStopLoss) || *a.NewStopLoss <= 0 {
				verr.Add("management.actions[%d].new_stop_loss must be a positive finite price", i)
				continue
			}
			action.NewStopLoss = a.NewStopLoss
		case domain.ManagementClosePartial:
			if a.ClosePct == nil || !finite(*a.ClosePct) || *a.ClosePct <= 0 || *a.ClosePct >= 100 {
				verr.Add("management.actions[%d].close_pct must be in (0,100)", i)
				continue
			}
			action.ClosePct = a.ClosePct
		case domain.ManagementUpdateTakeProfit:
			for j, t := range a.NewTakeProfit {
				if t.Price == nil || !finite(*t.Price) || *t.Price <= 0 {
					verr.Add("management.actions[%d].new_take_profit[%d].price must be positive", i, j)
					continue
				}
				closePct := 0.0
				if t.ClosePct != nil && finite(*t.ClosePct) {
					closePct = *t.ClosePct
				}
				action.NewTakeProfit = append(action.NewTakeProfit, domain.PriceTarget{
					Price: *t.Price, ClosePct: closePct, Reason: strings.TrimSpace(t.Reason),
				})
			}
			if len(action.NewTakeProfit) == 0 {
				verr.Add("management.actions[%d] requires at least one new take profit level", i)
				continue
			}
		}
		plan.Actions = append(plan.Actions, action)
	}
	if len(plan.Actions) == 0 {
		return nil
	}
	return plan
}

func cleanStrings(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if len(s) > 200 {
			s = s[:200]
		}
		out = append(out, s)
		if len(out) >= 8 {
			break
		}
	}
	return out
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
