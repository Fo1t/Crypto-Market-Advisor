package settings

import (
	"testing"
	"time"

	"github.com/crypto-market-advisor/advisor/internal/config"
)

func TestNormalizeBackfillsNewsFieldsFromOlderStoredDocument(t *testing.T) {
	defaults := Settings{
		Risk: Risk{CriticalNewsMaxLeverage: 15, CriticalNewsHighVolMaxLeverage: 8, CriticalNewsMaxAgeS: 7200},
		News: News{Enabled: true, FetchIntervalS: 300, LLMLookbackHours: 24, LLMMaxAssetItems: 8, LLMMaxGlobalItems: 5, HistoryMinSampleSize: 20, BybitEnabled: true},
		LLM:  LLM{PromptVersion: "market_advisor_v3_news", ContextSize: 16384},
	}
	stored := Settings{Risk: Risk{MinLeverage: 5, MaxLeverage: 50}, LLM: LLM{PromptVersion: "market_advisor_v2_multilingual"}}
	got := normalize(stored, defaults)
	if got.Risk.CriticalNewsMaxLeverage != 15 || got.Risk.CriticalNewsHighVolMaxLeverage != 8 || got.Risk.CriticalNewsMaxAgeS != 7200 {
		t.Fatalf("risk defaults were not restored: %+v", got.Risk)
	}
	if got.News.FetchIntervalS != 300 || !got.News.Enabled || !got.News.BybitEnabled {
		t.Fatalf("news defaults were not restored: %+v", got.News)
	}
	if got.LLM.PromptVersion != defaults.LLM.PromptVersion {
		t.Fatalf("prompt version was not upgraded: %q", got.LLM.PromptVersion)
	}
	if got.LLM.ContextSize != defaults.LLM.ContextSize {
		t.Fatalf("context size was not backfilled: %d", got.LLM.ContextSize)
	}
}

// TestAnalysisConfigFollowsTheStoredSwitch guards the wiring behind the
// "background analysis" checkbox: the stored document, not the environment,
// decides whether the periodic cycle may run.
func TestAnalysisConfigFollowsTheStoredSwitch(t *testing.T) {
	svc := &Service{
		base: config.Config{Analysis: config.AnalysisConfig{
			Enabled:            true,
			Interval:           5 * time.Minute,
			Timeframes:         []string{"1m", "5m", "15m", "1h", "4h", "1d"},
			CandleHistoryLimit: 500,
		}},
		current: Settings{General: General{
			AnalysisEnabled:   false,
			AnalysisIntervalS: 900,
			Timeframes:        []string{"15m", "1h"},
		}},
	}

	got := svc.AnalysisConfig()
	if got.Enabled {
		t.Fatal("a disabled switch must disable background analysis even when the environment enables it")
	}
	if got.Interval != 15*time.Minute {
		t.Fatalf("the edited interval must win, got %s", got.Interval)
	}
	if len(got.Timeframes) != 2 {
		t.Fatalf("the edited timeframes must win, got %v", got.Timeframes)
	}
	if got.CandleHistoryLimit != 500 {
		t.Fatalf("environment-owned fields must be preserved, got %d", got.CandleHistoryLimit)
	}
}
