package model

import "time"

// Role is a user role.
type Role string

const (
	RoleAdmin Role = "admin"
	RoleUser  Role = "user"
)

// User is a business user (or admin) in the single organization.
type User struct {
	ID           int64  `json:"id"`
	Username     string `json:"username"`
	DisplayName  string `json:"display_name"`
	Role         Role   `json:"role"`
	PasswordHash string `json:"-"`
	Active       bool   `json:"active"`
	// PasswordLoginEnabled is false for accounts that OIDC provisioned: they
	// sign in through the provider until an administrator sets a password.
	PasswordLoginEnabled bool      `json:"password_login_enabled"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// APIKey is a gateway credential owned by a user.
type APIKey struct {
	ID         int64      `json:"id"`
	UserID     int64      `json:"user_id"`
	Name       string     `json:"name"`
	KeyPrefix  string     `json:"key_prefix"` // e.g. "sk-llm-"
	KeyHash    string     `json:"-"`          // SHA-256 of full key
	KeySuffix  string     `json:"key_suffix"` // last 4 chars for display
	Active     bool       `json:"active"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	ModelAllow []string   `json:"model_allow,omitempty"` // empty = all
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

// ProviderType is the upstream protocol adapter type.
type ProviderType string

const (
	ProviderOpenAICompat ProviderType = "openai_compatible"
	ProviderAnthropic    ProviderType = "anthropic"
	ProviderGemini       ProviderType = "gemini"
)

// ProviderCompatibilityProfile selects vendor-specific behavior for a protocol adapter.
type ProviderCompatibilityProfile string

const (
	ProviderCompatibilityGeneric  ProviderCompatibilityProfile = "generic"
	ProviderCompatibilityDeepSeek ProviderCompatibilityProfile = "deepseek"
)

// Provider is an upstream service, classified by interface type (not vendor).
type Provider struct {
	ID                   int64                        `json:"id"`
	Name                 string                       `json:"name"`
	Type                 ProviderType                 `json:"type"`
	CompatibilityProfile ProviderCompatibilityProfile `json:"compatibility_profile"`
	BaseURL              string                       `json:"base_url"`
	APIKeyEnc            string                       `json:"-"` // AES-GCM encrypted credential
	ExtraHeader          map[string]string            `json:"extra_headers,omitempty"`
	TimeoutMS            int                          `json:"timeout_ms"`
	MaxConcurrency       int                          `json:"max_concurrency"` // zero = unlimited
	SupportsResponses    bool                         `json:"supports_responses"`
	SuccessCount         int64                        `json:"success_count"`
	FailureCount         int64                        `json:"failure_count"`
	LastError            string                       `json:"last_error,omitempty"`
	Active               bool                         `json:"active"`
	HealthStatus         string                       `json:"health_status"` // available|degraded|cooldown
	CooldownUntil        *time.Time                   `json:"cooldown_until,omitempty"`
	LastVerifiedAt       *time.Time                   `json:"last_verified_at,omitempty"`
	CreatedAt            time.Time                    `json:"created_at"`
	UpdatedAt            time.Time                    `json:"updated_at"`
}

// Capabilities describes what a model supports.
type Capabilities struct {
	Streaming         bool `json:"streaming"`
	Multimodal        bool `json:"multimodal"`
	ToolCalling       bool `json:"tool_calling"`
	StructuredOutput  bool `json:"structured_output"`
	Reasoning         bool `json:"reasoning"`
	ResponsesState    bool `json:"responses_state"` // native stateful Responses support
	Embeddings        bool `json:"embeddings"`
	ImagesGenerations bool `json:"images_generations"`
}

// ProviderModel is an upstream model offered by a provider.
type ProviderModel struct {
	ID           int64        `json:"id"`
	ProviderID   int64        `json:"provider_id"`
	ModelID      string       `json:"model_id"` // upstream model identifier
	Capabilities Capabilities `json:"capabilities"`
	Active       bool         `json:"active"`
	CreatedAt    time.Time    `json:"created_at"`
}

// ModelAlias is the model name exposed to callers.
type ModelAlias struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
}

// RouteEntry is one ordered upstream target for an alias.
type RouteEntry struct {
	ID            int64  `json:"id"`
	AliasID       int64  `json:"alias_id"`
	ProviderID    int64  `json:"provider_id"`
	ProviderModel string `json:"provider_model"`
	Priority      int    `json:"priority"`
	Active        bool   `json:"active"`
}

// PriceVersion is an immutable price snapshot for a model.
type PriceVersion struct {
	ID        int64              `json:"id"`
	ModelID   string             `json:"model_id"` // alias name or provider model
	Version   int                `json:"version"`
	Cost      map[string]Money   `json:"cost"`    // per-meter upstream cost price, CNY per unit
	Credits   map[string]Credits `json:"credits"` // per-meter credit price, credits per unit
	CreatedAt time.Time          `json:"created_at"`
}

// BudgetRule is a rolling-window budget rule for a user.
type BudgetRule struct {
	ID          int64     `json:"id"`
	UserID      int64     `json:"user_id"`
	WindowSecs  int64     `json:"window_secs"`            // rolling window duration
	CostLimit   *Money    `json:"cost_limit,omitempty"`   // upstream spend cap, in CNY
	CreditLimit *Credits  `json:"credit_limit,omitempty"` // caller quota, in credits
	Active      bool      `json:"active"`
	CreatedAt   time.Time `json:"created_at"`
}

// RateLimit is a rate limit configuration.
type RateLimit struct {
	ID          int64  `json:"id"`
	Scope       string `json:"scope"` // "user" | "key" | "model"
	ScopeID     int64  `json:"scope_id"`
	ModelID     string `json:"model_id,omitempty"` // empty = all models
	RPM         int64  `json:"rpm"`
	TPM         int64  `json:"tpm"`
	Concurrency int64  `json:"concurrency"`
	Active      bool   `json:"active"`
}
