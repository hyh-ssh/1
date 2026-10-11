package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zjvtit/omni-gateway/internal/config"
	gatewaycrypto "github.com/zjvtit/omni-gateway/internal/crypto"
	"github.com/zjvtit/omni-gateway/internal/store"
)

// oidcSeedTestStore provisions a migrated store on the isolated schema helper
// used by the other server integration tests.
func oidcSeedTestStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	s, err := store.New(ctx, serverTestDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

// TestSeedOIDCProviderFromEnvImportsOnceAndNeverOverwrites covers the startup
// seed end to end: GATEWAY_OIDC_* is encrypted and written while the row is
// missing, and from then on - after an administrator edits it or deliberately
// clears it - the environment never wins again.
func TestSeedOIDCProviderFromEnvImportsOnceAndNeverOverwrites(t *testing.T) {
	ctx := context.Background()
	s := oidcSeedTestStore(t)
	cipher, err := gatewaycrypto.NewCipher(masterKeyHex)
	if err != nil {
		t.Fatal(err)
	}

	// The values come from the real environment variables the deployment sets,
	// including the padding that config.Load trims.
	t.Setenv("GATEWAY_MASTER_KEY", masterKeyHex)
	t.Setenv("GATEWAY_OIDC_ISSUER", " https://idp.example ")
	t.Setenv("GATEWAY_OIDC_CLIENT_ID", " client-1 ")
	t.Setenv("GATEWAY_OIDC_CLIENT_SECRET", "s3cret-value")
	t.Setenv("GATEWAY_OIDC_REDIRECT_URL", " https://gw.example/api/auth/oidc/callback ")
	t.Setenv("GATEWAY_OIDC_DISPLAY_NAME", "Corp SSO")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}

	seeded, err := seedOIDCProviderFromEnv(ctx, s, cipher, cfg)
	if err != nil || !seeded {
		t.Fatalf("first seed: seeded=%v err=%v, want the row inserted", seeded, err)
	}
	provider, ok, err := s.GetOIDCProvider(ctx)
	if err != nil || !ok {
		t.Fatalf("row after seed: ok=%v err=%v", ok, err)
	}
	if !provider.Enabled || !provider.Ready() || !provider.EnvSeedApplied {
		t.Fatalf("seeded provider = %+v, want enabled, ready and env_seed_applied", provider)
	}
	if provider.Issuer != "https://idp.example" || provider.ClientID != "client-1" ||
		provider.RedirectURL != "https://gw.example/api/auth/oidc/callback" || provider.DisplayName != "Corp SSO" {
		t.Fatalf("seeded provider = %+v, want the trimmed environment values", provider)
	}
	if provider.ClientSecretCiphertext == "" || provider.ClientSecretCiphertext == "s3cret-value" ||
		strings.Contains(provider.ClientSecretCiphertext, "s3cret-value") {
		t.Fatalf("client secret was not encrypted at rest: %q", provider.ClientSecretCiphertext)
	}
	decrypted, err := cipher.Decrypt(provider.ClientSecretCiphertext)
	if err != nil || decrypted != "s3cret-value" {
		t.Fatalf("decrypted seeded secret = %q err=%v, want the environment value", decrypted, err)
	}

	// A restart re-runs the seed against the same row: a no-op that does not
	// even touch updated_at.
	before := provider
	if seeded, err = seedOIDCProviderFromEnv(ctx, s, cipher, cfg); err != nil || seeded {
		t.Fatalf("second seed: seeded=%v err=%v, want a no-op", seeded, err)
	}
	provider, _, err = s.GetOIDCProvider(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if provider != before {
		t.Fatalf("the second seed changed the row:\nbefore %+v\nafter  %+v", before, provider)
	}

	// An administrator replaces the configuration and the secret.
	adminCiphertext, err := cipher.Encrypt("admin-secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveOIDCProvider(ctx, store.OIDCProvider{
		Issuer:                 "https://idp.admin",
		ClientID:               "admin-client",
		ClientSecretCiphertext: adminCiphertext,
		RedirectURL:            "https://gw.example/api/auth/oidc/callback",
		DisplayName:            "Admin SSO",
		Enabled:                true,
	}, 0); err != nil {
		t.Fatal(err)
	}
	if seeded, err = seedOIDCProviderFromEnv(ctx, s, cipher, cfg); err != nil || seeded {
		t.Fatalf("seed after an administrator save: seeded=%v err=%v, want a no-op", seeded, err)
	}
	provider, _, err = s.GetOIDCProvider(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if provider.Issuer != "https://idp.admin" || provider.ClientID != "admin-client" || provider.ClientSecretCiphertext != adminCiphertext {
		t.Fatalf("seed overwrote the administrator values: %+v", provider)
	}

	// The administrator clears the provider. The row survives, so a restart
	// must not revive the environment values.
	if err := s.ClearOIDCProvider(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if seeded, err = seedOIDCProviderFromEnv(ctx, s, cipher, cfg); err != nil || seeded {
		t.Fatalf("seed after an administrator clear: seeded=%v err=%v, want a no-op", seeded, err)
	}
	provider, ok, err = s.GetOIDCProvider(ctx)
	if err != nil || !ok {
		t.Fatalf("row after clear and reseed: ok=%v err=%v, want the row kept", ok, err)
	}
	if provider.Enabled || provider.Issuer != "" || provider.ClientID != "" || provider.ClientSecretCiphertext != "" {
		t.Fatalf("the environment revived a cleared provider: %+v", provider)
	}
}

// recordingOIDCProviderSeeder captures the arguments the seed hands to the
// store, which is how the encryption-before-the-store guarantee is checked.
type recordingOIDCProviderSeeder struct {
	calls       int
	issuer      string
	clientID    string
	secret      string
	redirectURL string
	displayName string
	seeded      bool
	err         error
}

func (s *recordingOIDCProviderSeeder) SeedOIDCProviderFromEnv(_ context.Context, issuer, clientID, secret, redirectURL, displayName string) (bool, error) {
	s.calls++
	s.issuer, s.clientID, s.secret, s.redirectURL, s.displayName = issuer, clientID, secret, redirectURL, displayName
	return s.seeded, s.err
}

// TestSeedOIDCProviderFromEnvValidatesAndNeverLeaksTheSecret covers the seed's
// edge cases: an OIDC-free deployment stays untouched, a half-set environment
// is refused instead of partially imported, and no error ever echoes the
// client secret, because those errors are the only thing the server logs.
func TestSeedOIDCProviderFromEnvValidatesAndNeverLeaksTheSecret(t *testing.T) {
	ctx := context.Background()
	cipher, err := gatewaycrypto.NewCipher(masterKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	complete := func() *config.Config {
		return &config.Config{
			OIDCIssuer:       "https://idp.example",
			OIDCClientID:     "client-1",
			OIDCClientSecret: "top-secret",
			OIDCRedirectURL:  "https://gw.example/api/auth/oidc/callback",
		}
	}

	// No OIDC environment at all: nothing is written and no error is raised.
	none := &recordingOIDCProviderSeeder{}
	if seeded, err := seedOIDCProviderFromEnv(ctx, none, cipher, &config.Config{}); err != nil || seeded || none.calls != 0 {
		t.Fatalf("empty environment: seeded=%v err=%v calls=%d, want a silent no-op", seeded, err, none.calls)
	}

	// A partially set environment is refused as a whole, with the secret
	// nowhere in the message.
	partial := complete()
	partial.OIDCClientID = ""
	partialSeeder := &recordingOIDCProviderSeeder{}
	seeded, err := seedOIDCProviderFromEnv(ctx, partialSeeder, cipher, partial)
	if err == nil || seeded || partialSeeder.calls != 0 {
		t.Fatalf("incomplete environment: seeded=%v err=%v calls=%d, want a refusal without a write", seeded, err, partialSeeder.calls)
	}
	if !strings.Contains(err.Error(), "GATEWAY_OIDC_*") {
		t.Fatalf("incomplete environment error = %v, want it to name GATEWAY_OIDC_*", err)
	}
	if strings.Contains(err.Error(), "top-secret") {
		t.Fatalf("incomplete environment error leaked the secret: %v", err)
	}

	// The complete environment reaches the store with a ciphertext, never the
	// plaintext, and the store result is passed through.
	recorder := &recordingOIDCProviderSeeder{seeded: true}
	if seeded, err = seedOIDCProviderFromEnv(ctx, recorder, cipher, complete()); err != nil || !seeded {
		t.Fatalf("complete environment: seeded=%v err=%v, want the store result", seeded, err)
	}
	if recorder.calls != 1 {
		t.Fatalf("store calls = %d, want 1", recorder.calls)
	}
	if recorder.secret == "top-secret" || recorder.secret == "" || strings.Contains(recorder.secret, "top-secret") {
		t.Fatalf("the store received the plaintext secret: %q", recorder.secret)
	}
	if plain, err := cipher.Decrypt(recorder.secret); err != nil || plain != "top-secret" {
		t.Fatalf("the recorded ciphertext does not decrypt to the environment secret: %q err=%v", plain, err)
	}
	if recorder.issuer != "https://idp.example" || recorder.clientID != "client-1" || recorder.redirectURL != "https://gw.example/api/auth/oidc/callback" {
		t.Fatalf("recorded seed call = %+v, want the environment values", recorder)
	}

	// A store failure is reported verbatim and still never contains the secret.
	storeErr := errors.New("seed failed")
	failing := &recordingOIDCProviderSeeder{err: storeErr}
	if seeded, err = seedOIDCProviderFromEnv(ctx, failing, cipher, complete()); !errors.Is(err, storeErr) || seeded {
		t.Fatalf("store failure: seeded=%v err=%v, want %v", seeded, err, storeErr)
	}
	if strings.Contains(err.Error(), "top-secret") {
		t.Fatalf("store failure leaked the secret: %v", err)
	}

	// Missing inputs are refused loudly instead of seeding something partial.
	if _, err := seedOIDCProviderFromEnv(ctx, &recordingOIDCProviderSeeder{}, cipher, nil); err == nil {
		t.Fatal("nil configuration was accepted")
	}
	noCipher := &recordingOIDCProviderSeeder{}
	if seeded, err := seedOIDCProviderFromEnv(ctx, noCipher, nil, complete()); err == nil || seeded || noCipher.calls != 0 {
		t.Fatalf("missing cipher: seeded=%v err=%v calls=%d, want a refusal without a write", seeded, err, noCipher.calls)
	}
}
