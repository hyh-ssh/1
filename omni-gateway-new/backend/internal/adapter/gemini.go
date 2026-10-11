package adapter

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/zjvtit/omni-gateway/internal/model"
	"github.com/zjvtit/omni-gateway/internal/protocol"
)

// GeminiAdapter implements Google's generateContent API for normalized chat
// requests. The OpenAI Responses stateful contract is not available natively.
type GeminiAdapter struct {
	cfg    Config
	client *http.Client
}

func NewGeminiAdapter(cfg Config) *GeminiAdapter {
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: time.Duration(cfg.TimeoutMS) * time.Millisecond}
	}
	return &GeminiAdapter{cfg: cfg, client: client}
}

func (a *GeminiAdapter) Type() string { return "gemini" }

func (a *GeminiAdapter) endpoint(modelName string, stream bool) string {
	path := "/models/" + url.PathEscape(modelName)
	if stream {
		path += ":streamGenerateContent?alt=sse"
	} else {
		path += ":generateContent"
	}
	base := strings.TrimRight(a.cfg.BaseURL, "/")
	if strings.HasSuffix(base, "/v1beta") || strings.HasSuffix(base, "/v1") {
		return base + path
	}
	return base + "/v1beta" + path
}

func (a *GeminiAdapter) listEndpoint() string {
	base := strings.TrimRight(a.cfg.BaseURL, "/")
	if strings.HasSuffix(base, "/v1beta") || strings.HasSuffix(base, "/v1") {
		return base + "/models"
	}
	return base + "/v1beta/models"
}

func (a *GeminiAdapter) newRequest(ctx context.Context, method, endpoint string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if a.cfg.APIKey != "" {
		values := req.URL.Query()
		values.Set("key", a.cfg.APIKey)
		req.URL.RawQuery = values.Encode()
	}
	for key, value := range a.cfg.ExtraHeader {
		req.Header.Set(key, value)
	}
	return req, nil
}

func (a *GeminiAdapter) Chat(ctx context.Context, req *protocol.ChatRequest) (*protocol.ChatResponse, error) {
	payload, err := a.buildPayload(req)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	httpReq, err := a.newRequest(ctx, http.MethodPost, a.endpoint(req.Model, false), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	resp, responseBody, err := a.do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, ClassifyHTTPError(resp.StatusCode, responseBody, resp.Header)
	}
	var out geminiResponse
	if err := json.Unmarshal(responseBody, &out); err != nil {
		return nil, err
	}
	return geminiChatResponse(out, responseBody), nil
}

func (a *GeminiAdapter) ChatStream(ctx context.Context, req *protocol.ChatRequest, emit func(protocol.StreamEvent) error) error {
	payload, err := a.buildPayload(req)
	if err != nil {
		return err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	httpReq, err := a.newRequest(ctx, http.MethodPost, a.endpoint(req.Model, true), bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Accept", "text/event-stream")
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return classifyTransportError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		responseBody, readErr := readUpstreamBody(resp.Body)
		if readErr != nil {
			return fmt.Errorf("read Gemini error response: %w", readErr)
		}
		return ClassifyHTTPError(resp.StatusCode, responseBody, resp.Header)
	}
	// Gemini usageMetadata contains cumulative totals on successive SSE frames;
	// the gateway adds normalized usage events, so only emit positive deltas.
	previousUsage := model.NewUsage()
	callState := &geminiCallState{}
	return parseNamedSSE(resp.Body, func(_ string, data []byte) error {
		if strings.TrimSpace(string(data)) == "[DONE]" {
			return emit(protocol.StreamEvent{Type: "done", Raw: data})
		}
		var event geminiResponse
		if err := json.Unmarshal(data, &event); err != nil {
			return fmt.Errorf("decode Gemini stream event: %w", err)
		}
		return emitGeminiEvent(event, data, callState, func(item protocol.StreamEvent) error {
			if item.Type == "usage" {
				item.Usage = nativeUsageDelta(previousUsage, item.Usage)
				if len(item.Usage.Meters) == 0 {
					return nil
				}
			}
			return emit(item)
		})
	})
}

func (a *GeminiAdapter) Responses(context.Context, *protocol.ResponsesRequest) (*protocol.ResponsesResponse, error) {
	return nil, ErrUnsupported
}

func (a *GeminiAdapter) ResponsesStream(context.Context, *protocol.ResponsesRequest, func(protocol.StreamEvent) error) error {
	return ErrUnsupported
}

func (a *GeminiAdapter) ListModels(ctx context.Context) ([]string, error) {
	httpReq, err := a.newRequest(ctx, http.MethodGet, a.listEndpoint(), nil)
	if err != nil {
		return nil, err
	}
	resp, body, err := a.do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, ClassifyHTTPError(resp.StatusCode, body, resp.Header)
	}
	var out struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Models))
	for _, item := range out.Models {
		name := strings.TrimPrefix(item.Name, "models/")
		if name != "" {
			ids = append(ids, name)
		}
	}
	return ids, nil
}

func (a *GeminiAdapter) do(req *http.Request) (*http.Response, []byte, error) {
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, nil, classifyTransportError(err)
	}
	body, readErr := readUpstreamBody(resp.Body)
	_ = resp.Body.Close()
	if readErr != nil {
		return resp, nil, readErr
	}
	return resp, body, nil
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Role  string `json:"role"`
			Parts []struct {
				Text         string          `json:"text"`
				Thought      bool            `json:"thought"`
				FunctionCall json.RawMessage `json:"functionCall"`
				InlineData   json.RawMessage `json:"inlineData"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount        int64 `json:"promptTokenCount"`
		CandidatesTokenCount    int64 `json:"candidatesTokenCount"`
		TotalTokenCount         int64 `json:"totalTokenCount"`
		ThoughtsTokenCount      int64 `json:"thoughtsTokenCount"`
		CachedContentTokenCount int64 `json:"cachedContentTokenCount"`
	} `json:"usageMetadata"`
}

func geminiChatResponse(out geminiResponse, raw []byte) *protocol.ChatResponse {
	result := &protocol.ChatResponse{Raw: raw, Usage: geminiUsageMeters(out.UsageMetadata.PromptTokenCount, out.UsageMetadata.CandidatesTokenCount, out.UsageMetadata.TotalTokenCount, out.UsageMetadata.ThoughtsTokenCount, out.UsageMetadata.CachedContentTokenCount)}
	if len(out.Candidates) == 0 {
		result.Choices = []protocol.ChatChoice{{Index: 0, Message: protocol.Message{Role: "assistant"}}}
		return result
	}
	message := protocol.Message{Role: "assistant"}
	callState := &geminiCallState{}
	for _, part := range out.Candidates[0].Content.Parts {
		appendGeminiPart(&message, part.Text, part.Thought, part.FunctionCall, callState)
	}
	result.Choices = []protocol.ChatChoice{{Index: 0, Message: message, FinishReason: geminiFinishReason(out.Candidates[0].FinishReason, len(message.ToolCalls) > 0)}}
	return result
}

func emitGeminiEvent(event geminiResponse, raw []byte, callState *geminiCallState, emit func(protocol.StreamEvent) error) error {
	if len(event.Candidates) > 0 {
		candidate := event.Candidates[0]
		message := protocol.Message{Role: "assistant"}
		for _, part := range candidate.Content.Parts {
			appendGeminiPart(&message, part.Text, part.Thought, part.FunctionCall, callState)
		}
		if len(message.Content) > 0 || len(message.ToolCalls) > 0 || message.ReasoningContent != "" {
			if err := emit(protocol.StreamEvent{Type: "chunk", Delta: message, Raw: raw}); err != nil {
				return err
			}
		}
		if candidate.FinishReason != "" {
			if err := emit(protocol.StreamEvent{Type: "chunk", FinishReason: geminiFinishReason(candidate.FinishReason, callState.next > 0), Raw: raw}); err != nil {
				return err
			}
		}
	}
	usage := event.UsageMetadata
	// A usage-only frame (for example, thoughts or cached tokens reported
	// after the last candidate) must still surface as a usage event.
	if usage.PromptTokenCount != 0 || usage.CandidatesTokenCount != 0 || usage.TotalTokenCount != 0 ||
		usage.ThoughtsTokenCount != 0 || usage.CachedContentTokenCount != 0 {
		return emit(protocol.StreamEvent{Type: "usage", Usage: geminiUsageMeters(usage.PromptTokenCount, usage.CandidatesTokenCount, usage.TotalTokenCount, usage.ThoughtsTokenCount, usage.CachedContentTokenCount), Raw: raw})
	}
	return nil
}

// Gemini frames can contain multiple calls, so indices live for the entire
// response/stream, not just one chunk. Explicit upstream IDs keep their index.
type geminiCallState struct {
	indexes map[string]int
	next    int
}

var geminiCallSequence atomic.Uint64

func (s *geminiCallState) index(id string) int {
	if s.indexes == nil {
		s.indexes = make(map[string]int)
	}
	if index, ok := s.indexes[id]; ok {
		return index
	}
	index := s.next
	s.indexes[id] = index
	s.next++
	return index
}

func newGeminiCallID() string {
	return fmt.Sprintf("call_gemini_%x_%x", time.Now().UnixNano(), geminiCallSequence.Add(1))
}

// appendGeminiPart maps one candidate part onto the normalized assistant
// message. thought parts are model reasoning and never mix with visible
// content, even when a provider interleaves them.
func appendGeminiPart(message *protocol.Message, text string, thought bool, functionCall json.RawMessage, state *geminiCallState) {
	if text != "" {
		if thought {
			message.ReasoningContent += text
		} else {
			message.Content = append(message.Content, protocol.ContentPart{Type: "text", Text: text})
		}
	}
	if len(functionCall) > 0 {
		var call struct {
			ID   string          `json:"id"`
			Name string          `json:"name"`
			Args json.RawMessage `json:"args"`
		}
		if json.Unmarshal(functionCall, &call) == nil {
			if call.ID == "" {
				call.ID = newGeminiCallID()
			}
			if len(call.Args) == 0 {
				call.Args = json.RawMessage(`{}`)
			}
			index := state.index(call.ID)
			message.ToolCalls = append(message.ToolCalls, protocol.ToolCall{ID: call.ID, Type: "function", Index: &index, Function: protocol.ToolCallFunction{Name: call.Name, Arguments: string(call.Args)}})
		}
	}
}

// geminiUsageMeters keeps the established input/output/total convention.
// Reasoning and cache tokens are supplementary meters reported by the
// provider; they never change the total.
func geminiUsageMeters(prompt, candidates, total, thoughts, cached int64) model.Usage {
	meters := map[string]int64{"input": prompt, "output": candidates, "total": total}
	if thoughts != 0 {
		meters["reasoning"] = thoughts
	}
	if cached != 0 {
		meters["cache_input"] = cached
	}
	return model.Usage{Meters: meters}
}

// Sentinel errors for Gemini requests the adapter refuses locally. Each wraps
// ErrInvalidRequest so the gateway answers 400 instead of blaming the upstream,
// and every message is static: no caller-provided ID, function name or selector
// is echoed into logs or API errors.
var (
	errGeminiToolCallID            = fmt.Errorf("%w: Gemini tool_call_id has no preceding assistant function call", ErrInvalidRequest)
	errGeminiToolResultContent     = fmt.Errorf("%w: Gemini tool response only supports text content", ErrInvalidRequest)
	errGeminiToolChoiceUnsupported = fmt.Errorf("%w: Gemini tool_choice: unsupported selector", ErrInvalidRequest)
	errGeminiToolChoiceShape       = fmt.Errorf("%w: invalid Gemini tool_choice: expected auto, none, required or a named function", ErrInvalidRequest)
	errGeminiToolChoiceUndeclared  = fmt.Errorf("%w: Gemini tool_choice: named function is not declared in tools", ErrInvalidRequest)
	errGeminiToolChoiceNoTools     = fmt.Errorf(`%w: Gemini tool_choice: "required" needs declared tools`, ErrInvalidRequest)
	errGeminiReservedExtra         = fmt.Errorf("%w: Gemini extra payload fields cannot overwrite reserved keys", ErrInvalidRequest)
	errGeminiImageData             = fmt.Errorf("%w: Gemini image data must be base64 with an image media type", ErrInvalidRequest)
)

func (a *GeminiAdapter) buildPayload(req *protocol.ChatRequest) (map[string]any, error) {
	payload := map[string]any{"contents": make([]map[string]any, 0)}
	var systemParts []map[string]any
	contents := payload["contents"].([]map[string]any)
	// OpenAI tool results only identify a call ID; Gemini also needs its name.
	previousCalls := make(map[string]string)
	for _, message := range req.Messages {
		if message.Role == "system" || message.Role == "developer" {
			for _, part := range message.Content {
				if part.Type == "text" {
					systemParts = append(systemParts, map[string]any{"text": part.Text})
				}
			}
			continue
		}
		if message.Role == "tool" {
			name, ok := previousCalls[message.ToolCallID]
			if !ok || message.ToolCallID == "" {
				return nil, errGeminiToolCallID
			}
			response, err := geminiToolResponse(message.Content)
			if err != nil {
				return nil, err
			}
			contents = append(contents, map[string]any{"role": "user", "parts": []map[string]any{{"functionResponse": map[string]any{"id": message.ToolCallID, "name": name, "response": response}}}})
			continue
		}
		role := "user"
		if message.Role == "assistant" {
			role = "model"
		}
		parts, err := geminiParts(message.Content)
		if err != nil {
			return nil, err
		}
		if message.Role == "assistant" {
			for _, toolCall := range message.ToolCalls {
				args := json.RawMessage(toolCall.Function.Arguments)
				if len(args) == 0 {
					args = json.RawMessage(`{}`)
				}
				call := map[string]any{"name": toolCall.Function.Name, "args": args}
				if toolCall.ID != "" {
					call["id"] = toolCall.ID
					previousCalls[toolCall.ID] = toolCall.Function.Name
				}
				parts = append(parts, map[string]any{"functionCall": call})
			}
		}
		contents = append(contents, map[string]any{"role": role, "parts": parts})
	}
	payload["contents"] = contents
	if len(systemParts) > 0 {
		payload["systemInstruction"] = map[string]any{"parts": systemParts}
	}
	generation := map[string]any{}
	if req.Temperature != nil {
		generation["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		generation["topP"] = *req.TopP
	}
	if req.MaxTokens != nil {
		generation["maxOutputTokens"] = *req.MaxTokens
	}
	if len(req.Stop) > 0 {
		generation["stopSequences"] = req.Stop
	}
	if len(req.ResponseFormat) > 0 {
		var format map[string]json.RawMessage
		if json.Unmarshal(req.ResponseFormat, &format) == nil {
			if typ, ok := format["type"]; ok {
				var value string
				if json.Unmarshal(typ, &value) == nil && value == "json_object" {
					generation["responseMimeType"] = "application/json"
				}
			}
		}
	}
	if len(generation) > 0 {
		payload["generationConfig"] = generation
	}
	if len(req.Tools) > 0 {
		declarations := make([]map[string]any, 0, len(req.Tools))
		for _, tool := range req.Tools {
			declarations = append(declarations, map[string]any{"name": tool.Function.Name, "description": tool.Function.Description, "parameters": json.RawMessage(tool.Function.Parameters)})
		}
		payload["tools"] = []map[string]any{{"function_declarations": declarations}}
	}
	config, err := geminiFunctionCallingConfig(req.ToolChoice, req.Tools)
	if err != nil {
		return nil, err
	}
	if config != nil {
		payload["toolConfig"] = map[string]any{"functionCallingConfig": config}
	}
	for key, value := range req.Extra {
		if geminiReservedPayloadKey(key) {
			return nil, errGeminiReservedExtra
		}
		payload[key] = json.RawMessage(value)
	}
	return payload, nil
}

// geminiReservedPayloadKey reports whether a cross-protocol Extra field would
// overwrite a payload key the adapter builds itself.
func geminiReservedPayloadKey(key string) bool {
	switch key {
	case "contents", "systemInstruction", "generationConfig", "tools", "toolConfig":
		return true
	default:
		return false
	}
}

// Gemini requires a JSON object for functionResponse.response. Preserve object
// values as JSON; wrap all other text as a string rather than using RawMessage.
func geminiToolResponse(content []protocol.ContentPart) (any, error) {
	var text strings.Builder
	for _, part := range content {
		if part.Type != "text" && part.Type != "output_text" {
			return nil, errGeminiToolResultContent
		}
		text.WriteString(part.Text)
	}
	var object map[string]json.RawMessage
	if json.Unmarshal([]byte(text.String()), &object) == nil && object != nil {
		return object, nil
	}
	return map[string]any{"result": text.String()}, nil
}

func geminiFunctionCallingConfig(choice json.RawMessage, tools []protocol.Tool) (map[string]any, error) {
	if len(choice) == 0 || strings.TrimSpace(string(choice)) == "null" {
		return nil, nil
	}
	var mode string
	if err := json.Unmarshal(choice, &mode); err == nil {
		switch mode {
		case "auto":
			mode = "AUTO"
		case "none":
			mode = "NONE"
		case "required":
			mode = "ANY"
		default:
			return nil, errGeminiToolChoiceUnsupported
		}
	} else {
		var named struct {
			Type     string `json:"type"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}
		if json.Unmarshal(choice, &named) != nil || named.Type != "function" || named.Function.Name == "" {
			return nil, errGeminiToolChoiceShape
		}
		for _, tool := range tools {
			if tool.Function.Name == named.Function.Name {
				return map[string]any{"mode": "ANY", "allowedFunctionNames": []string{named.Function.Name}}, nil
			}
		}
		return nil, errGeminiToolChoiceUndeclared
	}
	if len(tools) == 0 {
		if mode == "ANY" {
			return nil, errGeminiToolChoiceNoTools
		}
		// With no declarations, AUTO/NONE are already the default behavior.
		return nil, nil
	}
	return map[string]any{"mode": mode}, nil
}

func geminiParts(content []protocol.ContentPart) ([]map[string]any, error) {
	parts := make([]map[string]any, 0, len(content))
	for _, part := range content {
		switch part.Type {
		case "text", "output_text":
			parts = append(parts, map[string]any{"text": part.Text})
		case "image_url", "input_image":
			imageData := part.ImageData
			if imageData == "" && strings.HasPrefix(part.ImageURL, "data:") {
				imageData = part.ImageURL
			}
			if imageData != "" {
				mediaType := part.MediaType
				if strings.HasPrefix(imageData, "data:") {
					header, encoded, hasComma := strings.Cut(imageData, ",")
					mimeType, encoding, hasEncoding := strings.Cut(strings.TrimPrefix(header, "data:"), ";")
					if !hasComma || !hasEncoding || encoding != "base64" || !strings.HasPrefix(mimeType, "image/") || (mediaType != "" && mediaType != mimeType) {
						return nil, errGeminiImageData
					}
					mediaType, imageData = mimeType, encoded
				}
				if mediaType == "" {
					mediaType = "image/png"
				}
				if !strings.HasPrefix(mediaType, "image/") || imageData == "" {
					return nil, errGeminiImageData
				}
				if _, err := base64.StdEncoding.Strict().DecodeString(imageData); err != nil {
					return nil, errGeminiImageData
				}
				parts = append(parts, map[string]any{"inlineData": map[string]any{"mimeType": mediaType, "data": imageData}})
			} else if part.ImageURL != "" {
				if strings.HasPrefix(strings.ToLower(part.ImageURL), "data:") {
					return nil, errGeminiImageData
				}
				parts = append(parts, map[string]any{"fileData": map[string]any{"fileUri": part.ImageURL}})
			}
		}
	}
	return parts, nil
}

// STOP with a function call is a tool handoff, not a completed text answer.
// Streaming calls may have appeared in an earlier frame than the STOP frame.
func geminiFinishReason(reason string, hadToolCalls bool) string {
	switch reason {
	case "STOP":
		if hadToolCalls {
			return "tool_calls"
		}
		return "stop"
	case "MAX_TOKENS":
		return "length"
	case "SAFETY", "RECITATION", "OTHER":
		return strings.ToLower(reason)
	default:
		return strings.ToLower(reason)
	}
}
