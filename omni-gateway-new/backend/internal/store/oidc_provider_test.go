package store

import (
	"context"
	"testing"
)

// TestOIDCProviderSaveSeedAndClear covers the singleton configuration row: the
// environment seeds it exactly once, an empty secret preserves the stored one,
// and clearing it keeps the row so a restart cannot seed the values back.
func TestOIDCProviderSaveSeedAndClear(t *testing.T) {
	ctx := context.Background()
	s := groupTestStore(t)

	if _, ok, err := s.GetOIDCProvider(ctx); err != nil || ok {
		t.Fatalf("unconfigured gateway: ok=%v err=%v, want no row", ok, err)
	}

	seeded, err := s.SeedOIDCProviderFromEnv(ctx, "https://idp.example", "client-1", "ciphertext-1", "https://gw.example/api/auth/oidc/callback", "企业账号")
	if err != nil || !seeded {
		t.Fatalf("first seed: seeded=%v err=%v, want inserted", seeded, err)
	}
	provider, ok, err := s.GetOIDCProvider(ctx)
	if err != nil || !ok {
		t.Fatalf("get after seed: ok=%v err=%v", ok, err)
	}
	if !provider.Enabled || !provider.Ready() || provider.Issuer != "https://idp.example" || provider.ClientID != "client-1" || provider.ClientSecretCiphertext != "ciphertext-1" {
		t.Fatalf("seeded provider = %+v, want the environment values enabled and ready", provider)
	}

	// An administrator edits the row, leaving the secret field untouched.
	err = s.SaveOIDCProvider(ctx, OIDCProvider{
		Issuer:      "https://idp.changed",
		ClientID:    "client-2",
		RedirectURL: "https://gw.example/api/auth/oidc/callback",
		Enabled:     true,
	}, 0)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	provider, _, err = s.GetOIDCProvider(ctx)
	if err != nil {
		t.Fatalf("get after save: %v", err)
	}
	if provider.Issuer != "https://idp.changed" || provider.ClientID != "client-2" {
		t.Fatalf("saved provider = %+v, want the saved issuer and client id", provider)
	}
	if provider.ClientSecretCiphertext != "ciphertext-1" {
		t.Fatalf("empty secret wiped the stored one: %q", provider.ClientSecretCiphertext)
	}

	// Seeding again must not touch the row an administrator now owns.
	seeded, err = s.SeedOIDCProviderFromEnv(ctx, "https://idp.example", "client-1", "ciphertext-1", "https://gw.example/api/auth/oidc/callback", "企业账号")
	if err != nil || seeded {
		t.Fatalf("second seed: seeded=%v err=%v, want a no-op", seeded, err)
	}
	provider, _, err = s.GetOIDCProvider(ctx)
	if err != nil {
		t.Fatalf("get after second seed: %v", err)
	}
	if provider.Issuer != "https://idp.changed" || provider.ClientID != "client-2" {
		t.Fatalf("seed overwrote administrator values: %+v", provider)
	}

	// Clearing blanks and disables the row without deleting it.
	if err := s.ClearOIDCProvider(ctx, 0); err != nil {
		t.Fatalf("clear: %v", err)
	}
	provider, ok, err = s.GetOIDCProvider(ctx)
	if err != nil || !ok {
		t.Fatalf("get after clear: ok=%v err=%v, want the row to survive", ok, err)
	}
	if provider.Enabled || provider.Ready() || provider.ClientSecretCiphertext != "" {
		t.Fatalf("cleared provider = %+v, want disabled and blank", provider)
	}
	seeded, err = s.SeedOIDCProviderFromEnv(ctx, "https://idp.example", "client-1", "ciphertext-1", "https://gw.example/api/auth/oidc/callback", "企业账号")
	if err != nil || seeded {
		t.Fatalf("seed after clear: seeded=%v err=%v, want a no-op", seeded, err)
	}

	// Incomplete environment values never seed at all.
	if seeded, err = s.SeedOIDCProviderFromEnv(ctx, "https://idp.example", "", "ciphertext-1", "https://gw.example/api/auth/oidc/callback", "企业账号"); err != nil || seeded {
		t.Fatalf("seed with an incomplete environment: seeded=%v err=%v, want a no-op", seeded, err)
	}
}
