package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/zjvtit/omni-gateway/internal/model"
	"github.com/zjvtit/omni-gateway/internal/store"
)

// oidcTestProviderSource is the cache-backed runtime view a handler consumes,
// reporting exactly config. The stored row carries a placeholder ciphertext so
// the source exercises its decrypt path, and the injectable decryptor maps it
// to the test's secret: no real master key is involved.
func oidcTestProviderSource(t *testing.T, config oidcProviderConfig) *oidcProviderSource {
	t.Helper()
	stored := store.OIDCProvider{
		Issuer:      config.Issuer,
		ClientID:    config.ClientID,
		RedirectURL: config.RedirectURL,
		DisplayName: config.DisplayName,
		Enabled:     config.Enabled,
	}
	if config.ClientSecret != "" {
		stored.ClientSecretCiphertext = "test-client-secret-ciphertext"
	}
	return newOIDCProviderSource(&stubOIDCProviderStore{provider: stored, exists: true}, func(string) (string, error) {
		return config.ClientSecret, nil
	})
}

// newOIDCTestHandler builds an OIDC handler over the shared auth fake and a
// provider source that reports config. The tests here never reach the store,
// but the constructor still needs one.
func newOIDCTestHandler(t *testing.T, config oidcProviderConfig) *oidcHandler {
	t.Helper()
	users := newFakeAuthStore(&model.User{ID: 1, Username: "admin", Role: model.RoleAdmin, Active: true})
	return newOIDCHandler(users, AuthConfig{OIDCProvider: oidcTestProviderSource(t, config)})
}

// decodeOIDCConfig decodes the /config payload and returns the raw body so a
// test can also search it for values that must not cross the boundary.
func decodeOIDCConfig(t *testing.T, recorder *httptest.ResponseRecorder) (bool, string, string) {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("config returned status %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var body struct {
		Enabled     bool   `json:"enabled"`
		DisplayName string `json:"display_name"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode config response %q: %v", recorder.Body.String(), err)
	}
	return body.Enabled, body.DisplayName, recorder.Body.String()
}

// A partial provider configuration keeps the feature flag off, and the client
// secret never crosses the boundary no matter which field is missing.
func TestOIDCConfigDisabledWithoutFullConfig(t *testing.T) {
	const secret = "oidc-client-secret-do-not-leak"
	cases := []struct {
		name   string
		config oidcProviderConfig
	}{
		{
			name: "stored but disabled",
			config: oidcProviderConfig{
				Issuer: "https://issuer.example", ClientID: "client-id", ClientSecret: secret,
				RedirectURL: "https://gateway.example/api/auth/oidc/callback", DisplayName: "企业账号",
			},
		},
		{
			name: "enabled but missing client secret",
			config: oidcProviderConfig{
				Issuer: "https://issuer.example", ClientID: "client-id",
				RedirectURL: "https://gateway.example/api/auth/oidc/callback", DisplayName: "企业账号", Enabled: true,
			},
		},
		{
			name: "enabled but missing issuer",
			config: oidcProviderConfig{
				ClientID: "client-id", ClientSecret: secret,
				RedirectURL: "https://gateway.example/api/auth/oidc/callback", Enabled: true,
			},
		},
		{
			name: "enabled but missing client id",
			config: oidcProviderConfig{
				Issuer: "https://issuer.example", ClientSecret: secret,
				RedirectURL: "https://gateway.example/api/auth/oidc/callback", Enabled: true,
			},
		},
		{
			name: "enabled but missing redirect url",
			config: oidcProviderConfig{
				Issuer: "https://issuer.example", ClientID: "client-id", ClientSecret: secret, Enabled: true,
			},
		},
		{
			name: "enabled with only issuer and client id",
			config: oidcProviderConfig{
				Issuer: "https://issuer.example", ClientID: "client-id", Enabled: true,
			},
		},
		{
			name:   "nothing stored",
			config: oidcProviderConfig{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := newOIDCTestHandler(t, tc.config)
			recorder := httptest.NewRecorder()
			handler.config(recorder, httptest.NewRequest(http.MethodGet, "/api/auth/oidc/config", nil))

			enabled, displayName, raw := decodeOIDCConfig(t, recorder)
			if enabled {
				t.Fatalf("partial config reported enabled=true: %s", raw)
			}
			if displayName != "" {
				t.Fatalf("disabled config leaked display name %q", displayName)
			}
			if strings.Contains(raw, "client_secret") {
				t.Fatalf("config response mentions client_secret: %s", raw)
			}
			if tc.config.ClientSecret != "" && strings.Contains(raw, tc.config.ClientSecret) {
				t.Fatalf("config response leaked the client secret: %s", raw)
			}
		})
	}
}

// With all four fields set and the provider enabled the console learns the flag
// and the cosmetic label, and only those: the secret is consumed server-side by
// the code exchange.
func TestOIDCConfigEnabledAndHidesSecret(t *testing.T) {
	const secret = "oidc-client-secret-do-not-leak"
	handler := newOIDCTestHandler(t, oidcProviderConfig{
		Issuer:       "https://issuer.example",
		ClientID:     "console",
		ClientSecret: secret,
		RedirectURL:  "https://gateway.example/api/auth/oidc/callback",
		DisplayName:  "企业账号",
		Enabled:      true,
	})
	recorder := httptest.NewRecorder()
	handler.config(recorder, httptest.NewRequest(http.MethodGet, "/api/auth/oidc/config", nil))

	enabled, displayName, raw := decodeOIDCConfig(t, recorder)
	if !enabled {
		t.Fatalf("full config reported enabled=false: %s", raw)
	}
	if displayName != "企业账号" {
		t.Fatalf("display_name = %q, want %q", displayName, "企业账号")
	}
	if strings.Contains(raw, secret) {
		t.Fatalf("config response leaked the client secret: %s", raw)
	}
	if strings.Contains(raw, "client_secret") {
		t.Fatalf("config response mentions client_secret: %s", raw)
	}
}

// A provider source that cannot read the row must fail closed: the console is
// told the feature is unavailable without any internal error crossing over.
func TestOIDCConfigFailsClosedOnSourceError(t *testing.T) {
	users := newFakeAuthStore(&model.User{ID: 1, Username: "admin", Role: model.RoleAdmin, Active: true})
	handler := newOIDCHandler(users, AuthConfig{
		OIDCProvider: newOIDCProviderSource(&stubOIDCProviderStore{err: errors.New("database connection refused")}, nil),
	})
	recorder := httptest.NewRecorder()
	handler.config(recorder, httptest.NewRequest(http.MethodGet, "/api/auth/oidc/config", nil))

	enabled, displayName, raw := decodeOIDCConfig(t, recorder)
	if enabled || displayName != "" {
		t.Fatalf("config from a failing source = enabled %v display %q: %s", enabled, displayName, raw)
	}
	if strings.Contains(raw, "database connection refused") {
		t.Fatalf("config response leaked the source error: %s", raw)
	}
}

// newOIDCTestIssuer serves just enough discovery metadata to build an
// authorization request. The /start path fetches only the discovery document;
// token and JWKS endpoints are never called.
func newOIDCTestIssuer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		issuer := "http://" + r.Host
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":                 issuer,
			"authorization_endpoint": issuer + "/oauth/authorize",
			"token_endpoint":         issuer + "/oauth/token",
			"jwks_uri":               issuer + "/oauth/jwks",
		})
	}))
	t.Cleanup(server.Close)
	return server
}

// /start must redirect to the provider with a PKCE-protected authorization
// request and hand the browser an HttpOnly state cookie that matches it.
func TestOIDCStartBuildsPKCERequest(t *testing.T) {
	const (
		clientID    = "console"
		redirectURL = "https://gateway.example/api/auth/oidc/callback"
	)
	issuer := newOIDCTestIssuer(t)
	users := newFakeAuthStore(&model.User{ID: 1, Username: "admin", Role: model.RoleAdmin, Active: true})
	handler := newOIDCHandler(users, AuthConfig{
		OIDCProvider: oidcTestProviderSource(t, oidcProviderConfig{
			Issuer:       issuer.URL,
			ClientID:     clientID,
			ClientSecret: "client-secret",
			RedirectURL:  redirectURL,
			Enabled:      true,
		}),
		OIDCClient: issuer.Client(),
	})

	recorder := httptest.NewRecorder()
	handler.start(recorder, httptest.NewRequest(http.MethodGet, "/api/auth/oidc/start", nil))
	if recorder.Code != http.StatusFound {
		t.Fatalf("start returned status %d, want %d: %s", recorder.Code, http.StatusFound, recorder.Body.String())
	}

	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatalf("start Location %q is not a URL: %v", recorder.Header().Get("Location"), err)
	}
	wantEndpoint, err := url.Parse(issuer.URL + "/oauth/authorize")
	if err != nil {
		t.Fatalf("parse issuer authorization endpoint: %v", err)
	}
	if location.Scheme != wantEndpoint.Scheme || location.Host != wantEndpoint.Host || location.Path != wantEndpoint.Path {
		t.Fatalf("start redirected to %q, want the issuer authorization endpoint %q", location, wantEndpoint)
	}

	query := location.Query()
	for key, want := range map[string]string{
		"response_type":         "code",
		"client_id":             clientID,
		"redirect_uri":          redirectURL,
		"code_challenge_method": "S256",
	} {
		if got := query.Get(key); got != want {
			t.Errorf("authorization request %s = %q, want %q", key, got, want)
		}
	}
	for _, key := range []string{"state", "nonce", "code_challenge"} {
		if query.Get(key) == "" {
			t.Errorf("authorization request %s is empty", key)
		}
	}
	if scope := query.Get("scope"); !strings.Contains(" "+scope+" ", " openid ") {
		t.Errorf("authorization request scope %q does not include openid", scope)
	}

	var stateCookie *http.Cookie
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == oidcStateCookieName {
			stateCookie = cookie
			break
		}
	}
	if stateCookie == nil {
		t.Fatalf("start did not set the %s cookie: %v", oidcStateCookieName, recorder.Result().Cookies())
	}
	if !stateCookie.HttpOnly {
		t.Errorf("state cookie is not HttpOnly: %#v", stateCookie)
	}
	if stateCookie.Value == "" {
		t.Error("state cookie is empty")
	}
	if stateCookie.Value != query.Get("state") {
		t.Errorf("state cookie %q does not match the state parameter %q", stateCookie.Value, query.Get("state"))
	}
}

// An unconfigured gateway must refuse /start with the whitelisted code and
// must not hand out a state cookie.
func TestOIDCStartNotConfiguredUsesWhitelistedCode(t *testing.T) {
	handler := newOIDCTestHandler(t, oidcProviderConfig{})
	recorder := httptest.NewRecorder()
	handler.start(recorder, httptest.NewRequest(http.MethodGet, "/api/auth/oidc/start", nil))

	if recorder.Code != http.StatusFound {
		t.Fatalf("start returned status %d, want %d: %s", recorder.Code, http.StatusFound, recorder.Body.String())
	}
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatalf("start Location %q is not a URL: %v", recorder.Header().Get("Location"), err)
	}
	if got := location.Query().Get("oauth_error"); got != "oidc_not_configured" {
		t.Fatalf("oauth_error = %q, want %q (Location %q)", got, "oidc_not_configured", location)
	}
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == oidcStateCookieName {
			t.Fatalf("unconfigured start set a state cookie: %v", cookie)
		}
	}
}

// An unconfigured gateway must refuse /callback with the whitelisted code
// before any state or provider data is touched.
func TestOIDCCallbackNotConfiguredUsesWhitelistedCode(t *testing.T) {
	handler := newOIDCTestHandler(t, oidcProviderConfig{})
	recorder := httptest.NewRecorder()
	handler.callback(recorder, httptest.NewRequest(http.MethodGet, "/api/auth/oidc/callback?code=code&state=state", nil))

	if recorder.Code != http.StatusFound {
		t.Fatalf("callback returned status %d, want %d: %s", recorder.Code, http.StatusFound, recorder.Body.String())
	}
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatalf("callback Location %q is not a URL: %v", recorder.Header().Get("Location"), err)
	}
	if got := location.Query().Get("oauth_error"); got != "oidc_not_configured" {
		t.Fatalf("oauth_error = %q, want %q (Location %q)", got, "oidc_not_configured", location)
	}
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == sessionCookieName {
			t.Fatalf("unconfigured callback minted a session cookie: %v", cookie)
		}
	}
}
