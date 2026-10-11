package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	gatewaycrypto "github.com/zjvtit/omni-gateway/internal/crypto"
	"github.com/zjvtit/omni-gateway/internal/model"
	"github.com/zjvtit/omni-gateway/internal/store"
)

// APIKeyStore is the persistence surface needed to authenticate gateway
// requests. It is intentionally separate from the management AuthStore so the
// middleware can be reused by the future /v1 gateway handlers.
type APIKeyStore interface {
	GetAPIKeyByHash(context.Context, string) (*model.APIKey, error)
	GetUserByID(context.Context, int64) (*model.User, error)
	TouchAPIKey(context.Context, int64) error
}

// Principal is the authenticated identity attached to a gateway request.
type Principal struct {
	User        *model.User
	APIKey      *model.APIKey
	ModelAccess *model.ModelAccess
}

type principalContextKey struct{}

// APIKeyMiddleware authenticates an Authorization: Bearer gateway key and
// attaches its user/key principal to the request context. Model allow-list
// checks happen after the request body has been decoded and can use
// ModelAllowed with PrincipalFromContext.
func APIKeyMiddleware(authStore APIKeyStore, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authStore == nil {
			writeError(w, http.StatusServiceUnavailable, "server_error", "authentication_unavailable", "gateway authentication unavailable")
			return
		}
		header := r.Header.Get("Authorization")
		token, ok := ParseBearerToken(header)
		// Native Anthropic clients send x-api-key. A supplied Authorization
		// header always takes precedence, including when it is malformed.
		if _, present := r.Header["Authorization"]; !present && r.URL.Path == "/v1/messages" {
			token = strings.TrimSpace(r.Header.Get("X-Api-Key"))
			ok = token != ""
		}
		if !ok {
			writeError(w, http.StatusUnauthorized, "authentication_error", "api_key_required", "gateway API key required")
			return
		}
		key, err := authStore.GetAPIKeyByHash(r.Context(), gatewaycrypto.HashAPIKey(token))
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusUnauthorized, "authentication_error", "invalid_api_key", "invalid gateway API key")
				return
			}
			writeError(w, http.StatusInternalServerError, "server_error", "authentication_unavailable", "gateway authentication unavailable")
			return
		}
		if key == nil || !key.Active || (key.ExpiresAt != nil && !key.ExpiresAt.After(time.Now().UTC())) {
			writeError(w, http.StatusUnauthorized, "authentication_error", "invalid_api_key", "invalid gateway API key")
			return
		}
		user, err := authStore.GetUserByID(r.Context(), key.UserID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusUnauthorized, "authentication_error", "invalid_api_key", "invalid gateway API key")
				return
			}
			writeError(w, http.StatusInternalServerError, "server_error", "authentication_unavailable", "gateway authentication unavailable")
			return
		}
		if user == nil || !user.Active {
			writeError(w, http.StatusUnauthorized, "authentication_error", "invalid_api_key", "invalid gateway API key")
			return
		}

		// Last-used tracking is observability data; a transient write failure
		// must not turn an already authenticated request into a 500.
		_ = authStore.TouchAPIKey(r.Context(), key.ID)
		principal := &Principal{User: user, APIKey: key}
		ctx := context.WithValue(r.Context(), principalContextKey{}, principal)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ParseBearerToken parses the strict Bearer form used by the gateway API.
func ParseBearerToken(header string) (string, bool) {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

// PrincipalFromContext returns the gateway identity attached by
// APIKeyMiddleware.
func PrincipalFromContext(ctx context.Context) (*Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(*Principal)
	return principal, ok && principal != nil
}

// ModelAllowed reports whether a gateway API key may call the supplied model
// alias. An empty allow-list means all model aliases are allowed.
func ModelAllowed(key *model.APIKey, modelAlias string) bool {
	if key == nil {
		return false
	}
	if len(key.ModelAllow) == 0 {
		return true
	}
	for _, allowed := range key.ModelAllow {
		if allowed == modelAlias {
			return true
		}
	}
	return false
}

// PrincipalModelAllowed uses only the key allow-list. Historical group grants
// remain stored but are no longer a runtime authorization source.
func PrincipalModelAllowed(principal *Principal, modelAlias string) bool {
	return principal != nil && ModelAllowed(principal.APIKey, modelAlias)
}
