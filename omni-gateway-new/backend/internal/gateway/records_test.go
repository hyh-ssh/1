package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zjvtit/omni-gateway/internal/adapter"
	"github.com/zjvtit/omni-gateway/internal/model"
	"github.com/zjvtit/omni-gateway/internal/policy"
	"github.com/zjvtit/omni-gateway/internal/protocol"
)

// An operation a provider cannot serve is reported to the client as 501
// operation_unsupported, so the persisted request must record the same verdict
// rather than degrading to a generic 502 "gateway request failed".
func TestRecordErrorClassifiesUnsupportedOperation(t *testing.T) {
	for _, tc := range []struct {
		name        string
		err         error
		wantType    string
		wantStatus  int
		wantSummary string // empty means only a non-empty summary is required
	}{
		{"nil", nil, "", 0, ""},
		{"sentinel", adapter.ErrUnsupported, "unsupported", http.StatusNotImplemented, ""},
		{
			"wrapped",
			fmt.Errorf("Messages field %q requires an Anthropic route: %w", "system", adapter.ErrUnsupported),
			"unsupported",
			http.StatusNotImplemented,
			"",
		},
		{
			// The Responses opt-in refusal wraps adapter.ErrUnsupported, so it must
			// keep its own summary: falling through to the generic branch would
			// still return 501 but lose the reason the client is shown.
			"responses opt-in",
			ErrResponsesUnsupported,
			"unsupported",
			http.StatusNotImplemented,
			"provider does not implement the OpenAI Responses API",
		},
		{"rate limit", policy.ErrRateLimited, "rate_limit", http.StatusTooManyRequests, ""},
		{"budget", policy.ErrBudgetExceeded, "budget", http.StatusTooManyRequests, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			errorType, summary, status := recordError(tc.err)
			if errorType != tc.wantType || status != tc.wantStatus {
				t.Fatalf("recordError(%v) = (%q, %q, %d), want type %q status %d", tc.err, errorType, summary, status, tc.wantType, tc.wantStatus)
			}
			if tc.wantSummary != "" && summary != tc.wantSummary {
				t.Fatalf("recordError(%v) summary = %q, want %q", tc.err, summary, tc.wantSummary)
			}
			if tc.wantType != "" && summary == "" {
				t.Fatalf("recordError(%v) returned an empty summary", tc.err)
			}
		})
	}
}

// End-to-end shape of the defect: an alias whose only routes are Anthropic
// cannot serve /v1/embeddings. The upstream is never contacted, no attempt row
// is written, and the request record must carry 501 instead of 502.
func TestUnsupportedOperationRecordsNotImplemented(t *testing.T) {
	var upstreamCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()

	service := testService(upstream.URL, upstream.URL, upstream.Client())
	for _, provider := range service.store.(*fakeRouteStore).providers {
		provider.Type = model.ProviderAnthropic
	}
	records := newFakeRecordStore()
	service.recordStore, service.settlement = records, &nativeSettlement{}

	ctx := WithRequestMetadata(context.Background(), RequestMetadata{
		ID: "unsupported-fixture", UserID: 1, APIKeyID: 2, ModelAlias: "chat", API: string(protocol.EmbeddingsAPI),
	})
	_, err := service.Native(ctx, &protocol.NativeRequest{
		API:   protocol.EmbeddingsAPI,
		Model: "chat",
		Raw:   json.RawMessage(`{"model":"chat","input":"hello"}`),
	})
	if !errors.Is(err, adapter.ErrUnsupported) {
		t.Fatalf("Native returned %v, want ErrUnsupported", err)
	}

	closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := service.Close(closeCtx); err != nil {
		t.Fatal(err)
	}

	requests, attempts := records.snapshot()
	if len(requests) != 1 {
		t.Fatalf("recorded %d requests, want 1", len(requests))
	}
	recorded := requests[0]
	if recorded.Status != model.StatusError {
		t.Fatalf("request status = %q, want %q", recorded.Status, model.StatusError)
	}
	if recorded.StatusCode != http.StatusNotImplemented {
		t.Fatalf("request status code = %d, want %d", recorded.StatusCode, http.StatusNotImplemented)
	}
	if recorded.ErrorType != "unsupported" || recorded.ErrorSummary == "" {
		t.Fatalf("request error = (%q, %q), want type %q with a summary", recorded.ErrorType, recorded.ErrorSummary, "unsupported")
	}
	if len(attempts) != 0 {
		t.Fatalf("recorded %d attempts, want 0: an unsupported operation never reaches an upstream", len(attempts))
	}
	if upstreamCalls != 0 {
		t.Fatalf("upstream was called %d times, want 0", upstreamCalls)
	}
	if !recorded.Credits.IsZero() {
		t.Fatalf("request credits = %v, want zero", recorded.Credits)
	}
}

// A provider without the Responses opt-in is refused before any upstream call
// or attempt, so the persisted request row must agree with the 501 the caller
// received instead of degrading to a generic 502. Both service entry points are
// exercised because each filters the routes itself; every subtest gets its own
// record store so the assertions describe exactly one persisted request.
func TestResponsesOptInRecordsNotImplemented(t *testing.T) {
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()

	for _, tc := range []struct {
		name      string
		streaming bool
		call      func(t *testing.T, service *Service, ctx context.Context)
	}{
		{
			name: "responses",
			call: func(t *testing.T, service *Service, ctx context.Context) {
				response, err := service.Responses(ctx, &protocol.ResponsesRequest{Model: "chat", Input: json.RawMessage(`"hello"`)})
				if response != nil || !errors.Is(err, ErrResponsesUnsupported) || !errors.Is(err, adapter.ErrUnsupported) {
					t.Fatalf("Responses = %#v, %v; want an explicit unsupported refusal", response, err)
				}
			},
		},
		{
			name:      "responses stream",
			streaming: true,
			call: func(t *testing.T, service *Service, ctx context.Context) {
				err := service.ResponsesStream(ctx, &protocol.ResponsesRequest{Model: "chat", Input: json.RawMessage(`"hello"`), Stream: true}, func(protocol.StreamEvent) error { return nil })
				if !errors.Is(err, ErrResponsesUnsupported) || !errors.Is(err, adapter.ErrUnsupported) {
					t.Fatalf("ResponsesStream = %v; want an explicit unsupported refusal", err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := NewService(&fakeRouteStore{
				aliases:   map[string]*model.ModelAlias{"chat": {ID: 1, Name: "chat", Active: true}},
				entries:   map[int64][]*model.RouteEntry{1: {{ID: 1, AliasID: 1, ProviderID: 1, ProviderModel: "provider-model", Priority: 10, Active: true}}},
				providers: map[int64]*model.Provider{1: {ID: 1, Name: "no-responses", Type: model.ProviderOpenAICompat, BaseURL: upstream.URL, Active: true}},
			}, nil, upstream.Client())
			records := newFakeRecordStore()
			service.recordStore, service.settlement = records, &nativeSettlement{}

			// The API label matches what httpapi attaches to /v1/responses.
			ctx := WithRequestMetadata(context.Background(), RequestMetadata{
				ID: "responses-unsupported-fixture", UserID: 1, APIKeyID: 2, API: "responses", ModelAlias: "chat", Streaming: tc.streaming,
			})
			tc.call(t, service, ctx)

			closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := service.Close(closeCtx); err != nil {
				t.Fatal(err)
			}

			requests, attempts := records.snapshot()
			if len(requests) != 1 {
				t.Fatalf("recorded %d requests, want 1", len(requests))
			}
			recorded := requests[0]
			if recorded.Status != model.StatusError {
				t.Fatalf("request status = %q, want %q", recorded.Status, model.StatusError)
			}
			if recorded.StatusCode != http.StatusNotImplemented {
				t.Fatalf("request status code = %d, want %d", recorded.StatusCode, http.StatusNotImplemented)
			}
			if recorded.ErrorType != "unsupported" {
				t.Fatalf("request error type = %q, want %q", recorded.ErrorType, "unsupported")
			}
			if len(attempts) != 0 {
				t.Fatalf("recorded %d attempts, want 0: a provider without the Responses opt-in is refused before any upstream call", len(attempts))
			}
			if !recorded.Credits.IsZero() {
				t.Fatalf("request credits = %v, want zero", recorded.Credits)
			}
		})
	}

	if calls := upstreamCalls.Load(); calls != 0 {
		t.Fatalf("the mock upstream received %d requests; a provider without the opt-in must be refused before any call", calls)
	}
}

func TestVisibleReasoningCountsTowardFallbackEstimate(t *testing.T) {
	message := protocol.Message{
		Role: "assistant", ReasoningContent: "four", Content: []protocol.ContentPart{{Type: "text", Text: "one"}},
		ToolCalls: []protocol.ToolCall{{Function: protocol.ToolCallFunction{Arguments: "two"}}},
	}
	if got := messageTextBytes(message); got != len("fouronetwo") {
		t.Fatalf("visible output bytes = %d, want reasoning + content + tool arguments", got)
	}
	usage := estimateChatUsage(&protocol.ChatRequest{}, &protocol.ChatResponse{Choices: []protocol.ChatChoice{{Message: message}}})
	if got := usage.Get("output"); got != 3 { // ceil(10 / 4), coarse fallback only
		t.Fatalf("estimated output = %d, want 3", got)
	}
}

// A locally refused request (ErrInvalidRequest) is the caller's 400 and must be
// recorded as invalid_request with a static summary. A real upstream 400 is an
// *adapter.Error and keeps its own "client" classification; it must never be
// folded into this branch.
func TestRecordErrorClassifiesLocalInvalidRequest(t *testing.T) {
	localErr := fmt.Errorf("tool_choice %q rejected locally: %w", "secret-selector", adapter.ErrInvalidRequest)
	for _, tc := range []struct {
		name       string
		err        error
		wantType   string
		wantStatus int
	}{
		{"sentinel", adapter.ErrInvalidRequest, "invalid_request", http.StatusBadRequest},
		{"wrapped", localErr, "invalid_request", http.StatusBadRequest},
		{"upstream client 400", &adapter.Error{StatusCode: http.StatusBadRequest, Type: "client", Message: "upstream-secret-body"}, "client", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			errorType, summary, status := recordError(tc.err)
			if errorType != tc.wantType || status != tc.wantStatus {
				t.Fatalf("recordError(%v) = (%q, %q, %d), want type %q status %d", tc.err, errorType, summary, status, tc.wantType, tc.wantStatus)
			}
			if summary == "" {
				t.Fatal("recordError returned an empty summary")
			}
			if strings.Contains(summary, "secret-selector") || strings.Contains(summary, "upstream-secret-body") {
				t.Fatalf("recordError summary leaks input text: %q", summary)
			}
		})
	}
	if _, summary, _ := recordError(localErr); summary != "request cannot be represented for the provider protocol" {
		t.Fatalf("local invalid-request summary = %q, want the static text", summary)
	}
}
