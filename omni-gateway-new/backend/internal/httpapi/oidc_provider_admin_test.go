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
	"testing"
	"time"

	gatewaycrypto "github.com/zjvtit/omni-gateway/internal/crypto"
	"github.com/zjvtit/omni-gateway/internal/model"
	"github.com/zjvtit/omni-gateway/internal/store"
)

const oidcProviderAdminPath = "/api/admin/oidc-provider"

// oidcProviderAdminTestStore provisions a private PostgreSQL schema and runs
// the real migrations, so these tests exercise exactly the SQL the admin
// endpoints call - including the secret-preserving upsert and the
// keep-the-row clear - and not a hand-written fake.
func oidcProviderAdminTestStore(t *testing.T) *store.Store {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL OIDC provider admin tests")
	}
	ctx := context.Background()
	admin, err := sql.Open("postgres", raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := admin.PingContext(ctx); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	schema := fmt.Sprintf("oidc_admin_test_%d", time.Now().UnixNano())
	quotedSchema := `"` + strings.ReplaceAll(schema, `"`, `""`) + `"`
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), "DROP SCHEMA "+quotedSchema+" CASCADE")
		admin.Close()
	})
	dsn, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	query := dsn.Query()
	query.Set("options", "-csearch_path="+schema)
	dsn.RawQuery = query.Encode()
	s, err := store.New(ctx, dsn.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

type oidcAdminHarness struct {
	handler http.Handler
	store   *store.Store
	cipher  *gatewaycrypto.Cipher
	cookie  *http.Cookie
	csrf    string
	adminID int64
}

func newOIDCAdminHarness(t *testing.T) *oidcAdminHarness {
	t.Helper()
	return newOIDCAdminHarnessWithConfig(t, AuthConfig{})
}

// newOIDCAdminHarnessWithConfig is the same harness with a caller-supplied
// AuthConfig, which is how tests wire the runtime provider source into the
// administrator handler and observe that writes invalidate it.
func newOIDCAdminHarnessWithConfig(t *testing.T, cfg AuthConfig) *oidcAdminHarness {
	t.Helper()
	return newOIDCAdminHarnessOnStore(t, oidcProviderAdminTestStore(t), cfg)
}

// newOIDCAdminHarnessOnStore runs the harness against a caller-supplied store,
// so a test can have another component read the very schema the API writes to.
func newOIDCAdminHarnessOnStore(t *testing.T, s *store.Store, cfg AuthConfig) *oidcAdminHarness {
	t.Helper()
	passwordHash, err := gatewaycrypto.HashPassword("password")
	if err != nil {
		t.Fatal(err)
	}
	adminID, err := s.CreateUser(context.Background(), &model.User{Username: "admin", DisplayName: "Admin", Role: model.RoleAdmin, PasswordHash: passwordHash, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := gatewaycrypto.NewCipher(strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandlerWithGatewayAndCipher(nil, s, s, nil, cfg, cipher)
	login := httptest.NewRecorder()
	handler.ServeHTTP(login, httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"admin","password":"password"}`)))
	if login.Code != http.StatusOK {
		t.Fatalf("admin login = %d %s", login.Code, login.Body.String())
	}
	var body struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &body); err != nil || body.CSRF == "" {
		t.Fatalf("missing CSRF token: %s", login.Body.String())
	}
	cookies := login.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("login set no session cookie")
	}
	return &oidcAdminHarness{handler: handler, store: s, cipher: cipher, cookie: cookies[0], csrf: body.CSRF, adminID: adminID}
}

func (h *oidcAdminHarness) do(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.AddCookie(h.cookie)
	request.Header.Set("X-CSRF-Token", h.csrf)
	recorder := httptest.NewRecorder()
	h.handler.ServeHTTP(recorder, request)
	return recorder
}

func decodeOIDCProviderView(t *testing.T, recorder *httptest.ResponseRecorder) adminOIDCProviderView {
	t.Helper()
	var view adminOIDCProviderView
	if err := json.Unmarshal(recorder.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode view %s: %v", recorder.Body.String(), err)
	}
	return view
}

// TestAdminOIDCProviderViewNeverExposesSecret covers the console read: an
// unconfigured gateway answers 200 with an empty disabled view, and a
// configured one reports has_client_secret as a boolean while neither the
// plaintext nor the stored ciphertext appears in the body.
func TestAdminOIDCProviderViewNeverExposesSecret(t *testing.T) {
	h := newOIDCAdminHarness(t)

	empty := h.do(t, http.MethodGet, oidcProviderAdminPath, "")
	if empty.Code != http.StatusOK {
		t.Fatalf("GET unconfigured = %d %s", empty.Code, empty.Body.String())
	}
	emptyView := decodeOIDCProviderView(t, empty)
	if emptyView.Enabled || emptyView.Ready || emptyView.HasClientSecret || emptyView.EnvSeedApplied ||
		emptyView.Issuer != "" || emptyView.ClientID != "" || emptyView.RedirectURL != "" || emptyView.DisplayName != "" ||
		emptyView.UpdatedAt != nil {
		t.Fatalf("unconfigured view = %+v, want empty disabled defaults", emptyView)
	}

	seeded, err := h.store.SeedOIDCProviderFromEnv(context.Background(), "https://idp.example", "client-1", "ciphertext-1", "https://gw.example/api/auth/oidc/callback", "Corp SSO")
	if err != nil || !seeded {
		t.Fatalf("seed = %v %v, want the row inserted", seeded, err)
	}
	configured := h.do(t, http.MethodGet, oidcProviderAdminPath, "")
	if configured.Code != http.StatusOK {
		t.Fatalf("GET configured = %d %s", configured.Code, configured.Body.String())
	}
	view := decodeOIDCProviderView(t, configured)
	if !view.Enabled || !view.Ready || !view.HasClientSecret || !view.EnvSeedApplied {
		t.Fatalf("configured view = %+v, want enabled, ready, has_client_secret and env_seed_applied", view)
	}
	if view.Issuer != "https://idp.example" || view.ClientID != "client-1" || view.RedirectURL != "https://gw.example/api/auth/oidc/callback" || view.DisplayName != "Corp SSO" {
		t.Fatalf("configured view = %+v, want the stored fields", view)
	}
	body := configured.Body.String()
	if strings.Contains(body, "ciphertext-1") || strings.Contains(body, `"client_secret":`) {
		t.Fatalf("configured view leaked secret material: %s", body)
	}
}

// TestAdminOIDCProviderPutSavesEncryptsAndPreservesSecret covers the write
// path: the secret is encrypted at rest, an empty client_secret in a later
// save keeps the stored ciphertext byte for byte, and the write is audited.
func TestAdminOIDCProviderPutSavesEncryptsAndPreservesSecret(t *testing.T) {
	h := newOIDCAdminHarness(t)
	ctx := context.Background()

	first := h.do(t, http.MethodPut, oidcProviderAdminPath,
		`{"enabled":true,"issuer":"https://idp.example","client_id":"client-1","client_secret":"s3cret-value","redirect_url":"https://gw.example/api/auth/oidc/callback","display_name":"Corp SSO"}`)
	if first.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", first.Code, first.Body.String())
	}
	firstView := decodeOIDCProviderView(t, first)
	if !firstView.Enabled || !firstView.Ready || !firstView.HasClientSecret {
		t.Fatalf("PUT view = %+v, want enabled, ready and has_client_secret", firstView)
	}
	if strings.Contains(first.Body.String(), "s3cret-value") || strings.Contains(first.Body.String(), `"client_secret":`) {
		t.Fatalf("PUT response leaked the secret: %s", first.Body.String())
	}

	stored, ok, err := h.store.GetOIDCProvider(ctx)
	if err != nil || !ok {
		t.Fatalf("stored provider: ok=%v err=%v", ok, err)
	}
	if !stored.Enabled || stored.Issuer != "https://idp.example" || stored.ClientID != "client-1" ||
		stored.RedirectURL != "https://gw.example/api/auth/oidc/callback" || stored.DisplayName != "Corp SSO" {
		t.Fatalf("stored provider = %+v, want the saved fields", stored)
	}
	if stored.UpdatedBy != h.adminID {
		t.Fatalf("stored updated_by = %d, want the acting administrator %d", stored.UpdatedBy, h.adminID)
	}
	if stored.ClientSecretCiphertext == "" || stored.ClientSecretCiphertext == "s3cret-value" {
		t.Fatalf("client secret was not encrypted at rest: %q", stored.ClientSecretCiphertext)
	}
	decrypted, err := h.cipher.Decrypt(stored.ClientSecretCiphertext)
	if err != nil || decrypted != "s3cret-value" {
		t.Fatalf("decrypted secret = %q err=%v, want the submitted value", decrypted, err)
	}

	// A console save that leaves the secret field empty must not touch it.
	second := h.do(t, http.MethodPut, oidcProviderAdminPath,
		`{"enabled":true,"issuer":"https://idp.changed","client_id":"client-2","client_secret":"","redirect_url":"https://gw.example/api/auth/oidc/callback"}`)
	if second.Code != http.StatusOK {
		t.Fatalf("PUT with an empty secret = %d %s", second.Code, second.Body.String())
	}
	after, _, err := h.store.GetOIDCProvider(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.ClientSecretCiphertext != stored.ClientSecretCiphertext {
		t.Fatalf("an empty client_secret changed the stored ciphertext: %q -> %q", stored.ClientSecretCiphertext, after.ClientSecretCiphertext)
	}
	if after.Issuer != "https://idp.changed" || after.ClientID != "client-2" {
		t.Fatalf("stored provider after the second save = %+v, want the edited fields", after)
	}
	read := h.do(t, http.MethodGet, oidcProviderAdminPath, "")
	if !strings.Contains(read.Body.String(), `"has_client_secret":true`) {
		t.Fatalf("GET after preserving the secret = %s, want has_client_secret true", read.Body.String())
	}

	entries, err := h.store.ListAudit(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	audited := false
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		if strings.Contains(entry.Detail, "s3cret-value") || strings.Contains(entry.Resource, "s3cret-value") {
			t.Fatalf("audit entry leaked the secret: %+v", entry)
		}
		if entry.Action == http.MethodPut && entry.Resource == "oidc-provider" {
			audited = true
			if entry.ActorID != h.adminID || entry.ActorName != "admin" {
				t.Fatalf("PUT audit = %+v, want the acting administrator", entry)
			}
		}
	}
	if !audited {
		t.Fatalf("no audit entry for the provider write: %+v", entries)
	}
}

// TestAdminOIDCProviderPutRejectsInvalidInputWithoutWriting covers validation:
// a rejected save answers 400 with an explicit message and leaves the stored
// row exactly as it was.
func TestAdminOIDCProviderPutRejectsInvalidInputWithoutWriting(t *testing.T) {
	h := newOIDCAdminHarness(t)
	ctx := context.Background()

	// Nothing is stored yet, so enabling without a secret must be refused.
	if _, ok, err := h.store.GetOIDCProvider(ctx); err != nil || ok {
		t.Fatalf("unexpected starting row: ok=%v err=%v", ok, err)
	}
	missingSecret := h.do(t, http.MethodPut, oidcProviderAdminPath,
		`{"enabled":true,"issuer":"https://idp.example","client_id":"client-1","redirect_url":"https://gw.example/api/auth/oidc/callback"}`)
	if missingSecret.Code != http.StatusBadRequest || !strings.Contains(missingSecret.Body.String(), "client_secret") {
		t.Fatalf("PUT without a stored secret = %d %s, want 400 mentioning client_secret", missingSecret.Code, missingSecret.Body.String())
	}
	if _, ok, err := h.store.GetOIDCProvider(ctx); err != nil || ok {
		t.Fatalf("a rejected PUT wrote a row: ok=%v err=%v", ok, err)
	}

	baseline := h.do(t, http.MethodPut, oidcProviderAdminPath,
		`{"enabled":true,"issuer":"https://idp.example","client_id":"client-1","client_secret":"s3cret-value","redirect_url":"https://gw.example/api/auth/oidc/callback"}`)
	if baseline.Code != http.StatusOK {
		t.Fatalf("baseline PUT = %d %s", baseline.Code, baseline.Body.String())
	}
	before, ok, err := h.store.GetOIDCProvider(ctx)
	if err != nil || !ok {
		t.Fatalf("baseline row: ok=%v err=%v", ok, err)
	}

	cases := []struct {
		name    string
		body    string
		message string
		code    string
	}{
		{"missing issuer", `{"enabled":true,"client_id":"client-1","client_secret":"other","redirect_url":"https://gw.example/api/auth/oidc/callback"}`, "issuer", "invalid_oidc_provider"},
		{"missing redirect_url", `{"enabled":true,"issuer":"https://idp.example","client_id":"client-1","client_secret":"other"}`, "redirect_url", "invalid_oidc_provider"},
		{"issuer not absolute", `{"enabled":true,"issuer":"idp.example","client_id":"client-1","client_secret":"other","redirect_url":"https://gw.example/api/auth/oidc/callback"}`, "issuer must be an absolute URL", "invalid_oidc_provider"},
		{"redirect_url not absolute", `{"enabled":true,"issuer":"https://idp.example","client_id":"client-1","client_secret":"other","redirect_url":"/api/auth/oidc/callback"}`, "redirect_url must be an absolute URL", "invalid_oidc_provider"},
		{"malformed body", `{"enabled":`, "invalid OIDC provider request", "invalid_request"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := h.do(t, http.MethodPut, oidcProviderAdminPath, tc.body)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("PUT %s = %d %s, want 400", tc.name, recorder.Code, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), tc.message) || !strings.Contains(recorder.Body.String(), tc.code) {
				t.Fatalf("PUT %s body = %s, want %q and %q", tc.name, recorder.Body.String(), tc.message, tc.code)
			}
			after, ok, err := h.store.GetOIDCProvider(ctx)
			if err != nil || !ok {
				t.Fatalf("row after rejected PUT: ok=%v err=%v", ok, err)
			}
			if after.Enabled != before.Enabled || after.Issuer != before.Issuer || after.ClientID != before.ClientID ||
				after.RedirectURL != before.RedirectURL || after.DisplayName != before.DisplayName ||
				after.ClientSecretCiphertext != before.ClientSecretCiphertext || !after.UpdatedAt.Equal(before.UpdatedAt) {
				t.Fatalf("rejected PUT changed the row:\nbefore %+v\nafter  %+v", before, after)
			}
		})
	}
}

// TestAdminOIDCProviderDeleteClearsRowButKeepsItDisabled covers DELETE: the
// fields blank and the provider disables, while the row survives so a restart
// cannot re-seed the environment values.
func TestAdminOIDCProviderDeleteClearsRowButKeepsItDisabled(t *testing.T) {
	h := newOIDCAdminHarness(t)
	ctx := context.Background()

	if seeded, err := h.store.SeedOIDCProviderFromEnv(ctx, "https://idp.example", "client-1", "ciphertext-1", "https://gw.example/api/auth/oidc/callback", "Corp SSO"); err != nil || !seeded {
		t.Fatalf("seed = %v %v, want the row inserted", seeded, err)
	}
	deleted := h.do(t, http.MethodDelete, oidcProviderAdminPath, "")
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d %s, want 204", deleted.Code, deleted.Body.String())
	}

	view := decodeOIDCProviderView(t, h.do(t, http.MethodGet, oidcProviderAdminPath, ""))
	if view.Enabled || view.Ready || view.HasClientSecret ||
		view.Issuer != "" || view.ClientID != "" || view.RedirectURL != "" || view.DisplayName != "" {
		t.Fatalf("view after DELETE = %+v, want disabled and blank", view)
	}
	if !view.EnvSeedApplied {
		t.Fatalf("view after DELETE = %+v, want env_seed_applied to stay true", view)
	}

	provider, ok, err := h.store.GetOIDCProvider(ctx)
	if err != nil || !ok {
		t.Fatalf("row after DELETE: ok=%v err=%v, want the row kept", ok, err)
	}
	if provider.Enabled || provider.Issuer != "" || provider.ClientID != "" || provider.ClientSecretCiphertext != "" || provider.RedirectURL != "" {
		t.Fatalf("stored provider after DELETE = %+v, want blank and disabled", provider)
	}
	if !provider.EnvSeedApplied {
		t.Fatalf("stored provider after DELETE = %+v, want env_seed_applied preserved", provider)
	}
	if seeded, err := h.store.SeedOIDCProviderFromEnv(ctx, "https://idp.example", "client-1", "ciphertext-1", "https://gw.example/api/auth/oidc/callback", "Corp SSO"); err != nil || seeded {
		t.Fatalf("seed after DELETE = %v %v, want a no-op", seeded, err)
	}

	entries, err := h.store.ListAudit(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	audited := false
	for _, entry := range entries {
		if entry != nil && entry.Action == http.MethodDelete && entry.Resource == "oidc-provider" {
			audited = true
		}
	}
	if !audited {
		t.Fatalf("no audit entry for the provider delete: %+v", entries)
	}
}

// TestAdminOIDCProviderRequiresAdministratorAndCSRF covers the surrounding
// policy: the endpoints stay administrator-only, anonymous callers are
// unauthenticated, and a mutation without the CSRF header is refused.
func TestAdminOIDCProviderRequiresAdministratorAndCSRF(t *testing.T) {
	h := newOIDCAdminHarness(t)
	ctx := context.Background()

	withoutCSRF := httptest.NewRequest(http.MethodPut, oidcProviderAdminPath, strings.NewReader(`{"enabled":false}`))
	withoutCSRF.AddCookie(h.cookie)
	csrfRecorder := httptest.NewRecorder()
	h.handler.ServeHTTP(csrfRecorder, withoutCSRF)
	if csrfRecorder.Code != http.StatusForbidden || !strings.Contains(csrfRecorder.Body.String(), "csrf_failed") {
		t.Fatalf("PUT without CSRF = %d %s, want 403 csrf_failed", csrfRecorder.Code, csrfRecorder.Body.String())
	}

	userHash, err := gatewaycrypto.HashPassword("user-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.CreateUser(ctx, &model.User{Username: "bob", DisplayName: "Bob", Role: model.RoleUser, PasswordHash: userHash, Active: true}); err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRecorder()
	h.handler.ServeHTTP(login, httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"bob","password":"user-password"}`)))
	if login.Code != http.StatusOK {
		t.Fatalf("user login = %d %s", login.Code, login.Body.String())
	}
	var body struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &body); err != nil || body.CSRF == "" {
		t.Fatalf("missing CSRF token: %s", login.Body.String())
	}
	userCookie := login.Result().Cookies()[0]

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		request := httptest.NewRequest(method, oidcProviderAdminPath, strings.NewReader(`{"enabled":false}`))
		request.AddCookie(userCookie)
		request.Header.Set("X-CSRF-Token", body.CSRF)
		recorder := httptest.NewRecorder()
		h.handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "administrator_required") {
			t.Fatalf("%s as a non-admin = %d %s, want 403 administrator_required", method, recorder.Code, recorder.Body.String())
		}
	}

	anonymous := httptest.NewRecorder()
	h.handler.ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, oidcProviderAdminPath, nil))
	if anonymous.Code != http.StatusUnauthorized || !strings.Contains(anonymous.Body.String(), "authentication_required") {
		t.Fatalf("anonymous GET = %d %s, want 401 authentication_required", anonymous.Code, anonymous.Body.String())
	}
}

// TestAdminOIDCProviderUnsupportedStore keeps the partial-store contract: a
// store without the OIDC provider methods degrades to 501 instead of panicking.
func TestAdminOIDCProviderUnsupportedStore(t *testing.T) {
	passwordHash, err := gatewaycrypto.HashPassword("password")
	if err != nil {
		t.Fatal(err)
	}
	admin := &authOnlyStore{fakeAuthStore: newFakeAuthStore(&model.User{ID: 1, Username: "admin", DisplayName: "Admin", Role: model.RoleAdmin, PasswordHash: passwordHash, Active: true, PasswordLoginEnabled: true})}
	handler := NewHandlerWithGatewayAndCipher(nil, admin, admin, nil, AuthConfig{}, nil)
	cookie, csrf := adminTestSession(t, handler)

	request := httptest.NewRequest(http.MethodGet, oidcProviderAdminPath, nil)
	request.AddCookie(cookie)
	request.Header.Set("X-CSRF-Token", csrf)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotImplemented || !strings.Contains(recorder.Body.String(), "admin_store_unsupported") {
		t.Fatalf("GET against a partial store = %d %s, want 501 admin_store_unsupported", recorder.Code, recorder.Body.String())
	}
}
