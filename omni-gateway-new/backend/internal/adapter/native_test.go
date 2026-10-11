package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zjvtit/omni-gateway/internal/model"
	"github.com/zjvtit/omni-gateway/internal/protocol"
)

func TestNativeJSONAndCredentialMapping(t *testing.T) {
	for _, api := range []protocol.NativeAPI{protocol.MessagesAPI, protocol.EmbeddingsAPI, protocol.ImagesAPI} {
		t.Run(string(api), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				if body["model"] != "upstream" || body["extension"] != "keep" {
					t.Errorf("lost request fields %v", body)
				}
				if api == protocol.MessagesAPI {
					if r.Header.Get("x-api-key") != "secret" || r.URL.Path != "/v1/messages" {
						t.Error("Anthropic auth/path incorrect")
					}
				} else if r.Header.Get("Authorization") != "Bearer secret" {
					t.Error("missing upstream credential")
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"model":"upstream","data":[{"url":"image"}],"usage":{"input_tokens":5,"output_tokens":2},"extension":"keep"}`)
			}))
			defer s.Close()
			cfg := Config{BaseURL: s.URL, APIKey: "secret", HTTPClient: s.Client()}
			var a NativeAdapter = NewOpenAIAdapter(cfg)
			if api == protocol.MessagesAPI {
				a = NewAnthropicAdapter(cfg)
			}
			res, err := a.Native(context.Background(), &protocol.NativeRequest{API: api, Model: "upstream", Raw: json.RawMessage(`{"model":"alias","extension":"keep"}`)})
			if err != nil || !strings.Contains(string(res.Raw), `"extension":"keep"`) {
				t.Fatalf("response=%v err=%v", res, err)
			}
			if res.Usage.Get("input") != 5 {
				t.Fatal("usage missing")
			}
			if api == protocol.ImagesAPI && res.Usage.Get("image") != 1 {
				t.Fatal("image metering missing")
			}
		})
	}
}

func TestNativeSSEUsageAndTruncation(t *testing.T) {
	input := ": heartbeat\n\nevent: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"real\",\"usage\":{\"input_tokens\":10}}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":3}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":5}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	total := model.NewUsage()
	var frames []string
	err := parseNativeSSE(strings.NewReader(input), protocol.MessagesAPI, func(e protocol.NativeEvent) error {
		total = total.Add(e.Usage)
		frames = append(frames, string(e.Wire))
		return nil
	})
	if err != nil || total.Get("input") != 10 || total.Get("output") != 5 || total.Get("total") != 15 {
		t.Fatalf("usage=%v err=%v", total, err)
	}
	if len(frames) != 4 || !strings.HasPrefix(frames[0], "event: message_start\n") || strings.Contains(strings.Join(frames, ""), ": heartbeat") {
		t.Fatalf("non-data heartbeat forwarded or native events lost: %q", frames)
	}
	if err := parseNativeSSE(strings.NewReader("data: {\"type\":\"message_start\"}\n\n"), protocol.MessagesAPI, func(protocol.NativeEvent) error { return nil }); err == nil {
		t.Fatal("truncated stream accepted")
	}
	want := errors.New("client disconnected")
	if err := parseNativeSSE(strings.NewReader(input), protocol.MessagesAPI, func(protocol.NativeEvent) error { return want }); !errors.Is(err, want) {
		t.Fatal("callback failure lost")
	}
}

func TestNativeSSEIgnoresNonDataFramesButRejectsEmptyErrorEvents(t *testing.T) {
	for _, api := range []protocol.NativeAPI{protocol.MessagesAPI, protocol.ImagesAPI} {
		t.Run(string(api), func(t *testing.T) {
			var frames []protocol.NativeEvent
			input := ": upstream comment\n\nevent: ping\n\nevent: error\n\n"
			err := parseNativeSSE(strings.NewReader(input), api, func(event protocol.NativeEvent) error {
				frames = append(frames, event)
				return nil
			})
			if err == nil || len(frames) != 0 {
				t.Fatalf("empty error event: err=%v frames=%v", err, frames)
			}
		})
	}
}

func TestNativeSSERejectsForeignTerminalBeforeEmitting(t *testing.T) {
	for _, tc := range []struct {
		api      protocol.NativeAPI
		terminal string
	}{
		{protocol.ImagesAPI, "message_stop"},
		{protocol.MessagesAPI, "image_generation.completed"},
	} {
		t.Run(string(tc.api), func(t *testing.T) {
			calls := 0
			input := "data: {\"type\":\"" + tc.terminal + "\"}\n\n"
			err := parseNativeSSE(strings.NewReader(input), tc.api, func(protocol.NativeEvent) error {
				calls++
				return nil
			})
			if err == nil || calls != 0 {
				t.Fatalf("foreign terminal accepted: err=%v emissions=%d", err, calls)
			}
		})
	}
}

func TestMessagesConversionRejectsUnsupportedSemantics(t *testing.T) {
	base := `{"messages":[{"role":"user","content":"hello"}],"max_tokens":20}`
	chat, err := messagesToChat(&protocol.NativeRequest{Model: "m", Raw: json.RawMessage(base)})
	if err != nil || chat.Model != "m" {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"messages":[{"role":"user","content":"hi"}],"max_tokens":1,"tools":[{"name":"x","input_schema":{},"cache_control":{"type":"ephemeral"}}]}`, `{"messages":[{"role":"assistant","content":"prefill"}],"max_tokens":1}`, `{"messages":[{"role":"user","content":[{"type":"image"}]}],"max_tokens":1}`} {
		if _, err := messagesToChat(&protocol.NativeRequest{Raw: json.RawMessage(raw)}); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("unsupported fields lost: %v", err)
		}
	}
}

func TestRetryAfterFormats(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, tt := range []struct {
		value string
		want  time.Duration
	}{{"10", 10 * time.Second}, {now.Add(time.Minute).Format(http.TimeFormat), time.Minute}, {"9999999", 5 * time.Minute}, {"bad", 0}, {"-1", 0}, {"0", 0}} {
		if got := ParseRetryAfter(tt.value, now); got != tt.want {
			t.Errorf("%s: %v", tt.value, got)
		}
	}
}

type messagesUsageTestAdapter struct{ Adapter }

func (messagesUsageTestAdapter) ChatStream(_ context.Context, _ *protocol.ChatRequest, emit func(protocol.StreamEvent) error) error {
	for _, event := range []protocol.StreamEvent{
		{Type: "usage", Usage: model.Usage{Meters: map[string]int64{"input": 10}}},
		{Type: "usage", Usage: model.Usage{Meters: map[string]int64{"output": 3}}},
		{Type: "usage", Usage: model.Usage{Meters: map[string]int64{"output": 2}}},
		{Type: "done"},
	} {
		if err := emit(event); err != nil {
			return err
		}
	}
	return nil
}

func TestConvertedMessagesStreamReportsCumulativeUsageToClient(t *testing.T) {
	req := &protocol.NativeRequest{API: protocol.MessagesAPI, Model: "alias", Stream: true, Raw: json.RawMessage(`{"model":"alias","messages":[{"role":"user","content":"hi"}],"max_tokens":12,"stream":true}`)}
	var finalUsage map[string]int64
	var deltaCount int
	total := model.NewUsage()
	err := convertedMessagesStream(context.Background(), messagesUsageTestAdapter{}, req, func(event protocol.NativeEvent) error {
		total = total.Add(event.Usage)
		if !strings.HasPrefix(string(event.Wire), "event: message_delta\n") {
			return nil
		}
		var data struct {
			Usage map[string]int64 `json:"usage"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(strings.SplitN(string(event.Wire), "data: ", 2)[1], "data: "))), &data); err != nil {
			return err
		}
		deltaCount++
		finalUsage = data.Usage
		return nil
	})
	if err != nil || deltaCount != 4 || finalUsage["input_tokens"] != 10 || finalUsage["output_tokens"] != 5 || total.Get("input") != 10 || total.Get("output") != 5 {
		t.Fatalf("Messages usage mismatch: deltaCount=%d final=%v total=%v err=%v", deltaCount, finalUsage, total, err)
	}
}

func TestMessagesToolConversionAndLocalRejection(t *testing.T) {
	valid := `{"model":"alias","max_tokens":32,"tools":[{"name":"lookup","description":"search","input_schema":{"type":"object"}}],"tool_choice":{"type":"tool","name":"lookup"},"messages":[{"role":"user","content":"find"},{"role":"assistant","content":[{"type":"text","text":"checking"},{"type":"tool_use","id":"call_1","name":"lookup","input":{"q":"red"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":[{"type":"text","text":"found"}]},{"type":"text","text":"thanks"}]}]}`
	chat, err := messagesToChat(&protocol.NativeRequest{Model: "provider-model", Raw: json.RawMessage(valid)})
	if err != nil || len(chat.Messages) != 4 || len(chat.Tools) != 1 || chat.Messages[1].ToolCalls[0].ID != "call_1" || chat.Messages[2].Role != "tool" || chat.Messages[2].ToolCallID != "call_1" || chat.Messages[3].Content[0].Text != "thanks" || !strings.Contains(string(chat.ToolChoice), `"lookup"`) {
		t.Fatalf("converted request=%+v err=%v", chat, err)
	}
	for _, invalid := range []string{
		`{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled"}}`,
		`{"model":"m","max_tokens":1,"messages":[{"role":"user","content":[{"type":"thinking","thinking":"hidden","signature":"sig"}]}]}`,
		`{"model":"m","max_tokens":1,"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}]}`,
		`{"model":"m","max_tokens":1,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"orphan","content":"result"}]}]}`,
		`{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"x","input_schema":{}}],"tool_choice":{"type":"tool","name":"other"}}`,
		`{"model":"m","max_tokens":1,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"x","name":"f","input":[]}]},{"role":"user","content":"hi"}]}`,
	} {
		if _, err := messagesToChat(&protocol.NativeRequest{Raw: json.RawMessage(invalid)}); !errors.Is(err, ErrUnsupported) || !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("expected local unsupported invalid request, got %v: %s", err, invalid)
		}
	}
}

func TestConvertedMessagesToolsOpenAIAndGemini(t *testing.T) {
	for _, kind := range []string{"openai", "gemini"} {
		t.Run(kind, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var request map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if kind == "openai" {
					if r.URL.Path != "/chat/completions" || !strings.Contains(string(request["messages"]), `"tool_call_id":"call_1"`) || !strings.Contains(string(request["tool_choice"]), `"lookup"`) || !strings.Contains(string(request["tools"]), `"lookup"`) {
						t.Errorf("lost OpenAI tools: %v", request)
					}
					io.WriteString(w, `{"id":"msg_1","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_2","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"blue\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":8,"completion_tokens":3}}`)
				} else {
					if !strings.Contains(r.URL.Path, ":generateContent") || !strings.Contains(string(request["contents"]), `"functionResponse"`) || !strings.Contains(string(request["toolConfig"]), `"lookup"`) {
						t.Errorf("lost Gemini tools: %v", request)
					}
					io.WriteString(w, `{"candidates":[{"content":{"parts":[{"functionCall":{"id":"call_2","name":"lookup","args":{"q":"blue"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":3}}`)
				}
			}))
			defer server.Close()
			cfg := Config{BaseURL: server.URL, HTTPClient: server.Client()}
			var upstream NativeAdapter = NewOpenAIAdapter(cfg)
			if kind == "gemini" {
				upstream = NewGeminiAdapter(cfg)
			}
			raw := json.RawMessage(`{"model":"alias","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"lookup","input":{"q":"red"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"found"}]}],"tools":[{"name":"lookup","input_schema":{"type":"object"}}],"tool_choice":{"type":"tool","name":"lookup"},"max_tokens":32}`)
			res, err := upstream.Native(context.Background(), &protocol.NativeRequest{API: protocol.MessagesAPI, Model: "provider", Raw: raw})
			if err != nil {
				t.Fatal(err)
			}
			var result struct {
				Stop    string `json:"stop_reason"`
				Content []struct {
					Type, ID, Name string
					Input          map[string]string
				} `json:"content"`
				Usage struct {
					Input int `json:"input_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal(res.Raw, &result); err != nil || result.Stop != "tool_use" || len(result.Content) != 1 || result.Content[0].Type != "tool_use" || result.Content[0].ID != "call_2" || result.Content[0].Input["q"] != "blue" || result.Usage.Input != 8 || requests != 1 {
				t.Fatalf("result=%s err=%v requests=%d", res.Raw, err, requests)
			}
		})
	}
}

func TestConvertedMessagesRejectsUnknownBeforeUpstreamAndReasoningAfter(t *testing.T) {
	for _, kind := range []string{"openai", "gemini"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if kind == "openai" {
					io.WriteString(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"visible","reasoning_content":"secret"},"finish_reason":"stop"}]}`)
				} else {
					io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"secret","thought":true},{"text":"visible"}]},"finishReason":"STOP"}]}`)
				}
			}))
			defer s.Close()
			cfg := Config{BaseURL: s.URL, HTTPClient: s.Client()}
			var a NativeAdapter = NewOpenAIAdapter(cfg)
			if kind == "gemini" {
				a = NewGeminiAdapter(cfg)
			}
			for _, stream := range []bool{false, true} {
				req := &protocol.NativeRequest{API: protocol.MessagesAPI, Model: "m", Stream: stream, Raw: json.RawMessage(`{"model":"m","messages":[{"role":"user","content":[{"type":"thinking","thinking":"private","signature":"sig"}]}],"max_tokens":1}`)}
				if stream {
					err := a.NativeStream(context.Background(), req, func(protocol.NativeEvent) error { return nil })
					if !errors.Is(err, ErrUnsupported) {
						t.Fatalf("stream local rejection: %v", err)
					}
				} else if _, err := a.Native(context.Background(), req); !errors.Is(err, ErrUnsupported) {
					t.Fatalf("local rejection: %v", err)
				}
			}
			if calls != 0 {
				t.Fatalf("unsupported request reached upstream: %d", calls)
			}
			req := &protocol.NativeRequest{API: protocol.MessagesAPI, Model: "m", Raw: json.RawMessage(`{"model":"m","messages":[{"role":"user","content":"hi"}],"max_tokens":1}`)}
			if result, err := a.Native(context.Background(), req); err == nil || result != nil || calls != 1 {
				t.Fatalf("thinking silently discarded: result=%v err=%v calls=%d", result, err, calls)
			}
		})
	}
}

type messagesEventsAdapter struct {
	Adapter
	events []protocol.StreamEvent
	err    error
}

func (a messagesEventsAdapter) ChatStream(_ context.Context, _ *protocol.ChatRequest, emit func(protocol.StreamEvent) error) error {
	for _, event := range a.events {
		if err := emit(event); err != nil {
			return err
		}
	}
	return a.err
}

func TestConvertedMessagesStreamFunctionFragmentsAndErrorBoundary(t *testing.T) {
	req := &protocol.NativeRequest{API: protocol.MessagesAPI, Model: "m", Stream: true, Raw: json.RawMessage(`{"model":"m","messages":[{"role":"user","content":"hi"}],"max_tokens":10,"stream":true}`)}
	zero := 0
	tools := []protocol.StreamEvent{
		{Type: "chunk", Delta: protocol.Message{ToolCalls: []protocol.ToolCall{{Index: &zero, ID: "call_1", Type: "function", Function: protocol.ToolCallFunction{Name: "lookup", Arguments: `{"q":`}}}}},
		{Type: "chunk", Delta: protocol.Message{ToolCalls: []protocol.ToolCall{{Index: &zero, Type: "function", Function: protocol.ToolCallFunction{Arguments: `"red"}`}}}}},
		{Type: "chunk", FinishReason: "tool_calls"},
		{Type: "done"},
	}
	var wire strings.Builder
	err := convertedMessagesStream(context.Background(), messagesEventsAdapter{events: tools}, req, func(event protocol.NativeEvent) error { wire.Write(event.Wire); return nil })
	if err != nil || strings.Count(wire.String(), "event: content_block_start") != 1 || strings.Count(wire.String(), `"type":"input_json_delta"`) != 2 || !strings.Contains(wire.String(), `"stop_reason":"tool_use"`) || !strings.HasSuffix(wire.String(), "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n") || strings.Contains(wire.String(), "signature_delta") {
		t.Fatalf("stream=%s err=%v", wire.String(), err)
	}
	wire.Reset()
	err = convertedMessagesStream(context.Background(), messagesEventsAdapter{events: []protocol.StreamEvent{{Type: "error", Error: "secret"}}}, req, func(event protocol.NativeEvent) error { wire.Write(event.Wire); return nil })
	if err == nil || wire.Len() != 0 {
		t.Fatalf("first error emitted frame: %s %v", wire.String(), err)
	}
	wire.Reset()
	err = convertedMessagesStream(context.Background(), messagesEventsAdapter{events: []protocol.StreamEvent{{Type: "chunk", Delta: protocol.Message{Content: []protocol.ContentPart{{Type: "text", Text: "hello"}}}}, {Type: "error", Error: "secret"}}}, req, func(event protocol.NativeEvent) error { wire.Write(event.Wire); return nil })
	if err == nil || strings.Contains(wire.String(), "secret") || strings.Contains(wire.String(), "message_stop") {
		t.Fatalf("mid-stream error=%s %v", wire.String(), err)
	}
}

func TestConvertedMessagesRealOpenAIAndGeminiStreams(t *testing.T) {
	for _, kind := range []string{"openai", "gemini"} {
		t.Run(kind, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if kind == "openai" {
					io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{\\\"q\\\":\"}}]}}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"red\\\"}\"}}]}}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
				} else {
					// Gemini ends at EOF after a finishReason, not necessarily [DONE].
					io.WriteString(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"functionCall\":{\"id\":\"call_1\",\"name\":\"lookup\",\"args\":{\"q\":\"red\"}}}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":4,\"candidatesTokenCount\":3}}\n\n")
				}
			}))
			defer s.Close()
			cfg := Config{BaseURL: s.URL, HTTPClient: s.Client()}
			var a NativeAdapter = NewOpenAIAdapter(cfg)
			if kind == "gemini" {
				a = NewGeminiAdapter(cfg)
			}
			req := &protocol.NativeRequest{API: protocol.MessagesAPI, Model: "m", Stream: true, Raw: json.RawMessage(`{"model":"m","messages":[{"role":"user","content":"hi"}],"max_tokens":10,"stream":true}`)}
			var events []protocol.NativeEvent
			err := a.NativeStream(context.Background(), req, func(e protocol.NativeEvent) error { events = append(events, e); return nil })
			var wire strings.Builder
			for _, e := range events {
				wire.Write(e.Wire)
			}
			if err != nil || strings.Count(wire.String(), `"type":"input_json_delta"`) != 2 && kind == "openai" || strings.Count(wire.String(), `"type":"input_json_delta"`) != 1 && kind == "gemini" || !strings.Contains(wire.String(), `"stop_reason":"tool_use"`) || !strings.Contains(wire.String(), "event: message_stop\n") || strings.Contains(wire.String(), "signature_delta") {
				t.Fatalf("stream=%s err=%v", wire.String(), err)
			}
		})
	}
}

func TestNativeMessagesTerminalFrameStopsBeforeTrailingError(t *testing.T) {
	input := "event: message_start\ndata: {\"type\":\"message_start\"}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"real signature\"}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n" +
		"event: error\ndata: {\"type\":\"error\",\"error\":{\"message\":\"secret\"}}\n\n"
	var wire strings.Builder
	if err := parseNativeSSE(strings.NewReader(input), protocol.MessagesAPI, func(event protocol.NativeEvent) error { wire.Write(event.Wire); return nil }); err != nil {
		t.Fatal(err)
	}
	if strings.Count(wire.String(), "event: message_stop") != 1 || strings.Contains(wire.String(), "secret") || !strings.Contains(wire.String(), `"signature":"real signature"`) {
		t.Fatalf("native passthrough after terminal frame: %s", wire.String())
	}
}
