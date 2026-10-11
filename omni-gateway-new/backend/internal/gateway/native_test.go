package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zjvtit/omni-gateway/internal/adapter"
	"github.com/zjvtit/omni-gateway/internal/billing"
	"github.com/zjvtit/omni-gateway/internal/model"
	"github.com/zjvtit/omni-gateway/internal/protocol"
)

type nativeSettlement struct {
	usages    []model.Usage
	successes []bool
}

func (s *nativeSettlement) Settle(_ context.Context, _ string, _, _ int64, _, _ string, _ int64, usage model.Usage, success bool) (*billing.Result, error) {
	s.usages = append(s.usages, usage)
	s.successes = append(s.successes, success)
	return &billing.Result{Persisted: true}, nil
}

func TestNativeFailoverRecordsAndCooldown(t *testing.T) {
	for _, api := range []protocol.NativeAPI{protocol.MessagesAPI, protocol.EmbeddingsAPI, protocol.ImagesAPI} {
		t.Run(string(api), func(t *testing.T) {
			var firstCalls, secondCalls atomic.Int32
			first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				firstCalls.Add(1)
				w.Header().Set("Retry-After", "60")
				w.WriteHeader(http.StatusTooManyRequests)
			}))
			defer first.Close()
			second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				secondCalls.Add(1)
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["model"] != "second-model" || body["extension"] != "keep" {
					t.Errorf("incorrect upstream request: %v %v", body, err)
				}
				_, _ = io.WriteString(w, `{"model":"second-model","extension":"keep","data":[{"url":"fixture"}],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}`)
			}))
			defer second.Close()
			service := testService(first.URL, second.URL, first.Client())
			if api == protocol.MessagesAPI {
				for _, provider := range service.store.(*fakeRouteStore).providers {
					provider.Type = model.ProviderAnthropic
				}
			}
			records := newFakeRecordStore()
			settlement := &nativeSettlement{}
			service.recordStore, service.settlement = records, settlement
			ctx := WithRequestMetadata(context.Background(), RequestMetadata{ID: "native-fixture", UserID: 1, APIKeyID: 2, ModelAlias: "chat", API: string(api)})
			req := &protocol.NativeRequest{API: api, Model: "chat", Raw: json.RawMessage(`{"model":"chat","extension":"keep"}`)}
			response, err := service.Native(ctx, req)
			if err != nil || response == nil || !strings.Contains(string(response.Raw), `"model":"chat"`) || response.Usage.Get("input") != 10 {
				t.Fatalf("native result=%v err=%v", response, err)
			}
			if len(settlement.usages) != 2 || settlement.successes[0] || !settlement.successes[1] || settlement.usages[1].Get("output") != 2 {
				t.Fatalf("attempt settlement was lost: %+v", settlement)
			}
			closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := service.Close(closeCtx); err != nil {
				t.Fatal(err)
			}
			requests, attempts := records.snapshot()
			if len(requests) != 1 || len(attempts) != 2 || attempts[0].Status != "error" || attempts[1].Status != "success" || attempts[0].SettlementID == attempts[1].SettlementID {
				t.Fatalf("incorrect request/attempt records: %+v %+v", requests, attempts)
			}
			// A second request during cooldown must bypass the unhealthy provider.
			if _, err := service.Native(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			if firstCalls.Load() != 1 || secondCalls.Load() != 2 {
				t.Fatalf("cooldown ignored: first=%d second=%d", firstCalls.Load(), secondCalls.Load())
			}
		})
	}
}

func TestNativeStreamFailoverBoundary(t *testing.T) {
	errorFrame := "event: error\ndata: {\"type\":\"error\",\"error\":{\"message\":\"sensitive upstream detail\"}}\n\n"
	startFrame := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"first-model\",\"usage\":{\"input_tokens\":10}}}\n\n"
	for _, tc := range []struct {
		name, wire    string
		callbackError bool
		wantFallback  bool
	}{
		{"error before output", errorFrame, false, true},
		{"error after output", startFrame + errorFrame, false, false},
		{"truncated stream", startFrame, false, false},
		{"client disconnect", startFrame, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, tc.wire)
			}))
			defer first.Close()
			var secondCalls atomic.Int32
			second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				secondCalls.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			}))
			defer second.Close()
			service := testService(first.URL, second.URL, first.Client())
			defer service.Close(context.Background())
			for _, provider := range service.store.(*fakeRouteStore).providers {
				provider.Type = model.ProviderAnthropic
			}
			var output strings.Builder
			disconnected := errors.New("client disconnected")
			err := service.NativeStream(context.Background(), &protocol.NativeRequest{API: protocol.MessagesAPI, Model: "chat", Stream: true, Raw: json.RawMessage(`{"model":"chat","stream":true}`)}, func(event protocol.NativeEvent) error {
				output.Write(event.Wire)
				if tc.callbackError {
					return disconnected
				}
				return nil
			})
			if (secondCalls.Load() == 1) != tc.wantFallback || (err == nil) != tc.wantFallback {
				t.Fatalf("fallback=%d err=%v", secondCalls.Load(), err)
			}
			if tc.callbackError && !errors.Is(err, disconnected) {
				t.Fatalf("callback error lost: %v", err)
			}
			if strings.Contains(output.String(), "sensitive") || strings.Contains(output.String(), "first-model") {
				t.Fatalf("upstream details leaked: %s", output.String())
			}
		})
	}
}

func TestMessagesNativeRoutePreservesThinkingAndConversionRejectsLocally(t *testing.T) {
	var openAICalls, anthropicCalls atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		openAICalls.Add(1)
		t.Error("unsupported native Messages request reached OpenAI")
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		anthropicCalls.Add(1)
		if r.URL.Path != "/v1/messages" || r.Header.Get("anthropic-version") == "" {
			t.Errorf("native Anthropic route: %s %v", r.URL.Path, r.Header)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || !strings.Contains(string(body), `"thinking":{"type":"enabled","budget_tokens":5}`) || !strings.Contains(string(body), `"model":"second-model"`) {
			t.Errorf("lost passthrough request: %s %v", body, err)
		}
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"second-model\",\"usage\":{\"input_tokens\":2}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\",\"signature\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"real thought\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"real signature\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_real\",\"name\":\"lookup\",\"input\":{}}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"q\\\":\\\"red\\\"}\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		} else {
			io.WriteString(w, `{"model":"second-model","content":[{"type":"thinking","thinking":"real thought","signature":"real signature"}],"usage":{"input_tokens":2,"output_tokens":3}}`)
		}
	}))
	defer second.Close()
	service := testService(first.URL, second.URL, first.Client())
	defer service.Close(context.Background())
	req := &protocol.NativeRequest{API: protocol.MessagesAPI, Model: "chat", Raw: json.RawMessage(`{"model":"chat","messages":[{"role":"user","content":"hi"}],"max_tokens":10,"thinking":{"type":"enabled","budget_tokens":5}}`)}
	_, err := service.Native(context.Background(), req)
	if !errors.Is(err, adapter.ErrInvalidRequest) || openAICalls.Load() != 0 || anthropicCalls.Load() != 0 {
		t.Fatalf("unconvertible request: err=%v OpenAI=%d Anthropic=%d", err, openAICalls.Load(), anthropicCalls.Load())
	}
	service.store.(*fakeRouteStore).providers[2].Type = model.ProviderAnthropic
	response, err := service.Native(context.Background(), req)
	if err != nil || response == nil || !strings.Contains(string(response.Raw), `"signature":"real signature"`) || !strings.Contains(string(response.Raw), `"model":"chat"`) {
		t.Fatalf("native passthrough=%v err=%v", response, err)
	}
	streamReq := *req
	streamReq.Stream = true
	streamReq.Raw = json.RawMessage(`{"model":"chat","messages":[{"role":"user","content":"hi"}],"max_tokens":10,"thinking":{"type":"enabled","budget_tokens":5},"stream":true}`)
	var wire strings.Builder
	if err := service.NativeStream(context.Background(), &streamReq, func(event protocol.NativeEvent) error { wire.Write(event.Wire); return nil }); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"thinking":"real thought"`, `"signature":"real signature"`, `"partial_json":"{\"q\":\"red\"}"`, `"model":"chat"`} {
		if !strings.Contains(wire.String(), want) {
			t.Fatalf("missing %s in %s", want, wire.String())
		}
	}
	if openAICalls.Load() != 0 || anthropicCalls.Load() != 2 || strings.Count(wire.String(), "event: message_stop") != 1 {
		t.Fatalf("calls=%d %d stream=%s", openAICalls.Load(), anthropicCalls.Load(), wire.String())
	}
}
