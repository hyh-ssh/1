package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zjvtit/omni-gateway/internal/adapter"
	llmgateway "github.com/zjvtit/omni-gateway/internal/gateway"
	"github.com/zjvtit/omni-gateway/internal/protocol"
)

func postResponses(t *testing.T, service *fakeChatService, body string) *httptest.ResponseRecorder {
	t.Helper()
	handler := NewHandlerWithGateway(nil, nil, gatewayAPIKeyStore(), service, AuthConfig{})
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-gateway")
	handler.ServeHTTP(recorder, req)
	return recorder
}

func TestResponsesInputArrayIsNormalizedForTheService(t *testing.T) {
	service := &fakeChatService{response: &protocol.ResponsesResponse{ID: "resp-1", Status: "completed"}}
	body := `{"model":"chat","input":[{"role":"user","content":"hi"},{"type":"function_call_output","call_id":"call-1","output":"42"}]}`
	recorder := postResponses(t, service, body)

	if recorder.Code != http.StatusOK {
		t.Fatalf("got status %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	got := service.gotResponsesRequest
	if got == nil {
		t.Fatal("service never received the Responses request")
	}
	if len(got.Messages) != 2 {
		t.Fatalf("got %d normalized messages, want 2: %#v", len(got.Messages), got.Messages)
	}
	if got.Messages[0].Role != "user" || got.Messages[0].Content[0].Text != "hi" {
		t.Fatalf("unexpected first message: %#v", got.Messages[0])
	}
	if got.Messages[1].Role != "tool" || got.Messages[1].ToolCallID != "call-1" {
		t.Fatalf("function call output was not normalized: %#v", got.Messages[1])
	}
	// The raw input is still forwarded verbatim, so a passthrough never loses
	// fields the gateway does not model.
	if !strings.Contains(string(got.Input), `"function_call_output"`) {
		t.Fatalf("raw input was not preserved: %s", got.Input)
	}
}

func TestResponsesStringInputBecomesAMessage(t *testing.T) {
	service := &fakeChatService{response: &protocol.ResponsesResponse{ID: "resp-1", Status: "completed"}}
	recorder := postResponses(t, service, `{"model":"chat","input":"hello"}`)

	if recorder.Code != http.StatusOK {
		t.Fatalf("got status %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	got := service.gotResponsesRequest
	if got == nil || len(got.Messages) != 1 || got.Messages[0].Role != "user" || got.Messages[0].Content[0].Text != "hello" {
		t.Fatalf("string input was not normalized: %#v", got)
	}
}

func TestResponsesRejectsMalformedInput(t *testing.T) {
	for _, body := range []string{
		`{"model":"chat","input":42}`,
		`{"model":"chat","input":{"role":"user"}}`,
		`{"model":"chat","input":[1,2]}`,
		`{"model":"chat","input":"  "}`,
	} {
		service := &fakeChatService{response: &protocol.ResponsesResponse{ID: "resp-1"}}
		recorder := postResponses(t, service, body)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("input %s: got status %d, want %d: %s", body, recorder.Code, http.StatusBadRequest, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), `"code":"invalid_request"`) || !strings.Contains(recorder.Body.String(), "invalid input") {
			t.Fatalf("input %s: unexpected error envelope: %s", body, recorder.Body.String())
		}
		if service.gotResponsesRequest != nil {
			t.Fatalf("input %s: malformed input reached the service", body)
		}
	}
}

func TestResponsesStreamSynthesizesToolCallArgumentDelta(t *testing.T) {
	service := &fakeChatService{responseEvents: []protocol.StreamEvent{
		{Type: "chunk", ToolCalls: []protocol.ToolCallDelta{{Index: 0, ID: "call-1", Name: "lookup", Arguments: `{"q":`}}},
		{Type: "done", Raw: json.RawMessage(`{"type":"response.completed","response":{"id":"resp-1","model":"provider-model"}}`)},
	}}
	recorder := postResponses(t, service, `{"model":"chat","input":"hi","stream":true}`)

	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("unexpected stream response: %d %q %s", recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, want := range []string{"response.function_call_arguments.delta", `"item_id":"call-1"`, `"name":"lookup"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("Responses SSE body is missing %q: %s", want, body)
		}
	}
}

func TestResponsesStreamProxiesUpstreamFunctionCallDeltas(t *testing.T) {
	raw := `{"type":"response.function_call_arguments.delta","output_index":1,"item_id":"item-1","model":"provider-model","delta":"{\"q\":"}`
	service := &fakeChatService{responseEvents: []protocol.StreamEvent{
		{Type: "chunk", Event: "response.function_call_arguments.delta", ResponseID: "resp-1", Index: 1, Raw: json.RawMessage(raw)},
		{Type: "done", Raw: json.RawMessage(`{"type":"response.completed","response":{"id":"resp-1"}}`)},
	}}
	recorder := postResponses(t, service, `{"model":"chat","input":"hi","stream":true}`)

	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected stream response: %d %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `"type":"response.function_call_arguments.delta"`) {
		t.Fatalf("upstream function-call delta was not proxied: %s", body)
	}
	if !strings.Contains(body, `"model":"chat"`) {
		t.Fatalf("proxied event did not expose the requested alias: %s", body)
	}
}

func TestResponsesDeltasEmitOneFramePerToolCallFragment(t *testing.T) {
	frames := makeResponsesDeltas(protocol.StreamEvent{
		Index: 2,
		ToolCalls: []protocol.ToolCallDelta{
			{Index: 2, ID: "call-a", Name: "alpha", Arguments: `{"x":`},
			{Index: 3, ID: "call-b", Name: "beta", Arguments: `1}`},
		},
	})
	if len(frames) != 2 {
		t.Fatalf("got %d frames, want 2: %#v", len(frames), frames)
	}
	for index, frame := range frames {
		var payload map[string]any
		if err := json.Unmarshal(frame, &payload); err != nil {
			t.Fatalf("frame %d is not JSON: %v", index, err)
		}
		if payload["type"] != "response.function_call_arguments.delta" {
			t.Fatalf("frame %d has type %v", index, payload["type"])
		}
	}
}

func TestResponsesDeltasFallBackToOutputText(t *testing.T) {
	frames := makeResponsesDeltas(protocol.StreamEvent{
		Delta: protocol.Message{Content: []protocol.ContentPart{{Type: "text", Text: "hi"}}},
	})
	if len(frames) != 1 {
		t.Fatalf("got %d frames, want 1: %#v", len(frames), frames)
	}
	var payload map[string]any
	if err := json.Unmarshal(frames[0], &payload); err != nil {
		t.Fatalf("frame is not JSON: %v", err)
	}
	if payload["type"] != "response.output_text.delta" || payload["delta"] != "hi" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
}

func TestResponsesUnsupportedProviderIsRefusedWith501(t *testing.T) {
	for _, stream := range []string{"false", "true"} {
		service := &fakeChatService{responseErr: llmgateway.ErrResponsesUnsupported, responseStreamErr: llmgateway.ErrResponsesUnsupported}
		handler := NewHandlerWithGateway(nil, nil, gatewayAPIKeyStore(), service, AuthConfig{})
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"chat","input":"hi","stream":`+stream+`}`))
		req.Header.Set("Authorization", "Bearer sk-gateway")
		req.Header.Set(RequestIDHeader, "req-responses-unsupported")
		handler.ServeHTTP(recorder, req)

		if recorder.Code != http.StatusNotImplemented {
			t.Fatalf("stream=%s: got status %d, want %d: %s", stream, recorder.Code, http.StatusNotImplemented, recorder.Body.String())
		}
		var payload struct {
			Error struct {
				Type      string `json:"type"`
				Code      string `json:"code"`
				Message   string `json:"message"`
				RequestID string `json:"request_id"`
			} `json:"error"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatalf("stream=%s: decode error body: %v (%s)", stream, err, recorder.Body.String())
		}
		if payload.Error.Type != "server_error" || payload.Error.Code != "responses_unsupported" || payload.Error.RequestID != recorder.Header().Get("X-Gateway-Request-ID") {
			t.Fatalf("stream=%s: unexpected error envelope: %s", stream, recorder.Body.String())
		}
		if !strings.Contains(payload.Error.Message, "does not implement the OpenAI Responses API") {
			t.Fatalf("stream=%s: error envelope is missing the refusal reason: %s", stream, recorder.Body.String())
		}
	}
}

// The mock adapter sees the provider model, while HTTP clients see the public
// alias. The production gateway performs this substitution when routing.
type mockResponsesService struct {
	*fakeChatService
	upstream *adapter.OpenAIAdapter
	seen     *protocol.ResponsesRequest
}

func (s *mockResponsesService) providerRequest(req *protocol.ResponsesRequest) *protocol.ResponsesRequest {
	s.seen = req
	copy := *req
	copy.Model = "provider-model"
	return &copy
}

func (s *mockResponsesService) Responses(ctx context.Context, req *protocol.ResponsesRequest) (*protocol.ResponsesResponse, error) {
	return s.upstream.Responses(ctx, s.providerRequest(req))
}

func (s *mockResponsesService) ResponsesStream(ctx context.Context, req *protocol.ResponsesRequest, emit func(protocol.StreamEvent) error) error {
	return s.upstream.ResponsesStream(ctx, s.providerRequest(req), emit)
}

type flushingResponsesRecorder struct {
	*httptest.ResponseRecorder
	flushes int
}

func (w *flushingResponsesRecorder) Flush() {
	w.flushes++
	w.ResponseRecorder.Flush()
}

func postResponsesWithFlush(t *testing.T, service ChatService, body string) *flushingResponsesRecorder {
	t.Helper()
	handler := NewHandlerWithGateway(nil, nil, gatewayAPIKeyStore(), service, AuthConfig{})
	recorder := &flushingResponsesRecorder{ResponseRecorder: httptest.NewRecorder()}
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-gateway")
	req.Header.Set(RequestIDHeader, "req-responses-mock")
	handler.ServeHTTP(recorder, req)
	return recorder
}

type responsesSSEFrame struct {
	name string
	data map[string]json.RawMessage
}

func parseResponsesSSEFrames(t *testing.T, body string) []responsesSSEFrame {
	t.Helper()
	if body == "" || !strings.HasSuffix(body, "\n\n") {
		t.Fatalf("invalid SSE framing: %q", body)
	}
	parts := strings.Split(strings.TrimSuffix(body, "\n\n"), "\n\n")
	frames := make([]responsesSSEFrame, 0, len(parts))
	for _, part := range parts {
		var frame responsesSSEFrame
		for _, line := range strings.Split(part, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				frame.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame.data); err != nil {
					t.Fatalf("invalid Responses SSE payload: %s: %v", line, err)
				}
			default:
				t.Fatalf("unexpected Responses SSE field: %s", line)
			}
		}
		if frame.data == nil {
			t.Fatalf("missing SSE data in %q", part)
		}
		frames = append(frames, frame)
	}
	return frames
}

func TestResponsesMockUpstreamRebuildsRequestAndPreservesRawSSE(t *testing.T) {
	requests := make(chan map[string]json.RawMessage, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/responses" || r.Header.Get("Authorization") != "Bearer upstream-secret" || r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("unexpected upstream request: %s %s %v", r.Method, r.URL.Path, r.Header)
			http.Error(w, "unexpected upstream request", http.StatusBadRequest)
			return
		}
		var payload map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode upstream request: %v", err)
			http.Error(w, "invalid upstream request", http.StatusBadRequest)
			return
		}
		requests <- payload
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []string{
			`{"type":"response.created","response":{"id":"resp-1","model":"provider-model"}}`,
			`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","call_id":"call-1","id":"item-1","name":"lookup","arguments":""}}`,
			`{"type":"response.function_call_arguments.delta","output_index":1,"item_id":"item-1","delta":"{\"q\":"}`,
			`{"type":"response.function_call_arguments.delta","output_index":1,"item_id":"item-1","delta":"\"x\"}"}`,
			`{"type":"response.output_text.delta","output_index":0,"delta":"hello","model":"provider-model"}`,
			`{"type":"response.completed","response":{"id":"resp-1","model":"provider-model","usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5,"output_tokens_details":{"reasoning_tokens":1}}}}`,
		} {
			var fields struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal([]byte(event), &fields)
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", fields.Type, event)
			w.(http.Flusher).Flush()
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()
	service := &mockResponsesService{fakeChatService: &fakeChatService{}, upstream: adapter.NewOpenAIAdapter(adapter.Config{BaseURL: upstream.URL, APIKey: "upstream-secret", HTTPClient: upstream.Client()})}
	recorder := postResponsesWithFlush(t, service, `{"model":"public-alias","input":[{"role":"user","content":"hi"},{"type":"function_call_output","call_id":"call-0","output":"42"}],"stream":true,"instructions":"brief","previous_response_id":"resp-prev","tools":[{"type":"function","name":"lookup"}],"metadata":{"trace":"one"}}`)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("unexpected stream: %d %v %s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
	if service.seen == nil || service.seen.Model != "public-alias" || len(service.seen.Messages) != 2 {
		t.Fatalf("HTTP request was not normalized: %#v", service.seen)
	}
	var sent map[string]json.RawMessage
	select {
	case sent = <-requests:
	default:
		t.Fatal("mock upstream did not receive a request")
	}
	for key, want := range map[string]string{
		"model": `"provider-model"`, "stream": `true`, "instructions": `"brief"`, "previous_response_id": `"resp-prev"`,
		"input": `[{"role":"user","content":"hi"},{"type":"function_call_output","call_id":"call-0","output":"42"}]`,
		"tools": `[{"type":"function","name":"lookup"}]`, "metadata": `{"trace":"one"}`,
	} {
		if string(sent[key]) != want {
			t.Errorf("upstream %s = %s, want %s", key, sent[key], want)
		}
	}
	frames := parseResponsesSSEFrames(t, recorder.Body.String())
	wantNames := []string{"response.created", "response.output_item.added", "response.function_call_arguments.delta", "response.function_call_arguments.delta", "response.output_text.delta", "response.completed"}
	if len(frames) != len(wantNames) || recorder.flushes != len(wantNames) {
		t.Fatalf("frames=%d flushes=%d, want %d: %s", len(frames), recorder.flushes, len(wantNames), recorder.Body.String())
	}
	for i, frame := range frames {
		if frame.name != wantNames[i] || string(frame.data["type"]) != string(mustJSON(wantNames[i])) {
			t.Errorf("frame %d: event name/payload lost: %+v", i, frame)
		}
	}
	if string(frames[2].data["delta"]) != `"{\"q\":"` || string(frames[3].data["delta"]) != `"\"x\"}"` || string(frames[2].data["item_id"]) != `"item-1"` {
		t.Fatalf("function-call fragments changed: %+v %+v", frames[2], frames[3])
	}
	if string(frames[4].data["model"]) != `"public-alias"` || !strings.Contains(string(frames[5].data["response"]), `"model":"public-alias"`) || !strings.Contains(string(frames[5].data["response"]), `"reasoning_tokens":1`) || strings.Contains(recorder.Body.String(), "[DONE]") {
		t.Fatalf("alias, usage or completion lost: %s", recorder.Body.String())
	}
}

func TestResponsesStreamFirstFailureStaysJSON(t *testing.T) {
	for _, tc := range []struct {
		name    string
		events  []protocol.StreamEvent
		failure error
	}{
		{"service error", nil, errors.New("secret upstream detail")},
		{"first error event", []protocol.StreamEvent{{Type: "error", Event: "response.failed", Raw: json.RawMessage(`{"type":"response.failed","error":{"message":"secret upstream detail"}}`)}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &fakeChatService{responseEvents: tc.events, responseStreamErr: tc.failure}
			recorder := postResponsesWithFlush(t, service, `{"model":"chat","input":"hi","stream":true}`)
			if recorder.Code != http.StatusBadGateway || recorder.Header().Get("Content-Type") != "application/json" || recorder.flushes != 0 || strings.Contains(recorder.Body.String(), "secret") {
				t.Fatalf("first failure committed a stream or leaked upstream detail: %d %v %s", recorder.Code, recorder.Header(), recorder.Body.String())
			}
			var body map[string]map[string]json.RawMessage
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil || string(body["error"]["code"]) != `"upstream_error"` || string(body["error"]["request_id"]) != `"`+recorder.Header().Get("X-Gateway-Request-ID")+`"` {
				t.Fatalf("bad JSON error envelope: %v %s", err, recorder.Body.String())
			}
		})
	}
}

// Even a service that ignores the callback error cannot turn a failed first
// event into an HTTP 200 (or append a success terminator).
type ignoringResponsesErrorService struct{ *fakeChatService }

func (s *ignoringResponsesErrorService) ResponsesStream(_ context.Context, _ *protocol.ResponsesRequest, emit func(protocol.StreamEvent) error) error {
	_ = emit(protocol.StreamEvent{Type: "error", Error: "private upstream detail"})
	_ = emit(protocol.StreamEvent{Type: "done"})
	return nil
}

func TestResponsesStreamIgnoredFirstErrorStillReturnsJSON(t *testing.T) {
	recorder := postResponsesWithFlush(t, &ignoringResponsesErrorService{&fakeChatService{}}, `{"model":"chat","input":"hi","stream":true}`)
	if recorder.Code != http.StatusBadGateway || recorder.Header().Get("Content-Type") != "application/json" || recorder.flushes != 0 || strings.Contains(recorder.Body.String(), "private upstream detail") {
		t.Fatalf("ignored callback error committed a success: %d %v %s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestResponsesStreamFailureAfterStartHasOneErrorAndNoCompletion(t *testing.T) {
	for _, tc := range []struct {
		name    string
		events  []protocol.StreamEvent
		failure error
		want    string
	}{
		{"returned error", nil, errors.New("secret upstream detail"), "error"},
		{"normalized error", []protocol.StreamEvent{{Type: "error", Error: "upstream interrupted"}}, errors.New("failure after event"), "error"},
		{"raw failed event", []protocol.StreamEvent{{Type: "error", Event: "response.failed", Raw: json.RawMessage(`{"type":"response.failed","response":{"id":"resp-1","model":"provider-model"},"error":{"code":"upstream_failed"}}`)}}, errors.New("failure after event"), "response.failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &fakeChatService{responseEvents: append([]protocol.StreamEvent{{Type: "chunk", Event: "response.created", Raw: json.RawMessage(`{"type":"response.created","response":{"id":"resp-1","model":"provider-model"}}`)}}, tc.events...), responseStreamErr: tc.failure}
			recorder := postResponsesWithFlush(t, service, `{"model":"chat","input":"hi","stream":true}`)
			if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "text/event-stream" {
				t.Fatalf("unexpected stream: %d %v %s", recorder.Code, recorder.Header(), recorder.Body.String())
			}
			frames := parseResponsesSSEFrames(t, recorder.Body.String())
			if len(frames) != 2 || frames[1].name != tc.want || recorder.flushes != 2 || strings.Contains(recorder.Body.String(), "[DONE]") || strings.Contains(recorder.Body.String(), "response.completed") {
				t.Fatalf("stream did not end at a single error: frames=%+v flushes=%d %s", frames, recorder.flushes, recorder.Body.String())
			}
			if tc.want == "error" && (!strings.Contains(string(frames[1].data["error"]), `"request_id":"`+recorder.Header().Get("X-Gateway-Request-ID")+`"`) || strings.Contains(recorder.Body.String(), "secret upstream detail")) {
				t.Fatalf("in-stream error envelope missing or leaking details: %s", recorder.Body.String())
			}
			if tc.want == "response.failed" && !strings.Contains(string(frames[1].data["response"]), `"model":"chat"`) {
				t.Fatalf("raw failed event lost public alias: %s", recorder.Body.String())
			}
		})
	}
}

func TestResponsesStreamCompletionAndUsageAreSentOnce(t *testing.T) {
	completed := json.RawMessage(`{"type":"response.incomplete","response":{"id":"resp-1","model":"provider-model","usage":{"input_tokens":3,"output_tokens":1}}}`)
	service := &fakeChatService{responseEvents: []protocol.StreamEvent{
		{Type: "usage", Event: "response.incomplete", Raw: completed},
		{Type: "done", Event: "response.incomplete", Raw: completed},
		{Type: "done", Event: "response.incomplete", Raw: completed},
		{Type: "chunk", Event: "response.output_text.delta", Raw: json.RawMessage(`{"type":"response.output_text.delta","delta":"too late"}`)},
	}}
	recorder := postResponsesWithFlush(t, service, `{"model":"chat","input":"hi","stream":true}`)
	frames := parseResponsesSSEFrames(t, recorder.Body.String())
	if recorder.Code != http.StatusOK || len(frames) != 1 || recorder.flushes != 1 || frames[0].name != "response.incomplete" || !strings.Contains(string(frames[0].data["response"]), `"input_tokens":3`) || strings.Contains(recorder.Body.String(), "too late") {
		t.Fatalf("completion or usage duplicated/lost: frames=%+v flushes=%d %s", frames, recorder.flushes, recorder.Body.String())
	}
}

func TestResponsesMockUpstreamNonStreamingPreservesShape(t *testing.T) {
	requests := make(chan map[string]json.RawMessage, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode upstream request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		requests <- payload
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"resp-1","object":"response","model":"provider-model","status":"completed","output":[{"type":"function_call","call_id":"call-1","name":"lookup","arguments":"{}"}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3,"input_tokens_details":{"cached_tokens":1}},"metadata":{"upstream":"kept"}}`)
	}))
	defer upstream.Close()
	service := &mockResponsesService{fakeChatService: &fakeChatService{}, upstream: adapter.NewOpenAIAdapter(adapter.Config{BaseURL: upstream.URL, HTTPClient: upstream.Client()})}
	recorder := postResponsesWithFlush(t, service, `{"model":"public-alias","input":"hi","metadata":{"client":"kept"}}`)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "application/json" || recorder.flushes != 0 {
		t.Fatalf("unexpected JSON response: %d %v %s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
	var sent map[string]json.RawMessage
	select {
	case sent = <-requests:
	default:
		t.Fatal("upstream not called")
	}
	if string(sent["model"]) != `"provider-model"` || string(sent["metadata"]) != `{"client":"kept"}` || string(sent["input"]) != `"hi"` {
		t.Fatalf("raw request fields lost: %+v", sent)
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if string(result["model"]) != `"public-alias"` || string(result["metadata"]) != `{"upstream":"kept"}` || !strings.Contains(string(result["output"]), `"call_id":"call-1"`) || !strings.Contains(string(result["usage"]), `"cached_tokens":1`) {
		t.Fatalf("upstream output/usage or public alias lost: %s", recorder.Body.String())
	}
}

func TestResponsesMockUpstreamStreamingFailures(t *testing.T) {
	for _, tc := range []struct {
		name          string
		upstream      string
		wantHTTP      int
		wantSSEFrames int
	}{
		{"upstream HTTP error", "http-error", http.StatusBadGateway, 0},
		{"first failed event", "first-failed", http.StatusBadGateway, 0},
		{"invalid event after start", "bad-event", http.StatusOK, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch tc.upstream {
				case "http-error":
					http.Error(w, "private upstream detail", http.StatusServiceUnavailable)
				case "first-failed":
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprint(w, "event: response.failed\ndata: {\"type\":\"response.failed\",\"error\":{\"message\":\"private upstream detail\"}}\n\n")
				case "bad-event":
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprint(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"model\":\"provider-model\"}}\n\n")
					w.(http.Flusher).Flush()
					_, _ = fmt.Fprint(w, "data: {broken JSON}\n\n")
				}
			}))
			defer upstream.Close()
			service := &mockResponsesService{fakeChatService: &fakeChatService{}, upstream: adapter.NewOpenAIAdapter(adapter.Config{BaseURL: upstream.URL, HTTPClient: upstream.Client()})}
			recorder := postResponsesWithFlush(t, service, `{"model":"chat","input":"hi","stream":true}`)
			if recorder.Code != tc.wantHTTP || recorder.flushes != tc.wantSSEFrames || strings.Contains(recorder.Body.String(), "private upstream detail") || strings.Contains(recorder.Body.String(), "[DONE]") {
				t.Fatalf("stream error not mapped safely: status=%d flushes=%d body=%s", recorder.Code, recorder.flushes, recorder.Body.String())
			}
			if tc.wantSSEFrames == 0 {
				if recorder.Header().Get("Content-Type") != "application/json" || !strings.Contains(recorder.Body.String(), `"code":"upstream_error"`) {
					t.Fatalf("first upstream failure was not a JSON error: %s", recorder.Body.String())
				}
			} else {
				frames := parseResponsesSSEFrames(t, recorder.Body.String())
				if len(frames) != 2 || frames[0].name != "response.created" || frames[1].name != "error" || !strings.Contains(string(frames[1].data["error"]), `"request_id":"`+recorder.Header().Get("X-Gateway-Request-ID")+`"`) {
					t.Fatalf("in-stream failure did not end with one error: %+v", frames)
				}
			}
		})
	}
}
