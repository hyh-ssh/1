package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zjvtit/omni-gateway/internal/model"
	"github.com/zjvtit/omni-gateway/internal/protocol"
)

func TestProductRetiredPolicyAPIs(t *testing.T) {
	h := newAdminHarness(t, AuthConfig{}, nil)
	for _, path := range []string{"/api/admin/budgets", "/api/admin/budgets/1", "/api/admin/user-groups", "/api/admin/user-groups/1"} {
		for _, method := range []string{"GET", "POST", "PATCH", "DELETE"} {
			h.expect(method, path, `{"name":"unused","user_id":1}`, 404)
		}
	}
}

func TestProductKeyOwnershipAndAdminGovernance(t *testing.T) {
	h := newAdminHarness(t, AuthConfig{}, nil)
	h.store.keys[20] = &model.APIKey{ID: 20, UserID: 2, Name: "other", KeyHash: "never-return-hash", Active: true}
	h.expect("POST", "/api/admin/api-keys", `{"user_id":2,"name":"forged"}`, 403)
	created := h.expect("POST", "/api/admin/api-keys", `{"name":"own"}`, 201)
	var data struct {
		Key model.APIKey `json:"key"`
	}
	_ = json.Unmarshal(created.Body.Bytes(), &data)
	if data.Key.UserID != 1 {
		t.Fatalf("owner=%d", data.Key.UserID)
	}
	denied := h.expect("PATCH", "/api/admin/api-keys/20", `{"user_id":1}`, 400)
	if !strings.Contains(denied.Body.String(), "key_owner_immutable") {
		t.Fatal(denied.Body)
	}
	for _, body := range []string{`{"name":"changed"}`, `{"expires_at":null}`, `{"model_allow":[]}`, `{"active":true}`, `{"active":false,"name":"changed"}`, `{"active":false,"unknown":1}`} {
		h.expect("PATCH", "/api/admin/api-keys/20", body, 403)
	}
	h.expect("DELETE", "/api/admin/api-keys/20", "", 403)
	h.expect("PATCH", "/api/admin/api-keys/20", `{"active":false}`, 200)
	get := h.expect("GET", "/api/admin/api-keys/20", "", 200)
	if strings.Contains(get.Body.String(), "never-return-hash") || h.store.keys[20].Active || h.store.keys[20].UserID != 2 {
		t.Fatal("governance or masking failed")
	}
}

func TestProductAllProtocolsIgnoreHistoricalGrants(t *testing.T) {
	keys := &groupedAPIKeyStore{fakeAPIKeyStore: *gatewayAPIKeyStore(), access: &model.ModelAccess{Restricted: true}}
	service := &nativeTestService{fakeChatService: fakeChatService{models: []string{"alias", "other"}, chatResponse: &protocol.ChatResponse{ID: "chat"}, response: &protocol.ResponsesResponse{ID: "resp", Status: "completed"}}}
	handler := NewHandlerWithGateway(nil, nil, keys, service, AuthConfig{})
	for _, tc := range []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"alias","messages":[{"role":"user","content":"hi"}]}`},
		{"/v1/responses", `{"model":"alias","input":"hi"}`},
		{"/v1/messages", `{"model":"alias","messages":[{"role":"user","content":"hi"}],"max_tokens":10}`},
		{"/v1/embeddings", `{"model":"alias","input":"hi"}`},
		{"/v1/images/generations", `{"model":"alias","prompt":"hi"}`},
	} {
		keys.key.ModelAllow = nil
		if w := gatewayRequest(t, handler, "POST", tc.path, "sk-gateway", tc.body); w.Code != 200 {
			t.Fatalf("%s empty allow=%d %s", tc.path, w.Code, w.Body)
		}
		keys.key.ModelAllow = []string{"other"}
		if w := gatewayRequest(t, handler, "POST", tc.path, "sk-gateway", tc.body); w.Code != 403 {
			t.Fatalf("%s restricted=%d", tc.path, w.Code)
		}
	}
	keys.key.ModelAllow = nil
	if w := gatewayRequest(t, handler, "GET", "/v1/models", "sk-gateway", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"alias"`) {
		t.Fatalf("models=%d %s", w.Code, w.Body)
	}
}

func TestProductAliasDraftReadinessAndImpact(t *testing.T) {
	h := newAdminHarness(t, AuthConfig{}, nil)
	created := h.expect("POST", "/api/admin/aliases", `{"name":"draft"}`, 201)
	var a model.ModelAlias
	_ = json.Unmarshal(created.Body.Bytes(), &a)
	if a.Active {
		t.Fatal("new alias must be inactive")
	}
	path := "/api/admin/aliases/" + itoa(int(a.ID))
	h.expect("PATCH", path, `{"active":true}`, 400)
	h.expect("POST", path+"/routes", `{"provider_id":999,"provider_model":"manual"}`, 404)
	h.store.providers[7] = &model.Provider{ID: 7, Name: "real", Type: model.ProviderOpenAICompat, Active: false, SupportsResponses: true}
	h.expect("POST", path+"/routes", `{"provider_id":7,"provider_model":"manual"}`, 201)
	h.expect("PATCH", path, `{"active":true}`, 400)
	h.store.providers[7].Active = true
	h.expect("PATCH", path, `{"active":true}`, 200)
	ready := h.expect("GET", path+"/readiness", "", 200)
	if !strings.Contains(ready.Body.String(), `"effective_routes":1`) || !strings.Contains(ready.Body.String(), `"pricing_status":"unpriced"`) {
		t.Fatal(ready.Body)
	}
	impact := h.expect("GET", "/api/admin/providers/7/impact", "", 200)
	if !strings.Contains(impact.Body.String(), `"loses_last_route":true`) || !strings.Contains(impact.Body.String(), `"alias_name":"draft"`) {
		t.Fatal(impact.Body)
	}
	h.expect("PATCH", "/api/admin/providers/7", `{"active":false}`, 400) // incomplete legacy provider input: no state changes on invalid write
	if !h.store.aliases[a.ID].Active {
		t.Fatal("impact may not disable aliases")
	}
}

type productSelfStore struct {
	*selfTestStore
	bound bool
}

func (s *productSelfStore) HasOIDCBinding(context.Context, int64) (bool, error) { return s.bound, nil }
func (s *productSelfStore) ListAliases(context.Context) ([]*model.ModelAlias, error) {
	return []*model.ModelAlias{{ID: 1, Name: "public", Active: true}, {ID: 2, Name: "hidden", Active: false}}, nil
}
func (s *productSelfStore) ListRouteEntries(context.Context, int64) ([]*model.RouteEntry, error) {
	return []*model.RouteEntry{{ID: 1, ProviderID: 9, ProviderModel: "internal-name", Active: true}}, nil
}
func (s *productSelfStore) GetProvider(context.Context, int64) (*model.Provider, error) {
	return &model.Provider{ID: 9, Name: "internal-provider", BaseURL: "http://internal", APIKeyEnc: "secret-ciphertext", Type: model.ProviderOpenAICompat, Active: true, SupportsResponses: true}, nil
}
func (s *productSelfStore) ListProviderModels(context.Context, int64) ([]*model.ProviderModel, error) {
	return nil, nil
}
func (s *productSelfStore) FirstSuccessfulRequestAt(context.Context, int64, int64) (*time.Time, error) {
	at := time.Date(2026, 10, 3, 1, 2, 3, 0, time.UTC)
	return &at, nil
}

func TestProductSSOOnlyPasswordCapability(t *testing.T) {
	s := &productSelfStore{selfTestStore: newSelfTestStore(t), bound: true}
	s.usersByID[2].PasswordLoginEnabled = false
	s.sessions["sso-session"] = fakeSession{userID: 2, csrf: "sso-csrf", expires: time.Now().Add(time.Hour)}
	s.sessions["other-sso"] = fakeSession{userID: 2, csrf: "other", expires: time.Now().Add(time.Hour)}
	handler := NewHandlerWithAuth(nil, s, AuthConfig{})
	cookie := &http.Cookie{Name: sessionCookieName, Value: "sso-session"}
	me := getSelf(t, handler, cookie, "/api/auth/me")
	if !strings.Contains(me.Body.String(), `"can_set_without_current":true`) {
		t.Fatal(me.Body)
	}
	noCSRF := writeSelf(t, handler, cookie, "", "POST", "/api/self/password", `{"new_password":"new-password"}`)
	if noCSRF.Code != 403 {
		t.Fatal(noCSRF.Code)
	}
	changed := writeSelf(t, handler, cookie, "sso-csrf", "POST", "/api/self/password", `{"new_password":"new-password","user_id":1}`)
	if changed.Code != 204 {
		t.Fatalf("setup=%d %s", changed.Code, changed.Body)
	}
	if _, ok := s.sessions["other-sso"]; ok || len(s.passwordSets) != 1 || s.passwordSets[0] != 2 {
		t.Fatal("other sessions or ownership")
	}
	if !s.bound {
		t.Fatal("binding removed")
	}
}

func TestProductSelfAccessOwnershipAndPersistedSuccess(t *testing.T) {
	s := &productSelfStore{selfTestStore: newSelfTestStore(t)}
	h := NewHandlerWithAuth(nil, s, AuthConfig{})
	cookie := loginSelf(t, h, "alice", "alice-password")
	access := getSelf(t, h, cookie, "/api/self/access?api_key_id=11")
	if access.Code != 200 || !strings.Contains(access.Body.String(), `"first_success_at":"2026-10-03T01:02:03Z"`) || !strings.Contains(access.Body.String(), `"id":"public"`) {
		t.Fatalf("access=%d %s", access.Code, access.Body)
	}
	for _, forbidden := range []string{"internal", "secret", "hidden", "alice-key-hash", "provider_id", "provider_model"} {
		if strings.Contains(access.Body.String(), forbidden) {
			t.Fatalf("leaked %s: %s", forbidden, access.Body)
		}
	}
	if w := getSelf(t, h, cookie, "/api/self/access?api_key_id=12"); w.Code != 404 {
		t.Fatalf("foreign access=%d", w.Code)
	}
}

func TestProductOwnRequestDiagnosticsAreSafe(t *testing.T) {
	s := newSelfTestStore(t)
	s.requests[2] = []*model.Request{{ID: "server-owned-id", UserID: 2, Status: model.StatusError, StatusCode: 502, ErrorType: "upstream", ErrorSummary: "password=secret https://internal/provider", Tags: map[string]string{"secret": "private"}, ClientIP: "private", UserAgent: "private"}}
	h := NewHandlerWithAuth(nil, s, AuthConfig{})
	cookie := loginSelf(t, h, "alice", "alice-password")
	w := getSelf(t, h, cookie, "/api/self/requests?status=error&from=2026-10-01T00:00:00Z&to=2026-10-04T00:00:00Z&request_id=server-owned-id&user_id=3")
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	for _, secret := range []string{"password=", "https://internal", "private"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal(w.Body)
		}
	}
	if s.requestFilters[0].Status != model.StatusError || s.requestFilters[0].From.IsZero() || s.requestFilters[0].UserID != 2 {
		t.Fatalf("filter=%+v", s.requestFilters[0])
	}
}

func TestProductAuditRecordsActualOutcomeAndSafeChange(t *testing.T) {
	h := newAdminHarness(t, AuthConfig{}, nil)
	h.expect("POST", "/api/admin/api-keys", `{"name":"own"}`, 201)
	h.expect("POST", "/api/admin/api-keys", `{"user_id":2,"name":"denied"}`, 403)
	h.expect("POST", "/api/admin/providers", `{"name":"bad","api_key":"sensitive-secret"}`, 400)
	audits, _ := json.Marshal(h.store.audit)
	if !strings.Contains(string(audits), `"result":"success"`) || !strings.Contains(string(audits), `"result":"denied"`) || !strings.Contains(string(audits), `"result":"failure"`) || !strings.Contains(string(audits), `"after":`) {
		t.Fatalf("audits=%s", audits)
	}
	if strings.Contains(string(audits), "sensitive-secret") || strings.Contains(string(audits), "key_hash") {
		t.Fatalf("audit secret=%s", audits)
	}
}

func TestProductDiagnosticIDIsServerOwned(t *testing.T) {
	h := NewHandlerWithGateway(nil, nil, gatewayAPIKeyStore(), &fakeChatService{}, AuthConfig{})
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[]}`))
	r.Header.Set("Authorization", "Bearer sk-gateway")
	r.Header.Set(RequestIDHeader, "caller-repeatable-id")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var body struct {
		Error errorDetail `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.RequestID == "" || body.Error.RequestID != w.Header().Get("X-Gateway-Request-ID") || body.Error.RequestID == "caller-repeatable-id" {
		t.Fatalf("diagnostic ID not searchable: %+v", body.Error)
	}
}

func TestProductPasswordSetupCapabilityBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name         string
		local, bound bool
		current      string
		status       int
	}{
		{"bound SSO", false, true, "", 204}, {"unbound disabled local", false, false, "", 400}, {"local even if SSO bound", true, true, "", 400}, {"wrong current", true, false, "wrong", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &productSelfStore{selfTestStore: newSelfTestStore(t), bound: tc.bound}
			s.usersByID[2].PasswordLoginEnabled = tc.local
			s.sessions["live"] = fakeSession{userID: 2, csrf: "csrf", expires: time.Now().Add(time.Hour)}
			h := NewHandlerWithAuth(nil, s, AuthConfig{})
			cookie := &http.Cookie{Name: sessionCookieName, Value: "live"}
			w := writeSelf(t, h, cookie, "csrf", "POST", "/api/self/password", `{"new_password":"new-password","current_password":"`+tc.current+`","user_id":1}`)
			if w.Code != tc.status {
				t.Fatal(w.Code, w.Body)
			}
			if tc.status != 204 && len(s.passwordSets) > 0 {
				t.Fatal("refused password write persisted")
			}
			if tc.status == 204 {
				me := getSelf(t, h, cookie, "/api/auth/me")
				if !strings.Contains(me.Body.String(), `"local_enabled":true`) || strings.Contains(me.Body.String(), `"can_set_without_current":true`) {
					t.Fatal(me.Body)
				}
			}
		})
	}
	s := &productSelfStore{selfTestStore: newSelfTestStore(t), bound: true}
	s.usersByID[2].PasswordLoginEnabled = false
	s.sessions["live"] = fakeSession{userID: 2, csrf: "csrf", expires: time.Now().Add(time.Hour)}
	h := NewHandlerWithAuth(nil, s, AuthConfig{})
	cookie := &http.Cookie{Name: sessionCookieName, Value: "live"}
	if w := writeSelf(t, h, cookie, "csrf", "POST", "/api/self/password", `{"new_password":"short"}`); w.Code != 400 || !strings.Contains(w.Body.String(), "weak_password") {
		t.Fatal(w.Code, w.Body)
	}
	if w := writeSelf(t, h, nil, "csrf", "POST", "/api/self/password", `{"new_password":"new-password"}`); w.Code != 401 {
		t.Fatal(w.Code, w.Body)
	}
	s.sessions["live"] = fakeSession{userID: 2, csrf: "csrf", expires: time.Now().Add(-time.Hour)}
	if w := writeSelf(t, h, cookie, "csrf", "POST", "/api/self/password", `{"new_password":"new-password"}`); w.Code != 401 {
		t.Fatal(w.Code, w.Body)
	}
}

func TestProductImpactAccountsForAlternativesAndNeverDisablesLegacyAliases(t *testing.T) {
	h := newAdminHarness(t, AuthConfig{}, nil)
	h.store.providers[1] = &model.Provider{ID: 1, Name: "target", Type: model.ProviderOpenAICompat, CompatibilityProfile: model.ProviderCompatibilityGeneric, BaseURL: "https://fixture.invalid/v1", Active: true}
	h.store.providers[2] = &model.Provider{ID: 2, Name: "alternative", Type: model.ProviderOpenAICompat, CompatibilityProfile: model.ProviderCompatibilityGeneric, BaseURL: "https://fixture.invalid/v1", Active: true}
	h.store.providers[3] = &model.Provider{ID: 3, Name: "inactive", Type: model.ProviderOpenAICompat, Active: false}
	h.store.aliases[11] = &model.ModelAlias{ID: 11, Name: "has-alternative", Active: true}
	h.store.aliases[12] = &model.ModelAlias{ID: 12, Name: "last-route", Active: true}
	h.store.aliases[13] = &model.ModelAlias{ID: 13, Name: "legacy-invalid", Active: true}
	h.store.routes[11] = []*model.RouteEntry{{ID: 21, AliasID: 11, ProviderID: 1, ProviderModel: "manual", Active: true}, {ID: 22, AliasID: 11, ProviderID: 2, ProviderModel: "manual", Active: true}}
	h.store.routes[12] = []*model.RouteEntry{{ID: 23, AliasID: 12, ProviderID: 1, ProviderModel: "manual", Active: true}, {ID: 24, AliasID: 12, ProviderID: 3, ProviderModel: "manual", Active: true}}
	var impact struct {
		Affected []impactAlias `json:"affected"`
		HighRisk bool          `json:"high_risk"`
	}
	w := h.expect("GET", "/api/admin/providers/1/impact", "", 200)
	if err := json.Unmarshal(w.Body.Bytes(), &impact); err != nil {
		t.Fatal(err)
	}
	if len(impact.Affected) != 2 || !impact.HighRisk || impact.Affected[0].RemainingRoutes != 1 || impact.Affected[0].LosesLastRoute || !impact.Affected[1].LosesLastRoute {
		t.Fatal(w.Body)
	}
	h.expect("GET", "/api/admin/aliases/11/routes/23/impact", "", 404)
	w = h.expect("GET", "/api/admin/aliases/11/routes/21/impact", "", 200)
	if !strings.Contains(w.Body.String(), `"remaining_routes":1`) || !strings.Contains(w.Body.String(), `"high_risk":false`) {
		t.Fatal(w.Body)
	}
	h.expect("PATCH", "/api/admin/aliases/13", `{"name":"still-active","active":true}`, 200)
	h.expect("PATCH", "/api/admin/providers/1", `{"active":false}`, 200)
	for _, a := range h.store.aliases {
		if !a.Active {
			t.Fatal("impact mutation auto-disabled an alias")
		}
	}
	w = h.expect("GET", "/api/admin/aliases/12/readiness", "", 200)
	if !strings.Contains(w.Body.String(), `"ready":false`) || !strings.Contains(w.Body.String(), `"provider_inactive"`) {
		t.Fatal(w.Body)
	}
	h.expect("POST", "/api/admin/aliases", `{"name":"not-ready","active":true}`, 400)
	if len(h.store.aliases) != 3 {
		t.Fatal("rejected active creation left an alias")
	}
}

func TestProductReadinessPricePriorityFreeAndTinyRates(t *testing.T) {
	h := newAdminHarness(t, AuthConfig{}, nil)
	h.store.providers[1] = &model.Provider{ID: 1, Name: "real", Type: model.ProviderOpenAICompat, Active: true, SupportsResponses: true}
	h.store.aliases[1] = &model.ModelAlias{ID: 1, Name: "public", Active: true}
	h.store.routes[1] = []*model.RouteEntry{{ID: 1, AliasID: 1, ProviderID: 1, ProviderModel: "upstream", Active: true}}
	h.store.prices["public"] = []*model.PriceVersion{{ModelID: "public", Version: 2, Cost: map[string]model.Money{"custom": model.MustMoney("1")}, Credits: map[string]model.Credits{"custom": model.MustCredits("1")}}}
	h.store.prices["upstream"] = []*model.PriceVersion{{ModelID: "upstream", Version: 7, Cost: map[string]model.Money{"custom": model.ZeroMoney}, Credits: map[string]model.Credits{"custom": model.ZeroCredits}}}
	w := h.expect("GET", "/api/admin/aliases/1/readiness", "", 200)
	if !strings.Contains(w.Body.String(), `"pricing_status":"free"`) || !strings.Contains(w.Body.String(), `"price_model_id":"upstream"`) || !strings.Contains(w.Body.String(), `"price_version":7`) {
		t.Fatal(w.Body)
	}
	h.store.prices["upstream"][0].Credits["custom"] = model.MustCredits("0.000000000000000000001")
	w = h.expect("GET", "/api/admin/aliases/1/readiness", "", 200)
	if !strings.Contains(w.Body.String(), `"pricing_status":"priced"`) {
		t.Fatal("tiny rate falsely free", w.Body)
	}
	delete(h.store.prices, "upstream")
	w = h.expect("GET", "/api/admin/aliases/1/readiness", "", 200)
	if !strings.Contains(w.Body.String(), `"price_model_id":"public"`) {
		t.Fatal("alias fallback failed", w.Body)
	}
}
