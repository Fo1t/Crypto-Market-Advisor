package history

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEmptyStatisticsMarshalCollectionsAsArrays(t *testing.T) {
	stats := emptyStatistics()

	payload, err := json.Marshal(stats)
	if err != nil {
		t.Fatalf("marshal statistics: %v", err)
	}

	got := string(payload)
	for _, field := range []string{"by_symbol", "by_direction", "by_leverage"} {
		if strings.Contains(got, `"`+field+`":null`) {
			t.Fatalf("%s must be an array, got %s", field, got)
		}
		if !strings.Contains(got, `"`+field+`":[]`) {
			t.Fatalf("%s empty array is missing from %s", field, got)
		}
	}
}
