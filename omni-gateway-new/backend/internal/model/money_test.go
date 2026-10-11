package model

import "testing"

func TestTotalTokensDoesNotDoubleCountSubsetMeters(t *testing.T) {
	usage := Usage{Meters: map[string]int64{"input": 10, "output": 5, "cache_input": 4, "reasoning": 2, "total": 15}}
	if got := usage.TotalTokens(); got != 15 {
		t.Fatalf("got %d tokens, want 15", got)
	}
	legacy := Usage{Meters: map[string]int64{"input": 10, "output": 5, "image": 1}}
	if legacy.TotalTokens() != 15 || (Usage{}).TotalTokens() != 0 {
		t.Fatal("legacy or empty usage total is incorrect")
	}
	combined := usage.Add(Usage{Meters: map[string]int64{"input": 3, "total": 3}, Estimated: true})
	if combined.TotalTokens() != 18 || !combined.Estimated || usage.TotalTokens() != 15 {
		t.Fatal("usage aggregation changed totals or original input")
	}
}
