package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zjvtit/omni-gateway/internal/billing"
	gatewaycrypto "github.com/zjvtit/omni-gateway/internal/crypto"
	llmgateway "github.com/zjvtit/omni-gateway/internal/gateway"
	"github.com/zjvtit/omni-gateway/internal/model"
	"github.com/zjvtit/omni-gateway/internal/policy"
	"github.com/zjvtit/omni-gateway/internal/store"
)

// Every integration case creates and drops its own schema. The fixture URL is
// supplied externally, never written into a deliverable or printed by a test.
func productHTTPStore(t *testing.T) *store.Store {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL is required for isolated product HTTP integration tests")
	}
	ctx := context.Background()
	db, err := sql.Open("postgres", raw)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("product_http_%d", time.Now().UnixNano())
	if _, err = db.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		db.Close()
		t.Fatal("create isolated schema failed")
	}
	t.Cleanup(func() {
		_, err := db.ExecContext(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
		if err != nil {
			t.Error("drop isolated schema failed")
		}
		db.Close()
	})
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal("invalid test DB URL")
	}
	q := u.Query()
	q.Set("options", "-csearch_path="+schema)
	u.RawQuery = q.Encode()
	s, err := store.New(ctx, u.String())
	if err != nil {
		t.Fatal("open isolated store failed")
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}
func productUser(t *testing.T, s *store.Store, name string, role model.Role) int64 {
	t.Helper()
	hash, err := gatewaycrypto.HashPassword("fixture-password")
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.CreateUser(context.Background(), &model.User{Username: name, Role: role, PasswordHash: hash, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func productSession(t *testing.T, s *store.Store, id int64, suffix string) *http.Cookie {
	t.Helper()
	session := "fixture-session-" + suffix
	if err := s.CreateSession(context.Background(), session, id, "fixture-csrf", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: sessionCookieName, Value: session}
}
func productExpect(t *testing.T, h http.Handler, c *http.Cookie, method, path, body string, status int) *httptest.ResponseRecorder {
	t.Helper()
	w := writeSelf(t, h, c, "fixture-csrf", method, path, body)
	if w.Code != status {
		t.Fatalf("%s %s status=%d want=%d error=%s", method, path, w.Code, status, w.Body)
	}
	return w
}

func TestProductPostgresRetirementAllProtocolsAndPersistedAccess(t *testing.T) {
	s := productHTTPStore(t)
	ctx := context.Background()
	owner := productUser(t, s, "owner", model.RoleUser)
	admin := productUser(t, s, "admin", model.RoleAdmin)
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/chat/completions":
			fmt.Fprint(w, `{"id":"mock-chat","model":"upstream","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
		case "/v1/responses":
			fmt.Fprint(w, `{"id":"mock-resp","model":"upstream","status":"completed","output":[],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`)
		case "/v1/embeddings":
			fmt.Fprint(w, `{"model":"upstream","data":[{"object":"embedding","embedding":[0.1]}],"usage":{"prompt_tokens":3,"total_tokens":3}}`)
		case "/v1/images/generations":
			fmt.Fprint(w, `{"created":1,"data":[{"b64_json":"Zml4dHVyZQ=="}]}`)
		default:
			t.Errorf("unexpected local upstream path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	p := &model.Provider{Name: "mock", Type: model.ProviderOpenAICompat, BaseURL: upstream.URL + "/v1", Active: true, SupportsResponses: true, TimeoutMS: 1000}
	must := productIDs(t)
	p.ID = must(s.CreateProvider(ctx, p))
	alias := must(s.CreateAlias(ctx, "public"))
	must(s.AddRouteEntry(ctx, alias, p.ID, "upstream", 1))
	if err := s.SetAliasActive(ctx, alias, true); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertProviderModel(ctx, p.ID, "upstream", model.Capabilities{Streaming: true, Embeddings: true, ImagesGenerations: true}); err != nil {
		t.Fatal(err)
	}
	must(s.CreatePriceVersion(ctx, &model.PriceVersion{ModelID: "upstream", Version: 7, Cost: map[string]model.Money{"input": model.ZeroMoney, "output": model.ZeroMoney, "image": model.ZeroMoney}, Credits: map[string]model.Credits{"input": model.ZeroCredits, "output": model.ZeroCredits, "image": model.ZeroCredits}}))
	key := &model.APIKey{UserID: owner, Name: "own", KeyHash: gatewaycrypto.HashAPIKey("sk-local-product"), Active: true}
	key.ID = must(s.CreateAPIKey(ctx, key))
	group := &model.UserGroup{Name: "historical restricted", Active: false, UserIDs: []int64{owner}}
	group.ID = must(s.CreateUserGroup(ctx, group))
	zero := model.ZeroMoney
	budget := &model.BudgetRule{UserID: owner, WindowSecs: 60, CostLimit: &zero, CreditLimit: &zero, Active: true}
	budget.ID = must(s.CreateBudgetRule(ctx, budget))
	prices := billing.NewService(s, s)
	defer prices.Close(ctx)
	service := llmgateway.NewServiceWithDependencies(s, nil, upstream.Client(), s, prices, policy.NewService(s))
	defer service.Close(ctx)
	h := NewHandlerWithGateway(nil, s, s, service, AuthConfig{})
	ownerCookie := productSession(t, s, owner, "owner")
	adminCookie := productSession(t, s, admin, "admin")
	accessPath := fmt.Sprintf("/api/self/access?api_key_id=%d", key.ID)
	initial := productExpect(t, h, ownerCookie, "GET", accessPath, "", 200)
	if !strings.Contains(initial.Body.String(), `"first_success_at":null`) {
		t.Fatal("last used incorrectly described as success")
	}
	for _, tc := range []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"public","messages":[{"role":"user","content":"fixture"}]}`},
		{"/v1/responses", `{"model":"public","input":"fixture"}`},
		{"/v1/messages", `{"model":"public","messages":[{"role":"user","content":"fixture"}],"max_tokens":10}`},
		{"/v1/embeddings", `{"model":"public","input":"fixture"}`},
		{"/v1/images/generations", `{"model":"public","prompt":"fixture"}`},
	} {
		w := gatewayRequest(t, h, "POST", tc.path, "sk-local-product", tc.body)
		if w.Code != 200 {
			t.Fatalf("%s blocked by historical policies: %d %s", tc.path, w.Code, w.Body)
		}
	}
	if err := service.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 5 {
		t.Fatalf("local upstream calls=%d want=5", calls.Load())
	}
	history := productExpect(t, h, ownerCookie, "GET", "/api/self/requests?settlement_status=free", "", 200)
	var requests []*model.Request
	if err := json.Unmarshal(history.Body.Bytes(), &requests); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 5 {
		t.Fatalf("free requests=%d want5", len(requests))
	}
	for _, r := range requests {
		if r.SettlementStatus != "free" || len(r.Usage.Meters) == 0 {
			t.Fatalf("free usage/status lost: %+v", r)
		}
		ledger, err := s.ListLedgerEntries(ctx, r.ID)
		if err != nil || len(ledger) == 0 {
			t.Fatal("free ledger missing", err)
		}
		for _, e := range ledger {
			if e.Quantity <= 0 || e.PriceVersion != 7 || !e.Cost.IsZero() || !e.Credits.IsZero() {
				t.Fatalf("zero ledger lost context: %+v", e)
			}
		}
	}
	access := productExpect(t, h, ownerCookie, "GET", accessPath, "", 200)
	if strings.Contains(access.Body.String(), `"first_success_at":null`) || strings.Contains(access.Body.String(), "upstream") || !strings.Contains(access.Body.String(), "embeddings") || !strings.Contains(access.Body.String(), "images_generations") {
		t.Fatal(access.Body)
	}
	beforeCalls := calls.Load()
	productExpect(t, h, ownerCookie, "GET", accessPath, "", 200)
	if calls.Load() != beforeCalls {
		t.Fatal("access query invoked upstream")
	}
	for _, path := range []string{"/api/admin/user-groups", "/api/admin/budgets", fmt.Sprintf("/api/admin/user-groups/%d", group.ID), fmt.Sprintf("/api/admin/budgets/%d", budget.ID)} {
		for _, method := range []string{"GET", "POST", "PATCH", "DELETE"} {
			productExpect(t, h, adminCookie, method, path, `{"active":false}`, 404)
		}
	}
	groups, err := s.ListUserGroups(ctx)
	if err != nil || len(groups) != 1 || len(groups[0].UserIDs) != 1 {
		t.Fatal("historical groups changed", err)
	}
	budgets, err := s.ListBudgetRules(ctx, owner)
	if err != nil || len(budgets) != 1 || !budgets[0].Active {
		t.Fatal("historical budget changed", err)
	}
	key.ModelAllow = []string{"not-public"}
	if err := s.UpdateAPIKey(ctx, key); err != nil {
		t.Fatal(err)
	}
	if w := gatewayRequest(t, h, "POST", "/v1/embeddings", "sk-local-product", `{"model":"public","input":"fixture"}`); w.Code != 403 {
		t.Fatal(w.Code, w.Body)
	}
	if w := gatewayRequest(t, h, "GET", "/v1/models", "sk-local-product", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"data":[]`) {
		t.Fatal(w.Code, w.Body)
	}
	key.ModelAllow = nil
	key.Active = false
	if err := s.UpdateAPIKey(ctx, key); err != nil {
		t.Fatal(err)
	}
	if w := gatewayRequest(t, h, "GET", "/v1/models", "sk-local-product", ""); w.Code != 401 {
		t.Fatal(w.Code, w.Body)
	}
	key.Active = true
	expired := time.Now().Add(-time.Hour)
	key.ExpiresAt = &expired
	if err := s.UpdateAPIKey(ctx, key); err != nil {
		t.Fatal(err)
	}
	if w := gatewayRequest(t, h, "GET", "/v1/models", "sk-local-product", ""); w.Code != 401 {
		t.Fatal(w.Code, w.Body)
	}
	key.ExpiresAt = nil
	if err := s.UpdateAPIKey(ctx, key); err != nil {
		t.Fatal(err)
	}
	u, err := s.GetUserByID(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	u.Active = false
	if err := s.UpdateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if w := gatewayRequest(t, h, "GET", "/v1/models", "sk-local-product", ""); w.Code != 401 {
		t.Fatal(w.Code, w.Body)
	}
}

func productIDs(t *testing.T) func(int64, error) int64 {
	return func(id int64, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
}

func TestProductPostgresOwnershipAuditAndPasswordSetup(t *testing.T) {
	s := productHTTPStore(t)
	ctx := context.Background()
	admin := productUser(t, s, "admin", model.RoleAdmin)
	other := productUser(t, s, "other", model.RoleUser)
	adminCookie := productSession(t, s, admin, "admin")
	otherCookie := productSession(t, s, other, "other")
	h := NewHandlerWithAuth(nil, s, AuthConfig{})
	productExpect(t, h, adminCookie, "POST", "/api/admin/prices", `{"model_id":"precision","version":1,"cost":{"custom_audio":"0.000000000000000000001"},"credits":{"custom_audio":"0"}}`, 201)
	conflict := productExpect(t, h, adminCookie, "POST", "/api/admin/prices", `{"model_id":"precision","version":1,"cost":{"custom_audio":"0"},"credits":{"custom_audio":"0"}}`, 409)
	if !strings.Contains(conflict.Body.String(), "price_version_conflict") {
		t.Fatal("duplicate version error not distinct")
	}
	price := productExpect(t, h, adminCookie, "GET", "/api/admin/prices?model_id=precision&latest=true", "", 200)
	if !strings.Contains(price.Body.String(), "0.000000000000000000001") {
		t.Fatal("immutable rate precision overwritten")
	}
	priceAudit := productExpect(t, h, adminCookie, "GET", "/api/admin/audit?resource=prices&result=success", "", 200)
	if !strings.Contains(priceAudit.Body.String(), `"after":`) || !strings.Contains(priceAudit.Body.String(), "custom_audio") {
		t.Fatal("safe price audit missing")
	}
	newKey := productExpect(t, h, adminCookie, "POST", "/api/admin/api-keys", `{"name":"admin-own"}`, 201)
	var keyCreated struct {
		Key model.APIKey `json:"key"`
	}
	if err := json.Unmarshal(newKey.Body.Bytes(), &keyCreated); err != nil || keyCreated.Key.UserID != admin {
		t.Fatal("admin key not owned by session")
	}
	forgedCase := productExpect(t, h, adminCookie, "PATCH", fmt.Sprintf("/api/admin/api-keys/%d", keyCreated.Key.ID), fmt.Sprintf(`{"User_ID":%d}`, other), 400)
	if !strings.Contains(forgedCase.Body.String(), "key_owner_immutable") {
		t.Fatal("case-insensitive ownership edit accepted")
	}
	productExpect(t, h, adminCookie, "DELETE", fmt.Sprintf("/api/admin/api-keys/%d", keyCreated.Key.ID), "", 204)
	key := &model.APIKey{UserID: other, Name: "other-key", Active: true, KeyHash: gatewaycrypto.HashAPIKey("fixture-key"), ModelAllow: []string{"limited"}}
	key.ID = productIDs(t)(s.CreateAPIKey(ctx, key))
	keyPath := fmt.Sprintf("/api/admin/api-keys/%d", key.ID)
	productExpect(t, h, adminCookie, "POST", "/api/admin/api-keys", fmt.Sprintf(`{"user_id":%d,"name":"forged"}`, other), 403)
	productExpect(t, h, otherCookie, "POST", "/api/self/api-keys", fmt.Sprintf(`{"user_id":%d,"name":"forged"}`, admin), 403)
	productExpect(t, h, adminCookie, "PATCH", keyPath, fmt.Sprintf(`{"user_id":%d}`, admin), 400)
	for _, body := range []string{`{"name":"not-owner"}`, `{"expires_at":null}`, `{"model_allow":[]}`, `{"active":true}`, `{"active":false,"name":"not-owner"}`} {
		productExpect(t, h, adminCookie, "PATCH", keyPath, body, 403)
	}
	productExpect(t, h, adminCookie, "DELETE", keyPath, "", 403)
	productExpect(t, h, adminCookie, "PATCH", keyPath, `{"active":false}`, 200)
	stored, err := s.GetAPIKeyByID(ctx, key.ID)
	if err != nil || stored.UserID != other || stored.Active || stored.Name != "other-key" || len(stored.ModelAllow) != 1 {
		t.Fatal("forbidden edits changed persistence", err)
	}
	selfPath := fmt.Sprintf("/api/self/api-keys/%d", key.ID)
	productExpect(t, h, otherCookie, "PATCH", selfPath, `{"name":"renewed","expires_at":"2999-01-01T00:00:00Z","model_allow":[],"active":true}`, 200)
	productExpect(t, h, otherCookie, "PATCH", selfPath, `{"expires_at":null}`, 200)
	stored, err = s.GetAPIKeyByID(ctx, key.ID)
	if err != nil || stored.ExpiresAt != nil || !stored.Active || stored.Name != "renewed" || len(stored.ModelAllow) != 0 {
		t.Fatal("owner lifecycle not persisted", err)
	}
	productExpect(t, h, adminCookie, "GET", keyPath, "", 200)
	denied := productExpect(t, h, adminCookie, "GET", "/api/admin/audit?resource=api-keys&result=denied&limit=2&offset=0", "", 200)
	var entries []*model.AuditEntry
	_ = json.Unmarshal(denied.Body.Bytes(), &entries)
	if len(entries) != 2 || entries[0].Result != "denied" || entries[1].Result != "denied" {
		t.Fatal(denied.Body)
	}
	page2 := productExpect(t, h, adminCookie, "GET", "/api/admin/audit?resource=api-keys&result=denied&limit=2&offset=2", "", 200)
	if page2.Body.String() == denied.Body.String() {
		t.Fatal("audit pagination repeated rows")
	}
	for _, secret := range []string{"fixture-key", "key_hash", "ciphertext", "password_hash"} {
		if strings.Contains(denied.Body.String(), secret) {
			t.Fatal("secret in audit")
		}
	}
	productExpect(t, h, otherCookie, "DELETE", selfPath, "", 204)
	if _, err := s.GetAPIKeyByID(ctx, key.ID); err != store.ErrNotFound {
		t.Fatal("owner delete failed", err)
	}
	sso, err := s.GetOrCreateOIDCUser(ctx, "https://fixture.invalid", "subject", &model.User{Username: "sso-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	ssoCookie := productSession(t, s, sso.ID, "sso")
	_ = productSession(t, s, sso.ID, "sso-other")
	before := productExpect(t, h, ssoCookie, "GET", "/api/auth/me", "", 200)
	if !strings.Contains(before.Body.String(), `"can_set_without_current":true`) {
		t.Fatal(before.Body)
	}
	productExpect(t, h, ssoCookie, "POST", "/api/self/password", fmt.Sprintf(`{"new_password":"new-fixture-password","user_id":%d}`, admin), 204)
	after := productExpect(t, h, ssoCookie, "GET", "/api/auth/me", "", 200)
	if !strings.Contains(after.Body.String(), `"local_enabled":true`) || strings.Contains(after.Body.String(), `"can_set_without_current":true`) {
		t.Fatal(after.Body)
	}
	if _, _, _, err := s.GetSession(ctx, "fixture-session-sso-other"); err != store.ErrNotFound {
		t.Fatal("other SSO session survived", err)
	}
	bound, err := s.GetUserByOIDC(ctx, "https://fixture.invalid", "subject")
	if err != nil || bound.ID != sso.ID || !bound.PasswordLoginEnabled {
		t.Fatal("OIDC binding or local capability lost", err)
	}
	login := writeSelf(t, h, nil, "", "POST", "/api/auth/login", `{"username":"sso-fixture","password":"new-fixture-password"}`)
	if login.Code != 200 {
		t.Fatal(login.Code, login.Body)
	}
	productExpect(t, h, ssoCookie, "POST", "/api/self/password", `{"new_password":"another-fixture-password"}`, 400)
	productExpect(t, h, ssoCookie, "POST", "/api/self/password", `{"current_password":"wrong","new_password":"another-fixture-password"}`, 403)
}

func TestProductPostgresRequestFiltersPagingAndSafeOwnership(t *testing.T) {
	s := productHTTPStore(t)
	ctx := context.Background()
	must := productIDs(t)
	admin := productUser(t, s, "admin", model.RoleAdmin)
	owner := productUser(t, s, "owner", model.RoleUser)
	other := productUser(t, s, "other", model.RoleUser)
	ownerKey := &model.APIKey{UserID: owner, Name: "owner", KeyHash: gatewaycrypto.HashAPIKey("owner-filter"), Active: true}
	ownerKey.ID = must(s.CreateAPIKey(ctx, ownerKey))
	otherKey := &model.APIKey{UserID: other, Name: "other", KeyHash: gatewaycrypto.HashAPIKey("other-filter"), Active: true}
	otherKey.ID = must(s.CreateAPIKey(ctx, otherKey))
	alias := must(s.CreateAlias(ctx, "filter-model"))
	group := &model.ModelGroup{Name: "classification", Active: true, AliasIDs: []int64{alias}}
	group.ID = must(s.CreateModelGroup(ctx, group))
	at := time.Date(2026, 10, 3, 3, 0, 0, 0, time.UTC)
	for i, state := range []string{"free", "credits_free", "settled", "unpriced", "failed", "none"} {
		id := fmt.Sprintf("own-%d", i)
		r := &model.Request{ID: id, UserID: owner, APIKeyID: ownerKey.ID, API: "embeddings", ModelAlias: "filter-model", Status: model.StatusSuccess, StatusCode: 200, CreatedAt: at.Add(time.Duration(i) * time.Minute), Usage: model.Usage{Meters: map[string]int64{"input": 5, "total": 5}}}
		if state == "failed" {
			r.Status = model.StatusError
			r.StatusCode = 502
			r.ErrorType = "upstream"
			r.ErrorSummary = "fixture raw secret https://internal.invalid"
			r.Tags = map[string]string{"hidden": "fixture-tag"}
			r.ClientIP = "fixture-private"
		}
		if err := s.InsertRequest(ctx, r); err != nil {
			t.Fatal(err)
		}
		a := &model.Attempt{RequestID: id, SettlementID: id + "-attempt", ProviderID: 17, ProviderModel: "internal-model", Status: string(r.Status), StatusCode: r.StatusCode, CreatedAt: r.CreatedAt, SettlementStatus: state, Usage: r.Usage}
		if state == "unpriced" {
			a.SettlementError = "unpriced"
		}
		if state == "failed" {
			a.SettlementError = "settlement_failed"
		}
		if err := s.InsertAttempt(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.InsertRequest(ctx, &model.Request{ID: "foreign-request", UserID: other, APIKeyID: otherKey.ID, ModelAlias: "other-model", API: "responses", Status: model.StatusSuccess, CreatedAt: at.Add(10 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	h := NewHandlerWithAuth(nil, s, AuthConfig{})
	adminCookie := productSession(t, s, admin, "admin")
	ownerCookie := productSession(t, s, owner, "owner")
	for _, state := range []string{"free", "credits_free", "settled", "unpriced", "failed", "none"} {
		q := fmt.Sprintf("/api/admin/requests?settlement_status=%s&user_id=%d&api_key_id=%d&model=filter-model&model_group_id=%d&api=embeddings&provider_id=17&from=2026-10-03T03:00:00Z&to=2026-10-03T03:05:00Z", state, owner, ownerKey.ID, group.ID)
		w := productExpect(t, h, adminCookie, "GET", q, "", 200)
		var rows []*model.Request
		if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil || len(rows) != 1 || rows[0].SettlementStatus != state {
			t.Fatalf("%s filter did not agree with projection: %s", state, w.Body)
		}
		stats := productExpect(t, h, adminCookie, "GET", strings.Replace(q, "/requests?", "/stats?", 1), "", 200)
		if !strings.Contains(stats.Body.String(), `"requests":1`) {
			t.Fatal("stats/request filter mismatch", stats.Body)
		}
		productExpect(t, h, adminCookie, "GET", "/api/admin/requests/"+rows[0].ID, "", 200)
	}
	page := productExpect(t, h, ownerCookie, "GET", "/api/self/requests?limit=2&offset=1&user_id="+fmt.Sprint(other), "", 200)
	var rows []*model.Request
	if err := json.Unmarshal(page.Body.Bytes(), &rows); err != nil || len(rows) != 2 || rows[0].ID != "own-4" || rows[1].ID != "own-3" {
		t.Fatal("pagination or account scope mismatch", page.Body)
	}
	for _, forbidden := range []string{"fixture raw secret", "internal.invalid", "fixture-tag", "fixture-private", "internal-model", "foreign-request"} {
		if strings.Contains(page.Body.String(), forbidden) {
			t.Fatal("unsafe self diagnostic", page.Body)
		}
	}
	if rows[0].ErrorSummary == "" || rows[0].ErrorSuggestion == "" || rows[0].StatusCode != 502 || rows[0].SettlementStatus != "failed" {
		t.Fatal("safe diagnosis missing", page.Body)
	}
	exact := productExpect(t, h, adminCookie, "GET", "/api/admin/requests?request_id=own-4", "", 200)
	if err := json.Unmarshal(exact.Body.Bytes(), &rows); err != nil || len(rows) != 1 || rows[0].ID != "own-4" {
		t.Fatal("exact request lookup wrong", exact.Body)
	}
	foreign := productExpect(t, h, ownerCookie, "GET", "/api/self/requests?request_id=foreign-request", "", 200)
	if strings.TrimSpace(foreign.Body.String()) != "[]" {
		t.Fatal("foreign request disclosed", foreign.Body)
	}
	productExpect(t, h, ownerCookie, "GET", fmt.Sprintf("/api/self/requests?api_key_id=%d", otherKey.ID), "", 404)
	inclusive := productExpect(t, h, ownerCookie, "GET", "/api/self/requests?from=2026-10-03T03:04:00Z&to=2026-10-03T03:04:00Z&status=error", "", 200)
	if err := json.Unmarshal(inclusive.Body.Bytes(), &rows); err != nil || len(rows) != 1 || rows[0].ID != "own-4" {
		t.Fatal("UTC inclusive bounds incorrect", inclusive.Body)
	}
	for _, q := range []string{"offset=-1", "limit=201", "status=other", "settlement_status=other", "from=bad", "from=2026-10-04T00:00:00Z&to=2026-10-03T00:00:00Z"} {
		productExpect(t, h, ownerCookie, "GET", "/api/self/requests?"+q, "", 400)
		productExpect(t, h, adminCookie, "GET", "/api/admin/requests?"+q, "", 400)
	}
}
