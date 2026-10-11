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
	"sync/atomic"
	"testing"
	"time"

	"github.com/zjvtit/omni-gateway/internal/adapter"
	"github.com/zjvtit/omni-gateway/internal/billing"
	gatewaycrypto "github.com/zjvtit/omni-gateway/internal/crypto"
	"github.com/zjvtit/omni-gateway/internal/model"
	"github.com/zjvtit/omni-gateway/internal/protocol"
)

type fakeRouteStore struct {
	aliases   map[string]*model.ModelAlias
	entries   map[int64][]*model.RouteEntry
	providers map[int64]*model.Provider
}

func (s *fakeRouteStore) GetAlias(_ context.Context, name string) (*model.ModelAlias, error) {
	alias, ok := s.aliases[name]
	if !ok {
		return nil, ErrModelNotFound
	}
	return alias, nil
}

func (s *fakeRouteStore) ListAliases(context.Context) ([]*model.ModelAlias, error) {
	aliases := make([]*model.ModelAlias, 0, len(s.aliases))
	for _, alias := range s.aliases {
		aliases = append(aliases, alias)
	}
	return aliases, nil
}

func (s *fakeRouteStore) ListRouteEntries(_ context.Context, aliasID int64) ([]*model.RouteEntry, error) {
	return append([]*model.RouteEntry(nil), s.entries[aliasID]...), nil
}

func (s *fakeRouteStore) GetProvider(_ context.Context, id int64) (*model.Provider, error) {
	provider, ok := s.providers[id]
	if !ok {
		return nil, ErrModelNotFound
	}
	return provider, nil
}

func TestServiceListModelsFiltersInactiveAndSorts(t *testing.T) {
	service := NewService(&fakeRouteStore{aliases: map[string]*model.ModelAlias{
		"zeta":  {Name: "zeta", Active: true},
		"alpha": {Name: "alpha", Active: true},
		"off":   {Name: "off", Active: false},
	}}, nil, nil)

	models, err := service.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels returned error: %v", err)
	}
	want := []string{"alpha", "zeta"}
	if len(models) != len(want) || models[0] != want[0] || models[1] != want[1] {
		t.Fatalf("got models %#v, want %#v", models, want)
	}
}

func TestServiceChatFailsOverOnRetryableHTTPError(t *testing.T) {
	var firstCalls, secondCalls atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstCalls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"error":"temporary"}`)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondCalls.Add(1)
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"upstream-2","model":"provider-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer second.Close()

	service := NewService(&fakeRouteStore{
		aliases: map[string]*model.ModelAlias{"chat": {ID: 1, Name: "chat", Active: true}},
		entries: map[int64][]*model.RouteEntry{1: {
			{ID: 2, AliasID: 1, ProviderID: 2, ProviderModel: "provider-model-2", Priority: 20, Active: true},
			{ID: 1, AliasID: 1, ProviderID: 1, ProviderModel: "provider-model-1", Priority: 10, Active: true},
		}},
		providers: map[int64]*model.Provider{
			1: {ID: 1, Name: "first", Type: model.ProviderOpenAICompat, BaseURL: first.URL, Active: true},
			2: {ID: 2, Name: "second", Type: model.ProviderOpenAICompat, BaseURL: second.URL, Active: true},
		},
	}, nil, first.Client())

	response, err := service.Chat(context.Background(), &protocol.ChatRequest{
		Model:    "chat",
		Messages: []protocol.Message{{Role: "user", Content: []protocol.ContentPart{{Type: "text", Text: "hello"}}}},
	})
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	if response.Model != "chat" || response.ID != "upstream-2" || response.Choices[0].Message.Content[0].Text != "ok" {
		t.Fatalf("unexpected response: %#v", response)
	}
	if firstCalls.Load() != 1 || secondCalls.Load() != 1 {
		t.Fatalf("unexpected route calls: first=%d second=%d", firstCalls.Load(), secondCalls.Load())
	}
}

func TestServiceChatDoesNotFailOverOnAuthenticationError(t *testing.T) {
	var secondCalls atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer second.Close()

	service := testService(first.URL, second.URL, first.Client())
	_, err := service.Chat(context.Background(), &protocol.ChatRequest{Model: "chat", Messages: []protocol.Message{{Role: "user"}}})
	if err == nil {
		t.Fatal("Chat returned nil error for upstream 401")
	}
	if secondCalls.Load() != 0 {
		t.Fatal("service failed over after an authentication error")
	}
}

func TestServiceChatStreamFailsOverBeforeFirstEvent(t *testing.T) {
	var secondCalls atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer second.Close()

	service := testService(first.URL, second.URL, first.Client())
	var events []protocol.StreamEvent
	err := service.ChatStream(context.Background(), &protocol.ChatRequest{Model: "chat", Messages: []protocol.Message{{Role: "user"}}}, func(event protocol.StreamEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("ChatStream returned error: %v", err)
	}
	if secondCalls.Load() != 1 || len(events) != 2 || events[0].Delta.Content[0].Text != "ok" {
		t.Fatalf("unexpected failover stream: calls=%d events=%#v", secondCalls.Load(), events)
	}
}

func TestServiceChatStreamDoesNotFailOverAfterEmission(t *testing.T) {
	var secondCalls atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\ndata: {broken}\n\n")
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer second.Close()

	service := testService(first.URL, second.URL, first.Client())
	var events []protocol.StreamEvent
	err := service.ChatStream(context.Background(), &protocol.ChatRequest{Model: "chat", Messages: []protocol.Message{{Role: "user"}}}, func(event protocol.StreamEvent) error {
		events = append(events, event)
		return nil
	})
	if err == nil {
		t.Fatal("ChatStream accepted malformed data after partial emission")
	}
	if secondCalls.Load() != 0 || len(events) != 1 || events[0].Delta.Content[0].Text != "partial" {
		t.Fatalf("service incorrectly failed over after emission: calls=%d events=%#v", secondCalls.Load(), events)
	}
}

func TestServiceReturnsNoRouteError(t *testing.T) {
	service := NewService(&fakeRouteStore{
		aliases:   map[string]*model.ModelAlias{"chat": {ID: 1, Name: "chat", Active: true}},
		entries:   map[int64][]*model.RouteEntry{1: {{ProviderModel: "", Active: true}}},
		providers: map[int64]*model.Provider{},
	}, nil, nil)
	_, err := service.Chat(context.Background(), &protocol.ChatRequest{Model: "chat"})
	if !errors.Is(err, ErrNoRoute) {
		t.Fatalf("got error %v, want ErrNoRoute", err)
	}
}

func testService(firstURL, secondURL string, client *http.Client) *Service {
	return NewService(&fakeRouteStore{
		aliases: map[string]*model.ModelAlias{"chat": {ID: 1, Name: "chat", Active: true}},
		entries: map[int64][]*model.RouteEntry{1: {
			{ID: 1, AliasID: 1, ProviderID: 1, ProviderModel: "first-model", Priority: 10, Active: true},
			{ID: 2, AliasID: 1, ProviderID: 2, ProviderModel: "second-model", Priority: 20, Active: true},
		}},
		providers: map[int64]*model.Provider{
			1: {ID: 1, Name: "first", Type: model.ProviderOpenAICompat, BaseURL: firstURL, Active: true, SupportsResponses: true},
			2: {ID: 2, Name: "second", Type: model.ProviderOpenAICompat, BaseURL: secondURL, Active: true, SupportsResponses: true},
		},
	}, nil, client)
}

func TestServiceRequestPayloadUsesUpstreamModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("invalid JSON: %v", err)
		}
		if payload["model"] != "provider-model" {
			t.Errorf("got upstream model %#v", payload["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"id","model":"provider-model","choices":[]}`)
	}))
	defer server.Close()
	service := NewService(&fakeRouteStore{
		aliases:   map[string]*model.ModelAlias{"alias": {ID: 2, Name: "alias", Active: true}},
		entries:   map[int64][]*model.RouteEntry{2: {{ProviderID: 1, ProviderModel: "provider-model", Active: true}}},
		providers: map[int64]*model.Provider{1: {ID: 1, Type: model.ProviderOpenAICompat, BaseURL: server.URL, Active: true}},
	}, nil, server.Client())
	response, err := service.Chat(context.Background(), &protocol.ChatRequest{Model: "alias", Messages: []protocol.Message{{Role: "user"}}})
	if err != nil || response.Model != "alias" {
		t.Fatalf("unexpected result: response=%#v err=%v", response, err)
	}
}

type fakeSettlement struct {
	result *billing.Result
	called int32
}

func (s *fakeSettlement) Settle(context.Context, string, int64, int64, string, string, int64, model.Usage, bool) (*billing.Result, error) {
	atomic.AddInt32(&s.called, 1)
	return s.result, nil
}

type fakeRecordStore struct {
	mu        sync.Mutex
	requests  []*model.Request
	attempts  []*model.Attempt
	content   []*model.ContentLog
	requestCh chan struct{}
	attemptCh chan struct{}
	contentCh chan struct{}
}

func newFakeRecordStore() *fakeRecordStore {
	return &fakeRecordStore{requestCh: make(chan struct{}, 1), attemptCh: make(chan struct{}, 4), contentCh: make(chan struct{}, 1)}
}

func (s *fakeRecordStore) InsertRequest(_ context.Context, request *model.Request) error {
	s.mu.Lock()
	s.requests = append(s.requests, request)
	s.mu.Unlock()
	s.requestCh <- struct{}{}
	return nil
}

func (s *fakeRecordStore) InsertAttempt(_ context.Context, attempt *model.Attempt) error {
	s.mu.Lock()
	s.attempts = append(s.attempts, attempt)
	s.mu.Unlock()
	s.attemptCh <- struct{}{}
	return nil
}

func (s *fakeRecordStore) InsertContentLog(_ context.Context, content *model.ContentLog) error {
	s.mu.Lock()
	s.content = append(s.content, content)
	s.mu.Unlock()
	s.contentCh <- struct{}{}
	return nil
}

func (s *fakeRecordStore) snapshot() (requests []*model.Request, attempts []*model.Attempt) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*model.Request(nil), s.requests...), append([]*model.Attempt(nil), s.attempts...)
}

func (s *fakeRecordStore) contentSnapshot() []*model.ContentLog {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*model.ContentLog(nil), s.content...)
}

func TestServiceRecordsRequestAndAttempts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"response-id","model":"provider-model","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
	}))
	defer server.Close()
	records := newFakeRecordStore()
	settlement := &fakeSettlement{result: &billing.Result{Cost: model.MustMoney("1.25"), Credits: model.MustMoney("2.5")}}
	service := NewServiceWithDependencies(&fakeRouteStore{
		aliases:   map[string]*model.ModelAlias{"alias": {ID: 2, Name: "alias", Active: true}},
		entries:   map[int64][]*model.RouteEntry{2: {{ID: 7, AliasID: 2, ProviderID: 9, ProviderModel: "provider-model", Priority: 4, Active: true}}},
		providers: map[int64]*model.Provider{9: {ID: 9, Type: model.ProviderOpenAICompat, BaseURL: server.URL, Active: true}},
	}, nil, server.Client(), records, settlement, nil)
	cipher, err := gatewaycrypto.NewCipher(strings.Repeat("c", 64))
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	service.SetContentLogging(records, cipher, 4096, true)
	ctx := WithRequestMetadata(context.Background(), RequestMetadata{ID: "req-recorded", UserID: 11, APIKeyID: 12, API: "chat_completions", ModelAlias: "alias"})
	response, err := service.Chat(ctx, &protocol.ChatRequest{Model: "alias", Messages: []protocol.Message{{Role: "user"}}})
	if err != nil || response == nil {
		t.Fatalf("Chat failed: response=%#v err=%v", response, err)
	}
	select {
	case <-records.requestCh:
	case <-time.After(2 * time.Second):
		t.Fatal("request record was not persisted")
	}
	select {
	case <-records.attemptCh:
	case <-time.After(2 * time.Second):
		t.Fatal("attempt record was not persisted")
	}
	select {
	case <-records.contentCh:
	case <-time.After(2 * time.Second):
		t.Fatal("content record was not persisted")
	}
	requests, attempts := records.snapshot()
	if len(requests) != 1 || len(attempts) != 1 {
		t.Fatalf("unexpected records: requests=%d attempts=%d", len(requests), len(attempts))
	}
	if requests[0].ID != "req-recorded" || requests[0].UserID != 11 || requests[0].APIKeyID != 12 || requests[0].Status != model.StatusSuccess || requests[0].StatusCode != http.StatusOK || requests[0].ModelAlias != "alias" || requests[0].Usage.Get("input") != 3 || requests[0].Usage.Get("output") != 2 || requests[0].Cost.String() != "1.25" || requests[0].Credits.String() != "2.5" {
		t.Fatalf("unexpected request record: %#v", requests[0])
	}
	if attempts[0].RequestID != "req-recorded" || attempts[0].ProviderID != 9 || attempts[0].ProviderModel != "provider-model" || attempts[0].Priority != 4 || attempts[0].Status != "success" || attempts[0].Usage.Get("total") != 5 || attempts[0].Cost.String() != "1.25" {
		t.Fatalf("unexpected attempt record: %#v", attempts[0])
	}
	contents := records.contentSnapshot()
	if len(contents) != 1 {
		t.Fatalf("unexpected content records: %d", len(contents))
	}
	if prompt, decryptErr := cipher.Decrypt(string(contents[0].Prompt)); decryptErr != nil || !strings.Contains(prompt, "alias") {
		t.Fatalf("unexpected encrypted prompt: %q %v", prompt, decryptErr)
	}
	if atomic.LoadInt32(&settlement.called) != 1 {
		t.Fatalf("settlement called %d times, want 1", settlement.called)
	}
}

func TestRouteResolutionFailureFallsThroughToNextRoute(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"response-id","model":"second-model","choices":[]}`)
	}))
	defer server.Close()
	service := NewService(&fakeRouteStore{
		aliases: map[string]*model.ModelAlias{"chat": {ID: 1, Name: "chat", Active: true}},
		entries: map[int64][]*model.RouteEntry{1: {
			{ID: 1, AliasID: 1, ProviderID: 1, ProviderModel: "broken-model", Priority: 10, Active: true},
			{ID: 2, AliasID: 1, ProviderID: 2, ProviderModel: "second-model", Priority: 20, Active: true},
		}},
		providers: map[int64]*model.Provider{
			1: {ID: 1, Name: "broken", Type: model.ProviderType("bogus"), BaseURL: server.URL, Active: true},
			2: {ID: 2, Name: "second", Type: model.ProviderOpenAICompat, BaseURL: server.URL, Active: true},
		},
	}, nil, server.Client())
	defer service.Close(context.Background())

	response, err := service.Chat(context.Background(), &protocol.ChatRequest{Model: "chat", Messages: []protocol.Message{{Role: "user"}}})
	if err != nil || response == nil {
		t.Fatalf("expected the healthy route to serve the request: response=%#v err=%v", response, err)
	}
	if counters := service.RuntimeCounters(); counters.RouteResolveFailures != 1 {
		t.Fatalf("route_resolve_failed = %d, want 1", counters.RouteResolveFailures)
	}
}

func TestRouteResolutionFailureReturnsErrorWhenNoRouteResolves(t *testing.T) {
	service := NewService(&fakeRouteStore{
		aliases: map[string]*model.ModelAlias{"chat": {ID: 1, Name: "chat", Active: true}},
		entries: map[int64][]*model.RouteEntry{1: {
			{ID: 1, AliasID: 1, ProviderID: 1, ProviderModel: "broken-model", Priority: 10, Active: true},
		}},
		providers: map[int64]*model.Provider{1: {ID: 1, Name: "broken", Type: model.ProviderType("bogus"), Active: true}},
	}, nil, nil)
	defer service.Close(context.Background())

	_, err := service.Chat(context.Background(), &protocol.ChatRequest{Model: "chat", Messages: []protocol.Message{{Role: "user"}}})
	if err == nil || errors.Is(err, ErrNoRoute) || !strings.Contains(err.Error(), "unsupported adapter type") {
		t.Fatalf("expected the resolution error, got %v", err)
	}
	if counters := service.RuntimeCounters(); counters.RouteResolveFailures != 1 {
		t.Fatalf("route_resolve_failed = %d, want 1", counters.RouteResolveFailures)
	}
}

func TestRouteResolutionFailureStillReportsNoRouteWithoutBrokenConfig(t *testing.T) {
	service := NewService(&fakeRouteStore{
		aliases:   map[string]*model.ModelAlias{"chat": {ID: 1, Name: "chat", Active: true}},
		entries:   map[int64][]*model.RouteEntry{1: {{ID: 1, AliasID: 1, ProviderID: 1, ProviderModel: "", Priority: 10, Active: true}}},
		providers: map[int64]*model.Provider{},
	}, nil, nil)
	defer service.Close(context.Background())

	_, err := service.Chat(context.Background(), &protocol.ChatRequest{Model: "chat", Messages: []protocol.Message{{Role: "user"}}})
	if !errors.Is(err, ErrNoRoute) {
		t.Fatalf("got %v, want ErrNoRoute", err)
	}
	if counters := service.RuntimeCounters(); counters.RouteResolveFailures != 0 {
		t.Fatalf("route_resolve_failed = %d, want 0", counters.RouteResolveFailures)
	}
}

func TestRuntimeCountersExposeTheDocumentedKeys(t *testing.T) {
	// The admin runtime endpoint marshals this struct as JSON, so these key names
	// are the contract the console and operators read. Renaming one would drop a
	// pricing signal without any compile error anywhere.
	body, err := json.Marshal(RuntimeCounters{UnpricedSettlements: 1, ZeroCreditSettlements: 2})
	if err != nil {
		t.Fatalf("marshal runtime counters: %v", err)
	}
	for _, want := range []string{`"unpriced_settlements":1`, `"zero_credit_settlements":2`, `"route_resolve_failed":0`} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("runtime counters JSON %s is missing %s", body, want)
		}
	}
}

func TestServiceResponsesRequiresProviderOptIn(t *testing.T) {
	var upstreamCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"response-id","output":[]}`)
	}))
	defer server.Close()
	service := NewService(&fakeRouteStore{
		aliases:   map[string]*model.ModelAlias{"chat": {ID: 1, Name: "chat", Active: true}},
		entries:   map[int64][]*model.RouteEntry{1: {{ID: 1, AliasID: 1, ProviderID: 1, ProviderModel: "provider-model", Priority: 10, Active: true}}},
		providers: map[int64]*model.Provider{1: {ID: 1, Name: "no-responses", Type: model.ProviderOpenAICompat, BaseURL: server.URL, Active: true}},
	}, nil, server.Client())
	defer service.Close(context.Background())

	response, err := service.Responses(context.Background(), &protocol.ResponsesRequest{Model: "chat", Input: json.RawMessage(`"hello"`)})
	if response != nil || !errors.Is(err, ErrResponsesUnsupported) || !errors.Is(err, adapter.ErrUnsupported) {
		t.Fatalf("Responses = %#v, %v; want an explicit unsupported refusal", response, err)
	}
	err = service.ResponsesStream(context.Background(), &protocol.ResponsesRequest{Model: "chat", Input: json.RawMessage(`"hello"`), Stream: true}, func(protocol.StreamEvent) error { return nil })
	if !errors.Is(err, ErrResponsesUnsupported) || !errors.Is(err, adapter.ErrUnsupported) {
		t.Fatalf("ResponsesStream = %v; want an explicit unsupported refusal", err)
	}
	if calls := upstreamCalls.Load(); calls != 0 {
		t.Fatalf("the mock upstream received %d requests; an unsupported provider must be refused before any call", calls)
	}
}

// An alias may list both opted-in and legacy providers. The lower-priority
// provider here has no opt-in, so both entry points must skip it and serve
// from the provider that declared Responses support.
func TestServiceResponsesSkipsProvidersWithoutOptIn(t *testing.T) {
	var skippedCalls, optedInCalls atomic.Int32
	skipped := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		skippedCalls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer skipped.Close()
	optedIn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		optedInCalls.Add(1)
		if r.URL.Path != "/responses" {
			t.Errorf("unexpected upstream path %q", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("invalid upstream payload: %v", err)
		} else if payload["model"] != "opted-in-model" {
			t.Errorf("upstream model = %#v, want the opted-in route's model", payload["model"])
		}
		if stream, _ := payload["stream"].(bool); stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"from-opted-in\"}\n\n")
			_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"opted-in-response\"}}\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"opted-in-response","model":"opted-in-model","output":[{"type":"message","content":[{"type":"text","text":"from-opted-in"}]}]}`)
	}))
	defer optedIn.Close()

	service := NewService(&fakeRouteStore{
		aliases: map[string]*model.ModelAlias{"chat": {ID: 1, Name: "chat", Active: true}},
		entries: map[int64][]*model.RouteEntry{1: {
			{ID: 1, AliasID: 1, ProviderID: 1, ProviderModel: "legacy-model", Priority: 10, Active: true},
			{ID: 2, AliasID: 1, ProviderID: 2, ProviderModel: "opted-in-model", Priority: 20, Active: true},
		}},
		providers: map[int64]*model.Provider{
			1: {ID: 1, Name: "without-opt-in", Type: model.ProviderOpenAICompat, BaseURL: skipped.URL, Active: true},
			2: {ID: 2, Name: "with-opt-in", Type: model.ProviderOpenAICompat, BaseURL: optedIn.URL, Active: true, SupportsResponses: true},
		},
	}, nil, optedIn.Client())
	defer service.Close(context.Background())

	response, err := service.Responses(context.Background(), &protocol.ResponsesRequest{Model: "chat", Input: json.RawMessage(`"hello"`)})
	if err != nil || response == nil {
		t.Fatalf("Responses failed: response=%#v err=%v", response, err)
	}
	if response.ID != "opted-in-response" || !strings.Contains(string(response.Output), "from-opted-in") {
		t.Fatalf("unexpected Responses payload: %#v", response)
	}

	var events []protocol.StreamEvent
	err = service.ResponsesStream(context.Background(), &protocol.ResponsesRequest{Model: "chat", Input: json.RawMessage(`"hello"`), Stream: true}, func(event protocol.StreamEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("ResponsesStream failed: %v", err)
	}
	sawText := false
	for _, event := range events {
		if len(event.Delta.Content) > 0 && event.Delta.Content[0].Text == "from-opted-in" {
			sawText = true
		}
	}
	if !sawText {
		t.Fatalf("streamed events did not carry the opted-in provider output: %#v", events)
	}

	if calls := skippedCalls.Load(); calls != 0 {
		t.Fatalf("the provider without the opt-in was called %d times, want 0", calls)
	}
	if calls := optedInCalls.Load(); calls != 2 {
		t.Fatalf("the opted-in provider handled %d requests, want 2 (one non-stream, one stream)", calls)
	}
}
