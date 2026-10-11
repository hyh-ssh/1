package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all runtime configuration sourced from environment variables.
type Config struct {
	ListenAddr string
	DBURL      string

	// MasterKey is the AES-256-GCM key used to encrypt provider credentials
	// and (optionally) request/response content bodies at rest.
	MasterKey          string
	PreviousMasterKeys []string

	// InitialAdmin is used to bootstrap the first admin account on first run.
	InitialAdminUser     string
	InitialAdminPassword string

	// BusinessTimezone is the IANA timezone used for daily/monthly billing
	// boundaries and dashboard display. Stored data is always UTC.
	BusinessTimezone string

	// ContentLoggingEnabled is the single global switch for persisting
	// prompt/response bodies. Defaults to false.
	ContentLoggingEnabled bool

	// ContentLogMaxBytes caps a single request/response body persisted size.
	ContentLogMaxBytes int

	// LogRetentionDays is informational; V1 keeps all data by default.
	LogRetentionDays int

	// ShutdownTimeout bounds graceful shutdown.
	ShutdownTimeout time.Duration

	// SessionTTL controls the lifetime of an administrator session cookie.
	SessionTTL time.Duration

	// CookieSecure marks the session cookie Secure. Keep false for local HTTP
	// development and enable it whenever the gateway is served over HTTPS.
	CookieSecure          bool
	AllowPrivateUpstreams bool

	// OIDC enables one configurable OpenID Connect provider. It is considered
	// disabled unless issuer, client ID, client secret and redirect URL are all
	// present; partial configuration must not prevent password-only startup.
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCRedirectURL  string
	OIDCDisplayName  string
}

// Load reads configuration from the environment, applying defaults.
func Load() (*Config, error) {
	c := &Config{
		ListenAddr:            getenv("GATEWAY_LISTEN_ADDR", ":8080"),
		DBURL:                 getenv("GATEWAY_DB_URL", "postgres://localhost:5432/llm_gateway?sslmode=disable"),
		MasterKey:             os.Getenv("GATEWAY_MASTER_KEY"),
		InitialAdminUser:      getenv("GATEWAY_ADMIN_USER", "admin"),
		InitialAdminPassword:  os.Getenv("GATEWAY_ADMIN_PASSWORD"),
		BusinessTimezone:      getenv("GATEWAY_BUSINESS_TZ", "Asia/Shanghai"),
		ContentLoggingEnabled: getenvBool("GATEWAY_CONTENT_LOGGING", false),
		ContentLogMaxBytes:    getenvInt("GATEWAY_CONTENT_LOG_MAX_BYTES", 1<<20), // 1 MiB
		LogRetentionDays:      getenvInt("GATEWAY_LOG_RETENTION_DAYS", 0),        // 0 = keep forever
		ShutdownTimeout:       getenvDuration("GATEWAY_SHUTDOWN_TIMEOUT", 10*time.Second),
		SessionTTL:            getenvDuration("GATEWAY_SESSION_TTL", 24*time.Hour),
		CookieSecure:          getenvBool("GATEWAY_COOKIE_SECURE", false),
		AllowPrivateUpstreams: getenvBool("GATEWAY_ALLOW_PRIVATE_UPSTREAMS", false),
		OIDCIssuer:            strings.TrimSpace(os.Getenv("GATEWAY_OIDC_ISSUER")),
		OIDCClientID:          strings.TrimSpace(os.Getenv("GATEWAY_OIDC_CLIENT_ID")),
		OIDCClientSecret:      os.Getenv("GATEWAY_OIDC_CLIENT_SECRET"),
		OIDCRedirectURL:       strings.TrimSpace(os.Getenv("GATEWAY_OIDC_REDIRECT_URL")),
		OIDCDisplayName:       getenv("GATEWAY_OIDC_DISPLAY_NAME", "企业账号"),
	}

	if c.MasterKey == "" {
		return nil, fmt.Errorf("GATEWAY_MASTER_KEY is required (32-byte base64 or hex key for AES-256-GCM)")
	}
	if old := strings.TrimSpace(os.Getenv("GATEWAY_PREVIOUS_MASTER_KEYS")); old != "" {
		c.PreviousMasterKeys = strings.Split(old, ",")
	}
	if _, err := time.LoadLocation(c.BusinessTimezone); err != nil {
		return nil, fmt.Errorf("invalid GATEWAY_BUSINESS_TZ %q: %w", c.BusinessTimezone, err)
	}
	return c, nil
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
func getenvDuration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return def
	}
	return d
}

func getenvBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func getenvInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
