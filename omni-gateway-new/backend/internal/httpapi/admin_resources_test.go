package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gatewaycrypto "github.com/zjvtit/omni-gateway/internal/crypto"
	"github.com/zjvtit/omni-gateway/internal/model"
	"github.com/zjvtit/omni-gateway/internal/store"
)

// adminHarness wires the admin API against the in-memory admin store and
// returns a request helper that already carries the session cookie and CSRF
// token, so each test only has to describe the request it cares about.
type adminHarness struct {
	t       *testing.T
	handler http.Handler
	store   *adminTestStore
	cookie  *http.Cookie
	csrf    string
}

func newAdminHarness(t *testing.T, cfg AuthConfig, cipher *gatewaycrypto.Cipher) *adminHarness {
	t.Helper()
	store := newAdminTestStore()
	handler := NewHandlerWithGatewayAndCipher(nil, store, store, nil, cfg, cipher)
	cookie, csrf := adminTestSession(t, handler)
	return &adminHarness{t: t, handler: handler, store: store, cookie: cookie, csrf: csrf}
}

func (h *adminHarness) do(method, path, body string) *httptest.ResponseRecorder {
	h.t.Helper()
	reader := strings.NewReader(body)
	request := httptest.NewRequest(method, path, reader)
	request.AddCookie(h.cookie)
	request.Header.Set("X-CSRF-Token", h.csrf)
	recorder := httptest.NewRecorder()
	h.handler.ServeHTTP(recorder, request)
	return recorder
}

func (h *adminHarness) expect(method, path, body string, wantStatus int) *httptest.ResponseRecorder {
	h.t.Helper()
	recorder := h.do(method, path, body)
	if recorder.Code != wantStatus {
		h.t.Fatalf("%s %s: got status %d, want %d (body %s)", method, path, recorder.Code, wantStatus, recorder.Body.String())
	}
	return recorder
}

// createPrice posts one price version and returns its decoded id.
func (h *adminHarness) createPrice(modelID string, version int) int64 {
	h.t.Helper()
	// Both ledgers are mandatory (see validatePriceMaps), so the helper sends a
	// credit rate for every meter it prices.
	body := `{"model_id":"` + modelID + `","version":` + itoa(version) + `,"cost":{"input":"0.01"},"credits":{"input":"0.5"}}`
	recorder := h.expect(http.MethodPost, "/api/admin/prices", body, http.StatusCreated)
	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil || created.ID <= 0 {
		h.t.Fatalf("create price: bad body %s", recorder.Body.String())
	}
	return created.ID
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var buf [20]byte
	i := len(buf)
	for value > 0 {
		i--
		buf[i] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func TestAdminPricesLifecycleAndValidation(t *testing.T) {
	h := newAdminHarness(t, AuthConfig{}, nil)

	empty := h.expect(http.MethodGet, "/api/admin/prices", "", http.StatusOK)
	if strings.TrimSpace(empty.Body.String()) != "[]" {
		t.Fatalf("empty price listing = %s, want []", empty.Body.String())
	}

	id := h.createPrice("gpt-4o", 1)

	all := h.expect(http.MethodGet, "/api/admin/prices", "", http.StatusOK)
	if !strings.Contains(all.Body.String(), `"gpt-4o"`) {
		t.Fatalf("listing did not include the created price: %s", all.Body.String())
	}

	latest := h.expect(http.MethodGet, "/api/admin/prices?model_id=gpt-4o&latest=true", "", http.StatusOK)
	if !strings.Contains(latest.Body.String(), `"version":1`) {
		t.Fatalf("latest price = %s", latest.Body.String())
	}

	versions := h.expect(http.MethodGet, "/api/admin/prices?model_id=gpt-4o", "", http.StatusOK)
	if !strings.Contains(versions.Body.String(), `"version":1`) || !strings.Contains(versions.Body.String(), `"gpt-4o"`) {
		t.Fatalf("version listing = %s, want the created gpt-4o version 1", versions.Body.String())
	}

	// An unknown model has no price versions.
	h.expect(http.MethodGet, "/api/admin/prices?model_id=missing&latest=true", "", http.StatusNotFound)

	// Price versions are immutable snapshots: only create is exposed.
	pricePath := "/api/admin/prices/" + itoa(int(id))
	h.expect(http.MethodPatch, pricePath, `{"version":2}`, http.StatusMethodNotAllowed)
	h.expect(http.MethodDelete, pricePath, "", http.StatusMethodNotAllowed)

	// Validation failures.
	h.expect(http.MethodPost, "/api/admin/prices", `{"model_id":"","version":1}`, http.StatusBadRequest)
	h.expect(http.MethodPost, "/api/admin/prices", `{"model_id":"x","version":0}`, http.StatusBadRequest)
	h.expect(http.MethodPost, "/api/admin/prices", `not json`, http.StatusBadRequest)
	badMoney := h.expect(http.MethodPost, "/api/admin/prices", `{"model_id":"x","version":1,"cost":{"input":"abc"}}`, http.StatusBadRequest)
	if !strings.Contains(badMoney.Body.String(), "invalid_price") {
		t.Fatalf("invalid money error = %s", badMoney.Body.String())
	}
	negative := h.expect(http.MethodPost, "/api/admin/prices", `{"model_id":"x","version":1,"credits":{"output":"-1"}}`, http.StatusBadRequest)
	if !strings.Contains(negative.Body.String(), "invalid_price") {
		t.Fatalf("negative money error = %s", negative.Body.String())
	}

	// Both ledgers must be explicit, and credits must cover every cost meter,
	// including an explicit zero when credits are waived. The decoder drops
	// the old "sale" field, so stale or incomplete payloads must still return
	// 400 rather than silently treating a missing credit rate as intentional zero.
	h.expect(http.MethodPost, "/api/admin/prices", `{"model_id":"x","version":1,"cost":{"input":"0.01"}}`, http.StatusBadRequest)
	h.expect(http.MethodPost, "/api/admin/prices", `{"model_id":"x","version":1,"credits":{"input":"0.5"}}`, http.StatusBadRequest)
	stale := h.expect(http.MethodPost, "/api/admin/prices", `{"model_id":"x","version":1,"sale":{"input":"0.5"}}`, http.StatusBadRequest)
	if !strings.Contains(stale.Body.String(), "invalid_price") {
		t.Fatalf("stale sale payload error = %s", stale.Body.String())
	}
	uncovered := h.expect(http.MethodPost, "/api/admin/prices", `{"model_id":"x","version":1,"cost":{"input":"0.01","output":"0.02"},"credits":{"input":"0.5"}}`, http.StatusBadRequest)
	if !strings.Contains(uncovered.Body.String(), "invalid_price") {
		t.Fatalf("uncovered meter error = %s", uncovered.Body.String())
	}
	// A two-ledger version that covers every meter is still accepted.
	valid := h.expect(http.MethodPost, "/api/admin/prices", `{"model_id":"x","version":2,"cost":{"input":"0.01","output":"0.02"},"credits":{"input":"0.5","output":"1"}}`, http.StatusCreated)
	if !strings.Contains(valid.Body.String(), `"credits"`) || !strings.Contains(valid.Body.String(), "0.5") {
		t.Fatalf("two-ledger price body = %s", valid.Body.String())
	}
}

func TestAdminPricesExplicitZeroRatesAndPrecision(t *testing.T) {
	for _, tc := range []struct {
		name, cost, credits string
	}{
		{name: "free", cost: "0", credits: "0"},
		{name: "cost only", cost: "0.0000023456789", credits: "0"},
		{name: "credits only", cost: "0", credits: "0.0000001234567"},
		{name: "paid precision", cost: "0.0000023456789", credits: "0.0000001234567"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newAdminHarness(t, AuthConfig{}, nil)
			body := `{"model_id":"custom","version":3,"cost":{"input":"` + tc.cost + `","output":"0","image":"0"},"credits":{"input":"` + tc.credits + `","output":"0","image":"0"}}`
			created := h.expect(http.MethodPost, "/api/admin/prices", body, http.StatusCreated)
			latest := h.expect(http.MethodGet, "/api/admin/prices?model_id=custom&latest=true", "", http.StatusOK)
			for _, response := range []*httptest.ResponseRecorder{created, latest} {
				var price model.PriceVersion
				if err := json.Unmarshal(response.Body.Bytes(), &price); err != nil {
					t.Fatal(err)
				}
				if price.ID <= 0 || price.ModelID != "custom" || price.Version != 3 || len(price.Cost) != 3 || len(price.Credits) != 3 {
					t.Fatalf("price context or meters lost: %+v", price)
				}
				if price.Cost["input"].String() != tc.cost || price.Credits["input"].String() != tc.credits {
					t.Fatalf("rates changed: cost=%s credits=%s", price.Cost["input"], price.Credits["input"])
				}
				for _, meter := range []string{"output", "image"} {
					cost, costOK := price.Cost[meter]
					credits, creditsOK := price.Credits[meter]
					if !costOK || !creditsOK || !cost.IsZero() || !credits.IsZero() {
						t.Fatalf("explicit zero meter %q lost: %+v", meter, price)
					}
				}
			}
		})
	}
}

func TestAdminPricesRejectIncompleteOrInvalidZeroRateConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, rates string
	}{
		{name: "negative cost", rates: `"cost":{"input":"-0.01"},"credits":{"input":"0"}`},
		{name: "negative credits", rates: `"cost":{"input":"0"},"credits":{"input":"-0.01"}`},
		{name: "invalid cost", rates: `"cost":{"input":"NaN"},"credits":{"input":"0"}`},
		{name: "invalid credits", rates: `"cost":{"input":"0"},"credits":{"input":"abc"}`},
		{name: "empty rate", rates: `"cost":{"input":"0"},"credits":{"input":""}`},
		{name: "missing cost", rates: `"credits":{"input":"0"}`},
		{name: "missing credits", rates: `"cost":{"input":"0"}`},
		{name: "empty cost", rates: `"cost":{},"credits":{"input":"0"}`},
		{name: "empty credits", rates: `"cost":{"input":"0"},"credits":{}`},
		{name: "null cost", rates: `"cost":null,"credits":{"input":"0"}`},
		{name: "null credits", rates: `"cost":{"input":"0"},"credits":null`},
		{name: "uncovered zero cost meter", rates: `"cost":{"input":"0","output":"0"},"credits":{"input":"0"}`},
		{name: "sale only", rates: `"sale":{"input":"0"}`},
		{name: "cost and stale sale", rates: `"cost":{"input":"0"},"sale":{"input":"0"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newAdminHarness(t, AuthConfig{}, nil)
			rejected := h.expect(http.MethodPost, "/api/admin/prices", `{"model_id":"invalid","version":1,`+tc.rates+`}`, http.StatusBadRequest)
			if !strings.Contains(rejected.Body.String(), `"invalid_price"`) {
				t.Fatalf("expected price validation error: %s", rejected.Body.String())
			}
			all := h.expect(http.MethodGet, "/api/admin/prices", "", http.StatusOK)
			if strings.TrimSpace(all.Body.String()) != "[]" {
				t.Fatalf("rejected price was saved: %s", all.Body.String())
			}
		})
	}
}

func TestAdminRetiredBudgetsPreserveHistory(t *testing.T) {
	h := newAdminHarness(t, AuthConfig{}, nil)
	h.store.budgets[1] = &model.BudgetRule{ID: 1, UserID: 1, WindowSecs: 60, Active: true}
	for _, path := range []string{"/api/admin/budgets", "/api/admin/budgets/1", "/api/admin/budgets?user_id=abc"} {
		for _, method := range []string{"GET", "POST", "PATCH", "DELETE", "PUT"} {
			h.expect(method, path, `{"user_id":1,"window_secs":60,"cost_limit":"1","active":false}`, 404)
		}
	}
	if len(h.store.budgets) != 1 || !h.store.budgets[1].Active {
		t.Fatal("retired API mutated historical budget")
	}
}

func TestAdminRateLimitsLifecycleAndValidation(t *testing.T) {
	h := newAdminHarness(t, AuthConfig{}, nil)

	h.expect(http.MethodGet, "/api/admin/rate-limits", "", http.StatusOK)
	h.expect(http.MethodGet, "/api/admin/rate-limits?scope=user&scope_id=1", "", http.StatusOK)
	h.expect(http.MethodGet, "/api/admin/rate-limits?scope=model", "", http.StatusOK)
	h.expect(http.MethodGet, "/api/admin/rate-limits?scope=user", "", http.StatusBadRequest)
	h.expect(http.MethodGet, "/api/admin/rate-limits?scope=bogus&scope_id=1", "", http.StatusOK)

	created := h.expect(http.MethodPost, "/api/admin/rate-limits", `{"scope":"key","scope_id":2,"rpm":60,"tpm":1000,"concurrency":4}`, http.StatusCreated)
	var rule struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &rule); err != nil || rule.ID <= 0 {
		t.Fatalf("create rate limit: %s", created.Body.String())
	}

	h.expect(http.MethodPost, "/api/admin/rate-limits", `{"scope":"bogus","scope_id":1}`, http.StatusBadRequest)
	h.expect(http.MethodPost, "/api/admin/rate-limits", `{"scope":"user"}`, http.StatusBadRequest)
	h.expect(http.MethodPost, "/api/admin/rate-limits", `{"scope":"user","scope_id":0}`, http.StatusBadRequest)
	h.expect(http.MethodPost, "/api/admin/rate-limits", `oops`, http.StatusBadRequest)

	path := "/api/admin/rate-limits/" + itoa(int(rule.ID))
	patched := h.expect(http.MethodPatch, path, `{"rpm":-5,"tpm":10,"concurrency":1,"active":false,"model_id":"chat"}`, http.StatusOK)
	if !strings.Contains(patched.Body.String(), `"rpm":0`) {
		t.Fatalf("negative rpm was not clamped: %s", patched.Body.String())
	}
	h.expect(http.MethodPatch, path, `{"scope":"bogus"}`, http.StatusBadRequest)
	h.expect(http.MethodPatch, path, `{"scope_id":0}`, http.StatusBadRequest)
	h.expect(http.MethodPatch, path, `bad`, http.StatusBadRequest)
	h.expect(http.MethodPatch, "/api/admin/rate-limits/999", `{"rpm":1}`, http.StatusNotFound)
	h.expect(http.MethodPatch, "/api/admin/rate-limits/abc", `{"rpm":1}`, http.StatusBadRequest)

	h.expect(http.MethodDelete, path, "", http.StatusNoContent)
	h.expect(http.MethodDelete, "/api/admin/rate-limits/abc", "", http.StatusBadRequest)
	h.expect(http.MethodPut, path, "", http.StatusMethodNotAllowed)
	h.expect(http.MethodPost, path, `{}`, http.StatusMethodNotAllowed)
}

// The console lets an administrator retarget an existing rule, so the admin API
// has to persist scope, scope_id and model_id, normalise model rules to
// scope_id 0 and accept an explicit empty model_id ("every model").
func TestAdminRateLimitsRetargetAndNormalizeModelScope(t *testing.T) {
	h := newAdminHarness(t, AuthConfig{}, nil)

	// A model rule is always stored under scope_id 0: the runtime only loads
	// model rules through ListRateLimits("model", 0).
	created := h.expect(http.MethodPost, "/api/admin/rate-limits", `{"scope":"model","scope_id":9,"model_id":"chat","rpm":5}`, http.StatusCreated)
	var modelRule model.RateLimit
	if err := json.Unmarshal(created.Body.Bytes(), &modelRule); err != nil {
		t.Fatalf("decode created model rule: %v", err)
	}
	if modelRule.ScopeID != 0 {
		t.Fatalf("created model rule scope_id = %d, want 0", modelRule.ScopeID)
	}
	if stored := h.store.limits[modelRule.ID]; stored == nil || stored.Scope != "model" || stored.ScopeID != 0 {
		t.Fatalf("stored model rule = %+v, want scope \"model\" with scope_id 0", stored)
	}

	created = h.expect(http.MethodPost, "/api/admin/rate-limits", `{"scope":"user","scope_id":1,"model_id":"chat","rpm":10,"tpm":20,"concurrency":1}`, http.StatusCreated)
	var userRule model.RateLimit
	if err := json.Unmarshal(created.Body.Bytes(), &userRule); err != nil {
		t.Fatalf("decode created user rule: %v", err)
	}
	path := "/api/admin/rate-limits/" + itoa(int(userRule.ID))

	// Retarget every field the console exposes in one patch.
	patched := h.expect(http.MethodPatch, path, `{"scope":"key","scope_id":7,"model_id":"embed","rpm":30,"tpm":40,"concurrency":2,"active":false}`, http.StatusOK)
	var patchedRule model.RateLimit
	if err := json.Unmarshal(patched.Body.Bytes(), &patchedRule); err != nil {
		t.Fatalf("decode patched rule: %v", err)
	}
	want := model.RateLimit{ID: userRule.ID, Scope: "key", ScopeID: 7, ModelID: "embed", RPM: 30, TPM: 40, Concurrency: 2, Active: false}
	if patchedRule != want {
		t.Fatalf("patched rule = %+v, want %+v", patchedRule, want)
	}
	if stored := h.store.limits[userRule.ID]; stored == nil || *stored != want {
		t.Fatalf("stored rule = %+v, want %+v", stored, want)
	}

	// Switching a rule to model scope normalises scope_id to 0.
	h.expect(http.MethodPatch, path, `{"scope":"model","scope_id":4}`, http.StatusOK)
	if stored := h.store.limits[userRule.ID]; stored == nil || stored.ScopeID != 0 {
		t.Fatalf("model-scoped patch stored scope_id %v, want 0", stored)
	}

	// An explicit empty model_id clears the filter; an omitted one keeps it.
	cleared := h.expect(http.MethodPatch, path, `{"model_id":""}`, http.StatusOK)
	var clearedRule model.RateLimit
	if err := json.Unmarshal(cleared.Body.Bytes(), &clearedRule); err != nil {
		t.Fatalf("decode cleared rule: %v", err)
	}
	if clearedRule.ModelID != "" {
		t.Fatalf("model_id after clearing = %q, want empty", clearedRule.ModelID)
	}
	h.expect(http.MethodPatch, path, `{"model_id":"embed"}`, http.StatusOK)
	kept := h.expect(http.MethodPatch, path, `{"rpm":11}`, http.StatusOK)
	var keptRule model.RateLimit
	if err := json.Unmarshal(kept.Body.Bytes(), &keptRule); err != nil {
		t.Fatalf("decode kept rule: %v", err)
	}
	if keptRule.ModelID != "embed" {
		t.Fatalf("model_id after an unrelated patch = %q, want \"embed\"", keptRule.ModelID)
	}
}

func TestAdminRequestsAuditAndStatsEndpoints(t *testing.T) {
	h := newAdminHarness(t, AuthConfig{}, nil)

	h.expect(http.MethodGet, "/api/admin/requests", "", http.StatusOK)
	h.expect(http.MethodGet, "/api/admin/requests?model=chat&api=chat.completions&user_id=1&provider_id=2&api_key_id=3&provider_model=m&status=success&limit=10&offset=5", "", http.StatusOK)
	h.expect(http.MethodGet, "/api/admin/requests?status=bogus", "", http.StatusBadRequest)
	h.expect(http.MethodGet, "/api/admin/requests?user_id=x", "", http.StatusBadRequest)
	h.expect(http.MethodGet, "/api/admin/requests?model_group_id=x", "", http.StatusBadRequest)
	h.expect(http.MethodGet, "/api/admin/requests?provider_id=x", "", http.StatusBadRequest)
	h.expect(http.MethodGet, "/api/admin/requests?api_key_id=x", "", http.StatusBadRequest)
	h.expect(http.MethodGet, "/api/admin/requests?limit=x", "", http.StatusBadRequest)
	h.expect(http.MethodGet, "/api/admin/requests?offset=x", "", http.StatusBadRequest)
	h.expect(http.MethodGet, "/api/admin/requests?from=not-a-time", "", http.StatusBadRequest)
	h.expect(http.MethodGet, "/api/admin/requests?to=not-a-time", "", http.StatusBadRequest)
	h.expect(http.MethodGet, "/api/admin/requests?from=2024-01-02T03:04:05Z&to=2024-02-02T03:04:05Z", "", http.StatusOK)
	h.expect(http.MethodGet, "/api/admin/requests/req-1", "", http.StatusNotFound)
	h.expect(http.MethodPost, "/api/admin/requests", `{}`, http.StatusMethodNotAllowed)

	h.expect(http.MethodGet, "/api/admin/audit", "", http.StatusOK)
	h.expect(http.MethodGet, "/api/admin/audit?limit=10", "", http.StatusOK)
	h.expect(http.MethodGet, "/api/admin/audit?limit=not-a-number", "", http.StatusBadRequest)
	h.expect(http.MethodGet, "/api/admin/audit?limit=100000", "", http.StatusBadRequest)
	h.expect(http.MethodGet, "/api/admin/audit?limit=-1", "", http.StatusBadRequest)
	h.expect(http.MethodGet, "/api/admin/audit/extra", "", http.StatusMethodNotAllowed)
	h.expect(http.MethodPost, "/api/admin/audit", `{}`, http.StatusMethodNotAllowed)

	h.expect(http.MethodGet, "/api/admin/stats", "", http.StatusOK)
	h.expect(http.MethodGet, "/api/admin/stats?status=error&model=chat", "", http.StatusOK)
	h.expect(http.MethodGet, "/api/admin/stats?limit=bad", "", http.StatusBadRequest)
	h.expect(http.MethodPost, "/api/admin/stats", `{}`, http.StatusMethodNotAllowed)
}

func TestAdminContentEndpointRequiresCipherAndStoreSupport(t *testing.T) {
	// Without a cipher the endpoint reports that decryption is unavailable.
	noCipher := newAdminHarness(t, AuthConfig{}, nil)
	unavailable := noCipher.expect(http.MethodGet, "/api/admin/content/req-1", "", http.StatusServiceUnavailable)
	if !strings.Contains(unavailable.Body.String(), "encryption_unavailable") {
		t.Fatalf("content without cipher = %s", unavailable.Body.String())
	}
	noCipher.expect(http.MethodPost, "/api/admin/content/req-1", "", http.StatusMethodNotAllowed)
	noCipher.expect(http.MethodGet, "/api/admin/content", "", http.StatusMethodNotAllowed)

	// With a cipher but a store that cannot serve content logs, the handler
	// degrades to the "unsupported store" response.
	cipher, err := gatewaycrypto.NewCipher(strings.Repeat("c", 64))
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	withCipher := newAdminHarness(t, AuthConfig{}, cipher)
	unsupported := withCipher.expect(http.MethodGet, "/api/admin/content/req-1", "", http.StatusNotImplemented)
	if !strings.Contains(unsupported.Body.String(), "admin_store_unsupported") {
		t.Fatalf("content with unsupported store = %s", unsupported.Body.String())
	}
}

func TestAdminRuntimeEndpointReportsConfiguredStats(t *testing.T) {
	withoutStats := newAdminHarness(t, AuthConfig{}, nil)
	withoutStats.expect(http.MethodGet, "/api/admin/runtime", "", http.StatusNotImplemented)
	withoutStats.expect(http.MethodPost, "/api/admin/runtime", `{}`, http.StatusNotFound)
	withoutStats.expect(http.MethodGet, "/api/admin/runtime/extra", "", http.StatusNotFound)

	withStats := newAdminHarness(t, AuthConfig{RuntimeStats: func() any {
		return map[string]any{"records": map[string]any{"failed": 1}}
	}}, nil)
	body := withStats.expect(http.MethodGet, "/api/admin/runtime", "", http.StatusOK).Body.String()
	if !strings.Contains(body, `"records"`) {
		t.Fatalf("runtime payload = %s", body)
	}
}

func TestAdminIndexAndUnknownRoutes(t *testing.T) {
	h := newAdminHarness(t, AuthConfig{}, nil)

	// The mux redirects the bare collection path to its trailing-slash form.
	redirect := h.do(http.MethodGet, "/api/admin", "")
	if redirect.Code != http.StatusMovedPermanently {
		t.Fatalf("GET /api/admin = %d, want a 301 redirect to the index", redirect.Code)
	}
	index := h.expect(http.MethodGet, "/api/admin/", "", http.StatusOK)
	if !strings.Contains(index.Body.String(), "llm-gateway-admin") {
		t.Fatalf("admin index = %s", index.Body.String())
	}
	h.expect(http.MethodGet, "/api/admin/does-not-exist", "", http.StatusNotFound)

	// OPTIONS is answered without authentication or CSRF.
	request := httptest.NewRequest(http.MethodOptions, "/api/admin/users", nil)
	recorder := httptest.NewRecorder()
	h.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS /api/admin/users = %d, want 204", recorder.Code)
	}
}

// authOnlyStore satisfies the authentication and API-key interceptors but not
// the administrator resource collections, so every admin handler that asserts
// on a richer store must degrade instead of panicking.
type authOnlyStore struct{ *fakeAuthStore }

func (s *authOnlyStore) GetAPIKeyByHash(context.Context, string) (*model.APIKey, error) {
	return nil, store.ErrNotFound
}

func (s *authOnlyStore) TouchAPIKey(context.Context, int64) error { return nil }

func TestAdminUnsupportedStoreDegradesGracefully(t *testing.T) {
	passwordHash, hashErr := gatewaycrypto.HashPassword("password")
	if hashErr != nil {
		t.Fatalf("hash password: %v", hashErr)
	}
	adminStore := &authOnlyStore{newFakeAuthStore(&model.User{ID: 1, Username: "admin", DisplayName: "Admin", Role: model.RoleAdmin, PasswordHash: passwordHash, Active: true, PasswordLoginEnabled: true})}
	handler := NewHandlerWithGatewayAndCipher(nil, adminStore, adminStore, nil, AuthConfig{}, nil)
	cookie, csrf := adminTestSession(t, handler)

	for _, path := range []string{"users", "api-keys", "providers", "aliases", "prices", "rate-limits", "requests", "audit", "stats"} {
		request := httptest.NewRequest(http.MethodGet, "/api/admin/"+path, nil)
		request.AddCookie(cookie)
		request.Header.Set("X-CSRF-Token", csrf)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNotImplemented {
			t.Fatalf("GET /api/admin/%s against a partial store = %d (%s), want 501", path, recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), "admin_store_unsupported") {
			t.Fatalf("GET /api/admin/%s body = %s", path, recorder.Body.String())
		}
	}

	// Content logging additionally requires a cipher, which is not configured
	// here, so this path reports the encryption failure first.
	request := httptest.NewRequest(http.MethodGet, "/api/admin/content/req-1", nil)
	request.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /api/admin/content/req-1 without a cipher = %d (%s), want 503", recorder.Code, recorder.Body.String())
	}
}
