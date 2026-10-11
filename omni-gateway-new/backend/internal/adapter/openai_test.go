package adapter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zjvtit/omni-gateway/internal/protocol"
)

func TestBuildChatPayloadIncludesNormalizedFields(t *testing.T) {
	adapter := &OpenAIAdapter{}
	payload := adapter.buildChatPayload(&protocol.ChatRequest{
		Model:           "vision-model",
		Messages:        []protocol.Message{{Role: "user", Content: []protocol.ContentPart{{Type: "text", Text: "describe"}, {Type: "input_image", ImageData: "aW1hZ2U=", MediaType: "image/png"}}}},
		ReasoningEffort: "low",
		User:            "user-1",
		Extra:           map[string]json.RawMessage{"seed": json.RawMessage(`7`)},
	}, true)

	if payload["model"] != "vision-model" || payload["stream"] != true {
		t.Fatalf("unexpected base payload: %#v", payload)
	}
	if payload["reasoning_effort"] != "low" || payload["user"] != "user-1" {
		t.Fatalf("normalized fields missing: %#v", payload)
	}
	if _, ok := payload["seed"]; !ok {
		t.Fatalf("extra field missing: %#v", payload)
	}
	streamOptions, ok := payload["stream_options"].(map[string]any)
	if !ok || streamOptions["include_usage"] != true {
		t.Fatalf("stream usage option missing: %#v", payload["stream_options"])
	}

	messages, ok := payload["messages"].([]map[string]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("unexpected messages: %#v", payload["messages"])
	}
	parts, ok := messages[0]["content"].([]map[string]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("unexpected message content: %#v", messages[0]["content"])
	}
	image, ok := parts[1]["image_url"].(map[string]any)
	if !ok || image["url"] != "data:image/png;base64,aW1hZ2U=" || parts[1]["type"] != "image_url" {
		t.Fatalf("unexpected image conversion: %#v", parts[1])
	}
}

func TestBuildResponsesPayloadIncludesPreviousResponseAndExtra(t *testing.T) {
	payload := (&OpenAIAdapter{}).buildResponsesPayload(&protocol.ResponsesRequest{
		Model:              "gpt-4.1",
		Input:              json.RawMessage(`[{"role":"user","content":"hi"}]`),
		Instructions:       "be concise",
		PreviousResponseID: "resp-1",
		Extra:              map[string]json.RawMessage{"temperature": json.RawMessage(`0.2`)},
	}, true)

	if payload["model"] != "gpt-4.1" || payload["stream"] != true || payload["instructions"] != "be concise" || payload["previous_response_id"] != "resp-1" {
		t.Fatalf("unexpected Responses payload: %#v", payload)
	}
	if string(payload["input"].(json.RawMessage)) != `[{"role":"user","content":"hi"}]` {
		t.Fatalf("input was not preserved: %#v", payload["input"])
	}
	if string(payload["temperature"].(json.RawMessage)) != "0.2" {
		t.Fatalf("extra field was not preserved: %#v", payload["temperature"])
	}
}

func TestParseOpenAIMessageMultimodal(t *testing.T) {
	message, err := parseOpenAIMessage(json.RawMessage(`{
		"role":"assistant",
		"content":[
			{"type":"text","text":"look"},
			{"type":"image_url","image_url":{"url":"https://example.test/a.png","detail":"high"}},
			{"type":"input_image","image_data":"YWJj","media_type":"image/jpeg"}
		],
		"tool_call_id":"call-1"
	}`))
	if err != nil {
		t.Fatalf("parseOpenAIMessage returned error: %v", err)
	}
	if message.Role != "assistant" || message.ToolCallID != "call-1" || len(message.Content) != 3 {
		t.Fatalf("unexpected message: %#v", message)
	}
	if message.Content[0].Text != "look" || message.Content[1].ImageURL != "https://example.test/a.png" {
		t.Fatalf("unexpected text/image content: %#v", message.Content)
	}
	if message.Content[2].ImageData != "YWJj" || message.Content[2].MediaType != "image/jpeg" {
		t.Fatalf("unexpected base64 image content: %#v", message.Content[2])
	}
}

func TestParseOpenAIUsageVariants(t *testing.T) {
	usage := parseOpenAIUsage(json.RawMessage(`{
		"input_tokens":10,
		"output_tokens":4,
		"total_tokens":14,
		"input_tokens_details":{"cached_tokens":3},
		"output_tokens_details":{"reasoning_tokens":2}
	}`))
	if usage.Get("input") != 10 || usage.Get("output") != 4 || usage.Get("total") != 14 || usage.Get("cache_input") != 3 || usage.Get("reasoning") != 2 {
		t.Fatalf("unexpected parsed usage: %#v", usage)
	}
}

func TestChatSendsAuthAndParsesResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" || r.Method != http.MethodPost {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("unexpected Authorization header: %q", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("invalid request JSON: %v", err)
		}
		if payload["model"] != "test-model" || payload["stream"] != false {
			t.Errorf("unexpected request payload: %#v", payload)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"chat-1","model":"test-model","created":1700000000,"system_fingerprint":"fp-test","choices":[{"index":0,"message":{"role":"assistant","content":"hello","reasoning_content":"thought"},"logprobs":{"content":[{"token":"hello"}]},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
	}))
	defer server.Close()

	response, err := NewOpenAIAdapter(Config{BaseURL: server.URL, APIKey: "secret", HTTPClient: server.Client()}).Chat(context.Background(), &protocol.ChatRequest{
		Model:    "test-model",
		Messages: []protocol.Message{{Role: "user", Content: []protocol.ContentPart{{Type: "text", Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	if response.ID != "chat-1" || response.Created != 1700000000 || response.SystemFingerprint != "fp-test" || len(response.Choices) != 1 || response.Choices[0].Message.Content[0].Text != "hello" || response.Choices[0].Message.ReasoningContent != "thought" || response.Usage.Get("input") != 2 || response.Usage.Get("output") != 1 {
		t.Fatalf("unexpected response: %#v", response)
	}
	if string(response.Choices[0].LogProbs) != `{"content":[{"token":"hello"}]}` {
		t.Fatalf("logprobs lost: %s", response.Choices[0].LogProbs)
	}
}

func TestStreamRequestSetsSSEAcceptHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != "text/event-stream" {
			t.Errorf("unexpected Accept header: %q", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	var events []protocol.StreamEvent
	err := NewOpenAIAdapter(Config{BaseURL: server.URL, HTTPClient: server.Client()}).ChatStream(context.Background(), &protocol.ChatRequest{Model: "model"}, func(event protocol.StreamEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("ChatStream returned error: %v", err)
	}
	if len(events) != 2 || events[0].Delta.Content[0].Text != "ok" || events[1].Type != "done" {
		t.Fatalf("unexpected stream events: %#v", events)
	}
}

func TestResponsesErrorIsClassified(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":"busy"}`)
	}))
	defer server.Close()

	_, err := NewOpenAIAdapter(Config{BaseURL: strings.TrimRight(server.URL, "/"), HTTPClient: server.Client()}).Responses(context.Background(), &protocol.ResponsesRequest{Model: "model"})
	if err == nil {
		t.Fatal("Responses returned nil error for HTTP 429")
	}
	upstream, ok := err.(*Error)
	if !ok || upstream.Type != "rate_limit" || upstream.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("unexpected classified error: %#v", err)
	}
}

func TestBuildChatPayloadMaxTokensField(t *testing.T) {
	maxTokens := 42
	for _, tc := range []struct {
		name, field, wantKey string
		stream               bool
	}{
		{name: "default nonstream", wantKey: "max_tokens"},
		{name: "default stream", wantKey: "max_tokens", stream: true},
		{name: "completion nonstream", field: "max_completion_tokens", wantKey: "max_completion_tokens"},
		{name: "completion stream", field: "max_completion_tokens", wantKey: "max_completion_tokens", stream: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := (&OpenAIAdapter{}).buildChatPayload(&protocol.ChatRequest{
				Model: "test", MaxTokens: &maxTokens, MaxTokensField: tc.field,
			}, tc.stream)
			if payload[tc.wantKey] != maxTokens {
				t.Fatalf("missing %s: %#v", tc.wantKey, payload)
			}
			other := "max_tokens"
			if tc.wantKey == other {
				other = "max_completion_tokens"
			}
			if _, ok := payload[other]; ok {
				t.Fatalf("unexpected %s: %#v", other, payload)
			}
			if tc.stream {
				options, ok := payload["stream_options"].(map[string]any)
				if !ok || options["include_usage"] != true {
					t.Fatalf("stream usage option lost: %#v", payload)
				}
			} else if _, ok := payload["stream_options"]; ok {
				t.Fatalf("nonstream includes stream_options: %#v", payload)
			}
		})
	}
	override := json.RawMessage(`{"include_usage":false}`)
	payload := (&OpenAIAdapter{}).buildChatPayload(&protocol.ChatRequest{Extra: map[string]json.RawMessage{"stream_options": override}}, true)
	if string(payload["stream_options"].(json.RawMessage)) != string(override) {
		t.Fatalf("extra stream_options override lost: %#v", payload)
	}
}

func TestParseOpenAIMessageReasoningAndToolIndex(t *testing.T) {
	msg, err := parseOpenAIMessage(json.RawMessage(`{"role":"assistant","content":null,"reasoning_content":"thinking","tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{}"}}]}`))
	if err != nil || msg.ReasoningContent != "thinking" || len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Index == nil || *msg.ToolCalls[0].Index != 0 {
		t.Fatalf("reasoning/tool index lost: %#v, %v", msg, err)
	}
	encoded, err := json.Marshal(msg.ToolCalls[0])
	if err != nil || !strings.Contains(string(encoded), `"index":0`) {
		t.Fatalf("index=0 omitted from JSON: %s, %v", encoded, err)
	}
	nullMsg, err := parseOpenAIMessage(json.RawMessage(`{"reasoning_content":null}`))
	if err != nil || nullMsg.ReasoningContent != "" {
		t.Fatalf("null reasoning not treated as empty: %#v, %v", nullMsg, err)
	}
	const secret = "do-not-echo-this"
	_, err = parseOpenAIMessage(json.RawMessage(`{"reasoning_content":{"private":"` + secret + `"}}`))
	if err == nil || !strings.Contains(err.Error(), "reasoning_content") || strings.Contains(err.Error(), secret) {
		t.Fatalf("unexpected reasoning type error: %v", err)
	}
}

func TestToOpenAIMessagesReasoningDoesNotSendToolIndex(t *testing.T) {
	index := 0
	msgs := (&OpenAIAdapter{}).toOpenAIMessages([]protocol.Message{{
		Role: "assistant", ReasoningContent: "thinking", ToolCalls: []protocol.ToolCall{{
			Index: &index, ID: "call-1", Type: "function", Function: protocol.ToolCallFunction{Name: "lookup", Arguments: "{}"},
		}},
	}})
	if len(msgs) != 1 || msgs[0]["reasoning_content"] != "thinking" {
		t.Fatalf("reasoning not sent: %#v", msgs)
	}
	calls := msgs[0]["tool_calls"].([]map[string]any)
	if _, ok := calls[0]["index"]; ok {
		t.Fatalf("nonstream tool call sent with index: %#v", calls[0])
	}
	if _, ok := (&OpenAIAdapter{}).toOpenAIMessages([]protocol.Message{{Role: "user"}})[0]["reasoning_content"]; ok {
		t.Fatal("empty reasoning_content emitted")
	}
}

func TestResponsesStreamRequiresTerminalEvent(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		wantError  bool
	}{
		{name: "partial then EOF", body: "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"part\"}\n\n", wantError: true},
		{name: "completed frame at EOF", body: "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\"}}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			var got []protocol.StreamEvent
			err := NewOpenAIAdapter(Config{BaseURL: server.URL, HTTPClient: server.Client()}).ResponsesStream(context.Background(), &protocol.ResponsesRequest{Model: "model"}, func(event protocol.StreamEvent) error {
				got = append(got, event)
				return nil
			})
			if tc.wantError {
				upstream, ok := err.(*Error)
				if !ok || upstream.StatusCode != http.StatusBadGateway || len(got) != 1 || len(got[0].Delta.Content) != 1 || got[0].Delta.Content[0].Text != "part" {
					t.Fatalf("partial Responses EOF: events=%#v err=%v", got, err)
				}
			} else if err != nil || len(got) != 1 || got[0].Type != "done" || got[0].Event != "response.completed" {
				t.Fatalf("completed Responses at EOF: events=%#v err=%v", got, err)
			}
		})
	}
}

// Only upstream Chat responses interpret reasoning as a reasoning_content alias;
// the exported request parser must keep treating it as an unknown extension.
func TestChatResponseReasoningAlias(t *testing.T) {
	for _, tc := range []struct {
		name, message, want string
		aliasRemoved        bool
	}{
		{"alias", `{"role":"assistant","reasoning":"thinking","content":"ok"}`, "thinking", true},
		{"canonical wins", `{"role":"assistant","reasoning_content":"canonical","reasoning":"alias"}`, "canonical", true},
		{"empty canonical", `{"role":"assistant","reasoning_content":"","reasoning":"alias"}`, "", true},
		{"null canonical", `{"role":"assistant","reasoning_content":null,"reasoning":"alias"}`, "", true},
		{"canonical only", `{"role":"assistant","reasoning_content":"canonical"}`, "canonical", false},
		{"no reasoning", `{"role":"assistant","content":"ok"}`, "", false},
		{"non-string alias", `{"role":"assistant","reasoning":{"private":"ignore"}}`, "", false},
		{"null alias", `{"role":"assistant","reasoning":null}`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, `{"id":"c-1","choices":[{"index":0,"message":`+tc.message+`,"finish_reason":"stop"}],"usage":{"completion_tokens_details":{"reasoning_tokens":2}}}`)
			}))
			defer upstream.Close()
			response, err := NewOpenAIAdapter(Config{BaseURL: upstream.URL, HTTPClient: upstream.Client()}).Chat(context.Background(), &protocol.ChatRequest{Model: "model"})
			if err != nil {
				t.Fatalf("Chat: %v", err)
			}
			if len(response.Choices) != 1 {
				t.Fatalf("Chat: response=%#v", response)
			}
			message := response.Choices[0].Message
			if message.ReasoningContent != tc.want || response.Usage.Get("reasoning") != 2 {
				t.Fatalf("response reasoning/usage: message=%#v usage=%#v", message, response.Usage)
			}
			_, aliasPresent := message.Extra["reasoning"]
			if tc.aliasRemoved && aliasPresent {
				t.Fatalf("valid response alias was left in Extra: %#v", message.Extra)
			}
			if tc.name == "non-string alias" && !aliasPresent {
				t.Fatalf("unrecognized extension was dropped: %#v", message.Extra)
			}
		})
	}

	requestMessage, err := ParseOpenAIMessage(json.RawMessage(`{"role":"assistant","reasoning":"inbound"}`))
	if err != nil || requestMessage.ReasoningContent != "" || string(requestMessage.Extra["reasoning"]) != `"inbound"` {
		t.Fatalf("inbound Chat parser changed: message=%#v err=%v", requestMessage, err)
	}
}
