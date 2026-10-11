package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/zjvtit/omni-gateway/internal/adapter"
	llmgateway "github.com/zjvtit/omni-gateway/internal/gateway"
	"github.com/zjvtit/omni-gateway/internal/model"
	"github.com/zjvtit/omni-gateway/internal/policy"
	"github.com/zjvtit/omni-gateway/internal/protocol"
)

const maxChatBodyBytes = 16 << 20

// ChatService is the public request surface implemented by the gateway
// orchestration layer. Keeping this interface in the HTTP package makes the
// handler straightforward to test without a database.
type ChatService interface {
	ListModels(context.Context) ([]string, error)
	Chat(context.Context, *protocol.ChatRequest) (*protocol.ChatResponse, error)
	ChatStream(context.Context, *protocol.ChatRequest, func(protocol.StreamEvent) error) error
}

// ResponsesService is implemented by gateways that expose the OpenAI Responses API.
// It is kept separate from ChatService so existing Chat-only test doubles and
// integrations remain source-compatible.
type ResponsesService interface {
	Responses(context.Context, *protocol.ResponsesRequest) (*protocol.ResponsesResponse, error)
	ResponsesStream(context.Context, *protocol.ResponsesRequest, func(protocol.StreamEvent) error) error
}

type gatewayHandler struct {
	service          ChatService
	responsesService ResponsesService
}

type chatRequestWire struct {
	Model           string            `json:"model"`
	Messages        []json.RawMessage `json:"messages"`
	Tools           []json.RawMessage `json:"tools"`
	ToolChoice      json.RawMessage   `json:"tool_choice"`
	Temperature     *float64          `json:"temperature"`
	TopP            *float64          `json:"top_p"`
	MaxTokens       *int              `json:"max_tokens"`
	MaxCompletion   *int              `json:"max_completion_tokens"`
	Stop            json.RawMessage   `json:"stop"`
	Stream          bool              `json:"stream"`
	ResponseFormat  json.RawMessage   `json:"response_format"`
	ReasoningEffort string            `json:"reasoning_effort"`
	User            string            `json:"user"`
}

type openAIModelList struct {
	Object string        `json:"object"`
	Data   []openAIModel `json:"data"`
}

type openAIModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
}

type openAIChatResponse struct {
	ID                string             `json:"id"`
	Object            string             `json:"object"`
	Created           int64              `json:"created"`
	Model             string             `json:"model"`
	SystemFingerprint string             `json:"system_fingerprint,omitempty"`
	Choices           []openAIChatChoice `json:"choices"`
	Usage             *openAIUsage       `json:"usage,omitempty"`
}

type openAIChatChoice struct {
	Index        int             `json:"index"`
	Message      *openAIMessage  `json:"message,omitempty"`
	Delta        *openAIMessage  `json:"delta,omitempty"`
	LogProbs     json.RawMessage `json:"logprobs,omitempty"`
	FinishReason *string         `json:"finish_reason"`
	deltaRaw     json.RawMessage // a validated upstream choice delta for sparse SSE
}

func (c openAIChatChoice) MarshalJSON() ([]byte, error) {
	type plain openAIChatChoice
	if len(c.deltaRaw) == 0 {
		return json.Marshal(plain(c))
	}
	return json.Marshal(struct {
		plain
		Delta json.RawMessage `json:"delta"`
	}{plain: plain(c), Delta: c.deltaRaw})
}

type openAIMessage struct {
	Role             string              `json:"role,omitempty"`
	Content          any                 `json:"content,omitempty"`
	ReasoningContent string              `json:"reasoning_content,omitempty"`
	Name             string              `json:"name,omitempty"`
	ToolCallID       string              `json:"tool_call_id,omitempty"`
	ToolCalls        []protocol.ToolCall `json:"tool_calls,omitempty"`
}

type openAIUsage struct {
	PromptTokens      int64                     `json:"prompt_tokens"`
	CompletionTokens  int64                     `json:"completion_tokens"`
	TotalTokens       int64                     `json:"total_tokens"`
	PromptDetails     *openAIInputTokenDetails  `json:"prompt_tokens_details,omitempty"`
	CompletionDetails *openAIOutputTokenDetails `json:"completion_tokens_details,omitempty"`
}

type openAIInputTokenDetails struct {
	CachedTokens int64 `json:"cached_tokens,omitempty"`
}

type openAIOutputTokenDetails struct {
	ReasoningTokens int64 `json:"reasoning_tokens,omitempty"`
}

func (h *gatewayHandler) models(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed", "method not allowed")
		return
	}
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication_error", "unauthorized", "gateway API key required")
		return
	}
	models, err := h.service.ListModels(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_error", "models_unavailable", "could not list models")
		return
	}
	data := make([]openAIModel, 0, len(models))
	for _, modelName := range models {
		if PrincipalModelAllowed(principal, modelName) {
			data = append(data, openAIModel{ID: modelName, Object: "model", OwnedBy: "llm-gateway"})
		}
	}
	writeJSON(w, http.StatusOK, openAIModelList{Object: "list", Data: data})
}

func (h *gatewayHandler) chat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed", "method not allowed")
		return
	}
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication_error", "unauthorized", "gateway API key required")
		return
	}
	request, err := decodeChatRequest(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", err.Error())
		return
	}
	if !PrincipalModelAllowed(principal, request.Model) {
		writeError(w, http.StatusForbidden, "permission_error", "model_not_allowed", "the API key is not allowed to use this model")
		return
	}
	if request.Stream {
		h.stream(w, r.WithContext(withGatewayRequestMetadata(r, principal, "chat_completions", request.Model, true)), request)
		return
	}
	response, err := h.service.Chat(withGatewayRequestMetadata(r, principal, "chat_completions", request.Model, false), request)
	if err != nil {
		writeGatewayError(w, err)
		return
	}
	if response == nil {
		writeError(w, http.StatusBadGateway, "upstream_error", "empty_response", "upstream returned an empty response")
		return
	}
	writeJSON(w, http.StatusOK, makeChatResponse(response))
}

func (h *gatewayHandler) stream(w http.ResponseWriter, r *http.Request, request *protocol.ChatRequest) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "server_error", "streaming_unsupported", "streaming is not supported by this server")
		return
	}
	streamID := "chatcmpl-" + RequestID(r.Context())
	if streamID == "chatcmpl-" {
		streamID = "chatcmpl-gateway"
	}
	created := time.Now().Unix()
	systemFingerprint := ""
	started := false
	doneSent := false
	errorSent := false
	writeEvent := func(event protocol.StreamEvent) error {
		if doneSent {
			return nil
		}
		if event.ResponseID != "" {
			streamID = event.ResponseID
		}
		if event.Created != 0 {
			created = event.Created
		}
		if event.SystemFingerprint != "" {
			systemFingerprint = event.SystemFingerprint
		}
		if event.Type == "error" && !started {
			return &adapter.Error{Type: "server", StatusCode: http.StatusBadGateway, Message: event.Error}
		}
		if !started {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.Header().Set("X-Accel-Buffering", "no")
			w.WriteHeader(http.StatusOK)
			started = true
		}
		var payload any
		switch event.Type {
		case "chunk":
			finishReason := nullableString(event.FinishReason)
			payload = openAIChatResponse{ID: streamID, Object: "chat.completion.chunk", Created: created, Model: request.Model, SystemFingerprint: systemFingerprint, Choices: []openAIChatChoice{{Index: event.Index, Delta: pointerMessage(toOpenAIMessage(event.Delta)), deltaRaw: streamChatDelta(event), LogProbs: copyRaw(event.LogProbs), FinishReason: finishReason}}}
		case "usage":
			payload = openAIChatResponse{ID: streamID, Object: "chat.completion.chunk", Created: created, Model: request.Model, SystemFingerprint: systemFingerprint, Choices: []openAIChatChoice{}, Usage: pointerUsage(event.Usage)}
		case "error":
			errorSent = true
			payload = errorBody(r.Context(), "upstream_error", "upstream_error", event.Error)
		case "done":
			doneSent = true
			_, err := io.WriteString(w, "data: [DONE]\n\n")
			if err != nil {
				return err
			}
			flusher.Flush()
			return nil
		default:
			return nil
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", encoded); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	err := h.service.ChatStream(r.Context(), request, writeEvent)
	if err != nil && !started {
		writeGatewayError(w, err)
		return
	}
	if err != nil && started && !doneSent && !errorSent {
		_ = writeEvent(protocol.StreamEvent{Type: "error", Error: "upstream stream failed"})
	}
	if err == nil && !doneSent && !errorSent {
		_ = writeEvent(protocol.StreamEvent{Type: "done"})
	}
}

// streamChatDelta keeps sparse tool-call fragments intact (including index,
// id, type, name and arguments only when actually present). The raw value is
// one decoded JSON choice's delta, not the upstream frame or an HTTP payload.
func streamChatDelta(event protocol.StreamEvent) json.RawMessage {
	if len(event.Raw) > 0 {
		var frame struct {
			Choices []struct {
				Index int             `json:"index"`
				Delta json.RawMessage `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal(event.Raw, &frame) == nil {
			for _, choice := range frame.Choices {
				if choice.Index == event.Index && len(choice.Delta) > 0 {
					return canonicalChatDelta(choice.Delta)
				}
			}
		}
	}
	return nil
}

// Canonicalize only a valid upstream reasoning alias. Otherwise forward the
// original delta verbatim so sparse tool fragments and unknown fields survive.
func canonicalChatDelta(raw json.RawMessage) json.RawMessage {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return raw
	}
	alias, ok := fields["reasoning"]
	if !ok || !hasJSONValue(alias) {
		return raw
	}
	var aliasText string
	if json.Unmarshal(alias, &aliasText) != nil {
		return raw
	}
	if _, canonicalPresent := fields["reasoning_content"]; !canonicalPresent {
		fields["reasoning_content"] = alias
	}
	delete(fields, "reasoning")
	normalized, err := json.Marshal(fields)
	if err != nil {
		return raw
	}
	return normalized
}

func decodeChatRequest(w http.ResponseWriter, r *http.Request) (*protocol.ChatRequest, error) {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxChatBodyBytes))
	var raw map[string]json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return nil, fmt.Errorf("invalid JSON body")
	}
	var extraToken json.RawMessage
	if err := decoder.Decode(&extraToken); err != io.EOF {
		return nil, fmt.Errorf("request body must contain one JSON object")
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid JSON body")
	}
	var wire chatRequestWire
	if err := json.Unmarshal(encoded, &wire); err != nil {
		return nil, fmt.Errorf("invalid request fields")
	}
	wire.Model = strings.TrimSpace(wire.Model)
	if wire.Model == "" {
		return nil, fmt.Errorf("model is required")
	}
	if len(wire.Messages) == 0 {
		return nil, fmt.Errorf("messages is required")
	}
	messages := make([]protocol.Message, 0, len(wire.Messages))
	for _, rawMessage := range wire.Messages {
		message, err := adapter.ParseOpenAIMessage(rawMessage)
		if err != nil {
			return nil, fmt.Errorf("invalid message: %w", err)
		}
		messages = append(messages, message)
	}
	tools := make([]protocol.Tool, 0, len(wire.Tools))
	for _, rawTool := range wire.Tools {
		var tool protocol.Tool
		if err := json.Unmarshal(rawTool, &tool); err != nil {
			return nil, fmt.Errorf("invalid tool: %w", err)
		}
		tool.Raw = copyRaw(rawTool)
		tools = append(tools, tool)
	}
	stop, err := parseStop(wire.Stop)
	if err != nil {
		return nil, err
	}
	hasMaxTokens := hasJSONValue(raw["max_tokens"])
	hasMaxCompletionTokens := hasJSONValue(raw["max_completion_tokens"])
	if hasMaxTokens && hasMaxCompletionTokens {
		return nil, fmt.Errorf("max_tokens and max_completion_tokens cannot be used together")
	}
	maxTokens := wire.MaxTokens
	if maxTokens == nil {
		maxTokens = wire.MaxCompletion
	}
	maxTokensField := ""
	if hasMaxCompletionTokens {
		maxTokensField = "max_completion_tokens"
	}
	request := &protocol.ChatRequest{
		Model:           wire.Model,
		Messages:        messages,
		Tools:           tools,
		ToolChoice:      copyRaw(wire.ToolChoice),
		Temperature:     wire.Temperature,
		TopP:            wire.TopP,
		MaxTokens:       maxTokens,
		MaxTokensField:  maxTokensField,
		Stop:            stop,
		Stream:          wire.Stream,
		ResponseFormat:  copyRaw(wire.ResponseFormat),
		ReasoningEffort: wire.ReasoningEffort,
		User:            wire.User,
		Extra:           make(map[string]json.RawMessage),
	}
	known := map[string]bool{
		"model": true, "messages": true, "tools": true, "tool_choice": true,
		"temperature": true, "top_p": true, "max_tokens": true, "max_completion_tokens": true,
		"stop": true, "stream": true, "response_format": true, "reasoning_effort": true, "user": true,
	}
	for key, value := range raw {
		if !known[key] {
			request.Extra[key] = copyRaw(value)
		}
	}
	return request, nil
}

func hasJSONValue(raw json.RawMessage) bool {
	return len(raw) > 0 && strings.TrimSpace(string(raw)) != "null"
}

func parseStop(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || !hasJSONValue(raw) {
		return nil, nil
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err == nil {
		return values, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return []string{value}, nil
	}
	return nil, fmt.Errorf("stop must be a string or array of strings")
}

func makeChatResponse(response *protocol.ChatResponse) openAIChatResponse {
	choices := make([]openAIChatChoice, 0, len(response.Choices))
	for _, choice := range response.Choices {
		finishReason := nullableString(choice.FinishReason)
		choices = append(choices, openAIChatChoice{Index: choice.Index, Message: pointerMessage(toOpenAIMessage(choice.Message)), LogProbs: copyRaw(choice.LogProbs), FinishReason: finishReason})
	}
	var usage *openAIUsage
	if len(response.Usage.Meters) > 0 {
		usage = pointerUsage(response.Usage)
	}
	created := response.Created
	if created == 0 {
		created = time.Now().Unix()
	}
	return openAIChatResponse{ID: response.ID, Object: "chat.completion", Created: created, Model: response.Model, SystemFingerprint: response.SystemFingerprint, Choices: choices, Usage: usage}
}

func toOpenAIMessage(message protocol.Message) openAIMessage {
	out := openAIMessage{Role: message.Role, ReasoningContent: message.ReasoningContent, Name: message.Name, ToolCallID: message.ToolCallID, ToolCalls: message.ToolCalls}
	if len(message.Content) == 1 && message.Content[0].Type == "text" {
		out.Content = message.Content[0].Text
	} else if len(message.Content) > 0 {
		parts := make([]map[string]any, 0, len(message.Content))
		for _, part := range message.Content {
			switch part.Type {
			case "text", "output_text":
				parts = append(parts, map[string]any{"type": "text", "text": part.Text})
			case "image_url", "input_image":
				url := part.ImageURL
				if url == "" {
					url = part.ImageData
				}
				parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}})
			default:
				parts = append(parts, map[string]any{"type": part.Type, part.Type: part.Text})
			}
		}
		out.Content = parts
	}
	return out
}

func pointerMessage(message openAIMessage) *openAIMessage { return &message }

func pointerUsage(usage model.Usage) *openAIUsage {
	result := &openAIUsage{
		PromptTokens:     usage.Get("input"),
		CompletionTokens: usage.Get("output"),
		TotalTokens:      usage.Get("total"),
	}
	if result.TotalTokens == 0 && (result.PromptTokens != 0 || result.CompletionTokens != 0) {
		result.TotalTokens = result.PromptTokens + result.CompletionTokens
	}
	if cached := usage.Get("cache_input"); cached != 0 {
		result.PromptDetails = &openAIInputTokenDetails{CachedTokens: cached}
	}
	if reasoning := usage.Get("reasoning"); reasoning != 0 {
		result.CompletionDetails = &openAIOutputTokenDetails{ReasoningTokens: reasoning}
	}
	return result
}

func nullableString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func copyRaw(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), value...)
}

func writeGatewayError(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	errorType := "upstream_error"
	code := "upstream_error"
	message := "upstream request failed"
	var upstream *adapter.Error
	switch {
	case errors.Is(err, llmgateway.ErrResponsesUnsupported):
		status, errorType, code, message = http.StatusNotImplemented, "server_error", "responses_unsupported", "the selected provider does not implement the OpenAI Responses API; use /v1/chat/completions instead"
	case errors.Is(err, adapter.ErrUnsupported):
		status, errorType, code, message = http.StatusNotImplemented, "server_error", "operation_unsupported", "the selected provider does not support this operation"
	case errors.Is(err, adapter.ErrInvalidRequest):
		// Only the adapter sentinel reaches this branch: a real upstream 400 is
		// an *adapter.Error and keeps the generic upstream mapping below.
		status, errorType, code, message = http.StatusBadRequest, "invalid_request_error", "invalid_request", "the request cannot be represented safely for the selected provider"
	case errors.Is(err, policy.ErrRateLimited):
		status, errorType, code, message = http.StatusTooManyRequests, "rate_limit_error", "rate_limited", "gateway rate limit exceeded"
	case errors.Is(err, policy.ErrBudgetExceeded):
		status, errorType, code, message = http.StatusTooManyRequests, "budget_error", "budget_exceeded", "budget limit exceeded"
	case errors.Is(err, llmgateway.ErrModelNotFound):
		status, errorType, code, message = http.StatusNotFound, "invalid_request_error", "model_not_found", "model alias not found"
	case errors.Is(err, llmgateway.ErrNoRoute):
		status, errorType, code, message = http.StatusServiceUnavailable, "server_error", "no_route", "no active route for model alias"
	case errors.Is(err, context.Canceled):
		status, errorType, code, message = http.StatusBadGateway, "upstream_error", "request_canceled", "request canceled"
	case errors.Is(err, context.DeadlineExceeded):
		status, errorType, code, message = http.StatusGatewayTimeout, "upstream_error", "request_timeout", "request timed out"
	case errors.As(err, &upstream):
		if upstream.StatusCode == http.StatusTooManyRequests {
			status, errorType, code, message = http.StatusTooManyRequests, "rate_limit_error", "upstream_rate_limit", "upstream rate limit exceeded"
		} else if upstream.StatusCode == http.StatusUnauthorized || upstream.StatusCode == http.StatusForbidden {
			code, message = "upstream_authentication_failed", "upstream authentication failed"
		}
	}
	writeError(w, status, errorType, code, message)
}
