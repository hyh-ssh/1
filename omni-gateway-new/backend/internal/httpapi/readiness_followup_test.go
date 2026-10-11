package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/zjvtit/omni-gateway/internal/model"
)

func TestCoverageFollowupConfirmedProtocols(t *testing.T) {
	all := &model.Capabilities{Streaming: true, Embeddings: true, ImagesGenerations: true, ResponsesState: true}
	for _, tc := range []struct {
		name      string
		typeID    model.ProviderType
		responses bool
		caps      *model.Capabilities
		want      []string
	}{
		{"OpenAI unknown model capabilities", model.ProviderOpenAICompat, false, nil, []string{"chat_completions", "messages"}},
		{"OpenAI empty capabilities", model.ProviderOpenAICompat, false, &model.Capabilities{}, []string{"chat_completions", "messages"}},
		{"state metadata alone does not opt into Responses", model.ProviderOpenAICompat, false, &model.Capabilities{ResponsesState: true}, []string{"chat_completions", "messages"}},
		{"OpenAI declared Responses", model.ProviderOpenAICompat, true, nil, []string{"chat_completions", "messages", "responses"}},
		{"confirmed embeddings only", model.ProviderOpenAICompat, false, &model.Capabilities{Embeddings: true}, []string{"chat_completions", "embeddings", "messages"}},
		{"confirmed images only", model.ProviderOpenAICompat, false, &model.Capabilities{ImagesGenerations: true}, []string{"chat_completions", "images_generations", "messages"}},
		{"all confirmed but Responses not declared", model.ProviderOpenAICompat, false, all, []string{"chat_completions", "embeddings", "images_generations", "messages"}},
		{"all confirmed OpenAI protocols", model.ProviderOpenAICompat, true, all, []string{"chat_completions", "embeddings", "images_generations", "messages", "responses"}},
		{"Anthropic native Messages and converted chat", model.ProviderAnthropic, false, nil, []string{"chat_completions", "messages"}},
		{"Anthropic metadata cannot enable unsupported native APIs", model.ProviderAnthropic, true, all, []string{"chat_completions", "messages"}},
		{"Gemini chat and converted Messages", model.ProviderGemini, true, all, []string{"chat_completions", "messages"}},
		{"unknown adapter does not promise protocols", model.ProviderType("unrecognized"), true, all, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := confirmedProtocols(&model.Provider{Type: tc.typeID, SupportsResponses: tc.responses}, tc.caps)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("confirmable protocols=%v want=%v", got, tc.want)
			}
		})
	}
}

type readinessFollowupStore struct {
	*adminTestStore
	models []*model.ProviderModel
}

func (s *readinessFollowupStore) ListProviderModels(context.Context, int64) ([]*model.ProviderModel, error) {
	return s.models, nil
}
func (s *readinessFollowupStore) FirstSuccessfulRequestAt(context.Context, int64, int64) (*time.Time, error) {
	return nil, nil
}

func TestCoverageFollowupReadinessHTTPProtocolsAndReasons(t *testing.T) {
	chat := []string{"chat_completions", "messages"}
	all := model.Capabilities{Streaming: true, Embeddings: true, ImagesGenerations: true}
	for _, tc := range []struct {
		name                                                                                     string
		typeID                                                                                   model.ProviderType
		responses                                                                                bool
		caps                                                                                     *model.Capabilities
		modelInactive, routeInactive, providerInactive, providerMissing, aliasInactive, unpriced bool
		want                                                                                     []string
		reasons                                                                                  []string
		known                                                                                    bool
	}{
		{name: "unknown model", typeID: model.ProviderOpenAICompat, want: chat, reasons: []string{"capabilities_unknown"}},
		{name: "empty metadata remains unknown", typeID: model.ProviderOpenAICompat, caps: &model.Capabilities{}, want: chat, reasons: []string{"capabilities_unknown"}},
		{name: "Responses declared with unknown capabilities", typeID: model.ProviderOpenAICompat, responses: true, want: []string{"chat_completions", "messages", "responses"}, reasons: []string{"capabilities_unknown"}},
		{name: "known native embedding and image support", typeID: model.ProviderOpenAICompat, responses: true, caps: &all, known: true, want: []string{"chat_completions", "embeddings", "images_generations", "messages", "responses"}},
		{name: "inactive model metadata is not confirmation", typeID: model.ProviderOpenAICompat, caps: &all, modelInactive: true, want: chat, reasons: []string{"capabilities_unknown"}},
		{name: "Anthropic no Responses or OpenAI native promises", typeID: model.ProviderAnthropic, responses: true, caps: &all, known: true, want: chat},
		{name: "Gemini converted Messages only", typeID: model.ProviderGemini, responses: true, caps: &all, known: true, want: chat},
		{name: "missing prices only warn", typeID: model.ProviderOpenAICompat, caps: &all, known: true, unpriced: true, want: []string{"chat_completions", "embeddings", "images_generations", "messages"}, reasons: []string{"pricing_missing"}},
		{name: "inactive route", typeID: model.ProviderOpenAICompat, caps: &all, routeInactive: true, want: []string{}, reasons: []string{"route_inactive", "no_effective_route", "capabilities_unknown"}},
		{name: "inactive provider", typeID: model.ProviderOpenAICompat, caps: &all, providerInactive: true, want: []string{}, reasons: []string{"provider_inactive", "no_effective_route", "capabilities_unknown"}},
		{name: "missing provider", typeID: model.ProviderOpenAICompat, providerMissing: true, unpriced: true, want: []string{}, reasons: []string{"provider_missing", "pricing_missing", "no_effective_route", "capabilities_unknown"}},
		{name: "inactive alias preserves route diagnostics", typeID: model.ProviderOpenAICompat, caps: &all, known: true, aliasInactive: true, want: []string{"chat_completions", "embeddings", "images_generations", "messages"}, reasons: []string{"alias_inactive"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &readinessFollowupStore{adminTestStore: newAdminTestStore()}
			s.sessions["readiness-session"] = fakeSession{userID: 1, csrf: "csrf", expires: time.Now().Add(time.Hour)}
			s.keys[7] = &model.APIKey{ID: 7, UserID: 1, Active: true}
			s.aliases[1] = &model.ModelAlias{ID: 1, Name: "public", Active: !tc.aliasInactive}
			s.routes[1] = []*model.RouteEntry{{ID: 3, AliasID: 1, ProviderID: 2, ProviderModel: "upstream", Active: !tc.routeInactive}}
			if !tc.providerMissing {
				s.providers[2] = &model.Provider{ID: 2, Name: "internal", Type: tc.typeID, Active: !tc.providerInactive, SupportsResponses: tc.responses}
			}
			if tc.caps != nil {
				s.models = []*model.ProviderModel{{ID: 4, ProviderID: 2, ModelID: "upstream", Active: !tc.modelInactive, Capabilities: *tc.caps}}
			}
			if !tc.unpriced {
				s.prices["upstream"] = []*model.PriceVersion{{ModelID: "upstream", Version: 11, Cost: map[string]model.Money{"input": model.ZeroMoney}, Credits: map[string]model.Credits{"input": model.ZeroCredits}}}
			}
			h := NewHandlerWithAuth(nil, s, AuthConfig{})
			cookie := &http.Cookie{Name: sessionCookieName, Value: "readiness-session"}
			w := getSelf(t, h, cookie, "/api/admin/aliases/1/readiness")
			if w.Code != 200 {
				t.Fatalf("readiness status=%d", w.Code)
			}
			var got aliasReadiness
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Protocols, tc.want) {
				t.Fatalf("HTTP protocols=%v want=%v", got.Protocols, tc.want)
			}
			wantReasons := append([]string{}, tc.reasons...)
			sort.Strings(wantReasons)
			sort.Strings(got.Reasons)
			if !reflect.DeepEqual(got.Reasons, wantReasons) {
				t.Fatalf("reasons=%v want=%v", got.Reasons, wantReasons)
			}
			effective := !tc.routeInactive && !tc.providerInactive && !tc.providerMissing
			if got.Ready != (effective && !tc.aliasInactive) || got.EffectiveRoutes != map[bool]int{true: 1, false: 0}[effective] {
				t.Fatal("readiness silently hid or activated routes")
			}
			if len(got.Routes) != 1 || got.Routes[0].Active != !tc.routeInactive || got.Routes[0].ProviderActive != (!tc.providerInactive && !tc.providerMissing) {
				t.Fatal("per-route status lost")
			}
			if tc.unpriced {
				if got.PricingStatus != "unpriced" {
					t.Fatal("missing price falsely free")
				}
			} else if effective && got.PricingStatus != "free" {
				t.Fatal("explicit free price not legal")
			}
			access := getSelf(t, h, cookie, "/api/self/access?api_key_id=7")
			if access.Code != 200 {
				t.Fatalf("access status=%d", access.Code)
			}
			var own struct {
				Models []accessModel `json:"models"`
			}
			if err := json.Unmarshal(access.Body.Bytes(), &own); err != nil {
				t.Fatal(err)
			}
			if tc.aliasInactive {
				if len(own.Models) != 0 {
					t.Fatal("inactive alias exposed to own key")
				}
				return
			}
			if len(own.Models) != 1 || !reflect.DeepEqual(own.Models[0].Protocols, tc.want) || (own.Models[0].Capabilities != nil) != tc.known {
				t.Fatal("self protocols or unknown-capability boundary disagrees with readiness")
			}
			if strings.Contains(access.Body.String(), "upstream") || strings.Contains(access.Body.String(), "internal") {
				t.Fatal("personal capabilities leaked provider internals")
			}
		})
	}
}

func TestCoverageFollowupAliasProviderRouteImpactReadOnly(t *testing.T) {
	h := newAdminHarness(t, AuthConfig{}, nil)
	h.store.providers[1] = &model.Provider{ID: 1, Name: "target", Active: true}
	h.store.providers[2] = &model.Provider{ID: 2, Name: "alternative", Active: true}
	h.store.aliases[11] = &model.ModelAlias{ID: 11, Name: "with-alternative", Active: true}
	h.store.aliases[12] = &model.ModelAlias{ID: 12, Name: "last-route", Active: true}
	h.store.aliases[13] = &model.ModelAlias{ID: 13, Name: "inactive-alias", Active: false}
	h.store.routes[11] = []*model.RouteEntry{{ID: 21, AliasID: 11, ProviderID: 1, Active: true}, {ID: 22, AliasID: 11, ProviderID: 2, Active: true}}
	h.store.routes[12] = []*model.RouteEntry{{ID: 23, AliasID: 12, ProviderID: 1, Active: true}}
	h.store.routes[13] = []*model.RouteEntry{{ID: 24, AliasID: 13, ProviderID: 1, Active: true}}
	snapshot := func() []byte {
		b, err := json.Marshal([]any{h.store.aliases, h.store.routes, h.store.providers})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	before := string(snapshot())
	for _, tc := range []struct {
		path, resource string
		want           []impactAlias
		highRisk       bool
	}{
		{"/api/admin/aliases/11/impact", "alias", []impactAlias{{11, "with-alternative", true, 0, true}}, true},
		{"/api/admin/aliases/12/impact", "alias", []impactAlias{{12, "last-route", true, 0, true}}, true},
		{"/api/admin/aliases/13/impact", "alias", []impactAlias{{13, "inactive-alias", false, 0, false}}, false},
		{"/api/admin/providers/1/impact", "provider", []impactAlias{{11, "with-alternative", true, 1, false}, {12, "last-route", true, 0, true}, {13, "inactive-alias", false, 0, false}}, true},
		{"/api/admin/aliases/11/routes/21/impact", "route", []impactAlias{{11, "with-alternative", true, 1, false}}, false},
		{"/api/admin/aliases/12/routes/23/impact", "route", []impactAlias{{12, "last-route", true, 0, true}}, true},
	} {
		t.Run(tc.path, func(t *testing.T) {
			w := h.expect("GET", tc.path, "", 200)
			var out struct {
				Resource string        `json:"resource"`
				Affected []impactAlias `json:"affected"`
				HighRisk bool          `json:"high_risk"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if out.Resource != tc.resource || out.HighRisk != tc.highRisk || !reflect.DeepEqual(out.Affected, tc.want) {
				t.Fatalf("impact mismatch for %s", tc.path)
			}
			if string(snapshot()) != before {
				t.Fatal("impact preview mutated alias/provider/route state")
			}
		})
	}
	h.expect("GET", "/api/admin/aliases/11/routes/23/impact", "", 404)
	h.expect("GET", "/api/admin/aliases/999/impact", "", 404)
	if string(snapshot()) != before {
		t.Fatal("failed impact lookup changed state")
	}
}

func TestCoverageFollowupMessagesNativeAndConversionBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		provider                     model.ProviderType
		body, upstreamPath, response string
		status, calls                int
	}{
		{"Anthropic native preserves thinking", model.ProviderAnthropic, `{"model":"public","messages":[{"role":"user","content":"fixture"}],"max_tokens":10,"thinking":{"type":"enabled","budget_tokens":1024}}`, "/v1/messages", `{"id":"msg","type":"message","role":"assistant","model":"upstream","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`, 200, 1},
		{"OpenAI converted text Messages", model.ProviderOpenAICompat, `{"model":"public","messages":[{"role":"user","content":"fixture"}],"max_tokens":10}`, "/v1/chat/completions", `{"id":"chat","model":"upstream","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`, 200, 1},
		{"OpenAI conversion does not promise native thinking", model.ProviderOpenAICompat, `{"model":"public","messages":[{"role":"user","content":"fixture"}],"max_tokens":10,"thinking":{"type":"enabled","budget_tokens":1024}}`, "", ``, 400, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != tc.upstreamPath {
					t.Errorf("unexpected local mock path: %s", r.URL.Path)
				}
				if tc.provider == model.ProviderAnthropic {
					var payload map[string]json.RawMessage
					if json.NewDecoder(r.Body).Decode(&payload) != nil || payload["thinking"] == nil {
						t.Error("native Messages field discarded")
					}
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, tc.response)
			}))
			defer upstream.Close()
			store := singleProviderStore(t, "public", "upstream", upstream.URL+"/v1")
			store.providers[1].Type = tc.provider
			keys, secret := contractAPIKeys()
			h := newContractHandler(t, store, keys, upstream.Client())
			w := gatewayRequest(t, h, "POST", "/v1/messages", secret, tc.body)
			if w.Code != tc.status || calls != tc.calls {
				t.Fatalf("native/conversion boundary status=%d calls=%d want=%d/%d", w.Code, calls, tc.status, tc.calls)
			}
			if w.Code == 200 && !strings.Contains(w.Body.String(), `"model":"public"`) {
				t.Fatal("public alias not preserved")
			}
		})
	}
}
