package model

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestStatsSummaryJSONRoundTrip(t *testing.T) {
	summary := StatsSummary{
		Trend: []TrendPoint{
			{Period: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), Requests: 10, Success: 9, Cost: MustMoney("1.10"), Credits: MustMoney("2.20")},
			{Period: time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC), Requests: 5, Success: 5, Cost: MustMoney("0.55"), Credits: MustMoney("1.10")},
		},
		ErrorTypes:            map[string]int64{"rate_limit": 2, "upstream_error": 1},
		Requests:              15,
		Success:               14,
		Errors:                1,
		SuccessRate:           14.0 / 15.0,
		InputTokens:           100,
		OutputTokens:          200,
		TotalTokens:           300,
		Cost:                  MustMoney("1.65"),
		Credits:               MustMoney("3.30"),
		AvgLatencyMS:          123.5,
		AvgTTFTMS:             45.25,
		FailoverCount:         2,
		EstimatedCount:        1,
		P50LatencyMS:          100,
		P95LatencyMS:          250,
		P99LatencyMS:          400,
		P50TTFTMS:             40,
		P95TTFTMS:             90,
		P99TTFTMS:             120,
		AvgUpstreamLatencyMS:  110.5,
		AvgOutputTokensPerSec: 16.25,
		AvgCostPerRequest:     MustMoney("0.11"),
		AvgCreditsPerRequest:  MustMoney("0.22"),
		FailedAttemptCost:     MustMoney("0.03"),
		SettlementErrors:      4,
		Routes: []RouteStats{
			{ModelAlias: "fast", ProviderID: 9, ProviderModel: "gpt-4o", Attempts: 16, Success: 14, Errors: 2, SuccessRate: 14.0 / 16.0, AvgLatencyMS: 120, P95LatencyMS: 250, Cost: MustMoney("1.65"), Credits: MustMoney("3.30"), FailedCost: MustMoney("0.03"), SettlementErrors: 4},
		},
	}

	raw, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got StatsSummary
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Requests != summary.Requests || got.Success != summary.Success || got.Errors != summary.Errors {
		t.Fatalf("counts = %+v, want %+v", got, summary)
	}
	if got.SuccessRate != summary.SuccessRate {
		t.Fatalf("SuccessRate = %v, want %v", got.SuccessRate, summary.SuccessRate)
	}
	if got.InputTokens != 100 || got.OutputTokens != 200 || got.TotalTokens != 300 {
		t.Fatalf("token totals = %d/%d/%d", got.InputTokens, got.OutputTokens, got.TotalTokens)
	}
	if !got.Cost.Equals(summary.Cost) || !got.Credits.Equals(summary.Credits) {
		t.Fatalf("totals = %s/%s, want %s/%s", got.Cost, got.Credits, summary.Cost, summary.Credits)
	}
	if !got.AvgCostPerRequest.Equals(summary.AvgCostPerRequest) || !got.AvgCreditsPerRequest.Equals(summary.AvgCreditsPerRequest) {
		t.Fatalf("per-request money lost")
	}
	if !got.FailedAttemptCost.Equals(summary.FailedAttemptCost) {
		t.Fatalf("FailedAttemptCost = %s", got.FailedAttemptCost)
	}
	if got.SettlementErrors != 4 || got.FailoverCount != 2 || got.EstimatedCount != 1 {
		t.Fatalf("counters = %d/%d/%d", got.SettlementErrors, got.FailoverCount, got.EstimatedCount)
	}
	if got.P50LatencyMS != 100 || got.P95LatencyMS != 250 || got.P99LatencyMS != 400 {
		t.Fatalf("latency percentiles = %v/%v/%v", got.P50LatencyMS, got.P95LatencyMS, got.P99LatencyMS)
	}
	if got.P50TTFTMS != 40 || got.P95TTFTMS != 90 || got.P99TTFTMS != 120 {
		t.Fatalf("ttft percentiles = %v/%v/%v", got.P50TTFTMS, got.P95TTFTMS, got.P99TTFTMS)
	}
	if got.AvgUpstreamLatencyMS != 110.5 || got.AvgOutputTokensPerSec != 16.25 {
		t.Fatalf("averages = %v/%v", got.AvgUpstreamLatencyMS, got.AvgOutputTokensPerSec)
	}
	if !reflect.DeepEqual(got.ErrorTypes, summary.ErrorTypes) {
		t.Fatalf("ErrorTypes = %v, want %v", got.ErrorTypes, summary.ErrorTypes)
	}
	if len(got.Trend) != 2 || len(got.Routes) != 1 {
		t.Fatalf("slices = %d/%d, want 2/1", len(got.Trend), len(got.Routes))
	}
	if !got.Trend[1].Cost.Equals(summary.Trend[1].Cost) || !got.Trend[1].Period.Equal(summary.Trend[1].Period) {
		t.Fatalf("trend point lost: %+v", got.Trend[1])
	}
	route := got.Routes[0]
	if route.ModelAlias != "fast" || route.ProviderID != 9 || route.ProviderModel != "gpt-4o" {
		t.Fatalf("route identity = %+v", route)
	}
	if route.Attempts != 16 || route.Success != 14 || route.Errors != 2 || route.SettlementErrors != 4 {
		t.Fatalf("route counters = %+v", route)
	}
	if route.SuccessRate != 14.0/16.0 || route.AvgLatencyMS != 120 || route.P95LatencyMS != 250 {
		t.Fatalf("route rates = %+v", route)
	}
	if !route.Cost.Equals(MustMoney("1.65")) || !route.Credits.Equals(MustMoney("3.30")) || !route.FailedCost.Equals(MustMoney("0.03")) {
		t.Fatalf("route money = %+v", route)
	}
}

func TestStatsSummaryZeroValueMarshals(t *testing.T) {
	raw, err := json.Marshal(StatsSummary{})
	if err != nil {
		t.Fatalf("marshal zero summary: %v", err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, moneyField := range []string{"cost", "credits", "avg_cost_per_request", "avg_credits_per_request", "failed_attempt_cost"} {
		if _, ok := keys[moneyField]; !ok {
			t.Errorf("%s must be present in zero summary: %s", moneyField, raw)
		}
	}
	// Profit is deliberately absent: cost is CNY while credits are a different
	// unit, so there is no defined difference between the two totals.
	if _, ok := keys["profit"]; ok {
		t.Errorf("profit must not be reported: %s", raw)
	}
	if string(keys["trend"]) != "null" || string(keys["routes"]) != "null" {
		t.Fatalf("nil slices = %s / %s, want null", keys["trend"], keys["routes"])
	}
}

func TestRouteStatsJSONRoundTrip(t *testing.T) {
	in := RouteStats{
		ModelAlias:       "fast",
		ProviderID:       9,
		ProviderModel:    "gpt-4o",
		Attempts:         16,
		Success:          14,
		Errors:           2,
		SuccessRate:      14.0 / 16.0,
		AvgLatencyMS:     120,
		P95LatencyMS:     250,
		Cost:             MustMoney("1.65"),
		Credits:          MustMoney("3.30"),
		FailedCost:       MustMoney("0.03"),
		SettlementErrors: 4,
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got RouteStats
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ModelAlias != "fast" || got.ProviderID != 9 || got.ProviderModel != "gpt-4o" {
		t.Fatalf("route identity lost: %+v", got)
	}
	if got.Attempts != 16 || got.Success != 14 || got.Errors != 2 || got.SettlementErrors != 4 {
		t.Fatalf("route counters lost: %+v", got)
	}
	if got.SuccessRate != 14.0/16.0 || got.AvgLatencyMS != 120 || got.P95LatencyMS != 250 {
		t.Fatalf("route rates lost: %+v", got)
	}
	if !got.Cost.Equals(MustMoney("1.65")) || !got.Credits.Equals(MustMoney("3.30")) || !got.FailedCost.Equals(MustMoney("0.03")) {
		t.Fatalf("route money lost: %+v", got)
	}
}
