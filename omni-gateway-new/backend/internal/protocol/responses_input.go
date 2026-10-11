package protocol

import (
	"encoding/json"
	"fmt"
	"strings"
)

// NormalizeResponsesInput converts the Responses `input` field into normalized
// messages so the Responses path can be inspected and orchestrated the way the
// Chat path already is.
//
// The Responses API accepts either a bare string (shorthand for a single user
// message) or an array of input items. Items that are not chat-like
// (input_image, reasoning, or a future item type) are not dropped: they remain
// available verbatim through ResponsesRequest.Input and are simply not
// represented in the normalized message list.
//
// A structurally invalid value (a number, an object, or an array element that is
// neither a string nor an object) is rejected so the gateway answers 400 instead
// of forwarding a body the upstream will reject anyway. Blank and null array
// elements carry nothing to normalize and are ignored rather than rejected.
func NormalizeResponsesInput(raw json.RawMessage) ([]Message, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if strings.TrimSpace(text) == "" {
			return nil, fmt.Errorf("input must not be empty")
		}
		return []Message{{Role: "user", Content: []ContentPart{{Type: "text", Text: text}}}}, nil
	}

	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("input must be a string or an array of input items")
	}

	messages := make([]Message, 0, len(items))
	for index, item := range items {
		message, ok, err := normalizeResponsesInputItem(item)
		if err != nil {
			return nil, fmt.Errorf("input item %d: %w", index, err)
		}
		if ok {
			messages = append(messages, message)
		}
	}
	return messages, nil
}

// normalizeResponsesInputItem converts one Responses input item. ok is false for
// items the message shape cannot represent; those stay in Input untouched.
func normalizeResponsesInputItem(raw json.RawMessage) (Message, bool, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return Message{}, false, nil
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if strings.TrimSpace(text) == "" {
			return Message{}, false, nil
		}
		return Message{Role: "user", Content: []ContentPart{{Type: "text", Text: text}}}, true, nil
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return Message{}, false, fmt.Errorf("must be a string or an object")
	}

	switch itemType := strings.TrimSpace(rawStringValue(fields["type"])); itemType {
	case "", "message":
		return normalizeResponsesMessageItem(fields, itemType)
	case "function_call":
		return normalizeResponsesFunctionCallItem(fields), true, nil
	case "function_call_output":
		return normalizeResponsesFunctionCallOutputItem(fields), true, nil
	default:
		// input_image, reasoning and future item types are not chat messages.
		return Message{}, false, nil
	}
}

// normalizeResponsesMessageItem maps a Responses message item onto a Message. An
// object without an explicit type is only treated as a message when it actually
// carries content, so an unrecognized item is never turned into an empty
// message.
func normalizeResponsesMessageItem(fields map[string]json.RawMessage, itemType string) (Message, bool, error) {
	role := strings.TrimSpace(rawStringValue(fields["role"]))
	if role == "" {
		role = "user"
	}
	parts, err := parseResponsesContent(fields["content"])
	if err != nil {
		return Message{}, false, err
	}
	if len(parts) == 0 && itemType == "" {
		return Message{}, false, nil
	}
	return Message{Role: role, Content: parts}, true, nil
}

// normalizeResponsesFunctionCallItem maps {"type":"function_call",...} onto an
// assistant message carrying one tool call, which is how the Chat path
// represents the same exchange.
func normalizeResponsesFunctionCallItem(fields map[string]json.RawMessage) Message {
	callID := responsesItemCallID(fields)
	return Message{
		Role: "assistant",
		ToolCalls: []ToolCall{{
			ID:   callID,
			Type: "function",
			Function: ToolCallFunction{
				Name:      strings.TrimSpace(rawStringValue(fields["name"])),
				Arguments: rawStringValue(fields["arguments"]),
			},
		}},
	}
}

// normalizeResponsesFunctionCallOutputItem maps {"type":"function_call_output"}
// onto a tool message so the result can be correlated by call id.
func normalizeResponsesFunctionCallOutputItem(fields map[string]json.RawMessage) Message {
	message := Message{Role: "tool", ToolCallID: responsesItemCallID(fields)}
	if output := responsesOutputText(fields["output"]); output != "" {
		message.Content = []ContentPart{{Type: "text", Text: output}}
	}
	return message
}

func responsesItemCallID(fields map[string]json.RawMessage) string {
	for _, key := range []string{"call_id", "id"} {
		if value := strings.TrimSpace(rawStringValue(fields[key])); value != "" {
			return value
		}
	}
	return ""
}

// responsesOutputText accepts the plain string, the structured {"text": ...},
// the nested {"content": ...} and the array-of-content-parts shapes seen for
// function_call_output.
func responsesOutputText(raw json.RawMessage) string {
	if text := rawStringValue(raw); text != "" {
		return text
	}
	if text, ok := responsesContentPartsText(raw); ok {
		return text
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return ""
	}
	for _, key := range []string{"text", "output_text"} {
		if text := rawStringValue(fields[key]); text != "" {
			return text
		}
	}
	content, ok := fields["content"]
	if !ok {
		return ""
	}
	if text := rawStringValue(content); text != "" {
		return text
	}
	text, _ := responsesContentPartsText(content)
	return text
}

// responsesContentPartsText joins the text of an array of content parts, the
// shape a Responses call output uses when its payload is not a bare string.
func responsesContentPartsText(raw json.RawMessage) (string, bool) {
	var rawParts []json.RawMessage
	if json.Unmarshal(raw, &rawParts) != nil {
		return "", false
	}
	parts, err := parseResponsesContent(raw)
	if err != nil {
		return "", false
	}
	var builder strings.Builder
	for _, part := range parts {
		builder.WriteString(part.Text)
	}
	return builder.String(), true
}

// parseResponsesContent normalizes a Responses content value, which may be a
// bare string or an array of content parts. Parts keep their declared type and
// text; parts the gateway has no dedicated field for are preserved by type.
func parseResponsesContent(raw json.RawMessage) ([]ContentPart, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if text == "" {
			return nil, nil
		}
		return []ContentPart{{Type: "text", Text: text}}, nil
	}

	var rawParts []json.RawMessage
	if err := json.Unmarshal(raw, &rawParts); err != nil {
		return nil, fmt.Errorf("content must be a string or an array of content parts")
	}

	parts := make([]ContentPart, 0, len(rawParts))
	for _, rawPart := range rawParts {
		var partText string
		if err := json.Unmarshal(rawPart, &partText); err == nil {
			if partText != "" {
				parts = append(parts, ContentPart{Type: "text", Text: partText})
			}
			continue
		}

		var fields map[string]json.RawMessage
		if err := json.Unmarshal(rawPart, &fields); err != nil {
			return nil, fmt.Errorf("content part must be a string or an object")
		}
		part := ContentPart{
			Type:      strings.TrimSpace(rawStringValue(fields["type"])),
			ImageURL:  rawStringValue(fields["image_url"]),
			MediaType: rawStringValue(fields["media_type"]),
			ImageData: rawStringValue(fields["image_data"]),
		}
		if part.Type == "" {
			part.Type = "text"
		}
		part.Text = rawStringValue(fields["text"])
		if part.Text == "" {
			// Some Responses parts name their text field after the part type,
			// for example {"type":"output_text","output_text":"hi"}.
			if value, ok := fields[part.Type]; ok {
				part.Text = rawStringValue(value)
			}
		}
		parts = append(parts, part)
	}
	return parts, nil
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
