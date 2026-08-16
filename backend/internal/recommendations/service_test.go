package recommendations

import (
	"testing"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

func TestKnownProviderVolumeLimitationIsNotATradeSignal(t *testing.T) {
	in := map[string]domain.RecommendationNarrative{
		"ru":    {SignalsAgainst: []string{"Недостающие данные об объёме", "Таймфреймы расходятся"}},
		"en":    {SignalsAgainst: []string{"Missing volume data", "Timeframes disagree"}},
		"zh-CN": {SignalsAgainst: []string{"缺少成交量数据", "各时间周期存在分歧"}},
	}

	out := withoutKnownVolumeLimitation(in)
	for language, narrative := range out {
		if len(narrative.SignalsAgainst) != 1 {
			t.Fatalf("%s: expected only the market-specific signal, got %v", language, narrative.SignalsAgainst)
		}
	}
}
