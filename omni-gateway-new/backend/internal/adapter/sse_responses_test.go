package adapter

import (
	"strings"
	"testing"

	"github.com/zjvtit/omni-gateway/internal/protocol"
)

func parseSSEEvents(t *testing.T, stream string) []protocol.StreamEvent {
	t.Helper()
	var got []protocol.StreamEvent
	if err := (&OpenAIAdapter{}).parseSSE(strings.NewReader(stream), func(event protocol.StreamEvent) error {
		got = append(got, event)
		return nil
	}); err != nil {
		t.Fatalf("parseSSE returned error: %v", err)
	}
	return got
}

func TestParseSSEResponsesFunctionCallArgumentFragments(t *testing.T) {
	stream := strings.Join([]string{
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","output_index":1,"response_id":"resp-9","item":{"type":"function_call","id":"item-1","call_id":"call-1","name":"lookup","arguments":""}}`,
		"",
		"event: response.function_call_arguments.delta",
		`data: {"type":"response.function_call_arguments.delta","output_index":1,"item_id":"item-1","name":"lookup","delta":"{\"q\":"}`,
		"",
		"event: response.function_call_arguments.delta",
		`data: {"type":"response.function_call_arguments.delta","output_index":1,"item_id":"item-1","delta":"\"x\"}"}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp-9","usage":{"input_tokens":3,"output_tokens":1}}}`,
		"",
	}, "\n")

	got := parseSSEEvents(t, stream)
	if len(got) != 5 {
		t.Fatalf("got %d events, want 5: %#v", len(got), got)
	}

	added := got[0]
	if added.Type != "chunk" || added.Event != "response.output_item.added" || added.ResponseID != "resp-9" || added.Index != 1 {
		t.Fatalf("unexpected output item event: %#v", added)
	}
	if len(added.ToolCalls) != 1 {
		t.Fatalf("output item did not expose a tool call: %#v", added)
	}
	if delta := added.ToolCalls[0]; delta.Index != 1 || delta.ID != "call-1" || delta.Name != "lookup" || delta.Arguments != "" {
		t.Fatalf("unexpected announced tool call: %#v", delta)
	}

	var arguments strings.Builder
	for _, event := range got[1:3] {
		if event.Type != "chunk" || event.Event != "response.function_call_arguments.delta" || event.Index != 1 {
			t.Fatalf("unexpected argument event: %#v", event)
		}
		if len(event.ToolCalls) != 1 {
			t.Fatalf("argument event carried %d tool calls, want 1: %#v", len(event.ToolCalls), event)
		}
		delta := event.ToolCalls[0]
		if delta.ID != "item-1" {
			t.Fatalf("argument fragment lost its item id: %#v", delta)
		}
		arguments.WriteString(delta.Arguments)
	}
	if arguments.String() != `{"q":"x"}` {
		t.Fatalf("reassembled arguments = %q, want %q", arguments.String(), `{"q":"x"}`)
	}

	if got[3].Type != "usage" || got[3].ResponseID != "resp-9" || got[3].Usage.Get("input") != 3 {
		t.Fatalf("unexpected usage event: %#v", got[3])
	}
	if got[4].Type != "done" || got[4].ResponseID != "resp-9" || got[4].Event != "response.completed" {
		t.Fatalf("unexpected completion event: %#v", got[4])
	}
}

func TestParseSSEResponsesToleratesMissingResponseID(t *testing.T) {
	got := parseSSEEvents(t, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n")
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1: %#v", len(got), got)
	}
	if got[0].Type != "chunk" || got[0].Event != "response.output_text.delta" || got[0].Delta.Content[0].Text != "hi" {
		t.Fatalf("unexpected delta event: %#v", got[0])
	}
	if got[0].ResponseID != "" {
		t.Fatalf("missing response id should stay empty, got %q", got[0].ResponseID)
	}
	if got[0].Index != 0 {
		t.Fatalf("missing output index should default to 0, got %d", got[0].Index)
	}
}

func TestParseSSEChatChunkCarriesIndexAndResponseID(t *testing.T) {
	got := parseSSEEvents(t, "data: {\"id\":\"chatcmpl-7\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":2,\"delta\":{\"content\":\"x\"}}]}\n\n")
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1: %#v", len(got), got)
	}
	if got[0].Index != 2 || got[0].ResponseID != "chatcmpl-7" || got[0].Delta.Content[0].Text != "x" {
		t.Fatalf("unexpected chat chunk: %#v", got[0])
	}
}

func TestParseSSEResponsesNonFunctionOutputItemStaysAChunk(t *testing.T) {
	got := parseSSEEvents(t, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"msg-1\",\"content\":[]}}\n\n")
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1: %#v", len(got), got)
	}
	if got[0].Type != "chunk" || len(got[0].ToolCalls) != 0 || got[0].Event != "response.output_item.added" {
		t.Fatalf("non-function item should not become a tool call: %#v", got[0])
	}
	if string(got[0].Raw) == "" {
		t.Fatal("non-function item lost its raw payload")
	}
}

func TestParseSSEResponsesOutputItemDoneDoesNotRepeatArguments(t *testing.T) {
	stream := strings.Join([]string{
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call-7","name":"lookup","arguments":""}}`,
		"",
		"event: response.function_call_arguments.delta",
		`data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"q\":1}"}`,
		"",
		"event: response.output_item.done",
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","call_id":"call-7","name":"lookup","arguments":"{\"q\":1}"}}`,
		"",
	}, "\n")

	got := parseSSEEvents(t, stream)
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3: %#v", len(got), got)
	}
	if len(got[2].ToolCalls) != 0 {
		t.Fatalf("output_item.done must not repeat the completed arguments: %#v", got[2].ToolCalls)
	}
	if got[2].Event != "response.output_item.done" || string(got[2].Raw) == "" {
		t.Fatalf("output_item.done should stay a raw chunk: %#v", got[2])
	}
}

func TestParseSSEResponsesFunctionCallArgumentsDoneIsNotADelta(t *testing.T) {
	got := parseSSEEvents(t, "data: {\"type\":\"response.function_call_arguments.done\",\"output_index\":0,\"delta\":\"{}\",\"arguments\":\"{}\"}\n\n")
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1: %#v", len(got), got)
	}
	if len(got[0].ToolCalls) != 0 {
		t.Fatalf(".done must not be counted as an argument fragment: %#v", got[0].ToolCalls)
	}
	if string(got[0].Raw) == "" {
		t.Fatal(".done event lost its raw payload")
	}
}

func TestParseSSEResponsesToleratesNonStringArgumentDelta(t *testing.T) {
	got := parseSSEEvents(t, "data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":0,\"delta\":{\"text\":\"x\"}}\n\n")
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1: %#v", len(got), got)
	}
	if len(got[0].ToolCalls) != 0 {
		t.Fatalf("non-string delta should fall through to the generic path: %#v", got[0])
	}
	if string(got[0].Raw) == "" {
		t.Fatal("non-string delta lost its raw payload")
	}
}

func TestParseSSEResponsesToleratesUndecodableDelta(t *testing.T) {
	got := parseSSEEvents(t, "data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":0,\"delta\":123}\n\n")
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1: %#v", len(got), got)
	}
	if len(got[0].ToolCalls) != 0 {
		t.Fatalf("a numeric delta must not become a tool call: %#v", got[0])
	}
	if string(got[0].Raw) == "" {
		t.Fatal("a delta the message shape cannot decode must still be proxied raw")
	}
}
