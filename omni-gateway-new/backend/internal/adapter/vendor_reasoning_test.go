package adapter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zjvtit/omni-gateway/internal/model"
	"github.com/zjvtit/omni-gateway/internal/protocol"
)

// These tests pin the fidelity guarantees of the cross-protocol Chat
// conversion for the Anthropic and Gemini vendors:
//   - one assistant choice per upstream message, with text, reasoning and
//     tool calls kept in their own normalized fields;
//   - streamed tool-call indexes are renumbered contiguously per stream
//     through pointers, so index 0 is distinguishable from "no index" and a
//     tool_use block keeps one index across its argument deltas;
//   - reasoning/cache usage meters are added without changing the existing
//     input/output/total accounting;
//   - Anthropic's cumulative stream usage is emitted as positive per-meter
//     increments, so summing the usage events reconstructs the final totals.
//
// Known limitation covered here: Anthropic's signature_delta carries an
// opaque provider signature with no normalized Chat field. It is intentionally
// dropped (never fabricated into content or reasoning) and the tests assert
// that it does not leak into any emitted event.

func TestAnthropicChatAggregatesBlocksIntoOneChoice(t *testing.T) {
	cases := []struct {
		name           string
		body           string
		wantContent    []string
		wantReasoning  string
		wantTools      []string // "id:name", order significant
		wantFinish     string
		wantInput      int64
		wantOutput     int64
		wantTotal      int64
		wantCacheRead  int64
		wantCacheWrite int64
	}{
		{
			name:           "thinking text and two tool calls",
			body:           `{"id":"msg-1","model":"claude","stop_reason":"tool_use","content":[{"type":"thinking","thinking":"weigh options"},{"type":"text","text":"Let me check."},{"type":"tool_use","id":"call_a","name":"lookup","input":{"q":"x"}},{"type":"tool_use","id":"call_b","name":"store","input":{"v":1}}],"usage":{"input_tokens":10,"output_tokens":4,"cache_read_input_tokens":6,"cache_creation_input_tokens":2}}`,
			wantContent:    []string{"Let me check."},
			wantReasoning:  "weigh options",
			wantTools:      []string{"call_a:lookup", "call_b:store"},
			wantFinish:     "tool_calls",
			wantInput:      10,
			wantOutput:     4,
			wantTotal:      14, // input + output; cache stays supplementary
			wantCacheRead:  6,
			wantCacheWrite: 2,
		},
		{
			name:        "multiple text blocks remain one choice",
			body:        `{"id":"msg-2","model":"claude","stop_reason":"end_turn","content":[{"type":"text","text":"one"},{"type":"text","text":"two"}],"usage":{"input_tokens":1,"output_tokens":2}}`,
			wantContent: []string{"one", "two"},
			wantFinish:  "stop",
			wantInput:   1,
			wantOutput:  2,
			wantTotal:   3,
		},
		{
			name:          "thinking only",
			body:          `{"id":"msg-3","model":"claude","stop_reason":"end_turn","content":[{"type":"thinking","thinking":"considered"}],"usage":{"input_tokens":1,"output_tokens":1}}`,
			wantReasoning: "considered",
			wantFinish:    "stop",
			wantInput:     1,
			wantOutput:    1,
			wantTotal:     2,
		},
		{
			name:        "tool calls win over an end_turn stop reason",
			body:        `{"id":"msg-4","model":"claude","stop_reason":"end_turn","content":[{"type":"text","text":"calling"},{"type":"tool_use","id":"call_c","name":"lookup","input":{}}],"usage":{"input_tokens":2,"output_tokens":3}}`,
			wantContent: []string{"calling"},
			wantTools:   []string{"call_c:lookup"},
			wantFinish:  "tool_calls",
			wantInput:   2,
			wantOutput:  3,
			wantTotal:   5,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			adapter := NewAnthropicAdapter(Config{BaseURL: server.URL + "/v1", APIKey: "test-key", TimeoutMS: 1000})
			response, err := adapter.Chat(context.Background(), &protocol.ChatRequest{
				Model:    "claude",
				Messages: []protocol.Message{{Role: "user", Content: []protocol.ContentPart{{Type: "text", Text: "hi"}}}},
			})
			if err != nil {
				t.Fatalf("Chat returned error: %v", err)
			}
			if len(response.Choices) != 1 {
				t.Fatalf("choices = %d, want 1", len(response.Choices))
			}
			choice := response.Choices[0]
			if choice.Index != 0 || choice.Message.Role != "assistant" {
				t.Fatalf("choice index/role = %d/%q, want 0/assistant", choice.Index, choice.Message.Role)
			}
			if len(choice.Message.Content) != len(tc.wantContent) {
				t.Fatalf("content parts = %d, want %d", len(choice.Message.Content), len(tc.wantContent))
			}
			for i, want := range tc.wantContent {
				if choice.Message.Content[i].Type != "text" || choice.Message.Content[i].Text != want {
					t.Fatalf("content[%d] = %#v, want text %q", i, choice.Message.Content[i], want)
				}
			}
			if choice.Message.ReasoningContent != tc.wantReasoning {
				t.Fatalf("reasoning = %q, want %q", choice.Message.ReasoningContent, tc.wantReasoning)
			}
			if len(choice.Message.ToolCalls) != len(tc.wantTools) {
				t.Fatalf("tool calls = %d, want %d", len(choice.Message.ToolCalls), len(tc.wantTools))
			}
			for i, want := range tc.wantTools {
				call := choice.Message.ToolCalls[i]
				if call.Index == nil || *call.Index != i {
					t.Fatalf("tool call %d index = %v, want %d", i, call.Index, i)
				}
				if call.ID+":"+call.Function.Name != want {
					t.Fatalf("tool call %d = %q, want %q", i, call.ID+":"+call.Function.Name, want)
				}
			}
			if choice.FinishReason != tc.wantFinish {
				t.Fatalf("finish reason = %q, want %q", choice.FinishReason, tc.wantFinish)
			}
			usage := response.Usage
			if usage.Get("input") != tc.wantInput || usage.Get("output") != tc.wantOutput || usage.Get("total") != tc.wantTotal {
				t.Fatalf("usage input/output/total = %d/%d/%d, want %d/%d/%d",
					usage.Get("input"), usage.Get("output"), usage.Get("total"), tc.wantInput, tc.wantOutput, tc.wantTotal)
			}
			if usage.Get("cache_input") != tc.wantCacheRead || usage.Get("cache_creation") != tc.wantCacheWrite {
				t.Fatalf("usage cache_input/cache_creation = %d/%d, want %d/%d",
					usage.Get("cache_input"), usage.Get("cache_creation"), tc.wantCacheRead, tc.wantCacheWrite)
			}
		})
	}
}

const anthropicReasoningStream = "event: message_start\n" +
	`data: {"type":"message_start","message":{"usage":{"input_tokens":5,"output_tokens":0,"cache_read_input_tokens":3,"cache_creation_input_tokens":1}}}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"draft plan"}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"UPSTREAM-SIGNATURE-VALUE"}}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":1,"content_block":{"type":"text"}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"answer"}}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"call_a","name":"lookup"}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"q\":\"x\"}"}}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":7}}` + "\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n\n"

func collectAnthropicStream(t *testing.T, stream string) []protocol.StreamEvent {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, stream)
	}))
	defer server.Close()
	adapter := NewAnthropicAdapter(Config{BaseURL: server.URL + "/v1", APIKey: "test-key", TimeoutMS: 1000})
	var events []protocol.StreamEvent
	err := adapter.ChatStream(context.Background(), &protocol.ChatRequest{
		Model:    "claude",
		Messages: []protocol.Message{{Role: "user", Content: []protocol.ContentPart{{Type: "text", Text: "hi"}}}},
	}, func(event protocol.StreamEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("ChatStream returned error: %v", err)
	}
	return events
}

func TestAnthropicChatStreamReasoningToolIndexAndSignature(t *testing.T) {
	events := collectAnthropicStream(t, anthropicReasoningStream)

	// message_start usage, thinking chunk, text chunk, tool_use start chunk,
	// input_json_delta chunk, message_delta usage, finish chunk, done.
	// content_block_start for thinking/text and signature_delta emit nothing.
	if len(events) != 8 {
		t.Fatalf("events = %d, want 8", len(events))
	}

	var reasoning, text string
	var startCall, argumentCall *protocol.ToolCall
	var startUsage, deltaUsage *protocol.StreamEvent
	var finishReasons []string
	var done bool
	for i := range events {
		event := events[i]
		if strings.Contains(string(event.Raw), "UPSTREAM-SIGNATURE-VALUE") {
			t.Fatalf("signature_delta leaked into event %d", i)
		}
		switch event.Type {
		case "usage":
			if event.Usage.Get("input") != 0 {
				copied := event
				startUsage = &copied
			}
			if event.Usage.Get("output") != 0 {
				copied := event
				deltaUsage = &copied
			}
		case "chunk":
			reasoning += event.Delta.ReasoningContent
			for _, part := range event.Delta.Content {
				text += part.Text
			}
			for _, call := range event.Delta.ToolCalls {
				copied := call
				if call.ID != "" || call.Function.Name != "" {
					startCall = &copied
				}
				if call.Function.Arguments != "" {
					argumentCall = &copied
				}
			}
			if event.FinishReason != "" {
				finishReasons = append(finishReasons, event.FinishReason)
			}
		case "done":
			done = true
		}
	}

	if reasoning != "draft plan" {
		t.Fatalf("reasoning deltas = %q, want %q", reasoning, "draft plan")
	}
	if text != "answer" {
		t.Fatalf("text deltas = %q, want %q", text, "answer")
	}
	if !done {
		t.Fatal("missing done event")
	}
	if len(finishReasons) != 1 || finishReasons[0] != "tool_calls" {
		t.Fatalf("finish reasons = %v, want [tool_calls]", finishReasons)
	}
	if startCall == nil || startCall.Index == nil || *startCall.Index != 0 ||
		startCall.ID != "call_a" || startCall.Function.Name != "lookup" {
		t.Fatalf("tool_use start = %#v, want tool index 0 (block index 2) id call_a name lookup", startCall)
	}
	if argumentCall == nil || argumentCall.Index == nil || *argumentCall.Index != 0 ||
		argumentCall.Function.Arguments != `{"q":"x"}` {
		t.Fatalf("input_json_delta = %#v, want tool index 0 arguments {\"q\":\"x\"}", argumentCall)
	}
	if startUsage == nil || startUsage.Usage.Get("input") != 5 || startUsage.Usage.Get("total") != 5 ||
		startUsage.Usage.Get("cache_input") != 3 || startUsage.Usage.Get("cache_creation") != 1 {
		t.Fatalf("message_start usage = %#v, want input/total 5/5 cache 3/1", startUsage)
	}
	if deltaUsage == nil || deltaUsage.Usage.Get("output") != 7 || deltaUsage.Usage.Get("total") != 7 {
		t.Fatalf("message_delta usage = %#v, want output/total 7/7", deltaUsage)
	}
}

func TestAnthropicChatStreamToolCallIndexZero(t *testing.T) {
	stream := "event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_zero","name":"lookup"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}` + "\n\n"
	events := collectAnthropicStream(t, stream)
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	for i, event := range events {
		if len(event.Delta.ToolCalls) != 1 {
			t.Fatalf("event %d tool calls = %d, want 1", i, len(event.Delta.ToolCalls))
		}
		index := event.Delta.ToolCalls[0].Index
		if index == nil || *index != 0 {
			t.Fatalf("event %d tool call index = %v, want pointer to 0", i, index)
		}
	}
}

func TestAnthropicChatStreamToolIndexesAreContiguousPerTool(t *testing.T) {
	// Anthropic numbers every content block (text included) while OpenAI
	// tool_calls[].index counts only tool calls, and the two tools' argument
	// deltas arrive interleaved: block 1 -> tool index 0, block 2 -> tool
	// index 1, with each block's arguments staying on its own tool index.
	stream := "event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call_a","name":"lookup"}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"call_b","name":"store"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"v\":"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"q\":\"x\"}"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"1}"}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"

	events := collectAnthropicStream(t, stream)
	startIndexes := map[string]int{}
	startDeltas := map[string]protocol.Message{}
	arguments := map[int]string{}
	for _, event := range events {
		for _, call := range event.Delta.ToolCalls {
			if call.Index == nil {
				t.Fatalf("tool call delta without index: %#v", call)
			}
			switch {
			case call.ID != "":
				startIndexes[call.ID] = *call.Index
				startDeltas[call.ID] = event.Delta
			case call.Function.Arguments != "":
				arguments[*call.Index] += call.Function.Arguments
			}
		}
	}

	if startIndexes["call_a"] != 0 || startIndexes["call_b"] != 1 {
		t.Fatalf("tool start indexes = %v, want call_a=0 call_b=1", startIndexes)
	}
	if arguments[0] != `{"q":"x"}` {
		t.Fatalf("arguments for tool index 0 = %q, want %q", arguments[0], `{"q":"x"}`)
	}
	if arguments[1] != `{"v":1}` {
		t.Fatalf("arguments for tool index 1 = %q, want %q", arguments[1], `{"v":1}`)
	}
	encoded, err := json.Marshal(startDeltas["call_a"])
	if err != nil {
		t.Fatalf("marshal call_a start delta: %v", err)
	}
	if !strings.Contains(string(encoded), `"index":0`) {
		t.Fatalf("serialized tool start = %s, want index 0 present", encoded)
	}
}

func TestAnthropicChatStreamUsageEmitsOnlyIncrements(t *testing.T) {
	// Anthropic repeats cumulative counters: message_start reports output 1 and
	// the final message_delta reports output 7. The gateway adds every usage
	// event it receives, so the adapter must send 1 then 6 and skip repeated
	// cumulative payloads; forwarding them verbatim would record output 15.
	stream := "event: message_start\n" +
		`data: {"type":"message_start","message":{"usage":{"input_tokens":5,"output_tokens":1,"cache_read_input_tokens":2,"cache_creation_input_tokens":3}}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{},"usage":{"output_tokens":7}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{},"usage":{"output_tokens":7}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{},"usage":{"input_tokens":5,"output_tokens":7,"cache_read_input_tokens":2,"cache_creation_input_tokens":3}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":8,"output_tokens":7,"cache_read_input_tokens":2,"cache_creation_input_tokens":3}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"

	events := collectAnthropicStream(t, stream)
	total := model.NewUsage()
	usageEvents := 0
	for _, event := range events {
		if event.Type != "usage" {
			continue
		}
		usageEvents++
		if len(event.Usage.Meters) == 0 {
			t.Fatalf("usage event %d carries no meters", usageEvents)
		}
		for key, value := range event.Usage.Meters {
			if value <= 0 {
				t.Fatalf("usage event %d meter %q = %d, want a positive increment", usageEvents, key, value)
			}
		}
		total = total.Add(event.Usage)
	}
	if usageEvents != 3 {
		t.Fatalf("usage events = %d, want 3 (repeated cumulative payloads emit nothing)", usageEvents)
	}
	if total.Get("input") != 8 || total.Get("output") != 7 || total.Get("total") != 15 {
		t.Fatalf("aggregated usage input/output/total = %d/%d/%d, want final 8/7/15",
			total.Get("input"), total.Get("output"), total.Get("total"))
	}
	if total.Get("cache_input") != 2 || total.Get("cache_creation") != 3 {
		t.Fatalf("aggregated cache usage = %d/%d, want 2/3", total.Get("cache_input"), total.Get("cache_creation"))
	}
}

func TestGeminiChatThoughtPartsAndUsage(t *testing.T) {
	body := `{"candidates":[{"content":{"role":"model","parts":[{"text":"step 1","thought":true},{"text":"visible"},{"text":"step 2","thought":true},{"functionCall":{"name":"lookup","args":{"q":"x"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":9,"candidatesTokenCount":3,"totalTokenCount":12,"thoughtsTokenCount":4,"cachedContentTokenCount":2}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()
	adapter := NewGeminiAdapter(Config{BaseURL: server.URL, APIKey: "test-key", TimeoutMS: 1000})
	response, err := adapter.Chat(context.Background(), &protocol.ChatRequest{
		Model:    "gemini-pro",
		Messages: []protocol.Message{{Role: "user", Content: []protocol.ContentPart{{Type: "text", Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	if len(response.Choices) != 1 {
		t.Fatalf("choices = %d, want 1", len(response.Choices))
	}
	message := response.Choices[0].Message
	if len(message.Content) != 1 || message.Content[0].Text != "visible" {
		t.Fatalf("content = %#v, want only [visible]", message.Content)
	}
	if message.ReasoningContent != "step 1step 2" {
		t.Fatalf("reasoning = %q, want %q", message.ReasoningContent, "step 1step 2")
	}
	if len(message.ToolCalls) != 1 || message.ToolCalls[0].Function.Name != "lookup" {
		t.Fatalf("tool calls = %#v, want lookup", message.ToolCalls)
	}
	if response.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("finish reason = %q, want tool_calls", response.Choices[0].FinishReason)

	}
	usage := response.Usage
	if usage.Get("input") != 9 || usage.Get("output") != 3 || usage.Get("total") != 12 {
		t.Fatalf("usage input/output/total = %d/%d/%d, want 9/3/12", usage.Get("input"), usage.Get("output"), usage.Get("total"))
	}
	if usage.Get("reasoning") != 4 || usage.Get("cache_input") != 2 {
		t.Fatalf("usage reasoning/cache_input = %d/%d, want 4/2", usage.Get("reasoning"), usage.Get("cache_input"))
	}
}

func TestGeminiChatUsageWithoutCandidates(t *testing.T) {
	body := `{"usageMetadata":{"thoughtsTokenCount":5,"cachedContentTokenCount":3}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()
	adapter := NewGeminiAdapter(Config{BaseURL: server.URL, APIKey: "test-key", TimeoutMS: 1000})
	response, err := adapter.Chat(context.Background(), &protocol.ChatRequest{Model: "gemini-pro"})
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	if len(response.Choices) != 1 || len(response.Choices[0].Message.Content) != 0 {
		t.Fatalf("choices = %#v, want a single empty assistant choice", response.Choices)
	}
	if response.Usage.Get("reasoning") != 5 || response.Usage.Get("cache_input") != 3 {
		t.Fatalf("usage reasoning/cache_input = %d/%d, want 5/3", response.Usage.Get("reasoning"), response.Usage.Get("cache_input"))
	}
}

func TestGeminiChatStreamThoughtsAndUsageOnlyFrame(t *testing.T) {
	stream := "data: " + `{"candidates":[{"content":{"parts":[{"text":"step","thought":true}]}}]}` + "\n\n" +
		"data: " + `{"candidates":[{"content":{"parts":[{"text":"visible"}]},"finishReason":"STOP"}]}` + "\n\n" +
		"data: " + `{"usageMetadata":{"thoughtsTokenCount":5,"cachedContentTokenCount":3}}` + "\n\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, stream)
	}))
	defer server.Close()
	adapter := NewGeminiAdapter(Config{BaseURL: server.URL, APIKey: "test-key", TimeoutMS: 1000})
	var events []protocol.StreamEvent
	err := adapter.ChatStream(context.Background(), &protocol.ChatRequest{
		Model:    "gemini-pro",
		Messages: []protocol.Message{{Role: "user", Content: []protocol.ContentPart{{Type: "text", Text: "hi"}}}},
	}, func(event protocol.StreamEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("ChatStream returned error: %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("events = %d, want 4", len(events))
	}
	thought := events[0]
	if thought.Type != "chunk" || thought.Delta.ReasoningContent != "step" || len(thought.Delta.Content) != 0 {
		t.Fatalf("thought event = %#v, want reasoning-only chunk", thought)
	}
	visible := events[1]
	if visible.Type != "chunk" || len(visible.Delta.Content) != 1 || visible.Delta.Content[0].Text != "visible" ||
		visible.Delta.ReasoningContent != "" {
		t.Fatalf("visible event = %#v, want text-only chunk", visible)
	}
	if events[2].Type != "chunk" || events[2].FinishReason != "stop" {
		t.Fatalf("finish event = %#v, want stop chunk", events[2])
	}
	if events[3].Type != "usage" || events[3].Usage.Get("reasoning") != 5 || events[3].Usage.Get("cache_input") != 3 {
		t.Fatalf("usage-only frame output = %#v, want reasoning 5 cache_input 3", events[3])
	}
}
