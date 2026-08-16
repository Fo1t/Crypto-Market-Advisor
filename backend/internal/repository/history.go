package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

// RecordWithOutcome joins a prediction with what actually happened and with the
// user's decision. The three stay separate structs on purpose: a prediction is
// never edited once the outcome is known.
type RecordWithOutcome struct {
	Recommendation domain.Recommendation
	Outcome        *domain.Outcome
	Decision       *domain.Decision
	Position       *ClosedTrade
}

// ClosedTrade is the realised result of a user position.
type ClosedTrade struct {
	PositionID   string
	Symbol       string
	Direction    domain.Direction
	OpenedAt     time.Time
	ClosedAt     time.Time
	NetPnL       string
	NetPnLPct    *float64
	HoldMinutes  int
	Leverage     string
	Result       domain.TradeResult
	SizeKnown    bool
	Confidence   *int
	MarketRegime string
}

// HistoryFilter narrows a history query.
type HistoryFilter struct {
	Symbol string
	Since  *time.Time
	// Before excludes everything predicted at or after that moment. A replay
	// standing on a past bar must not be shown the record of the predictions
	// that came later.
	Before *time.Time
	Limit  int
}

// RecordsWithOutcomes returns recent predictions joined with their outcomes and
// decisions, newest first.
func (r *RecommendationRepository) RecordsWithOutcomes(ctx context.Context, f HistoryFilter) ([]RecordWithOutcome, error) {
	limit := f.Limit
	if limit <= 0 || limit > 2000 {
		limit = 500
	}

	args := []any{limit}
	where := "1=1"
	if f.Symbol != "" {
		args = append(args, f.Symbol)
		where += fmt.Sprintf(" AND UPPER(rec.symbol) = UPPER($%d)", len(args))
	}
	if f.Since != nil {
		args = append(args, f.Since.UTC())
		where += fmt.Sprintf(" AND rec.created_at >= $%d", len(args))
	}
	if f.Before != nil {
		args = append(args, f.Before.UTC())
		where += fmt.Sprintf(" AND rec.created_at < $%d", len(args))
	}

	q := `
		SELECT rec.id, rec.analysis_run_id, rec.asset_id, rec.symbol, rec.created_at, rec.action,
			rec.confidence, rec.risk_level, rec.summary, rec.reference_price::text, rec.allocation_pct::text,
			rec.llm_leverage, rec.risk_max_leverage, rec.final_leverage, rec.leverage_reason, rec.entry,
			rec.take_profit, rec.stop_loss, rec.management, rec.signals_for, rec.signals_against,
			rec.invalidation, rec.model_name, rec.prompt_version, rec.schema_version, rec.risk_engine_output,
			rec.market_regime, rec.data_quality, rec.translations,
			o.recommendation_id, o.evaluated_at, o.finalized, o.price_after_5m, o.price_after_15m,
			o.price_after_1h, o.price_after_4h, o.price_after_24h, o.mfe_pct, o.mae_pct,
			o.first_tp_hit_index, o.first_sl_hit_index, o.status, o.ambiguous, o.ambiguity_reason, o.result,
			d.decision, d.linked_position_id, d.decided_at
		FROM recommendations rec
		LEFT JOIN recommendation_outcomes o ON o.recommendation_id = rec.id
		LEFT JOIN recommendation_decisions d ON d.recommendation_id = rec.id
		WHERE ` + where + `
		ORDER BY rec.created_at DESC
		LIMIT $1`

	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("records with outcomes: %w", err)
	}
	defer rows.Close()

	var out []RecordWithOutcome
	for rows.Next() {
		var rec domain.Recommendation
		var refPrice, allocPct string
		var action, riskLevel, regime, quality string
		var entry, tp, sl, mgmt, riskOut, translations []byte

		var outcomeID *string
		var evaluatedAt *time.Time
		var finalized *bool
		var p5, p15, p1h, p4h, p24h, mfe, mae *float64
		var tpIdx, slIdx *int
		var outcomeStatus, ambiguityReason, outcomeResult *string
		var ambiguous *bool

		var decision *string
		var linkedPosition *string
		var decidedAt *time.Time

		if err := rows.Scan(&rec.ID, &rec.AnalysisRunID, &rec.AssetID, &rec.Symbol, &rec.CreatedAt, &action,
			&rec.Confidence, &riskLevel, &rec.Summary, &refPrice, &allocPct, &rec.Leverage.LLMSuggested,
			&rec.Leverage.RiskMaximum, &rec.Leverage.Recommended, &rec.Leverage.Reason, &entry, &tp, &sl, &mgmt,
			&rec.SignalsFor, &rec.SignalsAgainst, &rec.Invalidation, &rec.ModelName, &rec.PromptVersion,
			&rec.SchemaVersion, &riskOut, &regime, &quality, &translations,
			&outcomeID, &evaluatedAt, &finalized, &p5, &p15, &p1h, &p4h, &p24h, &mfe, &mae,
			&tpIdx, &slIdx, &outcomeStatus, &ambiguous, &ambiguityReason, &outcomeResult,
			&decision, &linkedPosition, &decidedAt); err != nil {
			return nil, fmt.Errorf("scan history row: %w", err)
		}

		rec.Action = domain.RecommendationAction(action)
		rec.RiskLevel = domain.RiskLevel(riskLevel)
		rec.MarketRegime = domain.MarketRegime(regime)
		rec.DataQuality = domain.DataQualityStatus(quality)

		var err error
		if rec.ReferencePrice, err = numOut(refPrice); err != nil {
			return nil, err
		}
		if rec.AllocationPct, err = numOut(allocPct); err != nil {
			return nil, err
		}
		decodeRecommendationJSON(&rec, entry, tp, sl, mgmt)
		if len(translations) > 0 {
			if err := json.Unmarshal(translations, &rec.Translations); err != nil {
				return nil, fmt.Errorf("decode recommendation translations: %w", err)
			}
		}

		item := RecordWithOutcome{Recommendation: rec}
		if outcomeID != nil {
			o := domain.Outcome{
				RecommendationID: rec.ID,
				PriceAfter5m:     p5, PriceAfter15m: p15, PriceAfter1h: p1h,
				PriceAfter4h: p4h, PriceAfter24h: p24h, MFEPct: mfe, MAEPct: mae,
				FirstTPHitIndex: tpIdx, FirstSLHitIndex: slIdx,
			}
			if evaluatedAt != nil {
				o.EvaluatedAt = *evaluatedAt
			}
			if finalized != nil {
				o.Finalized = *finalized
			}
			if outcomeStatus != nil {
				o.Status = domain.OutcomeStatus(*outcomeStatus)
			}
			if ambiguous != nil {
				o.Ambiguous = *ambiguous
			}
			if ambiguityReason != nil {
				o.AmbiguityReason = *ambiguityReason
			}
			if outcomeResult != nil {
				o.Result = domain.TradeResult(*outcomeResult)
			}
			item.Outcome = &o
		}
		if decision != nil {
			d := domain.Decision{RecommendationID: rec.ID, Decision: domain.UserDecision(*decision)}
			if decidedAt != nil {
				d.DecidedAt = *decidedAt
			}
			item.Decision = &d
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// ClosedTrades returns finished user positions with their realised result,
// computed from the append-only fill history rather than from cached columns.
// before, when set, hides trades that had not finished by that moment, which is
// what lets a replay read the track record as it stood on the bar it is on.
func (r *PositionRepository) ClosedTrades(ctx context.Context, since, before *time.Time, limit int) ([]ClosedTrade, error) {
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	args := []any{limit}
	where := "p.status = 'CLOSED'"
	if since != nil {
		args = append(args, since.UTC())
		where += fmt.Sprintf(" AND p.closed_at >= $%d", len(args))
	}
	if before != nil {
		args = append(args, before.UTC())
		where += fmt.Sprintf(" AND p.closed_at < $%d", len(args))
	}

	q := `
		SELECT p.id::text, p.symbol, p.direction, p.opened_at, p.closed_at, p.leverage::text, p.size_known,
			p.initial_margin::text,
			COALESCE(SUM(f.realized_pnl), 0)::text AS gross,
			COALESCE(SUM(f.fee), 0)::text AS fees,
			COALESCE((SELECT SUM(amount) FROM funding_events fe WHERE fe.position_id = p.id), 0)::text AS funding,
			COALESCE((SELECT SUM(amount) FROM fee_events ee WHERE ee.position_id = p.id), 0)::text AS extra_fees,
			r.confidence, r.market_regime
		FROM positions p
		LEFT JOIN position_fills f ON f.position_id = p.id
		LEFT JOIN recommendations r ON r.id = p.recommendation_id
		WHERE ` + where + `
		GROUP BY p.id, r.confidence, r.market_regime
		ORDER BY p.closed_at DESC
		LIMIT $1`

	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("closed trades: %w", err)
	}
	defer rows.Close()

	var out []ClosedTrade
	for rows.Next() {
		var t ClosedTrade
		var direction string
		var closedAt *time.Time
		var leverage, gross, fees, funding, extraFees string
		var margin *string
		var regime *string

		if err := rows.Scan(&t.PositionID, &t.Symbol, &direction, &t.OpenedAt, &closedAt, &leverage,
			&t.SizeKnown, &margin, &gross, &fees, &funding, &extraFees, &t.Confidence, &regime); err != nil {
			return nil, fmt.Errorf("scan closed trade: %w", err)
		}
		t.Direction = domain.Direction(direction)
		t.Leverage = leverage
		if closedAt != nil {
			t.ClosedAt = *closedAt
			t.HoldMinutes = int(closedAt.Sub(t.OpenedAt).Minutes())
		}
		if regime != nil {
			t.MarketRegime = *regime
		}

		grossD, err := numOut(gross)
		if err != nil {
			return nil, err
		}
		feesD, err := numOut(fees)
		if err != nil {
			return nil, err
		}
		fundingD, err := numOut(funding)
		if err != nil {
			return nil, err
		}
		extraD, err := numOut(extraFees)
		if err != nil {
			return nil, err
		}

		net := grossD.Sub(feesD).Sub(extraD).Add(fundingD)
		t.NetPnL = net.String()
		t.Result = classifyTrade(net.String())

		if marginD, err := numOutPtr(margin); err == nil && marginD != nil && marginD.IsPositive() {
			pct, _ := net.Div(*marginD).Mul(hundredDecimal()).Float64()
			t.NetPnLPct = &pct
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
