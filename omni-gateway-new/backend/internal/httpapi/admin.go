package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/zjvtit/omni-gateway/internal/adapter"
	gatewaycrypto "github.com/zjvtit/omni-gateway/internal/crypto"
	"github.com/zjvtit/omni-gateway/internal/model"
	"github.com/zjvtit/omni-gateway/internal/store"
	"github.com/zjvtit/omni-gateway/internal/upstream"
)

const maxAdminBodyBytes = 2 << 20

type adminHandler struct {
	auth           *authHandler
	store          AuthStore
	cipher         *gatewaycrypto.Cipher
	upstreamClient *http.Client
	runtimeStats   func() any
	// oidcProviders is the runtime provider cache. It may be nil when the
	// server was started without one; writes then simply skip invalidation.
	oidcProviders *oidcProviderSource
}

func newAdminHandler(authStore AuthStore, cfg AuthConfig, cipher *gatewaycrypto.Cipher) http.Handler {
	client := cfg.UpstreamClient
	if client == nil {
		client = upstream.NewClient(false)
	}
	return &adminHandler{auth: newAuthHandler(authStore, cfg), store: authStore, cipher: cipher, upstreamClient: client, runtimeStats: cfg.RuntimeStats, oidcProviders: cfg.OIDCProvider}
}

func (h *adminHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	user, csrf, ok := h.auth.authenticate(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication_error", "authentication_required", "administrator authentication required")
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/admin"), "/")
	if !readOnly(r) || strings.HasPrefix(path, "content/") {
		detail := "admin API mutation"
		if readOnly(r) {
			detail = "content log read"
		}
		var finish func()
		w, finish = beginChangeAudit(w, r, h.store, user, path, detail)
		defer finish()
	}
	// Any active account may sign in now, so holding a session is not by itself
	// authorization: the management surface stays administrator-only.
	if user.Role != model.RoleAdmin {
		writeError(w, http.StatusForbidden, "invalid_request_error", "administrator_required", "administrator privileges required")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && !sameToken(r.Header.Get("X-CSRF-Token"), csrf) {
		writeError(w, http.StatusForbidden, "invalid_request_error", "csrf_failed", "invalid CSRF token")
		return
	}
	if path == "" {
		writeJSON(w, http.StatusOK, map[string]any{"service": "llm-gateway-admin", "status": "ok"})
		return
	}
	parts := strings.Split(path, "/")
	switch parts[0] {
	case "runtime":
		if r.Method != http.MethodGet || len(parts) != 1 {
			notFound(w)
			return
		}
		if h.runtimeStats == nil {
			writeAdminUnsupported(w)
			return
		}
		writeJSON(w, 200, h.runtimeStats())
	case "users":
		h.users(w, r, parts[1:])
	case "api-keys":
		h.apiKeys(w, r, parts[1:])
	case "providers":
		h.providers(w, r, parts[1:])
	case "aliases":
		h.aliases(w, r, parts[1:])
	case "prices":
		h.prices(w, r, parts[1:])
	case "rate-limits":
		h.rateLimits(w, r, parts[1:])
	case "requests":
		h.requests(w, r, parts[1:])
	case "audit":
		h.audit(w, r, parts[1:])
	case "stats":
		h.stats(w, r, parts[1:])
	case "content":
		h.content(w, r, parts[1:])
	case "oidc-provider":
		h.oidcProvider(w, r, parts[1:])
	default:
		notFound(w)
	}
}

type adminUserInput struct {
	Username    string     `json:"username"`
	DisplayName string     `json:"display_name"`
	Role        model.Role `json:"role"`
	Password    string     `json:"password"`
	Active      *bool      `json:"active"`
}

func (h *adminHandler) users(w http.ResponseWriter, r *http.Request, parts []string) {
	users, ok := h.store.(interface {
		ListUsers(context.Context) ([]*model.User, error)
		GetUserByID(context.Context, int64) (*model.User, error)
		CreateUser(context.Context, *model.User) (int64, error)
		UpdateUser(context.Context, *model.User) error
		SetUserPassword(context.Context, int64, string) error
	})
	if !ok {
		writeAdminUnsupported(w)
		return
	}
	if len(parts) == 0 && r.Method == http.MethodGet {
		items, err := users.ListUsers(r.Context())
		if err != nil {
			writeAdminStoreError(w, err)
			return
		}
		out := make([]authUser, 0, len(items))
		for _, item := range items {
			if item != nil {
				out = append(out, toAuthUser(item))
			}
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	if len(parts) == 0 && r.Method == http.MethodPost {
		var input adminUserInput
		if err := decodeAdminJSON(w, r, &input); err != nil || strings.TrimSpace(input.Username) == "" || input.Password == "" {
			writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "username and password are required")
			return
		}
		role := input.Role
		if role == "" {
			role = model.RoleUser
		}
		if role != model.RoleAdmin && role != model.RoleUser {
			writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_role", "role must be admin or user")
			return
		}
		hash, err := gatewaycrypto.HashPassword(input.Password)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "server_error", "password_hash_failed", "could not hash password")
			return
		}
		active := true
		if input.Active != nil {
			active = *input.Active
		}
		user := &model.User{Username: strings.TrimSpace(input.Username), DisplayName: input.DisplayName, Role: role, PasswordHash: hash, Active: active, PasswordLoginEnabled: true}
		id, err := users.CreateUser(r.Context(), user)
		if err != nil {
			writeAdminStoreError(w, err)
			return
		}
		user.ID = id
		writeJSON(w, http.StatusCreated, toAuthUser(user))
		return
	}
	if len(parts) != 1 {
		notFound(w)
		return
	}
	id, err := parseAdminID(parts[0])
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_id", "invalid user id")
		return
	}
	if r.Method != http.MethodPatch {
		writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed", "method not allowed")
		return
	}
	user, err := users.GetUserByID(r.Context(), id)
	if err != nil {
		writeAdminStoreError(w, err)
		return
	}
	var input adminUserInput
	if err := decodeAdminJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "invalid user request")
		return
	}
	if input.DisplayName != "" {
		user.DisplayName = input.DisplayName
	}
	if input.Role != "" {
		if input.Role != model.RoleAdmin && input.Role != model.RoleUser {
			writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_role", "role must be admin or user")
			return
		}
		user.Role = input.Role
	}
	if input.Active != nil {
		user.Active = *input.Active
	}
	if err := users.UpdateUser(r.Context(), user); err != nil {
		writeAdminStoreError(w, err)
		return
	}
	if input.Password != "" {
		hash, err := gatewaycrypto.HashPassword(input.Password)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "server_error", "password_hash_failed", "could not hash password")
			return
		}
		if err := users.SetUserPassword(r.Context(), id, hash); err != nil {
			writeAdminStoreError(w, err)
			return
		}
		user.PasswordLoginEnabled = true
	}
	writeJSON(w, http.StatusOK, toAuthUser(user))
}

type adminAPIKeyInput = apiKeyInput

func (h *adminHandler) apiKeys(w http.ResponseWriter, r *http.Request, parts []string) {
	keys, ok := h.store.(interface {
		GetAPIKeyByID(context.Context, int64) (*model.APIKey, error)
		ListAPIKeys(context.Context) ([]*model.APIKey, error)
		ListAPIKeysByUser(context.Context, int64) ([]*model.APIKey, error)
		CreateAPIKey(context.Context, *model.APIKey) (int64, error)
		UpdateAPIKey(context.Context, *model.APIKey) error
		DeleteAPIKey(context.Context, int64) error
	})
	if !ok {
		writeAdminUnsupported(w)
		return
	}
	if len(parts) == 0 && r.Method == http.MethodGet {
		var items []*model.APIKey
		var err error
		if rawUser := r.URL.Query().Get("user_id"); rawUser != "" {
			id, parseErr := parseAdminID(rawUser)
			if parseErr != nil {
				writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_id", "invalid user id")
				return
			}
			items, err = keys.ListAPIKeysByUser(r.Context(), id)
		} else {
			items, err = keys.ListAPIKeys(r.Context())
		}
		if err != nil {
			writeAdminStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
		return
	}
	owner, _, authenticated := h.auth.authenticate(r)
	if !authenticated || owner == nil {
		writeError(w, http.StatusUnauthorized, "authentication_error", "authentication_required", "administrator authentication required")
		return
	}
	if len(parts) == 0 && r.Method == http.MethodPost {
		(&selfHandler{store: h.store}).createAPIKey(w, r, owner, keys)
		return
	}
	if len(parts) != 1 {
		notFound(w)
		return
	}
	id, err := parseAdminID(parts[0])
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_id", "invalid API key id")
		return
	}
	key, err := keys.GetAPIKeyByID(r.Context(), id)
	if err != nil {
		writeAdminStoreError(w, err)
		return
	}
	if key == nil {
		notFound(w)
		return
	}
	// Work on a copy; rejected writes never mutate store-returned objects.
	copy := *key
	key = &copy
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, key)
	case http.MethodPatch:
		var input adminAPIKeyInput
		if err := decodeAdminJSON(w, r, &input); err != nil {
			writeError(w, 400, "invalid_request_error", "invalid_request", "invalid API key request")
			return
		}
		if !keyOwnerInput(w, input, key.UserID, false) {
			return
		}
		if key.UserID != owner.ID {
			if len(input.fields) != 1 || input.Active == nil || *input.Active {
				writeError(w, 403, "permission_error", "key_owner_required", "only the owner may edit or delete this key; administrators may only disable it")
				return
			}
			key.Active = false
		} else if !applyOwnKeyInput(w, input, key) {
			return
		}
		if err := keys.UpdateAPIKey(r.Context(), key); err != nil {
			writeAdminStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, key)
	case http.MethodDelete:
		if key.UserID != owner.ID {
			writeError(w, 403, "permission_error", "key_owner_required", "only the owner may delete this key")
			return
		}
		if err := keys.DeleteAPIKey(r.Context(), id); err != nil {
			writeAdminStoreError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		writeError(w, 405, "invalid_request_error", "method_not_allowed", "method not allowed")
	}
}

func newGatewayAPIKey() (secret, prefix, suffix string, err error) {
	raw := make([]byte, 24)
	if _, err = rand.Read(raw); err != nil {
		return "", "", "", err
	}
	secret = "sk-llm-" + base64.RawURLEncoding.EncodeToString(raw)
	prefix = secret[:minInt(12, len(secret))]
	suffix = secret[len(secret)-4:]
	return secret, prefix, suffix, nil
}

type adminProviderInput struct {
	Name                 string                              `json:"name"`
	Type                 model.ProviderType                  `json:"type"`
	CompatibilityProfile *model.ProviderCompatibilityProfile `json:"compatibility_profile"`
	BaseURL              string                              `json:"base_url"`
	APIKey               string                              `json:"api_key"`
	ExtraHeaders         map[string]string                   `json:"extra_headers"`
	TimeoutMS            int                                 `json:"timeout_ms"`
	MaxConcurrency       *int                                `json:"max_concurrency"`
	Active               *bool                               `json:"active"`
	SupportsResponses    *bool                               `json:"supports_responses"`
}

func (h *adminHandler) providers(w http.ResponseWriter, r *http.Request, parts []string) {
	providers, ok := h.store.(interface {
		ListProviders(context.Context) ([]*model.Provider, error)
		GetProvider(context.Context, int64) (*model.Provider, error)
		CreateProvider(context.Context, *model.Provider) (int64, error)
		UpdateProvider(context.Context, *model.Provider) error
		DeleteProvider(context.Context, int64) error
		ListProviderModels(context.Context, int64) ([]*model.ProviderModel, error)
		UpsertProviderModel(context.Context, int64, string, model.Capabilities) error
	})
	if !ok {
		writeAdminUnsupported(w)
		return
	}
	if len(parts) == 0 && r.Method == http.MethodGet {
		items, err := providers.ListProviders(r.Context())
		if err != nil {
			writeAdminStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
		return
	}
	if len(parts) == 0 && r.Method == http.MethodPost {
		var input adminProviderInput
		if err := decodeAdminJSON(w, r, &input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "invalid provider request")
			return
		}
		if err := validateProviderInput(input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_provider", err.Error())
			return
		}
		key, err := h.encryptProviderKey(input.APIKey)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "server_error", "encryption_unavailable", "provider credential encryption is unavailable")
			return
		}
		active := true
		if input.Active != nil {
			active = *input.Active
		}
		profile := model.ProviderCompatibilityGeneric
		if input.CompatibilityProfile != nil {
			profile = *input.CompatibilityProfile
		}
		provider := &model.Provider{Name: strings.TrimSpace(input.Name), Type: input.Type, CompatibilityProfile: profile, BaseURL: strings.TrimRight(input.BaseURL, "/"), APIKeyEnc: key, ExtraHeader: input.ExtraHeaders, TimeoutMS: input.TimeoutMS, Active: active, SupportsResponses: input.SupportsResponses != nil && *input.SupportsResponses}
		if input.MaxConcurrency != nil {
			provider.MaxConcurrency = *input.MaxConcurrency
		}
		if provider.TimeoutMS <= 0 {
			provider.TimeoutMS = 30000
		}
		id, err := providers.CreateProvider(r.Context(), provider)
		if err != nil {
			writeAdminStoreError(w, err)
			return
		}
		provider.ID = id
		writeJSON(w, http.StatusCreated, provider)
		return
	}
	if len(parts) < 1 {
		notFound(w)
		return
	}
	id, err := parseAdminID(parts[0])
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_id", "invalid provider id")
		return
	}
	provider, err := providers.GetProvider(r.Context(), id)
	if err != nil {
		writeAdminStoreError(w, err)
		return
	}
	if provider == nil {
		notFound(w)
		return
	}
	if len(parts) == 2 && parts[1] == "models" {
		switch r.Method {
		case http.MethodGet:
			items, err := providers.ListProviderModels(r.Context(), id)
			if err != nil {
				writeAdminStoreError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, items)
		case http.MethodPost:
			var input struct {
				ModelID      string             `json:"model_id"`
				Capabilities model.Capabilities `json:"capabilities"`
			}
			if err := decodeAdminJSON(w, r, &input); err != nil || strings.TrimSpace(input.ModelID) == "" {
				writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "model_id is required")
				return
			}
			if err := providers.UpsertProviderModel(r.Context(), id, strings.TrimSpace(input.ModelID), input.Capabilities); err != nil {
				writeAdminStoreError(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, map[string]any{"provider_id": id, "model_id": strings.TrimSpace(input.ModelID), "capabilities": input.Capabilities})
		default:
			writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed", "method not allowed")
		}
		return
	}
	if len(parts) == 2 && parts[1] == "impact" {
		h.impact(w, r, "provider", id, 0)
		return
	}
	if len(parts) == 2 && (parts[1] == "discover" || parts[1] == "verify") && r.Method == http.MethodPost {
		if provider.Type != model.ProviderOpenAICompat && provider.Type != model.ProviderAnthropic && provider.Type != model.ProviderGemini {
			writeError(w, http.StatusNotImplemented, "server_error", "provider_discovery_unsupported", "model discovery is not supported for this provider type")
			return
		}
		apiKey := ""
		if provider.APIKeyEnc != "" {
			if h.cipher == nil {
				writeError(w, http.StatusServiceUnavailable, "server_error", "encryption_unavailable", "provider credential encryption is unavailable")
				return
			}
			apiKey, err = h.cipher.Decrypt(provider.APIKeyEnc)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "server_error", "credential_decrypt_failed", "could not decrypt provider credentials")
				return
			}
		}
		var upstream adapter.Adapter
		client := *h.upstreamClient
		client.Timeout = time.Duration(max(provider.TimeoutMS, 1000)) * time.Millisecond
		switch provider.Type {
		case model.ProviderOpenAICompat:
			upstream = adapter.NewOpenAIAdapter(adapter.Config{BaseURL: provider.BaseURL, APIKey: apiKey, ExtraHeader: provider.ExtraHeader, TimeoutMS: provider.TimeoutMS, HTTPClient: &client})
		case model.ProviderAnthropic:
			upstream = adapter.NewAnthropicAdapter(adapter.Config{BaseURL: provider.BaseURL, APIKey: apiKey, ExtraHeader: provider.ExtraHeader, TimeoutMS: provider.TimeoutMS, HTTPClient: &client})
		case model.ProviderGemini:
			upstream = adapter.NewGeminiAdapter(adapter.Config{BaseURL: provider.BaseURL, APIKey: apiKey, ExtraHeader: provider.ExtraHeader, TimeoutMS: provider.TimeoutMS, HTTPClient: &client})
		}
		modelIDs, err := upstream.ListModels(r.Context())
		code, message := "", ""
		if err != nil {
			code, message = describeUpstreamProbeError(err)
		}
		if probeStore, ok := h.store.(interface {
			RecordProviderVerification(context.Context, int64, bool, string) error
		}); ok {
			if recordErr := probeStore.RecordProviderVerification(r.Context(), provider.ID, err == nil, message); recordErr != nil {
				writeAdminStoreError(w, recordErr)
				return
			}
		}
		if err != nil {
			writeError(w, http.StatusBadGateway, "upstream_error", code, message)
			return
		}
		if parts[1] == "verify" {
			writeJSON(w, http.StatusOK, map[string]any{"provider_id": provider.ID, "healthy": true, "models": modelIDs})
			return
		}
		for _, modelID := range modelIDs {
			var err error
			if discovery, ok := h.store.(interface {
				DiscoverProviderModel(context.Context, int64, string) error
			}); ok {
				err = discovery.DiscoverProviderModel(r.Context(), provider.ID, modelID)
			} else {
				err = providers.UpsertProviderModel(r.Context(), provider.ID, modelID, model.Capabilities{})
			}
			if err != nil {
				writeAdminStoreError(w, err)
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"provider_id": provider.ID, "models": modelIDs})
		return
	}
	if len(parts) != 1 {
		notFound(w)
		return
	}
	providerCopy := *provider
	provider = &providerCopy
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, provider)
	case http.MethodPatch:
		var input adminProviderInput
		if err := decodeAdminJSON(w, r, &input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "invalid provider request")
			return
		}
		if input.Name != "" {
			provider.Name = input.Name
		}
		if input.Type != "" {
			provider.Type = input.Type
		}
		if input.CompatibilityProfile != nil {
			provider.CompatibilityProfile = *input.CompatibilityProfile
		}
		if input.BaseURL != "" {
			provider.BaseURL = strings.TrimRight(input.BaseURL, "/")
		}
		if input.ExtraHeaders != nil {
			provider.ExtraHeader = input.ExtraHeaders
		}
		if input.TimeoutMS > 0 {
			provider.TimeoutMS = input.TimeoutMS
		}
		if input.Active != nil {
			provider.Active = *input.Active
		}
		if input.MaxConcurrency != nil {
			provider.MaxConcurrency = *input.MaxConcurrency
		}
		if input.SupportsResponses != nil {
			provider.SupportsResponses = *input.SupportsResponses
		}
		if err := validateProviderInput(adminProviderInput{Name: provider.Name, Type: provider.Type, CompatibilityProfile: &provider.CompatibilityProfile, BaseURL: provider.BaseURL, MaxConcurrency: &provider.MaxConcurrency, ExtraHeaders: provider.ExtraHeader, TimeoutMS: provider.TimeoutMS}); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_provider", err.Error())
			return
		}
		if input.APIKey != "" {
			if err := validateProviderInput(adminProviderInput{Name: provider.Name, Type: provider.Type, BaseURL: provider.BaseURL}); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_provider", err.Error())
				return
			}
			provider.APIKeyEnc, err = h.encryptProviderKey(input.APIKey)
			if err != nil {
				writeError(w, http.StatusServiceUnavailable, "server_error", "encryption_unavailable", "provider credential encryption is unavailable")
				return
			}
		}
		if err := providers.UpdateProvider(r.Context(), provider); err != nil {
			writeAdminStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, provider)
	case http.MethodDelete:
		if err := providers.DeleteProvider(r.Context(), id); err != nil {
			writeAdminStoreError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed", "method not allowed")
	}
}

func validateProviderInput(input adminProviderInput) error {
	if input.TimeoutMS < 0 || input.TimeoutMS > 600000 {
		return errors.New("timeout_ms must be between 0 and 600000")
	}
	for name, value := range input.ExtraHeaders {
		key := http.CanonicalHeaderKey(name)
		if key == "Authorization" || key == "X-Api-Key" || key == "Cookie" || key == "Proxy-Authorization" || key == "Host" || key == "Connection" || key == "Transfer-Encoding" || key == "Content-Length" || strings.ContainsAny(name+value, "\r\n") {
			return errors.New("extra_headers cannot override credentials or transport headers")
		}
	}
	if input.MaxConcurrency != nil && (*input.MaxConcurrency < 0 || *input.MaxConcurrency > 10000) {
		return errors.New("max_concurrency must be between 0 and 10000")
	}
	if strings.TrimSpace(input.Name) == "" || input.Type == "" || strings.TrimSpace(input.BaseURL) == "" {
		return errors.New("name, type and base_url are required")
	}
	if input.Type != model.ProviderOpenAICompat && input.Type != model.ProviderAnthropic && input.Type != model.ProviderGemini {
		return errors.New("unsupported provider type")
	}
	profile := model.ProviderCompatibilityGeneric
	if input.CompatibilityProfile != nil {
		profile = *input.CompatibilityProfile
	}
	if profile != model.ProviderCompatibilityGeneric && profile != model.ProviderCompatibilityDeepSeek {
		return errors.New("unsupported compatibility_profile")
	}
	if profile == model.ProviderCompatibilityDeepSeek && input.Type != model.ProviderOpenAICompat {
		return errors.New("deepseek compatibility_profile requires openai_compatible type")
	}
	parsed, err := url.Parse(input.BaseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New("base_url must be an http or https URL")
	}
	if err := upstream.ValidateURL(input.BaseURL); err != nil {
		return err
	}
	return nil
}

func (h *adminHandler) encryptProviderKey(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if h.cipher == nil {
		return "", errors.New("cipher is not configured")
	}
	return h.cipher.Encrypt(value)
}

func (h *adminHandler) aliases(w http.ResponseWriter, r *http.Request, parts []string) {
	aliases, ok := h.store.(interface {
		ListAliases(context.Context) ([]*model.ModelAlias, error)
		GetAlias(context.Context, string) (*model.ModelAlias, error)
		CreateAlias(context.Context, string) (int64, error)
		SetAliasActive(context.Context, int64, bool) error
		RenameAlias(context.Context, int64, string) error
		DeleteAlias(context.Context, int64) error
		AddRouteEntry(context.Context, int64, int64, string, int) (int64, error)
		ListRouteEntries(context.Context, int64) ([]*model.RouteEntry, error)
		DeleteRouteEntry(context.Context, int64) error
		SetRouteEntryActive(context.Context, int64, bool) error
	})
	if !ok {
		writeAdminUnsupported(w)
		return
	}
	if len(parts) == 0 && r.Method == http.MethodGet {
		items, err := aliases.ListAliases(r.Context())
		if err != nil {
			writeAdminStoreError(w, err)
			return
		}
		if readyStore, ok := h.store.(readinessStore); ok {
			out := make([]map[string]any, 0, len(items))
			for _, a := range items {
				if a == nil {
					continue
				}
				ready, err := computeReadiness(r.Context(), readyStore, a)
				if err != nil {
					writeAdminStoreError(w, err)
					return
				}
				out = append(out, map[string]any{"id": a.ID, "name": a.Name, "active": a.Active, "created_at": a.CreatedAt, "readiness": ready})
			}
			writeJSON(w, 200, out)
		} else {
			writeJSON(w, 200, items)
		}
		return
	}
	if len(parts) == 0 && r.Method == http.MethodPost {
		var input struct {
			Name   string `json:"name"`
			Active *bool  `json:"active"`
		}
		if err := decodeAdminJSON(w, r, &input); err != nil || strings.TrimSpace(input.Name) == "" {
			writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "name is required")
			return
		}
		if input.Active != nil && *input.Active {
			writeError(w, 400, "invalid_request_error", "alias_not_ready", "save an inactive draft, add an enabled route to an enabled provider, then enable the model")
			return
		}
		id, err := aliases.CreateAlias(r.Context(), strings.TrimSpace(input.Name))
		if err != nil {
			writeAdminStoreError(w, err)
			return
		}
		if err := aliases.SetAliasActive(r.Context(), id, false); err != nil {
			writeAdminStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"id": id, "name": strings.TrimSpace(input.Name), "active": false})
		return
	}
	if len(parts) < 1 {
		notFound(w)
		return
	}
	id, err := parseAdminID(parts[0])
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_id", "invalid alias id")
		return
	}
	if len(parts) == 2 && parts[1] == "readiness" {
		h.readiness(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "impact" {
		h.impact(w, r, "alias", id, 0)
		return
	}
	if len(parts) == 4 && parts[1] == "routes" && parts[3] == "impact" {
		routeID, err := parseAdminID(parts[2])
		if err != nil {
			writeError(w, 400, "invalid_request_error", "invalid_id", "invalid route id")
			return
		}
		h.impact(w, r, "route", id, routeID)
		return
	}
	if _, err := aliasByID(r.Context(), aliases, id); err != nil {
		writeAdminStoreError(w, err)
		return
	}
	if len(parts) == 3 && parts[1] == "routes" && parts[2] == "order" {
		if r.Method != http.MethodPatch {
			writeError(w, 405, "invalid_request_error", "method_not_allowed", "method not allowed")
			return
		}
		var input struct {
			RouteIDs []int64 `json:"route_ids"`
		}
		if err := decodeAdminJSON(w, r, &input); err != nil || input.RouteIDs == nil {
			writeError(w, 400, "invalid_request_error", "invalid_request", "route_ids is required")
			return
		}
		ordering, ok := h.store.(interface {
			ReorderRoutes(context.Context, int64, []int64) error
		})
		if !ok {
			writeAdminUnsupported(w)
			return
		}
		if err := ordering.ReorderRoutes(r.Context(), id, input.RouteIDs); err != nil {
			if errors.Is(err, store.ErrRouteOrder) {
				writeError(w, 409, "invalid_request_error", "route_order_conflict", err.Error())
			} else {
				writeAdminStoreError(w, err)
			}
			return
		}
		writeJSON(w, 200, map[string]any{"route_ids": input.RouteIDs})
		return
	}
	if len(parts) == 3 && parts[1] == "routes" {
		routeID, parseErr := parseAdminID(parts[2])
		if parseErr != nil {
			writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_id", "invalid route id")
			return
		}
		entries, err := aliases.ListRouteEntries(r.Context(), id)
		if err != nil {
			writeAdminStoreError(w, err)
			return
		}
		belongs := false
		for _, entry := range entries {
			if entry != nil && entry.ID == routeID {
				belongs = true
				break
			}
		}
		if !belongs {
			notFound(w)
			return
		}
		switch r.Method {
		case http.MethodDelete:
			if err := aliases.DeleteRouteEntry(r.Context(), routeID); err != nil {
				writeAdminStoreError(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case http.MethodPatch:
			var input struct {
				Active *bool `json:"active"`
			}
			if err := decodeAdminJSON(w, r, &input); err != nil || input.Active == nil {
				writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "active is required")
				return
			}
			if err := aliases.SetRouteEntryActive(r.Context(), routeID, *input.Active); err != nil {
				writeAdminStoreError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"id": routeID, "active": *input.Active})
		default:
			writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed", "method not allowed")
		}
		return
	}
	if len(parts) == 2 && parts[1] == "routes" {
		h.routes(w, r, aliases, id)
		return
	}
	if len(parts) != 1 {
		notFound(w)
		return
	}
	var input struct {
		Name   string `json:"name"`
		Active *bool  `json:"active"`
	}
	switch r.Method {
	case http.MethodDelete:
		if err := aliases.DeleteAlias(r.Context(), id); err != nil {
			writeAdminStoreError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodPatch:
		if err := decodeAdminJSON(w, r, &input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "invalid alias request")
			return
		}
		name := strings.TrimSpace(input.Name)
		if name == "" && input.Active == nil {
			writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "name or active is required")
			return
		}
		if input.Active != nil && *input.Active {
			a, err := aliasByID(r.Context(), aliases, id)
			if err != nil {
				writeAdminStoreError(w, err)
				return
			}
			if !a.Active {
				s, ok := h.store.(readinessStore)
				if !ok {
					writeAdminUnsupported(w)
					return
				}
				ready, err := computeReadiness(r.Context(), s, a)
				if err != nil {
					writeAdminStoreError(w, err)
					return
				}
				if ready.EffectiveRoutes == 0 {
					writeError(w, 400, "invalid_request_error", "alias_not_ready", "at least one enabled route must point to an existing enabled provider")
					return
				}
			}
		}
		if name != "" {
			if err := aliases.RenameAlias(r.Context(), id, name); err != nil {
				if errors.Is(err, store.ErrAliasConflict) {
					writeError(w, http.StatusConflict, "invalid_request_error", "alias_conflict", err.Error())
					return
				}
				writeAdminStoreError(w, err)
				return
			}
		}
		if input.Active != nil {
			if err := aliases.SetAliasActive(r.Context(), id, *input.Active); err != nil {
				writeAdminStoreError(w, err)
				return
			}
		}
		result := map[string]any{"id": id}
		if name != "" {
			result["name"] = name
		}
		if input.Active != nil {
			result["active"] = *input.Active
		}
		writeJSON(w, http.StatusOK, result)
	default:
		writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed", "method not allowed")
	}
}

func (h *adminHandler) routes(w http.ResponseWriter, r *http.Request, aliases interface {
	AddRouteEntry(context.Context, int64, int64, string, int) (int64, error)
	ListRouteEntries(context.Context, int64) ([]*model.RouteEntry, error)
	DeleteRouteEntry(context.Context, int64) error
	SetRouteEntryActive(context.Context, int64, bool) error
}, aliasID int64) {
	if r.Method == http.MethodGet {
		items, err := aliases.ListRouteEntries(r.Context(), aliasID)
		if err != nil {
			writeAdminStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
		return
	}
	if r.Method == http.MethodPost {
		var input struct {
			ProviderID    int64  `json:"provider_id"`
			ProviderModel string `json:"provider_model"`
			Priority      int    `json:"priority"`
		}
		if err := decodeAdminJSON(w, r, &input); err != nil || input.ProviderID <= 0 || strings.TrimSpace(input.ProviderModel) == "" {
			writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "provider_id and provider_model are required")
			return
		}
		providers, ok := h.store.(interface {
			GetProvider(context.Context, int64) (*model.Provider, error)
		})
		if !ok {
			writeAdminUnsupported(w)
			return
		}
		provider, err := providers.GetProvider(r.Context(), input.ProviderID)
		if err != nil {
			writeAdminStoreError(w, err)
			return
		}
		if provider == nil {
			notFound(w)
			return
		}
		input.ProviderModel = strings.TrimSpace(input.ProviderModel)
		id, err := aliases.AddRouteEntry(r.Context(), aliasID, input.ProviderID, input.ProviderModel, input.Priority)
		if err != nil {
			writeAdminStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"id": id, "alias_id": aliasID, "provider_id": input.ProviderID, "provider_model": input.ProviderModel, "priority": input.Priority, "active": true})
		return
	}
	if len(strings.Trim(r.URL.Path, "/")) > 0 {
		writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed", "method not allowed")
		return
	}
	writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed", "method not allowed")
}

type adminPriceInput struct {
	ModelID string            `json:"model_id"`
	Version int               `json:"version"`
	Cost    map[string]string `json:"cost"`
	Credits map[string]string `json:"credits"`
}

func (h *adminHandler) prices(w http.ResponseWriter, r *http.Request, parts []string) {
	prices, ok := h.store.(interface {
		ListAllPriceVersions(context.Context) ([]*model.PriceVersion, error)
		CreatePriceVersion(context.Context, *model.PriceVersion) (int64, error)
		GetLatestPriceVersion(context.Context, string) (*model.PriceVersion, error)
		ListPriceVersions(context.Context, string) ([]*model.PriceVersion, error)
	})
	if !ok {
		writeAdminUnsupported(w)
		return
	}
	if r.Method == http.MethodGet {
		modelID := strings.TrimSpace(r.URL.Query().Get("model_id"))
		if modelID == "" {
			items, listErr := prices.ListAllPriceVersions(r.Context())
			if listErr != nil {
				writeAdminStoreError(w, listErr)
				return
			}
			writeJSON(w, http.StatusOK, items)
			return
		}
		var value any
		var err error
		if r.URL.Query().Get("latest") == "true" {
			value, err = prices.GetLatestPriceVersion(r.Context(), modelID)
		} else {
			value, err = prices.ListPriceVersions(r.Context(), modelID)
		}
		if err != nil {
			writeAdminStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, value)
		return
	}
	if len(parts) == 0 && r.Method == http.MethodPost {
		var input adminPriceInput
		if err := decodeAdminJSON(w, r, &input); err != nil || strings.TrimSpace(input.ModelID) == "" || input.Version <= 0 {
			writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "model_id and positive version are required")
			return
		}
		cost, err := parseMoneyMap(input.Cost)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_price", err.Error())
			return
		}
		credits, err := parseCreditsMap(input.Credits)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_price", err.Error())
			return
		}
		if err := validatePriceMaps(cost, credits); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_price", err.Error())
			return
		}
		price := &model.PriceVersion{ModelID: strings.TrimSpace(input.ModelID), Version: input.Version, Cost: cost, Credits: credits}
		id, err := prices.CreatePriceVersion(r.Context(), price)
		if err != nil {
			writeAdminStoreError(w, err)
			return
		}
		price.ID = id
		writeJSON(w, http.StatusCreated, price)
		return
	}
	writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed", "method not allowed")
}

// parseMoneyMap parses per-meter upstream cost rates, in CNY per unit.
func parseMoneyMap(values map[string]string) (map[string]model.Money, error) {
	out := make(map[string]model.Money, len(values))
	for key, value := range values {
		money, err := model.NewMoney(value)
		if err != nil || money.IsNegative() {
			return nil, fmt.Errorf("invalid cost price for meter %q", key)
		}
		out[key] = money
	}
	return out, nil
}

// parseCreditsMap parses non-negative per-meter credit rates, in credits per
// unit. Explicit zero rates waive credits; rates keep their supplied precision,
// and only charged amounts are quantized (see model.RoundCredits).
func parseCreditsMap(values map[string]string) (map[string]model.Credits, error) {
	out := make(map[string]model.Credits, len(values))
	for key, value := range values {
		credits, err := model.NewCredits(value)
		if err != nil || credits.IsNegative() {
			return nil, fmt.Errorf("invalid credit price for meter %q: a credit rate must be a non-negative number", key)
		}
		out[key] = credits
	}
	return out, nil
}

// validatePriceMaps enforces the two-ledger price contract: a price version must
// explicitly declare both ledgers, and every upstream meter must also carry a
// caller-facing credit rate, including an explicit zero when credits are waived.
// Missing credits (including stale payloads using the pre-rename "sale" field)
// are incomplete configuration, not an intentional free rate.
func validatePriceMaps(cost map[string]model.Money, credits map[string]model.Credits) error {
	if len(cost) == 0 {
		return errors.New("cost must declare at least one meter and its CNY rate")
	}
	if len(credits) == 0 {
		return errors.New("credits must declare at least one meter and its credit rate")
	}
	for meter := range cost {
		if _, ok := credits[meter]; !ok {
			return fmt.Errorf("credits must declare a rate for meter %q, which cost charges", meter)
		}
	}
	return nil
}

type adminRateLimitInput struct {
	Scope   string `json:"scope"`
	ScopeID *int64 `json:"scope_id"`
	// ModelID is a pointer so PATCH can tell "omitted" (keep the current value)
	// apart from "empty string" (clear it, i.e. apply to every model).
	ModelID     *string `json:"model_id"`
	RPM         *int64  `json:"rpm"`
	TPM         *int64  `json:"tpm"`
	Concurrency *int64  `json:"concurrency"`
	Active      *bool   `json:"active"`
}

func (h *adminHandler) rateLimits(w http.ResponseWriter, r *http.Request, parts []string) {
	limits, ok := h.store.(interface {
		ListAllRateLimits(context.Context) ([]*model.RateLimit, error)
		GetRateLimit(context.Context, int64) (*model.RateLimit, error)
		ListRateLimits(context.Context, string, int64) ([]*model.RateLimit, error)
		CreateRateLimit(context.Context, *model.RateLimit) (int64, error)
		UpdateRateLimit(context.Context, *model.RateLimit) error
		DeleteRateLimit(context.Context, int64) error
	})
	if !ok {
		writeAdminUnsupported(w)
		return
	}
	if len(parts) == 0 && r.Method == http.MethodGet {
		if r.URL.Query().Get("scope") == "" {
			items, listErr := limits.ListAllRateLimits(r.Context())
			if listErr != nil {
				writeAdminStoreError(w, listErr)
				return
			}
			writeJSON(w, http.StatusOK, items)
			return
		}
		scope := r.URL.Query().Get("scope")
		var scopeID int64
		var err error
		if scope == "model" && r.URL.Query().Get("scope_id") == "" {
			scopeID = 0
		} else {
			scopeID, err = parseQueryID(r, "scope_id")
		}
		if scope == "" || err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request_error", "scope_required", "scope and scope_id are required")
			return
		}
		items, err := limits.ListRateLimits(r.Context(), scope, scopeID)
		if err != nil {
			writeAdminStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
		return
	}
	if len(parts) == 0 && r.Method == http.MethodPost {
		var input adminRateLimitInput
		if err := decodeAdminJSON(w, r, &input); err != nil || !validRateScope(input.Scope) || (input.Scope != "model" && (input.ScopeID == nil || *input.ScopeID <= 0)) {
			writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "scope and positive scope_id are required")
			return
		}
		rule := rateLimitFromInput(input)
		id, err := limits.CreateRateLimit(r.Context(), rule)
		if err != nil {
			writeAdminStoreError(w, err)
			return
		}
		rule.ID = id
		writeJSON(w, http.StatusCreated, rule)
		return
	}
	if len(parts) != 1 {
		writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed", "method not allowed")
		return
	}
	id, err := parseAdminID(parts[0])
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_id", "invalid rate-limit id")
		return
	}
	if r.Method == http.MethodDelete {
		if err := limits.DeleteRateLimit(r.Context(), id); err != nil {
			writeAdminStoreError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method == http.MethodPatch {
		var input adminRateLimitInput
		if err := decodeAdminJSON(w, r, &input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "invalid rate-limit request")
			return
		}
		if input.Scope != "" && !validRateScope(input.Scope) {
			writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_scope", "scope must be user, key, or model")
			return
		}
		if input.ScopeID != nil && input.Scope != "model" && *input.ScopeID <= 0 {
			writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_scope_id", "scope_id must be positive")
			return
		}
		rule, err := limits.GetRateLimit(r.Context(), id)
		if err != nil {
			writeAdminStoreError(w, err)
			return
		}
		if input.Scope != "" {
			rule.Scope = input.Scope
		}
		if input.ScopeID != nil {
			rule.ScopeID = *input.ScopeID
		}
		if input.ModelID != nil {
			rule.ModelID = strings.TrimSpace(*input.ModelID)
		}
		if input.RPM != nil {
			rule.RPM = maxInt64(*input.RPM, 0)
		}
		if input.TPM != nil {
			rule.TPM = maxInt64(*input.TPM, 0)
		}
		if input.Concurrency != nil {
			rule.Concurrency = maxInt64(*input.Concurrency, 0)
		}
		if input.Active != nil {
			rule.Active = *input.Active
		}
		// Model-scope rules are stored under scope_id 0: the runtime loads them with
		// ListRateLimits("model", 0), so any other value would never be applied.
		if rule.Scope == "model" {
			rule.ScopeID = 0
		}
		rule.ID = id
		if err := limits.UpdateRateLimit(r.Context(), rule); err != nil {
			writeAdminStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, rule)
		return
	}
	writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed", "method not allowed")
}

func rateLimitFromInput(input adminRateLimitInput) *model.RateLimit {
	active := true
	if input.Active != nil {
		active = *input.Active
	}
	scopeID, rpm, tpm, concurrency := int64(0), int64(0), int64(0), int64(0)
	if input.ScopeID != nil {
		scopeID = *input.ScopeID
	}
	if input.RPM != nil {
		rpm = maxInt64(*input.RPM, 0)
	}
	if input.TPM != nil {
		tpm = maxInt64(*input.TPM, 0)
	}
	if input.Concurrency != nil {
		concurrency = maxInt64(*input.Concurrency, 0)
	}
	modelID := ""
	if input.ModelID != nil {
		modelID = strings.TrimSpace(*input.ModelID)
	}
	// Model-scope rules are stored under scope_id 0 (see the PATCH handler).
	if input.Scope == "model" {
		scopeID = 0
	}
	return &model.RateLimit{Scope: input.Scope, ScopeID: scopeID, ModelID: modelID, RPM: rpm, TPM: tpm, Concurrency: concurrency, Active: active}
}

func validRateScope(scope string) bool {
	return scope == "user" || scope == "key" || scope == "model"
}

func (h *adminHandler) requests(w http.ResponseWriter, r *http.Request, parts []string) {
	queries, ok := h.store.(interface {
		ListRequests(context.Context, model.StatsFilter) ([]*model.Request, error)
		GetRequest(context.Context, string) (*model.Request, error)
		ListAttempts(context.Context, string) ([]*model.Attempt, error)
	})
	if !ok {
		writeAdminUnsupported(w)
		return
	}
	if len(parts) == 1 && r.Method == http.MethodGet {
		request, err := queries.GetRequest(r.Context(), parts[0])
		if err != nil {
			writeAdminStoreError(w, err)
			return
		}
		attempts, err := queries.ListAttempts(r.Context(), parts[0])
		if err != nil {
			writeAdminStoreError(w, err)
			return
		}
		var ledger []*model.LedgerEntry
		if ledgerStore, ok := h.store.(interface {
			ListLedgerEntries(context.Context, string) ([]*model.LedgerEntry, error)
		}); ok {
			ledger, err = ledgerStore.ListLedgerEntries(r.Context(), parts[0])
			if err != nil {
				writeAdminStoreError(w, err)
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"request": request, "attempts": attempts, "ledger": ledger})
		return
	}
	if len(parts) != 0 || r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed", "method not allowed")
		return
	}
	filter, err := requestFilterFromQuery(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_filter", err.Error())
		return
	}
	items, err := queries.ListRequests(r.Context(), filter)
	if err != nil {
		writeAdminStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (h *adminHandler) audit(w http.ResponseWriter, r *http.Request, parts []string) {
	queries, ok := h.store.(interface {
		ListAuditFiltered(context.Context, model.AuditFilter) ([]*model.AuditEntry, error)
	})
	if !ok || len(parts) != 0 || r.Method != http.MethodGet {
		if !ok {
			writeAdminUnsupported(w)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed", "method not allowed")
		}
		return
	}
	filter, err := auditFilterFromQuery(r)
	if err != nil {
		writeError(w, 400, "invalid_request_error", "invalid_filter", err.Error())
		return
	}
	items, err := queries.ListAuditFiltered(r.Context(), filter)
	if err != nil {
		writeAdminStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (h *adminHandler) stats(w http.ResponseWriter, r *http.Request, parts []string) {
	queries, ok := h.store.(interface {
		Stats(context.Context, model.StatsFilter) (*model.StatsSummary, error)
	})
	if !ok || len(parts) != 0 || r.Method != http.MethodGet {
		if !ok {
			writeAdminUnsupported(w)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed", "method not allowed")
		}
		return
	}
	filter, err := requestFilterFromQuery(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_filter", err.Error())
		return
	}
	value, err := queries.Stats(r.Context(), filter)
	if err != nil {
		writeAdminStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (h *adminHandler) content(w http.ResponseWriter, r *http.Request, parts []string) {
	if r.Method != http.MethodGet || len(parts) != 1 {
		writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed", "method not allowed")
		return
	}
	if h.cipher == nil {
		writeError(w, http.StatusServiceUnavailable, "server_error", "encryption_unavailable", "content decryption is unavailable")
		return
	}
	contentStore, ok := h.store.(interface {
		GetContentLog(context.Context, string) (*model.ContentLog, error)
	})
	if !ok {
		writeAdminUnsupported(w)
		return
	}
	log, err := contentStore.GetContentLog(r.Context(), parts[0])
	if err != nil {
		writeAdminStoreError(w, err)
		return
	}
	prompt, err := h.cipher.Decrypt(string(log.Prompt))
	if err != nil && len(log.Prompt) > 0 {
		writeError(w, http.StatusInternalServerError, "server_error", "content_decrypt_failed", "could not decrypt request content")
		return
	}
	response, err := h.cipher.Decrypt(string(log.Response))
	if err != nil && len(log.Response) > 0 {
		writeError(w, http.StatusInternalServerError, "server_error", "content_decrypt_failed", "could not decrypt response content")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"request_id": log.RequestID, "prompt": prompt, "response": response, "truncated": log.Truncated, "created_at": log.CreatedAt})
}

func decodeAdminJSON(w http.ResponseWriter, r *http.Request, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAdminBodyBytes))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("request body must contain one JSON value")
	}
	return nil
}

func parseAdminID(value string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("invalid id")
	}
	return id, nil
}

func parseQueryID(r *http.Request, name string) (int64, error) {
	value := r.URL.Query().Get(name)
	if value == "" {
		return 0, errors.New("missing id")
	}
	return parseAdminID(value)
}

func requestFilterFromQuery(r *http.Request) (model.StatsFilter, error) {
	filter := model.StatsFilter{ModelAlias: strings.TrimSpace(r.URL.Query().Get("model")), API: strings.TrimSpace(r.URL.Query().Get("api")), Limit: 50}
	filter.RequestID = strings.TrimSpace(r.URL.Query().Get("request_id"))
	filter.SettlementStatus = strings.TrimSpace(r.URL.Query().Get("settlement_status"))
	if filter.SettlementStatus != "" {
		switch filter.SettlementStatus {
		case "settled", "free", "credits_free", "unpriced", "failed", "none":
		default:
			return filter, errors.New("invalid settlement_status")
		}
	}
	if value := r.URL.Query().Get("model_group_id"); value != "" {
		id, err := parseAdminID(value)
		if err != nil {
			return filter, errors.New("invalid model_group_id")
		}
		filter.ModelGroupID = id
	}
	var err error
	if value := r.URL.Query().Get("user_id"); value != "" {
		filter.UserID, err = parseAdminID(value)
		if err != nil {
			return filter, errors.New("invalid user_id")
		}
	}
	if value := r.URL.Query().Get("provider_id"); value != "" {
		filter.ProviderID, err = parseAdminID(value)
		if err != nil {
			return filter, errors.New("invalid provider_id")
		}
	}
	filter.ProviderModel = strings.TrimSpace(r.URL.Query().Get("provider_model"))
	if value := r.URL.Query().Get("api_key_id"); value != "" {
		filter.APIKeyID, err = parseAdminID(value)
		if err != nil {
			return filter, errors.New("invalid api_key_id")
		}
	}
	if value := r.URL.Query().Get("status"); value != "" {
		filter.Status = model.RequestStatus(value)
		if filter.Status != model.StatusSuccess && filter.Status != model.StatusError {
			return filter, errors.New("invalid status")
		}
	}
	if value := r.URL.Query().Get("limit"); value != "" {
		filter.Limit, err = strconv.Atoi(value)
		if err != nil {
			return filter, errors.New("invalid limit")
		}
	}
	if value := r.URL.Query().Get("offset"); value != "" {
		filter.Offset, err = strconv.Atoi(value)
		if err != nil {
			return filter, errors.New("invalid offset")
		}
	}
	if value := r.URL.Query().Get("from"); value != "" {
		filter.From, err = time.Parse(time.RFC3339, value)
		if err != nil {
			return filter, errors.New("invalid from")
		}
	}
	if value := r.URL.Query().Get("to"); value != "" {
		filter.To, err = time.Parse(time.RFC3339, value)
		if err != nil {
			return filter, errors.New("invalid to")
		}
	}
	if filter.Limit < 1 || filter.Limit > 200 || filter.Offset < 0 {
		return filter, errors.New("limit must be 1..200 and offset nonnegative")
	}
	if !filter.From.IsZero() {
		filter.From = filter.From.UTC()
	}
	if !filter.To.IsZero() {
		filter.To = filter.To.UTC()
	}
	if !filter.From.IsZero() && !filter.To.IsZero() && filter.From.After(filter.To) {
		return filter, errors.New("from must not be after to")
	}
	return filter, nil
}

func writeAdminUnsupported(w http.ResponseWriter) {
	writeError(w, http.StatusNotImplemented, "server_error", "admin_store_unsupported", "administrator store operation is not configured")
}

func writeAdminStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrAliasNotReady) {
		writeError(w, 400, "invalid_request_error", "alias_not_ready", "at least one enabled route must point to an existing enabled provider")
		return
	}
	if errors.Is(err, store.ErrPriceConflict) {
		writeError(w, 409, "invalid_request_error", "price_version_conflict", "this model and version already exists; price versions are immutable")
		return
	}
	if errors.Is(err, store.ErrKeyOwnerImmutable) {
		writeError(w, 400, "invalid_request_error", "key_owner_immutable", "API key ownership cannot be changed")
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "invalid_request_error", "not_found", "resource not found")
		return
	}
	writeError(w, http.StatusInternalServerError, "server_error", "admin_store_error", "administrator store operation failed")
}

func maxInt64(value, minimum int64) int64 {
	if value < minimum {
		return minimum
	}
	return value
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
