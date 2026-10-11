package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/zjvtit/omni-gateway/internal/model"
	"github.com/zjvtit/omni-gateway/internal/protocol"
	"github.com/zjvtit/omni-gateway/internal/upstream"
)

// OpenAIAdapter implements the Adapter interface for OpenAI-compatible APIs.
type OpenAIAdapter struct {
	cfg    Config
	client *http.Client
}

// NewOpenAIAdapter constructs an OpenAIAdapter.
func NewOpenAIAdapter(cfg Config) *OpenAIAdapter {
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: time.Duration(cfg.TimeoutMS) * time.Millisecond}
	}
	return &OpenAIAdapter{cfg: cfg, client: client}
}

func (a *OpenAIAdapter) Type() string { return "openai_compatible" }

func (a *OpenAIAdapter) endpoint(path string) string {
	return strings.TrimRight(a.cfg.BaseURL, "/") + path
}

func (a *OpenAIAdapter) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, a.endpoint(path), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if a.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+a.cfg.APIKey)
	}
	for k, v := range a.cfg.ExtraHeader {
		req.Header.Set(k, v)
	}
	return req, nil
}

func (a *OpenAIAdapter) do(req *http.Request) (*http.Response, []byte, error) {
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, nil, classifyTransportError(err)
	}
	body, err := readUpstreamBody(resp.Body)
	closeErr := resp.Body.Close()
	if err != nil {
		return resp, nil, fmt.Errorf("read upstream response: %w", err)
	}
	if closeErr != nil {
		return resp, body, fmt.Errorf("close upstream response: %w", closeErr)
	}
	if resp.StatusCode >= 400 {
		return resp, body, ClassifyHTTPError(resp.StatusCode, body, resp.Header)
	}
	return resp, body, nil
}

func classifyTransportError(err error) *Error {
	e := &Error{Type: "connection", Message: err.Error()}
	var netErr net.Error
	switch {
	case errors.Is(err, upstream.ErrBlocked):
		// The guarded transport refused the destination before dialing, so keep
		// the policy reason instead of reporting a generic connection failure.
		e.Type = "blocked"
		e.Message = upstream.ErrBlocked.Error()
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()):
		e.Type = "timeout"
	}
	return e
}

// Chat implements non-streaming chat completion.
func (a *OpenAIAdapter) Chat(ctx context.Context, req *protocol.ChatRequest) (*protocol.ChatResponse, error) {
	payload := a.buildChatPayload(req, false)
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	httpReq, err := a.newRequest(ctx, http.MethodPost, "/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	_, respBody, err := a.do(httpReq)
	if err != nil {
		return nil, err
	}
	var out struct {
		ID                string `json:"id"`
		Model             string `json:"model"`
		Created           int64  `json:"created"`
		SystemFingerprint string `json:"system_fingerprint"`
		Choices           []struct {
			Index        int             `json:"index"`
			Message      json.RawMessage `json:"message"`
			FinishReason string          `json:"finish_reason"`
			LogProbs     json.RawMessage `json:"logprobs"`
		} `json:"choices"`
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, err
	}
	cr := &protocol.ChatResponse{ID: out.ID, Model: out.Model, Created: out.Created, SystemFingerprint: out.SystemFingerprint, Raw: respBody}
	cr.Usage = parseOpenAIUsage(out.Usage)
	for _, c := range out.Choices {
		msg, err := parseOpenAIChatResponseMessage(c.Message)
		if err != nil {
			return nil, err
		}
		cr.Choices = append(cr.Choices, protocol.ChatChoice{
			Index: c.Index, Message: msg, FinishReason: c.FinishReason, LogProbs: c.LogProbs,
		})
	}
	return cr, nil
}

// ChatStream implements streaming chat completion.
func (a *OpenAIAdapter) ChatStream(ctx context.Context, req *protocol.ChatRequest, emit func(protocol.StreamEvent) error) error {
	payload := a.buildChatPayload(req, true)
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	httpReq, err := a.newRequest(ctx, http.MethodPost, "/chat/completions", bytes.NewReader(body))
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
		b, readErr := readUpstreamBody(resp.Body)
		if readErr != nil {
			return fmt.Errorf("read upstream error response: %w", readErr)
		}
		return ClassifyHTTPError(resp.StatusCode, b, resp.Header)
	}
	return a.parseCompletedSSE(resp.Body, emit)
}

// parseCompletedSSE only accepts a stream with an explicit terminal event.
// A clean transport EOF alone is not proof that the model finished generating;
// returning nil here would make HTTP handlers invent a successful [DONE].
func (a *OpenAIAdapter) parseCompletedSSE(body io.Reader, emit func(protocol.StreamEvent) error) error {
	completed := false
	err := a.parseSSE(body, func(event protocol.StreamEvent) error {
		if event.Type == "done" {
			completed = true
		}
		return emit(event)
	})
	if err != nil {
		return err
	}
	if !completed {
		return &Error{Type: "server", StatusCode: http.StatusBadGateway, Message: "upstream stream ended before completion"}
	}
	return nil
}

// Responses implements non-streaming Responses API.
func (a *OpenAIAdapter) Responses(ctx context.Context, req *protocol.ResponsesRequest) (*protocol.ResponsesResponse, error) {
	payload := a.buildResponsesPayload(req, false)
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	httpReq, err := a.newRequest(ctx, http.MethodPost, "/responses", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	_, respBody, err := a.do(httpReq)
	if err != nil {
		return nil, err
	}
	var out struct {
		ID     string          `json:"id"`
		Model  string          `json:"model"`
		Output json.RawMessage `json:"output"`
		Usage  json.RawMessage `json:"usage"`
		Status string          `json:"status"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, err
	}
	rr := &protocol.ResponsesResponse{ID: out.ID, Model: out.Model, Output: out.Output, Status: out.Status, Raw: respBody}
	rr.Usage = parseOpenAIUsage(out.Usage)
	return rr, nil
}

// ResponsesStream implements streaming Responses API.
func (a *OpenAIAdapter) ResponsesStream(ctx context.Context, req *protocol.ResponsesRequest, emit func(protocol.StreamEvent) error) error {
	payload := a.buildResponsesPayload(req, true)
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	httpReq, err := a.newRequest(ctx, http.MethodPost, "/responses", bytes.NewReader(body))
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
		b, readErr := readUpstreamBody(resp.Body)
		if readErr != nil {
			return fmt.Errorf("read upstream error response: %w", readErr)
		}
		return ClassifyHTTPError(resp.StatusCode, b, resp.Header)
	}
	return a.parseCompletedSSE(resp.Body, emit)
}

// buildResponsesPayload constructs an OpenAI Responses request body.
func (a *OpenAIAdapter) buildResponsesPayload(req *protocol.ResponsesRequest, stream bool) map[string]any {
	payload := map[string]any{
		"model":  req.Model,
		"stream": stream,
	}
	if len(req.Input) > 0 {
		payload["input"] = json.RawMessage(req.Input)
	}
	if req.Instructions != "" {
		payload["instructions"] = req.Instructions
	}
	if req.PreviousResponseID != "" {
		payload["previous_response_id"] = req.PreviousResponseID
	}
	for k, v := range req.Extra {
		payload[k] = json.RawMessage(v)
	}
	return payload
}

// ListModels implements model discovery.
func (a *OpenAIAdapter) ListModels(ctx context.Context) ([]string, error) {
	httpReq, err := a.newRequest(ctx, http.MethodGet, "/models", nil)
	if err != nil {
		return nil, err
	}
	_, respBody, err := a.do(httpReq)
	if err != nil {
		return nil, err
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		ids = append(ids, m.ID)
	}
	return ids, nil
}

// buildChatPayload constructs the OpenAI chat completion request body.
func (a *OpenAIAdapter) buildChatPayload(req *protocol.ChatRequest, stream bool) map[string]any {
	payload := map[string]any{
		"model":  req.Model,
		"stream": stream,
	}
	if len(req.Messages) > 0 {
		payload["messages"] = a.toOpenAIMessages(req.Messages)
	}
	if len(req.Tools) > 0 {
		payload["tools"] = a.toOpenAITools(req.Tools)
	}
	if req.ToolChoice != nil {
		payload["tool_choice"] = json.RawMessage(req.ToolChoice)
	}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		payload["top_p"] = *req.TopP
	}
	if req.MaxTokens != nil {
		field := "max_tokens"
		if req.MaxTokensField == "max_completion_tokens" {
			field = req.MaxTokensField
		}
		payload[field] = *req.MaxTokens
	}
	if len(req.Stop) > 0 {
		payload["stop"] = req.Stop
	}
	if req.ResponseFormat != nil {
		payload["response_format"] = json.RawMessage(req.ResponseFormat)
	}
	if req.ReasoningEffort != "" {
		payload["reasoning_effort"] = req.ReasoningEffort
	}
	if req.User != "" {
		payload["user"] = req.User
	}
	if stream {
		// OpenAI-compatible APIs that support streamed usage require this
		// option to be present in the request. An explicit Extra value can
		// replace it below for providers with a different convention.
		payload["stream_options"] = map[string]any{"include_usage": true}
	}
	// Rebuild the payload from decoded fields; never forward an inbound body or
	// headers wholesale. Same-protocol extensions remain JSON values.
	for k, v := range req.Extra {
		payload[k] = json.RawMessage(v)
	}
	if req.CompatibilityProfile == "deepseek" {
		applyDeepSeekChatRules(payload, req)
	}
	return payload
}

// DeepSeek-specific Chat handling is selected only by the resolved provider.
// A valid thinking.type wins over effort; budget_tokens and the OpenAI-style
// reasoning_effort are not sent to DeepSeek. No capability is inferred from a
// model name: only the selected provider model's explicit record can opt in to
// the multi-turn single-space compatibility workaround.
func applyDeepSeekChatRules(payload map[string]any, req *protocol.ChatRequest) {
	thinkingType := ""
	if raw, ok := req.Extra["thinking"]; ok {
		var thinking struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &thinking) == nil && (thinking.Type == "enabled" || thinking.Type == "disabled") {
			thinkingType = thinking.Type
		}
	}
	delete(payload, "thinking")
	delete(payload, "reasoning_effort")
	if thinkingType == "" && req.ReasoningEffort != "" {
		thinkingType = "enabled"
		if req.ReasoningEffort == "none" {
			thinkingType = "disabled"
		}
	}
	if thinkingType != "" {
		payload["thinking"] = map[string]string{"type": thinkingType}
	}
	if messages, ok := payload["messages"].([]map[string]any); ok {
		for _, message := range messages {
			if message["role"] != "assistant" {
				continue
			}
			if message["reasoning_content"] == nil {
				if fields, ok := message["provider_specific_fields"].(json.RawMessage); ok {
					var providerFields map[string]json.RawMessage
					if json.Unmarshal(fields, &providerFields) == nil {
						var reasoning string
						if json.Unmarshal(providerFields["reasoning_content"], &reasoning) == nil && reasoning != "" {
							message["reasoning_content"] = reasoning
							delete(providerFields, "reasoning_content")
							if len(providerFields) == 0 {
								delete(message, "provider_specific_fields")
							} else {
								message["provider_specific_fields"] = providerFields
							}
						}
					}
				}
			}
			if thinkingType == "enabled" && req.ModelSupportsReasoning && message["reasoning_content"] == nil {
				message["reasoning_content"] = " "
			}
		}
	}
	if len(req.Tools) == 0 {
		delete(payload, "tool_choice")
		delete(payload, "parallel_tool_calls")
		return
	}
	functionTools := make([]map[string]any, 0, len(req.Tools))
	functionNames := make(map[string]bool)
	for i, tool := range req.Tools {
		if tool.Type == "function" {
			functionTools = append(functionTools, payload["tools"].([]map[string]any)[i])
			functionNames[tool.Function.Name] = true
		}
	}
	if len(functionTools) == 0 {
		delete(payload, "tools")
		delete(payload, "tool_choice")
		delete(payload, "parallel_tool_calls")
		return
	}
	payload["tools"] = functionTools
	if choice, ok := payload["tool_choice"]; ok {
		valid := false
		if raw, ok := choice.(json.RawMessage); ok {
			var selector string
			if json.Unmarshal(raw, &selector) == nil {
				valid = selector == "auto" || selector == "none" || selector == "required"
			} else {
				var selected struct {
					Type     string `json:"type"`
					Function struct {
						Name string `json:"name"`
					} `json:"function"`
				}
				valid = json.Unmarshal(raw, &selected) == nil && selected.Type == "function" && functionNames[selected.Function.Name]
			}
		}
		if !valid {
			delete(payload, "tool_choice")
		}
	}
}

func (a *OpenAIAdapter) toOpenAIMessages(msgs []protocol.Message) []map[string]any {
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		mm := map[string]any{"role": m.Role}
		if m.Name != "" {
			mm["name"] = m.Name
		}
		if m.ToolCallID != "" {
			mm["tool_call_id"] = m.ToolCallID
		}
		if m.ReasoningContent != "" {
			mm["reasoning_content"] = m.ReasoningContent
		}
		if len(m.ToolCalls) > 0 {
			tcs := make([]map[string]any, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				if fields := rawJSONObject(tc.Raw); fields != nil {
					tcs = append(tcs, fields)
					continue
				}
				tcs = append(tcs, map[string]any{
					"id": tc.ID, "type": tc.Type,
					"function": map[string]any{"name": tc.Function.Name, "arguments": tc.Function.Arguments},
				})
			}
			mm["tool_calls"] = tcs
		}
		if len(m.Content) == 1 && m.Content[0].Type == "text" && len(m.Content[0].Raw) == 0 {
			mm["content"] = m.Content[0].Text
		} else if len(m.Content) > 0 {
			parts := make([]map[string]any, 0, len(m.Content))
			for _, cp := range m.Content {
				if fields := rawJSONObject(cp.Raw); fields != nil {
					parts = append(parts, fields)
					continue
				}
				switch cp.Type {
				case "text":
					parts = append(parts, map[string]any{"type": "text", "text": cp.Text})
				case "image_url", "input_image":
					imageURL := cp.ImageURL
					if imageURL == "" && cp.ImageData != "" {
						imageURL = cp.ImageData
						if !strings.HasPrefix(imageURL, "data:") {
							mediaType := cp.MediaType
							if mediaType == "" {
								mediaType = "image/png"
							}
							imageURL = "data:" + mediaType + ";base64," + imageURL
						}
					}
					parts = append(parts, map[string]any{
						"type":      "image_url",
						"image_url": map[string]any{"url": imageURL},
					})
				default:
					parts = append(parts, map[string]any{"type": cp.Type, cp.Type: cp.Text})
				}
			}
			mm["content"] = parts
		}
		for key, value := range m.Extra {
			mm[key] = json.RawMessage(value)
		}
		out = append(out, mm)
	}
	return out
}

// toOpenAITools converts normalized tools to OpenAI format.
func (a *OpenAIAdapter) toOpenAITools(tools []protocol.Tool) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		if fields := rawJSONObject(t.Raw); fields != nil {
			out = append(out, fields)
			continue
		}
		function := map[string]any{
			"name":        t.Function.Name,
			"description": t.Function.Description,
		}
		if len(t.Function.Parameters) > 0 {
			function["parameters"] = json.RawMessage(t.Function.Parameters)
		}
		toolType := t.Type
		if toolType == "" {
			toolType = "function"
		}
		out = append(out, map[string]any{"type": toolType, "function": function})
	}
	return out
}

// rawJSONObject decodes one JSON object into raw-valued fields, preserving
// unknown same-protocol fields without forwarding an unvalidated request body.
func rawJSONObject(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var decoded map[string]json.RawMessage
	if json.Unmarshal(raw, &decoded) != nil || decoded == nil {
		return nil
	}
	fields := make(map[string]any, len(decoded))
	for key, value := range decoded {
		fields[key] = value
	}
	return fields
}

// parseOpenAIUsage extracts usage from a raw JSON usage object.
func parseOpenAIUsage(raw json.RawMessage) model.Usage {
	u := model.NewUsage()
	if !hasJSONValue(raw) {
		return u
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return u
	}

	if n, ok := usageInt(fields, "prompt_tokens"); ok {
		u.Meters["input"] = n
	} else if n, ok := usageInt(fields, "input_tokens"); ok {
		u.Meters["input"] = n
	}
	if n, ok := usageInt(fields, "completion_tokens"); ok {
		u.Meters["output"] = n
	} else if n, ok := usageInt(fields, "output_tokens"); ok {
		u.Meters["output"] = n
	}
	if n, ok := usageInt(fields, "total_tokens"); ok {
		u.Meters["total"] = n
	}

	hasCache := false
	if n, ok := usageInt(fields, "cached_tokens"); ok {
		u.Meters["cache_input"] = n
		hasCache = true
	}
	hasReasoning := false
	if n, ok := usageInt(fields, "reasoning_tokens"); ok {
		u.Meters["reasoning"] = n
		hasReasoning = true
	}

	// OpenAI has used both prompt/input_tokens_details and completion/output_tokens_details across its APIs.
	for _, key := range []string{
		"prompt_tokens_details", "input_tokens_details",
		"completion_tokens_details", "output_tokens_details",
	} {
		detailsRaw, ok := fields[key]
		if !ok {
			continue
		}
		var details map[string]json.RawMessage
		if err := json.Unmarshal(detailsRaw, &details); err != nil {
			continue
		}
		if !hasCache {
			if n, ok := usageInt(details, "cached_tokens"); ok {
				u.Meters["cache_input"] = n
				hasCache = true
			}
		}
		if !hasReasoning {
			if n, ok := usageInt(details, "reasoning_tokens"); ok {
				u.Meters["reasoning"] = n
				hasReasoning = true
			}
		}
	}
	return u
}

func usageInt(fields map[string]json.RawMessage, key string) (int64, bool) {
	raw, ok := fields[key]
	if !ok || !hasJSONValue(raw) {
		return 0, false
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, false
	}
	return value, true
}

// parseOpenAIChatResponseMessage accepts the reasoning alias only on upstream
// Chat responses. Keep ParseOpenAIMessage (the inbound request parser) unchanged.
func parseOpenAIChatResponseMessage(raw json.RawMessage) (protocol.Message, error) {
	msg, err := parseOpenAIMessage(raw)
	if err != nil {
		return msg, err
	}
	if alias, ok := msg.Extra["reasoning"]; ok && hasJSONValue(alias) {
		var reasoning string
		if json.Unmarshal(alias, &reasoning) == nil {
			// A present canonical field wins even when it is empty or null.
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				return msg, err
			}
			if _, present := fields["reasoning_content"]; !present {
				msg.ReasoningContent = reasoning
			}
			delete(msg.Extra, "reasoning")
		}
	}
	return msg, nil
}

// ParseOpenAIMessage converts an OpenAI-compatible message payload into the
// normalized protocol message used by the gateway request path.
func ParseOpenAIMessage(raw json.RawMessage) (protocol.Message, error) {
	return parseOpenAIMessage(raw)
}

// parseOpenAIMessage converts a raw JSON message into a normalized Message.
func parseOpenAIMessage(raw json.RawMessage) (protocol.Message, error) {
	msg := protocol.Message{}
	if !hasJSONValue(raw) {
		return msg, nil
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return msg, err
	}
	if role, ok := fields["role"]; ok {
		if err := json.Unmarshal(role, &msg.Role); err != nil {
			return msg, fmt.Errorf("decode message role: %w", err)
		}
	}
	if name, ok := fields["name"]; ok {
		if err := json.Unmarshal(name, &msg.Name); err != nil {
			return msg, fmt.Errorf("decode message name: %w", err)
		}
	}
	if reasoning, ok := fields["reasoning_content"]; ok && hasJSONValue(reasoning) {
		if err := json.Unmarshal(reasoning, &msg.ReasoningContent); err != nil {
			return msg, fmt.Errorf("decode message reasoning_content: %w", err)
		}
	}
	if content, ok := fields["content"]; ok {
		parts, err := parseOpenAIContent(content)
		if err != nil {
			return msg, err
		}
		msg.Content = parts
	}
	if toolCalls, ok := fields["tool_calls"]; ok && hasJSONValue(toolCalls) {
		var rawCalls []json.RawMessage
		if err := json.Unmarshal(toolCalls, &rawCalls); err != nil {
			return msg, fmt.Errorf("decode message tool calls: %w", err)
		}
		for _, rawCall := range rawCalls {
			var call protocol.ToolCall
			if err := json.Unmarshal(rawCall, &call); err != nil {
				return msg, fmt.Errorf("decode message tool call: %w", err)
			}
			call.Raw = append(json.RawMessage(nil), rawCall...)
			msg.ToolCalls = append(msg.ToolCalls, call)
		}
	}
	if toolCallID, ok := fields["tool_call_id"]; ok {
		if err := json.Unmarshal(toolCallID, &msg.ToolCallID); err != nil {
			return msg, fmt.Errorf("decode message tool call id: %w", err)
		}
	}
	for key, value := range fields {
		switch key {
		case "role", "name", "reasoning_content", "content", "tool_calls", "tool_call_id":
			continue
		}
		if msg.Extra == nil {
			msg.Extra = make(map[string]json.RawMessage)
		}
		msg.Extra[key] = append(json.RawMessage(nil), value...)
	}
	return msg, nil
}

func parseOpenAIContent(raw json.RawMessage) ([]protocol.ContentPart, error) {
	if !hasJSONValue(raw) {
		return nil, nil
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return []protocol.ContentPart{{Type: "text", Text: text}}, nil
	}

	var rawParts []json.RawMessage
	if err := json.Unmarshal(raw, &rawParts); err != nil {
		// A few compatible APIs return a single content-part object instead
		// of an array. Accept that shape without weakening malformed JSON
		// detection for other values.
		var single map[string]json.RawMessage
		if objectErr := json.Unmarshal(raw, &single); objectErr != nil {
			return nil, fmt.Errorf("decode message content: %w", err)
		}
		part, partErr := parseOpenAIContentPart(single)
		if partErr != nil {
			return nil, partErr
		}
		part.Raw = append(json.RawMessage(nil), raw...)
		return []protocol.ContentPart{part}, nil
	}

	parts := make([]protocol.ContentPart, 0, len(rawParts))
	for _, rawPart := range rawParts {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(rawPart, &fields); err != nil {
			return nil, fmt.Errorf("decode message content part: %w", err)
		}
		part, err := parseOpenAIContentPart(fields)
		if err != nil {
			return nil, err
		}
		part.Raw = append(json.RawMessage(nil), rawPart...)
		parts = append(parts, part)
	}
	return parts, nil
}

func parseOpenAIContentPart(fields map[string]json.RawMessage) (protocol.ContentPart, error) {
	part := protocol.ContentPart{}
	if typeRaw, ok := fields["type"]; ok {
		if err := json.Unmarshal(typeRaw, &part.Type); err != nil {
			return part, fmt.Errorf("decode content part type: %w", err)
		}
	}
	if textRaw, ok := fields["text"]; ok {
		if err := json.Unmarshal(textRaw, &part.Text); err != nil && string(textRaw) != "null" {
			return part, fmt.Errorf("decode content part text: %w", err)
		}
	}
	if mediaType, ok := fields["media_type"]; ok {
		if err := json.Unmarshal(mediaType, &part.MediaType); err != nil {
			return part, fmt.Errorf("decode content part media type: %w", err)
		}
	}
	if data, ok := fields["image_data"]; ok {
		if err := json.Unmarshal(data, &part.ImageData); err != nil {
			return part, fmt.Errorf("decode content image data: %w", err)
		}
	}
	if data, ok := fields["data"]; ok && part.ImageData == "" {
		if err := json.Unmarshal(data, &part.ImageData); err != nil {
			return part, fmt.Errorf("decode content image data: %w", err)
		}
	}
	if imageURL, ok := fields["image_url"]; ok {
		if err := parseOpenAIImageURL(imageURL, &part); err != nil {
			return part, err
		}
	}
	if part.Text == "" && part.Type != "text" {
		// Preserve simple Responses fields such as refusal or output_text when
		// they use a field named after the content type.
		if value, ok := fields[part.Type]; ok {
			_ = json.Unmarshal(value, &part.Text)
		}
	}
	return part, nil
}

func parseOpenAIImageURL(raw json.RawMessage, part *protocol.ContentPart) error {
	var url string
	if err := json.Unmarshal(raw, &url); err == nil {
		part.ImageURL = url
		return nil
	}
	var image map[string]json.RawMessage
	if err := json.Unmarshal(raw, &image); err != nil {
		return fmt.Errorf("decode content image URL: %w", err)
	}
	if urlRaw, ok := image["url"]; ok {
		if err := json.Unmarshal(urlRaw, &part.ImageURL); err != nil {
			return fmt.Errorf("decode content image URL value: %w", err)
		}
	}
	if dataRaw, ok := image["data"]; ok && part.ImageData == "" {
		if err := json.Unmarshal(dataRaw, &part.ImageData); err != nil {
			return fmt.Errorf("decode content image data: %w", err)
		}
	}
	if mediaRaw, ok := image["media_type"]; ok && part.MediaType == "" {
		if err := json.Unmarshal(mediaRaw, &part.MediaType); err != nil {
			return fmt.Errorf("decode content image media type: %w", err)
		}
	}
	return nil
}
