package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	gatewaycrypto "github.com/zjvtit/omni-gateway/internal/crypto"
	"github.com/zjvtit/omni-gateway/internal/model"
	"github.com/zjvtit/omni-gateway/internal/store"
)

// oidcE2EStore is an in-memory AuthStore that also serves the provider source
// and the OIDC user resolution the callback needs. It records every identity
// the callback binds, so the tests can assert on issuer+subject, the generated
// username and the created account without a database.
type oidcE2EStore struct {
	*fakeAuthStore
	mu       sync.Mutex
	provider store.OIDCProvider
	exists   bool
	active   bool

	boundIssuers   []string
	boundSubjects  []string
	requestedUsers []*model.User
	boundUsers     map[string]*model.User
}

func newOIDCE2EStore(admin *model.User) *oidcE2EStore {
	return &oidcE2EStore{
		fakeAuthStore: newFakeAuthStore(admin),
		active:        true,
		boundUsers:    make(map[string]*model.User),
	}
}

func (s *oidcE2EStore) GetOIDCProvider(context.Context) (store.OIDCProvider, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.provider, s.exists, nil
}

func (s *oidcE2EStore) setActive(active bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active = active
}

func (s *oidcE2EStore) GetOrCreateOIDCUser(_ context.Context, issuer, subject string, user *model.User) (*model.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.boundIssuers = append(s.boundIssuers, issuer)
	s.boundSubjects = append(s.boundSubjects, subject)
	s.requestedUsers = append(s.requestedUsers, user)
	key := issuer + "\x00" + subject
	if existing, ok := s.boundUsers[key]; ok {
		return existing, nil
	}
	created := &model.User{
		ID:          int64(100 + len(s.boundUsers)),
		Username:    user.Username,
		DisplayName: user.DisplayName,
		Role:        user.Role,
		Active:      s.active,
	}
	s.boundUsers[key] = created
	s.usersByID[created.ID] = created
	s.usersByName[created.Username] = created
	return created, nil
}

// oidcCallbackHarness wires an oidcHandler over the in-memory store and the
// fake identity provider, with the provider row already enabled and the client
// secret encrypted by a real master cipher.
type oidcCallbackHarness struct {
	handler *oidcHandler
	store   *oidcE2EStore
	idp     *fakeOIDCIdP
	source  *oidcProviderSource
}

func newOIDCCallbackHarness(t *testing.T, idp *fakeOIDCIdP) *oidcCallbackHarness {
	t.Helper()
	cipher, err := gatewaycrypto.NewCipher(strings.Repeat("e", 64))
	if err != nil {
		t.Fatalf("build master cipher: %v", err)
	}
	secretCiphertext, err := cipher.Encrypt(oidcE2EClientSecret)
	if err != nil {
		t.Fatalf("encrypt provider secret: %v", err)
	}
	users := newOIDCE2EStore(&model.User{ID: 1, Username: "admin", Role: model.RoleAdmin, Active: true})
	users.provider = store.OIDCProvider{
		Issuer:                 idp.server.URL,
		ClientID:               oidcE2EClientID,
		ClientSecretCiphertext: secretCiphertext,
		RedirectURL:            oidcE2ERedirectURL,
		DisplayName:            "E2E IdP",
		Enabled:                true,
	}
	users.exists = true
	source := newOIDCProviderSource(users, cipher.Decrypt)
	return &oidcCallbackHarness{
		handler: newOIDCHandler(users, AuthConfig{OIDCProvider: source, OIDCClient: idp.server.Client()}),
		store:   users,
		idp:     idp,
		source:  source,
	}
}

// oidcLoginAttempt is what /start handed the browser: the state cookie plus the
// state, nonce and PKCE challenge embedded in the authorization request.
type oidcLoginAttempt struct {
	state         string
	nonce         string
	codeChallenge string
	stateCookie   *http.Cookie
}

func (h *oidcCallbackHarness) beginLogin(t *testing.T) oidcLoginAttempt {
	t.Helper()
	recorder := httptest.NewRecorder()
	h.handler.start(recorder, httptest.NewRequest(http.MethodGet, "/api/auth/oidc/start", nil))
	if recorder.Code != http.StatusFound {
		t.Fatalf("start = %d %s, want 302", recorder.Code, recorder.Body.String())
	}
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatalf("start Location %q is not a URL: %v", recorder.Header().Get("Location"), err)
	}
	if want := h.idp.server.URL + "/oauth/authorize"; location.Scheme+"://"+location.Host+location.Path != want {
		t.Fatalf("start redirected to %q, want the fake IdP authorization endpoint %q", location, want)
	}
	query := location.Query()
	if got := query.Get("code_challenge_method"); got != "S256" {
		t.Fatalf("code_challenge_method = %q, want S256", got)
	}
	attempt := oidcLoginAttempt{
		state:         query.Get("state"),
		nonce:         query.Get("nonce"),
		codeChallenge: query.Get("code_challenge"),
	}
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == oidcStateCookieName {
			attempt.stateCookie = cookie
			break
		}
	}
	if attempt.state == "" || attempt.nonce == "" || attempt.codeChallenge == "" || attempt.stateCookie == nil {
		t.Fatalf("incomplete login attempt: %+v", attempt)
	}
	if !attempt.stateCookie.HttpOnly || attempt.stateCookie.Value != attempt.state {
		t.Fatalf("state cookie = %+v, want HttpOnly and equal to state %q", attempt.stateCookie, attempt.state)
	}
	h.idp.setNonce(attempt.nonce)
	return attempt
}

func (h *oidcCallbackHarness) callbackRequest(t *testing.T, query string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/auth/oidc/callback?"+query, nil)
	if cookie != nil {
		request.AddCookie(cookie)
	}
	h.handler.callback(recorder, request)
	return recorder
}

func findResponseCookie(recorder *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}

// assertOIDCErrorRedirect is the whitelist contract: the browser lands on "/"
// with exactly one oauth_error parameter, and no session cookie is issued.
func assertOIDCErrorRedirect(t *testing.T, recorder *httptest.ResponseRecorder, want string) {
	t.Helper()
	if recorder.Code != http.StatusFound {
		t.Fatalf("callback = %d %s, want a 302 redirect", recorder.Code, recorder.Body.String())
	}
	if location := recorder.Header().Get("Location"); location != "/?oauth_error="+want {
		t.Fatalf("Location = %q, want %q", location, "/?oauth_error="+want)
	}
	if cookie := findResponseCookie(recorder, sessionCookieName); cookie != nil {
		t.Fatalf("failed callback minted a session cookie: %v", cookie)
	}
}

// assertNoSensitiveOIDCValues is the leak contract: neither the body nor the
// Location header may carry the client secret, the provider tokens or the raw
// id_token.
func assertNoSensitiveOIDCValues(t *testing.T, label string, recorder *httptest.ResponseRecorder, values ...string) {
	t.Helper()
	haystacks := map[string]string{
		"body":     recorder.Body.String(),
		"Location": recorder.Header().Get("Location"),
	}
	for where, haystack := range haystacks {
		for _, value := range values {
			if value == "" {
				continue
			}
			if strings.Contains(haystack, value) {
				t.Fatalf("%s %s leaked %q: %s", label, where, value, haystack)
			}
		}
	}
}

// ① A correct callback mints a password-identical session, binds the identity
// on issuer+subject and creates a fresh role=user account named oidc-*.
func TestOIDCCallbackCreatesUserAndSession(t *testing.T) {
	idp := newFakeOIDCIdP(t)
	h := newOIDCCallbackHarness(t, idp)
	attempt := h.beginLogin(t)

	recorder := h.callbackRequest(t, "code=e2e-code&state="+url.QueryEscape(attempt.state), attempt.stateCookie)
	assertNoSensitiveOIDCValues(t, "success", recorder, oidcE2EClientSecret, oidcE2EAccessToken, oidcE2ERefreshToken, idp.recordedIDToken())
	if recorder.Code != http.StatusFound {
		t.Fatalf("callback = %d %s, want 302", recorder.Code, recorder.Body.String())
	}
	if location := recorder.Header().Get("Location"); location != "/" {
		t.Fatalf("callback Location = %q, want %q", location, "/")
	}

	sessionCookie := findResponseCookie(recorder, sessionCookieName)
	if sessionCookie == nil {
		t.Fatalf("callback did not set the %s cookie: %v", sessionCookieName, recorder.Result().Cookies())
	}
	// The OIDC session cookie carries exactly the password-login attributes.
	if !sessionCookie.HttpOnly || sessionCookie.Path != "/" || sessionCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie attributes = %+v, want HttpOnly, Path=/, SameSite=Lax", sessionCookie)
	}
	if sessionCookie.Secure != h.handler.cookieSecure {
		t.Fatalf("session cookie Secure = %v, want %v", sessionCookie.Secure, h.handler.cookieSecure)
	}
	if sessionCookie.MaxAge < 1 || sessionCookie.Expires.IsZero() {
		t.Fatalf("session cookie lacks an expiry: %+v", sessionCookie)
	}

	session, ok := h.store.sessions[sessionCookie.Value]
	if !ok {
		t.Fatalf("no session row stored for the issued cookie %q", sessionCookie.Value)
	}

	// Identity binding uses issuer+subject, never email or username.
	if len(h.store.boundIssuers) != 1 || h.store.boundIssuers[0] != idp.server.URL {
		t.Fatalf("bound issuers = %v, want [%s]", h.store.boundIssuers, idp.server.URL)
	}
	if len(h.store.boundSubjects) != 1 || h.store.boundSubjects[0] != oidcE2ESubject {
		t.Fatalf("bound subjects = %v, want [%s]", h.store.boundSubjects, oidcE2ESubject)
	}
	created := h.store.boundUsers[idp.server.URL+"\x00"+oidcE2ESubject]
	if created == nil {
		t.Fatalf("no account bound to issuer+subject: %v", h.store.boundUsers)
	}
	if !strings.HasPrefix(created.Username, "oidc-") {
		t.Fatalf("created username = %q, want an oidc- prefix", created.Username)
	}
	if created.Role != model.RoleUser {
		t.Fatalf("created role = %q, want %q", created.Role, model.RoleUser)
	}
	if created.DisplayName != "E2E User" {
		t.Fatalf("created display name = %q, want the id_token name claim", created.DisplayName)
	}
	if session.userID != created.ID {
		t.Fatalf("session user = %d, want the created account %d", session.userID, created.ID)
	}
	if len(h.store.requestedUsers) != 1 || !strings.HasPrefix(h.store.requestedUsers[0].Username, "oidc-") {
		t.Fatalf("requested account = %+v, want the oidc-* placeholder", h.store.requestedUsers)
	}

	// PKCE: the verifier sent to the token endpoint must hash to the challenge.
	verifier := idp.recordedCodeVerifier()
	if verifier == "" {
		t.Fatal("token exchange sent no code_verifier")
	}
	digest := sha256.Sum256([]byte(verifier))
	if got := base64.RawURLEncoding.EncodeToString(digest[:]); got != attempt.codeChallenge {
		t.Fatalf("code_verifier hashes to %q, want the authorization challenge %q", got, attempt.codeChallenge)
	}
	if calls := idp.tokenCallCount(); calls != 1 {
		t.Fatalf("token endpoint calls = %d, want 1", calls)
	}
}

// ② A callback whose state does not match the cookie, or whose cookie does not
// match the state, fails with a whitelisted code and no session.
func TestOIDCCallbackRejectsStateMismatch(t *testing.T) {
	idp := newFakeOIDCIdP(t)
	h := newOIDCCallbackHarness(t, idp)

	tamperedState := h.beginLogin(t)
	recorder := h.callbackRequest(t, "code=e2e-code&state="+url.QueryEscape(tamperedState.state+"-tampered"), tamperedState.stateCookie)
	assertNoSensitiveOIDCValues(t, "tampered state", recorder, oidcE2EClientSecret, oidcE2EAccessToken, oidcE2ERefreshToken, idp.recordedIDToken())
	assertOIDCErrorRedirect(t, recorder, "invalid_state")

	wrongCookie := h.beginLogin(t)
	recorder = h.callbackRequest(t, "code=e2e-code&state="+url.QueryEscape(wrongCookie.state),
		&http.Cookie{Name: oidcStateCookieName, Value: "not-the-state"})
	assertNoSensitiveOIDCValues(t, "wrong cookie", recorder, oidcE2EClientSecret, oidcE2EAccessToken, oidcE2ERefreshToken, idp.recordedIDToken())
	assertOIDCErrorRedirect(t, recorder, "invalid_state")

	if calls := idp.tokenCallCount(); calls != 0 {
		t.Fatalf("token endpoint calls for mismatched state = %d, want 0", calls)
	}
	if len(h.store.boundIssuers) != 0 {
		t.Fatalf("mismatched state bound identities: %v", h.store.boundIssuers)
	}
}

// ③ The state is single-use: replaying a consumed callback fails and never
// exchanges the code again.
func TestOIDCCallbackRejectsStateReplay(t *testing.T) {
	idp := newFakeOIDCIdP(t)
	h := newOIDCCallbackHarness(t, idp)
	attempt := h.beginLogin(t)
	callbackURL := "code=e2e-code&state=" + url.QueryEscape(attempt.state)

	first := h.callbackRequest(t, callbackURL, attempt.stateCookie)
	if first.Code != http.StatusFound || first.Header().Get("Location") != "/" {
		t.Fatalf("first callback = %d %q, want 302 to /", first.Code, first.Header().Get("Location"))
	}
	if findResponseCookie(first, sessionCookieName) == nil {
		t.Fatalf("first callback did not mint a session: %v", first.Result().Cookies())
	}

	replay := h.callbackRequest(t, callbackURL, attempt.stateCookie)
	assertNoSensitiveOIDCValues(t, "replay", replay, oidcE2EClientSecret, oidcE2EAccessToken, oidcE2ERefreshToken, idp.recordedIDToken())
	assertOIDCErrorRedirect(t, replay, "state_expired")

	if calls := idp.tokenCallCount(); calls != 1 {
		t.Fatalf("token endpoint calls = %d, want 1 (the replay must not exchange again)", calls)
	}
}
