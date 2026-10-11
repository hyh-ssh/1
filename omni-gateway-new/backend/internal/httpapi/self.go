package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	gatewaycrypto "github.com/zjvtit/omni-gateway/internal/crypto"
	"github.com/zjvtit/omni-gateway/internal/model"
)

// maxSelfRequestPage bounds the request history a single account can page
// through, matching the store's own clamp so a caller cannot ask for an
// unbounded scan.
const (
	defaultSelfRequestPage = 50
	maxSelfRequestPage     = 200
	// minSelfPasswordLength is the floor for a password an account sets for
	// itself. The administrator surface has no such rule (it also serves the
	// bootstrap user), but a self-service rotation is the right place to refuse
	// a one-character secret.
	minSelfPasswordLength = 8
	// maxSelfKeyNameRunes bounds a key label so one account cannot store an
	// unbounded string for the console to render back.
	maxSelfKeyNameRunes = 64
)

// selfKeyStore is the persistence surface an account needs to manage its own
// gateway credentials. It mirrors the administrator one minus the user id: on
// this surface the owner of a key is always the session account.
type selfKeyStore interface {
	GetAPIKeyByID(context.Context, int64) (*model.APIKey, error)
	ListAPIKeysByUser(context.Context, int64) ([]*model.APIKey, error)
	CreateAPIKey(context.Context, *model.APIKey) (int64, error)
	UpdateAPIKey(context.Context, *model.APIKey) error
	DeleteAPIKey(context.Context, int64) error
}

// selfUserStore is the account's own profile persistence.
type selfUserStore interface {
	SetUserPassword(context.Context, int64, string) error
}

// selfSessionStore revokes an account's other sessions, which a password change
// uses to sign the account out everywhere else.
type selfSessionStore interface {
	DeleteUserSessionsExcept(context.Context, int64, string) error
}

// selfHandler serves the signed-in account's own view. It exists because any
// active account may sign in: an administrator uses /api/admin, every other
// account would otherwise have no endpoint to call.
//
// Every query below is scoped by the session account's id and no request
// parameter can widen it, so a caller can never read or write another account's
// keys, budgets, limits or request history through this surface.
type selfHandler struct {
	auth  *authHandler
	store AuthStore
}

func newSelfHandler(authStore AuthStore, cfg AuthConfig) http.Handler {
	return &selfHandler{auth: newAuthHandler(authStore, cfg), store: authStore}
}

func (h *selfHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	user, csrf, ok := h.auth.authenticate(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication_error", "authentication_required", "authentication required")
		return
	}
	if !readOnly(r) {
		path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/self"), "/")
		detail := "self API mutation"
		if strings.HasPrefix(path, "api-keys") {
			switch r.Method {
			case "POST":
				detail = "self API key created"
			case "PATCH":
				detail = "self API key updated"
			case "DELETE":
				detail = "self API key deleted"
			}
		}
		if path == "password" {
			detail = "self password changed"
			path = "users/" + fmt.Sprint(user.ID)
		}
		var finish func()
		w, finish = beginChangeAudit(w, r, h.store, user, path, detail)
		defer finish()
	}
	// A session cookie is enough to read, but never enough to write: creating a
	// credential or changing the password also requires the session's CSRF token,
	// the same gate /api/admin uses, so a cross-site form cannot do either.
	if r.Method != http.MethodGet && r.Method != http.MethodHead && !sameToken(r.Header.Get("X-CSRF-Token"), csrf) {
		writeError(w, http.StatusForbidden, "invalid_request_error", "csrf_failed", "invalid CSRF token")
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/self"), "/"), "/")
	switch {
	case len(parts) == 1 && parts[0] == "summary":
		h.summary(w, r, user)
	case len(parts) == 1 && parts[0] == "api-keys":
		h.apiKeys(w, r, user, "")
	case len(parts) == 2 && parts[0] == "api-keys":
		h.apiKeys(w, r, user, parts[1])
	case len(parts) == 1 && parts[0] == "requests":
		h.requests(w, r, user)
	case len(parts) == 1 && parts[0] == "access":
		h.access(w, r, user)
	case len(parts) == 1 && parts[0] == "password":
		h.changePassword(w, r, user)
	default:
		notFound(w)
	}
}

// summary answers the account page in one round trip: the profile, its own
// usage aggregates, its budget rules and the rate limits that apply to it.
func (h *selfHandler) summary(w http.ResponseWriter, r *http.Request, user *model.User) {
	if !readOnly(r) {
		writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed", "method not allowed")
		return
	}
	queries, ok := h.store.(interface {
		Stats(context.Context, model.StatsFilter) (*model.StatsSummary, error)
		ListRateLimits(context.Context, string, int64) ([]*model.RateLimit, error)
		ListAPIKeysByUser(context.Context, int64) ([]*model.APIKey, error)
	})
	if !ok {
		writeAdminUnsupported(w)
		return
	}
	usage, err := queries.Stats(r.Context(), model.StatsFilter{UserID: user.ID})
	if err != nil {
		writeAdminStoreError(w, err)
		return
	}
	if usage == nil {
		usage = &model.StatsSummary{}
	}
	limits, err := queries.ListRateLimits(r.Context(), "user", user.ID)
	if err != nil {
		writeAdminStoreError(w, err)
		return
	}
	// Key-scoped rules belong to the account too, because the keys are its own.
	// They are listed after the account-wide rules so the console can show both
	// without a second request.
	keys, err := queries.ListAPIKeysByUser(r.Context(), user.ID)
	if err != nil {
		writeAdminStoreError(w, err)
		return
	}
	for _, key := range keys {
		if key == nil {
			continue
		}
		keyLimits, err := queries.ListRateLimits(r.Context(), "key", key.ID)
		if err != nil {
			writeAdminStoreError(w, err)
			return
		}
		limits = append(limits, keyLimits...)
	}
	if limits == nil {
		limits = []*model.RateLimit{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user":   authUserWithCapabilities(r.Context(), h.store, user),
		"usage":  toSelfUsage(usage),
		"limits": limits,
	})
}

// selfUsage is the subset of model.StatsSummary that belongs to the account
// itself: only the counters its console renders. The topology fields of the
// administrator report (`routes` maps an alias to a provider id and upstream
// model, `trend` and `error_types` describe operation) are deliberately not
// forwarded, because an ordinary account has no business seeing how the
// gateway routes internally.
type selfUsage struct {
	Requests         int64         `json:"requests"`
	Success          int64         `json:"success"`
	Errors           int64         `json:"errors"`
	SuccessRate      float64       `json:"success_rate"`
	InputTokens      int64         `json:"input_tokens"`
	OutputTokens     int64         `json:"output_tokens"`
	TotalTokens      int64         `json:"total_tokens"`
	Cost             model.Money   `json:"cost"`
	Credits          model.Credits `json:"credits"`
	AvgLatencyMS     float64       `json:"avg_latency_ms"`
	FailoverCount    int64         `json:"failover_count"`
	SettlementErrors int64         `json:"settlement_errors"`
}

func toSelfUsage(summary *model.StatsSummary) selfUsage {
	if summary == nil {
		return selfUsage{}
	}
	return selfUsage{
		Requests:         summary.Requests,
		Success:          summary.Success,
		Errors:           summary.Errors,
		SuccessRate:      summary.SuccessRate,
		InputTokens:      summary.InputTokens,
		OutputTokens:     summary.OutputTokens,
		TotalTokens:      summary.TotalTokens,
		Cost:             summary.Cost,
		Credits:          summary.Credits,
		AvgLatencyMS:     summary.AvgLatencyMS,
		FailoverCount:    summary.FailoverCount,
		SettlementErrors: summary.SettlementErrors,
	}
}

// The owner is always the authenticated session. Explicit ownership changes
// are rejected rather than silently returning a misleading successful edit.
type selfAPIKeyInput = apiKeyInput

// apiKeys serves the account's own gateway credentials. rawID is empty for the
// collection (GET lists, POST creates) and holds the key id otherwise.
func (h *selfHandler) apiKeys(w http.ResponseWriter, r *http.Request, user *model.User, rawID string) {
	keys, ok := h.store.(selfKeyStore)
	if !ok {
		writeAdminUnsupported(w)
		return
	}
	if rawID == "" {
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			items, err := keys.ListAPIKeysByUser(r.Context(), user.ID)
			if err != nil {
				writeAdminStoreError(w, err)
				return
			}
			if items == nil {
				items = []*model.APIKey{}
			}
			writeJSON(w, http.StatusOK, items)
		case http.MethodPost:
			h.createAPIKey(w, r, user, keys)
		default:
			writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed", "method not allowed")
		}
		return
	}
	id, err := parseAdminID(rawID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_id", "invalid API key id")
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		if key, ok := h.ownedAPIKey(w, r, keys, user, id); ok {
			writeJSON(w, 200, key)
		}
	case http.MethodPatch:
		h.updateAPIKey(w, r, user, keys, id)
	case http.MethodDelete:
		h.deleteAPIKey(w, r, user, keys, id)
	default:
		writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed", "method not allowed")
	}
}

// createAPIKey issues a credential for the session account. The plaintext
// secret is returned exactly once, here; only its hash is stored.
func (h *selfHandler) createAPIKey(w http.ResponseWriter, r *http.Request, user *model.User, keys selfKeyStore) {
	var input selfAPIKeyInput
	if err := decodeSelfJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "invalid API key request")
		return
	}
	if !keyOwnerInput(w, input, user.ID, true) {
		return
	}
	name, ok := selfAPIKeyName(w, input.Name)
	if !ok {
		return
	}
	if !futureExpiry(w, input.ExpiresAt) {
		return
	}
	secret, prefix, suffix, err := newGatewayAPIKey()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_error", "key_generation_failed", "could not generate API key")
		return
	}
	active := true
	if input.Active != nil {
		active = *input.Active
	}
	key := &model.APIKey{UserID: user.ID, Name: name, KeyPrefix: prefix, KeyHash: gatewaycrypto.HashAPIKey(secret), KeySuffix: suffix, Active: active, ExpiresAt: input.ExpiresAt, ModelAllow: input.ModelAllow}
	id, err := keys.CreateAPIKey(r.Context(), key)
	if err != nil {
		writeAdminStoreError(w, err)
		return
	}
	key.ID = id
	writeJSON(w, http.StatusCreated, map[string]any{"key": key, "secret": secret})
}

// updateAPIKey renames, re-scopes, re-expires or (de)activates an own key. The
// owner is never changed, so a key cannot be handed to another account.
func (h *selfHandler) updateAPIKey(w http.ResponseWriter, r *http.Request, user *model.User, keys selfKeyStore, id int64) {
	var input selfAPIKeyInput
	if err := decodeSelfJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "invalid API key request")
		return
	}
	key, ok := h.ownedAPIKey(w, r, keys, user, id)
	if !ok {
		return
	}
	copy := *key
	key = &copy
	if !applyOwnKeyInput(w, input, key) {
		return
	}
	if err := keys.UpdateAPIKey(r.Context(), key); err != nil {
		writeAdminStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, key)
}

// deleteAPIKey removes an own key. The row is loaded first so that deleting
// another account's id answers 404 instead of succeeding silently.
func (h *selfHandler) deleteAPIKey(w http.ResponseWriter, r *http.Request, user *model.User, keys selfKeyStore, id int64) {
	if _, ok := h.ownedAPIKey(w, r, keys, user, id); !ok {
		return
	}
	if err := keys.DeleteAPIKey(r.Context(), id); err != nil {
		writeAdminStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ownedAPIKey loads a key and refuses one that belongs to another account. A
// foreign id answers 404 rather than 403: this surface must not confirm that a
// guessed id exists.
func (h *selfHandler) ownedAPIKey(w http.ResponseWriter, r *http.Request, keys selfKeyStore, user *model.User, id int64) (*model.APIKey, bool) {
	key, err := keys.GetAPIKeyByID(r.Context(), id)
	if err != nil {
		writeAdminStoreError(w, err)
		return nil, false
	}
	if key == nil || key.UserID != user.ID {
		writeError(w, http.StatusNotFound, "invalid_request_error", "not_found", "resource not found")
		return nil, false
	}
	return key, true
}

// selfPasswordInput is the password rotation body.
type selfPasswordInput struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// changePassword rotates the account's own password. The current password is
// required even though the session is already authenticated, so that somebody
// walking up to an unlocked console cannot lock the owner out of the account.
func (h *selfHandler) changePassword(w http.ResponseWriter, r *http.Request, user *model.User) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed", "method not allowed")
		return
	}
	users, ok := h.store.(selfUserStore)
	if !ok {
		writeAdminUnsupported(w)
		return
	}
	var input selfPasswordInput
	if err := decodeSelfJSON(w, r, &input); err != nil || input.NewPassword == "" {
		writeError(w, 400, "invalid_request_error", "invalid_request", "new password is required")
		return
	}
	capabilities := authUserWithCapabilities(r.Context(), h.store, user).PasswordCapabilities
	if !capabilities.CanSetWithoutCurrent {
		if input.CurrentPassword == "" {
			writeError(w, 400, "invalid_request_error", "invalid_request", "current password is required")
			return
		}
		valid, err := gatewaycrypto.VerifyPassword(input.CurrentPassword, user.PasswordHash)
		if err != nil || !valid {
			writeError(w, 403, "authentication_error", "invalid_current_password", "current password is incorrect")
			return
		}
	}
	if len([]rune(input.NewPassword)) < minSelfPasswordLength {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "weak_password", fmt.Sprintf("new password must be at least %d characters", minSelfPasswordLength))
		return
	}
	if input.NewPassword == input.CurrentPassword {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "new password must differ from the current one")
		return
	}
	hash, err := gatewaycrypto.HashPassword(input.NewPassword)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_error", "password_hash_failed", "could not hash password")
		return
	}
	cookie, _ := r.Cookie(sessionCookieName)
	if atomic, ok := h.store.(interface {
		ChangeOwnPassword(context.Context, int64, string, string, string) error
	}); ok {
		if err := atomic.ChangeOwnPassword(r.Context(), user.ID, user.PasswordHash, hash, cookie.Value); err != nil {
			writeAdminStoreError(w, err)
			return
		}
	} else {
		sessions, ok := h.store.(selfSessionStore)
		if !ok {
			writeAdminUnsupported(w)
			return
		}
		if err := users.SetUserPassword(r.Context(), user.ID, hash); err != nil {
			writeAdminStoreError(w, err)
			return
		}
		if err := sessions.DeleteUserSessionsExcept(r.Context(), user.ID, cookie.Value); err != nil {
			writeError(w, 500, "server_error", "session_revocation_failed", "password changed but other sessions could not be signed out")
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// requests lists the account's own call history, newest first.
func (h *selfHandler) requests(w http.ResponseWriter, r *http.Request, user *model.User) {
	if !readOnly(r) {
		writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed", "method not allowed")
		return
	}
	queries, ok := h.store.(interface {
		ListRequests(context.Context, model.StatsFilter) ([]*model.Request, error)
	})
	if !ok {
		writeAdminUnsupported(w)
		return
	}
	// Only safe self filters are accepted; ownership is always from the session.
	query := r.URL.Query()
	for _, name := range []string{"user_id", "provider_id", "provider_model", "model_group_id", "model"} {
		query.Del(name)
	}
	copy := r.Clone(r.Context())
	u := *r.URL
	u.RawQuery = query.Encode()
	copy.URL = &u
	filter, err := requestFilterFromQuery(copy)
	if err != nil {
		writeError(w, 400, "invalid_request_error", "invalid_filter", err.Error())
		return
	}
	filter.UserID = user.ID
	if filter.APIKeyID != 0 {
		keys, ok := h.store.(selfKeyStore)
		if !ok {
			writeAdminUnsupported(w)
			return
		}
		if _, ok := h.ownedAPIKey(w, r, keys, user, filter.APIKeyID); !ok {
			return
		}
	}
	items, err := queries.ListRequests(r.Context(), filter)
	if err != nil {
		writeAdminStoreError(w, err)
		return
	}
	if items == nil {
		items = []*model.Request{}
	}
	writeJSON(w, http.StatusOK, safeOwnRequests(items))
}

// decodeSelfJSON reads exactly one bounded JSON value, like the administrator
// decoder: a two-value or oversized body is rejected outright.
func decodeSelfJSON(w http.ResponseWriter, r *http.Request, target any) error {
	return decodeAdminJSON(w, r, target)
}

// readOnly reports whether the request is a plain read.
func readOnly(r *http.Request) bool {
	return r.Method == http.MethodGet || r.Method == http.MethodHead
}

// selfAPIKeyName validates a key label and writes the answer itself when the
// label is unusable.
func selfAPIKeyName(w http.ResponseWriter, raw string) (string, bool) {
	name := strings.TrimSpace(raw)
	if name == "" {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "API key name is required")
		return "", false
	}
	if len([]rune(name)) > maxSelfKeyNameRunes {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", fmt.Sprintf("API key name must be at most %d characters", maxSelfKeyNameRunes))
		return "", false
	}
	return name, true
}

// futureExpiry rejects an expiry that has already passed, which would hand the
// account a credential that can never be used. A nil expiry means "no expiry".
func futureExpiry(w http.ResponseWriter, expires *time.Time) bool {
	if expires != nil && !expires.After(time.Now().UTC()) {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_expiry", "expires_at must be in the future")
		return false
	}
	return true
}
