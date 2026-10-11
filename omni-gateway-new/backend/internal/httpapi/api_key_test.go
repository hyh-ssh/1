package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gatewaycrypto "github.com/zjvtit/omni-gateway/internal/crypto"
	"github.com/zjvtit/omni-gateway/internal/model"
	"github.com/zjvtit/omni-gateway/internal/store"
)

type fakeAPIKeyStore struct {
	key     *model.APIKey
	user    *model.User
	keyErr  error
	userErr error
	touched int
}

func (s *fakeAPIKeyStore) GetAPIKeyByHash(_ context.Context, hash string) (*model.APIKey, error) {
	if s.keyErr != nil {
		return nil, s.keyErr
	}
	if s.key == nil || s.key.KeyHash != hash {
		return nil, store.ErrNotFound
	}
	return s.key, nil
}

func (s *fakeAPIKeyStore) GetUserByID(_ context.Context, id int64) (*model.User, error) {
	if s.userErr != nil {
		return nil, s.userErr
	}
	if s.user == nil || s.user.ID != id {
		return nil, store.ErrNotFound
	}
	return s.user, nil
}

func (s *fakeAPIKeyStore) TouchAPIKey(context.Context, int64) error {
	s.touched++
	return nil
}

func TestParseBearerToken(t *testing.T) {
	cases := []struct {
		header string
		want   string
		ok     bool
	}{
		{header: "Bearer secret", want: "secret", ok: true},
		{header: "bearer secret", want: "secret", ok: true},
		{header: "Bearer  secret", want: "secret", ok: true},
		{header: "", ok: false},
		{header: "Basic secret", ok: false},
		{header: "Bearer", ok: false},
		{header: "Bearer one two", ok: false},
	}
	for _, tc := range cases {
		got, ok := ParseBearerToken(tc.header)
		if got != tc.want || ok != tc.ok {
			t.Errorf("ParseBearerToken(%q) = %q, %v; want %q, %v", tc.header, got, ok, tc.want, tc.ok)
		}
	}
}

func TestAPIKeyMiddlewareAuthenticatesAndAttachesPrincipal(t *testing.T) {
	secret := "sk-test-secret"
	users := &fakeAPIKeyStore{
		key:  &model.APIKey{ID: 4, UserID: 9, KeyHash: gatewaycrypto.HashAPIKey(secret), Active: true},
		user: &model.User{ID: 9, Username: "alice", Active: true},
	}
	var got *Principal
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ok bool
		got, ok = PrincipalFromContext(r.Context())
		if !ok {
			t.Error("principal missing from context")
		}
		w.WriteHeader(http.StatusNoContent)
	})

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	APIKeyMiddleware(users, next).ServeHTTP(recorder, req)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("got status %d, want %d", recorder.Code, http.StatusNoContent)
	}
	if got == nil || got.APIKey.ID != 4 || got.User.Username != "alice" {
		t.Fatalf("unexpected principal: %#v", got)
	}
	if users.touched != 1 {
		t.Fatalf("TouchAPIKey called %d times, want 1", users.touched)
	}
}

func TestAPIKeyMiddlewareRejectsInvalidKeys(t *testing.T) {
	secret := "sk-test-secret"
	base := &fakeAPIKeyStore{
		key:  &model.APIKey{ID: 4, UserID: 9, KeyHash: gatewaycrypto.HashAPIKey(secret), Active: true},
		user: &model.User{ID: 9, Active: true},
	}
	cases := []struct {
		name   string
		header string
		store  *fakeAPIKeyStore
		status int
	}{
		{name: "missing header", status: http.StatusUnauthorized},
		{name: "unknown key", header: "Bearer other", store: base, status: http.StatusUnauthorized},
		{name: "inactive key", header: "Bearer " + secret, store: &fakeAPIKeyStore{key: &model.APIKey{KeyHash: gatewaycrypto.HashAPIKey(secret), UserID: 9, Active: false}, user: base.user}, status: http.StatusUnauthorized},
		{name: "expired key", header: "Bearer " + secret, store: &fakeAPIKeyStore{key: &model.APIKey{KeyHash: gatewaycrypto.HashAPIKey(secret), UserID: 9, Active: true, ExpiresAt: timePtr(time.Now().UTC().Add(-time.Minute))}, user: base.user}, status: http.StatusUnauthorized},
		{name: "inactive user", header: "Bearer " + secret, store: &fakeAPIKeyStore{key: base.key, user: &model.User{ID: 9, Active: false}}, status: http.StatusUnauthorized},
		{name: "store failure", header: "Bearer " + secret, store: &fakeAPIKeyStore{keyErr: errors.New("db down")}, status: http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backend := tc.store
			if backend == nil {
				backend = base
			}
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
			req.Header.Set("Authorization", tc.header)
			called := false
			next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
			APIKeyMiddleware(backend, next).ServeHTTP(recorder, req)
			if recorder.Code != tc.status {
				t.Fatalf("got status %d, want %d: %s", recorder.Code, tc.status, recorder.Body.String())
			}
			if called {
				t.Fatal("next handler was called for a rejected key")
			}
		})
	}
}

func TestAPIKeyMiddlewareHandlesNilStoreAndUserLookupNotFound(t *testing.T) {
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	APIKeyMiddleware(nil, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(recorder, req)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("got status %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}

	secret := "sk-test-secret"
	backend := &fakeAPIKeyStore{
		key:     &model.APIKey{UserID: 9, KeyHash: gatewaycrypto.HashAPIKey(secret), Active: true},
		userErr: store.ErrNotFound,
	}
	recorder = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/models", strings.NewReader(""))
	req.Header.Set("Authorization", "Bearer "+secret)
	APIKeyMiddleware(backend, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(recorder, req)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("got status %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
}

func TestModelAllowed(t *testing.T) {
	if !ModelAllowed(&model.APIKey{}, "anything") {
		t.Fatal("empty allow-list should allow every model")
	}
	key := &model.APIKey{ModelAllow: []string{"chat", "vision"}}
	if !ModelAllowed(key, "chat") || ModelAllowed(key, "embeddings") || ModelAllowed(nil, "chat") {
		t.Fatal("model allow-list result was incorrect")
	}
}

type groupedAPIKeyStore struct {
	fakeAPIKeyStore
	access    *model.ModelAccess
	accessErr error
}

func (s *groupedAPIKeyStore) GetUserModelAccess(context.Context, int64) (*model.ModelAccess, error) {
	return s.access, s.accessErr
}

func TestPrincipalModelAllowedIgnoresHistoricalGroups(t *testing.T) {
	p := &Principal{APIKey: &model.APIKey{}, ModelAccess: &model.ModelAccess{Restricted: true}}
	if !PrincipalModelAllowed(p, "chat") || !PrincipalModelAllowed(p, "vision") {
		t.Fatal("empty key allow-list must allow all models regardless of old groups")
	}
	p.APIKey.ModelAllow = []string{"vision"}
	if PrincipalModelAllowed(p, "chat") || !PrincipalModelAllowed(p, "vision") {
		t.Fatal("key allow-list must remain enforced")
	}
	if PrincipalModelAllowed(nil, "chat") {
		t.Fatal("nil principal allowed")
	}
}

func TestAPIKeyMiddlewareDoesNotLoadHistoricalGroupAccess(t *testing.T) {
	secret := "sk-group"
	s := &groupedAPIKeyStore{fakeAPIKeyStore: fakeAPIKeyStore{key: &model.APIKey{UserID: 1, KeyHash: gatewaycrypto.HashAPIKey(secret), Active: true}, user: &model.User{ID: 1, Active: true}}, accessErr: errors.New("retired group query must not be used")}
	r := httptest.NewRequest("GET", "/v1/models", nil)
	r.Header.Set("Authorization", "Bearer "+secret)
	w := httptest.NewRecorder()
	APIKeyMiddleware(s, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := PrincipalFromContext(r.Context())
		if p.ModelAccess != nil {
			t.Fatal("historical group access was loaded")
		}
		w.WriteHeader(204)
	})).ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatalf("historical group store blocked admission: %d %s", w.Code, w.Body)
	}
}

func TestAnthropicAPIKeyHeaderFallback(t *testing.T) {
	secret := "sk-anthropic"
	s := &fakeAPIKeyStore{key: &model.APIKey{UserID: 1, KeyHash: gatewaycrypto.HashAPIKey(secret), Active: true}, user: &model.User{ID: 1, Active: true}}
	for _, tc := range []struct {
		path, authorization string
		present             bool
		status              int
	}{
		{"/v1/messages", "", false, 204}, {"/v1/chat/completions", "", false, 401}, {"/v1/models", "", false, 401},
		{"/v1/messages", "Basic invalid", true, 401}, {"/v1/messages", "", true, 401}, {"/v1/messages", "Bearer unknown", true, 401}, {"/v1/messages", "Bearer " + secret, true, 204},
	} {
		req := httptest.NewRequest(http.MethodPost, tc.path, nil)
		req.Header.Set("X-Api-Key", secret)
		if tc.present {
			req.Header.Set("Authorization", tc.authorization)
		}
		rec := httptest.NewRecorder()
		APIKeyMiddleware(s, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })).ServeHTTP(rec, req)
		if rec.Code != tc.status {
			t.Errorf("path=%s authorization=%q present=%v status=%d want=%d", tc.path, tc.authorization, tc.present, rec.Code, tc.status)
		}
	}
}

func TestModelsEndpointFiltersOnlyKeyAllowList(t *testing.T) {
	secret := "sk-models"
	s := &groupedAPIKeyStore{fakeAPIKeyStore: fakeAPIKeyStore{key: &model.APIKey{UserID: 1, KeyHash: gatewaycrypto.HashAPIKey(secret), Active: true, ModelAllow: []string{"chat", "embedding"}}, user: &model.User{ID: 1, Active: true}}, access: &model.ModelAccess{Restricted: true, Models: []string{"chat", "vision"}}}
	h := NewHandlerWithGateway(nil, nil, s, &fakeChatService{models: []string{"chat", "vision", "embedding"}}, AuthConfig{})
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"id":"chat"`) || strings.Contains(rec.Body.String(), "vision") || !strings.Contains(rec.Body.String(), "embedding") {
		t.Fatalf("model filtering=%d %s", rec.Code, rec.Body)
	}
}

func timePtr(value time.Time) *time.Time { return &value }
