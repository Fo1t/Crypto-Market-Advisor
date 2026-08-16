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

// BacktestRepository stores backtest runs and their simulated trades.
type BacktestRepository struct{ pool *pgxpool.Pool }

const backtestColumns = `id, mode, symbol, asset_id, timeframe, date_from, date_to, analysis_interval,
	status, params, metrics, estimated_steps, completed_steps, error_message, started_at, finished_at, created_at`

func scanRun(row pgx.Row) (domain.BacktestRun, error) {
	var r domain.BacktestRun
	var mode, timeframe, status string
	var params, metrics []byte

	err := row.Scan(&r.ID, &mode, &r.Symbol, &r.AssetID, &timeframe, &r.DateFrom, &r.DateTo, &r.Interval,
		&status, &params, &metrics, &r.EstimatedSteps, &r.CompletedSteps, &r.ErrorMessage,
		&r.StartedAt, &r.FinishedAt, &r.CreatedAt)
	if err != nil {
		return r, err
	}
	r.Mode = domain.BacktestMode(mode)
	r.Timeframe = domain.Timeframe(timeframe)
	r.Status = domain.BacktestStatus(status)
	if len(params) > 0 {
		if err := json.Unmarshal(params, &r.Params); err != nil {
			return r, fmt.Errorf("unmarshal backtest params: %w", err)
		}
	}
	if len(metrics) > 0 {
		r.Metrics = &domain.BacktestMetrics{}
		if err := json.Unmarshal(metrics, r.Metrics); err != nil {
			return r, fmt.Errorf("unmarshal backtest metrics: %w", err)
		}
	}
	return r, nil
}

// Create stores a new backtest run.
func (r *BacktestRepository) Create(ctx context.Context, run domain.BacktestRun) error {
	params, err := json.Marshal(run.Params)
	if err != nil {
		return fmt.Errorf("marshal params: %w", err)
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO backtest_runs (id, mode, symbol, asset_id, timeframe, date_from, date_to,
			analysis_interval, status, params, estimated_steps)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		run.ID, string(run.Mode), run.Symbol, run.AssetID, string(run.Timeframe), run.DateFrom.UTC(),
		run.DateTo.UTC(), run.Interval, string(run.Status), params, run.EstimatedSteps)
	if err != nil {
		return fmt.Errorf("create backtest run: %w", err)
	}
	return nil
}

// UpdateProgress records how far a running backtest has progressed.
func (r *BacktestRepository) UpdateProgress(ctx context.Context, id uuid.UUID, completed int) error {
	_, err := r.pool.Exec(ctx, `UPDATE backtest_runs SET completed_steps = $2 WHERE id = $1`, id, completed)
	if err != nil {
		return fmt.Errorf("update backtest progress: %w", err)
	}
	return nil
}

// SetStatus transitions a run, optionally recording metrics and an error.
func (r *BacktestRepository) SetStatus(ctx context.Context, id uuid.UUID, status domain.BacktestStatus, metrics *domain.BacktestMetrics, errMsg string) error {
	var raw []byte
	if metrics != nil {
		var err error
		if raw, err = json.Marshal(metrics); err != nil {
			return fmt.Errorf("marshal metrics: %w", err)
		}
	}
	var started, finished *time.Time
	now := time.Now().UTC()
	switch status {
	case domain.BacktestRunning:
		started = &now
	case domain.BacktestCompleted, domain.BacktestFailed, domain.BacktestCanceled:
		finished = &now
	}

	_, err := r.pool.Exec(ctx, `
		UPDATE backtest_runs SET status = $2, metrics = COALESCE($3, metrics), error_message = $4,
			started_at = COALESCE($5, started_at), finished_at = COALESCE($6, finished_at)
		WHERE id = $1`, id, string(status), raw, errMsg, started, finished)
	if err != nil {
		return fmt.Errorf("set backtest status: %w", err)
	}
	return nil
}

// Get returns one backtest run.
func (r *BacktestRepository) Get(ctx context.Context, id uuid.UUID) (domain.BacktestRun, error) {
	run, err := scanRun(r.pool.QueryRow(ctx, `SELECT `+backtestColumns+` FROM backtest_runs WHERE id = $1 AND deleted_at IS NULL`, id))
	if err != nil {
		return run, mapNoRows(err)
	}
	return run, nil
}

// BacktestFilter narrows the list of runs. Every field is optional; an empty
// filter matches everything that has not been hidden.
type BacktestFilter struct {
	Mode      string
	Symbol    string
	Status    string
	Timeframe string
}

// where builds the shared condition of listing and bulk hiding, so the two can
// never disagree about what "the runs I am looking at" means.
func (f BacktestFilter) where(start int) (string, []any) {
	clause := " WHERE deleted_at IS NULL"
	args := make([]any, 0, 4)
	add := func(column, value string) {
		if value == "" {
			return
		}
		args = append(args, value)
		clause += fmt.Sprintf(" AND %s = $%d", column, start+len(args)-1)
	}
	add("mode", f.Mode)
	add("symbol", f.Symbol)
	add("status", f.Status)
	add("timeframe", f.Timeframe)
	return clause, args
}

// List returns backtest runs newest first, together with the total number of
// runs the filter matches.
func (r *BacktestRepository) List(ctx context.Context, filter BacktestFilter, limit, offset int) ([]domain.BacktestRun, int, error) {
	clause, args := filter.where(1)

	var total int
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM backtest_runs`+clause, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count backtest runs: %w", err)
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+backtestColumns+` FROM backtest_runs`+clause+
			fmt.Sprintf(` ORDER BY created_at DESC LIMIT $%d OFFSET $%d`, len(args)+1, len(args)+2),
		append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list backtest runs: %w", err)
	}
	defer rows.Close()

	var out []domain.BacktestRun
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan backtest run: %w", err)
		}
		out = append(out, run)
	}
	return out, total, rows.Err()
}

// SoftDeleteMatching hides every finished run the filter selects and reports how
// many were hidden.
//
// A run that is still pending or running is left alone: hiding something that is
// about to write its own results would look like a lost run rather than a tidy
// list. Everything hidden keeps its parameters, metrics and trades.
func (r *BacktestRepository) SoftDeleteMatching(ctx context.Context, filter BacktestFilter) (int, error) {
	clause, args := filter.where(1)
	tag, err := r.pool.Exec(ctx,
		`UPDATE backtest_runs SET deleted_at = now()`+clause+
			` AND status NOT IN ('pending', 'running')`, args...)
	if err != nil {
		return 0, fmt.Errorf("hide backtest runs: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// HiddenStats reports how much a purge would free.
type HiddenStats struct {
	Runs   int    `json:"runs"`
	Trades int    `json:"trades"`
	Bytes  int64  `json:"bytes"`
	Size   string `json:"size"`
}

// HiddenSummary counts what has been hidden and estimates its share of the
// stored trades, so the purge can say what it is about to remove rather than
// asking for blind confirmation.
func (r *BacktestRepository) HiddenSummary(ctx context.Context) (HiddenStats, error) {
	var out HiddenStats
	err := r.pool.QueryRow(ctx, `
        WITH hidden AS (SELECT id FROM backtest_runs WHERE deleted_at IS NOT NULL),
             trades AS (SELECT count(*) AS n FROM backtest_trades WHERE run_id IN (SELECT id FROM hidden))
        SELECT (SELECT count(*) FROM hidden), (SELECT n FROM trades),
               (SELECT n FROM trades) * (
                   SELECT GREATEST(1, pg_total_relation_size('backtest_trades') /
                          GREATEST(1, (SELECT count(*) FROM backtest_trades)))
               )`).Scan(&out.Runs, &out.Trades, &out.Bytes)
	if err != nil {
		return out, fmt.Errorf("hidden backtest summary: %w", err)
	}
	out.Size = humanBytes(out.Bytes)
	return out, nil
}

// humanBytes renders a byte count the way a person reads it.
func humanBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	value, exp := float64(bytes)/unit, 0
	for value >= unit && exp < 3 {
		value /= unit
		exp++
	}
	return fmt.Sprintf("%.1f %s", value, []string{"KB", "MB", "GB", "TB"}[exp])
}

// Purge permanently removes every hidden run together with its trades, which is
// the only operation here that actually frees space: hiding a run leaves all of
// it in the database, and a few thousand replays add up to hundreds of megabytes
// of simulated trades.
//
// It deliberately touches only what was already hidden. Deleting straight from
// the visible list would make one mis-click irreversible.
func (r *BacktestRepository) Purge(ctx context.Context) (int, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM backtest_runs WHERE deleted_at IS NOT NULL`)
	if err != nil {
		return 0, fmt.Errorf("purge backtest runs: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// SoftDelete hides a finished run while retaining its parameters, metrics and
// simulated trades for auditability. Active runs must be canceled first.
func (r *BacktestRepository) SoftDelete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE backtest_runs
		SET deleted_at = NOW()
		WHERE id = $1 AND deleted_at IS NULL AND status NOT IN ('pending', 'running')`, id)
	if err != nil {
		return fmt.Errorf("soft-delete backtest run: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// CleanupInterrupted retires runs that were still pending or running when the
// process stopped. Nothing can resume them: trades and metrics are written only
// at the end of a run, so an interrupted row holds no result. They are marked
// canceled with the reason and hidden the same way a user-deleted run is, which
// keeps the audit row without leaving a permanently "running" entry that cannot
// even be removed from the UI.
func (r *BacktestRepository) CleanupInterrupted(ctx context.Context) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE backtest_runs
		SET status = 'canceled',
			error_message = 'interrupted by a backend restart',
			finished_at = COALESCE(finished_at, NOW()),
			deleted_at = NOW()
		WHERE status IN ('pending', 'running') AND deleted_at IS NULL`)
	if err != nil {
		return 0, fmt.Errorf("clean up interrupted backtest runs: %w", err)
	}
	return tag.RowsAffected(), nil
}

// SaveEquityCurve stores the downsampled account-value curve of a finished run.
// It lives in its own column because the run listing does not need it.
func (r *BacktestRepository) SaveEquityCurve(ctx context.Context, id uuid.UUID, curve []domain.EquityPoint) error {
	if len(curve) == 0 {
		return nil
	}
	raw, err := json.Marshal(curve)
	if err != nil {
		return fmt.Errorf("marshal equity curve: %w", err)
	}
	if _, err := r.pool.Exec(ctx,
		`UPDATE backtest_runs SET equity_curve = $2 WHERE id = $1`, id, raw); err != nil {
		return fmt.Errorf("store equity curve: %w", err)
	}
	return nil
}

// EquityCurve returns the stored curve of a run, or nil when it has none.
func (r *BacktestRepository) EquityCurve(ctx context.Context, id uuid.UUID) ([]domain.EquityPoint, error) {
	var raw []byte
	if err := r.pool.QueryRow(ctx,
		`SELECT equity_curve FROM backtest_runs WHERE id = $1 AND deleted_at IS NULL`, id).Scan(&raw); err != nil {
		return nil, mapNoRows(err)
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var curve []domain.EquityPoint
	if err := json.Unmarshal(raw, &curve); err != nil {
		return nil, fmt.Errorf("decode equity curve: %w", err)
	}
	return curve, nil
}

// InsertTrades stores the simulated trades of a run.
func (r *BacktestRepository) InsertTrades(ctx context.Context, trades []domain.BacktestTrade) error {
	if len(trades) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, t := range trades {
		details, err := json.Marshal(map[string]any{
			"executions":     t.Executions,
			"strategy_votes": t.StrategyVotes,
		})
		if err != nil {
			return fmt.Errorf("marshal backtest trade details: %w", err)
		}
		batch.Queue(`
			INSERT INTO backtest_trades (id, run_id, symbol, direction, opened_at, closed_at, entry_price,
				exit_price, quantity, leverage, allocation_pct, gross_pnl, fees, funding, net_pnl, pnl_pct,
				mfe_pct, mae_pct, exit_reason, confidence, details)
			VALUES ($1,$2,$3,$4,$5,$6,$7::numeric,$8::numeric,$9::numeric,$10::numeric,$11::numeric,
				$12::numeric,$13::numeric,$14::numeric,$15::numeric,$16,$17,$18,$19,$20,$21)`,
			t.ID, t.RunID, t.Symbol, string(t.Direction), t.OpenedAt.UTC(), t.ClosedAt, numIn(t.EntryPrice),
			numInPtr(t.ExitPrice), numIn(t.Quantity), numIn(t.Leverage), numIn(t.AllocationPct),
			numIn(t.GrossPnL), numIn(t.Fees), numIn(t.Funding), numIn(t.NetPnL), t.PnLPct,
			t.MFEPct, t.MAEPct, t.ExitReason, t.Confidence, details)
	}
	br := r.pool.SendBatch(ctx, batch)
	defer func() { _ = br.Close() }()
	for range trades {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("insert backtest trade: %w", err)
		}
	}
	return nil
}

// Trades returns the trades of a run.
func (r *BacktestRepository) Trades(ctx context.Context, runID uuid.UUID) ([]domain.BacktestTrade, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, run_id, symbol, direction, opened_at, closed_at, entry_price::text, exit_price::text,
			quantity::text, leverage::text, allocation_pct::text, gross_pnl::text, fees::text, funding::text,
			net_pnl::text, pnl_pct, mfe_pct, mae_pct, exit_reason, confidence, details
		FROM backtest_trades WHERE run_id = $1 ORDER BY opened_at ASC`, runID)
	if err != nil {
		return nil, fmt.Errorf("query backtest trades: %w", err)
	}
	defer rows.Close()

	var out []domain.BacktestTrade
	for rows.Next() {
		var t domain.BacktestTrade
		var direction string
		var entry, qty, lev, alloc, gross, fees, funding, net string
		var exit *string
		var details []byte
		if err := rows.Scan(&t.ID, &t.RunID, &t.Symbol, &direction, &t.OpenedAt, &t.ClosedAt, &entry, &exit,
			&qty, &lev, &alloc, &gross, &fees, &funding, &net, &t.PnLPct, &t.MFEPct, &t.MAEPct,
			&t.ExitReason, &t.Confidence, &details); err != nil {
			return nil, fmt.Errorf("scan backtest trade: %w", err)
		}
		t.Direction = domain.Direction(direction)
		var err error
		if t.EntryPrice, err = numOut(entry); err != nil {
			return nil, err
		}
		if t.ExitPrice, err = numOutPtr(exit); err != nil {
			return nil, err
		}
		if t.Quantity, err = numOut(qty); err != nil {
			return nil, err
		}
		if t.Leverage, err = numOut(lev); err != nil {
			return nil, err
		}
		if t.AllocationPct, err = numOut(alloc); err != nil {
			return nil, err
		}
		if t.GrossPnL, err = numOut(gross); err != nil {
			return nil, err
		}
		if t.Fees, err = numOut(fees); err != nil {
			return nil, err
		}
		if t.Funding, err = numOut(funding); err != nil {
			return nil, err
		}
		if t.NetPnL, err = numOut(net); err != nil {
			return nil, err
		}
		if len(details) > 0 {
			var payload struct {
				Executions    []domain.BacktestExecution `json:"executions"`
				StrategyVotes []domain.StrategyVote      `json:"strategy_votes"`
			}
			if err := json.Unmarshal(details, &payload); err != nil {
				return nil, fmt.Errorf("unmarshal backtest trade details: %w", err)
			}
			t.Executions = payload.Executions
			t.StrategyVotes = payload.StrategyVotes
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
