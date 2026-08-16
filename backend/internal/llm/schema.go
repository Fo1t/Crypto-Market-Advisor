// Package llm integrates with an OpenAI-compatible inference server, builds the
// compact feature payload, parses the model answer and validates it strictly.
//
// Nothing the model returns is trusted: every field is parsed into a typed Go
// struct and then checked semantically before it can become a recommendation.
package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ResponseSchemaVersion versions the JSON contract with the model.
const ResponseSchemaVersion = 3

// Response is the raw structure the model is asked to produce.
// Every numeric field is a pointer so that "absent" is distinguishable from
// "zero" during validation.
type Response struct {
	Action                  string                   `json:"action"`
	Confidence              *float64                 `json:"confidence"`
	RiskLevel               string                   `json:"risk_level"`
	Summary                 string                   `json:"summary"`
	RecommendedAllocationPc *float64                 `json:"recommended_allocation_pct"`
	Leverage                *LeverageJSON            `json:"leverage"`
	Entry                   *EntryJSON               `json:"entry"`
	TakeProfit              []TargetJSON             `json:"take_profit"`
	StopLoss                []TargetJSON             `json:"stop_loss"`
	Management              *ManageJSON              `json:"management"`
	SignalsFor              []string                 `json:"signals_for"`
	SignalsAgainst          []string                 `json:"signals_against"`
	Invalidation            []string                 `json:"invalidation_conditions"`
	Translations            map[string]NarrativeJSON `json:"translations"`
	NewsAssessment          *NewsAssessmentJSON      `json:"news_assessment"`
}

// NewsAssessmentJSON is optional because news may be disabled, unavailable or
// empty. Reasons remain multilingual while enums and identifiers stay shared.
type NewsAssessmentJSON struct {
	OverallSentiment  string            `json:"overall_sentiment"`
	Impact            string            `json:"impact"`
	TimeHorizon       string            `json:"time_horizon"`
	Confidence        *float64          `json:"confidence"`
	ImportantClusters []string          `json:"important_clusters"`
	Reasons           map[string]string `json:"reasons"`
}

// NarrativeJSON is one language variant of every model-authored string. The
// arrays of reasons align by index with the shared TP, SL and management arrays.
type NarrativeJSON struct {
	Summary           string   `json:"summary"`
	LeverageReason    string   `json:"leverage_reason"`
	TakeProfitReasons []string `json:"take_profit_reasons"`
	StopLossReasons   []string `json:"stop_loss_reasons"`
	ManagementReasons []string `json:"management_reasons"`
	SignalsFor        []string `json:"signals_for"`
	SignalsAgainst    []string `json:"signals_against"`
	Invalidation      []string `json:"invalidation_conditions"`
}

// LeverageJSON is the model's leverage suggestion.
type LeverageJSON struct {
	Recommended *float64 `json:"recommended"`
	Reason      string   `json:"reason"`
}

// EntryJSON is the model's entry plan.
type EntryJSON struct {
	Type         string   `json:"type"`
	CurrentPrice *float64 `json:"current_price"`
	PreferredMin *float64 `json:"preferred_min"`
	PreferredMax *float64 `json:"preferred_max"`
}

// TargetJSON is one take-profit or stop-loss step.
type TargetJSON struct {
	Price    *float64 `json:"price"`
	ClosePct *float64 `json:"close_pct"`
	Reason   string   `json:"reason"`
}

// ManageJSON groups management actions for an existing position.
type ManageJSON struct {
	PositionID string             `json:"position_id"`
	Actions    []ManageActionJSON `json:"actions"`
}

// ManageActionJSON is one requested change to a position.
type ManageActionJSON struct {
	Type          string       `json:"type"`
	NewStopLoss   *float64     `json:"new_stop_loss"`
	NewTakeProfit []TargetJSON `json:"new_take_profit"`
	ClosePct      *float64     `json:"close_pct"`
	Reason        string       `json:"reason"`
}

// ErrNoJSON is returned when the model produced no parseable JSON object.
var ErrNoJSON = errors.New("no JSON object found in model output")

// ParseResponse extracts and decodes the JSON object from a raw completion.
// Local models frequently wrap JSON in prose or code fences, so the first
// balanced object in the text is used rather than requiring a pristine answer.
func ParseResponse(raw string) (*Response, error) {
	text := stripCodeFences(strings.TrimSpace(raw))
	if text == "" {
		return nil, ErrNoJSON
	}

	candidate, err := extractJSONObject(text)
	if err != nil {
		return nil, err
	}

	var resp Response
	decoder := json.NewDecoder(strings.NewReader(candidate))
	decoder.UseNumber()
	if err := json.Unmarshal([]byte(candidate), &resp); err != nil {
		return nil, fmt.Errorf("decode model json: %w", err)
	}
	return &resp, nil
}

// stripCodeFences removes ```json fences and any <think> block a reasoning
// model may emit before the answer.
func stripCodeFences(s string) string {
	if idx := strings.LastIndex(s, "</think>"); idx >= 0 {
		s = s[idx+len("</think>"):]
	}
	s = strings.TrimSpace(s)

	if !strings.HasPrefix(s, "```") {
		return s
	}
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```JSON")
	s = strings.TrimPrefix(s, "```")
	if idx := strings.LastIndex(s, "```"); idx >= 0 {
		s = s[:idx]
	}
	return strings.TrimSpace(s)
}

// extractJSONObject returns the first balanced {...} block, ignoring braces
// inside strings so that a reason text containing "{" cannot break parsing.
func extractJSONObject(s string) (string, error) {
	start := strings.Index(s, "{")
	if start < 0 {
		return "", ErrNoJSON
	}

	depth := 0
	inString := false
	escaped := false

	for i := start; i < len(s); i++ {
		ch := s[i]
		if escaped {
			escaped = false
			continue
		}
		switch {
		case ch == '\\' && inString:
			escaped = true
		case ch == '"':
			inString = !inString
		case inString:
			// braces inside strings are literal text
		case ch == '{':
			depth++
		case ch == '}':
			depth--
			if depth == 0 {
				return s[start : i+1], nil
			}
		}
	}
	return "", fmt.Errorf("%w: unbalanced object", ErrNoJSON)
}
