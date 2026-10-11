package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zjvtit/omni-gateway/internal/adapter"
	"github.com/zjvtit/omni-gateway/internal/model"
	"github.com/zjvtit/omni-gateway/internal/protocol"
)

type chatProfileStore struct {
	*fakeRouteStore
	reasoning bool
}

func (s *chatProfileStore) GetProviderModel(_ context.Context, _ int64, name string) (*model.ProviderModel, error) {
	return &model.ProviderModel{ModelID: name, Active: true, Capabilities: model.Capabilities{Reasoning: s.reasoning}}, nil
}

func TestChatResolvedRouteDeepSeekProfileAndReasoningGate(t *testing.T) {
	cases := []struct {
		name          string
		profile       model.ProviderCompatibilityProfile
		reasoning     bool
		thinking      json.RawMessage
		effort        string
		wantThinking  string
		wantReasoning any
	}{
		{name: "generic route preserves fields", profile: model.ProviderCompatibilityGeneric, reasoning: true, thinking: json.RawMessage(`{"type":"enabled","budget_tokens":15}`), effort: "low"},
		{name: "explicit enabled capable small budget", profile: model.ProviderCompatibilityDeepSeek, reasoning: true, thinking: json.RawMessage(`{"type":"enabled","budget_tokens":15}`), effort: "none", wantThinking: "enabled", wantReasoning: " "},
		{name: "explicit enabled capable large budget", profile: model.ProviderCompatibilityDeepSeek, reasoning: true, thinking: json.RawMessage(`{"type":"enabled","budget_tokens":4096}`), effort: "none", wantThinking: "enabled", wantReasoning: " "},
		{name: "explicit enabled not capable", profile: model.ProviderCompatibilityDeepSeek, reasoning: false, thinking: json.RawMessage(`{"type":"enabled"}`), wantThinking: "enabled"},
		{name: "explicit disabled wins", profile: model.ProviderCompatibilityDeepSeek, reasoning: true, thinking: json.RawMessage(`{"type":"disabled","budget_tokens":10}`), effort: "high", wantThinking: "disabled"},
		{name: "invalid thinking falls back", profile: model.ProviderCompatibilityDeepSeek, reasoning: true, thinking: json.RawMessage(`{"type":"invalid","budget_tokens":10}`), effort: "none", wantThinking: "disabled"},
		{name: "invalid thinking without effort omitted", profile: model.ProviderCompatibilityDeepSeek, reasoning: true, thinking: json.RawMessage(`{"type":"invalid"}`)},
		{name: "effort low enables", profile: model.ProviderCompatibilityDeepSeek, reasoning: true, effort: "low", wantThinking: "enabled", wantReasoning: " "},
		{name: "effort medium enables", profile: model.ProviderCompatibilityDeepSeek, reasoning: true, effort: "medium", wantThinking: "enabled", wantReasoning: " "},
		{name: "effort high enables", profile: model.ProviderCompatibilityDeepSeek, reasoning: true, effort: "high", wantThinking: "enabled", wantReasoning: " "},
		{name: "effort none disables", profile: model.ProviderCompatibilityDeepSeek, reasoning: true, effort: "none", wantThinking: "disabled"},
		{name: "capable without explicit thinking", profile: model.ProviderCompatibilityDeepSeek, reasoning: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var sent map[string]json.RawMessage
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/chat/completions" {
					t.Errorf("path = %s", r.URL.Path)
				}
				if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
					t.Errorf("decode: %v", err)
				}
				_, _ = io.WriteString(w, `{"id":"id","model":"actual","choices":[]}`)
			}))
			defer upstream.Close()
			store := &chatProfileStore{fakeRouteStore: &fakeRouteStore{
				aliases:   map[string]*model.ModelAlias{"public": {ID: 1, Name: "public", Active: true}},
				entries:   map[int64][]*model.RouteEntry{1: {{ID: 1, ProviderID: 7, ProviderModel: "actual", Active: true}}},
				providers: map[int64]*model.Provider{7: {ID: 7, Type: model.ProviderOpenAICompat, CompatibilityProfile: tc.profile, BaseURL: upstream.URL, Active: true}},
			}, reasoning: tc.reasoning}
			service := NewService(store, nil, upstream.Client())
			req := &protocol.ChatRequest{Model: "public", Messages: []protocol.Message{
				{Role: "assistant", Content: []protocol.ContentPart{{Type: "text", Text: "prior"}}},
				{Role: "user", Content: []protocol.ContentPart{{Type: "text", Text: "now"}}},
			}, ReasoningEffort: tc.effort}
			if tc.thinking != nil {
				req.Extra = map[string]json.RawMessage{"thinking": tc.thinking}
			}
			response, err := service.Chat(context.Background(), req)
			if err != nil || response.Model != "public" {
				t.Fatalf("response=%+v err=%v", response, err)
			}
			if got := string(sent["model"]); got != `"actual"` {
				t.Errorf("upstream model = %s", got)
			}
			var thinking map[string]json.RawMessage
			_ = json.Unmarshal(sent["thinking"], &thinking)
			if tc.profile == model.ProviderCompatibilityGeneric {
				if got := string(sent["thinking"]); got != string(tc.thinking) {
					t.Errorf("generic thinking = %s", got)
				}
				if string(sent["reasoning_effort"]) != `"low"` {
					t.Errorf("generic effort = %s", sent["reasoning_effort"])
				}
			} else {
				var got string
				_ = json.Unmarshal(thinking["type"], &got)
				if got != tc.wantThinking || len(thinking) > 1 {
					t.Errorf("thinking = %s want %q", sent["thinking"], tc.wantThinking)
				}
				if len(sent["reasoning_effort"]) != 0 {
					t.Errorf("DeepSeek leaked effort: %s", sent["reasoning_effort"])
				}
			}
			var messages []map[string]json.RawMessage
			if err := json.Unmarshal(sent["messages"], &messages); err != nil {
				t.Fatal(err)
			}
			var reasoning any
			if len(messages[0]["reasoning_content"]) > 0 {
				_ = json.Unmarshal(messages[0]["reasoning_content"], &reasoning)
			}
			if reasoning != tc.wantReasoning {
				t.Errorf("assistant reasoning = %#v want %#v", reasoning, tc.wantReasoning)
			}
			if len(messages[1]["reasoning_content"]) != 0 {
				t.Errorf("user acquired reasoning: %s", messages[1]["reasoning_content"])
			}
		})
	}
}

func TestChatCrossProtocolUnsupportedFieldsNeverReachUpstream(t *testing.T) {
	for _, typ := range []model.ProviderType{model.ProviderAnthropic, model.ProviderGemini} {
		t.Run(string(typ), func(t *testing.T) {
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(http.StatusOK) }))
			defer upstream.Close()
			service := NewService(&fakeRouteStore{
				aliases:   map[string]*model.ModelAlias{"public": {ID: 1, Name: "public", Active: true}},
				entries:   map[int64][]*model.RouteEntry{1: {{ProviderID: 1, ProviderModel: "actual", Active: true}}},
				providers: map[int64]*model.Provider{1: {ID: 1, Type: typ, BaseURL: upstream.URL, Active: true}},
			}, nil, upstream.Client())
			for _, req := range []*protocol.ChatRequest{
				{Model: "public", Messages: []protocol.Message{{Role: "user"}}, Extra: map[string]json.RawMessage{"unknown": json.RawMessage(`true`)}},
				{Model: "public", Messages: []protocol.Message{{Role: "assistant", ReasoningContent: "private"}}},
				{Model: "public", Messages: []protocol.Message{{Role: "user", Extra: map[string]json.RawMessage{"audio": json.RawMessage(`{}`)}}}},
				{Model: "public", Messages: []protocol.Message{{Role: "user", Content: []protocol.ContentPart{{Type: "text", Text: "hello", Raw: json.RawMessage(`{"type":"text","text":"hello","cache_control":{"type":"ephemeral"}}`)}}}}},
				{Model: "public", Messages: []protocol.Message{{Role: "user"}}, Tools: []protocol.Tool{{Type: "custom"}}},
			} {
				if _, err := service.Chat(context.Background(), req); !errors.Is(err, adapter.ErrInvalidRequest) {
					t.Errorf("Chat err = %v", err)
				}
				if err := service.ChatStream(context.Background(), req, func(protocol.StreamEvent) error { return nil }); !errors.Is(err, adapter.ErrInvalidRequest) {
					t.Errorf("ChatStream err = %v", err)
				}
			}
			if calls != 0 {
				t.Errorf("unsupported conversions made %d upstream requests", calls)
			}
		})
	}
}
