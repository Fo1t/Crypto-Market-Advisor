package marketdata

import (
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

// importTestService builds a service with no exchange client, which is the one
// shape that exercises the job machinery without a database or a network: every
// unit fails fast with ErrNotTradable, and the job still has to report it per
// asset instead of dying half-way.
func importTestService() *Service {
	return &Service{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func waitForImport(t *testing.T, s *Service) ImportProgress {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		progress := s.ImportStatus()
		if progress.Status != ImportRunning {
			return progress
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("import did not finish in time")
	return ImportProgress{}
}

func TestImportStatusIsIdleBeforeAnythingIsRequested(t *testing.T) {
	progress := importTestService().ImportStatus()
	if progress.Status != ImportIdle {
		t.Fatalf("expected an idle status, got %q", progress.Status)
	}
	if progress.Items == nil {
		t.Fatal("items must be an empty list rather than null, so the UI has nothing to special-case")
	}
}

func TestImportReportsEveryRequestedUnit(t *testing.T) {
	s := importTestService()
	assets := []domain.Asset{{Symbol: "BTC", BybitSymbol: "BTCUSDT"}, {Symbol: "ETH", BybitSymbol: "ETHUSDT"}}
	timeframes := []domain.Timeframe{domain.TF1h, domain.TF1d}

	started, err := s.StartImport(assets, timeframes, ts(0), ts(60))
	if err != nil {
		t.Fatalf("start import: %v", err)
	}
	if started.Total != 4 {
		t.Fatalf("expected four asset/timeframe units, got %d", started.Total)
	}

	progress := waitForImport(t, s)
	if progress.Status != ImportFailed {
		t.Fatalf("with no exchange client every unit fails, so the job must be failed; got %q", progress.Status)
	}
	if progress.Completed != 4 || len(progress.Items) != 4 {
		t.Fatalf("every unit must be reported: completed=%d items=%d", progress.Completed, len(progress.Items))
	}
	for _, item := range progress.Items {
		if item.Error == "" {
			t.Fatalf("expected %s %s to carry its own error", item.Symbol, item.Timeframe)
		}
	}
	if progress.FinishedAt == nil {
		t.Fatal("a finished job must carry its finish time")
	}
}

func TestImportRejectsASecondJobWhileOneRuns(t *testing.T) {
	s := importTestService()
	// A job that is deliberately never drained: the second request has to be
	// refused while the first is still in flight.
	s.importJob = &ImportProgress{Status: ImportRunning}

	if _, err := s.StartImport([]domain.Asset{{Symbol: "BTC"}}, []domain.Timeframe{domain.TF1h}, ts(0), ts(60)); !errors.Is(err, ErrImportRunning) {
		t.Fatalf("expected ErrImportRunning, got %v", err)
	}
}

func TestImportRequiresAssetsAndTimeframes(t *testing.T) {
	s := importTestService()
	if _, err := s.StartImport(nil, []domain.Timeframe{domain.TF1h}, ts(0), ts(60)); err == nil {
		t.Fatal("an import without assets must be refused")
	}
	if _, err := s.StartImport([]domain.Asset{{Symbol: "BTC"}}, nil, ts(0), ts(60)); err == nil {
		t.Fatal("an import without timeframes must be refused")
	}
}
