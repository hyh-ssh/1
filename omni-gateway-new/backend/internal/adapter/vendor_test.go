package adapter

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zjvtit/omni-gateway/internal/protocol"
)

func TestAnthropicAdapterChatAndStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "anthropic-secret" || r.Header.Get("anthropic-version") == "" {
			t.Errorf("unexpected Anthropic request: path=%s headers=%v", r.URL.Path, r.Header)
		}
		if r.Header.Get("Accept") == "text/event-stream" {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n")
			_, _ = io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n")
			_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg-1","model":"claude","stop_reason":"end_turn","content":[{"type":"text","text":"hello"}],"usage":{"input_tokens":3,"output_tokens":2}}`)
	}))
	defer server.Close()
	adapter := NewAnthropicAdapter(Config{BaseURL: server.URL + "/v1", APIKey: "anthropic-secret", TimeoutMS: 1000})
	response, err := adapter.Chat(context.Background(), &protocol.ChatRequest{Model: "claude", Messages: []protocol.Message{{Role: "user", Content: []protocol.ContentPart{{Type: "text", Text: "hi"}}}}})
	if err != nil || response == nil || response.Choices[0].Message.Content[0].Text != "hello" || response.Usage.Get("input") != 3 {
		t.Fatalf("unexpected Anthropic response: %#v %v", response, err)
	}
	var events []protocol.StreamEvent
	err = adapter.ChatStream(context.Background(), &protocol.ChatRequest{Model: "claude", Messages: []protocol.Message{{Role: "user"}}}, func(event protocol.StreamEvent) error { events = append(events, event); return nil })
	if err != nil || len(events) != 4 || events[0].Delta.Content[0].Text != "hi" || events[3].Type != "done" {
		t.Fatalf("unexpected Anthropic stream: %#v %v", events, err)
	}
}

func TestGeminiAdapterChatAndStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, ":generateContent") && !strings.Contains(r.URL.Path, ":streamGenerateContent") {
			t.Errorf("unexpected Gemini path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("key") != "gemini-secret" {
			t.Errorf("missing Gemini key: %s", r.URL.RawQuery)
		}
		if strings.Contains(r.URL.Path, ":streamGenerateContent") {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}]}}]}\n\n")
			_, _ = io.WriteString(w, "data: {\"candidates\":[{\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":1,\"candidatesTokenCount\":1,\"totalTokenCount\":2}}\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1,"totalTokenCount":3}}`)
	}))
	defer server.Close()
	adapter := NewGeminiAdapter(Config{BaseURL: server.URL, APIKey: "gemini-secret", TimeoutMS: 1000})
	response, err := adapter.Chat(context.Background(), &protocol.ChatRequest{Model: "gemini-pro", Messages: []protocol.Message{{Role: "user", Content: []protocol.ContentPart{{Type: "text", Text: "hi"}}}}})
	if err != nil || response == nil || response.Choices[0].Message.Content[0].Text != "hello" || response.Usage.Get("total") != 3 {
		t.Fatalf("unexpected Gemini response: %#v %v", response, err)
	}
	var events []protocol.StreamEvent
	err = adapter.ChatStream(context.Background(), &protocol.ChatRequest{Model: "gemini-pro", Messages: []protocol.Message{{Role: "user"}}}, func(event protocol.StreamEvent) error { events = append(events, event); return nil })
	if err != nil || len(events) != 3 || events[0].Delta.Content[0].Text != "hi" || events[2].Usage.Get("total") != 2 {
		t.Fatalf("unexpected Gemini stream: %#v %v", events, err)
	}
}
