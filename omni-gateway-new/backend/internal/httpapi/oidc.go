package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/zjvtit/omni-gateway/internal/model"
	"golang.org/x/oauth2"
)

const (
	oidcStateCookieName = "llm_gateway_oidc_state"
	oidcStateTTL        = 5 * time.Minute
	oidcMaxTransactions = 4096
)

// oidcClaims is the minimal identity surface this gateway consumes. Email is
// only ever a display-name candidate: identity is bound on issuer+subject.
type oidcClaims struct {
	Subject           string `json:"sub"`
	Nonce             string `json:"nonce"`
	Name              string `json:"name"`
	PreferredUsername string `json:"preferred_username"`
	Email             string `json:"email"`
}

type oidcTransaction struct {
	verifier string
	nonce    string
	expires  time.Time
}

// oidcUserStore is optional so the password-only store fakes used by the
// existing tests keep compiling unchanged.
type oidcUserStore interface {
	GetOrCreateOIDCUser(context.Context, string, string, *model.User) (*model.User, error)
}

type oidcHandler struct {
	store        AuthStore
	sessionTTL   time.Duration
	cookieSecure bool
	client       *http.Client

	// source is the live, cache-backed read of the administrator configured
	// provider. It is consulted per request, so a console change takes effect
	// as soon as the admin endpoints invalidate it; no restart is involved.
	source *oidcProviderSource

	// providerMu guards the discovery cache. The cached provider is keyed by
	// issuer: a successful discovery is reused only while the issuer is
	// unchanged, and a new issuer always triggers a fresh discovery.
	providerMu     sync.Mutex
	providerIssuer string
	provider       *oidc.Provider

	txMu sync.Mutex
	tx   map[string]oidcTransaction
}

func newOIDCHandler(authStore AuthStore, cfg AuthConfig) *oidcHandler {
	client := cfg.OIDCClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &oidcHandler{
		store:        authStore,
		sessionTTL:   cfg.SessionTTL,
		cookieSecure: cfg.CookieSecure,
		client:       client,
		source:       cfg.OIDCProvider,
		tx:           make(map[string]oidcTransaction),
	}
}

// providerConfig reads the current provider through the cache-backed source.
// A handler built without a source - the shape of the password-only wiring -
// reports "not configured" instead of failing.
func (h *oidcHandler) providerConfig(ctx context.Context) (oidcProviderConfig, error) {
	if h == nil || h.source == nil {
		return oidcProviderConfig{}, nil
	}
	return h.source.Provider(ctx)
}

func (h *oidcHandler) config(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed", "method not allowed")
		return
	}
	// The provider is read per request, so a console save or clear is visible
	// immediately; a read failure fails closed to disabled rather than
	// advertising a provider the gateway cannot actually use. Only the feature
	// flag and a cosmetic label cross the boundary; the client secret and the
	// endpoint metadata stay server-side.
	providerConfig, err := h.providerConfig(r.Context())
	ready := err == nil && providerConfig.ready()
	body := map[string]any{"enabled": ready}
	if ready && strings.TrimSpace(providerConfig.DisplayName) != "" {
		body["display_name"] = strings.TrimSpace(providerConfig.DisplayName)
	}
	writeJSON(w, http.StatusOK, body)
}

func (h *oidcHandler) start(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed", "method not allowed")
		return
	}
	providerConfig, err := h.providerConfig(r.Context())
	if err != nil {
		h.redirectError(w, "provider_unavailable")
		return
	}
	if !providerConfig.ready() {
		h.redirectError(w, "oidc_not_configured")
		return
	}
	provider, err := h.providerFor(r.Context(), strings.TrimSpace(providerConfig.Issuer))
	if err != nil {
		h.redirectError(w, "provider_unavailable")
		return
	}
	state, err := randomToken(32)
	if err != nil {
		h.redirectError(w, "provider_unavailable")
		return
	}
	nonce, err := randomToken(32)
	if err != nil {
		h.redirectError(w, "provider_unavailable")
		return
	}
	verifier := oauth2.GenerateVerifier()
	h.saveTransaction(state, oidcTransaction{
		verifier: verifier,
		nonce:    nonce,
		expires:  time.Now().UTC().Add(oidcStateTTL),
	})
	http.SetCookie(w, &http.Cookie{
		Name:     oidcStateCookieName,
		Value:    state,
		Path:     "/",
		MaxAge:   int(oidcStateTTL.Seconds()),
		HttpOnly: true,
		Secure:   h.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
	location := h.oauthConfig(providerConfig, provider).AuthCodeURL(state,
		oauth2.S256ChallengeOption(verifier),
		oauth2.SetAuthURLParam("nonce", nonce),
	)
	http.Redirect(w, r, location, http.StatusFound)
}

func (h *oidcHandler) callback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed", "method not allowed")
		return
	}
	// The provider configuration is server-side state, so reading it first
	// cannot be influenced by the callback. An unconfigured gateway refuses
	// before any provider data is believed, and a source failure is never
	// mistaken for a configured provider.
	providerConfig, err := h.providerConfig(r.Context())
	if err != nil {
		h.redirectError(w, "provider_unavailable")
		return
	}
	if !providerConfig.ready() {
		h.redirectError(w, "oidc_not_configured")
		return
	}
	issuer := strings.TrimSpace(providerConfig.Issuer)
	// The state check runs before anything the provider sent is believed, so a
	// forged callback can never consume a pending transaction or mint a session.
	state := r.URL.Query().Get("state")
	if state == "" {
		h.redirectError(w, "invalid_state")
		return
	}
	cookie, err := r.Cookie(oidcStateCookieName)
	if err != nil || cookie.Value == "" || !sameToken(cookie.Value, state) {
		h.redirectError(w, "invalid_state")
		return
	}
	transaction, ok := h.takeTransaction(state)
	if !ok {
		h.redirectError(w, "state_expired")
		return
	}
	h.clearStateCookie(w)
	if providerError := r.URL.Query().Get("error"); providerError != "" {
		if providerError == "access_denied" {
			h.redirectError(w, "access_denied")
			return
		}
		h.redirectError(w, "provider_unavailable")
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		h.redirectError(w, "missing_code")
		return
	}
	provider, err := h.providerFor(r.Context(), issuer)
	if err != nil {
		h.redirectError(w, "provider_unavailable")
		return
	}
	ctx := h.oidcContext(r.Context())
	token, err := h.oauthConfig(providerConfig, provider).Exchange(ctx, code, oauth2.VerifierOption(transaction.verifier))
	if err != nil {
		h.redirectError(w, "token_exchange_failed")
		return
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		h.redirectError(w, "userinfo_failed")
		return
	}
	idToken, err := provider.Verifier(&oidc.Config{ClientID: strings.TrimSpace(providerConfig.ClientID)}).Verify(ctx, rawIDToken)
	if err != nil {
		h.redirectError(w, "userinfo_failed")
		return
	}
	var claims oidcClaims
	if err := idToken.Claims(&claims); err != nil {
		h.redirectError(w, "userinfo_failed")
		return
	}
	if claims.Subject == "" || claims.Nonce == "" || !sameToken(claims.Nonce, transaction.nonce) {
		h.redirectError(w, "invalid_state")
		return
	}
	users, ok := h.store.(oidcUserStore)
	if !ok {
		h.redirectError(w, "provider_unavailable")
		return
	}
	// The store generates the placeholder credential for a new account itself:
	// a caller-supplied value must not be able to store a malformed hash.
	localUser, err := users.GetOrCreateOIDCUser(ctx, issuer, claims.Subject, &model.User{
		Username:    oidcLocalUsername(issuer, claims),
		DisplayName: firstNonEmpty(claims.Name, claims.PreferredUsername, claims.Email),
		Role:        model.RoleUser,
		Active:      true,
	})
	if err != nil {
		h.redirectError(w, "userinfo_failed")
		return
	}
	if localUser == nil || !localUser.Active {
		h.redirectError(w, "account_disabled")
		return
	}
	if err := h.issueSession(ctx, localUser.ID, w); err != nil {
		h.redirectError(w, "provider_unavailable")
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

// providerFor discovers the provider metadata through oidc.NewProvider. Only a
// success is cached, and the entry is keyed by issuer: a momentarily
// unreachable provider is retried instead of being latched off, and an issuer
// changed in the console forces a fresh discovery rather than reusing the
// previous issuer's metadata.
func (h *oidcHandler) providerFor(ctx context.Context, issuer string) (*oidc.Provider, error) {
	h.providerMu.Lock()
	defer h.providerMu.Unlock()
	if h.provider != nil && h.providerIssuer == issuer {
		return h.provider, nil
	}
	provider, err := oidc.NewProvider(h.oidcContext(ctx), issuer)
	if err != nil {
		return nil, err
	}
	h.provider = provider
	h.providerIssuer = issuer
	return provider, nil
}

func (h *oidcHandler) oidcContext(ctx context.Context) context.Context {
	if h.client != nil {
		return oidc.ClientContext(ctx, h.client)
	}
	return ctx
}

func (h *oidcHandler) oauthConfig(providerConfig oidcProviderConfig, provider *oidc.Provider) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     strings.TrimSpace(providerConfig.ClientID),
		ClientSecret: providerConfig.ClientSecret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  strings.TrimSpace(providerConfig.RedirectURL),
		Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
	}
}

func (h *oidcHandler) saveTransaction(state string, transaction oidcTransaction) {
	now := time.Now().UTC()
	h.txMu.Lock()
	defer h.txMu.Unlock()
	for key, item := range h.tx {
		if !item.expires.After(now) {
			delete(h.tx, key)
		}
	}
	// Bounded store: an unauthenticated caller must not be able to grow it
	// without limit by hitting /start repeatedly.
	if len(h.tx) >= oidcMaxTransactions {
		for key := range h.tx {
			delete(h.tx, key)
			if len(h.tx) < oidcMaxTransactions {
				break
			}
		}
	}
	h.tx[state] = transaction
}

// takeTransaction consumes a state exactly once; a replay finds nothing.
func (h *oidcHandler) takeTransaction(state string) (oidcTransaction, bool) {
	h.txMu.Lock()
	defer h.txMu.Unlock()
	transaction, ok := h.tx[state]
	if ok {
		delete(h.tx, state)
	}
	if !ok || !transaction.expires.After(time.Now().UTC()) {
		return oidcTransaction{}, false
	}
	return transaction, true
}

func (h *oidcHandler) clearStateCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     oidcStateCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   h.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

// redirectError sends the browser back to the console with a fixed, whitelisted
// code. Provider errors, codes, tokens and the state value are never echoed.
func (h *oidcHandler) redirectError(w http.ResponseWriter, code string) {
	values := url.Values{}
	values.Set("oauth_error", code)
	w.Header().Set("Location", "/?"+values.Encode())
	w.WriteHeader(http.StatusFound)
}

// issueSession mirrors the password path: same session table, same CSRF token,
// same cookie attributes, so every downstream authorization rule is unchanged.
func (h *oidcHandler) issueSession(ctx context.Context, userID int64, w http.ResponseWriter) error {
	sessionID, err := randomToken(32)
	if err != nil {
		return err
	}
	csrfToken, err := randomToken(32)
	if err != nil {
		return err
	}
	ttl := h.sessionTTL
	if ttl <= 0 {
		ttl = defaultSessionTTL
	}
	expires := time.Now().UTC().Add(ttl)
	if err := h.store.CreateSession(ctx, sessionID, userID, csrfToken, expires); err != nil {
		return err
	}
	maxAge := int(time.Until(expires).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sessionID,
		Path:     "/",
		Expires:  expires,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   h.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

// oidcLocalUsername chooses the local account name a first-time OIDC login gets.
// A usable preferred_username is used verbatim, because that is the identifier
// the campus IdP already publishes (a student/staff number) and the console
// renders the local username rather than the display name. Identity is still
// bound on issuer+subject through oidc_identities: the username never links
// accounts, and the store appends a deterministic suffix when the requested
// name is already held by an unrelated account.
func oidcLocalUsername(issuer string, claims oidcClaims) string {
	if name := normalizeOIDCUsername(claims.PreferredUsername); name != "" {
		return name
	}
	return oidcUsername(issuer, claims.Subject)
}

// normalizeOIDCUsername accepts a preferred_username only when it is safe to
// store and display as an account name: 1-32 characters of ASCII letters,
// digits, dot, underscore or hyphen, not starting with punctuation. Anything
// else falls back to the derived name instead of being repaired, so a hostile
// claim cannot be reshaped into something that reads like another account.
func normalizeOIDCUsername(value string) string {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) == 0 || len(trimmed) > 32 {
		return ""
	}
	for i := 0; i < len(trimmed); i++ {
		ch := trimmed[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9':
		case ch == '.', ch == '_', ch == '-':
			if i == 0 {
				return ""
			}
		default:
			return ""
		}
	}
	return trimmed
}

// oidcUsername derives a stable, collision-resistant local username from the
// identity pair. A username is never used to link accounts.
func oidcUsername(issuer, subject string) string {
	digest := sha256Hex(issuer + "\x00" + subject)
	return "oidc-" + digest[:24]
}

// sha256Hex returns the lowercase hex form of the SHA-256 digest of value.
func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return "OIDC 用户"
}
