package adapter

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/zjvtit/omni-gateway/internal/protocol"
)

// anthropicToolTurnMessages mirrors the OpenAI replay of one assistant turn
// that called two tools and received both results, plus the system/developer
// mapping and a trailing plain user message.
func anthropicToolTurnMessages() []protocol.Message {
	return []protocol.Message{
		{Role: "system", Content: []protocol.ContentPart{{Type: "text", Text: "be brief"}}},
		{Role: "developer", Content: []protocol.ContentPart{{Type: "text", Text: "be exact"}}},
		{Role: "user", Content: []protocol.ContentPart{{Type: "text", Text: "weather and time"}}},
		{Role: "assistant", ToolCalls: []protocol.ToolCall{
			{ID: "call_weather", Type: "function", Function: protocol.ToolCallFunction{Name: "get_weather", Arguments: `{"city":"Paris"}`}},
			{ID: "call_time", Type: "function", Function: protocol.ToolCallFunction{Name: "get_time", Arguments: `{"zone":"CET"}`}},
		}},
		{Role: "tool", ToolCallID: "call_weather", Content: []protocol.ContentPart{{Type: "text", Text: "sunny"}}},
		{Role: "tool", ToolCallID: "call_time", Content: []protocol.ContentPart{{Type: "text", Text: "12:00"}}},
		{Role: "user", Content: []protocol.ContentPart{{Type: "text", Text: "thanks"}}},
	}
}

// anthropicDecodedMessages returns the outgoing messages array of a decoded
// payload. JSON decoding produces []any of map[string]any, unlike the
// []map[string]any buildPayload assembles before marshalling.
func anthropicDecodedMessages(t *testing.T, payload map[string]any) []any {
	t.Helper()
	messages, ok := payload["messages"].([]any)
	if !ok {
		t.Fatalf("messages = %#v, want JSON array", payload["messages"])
	}
	return messages
}

// assertAnthropicPlainTurn asserts a message that must stay a single plain
// text turn, i.e. that was not folded into another message.
func assertAnthropicPlainTurn(t *testing.T, raw any, wantRole, wantText string) {
	t.Helper()
	message, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("message = %#v, want object", raw)
	}
	if message["role"] != wantRole || message["content"] != wantText {
		t.Fatalf("message = %#v, want role %q with plain text %q", message, wantRole, wantText)
	}
}

func assertAnthropicToolUseBlock(t *testing.T, raw any, wantID, wantName string) {
	t.Helper()
	block, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("content block = %#v, want object", raw)
	}
	if block["type"] != "tool_use" || block["id"] != wantID || block["name"] != wantName {
		t.Fatalf("content block = %#v, want tool_use id %q name %q", block, wantID, wantName)
	}
}

func assertAnthropicToolResultBlock(t *testing.T, raw any, wantID, wantText string) {
	t.Helper()
	block, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("content block = %#v, want object", raw)
	}
	if block["type"] != "tool_result" || block["tool_use_id"] != wantID || block["content"] != wantText {
		t.Fatalf("content block = %#v, want tool_result id %q content %q", block, wantID, wantText)
	}
}

// TestAnthropicConsecutiveToolResultsMergeIntoOneUserTurn performs a real
// round trip for both Chat and ChatStream and inspects the JSON body the
// adapter sent upstream: two consecutive tool results must share one user
// message holding exactly two ordered tool_result blocks, each keeping its own
// tool_use_id and text.
func TestAnthropicConsecutiveToolResultsMergeIntoOneUserTurn(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			req := &protocol.ChatRequest{Model: "claude", Messages: anthropicToolTurnMessages()}
			payload, sent, err := captureAnthropicRequest(t, req, stream)
			if err != nil || !sent {
				t.Fatalf("request not sent upstream: sent=%v err=%v", sent, err)
			}
			if payload["system"] != "be brief\n\nbe exact" {
				t.Fatalf("system = %#v, want hoisted system+developer text", payload["system"])
			}
			messages := anthropicDecodedMessages(t, payload)
			if len(messages) != 4 {
				t.Fatalf("messages = %d, want 4: %#v", len(messages), messages)
			}
			// Existing user message order and text survive; it does not absorb
			// the tool results that follow the assistant turn.
			assertAnthropicPlainTurn(t, messages[0], "user", "weather and time")

			assistant, ok := messages[1].(map[string]any)
			if !ok || assistant["role"] != "assistant" {
				t.Fatalf("messages[1] = %#v, want assistant", messages[1])
			}
			uses, ok := assistant["content"].([]any)
			if !ok || len(uses) != 2 {
				t.Fatalf("assistant content = %#v, want 2 tool_use blocks", assistant["content"])
			}
			assertAnthropicToolUseBlock(t, uses[0], "call_weather", "get_weather")
			assertAnthropicToolUseBlock(t, uses[1], "call_time", "get_time")

			// Both results belong to one user turn, in the original order.
			group, ok := messages[2].(map[string]any)
			if !ok || group["role"] != "user" {
				t.Fatalf("messages[2] = %#v, want a single user turn with both tool results", messages[2])
			}
			results, ok := group["content"].([]any)
			if !ok || len(results) != 2 {
				t.Fatalf("tool result turn content = %#v, want exactly 2 tool_result blocks", group["content"])
			}
			assertAnthropicToolResultBlock(t, results[0], "call_weather", "sunny")
			assertAnthropicToolResultBlock(t, results[1], "call_time", "12:00")

			// The turn after the results stays separate and stays a user text.
			assertAnthropicPlainTurn(t, messages[3], "user", "thanks")
		})
	}
}

// TestAnthropicConsecutiveUserMessagesAreNotMerged guards the other side of
// the grouping rule: only tool messages merge, ordinary consecutive user
// messages must be forwarded one by one.
func TestAnthropicConsecutiveUserMessagesAreNotMerged(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			req := &protocol.ChatRequest{Model: "claude", Messages: []protocol.Message{
				{Role: "user", Content: []protocol.ContentPart{{Type: "text", Text: "one"}}},
				{Role: "user", Content: []protocol.ContentPart{{Type: "text", Text: "two"}}},
				{Role: "user", Content: []protocol.ContentPart{{Type: "text", Text: "three"}}},
			}}
			payload, sent, err := captureAnthropicRequest(t, req, stream)
			if err != nil || !sent {
				t.Fatalf("request not sent upstream: sent=%v err=%v", sent, err)
			}
			messages := anthropicDecodedMessages(t, payload)
			if len(messages) != 3 {
				t.Fatalf("messages = %d, want 3 separate user turns: %#v", len(messages), messages)
			}
			assertAnthropicPlainTurn(t, messages[0], "user", "one")
			assertAnthropicPlainTurn(t, messages[1], "user", "two")
			assertAnthropicPlainTurn(t, messages[2], "user", "three")
		})
	}
}

// TestAnthropicToolResultGroupsCloseAtOtherMessages checks that a non-tool
// message ends the current group, so a later tool run opens a fresh user turn
// while still merging its own consecutive results.
func TestAnthropicToolResultGroupsCloseAtOtherMessages(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			req := &protocol.ChatRequest{Model: "claude", Messages: []protocol.Message{
				{Role: "assistant", ToolCalls: []protocol.ToolCall{{ID: "call_a", Type: "function", Function: protocol.ToolCallFunction{Name: "first", Arguments: `{}`}}}},
				{Role: "tool", ToolCallID: "call_a", Content: []protocol.ContentPart{{Type: "text", Text: "result-a"}}},
				{Role: "assistant", Content: []protocol.ContentPart{{Type: "text", Text: "another round"}}, ToolCalls: []protocol.ToolCall{
					{ID: "call_b", Type: "function", Function: protocol.ToolCallFunction{Name: "second", Arguments: `{}`}},
					{ID: "call_c", Type: "function", Function: protocol.ToolCallFunction{Name: "third", Arguments: `{}`}},
				}},
				{Role: "tool", ToolCallID: "call_b", Content: []protocol.ContentPart{{Type: "text", Text: "result-b"}}},
				{Role: "tool", ToolCallID: "call_c", Content: []protocol.ContentPart{{Type: "text", Text: "result-c"}}},
			}}
			payload, sent, err := captureAnthropicRequest(t, req, stream)
			if err != nil || !sent {
				t.Fatalf("request not sent upstream: sent=%v err=%v", sent, err)
			}
			messages := anthropicDecodedMessages(t, payload)
			if len(messages) != 4 {
				t.Fatalf("messages = %d, want 4: %#v", len(messages), messages)
			}
			first, ok := messages[1].(map[string]any)
			if !ok || first["role"] != "user" {
				t.Fatalf("messages[1] = %#v, want the first tool result turn", messages[1])
			}
			firstResults, ok := first["content"].([]any)
			if !ok || len(firstResults) != 1 {
				t.Fatalf("first tool result turn content = %#v, want 1 block", first["content"])
			}
			assertAnthropicToolResultBlock(t, firstResults[0], "call_a", "result-a")

			second, ok := messages[3].(map[string]any)
			if !ok || second["role"] != "user" {
				t.Fatalf("messages[3] = %#v, want the second tool result turn", messages[3])
			}
			secondResults, ok := second["content"].([]any)
			if !ok || len(secondResults) != 2 {
				t.Fatalf("second tool result turn content = %#v, want 2 blocks", second["content"])
			}
			assertAnthropicToolResultBlock(t, secondResults[0], "call_b", "result-b")
			assertAnthropicToolResultBlock(t, secondResults[1], "call_c", "result-c")
		})
	}
}

// TestAnthropicRejectsUnrepresentableToolResults checks on the wire that a tool
// result the Anthropic Messages API cannot represent is refused before any HTTP
// request: an empty or unknown tool_call_id and a result carrying an image. The
// refusal must classify as ErrInvalidRequest, must never reach the upstream,
// and its static text must not echo the ID, tool name, output, or image URL.
// The positive row proves an output_text result is still forwarded.
func TestAnthropicRejectsUnrepresentableToolResults(t *testing.T) {
	const (
		knownCallID = "call_known"
		unknownCall = "call_unknown"
		toolName    = "secret_tool"
		imageURL    = "https://cdn.example.com/secret-diagram.png"
		resultText  = "secret-result"
	)
	assistantTurn := func(id string) protocol.Message {
		return protocol.Message{Role: "assistant", ToolCalls: []protocol.ToolCall{
			{ID: id, Type: "function", Function: protocol.ToolCallFunction{Name: toolName, Arguments: `{}`}},
		}}
	}
	cases := []struct {
		name      string
		messages  []protocol.Message
		wantErr   error  // nil means the request must reach the upstream
		wantText  string // tool_result text expected upstream for a forwarded request
		forbidden []string
	}{
		{
			name: "unknown tool_call_id",
			messages: []protocol.Message{
				assistantTurn(knownCallID),
				{Role: "tool", ToolCallID: unknownCall, Content: []protocol.ContentPart{{Type: "text", Text: resultText}}},
			},
			wantErr:   errAnthropicToolResultUnknownID,
			forbidden: []string{unknownCall, resultText, toolName},
		},
		{
			name: "empty tool_call_id",
			messages: []protocol.Message{
				assistantTurn(knownCallID),
				{Role: "tool", Content: []protocol.ContentPart{{Type: "text", Text: resultText}}},
			},
			wantErr:   errAnthropicToolResultMissingID,
			forbidden: []string{knownCallID, resultText, toolName},
		},
		{
			name: "intervening user closes the assistant turn",
			messages: []protocol.Message{
				assistantTurn(knownCallID),
				{Role: "user", Content: []protocol.ContentPart{{Type: "text", Text: "another turn"}}},
				{Role: "tool", ToolCallID: knownCallID, Content: []protocol.ContentPart{{Type: "text", Text: resultText}}},
			},
			wantErr:   errAnthropicToolResultUnknownID,
			forbidden: []string{knownCallID, resultText, toolName},
		},
		{
			name: "duplicate result consumes the call ID",
			messages: []protocol.Message{
				assistantTurn(knownCallID),
				{Role: "tool", ToolCallID: knownCallID, Content: []protocol.ContentPart{{Type: "text", Text: "first"}}},
				{Role: "tool", ToolCallID: knownCallID, Content: []protocol.ContentPart{{Type: "text", Text: resultText}}},
			},
			wantErr:   errAnthropicToolResultUnknownID,
			forbidden: []string{knownCallID, resultText, toolName},
		},
		{
			name: "mixed text and image tool result",
			messages: []protocol.Message{
				assistantTurn(knownCallID),
				{Role: "tool", ToolCallID: knownCallID, Content: []protocol.ContentPart{{Type: "text", Text: resultText}, {Type: "image_url", ImageURL: imageURL}}},
			},
			wantErr:   errAnthropicToolResultContent,
			forbidden: []string{knownCallID, resultText, imageURL, toolName},
		},
		{
			name: "image tool result",
			messages: []protocol.Message{
				assistantTurn(knownCallID),
				{Role: "tool", ToolCallID: knownCallID, Content: []protocol.ContentPart{{Type: "image_url", ImageURL: imageURL}}},
			},
			wantErr:   errAnthropicToolResultContent,
			forbidden: []string{knownCallID, imageURL, toolName},
		},
		{
			name: "output_text forwarded",
			messages: []protocol.Message{
				assistantTurn(knownCallID),
				{Role: "tool", ToolCallID: knownCallID, Content: []protocol.ContentPart{{Type: "output_text", Text: resultText}}},
			},
			wantText: resultText,
		},
	}
	for _, stream := range []bool{false, true} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("stream=%t/%s", stream, tc.name), func(t *testing.T) {
				req := &protocol.ChatRequest{Model: "claude", Messages: tc.messages}
				payload, sent, err := captureAnthropicRequest(t, req, stream)
				if tc.wantErr == nil {
					if err != nil || !sent {
						t.Fatalf("request not sent upstream: sent=%v err=%v", sent, err)
					}
					assertAnthropicSingleToolResult(t, payload, knownCallID, tc.wantText)
					return
				}
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error = %v, want %v", err, tc.wantErr)
				}
				if !errors.Is(err, ErrInvalidRequest) {
					t.Fatalf("error = %v, want ErrInvalidRequest", err)
				}
				if sent {
					t.Fatalf("refused tool result still reached the upstream: %#v", payload)
				}
				for _, forbidden := range tc.forbidden {
					if strings.Contains(err.Error(), forbidden) {
						t.Fatalf("error text leaks request data %q: %v", forbidden, err)
					}
				}
			})
		}
	}
}

// assertAnthropicSingleToolResult asserts the payload holds the assistant
// tool_use turn followed by exactly one user turn with one tool_result block.
func assertAnthropicSingleToolResult(t *testing.T, payload map[string]any, wantID, wantText string) {
	t.Helper()
	messages := anthropicDecodedMessages(t, payload)
	if len(messages) != 2 {
		t.Fatalf("messages = %d, want assistant turn plus one result turn: %#v", len(messages), messages)
	}
	group, ok := messages[1].(map[string]any)
	if !ok || group["role"] != "user" {
		t.Fatalf("messages[1] = %#v, want user tool result turn", messages[1])
	}
	results, ok := group["content"].([]any)
	if !ok || len(results) != 1 {
		t.Fatalf("tool result turn content = %#v, want 1 block", group["content"])
	}
	assertAnthropicToolResultBlock(t, results[0], wantID, wantText)
}
