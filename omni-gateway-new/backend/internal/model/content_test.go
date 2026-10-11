package model

import (
	"encoding/json"
	"testing"
	"time"
)

func TestContentLogJSONExcludesBodies(t *testing.T) {
	created := time.Date(2024, 7, 8, 9, 10, 11, 0, time.UTC)
	log := ContentLog{
		RequestID: "req-1",
		Prompt:    []byte("secret prompt"),
		Response:  []byte("secret response"),
		Truncated: true,
		CreatedAt: created,
	}
	raw, err := json.Marshal(log)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, hidden := range []string{"prompt", "response", "Prompt", "Response"} {
		if _, ok := keys[hidden]; ok {
			t.Errorf("%s must not be serialized: %s", hidden, raw)
		}
	}
	if string(keys["request_id"]) != `"req-1"` {
		t.Fatalf("request_id = %s, want \"req-1\"", keys["request_id"])
	}
	if string(keys["truncated"]) != "true" {
		t.Fatalf("truncated = %s, want true", keys["truncated"])
	}
	if string(keys["created_at"]) != `"2024-07-08T09:10:11Z"` {
		t.Fatalf("created_at = %s", keys["created_at"])
	}
}

func TestContentLogRoundTripDropsBodies(t *testing.T) {
	log := ContentLog{
		RequestID: "req-2",
		Prompt:    []byte("p"),
		Response:  []byte("r"),
		Truncated: false,
		CreatedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	raw, err := json.Marshal(log)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got ContentLog
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.RequestID != "req-2" || got.Truncated != false {
		t.Fatalf("scalar fields = %+v", got)
	}
	if !got.CreatedAt.Equal(log.CreatedAt) {
		t.Fatalf("CreatedAt = %v, want %v", got.CreatedAt, log.CreatedAt)
	}
	if got.Prompt != nil || got.Response != nil {
		t.Fatalf("bodies should stay empty after round trip: %q/%q", got.Prompt, got.Response)
	}
}

func TestContentLogZeroValue(t *testing.T) {
	raw, err := json.Marshal(ContentLog{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got ContentLog
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.RequestID != "" || got.Truncated || !got.CreatedAt.IsZero() {
		t.Fatalf("zero ContentLog = %+v", got)
	}
}
