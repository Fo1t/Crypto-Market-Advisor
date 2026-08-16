package scheduler

import (
	"testing"
	"time"

	"github.com/crypto-market-advisor/advisor/internal/config"
)

// TestBackgroundAnalysisSwitchAppliesWithoutRestart is the regression guard for
// the bug where the analysis cycle kept running after the settings switch was
// turned off: the switch was only read once, at process start.
func TestBackgroundAnalysisSwitchAppliesWithoutRestart(t *testing.T) {
	s := &Scheduler{analysisCfg: config.AnalysisConfig{Enabled: true, Interval: 5 * time.Minute}}
	if !s.analysisEnabled() {
		t.Fatal("the scheduler must start from its configured state")
	}

	s.SetAnalysisConfig(config.AnalysisConfig{Enabled: false, Interval: 15 * time.Minute})
	if s.analysisEnabled() {
		t.Fatal("switching background analysis off must stop the cycle")
	}
	if got := s.currentAnalysisConfig().Interval; got != 15*time.Minute {
		t.Fatalf("the edited interval must be picked up, got %s", got)
	}

	s.SetAnalysisConfig(config.AnalysisConfig{Enabled: true, Interval: 5 * time.Minute})
	if !s.analysisEnabled() {
		t.Fatal("switching it back on must resume the cycle without a restart")
	}
}

func TestUntilNextBoundaryAlignsToTheInterval(t *testing.T) {
	now := time.Date(2026, 8, 15, 10, 3, 20, 0, time.UTC)
	if got := untilNextBoundary(now, 5*time.Minute); got != 100*time.Second {
		t.Fatalf("expected the next 5-minute boundary in 1m40s, got %s", got)
	}
	// A zero interval must not busy-loop.
	if got := untilNextBoundary(now, 0); got != time.Minute {
		t.Fatalf("a zero interval must fall back to a minute, got %s", got)
	}
}
