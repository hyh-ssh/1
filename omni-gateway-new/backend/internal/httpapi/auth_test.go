package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gatewaycrypto "github.com/zjvtit/omni-gateway/internal/crypto"
	"github.com/zjvtit/omni-gateway/internal/model"
	"github.com/zjvtit/omni-gateway/internal/store"
)

type fakeAuthStore struct {
	usersByName map[string]*model.User
	usersByID   map[int64]*model.User
	sessions    map[string]fakeSession
}

type fakeSession struct {
	userID  int64
	csrf    string
	expires time.Time
}

func newFakeAuthStore(user *model.User) *fakeAuthStore {
	return &fakeAuthStore{
		usersByName: map[string]*model.User{user.Username: user},
		usersByID:   map[int64]*model.User{user.ID: user},
		sessions:    map[string]fakeSession{},
	}
}

func (s *fakeAuthStore) GetUserByUsername(_ context.Context, username string) (*model.User, error) {
	user, ok := s.usersByName[username]
	if !ok {
		return nil, store.ErrNotFound
	}
	return user, nil
}

func (s *fakeAuthStore) GetUserByID(_ context.Context, id int64) (*model.User, error) {
	user, ok := s.usersByID[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return user, nil
}

func (s *fakeAuthStore) CreateSession(_ context.Context, id string, userID int64, csrf string, expires time.Time) error {
	s.sessions[id] = fakeSession{userID: userID, csrf: csrf, expires: expires}
	return nil
}

func (s *fakeAuthStore) GetSession(_ context.Context, id string) (int64, string, time.Time, error) {
	session, ok := s.sessions[id]
	if !ok {
		return 0, "", time.Time{}, store.ErrNotFound
	}
	return session.userID, session.csrf, session.expires, nil
}

func (s *fakeAuthStore) DeleteSession(_ context.Context, id string) error {
	if _, ok := s.sessions[id]; !ok {
		return store.ErrNotFound
	}
	delete(s.sessions, id)
	return nil
}

func TestManagementSessionLifecycle(t *testing.T) {
	passwordHash, err := gatewaycrypto.HashPassword("password")
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}
	users := newFakeAuthStore(&model.User{
		ID:                   7,
		Username:             "admin",
		DisplayName:          "Administrator",
		Role:                 model.RoleAdmin,
		PasswordHash:         passwordHash,
		Active:               true,
		PasswordLoginEnabled: true,
	})
	handler := NewHandlerWithAuth(nil, users, AuthConfig{SessionTTL: time.Hour, CookieSecure: true})

	loginRequest := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"admin","password":"password"}`))
	loginRecorder := httptest.NewRecorder()
	handler.ServeHTTP(loginRecorder, loginRequest)
	if loginRecorder.Code != http.StatusOK {
		t.Fatalf("login returned status %d: %s", loginRecorder.Code, loginRecorder.Body.String())
	}
	cookies := loginRecorder.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookieName || cookies[0].Value == "" || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("unexpected session cookie: %#v", cookies)
	}
	var loginResponse struct {
		User      authUser `json:"user"`
		CSRFToken string   `json:"csrf_token"`
	}
	if err := json.Unmarshal(loginRecorder.Body.Bytes(), &loginResponse); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	if loginResponse.User.ID != 7 || loginResponse.User.Username != "admin" || loginResponse.CSRFToken == "" {
		t.Fatalf("unexpected login response: %#v", loginResponse)
	}

	meRequest := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	meRequest.AddCookie(cookies[0])
	meRecorder := httptest.NewRecorder()
	handler.ServeHTTP(meRecorder, meRequest)
	if meRecorder.Code != http.StatusOK || !strings.Contains(meRecorder.Body.String(), `"username":"admin"`) {
		t.Fatalf("unexpected /me response: %d %s", meRecorder.Code, meRecorder.Body.String())
	}

	logoutRequest := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	logoutRequest.AddCookie(cookies[0])
	logoutRequest.Header.Set("X-CSRF-Token", loginResponse.CSRFToken)
	logoutRecorder := httptest.NewRecorder()
	handler.ServeHTTP(logoutRecorder, logoutRequest)
	if logoutRecorder.Code != http.StatusNoContent {
		t.Fatalf("logout returned status %d: %s", logoutRecorder.Code, logoutRecorder.Body.String())
	}
	if len(users.sessions) != 0 {
		t.Fatalf("logout did not delete session: %#v", users.sessions)
	}
}

// A login with a wrong password must not create a session, and a disabled
// account must be told apart from bad credentials. Previously both cases
// answered "invalid credentials", which made a newly created account look like
// a typo to the person signing in.
func TestManagementLoginRejectsBadCredentialsAndReportsDisabledAccounts(t *testing.T) {
	passwordHash, err := gatewaycrypto.HashPassword("password")
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}
	cases := []struct {
		name    string
		user    *model.User
		body    string
		status  int
		code    string
		message string
	}{
		{
			name:    "wrong password",
			user:    &model.User{ID: 1, Username: "admin", Role: model.RoleAdmin, PasswordHash: passwordHash, Active: true, PasswordLoginEnabled: true},
			body:    `{"username":"admin","password":"wrong"}`,
			status:  http.StatusUnauthorized,
			code:    "invalid_credentials",
			message: "invalid credentials",
		},
		{
			name:    "unknown account",
			user:    &model.User{ID: 1, Username: "admin", Role: model.RoleAdmin, PasswordHash: passwordHash, Active: true, PasswordLoginEnabled: true},
			body:    `{"username":"nobody","password":"password"}`,
			status:  http.StatusUnauthorized,
			code:    "invalid_credentials",
			message: "invalid credentials",
		},
		{
			name:    "inactive admin",
			user:    &model.User{ID: 1, Username: "admin", Role: model.RoleAdmin, PasswordHash: passwordHash, Active: false},
			body:    `{"username":"admin","password":"password"}`,
			status:  http.StatusForbidden,
			code:    "account_disabled",
			message: "account is disabled",
		},
		{
			name:    "inactive ordinary account",
			user:    &model.User{ID: 2, Username: "user", Role: model.RoleUser, PasswordHash: passwordHash, Active: false},
			body:    `{"username":"user","password":"password"}`,
			status:  http.StatusForbidden,
			code:    "account_disabled",
			message: "account is disabled",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			users := newFakeAuthStore(tc.user)
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(tc.body))
			NewHandlerWithAuth(nil, users, AuthConfig{}).ServeHTTP(recorder, req)
			if recorder.Code != tc.status {
				t.Fatalf("got status %d, want %d: %s", recorder.Code, tc.status, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), `"code":"`+tc.code+`"`) {
				t.Fatalf("unexpected error body: %s", recorder.Body.String())
			}
			// The console translates by message text (errors.ts), so the exact
			// wording is part of the contract: changing it silently reverts the
			// UI to English.
			if !strings.Contains(recorder.Body.String(), `"message":"`+tc.message+`"`) {
				t.Fatalf("error message is not the one the console maps: %s", recorder.Body.String())
			}
			if len(users.sessions) != 0 {
				t.Fatalf("a failed login created a session: %#v", users.sessions)
			}
		})
	}
}

// Any active account may sign in; only the admin role reaches /api/admin, and
// an ordinary account reads its own data through /api/self.
func TestManagementLoginAdmitsOrdinaryAccount(t *testing.T) {
	passwordHash, err := gatewaycrypto.HashPassword("password")
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}
	users := newFakeAuthStore(&model.User{ID: 4, Username: "alice", DisplayName: "Alice", Role: model.RoleUser, PasswordHash: passwordHash, Active: true, PasswordLoginEnabled: true})
	handler := NewHandlerWithAuth(nil, users, AuthConfig{})

	loginRecorder := httptest.NewRecorder()
	handler.ServeHTTP(loginRecorder, httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"alice","password":"password"}`)))
	if loginRecorder.Code != http.StatusOK {
		t.Fatalf("ordinary account login returned %d: %s", loginRecorder.Code, loginRecorder.Body.String())
	}
	var loginResponse struct {
		User      authUser `json:"user"`
		CSRFToken string   `json:"csrf_token"`
	}
	if err := json.Unmarshal(loginRecorder.Body.Bytes(), &loginResponse); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	if loginResponse.User.Role != model.RoleUser || loginResponse.CSRFToken == "" {
		t.Fatalf("unexpected login response: %#v", loginResponse)
	}
	cookies := loginRecorder.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("login did not set a session cookie")
	}

	meRecorder := httptest.NewRecorder()
	meRequest := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	meRequest.AddCookie(cookies[0])
	handler.ServeHTTP(meRecorder, meRequest)
	if meRecorder.Code != http.StatusOK || !strings.Contains(meRecorder.Body.String(), `"role":"user"`) {
		t.Fatalf("me returned %d: %s", meRecorder.Code, meRecorder.Body.String())
	}

	// Holding a session is not authorization: the management surface answers
	// 403 for a valid non-admin session, and the write never reaches the store.
	for _, tc := range []struct {
		method string
		path   string
		csrf   bool
	}{
		{method: http.MethodGet, path: "/api/admin/users"},
		{method: http.MethodPost, path: "/api/admin/users", csrf: true},
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{"username":"mallory","password":"x"}`))
		request.AddCookie(cookies[0])
		if tc.csrf {
			request.Header.Set("X-CSRF-Token", loginResponse.CSRFToken)
		}
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "administrator_required") {
			t.Fatalf("%s %s returned %d: %s", tc.method, tc.path, recorder.Code, recorder.Body.String())
		}
	}
}

func TestManagementLogoutRequiresCSRF(t *testing.T) {
	passwordHash, err := gatewaycrypto.HashPassword("password")
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}
	users := newFakeAuthStore(&model.User{ID: 1, Username: "admin", Role: model.RoleAdmin, PasswordHash: passwordHash, Active: true, PasswordLoginEnabled: true})
	handler := NewHandlerWithAuth(nil, users, AuthConfig{})

	loginRecorder := httptest.NewRecorder()
	handler.ServeHTTP(loginRecorder, httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"admin","password":"password"}`)))
	cookie := loginRecorder.Result().Cookies()[0]
	var loginResponse struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.Unmarshal(loginRecorder.Body.Bytes(), &loginResponse); err != nil {
		t.Fatalf("decode login response: %v", err)
	}

	logoutRecorder := httptest.NewRecorder()
	logoutRequest := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	logoutRequest.AddCookie(cookie)
	handler.ServeHTTP(logoutRecorder, logoutRequest)
	if logoutRecorder.Code != http.StatusForbidden {
		t.Fatalf("got status %d, want %d", logoutRecorder.Code, http.StatusForbidden)
	}
	if len(users.sessions) != 1 {
		t.Fatal("invalid CSRF request removed the session")
	}

	logoutRecorder = httptest.NewRecorder()
	logoutRequest = httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	logoutRequest.AddCookie(cookie)
	logoutRequest.Header.Set("X-CSRF-Token", loginResponse.CSRFToken)
	handler.ServeHTTP(logoutRecorder, logoutRequest)
	if logoutRecorder.Code != http.StatusNoContent {
		t.Fatalf("got status %d, want %d", logoutRecorder.Code, http.StatusNoContent)
	}
}

func TestManagementRoutesReturnExpectedErrors(t *testing.T) {
	users := newFakeAuthStore(&model.User{ID: 1, Username: "admin", Role: model.RoleAdmin, Active: true})
	handler := NewHandlerWithAuth(nil, users, AuthConfig{})

	badLogin := httptest.NewRecorder()
	handler.ServeHTTP(badLogin, httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader("not-json")))
	if badLogin.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want %d", badLogin.Code, http.StatusBadRequest)
	}

	unauthenticated := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/api/auth/me", nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("got status %d, want %d", unauthenticated.Code, http.StatusUnauthorized)
	}

	method := httptest.NewRecorder()
	handler.ServeHTTP(method, httptest.NewRequest(http.MethodGet, "/api/auth/login", nil))
	if method.Code != http.StatusMethodNotAllowed {
		t.Fatalf("got status %d, want %d", method.Code, http.StatusMethodNotAllowed)
	}

}

// Deactivating an account must also cut off a session that is already open;
// otherwise disabling somebody would only stop their *next* sign-in. Ordinary
// accounts can sign in now, so this is the control an administrator relies on.
func TestDeactivatedAccountLosesItsLiveSession(t *testing.T) {
	passwordHash, err := gatewaycrypto.HashPassword("password")
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}
	user := &model.User{ID: 2, Username: "alice", Role: model.RoleUser, PasswordHash: passwordHash, Active: true, PasswordLoginEnabled: true}
	users := newFakeAuthStore(user)
	handler := NewHandlerWithAuth(nil, users, AuthConfig{})

	loginRecorder := httptest.NewRecorder()
	handler.ServeHTTP(loginRecorder, httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"alice","password":"password"}`)))
	if loginRecorder.Code != http.StatusOK {
		t.Fatalf("login returned %d: %s", loginRecorder.Code, loginRecorder.Body.String())
	}
	cookies := loginRecorder.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("login did not set a session cookie")
	}

	user.Active = false

	for _, path := range []string{"/api/auth/me", "/api/self/summary"} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.AddCookie(cookies[0])
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized || !strings.Contains(recorder.Body.String(), "authentication_required") {
			t.Fatalf("%s with a deactivated account returned %d: %s", path, recorder.Code, recorder.Body.String())
		}
	}
}

// A login for a username that does not exist verifies the submitted password
// against a dummy hash, so it costs what a real account costs and the response
// time no longer answers "does this username exist?". That only holds while the
// dummy hash is well formed: VerifyPassword returns an error *before* running
// Argon2id when the format or the cost parameters do not match, which is the
// fast path this equalization removes.
func TestUnknownUsernamePaysForAPasswordCheck(t *testing.T) {
	hash := dummyPasswordHash()
	if hash == "" {
		t.Fatal("the timing equalizer hash could not be computed")
	}
	matched, err := gatewaycrypto.VerifyPassword("definitely-not-the-password", hash)
	if err != nil {
		t.Fatalf("the equalizer hash is not a usable Argon2id hash, so an unknown username answers without hashing: %v", err)
	}
	if matched {
		t.Fatal("the equalizer hash matches an arbitrary password")
	}

	// Prove the two cases really do take the same code path: a wrong password
	// against a real hash and the same password against the dummy hash both
	// return "not valid" without an error, and neither creates a session.
	users := newFakeAuthStore(&model.User{ID: 1, Username: "admin", Role: model.RoleAdmin, PasswordHash: hash, Active: true, PasswordLoginEnabled: true})
	handler := NewHandlerWithAuth(nil, users, AuthConfig{})
	for _, username := range []string{"admin", "nobody"} {
		recorder := httptest.NewRecorder()
		body := `{"username":"` + username + `","password":"definitely-not-the-password"}`
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body)))
		if recorder.Code != http.StatusUnauthorized || !strings.Contains(recorder.Body.String(), "invalid_credentials") {
			t.Fatalf("%s returned %d: %s", username, recorder.Code, recorder.Body.String())
		}
	}
	if len(users.sessions) != 0 {
		t.Fatalf("a failed login created a session: %#v", users.sessions)
	}
}

// An account that OIDC provisioned has password login switched off until an
// administrator sets a password. A caller who submits the account's *correct*
// password must get exactly the response a wrong password gets: any difference
// in status, code, message or headers would tell an anonymous caller that the
// account exists and which sign-in method it uses.
func TestPasswordLoginDisabledAccountAnswersExactlyLikeAWrongPassword(t *testing.T) {
	passwordHash, err := gatewaycrypto.HashPassword("password")
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}
	oidcUser := &model.User{ID: 9, Username: "oidc-user", DisplayName: "OIDC User", Role: model.RoleUser, PasswordHash: passwordHash, Active: true}
	passwordUser := &model.User{ID: 9, Username: "oidc-user", DisplayName: "OIDC User", Role: model.RoleUser, PasswordHash: passwordHash, Active: true, PasswordLoginEnabled: true}

	attempt := func(user *model.User, body string) (*httptest.ResponseRecorder, *fakeAuthStore) {
		t.Helper()
		users := newFakeAuthStore(user)
		recorder := httptest.NewRecorder()
		NewHandlerWithAuth(nil, users, AuthConfig{}).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body)))
		return recorder, users
	}

	// The reference answer: an ordinary wrong password on an account that may
	// sign in with one.
	reference, _ := attempt(passwordUser, `{"username":"oidc-user","password":"wrong"}`)
	if reference.Code != http.StatusUnauthorized {
		t.Fatalf("wrong-password reference returned %d: %s", reference.Code, reference.Body.String())
	}
	// The body carries a per-request request_id, so "identical" means identical
	// in every other field: the same error type, code and message.
	type errorBody struct {
		Error struct {
			Message   string `json:"message"`
			Type      string `json:"type"`
			Code      string `json:"code"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	describe := func(body string) (errorBody, string) {
		t.Helper()
		var parsed errorBody
		if err := json.Unmarshal([]byte(body), &parsed); err != nil {
			t.Fatalf("decode error body %q: %v", body, err)
		}
		requestID := parsed.Error.RequestID
		if requestID == "" {
			t.Fatalf("error body carries no request_id: %s", body)
		}
		parsed.Error.RequestID = ""
		return parsed, requestID
	}
	referenceBody, referenceRequestID := describe(reference.Body.String())
	for _, tc := range []struct {
		name string
		user *model.User
		body string
	}{
		{name: "correct password without password login", user: oidcUser, body: `{"username":"oidc-user","password":"password"}`},
		{name: "unknown account", user: passwordUser, body: `{"username":"nobody","password":"password"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder, users := attempt(tc.user, tc.body)
			if recorder.Code != reference.Code {
				t.Fatalf("got status %d, want the wrong-password status %d: %s", recorder.Code, reference.Code, recorder.Body.String())
			}
			gotBody, gotRequestID := describe(recorder.Body.String())
			if gotBody != referenceBody {
				t.Fatalf("got error %+v, want the wrong-password error %+v", gotBody, referenceBody)
			}
			if gotRequestID == referenceRequestID {
				t.Fatalf("request_id %q was reused across two logins", gotRequestID)
			}
			if got, want := recorder.Result().Header.Get("Content-Type"), reference.Result().Header.Get("Content-Type"); got != want {
				t.Fatalf("got Content-Type %q, want %q", got, want)
			}
			if len(recorder.Result().Cookies()) != 0 {
				t.Fatalf("a refused login set a cookie: %v", recorder.Result().Cookies())
			}
			if len(users.sessions) != 0 {
				t.Fatalf("a refused login created a session: %#v", users.sessions)
			}
		})
	}

	// Positive control: the same credentials succeed once password login is on.
	// Without it the refusals above could come from a broken fixture.
	recorder, users := attempt(passwordUser, `{"username":"oidc-user","password":"password"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("enabled account login returned %d: %s", recorder.Code, recorder.Body.String())
	}
	if len(users.sessions) != 1 {
		t.Fatalf("enabled account login created %d sessions, want 1", len(users.sessions))
	}

	// An account that is both disabled and without password login still reports
	// account_disabled: the flag must not mask the state an administrator set.
	disabled := &model.User{ID: 9, Username: "oidc-user", Role: model.RoleUser, PasswordHash: passwordHash, Active: false}
	recorder, _ = attempt(disabled, `{"username":"oidc-user","password":"password"}`)
	if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "account_disabled") {
		t.Fatalf("disabled account returned %d: %s", recorder.Code, recorder.Body.String())
	}
}

// normalizeResponseBody replaces the per-request request id with a placeholder
// so two error responses can be compared byte for byte.
func normalizeResponseBody(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	requestID := recorder.Result().Header.Get("X-Gateway-Request-ID")
	if requestID == "" {
		t.Fatal("response carries no request id header")
	}
	body := recorder.Body.String()
	if !strings.Contains(body, requestID) {
		t.Fatalf("response body does not carry its request id %q: %s", requestID, body)
	}
	return strings.Replace(body, requestID, "<request-id>", 1)
}

// The production shape of an OIDC-provisioned account is now a well-formed
// Argon2id hash with password_login_enabled=false. Submitting any password for
// it must produce exactly the wrong-password answer — status, error
// type/code/message, Content-Type, no Set-Cookie, no session — so that the
// password_login_enabled gate, and not a hash-format rejection, is what refuses
// the login. An inactive account still reports account_disabled first.
func TestPasswordLoginDisabledAccountWithValidHashMatchesWrongPasswordByteForByte(t *testing.T) {
	passwordHash, err := gatewaycrypto.HashPassword("random-oidc-placeholder-value")
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}
	attempt := func(user *model.User, password string) (*httptest.ResponseRecorder, *fakeAuthStore) {
		t.Helper()
		users := newFakeAuthStore(user)
		recorder := httptest.NewRecorder()
		body := `{"username":"` + user.Username + `","password":"` + password + `"}`
		NewHandlerWithAuth(nil, users, AuthConfig{}).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body)))
		return recorder, users
	}

	// The reference answer: an ordinary wrong password on an account that may
	// sign in with one.
	reference, _ := attempt(&model.User{ID: 9, Username: "oidc-user", Role: model.RoleUser, PasswordHash: passwordHash, Active: true, PasswordLoginEnabled: true}, "wrong")
	if reference.Code != http.StatusUnauthorized {
		t.Fatalf("wrong-password reference returned %d: %s", reference.Code, reference.Body.String())
	}
	referenceBody := normalizeResponseBody(t, reference)
	for _, want := range []string{`"type":"authentication_error"`, `"code":"invalid_credentials"`, `"message":"invalid credentials"`} {
		if !strings.Contains(referenceBody, want) {
			t.Fatalf("reference body is missing %s: %s", want, referenceBody)
		}
	}

	disabledLogin := &model.User{ID: 9, Username: "oidc-user", Role: model.RoleUser, PasswordHash: passwordHash, Active: true, PasswordLoginEnabled: false}
	for _, password := range []string{"random-oidc-placeholder-value", "wrong", "yet another attempt"} {
		recorder, users := attempt(disabledLogin, password)
		if recorder.Code != reference.Code {
			t.Fatalf("password %q returned status %d, want the wrong-password status %d: %s", password, recorder.Code, reference.Code, recorder.Body.String())
		}
		if got := normalizeResponseBody(t, recorder); got != referenceBody {
			t.Fatalf("password %q answered differently from a wrong password:\n got %s\nwant %s", password, got, referenceBody)
		}
		if got, want := recorder.Result().Header.Get("Content-Type"), reference.Result().Header.Get("Content-Type"); got != want {
			t.Fatalf("Content-Type = %q, want %q", got, want)
		}
		if len(recorder.Result().Cookies()) != 0 {
			t.Fatalf("a refused login set a cookie: %v", recorder.Result().Cookies())
		}
		if len(users.sessions) != 0 {
			t.Fatalf("a refused login created a session: %#v", users.sessions)
		}
	}

	// account_disabled outranks password_login_enabled.
	inactive := &model.User{ID: 9, Username: "oidc-user", Role: model.RoleUser, PasswordHash: passwordHash, Active: false, PasswordLoginEnabled: false}
	recorder, _ := attempt(inactive, "random-oidc-placeholder-value")
	if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), `"code":"account_disabled"`) {
		t.Fatalf("inactive account with password login disabled returned %d: %s", recorder.Code, recorder.Body.String())
	}
}

// Rows written by the first OIDC implementation held a 64-character hex token,
// which VerifyPassword rejects before running Argon2id. Such a login must still
// pay for one dummy verification, so it costs what a wrong password costs and
// cannot be told apart by response time. The counting seam proves the fallback
// verification actually runs, and with the well-formed dummy encoding rather
// than the rejected stored value; the response is compared byte for byte with a
// wrong-password response.
func TestUnparseableStoredHashStillPaysForAPasswordCheck(t *testing.T) {
	wellFormed, err := gatewaycrypto.HashPassword("a-real-password")
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}
	legacyHash := strings.Repeat("ab", 32) // pre-fix OIDC placeholder shape

	var verified []string
	original := verifyPasswordHash
	verifyPasswordHash = func(password, encoded string) (bool, error) {
		verified = append(verified, encoded)
		return original(password, encoded)
	}
	defer func() { verifyPasswordHash = original }()

	attempt := func(user *model.User, body string) (*httptest.ResponseRecorder, *fakeAuthStore) {
		t.Helper()
		users := newFakeAuthStore(user)
		recorder := httptest.NewRecorder()
		NewHandlerWithAuth(nil, users, AuthConfig{}).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body)))
		return recorder, users
	}

	legacy := &model.User{ID: 9, Username: "legacy-oidc", Role: model.RoleUser, PasswordHash: legacyHash, Active: true, PasswordLoginEnabled: true}
	legacyRecorder, legacyUsers := attempt(legacy, `{"username":"legacy-oidc","password":"anything"}`)
	if legacyRecorder.Code != http.StatusUnauthorized || !strings.Contains(legacyRecorder.Body.String(), `"code":"invalid_credentials"`) {
		t.Fatalf("malformed-hash account returned %d: %s", legacyRecorder.Code, legacyRecorder.Body.String())
	}
	if len(verified) != 2 || verified[0] != legacyHash || verified[1] != dummyPasswordHash() {
		t.Fatalf("verifications = %q, want the rejected stored hash followed by the dummy hash", verified)
	}
	if dummy := dummyPasswordHash(); dummy == "" {
		t.Fatal("the dummy hash is empty, so the fallback verification did no work")
	} else if matched, err := original("anything", dummy); err != nil || matched {
		t.Fatalf("the dummy hash is not a usable non-matching Argon2id encoding: matched=%v err=%v", matched, err)
	}
	if len(legacyUsers.sessions) != 0 {
		t.Fatalf("a refused login created a session: %#v", legacyUsers.sessions)
	}

	// Reference: a wrong password against a well-formed hash runs exactly one
	// verification (its own) and gets the same response bytes.
	verified = nil
	reference, _ := attempt(&model.User{ID: 9, Username: "legacy-oidc", Role: model.RoleUser, PasswordHash: wellFormed, Active: true, PasswordLoginEnabled: true}, `{"username":"legacy-oidc","password":"wrong"}`)
	if reference.Code != legacyRecorder.Code {
		t.Fatalf("wrong password returned %d, malformed hash returned %d", reference.Code, legacyRecorder.Code)
	}
	if len(verified) != 1 || verified[0] != wellFormed {
		t.Fatalf("wrong-password verifications = %q, want exactly the stored hash", verified)
	}
	if got, want := normalizeResponseBody(t, legacyRecorder), normalizeResponseBody(t, reference); got != want {
		t.Fatalf("malformed-hash answer differs from a wrong-password answer:\n got %s\nwant %s", got, want)
	}

	// The unknown-username path keeps its own single dummy verification, so the
	// three refusals do the same amount of cryptographic work.
	verified = nil
	unknown, _ := attempt(&model.User{ID: 9, Username: "legacy-oidc", Role: model.RoleUser, PasswordHash: wellFormed, Active: true, PasswordLoginEnabled: true}, `{"username":"nobody","password":"anything"}`)
	if unknown.Code != http.StatusUnauthorized {
		t.Fatalf("unknown account returned %d: %s", unknown.Code, unknown.Body.String())
	}
	if len(verified) != 1 || verified[0] != dummyPasswordHash() {
		t.Fatalf("unknown-username verifications = %q, want one dummy verification", verified)
	}
}
