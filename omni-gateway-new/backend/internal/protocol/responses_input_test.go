package protocol

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeResponsesInputShapes(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []Message
	}{
		{
			name: "absent input",
			raw:  "",
			want: nil,
		},
		{
			name: "null input",
			raw:  `null`,
			want: nil,
		},
		{
			name: "bare string becomes one user message",
			raw:  `"hello"`,
			want: []Message{{Role: "user", Content: []ContentPart{{Type: "text", Text: "hello"}}}},
		},
		{
			name: "message array with string content",
			raw:  `[{"role":"user","content":"hi"},{"role":"assistant","content":"there"}]`,
			want: []Message{
				{Role: "user", Content: []ContentPart{{Type: "text", Text: "hi"}}},
				{Role: "assistant", Content: []ContentPart{{Type: "text", Text: "there"}}},
			},
		},
		{
			name: "typed message item with content parts",
			raw:  `[{"type":"message","role":"user","content":[{"type":"input_text","text":"look"}]}]`,
			want: []Message{{Role: "user", Content: []ContentPart{{Type: "input_text", Text: "look"}}}},
		},
		{
			name: "content part named after its type is read",
			raw:  `[{"type":"message","role":"assistant","content":[{"type":"output_text","output_text":"done"}]}]`,
			want: []Message{{Role: "assistant", Content: []ContentPart{{Type: "output_text", Text: "done"}}}},
		},
		{
			name: "string item inside the array",
			raw:  `["plain"]`,
			want: []Message{{Role: "user", Content: []ContentPart{{Type: "text", Text: "plain"}}}},
		},
		{
			name: "role defaults to user",
			raw:  `[{"content":"hello"}]`,
			want: []Message{{Role: "user", Content: []ContentPart{{Type: "text", Text: "hello"}}}},
		},
		{
			name: "function call item becomes an assistant tool call",
			raw:  `[{"type":"function_call","call_id":"call-1","name":"lookup","arguments":"{\"q\":\"x\"}"}]`,
			want: []Message{{Role: "assistant", ToolCalls: []ToolCall{{
				ID:       "call-1",
				Type:     "function",
				Function: ToolCallFunction{Name: "lookup", Arguments: `{"q":"x"}`},
			}}}},
		},
		{
			name: "function call output item becomes a tool message",
			raw:  `[{"type":"function_call_output","call_id":"call-1","output":"42"}]`,
			want: []Message{{Role: "tool", ToolCallID: "call-1", Content: []ContentPart{{Type: "text", Text: "42"}}}},
		},
		{
			name: "unknown item types are skipped without loss",
			raw:  `[{"type":"input_image","image_url":"https://example.test/a.png"},{"role":"user","content":"hi"}]`,
			want: []Message{{Role: "user", Content: []ContentPart{{Type: "text", Text: "hi"}}}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var raw json.RawMessage
			if tc.raw != "" {
				raw = json.RawMessage(tc.raw)
			}
			got, err := NormalizeResponsesInput(raw)
			if err != nil {
				t.Fatalf("NormalizeResponsesInput(%s) returned error: %v", tc.raw, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("NormalizeResponsesInput(%s) = %#v, want %#v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestNormalizeResponsesInputRejectsMalformedValues(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr string
	}{
		{name: "empty string", raw: `""`, wantErr: "input must not be empty"},
		{name: "blank string", raw: `"   "`, wantErr: "input must not be empty"},
		{name: "number", raw: `42`, wantErr: "input must be a string or an array of input items"},
		{name: "object", raw: `{"role":"user"}`, wantErr: "input must be a string or an array of input items"},
		{name: "boolean", raw: `true`, wantErr: "input must be a string or an array of input items"},
		{name: "array of numbers", raw: `[1,2]`, wantErr: "input item 0: must be a string or an object"},
		{name: "array with a later invalid element", raw: `[null,7]`, wantErr: "input item 1: must be a string or an object"},
		{name: "content of an unsupported type", raw: `[{"role":"user","content":42}]`, wantErr: "content must be a string or an array of content parts"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeResponsesInput(json.RawMessage(tc.raw))
			if err == nil {
				t.Fatalf("NormalizeResponsesInput(%s) = %#v, want error", tc.raw, got)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("NormalizeResponsesInput(%s) error = %q, want it to contain %q", tc.raw, err.Error(), tc.wantErr)
			}
		})
	}
}

func TestNormalizeResponsesInputKeepsUnknownItemsAvailableRaw(t *testing.T) {
	raw := json.RawMessage(`[{"type":"reasoning","summary":[]},{"type":"input_image","image_url":"https://example.test/a.png"}]`)
	messages, err := NormalizeResponsesInput(raw)
	if err != nil {
		t.Fatalf("NormalizeResponsesInput returned error: %v", err)
	}
	if len(messages) != 0 {
		t.Fatalf("non-chat items produced messages: %#v", messages)
	}
	// The original value is untouched, so a same-protocol passthrough still
	// forwards every field the gateway cannot model.
	if string(raw) != `[{"type":"reasoning","summary":[]},{"type":"input_image","image_url":"https://example.test/a.png"}]` {
		t.Fatalf("raw input was mutated: %s", raw)
	}
}

func TestNormalizeResponsesInputJoinsFunctionCallOutputParts(t *testing.T) {
	body := json.RawMessage(`[{"type":"function_call_output","call_id":"call-2","output":[{"type":"input_text","text":"a"},{"type":"input_text","text":"b"}]}]`)
	messages, err := NormalizeResponsesInput(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(messages) != 1 || messages[0].Role != "tool" || messages[0].ToolCallID != "call-2" {
		t.Fatalf("unexpected messages: %#v", messages)
	}
	if len(messages[0].Content) != 1 || messages[0].Content[0].Text != "ab" {
		t.Fatalf("array shaped output was dropped: %#v", messages[0].Content)
	}
}

func TestNormalizeResponsesInputIgnoresBlankAndNullElements(t *testing.T) {
	messages, err := NormalizeResponsesInput(json.RawMessage(`[null,"",{"role":"user","content":"hi"}]`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(messages) != 1 || messages[0].Content[0].Text != "hi" {
		t.Fatalf("unexpected messages: %#v", messages)
	}
}
