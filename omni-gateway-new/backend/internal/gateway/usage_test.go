package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zjvtit/omni-gateway/internal/background"
	"github.com/zjvtit/omni-gateway/internal/billing"
	"github.com/zjvtit/omni-gateway/internal/model"
	"github.com/zjvtit/omni-gateway/internal/protocol"
)

func TestTextUsageFallbackExcludesMediaAndMetadata(t *testing.T) {
	raw := json.RawMessage(`{"messages":[{"content":[{"type":"text","text":"hello"},{"type":"image","source":{"data":"base64-data"}}]}],"system":"test","metadata":{"text":"not input"}}`)
	if got := jsonTextBytes(raw); got != 9 {
		t.Fatalf("text bytes=%d", got)
	}
	u := estimateUsage(jsonTextBytes(raw), len("世界"))
	if !u.Estimated || u.Get("input") != 3 || u.Get("output") != 2 || u.TotalTokens() != 5 {
		t.Fatalf("unexpected fallback: %+v", u)
	}
	b, err := json.Marshal(u)
	if err != nil || !strings.Contains(string(b), `"source":"text_bytes_v1"`) {
		t.Fatalf("missing source: %s %v", b, err)
	}
	if estimateUsage(0, 0).Estimated {
		t.Fatal("invented usage for an empty payload")
	}
	if jsonTextBytes(json.RawMessage(`bad json`)) != 0 {
		t.Fatal("invalid JSON counted")
	}
	if got := nativeEventTextBytes(protocol.NativeEvent{Wire: []byte("data: {\"delta\":\n" + "data: {\"text\":\"hello\"}}\n\n")}); got != 5 {
		t.Fatalf("SSE text bytes=%d", got)
	}
}

func TestChatSettlementUsesFallbackOnlyWhenUsageMissing(t *testing.T) {
	for _, reported := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "reported"}[reported], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				usage := ""
				if reported {
					usage = `,"usage":{"prompt_tokens":8,"completion_tokens":3,"total_tokens":11}`
				}
				io.WriteString(w, `{"model":"real","choices":[{"message":{"role":"assistant","content":"world"},"finish_reason":"stop"}]`+usage+`}`)
			}))
			defer server.Close()
			service := testService(server.URL, server.URL, server.Client())
			defer service.Close(context.Background())
			settlement := &nativeSettlement{}
			service.settlement = settlement
			ctx := WithRequestMetadata(context.Background(), RequestMetadata{ID: "estimate", UserID: 1, APIKeyID: 1, ModelAlias: "chat"})
			_, err := service.Chat(ctx, &protocol.ChatRequest{Model: "chat", Messages: []protocol.Message{{Role: "user", Content: []protocol.ContentPart{{Type: "text", Text: "hello"}}}}})
			if err != nil {
				t.Fatal(err)
			}
			if len(settlement.usages) != 1 || settlement.usages[0].Estimated == reported {
				t.Fatalf("wrong usage: %+v", settlement.usages)
			}
			want := int64(4)
			if reported {
				want = 11
			}
			if settlement.usages[0].TotalTokens() != want {
				t.Fatalf("incorrect tokens: %+v", settlement.usages[0])
			}
		})
	}
}

func TestInterruptedStreamEstimatesObservedTextForCost(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"world\"}}]}\n\n")
	}))
	defer server.Close()
	service := testService(server.URL, server.URL, server.Client())
	defer service.Close(context.Background())
	settlement := &nativeSettlement{}
	service.settlement = settlement
	ctx := WithRequestMetadata(context.Background(), RequestMetadata{ID: "estimate-stream", ModelAlias: "chat", Streaming: true})
	err := service.ChatStream(ctx, &protocol.ChatRequest{Model: "chat", Messages: []protocol.Message{{Content: []protocol.ContentPart{{Text: "hello"}}}}}, func(protocol.StreamEvent) error { return nil })
	if err == nil || len(settlement.usages) != 1 || !settlement.usages[0].Estimated || settlement.usages[0].TotalTokens() != 4 || settlement.successes[0] {
		t.Fatalf("partial stream settlement: %+v err=%v", settlement, err)
	}
}

func TestInterruptedResponsesStreamEstimatesObservedToolArguments(t *testing.T) {
	// A stream that only produced function-call argument fragments has still
	// generated output, so the failure path must estimate it instead of
	// settling the attempt with no usage at all.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: response.function_call_arguments.delta\n")
		io.WriteString(w, "data: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"{\\\"q\\\":\\\"x\\\"}\"}\n\n")
	}))
	defer server.Close()
	service := testService(server.URL, server.URL, server.Client())
	defer service.Close(context.Background())
	settlement := &nativeSettlement{}
	service.settlement = settlement
	ctx := WithRequestMetadata(context.Background(), RequestMetadata{ID: "estimate-responses-tool", ModelAlias: "chat", Streaming: true})
	err := service.ResponsesStream(ctx, &protocol.ResponsesRequest{Model: "chat", Input: json.RawMessage(`"hello"`), Stream: true}, func(protocol.StreamEvent) error { return nil })
	if err == nil {
		t.Fatal("interrupted Responses stream should fail")
	}
	if len(settlement.usages) != 1 {
		t.Fatalf("settled %d attempts, want 1: %+v", len(settlement.usages), settlement)
	}
	usage := settlement.usages[0]
	if !usage.Estimated || usage.Get("input") != 2 || usage.Get("output") != 3 || usage.TotalTokens() != 5 {
		t.Fatalf("tool argument fragments were not estimated: %+v", usage)
	}
	if len(settlement.successes) != 1 || settlement.successes[0] {
		t.Fatalf("interrupted stream was recorded as a success: %+v", settlement.successes)
	}
}

func TestResponsesAndNativeFallbackText(t *testing.T) {
	u := estimateResponsesUsage(&protocol.ResponsesRequest{Input: json.RawMessage(`"hello"`), Instructions: "test"}, &protocol.ResponsesResponse{Output: json.RawMessage(`[{"content":[{"text":"world"}]}]`)})
	if u.Get("input") != 3 || u.Get("output") != 2 {
		t.Fatalf("Responses estimate: %+v", u)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"type":"message","content":[{"type":"text","text":"world"}]}`)
	}))
	defer server.Close()
	service := testService(server.URL, server.URL, server.Client())
	defer service.Close(context.Background())
	for _, p := range service.store.(*fakeRouteStore).providers {
		p.Type = model.ProviderAnthropic
	}
	settlement := &nativeSettlement{}
	service.settlement = settlement
	ctx := WithRequestMetadata(context.Background(), RequestMetadata{ID: "estimate-native", ModelAlias: "chat"})
	_, err := service.Native(ctx, &protocol.NativeRequest{API: protocol.MessagesAPI, Model: "chat", Raw: json.RawMessage(`{"messages":[{"role":"user","content":"hello"}]}`)})
	if err != nil || len(settlement.usages) != 1 || !settlement.usages[0].Estimated || settlement.usages[0].TotalTokens() != 4 {
		t.Fatalf("native usage: %+v %v", settlement, err)
	}
}

func TestFailedAttemptUsageRule(t *testing.T) {
	reported := model.Usage{Meters: map[string]int64{"input": 5, "output": 6, "total": 11}}
	cases := []struct {
		name          string
		observed      model.Usage
		input, output int
		wantTotal     int64
		wantEstimated bool
	}{
		{name: "reported usage wins", observed: reported, input: 400, output: 40, wantTotal: 11},
		{name: "observed partial output is estimated", observed: model.NewUsage(), input: 400, output: 40, wantTotal: 110, wantEstimated: true},
		{name: "neither reported nor observed usage", observed: model.NewUsage(), input: 400, output: 0, wantTotal: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := failedAttemptUsage(tc.observed, tc.input, tc.output)
			if got.TotalTokens() != tc.wantTotal || got.Estimated != tc.wantEstimated {
				t.Fatalf("failedAttemptUsage = %+v (total=%d estimated=%v), want total=%d estimated=%v", got, got.TotalTokens(), got.Estimated, tc.wantTotal, tc.wantEstimated)
			}
		})
	}
}

func TestChatFailureCarriesNoUsageWhenNothingWasObserved(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream exploded", http.StatusInternalServerError)
	}))
	defer server.Close()
	service := testService(server.URL, server.URL, server.Client())
	defer service.Close(context.Background())
	settlement := &nativeSettlement{}
	service.settlement = settlement
	ctx := WithRequestMetadata(context.Background(), RequestMetadata{ID: "failure-usage", UserID: 1, APIKeyID: 1, ModelAlias: "chat"})
	if _, err := service.Chat(ctx, &protocol.ChatRequest{Model: "chat", Messages: []protocol.Message{{Role: "user", Content: []protocol.ContentPart{{Type: "text", Text: "hello"}}}}}); err == nil {
		t.Fatal("expected the chat call to fail")
	}
	if len(settlement.usages) == 0 {
		t.Fatal("expected a settlement call for every failed attempt")
	}
	for i, usage := range settlement.usages {
		if usage.TotalTokens() != 0 || usage.Estimated || settlement.successes[i] {
			t.Fatalf("attempt %d must carry no usage: %+v success=%v", i, usage, settlement.successes[i])
		}
	}
}

func TestUnpricedSettlementIsMarkedAndCounted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"response-id","model":"provider-model","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
	}))
	defer server.Close()
	records := newFakeRecordStore()
	settlement := &fakeSettlement{result: &billing.Result{Unpriced: true}}
	service := NewServiceWithDependencies(&fakeRouteStore{
		aliases:   map[string]*model.ModelAlias{"alias": {ID: 2, Name: "alias", Active: true}},
		entries:   map[int64][]*model.RouteEntry{2: {{ID: 7, AliasID: 2, ProviderID: 9, ProviderModel: "provider-model", Priority: 4, Active: true}}},
		providers: map[int64]*model.Provider{9: {ID: 9, Type: model.ProviderOpenAICompat, BaseURL: server.URL, Active: true}},
	}, nil, server.Client(), records, settlement, nil)
	defer service.Close(context.Background())
	ctx := WithRequestMetadata(context.Background(), RequestMetadata{ID: "unpriced-req", UserID: 11, APIKeyID: 12, API: "chat_completions", ModelAlias: "alias"})
	if _, err := service.Chat(ctx, &protocol.ChatRequest{Model: "alias", Messages: []protocol.Message{{Role: "user"}}}); err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	select {
	case <-records.attemptCh:
	case <-time.After(2 * time.Second):
		t.Fatal("attempt record was not persisted")
	}
	_, attempts := records.snapshot()
	if len(attempts) != 1 {
		t.Fatalf("got %d attempts, want 1", len(attempts))
	}
	if attempts[0].SettlementError != "unpriced" {
		t.Fatalf("attempt settlement_error = %q, want %q", attempts[0].SettlementError, "unpriced")
	}
	if counters := service.RuntimeCounters(); counters.UnpricedSettlements != 1 {
		t.Fatalf("unpriced counter = %d, want 1", counters.UnpricedSettlements)
	}
}

func TestZeroCreditSettlementIsCounted(t *testing.T) {
	// A positive credit rate whose amount quantizes to 0.000 is still a pricing
	// hole, unlike an explicit zero rate: operators must be able to observe it.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"response-id","model":"provider-model","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
	}))
	defer server.Close()
	records := newFakeRecordStore()
	settlement := &fakeSettlement{result: &billing.Result{ZeroCreditMeters: 2}}
	service := NewServiceWithDependencies(&fakeRouteStore{
		aliases:   map[string]*model.ModelAlias{"alias": {ID: 2, Name: "alias", Active: true}},
		entries:   map[int64][]*model.RouteEntry{2: {{ID: 7, AliasID: 2, ProviderID: 9, ProviderModel: "provider-model", Priority: 4, Active: true}}},
		providers: map[int64]*model.Provider{9: {ID: 9, Type: model.ProviderOpenAICompat, BaseURL: server.URL, Active: true}},
	}, nil, server.Client(), records, settlement, nil)
	defer service.Close(context.Background())
	ctx := WithRequestMetadata(context.Background(), RequestMetadata{ID: "zero-credit-req", UserID: 11, APIKeyID: 12, API: "chat_completions", ModelAlias: "alias"})
	if _, err := service.Chat(ctx, &protocol.ChatRequest{Model: "alias", Messages: []protocol.Message{{Role: "user"}}}); err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	select {
	case <-records.attemptCh:
	case <-time.After(2 * time.Second):
		t.Fatal("attempt record was not persisted")
	}
	counters := service.RuntimeCounters()
	if counters.ZeroCreditSettlements != 1 {
		t.Fatalf("zero-credit counter = %d, want 1", counters.ZeroCreditSettlements)
	}
	if counters.UnpricedSettlements != 0 {
		t.Fatalf("unpriced counter = %d, want 0: the two holes are distinct", counters.UnpricedSettlements)
	}
}

type zeroRatePriceStore struct {
	price *model.PriceVersion
}

func (s *zeroRatePriceStore) GetLatestPriceVersion(context.Context, string) (*model.PriceVersion, error) {
	return s.price, nil
}

type zeroRateLedgerStore struct {
	mu      sync.Mutex
	entries []*model.LedgerEntry
	calls   int
	fail    bool
}

func (s *zeroRateLedgerStore) InsertLedgerEntries(_ context.Context, entries []*model.LedgerEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.fail {
		return errors.New("test ledger unavailable")
	}
	s.entries = append(s.entries, entries...)
	return nil
}

type zeroRateUsagePolicy struct {
	admissions       int
	usages           []model.Usage
	userID, apiKeyID int64
	alias            string
}

func (p *zeroRateUsagePolicy) Acquire(context.Context, int64, int64, string) (func(), error) {
	p.admissions++
	return func() {}, nil
}

func (p *zeroRateUsagePolicy) ObserveUsage(_ context.Context, userID, apiKeyID int64, alias string, usage model.Usage) {
	p.userID, p.apiKeyID, p.alias = userID, apiKeyID, alias
	p.usages = append(p.usages, usage)
}

func TestZeroRateCallsRecordUsageForStreamingReportedAndEstimatedResponses(t *testing.T) {
	for _, tc := range []struct {
		name, costRate string
		failLedger     bool
	}{
		{name: "free", costRate: "0"},
		{name: "cost only", costRate: "0.01"},
		{name: "free persistence failure", costRate: "0", failLedger: true},
	} {
		for _, streaming := range []bool{false, true} {
			for _, reported := range []bool{false, true} {
				name := tc.name + "/" + map[bool]string{false: "nonstream", true: "stream"}[streaming] + "/" + map[bool]string{false: "estimated", true: "reported"}[reported]
				t.Run(name, func(t *testing.T) {
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						time.Sleep(5 * time.Millisecond)
						usage := ""
						if reported {
							usage = `,"usage":{"prompt_tokens":8,"completion_tokens":3,"total_tokens":11}`
						}
						if streaming {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"world\"}}]}\n\n")
							_, _ = io.WriteString(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}]`+usage+"}\n\ndata: [DONE]\n\n")
							return
						}
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, `{"model":"provider-model","choices":[{"message":{"role":"assistant","content":"world"},"finish_reason":"stop"}]`+usage+`}`)
					}))
					defer server.Close()
					ledgers := &zeroRateLedgerStore{fail: tc.failLedger}
					settlement := billing.NewService(&zeroRatePriceStore{price: &model.PriceVersion{Version: 7,
						Cost:    map[string]model.Money{"input": model.MustMoney(tc.costRate), "output": model.MustMoney(tc.costRate)},
						Credits: map[string]model.Credits{"input": model.ZeroCredits, "output": model.ZeroCredits},
					}}, ledgers)
					t.Cleanup(func() {
						err := settlement.Close(context.Background())
						if (tc.failLedger && !errors.Is(err, background.ErrFailed)) || (!tc.failLedger && err != nil) {
							t.Errorf("settlement close=%v", err)
						}
					})
					records := newFakeRecordStore()
					usagePolicy := &zeroRateUsagePolicy{}
					service := NewServiceWithDependencies(&fakeRouteStore{
						aliases:   map[string]*model.ModelAlias{"alias": {ID: 2, Name: "alias", Active: true}},
						entries:   map[int64][]*model.RouteEntry{2: {{ID: 7, AliasID: 2, ProviderID: 9, ProviderModel: "provider-model", Priority: 4, Active: true}}},
						providers: map[int64]*model.Provider{9: {ID: 9, Type: model.ProviderOpenAICompat, BaseURL: server.URL, Active: true}},
					}, nil, server.Client(), records, settlement, usagePolicy)
					defer service.Close(context.Background())
					ctx := WithRequestMetadata(context.Background(), RequestMetadata{ID: "zero-rate-req", UserID: 11, APIKeyID: 12, API: "chat_completions", ModelAlias: "alias", Streaming: streaming})
					request := &protocol.ChatRequest{Model: "alias", Messages: []protocol.Message{{Role: "user", Content: []protocol.ContentPart{{Type: "text", Text: "hello"}}}}}
					var err error
					if streaming {
						var output string
						err = service.ChatStream(ctx, request, func(event protocol.StreamEvent) error {
							for _, part := range event.Delta.Content {
								output += part.Text
							}
							return nil
						})
						if output != "world" {
							t.Fatalf("stream output=%q", output)
						}
					} else {
						var response *protocol.ChatResponse
						response, err = service.Chat(ctx, request)
						if response == nil || response.Model != "alias" {
							t.Fatalf("response=%+v err=%v", response, err)
						}
					}
					if err != nil {
						t.Fatal(err)
					}
					if err := service.Close(context.Background()); err != nil {
						t.Fatal(err)
					}
					requests, attempts := records.snapshot()
					if len(requests) != 1 || len(attempts) != 1 {
						t.Fatalf("records: requests=%d attempts=%d", len(requests), len(attempts))
					}
					wantInput, wantOutput, wantTotal := int64(2), int64(2), int64(4)
					if reported {
						wantInput, wantOutput, wantTotal = 8, 3, 11
					}
					r, a := requests[0], attempts[0]
					wantCost := model.MustMoney(tc.costRate).Mul(model.NewMoneyInt(wantTotal))
					if r.ID != "zero-rate-req" || r.UserID != 11 || r.APIKeyID != 12 || r.ModelAlias != "alias" || r.API != "chat_completions" || r.Streaming != streaming || r.Status != model.StatusSuccess || r.StatusCode != http.StatusOK || r.CreatedAt.IsZero() || r.TotalLatencyMS <= 0 || r.UpstreamLatencyMS <= 0 || !r.Cost.Equal(wantCost) || !r.Credits.IsZero() {
						t.Fatalf("request context or totals lost: %+v", r)
					}
					if a.RequestID != r.ID || a.SettlementID == "" || a.ProviderID != 9 || a.ProviderModel != "provider-model" || a.Priority != 4 || a.Status != "success" || a.StatusCode != http.StatusOK || a.CreatedAt.IsZero() || a.LatencyMS <= 0 || !a.Cost.Equal(wantCost) {
						t.Fatalf("attempt context or totals lost: %+v", a)
					}
					wantSettlementError := ""
					if tc.failLedger {
						wantSettlementError = "settlement_failed"
					}
					if a.SettlementError != wantSettlementError {
						t.Fatalf("settlement error=%q, want %q", a.SettlementError, wantSettlementError)
					}
					if usagePolicy.admissions != 1 || len(usagePolicy.usages) != 1 || usagePolicy.userID != 11 || usagePolicy.apiKeyID != 12 || usagePolicy.alias != "alias" {
						t.Fatalf("policy was bypassed: %+v", usagePolicy)
					}
					for _, usage := range []model.Usage{r.Usage, a.Usage, usagePolicy.usages[0]} {
						if usage.Get("input") != wantInput || usage.Get("output") != wantOutput || usage.TotalTokens() != wantTotal || usage.Estimated == reported {
							t.Fatalf("usage lost or counted twice: %+v", usage)
						}
					}
					if counters := service.RuntimeCounters(); counters.UnpricedSettlements != 0 || counters.ZeroCreditSettlements != 0 {
						t.Fatalf("explicit free rates misdiagnosed: %+v", counters)
					}
					ledgers.mu.Lock()
					defer ledgers.mu.Unlock()
					if tc.failLedger {
						if ledgers.calls < 2 || len(ledgers.entries) != 0 || settlement.PersistenceStats().Failed != 1 {
							t.Fatalf("zero-amount failure hidden: writes=%d rows=%d stats=%+v", ledgers.calls, len(ledgers.entries), settlement.PersistenceStats())
						}
						return
					}
					if ledgers.calls != 1 || len(ledgers.entries) != 3 {
						t.Fatalf("zero-amount ledger was skipped: writes=%d rows=%d", ledgers.calls, len(ledgers.entries))
					}
					for _, entry := range ledgers.entries {
						if entry.RequestID != r.ID || entry.SettlementID != a.SettlementID || entry.PriceVersion != 7 || entry.Quantity != a.Usage.Get(entry.Meter) || entry.Quantity <= 0 || entry.Estimated == reported || !entry.Credits.IsZero() {
							t.Fatalf("ledger context or usage lost: %+v", entry)
						}
						if tc.costRate == "0" && !entry.Cost.IsZero() {
							t.Fatalf("free meter charged cost: %+v", entry)
						}
					}
				})
			}
		}
	}
}
