package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

// RecommendationRepository stores predictions, decisions, outcomes and inferences.
type RecommendationRepository struct{ pool *pgxpool.Pool }

const recColumns = `id, analysis_run_id, asset_id, symbol, created_at, action, confidence, risk_level,
	summary, reference_price::text, allocation_pct::text, llm_leverage, risk_max_leverage, final_leverage,
	leverage_reason, entry, take_profit, stop_loss, management, signals_for, signals_against,
	invalidation, model_name, prompt_version, schema_version, risk_engine_output, market_regime, data_quality,
	dismissed_at, translations, news_assessment`

// Insert persists an immutable recommendation.
func (r *RecommendationRepository) Insert(ctx context.Context, rec domain.Recommendation, validated any) error {
	entry, err := marshalNullable(rec.Entry)
	if err != nil {
		return err
	}
	tp, err := json.Marshal(rec.TakeProfit)
	if err != nil {
		return fmt.Errorf("marshal take profit: %w", err)
	}
	sl, err := json.Marshal(rec.StopLoss)
	if err != nil {
		return fmt.Errorf("marshal stop loss: %w", err)
	}
	mgmt, err := marshalNullable(rec.Management)
	if err != nil {
		return err
	}
	riskOut, err := json.Marshal(map[string]any{
		"leverage": rec.Leverage,
		"notes":    rec.RiskEngineNotes,
	})
	if err != nil {
		return fmt.Errorf("marshal risk output: %w", err)
	}
	validatedJSON, err := marshalNullable(validated)
	if err != nil {
		return err
	}
	translations, err := json.Marshal(rec.Translations)
	if err != nil {
		return fmt.Errorf("marshal recommendation translations: %w", err)
	}
	newsAssessment, err := marshalNullable(rec.NewsAssessment)
	if err != nil {
		return fmt.Errorf("marshal recommendation news assessment: %w", err)
	}

	_, err = r.pool.Exec(ctx, `
		INSERT INTO recommendations (id, analysis_run_id, asset_id, symbol, created_at, action, confidence,
			risk_level, summary, reference_price, allocation_pct, llm_leverage, risk_max_leverage, final_leverage,
			leverage_reason, entry, take_profit, stop_loss, management, signals_for, signals_against, invalidation,
			model_name, prompt_version, schema_version, risk_engine_output, validated_output, market_regime, data_quality,
			translations, news_assessment)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::numeric,$11::numeric,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31)`,
		rec.ID, rec.AnalysisRunID, rec.AssetID, rec.Symbol, rec.CreatedAt.UTC(), string(rec.Action), rec.Confidence,
		string(rec.RiskLevel), rec.Summary, numIn(rec.ReferencePrice), numIn(rec.AllocationPct),
		rec.Leverage.LLMSuggested, rec.Leverage.RiskMaximum, rec.Leverage.Recommended, rec.Leverage.Reason,
		entry, tp, sl, mgmt, rec.SignalsFor, rec.SignalsAgainst, rec.Invalidation,
		rec.ModelName, rec.PromptVersion, rec.SchemaVersion, riskOut, validatedJSON,
		string(rec.MarketRegime), string(rec.DataQuality), translations, newsAssessment)
	if err != nil {
		return fmt.Errorf("insert recommendation: %w", err)
	}
	return nil
}

func marshalNullable(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	// Typed nil pointers must serialise as SQL NULL, not the string "null".
	switch t := v.(type) {
	case *domain.EntryPlan:
		if t == nil {
			return nil, nil
		}
	case *domain.ManagementPlan:
		if t == nil {
			return nil, nil
		}
	case *domain.NewsAssessment:
		if t == nil {
			return nil, nil
		}
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal json column: %w", err)
	}
	return raw, nil
}

func scanRecommendation(row pgx.Row) (domain.Recommendation, error) {
	var rec domain.Recommendation
	var refPrice, allocPct string
	var action, riskLevel, regime, quality string
	var entry, tp, sl, mgmt, riskOut, translations, newsAssessment []byte

	err := row.Scan(&rec.ID, &rec.AnalysisRunID, &rec.AssetID, &rec.Symbol, &rec.CreatedAt, &action,
		&rec.Confidence, &riskLevel, &rec.Summary, &refPrice, &allocPct, &rec.Leverage.LLMSuggested,
		&rec.Leverage.RiskMaximum, &rec.Leverage.Recommended, &rec.Leverage.Reason, &entry, &tp, &sl, &mgmt,
		&rec.SignalsFor, &rec.SignalsAgainst, &rec.Invalidation, &rec.ModelName, &rec.PromptVersion,
		&rec.SchemaVersion, &riskOut, &regime, &quality, &rec.DismissedAt, &translations, &newsAssessment)
	if err != nil {
		return rec, err
	}

	rec.Action = domain.RecommendationAction(action)
	rec.RiskLevel = domain.RiskLevel(riskLevel)
	rec.MarketRegime = domain.MarketRegime(regime)
	rec.DataQuality = domain.DataQualityStatus(quality)

	if rec.ReferencePrice, err = numOut(refPrice); err != nil {
		return rec, err
	}
	if rec.AllocationPct, err = numOut(allocPct); err != nil {
		return rec, err
	}
	decodeRecommendationJSON(&rec, entry, tp, sl, mgmt)
	if len(riskOut) > 0 {
		var out struct {
			Notes []string `json:"notes"`
		}
		if err := json.Unmarshal(riskOut, &out); err == nil {
			rec.RiskEngineNotes = out.Notes
		}
	}
	if len(translations) > 0 {
		if err := json.Unmarshal(translations, &rec.Translations); err != nil {
			return rec, fmt.Errorf("decode recommendation translations: %w", err)
		}
	}
	if len(newsAssessment) > 0 {
		if err := json.Unmarshal(newsAssessment, &rec.NewsAssessment); err != nil {
			return rec, fmt.Errorf("decode recommendation news assessment: %w", err)
		}
	}
	return rec, nil
}

// Get returns one recommendation.
func (r *RecommendationRepository) Get(ctx context.Context, id uuid.UUID) (domain.Recommendation, error) {
	rec, err := scanRecommendation(r.pool.QueryRow(ctx, `SELECT `+recColumns+` FROM recommendations WHERE id = $1`, id))
	if err != nil {
		return rec, mapNoRows(err)
	}
	return rec, nil
}

// ListFilter narrows a recommendation listing.
type ListFilter struct {
	AssetID       *int64
	Symbol        string
	Action        string
	RiskLevel     string
	DataQuality   string
	MinConfidence *int
	MaxConfidence *int
	Visibility    string
	Since         *time.Time
	Until         *time.Time
	Limit         int
	Offset        int
	OnlyOpen      bool
}

// List returns recommendations newest first along with the total row count.
func (r *RecommendationRepository) List(ctx context.Context, f ListFilter) ([]domain.Recommendation, int, error) {
	where := []string{"1=1"}
	args := []any{}
	add := func(clause string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}
	if f.AssetID != nil {
		add("asset_id = $%d", *f.AssetID)
	}
	if f.Symbol != "" {
		add("UPPER(symbol) = UPPER($%d)", f.Symbol)
	}
	if f.Action != "" {
		add("action = $%d", f.Action)
	}
	if f.RiskLevel != "" {
		add("risk_level = $%d", f.RiskLevel)
	}
	if f.DataQuality != "" {
		add("data_quality = $%d", f.DataQuality)
	}
	if f.MinConfidence != nil {
		add("confidence >= $%d", *f.MinConfidence)
	}
	if f.MaxConfidence != nil {
		add("confidence <= $%d", *f.MaxConfidence)
	}
	switch f.Visibility {
	case "all":
	case "dismissed":
		where = append(where, "dismissed_at IS NOT NULL")
	default:
		where = append(where, "dismissed_at IS NULL")
	}
	if f.Since != nil {
		add("created_at >= $%d", f.Since.UTC())
	}
	if f.Until != nil {
		add("created_at <= $%d", f.Until.UTC())
	}
	clause := strings.Join(where, " AND ")

	var total int
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM recommendations WHERE `+clause, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count recommendations: %w", err)
	}

	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	args = append(args, limit, f.Offset)
	q := fmt.Sprintf(`SELECT %s FROM recommendations WHERE %s ORDER BY created_at DESC LIMIT $%d OFFSET $%d`,
		recColumns, clause, len(args)-1, len(args))

	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list recommendations: %w", err)
	}
	defer rows.Close()

	var out []domain.Recommendation
	for rows.Next() {
		rec, err := scanRecommendation(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan recommendation: %w", err)
		}
		out = append(out, rec)
	}
	return out, total, rows.Err()
}

// LatestPerAsset returns the newest recommendation for each asset.
func (r *RecommendationRepository) LatestPerAsset(ctx context.Context) (map[int64]domain.Recommendation, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT ON (asset_id) `+recColumns+`
		FROM recommendations WHERE dismissed_at IS NULL ORDER BY asset_id, created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("latest per asset: %w", err)
	}
	defer rows.Close()

	out := make(map[int64]domain.Recommendation)
	for rows.Next() {
		rec, err := scanRecommendation(rows)
		if err != nil {
			return nil, fmt.Errorf("scan recommendation: %w", err)
		}
		out[rec.AssetID] = rec
	}
	return out, rows.Err()
}

// SetDismissed hides or restores a recommendation without deleting its
// prediction, decision, inference trace, or outcome history.
func (r *RecommendationRepository) SetDismissed(ctx context.Context, id uuid.UUID, dismissed bool) error {
	var dismissedAt *time.Time
	if dismissed {
		now := time.Now().UTC()
		dismissedAt = &now
	}
	result, err := r.pool.Exec(ctx, `UPDATE recommendations SET dismissed_at = $2 WHERE id = $1`, id, dismissedAt)
	if err != nil {
		return fmt.Errorf("set recommendation visibility: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DismissAll hides every currently visible recommendation in one operation.
// Historical rows and all related decisions, outcomes and inference traces stay intact.
func (r *RecommendationRepository) DismissAll(ctx context.Context) (int64, error) {
	result, err := r.pool.Exec(ctx, `
		UPDATE recommendations
		SET dismissed_at = $1
		WHERE dismissed_at IS NULL`, time.Now().UTC())
	if err != nil {
		return 0, fmt.Errorf("dismiss all recommendations: %w", err)
	}
	return result.RowsAffected(), nil
}

// SetDecision records the user's decision without touching the prediction.
func (r *RecommendationRepository) SetDecision(ctx context.Context, d domain.Decision) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO recommendation_decisions (recommendation_id, decision, linked_position_id, decided_at, note)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (recommendation_id) DO UPDATE SET decision = EXCLUDED.decision,
			linked_position_id = EXCLUDED.linked_position_id, decided_at = EXCLUDED.decided_at, note = EXCLUDED.note`,
		d.RecommendationID, string(d.Decision), d.LinkedPositionID, d.DecidedAt.UTC(), d.Note)
	if err != nil {
		return fmt.Errorf("set decision: %w", err)
	}
	return nil
}

// Decisions returns decisions for the given recommendation IDs.
func (r *RecommendationRepository) Decisions(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]domain.Decision, error) {
	out := make(map[uuid.UUID]domain.Decision)
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT recommendation_id, decision, linked_position_id, decided_at, note
		FROM recommendation_decisions WHERE recommendation_id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("query decisions: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var d domain.Decision
		var decision string
		if err := rows.Scan(&d.RecommendationID, &decision, &d.LinkedPositionID, &d.DecidedAt, &d.Note); err != nil {
			return nil, fmt.Errorf("scan decision: %w", err)
		}
		d.Decision = domain.UserDecision(decision)
		out[d.RecommendationID] = d
	}
	return out, rows.Err()
}

// UpsertOutcome writes the market outcome of a recommendation.
func (r *RecommendationRepository) UpsertOutcome(ctx context.Context, o domain.Outcome) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO recommendation_outcomes (recommendation_id, evaluated_at, finalized, price_after_5m,
			price_after_15m, price_after_1h, price_after_4h, price_after_24h, mfe_pct, mae_pct,
			first_tp_hit_index, first_sl_hit_index, status, ambiguous, ambiguity_reason, result)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
		ON CONFLICT (recommendation_id) DO UPDATE SET
			evaluated_at = EXCLUDED.evaluated_at, finalized = EXCLUDED.finalized,
			price_after_5m = COALESCE(EXCLUDED.price_after_5m, recommendation_outcomes.price_after_5m),
			price_after_15m = COALESCE(EXCLUDED.price_after_15m, recommendation_outcomes.price_after_15m),
			price_after_1h = COALESCE(EXCLUDED.price_after_1h, recommendation_outcomes.price_after_1h),
			price_after_4h = COALESCE(EXCLUDED.price_after_4h, recommendation_outcomes.price_after_4h),
			price_after_24h = COALESCE(EXCLUDED.price_after_24h, recommendation_outcomes.price_after_24h),
			mfe_pct = EXCLUDED.mfe_pct, mae_pct = EXCLUDED.mae_pct,
			first_tp_hit_index = EXCLUDED.first_tp_hit_index, first_sl_hit_index = EXCLUDED.first_sl_hit_index,
			status = EXCLUDED.status, ambiguous = EXCLUDED.ambiguous,
			ambiguity_reason = EXCLUDED.ambiguity_reason, result = EXCLUDED.result`,
		o.RecommendationID, o.EvaluatedAt.UTC(), o.Finalized, o.PriceAfter5m, o.PriceAfter15m,
		o.PriceAfter1h, o.PriceAfter4h, o.PriceAfter24h, o.MFEPct, o.MAEPct,
		o.FirstTPHitIndex, o.FirstSLHitIndex, string(o.Status), o.Ambiguous, o.AmbiguityReason,
		nullString(string(o.Result)))
	if err != nil {
		return fmt.Errorf("upsert outcome: %w", err)
	}
	return nil
}

func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Outcomes returns outcomes for the given recommendations.
func (r *RecommendationRepository) Outcomes(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]domain.Outcome, error) {
	out := make(map[uuid.UUID]domain.Outcome)
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT recommendation_id, evaluated_at, finalized, price_after_5m, price_after_15m, price_after_1h,
			price_after_4h, price_after_24h, mfe_pct, mae_pct, first_tp_hit_index, first_sl_hit_index,
			status, ambiguous, ambiguity_reason, result
		FROM recommendation_outcomes WHERE recommendation_id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("query outcomes: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		o, err := scanOutcome(rows)
		if err != nil {
			return nil, err
		}
		out[o.RecommendationID] = o
	}
	return out, rows.Err()
}

func scanOutcome(row pgx.Row) (domain.Outcome, error) {
	var o domain.Outcome
	var status string
	var result *string
	err := row.Scan(&o.RecommendationID, &o.EvaluatedAt, &o.Finalized, &o.PriceAfter5m, &o.PriceAfter15m,
		&o.PriceAfter1h, &o.PriceAfter4h, &o.PriceAfter24h, &o.MFEPct, &o.MAEPct,
		&o.FirstTPHitIndex, &o.FirstSLHitIndex, &status, &o.Ambiguous, &o.AmbiguityReason, &result)
	if err != nil {
		return o, fmt.Errorf("scan outcome: %w", err)
	}
	o.Status = domain.OutcomeStatus(status)
	if result != nil {
		o.Result = domain.TradeResult(*result)
	}
	return o, nil
}

// PendingOutcomes returns recommendations whose outcome is not final yet.
func (r *RecommendationRepository) PendingOutcomes(ctx context.Context, limit int) ([]domain.Recommendation, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+recColumns+` FROM recommendations rec
		WHERE NOT EXISTS (
			SELECT 1 FROM recommendation_outcomes o
			WHERE o.recommendation_id = rec.id AND o.finalized
		)
		ORDER BY rec.created_at ASC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("pending outcomes: %w", err)
	}
	defer rows.Close()

	var out []domain.Recommendation
	for rows.Next() {
		rec, err := scanRecommendation(rows)
		if err != nil {
			return nil, fmt.Errorf("scan recommendation: %w", err)
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// InsertInference stores one LLM call trace.
func (r *RecommendationRepository) InsertInference(ctx context.Context, rec domain.InferenceRecord) error {
	input, err := marshalNullable(rec.Input)
	if err != nil {
		return err
	}
	parsed, err := marshalNullable(rec.ParsedOutput)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO llm_inferences (id, recommendation_id, analysis_run_id, backtest_run_id, symbol, created_at,
			model_name, prompt_version, schema_version, cache_key, llm_input, llm_raw_output, parsed_output, status,
			error_message, repair_attempted, latency_ms, prompt_tokens, completion_tokens)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)`,
		rec.ID, rec.RecommendationID, rec.AnalysisRunID, rec.BacktestRunID, rec.Symbol, rec.CreatedAt.UTC(),
		rec.ModelName, rec.PromptVersion, rec.SchemaVersion, rec.CacheKey, input, rec.RawOutput, parsed, string(rec.Status),
		rec.ErrorMessage, rec.RepairAttempted, rec.LatencyMS, rec.PromptTokens, rec.CompletionTokens)
	if err != nil {
		return fmt.Errorf("insert inference: %w", err)
	}
	return nil
}

// CachedInference looks up a previous successful inference by cache key.
func (r *RecommendationRepository) CachedInference(ctx context.Context, cacheKey string) (string, bool, error) {
	var raw *string
	err := r.pool.QueryRow(ctx, `
		SELECT llm_raw_output FROM llm_inferences
		WHERE cache_key = $1 AND status IN ('ok','repaired','cached') AND llm_raw_output IS NOT NULL
		ORDER BY created_at DESC LIMIT 1`, cacheKey).Scan(&raw)
	if err != nil {
		if errors.Is(mapNoRows(err), ErrNotFound) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("cached inference: %w", err)
	}
	if raw == nil {
		return "", false, nil
	}
	return *raw, true, nil
}

// LastInferenceTime returns when the most recent inference happened.
func (r *RecommendationRepository) LastInferenceTime(ctx context.Context) (time.Time, bool, error) {
	var t time.Time
	err := r.pool.QueryRow(ctx, `SELECT created_at FROM llm_inferences ORDER BY created_at DESC LIMIT 1`).Scan(&t)
	if err != nil {
		if errors.Is(mapNoRows(err), ErrNotFound) {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, fmt.Errorf("last inference time: %w", err)
	}
	return t, true, nil
}

// InferenceContextUsage summarises how much of the model context recent
// inferences really consumed. The numbers come from the server's own usage
// accounting, so the UI can warn before llama.cpp refuses an oversized prompt
// instead of only reporting the refusal afterwards.
func (r *RecommendationRepository) InferenceContextUsage(ctx context.Context, sample int) (domain.InferenceContextUsage, error) {
	if sample <= 0 || sample > 500 {
		sample = 50
	}
	var out domain.InferenceContextUsage
	err := r.pool.QueryRow(ctx, `
		WITH recent AS (
			SELECT created_at, prompt_tokens AS prompt, COALESCE(completion_tokens, 0) AS output
			FROM llm_inferences
			WHERE prompt_tokens IS NOT NULL AND prompt_tokens > 0
			ORDER BY created_at DESC
			LIMIT $1
		)
		SELECT
			(SELECT count(*) FROM recent),
			COALESCE((SELECT prompt FROM recent ORDER BY created_at DESC LIMIT 1), 0),
			COALESCE((SELECT output FROM recent ORDER BY created_at DESC LIMIT 1), 0),
			(SELECT created_at FROM recent ORDER BY created_at DESC LIMIT 1),
			COALESCE((SELECT max(prompt) FROM recent), 0),
			(SELECT created_at FROM recent ORDER BY prompt DESC, created_at DESC LIMIT 1)`, sample).
		Scan(&out.Samples, &out.LastPromptTokens, &out.LastOutputTokens, &out.LastAt,
			&out.PeakPromptTokens, &out.PeakAt)
	if err != nil {
		return domain.InferenceContextUsage{}, fmt.Errorf("inference context usage: %w", err)
	}
	return out, nil
}

// StatRow is one aggregated statistics bucket.
type StatRow struct {
	Bucket string
	Total  int
	Wins   int
	Losses int
	AvgPnL decimal.Decimal
}

// decodeRecommendationJSON fills the JSON-backed fields of a recommendation.
// Malformed JSON in a stored row is skipped rather than failing the whole
// listing: a broken plan must not hide the rest of the history.
func decodeRecommendationJSON(rec *domain.Recommendation, entry, tp, sl, mgmt []byte) {
	if len(entry) > 0 {
		var plan domain.EntryPlan
		if err := json.Unmarshal(entry, &plan); err == nil {
			rec.Entry = &plan
		}
	}
	if len(tp) > 0 {
		_ = json.Unmarshal(tp, &rec.TakeProfit)
	}
	if len(sl) > 0 {
		_ = json.Unmarshal(sl, &rec.StopLoss)
	}
	if len(mgmt) > 0 {
		var plan domain.ManagementPlan
		if err := json.Unmarshal(mgmt, &plan); err == nil {
			rec.Management = &plan
		}
	}
}
