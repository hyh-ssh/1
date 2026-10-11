package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	gatewaycrypto "github.com/zjvtit/omni-gateway/internal/crypto"
	"github.com/zjvtit/omni-gateway/internal/store"
)

// countingOIDCProviderStore wraps the real store and counts the reads the
// source performs, which is the observable proof that a cache hit skipped the
// database.
type countingOIDCProviderStore struct {
	store *store.Store
	reads int64
}

func (s *countingOIDCProviderStore) GetOIDCProvider(ctx context.Context) (store.OIDCProvider, bool, error) {
	s.reads++
	return s.store.GetOIDCProvider(ctx)
}

// TestOIDCProviderSourceDecryptsCachesAndInvalidates covers the runtime read:
// the stored ciphertext is decrypted into the plaintext the code exchange needs,
// reads are served from the cache, and only Invalidate makes a saved change
// visible.
func TestOIDCProviderSourceDecryptsCachesAndInvalidates(t *testing.T) {
	ctx := context.Background()
	s := oidcProviderAdminTestStore(t)
	cipher, err := gatewaycrypto.NewCipher(strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	firstCiphertext, err := cipher.Encrypt("s3cret-value")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveOIDCProvider(ctx, store.OIDCProvider{
		Issuer:                 "https://idp.example",
		ClientID:               "client-1",
		ClientSecretCiphertext: firstCiphertext,
		RedirectURL:            "https://gw.example/api/auth/oidc/callback",
		DisplayName:            "Corp SSO",
		Enabled:                true,
	}, 0); err != nil {
		t.Fatal(err)
	}

	counter := &countingOIDCProviderStore{store: s}
	source := newOIDCProviderSource(counter, cipher.Decrypt)

	first, err := source.Provider(ctx)
	if err != nil {
		t.Fatalf("Provider after save: %v", err)
	}
	if !first.Enabled || !first.ready() {
		t.Fatalf("config = %+v, want an enabled ready provider", first)
	}
	if first.Issuer != "https://idp.example" || first.ClientID != "client-1" ||
		first.RedirectURL != "https://gw.example/api/auth/oidc/callback" || first.DisplayName != "Corp SSO" {
		t.Fatalf("config = %+v, want the stored fields", first)
	}
	if first.ClientSecret != "s3cret-value" {
		t.Fatalf("ClientSecret = %q, want the decrypted plaintext", first.ClientSecret)
	}
	if counter.reads != 1 || source.loadCount() != 1 {
		t.Fatalf("reads after the first load = %d/%d, want 1", counter.reads, source.loadCount())
	}

	// A second call is a cache hit: the store is not touched again.
	second, err := source.Provider(ctx)
	if err != nil || second != first {
		t.Fatalf("cached Provider = %+v err=%v, want the first result unchanged", second, err)
	}
	if counter.reads != 1 {
		t.Fatalf("store reads after a cache hit = %d, want 1", counter.reads)
	}

	// A save behind the source's back is invisible until it is invalidated.
	rotatedCiphertext, err := cipher.Encrypt("rotated-secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveOIDCProvider(ctx, store.OIDCProvider{
		Issuer:                 "https://idp.changed",
		ClientID:               "client-2",
		ClientSecretCiphertext: rotatedCiphertext,
		RedirectURL:            "https://gw.example/api/auth/oidc/callback",
		DisplayName:            "Corp SSO",
		Enabled:                true,
	}, 0); err != nil {
		t.Fatal(err)
	}
	stale, err := source.Provider(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stale.Issuer != "https://idp.example" || stale.ClientSecret != "s3cret-value" {
		t.Fatalf("Provider without Invalidate = %+v, want the cached values", stale)
	}
	if counter.reads != 1 {
		t.Fatalf("store reads before Invalidate = %d, want 1", counter.reads)
	}

	source.Invalidate()
	fresh, err := source.Provider(ctx)
	if err != nil {
		t.Fatalf("Provider after Invalidate: %v", err)
	}
	if fresh.Issuer != "https://idp.changed" || fresh.ClientID != "client-2" || fresh.ClientSecret != "rotated-secret" {
		t.Fatalf("Provider after Invalidate = %+v, want the saved values", fresh)
	}
	if counter.reads != 2 || source.loadCount() != 2 {
		t.Fatalf("reads after Invalidate = %d/%d, want 2", counter.reads, source.loadCount())
	}

	// Clearing and invalidating must surface as the "not configured" state, not
	// as an error and not as the previous configuration.
	if err := s.ClearOIDCProvider(ctx, 0); err != nil {
		t.Fatal(err)
	}
	source.Invalidate()
	cleared, err := source.Provider(ctx)
	if err != nil {
		t.Fatalf("Provider after clear: %v", err)
	}
	if cleared != (oidcProviderConfig{}) || cleared.ready() {
		t.Fatalf("Provider after clear = %+v, want the zero value", cleared)
	}
}

// stubOIDCProviderStore is a scriptable store for the failure paths, where a
// real outage cannot be produced on demand.
type stubOIDCProviderStore struct {
	provider store.OIDCProvider
	exists   bool
	err      error
	reads    int64
}

func (s *stubOIDCProviderStore) GetOIDCProvider(context.Context) (store.OIDCProvider, bool, error) {
	s.reads++
	if s.err != nil {
		return store.OIDCProvider{}, false, s.err
	}
	return s.provider, s.exists, nil
}

// TestOIDCProviderSourceNeverCachesFailures covers the cache contract on the
// unhappy paths: a missing row is a cacheable "not configured" state, while a
// read or decrypt failure is returned every time until it clears, so a transient
// database outage cannot lock the gateway.
func TestOIDCProviderSourceNeverCachesFailures(t *testing.T) {
	ctx := context.Background()

	// No row: (zero value, nil) and the "not configured" answer is cacheable.
	missing := &stubOIDCProviderStore{}
	source := newOIDCProviderSource(missing, nil)
	config, err := source.Provider(ctx)
	if err != nil || config != (oidcProviderConfig{}) {
		t.Fatalf("unconfigured Provider = %+v err=%v, want the zero value and no error", config, err)
	}
	if _, err := source.Provider(ctx); err != nil {
		t.Fatal(err)
	}
	if missing.reads != 1 {
		t.Fatalf("store reads for a cached unconfigured state = %d, want 1", missing.reads)
	}

	// A read failure is reported and never cached: the next call retries.
	outage := &stubOIDCProviderStore{err: errors.New("connection refused")}
	failing := newOIDCProviderSource(outage, nil)
	for attempt := 1; attempt <= 2; attempt++ {
		if _, err := failing.Provider(ctx); err == nil || !strings.Contains(err.Error(), "connection refused") {
			t.Fatalf("attempt %d: err=%v, want the store failure", attempt, err)
		}
	}
	if outage.reads != 2 {
		t.Fatalf("store reads during an outage = %d, want 2 (the failure must not be cached)", outage.reads)
	}

	// Recovery needs no Invalidate: the failure left nothing behind.
	outage.err = nil
	outage.exists = true
	outage.provider = store.OIDCProvider{Issuer: "https://idp.example", ClientID: "client-1", RedirectURL: "https://gw.example/cb", Enabled: true}
	recovered, err := failing.Provider(ctx)
	if err != nil || recovered.Issuer != "https://idp.example" {
		t.Fatalf("Provider after recovery = %+v err=%v, want the stored row", recovered, err)
	}

	// A decrypt failure behaves the same way and never echoes the ciphertext.
	broken := &stubOIDCProviderStore{
		exists:   true,
		provider: store.OIDCProvider{Issuer: "https://idp.example", ClientSecretCiphertext: "ciphertext-material", Enabled: true},
	}
	undecryptable := newOIDCProviderSource(broken, func(string) (string, error) { return "", errors.New("ciphertext authentication failed") })
	for attempt := 1; attempt <= 2; attempt++ {
		_, err := undecryptable.Provider(ctx)
		if err == nil || !strings.Contains(err.Error(), "decrypt oidc provider client secret") {
			t.Fatalf("attempt %d: err=%v, want a decrypt failure", attempt, err)
		}
		if strings.Contains(err.Error(), "ciphertext-material") {
			t.Fatalf("decrypt error leaked the stored ciphertext: %v", err)
		}
	}
	if broken.reads != 2 {
		t.Fatalf("store reads after a decrypt failure = %d, want 2 (the failure must not be cached)", broken.reads)
	}

	// A stored secret without a cipher is a loud configuration error, never a
	// silent empty secret.
	noCipher := newOIDCProviderSource(&stubOIDCProviderStore{exists: true, provider: store.OIDCProvider{ClientSecretCiphertext: "ciphertext-material"}}, nil)
	if _, err := noCipher.Provider(ctx); err == nil || !strings.Contains(err.Error(), "no cipher") {
		t.Fatalf("Provider without a cipher = %v, want a configuration error", err)
	}

	// A source without a store is also an error rather than a panic.
	if _, err := newOIDCProviderSource(nil, nil).Provider(ctx); err == nil {
		t.Fatal("Provider without a store returned no error")
	}

	// Nil sources are what handlers built without a provider look like; they
	// must be inert instead of panicking.
	var nilSource *oidcProviderSource
	if config, err := nilSource.Provider(ctx); err != nil || config != (oidcProviderConfig{}) {
		t.Fatalf("nil source Provider = %+v err=%v, want the zero value", config, err)
	}
	nilSource.Invalidate()
	if nilSource.loadCount() != 0 {
		t.Fatal("nil source reported reads")
	}
}

// TestAdminOIDCProviderWritesInvalidateRuntimeSource covers the wiring: a PUT
// and a DELETE through the administrator API make the next Provider call see
// the new configuration, while a write made behind the API's back stays
// invisible until the cache is invalidated.
func TestAdminOIDCProviderWritesInvalidateRuntimeSource(t *testing.T) {
	ctx := context.Background()
	s := oidcProviderAdminTestStore(t)
	counter := &countingOIDCProviderStore{store: s}
	cipher, err := gatewaycrypto.NewCipher(strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	source := newOIDCProviderSource(counter, cipher.Decrypt)
	// The harness runs against the same schema the source reads through the
	// counter, so a write and a cached read observe the same row.
	h := newOIDCAdminHarnessOnStore(t, s, AuthConfig{OIDCProvider: source})

	unconfigured, err := source.Provider(ctx)
	if err != nil || unconfigured.Enabled {
		t.Fatalf("initial Provider = %+v err=%v, want disabled", unconfigured, err)
	}
	if counter.reads != 1 {
		t.Fatalf("store reads after the initial load = %d, want 1", counter.reads)
	}

	first := h.do(t, http.MethodPut, oidcProviderAdminPath,
		`{"enabled":true,"issuer":"https://idp.example","client_id":"client-1","client_secret":"s3cret-value","redirect_url":"https://gw.example/api/auth/oidc/callback","display_name":"Corp SSO"}`)
	if first.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", first.Code, first.Body.String())
	}
	afterPut, err := source.Provider(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !afterPut.ready() || afterPut.ClientSecret != "s3cret-value" || afterPut.Issuer != "https://idp.example" {
		t.Fatalf("Provider after PUT = %+v, want the saved provider with the decrypted secret", afterPut)
	}
	if counter.reads != 2 {
		t.Fatalf("store reads after PUT = %d, want 2 (PUT must invalidate)", counter.reads)
	}

	// Every save invalidates, so a rotated secret is visible immediately.
	second := h.do(t, http.MethodPut, oidcProviderAdminPath,
		`{"enabled":true,"issuer":"https://idp.example","client_id":"client-2","client_secret":"second-secret","redirect_url":"https://gw.example/api/auth/oidc/callback"}`)
	if second.Code != http.StatusOK {
		t.Fatalf("second PUT = %d %s", second.Code, second.Body.String())
	}
	afterRotate, err := source.Provider(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if afterRotate.ClientID != "client-2" || afterRotate.ClientSecret != "second-secret" {
		t.Fatalf("Provider after the second PUT = %+v, want the rotated values", afterRotate)
	}
	if counter.reads != 3 {
		t.Fatalf("store reads after the second PUT = %d, want 3", counter.reads)
	}

	// A write that bypasses the API does not invalidate; only the API contract
	// is claimed here.
	if err := counter.store.SaveOIDCProvider(ctx, store.OIDCProvider{
		Issuer:      "https://idp.bypassed",
		ClientID:    "client-3",
		RedirectURL: "https://gw.example/api/auth/oidc/callback",
		Enabled:     true,
	}, 0); err != nil {
		t.Fatal(err)
	}
	bypassed, err := source.Provider(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if bypassed.Issuer != "https://idp.example" || counter.reads != 3 {
		t.Fatalf("Provider after a bypassing write = %+v (reads %d), want the cached values", bypassed, counter.reads)
	}

	deleted := h.do(t, http.MethodDelete, oidcProviderAdminPath, "")
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d %s, want 204", deleted.Code, deleted.Body.String())
	}
	afterDelete, err := source.Provider(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if afterDelete != (oidcProviderConfig{}) || afterDelete.ready() {
		t.Fatalf("Provider after DELETE = %+v, want the not-configured zero value", afterDelete)
	}
	if counter.reads != 4 {
		t.Fatalf("store reads after DELETE = %d, want 4 (DELETE must invalidate)", counter.reads)
	}
}
