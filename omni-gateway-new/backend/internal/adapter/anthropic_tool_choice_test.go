package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/zjvtit/omni-gateway/internal/protocol"
)

const anthropicToolChoiceResponse = `{"id":"msg-tool-choice","model":"claude","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`

// captureAnthropicRequest performs one adapter call and returns the decoded
// JSON body the adapter sent upstream. sent is false when the adapter rejected
// the request before any HTTP round trip, which is what tool_choice validation
// must do. The buffered channel gives the handler goroutine and the test
// goroutine a happens-before edge.
func captureAnthropicRequest(t *testing.T, req *protocol.ChatRequest, stream bool) (payload map[string]any, sent bool, err error) {
	t.Helper()
	payloads := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			t.Errorf("read Anthropic request body: %v", readErr)
		}
		var decoded map[string]any
		if decodeErr := json.Unmarshal(body, &decoded); decodeErr != nil {
			t.Errorf("decode Anthropic request body: %v", decodeErr)
		} else {
			select {
			case payloads <- decoded:
			default:
			}
		}
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, anthropicToolChoiceResponse)
	}))
	t.Cleanup(server.Close)
	adapter := NewAnthropicAdapter(Config{BaseURL: server.URL + "/v1", APIKey: "test-key", TimeoutMS: 1000})
	if stream {
		err = adapter.ChatStream(context.Background(), req, func(protocol.StreamEvent) error { return nil })
	} else {
		_, err = adapter.Chat(context.Background(), req)
	}
	select {
	case payload = <-payloads:
		return payload, true, err
	default:
		return nil, false, err
	}
}

func anthropicTestTools(names ...string) []protocol.Tool {
	tools := make([]protocol.Tool, 0, len(names))
	for _, name := range names {
		tools = append(tools, protocol.Tool{
			Type: "function",
			Function: protocol.ToolFunction{
				Name:        name,
				Description: "desc-" + name,
				Parameters:  json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}`),
			},
		})
	}
	return tools
}

func anthropicTestToolSchema() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{"q": map[string]any{"type": "string"}},
		"required":   []any{"q"},
	}
}

func assertAnthropicTools(t *testing.T, raw any, names []string) {
	t.Helper()
	tools, ok := raw.([]any)
	if !ok || len(tools) != len(names) {
		t.Fatalf("tools = %#v, want %d declarations", raw, len(names))
	}
	for i, name := range names {
		tool, ok := tools[i].(map[string]any)
		if !ok {
			t.Fatalf("tool %d = %#v, want object", i, tools[i])
		}
		if tool["name"] != name {
			t.Fatalf("tool %d name = %v, want %q", i, tool["name"], name)
		}
		if tool["description"] != "desc-"+name {
			t.Fatalf("tool %d description = %v, want %q", i, tool["description"], "desc-"+name)
		}
		if !reflect.DeepEqual(tool["input_schema"], anthropicTestToolSchema()) {
			t.Fatalf("tool %d input_schema = %#v, want %#v", i, tool["input_schema"], anthropicTestToolSchema())
		}
	}
}

func TestAnthropicToolChoiceMapping(t *testing.T) {
	cases := []struct {
		name       string
		toolChoice json.RawMessage
		tools      []string
		want       map[string]any // nil means tool_choice must be omitted upstream
		wantErr    error          // non-nil means the request must be rejected before sending
		forbidden  []string       // substrings that must not leak into the error text
	}{
		{
			name:       "auto selector maps to type auto",
			toolChoice: json.RawMessage(`"auto"`),
			tools:      []string{"lookup"},
			want:       map[string]any{"type": "auto"},
		},
		{
			name:       "required selector maps to type any",
			toolChoice: json.RawMessage(`"required"`),
			tools:      []string{"lookup"},
			want:       map[string]any{"type": "any"},
		},
		{
			name:       "none selector maps to type none",
			toolChoice: json.RawMessage(`"none"`),
			tools:      []string{"lookup"},
			want:       map[string]any{"type": "none"},
		},
		{
			name:       "declared named function maps to type tool",
			toolChoice: json.RawMessage(`{"type":"function","function":{"name":"lookup"}}`),
			tools:      []string{"lookup", "store"},
			want:       map[string]any{"type": "tool", "name": "lookup"},
		},
		{
			name:       "auto without tools is omitted",
			toolChoice: json.RawMessage(`"auto"`),
			want:       nil,
		},
		{
			name:       "none without tools is omitted",
			toolChoice: json.RawMessage(`"none"`),
			want:       nil,
		},
		{
			name:       "null is treated as unset",
			toolChoice: json.RawMessage(`null`),
			tools:      []string{"lookup"},
			want:       nil,
		},
		{
			name:       "required without tools is rejected",
			toolChoice: json.RawMessage(`"required"`),
			wantErr:    errAnthropicToolChoiceNoTools,
		},
		{
			name:       "undeclared named function is rejected",
			toolChoice: json.RawMessage(`{"type":"function","function":{"name":"secret-undeclared-tool"}}`),
			tools:      []string{"lookup"},
			wantErr:    errAnthropicToolChoiceUndeclared,
			forbidden:  []string{"secret-undeclared-tool"},
		},
		{
			name:       "named function without tools is rejected",
			toolChoice: json.RawMessage(`{"type":"function","function":{"name":"lookup"}}`),
			wantErr:    errAnthropicToolChoiceUndeclared,
			forbidden:  []string{"lookup"},
		},
		{
			name:       "whitespace-padded declared name is rejected",
			toolChoice: json.RawMessage(`{"type":"function","function":{"name":" lookup "}}`),
			tools:      []string{"lookup"},
			wantErr:    errAnthropicToolChoiceUndeclared,
		},
		{
			name:       "native Anthropic auto object is rejected",
			toolChoice: json.RawMessage(`{"type":"auto"}`),
			tools:      []string{"lookup"},
			wantErr:    errAnthropicToolChoiceShape,
		},
		{
			name:       "native Anthropic any object is rejected",
			toolChoice: json.RawMessage(`{"type":"any"}`),
			tools:      []string{"lookup"},
			wantErr:    errAnthropicToolChoiceShape,
		},
		{
			name:       "native Anthropic tool object is rejected",
			toolChoice: json.RawMessage(`{"type":"tool","name":"lookup"}`),
			tools:      []string{"lookup"},
			wantErr:    errAnthropicToolChoiceShape,
			forbidden:  []string{"lookup"},
		},
		{
			name:       "native Anthropic none object is rejected",
			toolChoice: json.RawMessage(`{"type":"none"}`),
			tools:      []string{"lookup"},
			wantErr:    errAnthropicToolChoiceShape,
		},
		{
			name:       "unknown selector is rejected",
			toolChoice: json.RawMessage(`"everything"`),
			tools:      []string{"lookup"},
			wantErr:    errAnthropicToolChoiceShape,
			forbidden:  []string{"everything"},
		},
		{
			name:       "function object without a name is rejected",
			toolChoice: json.RawMessage(`{"type":"function","function":{}}`),
			tools:      []string{"lookup"},
			wantErr:    errAnthropicToolChoiceShape,
		},
		{
			name:       "blank function name is rejected",
			toolChoice: json.RawMessage(`{"type":"function","function":{"name":"   "}}`),
			tools:      []string{"lookup"},
			wantErr:    errAnthropicToolChoiceShape,
		},
		{
			name:       "non-function object is rejected",
			toolChoice: json.RawMessage(`{"type":"custom","name":"lookup"}`),
			tools:      []string{"lookup"},
			wantErr:    errAnthropicToolChoiceShape,
		},
		{
			name:       "malformed JSON string is rejected without leaking it",
			toolChoice: json.RawMessage(`"secret-json-fragment`),
			tools:      []string{"lookup"},
			wantErr:    errAnthropicToolChoiceShape,
			forbidden:  []string{"secret-json-fragment"},
		},
		{
			name:       "malformed JSON object is rejected without leaking it",
			toolChoice: json.RawMessage(`{"secret-object-fragment":`),
			tools:      []string{"lookup"},
			wantErr:    errAnthropicToolChoiceShape,
			forbidden:  []string{"secret-object-fragment"},
		},
		{
			name:       "array value is rejected",
			toolChoice: json.RawMessage(`["auto"]`),
			tools:      []string{"lookup"},
			wantErr:    errAnthropicToolChoiceShape,
		},
		{
			name:       "number value is rejected",
			toolChoice: json.RawMessage(`42`),
			tools:      []string{"lookup"},
			wantErr:    errAnthropicToolChoiceShape,
		},
		{
			name:       "boolean value is rejected",
			toolChoice: json.RawMessage(`true`),
			tools:      []string{"lookup"},
			wantErr:    errAnthropicToolChoiceShape,
		},
	}
	for _, stream := range []bool{false, true} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("stream=%t/%s", stream, tc.name), func(t *testing.T) {
				req := &protocol.ChatRequest{
					Model:      "claude",
					Messages:   []protocol.Message{{Role: "user", Content: []protocol.ContentPart{{Type: "text", Text: "hi"}}}},
					Tools:      anthropicTestTools(tc.tools...),
					ToolChoice: tc.toolChoice,
				}
				payload, sent, err := captureAnthropicRequest(t, req, stream)
				if tc.wantErr != nil {
					if !errors.Is(err, tc.wantErr) {
						t.Fatalf("error = %v, want %v", err, tc.wantErr)
					}
					if !errors.Is(err, ErrInvalidRequest) {
						t.Fatalf("error = %v, want ErrInvalidRequest", err)
					}
					if sent {
						t.Fatalf("rejected tool_choice was still sent upstream: %#v", payload["tool_choice"])
					}
					for _, forbidden := range tc.forbidden {
						if strings.Contains(err.Error(), forbidden) {
							t.Fatalf("error text leaks request data %q: %v", forbidden, err)
						}
					}
					return
				}
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if !sent {
					t.Fatal("adapter did not send the request upstream")
				}
				if payload["stream"] != stream {
					t.Fatalf("stream = %v, want %v", payload["stream"], stream)
				}
				if payload["model"] != "claude" {
					t.Fatalf("model = %v, want claude", payload["model"])
				}
				if len(tc.tools) == 0 {
					if tools, ok := payload["tools"]; ok {
						t.Fatalf("tools unexpectedly present without declarations: %#v", tools)
					}
				} else {
					assertAnthropicTools(t, payload["tools"], tc.tools)
				}
				if tc.want == nil {
					if got, ok := payload["tool_choice"]; ok {
						t.Fatalf("tool_choice = %#v, want omitted", got)
					}
				} else if got := payload["tool_choice"]; !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("tool_choice = %#v, want %#v", got, tc.want)
				}
			})
		}
	}
}

func TestAnthropicToolChoiceDoesNotChangeBlockAggregation(t *testing.T) {
	// Non-streaming responses must keep aggregating thinking, tool calls and
	// usage the same way while tool_choice is mapped.
	const body = `{"id":"msg-1","model":"claude","stop_reason":"tool_use","content":[{"type":"thinking","thinking":"weigh options"},{"type":"text","text":"checking"},{"type":"tool_use","id":"call_a","name":"lookup","input":{"q":"x"}}],"usage":{"input_tokens":10,"output_tokens":4,"cache_read_input_tokens":6,"cache_creation_input_tokens":2}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	adapter := NewAnthropicAdapter(Config{BaseURL: server.URL + "/v1", APIKey: "test-key", TimeoutMS: 1000})
	response, err := adapter.Chat(context.Background(), &protocol.ChatRequest{
		Model:      "claude",
		Messages:   []protocol.Message{{Role: "user", Content: []protocol.ContentPart{{Type: "text", Text: "hi"}}}},
		Tools:      anthropicTestTools("lookup"),
		ToolChoice: json.RawMessage(`"required"`),
	})
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	if len(response.Choices) != 1 {
		t.Fatalf("choices = %d, want 1", len(response.Choices))
	}
	choice := response.Choices[0]
	if choice.Message.ReasoningContent != "weigh options" {
		t.Fatalf("reasoning = %q, want %q", choice.Message.ReasoningContent, "weigh options")
	}
	if len(choice.Message.Content) != 1 || choice.Message.Content[0].Type != "text" || choice.Message.Content[0].Text != "checking" {
		t.Fatalf("content = %#v, want single text %q", choice.Message.Content, "checking")
	}
	if len(choice.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(choice.Message.ToolCalls))
	}
	call := choice.Message.ToolCalls[0]
	if call.Index == nil || *call.Index != 0 || call.ID != "call_a" || call.Function.Name != "lookup" || call.Function.Arguments != `{"q":"x"}` {
		t.Fatalf("tool call = %#v, want index 0 id call_a lookup arguments {\"q\":\"x\"}", call)
	}
	if choice.FinishReason != "tool_calls" {
		t.Fatalf("finish reason = %q, want tool_calls", choice.FinishReason)
	}
	usage := response.Usage
	if usage.Get("input") != 10 || usage.Get("output") != 4 || usage.Get("total") != 14 ||
		usage.Get("cache_input") != 6 || usage.Get("cache_creation") != 2 {
		t.Fatalf("usage input/output/total/cache = %d/%d/%d/%d/%d, want 10/4/14/6/2",
			usage.Get("input"), usage.Get("output"), usage.Get("total"), usage.Get("cache_input"), usage.Get("cache_creation"))
	}
}

func TestAnthropicToolChoiceDoesNotChangeStreamNormalization(t *testing.T) {
	// Streaming must keep normalizing thinking, per-meter usage increments and
	// contiguous tool-call indexes while tool_choice is mapped. The baseline
	// without tool_choice lives in vendor_reasoning_test.go.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, anthropicReasoningStream)
	}))
	t.Cleanup(server.Close)
	adapter := NewAnthropicAdapter(Config{BaseURL: server.URL + "/v1", APIKey: "test-key", TimeoutMS: 1000})
	var events []protocol.StreamEvent
	err := adapter.ChatStream(context.Background(), &protocol.ChatRequest{
		Model:      "claude",
		Messages:   []protocol.Message{{Role: "user", Content: []protocol.ContentPart{{Type: "text", Text: "hi"}}}},
		Tools:      anthropicTestTools("lookup"),
		ToolChoice: json.RawMessage(`{"type":"function","function":{"name":"lookup"}}`),
	}, func(event protocol.StreamEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("ChatStream returned error: %v", err)
	}
	if len(events) != 8 {
		t.Fatalf("events = %d, want 8", len(events))
	}
	var reasoning, text, arguments string
	var startIndex *int
	var startID, startName string
	var startUsage, deltaUsage *protocol.StreamEvent
	var finishReason string
	done := false
	for i := range events {
		event := events[i]
		if strings.Contains(string(event.Raw), "UPSTREAM-SIGNATURE-VALUE") {
			t.Fatalf("signature_delta leaked into event %d", i)
		}
		switch event.Type {
		case "usage":
			copied := event
			if event.Usage.Get("input") != 0 {
				startUsage = &copied
			}
			if event.Usage.Get("output") != 0 {
				deltaUsage = &copied
			}
		case "chunk":
			reasoning += event.Delta.ReasoningContent
			for _, part := range event.Delta.Content {
				text += part.Text
			}
			for _, call := range event.Delta.ToolCalls {
				if call.ID != "" || call.Function.Name != "" {
					startIndex, startID, startName = call.Index, call.ID, call.Function.Name
				}
				arguments += call.Function.Arguments
			}
			if event.FinishReason != "" {
				finishReason = event.FinishReason
			}
		case "done":
			done = true
		}
	}
	if reasoning != "draft plan" || text != "answer" {
		t.Fatalf("reasoning/text = %q/%q, want draft plan/answer", reasoning, text)
	}
	if startIndex == nil || *startIndex != 0 || startID != "call_a" || startName != "lookup" {
		t.Fatalf("tool start index/id/name = %v/%q/%q, want 0/call_a/lookup", startIndex, startID, startName)
	}
	if arguments != `{"q":"x"}` {
		t.Fatalf("tool arguments = %q, want %q", arguments, `{"q":"x"}`)
	}
	if startUsage == nil || startUsage.Usage.Get("input") != 5 || startUsage.Usage.Get("total") != 5 ||
		startUsage.Usage.Get("cache_input") != 3 || startUsage.Usage.Get("cache_creation") != 1 {
		t.Fatalf("message_start usage = %#v, want input/total 5/5 cache 3/1", startUsage)
	}
	if deltaUsage == nil || deltaUsage.Usage.Get("output") != 7 || deltaUsage.Usage.Get("total") != 7 {
		t.Fatalf("message_delta usage = %#v, want output/total 7/7", deltaUsage)
	}
	if finishReason != "tool_calls" || !done {
		t.Fatalf("finish reason/done = %q/%v, want tool_calls/true", finishReason, done)
	}
}

// A cross-protocol Extra field must never overwrite a payload key the adapter
// builds itself. The refusal happens before any byte reaches the upstream and
// the error text stays static, so neither the key nor its value is echoed.
func TestAnthropicReservedExtraFieldsRejected(t *testing.T) {
	reserved := []string{"model", "messages", "system", "tools", "tool_choice", "max_tokens", "stream"}
	var errorText string
	for _, key := range reserved {
		t.Run(key, func(t *testing.T) {
			req := &protocol.ChatRequest{
				Model:    "claude",
				Messages: []protocol.Message{{Role: "user", Content: []protocol.ContentPart{{Type: "text", Text: "hi"}}}},
				Extra:    map[string]json.RawMessage{key: json.RawMessage(`"secret-extra-value"`)},
			}
			for _, stream := range []bool{false, true} {
				payload, sent, err := captureAnthropicRequest(t, req, stream)
				if !errors.Is(err, ErrInvalidRequest) {
					t.Fatalf("Extra %q stream=%v: error = %v, want ErrInvalidRequest", key, stream, err)
				}
				if sent {
					t.Fatalf("Extra %q stream=%v reached the upstream payload: %#v", key, stream, payload)
				}
				if strings.Contains(err.Error(), "secret-extra-value") {
					t.Fatalf("Extra %q error text leaks the value: %v", key, err)
				}
				if errorText == "" {
					errorText = err.Error()
				} else if err.Error() != errorText {
					t.Fatalf("error text depends on the input: %q vs %q", err.Error(), errorText)
				}
			}
		})
	}
}

// Non-reserved Extra fields must keep flowing through untouched.
func TestAnthropicNonReservedExtraPassesThrough(t *testing.T) {
	req := &protocol.ChatRequest{
		Model:    "claude",
		Messages: []protocol.Message{{Role: "user", Content: []protocol.ContentPart{{Type: "text", Text: "hi"}}}},
		Extra: map[string]json.RawMessage{
			"metadata": json.RawMessage(`{"trace":"one"}`),
			"thinking": json.RawMessage(`{"type":"enabled","budget_tokens":1024}`),
		},
	}
	payload, sent, err := captureAnthropicRequest(t, req, false)
	if err != nil || !sent {
		t.Fatalf("non-reserved Extra was rejected: sent=%v err=%v", sent, err)
	}
	if payload["model"] != "claude" {
		t.Fatalf("adapter model field changed: %#v", payload["model"])
	}
	if !reflect.DeepEqual(payload["metadata"], map[string]any{"trace": "one"}) {
		t.Fatalf("metadata extra = %#v", payload["metadata"])
	}
	if budget, ok := payload["thinking"].(map[string]any); !ok || budget["budget_tokens"] != float64(1024) {
		t.Fatalf("thinking extra = %#v", payload["thinking"])
	}
}
