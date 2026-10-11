package httpapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/zjvtit/omni-gateway/internal/store"
)

// oidcProviderConfig is the runtime view of the gateway's single primary OIDC
// provider, with the client secret already decrypted.
//
// The zero value - Enabled false and every other field empty - is the
// "not configured" state: the gateway has no provider to send users to.
// Provider reports that state as (oidcProviderConfig{}, nil) rather than as an
// error, because an unconfigured gateway is a normal state and not a failure.
// Callers that need "disabled" and "misconfigured" to be distinct use ready().
//
// ClientSecret is plaintext by design: it is consumed exclusively by the
// server-to-server code exchange and must never be logged or returned to a
// browser.
type oidcProviderConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	DisplayName  string
	Enabled      bool
}

// ready mirrors store.OIDCProvider.Ready: the provider is enabled and complete
// enough to start a login.
func (c oidcProviderConfig) ready() bool {
	return c.Enabled &&
		strings.TrimSpace(c.Issuer) != "" &&
		strings.TrimSpace(c.ClientID) != "" &&
		c.ClientSecret != "" &&
		strings.TrimSpace(c.RedirectURL) != ""
}

// oidcProviderSourceStore is the slice of the store the source reads. Naming it
// as an interface keeps the cache observable in tests without a database.
type oidcProviderSourceStore interface {
	GetOIDCProvider(context.Context) (store.OIDCProvider, bool, error)
}

// oidcProviderSource is the only place the runtime reads the primary OIDC
// provider configuration from. It decrypts the stored client secret and caches
// the result until Invalidate is called, so a login request never pays a
// database round trip for the configuration.
//
// Only successful reads are cached. A failed read - a transient database
// outage, an unreadable secret - returns the error without touching the cache,
// so one bad moment cannot lock the gateway into a permanent failure.
//
// The type never logs anything, which is what keeps the decrypted secret out of
// log lines by construction.
type oidcProviderSource struct {
	mu       sync.Mutex
	store    oidcProviderSourceStore
	decrypt  func(string) (string, error)
	cached   oidcProviderConfig
	cachedOK bool
	reads    int64
}

// newOIDCProviderSource builds a source over the singleton provider row. The
// decrypt function is the master cipher's Decrypt, injected so the source does
// not depend on the cipher type itself.
func newOIDCProviderSource(db oidcProviderSourceStore, decrypt func(string) (string, error)) *oidcProviderSource {
	return &oidcProviderSource{store: db, decrypt: decrypt}
}

// NewOIDCProviderSource is the constructor cmd/server wires into
// AuthConfig.OIDCProvider. It is exported only because the server binary lives
// in another package; the type stays package-private so the runtime surface
// remains Provider / Invalidate.
func NewOIDCProviderSource(db oidcProviderSourceStore, decrypt func(string) (string, error)) *oidcProviderSource {
	return newOIDCProviderSource(db, decrypt)
}

// Provider returns the current provider configuration, loading and decrypting
// it on a cache miss. The zero value with Enabled false means "no primary OIDC
// provider is configured"; errors are reserved for real failures and are never
// cached.
func (s *oidcProviderSource) Provider(ctx context.Context) (oidcProviderConfig, error) {
	if s == nil {
		return oidcProviderConfig{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cachedOK {
		return s.cached, nil
	}
	config, err := s.load(ctx)
	if err != nil {
		return oidcProviderConfig{}, err
	}
	s.cached = config
	s.cachedOK = true
	return config, nil
}

// load performs the uncached read and decryption. The caller holds s.mu.
func (s *oidcProviderSource) load(ctx context.Context) (oidcProviderConfig, error) {
	if s.store == nil {
		return oidcProviderConfig{}, errors.New("oidc provider source has no store")
	}
	s.reads++
	provider, ok, err := s.store.GetOIDCProvider(ctx)
	if err != nil {
		return oidcProviderConfig{}, fmt.Errorf("load oidc provider: %w", err)
	}
	if !ok {
		// No row yet: a normal, cacheable "not configured" state.
		return oidcProviderConfig{}, nil
	}
	config := oidcProviderConfig{
		Issuer:      provider.Issuer,
		ClientID:    provider.ClientID,
		RedirectURL: provider.RedirectURL,
		DisplayName: provider.DisplayName,
		Enabled:     provider.Enabled,
	}
	if provider.ClientSecretCiphertext != "" {
		if s.decrypt == nil {
			return oidcProviderConfig{}, errors.New("oidc provider client secret is stored but no cipher is configured")
		}
		secret, err := s.decrypt(provider.ClientSecretCiphertext)
		if err != nil {
			// The wrapped error carries no secret material; Decrypt only
			// reports that authentication failed.
			return oidcProviderConfig{}, fmt.Errorf("decrypt oidc provider client secret: %w", err)
		}
		config.ClientSecret = secret
	}
	return config, nil
}

// Invalidate drops the cache so the next Provider call reads the row again.
// The administrator endpoints call it after a successful save or clear, which
// is what makes a console change take effect without a restart. It is safe to
// call on a nil source, which is how handlers built without a source behave.
func (s *oidcProviderSource) Invalidate() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.cached = oidcProviderConfig{}
	s.cachedOK = false
	s.mu.Unlock()
}

// loadCount reports how many store reads this source has performed. Tests use
// it as the observable proof that a cache hit skipped the database.
func (s *oidcProviderSource) loadCount() int64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}
