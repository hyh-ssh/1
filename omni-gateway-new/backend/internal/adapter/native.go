package adapter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/zjvtit/omni-gateway/internal/model"
	"github.com/zjvtit/omni-gateway/internal/protocol"
)

// NativeAdapter is optional so existing normalized adapters remain compatible.
type NativeAdapter interface {
	Native(context.Context, *protocol.NativeRequest) (*protocol.NativeResponse, error)
	NativeStream(context.Context, *protocol.NativeRequest, func(protocol.NativeEvent) error) error
}

func (a *OpenAIAdapter) Native(ctx context.Context, req *protocol.NativeRequest) (*protocol.NativeResponse, error) {
	if req == nil {
		return nil, errors.New("nil native request")
	}
	if req.API == protocol.MessagesAPI {
		return convertedMessages(ctx, a, req)
	}
	path, err := openAINativePath(req.API)
	if err != nil {
		return nil, err
	}
	return callNative(ctx, a.client, a.newRequest, path, req)
}

func (a *OpenAIAdapter) NativeStream(ctx context.Context, req *protocol.NativeRequest, emit func(protocol.NativeEvent) error) error {
	if req == nil || emit == nil {
		return errors.New("invalid native stream request")
	}
	if req.API == protocol.MessagesAPI {
		return convertedMessagesStream(ctx, a, req, emit)
	}
	if req.API != protocol.ImagesAPI {
		return ErrUnsupported
	}
	return streamNative(ctx, a.client, a.newRequest, "/images/generations", req, emit)
}

func (a *AnthropicAdapter) Native(ctx context.Context, req *protocol.NativeRequest) (*protocol.NativeResponse, error) {
	if req == nil {
		return nil, errors.New("nil native request")
	}
	if req.API != protocol.MessagesAPI {
		return nil, ErrUnsupported
	}
	return callNative(ctx, a.client, a.newRequest, "/messages", req)
}

func (a *AnthropicAdapter) NativeStream(ctx context.Context, req *protocol.NativeRequest, emit func(protocol.NativeEvent) error) error {
	if req == nil || emit == nil {
		return errors.New("invalid native stream request")
	}
	if req.API != protocol.MessagesAPI {
		return ErrUnsupported
	}
	return streamNative(ctx, a.client, a.newRequest, "/messages", req, emit)
}

func (a *GeminiAdapter) Native(ctx context.Context, req *protocol.NativeRequest) (*protocol.NativeResponse, error) {
	if req == nil {
		return nil, errors.New("nil native request")
	}
	if req.API != protocol.MessagesAPI {
		return nil, ErrUnsupported
	}
	return convertedMessages(ctx, a, req)
}

func (a *GeminiAdapter) NativeStream(ctx context.Context, req *protocol.NativeRequest, emit func(protocol.NativeEvent) error) error {
	if req == nil || emit == nil {
		return errors.New("invalid native stream request")
	}
	if req.API != protocol.MessagesAPI {
		return ErrUnsupported
	}
	return convertedMessagesStream(ctx, a, req, emit)
}

func openAINativePath(api protocol.NativeAPI) (string, error) {
	switch api {
	case protocol.EmbeddingsAPI:
		return "/embeddings", nil
	case protocol.ImagesAPI:
		return "/images/generations", nil
	default:
		return "", ErrUnsupported
	}
}

type nativeRequestFactory func(context.Context, string, string, io.Reader) (*http.Request, error)

func prepareNative(ctx context.Context, factory nativeRequestFactory, path string, req *protocol.NativeRequest) (*http.Request, error) {
	body, err := protocol.RewriteNativeModel(req.Raw, req.Model, true)
	if err != nil {
		return nil, err
	}
	return factory(ctx, http.MethodPost, path, bytes.NewReader(body))
}

func callNative(ctx context.Context, client *http.Client, factory nativeRequestFactory, path string, req *protocol.NativeRequest) (*protocol.NativeResponse, error) {
	httpReq, err := prepareNative(ctx, factory, path, req)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, classifyTransportError(err)
	}
	defer resp.Body.Close()
	body, err := readUpstreamBody(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read native response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, ClassifyHTTPError(resp.StatusCode, body, resp.Header)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil || object == nil {
		return nil, errors.New("upstream returned an invalid JSON object")
	}
	usage := parseNativeUsage(req.API, object["usage"])
	if req.API == protocol.MessagesAPI {
		usage.Meters["total"] = usage.Get("input") + usage.Get("output") + usage.Get("cache_input") + usage.Get("cache_creation")
	}
	if req.API == protocol.ImagesAPI {
		var images []json.RawMessage
		if json.Unmarshal(object["data"], &images) == nil {
			usage.Meters["image"] = int64(len(images))
		}
	}
	return &protocol.NativeResponse{Raw: body, Usage: usage}, nil
}

func streamNative(ctx context.Context, client *http.Client, factory nativeRequestFactory, path string, req *protocol.NativeRequest, emit func(protocol.NativeEvent) error) error {
	httpReq, err := prepareNative(ctx, factory, path, req)
	if err != nil {
		return err
	}
	httpReq.Header.Set("Accept", "text/event-stream")
	resp, err := client.Do(httpReq)
	if err != nil {
		return classifyTransportError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, err := readUpstreamBody(resp.Body)
		if err != nil {
			return fmt.Errorf("read upstream stream error: %w", err)
		}
		return ClassifyHTTPError(resp.StatusCode, body, resp.Header)
	}
	if !strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		return errors.New("upstream did not return an SSE stream")
	}
	return parseNativeSSE(resp.Body, req.API, emit)
}

func parseNativeUsage(api protocol.NativeAPI, raw json.RawMessage) model.Usage {
	usage := parseOpenAIUsage(raw)
	if api != protocol.MessagesAPI {
		return usage
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return usage
	}
	if count, ok := usageInt(fields, "cache_read_input_tokens"); ok {
		usage.Meters["cache_input"] = count
	}
	if count, ok := usageInt(fields, "cache_creation_input_tokens"); ok {
		usage.Meters["cache_creation"] = count
	}
	return usage
}

// Native counters are cumulative; merge only fields present in each event.
func nativeUsageDelta(previous model.Usage, current model.Usage) model.Usage {
	delta := model.NewUsage()
	for meter, value := range current.Meters {
		if value > previous.Get(meter) {
			delta.Meters[meter] = value - previous.Get(meter)
			previous.Meters[meter] = value
		}
	}
	return delta
}

func parseNativeSSE(reader io.Reader, api protocol.NativeAPI, emit func(protocol.NativeEvent) error) error {
	if reader == nil || emit == nil {
		return errors.New("invalid native SSE reader or callback")
	}
	const maxFrameBytes = 32 << 20
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), maxFrameBytes)
	var lines []string
	frameBytes := 0
	previous := model.NewUsage()
	done := false
	dispatch := func() error {
		if len(lines) == 0 {
			return nil
		}
		event := protocol.NativeEvent{Wire: []byte(strings.Join(lines, "\n") + "\n\n")}
		var data []string
		name := ""
		for _, line := range lines {
			field, value, _ := strings.Cut(line, ":")
			value = strings.TrimPrefix(value, " ")
			if field == "data" {
				data = append(data, value)
			}
			if field == "event" {
				name = value
			}
		}
		lines, frameBytes = nil, 0
		if name == "error" && len(data) == 0 {
			return &Error{StatusCode: http.StatusBadGateway, Type: "server", Message: "upstream stream failed"}
		}
		// Comments, keepalives and other events without data are not business
		// events. Emitting them would commit HTTP 200 before a first-frame error.
		if len(data) == 0 {
			return nil
		}
		if len(data) > 0 {
			raw := []byte(strings.Join(data, "\n"))
			if strings.TrimSpace(string(raw)) == "[DONE]" {
				// Native Messages and Images require their own terminal event.
				// A bare sentinel is not a completed image (nor message), and
				// forwarding it before completion would commit HTTP 200 early.
				return nil
			} else {
				var object map[string]json.RawMessage
				if json.Unmarshal(raw, &object) != nil || object == nil {
					return errors.New("upstream sent invalid SSE JSON")
				}
				var eventType string
				_ = json.Unmarshal(object["type"], &eventType)
				if eventType == "" {
					eventType = name
				}
				if (api == protocol.ImagesAPI && eventType == "message_stop") || (api == protocol.MessagesAPI && eventType == "image_generation.completed") {
					return errors.New("upstream sent an unexpected SSE terminal event")
				}
				usageRaw := object["usage"]
				if eventType == "message_start" {
					var message map[string]json.RawMessage
					_ = json.Unmarshal(object["message"], &message)
					usageRaw = message["usage"]
				}
				current := parseNativeUsage(api, usageRaw)
				if api == protocol.ImagesAPI && eventType == "image_generation.completed" {
					current.Meters["image"] = 1
				}
				event.Usage = nativeUsageDelta(previous, current)
				if api == protocol.MessagesAPI && len(current.Meters) > 0 {
					total := previous.Get("input") + previous.Get("output") + previous.Get("cache_input") + previous.Get("cache_creation")
					if total > previous.Get("total") {
						event.Usage.Meters["total"] = total - previous.Get("total")
						previous.Meters["total"] = total
					}
				}
				event.Done = (api == protocol.MessagesAPI && eventType == "message_stop") || (api == protocol.ImagesAPI && eventType == "image_generation.completed")
				event.Content = (api == protocol.MessagesAPI && eventType == "content_block_delta") || (api == protocol.ImagesAPI && eventType == "image_generation.partial_image")
				event.Failed = eventType == "error" || object["error"] != nil
			}
		}
		if err := emit(event); err != nil {
			return err
		}
		if event.Failed {
			return &Error{StatusCode: http.StatusBadGateway, Type: "server", Message: "upstream stream failed"}
		}
		done = done || event.Done
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			if done && (api == protocol.MessagesAPI || api == protocol.ImagesAPI) {
				return nil
			}
			continue
		}
		frameBytes += len(line) + 1
		if frameBytes > maxFrameBytes {
			return errors.New("upstream SSE frame exceeds size limit")
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read native SSE: %w", err)
	}
	if err := dispatch(); err != nil {
		return err
	}
	if !done {
		return &Error{StatusCode: http.StatusBadGateway, Type: "server", Message: "upstream stream ended before completion"}
	}
	return nil
}

// Conversion failures are skipped when an Anthropic route is available. If no
// route can preserve the request, the gateway reports ErrInvalidRequest (400).
func messagesConversionError(reason string) error {
	return fmt.Errorf("%w: %w: %s", ErrUnsupported, ErrInvalidRequest, reason)
}

func messagesObject(raw json.RawMessage, allowed ...string) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, messagesConversionError("expected a JSON object")
	}
	for key := range fields {
		found := false
		for _, field := range allowed {
			if key == field {
				found = true
				break
			}
		}
		if !found {
			return nil, messagesConversionError("unsupported Messages field")
		}
	}
	return fields, nil
}

func messagesString(raw json.RawMessage) (string, error) {
	var value string
	if len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &value) != nil {
		return "", messagesConversionError("expected a string")
	}
	return value, nil
}

func messagesJSONObject(raw json.RawMessage) error {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return messagesConversionError("tool input and schema must be JSON objects")
	}
	return nil
}

func messagesToChat(req *protocol.NativeRequest) (*protocol.ChatRequest, error) {
	if req == nil {
		return nil, messagesConversionError("missing Messages request")
	}
	fields, err := messagesObject(req.Raw, "model", "messages", "system", "max_tokens", "stream", "temperature", "top_p", "stop_sequences", "tools", "tool_choice")
	if err != nil {
		return nil, err
	}
	var limit int
	if json.Unmarshal(fields["max_tokens"], &limit) != nil || limit <= 0 {
		return nil, messagesConversionError("positive max_tokens is required")
	}
	chat := &protocol.ChatRequest{Model: req.Model, Stream: req.Stream, MaxTokens: &limit}
	if raw, ok := fields["temperature"]; ok {
		var value float64
		if string(raw) == "null" || json.Unmarshal(raw, &value) != nil || value < 0 || value > 1 {
			return nil, messagesConversionError("invalid temperature")
		}
		chat.Temperature = &value
	}
	if raw, ok := fields["top_p"]; ok {
		var value float64
		if string(raw) == "null" || json.Unmarshal(raw, &value) != nil || value < 0 || value > 1 {
			return nil, messagesConversionError("invalid top_p")
		}
		chat.TopP = &value
	}
	if raw, ok := fields["stop_sequences"]; ok {
		if json.Unmarshal(raw, &chat.Stop) != nil || chat.Stop == nil {
			return nil, messagesConversionError("invalid stop_sequences")
		}
	}
	if raw, ok := fields["tools"]; ok {
		var tools []json.RawMessage
		if json.Unmarshal(raw, &tools) != nil || tools == nil {
			return nil, messagesConversionError("invalid tools")
		}
		for _, rawTool := range tools {
			tool, err := messagesObject(rawTool, "name", "description", "input_schema")
			if err != nil {
				return nil, err
			}
			name, err := messagesString(tool["name"])
			if err != nil || name == "" || messagesJSONObject(tool["input_schema"]) != nil {
				return nil, messagesConversionError("invalid function tool")
			}
			function := protocol.ToolFunction{Name: name, Parameters: tool["input_schema"]}
			if raw, ok := tool["description"]; ok {
				function.Description, err = messagesString(raw)
				if err != nil {
					return nil, err
				}
			}
			chat.Tools = append(chat.Tools, protocol.Tool{Type: "function", Function: function})
		}
	}
	if raw, ok := fields["tool_choice"]; ok {
		choice, err := messagesObject(raw, "type", "name")
		if err != nil {
			return nil, err
		}
		kind, err := messagesString(choice["type"])
		if err != nil || len(chat.Tools) == 0 {
			return nil, messagesConversionError("tool_choice requires declared tools")
		}
		switch kind {
		case "auto":
			if len(choice) != 1 {
				return nil, messagesConversionError("invalid tool_choice")
			}
			chat.ToolChoice = json.RawMessage(`"auto"`)
		case "any":
			if len(choice) != 1 {
				return nil, messagesConversionError("invalid tool_choice")
			}
			chat.ToolChoice = json.RawMessage(`"required"`)
		case "tool":
			name, err := messagesString(choice["name"])
			if err != nil || name == "" {
				return nil, messagesConversionError("invalid named tool_choice")
			}
			declared := false
			for _, tool := range chat.Tools {
				declared = declared || tool.Function.Name == name
			}
			if !declared {
				return nil, messagesConversionError("undeclared named tool_choice")
			}
			chat.ToolChoice, _ = json.Marshal(map[string]any{"type": "function", "function": map[string]string{"name": name}})
		default:
			return nil, messagesConversionError("unsupported tool_choice")
		}
	}
	if raw, ok := fields["system"]; ok {
		parts, err := messagesText(raw)
		if err != nil {
			return nil, err
		}
		chat.Messages = append(chat.Messages, protocol.Message{Role: "system", Content: parts})
	}
	var wireMessages []json.RawMessage
	if json.Unmarshal(fields["messages"], &wireMessages) != nil || len(wireMessages) == 0 {
		return nil, messagesConversionError("messages are required")
	}
	// A tool_result is only meaningful for an unconsumed tool_use in the
	// immediately preceding assistant turn, not for any older call ID.
	pending := map[string]bool{}
	for _, raw := range wireMessages {
		message, err := messagesObject(raw, "role", "content")
		if err != nil {
			return nil, err
		}
		role, err := messagesString(message["role"])
		if err != nil || (role != "user" && role != "assistant") {
			return nil, messagesConversionError("Messages roles must be user or assistant")
		}
		if role == "assistant" && len(pending) > 0 {
			return nil, messagesConversionError("unconsumed tool_use before assistant turn")
		}
		var blocks []json.RawMessage
		if json.Unmarshal(message["content"], &blocks) != nil || blocks == nil {
			parts, err := messagesText(message["content"])
			if err != nil {
				return nil, err
			}
			chat.Messages = append(chat.Messages, protocol.Message{Role: role, Content: parts})
		} else {
			if len(blocks) == 0 {
				return nil, messagesConversionError("empty Messages content")
			}
			item := protocol.Message{Role: role}
			seenCall, seenTextAfterResult := false, false
			for _, rawBlock := range blocks {
				block, err := messagesObject(rawBlock, "type", "text", "id", "name", "input", "tool_use_id", "content", "is_error")
				if err != nil {
					return nil, err
				}
				kind, err := messagesString(block["type"])
				if err != nil {
					return nil, err
				}
				switch kind {
				case "text":
					if len(block) != 2 || seenCall {
						return nil, messagesConversionError("interleaved or unsupported text block")
					}
					text, err := messagesString(block["text"])
					if err != nil {
						return nil, err
					}
					if role == "user" && len(item.Content) == 0 && len(chat.Messages) > 0 && chat.Messages[len(chat.Messages)-1].Role == "tool" {
						seenTextAfterResult = true
					}
					item.Content = append(item.Content, protocol.ContentPart{Type: "text", Text: text})
				case "tool_use":
					if role != "assistant" || len(block) != 4 || messagesJSONObject(block["input"]) != nil {
						return nil, messagesConversionError("invalid tool_use")
					}
					id, idErr := messagesString(block["id"])
					name, nameErr := messagesString(block["name"])
					if idErr != nil || nameErr != nil || id == "" || name == "" || pending[id] {
						return nil, messagesConversionError("invalid tool_use identity")
					}
					seenCall, pending[id] = true, true
					item.ToolCalls = append(item.ToolCalls, protocol.ToolCall{ID: id, Type: "function", Function: protocol.ToolCallFunction{Name: name, Arguments: string(block["input"])}})
				case "tool_result":
					if role != "user" || len(block) != 3 || seenTextAfterResult || len(item.Content) > 0 {
						return nil, messagesConversionError("unsupported tool_result")
					}
					id, err := messagesString(block["tool_use_id"])
					if err != nil || !pending[id] {
						return nil, messagesConversionError("orphan or duplicate tool_result")
					}
					parts, err := messagesText(block["content"])
					if err != nil {
						return nil, err
					}
					delete(pending, id)
					var text strings.Builder
					for _, part := range parts {
						text.WriteString(part.Text)
					}
					chat.Messages = append(chat.Messages, protocol.Message{Role: "tool", ToolCallID: id, Content: []protocol.ContentPart{{Type: "text", Text: text.String()}}})
				default:
					return nil, messagesConversionError("unsupported Messages content block")
				}
			}
			if len(item.Content) > 0 || len(item.ToolCalls) > 0 {
				chat.Messages = append(chat.Messages, item)
			} else if len(chat.Messages) == 0 || chat.Messages[len(chat.Messages)-1].Role != "tool" {
				return nil, messagesConversionError("empty Messages content")
			}
		}
		if role == "user" && len(pending) > 0 && chat.Messages[len(chat.Messages)-1].Role != "tool" {
			return nil, messagesConversionError("unconsumed tool_use before user content")
		}
	}
	if len(chat.Messages) == 0 || chat.Messages[len(chat.Messages)-1].Role == "assistant" {
		return nil, messagesConversionError("assistant prefill requires an Anthropic route")
	}
	return chat, nil
}

func messagesText(raw json.RawMessage) ([]protocol.ContentPart, error) {
	if text, err := messagesString(raw); err == nil {
		return []protocol.ContentPart{{Type: "text", Text: text}}, nil
	}
	var blocks []json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil || blocks == nil || len(blocks) == 0 {
		return nil, messagesConversionError("only text Messages content can be converted")
	}
	parts := make([]protocol.ContentPart, 0, len(blocks))
	for _, rawBlock := range blocks {
		block, err := messagesObject(rawBlock, "type", "text")
		if err != nil {
			return nil, err
		}
		kind, err := messagesString(block["type"])
		if err != nil || kind != "text" || len(block) != 2 {
			return nil, messagesConversionError("only text Messages content can be converted")
		}
		text, err := messagesString(block["text"])
		if err != nil {
			return nil, err
		}
		parts = append(parts, protocol.ContentPart{Type: "text", Text: text})
	}
	return parts, nil
}

func messagesToolBlock(call protocol.ToolCall) (map[string]any, error) {
	if call.Type != "function" || call.ID == "" || call.Function.Name == "" {
		return nil, errors.New("upstream returned an invalid function tool call")
	}
	input := json.RawMessage(call.Function.Arguments)
	var object map[string]json.RawMessage
	if json.Unmarshal(input, &object) != nil || object == nil {
		return nil, errors.New("upstream tool input is not a JSON object")
	}
	return map[string]any{"type": "tool_use", "id": call.ID, "name": call.Function.Name, "input": input}, nil
}

func convertedMessages(ctx context.Context, upstream Adapter, req *protocol.NativeRequest) (*protocol.NativeResponse, error) {
	chat, err := messagesToChat(req)
	if err != nil {
		return nil, err
	}
	response, err := upstream.Chat(ctx, chat)
	if err != nil {
		return nil, err
	}
	if response == nil || len(response.Choices) != 1 || response.Choices[0].Index != 0 {
		return nil, errors.New("upstream returned an invalid converted Messages response")
	}
	choice := response.Choices[0]
	if choice.Message.Role != "assistant" || choice.Message.ToolCallID != "" || choice.Message.Name != "" || choice.FinishReason == "" {
		return nil, errors.New("upstream assistant message cannot be converted to Messages")
	}
	if choice.Message.ReasoningContent != "" || len(choice.LogProbs) > 0 {
		return nil, errors.New("upstream thinking or logprobs cannot be converted to Messages")
	}
	if err := messagesResponseRaw(response.Raw); err != nil {
		return nil, err
	}
	content := make([]map[string]any, 0, len(choice.Message.Content)+len(choice.Message.ToolCalls))
	for _, part := range choice.Message.Content {
		if part.Type != "text" && part.Type != "output_text" {
			return nil, errors.New("unexpected non-text Messages response")
		}
		content = append(content, map[string]any{"type": "text", "text": part.Text})
	}
	for _, call := range choice.Message.ToolCalls {
		block, err := messagesToolBlock(call)
		if err != nil {
			return nil, err
		}
		content = append(content, block)
	}
	stop, err := messagesStopReason(choice.FinishReason, len(choice.Message.ToolCalls) > 0)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]any{"id": response.ID, "type": "message", "role": "assistant", "model": req.Model, "content": content, "stop_reason": stop, "stop_sequence": nil, "usage": messagesUsage(response.Usage)})
	if err != nil {
		return nil, err
	}
	return &protocol.NativeResponse{Raw: body, Usage: response.Usage}, nil
}

// Chat normalization discards unrecognized fields. Reject them rather than
// silently losing refusal, reasoning or provider-specific signature data.
func messagesChatRaw(raw json.RawMessage) error {
	fields, err := messagesObject(raw, "role", "content", "tool_calls")
	if err != nil {
		return errors.New("unsupported upstream Chat message field")
	}
	if value, ok := fields["content"]; ok && string(value) != "null" {
		var text string
		if json.Unmarshal(value, &text) != nil {
			var parts []json.RawMessage
			if json.Unmarshal(value, &parts) != nil {
				return errors.New("unsupported upstream Chat content")
			}
			for _, part := range parts {
				fields, err := messagesObject(part, "type", "text")
				if err != nil || string(fields["type"]) != `"text"` {
					return errors.New("unsupported upstream Chat content part")
				}
				if _, err := messagesString(fields["text"]); err != nil {
					return errors.New("unsupported upstream Chat text")
				}
			}
		}
	}
	if calls, ok := fields["tool_calls"]; ok && string(calls) != "null" {
		var list []json.RawMessage
		if json.Unmarshal(calls, &list) != nil {
			return errors.New("invalid upstream Chat tool calls")
		}
		for _, call := range list {
			fields, err := messagesObject(call, "index", "id", "type", "function")
			if err != nil {
				return errors.New("unsupported upstream Chat tool call")
			}
			if function, ok := fields["function"]; ok {
				if _, err := messagesObject(function, "name", "arguments"); err != nil {
					return errors.New("unsupported upstream Chat function")
				}
			}
		}
	}
	return nil
}

// Check provider content, not provider metadata, which does not appear in
// Anthropic content blocks. In particular thought/signature parts must fail.
func messagesGeminiRaw(candidates json.RawMessage, stream bool) error {
	var list []struct {
		Content struct {
			Parts []json.RawMessage `json:"parts"`
		} `json:"content"`
	}
	if json.Unmarshal(candidates, &list) != nil || (!stream && len(list) != 1) || len(list) > 1 {
		return errors.New("invalid upstream Gemini candidates")
	}
	for _, candidate := range list {
		seenCall := false
		for _, rawPart := range candidate.Content.Parts {
			part, err := messagesObject(rawPart, "text", "functionCall", "thought")
			if err != nil || len(part) == 0 {
				return errors.New("unsupported upstream Gemini part")
			}
			if thought, ok := part["thought"]; ok && string(thought) != "false" {
				return errors.New("Gemini thinking cannot be converted to Messages")
			}
			if text, ok := part["text"]; ok {
				if seenCall {
					return errors.New("upstream Gemini text after function call cannot be converted")
				}
				if _, err := messagesString(text); err != nil || part["functionCall"] != nil {
					return errors.New("unsupported upstream Gemini text")
				}
			} else if call, ok := part["functionCall"]; ok {
				seenCall = true
				fields, err := messagesObject(call, "id", "name", "args")
				if err != nil || messagesJSONObject(fields["args"]) != nil {
					return errors.New("unsupported upstream Gemini function call")
				}
				if _, err := messagesString(fields["name"]); err != nil {
					return errors.New("invalid upstream Gemini function name")
				}
			} else {
				return errors.New("unsupported upstream Gemini part")
			}
		}
	}
	return nil
}

func messagesResponseRaw(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(raw, &root) != nil || root == nil {
		return errors.New("invalid upstream Messages conversion response")
	}
	if choices, ok := root["choices"]; ok {
		var list []map[string]json.RawMessage
		if json.Unmarshal(choices, &list) != nil || len(list) != 1 {
			return errors.New("invalid upstream Chat choices")
		}
		if err := messagesChatRaw(list[0]["message"]); err != nil {
			return err
		}
	}
	if candidates, ok := root["candidates"]; ok {
		return messagesGeminiRaw(candidates, false)
	}
	return nil
}

func messagesStopReason(reason string, hasCalls bool) (string, error) {
	switch reason {
	case "tool_calls", "tool_use":
		if hasCalls {
			return "tool_use", nil
		}
	case "", "stop", "end_turn":
		if hasCalls {
			return "tool_use", nil
		}
		return "end_turn", nil
	case "length", "max_tokens":
		return "max_tokens", nil
	}
	return "", errors.New("upstream finish reason cannot be converted to Messages")
}

func messagesUsage(usage model.Usage) map[string]int64 {
	return map[string]int64{"input_tokens": usage.Get("input"), "output_tokens": usage.Get("output")}
}

func convertedMessagesStream(ctx context.Context, upstream Adapter, req *protocol.NativeRequest, emit func(protocol.NativeEvent) error) error {
	chat, err := messagesToChat(req)
	if err != nil {
		return err
	}
	started, done, finishSeen := false, false, false
	usage := model.NewUsage()
	stop := "end_turn"
	blockIndex, blockKind := -1, ""
	type toolStream struct {
		id, name, arguments string
		started             bool
	}
	tools := make(map[int]*toolStream)
	activeTool := -1
	write := func(kind string, payload map[string]any, increment model.Usage) error {
		payload["type"] = kind
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		return emit(protocol.NativeEvent{Wire: []byte("event: " + kind + "\ndata: " + string(raw) + "\n\n"), Usage: increment, Content: kind == "content_block_delta", Done: kind == "message_stop"})
	}
	start := func() error {
		if started {
			return nil
		}
		started = true
		return write("message_start", map[string]any{"message": map[string]any{"id": "msg_gateway", "type": "message", "role": "assistant", "model": req.Model, "content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": messagesUsage(model.NewUsage())}}, model.NewUsage())
	}
	closeBlock := func() error {
		if blockKind == "" {
			return nil
		}
		if err := write("content_block_stop", map[string]any{"index": blockIndex}, model.NewUsage()); err != nil {
			return err
		}
		blockKind = ""
		return nil
	}
	finish := func() error {
		for index := 0; index < len(tools); index++ {
			state := tools[index]
			if !state.started || messagesJSONObject(json.RawMessage(state.arguments)) != nil {
				return errors.New("incomplete upstream tool call")
			}
		}
		if err := closeBlock(); err != nil {
			return err
		}
		if len(tools) > 0 && stop == "end_turn" {
			stop = "tool_use"
		}
		if err := write("message_delta", map[string]any{"delta": map[string]any{"stop_reason": stop, "stop_sequence": nil}, "usage": messagesUsage(usage)}, model.NewUsage()); err != nil {
			return err
		}
		done = true
		return write("message_stop", map[string]any{}, model.NewUsage())
	}
	err = upstream.ChatStream(ctx, chat, func(event protocol.StreamEvent) error {
		if done {
			return nil
		}
		if event.Type == "error" {
			return &Error{StatusCode: 502, Type: "server", Message: "upstream stream failed"}
		}
		if event.Type != "chunk" && event.Type != "usage" && event.Type != "done" {
			return errors.New("unsupported upstream Chat stream event")
		}
		if event.Index != 0 || len(event.ToolCalls) > 0 || event.Delta.ReasoningContent != "" || len(event.LogProbs) > 0 || event.Delta.Role != "" && event.Delta.Role != "assistant" {
			return errors.New("unsupported upstream Chat stream delta")
		}
		if err := messagesStreamRaw(event.Raw); err != nil {
			return err
		}
		if err := start(); err != nil {
			return err
		}
		if event.Type == "usage" {
			usage = usage.Add(event.Usage)
			return write("message_delta", map[string]any{"delta": map[string]any{}, "usage": messagesUsage(usage)}, event.Usage)
		}
		if event.FinishReason != "" {
			finishSeen = true
			var err error
			stop, err = messagesStopReason(event.FinishReason, len(tools) > 0)
			if err != nil {
				return err
			}
		}
		for _, part := range event.Delta.Content {
			if part.Type != "text" && part.Type != "output_text" {
				return errors.New("unexpected non-text Messages stream")
			}
			if blockKind != "text" {
				if activeTool >= 0 {
					return errors.New("upstream interleaved tool and text deltas")
				}
				if err := closeBlock(); err != nil {
					return err
				}
				blockIndex++
				blockKind = "text"
				if err := write("content_block_start", map[string]any{"index": blockIndex, "content_block": map[string]string{"type": "text", "text": ""}}, model.NewUsage()); err != nil {
					return err
				}
			}
			if err := write("content_block_delta", map[string]any{"index": blockIndex, "delta": map[string]string{"type": "text_delta", "text": part.Text}}, model.NewUsage()); err != nil {
				return err
			}
		}
		for _, call := range event.Delta.ToolCalls {
			if call.Index == nil || *call.Index < 0 || call.Type != "" && call.Type != "function" {
				return errors.New("invalid upstream tool call delta")
			}
			index := *call.Index
			state := tools[index]
			if state == nil {
				if index != len(tools) {
					return errors.New("non-sequential upstream tool calls")
				}
				state = &toolStream{}
				tools[index] = state
			}
			if call.ID != "" {
				if state.id != "" && state.id != call.ID {
					return errors.New("changed upstream tool call ID")
				}
				state.id = call.ID
			}
			if call.Function.Name != "" {
				if state.name != "" && state.name != call.Function.Name {
					return errors.New("changed upstream tool name")
				}
				state.name = call.Function.Name
			}
			state.arguments += call.Function.Arguments
			if !state.started {
				if state.id == "" || state.name == "" {
					continue
				}
				if activeTool >= 0 && activeTool != index {
					if messagesJSONObject(json.RawMessage(tools[activeTool].arguments)) != nil {
						return errors.New("interleaved upstream tool calls")
					}
					if err := closeBlock(); err != nil {
						return err
					}
					activeTool = -1
				}
				if err := closeBlock(); err != nil {
					return err
				}
				blockIndex++
				blockKind, activeTool, state.started = "tool_use", index, true
				if err := write("content_block_start", map[string]any{"index": blockIndex, "content_block": map[string]any{"type": "tool_use", "id": state.id, "name": state.name, "input": map[string]any{}}}, model.NewUsage()); err != nil {
					return err
				}
				if state.arguments != "" {
					if err := write("content_block_delta", map[string]any{"index": blockIndex, "delta": map[string]string{"type": "input_json_delta", "partial_json": state.arguments}}, model.NewUsage()); err != nil {
						return err
					}
				}
			} else if call.Function.Arguments != "" {
				if activeTool != index {
					return errors.New("interleaved upstream tool arguments")
				}
				if err := write("content_block_delta", map[string]any{"index": blockIndex, "delta": map[string]string{"type": "input_json_delta", "partial_json": call.Function.Arguments}}, model.NewUsage()); err != nil {
					return err
				}
			}
		}
		if event.Type == "done" {
			return finish()
		}
		return nil
	})
	if err == nil && !done {
		// Gemini streamGenerateContent usually ends at EOF after its final
		// finishReason rather than sending the Chat [DONE] sentinel.
		if finishSeen {
			return finish()
		}
		return &Error{StatusCode: 502, Type: "server", Message: "upstream stream ended before completion"}
	}
	return err
}

func messagesStreamRaw(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return errors.New("invalid upstream Chat stream frame")
	}
	if choices, ok := fields["choices"]; ok {
		var list []map[string]json.RawMessage
		if json.Unmarshal(choices, &list) != nil {
			return errors.New("invalid upstream Chat stream choices")
		}
		for _, choice := range list {
			if delta, ok := choice["delta"]; ok && string(delta) != "null" {
				if err := messagesChatRaw(delta); err != nil {
					return err
				}
			}
		}
	}
	if candidates, ok := fields["candidates"]; ok {
		return messagesGeminiRaw(candidates, true)
	}
	return nil
}
