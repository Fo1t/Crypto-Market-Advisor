package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/crypto-market-advisor/advisor/internal/domain"
	"github.com/crypto-market-advisor/advisor/internal/repository"
)

// RecommendationDTO is the API view of an advisory card.
type RecommendationDTO struct {
	ID             string                                    `json:"id"`
	AnalysisRunID  *string                                   `json:"analysis_run_id,omitempty"`
	Symbol         string                                    `json:"symbol"`
	CreatedAt      time.Time                                 `json:"created_at"`
	DismissedAt    *time.Time                                `json:"dismissed_at,omitempty"`
	Action         domain.RecommendationAction               `json:"action"`
	Confidence     int                                       `json:"confidence"`
	RiskLevel      domain.RiskLevel                          `json:"risk_level"`
	Summary        string                                    `json:"summary"`
	ReferencePrice string                                    `json:"reference_price"`
	AllocationPct  string                                    `json:"recommended_allocation_pct"`
	Leverage       domain.LeveragePlan                       `json:"leverage"`
	Entry          *domain.EntryPlan                         `json:"entry,omitempty"`
	TakeProfit     []domain.PriceTarget                      `json:"take_profit"`
	StopLoss       []domain.PriceTarget                      `json:"stop_loss"`
	Management     *domain.ManagementPlan                    `json:"management,omitempty"`
	SignalsFor     []string                                  `json:"signals_for"`
	SignalsAgainst []string                                  `json:"signals_against"`
	Invalidation   []string                                  `json:"invalidation_conditions"`
	RiskNotes      []string                                  `json:"risk_engine_notes,omitempty"`
	Translations   map[string]domain.RecommendationNarrative `json:"translations,omitempty"`
	ModelName      string                                    `json:"model_name"`
	PromptVersion  string                                    `json:"prompt_version"`
	MarketRegime   domain.MarketRegime                       `json:"market_regime"`
	DataQuality    domain.DataQualityStatus                  `json:"data_quality"`
	Freshness      domain.RecommendationFreshness            `json:"freshness"`
	Decision       *domain.Decision                          `json:"decision,omitempty"`
	Outcome        *domain.Outcome                           `json:"outcome,omitempty"`
}

func recommendationToDTO(rec domain.Recommendation, decision *domain.Decision, outcome *domain.Outcome, staleAfter time.Duration) RecommendationDTO {
	dto := RecommendationDTO{
		ID: rec.ID.String(), Symbol: rec.Symbol, CreatedAt: rec.CreatedAt, DismissedAt: rec.DismissedAt, Action: rec.Action,
		Confidence: rec.Confidence, RiskLevel: rec.RiskLevel, Summary: rec.Summary,
		ReferencePrice: rec.ReferencePrice.String(), AllocationPct: rec.AllocationPct.String(),
		Leverage: rec.Leverage, Entry: rec.Entry, TakeProfit: rec.TakeProfit, StopLoss: rec.StopLoss,
		Management: rec.Management, SignalsFor: rec.SignalsFor, SignalsAgainst: rec.SignalsAgainst,
		Invalidation: rec.Invalidation, RiskNotes: rec.RiskEngineNotes, ModelName: rec.ModelName,
		Translations:  rec.Translations,
		PromptVersion: rec.PromptVersion, MarketRegime: rec.MarketRegime, DataQuality: rec.DataQuality,
		Freshness: rec.Freshness(time.Now().UTC(), staleAfter),
		Decision:  decision, Outcome: outcome,
	}
	if rec.AnalysisRunID != nil {
		id := rec.AnalysisRunID.String()
		dto.AnalysisRunID = &id
	}
	if dto.TakeProfit == nil {
		dto.TakeProfit = []domain.PriceTarget{}
	}
	if dto.StopLoss == nil {
		dto.StopLoss = []domain.PriceTarget{}
	}
	return dto
}

func (s *Server) handleListRecommendations(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	limit, offset := pagination(r, 50)
	action := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("action")))
	if action != "" && !domain.RecommendationAction(action).Valid() {
		WriteError(w, r, s.log, ErrValidation("invalid recommendation action"))
		return
	}
	riskLevel := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("risk_level")))
	if riskLevel != "" && !domain.RiskLevel(riskLevel).Valid() {
		WriteError(w, r, s.log, ErrValidation("invalid risk level"))
		return
	}
	dataQuality := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("data_quality")))
	if dataQuality != "" && dataQuality != string(domain.DataQualityOK) &&
		dataQuality != string(domain.DataQualityDegraded) && dataQuality != string(domain.DataQualityUnusable) {
		WriteError(w, r, s.log, ErrValidation("invalid data quality"))
		return
	}
	visibility := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("visibility")))
	if visibility == "" {
		visibility = "active"
	}
	if visibility != "active" && visibility != "dismissed" && visibility != "all" {
		WriteError(w, r, s.log, ErrValidation("visibility must be active, dismissed, or all"))
		return
	}
	minConfidence, err := parseConfidenceFilter(r.URL.Query().Get("min_confidence"), "min_confidence")
	if err != nil {
		WriteError(w, r, s.log, err)
		return
	}
	maxConfidence, err := parseConfidenceFilter(r.URL.Query().Get("max_confidence"), "max_confidence")
	if err != nil {
		WriteError(w, r, s.log, err)
		return
	}
	if minConfidence != nil && maxConfidence != nil && *minConfidence > *maxConfidence {
		WriteError(w, r, s.log, ErrValidation("min_confidence must not exceed max_confidence"))
		return
	}
	filter := repository.ListFilter{
		Symbol:        strings.ToUpper(r.URL.Query().Get("symbol")),
		Action:        action,
		RiskLevel:     riskLevel,
		DataQuality:   dataQuality,
		MinConfidence: minConfidence,
		MaxConfidence: maxConfidence,
		Visibility:    visibility,
		Since:         parseSince(r),
		Limit:         limit,
		Offset:        offset,
	}

	recs, total, err := s.deps.Repos.Recommendations.List(ctx, filter)
	if err != nil {
		WriteError(w, r, s.log, ErrInternal("failed to list recommendations").WithCause(err))
		return
	}

	ids := make([]uuid.UUID, 0, len(recs))
	for _, rec := range recs {
		ids = append(ids, rec.ID)
	}
	decisions, outcomes := s.decisionsAndOutcomes(ctx, ids)

	items := make([]RecommendationDTO, 0, len(recs))
	for _, rec := range recs {
		items = append(items, s.decorate(rec, decisions, outcomes))
	}
	WriteJSON(w, http.StatusOK, Page[RecommendationDTO]{Items: items, Total: total, Limit: limit, Offset: offset})
}

func parseConfidenceFilter(raw, name string) (*int, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 || value > 100 {
		return nil, ErrValidation(name + " must be an integer between 0 and 100")
	}
	return &value, nil
}

func (s *Server) decisionsAndOutcomes(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]domain.Decision, map[uuid.UUID]domain.Outcome) {
	decisions, err := s.deps.Repos.Recommendations.Decisions(ctx, ids)
	if err != nil {
		s.log.Warn("load decisions failed", "error", err)
		decisions = map[uuid.UUID]domain.Decision{}
	}
	outcomes, err := s.deps.Repos.Recommendations.Outcomes(ctx, ids)
	if err != nil {
		s.log.Warn("load outcomes failed", "error", err)
		outcomes = map[uuid.UUID]domain.Outcome{}
	}
	return decisions, outcomes
}

func (s *Server) decorate(rec domain.Recommendation, decisions map[uuid.UUID]domain.Decision, outcomes map[uuid.UUID]domain.Outcome) RecommendationDTO {
	var decision *domain.Decision
	if d, ok := decisions[rec.ID]; ok {
		decision = &d
	}
	var outcome *domain.Outcome
	if o, ok := outcomes[rec.ID]; ok {
		outcome = &o
	}
	return recommendationToDTO(rec, decision, outcome, s.deps.Config.Analysis.StaleAfter)
}

func (s *Server) handleGetRecommendation(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(r, "id")
	if err != nil {
		WriteError(w, r, s.log, err)
		return
	}
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	rec, err := s.deps.Repos.Recommendations.Get(ctx, id)
	if err != nil {
		WriteError(w, r, s.log, notFoundOr(err, "recommendation not found"))
		return
	}
	decisions, outcomes := s.decisionsAndOutcomes(ctx, []uuid.UUID{id})
	WriteJSON(w, http.StatusOK, s.decorate(rec, decisions, outcomes))
}

func (s *Server) handleDismissRecommendation(w http.ResponseWriter, r *http.Request) {
	s.setRecommendationDismissed(w, r, true)
}

func (s *Server) handleDismissAllRecommendations(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()
	dismissed, err := s.deps.Repos.Recommendations.DismissAll(ctx)
	if err != nil {
		WriteError(w, r, s.log, ErrInternal("failed to dismiss recommendations").WithCause(err))
		return
	}
	WriteJSON(w, http.StatusOK, map[string]int64{"dismissed_count": dismissed})
}

func (s *Server) handleRestoreRecommendation(w http.ResponseWriter, r *http.Request) {
	s.setRecommendationDismissed(w, r, false)
}

func (s *Server) setRecommendationDismissed(w http.ResponseWriter, r *http.Request, dismissed bool) {
	id, err := parseUUID(r, "id")
	if err != nil {
		WriteError(w, r, s.log, err)
		return
	}
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()
	if err := s.deps.Repos.Recommendations.SetDismissed(ctx, id, dismissed); err != nil {
		WriteError(w, r, s.log, notFoundOr(err, "recommendation not found"))
		return
	}
	if dismissed {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]string{"status": "restored"})
}

// DecisionRequest records what the user did with a recommendation.
type DecisionRequest struct {
	Decision string `json:"decision"`
	Note     string `json:"note"`
}

func (s *Server) handleRecommendationDecision(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(r, "id")
	if err != nil {
		WriteError(w, r, s.log, err)
		return
	}
	var req DecisionRequest
	if err := decodeJSON(r, &req); err != nil {
		WriteError(w, r, s.log, err)
		return
	}

	decision := domain.UserDecision(strings.ToUpper(strings.TrimSpace(req.Decision)))
	if !decision.Valid() {
		WriteError(w, r, s.log, ErrValidation("decision must be one of "+domain.AllowedDecisions()))
		return
	}

	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	if _, err := s.deps.Repos.Recommendations.Get(ctx, id); err != nil {
		WriteError(w, r, s.log, notFoundOr(err, "recommendation not found"))
		return
	}

	// OPENED is only recorded through the position endpoint, which links the
	// position to the recommendation; accepting it here would create a decision
	// that points at nothing.
	if decision == domain.DecisionOpened {
		WriteError(w, r, s.log, ErrValidation("record an opened trade by creating a position with this recommendation_id"))
		return
	}

	if err := s.deps.Repos.Recommendations.SetDecision(ctx, domain.Decision{
		RecommendationID: id,
		Decision:         decision,
		DecidedAt:        time.Now().UTC(),
		Note:             req.Note,
	}); err != nil {
		WriteError(w, r, s.log, ErrInternal("failed to store the decision").WithCause(err))
		return
	}
	WriteJSON(w, http.StatusOK, map[string]string{"status": "ok", "decision": string(decision)})
}

// repositoryListFilter builds the filter used by the dashboard summary.
func repositoryListFilter(limit int) repository.ListFilter {
	return repository.ListFilter{Limit: limit}
}
