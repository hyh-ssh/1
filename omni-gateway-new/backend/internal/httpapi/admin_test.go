package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	gatewaycrypto "github.com/zjvtit/omni-gateway/internal/crypto"
	"github.com/zjvtit/omni-gateway/internal/model"
	"github.com/zjvtit/omni-gateway/internal/store"
)

type adminTestStore struct {
	*fakeAuthStore
	mu        sync.Mutex
	keys      map[int64]*model.APIKey
	providers map[int64]*model.Provider
	aliases   map[int64]*model.ModelAlias
	routes    map[int64][]*model.RouteEntry
	prices    map[string][]*model.PriceVersion
	budgets   map[int64]*model.BudgetRule
	limits    map[int64]*model.RateLimit
	usersNext int64
	keysNext  int64
	provNext  int64
	aliasNext int64
	routeNext int64
	audit     []*model.AuditEntry
}

func newAdminTestStore() *adminTestStore {
	passwordHash, _ := gatewaycrypto.HashPassword("password")
	base := newFakeAuthStore(&model.User{ID: 1, Username: "admin", DisplayName: "Admin", Role: model.RoleAdmin, PasswordHash: passwordHash, Active: true, PasswordLoginEnabled: true})
	return &adminTestStore{
		fakeAuthStore: base,
		keys:          map[int64]*model.APIKey{},
		providers:     map[int64]*model.Provider{},
		aliases:       map[int64]*model.ModelAlias{},
		routes:        map[int64][]*model.RouteEntry{},
		prices:        map[string][]*model.PriceVersion{},
		budgets:       map[int64]*model.BudgetRule{},
		limits:        map[int64]*model.RateLimit{},
		usersNext:     1,
		keysNext:      1,
		provNext:      1,
		aliasNext:     1,
		routeNext:     1,
	}
}

func (s *adminTestStore) ListUsers(context.Context) ([]*model.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*model.User, 0, len(s.usersByID))
	for _, user := range s.usersByID {
		out = append(out, user)
	}
	return out, nil
}

func (s *adminTestStore) CreateUser(_ context.Context, user *model.User) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usersNext++
	user.ID = s.usersNext
	s.usersByID[user.ID] = user
	s.usersByName[user.Username] = user
	return user.ID, nil
}

func (s *adminTestStore) UpdateUser(_ context.Context, user *model.User) error { return nil }
func (s *adminTestStore) SetUserPassword(_ context.Context, id int64, hash string) error {
	if user := s.usersByID[id]; user != nil {
		user.PasswordHash = hash
	}
	return nil
}

func (s *adminTestStore) ListAPIKeys(context.Context) ([]*model.APIKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*model.APIKey, 0, len(s.keys))
	for _, key := range s.keys {
		out = append(out, key)
	}
	return out, nil
}
func (s *adminTestStore) ListAPIKeysByUser(_ context.Context, userID int64) ([]*model.APIKey, error) {
	all, _ := s.ListAPIKeys(context.Background())
	out := make([]*model.APIKey, 0)
	for _, key := range all {
		if key.UserID == userID {
			out = append(out, key)
		}
	}
	return out, nil
}
func (s *adminTestStore) GetAPIKeyByID(_ context.Context, id int64) (*model.APIKey, error) {
	key := s.keys[id]
	if key == nil {
		return nil, store.ErrNotFound
	}
	return key, nil
}
func (s *adminTestStore) CreateAPIKey(_ context.Context, key *model.APIKey) (int64, error) {
	s.keysNext++
	key.ID = s.keysNext
	s.keys[key.ID] = key
	return key.ID, nil
}
func (s *adminTestStore) UpdateAPIKey(_ context.Context, key *model.APIKey) error {
	s.keys[key.ID] = key
	return nil
}
func (s *adminTestStore) DeleteAPIKey(_ context.Context, id int64) error {
	delete(s.keys, id)
	return nil
}
func (s *adminTestStore) GetAPIKeyByHash(_ context.Context, hash string) (*model.APIKey, error) {
	for _, key := range s.keys {
		if key.KeyHash == hash {
			return key, nil
		}
	}
	return nil, store.ErrNotFound
}
func (s *adminTestStore) TouchAPIKey(context.Context, int64) error { return nil }

func (s *adminTestStore) ListProviders(context.Context) ([]*model.Provider, error) {
	out := make([]*model.Provider, 0, len(s.providers))
	for _, provider := range s.providers {
		out = append(out, provider)
	}
	return out, nil
}
func (s *adminTestStore) GetProvider(_ context.Context, id int64) (*model.Provider, error) {
	provider := s.providers[id]
	if provider == nil {
		return nil, store.ErrNotFound
	}
	return provider, nil
}
func (s *adminTestStore) CreateProvider(_ context.Context, provider *model.Provider) (int64, error) {
	s.provNext++
	provider.ID = s.provNext
	s.providers[provider.ID] = provider
	return provider.ID, nil
}
func (s *adminTestStore) UpdateProvider(_ context.Context, provider *model.Provider) error {
	s.providers[provider.ID] = provider
	return nil
}
func (s *adminTestStore) DeleteProvider(_ context.Context, id int64) error {
	delete(s.providers, id)
	return nil
}
func (s *adminTestStore) ListProviderModels(context.Context, int64) ([]*model.ProviderModel, error) {
	return nil, nil
}
func (s *adminTestStore) UpsertProviderModel(context.Context, int64, string, model.Capabilities) error {
	return nil
}

func (s *adminTestStore) ListAliases(context.Context) ([]*model.ModelAlias, error) {
	out := make([]*model.ModelAlias, 0, len(s.aliases))
	for _, alias := range s.aliases {
		out = append(out, alias)
	}
	return out, nil
}
func (s *adminTestStore) GetAlias(_ context.Context, name string) (*model.ModelAlias, error) {
	for _, alias := range s.aliases {
		if alias.Name == name {
			return alias, nil
		}
	}
	return nil, store.ErrNotFound
}
func (s *adminTestStore) CreateAlias(_ context.Context, name string) (int64, error) {
	s.aliasNext++
	s.aliases[s.aliasNext] = &model.ModelAlias{ID: s.aliasNext, Name: name, Active: true}
	return s.aliasNext, nil
}
func (s *adminTestStore) SetAliasActive(_ context.Context, id int64, active bool) error {
	alias, ok := s.aliases[id]
	if !ok {
		return store.ErrNotFound
	}
	alias.Active = active
	return nil
}
func (s *adminTestStore) RenameAlias(_ context.Context, id int64, name string) error {
	alias, ok := s.aliases[id]
	if !ok {
		return store.ErrNotFound
	}
	for otherID, other := range s.aliases {
		if otherID != id && other.Name == name {
			return store.ErrAliasConflict
		}
	}
	alias.Name = name
	return nil
}
func (s *adminTestStore) DeleteAlias(_ context.Context, id int64) error {
	if _, ok := s.aliases[id]; !ok {
		return store.ErrNotFound
	}
	delete(s.aliases, id)
	delete(s.routes, id)
	return nil
}
func (s *adminTestStore) AddRouteEntry(_ context.Context, aliasID, providerID int64, providerModel string, priority int) (int64, error) {
	s.routeNext++
	entry := &model.RouteEntry{ID: s.routeNext, AliasID: aliasID, ProviderID: providerID, ProviderModel: providerModel, Priority: priority, Active: true}
	s.routes[aliasID] = append(s.routes[aliasID], entry)
	return entry.ID, nil
}
func (s *adminTestStore) ListRouteEntries(_ context.Context, aliasID int64) ([]*model.RouteEntry, error) {
	return s.routes[aliasID], nil
}
func (s *adminTestStore) DeleteRouteEntry(_ context.Context, id int64) error { return nil }
func (s *adminTestStore) SetRouteEntryActive(_ context.Context, id int64, active bool) error {
	for _, entries := range s.routes {
		for _, entry := range entries {
			if entry.ID == id {
				entry.Active = active
			}
		}
	}
	return nil
}

func (s *adminTestStore) CreatePriceVersion(_ context.Context, price *model.PriceVersion) (int64, error) {
	s.prices[price.ModelID] = append(s.prices[price.ModelID], price)
	return int64(len(s.prices[price.ModelID])), nil
}
func (s *adminTestStore) GetLatestPriceVersion(_ context.Context, modelID string) (*model.PriceVersion, error) {
	values := s.prices[modelID]
	if len(values) == 0 {
		return nil, store.ErrNotFound
	}
	return values[len(values)-1], nil
}
func (s *adminTestStore) ListPriceVersions(_ context.Context, modelID string) ([]*model.PriceVersion, error) {
	return s.prices[modelID], nil
}
func (s *adminTestStore) ListAllPriceVersions(context.Context) ([]*model.PriceVersion, error) {
	out := make([]*model.PriceVersion, 0)
	for _, values := range s.prices {
		out = append(out, values...)
	}
	return out, nil
}

func (s *adminTestStore) ListBudgetRules(_ context.Context, userID int64) ([]*model.BudgetRule, error) {
	out := make([]*model.BudgetRule, 0)
	for _, rule := range s.budgets {
		if rule.UserID == userID {
			out = append(out, rule)
		}
	}
	return out, nil
}
func (s *adminTestStore) GetBudgetRule(_ context.Context, id int64) (*model.BudgetRule, error) {
	rule := s.budgets[id]
	if rule == nil {
		return nil, store.ErrNotFound
	}
	return rule, nil
}
func (s *adminTestStore) CreateBudgetRule(_ context.Context, rule *model.BudgetRule) (int64, error) {
	id := int64(len(s.budgets) + 1)
	rule.ID = id
	s.budgets[id] = rule
	return id, nil
}
func (s *adminTestStore) UpdateBudgetRule(_ context.Context, rule *model.BudgetRule) error {
	s.budgets[rule.ID] = rule
	return nil
}
func (s *adminTestStore) DeleteBudgetRule(_ context.Context, id int64) error {
	delete(s.budgets, id)
	return nil
}
func (s *adminTestStore) ListAllBudgetRules(context.Context) ([]*model.BudgetRule, error) {
	out := make([]*model.BudgetRule, 0, len(s.budgets))
	for _, rule := range s.budgets {
		out = append(out, rule)
	}
	return out, nil
}
func (s *adminTestStore) SumLedgerInWindow(context.Context, int64, int64) (model.Money, model.Credits, error) {
	return model.ZeroMoney, model.ZeroMoney, nil
}

func (s *adminTestStore) ListAllRateLimits(context.Context) ([]*model.RateLimit, error) {
	out := make([]*model.RateLimit, 0, len(s.limits))
	for _, rule := range s.limits {
		out = append(out, rule)
	}
	return out, nil
}

func (s *adminTestStore) GetRateLimit(_ context.Context, id int64) (*model.RateLimit, error) {
	rule := s.limits[id]
	if rule == nil {
		return nil, store.ErrNotFound
	}
	return rule, nil
}
func (s *adminTestStore) ListRateLimits(_ context.Context, scope string, scopeID int64) ([]*model.RateLimit, error) {
	out := make([]*model.RateLimit, 0)
	for _, rule := range s.limits {
		if rule.Scope == scope && rule.ScopeID == scopeID {
			out = append(out, rule)
		}
	}
	return out, nil
}
func (s *adminTestStore) CreateRateLimit(_ context.Context, rule *model.RateLimit) (int64, error) {
	id := int64(len(s.limits) + 1)
	rule.ID = id
	s.limits[id] = rule
	return id, nil
}
func (s *adminTestStore) UpdateRateLimit(_ context.Context, rule *model.RateLimit) error {
	s.limits[rule.ID] = rule
	return nil
}
func (s *adminTestStore) DeleteRateLimit(_ context.Context, id int64) error {
	delete(s.limits, id)
	return nil
}

func (s *adminTestStore) ListRequests(context.Context, model.StatsFilter) ([]*model.Request, error) {
	return nil, nil
}
func (s *adminTestStore) GetRequest(context.Context, string) (*model.Request, error) {
	return nil, store.ErrNotFound
}
func (s *adminTestStore) ListAttempts(context.Context, string) ([]*model.Attempt, error) {
	return nil, nil
}
func (s *adminTestStore) ListAuditFiltered(context.Context, model.AuditFilter) ([]*model.AuditEntry, error) {
	return s.audit, nil
}
func (s *adminTestStore) Stats(context.Context, model.StatsFilter) (*model.StatsSummary, error) {
	return &model.StatsSummary{}, nil
}
func (s *adminTestStore) InsertAudit(_ context.Context, entry *model.AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audit = append(s.audit, entry)
	return nil
}

func TestAdminAPIRequiresSessionAndCSRF(t *testing.T) {
	store := newAdminTestStore()
	cipher, err := gatewaycrypto.NewCipher(strings.Repeat("a", 64))
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	handler := NewHandlerWithGatewayAndCipher(nil, store, store, nil, AuthConfig{}, cipher)
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/admin/users", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("got unauthenticated status %d", unauthorized.Code)
	}

	login := httptest.NewRecorder()
	handler.ServeHTTP(login, httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"admin","password":"password"}`)))
	if login.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	var loginBody struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &loginBody); err != nil || loginBody.CSRF == "" {
		t.Fatalf("missing CSRF token: %s", login.Body.String())
	}

	withoutCSRF := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/admin/users", strings.NewReader(`{"username":"alice","password":"secret"}`))
	request.AddCookie(cookie)
	handler.ServeHTTP(withoutCSRF, request)
	if withoutCSRF.Code != http.StatusForbidden {
		t.Fatalf("got no-CSRF status %d", withoutCSRF.Code)
	}

	withCSRF := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/admin/users", strings.NewReader(`{"username":"alice","display_name":"Alice","password":"secret"}`))
	request.AddCookie(cookie)
	request.Header.Set("X-CSRF-Token", loginBody.CSRF)
	handler.ServeHTTP(withCSRF, request)
	if withCSRF.Code != http.StatusCreated {
		t.Fatalf("create user failed: %d %s", withCSRF.Code, withCSRF.Body.String())
	}
}

func TestAdminAPIEncryptsProviderCredentialsAndReturnsAPIKeyOnce(t *testing.T) {
	admin := newAdminTestStore()
	cipher, _ := gatewaycrypto.NewCipher(strings.Repeat("b", 64))
	handler := NewHandlerWithGatewayAndCipher(nil, admin, admin, nil, AuthConfig{}, cipher)
	login := httptest.NewRecorder()
	handler.ServeHTTP(login, httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"admin","password":"password"}`)))
	cookie := login.Result().Cookies()[0]
	var loginBody struct {
		CSRF string `json:"csrf_token"`
	}
	_ = json.Unmarshal(login.Body.Bytes(), &loginBody)

	keyRecorder := httptest.NewRecorder()
	keyRequest := httptest.NewRequest(http.MethodPost, "/api/admin/api-keys", strings.NewReader(`{"user_id":1,"name":"cli"}`))
	keyRequest.AddCookie(cookie)
	keyRequest.Header.Set("X-CSRF-Token", loginBody.CSRF)
	handler.ServeHTTP(keyRecorder, keyRequest)
	if keyRecorder.Code != http.StatusCreated || !strings.Contains(keyRecorder.Body.String(), `"secret":"sk-llm-`) {
		t.Fatalf("create key failed: %d %s", keyRecorder.Code, keyRecorder.Body.String())
	}

	providerRecorder := httptest.NewRecorder()
	providerRequest := httptest.NewRequest(http.MethodPost, "/api/admin/providers", strings.NewReader(`{"name":"OpenAI","type":"openai_compatible","base_url":"https://api.example.com/v1","api_key":"secret-key"}`))
	providerRequest.AddCookie(cookie)
	providerRequest.Header.Set("X-CSRF-Token", loginBody.CSRF)
	handler.ServeHTTP(providerRecorder, providerRequest)
	if providerRecorder.Code != http.StatusCreated {
		t.Fatalf("create provider failed: %d %s", providerRecorder.Code, providerRecorder.Body.String())
	}
	provider := admin.providers[admin.provNext]
	if provider == nil || provider.APIKeyEnc == "" {
		t.Fatal("provider credential was not encrypted")
	}
	decrypted, err := cipher.Decrypt(provider.APIKeyEnc)
	if err != nil || decrypted != "secret-key" {
		t.Fatalf("could not decrypt provider credential: %q %v", decrypted, err)
	}
}

func adminTestSession(t *testing.T, handler http.Handler) (*http.Cookie, string) {
	t.Helper()
	login := httptest.NewRecorder()
	handler.ServeHTTP(login, httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"admin","password":"password"}`)))
	if login.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", login.Code, login.Body.String())
	}
	var body struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &body); err != nil || body.CSRF == "" {
		t.Fatalf("missing CSRF token: %s", login.Body.String())
	}
	return login.Result().Cookies()[0], body.CSRF
}

func TestAdminAPIRenamesAndDeletesAliases(t *testing.T) {
	admin := newAdminTestStore()
	handler := NewHandlerWithGatewayAndCipher(nil, admin, admin, nil, AuthConfig{}, nil)
	cookie, csrf := adminTestSession(t, handler)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		var reader *strings.Reader
		if body == "" {
			reader = strings.NewReader("")
		} else {
			reader = strings.NewReader(body)
		}
		request := httptest.NewRequest(method, path, reader)
		request.AddCookie(cookie)
		request.Header.Set("X-CSRF-Token", csrf)
		handler.ServeHTTP(recorder, request)
		return recorder
	}
	createAlias := func(name string) int64 {
		t.Helper()
		recorder := do(http.MethodPost, "/api/admin/aliases", `{"name":"`+name+`"}`)
		if recorder.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", name, recorder.Code, recorder.Body.String())
		}
		var created struct {
			ID int64 `json:"id"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil || created.ID <= 0 {
			t.Fatalf("create %s: bad body %s", name, recorder.Body.String())
		}
		return created.ID
	}
	chatID := createAlias("chat")
	visionID := createAlias("vision")
	chatPath := fmt.Sprintf("/api/admin/aliases/%d", chatID)
	visionPath := fmt.Sprintf("/api/admin/aliases/%d", visionID)

	rename := do(http.MethodPatch, chatPath, `{"name":"chat-v2"}`)
	if rename.Code != http.StatusOK || !strings.Contains(rename.Body.String(), `"name":"chat-v2"`) {
		t.Fatalf("rename: %d %s", rename.Code, rename.Body.String())
	}
	if admin.aliases[chatID].Name != "chat-v2" {
		t.Fatalf("alias not renamed in store: %+v", admin.aliases[1])
	}

	conflict := do(http.MethodPatch, chatPath, `{"name":"vision"}`)
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "alias_conflict") {
		t.Fatalf("expected 409 alias_conflict, got %d %s", conflict.Code, conflict.Body.String())
	}

	toggle := do(http.MethodPatch, chatPath, `{"active":false}`)
	if toggle.Code != http.StatusOK || !strings.Contains(toggle.Body.String(), `"active":false`) {
		t.Fatalf("toggle: %d %s", toggle.Code, toggle.Body.String())
	}
	if admin.aliases[chatID].Active {
		t.Fatal("alias still active after toggle")
	}

	if code := do(http.MethodPatch, chatPath, `{}`).Code; code != http.StatusBadRequest {
		t.Fatalf("empty patch should be rejected: %d", code)
	}

	if code := do(http.MethodDelete, visionPath, "").Code; code != http.StatusNoContent {
		t.Fatalf("delete vision: %d", code)
	}
	if _, ok := admin.aliases[visionID]; ok {
		t.Fatal("deleted alias still present")
	}
	if code := do(http.MethodDelete, "/api/admin/aliases/999", "").Code; code != http.StatusNotFound {
		t.Fatalf("delete missing alias should 404: %d", code)
	}

	// Renaming an alias onto the name it already has is a no-op, not a conflict.
	sameName := do(http.MethodPatch, chatPath, `{"name":"chat-v2"}`)
	if sameName.Code != http.StatusOK {
		t.Fatalf("rename to the same name should succeed: %d %s", sameName.Code, sameName.Body.String())
	}

	// A missing id is reported the same way for rename and for the active toggle.
	if code := do(http.MethodPatch, "/api/admin/aliases/999999", `{"name":"ghost"}`).Code; code != http.StatusNotFound {
		t.Fatalf("rename of a missing alias should 404: %d", code)
	}
	if code := do(http.MethodPatch, "/api/admin/aliases/999999", `{"active":false}`).Code; code != http.StatusNotFound {
		t.Fatalf("toggle of a missing alias should 404: %d", code)
	}

	// Deleting an alias that still owns route entries succeeds and cascades.
	admin.providers[1] = &model.Provider{ID: 1, Name: "existing", Active: true, Type: model.ProviderOpenAICompat}
	if code := do(http.MethodPost, chatPath+"/routes", `{"provider_id":1,"provider_model":"gpt-4o-mini","priority":10}`).Code; code != http.StatusCreated {
		t.Fatalf("add route entry should be 201: %d", code)
	}
	deleteWithRoutes := do(http.MethodDelete, chatPath, "")
	if deleteWithRoutes.Code != http.StatusNoContent {
		t.Fatalf("delete alias with route entries: %d %s", deleteWithRoutes.Code, deleteWithRoutes.Body.String())
	}
	if _, ok := admin.routes[chatID]; ok {
		t.Fatal("route entries survived the alias delete")
	}
}

func TestAdminAuditDistinguishesReadsFromMutations(t *testing.T) {
	store := newAdminTestStore()
	cipher, err := gatewaycrypto.NewCipher(strings.Repeat("a", 64))
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	handler := NewHandlerWithGatewayAndCipher(nil, store, store, nil, AuthConfig{}, cipher)

	login := httptest.NewRecorder()
	handler.ServeHTTP(login, httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"admin","password":"password"}`)))
	if login.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	var loginBody struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &loginBody); err != nil || loginBody.CSRF == "" {
		t.Fatalf("missing CSRF token: %s", login.Body.String())
	}

	mutation := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/admin/aliases", strings.NewReader(`{"name":"audited"}`))
	request.AddCookie(cookie)
	request.Header.Set("X-CSRF-Token", loginBody.CSRF)
	handler.ServeHTTP(mutation, request)
	if mutation.Code != http.StatusCreated {
		t.Fatalf("create alias: %d %s", mutation.Code, mutation.Body.String())
	}

	// Plain reads are deliberately excluded from the audit trail.
	list := httptest.NewRequest(http.MethodGet, "/api/admin/audit", nil)
	list.AddCookie(cookie)
	handler.ServeHTTP(httptest.NewRecorder(), list)

	// Reading a content log is a read that must be audited, and must not be
	// labelled as a mutation.
	content := httptest.NewRequest(http.MethodGet, "/api/admin/content/req-1", nil)
	content.AddCookie(cookie)
	handler.ServeHTTP(httptest.NewRecorder(), content)

	store.mu.Lock()
	defer store.mu.Unlock()
	// The session login is audited first, then the mutation, then the
	// content-log read. The plain GET /api/admin/audit adds nothing.
	if len(store.audit) != 3 {
		t.Fatalf("expected 3 audit entries (login, mutation, content read), got %d", len(store.audit))
	}
	if got := store.audit[0]; got.Action != "login_succeeded" || got.Resource != "session" || got.ActorName != "admin" {
		t.Fatalf("login audit = %s/%q by %q", got.Action, got.Resource, got.ActorName)
	}
	if got := store.audit[1]; got.Action != http.MethodPost || got.Detail != "admin API mutation" || got.Resource != "aliases" {
		t.Fatalf("mutation audit = %s/%q/%q", got.Action, got.Detail, got.Resource)
	}
	if got := store.audit[2]; got.Action != http.MethodGet || got.Detail != "content log read" || got.Resource != "content" || got.ResourceID != "req-1" || got.Result != "failure" {
		t.Fatalf("content-read audit = %s/%q/%q", got.Action, got.Detail, got.Resource)
	}
}
