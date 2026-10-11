package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/zjvtit/omni-gateway/internal/model"
	"github.com/zjvtit/omni-gateway/internal/protocol"
)

// AnthropicAdapter implements the native Anthropic Messages API for chat
// requests. Responses API calls are intentionally unsupported because the
// provider does not expose OpenAI's stateful Responses contract.
type AnthropicAdapter struct {
	cfg    Config
	client *http.Client
}

func NewAnthropicAdapter(cfg Config) *AnthropicAdapter {
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: time.Duration(cfg.TimeoutMS) * time.Millisecond}
	}
	return &AnthropicAdapter{cfg: cfg, client: client}
}

func (a *AnthropicAdapter) Type() string { return "anthropic" }

func (a *AnthropicAdapter) endpoint(path string) string {
	base := strings.TrimRight(a.cfg.BaseURL, "/")
	if strings.HasSuffix(base, "/v1") {
		return base + path
	}
	return base + "/v1" + path
}

func (a *AnthropicAdapter) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	if strings.TrimSpace(a.cfg.BaseURL) == "" {
		return nil, errors.New("Anthropic base URL is empty")
	}
	req, err := http.NewRequestWithContext(ctx, method, a.endpoint(path), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if a.cfg.APIKey != "" {
		req.Header.Set("x-api-key", a.cfg.APIKey)
	}
	req.Header.Set("anthropic-version", "2023-06-01")
	for key, value := range a.cfg.ExtraHeader {
		req.Header.Set(key, value)
	}
	return req, nil
}

func (a *AnthropicAdapter) Chat(ctx context.Context, req *protocol.ChatRequest) (*protocol.ChatResponse, error) {
	payload, err := a.buildPayload(req, false)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	httpReq, err := a.newRequest(ctx, http.MethodPost, "/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, classifyTransportError(err)
	}
	responseBody, readErr := readUpstreamBody(resp.Body)
	closeErr := resp.Body.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read Anthropic response: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close Anthropic response: %w", closeErr)
	}
	if resp.StatusCode >= 400 {
		return nil, ClassifyHTTPError(resp.StatusCode, responseBody, resp.Header)
	}
	var out anthropicMessageResponse
	if err := json.Unmarshal(responseBody, &out); err != nil {
		return nil, err
	}
	result := &protocol.ChatResponse{ID: out.ID, Model: out.Model, Raw: responseBody, Usage: model.NewUsage()}
	result.Usage.Meters["input"] = out.Usage.InputTokens
	result.Usage.Meters["output"] = out.Usage.OutputTokens
	result.Usage.Meters["total"] = out.Usage.InputTokens + out.Usage.OutputTokens
	if out.Usage.CacheReadInputTokens != 0 {
		result.Usage.Meters["cache_input"] = out.Usage.CacheReadInputTokens
	}
	if out.Usage.CacheCreationInputTokens != 0 {
		result.Usage.Meters["cache_creation"] = out.Usage.CacheCreationInputTokens
	}
	// Anthropic reports one logical assistant turn as an ordered list of
	// content blocks. Aggregate them into a single choice so callers see the
	// text, thinking and tool calls the way the message was actually
	// generated, and keep thinking text out of the visible content.
	message := protocol.Message{Role: "assistant"}
	for _, block := range out.Content {
		switch block.Type {
		case "text":
			message.Content = append(message.Content, protocol.ContentPart{Type: "text", Text: block.Text})
		case "thinking":
			message.ReasoningContent += block.Thinking
		case "tool_use":
			index := len(message.ToolCalls)
			message.ToolCalls = append(message.ToolCalls, protocol.ToolCall{ID: block.ID, Type: "function", Index: &index, Function: protocol.ToolCallFunction{Name: block.Name, Arguments: string(block.Input)}})
		}
	}
	finishReason := anthropicFinishReason(out.StopReason)
	if len(message.ToolCalls) > 0 && (finishReason == "" || finishReason == "stop") {
		finishReason = "tool_calls"
	}
	result.Choices = []protocol.ChatChoice{{Index: 0, Message: message, FinishReason: finishReason}}
	return result, nil
}

func (a *AnthropicAdapter) ChatStream(ctx context.Context, req *protocol.ChatRequest, emit func(protocol.StreamEvent) error) error {
	payload, err := a.buildPayload(req, true)
	if err != nil {
		return err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	httpReq, err := a.newRequest(ctx, http.MethodPost, "/messages", bytes.NewReader(body))
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
			return fmt.Errorf("read Anthropic error response: %w", readErr)
		}
		return ClassifyHTTPError(resp.StatusCode, responseBody, resp.Header)
	}
	// Anthropic content_block indexes number every block of the message, while
	// OpenAI tool_calls[].index counts tool calls only. Map each tool_use block
	// to the next contiguous tool index when it appears, so text/thinking
	// blocks never consume one and a block's start and its input_json_delta
	// share the same tool index.
	toolIndexByBlock := map[int]int{}
	nextToolIndex := 0
	toolIndexFor := func(blockIndex int) int {
		if index, ok := toolIndexByBlock[blockIndex]; ok {
			return index
		}
		index := nextToolIndex
		toolIndexByBlock[blockIndex] = index
		nextToolIndex++
		return index
	}
	// Anthropic reports cumulative token counters in message_start and in every
	// message_delta, while the gateway adds each usage event it receives.
	// Track the cumulative values and emit only positive per-meter deltas.
	usageDeltas := anthropicUsageDeltas{}
	return parseNamedSSE(resp.Body, func(eventName string, data []byte) error {
		if strings.TrimSpace(string(data)) == "[DONE]" {
			return emit(protocol.StreamEvent{Type: "done", Raw: data})
		}
		var event anthropicStreamEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return fmt.Errorf("decode Anthropic stream event: %w", err)
		}
		eventType := event.Type
		if eventType == "" {
			eventType = eventName
		}
		switch eventType {
		case "message_start":
			meters := usageDeltas.delta(event.Message.Usage)
			if len(meters) == 0 {
				return nil
			}
			return emit(protocol.StreamEvent{Type: "usage", Usage: model.Usage{Meters: meters}, Raw: data})
		case "content_block_start":
			if event.ContentBlock.Type == "tool_use" {
				index := toolIndexFor(event.Index)
				return emit(protocol.StreamEvent{Type: "chunk", Delta: protocol.Message{Role: "assistant", ToolCalls: []protocol.ToolCall{{Index: &index, ID: event.ContentBlock.ID, Type: "function", Function: protocol.ToolCallFunction{Name: event.ContentBlock.Name}}}}, Raw: data})
			}
		case "content_block_delta":
			if event.Delta.Type == "text_delta" {
				return emit(protocol.StreamEvent{Type: "chunk", Delta: protocol.Message{Role: "assistant", Content: []protocol.ContentPart{{Type: "text", Text: event.Delta.Text}}}, Raw: data})
			}
			if event.Delta.Type == "thinking_delta" {
				if event.Delta.Thinking == "" {
					return nil
				}
				return emit(protocol.StreamEvent{Type: "chunk", Delta: protocol.Message{Role: "assistant", ReasoningContent: event.Delta.Thinking}, Raw: data})
			}
			if event.Delta.Type == "input_json_delta" {
				index := toolIndexFor(event.Index)
				return emit(protocol.StreamEvent{Type: "chunk", Delta: protocol.Message{Role: "assistant", ToolCalls: []protocol.ToolCall{{Index: &index, Type: "function", Function: protocol.ToolCallFunction{Arguments: event.Delta.PartialJSON}}}}, Raw: data})
			}
			// signature_delta carries an opaque provider signature for the
			// preceding thinking block. The normalized Chat model has no field
			// for it, so it is intentionally dropped instead of being
			// fabricated into visible content or reasoning text.
		case "message_delta":
			if meters := usageDeltas.delta(event.Usage); len(meters) > 0 {
				if err := emit(protocol.StreamEvent{Type: "usage", Usage: model.Usage{Meters: meters}, Raw: data}); err != nil {
					return err
				}
			}
			if event.Delta.StopReason != "" {
				return emit(protocol.StreamEvent{Type: "chunk", FinishReason: anthropicFinishReason(event.Delta.StopReason), Raw: data})
			}
		case "message_stop":
			return emit(protocol.StreamEvent{Type: "done", Raw: data})
		case "error":
			return emit(protocol.StreamEvent{Type: "error", Error: anthropicErrorMessage(event.Error), Raw: data})
		default:
			return emit(protocol.StreamEvent{Type: "chunk", Raw: data})
		}
		return nil
	})
}

func (a *AnthropicAdapter) Responses(context.Context, *protocol.ResponsesRequest) (*protocol.ResponsesResponse, error) {
	return nil, ErrUnsupported
}

func (a *AnthropicAdapter) ResponsesStream(context.Context, *protocol.ResponsesRequest, func(protocol.StreamEvent) error) error {
	return ErrUnsupported
}

func (a *AnthropicAdapter) ListModels(ctx context.Context) ([]string, error) {
	httpReq, err := a.newRequest(ctx, http.MethodGet, "/models", nil)
	if err != nil {
		return nil, err
	}
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, classifyTransportError(err)
	}
	body, readErr := readUpstreamBody(resp.Body)
	_ = resp.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	if resp.StatusCode >= 400 {
		return nil, ClassifyHTTPError(resp.StatusCode, body, resp.Header)
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Data))
	for _, item := range out.Data {
		if item.ID != "" {
			ids = append(ids, item.ID)
		}
	}
	return ids, nil
}

type anthropicMessageResponse struct {
	ID         string `json:"id"`
	Model      string `json:"model"`
	StopReason string `json:"stop_reason"`
	Content    []struct {
		Type     string          `json:"type"`
		Text     string          `json:"text"`
		Thinking string          `json:"thinking"`
		ID       string          `json:"id"`
		Name     string          `json:"name"`
		Input    json.RawMessage `json:"input"`
	} `json:"content"`
	Usage anthropicUsage `json:"usage"`
}

type anthropicUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
}

type anthropicStreamEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message struct {
		Usage anthropicUsage `json:"usage"`
	} `json:"message"`
	ContentBlock struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage anthropicUsage `json:"usage"`
	Error any            `json:"error"`
}

func (a *AnthropicAdapter) buildPayload(req *protocol.ChatRequest, stream bool) (map[string]any, error) {
	payload := map[string]any{"model": req.Model, "max_tokens": 4096, "messages": make([]map[string]any, 0), "stream": stream}
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		payload["max_tokens"] = *req.MaxTokens
	}
	messages := payload["messages"].([]map[string]any)
	var system []string
	// Consecutive OpenAI tool messages are the results of one assistant turn.
	// Anthropic expects them as a single user turn whose content array carries
	// one tool_result per call, so a run of tool messages is appended to the
	// group started by the first one and any other message closes the group.
	// A tool_result must refer to one unconsumed tool_use from the immediately
	// preceding assistant turn. Intervening messages close that turn, and each
	// result consumes its ID so duplicate results cannot be forwarded.
	toolGroup := -1
	assistantToolCalls := make(map[string]struct{})
	for _, message := range req.Messages {
		if message.Role == "system" || message.Role == "developer" {
			for _, part := range message.Content {
				if part.Type == "text" {
					system = append(system, part.Text)
				}
			}
			toolGroup = -1
			clear(assistantToolCalls)
			continue
		}
		if message.Role == "tool" {
			if message.ToolCallID == "" {
				return nil, errAnthropicToolResultMissingID
			}
			if _, ok := assistantToolCalls[message.ToolCallID]; !ok {
				return nil, errAnthropicToolResultUnknownID
			}
			block, err := anthropicToolResultBlock(message)
			if err != nil {
				return nil, err
			}
			delete(assistantToolCalls, message.ToolCallID)
			if toolGroup >= 0 {
				group := messages[toolGroup]
				group["content"] = append(group["content"].([]map[string]any), block)
				continue
			}
			messages = append(messages, map[string]any{"role": "user", "content": []map[string]any{block}})
			toolGroup = len(messages) - 1
			continue
		}
		clear(assistantToolCalls)
		if message.Role == "assistant" {
			for _, toolCall := range message.ToolCalls {
				if toolCall.ID != "" {
					assistantToolCalls[toolCall.ID] = struct{}{}
				}
			}
		}
		toolGroup = -1
		messages = append(messages, map[string]any{"role": message.Role, "content": anthropicContent(message)})
	}
	payload["messages"] = messages
	if len(system) > 0 {
		payload["system"] = strings.Join(system, "\n\n")
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(req.Tools))
		for _, tool := range req.Tools {
			tools = append(tools, map[string]any{"name": tool.Function.Name, "description": tool.Function.Description, "input_schema": json.RawMessage(tool.Function.Parameters)})
		}
		payload["tools"] = tools
	}
	if req.ToolChoice != nil {
		choice, err := anthropicToolChoice(req.ToolChoice, req.Tools)
		if err != nil {
			return nil, err
		}
		if choice != nil {
			payload["tool_choice"] = choice
		}
	}
	for key, value := range req.Extra {
		if anthropicReservedPayloadKey(key) {
			return nil, errAnthropicReservedExtra
		}
		payload[key] = json.RawMessage(value)
	}
	return payload, nil
}

// anthropicReservedPayloadKey reports whether a cross-protocol Extra field
// would overwrite a payload key the adapter builds itself.
func anthropicReservedPayloadKey(key string) bool {
	switch key {
	case "model", "messages", "system", "tools", "tool_choice", "max_tokens", "stream":
		return true
	default:
		return false
	}
}

// Sentinel errors returned for tool_choice values the Anthropic Messages API
// cannot represent. Each wraps ErrInvalidRequest so the gateway answers 400
// instead of blaming the upstream, and the texts are static so no part of the
// caller's JSON (tool names included) is echoed into logs or API errors.
var (
	errAnthropicToolChoiceShape      = fmt.Errorf(`%w: Anthropic tool_choice: unsupported shape; expected "auto", "required", "none", or {"type":"function","function":{"name":"..."}}`, ErrInvalidRequest)
	errAnthropicToolChoiceUndeclared = fmt.Errorf("%w: Anthropic tool_choice: named function is not declared in tools", ErrInvalidRequest)
	errAnthropicToolChoiceNoTools    = fmt.Errorf(`%w: Anthropic tool_choice: "required" needs at least one declared tool`, ErrInvalidRequest)
)

// errAnthropicReservedExtra rejects a cross-protocol Extra field that would
// overwrite a payload key the adapter itself builds. The message is static and
// never echoes the offending key or value.
var errAnthropicReservedExtra = fmt.Errorf("%w: Anthropic extra payload fields cannot overwrite reserved keys", ErrInvalidRequest)

// anthropicToolChoice maps the normalized OpenAI tool_choice onto the object
// shape the Anthropic Messages API accepts. OpenAI's string selectors are not
// valid Anthropic payloads, so "auto" becomes {type:auto}, "required" becomes
// {type:any}, and a named function becomes {type:tool,name:...} only when it
// matches a declared tool name exactly. Native Anthropic objects are not part
// of the normalized ChatRequest contract (native passthrough goes through
// protocol.NativeRequest), so any other shape is rejected instead of being
// forwarded verbatim or silently dropped.
//
// With no declared tools a tool_choice object is invalid upstream: "auto" and
// "none" are dropped because they are equivalent to the default behavior when
// no tool exists, "required" and named functions cannot be satisfied and
// return an error. A JSON null means "unset" in OpenAI's wire format and is
// dropped as well.
func anthropicToolChoice(raw json.RawMessage, tools []protocol.Tool) (map[string]any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	switch trimmed[0] {
	case '"':
		var selector string
		if err := json.Unmarshal(trimmed, &selector); err != nil {
			return nil, errAnthropicToolChoiceShape
		}
		switch selector {
		case "auto":
			if len(tools) == 0 {
				return nil, nil
			}
			return map[string]any{"type": "auto"}, nil
		case "required":
			if len(tools) == 0 {
				return nil, errAnthropicToolChoiceNoTools
			}
			return map[string]any{"type": "any"}, nil
		case "none":
			if len(tools) == 0 {
				return nil, nil
			}
			return map[string]any{"type": "none"}, nil
		default:
			return nil, errAnthropicToolChoiceShape
		}
	case '{':
		var choice struct {
			Type     string `json:"type"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}
		if err := json.Unmarshal(trimmed, &choice); err != nil {
			return nil, errAnthropicToolChoiceShape
		}
		name := choice.Function.Name
		if choice.Type != "function" || strings.TrimSpace(name) == "" {
			return nil, errAnthropicToolChoiceShape
		}
		for _, tool := range tools {
			if tool.Function.Name == name {
				return map[string]any{"type": "tool", "name": name}, nil
			}
		}
		return nil, errAnthropicToolChoiceUndeclared
	default:
		return nil, errAnthropicToolChoiceShape
	}
}

// Sentinel errors returned for OpenAI tool results the Anthropic Messages API
// cannot represent. Each wraps ErrInvalidRequest so the gateway answers 400
// instead of blaming the upstream, and the texts are static so no tool call ID,
// tool name, or tool output is echoed into logs or API errors.
var (
	errAnthropicToolResultMissingID = fmt.Errorf("%w: Anthropic tool result: tool_call_id is missing", ErrInvalidRequest)
	errAnthropicToolResultUnknownID = fmt.Errorf("%w: Anthropic tool result: tool_call_id has no preceding assistant tool call", ErrInvalidRequest)
	errAnthropicToolResultContent   = fmt.Errorf("%w: Anthropic tool result: only text content is representable", ErrInvalidRequest)
)

// anthropicToolResultBlock renders one OpenAI tool message as one Anthropic
// tool_result block. The block keeps that message's own tool_call_id and text,
// so merging consecutive results into a single user turn never attaches one
// call's output to an unrelated ID and never turns it into plain user text.
// Only text and output_text parts are representable inside a tool_result, so
// an image or any other part is rejected instead of being silently dropped.
func anthropicToolResultBlock(message protocol.Message) (map[string]any, error) {
	var text strings.Builder
	for _, part := range message.Content {
		switch part.Type {
		case "text", "output_text":
			text.WriteString(part.Text)
		default:
			return nil, errAnthropicToolResultContent
		}
	}
	return map[string]any{"type": "tool_result", "tool_use_id": message.ToolCallID, "content": text.String()}, nil
}

func anthropicContent(message protocol.Message) any {
	if len(message.Content) == 1 && len(message.ToolCalls) == 0 && message.Content[0].Type == "text" {
		return message.Content[0].Text
	}
	parts := make([]map[string]any, 0, len(message.Content)+len(message.ToolCalls))
	for _, part := range message.Content {
		switch part.Type {
		case "text", "output_text":
			parts = append(parts, map[string]any{"type": "text", "text": part.Text})
		case "image_url", "input_image":
			imageData := part.ImageData
			mediaType := part.MediaType
			if imageData == "" && strings.HasPrefix(part.ImageURL, "data:") {
				prefix, encoded, found := strings.Cut(part.ImageURL, ",")
				if found {
					imageData = encoded
					mediaType = strings.TrimSuffix(strings.TrimPrefix(prefix, "data:"), ";base64")
				}
			}
			if imageData != "" {
				if mediaType == "" {
					mediaType = "image/png"
				}
				parts = append(parts, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": mediaType, "data": imageData}})
			}
		}
	}
	for _, toolCall := range message.ToolCalls {
		input := json.RawMessage(toolCall.Function.Arguments)
		if len(input) == 0 {
			input = json.RawMessage(`{}`)
		}
		parts = append(parts, map[string]any{"type": "tool_use", "id": toolCall.ID, "name": toolCall.Function.Name, "input": input})
	}
	return parts
}

// anthropicUsageDeltas turns Anthropic's cumulative stream usage into the
// per-event increments the gateway sums. message_start and every message_delta
// repeat cumulative input/output/cache counters, so only positive differences
// are emitted and total sums just the input and output differences.
type anthropicUsageDeltas struct {
	input         int64
	output        int64
	cacheRead     int64
	cacheCreation int64
}

// delta records one cumulative usage payload and returns the meters that grew
// since the previous one. An empty map means the payload reported nothing new.
func (t *anthropicUsageDeltas) delta(usage anthropicUsage) map[string]int64 {
	input := t.advance(&t.input, usage.InputTokens)
	output := t.advance(&t.output, usage.OutputTokens)
	cacheRead := t.advance(&t.cacheRead, usage.CacheReadInputTokens)
	cacheCreation := t.advance(&t.cacheCreation, usage.CacheCreationInputTokens)
	meters := map[string]int64{}
	if input > 0 {
		meters["input"] = input
	}
	if output > 0 {
		meters["output"] = output
	}
	if cacheRead > 0 {
		meters["cache_input"] = cacheRead
	}
	if cacheCreation > 0 {
		meters["cache_creation"] = cacheCreation
	}
	// Keep the established accounting convention: total is input + output.
	// Cache meters stay supplementary and never inflate or shrink it.
	if input > 0 || output > 0 {
		meters["total"] = input + output
	}
	return meters
}

// advance keeps the highest cumulative value seen for one meter, so a field
// that a later event omits or repeats is never counted twice, and returns the
// positive increment for the event being processed.
func (t *anthropicUsageDeltas) advance(baseline *int64, value int64) int64 {
	if value <= *baseline {
		return 0
	}
	increment := value - *baseline
	*baseline = value
	return increment
}

func anthropicFinishReason(reason string) string {
	switch reason {
	case "end_turn", "stop_sequence":
		return "stop"
	case "tool_use", "max_tokens":
		if reason == "tool_use" {
			return "tool_calls"
		}
		return "length"
	default:
		return reason
	}
}

func anthropicErrorMessage(value any) string {
	if object, ok := value.(map[string]any); ok {
		if message, ok := object["message"].(string); ok {
			return message
		}
	}
	if text, ok := value.(string); ok && text != "" {
		return text
	}
	return "upstream Anthropic error"
}
