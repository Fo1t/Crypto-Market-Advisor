package llm

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

func responseWithNewsAssessment(t *testing.T, clusterID uuid.UUID) string {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(validMultilingual), &payload); err != nil {
		t.Fatal(err)
	}
	payload["news_assessment"] = map[string]any{
		"overall_sentiment":  "MIXED",
		"impact":             "HIGH",
		"time_horizon":       "SHORT_TERM",
		"confidence":         74,
		"important_clusters": []string{clusterID.String()},
		"reasons": map[string]string{
			"ru":    "Важная новость повышает краткосрочную неопределённость.",
			"en":    "Important news increases short-term uncertainty.",
			"zh-CN": "重要新闻增加了短期不确定性。",
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func baseContext() ValidationContext {
	return ValidationContext{
		Symbol:           "BTC",
		ReferencePrice:   100000,
		MinLeverage:      5,
		MaxLeverage:      50,
		MaxAllocationPct: 15,
	}
}

const validLong = `{
  "action": "OPEN_LONG",
  "confidence": 72,
  "risk_level": "medium",
  "summary": "Multi-timeframe continuation.",
  "recommended_allocation_pct": 5,
  "leverage": {"recommended": 10, "reason": "moderate volatility"},
  "entry": {"type": "market", "current_price": 100000},
  "take_profit": [
    {"price": 102000, "close_pct": 50},
    {"price": 104000, "close_pct": 50}
  ],
  "stop_loss": [{"price": 98000, "close_pct": 100}],
  "signals_for": ["1h uptrend"],
  "signals_against": ["4h resistance"],
  "invalidation_conditions": ["1h close below 98000"]
}`

const validMultilingual = `{
  "action": "OPEN_LONG",
  "confidence": 72,
  "risk_level": "medium",
  "recommended_allocation_pct": 5,
  "leverage": {"recommended": 10},
  "entry": {"type": "market", "current_price": 100000},
  "take_profit": [
    {"price": 102000, "close_pct": 50},
    {"price": 104000, "close_pct": 50}
  ],
  "stop_loss": [{"price": 98000, "close_pct": 100}],
  "translations": {
    "ru": {
      "summary": "Продолжение тренда подтверждается несколькими таймфреймами.",
      "leverage_reason": "Умеренная волатильность",
      "take_profit_reasons": ["Ближайшее сопротивление", "Следующее сопротивление"],
      "stop_loss_reasons": ["Ниже подтверждённой поддержки"],
      "management_reasons": [],
      "signals_for": ["Восходящий тренд на часовом графике"],
      "signals_against": ["Сопротивление на четырёхчасовом графике"],
      "invalidation_conditions": ["Часовая свеча закроется ниже 98000"]
    },
    "en": {
      "summary": "Multiple timeframes confirm trend continuation.",
      "leverage_reason": "Moderate volatility",
      "take_profit_reasons": ["Nearest resistance", "Next resistance"],
      "stop_loss_reasons": ["Below confirmed support"],
      "management_reasons": [],
      "signals_for": ["One-hour uptrend"],
      "signals_against": ["Four-hour resistance"],
      "invalidation_conditions": ["One-hour candle closes below 98000"]
    },
    "zh-CN": {
      "summary": "多个时间周期确认趋势延续。",
      "leverage_reason": "波动率适中",
      "take_profit_reasons": ["最近阻力位", "下一个阻力位"],
      "stop_loss_reasons": ["确认支撑位下方"],
      "management_reasons": [],
      "signals_for": ["一小时图呈上升趋势"],
      "signals_against": ["四小时图存在阻力"],
      "invalidation_conditions": ["一小时蜡烛收于98000下方"]
    }
  }
}`

func mustValidate(t *testing.T, raw string, ctx ValidationContext) *Validated {
	t.Helper()
	resp, err := ParseResponse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	v, err := Validate(resp, ctx)
	if err != nil {
		t.Fatalf("validation failed: %v", err)
	}
	return v
}

func expectRejection(t *testing.T, raw string, ctx ValidationContext, substring string) {
	t.Helper()
	resp, err := ParseResponse(raw)
	if err != nil {
		if substring != "" && !strings.Contains(err.Error(), substring) {
			t.Fatalf("expected parse error containing %q, got %v", substring, err)
		}
		return
	}
	_, err = Validate(resp, ctx)
	if err == nil {
		t.Fatal("expected validation to reject the response")
	}
	if substring != "" && !strings.Contains(err.Error(), substring) {
		t.Fatalf("expected error containing %q, got %v", substring, err)
	}
}

func TestValidLongResponse(t *testing.T) {
	v := mustValidate(t, validLong, baseContext())

	if v.Action != domain.RecommendationOpenLong {
		t.Fatalf("unexpected action %s", v.Action)
	}
	if v.Confidence != 72 || v.Leverage != 10 {
		t.Fatalf("unexpected confidence/leverage: %d/%d", v.Confidence, v.Leverage)
	}
	if len(v.TakeProfit) != 2 || len(v.StopLoss) != 1 {
		t.Fatalf("unexpected target counts: %d tp, %d sl", len(v.TakeProfit), len(v.StopLoss))
	}
	if v.TakeProfit[0].Price >= v.TakeProfit[1].Price {
		t.Fatal("take profit levels must be ordered by the sequence they are hit")
	}
}

func TestNewsAssessmentValidatesClustersAndLanguages(t *testing.T) {
	clusterID := uuid.New()
	ctx := baseContext()
	ctx.NewsClusterIDs = []uuid.UUID{clusterID}
	validated := mustValidate(t, responseWithNewsAssessment(t, clusterID), ctx)
	if validated.NewsAssessment == nil || validated.NewsAssessment.Impact != "HIGH" {
		t.Fatalf("assessment=%+v", validated.NewsAssessment)
	}
	if problem := multilingualProblem(validated); problem != "" {
		t.Fatalf("multilingual assessment rejected: %s", problem)
	}

	foreignID := uuid.New()
	resp, err := ParseResponse(responseWithNewsAssessment(t, foreignID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Validate(resp, ctx); err == nil || !strings.Contains(err.Error(), "absent from input") {
		t.Fatalf("foreign cluster must be rejected, got %v", err)
	}
}

func TestMalformedJSONIsRejected(t *testing.T) {
	expectRejection(t, `{"action": "OPEN_LONG", "confidence":`, baseContext(), "")
	expectRejection(t, `I think you should go long!`, baseContext(), "")
}

func TestJSONWrappedInProseAndFencesIsAccepted(t *testing.T) {
	wrapped := "Sure, here is my analysis:\n```json\n" + validLong + "\n```\nHope that helps."
	v := mustValidate(t, wrapped, baseContext())
	if v.Action != domain.RecommendationOpenLong {
		t.Fatalf("unexpected action %s", v.Action)
	}
}

func TestReasoningBlockIsStripped(t *testing.T) {
	wrapped := "<think>Let me weigh the signals { and braces }</think>" + validLong
	v := mustValidate(t, wrapped, baseContext())
	if v.Confidence != 72 {
		t.Fatalf("unexpected confidence %d", v.Confidence)
	}
}

func TestInvalidEnumIsRejected(t *testing.T) {
	raw := strings.Replace(validLong, `"OPEN_LONG"`, `"BUY_NOW"`, 1)
	expectRejection(t, raw, baseContext(), "action must be one of")

	raw = strings.Replace(validLong, `"medium"`, `"catastrophic"`, 1)
	expectRejection(t, raw, baseContext(), "risk_level must be one of")
}

func TestLeverageOutOfRangeIsRejected(t *testing.T) {
	raw := strings.Replace(validLong, `"recommended": 10`, `"recommended": 100`, 1)
	expectRejection(t, raw, baseContext(), "leverage.recommended must be between")

	raw = strings.Replace(validLong, `"recommended": 10`, `"recommended": 1`, 1)
	expectRejection(t, raw, baseContext(), "leverage.recommended must be between")
}

func TestConfidenceOutOfRangeIsRejected(t *testing.T) {
	raw := strings.Replace(validLong, `"confidence": 72`, `"confidence": 140`, 1)
	expectRejection(t, raw, baseContext(), "confidence must be between 0 and 100")
}

func TestReversedTakeProfitIsRejected(t *testing.T) {
	raw := strings.Replace(validLong, `{"price": 102000, "close_pct": 50}`, `{"price": 98500, "close_pct": 50}`, 1)
	expectRejection(t, raw, baseContext(), "wrong side of the reference price")
}

func TestReversedStopLossIsRejected(t *testing.T) {
	raw := strings.Replace(validLong, `{"price": 98000, "close_pct": 100}`, `{"price": 101000, "close_pct": 100}`, 1)
	expectRejection(t, raw, baseContext(), "wrong side of the reference price")
}

func TestShortDirectionIsValidatedMirrorImage(t *testing.T) {
	raw := `{
	  "action": "OPEN_SHORT",
	  "confidence": 65,
	  "risk_level": "high",
	  "summary": "Breakdown continuation.",
	  "recommended_allocation_pct": 4,
	  "leverage": {"recommended": 8},
	  "entry": {"type": "market", "current_price": 100000},
	  "take_profit": [{"price": 97000, "close_pct": 100}],
	  "stop_loss": [{"price": 102000, "close_pct": 100}],
	  "signals_for": ["breakdown"],
	  "signals_against": [],
	  "invalidation_conditions": []
	}`
	v := mustValidate(t, raw, baseContext())
	if v.Action != domain.RecommendationOpenShort {
		t.Fatalf("unexpected action %s", v.Action)
	}

	reversed := strings.Replace(raw, `{"price": 97000, "close_pct": 100}`, `{"price": 103000, "close_pct": 100}`, 1)
	expectRejection(t, reversed, baseContext(), "wrong side of the reference price")
}

func TestClosePctMustSumTo100(t *testing.T) {
	raw := strings.Replace(validLong, `{"price": 104000, "close_pct": 50}`, `{"price": 104000, "close_pct": 20}`, 1)
	expectRejection(t, raw, baseContext(), "must sum to 100")
}

func TestNegativeClosePctIsRejected(t *testing.T) {
	raw := strings.Replace(validLong, `"close_pct": 100`, `"close_pct": -100`, 1)
	expectRejection(t, raw, baseContext(), "close_pct must be in (0,100]")
}

func TestSmallRoundingDriftIsNormalised(t *testing.T) {
	raw := strings.Replace(validLong, `{"price": 104000, "close_pct": 50}`, `{"price": 104000, "close_pct": 48}`, 1)
	v := mustValidate(t, raw, baseContext())

	var sum float64
	for _, tp := range v.TakeProfit {
		sum += tp.ClosePct
	}
	if sum < 99.9 || sum > 100.1 {
		t.Fatalf("close percentages must be normalised to 100, got %v", sum)
	}
}

func TestNoActionDropsTradePlan(t *testing.T) {
	raw := `{
	  "action": "NO_ACTION",
	  "confidence": 40,
	  "risk_level": "low",
	  "summary": "Signals conflict, no edge.",
	  "recommended_allocation_pct": 0,
	  "leverage": null,
	  "entry": null,
	  "take_profit": [{"price": 1, "close_pct": 100}],
	  "stop_loss": null,
	  "signals_for": [],
	  "signals_against": ["timeframe conflict"],
	  "invalidation_conditions": []
	}`
	v := mustValidate(t, raw, baseContext())

	if v.Action != domain.RecommendationNoAction {
		t.Fatalf("unexpected action %s", v.Action)
	}
	if len(v.TakeProfit) != 0 || len(v.StopLoss) != 0 || v.Entry != nil {
		t.Fatal("NO_ACTION must not carry a trade plan")
	}
}

func TestEntryActionRequiresTargets(t *testing.T) {
	raw := `{
	  "action": "OPEN_LONG",
	  "confidence": 70,
	  "risk_level": "medium",
	  "summary": "Long.",
	  "recommended_allocation_pct": 5,
	  "leverage": {"recommended": 10},
	  "entry": {"type": "market", "current_price": 100000},
	  "take_profit": [],
	  "stop_loss": [],
	  "signals_for": [],
	  "signals_against": [],
	  "invalidation_conditions": []
	}`
	expectRejection(t, raw, baseContext(), "take_profit level is required")
}

func TestManagementRequiresKnownPosition(t *testing.T) {
	known := uuid.New()
	ctx := baseContext()
	ctx.OpenPositionIDs = []uuid.UUID{known}

	raw := `{
	  "action": "MANAGE_POSITION",
	  "confidence": 60,
	  "risk_level": "medium",
	  "summary": "Tighten the stop.",
	  "management": {
	    "position_id": "` + known.String() + `",
	    "actions": [{"type": "MOVE_STOP_LOSS", "new_stop_loss": 99000, "reason": "break even"}]
	  }
	}`
	v := mustValidate(t, raw, ctx)
	if v.Management == nil || v.Management.PositionID != known {
		t.Fatal("management plan must reference the known position")
	}

	unknown := strings.Replace(raw, known.String(), uuid.New().String(), 1)
	expectRejection(t, unknown, ctx, "does not match any open position")

	notAUUID := strings.Replace(raw, known.String(), "position-1", 1)
	expectRejection(t, notAUUID, ctx, "must be a valid position UUID")
}

func TestEntryZoneFarFromPriceIsRejected(t *testing.T) {
	raw := strings.Replace(validLong,
		`"entry": {"type": "market", "current_price": 100000}`,
		`"entry": {"type": "zone", "current_price": 100000, "preferred_min": 70000, "preferred_max": 72000}`, 1)
	expectRejection(t, raw, baseContext(), "away from the current price")
}

func TestNaNAndInfinityAreRejected(t *testing.T) {
	// JSON has no NaN literal, so a model emitting one produces invalid JSON
	// and must be rejected at the parse stage rather than silently coerced.
	expectRejection(t, strings.Replace(validLong, `"confidence": 72`, `"confidence": NaN`, 1), baseContext(), "")
	expectRejection(t, strings.Replace(validLong, `"price": 102000`, `"price": Infinity`, 1), baseContext(), "")
}

func TestEmptySummaryIsRejected(t *testing.T) {
	raw := strings.Replace(validLong, `"summary": "Multi-timeframe continuation.",`, `"summary": "",`, 1)
	expectRejection(t, raw, baseContext(), "summary must not be empty")
}

func TestRepairPromptListsEveryProblem(t *testing.T) {
	prompt := RepairPrompt([]string{"confidence must be between 0 and 100", "leverage too high"})
	if !strings.Contains(prompt, "confidence must be between") || !strings.Contains(prompt, "leverage too high") {
		t.Fatal("repair prompt must restate every problem")
	}
	if !strings.Contains(prompt, "NO_ACTION") {
		t.Fatal("repair prompt must offer NO_ACTION as an escape hatch")
	}
}
