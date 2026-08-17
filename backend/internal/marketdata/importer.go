package marketdata

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

// Import job states, as the UI reads them.
const (
	// ImportIdle is reported when no import has been requested yet.
	ImportIdle      = "idle"
	ImportRunning   = "running"
	ImportCompleted = "completed"
	ImportFailed    = "failed"
	ImportCanceled  = "canceled"
)

// ErrImportRunning reports a second import requested while one is in flight.
// Only one runs at a time: the public endpoints are rate limited, and two jobs
// competing for the same budget would finish later than the same work in order.
var ErrImportRunning = errors.New("a historical import is already running")

// ImportItem is the outcome for one asset and timeframe.
type ImportItem struct {
	Symbol    string `json:"symbol"`
	Timeframe string `json:"timeframe"`
	Candles   int    `json:"candles"`
	Error     string `json:"error,omitempty"`
}

// ImportProgress is the whole job as the UI polls it.
type ImportProgress struct {
	ID         string       `json:"id"`
	Status     string       `json:"status"`
	StartedAt  time.Time    `json:"started_at"`
	FinishedAt *time.Time   `json:"finished_at,omitempty"`
	From       time.Time    `json:"from"`
	To         time.Time    `json:"to"`
	Symbols    []string     `json:"symbols"`
	Timeframes []string     `json:"timeframes"`
	Total      int          `json:"total"`
	Completed  int          `json:"completed"`
	Current    string       `json:"current,omitempty"`
	Candles    int          `json:"candles"`
	Items      []ImportItem `json:"items"`
	Error      string       `json:"error,omitempty"`
}

// StartImport begins downloading an explicit window of history in the
// background and returns the job as it stands at that moment.
//
// The work is detached from the request that asked for it: several assets over
// several years take far longer than any sane HTTP timeout, and a job the
// browser can lose by navigating away is not a download the user can trust.
func (s *Service) StartImport(assets []domain.Asset, timeframes []domain.Timeframe, from, to time.Time) (ImportProgress, error) {
	if len(assets) == 0 || len(timeframes) == 0 {
		return ImportProgress{}, errors.New("at least one asset and one timeframe are required")
	}

	s.importMu.Lock()
	defer s.importMu.Unlock()
	if s.importJob != nil && s.importJob.Status == ImportRunning {
		return ImportProgress{}, ErrImportRunning
	}

	job := &ImportProgress{
		ID:        uuid.NewString(),
		Status:    ImportRunning,
		StartedAt: time.Now().UTC(),
		From:      from.UTC(),
		To:        to.UTC(),
		Total:     len(assets) * len(timeframes),
		Items:     []ImportItem{},
	}
	for _, asset := range assets {
		job.Symbols = append(job.Symbols, asset.Symbol)
	}
	for _, tf := range timeframes {
		job.Timeframes = append(job.Timeframes, string(tf))
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.importJob, s.importCancel = job, cancel
	go s.runImport(ctx, assets, timeframes, job.From, job.To)

	return snapshotImport(job), nil
}

// ImportStatus returns the current or last finished job.
func (s *Service) ImportStatus() ImportProgress {
	s.importMu.Lock()
	defer s.importMu.Unlock()
	if s.importJob == nil {
		return ImportProgress{Status: ImportIdle, Symbols: []string{}, Timeframes: []string{}, Items: []ImportItem{}}
	}
	return snapshotImport(s.importJob)
}

// CancelImport stops a running import. What was already stored stays stored:
// candles are upserted per timeframe, so a cancelled job leaves a smaller
// window rather than a corrupt one.
func (s *Service) CancelImport() bool {
	s.importMu.Lock()
	defer s.importMu.Unlock()
	if s.importJob == nil || s.importJob.Status != ImportRunning || s.importCancel == nil {
		return false
	}
	s.importCancel()
	return true
}

func (s *Service) runImport(ctx context.Context, assets []domain.Asset, timeframes []domain.Timeframe, from, to time.Time) {
	failures := 0
	for _, asset := range assets {
		for _, tf := range timeframes {
			if ctx.Err() != nil {
				s.finishImport(ImportCanceled, "")
				return
			}
			s.updateImport(func(job *ImportProgress) {
				job.Current = asset.Symbol + " " + string(tf)
			})

			count, err := s.ImportCandles(ctx, asset, tf, from, to)
			item := ImportItem{Symbol: asset.Symbol, Timeframe: string(tf), Candles: count}
			if err != nil {
				// One unlisted asset or one refused timeframe must not abort the
				// rest of the job: the user asked for a set, and the answer is
				// per member of it.
				item.Error = err.Error()
				failures++
				s.log.Warn("history import failed",
					slog.String("symbol", asset.Symbol),
					slog.String("timeframe", string(tf)),
					slog.String("error", err.Error()))
			}
			s.updateImport(func(job *ImportProgress) {
				job.Items = append(job.Items, item)
				job.Completed++
				job.Candles += count
			})
		}
	}

	if ctx.Err() != nil {
		s.finishImport(ImportCanceled, "")
		return
	}
	if failures > 0 && failures == len(assets)*len(timeframes) {
		s.finishImport(ImportFailed, "no candles could be downloaded; see the per-asset errors")
		return
	}
	s.finishImport(ImportCompleted, "")
}

func (s *Service) updateImport(apply func(*ImportProgress)) {
	s.importMu.Lock()
	defer s.importMu.Unlock()
	if s.importJob != nil {
		apply(s.importJob)
	}
}

func (s *Service) finishImport(status, message string) {
	now := time.Now().UTC()
	s.updateImport(func(job *ImportProgress) {
		job.Status, job.FinishedAt, job.Current, job.Error = status, &now, "", message
	})
	s.log.Info("history import finished", slog.String("status", status))
}

// snapshotImport copies the job so a reader never observes a slice the worker
// is still appending to.
func snapshotImport(job *ImportProgress) ImportProgress {
	out := *job
	out.Symbols = append([]string(nil), job.Symbols...)
	out.Timeframes = append([]string(nil), job.Timeframes...)
	out.Items = append([]ImportItem(nil), job.Items...)
	if job.FinishedAt != nil {
		finished := *job.FinishedAt
		out.FinishedAt = &finished
	}
	return out
}

// ImportCandles downloads one asset's bars for an explicit window and stores
// them.
//
// It differs from BackfillCandles in what it is for. The scheduled backfill
// advances from the newest stored bar within a fixed per-timeframe window; it
// is how the history stays current. An import is the user naming a period —
// typically one older or finer than the automatic window ever reaches — and
// getting exactly that. Bars are upserted, so importing a period twice is
// harmless and re-importing repairs a range that was fetched while the
// exchange was returning gaps.
func (s *Service) ImportCandles(ctx context.Context, asset domain.Asset, tf domain.Timeframe, from, to time.Time) (int, error) {
	if s.bybit == nil || asset.BybitSymbol == "" {
		return 0, fmt.Errorf("%s: %w", asset.Symbol, ErrNotTradable)
	}
	fetch, err := s.klineFetcher(ctx, asset)
	if err != nil {
		return 0, err
	}
	candles, err := fetch(tf, from.UTC(), to.UTC())
	if err != nil {
		return 0, err
	}
	// A forming bar is not history: storing it would put a partial candle into
	// the same series every analysis and backtest treats as settled.
	closed := ClosedOnly(candles)
	if len(closed) == 0 {
		return 0, nil
	}
	if err := s.repos.Candles.UpsertMany(ctx, asset.ID, tf, closed); err != nil {
		return 0, fmt.Errorf("store %s candles: %w", tf, err)
	}
	s.log.Debug("history imported",
		slog.String("symbol", asset.Symbol),
		slog.String("timeframe", string(tf)),
		slog.Int("candles", len(closed)))
	return len(closed), nil
}
