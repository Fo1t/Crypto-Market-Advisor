package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

// PositionRepository stores positions and their append-only history.
type PositionRepository struct{ pool *pgxpool.Pool }

const positionColumns = `id, asset_id, symbol, direction, status, entry_price::text, leverage::text,
	initial_quantity::text, remaining_quantity::text, initial_notional::text, initial_margin::text,
	size_known, opened_at, closed_at, recommendation_id, fee_type, original_plan, current_plan, note,
	created_at, updated_at`

func scanPosition(row pgx.Row) (domain.Position, error) {
	var p domain.Position
	var direction, status, feeType string
	var entryPrice, leverage string
	var qty, remaining, notional, margin *string
	var originalPlan, currentPlan []byte

	err := row.Scan(&p.ID, &p.AssetID, &p.Symbol, &direction, &status, &entryPrice, &leverage,
		&qty, &remaining, &notional, &margin, &p.SizeKnown, &p.OpenedAt, &p.ClosedAt,
		&p.RecommendationID, &feeType, &originalPlan, &currentPlan, &p.Note, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return p, err
	}

	p.Direction = domain.Direction(direction)
	p.Status = domain.PositionStatus(status)
	p.FeeType = domain.FeeType(feeType)

	if p.EntryPrice, err = numOut(entryPrice); err != nil {
		return p, err
	}
	if p.Leverage, err = numOut(leverage); err != nil {
		return p, err
	}
	if p.InitialQuantity, err = numOutPtr(qty); err != nil {
		return p, err
	}
	if p.RemainingQuantity, err = numOutPtr(remaining); err != nil {
		return p, err
	}
	if p.InitialNotional, err = numOutPtr(notional); err != nil {
		return p, err
	}
	if p.InitialMargin, err = numOutPtr(margin); err != nil {
		return p, err
	}
	if len(originalPlan) > 0 {
		p.OriginalPlan = &domain.TradePlan{}
		if err := json.Unmarshal(originalPlan, p.OriginalPlan); err != nil {
			return p, fmt.Errorf("unmarshal original plan: %w", err)
		}
	}
	if len(currentPlan) > 0 {
		p.CurrentPlan = &domain.TradePlan{}
		if err := json.Unmarshal(currentPlan, p.CurrentPlan); err != nil {
			return p, fmt.Errorf("unmarshal current plan: %w", err)
		}
	}
	return p, nil
}

// Create inserts a position together with its opening fill and OPENED event.
func (r *PositionRepository) Create(ctx context.Context, p domain.Position, openFill domain.Fill) error {
	originalPlan, err := marshalNullable(p.OriginalPlan)
	if err != nil {
		return err
	}
	currentPlan, err := marshalNullable(p.CurrentPlan)
	if err != nil {
		return err
	}

	return inTx(ctx, r.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO positions (id, asset_id, symbol, direction, status, entry_price, leverage,
				initial_quantity, remaining_quantity, initial_notional, initial_margin, size_known,
				opened_at, recommendation_id, fee_type, original_plan, current_plan, note)
			VALUES ($1,$2,$3,$4,$5,$6::numeric,$7::numeric,$8::numeric,$9::numeric,$10::numeric,$11::numeric,
				$12,$13,$14,$15,$16,$17,$18)`,
			p.ID, p.AssetID, p.Symbol, string(p.Direction), string(p.Status), numIn(p.EntryPrice), numIn(p.Leverage),
			numInPtr(p.InitialQuantity), numInPtr(p.RemainingQuantity), numInPtr(p.InitialNotional),
			numInPtr(p.InitialMargin), p.SizeKnown, p.OpenedAt.UTC(), p.RecommendationID, string(p.FeeType),
			originalPlan, currentPlan, p.Note); err != nil {
			return fmt.Errorf("insert position: %w", err)
		}
		if err := insertFillTx(ctx, tx, openFill); err != nil {
			return err
		}
		return insertEventTx(ctx, tx, p.ID, domain.EventOpened, map[string]any{
			"entry_price": p.EntryPrice.String(),
			"leverage":    p.Leverage.String(),
			"direction":   string(p.Direction),
		}, p.OpenedAt)
	})
}

func insertFillTx(ctx context.Context, tx pgx.Tx, f domain.Fill) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO position_fills (id, position_id, kind, quantity, close_pct, price, fee, fee_type,
			fee_estimated, realized_pnl, executed_at, note)
		VALUES ($1,$2,$3,$4::numeric,$5::numeric,$6::numeric,$7::numeric,$8,$9,$10::numeric,$11,$12)`,
		f.ID, f.PositionID, string(f.Kind), numInPtr(f.Quantity), numInPtr(f.ClosePct), numIn(f.Price),
		numIn(f.Fee), string(f.FeeType), f.FeeEstimated, numIn(f.RealizedPnL), f.ExecutedAt.UTC(), f.Note)
	if err != nil {
		return fmt.Errorf("insert fill: %w", err)
	}
	return nil
}

func insertEventTx(ctx context.Context, tx pgx.Tx, positionID uuid.UUID, t domain.PositionEventType, payload map[string]any, at time.Time) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal event payload: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO position_events (position_id, event_type, payload, occurred_at)
		VALUES ($1,$2,$3,$4)`, positionID, string(t), raw, at.UTC()); err != nil {
		return fmt.Errorf("insert position event: %w", err)
	}
	return nil
}

// ApplyClose records a closing fill and updates the cached position state
// inside one transaction. History is never rewritten.
func (r *PositionRepository) ApplyClose(ctx context.Context, p domain.Position, fill domain.Fill, eventType domain.PositionEventType, payload map[string]any) error {
	currentPlan, err := marshalNullable(p.CurrentPlan)
	if err != nil {
		return err
	}
	return inTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := insertFillTx(ctx, tx, fill); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE positions SET status = $2, remaining_quantity = $3::numeric, closed_at = $4,
				current_plan = $5, updated_at = NOW()
			WHERE id = $1`,
			p.ID, string(p.Status), numInPtr(p.RemainingQuantity), p.ClosedAt, currentPlan); err != nil {
			return fmt.Errorf("update position: %w", err)
		}
		return insertEventTx(ctx, tx, p.ID, eventType, payload, fill.ExecutedAt)
	})
}

// UpdatePlan replaces the current trade plan and records the change.
func (r *PositionRepository) UpdatePlan(ctx context.Context, id uuid.UUID, plan domain.TradePlan, payload map[string]any) error {
	raw, err := json.Marshal(plan)
	if err != nil {
		return fmt.Errorf("marshal plan: %w", err)
	}
	return inTx(ctx, r.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE positions SET current_plan = $2, updated_at = NOW() WHERE id = $1`, id, raw)
		if err != nil {
			return fmt.Errorf("update plan: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return insertEventTx(ctx, tx, id, domain.EventPlanUpdated, payload, time.Now().UTC())
	})
}

// Get returns one position.
func (r *PositionRepository) Get(ctx context.Context, id uuid.UUID) (domain.Position, error) {
	p, err := scanPosition(r.pool.QueryRow(ctx, `SELECT `+positionColumns+` FROM positions WHERE id = $1`, id))
	if err != nil {
		return p, mapNoRows(err)
	}
	return p, nil
}

// List returns positions, optionally only the open ones.
func (r *PositionRepository) List(ctx context.Context, onlyOpen bool, assetID *int64) ([]domain.Position, error) {
	q := `SELECT ` + positionColumns + ` FROM positions WHERE 1=1`
	args := []any{}
	if onlyOpen {
		q += ` AND status <> 'CLOSED'`
	}
	if assetID != nil {
		args = append(args, *assetID)
		q += fmt.Sprintf(` AND asset_id = $%d`, len(args))
	}
	q += ` ORDER BY opened_at DESC`

	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list positions: %w", err)
	}
	defer rows.Close()

	var out []domain.Position
	for rows.Next() {
		p, err := scanPosition(rows)
		if err != nil {
			return nil, fmt.Errorf("scan position: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Fills returns every fill of a position in execution order.
func (r *PositionRepository) Fills(ctx context.Context, positionID uuid.UUID) ([]domain.Fill, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, position_id, kind, quantity::text, close_pct::text, price::text, fee::text, fee_type,
			fee_estimated, realized_pnl::text, executed_at, created_at, note
		FROM position_fills WHERE position_id = $1 ORDER BY executed_at ASC, created_at ASC`, positionID)
	if err != nil {
		return nil, fmt.Errorf("query fills: %w", err)
	}
	defer rows.Close()

	var out []domain.Fill
	for rows.Next() {
		var f domain.Fill
		var kind, feeType string
		var qty, closePct *string
		var price, fee, realized string
		if err := rows.Scan(&f.ID, &f.PositionID, &kind, &qty, &closePct, &price, &fee, &feeType,
			&f.FeeEstimated, &realized, &f.ExecutedAt, &f.CreatedAt, &f.Note); err != nil {
			return nil, fmt.Errorf("scan fill: %w", err)
		}
		f.Kind = domain.FillKind(kind)
		f.FeeType = domain.FeeType(feeType)
		var err error
		if f.Quantity, err = numOutPtr(qty); err != nil {
			return nil, err
		}
		if f.ClosePct, err = numOutPtr(closePct); err != nil {
			return nil, err
		}
		if f.Price, err = numOut(price); err != nil {
			return nil, err
		}
		if f.Fee, err = numOut(fee); err != nil {
			return nil, err
		}
		if f.RealizedPnL, err = numOut(realized); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Events returns the audit trail of a position.
func (r *PositionRepository) Events(ctx context.Context, positionID uuid.UUID) ([]domain.PositionEvent, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, position_id, event_type, payload, occurred_at, created_at
		FROM position_events WHERE position_id = $1 ORDER BY occurred_at ASC, id ASC`, positionID)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer rows.Close()

	var out []domain.PositionEvent
	for rows.Next() {
		var e domain.PositionEvent
		var eventType string
		var payload []byte
		if err := rows.Scan(&e.ID, &e.PositionID, &eventType, &payload, &e.OccurredAt, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		e.Type = domain.PositionEventType(eventType)
		if len(payload) > 0 {
			_ = json.Unmarshal(payload, &e.Payload)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AddCashEvent stores a manual fee or funding entry.
func (r *PositionRepository) AddCashEvent(ctx context.Context, table string, e domain.CashEvent, eventType domain.PositionEventType) error {
	if table != "fee_events" && table != "funding_events" {
		return fmt.Errorf("unsupported cash event table %q", table)
	}
	return inTx(ctx, r.pool, func(tx pgx.Tx) error {
		var err error
		if table == "fee_events" {
			_, err = tx.Exec(ctx, `
				INSERT INTO fee_events (id, position_id, amount, fee_type, occurred_at, note)
				VALUES ($1,$2,$3::numeric,$4,$5,$6)`,
				e.ID, e.PositionID, numIn(e.Amount), string(e.FeeType), e.OccurredAt.UTC(), e.Note)
		} else {
			_, err = tx.Exec(ctx, `
				INSERT INTO funding_events (id, position_id, amount, occurred_at, note)
				VALUES ($1,$2,$3::numeric,$4,$5)`,
				e.ID, e.PositionID, numIn(e.Amount), e.OccurredAt.UTC(), e.Note)
		}
		if err != nil {
			return fmt.Errorf("insert cash event: %w", err)
		}
		return insertEventTx(ctx, tx, e.PositionID, eventType, map[string]any{
			"amount": e.Amount.String(),
			"note":   e.Note,
		}, e.OccurredAt)
	})
}

// CashEvents returns manual fee or funding entries for a position.
func (r *PositionRepository) CashEvents(ctx context.Context, table string, positionID uuid.UUID) ([]domain.CashEvent, error) {
	var q string
	switch table {
	case "fee_events":
		q = `SELECT id, position_id, amount::text, fee_type, occurred_at, note, created_at FROM fee_events WHERE position_id = $1 ORDER BY occurred_at`
	case "funding_events":
		q = `SELECT id, position_id, amount::text, '' AS fee_type, occurred_at, note, created_at FROM funding_events WHERE position_id = $1 ORDER BY occurred_at`
	default:
		return nil, fmt.Errorf("unsupported cash event table %q", table)
	}

	rows, err := r.pool.Query(ctx, q, positionID)
	if err != nil {
		return nil, fmt.Errorf("query cash events: %w", err)
	}
	defer rows.Close()

	var out []domain.CashEvent
	for rows.Next() {
		var e domain.CashEvent
		var amount, feeType string
		if err := rows.Scan(&e.ID, &e.PositionID, &amount, &feeType, &e.OccurredAt, &e.Note, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan cash event: %w", err)
		}
		if e.Amount, err = numOut(amount); err != nil {
			return nil, err
		}
		e.FeeType = domain.FeeType(feeType)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Delete removes a position and its history.
func (r *PositionRepository) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM positions WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete position: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
