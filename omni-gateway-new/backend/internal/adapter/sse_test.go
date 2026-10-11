package adapter

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/zjvtit/omni-gateway/internal/protocol"
)

func TestParseSSEChatCompletion(t *testing.T) {
	stream := strings.Join([]string{
		": keepalive",
		"",
		"event: message",
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1700000000,"system_fingerprint":"fp-stream","choices":[{"index":0,"delta":{"role":"assistant","content":"hel","reasoning_content":"let me"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1700000000,"system_fingerprint":"fp-stream","choices":[{"index":0,"delta":{"content":"lo","reasoning_content":" think","tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]},"logprobs":{"content":[{"token":"lo"}]},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1700000000,"system_fingerprint":"fp-stream","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":5,"total_tokens":17,"prompt_tokens_details":{"cached_tokens":3}}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")

	var got []protocol.StreamEvent
	adapter := &OpenAIAdapter{}
	if err := adapter.parseSSE(strings.NewReader(stream), func(event protocol.StreamEvent) error {
		got = append(got, event)
		return nil
	}); err != nil {
		t.Fatalf("parseSSE returned error: %v", err)
	}

	if len(got) != 4 {
		t.Fatalf("got %d events, want 4: %#v", len(got), got)
	}
	if got[0].Type != "chunk" || got[0].Delta.Role != "assistant" || got[0].Delta.Content[0].Text != "hel" || got[0].Delta.ReasoningContent != "let me" || got[0].Created != 1700000000 || got[0].SystemFingerprint != "fp-stream" {
		t.Fatalf("unexpected first chunk: %#v", got[0])
	}
	if got[1].Type != "chunk" || got[1].Delta.Content[0].Text != "lo" || got[1].Delta.ReasoningContent != " think" || len(got[1].Delta.ToolCalls) != 1 || got[1].ResponseID != "chatcmpl-1" || got[1].Index != 0 {
		t.Fatalf("unexpected second chunk: %#v", got[1])
	}
	if got[1].Delta.ToolCalls[0].Function.Name != "lookup" {
		t.Fatalf("unexpected tool call: %#v", got[1].Delta.ToolCalls[0])
	}
	if tc := got[1].Delta.ToolCalls[0]; tc.Index == nil || *tc.Index != 0 {
		t.Fatalf("tool index=0 lost: %#v", tc)
	}
	encoded, err := json.Marshal(got[1].Delta.ToolCalls[0])
	if err != nil || !strings.Contains(string(encoded), `"index":0`) {
		t.Fatalf("tool index=0 omitted from JSON: %s, %v", encoded, err)
	}
	if string(got[1].LogProbs) != `{"content":[{"token":"lo"}]}` || got[1].Created != 1700000000 || got[1].SystemFingerprint != "fp-stream" {
		t.Fatalf("chunk metadata lost: %#v", got[1])
	}
	if got[2].Type != "usage" || got[2].Usage.Get("input") != 12 || got[2].Usage.Get("output") != 5 || got[2].Usage.Get("cache_input") != 3 || got[2].Created != 1700000000 || got[2].SystemFingerprint != "fp-stream" || got[2].ResponseID != "chatcmpl-1" {
		t.Fatalf("unexpected usage event: %#v", got[2])
	}
	if got[3].Type != "done" {
		t.Fatalf("unexpected final event: %#v", got[3])
	}
}

func TestSSERejectsOversizedMultilineFrame(t *testing.T) {
	// Every line is within the scanner limit, but their combined frame is not.
	payload := strings.Repeat("data: "+strings.Repeat(" ", 1024)+"\n", maxSSELineSize/1024+1)
	for name, parse := range map[string]func(io.Reader) error{
		"openai": func(r io.Reader) error {
			return (&OpenAIAdapter{}).parseSSE(r, func(protocol.StreamEvent) error { return nil })
		},
		"named": func(r io.Reader) error { return parseNamedSSE(r, func(string, []byte) error { return nil }) },
	} {
		t.Run(name, func(t *testing.T) {
			if err := parse(strings.NewReader(payload)); err == nil || !strings.Contains(err.Error(), "frame exceeds") {
				t.Fatalf("oversized frame accepted: %v", err)
			}
		})
	}
}

func TestParseSSEHandlesMultilineDataAndEOF(t *testing.T) {
	stream := "data: {\"choices\":[{\"delta\":{\n" +
		"data: \"role\":\"assistant\",\n" +
		"data: \"content\":\"ok\"},\"finish_reason\":null}]}"

	var got []protocol.StreamEvent
	if err := (&OpenAIAdapter{}).parseSSE(strings.NewReader(stream), func(event protocol.StreamEvent) error {
		got = append(got, event)
		return nil
	}); err != nil {
		t.Fatalf("parseSSE returned error: %v", err)
	}
	if len(got) != 1 || got[0].Type != "chunk" || got[0].Delta.Content[0].Text != "ok" {
		t.Fatalf("unexpected events: %#v", got)
	}
}

func TestParseSSEResponsesEvents(t *testing.T) {
	stream := strings.Join([]string{
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","delta":"hello"}`,
		"",
		`data: {"type":"response.completed","response":{"usage":{"input_tokens":4,"output_tokens":2}}}`,
		"",
	}, "\n")

	var got []protocol.StreamEvent
	if err := (&OpenAIAdapter{}).parseSSE(strings.NewReader(stream), func(event protocol.StreamEvent) error {
		got = append(got, event)
		return nil
	}); err != nil {
		t.Fatalf("parseSSE returned error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3: %#v", len(got), got)
	}
	if got[0].Type != "chunk" || got[0].Delta.Content[0].Text != "hello" {
		t.Fatalf("unexpected delta: %#v", got[0])
	}
	if string(got[0].Raw) == "" {
		t.Fatal("Responses delta did not preserve Raw")
	}
	if got[1].Type != "usage" || got[1].Usage.Get("input") != 4 || got[1].Usage.Get("output") != 2 {
		t.Fatalf("unexpected usage: %#v", got[1])
	}
	if got[2].Type != "done" || string(got[2].Raw) == "" {
		t.Fatalf("unexpected completion event: %#v", got[2])
	}
}

func TestParseSSEStopsOnEmitError(t *testing.T) {
	wantErr := errors.New("client disconnected")
	stream := "data: {\"choices\":[{\"delta\":{\"content\":\"one\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"two\"}}]}\n\n"
	calls := 0
	err := (&OpenAIAdapter{}).parseSSE(strings.NewReader(stream), func(protocol.StreamEvent) error {
		calls++
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("got error %v, want %v", err, wantErr)
	}
	if calls != 1 {
		t.Fatalf("callback called %d times, want 1", calls)
	}
}

func TestParseSSEAllowsLargeDataLine(t *testing.T) {
	content := strings.Repeat("x", 128*1024)
	stream := `data: {"choices":[{"delta":{"content":"` + content + `"}}]}`

	var got []protocol.StreamEvent
	if err := (&OpenAIAdapter{}).parseSSE(strings.NewReader(stream), func(event protocol.StreamEvent) error {
		got = append(got, event)
		return nil
	}); err != nil {
		t.Fatalf("parseSSE returned error: %v", err)
	}
	if len(got) != 1 || len(got[0].Delta.Content) != 1 || len(got[0].Delta.Content[0].Text) != len(content) {
		t.Fatalf("large event was not preserved: type=%d events=%#v", len(got), got)
	}
}

func TestParseSSERejectsMalformedJSON(t *testing.T) {
	err := (&OpenAIAdapter{}).parseSSE(strings.NewReader("data: {not-json}\n\n"), func(protocol.StreamEvent) error {
		t.Fatal("callback should not be called for malformed JSON")
		return nil
	})
	if err == nil {
		t.Fatal("parseSSE accepted malformed JSON")
	}
}

func TestParseSSEChatMultipleChoicesMetadata(t *testing.T) {
	stream := `data: {"id":"cmpl-two","created":77,"system_fingerprint":"fp-two","choices":[{"index":3,"delta":{"reasoning_content":"why"},"logprobs":{"content":[{"token":"a"}]},"finish_reason":null},{"index":1,"delta":{"content":"ok"},"logprobs":{"content":[{"token":"b"}]},"finish_reason":"stop"}]}` + "\n\n"
	var got []protocol.StreamEvent
	if err := (&OpenAIAdapter{}).parseSSE(strings.NewReader(stream), func(event protocol.StreamEvent) error {
		got = append(got, event)
		return nil
	}); err != nil {
		t.Fatalf("parseSSE returned error: %v", err)
	}
	if len(got) != 2 || got[0].Index != 3 || got[1].Index != 1 || got[0].ResponseID != "cmpl-two" || got[1].ResponseID != "cmpl-two" || got[0].Created != 77 || got[1].SystemFingerprint != "fp-two" || got[0].Delta.ReasoningContent != "why" || got[1].FinishReason != "stop" || string(got[0].LogProbs) != `{"content":[{"token":"a"}]}` || string(got[1].LogProbs) != `{"content":[{"token":"b"}]}` {
		t.Fatalf("choice metadata lost: %#v", got)
	}
}

func TestParseSSEChatRejectsNonStringReasoning(t *testing.T) {
	const secret = "do-not-echo-this"
	stream := `data: {"choices":[{"delta":{"reasoning_content":{"secret":"` + secret + `"}}}]}` + "\n\n"
	err := (&OpenAIAdapter{}).parseSSE(strings.NewReader(stream), func(protocol.StreamEvent) error {
		t.Fatal("invalid delta emitted")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "reasoning_content") || strings.Contains(err.Error(), secret) {
		t.Fatalf("unexpected reasoning error: %v", err)
	}
}

func TestParseSSEStopsAfterTerminalEvent(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{
			name: "chat done",
			body: "data: {\"choices\":[],\"usage\":{\"completion_tokens\":2}}\n\n" +
				"data: [DONE]\n\n" +
				"data: {\"choices\":[],\"usage\":{\"completion_tokens\":999}}\n\n" +
				"data: {malformed}\n\n",
		},
		{
			name: "responses completed",
			body: "event: response.completed\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"output_tokens\":2}}}\n\n" +
				"data: [DONE]\n\n" +
				"data: {malformed}\n\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var events []protocol.StreamEvent
			err := (&OpenAIAdapter{}).parseSSE(strings.NewReader(tc.body), func(event protocol.StreamEvent) error {
				events = append(events, event)
				return nil
			})
			if err != nil || len(events) != 2 || events[0].Type != "usage" || events[0].Usage.Get("output") != 2 || events[1].Type != "done" {
				t.Fatalf("terminal event did not stop subsequent frames: types/usage=%v, error=%v", eventTypes(events), err)
			}
		})
	}
}

func eventTypes(events []protocol.StreamEvent) []string {
	types := make([]string, 0, len(events))
	for _, event := range events {
		types = append(types, event.Type)
	}
	return types
}

func TestParseSSEChatResponseReasoningAlias(t *testing.T) {
	stream := `data: {"id":"c-1","choices":[` +
		`{"index":0,"delta":{"reasoning":"alias","tool_calls":[{"index":0,"function":{"arguments":"part"}}],"custom":{"keep":true}}},` +
		`{"index":1,"delta":{"reasoning_content":"canonical","reasoning":"ignore"}},` +
		`{"index":2,"delta":{"reasoning_content":"","reasoning":"ignored"}},` +
		`{"index":3,"delta":{"reasoning_content":null,"reasoning":"ignored"}},` +
		`{"index":4,"delta":{"content":"plain"}},` +
		`{"index":5,"delta":{"reasoning":99}}` +
		`]}` + "\n\n"
	var got []protocol.StreamEvent
	if err := (&OpenAIAdapter{}).parseSSE(strings.NewReader(stream), func(event protocol.StreamEvent) error {
		got = append(got, event)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 6 {
		t.Fatalf("choices lost: %#v", got)
	}
	for i, want := range []string{"alias", "canonical", "", "", "", ""} {
		if got[i].Index != i || got[i].Delta.ReasoningContent != want {
			t.Fatalf("choice %d: index=%d reasoning=%q, want %q", i, got[i].Index, got[i].Delta.ReasoningContent, want)
		}
	}
	if len(got[0].Delta.ToolCalls) != 1 || got[0].Delta.ToolCalls[0].Index == nil || *got[0].Delta.ToolCalls[0].Index != 0 || string(got[0].Delta.Extra["custom"]) != `{"keep":true}` {
		t.Fatalf("sparse tool delta or extension lost: %#v", got[0].Delta)
	}
	if _, ok := got[0].Delta.Extra["reasoning"]; ok {
		t.Fatalf("response alias duplicated in normalized delta: %#v", got[0].Delta.Extra)
	}
	if string(got[5].Delta.Extra["reasoning"]) != "99" || !strings.Contains(string(got[0].Raw), `"reasoning":"alias"`) {
		t.Fatalf("invalid alias or raw upstream frame lost: %#v", got)
	}
}
