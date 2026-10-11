package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zjvtit/omni-gateway/internal/protocol"
)

type geminiToolTestPayload struct {
	Contents []struct {
		Role  string `json:"role"`
		Parts []struct {
			Text         string `json:"text"`
			FunctionCall *struct {
				ID   string          `json:"id"`
				Name string          `json:"name"`
				Args json.RawMessage `json:"args"`
			} `json:"functionCall"`
			FunctionResponse *struct {
				ID       string          `json:"id"`
				Name     string          `json:"name"`
				Response json.RawMessage `json:"response"`
			} `json:"functionResponse"`
		} `json:"parts"`
	} `json:"contents"`
}

func TestGeminiToolChatRoundTrip(t *testing.T) {
	var requests atomic.Int32
	posted := make(chan geminiToolTestPayload, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/gemini-upstream:generateContent" {
			t.Errorf("unexpected model path: %s", r.URL.Path)
		}
		if requests.Add(1) == 1 {
			_, _ = io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"think","thought":true},{"functionCall":{"id":"native-1","name":"weather","args":{"city":"Paris"}}},{"functionCall":{"name":"sum","args":{"x":2}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":3,"totalTokenCount":7,"thoughtsTokenCount":1}}`)
			return
		}
		var payload geminiToolTestPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode follow-up: %v", err)
		}
		posted <- payload
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"done"}]},"finishReason":"STOP"}]}`)
	}))
	defer server.Close()

	adapter := NewGeminiAdapter(Config{BaseURL: server.URL, HTTPClient: server.Client()})
	initial, err := adapter.Chat(context.Background(), &protocol.ChatRequest{Model: "gemini-upstream", Messages: []protocol.Message{{Role: "user", Content: []protocol.ContentPart{{Type: "text", Text: "query"}}}}})
	if err != nil {
		t.Fatalf("first Chat: %v", err)
	}
	assistant := initial.Choices[0].Message
	if assistant.ReasoningContent != "think" || len(assistant.Content) != 0 || initial.Choices[0].FinishReason != "tool_calls" || initial.Usage.Get("total") != 7 || initial.Usage.Get("reasoning") != 1 {
		t.Fatalf("thought/usage changed: reasoning=%q content=%v usage=%v", assistant.ReasoningContent, assistant.Content, initial.Usage.Meters)
	}
	calls := assistant.ToolCalls
	if len(calls) != 2 || calls[0].ID != "native-1" || calls[0].Function.Name != "weather" || calls[0].Function.Arguments != `{"city":"Paris"}` || calls[1].ID == "" || calls[1].ID == calls[0].ID || calls[1].Function.Name != "sum" || calls[0].Index == nil || *calls[0].Index != 0 || calls[1].Index == nil || *calls[1].Index != 1 {
		t.Fatalf("bad parallel calls: %+v", calls)
	}
	followup, err := adapter.Chat(context.Background(), &protocol.ChatRequest{Model: "gemini-upstream", Messages: []protocol.Message{
		{Role: "user", Content: []protocol.ContentPart{{Type: "text", Text: "query"}}}, assistant,
		{Role: "tool", ToolCallID: calls[1].ID, Content: []protocol.ContentPart{{Type: "text", Text: `{"value":2}`}}},
		{Role: "tool", ToolCallID: calls[0].ID, Content: []protocol.ContentPart{{Type: "text", Text: "cloudy (unquoted)"}}},
		{Role: "user", Content: []protocol.ContentPart{{Type: "text", Text: "summarize"}}},
	}})
	if err != nil || followup == nil || followup.Choices[0].Message.Content[0].Text != "done" {
		t.Fatalf("follow-up Chat: response=%v error=%v", followup, err)
	}
	payload := <-posted
	if requests.Load() != 2 || len(payload.Contents) != 5 || payload.Contents[0].Role != "user" || payload.Contents[1].Role != "model" || payload.Contents[4].Role != "user" || payload.Contents[4].Parts[0].Text != "summarize" {
		t.Fatalf("ordinary content/roles changed: requests=%d contents=%+v", requests.Load(), payload.Contents)
	}
	parts := payload.Contents[1].Parts
	if len(parts) != 2 || parts[0].FunctionCall == nil || parts[1].FunctionCall == nil || parts[0].FunctionCall.ID != calls[0].ID || parts[1].FunctionCall.ID != calls[1].ID {
		t.Fatalf("assistant calls not sent as Gemini functionCalls: %+v", parts)
	}
	for i, want := range []struct {
		id, name, response string
	}{{calls[1].ID, "sum", `{"value":2}`}, {calls[0].ID, "weather", `{"result":"cloudy (unquoted)"}`}} {
		content := payload.Contents[2+i]
		if content.Role != "user" || len(content.Parts) != 1 || content.Parts[0].FunctionResponse == nil {
			t.Fatalf("tool result %d was not a functionResponse: %+v", i, content)
		}
		got := content.Parts[0].FunctionResponse
		if got.ID != want.id || got.Name != want.name || string(got.Response) != want.response {
			t.Fatalf("wrong tool result %d: id=%q name=%q response=%s", i, got.ID, got.Name, got.Response)
		}
	}
}

func TestGeminiToolStreamIndicesAndUsage(t *testing.T) {
	stream := "data: " + `{"candidates":[{"content":{"parts":[{"functionCall":{"id":"native-a","name":"first","args":{}}},{"functionCall":{"name":"second","args":{}}}]}}],"usageMetadata":{"promptTokenCount":2,"totalTokenCount":2}}` + "\n\n" +
		"data: " + `{"candidates":[{"content":{"parts":[{"functionCall":{"id":"native-a","name":"first","args":{}}},{"functionCall":{"id":"native-b","name":"third","args":{}}},{"functionCall":{"name":"fourth","args":{}}},{"text":"plan","thought":true},{"text":"visible"}]} }],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1,"totalTokenCount":3}}` + "\n\n" +
		"data: " + `{"candidates":[{"content":{"parts":[]},"finishReason":"STOP"}]}` + "\n\n" +
		"data: [DONE]\n\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, stream)
	}))
	defer server.Close()

	var events []protocol.StreamEvent
	err := NewGeminiAdapter(Config{BaseURL: server.URL, HTTPClient: server.Client()}).ChatStream(context.Background(), &protocol.ChatRequest{Model: "gemini-upstream"}, func(e protocol.StreamEvent) error {
		events = append(events, e)
		return nil
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	var calls []protocol.ToolCall
	var usage []int64
	var thought, visible string
	var done, handoff bool
	for _, event := range events {
		switch event.Type {
		case "chunk":
			calls = append(calls, event.Delta.ToolCalls...)
			thought += event.Delta.ReasoningContent
			for _, part := range event.Delta.Content {
				visible += part.Text
			}
			handoff = handoff || event.FinishReason == "tool_calls"
		case "usage":
			usage = append(usage, event.Usage.Get("total"))
		case "done":
			done = true
		}
	}
	if len(calls) != 5 || thought != "plan" || visible != "visible" || !handoff || !done || !reflect.DeepEqual(usage, []int64{2, 1}) {
		t.Fatalf("stream changed: calls=%+v thought=%q text=%q usage=%v handoff=%v done=%v", calls, thought, visible, usage, handoff, done)
	}
	seen := map[string]bool{}
	for i, want := range []int{0, 1, 0, 2, 3} {
		if calls[i].Index == nil || *calls[i].Index != want || calls[i].ID == "" {
			t.Fatalf("call %d has incorrect ID/index: %+v", i, calls[i])
		}
		if i != 2 {
			if seen[calls[i].ID] {
				t.Fatalf("duplicate call ID for different calls: %q", calls[i].ID)
			}
			seen[calls[i].ID] = true
		}
	}
	if calls[0].ID != "native-a" || calls[2].ID != calls[0].ID || calls[3].ID != "native-b" || calls[1].ID == calls[4].ID {
		t.Fatalf("native/generated IDs were not stable: %+v", calls)
	}
	// Even a generated stream ID can be used by a later OpenAI tool message.
	payload, err := (&GeminiAdapter{}).buildPayload(&protocol.ChatRequest{Messages: []protocol.Message{
		{Role: "assistant", ToolCalls: []protocol.ToolCall{calls[1]}},
		{Role: "tool", ToolCallID: calls[1].ID, Content: []protocol.ContentPart{{Type: "text", Text: "ok"}}},
	}})
	if err != nil {
		t.Fatalf("stream tool follow-up: %v", err)
	}
	contents := payload["contents"].([]map[string]any)
	response := contents[1]["parts"].([]map[string]any)[0]["functionResponse"].(map[string]any)
	if response["id"] != calls[1].ID || response["name"] != "second" {
		t.Fatalf("stream ID not returned to Gemini functionResponse")
	}
}

func TestGeminiToolChoice(t *testing.T) {
	tools := []protocol.Tool{{Type: "function", Function: protocol.ToolFunction{Name: "lookup", Parameters: json.RawMessage(`{"type":"object"}`)}}}
	for _, tc := range []struct {
		name      string
		choice    string
		tools     []protocol.Tool
		mode      string
		allowed   []string
		errorPart string
		forbidden string // caller-provided text that must not be echoed
	}{
		{"default", "", tools, "", nil, "", ""},
		{"auto", `"auto"`, tools, "AUTO", nil, "", ""},
		{"none", `"none"`, tools, "NONE", nil, "", ""},
		{"required", `"required"`, tools, "ANY", nil, "", ""},
		{"named", `{"type":"function","function":{"name":"lookup"}}`, tools, "ANY", []string{"lookup"}, "", ""},
		{"no tools auto", `"auto"`, nil, "", nil, "", ""},
		{"no tools none", `"none"`, nil, "", nil, "", ""},
		{"no tools required", `"required"`, nil, "", nil, "needs declared tools", ""},
		{"no tools named", `{"type":"function","function":{"name":"lookup"}}`, nil, "", nil, "not declared", "lookup"},
		{"unknown name", `{"type":"function","function":{"name":"missing"}}`, tools, "", nil, "not declared", "missing"},
		{"unknown mode", `"sometimes"`, tools, "", nil, "unsupported", "sometimes"},
		{"invalid name", `{"type":"function","function":{}}`, tools, "", nil, "invalid", ""},
		{"invalid shape", `42`, tools, "", nil, "invalid", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := (&GeminiAdapter{}).buildPayload(&protocol.ChatRequest{Tools: tc.tools, ToolChoice: json.RawMessage(tc.choice)})
			if tc.errorPart != "" {
				if err == nil || !errors.Is(err, ErrInvalidRequest) || !strings.Contains(err.Error(), tc.errorPart) {
					t.Fatalf("wanted local ErrInvalidRequest containing %q; got %v", tc.errorPart, err)
				}
				if tc.forbidden != "" && strings.Contains(err.Error(), tc.forbidden) {
					t.Fatalf("error text leaks request data %q: %v", tc.forbidden, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("buildPayload: %v", err)
			}
			_, hasTools := payload["tools"]
			if hasTools != (len(tc.tools) > 0) {
				t.Fatalf("unexpected tools presence: %v", hasTools)
			}
			value, configured := payload["toolConfig"]
			if configured != (tc.mode != "") {
				t.Fatalf("toolConfig presence: %v", configured)
			}
			if configured {
				config := value.(map[string]any)["functionCallingConfig"].(map[string]any)
				if config["mode"] != tc.mode || (tc.allowed != nil && !reflect.DeepEqual(config["allowedFunctionNames"], tc.allowed)) || (tc.allowed == nil && config["allowedFunctionNames"] != nil) {
					t.Fatalf("wrong calling config: %v", config)
				}
			}
			if _, err := json.Marshal(payload); err != nil {
				t.Fatalf("invalid Gemini payload: %v", err)
			}
		})
	}
}

func TestGeminiToolResultSafeJSON(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{`{"valid":true}`, `{"valid":true}`},
		{`[1,2]`, `{"result":"[1,2]"}`},
		{`null`, `{"result":"null"}`},
		{`unquoted "value"`, `{"result":"unquoted \"value\""}`},
		{"", `{"result":""}`},
	} {
		t.Run(tc.text, func(t *testing.T) {
			response, err := geminiToolResponse([]protocol.ContentPart{{Type: "text", Text: tc.text}})
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(response)
			if err != nil || string(encoded) != tc.want {
				t.Fatalf("wrong safe response: %s, %v", encoded, err)
			}
		})
	}
}

// Locally refused Gemini tool requests never reach the upstream, wrap
// ErrInvalidRequest, and keep provider-visible IDs and URLs out of the error
// text.
func TestGeminiLocalToolErrorsDoNotReachUpstream(t *testing.T) {
	const (
		unknownID = "secret-missing-tool-call"
		laterID   = "secret-later-tool-call"
		imageURL  = "https://secret.example/tool.png"
	)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	adapter := NewGeminiAdapter(Config{BaseURL: server.URL, HTTPClient: server.Client()})
	for _, tc := range []struct {
		name      string
		wantPart  string
		forbidden []string
		messages  []protocol.Message
	}{
		{"unknown id", "tool_call_id", []string{unknownID}, []protocol.Message{{Role: "tool", ToolCallID: unknownID, Content: []protocol.ContentPart{{Type: "text", Text: "result"}}}}},
		{"not preceding", "tool_call_id", []string{laterID}, []protocol.Message{{Role: "tool", ToolCallID: laterID}, {Role: "assistant", ToolCalls: []protocol.ToolCall{{ID: laterID, Function: protocol.ToolCallFunction{Name: "f"}}}}}},
		{"missing id", "tool_call_id", nil, []protocol.Message{{Role: "assistant", ToolCalls: []protocol.ToolCall{{ID: "known", Function: protocol.ToolCallFunction{Name: "f"}}}}, {Role: "tool", Content: []protocol.ContentPart{{Type: "text", Text: "result"}}}}},
		{
			"non-text result",
			"only supports text content",
			[]string{"secret.example"},
			[]protocol.Message{
				{Role: "assistant", ToolCalls: []protocol.ToolCall{{ID: "known", Function: protocol.ToolCallFunction{Name: "f"}}}},
				{Role: "tool", ToolCallID: "known", Content: []protocol.ContentPart{{Type: "image_url", ImageURL: imageURL}}},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := &protocol.ChatRequest{Model: "gemini-upstream", Messages: tc.messages}
			check := func(err error) {
				t.Helper()
				if err == nil || !errors.Is(err, ErrInvalidRequest) || !strings.Contains(err.Error(), tc.wantPart) {
					t.Fatalf("wanted local ErrInvalidRequest containing %q, got %v", tc.wantPart, err)
				}
				for _, forbidden := range tc.forbidden {
					if strings.Contains(err.Error(), forbidden) {
						t.Fatalf("error text leaks request data %q: %v", forbidden, err)
					}
				}
			}
			_, err := adapter.Chat(context.Background(), req)
			check(err)
			check(adapter.ChatStream(context.Background(), req, func(protocol.StreamEvent) error { return nil }))
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("unexpected upstream requests: %d", requests.Load())
	}
}

// A cross-protocol Extra field must never overwrite a payload key the adapter
// builds itself, and the refusal is static and local.
func TestGeminiReservedExtraFieldsRejected(t *testing.T) {
	reserved := []string{"contents", "systemInstruction", "generationConfig", "tools", "toolConfig"}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	adapter := NewGeminiAdapter(Config{BaseURL: server.URL, HTTPClient: server.Client()})
	var errorText string
	for _, key := range reserved {
		req := &protocol.ChatRequest{
			Model:    "gemini-upstream",
			Messages: []protocol.Message{{Role: "user", Content: []protocol.ContentPart{{Type: "text", Text: "hi"}}}},
			Extra:    map[string]json.RawMessage{key: json.RawMessage(`"secret-extra-value"`)},
		}
		for _, stream := range []bool{false, true} {
			var err error
			if stream {
				err = adapter.ChatStream(context.Background(), req, func(protocol.StreamEvent) error { return nil })
			} else {
				_, err = adapter.Chat(context.Background(), req)
			}
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("Extra %q stream=%v: error = %v, want ErrInvalidRequest", key, stream, err)
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
	}
	if requests.Load() != 0 {
		t.Fatalf("reserved Extra reached the upstream %d times, want 0", requests.Load())
	}
}

// Non-reserved Extra fields keep their passthrough behavior.
func TestGeminiNonReservedExtraPassesThrough(t *testing.T) {
	payload, err := (&GeminiAdapter{}).buildPayload(&protocol.ChatRequest{
		Messages: []protocol.Message{{Role: "user", Content: []protocol.ContentPart{{Type: "text", Text: "hi"}}}},
		Extra: map[string]json.RawMessage{
			"safetySettings": json.RawMessage(`[{"category":"HARM_CATEGORY_HATE_SPEECH","threshold":"BLOCK_ONLY_HIGH"}]`),
			"cachedContent":  json.RawMessage(`"cachedContents/abc"`),
		},
	})
	if err != nil {
		t.Fatalf("non-reserved Extra was rejected: %v", err)
	}
	contents, ok := payload["contents"].([]map[string]any)
	if !ok || len(contents) != 1 {
		t.Fatalf("adapter contents field changed: %#v", payload["contents"])
	}
	if raw, ok := payload["cachedContent"].(json.RawMessage); !ok || string(raw) != `"cachedContents/abc"` {
		t.Fatalf("cachedContent extra = %#v", payload["cachedContent"])
	}
	if _, ok := payload["safetySettings"].(json.RawMessage); !ok {
		t.Fatalf("safetySettings extra = %#v", payload["safetySettings"])
	}
	if _, err := json.Marshal(payload); err != nil {
		t.Fatalf("invalid payload with passthrough Extra: %v", err)
	}
}

func TestGeminiOrdinaryPayload(t *testing.T) {
	adapter := &GeminiAdapter{}
	payload, err := adapter.buildPayload(&protocol.ChatRequest{Messages: []protocol.Message{
		{Role: "system", Content: []protocol.ContentPart{{Type: "text", Text: "system"}}},
		{Role: "user", Content: []protocol.ContentPart{{Type: "text", Text: "hello"}}},
		{Role: "assistant", Content: []protocol.ContentPart{{Type: "text", Text: "world"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	contents := payload["contents"].([]map[string]any)
	if len(contents) != 2 || contents[0]["role"] != "user" || contents[1]["role"] != "model" || contents[0]["parts"].([]map[string]any)[0]["text"] != "hello" || contents[1]["parts"].([]map[string]any)[0]["text"] != "world" {
		t.Fatalf("ordinary messages changed: %v", contents)
	}
	if payload["systemInstruction"].(map[string]any)["parts"].([]map[string]any)[0]["text"] != "system" {
		t.Fatalf("system instruction lost")
	}
	if _, ok := payload["toolConfig"]; ok {
		t.Fatalf("unexpected tool config for ordinary chat")
	}
}

func TestGeminiFinishReasonToolHandoff(t *testing.T) {
	for _, tc := range []struct {
		reason string
		tools  bool
		want   string
	}{
		{"STOP", true, "tool_calls"},
		{"STOP", false, "stop"},
		{"MAX_TOKENS", true, "length"},
		{"MAX_TOKENS", false, "length"},
	} {
		if got := geminiFinishReason(tc.reason, tc.tools); got != tc.want {
			t.Errorf("finish reason %q with tools=%v: got %q, want %q", tc.reason, tc.tools, got, tc.want)
		}
	}
}

func TestGeminiImageDataURLAndInvalidImage(t *testing.T) {
	for _, tc := range []struct {
		name string
		part protocol.ContentPart
		key  string
		want map[string]any
	}{
		{"data URL", protocol.ContentPart{Type: "image_url", ImageURL: "data:image/png;base64,aGVsbG8="}, "inlineData", map[string]any{"mimeType": "image/png", "data": "aGVsbG8="}},
		{"raw base64", protocol.ContentPart{Type: "input_image", ImageData: "aGVsbG8=", MediaType: "image/jpeg"}, "inlineData", map[string]any{"mimeType": "image/jpeg", "data": "aGVsbG8="}},
		{"remote URI", protocol.ContentPart{Type: "image_url", ImageURL: "https://example.invalid/image.png"}, "fileData", map[string]any{"fileUri": "https://example.invalid/image.png"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := (&GeminiAdapter{}).buildPayload(&protocol.ChatRequest{Messages: []protocol.Message{{Role: "user", Content: []protocol.ContentPart{tc.part}}}})
			if err != nil {
				t.Fatal(err)
			}
			parts := payload["contents"].([]map[string]any)[0]["parts"].([]map[string]any)
			if len(parts) != 1 || !reflect.DeepEqual(parts[0][tc.key], tc.want) {
				t.Fatalf("image part mapping: %v, want %s=%v", parts, tc.key, tc.want)
			}
		})
	}
	var upstreamCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	a := NewGeminiAdapter(Config{BaseURL: server.URL, HTTPClient: server.Client()})
	for _, imageURL := range []string{"data:image/png;base64,not-base64-secret", "data:text/plain;base64,aGVsbG8=", "data:image/png,aGVsbG8=", "DATA:image/png;base64,aGVsbG8="} {
		req := &protocol.ChatRequest{Model: "gemini-upstream", Messages: []protocol.Message{{Role: "user", Content: []protocol.ContentPart{{Type: "image_url", ImageURL: imageURL}}}}}
		_, err := a.Chat(context.Background(), req)
		if !errors.Is(err, ErrInvalidRequest) || strings.Contains(err.Error(), "not-base64-secret") {
			t.Fatalf("invalid image must be refused without echo: %v", err)
		}
		err = a.ChatStream(context.Background(), req, func(protocol.StreamEvent) error { return nil })
		if !errors.Is(err, ErrInvalidRequest) || strings.Contains(err.Error(), "not-base64-secret") {
			t.Fatalf("invalid stream image must be refused without echo: %v", err)
		}
	}
	if upstreamCalls.Load() != 0 {
		t.Fatalf("invalid images reached upstream %d times", upstreamCalls.Load())
	}
}

func TestGeminiIdlessCallsWithSameNameStayDistinct(t *testing.T) {
	state := &geminiCallState{}
	var first, second protocol.Message
	appendGeminiPart(&first, "", false, json.RawMessage(`{"name":"same","args":{}}`), state)
	appendGeminiPart(&second, "", false, json.RawMessage(`{"name":"same","args":{}}`), state)
	if len(first.ToolCalls) != 1 || len(second.ToolCalls) != 1 || first.ToolCalls[0].ID == second.ToolCalls[0].ID || *first.ToolCalls[0].Index != 0 || *second.ToolCalls[0].Index != 1 {
		t.Fatalf("ambiguous ID-less calls were conflated: first=%+v second=%+v", first.ToolCalls, second.ToolCalls)
	}
}
