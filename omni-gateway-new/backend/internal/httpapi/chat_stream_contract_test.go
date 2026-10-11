package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zjvtit/omni-gateway/internal/adapter"
	"github.com/zjvtit/omni-gateway/internal/model"
	"github.com/zjvtit/omni-gateway/internal/protocol"
)

type adapterChatContractService struct{ upstream *adapter.OpenAIAdapter }

func (s adapterChatContractService) ListModels(context.Context) ([]string, error) {
	return []string{"public"}, nil
}
func (s adapterChatContractService) Chat(ctx context.Context, r *protocol.ChatRequest) (*protocol.ChatResponse, error) {
	return s.upstream.Chat(ctx, r)
}
func (s adapterChatContractService) ChatStream(ctx context.Context, r *protocol.ChatRequest, emit func(protocol.StreamEvent) error) error {
	return s.upstream.ChatStream(ctx, r, emit)
}

func chatContractHandler(url string, client *http.Client) http.Handler {
	service := adapterChatContractService{upstream: adapter.NewOpenAIAdapter(adapter.Config{BaseURL: url, HTTPClient: client})}
	return NewHandlerWithGateway(nil, nil, gatewayAPIKeyStore(), service, AuthConfig{})
}

func chatContractRequest(ctx context.Context, url string) *http.Request {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url+"/v1/chat/completions", strings.NewReader(`{"model":"public","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	req.Header.Set("Authorization", "Bearer sk-gateway")
	return req
}

func TestChatSSEFlushesSparseParallelToolsAndUsageBeforeDone(t *testing.T) {
	releaseNext := make(chan struct{})
	defer func() {
		select {
		case <-releaseNext:
		default:
			close(releaseNext)
		}
	}()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"id":"c-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"think","tool_calls":[{"index":0,"id":"call-a","type":"function","function":{"name":"alpha","arguments":""}},{"index":1,"id":"call-b","type":"function","function":{"name":"beta","arguments":""}}]},"finish_reason":null}]}`+"\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-releaseNext:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, `data: {"id":"c-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"a\":"}},{"index":1,"function":{"arguments":"{\"b\":2}"}}]},"finish_reason":null}]}`+"\n\n")
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, `data: {"id":"c-1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, `data: {"id":"c-1","choices":[],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`+"\n\n")
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	defer upstream.Close()
	gateway := httptest.NewServer(chatContractHandler(upstream.URL, upstream.Client()))
	defer gateway.Close()
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Do(chatContractRequest(context.Background(), gateway.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status=%d type=%q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	reader := bufio.NewReader(resp.Body)
	first := make(chan string, 1)
	go func() { line, _ := reader.ReadString('\n'); first <- line }()
	select {
	case line := <-first:
		if !strings.Contains(line, `"reasoning_content":"think"`) || !strings.Contains(line, `"index":1`) {
			t.Fatalf("first flushed frame = %s", line)
		}
	case <-time.After(time.Second):
		t.Fatal("first frame was buffered until upstream completion")
	}
	close(releaseNext)
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Count(text, "data: [DONE]") != 1 || !strings.Contains(text, `"finish_reason":"tool_calls"`) || !strings.Contains(text, `"total_tokens":6`) {
		t.Fatalf("completion/usage missing: %s", text)
	}
	var sparse struct {
		Choices []struct {
			Delta struct {
				ToolCalls []map[string]json.RawMessage `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
	}
	frames := strings.Split(strings.TrimSpace(text), "\n\n")
	if len(frames) < 1 || json.Unmarshal([]byte(strings.TrimPrefix(frames[0], "data: ")), &sparse) != nil || len(sparse.Choices) != 1 || len(sparse.Choices[0].Delta.ToolCalls) != 2 {
		t.Fatalf("sparse parallel tool delta missing: %s", text)
	}
	for _, call := range sparse.Choices[0].Delta.ToolCalls {
		if _, hasID := call["id"]; hasID {
			t.Fatalf("invented id in argument-only delta: %s", text)
		}
		if _, hasType := call["type"]; hasType {
			t.Fatalf("invented type in argument-only delta: %s", text)
		}
	}
}

func TestChatSSEFirstFrameAndMidStreamErrors(t *testing.T) {
	for _, tc := range []struct {
		name, frames string
		wantStatus   int
		wantSSE      bool
	}{
		{name: "upstream HTTP error", frames: "HTTP_503", wantStatus: 502},
		{name: "first frame malformed", frames: "data: {broken}\n\n", wantStatus: 502},
		{name: "empty stream EOF", frames: "", wantStatus: 502},
		{name: "first chunk then EOF without terminal", frames: "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n", wantStatus: 200, wantSSE: true},
		{name: "last frame at EOF without blank line", frames: "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}", wantStatus: 200, wantSSE: true},
		{name: "stream failure after first chunk", frames: "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\ndata: {broken}\n\n", wantStatus: 200, wantSSE: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.frames == "HTTP_503" {
					w.WriteHeader(503)
					_, _ = io.WriteString(w, `{"error":{"message":"unavailable"}}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, tc.frames)
				w.(http.Flusher).Flush()
			}))
			defer upstream.Close()
			gateway := httptest.NewServer(chatContractHandler(upstream.URL, upstream.Client()))
			defer gateway.Close()
			client := &http.Client{Timeout: 3 * time.Second}
			resp, err := client.Do(chatContractRequest(context.Background(), gateway.URL))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status=%d body=%s", resp.StatusCode, body)
			}
			if tc.wantSSE {
				if !strings.Contains(string(body), `"content":"partial"`) || strings.Count(string(body), `"code":"upstream_error"`) != 1 || strings.Contains(string(body), "[DONE]") {
					t.Fatalf("midstream error termination: %s", body)
				}
			} else if !json.Valid(body) || strings.Contains(string(body), "[DONE]") {
				t.Fatalf("first failure must be JSON, not SSE: %s", body)
			}
		})
	}
}

// A clean HTTP EOF is not an application-level completion. The real gateway
// must not settle a partial generation as success or invent a [DONE] frame.
func TestChatContractPrematureEOFSettlesFailure(t *testing.T) {
	for _, tc := range []struct {
		name, frame string
		wantStatus  int
	}{
		{name: "no frames", wantStatus: http.StatusBadGateway},
		{name: "partial output", frame: `data: {"choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":null}]}` + "\n\n", wantStatus: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := newContractUpstream(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, tc.frame)
			})
			defer upstream.Close()
			store := singleProviderStore(t, "public-model", "vendor-model", upstream.server.URL)
			keys, secret := contractAPIKeys()
			service := newContractService(t, store, upstream.client())
			settlement := &contractSettlement{}
			service.SetSettlement(settlement)
			handler := NewHandlerWithGateway(nil, nil, keys, service, AuthConfig{})
			response := gatewayRequest(t, handler, http.MethodPost, "/v1/chat/completions", secret, `{"model":"public-model","messages":[{"role":"user","content":"hi"}],"stream":true}`)
			body := response.Body.String()
			if response.Code != tc.wantStatus || strings.Contains(body, "[DONE]") {
				t.Fatalf("premature EOF status=%d body=%s", response.Code, body)
			}
			if tc.wantStatus == http.StatusOK {
				if !strings.Contains(body, `"content":"partial"`) || !strings.Contains(body, `"code":"upstream_error"`) {
					t.Fatalf("partial output must be followed by an error frame: %s", body)
				}
			} else if !json.Valid(response.Body.Bytes()) {
				t.Fatalf("first-frame failure must be JSON: %s", body)
			}
			_, alias, success := settlement.last()
			if alias != "public-model" || success {
				t.Fatalf("unexpected settlement: alias=%q success=%v", alias, success)
			}
		})
	}
}

// A completed stream may still be cut short by a caller's output-token limit.
// That is signaled by finish_reason=length, not by a transport-error frame.
func TestChatSSEPreservesLengthAndLiteralDSML(t *testing.T) {
	const content = `<｜DSML｜ invoke name="Bash"> bc=(aR,aS,pR,p`
	upstream := newContractUpstream(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": content}, "finish_reason": "length"}}})
		_, _ = io.WriteString(w, "data: "+string(chunk)+"\n\ndata: [DONE]\n\n")
	})
	defer upstream.Close()
	store := singleProviderStore(t, "public-model", "vendor-model", upstream.server.URL)
	keys, secret := contractAPIKeys()
	handler := newContractHandler(t, store, keys, upstream.client())
	response := gatewayRequest(t, handler, http.MethodPost, "/v1/chat/completions", secret, `{"model":"public-model","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	frames := strings.Split(strings.TrimSpace(response.Body.String()), "\n\n")
	if response.Code != http.StatusOK || len(frames) != 2 || frames[1] != "data: [DONE]" {
		t.Fatalf("unexpected length stream: status=%d body=%s", response.Code, response.Body.String())
	}
	var got struct {
		Choices []struct {
			Delta        map[string]json.RawMessage `json:"delta"`
			FinishReason string                     `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(frames[0], "data: ")), &got); err != nil || len(got.Choices) != 1 {
		t.Fatalf("decode length frame: %v (%s)", err, frames[0])
	}
	var text string
	if err := json.Unmarshal(got.Choices[0].Delta["content"], &text); err != nil || text != content || got.Choices[0].FinishReason != "length" {
		t.Fatalf("length/content altered: content=%q finish=%q err=%v", text, got.Choices[0].FinishReason, err)
	}
	if _, inventedTool := got.Choices[0].Delta["tool_calls"]; inventedTool {
		t.Fatalf("literal DSML was converted into a tool call: %s", frames[0])
	}
}

func TestChatContractReasoningAliasNonStream(t *testing.T) {
	for _, tc := range []struct {
		name, message, want string
	}{
		{"upstream alias", `{"role":"assistant","content":"answer","reasoning":"thought"}`, "thought"},
		{"canonical wins", `{"role":"assistant","reasoning":"ignore","reasoning_content":"canonical"}`, "canonical"},
		{"empty canonical", `{"role":"assistant","reasoning_content":"","reasoning":"thought"}`, ""},
		{"null canonical", `{"role":"assistant","reasoning_content":null,"reasoning":"thought"}`, ""},
		{"no reasoning, usage only", `{"role":"assistant","content":"answer"}`, ""},
		{"non-string alias", `{"role":"assistant","content":"answer","reasoning":7}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := newContractUpstream(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"c-1","model":"vendor-model","choices":[{"index":0,"message":`+tc.message+`,"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"completion_tokens_details":{"reasoning_tokens":1}}}`)
			})
			defer upstream.Close()
			store := singleProviderStore(t, "public-model", "vendor-model", upstream.server.URL)
			store.providers[1].CompatibilityProfile = model.ProviderCompatibilityGeneric
			keys, secret := contractAPIKeys()
			handler := newContractHandler(t, store, keys, upstream.client())
			response := gatewayRequest(t, handler, http.MethodPost, "/v1/chat/completions", secret, `{"model":"public-model","messages":[{"role":"user","content":"hi"}]}`)
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			var body struct {
				Model   string `json:"model"`
				Choices []struct {
					Message map[string]json.RawMessage `json:"message"`
				} `json:"choices"`
				Usage struct {
					CompletionDetails struct {
						ReasoningTokens int `json:"reasoning_tokens"`
					} `json:"completion_tokens_details"`
				} `json:"usage"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || len(body.Choices) != 1 {
				t.Fatalf("decode public Chat response: %v (%s)", err, response.Body.String())
			}
			message := body.Choices[0].Message
			if body.Model != "public-model" || body.Usage.CompletionDetails.ReasoningTokens != 1 {
				t.Fatalf("model or usage changed: %s", response.Body.String())
			}
			if _, hasAlias := message["reasoning"]; hasAlias {
				t.Fatalf("public response leaked reasoning alias: %s", response.Body.String())
			}
			if tc.want == "" {
				if _, invented := message["reasoning_content"]; invented {
					t.Fatalf("invented reasoning from usage or content: %s", response.Body.String())
				}
			} else if string(message["reasoning_content"]) != `"`+tc.want+`"` {
				t.Fatalf("reasoning_content = %s, want %q: %s", message["reasoning_content"], tc.want, response.Body.String())
			}
			call := upstream.lastCall(t)
			if call.Path != "/chat/completions" || !strings.Contains(string(call.Body), `"model":"vendor-model"`) || strings.Contains(string(call.Body), `"reasoning_content"`) {
				t.Fatalf("upstream request unexpectedly changed: %s %s", call.Path, call.Body)
			}
		})
	}
}

func TestChatContractReasoningAliasStream(t *testing.T) {
	for _, tc := range []struct {
		name, delta, want, rawAlias string
		unchanged                   bool
	}{
		{"upstream alias with sparse tool", `{"reasoning":"thought","tool_calls":[{"index":0,"function":{"arguments":"fragment"}}],"custom":{"keep":true}}`, `"thought"`, "", false},
		{"canonical wins", `{"reasoning":"ignore","reasoning_content":"canonical","content":"answer"}`, `"canonical"`, "", false},
		{"empty canonical", `{"reasoning_content":"","reasoning":"thought"}`, `""`, "", false},
		{"null canonical", `{"reasoning_content":null,"reasoning":"thought"}`, "null", "", false},
		{"no reasoning, sparse tool", `{"tool_calls":[{"index":0,"function":{"arguments":"fragment"}}],"custom":{"keep":true}}`, "", "", true},
		{"canonical only", `{"reasoning_content":"canonical","content":"answer"}`, `"canonical"`, "", true},
		{"non-string alias", `{"content":"answer","reasoning":7}`, "", "7", true},
		{"null alias", `{"content":"answer","reasoning":null}`, "", "null", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := newContractUpstream(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, `data: {"id":"c-1","choices":[{"index":0,"delta":`+tc.delta+`,"finish_reason":null}]}`+"\n\n"+
					`data: {"id":"c-1","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2,"completion_tokens_details":{"reasoning_tokens":1}}}`+"\n\n"+
					"data: [DONE]\n\n")
			})
			defer upstream.Close()
			store := singleProviderStore(t, "public-model", "vendor-model", upstream.server.URL)
			store.providers[1].CompatibilityProfile = model.ProviderCompatibilityGeneric
			keys, secret := contractAPIKeys()
			handler := newContractHandler(t, store, keys, upstream.client())
			response := gatewayRequest(t, handler, http.MethodPost, "/v1/chat/completions", secret, `{"model":"public-model","messages":[{"role":"user","content":"hi"}],"stream":true}`)
			frames := strings.Split(strings.TrimSpace(response.Body.String()), "\n\n")
			if response.Code != http.StatusOK || len(frames) != 3 || frames[2] != "data: [DONE]" {
				t.Fatalf("unexpected public SSE status=%d body=%s", response.Code, response.Body.String())
			}
			var chunk struct {
				Model   string `json:"model"`
				Choices []struct {
					Delta json.RawMessage `json:"delta"`
				} `json:"choices"`
			}
			if err := json.Unmarshal([]byte(strings.TrimPrefix(frames[0], "data: ")), &chunk); err != nil || len(chunk.Choices) != 1 || chunk.Model != "public-model" {
				t.Fatalf("decode public SSE chunk: %v (%s)", err, frames[0])
			}
			var delta map[string]json.RawMessage
			if err := json.Unmarshal(chunk.Choices[0].Delta, &delta); err != nil {
				t.Fatalf("decode public SSE delta: %v (%s)", err, frames[0])
			}
			if tc.unchanged && string(chunk.Choices[0].Delta) != tc.delta {
				t.Fatalf("raw delta changed without a valid alias: %s", chunk.Choices[0].Delta)
			}
			if string(delta["reasoning"]) != tc.rawAlias {
				t.Fatalf("alias incorrectly forwarded: %s", chunk.Choices[0].Delta)
			}
			if tc.want == "" {
				if _, invented := delta["reasoning_content"]; invented {
					t.Fatalf("invented reasoning from usage: %s", chunk.Choices[0].Delta)
				}
			} else if string(delta["reasoning_content"]) != tc.want {
				t.Fatalf("reasoning_content = %s, want %s: %s", delta["reasoning_content"], tc.want, chunk.Choices[0].Delta)
			}
			if tc.name == "upstream alias with sparse tool" || tc.name == "no reasoning, sparse tool" {
				var tools []map[string]json.RawMessage
				if err := json.Unmarshal(delta["tool_calls"], &tools); err != nil || len(tools) != 1 || len(tools[0]) != 2 || string(tools[0]["index"]) != "0" || string(delta["custom"]) != `{"keep":true}` {
					t.Fatalf("sparse tool or unrelated field lost: %s", chunk.Choices[0].Delta)
				}
			}
			if !strings.Contains(frames[1], `"reasoning_tokens":1`) || !strings.Contains(string(upstream.lastCall(t).Body), `"model":"vendor-model"`) {
				t.Fatalf("usage or forwarded model changed: %s", response.Body.String())
			}
		})
	}
}
