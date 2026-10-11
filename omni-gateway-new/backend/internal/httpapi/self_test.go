package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	gatewaycrypto "github.com/zjvtit/omni-gateway/internal/crypto"
	"github.com/zjvtit/omni-gateway/internal/model"
	"github.com/zjvtit/omni-gateway/internal/store"
)

// selfTestStore records the scope every self query was issued with, so a test
// can prove the handler asked for the session account and nothing else. It also
// holds another account's rows: if the handler ever widened the scope, that
// data would show up in the response body.
type selfTestStore struct {
	*fakeAuthStore
	mu             sync.Mutex
	statsUserIDs   []int64
	budgetUserIDs  []int64
	keyUserIDs     []int64
	limitScopes    []string
	requestFilters []model.StatsFilter
	stats          map[int64]*model.StatsSummary
	keys           map[int64][]*model.APIKey
	budgets        map[int64][]*model.BudgetRule
	limits         map[string][]*model.RateLimit
	requests       map[int64][]*model.Request
	err            error
	// Write-side recordings: every mutation the self surface performs lands in
	// one of these, so a test can prove what was written and what was not.
	createdKeys    []*model.APIKey
	updatedKeys    []*model.APIKey
	deletedKeyIDs  []int64
	passwordSets   []int64
	prunedSessions []string
	audits         []*model.AuditEntry
}

func newSelfTestStore(t *testing.T) *selfTestStore {
	t.Helper()
	aliceHash, err := gatewaycrypto.HashPassword("alice-password")
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}
	adminHash, err := gatewaycrypto.HashPassword("admin-password")
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}
	alice := &model.User{ID: 2, Username: "alice", DisplayName: "Alice", Role: model.RoleUser, PasswordHash: aliceHash, Active: true, PasswordLoginEnabled: true}
	admin := &model.User{ID: 1, Username: "admin", DisplayName: "Admin", Role: model.RoleAdmin, PasswordHash: adminHash, Active: true, PasswordLoginEnabled: true}
	bob := &model.User{ID: 3, Username: "bob", DisplayName: "Bob", Role: model.RoleUser, Active: false}
	base := newFakeAuthStore(alice)
	base.usersByID[admin.ID] = admin
	base.usersByID[bob.ID] = bob
	base.usersByName[admin.Username] = admin
	base.usersByName[bob.Username] = bob

	return &selfTestStore{
		fakeAuthStore: base,
		stats: map[int64]*model.StatsSummary{
			2: {Requests: 7, Success: 6, Errors: 1, TotalTokens: 1234},
			3: {Requests: 99, Success: 99, TotalTokens: 999999},
		},
		keys: map[int64][]*model.APIKey{
			2: {{ID: 11, UserID: 2, Name: "alice-key", KeyPrefix: "sk-llm-", KeyHash: "alice-key-hash", KeySuffix: "aaaa", Active: true}},
			3: {{ID: 12, UserID: 3, Name: "bob-key", KeyPrefix: "sk-llm-", KeyHash: "bob-key-hash", KeySuffix: "bbbb", Active: true}},
		},
		budgets: map[int64][]*model.BudgetRule{
			2: {{ID: 21, UserID: 2, WindowSecs: 86400, Active: true}},
			3: {{ID: 22, UserID: 3, WindowSecs: 3600, Active: true}},
		},
		limits: map[string][]*model.RateLimit{
			"user#2": {{ID: 31, Scope: "user", ScopeID: 2, RPM: 60, Active: true}},
			"key#11": {{ID: 32, Scope: "key", ScopeID: 11, RPM: 30, Active: true}},
			"user#3": {{ID: 33, Scope: "user", ScopeID: 3, RPM: 1, Active: true}},
		},
		requests: map[int64][]*model.Request{
			2: {{ID: "req-alice", UserID: 2, Status: model.StatusSuccess}, {ID: "req-alice-2", UserID: 2, Status: model.StatusError}},
			3: {{ID: "req-bob", UserID: 3, Status: model.StatusSuccess}},
		},
	}
}

func (s *selfTestStore) Stats(_ context.Context, filter model.StatsFilter) (*model.StatsSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statsUserIDs = append(s.statsUserIDs, filter.UserID)
	if s.err != nil {
		return nil, s.err
	}
	if summary := s.stats[filter.UserID]; summary != nil {
		return summary, nil
	}
	return &model.StatsSummary{}, nil
}

func (s *selfTestStore) ListBudgetRules(_ context.Context, userID int64) ([]*model.BudgetRule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.budgetUserIDs = append(s.budgetUserIDs, userID)
	if s.err != nil {
		return nil, s.err
	}
	return s.budgets[userID], nil
}

func (s *selfTestStore) ListAPIKeysByUser(_ context.Context, userID int64) ([]*model.APIKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keyUserIDs = append(s.keyUserIDs, userID)
	if s.err != nil {
		return nil, s.err
	}
	return s.keys[userID], nil
}

// GetAPIKeyByID can load a key of any account, which is exactly what lets a
// test prove the handler refuses a foreign one.
func (s *selfTestStore) GetAPIKeyByID(_ context.Context, id int64) (*model.APIKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	for _, keys := range s.keys {
		for _, key := range keys {
			if key.ID == id {
				return key, nil
			}
		}
	}
	return nil, store.ErrNotFound
}

func (s *selfTestStore) CreateAPIKey(_ context.Context, key *model.APIKey) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return 0, s.err
	}
	id := int64(100 + len(s.createdKeys))
	key.ID = id
	s.createdKeys = append(s.createdKeys, key)
	s.keys[key.UserID] = append(s.keys[key.UserID], key)
	return id, nil
}

func (s *selfTestStore) UpdateAPIKey(_ context.Context, key *model.APIKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.updatedKeys = append(s.updatedKeys, key)
	for i, existing := range s.keys[key.UserID] {
		if existing.ID == key.ID {
			s.keys[key.UserID][i] = key
		}
	}
	return nil
}

func (s *selfTestStore) DeleteAPIKey(_ context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.deletedKeyIDs = append(s.deletedKeyIDs, id)
	for userID, keys := range s.keys {
		kept := make([]*model.APIKey, 0, len(keys))
		for _, key := range keys {
			if key.ID != id {
				kept = append(kept, key)
			}
		}
		s.keys[userID] = kept
	}
	return nil
}

func (s *selfTestStore) SetUserPassword(_ context.Context, id int64, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.passwordSets = append(s.passwordSets, id)
	if user := s.usersByID[id]; user != nil {
		user.PasswordHash = hash
		user.PasswordLoginEnabled = true
	}
	return nil
}

// DeleteUserSessionsExcept revokes the account's other sessions, like the real
// store does, so a test can prove a password change signs other devices out.
func (s *selfTestStore) DeleteUserSessionsExcept(_ context.Context, userID int64, keepID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.prunedSessions = append(s.prunedSessions, strconv.FormatInt(userID, 10)+"#"+keepID)
	for id, session := range s.sessions {
		if session.userID == userID && id != keepID {
			delete(s.sessions, id)
		}
	}
	return nil
}

// InsertAudit records an audit entry: the self surface must leave a trace of
// its writes, and never a secret inside it.
func (s *selfTestStore) InsertAudit(_ context.Context, entry *model.AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audits = append(s.audits, entry)
	return nil
}

func (s *selfTestStore) ListRateLimits(_ context.Context, scope string, scopeID int64) ([]*model.RateLimit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.limitScopes = append(s.limitScopes, scope+"#"+strconv.FormatInt(scopeID, 10))
	if s.err != nil {
		return nil, s.err
	}
	return s.limits[scope+"#"+strconv.FormatInt(scopeID, 10)], nil
}

func (s *selfTestStore) ListRequests(_ context.Context, filter model.StatsFilter) ([]*model.Request, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requestFilters = append(s.requestFilters, filter)
	if s.err != nil {
		return nil, s.err
	}
	rows := s.requests[filter.UserID]
	offset := filter.Offset
	if offset > len(rows) {
		offset = len(rows)
	}
	rows = rows[offset:]
	if filter.Limit > 0 && filter.Limit < len(rows) {
		rows = rows[:filter.Limit]
	}
	return rows, nil
}

func loginSelf(t *testing.T, handler http.Handler, username, password string) *http.Cookie {
	t.Helper()
	recorder := httptest.NewRecorder()
	body := `{"username":"` + username + `","password":"` + password + `"}`
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("login as %s returned %d: %s", username, recorder.Code, recorder.Body.String())
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatalf("login as %s did not set a session cookie", username)
	}
	return cookies[0]
}

func getSelf(t *testing.T, handler http.Handler, cookie *http.Cookie, path string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	if cookie != nil {
		request.AddCookie(cookie)
	}
	handler.ServeHTTP(recorder, request)
	return recorder
}

// selfCSRF reads the CSRF token the login response handed the browser. Every
// write below has to present it, exactly like the console does.
func selfCSRF(t *testing.T, store *selfTestStore, cookie *http.Cookie) string {
	t.Helper()
	session, ok := store.sessions[cookie.Value]
	if !ok {
		t.Fatalf("no session was recorded for the login cookie")
	}
	return session.csrf
}

// selfAuditCount counts the audit entries carrying a detail. Signing in is
// audited too (with the action "login_succeeded"), so a test must look for the
// entry it cares about instead of counting every entry in the log.
func selfAuditCount(store *selfTestStore, detail string) int {
	count := 0
	for _, entry := range store.audits {
		if entry.Detail == detail && entry.Result == "success" {
			count++
		}
	}
	return count
}

func writeSelf(t *testing.T, handler http.Handler, cookie *http.Cookie, csrf, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, reader)
	if cookie != nil {
		request.AddCookie(cookie)
	}
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	handler.ServeHTTP(recorder, request)
	return recorder
}

// The account view must answer for the signed-in account even when the caller
// tries to point it at somebody else's id.
func TestSelfSummaryIsScopedToTheSessionAccount(t *testing.T) {
	backend := newSelfTestStore(t)
	handler := NewHandlerWithAuth(nil, backend, AuthConfig{})
	cookie := loginSelf(t, handler, "alice", "alice-password")

	recorder := getSelf(t, handler, cookie, "/api/self/summary?user_id=3&scope_id=3&api_key_id=12&limit=100")
	if recorder.Code != http.StatusOK {
		t.Fatalf("summary returned %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `"requests":7`) || !strings.Contains(body, `"total_tokens":1234`) {
		t.Fatalf("summary did not use the session account's usage: %s", body)
	}
	for _, leaked := range []string{"bob", "999999", `"scope_id":3`} {
		if strings.Contains(body, leaked) {
			t.Fatalf("summary leaked other accounts' data (%q): %s", leaked, body)
		}
	}
	if len(backend.statsUserIDs) != 1 || backend.statsUserIDs[0] != 2 {
		t.Fatalf("stats was scoped to %v, want [2]", backend.statsUserIDs)
	}
	if len(backend.budgetUserIDs) != 0 || strings.Contains(body, `"budgets"`) {
		t.Fatal("retired budgets were queried or exposed")
	}
	// The account-wide rule plus the rule attached to its own key, and nothing
	// for the other account's key.
	if strings.Join(backend.limitScopes, ",") != "user#2,key#11" {
		t.Fatalf("limits were scoped to %v", backend.limitScopes)
	}
	if len(backend.keyUserIDs) != 1 || backend.keyUserIDs[0] != 2 {
		t.Fatalf("keys were scoped to %v, want [2]", backend.keyUserIDs)
	}
}

func TestSelfApiKeysReturnsOwnMaskedKeysOnly(t *testing.T) {
	backend := newSelfTestStore(t)
	handler := NewHandlerWithAuth(nil, backend, AuthConfig{})
	cookie := loginSelf(t, handler, "alice", "alice-password")

	recorder := getSelf(t, handler, cookie, "/api/self/api-keys?user_id=3")
	if recorder.Code != http.StatusOK {
		t.Fatalf("api-keys returned %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "alice-key") || !strings.Contains(body, `"key_suffix":"aaaa"`) {
		t.Fatalf("own key missing from response: %s", body)
	}
	for _, leaked := range []string{"bob-key", "alice-key-hash", "bob-key-hash", `"user_id":3`} {
		if strings.Contains(body, leaked) {
			t.Fatalf("api-keys leaked %q: %s", leaked, body)
		}
	}
	if len(backend.keyUserIDs) != 1 || backend.keyUserIDs[0] != 2 {
		t.Fatalf("api-keys queried users %v, want [2]", backend.keyUserIDs)
	}
}

func TestSelfRequestsBoundsThePageAndIgnoresForeignFilters(t *testing.T) {
	backend := newSelfTestStore(t)
	handler := NewHandlerWithAuth(nil, backend, AuthConfig{})
	cookie := loginSelf(t, handler, "alice", "alice-password")

	recorder := getSelf(t, handler, cookie, "/api/self/requests?user_id=3&limit=5&offset=1&status=error")
	if recorder.Code != http.StatusOK {
		t.Fatalf("requests returned %d: %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "req-bob") {
		t.Fatalf("requests leaked another account's rows: %s", recorder.Body.String())
	}
	if len(backend.requestFilters) != 1 {
		t.Fatalf("requests filter calls = %v", backend.requestFilters)
	}
	got := backend.requestFilters[0]
	if got.UserID != 2 || got.Limit != 5 || got.Offset != 1 || got.Status != model.StatusError {
		t.Fatalf("unexpected filter: %+v", got)
	}

	// The default page and the explicit bounds both stay inside the clamp.
	backend.requestFilters = nil
	getSelf(t, handler, cookie, "/api/self/requests")
	if len(backend.requestFilters) != 1 || backend.requestFilters[0].Limit != defaultSelfRequestPage {
		t.Fatalf("default page = %+v", backend.requestFilters)
	}
	for _, query := range []string{"limit=0", "limit=201", "limit=abc", "offset=-1"} {
		backend.requestFilters = nil
		recorder := getSelf(t, handler, cookie, "/api/self/requests?"+query)
		if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "invalid_filter") {
			t.Fatalf("%s returned %d: %s", query, recorder.Code, recorder.Body.String())
		}
		if len(backend.requestFilters) != 0 {
			t.Fatalf("%s reached the store: %v", query, backend.requestFilters)
		}
	}
}

func TestSelfSurfaceRequiresSessionAndRejectsUnsupportedMethods(t *testing.T) {
	backend := newSelfTestStore(t)
	handler := NewHandlerWithAuth(nil, backend, AuthConfig{})

	for _, path := range []string{"/api/self/summary", "/api/self/api-keys", "/api/self/requests", "/api/self/password"} {
		recorder := getSelf(t, handler, nil, path)
		if recorder.Code != http.StatusUnauthorized || !strings.Contains(recorder.Body.String(), "authentication_required") {
			t.Fatalf("%s without a session returned %d: %s", path, recorder.Code, recorder.Body.String())
		}
	}

	cookie := loginSelf(t, handler, "alice", "alice-password")
	csrf := selfCSRF(t, backend, cookie)
	// A valid session and a valid token are still not a licence to use a method
	// the surface does not implement.
	for _, item := range []struct{ method, path string }{
		{http.MethodPut, "/api/self/api-keys"},
		{http.MethodPut, "/api/self/summary"},
		{http.MethodDelete, "/api/self/requests"},
		{http.MethodPost, "/api/self/api-keys/11"},
	} {
		recorder := writeSelf(t, handler, cookie, csrf, item.method, item.path, `{"name":"mine"}`)
		if recorder.Code != http.StatusMethodNotAllowed || !strings.Contains(recorder.Body.String(), "method_not_allowed") {
			t.Fatalf("%s %s returned %d: %s", item.method, item.path, recorder.Code, recorder.Body.String())
		}
	}
	if recorder := getSelf(t, handler, cookie, "/api/self/unknown"); recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown self path returned %d", recorder.Code)
	}
	if recorder := writeSelf(t, handler, cookie, csrf, http.MethodDelete, "/api/self/api-keys/abc", ""); recorder.Code != http.StatusBadRequest {
		t.Fatalf("a non-numeric key id returned %d", recorder.Code)
	}
}

// A store failure must answer with the generic envelope instead of echoing the
// driver error, and a missing row stays a 404.
func TestSelfStoreFailuresAreNotLeaked(t *testing.T) {
	backend := newSelfTestStore(t)
	handler := NewHandlerWithAuth(nil, backend, AuthConfig{})
	cookie := loginSelf(t, handler, "alice", "alice-password")

	backend.err = errors.New("postgres password=secret")
	recorder := getSelf(t, handler, cookie, "/api/self/summary")
	if recorder.Code != http.StatusInternalServerError || strings.Contains(recorder.Body.String(), "secret") {
		t.Fatalf("summary failure returned %d: %s", recorder.Code, recorder.Body.String())
	}

	backend.err = store.ErrNotFound
	recorder = getSelf(t, handler, cookie, "/api/self/api-keys")
	if recorder.Code != http.StatusNotFound || strings.Contains(recorder.Body.String(), "secret") {
		t.Fatalf("missing keys returned %d: %s", recorder.Code, recorder.Body.String())
	}
}

// An administrator may use the same account view, and its own summary must
// still be scoped to its own id.
func TestSelfSummaryWorksForAdministrators(t *testing.T) {
	backend := newSelfTestStore(t)
	handler := NewHandlerWithAuth(nil, backend, AuthConfig{})
	cookie := loginSelf(t, handler, "admin", "admin-password")

	recorder := getSelf(t, handler, cookie, "/api/self/summary")
	if recorder.Code != http.StatusOK {
		t.Fatalf("admin summary returned %d: %s", recorder.Code, recorder.Body.String())
	}
	if len(backend.statsUserIDs) != 1 || backend.statsUserIDs[0] != 1 {
		t.Fatalf("admin stats scope = %v, want [1]", backend.statsUserIDs)
	}
	if !strings.Contains(recorder.Body.String(), `"role":"admin"`) {
		t.Fatalf("admin summary lost the role: %s", recorder.Body.String())
	}
}

// Every write of this surface needs the session's CSRF token, so a cross-site
// form cannot create a credential or change the password with a stolen cookie.
func TestSelfWriteRequiresCSRF(t *testing.T) {
	backend := newSelfTestStore(t)
	handler := NewHandlerWithAuth(nil, backend, AuthConfig{})
	cookie := loginSelf(t, handler, "alice", "alice-password")

	cases := []struct{ method, path, body string }{
		{http.MethodPost, "/api/self/api-keys", `{"name":"mine"}`},
		{http.MethodPatch, "/api/self/api-keys/11", `{"active":false}`},
		{http.MethodDelete, "/api/self/api-keys/11", ""},
		{http.MethodPost, "/api/self/password", `{"current_password":"alice-password","new_password":"alice-password-2"}`},
	}
	for _, item := range cases {
		for _, token := range []string{"", "not-the-session-token"} {
			recorder := writeSelf(t, handler, cookie, token, item.method, item.path, item.body)
			if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "csrf_failed") {
				t.Fatalf("%s %s with token %q returned %d: %s", item.method, item.path, token, recorder.Code, recorder.Body.String())
			}
		}
	}
	if len(backend.createdKeys) != 0 || len(backend.updatedKeys) != 0 || len(backend.deletedKeyIDs) != 0 || len(backend.passwordSets) != 0 {
		t.Fatalf("a rejected write still reached the store: %+v", backend)
	}
	// Reads stay token-free: the console loads them on first paint.
	if recorder := getSelf(t, handler, cookie, "/api/self/api-keys"); recorder.Code != http.StatusOK {
		t.Fatalf("reading keys returned %d: %s", recorder.Code, recorder.Body.String())
	}
}

// A key created here belongs to the session account even when the payload names
// somebody else, and the plaintext secret leaves the server exactly once.
func TestSelfCreatesItsOwnAPIKey(t *testing.T) {
	backend := newSelfTestStore(t)
	handler := NewHandlerWithAuth(nil, backend, AuthConfig{})
	cookie := loginSelf(t, handler, "alice", "alice-password")
	csrf := selfCSRF(t, backend, cookie)

	// Reject forged creation, then issue a key without a client-selected owner.
	if denied := writeSelf(t, handler, cookie, csrf, http.MethodPost, "/api/self/api-keys", `{"name":"forged","user_id":3}`); denied.Code != 403 || len(backend.createdKeys) != 0 {
		t.Fatal("forged ownership accepted")
	}
	recorder := writeSelf(t, handler, cookie, csrf, http.MethodPost, "/api/self/api-keys", `{"name":"laptop"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create returned %d: %s", recorder.Code, recorder.Body.String())
	}
	if len(backend.createdKeys) != 1 {
		t.Fatalf("created keys = %d, want 1", len(backend.createdKeys))
	}
	created := backend.createdKeys[0]
	if created.UserID != 2 {
		t.Fatalf("the key was created for user %d, want the session account 2", created.UserID)
	}
	if created.Name != "laptop" || !created.Active {
		t.Fatalf("unexpected stored key: %+v", created)
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("create body is not JSON: %v", err)
	}
	secret, _ := body["secret"].(string)
	if !strings.HasPrefix(secret, "sk-llm-") {
		t.Fatalf("create response carried no secret: %s", recorder.Body.String())
	}
	if gatewaycrypto.HashAPIKey(secret) != created.KeyHash {
		t.Fatal("the stored hash does not match the returned secret")
	}
	if strings.Contains(recorder.Body.String(), created.KeyHash) {
		t.Fatalf("create response leaked the stored hash: %s", recorder.Body.String())
	}

	// The list repeats neither the secret nor its hash.
	list := getSelf(t, handler, cookie, "/api/self/api-keys")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "laptop") {
		t.Fatalf("list returned %d: %s", list.Code, list.Body.String())
	}
	if strings.Contains(list.Body.String(), secret) || strings.Contains(list.Body.String(), created.KeyHash) {
		t.Fatalf("list leaked the secret or its hash: %s", list.Body.String())
	}
	if selfAuditCount(backend, "self API key created") != 1 {
		t.Fatalf("create was not audited exactly once: %+v", backend.audits)
	}
	for _, entry := range backend.audits {
		if entry.Detail == "self API key created" && (entry.ActorID != 2 || entry.Action != http.MethodPost) {
			t.Fatalf("the audit entry does not name the account and the method: %+v", entry)
		}
	}
}

// An account may pause, rename and delete its own key, but it cannot hand the
// key to somebody else through the payload.
func TestSelfTogglesAndDeletesItsOwnAPIKey(t *testing.T) {
	backend := newSelfTestStore(t)
	handler := NewHandlerWithAuth(nil, backend, AuthConfig{})
	cookie := loginSelf(t, handler, "alice", "alice-password")
	csrf := selfCSRF(t, backend, cookie)

	// Key 11 belongs to alice.
	recorder := writeSelf(t, handler, cookie, csrf, http.MethodPatch, "/api/self/api-keys/11", `{"active":false,"name":"renamed","model_allow":["gpt-4o"]}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("update returned %d: %s", recorder.Code, recorder.Body.String())
	}
	if len(backend.updatedKeys) != 1 {
		t.Fatalf("updated keys = %d, want 1", len(backend.updatedKeys))
	}
	updated := backend.updatedKeys[0]
	if updated.ID != 11 || updated.Active || updated.Name != "renamed" || len(updated.ModelAllow) != 1 {
		t.Fatalf("update was not applied: %+v", updated)
	}
	if updated.UserID != 2 {
		t.Fatalf("update reassigned the owner: %+v", updated)
	}

	// A second payload tries to move the key to bob; the owner stays put.
	recorder = writeSelf(t, handler, cookie, csrf, http.MethodPatch, "/api/self/api-keys/11", `{"user_id":3}`)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "key_owner_immutable") {
		t.Fatalf("ownership edit must fail explicitly: %d %s", recorder.Code, recorder.Body.String())
	}
	if len(backend.updatedKeys) != 1 || backend.keys[2][0].UserID != 2 {
		t.Fatal("rejected ownership mutation was persisted")
	}

	recorder = writeSelf(t, handler, cookie, csrf, http.MethodDelete, "/api/self/api-keys/11", "")
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("delete returned %d: %s", recorder.Code, recorder.Body.String())
	}
	if len(backend.deletedKeyIDs) != 1 || backend.deletedKeyIDs[0] != 11 {
		t.Fatalf("deleted keys = %v, want [11]", backend.deletedKeyIDs)
	}
	if list := getSelf(t, handler, cookie, "/api/self/api-keys"); strings.Contains(list.Body.String(), "alice-key") {
		t.Fatalf("the deleted key is still listed: %s", list.Body.String())
	}
}

// Another account's key is invisible here: the answer is a plain 404 and no
// mutation happens, so a guessed id confirms nothing.
func TestSelfCannotTouchAnotherAccountsKey(t *testing.T) {
	backend := newSelfTestStore(t)
	handler := NewHandlerWithAuth(nil, backend, AuthConfig{})
	cookie := loginSelf(t, handler, "alice", "alice-password")
	csrf := selfCSRF(t, backend, cookie)

	// Key 12 belongs to bob.
	for _, method := range []string{http.MethodPatch, http.MethodDelete} {
		recorder := writeSelf(t, handler, cookie, csrf, method, "/api/self/api-keys/12", `{"active":false}`)
		if recorder.Code != http.StatusNotFound || !strings.Contains(recorder.Body.String(), "not_found") {
			t.Fatalf("%s on another account's key returned %d: %s", method, recorder.Code, recorder.Body.String())
		}
	}
	if len(backend.updatedKeys) != 0 || len(backend.deletedKeyIDs) != 0 {
		t.Fatalf("another account's key was mutated: updated=%d deleted=%v", len(backend.updatedKeys), backend.deletedKeyIDs)
	}
	for _, entry := range backend.audits {
		if strings.HasPrefix(entry.Detail, "self API key") && entry.Result == "success" {
			t.Fatal("refused foreign mutation audited as success")
		}
	}
	// An id that does not exist answers exactly the same way.
	missing := writeSelf(t, handler, cookie, csrf, http.MethodDelete, "/api/self/api-keys/9999", "")
	if missing.Code != http.StatusNotFound || !strings.Contains(missing.Body.String(), "not_found") {
		t.Fatalf("a missing key returned %d: %s", missing.Code, missing.Body.String())
	}
	// Bob's key is still there and still listed for bob alone.
	if list := getSelf(t, handler, cookie, "/api/self/api-keys"); strings.Contains(list.Body.String(), "bob-key") {
		t.Fatalf("another account's key is listed: %s", list.Body.String())
	}
}

// Rotating the password stores the new hash, revokes the account's other
// sessions and keeps the one that made the change alive.
func TestSelfChangesItsOwnPassword(t *testing.T) {
	backend := newSelfTestStore(t)
	handler := NewHandlerWithAuth(nil, backend, AuthConfig{})
	cookie := loginSelf(t, handler, "alice", "alice-password")
	csrf := selfCSRF(t, backend, cookie)
	// A second, older session of the same account must not survive the change.
	if err := backend.CreateSession(context.Background(), "older-session", 2, "older-csrf", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("seeding a second session failed: %v", err)
	}

	recorder := writeSelf(t, handler, cookie, csrf, http.MethodPost, "/api/self/password", `{"current_password":"alice-password","new_password":"alice-password-2"}`)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("password change returned %d: %s", recorder.Code, recorder.Body.String())
	}
	if len(backend.passwordSets) != 1 || backend.passwordSets[0] != 2 {
		t.Fatalf("password sets = %v, want [2]", backend.passwordSets)
	}
	stored := backend.usersByID[2].PasswordHash
	if ok, err := gatewaycrypto.VerifyPassword("alice-password-2", stored); err != nil || !ok {
		t.Fatalf("the new password does not verify against the stored hash: %v", err)
	}
	if stale, err := gatewaycrypto.VerifyPassword("alice-password", stored); err != nil || stale {
		t.Fatalf("the old password still verifies against the stored hash: %v", err)
	}
	if len(backend.prunedSessions) != 1 || backend.prunedSessions[0] != "2#"+cookie.Value {
		t.Fatalf("sessions were pruned as %v", backend.prunedSessions)
	}
	if _, survived := backend.sessions["older-session"]; survived {
		t.Fatal("another session of the account survived the password change")
	}
	if _, current := backend.sessions[cookie.Value]; !current {
		t.Fatal("the session that changed the password was signed out")
	}
	if selfAuditCount(backend, "self password changed") != 1 {
		t.Fatalf("the change was not audited exactly once: %+v", backend.audits)
	}
	for _, entry := range backend.audits {
		if entry.Detail == "self password changed" && entry.ActorID != 2 {
			t.Fatalf("the audit entry does not name the account: %+v", entry)
		}
	}
	for _, entry := range backend.audits {
		if strings.Contains(entry.Detail, "alice-password") {
			t.Fatalf("the audit entry carries the password: %+v", entry)
		}
	}
	// The rotated password is the one that signs in from now on.
	loginSelf(t, handler, "alice", "alice-password-2")
}

// A rejected rotation must leave the stored password untouched.
func TestSelfRejectsBadPasswordChanges(t *testing.T) {
	backend := newSelfTestStore(t)
	handler := NewHandlerWithAuth(nil, backend, AuthConfig{})
	cookie := loginSelf(t, handler, "alice", "alice-password")
	csrf := selfCSRF(t, backend, cookie)

	cases := []struct{ name, body, text string }{
		{"wrong current password", `{"current_password":"nope","new_password":"alice-password-2"}`, "invalid_current_password"},
		{"too short", `{"current_password":"alice-password","new_password":"short"}`, "weak_password"},
		{"unchanged", `{"current_password":"alice-password","new_password":"alice-password"}`, "must differ"},
		{"missing new password", `{"current_password":"alice-password"}`, "invalid_request"},
	}
	for _, item := range cases {
		recorder := writeSelf(t, handler, cookie, csrf, http.MethodPost, "/api/self/password", item.body)
		if recorder.Code < 400 || !strings.Contains(recorder.Body.String(), item.text) {
			t.Fatalf("%s returned %d, want a 4xx naming %q: %s", item.name, recorder.Code, item.text, recorder.Body.String())
		}
	}
	if len(backend.passwordSets) != 0 {
		t.Fatalf("a rejected change still wrote a password: %v", backend.passwordSets)
	}
	if loginSelf(t, handler, "alice", "alice-password") == nil {
		t.Fatal("the original password stopped working")
	}
}

// The label and the expiry are validated before anything is written, so an
// unusable key never reaches the store.
func TestSelfValidatesItsKeyInput(t *testing.T) {
	backend := newSelfTestStore(t)
	handler := NewHandlerWithAuth(nil, backend, AuthConfig{})
	cookie := loginSelf(t, handler, "alice", "alice-password")
	csrf := selfCSRF(t, backend, cookie)

	cases := []struct{ name, body, text string }{
		{"missing name", `{}`, "name is required"},
		{"blank name", `{"name":"   "}`, "name is required"},
		{"long name", `{"name":"` + strings.Repeat("x", maxSelfKeyNameRunes+1) + `"}`, "at most 64 characters"},
		{"past expiry", `{"name":"laptop","expires_at":"2001-01-01T00:00:00Z"}`, "expires_at must be in the future"},
	}
	for _, item := range cases {
		recorder := writeSelf(t, handler, cookie, csrf, http.MethodPost, "/api/self/api-keys", item.body)
		if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), item.text) {
			t.Fatalf("%s returned %d, want 400 naming %q: %s", item.name, recorder.Code, item.text, recorder.Body.String())
		}
	}
	if len(backend.createdKeys) != 0 {
		t.Fatalf("an invalid request still created a key: %+v", backend.createdKeys)
	}

	// A future expiry and a whitelist are accepted and stored as given.
	recorder := writeSelf(t, handler, cookie, csrf, http.MethodPost, "/api/self/api-keys", `{"name":"laptop","model_allow":["gpt-4o"],"expires_at":"2999-01-01T00:00:00Z"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("valid create returned %d: %s", recorder.Code, recorder.Body.String())
	}
	created := backend.createdKeys[0]
	if len(created.ModelAllow) != 1 || created.ModelAllow[0] != "gpt-4o" || created.ExpiresAt == nil {
		t.Fatalf("the whitelist or the expiry was dropped: %+v", created)
	}
	// The same rules apply to an update.
	if recorder := writeSelf(t, handler, cookie, csrf, http.MethodPatch, "/api/self/api-keys/11", `{"name":""}`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("an explicit empty name must be rejected, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if recorder := writeSelf(t, handler, cookie, csrf, http.MethodPatch, "/api/self/api-keys/11", `{"expires_at":"2001-01-01T00:00:00Z"}`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("a past expiry on update returned %d", recorder.Code)
	}
}
