package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadDefaultsAndSecurityOptions(t *testing.T) {
	for _, key := range []string{"GATEWAY_MASTER_KEY", "GATEWAY_PREVIOUS_MASTER_KEYS", "GATEWAY_BUSINESS_TZ", "GATEWAY_LISTEN_ADDR", "GATEWAY_DB_URL", "GATEWAY_CONTENT_LOGGING", "GATEWAY_CONTENT_LOG_MAX_BYTES", "GATEWAY_LOG_RETENTION_DAYS", "GATEWAY_SHUTDOWN_TIMEOUT", "GATEWAY_SESSION_TTL", "GATEWAY_COOKIE_SECURE", "GATEWAY_ALLOW_PRIVATE_UPSTREAMS"} {
		t.Setenv(key, "")
	}
	if _, err := Load(); err == nil {
		t.Fatal("missing master key accepted")
	}
	t.Setenv("GATEWAY_MASTER_KEY", strings.Repeat("1", 64))
	cfg, err := Load()
	if err != nil || cfg.AllowPrivateUpstreams || cfg.ContentLoggingEnabled || cfg.SessionTTL != 24*time.Hour || cfg.ContentLogMaxBytes != 1<<20 {
		t.Fatalf("incorrect defaults: %+v %v", cfg, err)
	}
	t.Setenv("GATEWAY_PREVIOUS_MASTER_KEYS", strings.Repeat("2", 64)+","+strings.Repeat("3", 64))
	t.Setenv("GATEWAY_ALLOW_PRIVATE_UPSTREAMS", "true")
	t.Setenv("GATEWAY_COOKIE_SECURE", "true")
	t.Setenv("GATEWAY_CONTENT_LOG_MAX_BYTES", "2048")
	t.Setenv("GATEWAY_SESSION_TTL", "2h")
	cfg, err = Load()
	if err != nil || !cfg.AllowPrivateUpstreams || !cfg.CookieSecure || len(cfg.PreviousMasterKeys) != 2 || cfg.ContentLogMaxBytes != 2048 || cfg.SessionTTL != 2*time.Hour {
		t.Fatalf("options ignored: %+v %v", cfg, err)
	}
	t.Setenv("GATEWAY_ALLOW_PRIVATE_UPSTREAMS", "invalid")
	t.Setenv("GATEWAY_CONTENT_LOG_MAX_BYTES", "invalid")
	t.Setenv("GATEWAY_SESSION_TTL", "-1s")
	cfg, err = Load()
	if err != nil || cfg.AllowPrivateUpstreams || cfg.SessionTTL != 24*time.Hour || cfg.ContentLogMaxBytes != 1<<20 {
		t.Fatal("invalid options failed to default safely")
	}
	t.Setenv("GATEWAY_BUSINESS_TZ", "Not/AZone")
	if _, err := Load(); err == nil {
		t.Fatal("invalid timezone accepted")
	}
}
