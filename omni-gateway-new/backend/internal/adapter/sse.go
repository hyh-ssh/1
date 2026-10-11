package adapter

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/zjvtit/omni-gateway/internal/protocol"
)

const (
	// Scanner's default token limit is too small for tool-call arguments and
	// structured output deltas. Keep the normal buffer modest while allowing a
	// reasonably large individual SSE line.
	initialSSEBufferSize = 64 * 1024
	maxSSELineSize       = 16 * 1024 * 1024
)

// parseSSE reads an SSE response and translates both Chat Completions chunks
// and Responses events into the gateway's normalized stream events.
//
// The SSE framing is deliberately handled here instead of with a line-by-line
// strings.Split. An SSE data event may contain multiple data lines, may end at
// EOF without a trailing blank line, and may contain comments or fields that
// are not relevant to the gateway.
func (a *OpenAIAdapter) parseSSE(r io.Reader, emit func(protocol.StreamEvent) error) error {
	if r == nil {
		return errors.New("nil SSE response body")
	}
	if emit == nil {
		return errors.New("nil SSE event callback")
	}

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, initialSSEBufferSize), maxSSELineSize)

	var dataLines []string
	var eventName string
	frameBytes := 0
	done := false
	dispatch := func() error {
		frameBytes = 0
		if len(dataLines) == 0 {
			// An event without data has no payload to forward. Reset the event
			// name because a blank line terminates the SSE event.
			eventName = ""
			return nil
		}

		data := strings.Join(dataLines, "\n")
		name := eventName
		dataLines = nil
		eventName = ""

		events, err := normalizeSSEData(data, name)
		if err != nil {
			return err
		}
		for _, event := range events {
			if err := emit(event); err != nil {
				return err
			}
			if event.Type == "done" {
				done = true
				break
			}
		}
		return nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			if done {
				// Frames after a terminal event must not affect usage or output.
				return nil
			}
			continue
		}
		frameBytes += len(line) + 1
		if frameBytes > maxSSELineSize {
			return errors.New("upstream SSE frame exceeds size limit")
		}
		// A colon at the beginning of a line is an SSE comment/keepalive.
		if strings.HasPrefix(line, ":") {
			continue
		}

		field, value := line, ""
		if colon := strings.IndexByte(line, ':'); colon >= 0 {
			field, value = line[:colon], line[colon+1:]
			// The optional single space after the colon is not part of the
			// field value according to the SSE specification.
			if strings.HasPrefix(value, " ") {
				value = value[1:]
			}
		}

		switch field {
		case "data":
			dataLines = append(dataLines, value)
		case "event":
			eventName = value
			// id and retry are meaningful to a browser EventSource but do not
			// affect an upstream-to-client proxy, so intentionally ignore them.
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read SSE stream: %w", err)
	}
	return dispatch()
}

// normalizeSSEData decodes one complete SSE data event. A single Responses
// event can produce two normalized events (usage followed by done), and a
// Chat Completions event can contain more than one choice, hence the slice.
func normalizeSSEData(data, eventName string) ([]protocol.StreamEvent, error) {
	trimmed := strings.TrimSpace(data)
	if trimmed == "" {
		return nil, nil
	}
	if trimmed == "[DONE]" {
		return []protocol.StreamEvent{{Type: "done"}}, nil
	}

	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal([]byte(data), &rawMap); err != nil {
		return nil, fmt.Errorf("decode SSE data: %w", err)
	}
	raw := json.RawMessage(append([]byte(nil), []byte(data)...))

	if isChatChunk(rawMap) {
		return normalizeChatChunk(rawMap, raw)
	}
	return normalizeResponsesEvent(rawMap, raw, eventName)
}

func isChatChunk(raw map[string]json.RawMessage) bool {
	if object, ok := raw["object"]; ok {
		var objectName string
		if json.Unmarshal(object, &objectName) == nil && strings.HasPrefix(objectName, "chat.completion") {
			return true
		}
	}
	_, hasChoices := raw["choices"]
	return hasChoices
}

func normalizeChatChunk(raw map[string]json.RawMessage, rawData json.RawMessage) ([]protocol.StreamEvent, error) {
	var chunk struct {
		ID                string `json:"id"`
		Created           int64  `json:"created"`
		SystemFingerprint string `json:"system_fingerprint"`
		Choices           []struct {
			Index        int             `json:"index"`
			Delta        json.RawMessage `json:"delta"`
			FinishReason *string         `json:"finish_reason"`
			LogProbs     json.RawMessage `json:"logprobs"`
		} `json:"choices"`
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(rawData, &chunk); err != nil {
		return nil, fmt.Errorf("decode Chat Completions chunk: %w", err)
	}

	events := make([]protocol.StreamEvent, 0, len(chunk.Choices)+1)
	for _, choice := range chunk.Choices {
		delta := protocol.Message{}
		if hasJSONValue(choice.Delta) {
			var err error
			delta, err = parseOpenAIChatResponseMessage(choice.Delta)
			if err != nil {
				return nil, fmt.Errorf("decode Chat Completions delta: %w", err)
			}
		}
		finishReason := ""
		if choice.FinishReason != nil {
			finishReason = *choice.FinishReason
		}
		events = append(events, protocol.StreamEvent{
			Type:              "chunk",
			Index:             choice.Index,
			ResponseID:        chunk.ID,
			Created:           chunk.Created,
			SystemFingerprint: chunk.SystemFingerprint,
			LogProbs:          choice.LogProbs,
			Delta:             delta,
			FinishReason:      finishReason,
			Raw:               rawData,
		})
	}

	if hasJSONValue(chunk.Usage) {
		events = append(events, protocol.StreamEvent{
			Type:              "usage",
			ResponseID:        chunk.ID,
			Created:           chunk.Created,
			SystemFingerprint: chunk.SystemFingerprint,
			Usage:             parseOpenAIUsage(chunk.Usage),
			Raw:               rawData,
		})
	}
	return events, nil
}

func normalizeResponsesEvent(raw map[string]json.RawMessage, rawData json.RawMessage, eventName string) ([]protocol.StreamEvent, error) {
	eventType := eventName
	if rawType, ok := raw["type"]; ok {
		var parsedType string
		if json.Unmarshal(rawType, &parsedType) == nil && parsedType != "" {
			eventType = parsedType
		}
	}

	if eventType == "" {
		if _, ok := raw["error"]; ok {
			eventType = "error"
		}
	}
	responseID := responsesEventResponseID(raw)
	index := responsesEventIndex(raw)

	if strings.EqualFold(eventType, "error") || strings.HasSuffix(eventType, ".error") {
		return []protocol.StreamEvent{{
			Type:       "error",
			Event:      eventType,
			ResponseID: responseID,
			Index:      index,
			Error:      responseErrorMessage(raw),
			Raw:        rawData,
		}}, nil
	}

	// The Responses API reports usage on the final response object rather
	// than as a standalone usage event.
	if eventType == "response.completed" || eventType == "response.incomplete" {
		events := make([]protocol.StreamEvent, 0, 2)
		var usage json.RawMessage
		if response, ok := raw["response"]; ok {
			var responseMap map[string]json.RawMessage
			if err := json.Unmarshal(response, &responseMap); err == nil {
				usage = responseMap["usage"]
			}
		}
		if !hasJSONValue(usage) {
			usage = raw["usage"]
		}
		if hasJSONValue(usage) {
			events = append(events, protocol.StreamEvent{
				Type:       "usage",
				Event:      eventType,
				ResponseID: responseID,
				Usage:      parseOpenAIUsage(usage),
				Raw:        rawData,
			})
		}
		events = append(events, protocol.StreamEvent{Type: "done", Event: eventType, ResponseID: responseID, Raw: rawData})
		return events, nil
	}
	if eventType == "response.failed" {
		return []protocol.StreamEvent{{
			Type:       "error",
			Event:      eventType,
			ResponseID: responseID,
			Index:      index,
			Error:      responseErrorMessage(raw),
			Raw:        rawData,
		}}, nil
	}

	// Function-call arguments arrive as a top-level delta string too; only the
	// event type distinguishes them from output text. Normalizing them into
	// ToolCallDelta keeps the argument fragments ordered and attributable to a
	// specific output item.
	if isResponsesFunctionCallDelta(eventType) {
		delta, ok := raw["delta"]
		var arguments string
		// A provider that wraps the fragment in a non-string value is not an
		// error: it falls through to the generic delta handling below and is
		// still proxied verbatim through Raw.
		if ok && hasJSONValue(delta) && json.Unmarshal(delta, &arguments) == nil {
			return []protocol.StreamEvent{{
				Type:       "chunk",
				Event:      eventType,
				ResponseID: responseID,
				Index:      index,
				ToolCalls: []protocol.ToolCallDelta{{
					Index:     index,
					ID:        responsesToolCallID(raw),
					Name:      strings.TrimSpace(rawStringValue(raw["name"])),
					Arguments: arguments,
				}},
				Raw: rawData,
			}}, nil
		}
	}

	// A function-call output item announces the call id and function name
	// before any argument delta arrives. Only response.output_item.added is
	// mapped: response.output_item.done repeats the completed arguments, which
	// would double count the fragments already reported by the argument delta
	// events above.
	if eventType == "response.output_item.added" {
		if item, ok := raw["item"]; ok {
			if delta, ok := responsesToolCallDeltaFromItem(item, index); ok {
				return []protocol.StreamEvent{{
					Type:       "chunk",
					Event:      eventType,
					ResponseID: responseID,
					Index:      index,
					ToolCalls:  []protocol.ToolCallDelta{delta},
					Raw:        rawData,
				}}, nil
			}
		}
	}

	// Responses text and function-argument deltas use a top-level string
	// field named delta. Preserve every upstream event in Raw while exposing
	// text deltas in the common Message shape where possible.
	if delta, ok := raw["delta"]; ok && hasJSONValue(delta) {
		var text string
		if json.Unmarshal(delta, &text) == nil {
			return []protocol.StreamEvent{{
				Type:       "chunk",
				Event:      eventType,
				ResponseID: responseID,
				Index:      index,
				Delta: protocol.Message{
					Role:    "assistant",
					Content: []protocol.ContentPart{{Type: "text", Text: text}},
				},
				Raw: rawData,
			}}, nil
		}

		message, err := parseOpenAIMessage(delta)
		if err == nil {
			return []protocol.StreamEvent{{
				Type:       "chunk",
				Event:      eventType,
				ResponseID: responseID,
				Index:      index,
				Delta:      message,
				Raw:        rawData,
			}}, nil
		}
		// A delta the message shape cannot decode is not fatal: it falls through
		// to the raw chunk below and is still proxied through Raw.
	}

	// Some compatible Responses implementations send usage as a top-level
	// field. Handle it even when their event type is non-standard.
	if usage, ok := raw["usage"]; ok && hasJSONValue(usage) {
		return []protocol.StreamEvent{{
			Type:       "usage",
			Event:      eventType,
			ResponseID: responseID,
			Usage:      parseOpenAIUsage(usage),
			Raw:        rawData,
		}}, nil
	}

	// Unknown Responses event types remain observable through Raw. Treating
	// them as chunks lets callers proxy new event types without losing data.
	return []protocol.StreamEvent{{Type: "chunk", Event: eventType, ResponseID: responseID, Index: index, Raw: rawData}}, nil
}

// isResponsesFunctionCallDelta reports whether a Responses event streams a
// function-call argument fragment. Only the .delta event qualifies: the .done
// event repeats the complete arguments, and counting both would double count
// the same bytes.
func isResponsesFunctionCallDelta(eventType string) bool {
	return eventType == "response.function_call_arguments.delta"
}

// responsesEventResponseID reports the upstream response id carried by a
// Responses event, either nested under response or as a top-level field. Some
// compatible implementations omit it entirely, in which case it stays empty.
func responsesEventResponseID(raw map[string]json.RawMessage) string {
	if response, ok := raw["response"]; ok {
		var fields map[string]json.RawMessage
		if json.Unmarshal(response, &fields) == nil {
			if id := strings.TrimSpace(rawStringValue(fields["id"])); id != "" {
				return id
			}
		}
		if id := strings.TrimSpace(rawStringValue(response)); id != "" {
			return id
		}
	}
	return strings.TrimSpace(rawStringValue(raw["response_id"]))
}

// responsesEventIndex reports the output (or choice) index a Responses event
// refers to, defaulting to the first output when the provider omits it.
func responsesEventIndex(raw map[string]json.RawMessage) int {
	for _, key := range []string{"output_index", "index"} {
		if value, ok := raw[key]; ok {
			var index int
			if json.Unmarshal(value, &index) == nil {
				return index
			}
		}
	}
	return 0
}

// responsesToolCallID resolves the identifier that ties a function-call
// argument delta to its output item.
func responsesToolCallID(raw map[string]json.RawMessage) string {
	for _, key := range []string{"item_id", "call_id", "id"} {
		if id := strings.TrimSpace(rawStringValue(raw[key])); id != "" {
			return id
		}
	}
	return ""
}

// responsesToolCallDeltaFromItem extracts a function-call delta from a Responses
// output item such as {"type":"function_call","call_id":"...","name":"...",
// "arguments":"..."}. Non-function items report ok=false so they keep flowing
// through the generic chunk path.
func responsesToolCallDeltaFromItem(raw json.RawMessage, index int) (protocol.ToolCallDelta, bool) {
	if !hasJSONValue(raw) {
		return protocol.ToolCallDelta{}, false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return protocol.ToolCallDelta{}, false
	}
	if rawStringValue(fields["type"]) != "function_call" {
		return protocol.ToolCallDelta{}, false
	}
	// Only the identity is announced here. The arguments are counted when
	// they arrive as delta events, so copying the item payload would count
	// a provider that repeats them in the item twice.
	delta := protocol.ToolCallDelta{
		Index: index,
		ID:    strings.TrimSpace(rawStringValue(fields["call_id"])),
		Name:  strings.TrimSpace(rawStringValue(fields["name"])),
	}
	if delta.ID == "" {
		delta.ID = strings.TrimSpace(rawStringValue(fields["id"]))
	}
	if delta.ID == "" && delta.Name == "" {
		return protocol.ToolCallDelta{}, false
	}
	return delta, true
}

// rawStringValue decodes a JSON string. Any other JSON value, including an
// absent field, yields the empty string.
func rawStringValue(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}

func responseErrorMessage(raw map[string]json.RawMessage) string {
	if message, ok := raw["message"]; ok {
		var text string
		if json.Unmarshal(message, &text) == nil && text != "" {
			return text
		}
	}
	if errValue, ok := raw["error"]; ok {
		var text string
		if json.Unmarshal(errValue, &text) == nil && text != "" {
			return text
		}
		var errObject map[string]json.RawMessage
		if json.Unmarshal(errValue, &errObject) == nil {
			if nested, ok := errObject["message"]; ok && json.Unmarshal(nested, &text) == nil {
				return text
			}
		}
	}
	return "upstream Responses stream error"
}

func hasJSONValue(raw json.RawMessage) bool {
	return len(raw) > 0 && strings.TrimSpace(string(raw)) != "null"
}
