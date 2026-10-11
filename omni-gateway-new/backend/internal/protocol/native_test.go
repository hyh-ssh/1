package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRewriteNativeModelPreservesPayload(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		require   bool
		want      string
	}{
		{"root", `{"model":"upstream","extension":{"model":"user value"}}`, false, `{"model":"alias","extension":{"model":"user value"}}`},
		{"insert", `{"input":"hello"}`, true, `{"input":"hello","model":"alias"}`},
		{"unrelated", `{"extension":true}`, false, `{"extension":true}`},
		{"message start", `{"type":"message_start","message":{"model":"upstream","id":"id"}}`, false, `{"type":"message_start","message":{"model":"alias","id":"id"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RewriteNativeModel(json.RawMessage(tc.raw), "alias", tc.require)
			if err != nil {
				t.Fatal(err)
			}
			var actual, expected any
			_ = json.Unmarshal(got, &actual)
			_ = json.Unmarshal([]byte(tc.want), &expected)
			a, _ := json.Marshal(actual)
			b, _ := json.Marshal(expected)
			if string(a) != string(b) {
				t.Fatalf("got %s, want %s", a, b)
			}
		})
	}
	for _, raw := range []string{"null", "[]", "broken", `{"type":"message_start","message":[]}`} {
		if _, err := RewriteNativeModel(json.RawMessage(raw), "alias", false); err == nil {
			t.Fatalf("accepted invalid object %s", raw)
		}
	}
}

func TestRewriteNativeSSEPreservesMetadata(t *testing.T) {
	wire := ": comment\nevent: message_start\nid: event-1\nretry: 100\ndata: {\"type\":\"message_start\",\n" +
		"data: \"message\":{\"model\":\"upstream\"}}\n\n"
	got, err := RewriteNativeEventModel(NativeEvent{Wire: []byte(wire)}, "alias")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{": comment\n", "event: message_start\n", "id: event-1\n", "retry: 100\n", `"model":"alias"`} {
		if !strings.Contains(string(got.Wire), expected) {
			t.Fatalf("lost field %s: %s", expected, got.Wire)
		}
	}
	for _, wire := range []string{": heartbeat\n\n", "data: [DONE]\n\n"} {
		got, err := RewriteNativeEventModel(NativeEvent{Wire: []byte(wire)}, "alias")
		if err != nil || string(got.Wire) != wire {
			t.Fatalf("changed control event: %s %v", got.Wire, err)
		}
	}
	if _, err := RewriteNativeEventModel(NativeEvent{Wire: []byte("data: broken\n\n")}, "alias"); err == nil {
		t.Fatal("accepted malformed SSE data")
	}
}
