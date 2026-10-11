package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zjvtit/omni-gateway/internal/adapter"
	gatewaycrypto "github.com/zjvtit/omni-gateway/internal/crypto"
	llmgateway "github.com/zjvtit/omni-gateway/internal/gateway"
	"github.com/zjvtit/omni-gateway/internal/model"
	"github.com/zjvtit/omni-gateway/internal/protocol"
)

type fakeChatService struct {
	models              []string
	modelsErr           error
	chatResponse        *protocol.ChatResponse
	chatErr             error
	streamEvents        []protocol.StreamEvent
	streamErr           error
	gotRequest          *protocol.ChatRequest
	response            *protocol.ResponsesResponse
	responseErr         error
	responseEvents      []protocol.StreamEvent
	responseStreamErr   error
	gotResponsesRequest *protocol.ResponsesRequest
}

func (s *fakeChatService) ListModels(context.Context) ([]string, error) {
	return s.models, s.modelsErr
}

func (s *fakeChatService) Chat(_ context.Context, request *protocol.ChatRequest) (*protocol.ChatResponse, error) {
	copy := *request
	s.gotRequest = &copy
	return s.chatResponse, s.chatErr
}

func (s *fakeChatService) ChatStream(_ context.Context, request *protocol.ChatRequest, emit func(protocol.StreamEvent) error) error {
	copy := *request
	s.gotRequest = &copy
	for _, event := range s.streamEvents {
		if err := emit(event); err != nil {
			return err
		}
	}
	return s.streamErr
}

func (s *fakeChatService) Responses(_ context.Context, request *protocol.ResponsesRequest) (*protocol.ResponsesResponse, error) {
	copy := *request
	copy.Input = append(json.RawMessage(nil), request.Input...)
	s.gotResponsesRequest = &copy
	return s.response, s.responseErr
}

func (s *fakeChatService) ResponsesStream(_ context.Context, request *protocol.ResponsesRequest, emit func(protocol.StreamEvent) error) error {
	copy := *request
	copy.Input = append(json.RawMessage(nil), request.Input...)
	s.gotResponsesRequest = &copy
	for _, event := range s.responseEvents {
		if err := emit(event); err != nil {
			return err
		}
	}
	return s.responseStreamErr
}

func gatewayAPIKeyStore() *fakeAPIKeyStore {
	secret := "sk-gateway"
	return &fakeAPIKeyStore{
		key:  &model.APIKey{ID: 1, UserID: 2, KeyHash: hashAPIKeyForTest(secret), Active: true},
		user: &model.User{ID: 2, Active: true},
	}
}

func hashAPIKeyForTest(value string) string {
	// Keep the test helper independent of the middleware's internal storage
	// details while using the same production hash implementation.
	return gatewaycrypto.HashAPIKey(value)
}

func TestGatewayModelsEndpointUsesAPIKeyAndAllowList(t *testing.T) {
	secret := "sk-gateway"
	apiKeys := gatewayAPIKeyStore()
	apiKeys.key.ModelAllow = []string{"chat"}
	service := &fakeChatService{models: []string{"vision", "chat", "inactive"}}
	handler := NewHandlerWithGateway(nil, nil, apiKeys, service, AuthConfig{})

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("got status %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var response openAIModelList
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode models response: %v", err)
	}
	if len(response.Data) != 1 || response.Data[0].ID != "chat" {
		t.Fatalf("unexpected models response: %#v", response)
	}
}

func TestGatewayChatEndpointDecodesOpenAIRequest(t *testing.T) {
	secret := "sk-gateway"
	apiKeys := gatewayAPIKeyStore()
	service := &fakeChatService{chatResponse: &protocol.ChatResponse{
		ID:                "upstream-id",
		Model:             "chat",
		Created:           1700000000,
		SystemFingerprint: "fp-response",
		Choices: []protocol.ChatChoice{{
			Index:        0,
			Message:      protocol.Message{Role: "assistant", ReasoningContent: "reasoning", Content: []protocol.ContentPart{{Type: "text", Text: "hello"}}},
			FinishReason: "stop",
			LogProbs:     json.RawMessage(`{"tokens":["hello"]}`),
		}},
		Usage: model.Usage{Meters: map[string]int64{"input": 2, "output": 1, "total": 3}},
	}}
	handler := NewHandlerWithGateway(nil, nil, apiKeys, service, AuthConfig{})
	body := `{"model":"chat","messages":[{"role":"user","content":"hi"}],"stop":"END","max_completion_tokens":12,"stream":false,"metadata":{"trace":"one"}}`
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+secret)
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("got status %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if service.gotRequest == nil || service.gotRequest.Model != "chat" || service.gotRequest.Messages[0].Content[0].Text != "hi" || service.gotRequest.MaxTokens == nil || *service.gotRequest.MaxTokens != 12 || service.gotRequest.MaxTokensField != "max_completion_tokens" || len(service.gotRequest.Stop) != 1 || service.gotRequest.Stop[0] != "END" {
		t.Fatalf("unexpected normalized request: %#v", service.gotRequest)
	}
	if string(service.gotRequest.Extra["metadata"]) != `{"trace":"one"}` {
		t.Fatalf("unknown field was not preserved: %#v", service.gotRequest.Extra)
	}
	var response openAIChatResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode chat response: %v", err)
	}
	if response.ID != "upstream-id" || response.Object != "chat.completion" || response.Model != "chat" || response.Created != 1700000000 || response.SystemFingerprint != "fp-response" || response.Usage == nil || response.Usage.TotalTokens != 3 || response.Choices[0].Message.Content != "hello" || response.Choices[0].Message.ReasoningContent != "reasoning" || string(response.Choices[0].LogProbs) != `{"tokens":["hello"]}` || response.Choices[0].FinishReason == nil || *response.Choices[0].FinishReason != "stop" {
		t.Fatalf("unexpected chat response: %#v", response)
	}
}

func TestGatewayChatMaxTokensFieldCompatibility(t *testing.T) {
	service := &fakeChatService{chatResponse: &protocol.ChatResponse{Model: "chat"}}
	handler := NewHandlerWithGateway(nil, nil, gatewayAPIKeyStore(), service, AuthConfig{})
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"chat","messages":[{"role":"user","content":"hi"}],"max_tokens":7}`))
	req.Header.Set("Authorization", "Bearer sk-gateway")
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK || service.gotRequest == nil || service.gotRequest.MaxTokens == nil || *service.gotRequest.MaxTokens != 7 || service.gotRequest.MaxTokensField != "" {
		t.Fatalf("max_tokens default behavior changed: status=%d request=%#v body=%s", recorder.Code, service.gotRequest, recorder.Body.String())
	}

	conflictingService := &fakeChatService{chatResponse: &protocol.ChatResponse{Model: "chat"}}
	conflictingHandler := NewHandlerWithGateway(nil, nil, gatewayAPIKeyStore(), conflictingService, AuthConfig{})
	conflictingRecorder := httptest.NewRecorder()
	conflictingRequest := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"chat","messages":[{"role":"user","content":"hi"}],"max_tokens":7,"max_completion_tokens":8}`))
	conflictingRequest.Header.Set("Authorization", "Bearer sk-gateway")
	conflictingHandler.ServeHTTP(conflictingRecorder, conflictingRequest)
	if conflictingRecorder.Code != http.StatusBadRequest || !strings.Contains(conflictingRecorder.Body.String(), "max_tokens and max_completion_tokens cannot be used together") || conflictingService.gotRequest != nil {
		t.Fatalf("conflicting max token fields were not rejected locally: status=%d request=%#v body=%s", conflictingRecorder.Code, conflictingService.gotRequest, conflictingRecorder.Body.String())
	}
}

func TestGatewayChatNullableMaxTokenFieldsDoNotConflict(t *testing.T) {
	for _, tc := range []struct {
		name, body, field string
	}{
		{"null max_tokens", `{"model":"chat","messages":[{"role":"user","content":"hi"}],"max_tokens":null,"max_completion_tokens":8}`, "max_completion_tokens"},
		{"null max_completion_tokens", `{"model":"chat","messages":[{"role":"user","content":"hi"}],"max_tokens":7,"max_completion_tokens":null}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &fakeChatService{chatResponse: &protocol.ChatResponse{Model: "chat"}}
			handler := NewHandlerWithGateway(nil, nil, gatewayAPIKeyStore(), service, AuthConfig{})
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer sk-gateway")
			handler.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusOK || service.gotRequest == nil || service.gotRequest.MaxTokens == nil || service.gotRequest.MaxTokensField != tc.field {
				t.Fatalf("nullable token field rejected or renamed: status=%d field=%q", recorder.Code, tc.field)
			}
		})
	}
}

func TestGatewayChatStreamEndpointWritesOpenAISSE(t *testing.T) {
	index0, index1 := 0, 1
	service := &fakeChatService{streamEvents: []protocol.StreamEvent{
		{
			Type:              "chunk",
			Index:             1,
			ResponseID:        "chatcmpl-upstream-stream",
			Created:           1700000123,
			SystemFingerprint: "fp-stream",
			LogProbs:          json.RawMessage(`{"token_logprobs":[-0.1]}`),
			Delta: protocol.Message{
				Role:             "assistant",
				ReasoningContent: "stream reasoning",
				ToolCalls: []protocol.ToolCall{
					{ID: "call-0", Type: "function", Index: &index0, Function: protocol.ToolCallFunction{Name: "first", Arguments: "{}"}},
					{ID: "call-1", Type: "function", Index: &index1, Function: protocol.ToolCallFunction{Name: "second", Arguments: "{}"}},
				},
			},
			Raw: json.RawMessage(`{"secret":"do-not-leak"}`),
		},
		{Type: "usage", Usage: model.Usage{Meters: map[string]int64{"input": 1, "output": 1, "total": 2}}},
		{Type: "done"},
		{Type: "chunk", Delta: protocol.Message{Content: []protocol.ContentPart{{Type: "text", Text: "after done"}}}},
	}}
	handler := NewHandlerWithGateway(nil, nil, gatewayAPIKeyStore(), service, AuthConfig{})
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"chat","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	req.Header.Set("Authorization", "Bearer sk-gateway")
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "text/event-stream" || recorder.Header().Get("X-Accel-Buffering") != "no" {
		t.Fatalf("unexpected stream response: %d headers=%v", recorder.Code, recorder.Header())
	}
	body := recorder.Body.String()
	if strings.Contains(body, "do-not-leak") || strings.Contains(body, "after done") || strings.Count(body, "data: ") != 3 || strings.Count(body, "data: [DONE]") != 1 {
		t.Fatalf("unexpected SSE body (raw/duplicate event leaked): %s", body)
	}
	frames := strings.Split(strings.TrimSpace(body), "\n\n")
	if len(frames) != 3 {
		t.Fatalf("got %d SSE frames, want 3: %s", len(frames), body)
	}
	var chunk, usage openAIChatResponse
	if err := json.Unmarshal([]byte(strings.TrimPrefix(frames[0], "data: ")), &chunk); err != nil {
		t.Fatalf("decode chat chunk: %v", err)
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(frames[1], "data: ")), &usage); err != nil {
		t.Fatalf("decode usage chunk: %v", err)
	}
	if chunk.ID != "chatcmpl-upstream-stream" || chunk.Created != 1700000123 || chunk.SystemFingerprint != "fp-stream" || len(chunk.Choices) != 1 || chunk.Choices[0].Index != 1 || string(chunk.Choices[0].LogProbs) != `{"token_logprobs":[-0.1]}` || chunk.Choices[0].Delta.ReasoningContent != "stream reasoning" {
		t.Fatalf("upstream stream metadata was not preserved: %#v", chunk)
	}
	calls := chunk.Choices[0].Delta.ToolCalls
	if len(calls) != 2 || calls[0].Index == nil || *calls[0].Index != 0 || calls[1].Index == nil || *calls[1].Index != 1 {
		t.Fatalf("parallel tool-call indexes were not preserved: %#v", calls)
	}
	if usage.ID != chunk.ID || usage.Created != chunk.Created || usage.SystemFingerprint != chunk.SystemFingerprint || usage.Usage == nil || usage.Usage.TotalTokens != 2 {
		t.Fatalf("usage chunk did not retain upstream metadata and usage: %#v", usage)
	}
}

func TestGatewayChatStreamFallsBackToGeneratedIDAndTime(t *testing.T) {
	service := &fakeChatService{streamEvents: []protocol.StreamEvent{{
		Type:  "chunk",
		Index: 0,
		Delta: protocol.Message{Content: []protocol.ContentPart{{Type: "text", Text: "hello"}}},
	}}}
	handler := NewHandlerWithGateway(nil, nil, gatewayAPIKeyStore(), service, AuthConfig{})
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"chat","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	req.Header.Set("Authorization", "Bearer sk-gateway")
	req.Header.Set(RequestIDHeader, "stream-fallback")
	handler.ServeHTTP(recorder, req)
	frames := strings.Split(strings.TrimSpace(recorder.Body.String()), "\n\n")
	var chunk openAIChatResponse
	if len(frames) != 2 || json.Unmarshal([]byte(strings.TrimPrefix(frames[0], "data: ")), &chunk) != nil {
		t.Fatalf("unexpected fallback stream: %s", recorder.Body.String())
	}
	if chunk.ID != "chatcmpl-stream-fallback" || chunk.Created <= 0 || !strings.HasPrefix(frames[1], "data: [DONE]") {
		t.Fatalf("fallback stream id/time missing: %#v; %s", chunk, recorder.Body.String())
	}
}

func TestGatewayChatEndpointReturnsStableErrors(t *testing.T) {
	secret := "sk-gateway"
	apiKeys := gatewayAPIKeyStore()
	service := &fakeChatService{chatErr: llmgateway.ErrModelNotFound}
	handler := NewHandlerWithGateway(nil, nil, apiKeys, service, AuthConfig{})

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"missing","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+secret)
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusNotFound || !strings.Contains(recorder.Body.String(), `"code":"model_not_found"`) {
		t.Fatalf("unexpected model error: %d %s", recorder.Code, recorder.Body.String())
	}

	badBody := httptest.NewRecorder()
	badRequest := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"chat","messages":"wrong"}`))
	badRequest.Header.Set("Authorization", "Bearer "+secret)
	handler.ServeHTTP(badBody, badRequest)
	if badBody.Code != http.StatusBadRequest || !strings.Contains(badBody.Body.String(), "invalid request fields") {
		t.Fatalf("unexpected malformed request error: %d %s", badBody.Code, badBody.Body.String())
	}
}

func TestGatewayResponsesEndpointPreservesResponseShape(t *testing.T) {
	secret := "sk-gateway"
	apiKeys := gatewayAPIKeyStore()
	service := &fakeChatService{response: &protocol.ResponsesResponse{
		ID:     "resp-upstream",
		Model:  "provider-model",
		Status: "completed",
		Output: json.RawMessage(`[{"type":"message","content":[{"type":"output_text","text":"hello"}]}]`),
		Usage:  model.Usage{Meters: map[string]int64{"input": 4, "output": 2, "total": 6}},
		Raw:    json.RawMessage(`{"id":"resp-upstream","object":"response","model":"provider-model","status":"completed","output":[{"type":"message"}]}`),
	}}
	handler := NewHandlerWithGateway(nil, nil, apiKeys, service, AuthConfig{})
	body := `{"model":"chat","input":"hello","instructions":"be concise","previous_response_id":"resp-old","stream":false,"metadata":{"trace":"one"}}`
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+secret)
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("unexpected response: %d %q %s", recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.String())
	}
	if service.gotResponsesRequest == nil || service.gotResponsesRequest.Model != "chat" || string(service.gotResponsesRequest.Input) != `"hello"` || service.gotResponsesRequest.Instructions != "be concise" || service.gotResponsesRequest.PreviousResponseID != "resp-old" || string(service.gotResponsesRequest.Extra["metadata"]) != `{"trace":"one"}` {
		t.Fatalf("unexpected normalized Responses request: %#v", service.gotResponsesRequest)
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode Responses response: %v", err)
	}
	var responseModel, objectName, status string
	_ = json.Unmarshal(response["model"], &responseModel)
	_ = json.Unmarshal(response["object"], &objectName)
	_ = json.Unmarshal(response["status"], &status)
	if responseModel != "chat" || objectName != "response" || status != "completed" || string(response["output"]) != string(json.RawMessage(`[{"type":"message"}]`)) {
		t.Fatalf("unexpected Responses response: %#v", response)
	}
	if !strings.Contains(string(response["usage"]), `"input_tokens":4`) {
		t.Fatalf("missing normalized usage: %s", response["usage"])
	}
	if !strings.Contains(recorder.Body.String(), `"model":"chat"`) {
		t.Fatal("response did not expose the requested alias")
	}

}

func TestGatewayResponsesStreamProxiesRawEvents(t *testing.T) {
	secret := "sk-gateway"
	apiKeys := gatewayAPIKeyStore()
	service := &fakeChatService{responseEvents: []protocol.StreamEvent{
		{Type: "chunk", Raw: json.RawMessage(`{"type":"response.created","response":{"id":"resp-1","model":"provider-model"}}`)},
		{Type: "chunk", Raw: json.RawMessage(`{"type":"response.output_text.delta","delta":"hello"}`)},
		{Type: "usage", Raw: json.RawMessage(`{"type":"response.completed","response":{"id":"resp-1","model":"provider-model","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`)},
		{Type: "done", Raw: json.RawMessage(`{"type":"response.completed","response":{"id":"resp-1","model":"provider-model","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`)},
	}}
	handler := NewHandlerWithGateway(nil, nil, apiKeys, service, AuthConfig{})
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"chat","input":"hello","stream":true}`))
	req.Header.Set("Authorization", "Bearer "+secret)
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("unexpected stream response: %d %q", recorder.Code, recorder.Header().Get("Content-Type"))
	}
	body := recorder.Body.String()
	if strings.Count(body, "data: ") != 3 || !strings.Contains(body, `"type":"response.created"`) || !strings.Contains(body, `"delta":"hello"`) || !strings.Contains(body, `"model":"chat"`) {
		t.Fatalf("unexpected Responses SSE body: %s", body)
	}
	if strings.Contains(body, "[DONE]") {
		t.Fatal("Responses completion event should be proxied instead of duplicated with [DONE]")
	}

}

func TestJSONErrorResponseCarriesRequestID(t *testing.T) {
	apiKeys := gatewayAPIKeyStore()
	service := &fakeChatService{chatErr: llmgateway.ErrModelNotFound}
	handler := NewHandlerWithGateway(nil, nil, apiKeys, service, AuthConfig{})
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"missing","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer sk-gateway")
	req.Header.Set(RequestIDHeader, "req-json-error")
	handler.ServeHTTP(recorder, req)

	var payload struct {
		Error struct {
			Code      string `json:"code"`
			Type      string `json:"type"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error body: %v (%s)", err, recorder.Body.String())
	}
	if payload.Error.Code != "model_not_found" || payload.Error.RequestID != recorder.Header().Get("X-Gateway-Request-ID") || payload.Error.RequestID == "req-json-error" {
		t.Fatalf("unexpected error envelope: %s", recorder.Body.String())
	}
}

func TestChatStreamErrorEnvelopeCarriesCodeAndRequestID(t *testing.T) {
	apiKeys := gatewayAPIKeyStore()
	service := &fakeChatService{streamEvents: []protocol.StreamEvent{
		{Type: "chunk", Delta: protocol.Message{Role: "assistant", Content: []protocol.ContentPart{{Type: "text", Text: "partial"}}}},
		{Type: "error", Error: "upstream connection reset"},
	}}
	handler := NewHandlerWithGateway(nil, nil, apiKeys, service, AuthConfig{})
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"chat","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	req.Header.Set("Authorization", "Bearer sk-gateway")
	req.Header.Set(RequestIDHeader, "req-stream-error")
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected stream status: %d", recorder.Code)
	}
	body := recorder.Body.String()
	for _, want := range []string{
		`"message":"upstream connection reset"`,
		`"type":"upstream_error"`,
		`"code":"upstream_error"`,
		`"request_id":"` + recorder.Header().Get("X-Gateway-Request-ID") + `"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream error envelope missing %s: %s", want, body)
		}
	}
}

func TestResponsesStreamErrorEnvelopeCarriesCodeAndRequestID(t *testing.T) {
	apiKeys := gatewayAPIKeyStore()
	service := &fakeChatService{responseEvents: []protocol.StreamEvent{
		{Type: "chunk", Delta: protocol.Message{Role: "assistant", Content: []protocol.ContentPart{{Type: "text", Text: "partial"}}}},
		{Type: "error", Error: "upstream stream interrupted"},
	}}
	handler := NewHandlerWithGateway(nil, nil, apiKeys, service, AuthConfig{})
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"chat","input":"hi","stream":true}`))
	req.Header.Set("Authorization", "Bearer sk-gateway")
	req.Header.Set(RequestIDHeader, "req-responses-error")
	handler.ServeHTTP(recorder, req)

	body := recorder.Body.String()
	for _, want := range []string{
		`"type":"error"`,
		`"message":"upstream stream interrupted"`,
		`"code":"upstream_error"`,
		`"request_id":"` + recorder.Header().Get("X-Gateway-Request-ID") + `"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("responses stream error envelope missing %s: %s", want, body)
		}
	}
}

// A locally refused request maps to 400 invalid_request_error with a static
// message: the wrapped cause, which can quote request data, must not leak. A
// real upstream 400 keeps the generic upstream mapping instead of being folded
// into the local branch, and the Responses 501 is not shadowed.
func TestGatewayLocalInvalidRequestIsRedacted400(t *testing.T) {
	localErr := fmt.Errorf("tool_choice %q is not declared: %w", "secret-selector", adapter.ErrInvalidRequest)
	service := &fakeChatService{chatErr: localErr, streamErr: localErr}
	handler := NewHandlerWithGateway(nil, nil, gatewayAPIKeyStore(), service, AuthConfig{})

	for _, tc := range []struct {
		name string
		body string
	}{
		{"chat", `{"model":"chat","messages":[{"role":"user","content":"hi"}]}`},
		{"stream", `{"model":"chat","messages":[{"role":"user","content":"hi"}],"stream":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer sk-gateway")
			handler.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
			}
			body := recorder.Body.String()
			for _, want := range []string{
				`"type":"invalid_request_error"`,
				`"code":"invalid_request"`,
				`"message":"the request cannot be represented safely for the selected provider"`,
			} {
				if !strings.Contains(body, want) {
					t.Fatalf("400 body missing %s: %s", want, body)
				}
			}
			if strings.Contains(body, "secret-selector") || strings.Contains(body, "is not declared") {
				t.Fatalf("400 body leaked the wrapped cause: %s", body)
			}
		})
	}

	// A genuine upstream 400 is an *adapter.Error, not a local refusal.
	upstreamService := &fakeChatService{chatErr: &adapter.Error{StatusCode: http.StatusBadRequest, Type: "client", Message: "upstream-secret-400"}}
	upstreamHandler := NewHandlerWithGateway(nil, nil, gatewayAPIKeyStore(), upstreamService, AuthConfig{})
	upstreamRecorder := httptest.NewRecorder()
	upstreamReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"chat","messages":[{"role":"user","content":"hi"}]}`))
	upstreamReq.Header.Set("Authorization", "Bearer sk-gateway")
	upstreamHandler.ServeHTTP(upstreamRecorder, upstreamReq)
	if upstreamRecorder.Code != http.StatusBadGateway || strings.Contains(upstreamRecorder.Body.String(), "invalid_request") {
		t.Fatalf("upstream 400 was folded into the local invalid-request branch: %d %s", upstreamRecorder.Code, upstreamRecorder.Body.String())
	}

	// The Responses refusal must stay 501 and not be shadowed by the new case.
	unsupported := httptest.NewRecorder()
	writeGatewayError(unsupported, llmgateway.ErrResponsesUnsupported)
	if unsupported.Code != http.StatusNotImplemented {
		t.Fatalf("Responses refusal status = %d, want %d", unsupported.Code, http.StatusNotImplemented)
	}
}

func TestGatewayDeadlineExceededIsRedacted504(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeGatewayError(recorder, fmt.Errorf("request with sensitive context: %w", context.DeadlineExceeded))
	if recorder.Code != http.StatusGatewayTimeout {
		t.Fatalf("deadline status = %d, want %d", recorder.Code, http.StatusGatewayTimeout)
	}
	body := recorder.Body.String()
	for _, want := range []string{`"type":"upstream_error"`, `"code":"request_timeout"`, `"message":"request timed out"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("deadline error body missing %s: %s", want, body)
		}
	}
	if strings.Contains(body, "sensitive context") {
		t.Fatal("deadline response leaked the wrapped cause")
	}
}
