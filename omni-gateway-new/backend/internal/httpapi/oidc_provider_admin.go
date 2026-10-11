package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/zjvtit/omni-gateway/internal/store"
)

// oidcProviderAdminStore is the slice of the management store the primary OIDC
// provider endpoints need. It is asserted per request, like the other optional
// administrator collections, so password-only stores keep working untouched.
type oidcProviderAdminStore interface {
	GetOIDCProvider(context.Context) (store.OIDCProvider, bool, error)
	SaveOIDCProvider(context.Context, store.OIDCProvider, int64) error
	ClearOIDCProvider(context.Context, int64) error
}

type adminOIDCProviderInput struct {
	Enabled      bool   `json:"enabled"`
	Issuer       string `json:"issuer"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RedirectURL  string `json:"redirect_url"`
	DisplayName  string `json:"display_name"`
}

// adminOIDCProviderView is the only shape the stored provider crosses the
// console boundary in: the secret is reduced to whether one is stored, so the
// plaintext and the ciphertext never reach a browser.
type adminOIDCProviderView struct {
	Enabled         bool       `json:"enabled"`
	Issuer          string     `json:"issuer"`
	ClientID        string     `json:"client_id"`
	RedirectURL     string     `json:"redirect_url"`
	DisplayName     string     `json:"display_name"`
	HasClientSecret bool       `json:"has_client_secret"`
	Ready           bool       `json:"ready"`
	EnvSeedApplied  bool       `json:"env_seed_applied"`
	UpdatedAt       *time.Time `json:"updated_at"`
	UpdatedBy       int64      `json:"updated_by"`
}

func adminOIDCProviderResponse(provider store.OIDCProvider) adminOIDCProviderView {
	view := adminOIDCProviderView{
		Enabled:         provider.Enabled,
		Issuer:          provider.Issuer,
		ClientID:        provider.ClientID,
		RedirectURL:     provider.RedirectURL,
		DisplayName:     provider.DisplayName,
		HasClientSecret: provider.ClientSecretCiphertext != "",
		Ready:           provider.Ready(),
		EnvSeedApplied:  provider.EnvSeedApplied,
		UpdatedBy:       provider.UpdatedBy,
	}
	if !provider.UpdatedAt.IsZero() {
		updatedAt := provider.UpdatedAt
		view.UpdatedAt = &updatedAt
	}
	return view
}

// oidcProvider serves /api/admin/oidc-provider: the single primary OAuth/OIDC
// provider an administrator configures for the gateway. Authentication, role
// enforcement, CSRF and the write audit trail are handled by the admin
// dispatcher before this ever runs.
func (h *adminHandler) oidcProvider(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) != 0 {
		notFound(w)
		return
	}
	providers, ok := h.store.(oidcProviderAdminStore)
	if !ok {
		writeAdminUnsupported(w)
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		h.readOIDCProvider(w, r, providers)
	case http.MethodPut:
		h.saveOIDCProvider(w, r, providers)
	case http.MethodDelete:
		h.clearOIDCProvider(w, r, providers)
	default:
		writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed", "method not allowed")
	}
}

func (h *adminHandler) readOIDCProvider(w http.ResponseWriter, r *http.Request, providers oidcProviderAdminStore) {
	provider, ok, err := providers.GetOIDCProvider(r.Context())
	if err != nil {
		writeAdminStoreError(w, err)
		return
	}
	if !ok {
		// An unconfigured gateway is a normal state, not a missing resource:
		// the console renders the empty form from a 200.
		writeJSON(w, http.StatusOK, adminOIDCProviderResponse(store.OIDCProvider{}))
		return
	}
	writeJSON(w, http.StatusOK, adminOIDCProviderResponse(provider))
}

func (h *adminHandler) saveOIDCProvider(w http.ResponseWriter, r *http.Request, providers oidcProviderAdminStore) {
	var input adminOIDCProviderInput
	if err := decodeAdminJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "invalid OIDC provider request")
		return
	}
	input.Issuer = strings.TrimSpace(input.Issuer)
	input.ClientID = strings.TrimSpace(input.ClientID)
	input.RedirectURL = strings.TrimSpace(input.RedirectURL)
	input.DisplayName = strings.TrimSpace(input.DisplayName)

	// The stored row decides whether an omitted secret is acceptable and the
	// store's own upsert preserves that secret, so validation reads it first
	// and nothing is written when the request is rejected below.
	stored, exists, err := providers.GetOIDCProvider(r.Context())
	if err != nil {
		writeAdminStoreError(w, err)
		return
	}
	hasStoredSecret := exists && stored.ClientSecretCiphertext != ""
	if err := validateOIDCProviderInput(input, hasStoredSecret); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_oidc_provider", err.Error())
		return
	}

	provider := store.OIDCProvider{
		Issuer:      input.Issuer,
		ClientID:    input.ClientID,
		RedirectURL: input.RedirectURL,
		DisplayName: input.DisplayName,
		Enabled:     input.Enabled,
	}
	// An empty client_secret means "keep the stored one", which the store
	// enforces; only a supplied secret is encrypted here.
	if input.ClientSecret != "" {
		encrypted, err := h.encryptProviderKey(input.ClientSecret)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "server_error", "encryption_unavailable", "OIDC client secret encryption is unavailable")
			return
		}
		provider.ClientSecretCiphertext = encrypted
	}
	if err := providers.SaveOIDCProvider(r.Context(), provider, h.actorID(r)); err != nil {
		writeAdminStoreError(w, err)
		return
	}
	// The save is committed, so the runtime cache is stale by definition.
	h.oidcProviders.Invalidate()
	h.readOIDCProvider(w, r, providers)
}

func (h *adminHandler) clearOIDCProvider(w http.ResponseWriter, r *http.Request, providers oidcProviderAdminStore) {
	if err := providers.ClearOIDCProvider(r.Context(), h.actorID(r)); err != nil {
		writeAdminStoreError(w, err)
		return
	}
	// The runtime cache must not keep serving the configuration that was just
	// cleared; the next Provider call reads the (kept, blank) row again.
	h.oidcProviders.Invalidate()
	w.WriteHeader(http.StatusNoContent)
}

func validateOIDCProviderInput(input adminOIDCProviderInput, hasStoredSecret bool) error {
	if input.Issuer != "" && !absoluteURL(input.Issuer) {
		return errors.New("issuer must be an absolute URL")
	}
	if input.RedirectURL != "" && !absoluteURL(input.RedirectURL) {
		return errors.New("redirect_url must be an absolute URL")
	}
	if !input.Enabled {
		// A disabled provider may keep partial values, but anything present
		// still had to parse above.
		return nil
	}
	missing := make([]string, 0, 4)
	if input.Issuer == "" {
		missing = append(missing, "issuer")
	}
	if input.ClientID == "" {
		missing = append(missing, "client_id")
	}
	if input.ClientSecret == "" && !hasStoredSecret {
		missing = append(missing, "client_secret")
	}
	if input.RedirectURL == "" {
		missing = append(missing, "redirect_url")
	}
	if len(missing) > 0 {
		return fmt.Errorf("enabled OIDC provider requires %s", strings.Join(missing, ", "))
	}
	return nil
}

func absoluteURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.IsAbs() && parsed.Host != ""
}

// actorID resolves the signed-in administrator again for audit attribution,
// the same way recordAudit does.
func (h *adminHandler) actorID(r *http.Request) int64 {
	user, _, ok := h.auth.authenticate(r)
	if !ok || user == nil {
		return 0
	}
	return user.ID
}
