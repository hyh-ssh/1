package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	gatewaycrypto "github.com/zjvtit/omni-gateway/internal/crypto"
	"github.com/zjvtit/omni-gateway/internal/model"
	"github.com/zjvtit/omni-gateway/internal/store"
)

const (
	sessionCookieName = "llm_gateway_session"
	defaultSessionTTL = 24 * time.Hour
	maxAuthBodyBytes  = 1 << 20
)

// dummyPasswordHash is a well-formed Argon2id hash of a value no caller can
// supply. When the submitted username does not exist there is nothing to verify
// against, so the submitted password is verified against this hash instead: the
// answer then costs what a real account's answer costs, and its timing no
// longer tells an anonymous caller whether a username exists. It is computed
// once, lazily, because a hard-coded string would silently stop matching if the
// hash parameters ever changed, and VerifyPassword rejects a mismatched format
// immediately — which would restore the very timing gap this closes.
var dummyPasswordHash = sync.OnceValue(func() string {
	hash, err := gatewaycrypto.HashPassword("omni-gateway-timing-equalizer")
	if err != nil {
		// Without entropy there is nothing to compare against. The login then
		// answers as fast as it did before this was added: degraded, not wrong.
		return ""
	}
	return hash
})

// verifyPasswordHash is the primitive the login handler verifies submitted
// passwords with. It is a variable so tests can observe that every refused
// login performs exactly one verification; production always points it at
// gatewaycrypto.VerifyPassword.
var verifyPasswordHash = gatewaycrypto.VerifyPassword

// verifyAgainstDummyHash spends the same work as verifying a real password.
func verifyAgainstDummyHash(password string) {
	_, _ = verifyPasswordHash(password, dummyPasswordHash())
}

// AuthStore is the persistence surface needed by the management session API.
type AuthStore interface {
	GetUserByUsername(context.Context, string) (*model.User, error)
	GetUserByID(context.Context, int64) (*model.User, error)
	CreateSession(context.Context, string, int64, string, time.Time) error
	GetSession(context.Context, string) (userID int64, csrf string, expires time.Time, err error)
	DeleteSession(context.Context, string) error
}

// AuthConfig controls administrator session cookies.
type AuthConfig struct {
	SessionTTL     time.Duration
	CookieSecure   bool
	UpstreamClient *http.Client
	RuntimeStats   func() any
	// OIDCClient overrides the HTTP client used for OIDC discovery, token
	// exchange and JWKS fetches. Tests use it to reach a fake identity
	// provider; production leaves it nil.
	OIDCClient *http.Client

	// OIDCProvider is the live, cache-backed read of the administrator
	// configured primary OIDC provider. It is the only source the runtime
	// consumes: GATEWAY_OIDC_* values are seeded into the database once at
	// startup and never read again. The administrator endpoints invalidate it
	// after every successful write so console changes apply without a restart.
	OIDCProvider *oidcProviderSource
}

type authHandler struct {
	store        AuthStore
	sessionTTL   time.Duration
	cookieSecure bool
	limiter      loginLimiter
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type authUser struct {
	ID                   int64                `json:"id"`
	Username             string               `json:"username"`
	DisplayName          string               `json:"display_name"`
	Role                 model.Role           `json:"role"`
	Active               bool                 `json:"active"`
	PasswordLoginEnabled bool                 `json:"password_login_enabled"`
	PasswordCapabilities passwordCapabilities `json:"password_capabilities"`
}

// NewHandlerWithAuth creates the base HTTP handler and enables management
// session endpoints when a store is supplied.
func NewHandlerWithAuth(db Pinger, authStore AuthStore, cfg AuthConfig) http.Handler {
	return NewHandlerWithGateway(db, authStore, nil, nil, cfg)
}

// NewHandlerWithGateway creates the complete base handler and optionally
// mounts the API-key-protected public gateway routes.
func NewHandlerWithGateway(db Pinger, authStore AuthStore, apiKeyStore APIKeyStore, service ChatService, cfg AuthConfig) http.Handler {
	return NewHandlerWithGatewayAndCipher(db, authStore, apiKeyStore, service, cfg, nil)
}

// NewHandlerWithGatewayAndCipher is the fully wired constructor used by the
// server when administrator provider credentials need encryption at rest.
func NewHandlerWithGatewayAndCipher(db Pinger, authStore AuthStore, apiKeyStore APIKeyStore, service ChatService, cfg AuthConfig, cipher *gatewaycrypto.Cipher) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthz)
	mux.HandleFunc("/readyz", readyz(db))
	if authStore != nil {
		auth := newAuthHandler(authStore, cfg)
		mux.HandleFunc("/api/auth/login", auth.login)
		mux.HandleFunc("/api/auth/me", auth.me)
		mux.HandleFunc("/api/auth/logout", auth.logout)
		oidc := newOIDCHandler(authStore, cfg)
		mux.HandleFunc("/api/auth/oidc/config", oidc.config)
		mux.HandleFunc("/api/auth/oidc/start", oidc.start)
		mux.HandleFunc("/api/auth/oidc/callback", oidc.callback)
		// Every signed-in account gets its own read-only view; a non-admin
		// session has nothing else to talk to, since /api/admin stays
		// administrator-only.
		self := newSelfHandler(authStore, cfg)
		mux.Handle("/api/self", self)
		mux.Handle("/api/self/", self)
	}
	if authStore != nil {
		admin := newAdminHandler(authStore, cfg, cipher)
		mux.Handle("/api/admin/", admin)
		RegisterGroupRoutes(mux, authStore, cfg)
	}
	if service != nil {
		if apiKeyStore == nil && authStore != nil {
			if candidate, ok := authStore.(APIKeyStore); ok {
				apiKeyStore = candidate
			}
		}
		responses, _ := service.(ResponsesService)
		public := &gatewayHandler{service: service, responsesService: responses}
		protected := func(handler http.HandlerFunc) http.Handler {
			return APIKeyMiddleware(apiKeyStore, http.HandlerFunc(handler))
		}
		mux.Handle("/v1/models", protected(public.models))
		mux.Handle("/v1/chat/completions", protected(public.chat))
		mux.Handle("/v1/messages", protected(public.messages))
		mux.Handle("/v1/embeddings", protected(public.embeddings))
		mux.Handle("/v1/images/generations", protected(public.images))
		if responses != nil {
			mux.Handle("/v1/responses", protected(public.responses))
		}
	}
	mux.HandleFunc("/", index)
	return requestIDMiddleware(mux)
}

func newAuthHandler(authStore AuthStore, cfg AuthConfig) *authHandler {
	ttl := cfg.SessionTTL
	if ttl <= 0 {
		ttl = defaultSessionTTL
	}
	return &authHandler{store: authStore, sessionTTL: ttl, cookieSecure: cfg.CookieSecure}
}

func (h *authHandler) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed", "method not allowed")
		return
	}
	var input loginRequest
	finish, wait := h.limiter.begin(peerIP(r), time.Now())
	if finish == nil {
		writeLoginLimited(w, wait)
		return
	}
	success := false
	defer func() { finish(success); h.auditLogin(r, strings.TrimSpace(input.Username), success) }()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAuthBodyBytes))
	if err := decoder.Decode(&input); err != nil || strings.TrimSpace(input.Username) == "" || input.Password == "" {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "invalid login request")
		return
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "invalid login request")
		return
	}

	user, err := h.store.GetUserByUsername(r.Context(), strings.TrimSpace(input.Username))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			verifyAgainstDummyHash(input.Password)
			writeError(w, http.StatusUnauthorized, "authentication_error", "invalid_credentials", "invalid credentials")
			return
		}
		writeError(w, http.StatusInternalServerError, "server_error", "authentication_unavailable", "authentication unavailable")
		return
	}
	if user == nil {
		verifyAgainstDummyHash(input.Password)
		writeError(w, http.StatusUnauthorized, "authentication_error", "invalid_credentials", "invalid credentials")
		return
	}
	valid, err := verifyPasswordHash(input.Password, user.PasswordHash)
	if err != nil || !valid {
		// A stored hash that VerifyPassword cannot parse (an account an older
		// OIDC build provisioned with a random token) fails before Argon2id
		// runs. Verify the submitted password against the dummy hash anyway so
		// this refusal costs what a wrong password against a real hash costs,
		// then answer with the identical invalid_credentials body.
		if err != nil {
			verifyAgainstDummyHash(input.Password)
		}
		writeError(w, http.StatusUnauthorized, "authentication_error", "invalid_credentials", "invalid credentials")
		return
	}
	// Any active account may open a console session: an administrator gets the
	// management console and every other account only its own view (/api/self).
	// The account state is reported only after the password has been verified,
	// so an unknown username and a wrong password both answer "invalid
	// credentials", and a disabled account is only named to whoever proved the
	// password. An unknown username still pays for a password check (against
	// dummyPasswordHash), so the two cases also cost the same time.
	if !user.Active {
		writeError(w, http.StatusForbidden, "authentication_error", "account_disabled", "account is disabled")
		return
	}
	// An account that OIDC provisioned has no usable password until an
	// administrator sets one. The answer is the exact one a wrong password
	// gets: the caller must not learn whether the account exists, and must not
	// be told which sign-in method it uses.
	if !user.PasswordLoginEnabled {
		writeError(w, http.StatusUnauthorized, "authentication_error", "invalid_credentials", "invalid credentials")
		return
	}

	sessionID, err := randomToken(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_error", "session_creation_failed", "could not create session")
		return
	}
	csrfToken, err := randomToken(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_error", "session_creation_failed", "could not create session")
		return
	}
	expires := time.Now().UTC().Add(h.sessionTTL)
	if err := h.store.CreateSession(r.Context(), sessionID, user.ID, csrfToken, expires); err != nil {
		writeError(w, http.StatusInternalServerError, "server_error", "session_creation_failed", "could not create session")
		return
	}
	h.setSessionCookie(w, sessionID, expires)
	success = true
	writeJSON(w, http.StatusOK, map[string]any{
		"user":       authUserWithCapabilities(r.Context(), h.store, user),
		"csrf_token": csrfToken,
	})
}

func (h *authHandler) me(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed", "method not allowed")
		return
	}
	user, csrf, ok := h.authenticate(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication_error", "authentication_required", "authentication required")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": authUserWithCapabilities(r.Context(), h.store, user), "csrf_token": csrf})
}

func (h *authHandler) logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed", "method not allowed")
		return
	}
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		h.clearSessionCookie(w)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	_, csrf, ok := h.authenticate(r)
	if !ok || !sameToken(r.Header.Get("X-CSRF-Token"), csrf) {
		writeError(w, http.StatusForbidden, "invalid_request_error", "csrf_failed", "invalid CSRF token")
		return
	}
	if err := h.store.DeleteSession(r.Context(), cookie.Value); err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "server_error", "session_deletion_failed", "could not end session")
		return
	}
	h.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// authenticate resolves the session cookie to an active account of any role.
// Callers that need the administrator role must check it themselves: /api/self
// serves ordinary accounts, while every management route requires admin.
func (h *authHandler) authenticate(r *http.Request) (*model.User, string, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return nil, "", false
	}
	userID, csrf, expires, err := h.store.GetSession(r.Context(), cookie.Value)
	if err != nil || expires.Before(time.Now().UTC()) {
		return nil, "", false
	}
	user, err := h.store.GetUserByID(r.Context(), userID)
	if user == nil || err != nil || !user.Active {
		return nil, "", false
	}
	return user, csrf, true
}

func (h *authHandler) setSessionCookie(w http.ResponseWriter, value string, expires time.Time) {
	maxAge := int(time.Until(expires).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		Path:     "/",
		Expires:  expires,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   h.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (h *authHandler) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   h.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

func toAuthUser(user *model.User) authUser {
	return authUser{ID: user.ID, Username: user.Username, DisplayName: user.DisplayName, Role: user.Role, Active: user.Active, PasswordLoginEnabled: user.PasswordLoginEnabled, PasswordCapabilities: passwordCapabilities{LocalEnabled: user.PasswordLoginEnabled}}
}

func sameToken(got, want string) bool {
	if got == "" || want == "" || len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func randomToken(size int) (string, error) {
	if size <= 0 {
		return "", fmt.Errorf("token size must be positive")
	}
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}
