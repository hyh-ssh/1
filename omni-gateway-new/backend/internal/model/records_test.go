package model

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestRequestStatusConstants(t *testing.T) {
	tests := []struct {
		status RequestStatus
		want   string
	}{
		{StatusSuccess, "success"},
		{StatusError, "error"},
	}
	for _, tc := range tests {
		if string(tc.status) != tc.want {
			t.Errorf("status = %q, want %q", tc.status, tc.want)
		}
	}
}

func TestRequestJSONRoundTrip(t *testing.T) {
	req := Request{
		ID:                 "req-1",
		UserID:             2,
		APIKeyID:           3,
		API:                "chat_completions",
		Streaming:          true,
		ModelAlias:         "fast",
		Status:             StatusSuccess,
		StatusCode:         200,
		Usage:              Usage{Meters: map[string]int64{"input": 10, "output": 20, "total": 30}},
		Cost:               MustMoney("0.1234"),
		Credits:            MustMoney("0.2468"),
		TotalLatencyMS:     1200,
		UpstreamLatencyMS:  1000,
		TTFTMS:             120,
		OutputTokensPerSec: 16.5,
		ClientIP:           "10.0.0.1",
		UserAgent:          "test-agent",
		Tags:               map[string]string{"env": "prod"},
		CreatedAt:          time.Date(2024, 6, 1, 1, 2, 3, 0, time.UTC),
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Request
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != req.ID || got.Status != req.Status || got.StatusCode != req.StatusCode || got.Streaming != req.Streaming {
		t.Fatalf("identity fields = %+v, want %+v", got, req)
	}
	if !got.Cost.Equals(req.Cost) || !got.Credits.Equals(req.Credits) {
		t.Fatalf("money lost: cost=%s credits=%s", got.Cost, got.Credits)
	}
	if !reflect.DeepEqual(got.Usage.Meters, req.Usage.Meters) {
		t.Fatalf("usage = %v, want %v", got.Usage.Meters, req.Usage.Meters)
	}
	if !reflect.DeepEqual(got.Tags, req.Tags) {
		t.Fatalf("tags = %v, want %v", got.Tags, req.Tags)
	}
	if got.OutputTokensPerSec != 16.5 {
		t.Fatalf("OutputTokensPerSec = %v, want 16.5", got.OutputTokensPerSec)
	}
	if !got.CreatedAt.Equal(req.CreatedAt) {
		t.Fatalf("CreatedAt = %v", got.CreatedAt)
	}
}

func TestRequestOmitsEmptyErrorAndTagFields(t *testing.T) {
	raw, err := json.Marshal(Request{ID: "r", Status: StatusSuccess})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, omitted := range []string{"error_type", "error_summary", "client_ip", "user_agent", "tags"} {
		if _, ok := keys[omitted]; ok {
			t.Errorf("%s should be omitted: %s", omitted, raw)
		}
	}
	if _, ok := keys["cost"]; !ok {
		t.Errorf("cost must always be present: %s", raw)
	}
}

func TestRequestErrorFieldsPreserved(t *testing.T) {
	req := Request{
		ID:           "req-err",
		Status:       StatusError,
		StatusCode:   429,
		ErrorType:    "rate_limit",
		ErrorSummary: "upstream throttled",
		Cost:         MustMoney("0"),
		Credits:      ZeroMoney,
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Request
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ErrorType != "rate_limit" || got.ErrorSummary != "upstream throttled" {
		t.Fatalf("error fields = %q/%q", got.ErrorType, got.ErrorSummary)
	}
	if !got.Cost.IsZero() || !got.Credits.IsZero() {
		t.Fatalf("zero money should round trip as zero: %s/%s", got.Cost, got.Credits)
	}
}

func TestAttemptRoundTrip(t *testing.T) {
	attempt := Attempt{
		ID:            1,
		SettlementID:  "set-1",
		RequestID:     "req-1",
		ProviderID:    9,
		ProviderModel: "gpt-4o",
		Priority:      1,
		Status:        "error",
		StatusCode:    500,
		ErrorType:     "upstream_error",
		ErrorSummary:  "boom",
		Usage:         Usage{Meters: map[string]int64{"input": 5}},
		Cost:          MustMoney("0.0005"),
		LatencyMS:     800,
		TTFTMS:        0,
		CreatedAt:     time.Date(2024, 6, 2, 0, 0, 0, 0, time.UTC),
	}
	raw, err := json.Marshal(attempt)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Attempt
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != attempt.ID || got.SettlementID != attempt.SettlementID || got.Status != attempt.Status {
		t.Fatalf("identity = %+v, want %+v", got, attempt)
	}
	if !got.Cost.Equals(attempt.Cost) {
		t.Fatalf("cost = %s, want %s", got.Cost, attempt.Cost)
	}
	if !reflect.DeepEqual(got.Usage.Meters, attempt.Usage.Meters) {
		t.Fatalf("usage = %v", got.Usage.Meters)
	}
	if got.ErrorType != "upstream_error" || got.ErrorSummary != "boom" {
		t.Fatalf("error fields = %q/%q", got.ErrorType, got.ErrorSummary)
	}
	if !got.CreatedAt.Equal(attempt.CreatedAt) {
		t.Fatalf("CreatedAt = %v", got.CreatedAt)
	}

	t.Run("empty settlement error omitted", func(t *testing.T) {
		raw, _ := json.Marshal(Attempt{ID: 2})
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(raw, &keys); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if _, ok := keys["settlement_error"]; ok {
			t.Fatalf("settlement_error should be omitted: %s", raw)
		}
	})
}

func TestLedgerEntryRoundTrip(t *testing.T) {
	entry := LedgerEntry{
		ID:            1,
		SettlementID:  "set-1",
		RequestID:     "req-1",
		UserID:        2,
		APIKeyID:      3,
		ModelAlias:    "fast",
		ProviderID:    9,
		ProviderModel: "gpt-4o",
		Meter:         "input",
		Quantity:      1000,
		Cost:          MustMoney("0.0015"),
		Credits:       MustMoney("0.003"),
		PriceVersion:  4,
		Estimated:     true,
		CreatedAt:     time.Date(2024, 6, 3, 0, 0, 0, 0, time.UTC),
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got LedgerEntry
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != entry.ID || got.SettlementID != entry.SettlementID || got.RequestID != entry.RequestID {
		t.Fatalf("identity fields = %+v, want %+v", got, entry)
	}
	if got.UserID != entry.UserID || got.APIKeyID != entry.APIKeyID || got.ModelAlias != entry.ModelAlias {
		t.Fatalf("ownership fields = %+v, want %+v", got, entry)
	}
	if got.ProviderID != entry.ProviderID || got.ProviderModel != entry.ProviderModel || got.Meter != entry.Meter {
		t.Fatalf("routing fields = %+v, want %+v", got, entry)
	}
	if got.Quantity != entry.Quantity || got.PriceVersion != entry.PriceVersion || got.Estimated != entry.Estimated {
		t.Fatalf("quantity fields = %+v, want %+v", got, entry)
	}
	if !got.Cost.Equals(entry.Cost) || !got.Credits.Equals(entry.Credits) {
		t.Fatalf("money = %s/%s, want %s/%s", got.Cost, got.Credits, entry.Cost, entry.Credits)
	}
	if !got.CreatedAt.Equal(entry.CreatedAt) {
		t.Fatalf("CreatedAt = %v", got.CreatedAt)
	}
}

func TestAuditEntryRoundTrip(t *testing.T) {
	full := AuditEntry{
		ID:         1,
		ActorID:    2,
		ActorName:  "admin",
		Action:     "update",
		Resource:   "provider",
		ResourceID: "9",
		Detail:     "changed base url",
		IP:         "192.168.1.1",
		CreatedAt:  time.Date(2024, 6, 4, 0, 0, 0, 0, time.UTC),
	}
	raw, err := json.Marshal(full)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got AuditEntry
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != full.ID || got.ActorID != full.ActorID || got.ActorName != full.ActorName || got.Action != full.Action || got.Resource != full.Resource {
		t.Fatalf("audit entry = %+v, want %+v", got, full)
	}
	if got.ResourceID != "9" || got.Detail != "changed base url" || got.IP != "192.168.1.1" {
		t.Fatalf("optional fields lost: %+v", got)
	}

	t.Run("optional fields omitted when empty", func(t *testing.T) {
		raw, _ := json.Marshal(AuditEntry{ID: 2, Action: "login"})
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(raw, &keys); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		for _, omitted := range []string{"resource_id", "detail", "ip"} {
			if _, ok := keys[omitted]; ok {
				t.Errorf("%s should be omitted: %s", omitted, raw)
			}
		}
	})
}
