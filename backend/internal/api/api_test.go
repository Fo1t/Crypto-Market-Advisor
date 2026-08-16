package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/crypto-market-advisor/advisor/internal/domain"
	"github.com/crypto-market-advisor/advisor/internal/logging"
	"github.com/crypto-market-advisor/advisor/internal/repository"
)

func TestWriteErrorProducesTheStandardEnvelope(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/markets/BTC", nil)

	WriteError(recorder, request, logging.New("error", "text"), ErrNotFound("market not found"))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", recorder.Code)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if body.Error.Code != string(CodeNotFound) || body.Error.Message != "market not found" {
		t.Fatalf("unexpected envelope: %+v", body.Error)
	}
}

func TestUnknownErrorBecomesInternalWithoutLeakingDetails(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/statistics", nil)

	WriteError(recorder, request, logging.New("error", "text"), errors.New("connection refused to 10.0.0.5:5432"))

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "10.0.0.5") {
		t.Fatal("internal details must not reach the client")
	}
}

func TestRequestIDMiddlewareAddsAndReusesID(t *testing.T) {
	handler := RequestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if logging.RequestID(r.Context()) == "" {
			t.Error("request id must be in the context")
		}
		w.WriteHeader(http.StatusOK)
	}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if recorder.Header().Get("X-Request-ID") == "" {
		t.Fatal("a request id must be returned")
	}

	recorder = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	request.Header.Set("X-Request-ID", "caller-supplied")
	handler.ServeHTTP(recorder, request)

	if got := recorder.Header().Get("X-Request-ID"); got != "caller-supplied" {
		t.Fatalf("a caller-supplied request id must be preserved, got %q", got)
	}
}

func TestRecoverMiddlewareTurnsPanicIntoFiveHundred(t *testing.T) {
	handler := RecoverMiddleware(logging.New("error", "text"))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/health", nil))

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), string(CodeInternal)) {
		t.Fatalf("expected the standard envelope, got %s", recorder.Body.String())
	}
}

func TestCORSPreflight(t *testing.T) {
	handler := CORSMiddleware([]string{"*"})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodOptions, "/api/markets", nil))

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("preflight must short-circuit, got %d", recorder.Code)
	}
	if recorder.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatal("CORS headers are missing")
	}
}

func TestPaginationBounds(t *testing.T) {
	cases := []struct {
		query         string
		limit, offset int
	}{
		{"", 50, 0},
		{"?limit=10&offset=20", 10, 20},
		{"?limit=100000", 50, 0},
		{"?limit=abc&offset=-5", 50, 0},
	}
	for _, c := range cases {
		limit, offset := pagination(httptest.NewRequest(http.MethodGet, "/api/recommendations"+c.query, nil), 50)
		if limit != c.limit || offset != c.offset {
			t.Fatalf("%q: expected %d/%d, got %d/%d", c.query, c.limit, c.offset, limit, offset)
		}
	}
}

func TestParseSince(t *testing.T) {
	if got := parseSince(httptest.NewRequest(http.MethodGet, "/api/statistics", nil)); got != nil {
		t.Fatalf("expected no window, got %v", got)
	}

	got := parseSince(httptest.NewRequest(http.MethodGet, "/api/statistics?days=7", nil))
	if got == nil || time.Since(*got) < 6*24*time.Hour {
		t.Fatalf("expected a 7 day window, got %v", got)
	}

	exact := parseSince(httptest.NewRequest(http.MethodGet, "/api/statistics?since=2024-01-02T03:04:05Z", nil))
	if exact == nil || exact.Year() != 2024 || exact.Month() != time.January {
		t.Fatalf("expected the explicit timestamp, got %v", exact)
	}
}

func TestParseConfidenceFilter(t *testing.T) {
	value, err := parseConfidenceFilter("75", "min_confidence")
	if err != nil || value == nil || *value != 75 {
		t.Fatalf("expected 75, got %v / %v", value, err)
	}
	if value, err := parseConfidenceFilter("", "min_confidence"); err != nil || value != nil {
		t.Fatalf("empty filter must be omitted, got %v / %v", value, err)
	}
	for _, invalid := range []string{"-1", "101", "abc"} {
		if _, err := parseConfidenceFilter(invalid, "min_confidence"); err == nil {
			t.Fatalf("%q must be rejected", invalid)
		}
	}
}

func TestDecodeJSONRejectsUnknownFields(t *testing.T) {
	var target CreateMarketRequest
	request := httptest.NewRequest(http.MethodPost, "/api/markets",
		strings.NewReader(`{"coingecko_id":"bitcoin","symbol":"BTC","surprise":true}`))

	err := decodeJSON(request, &target)
	if err == nil {
		t.Fatal("unknown fields must be rejected")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status() != http.StatusBadRequest {
		t.Fatalf("expected a 400 APIError, got %v", err)
	}
}

func TestRecommendationDTOFreshness(t *testing.T) {
	base := domain.Recommendation{
		ID:             uuid.New(),
		Symbol:         "BTC",
		Action:         domain.RecommendationOpenLong,
		Confidence:     70,
		ReferencePrice: decimal.NewFromInt(100000),
		AllocationPct:  decimal.NewFromInt(5),
		DataQuality:    domain.DataQualityOK,
		CreatedAt:      time.Now().UTC(),
	}

	fresh := recommendationToDTO(base, nil, nil, 30*time.Minute)
	if fresh.Freshness != domain.FreshnessFresh {
		t.Fatalf("a new recommendation must be fresh, got %s", fresh.Freshness)
	}

	old := base
	old.CreatedAt = time.Now().UTC().Add(-2 * time.Hour)
	if got := recommendationToDTO(old, nil, nil, 30*time.Minute); got.Freshness != domain.FreshnessStale {
		t.Fatalf("an old recommendation must be stale, got %s", got.Freshness)
	}

	degraded := base
	degraded.DataQuality = domain.DataQualityDegraded
	if got := recommendationToDTO(degraded, nil, nil, 30*time.Minute); got.Freshness != domain.FreshnessIncomplete {
		t.Fatalf("degraded data must be flagged incomplete, got %s", got.Freshness)
	}

	// Money must cross the wire as an exact decimal string, never as a float.
	if fresh.ReferencePrice != "100000" || fresh.AllocationPct != "5" {
		t.Fatalf("decimals must serialise exactly: %s / %s", fresh.ReferencePrice, fresh.AllocationPct)
	}
}

func TestNotFoundMapping(t *testing.T) {
	err := notFoundOr(repository.ErrNotFound, "position not found")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status() != http.StatusNotFound {
		t.Fatalf("repository misses must become 404, got %v", err)
	}

	err = notFoundOr(errors.New("syntax error at or near"), "position not found")
	if !errors.As(err, &apiErr) || apiErr.Status() != http.StatusInternalServerError {
		t.Fatalf("other errors must become 500, got %v", err)
	}
	if notFoundOr(nil, "x") != nil {
		t.Fatal("a nil error must stay nil")
	}
}

func TestWriteJSONSetsContentType(t *testing.T) {
	recorder := httptest.NewRecorder()
	WriteJSON(recorder, http.StatusCreated, map[string]string{"status": "ok"})

	if recorder.Code != http.StatusCreated {
		t.Fatalf("unexpected status %d", recorder.Code)
	}
	if ct := recorder.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("unexpected content type %q", ct)
	}
}
