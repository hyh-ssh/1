package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// OIDCProvider is the gateway's single primary OIDC provider, configured by an
// administrator from the console. Exactly one row can exist (id = 1), so this
// struct is the whole configuration surface.
//
// ClientSecretCiphertext is opaque here: the store never sees the client secret
// in the clear because the cipher lives with the caller (see RotateSecrets,
// which takes its cipher the same way). Callers encrypt before saving and
// decrypt after loading, and the plaintext never reaches a browser or a log.
type OIDCProvider struct {
	Issuer                 string
	ClientID               string
	ClientSecretCiphertext string
	RedirectURL            string
	DisplayName            string
	Enabled                bool
	EnvSeedApplied         bool
	UpdatedAt              time.Time
	UpdatedBy              int64
}

// Ready reports whether the provider is configured well enough to start a login.
func (p OIDCProvider) Ready() bool {
	return p.Enabled &&
		strings.TrimSpace(p.Issuer) != "" &&
		strings.TrimSpace(p.ClientID) != "" &&
		p.ClientSecretCiphertext != "" &&
		strings.TrimSpace(p.RedirectURL) != ""
}

const oidcProviderColumns = `issuer, client_id, client_secret_ciphertext, redirect_url, display_name, enabled, env_seed_applied, updated_at, updated_by`

// GetOIDCProvider loads the singleton row. The second result is false when the
// row does not exist yet, which is the state an unconfigured gateway starts in.
func (s *Store) GetOIDCProvider(ctx context.Context) (OIDCProvider, bool, error) {
	ctx = normalizeContext(ctx)
	var provider OIDCProvider
	var updatedBy *int64
	err := s.pool.QueryRow(ctx,
		`SELECT `+oidcProviderColumns+` FROM oidc_provider WHERE id=1`,
	).Scan(
		&provider.Issuer,
		&provider.ClientID,
		&provider.ClientSecretCiphertext,
		&provider.RedirectURL,
		&provider.DisplayName,
		&provider.Enabled,
		&provider.EnvSeedApplied,
		&provider.UpdatedAt,
		&updatedBy,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return OIDCProvider{}, false, nil
	}
	if err != nil {
		return OIDCProvider{}, false, fmt.Errorf("get oidc provider: %w", err)
	}
	if updatedBy != nil {
		provider.UpdatedBy = *updatedBy
	}
	return provider, true, nil
}

// SaveOIDCProvider upserts the singleton row. An empty ClientSecretCiphertext
// means "keep the stored secret", so a console form that never echoes the
// secret cannot wipe it by saving its other fields.
func (s *Store) SaveOIDCProvider(ctx context.Context, in OIDCProvider, updatedBy int64) error {
	ctx = normalizeContext(ctx)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO oidc_provider(id, issuer, client_id, client_secret_ciphertext, redirect_url, display_name, enabled, env_seed_applied, updated_at, updated_by)
		VALUES (1, $1, $2, $3, $4, $5, $6, $7, now(), $8)
		ON CONFLICT (id) DO UPDATE SET
			issuer = EXCLUDED.issuer,
			client_id = EXCLUDED.client_id,
			client_secret_ciphertext = CASE
				WHEN EXCLUDED.client_secret_ciphertext = '' THEN oidc_provider.client_secret_ciphertext
				ELSE EXCLUDED.client_secret_ciphertext
			END,
			redirect_url = EXCLUDED.redirect_url,
			display_name = EXCLUDED.display_name,
			enabled = EXCLUDED.enabled,
			env_seed_applied = oidc_provider.env_seed_applied OR EXCLUDED.env_seed_applied,
			updated_at = now(),
			updated_by = EXCLUDED.updated_by`,
		in.Issuer,
		in.ClientID,
		in.ClientSecretCiphertext,
		in.RedirectURL,
		in.DisplayName,
		in.Enabled,
		in.EnvSeedApplied,
		nullableUpdatedBy(updatedBy),
	)
	if err != nil {
		return fmt.Errorf("save oidc provider: %w", err)
	}
	return nil
}

// ClearOIDCProvider blanks the configuration and disables the provider. The row
// itself is kept on purpose: environment variables only seed a missing row, so
// keeping a blank one stops a restart from seeding the values back after an
// administrator deliberately turned the provider off.
func (s *Store) ClearOIDCProvider(ctx context.Context, updatedBy int64) error {
	ctx = normalizeContext(ctx)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO oidc_provider(id, issuer, client_id, client_secret_ciphertext, redirect_url, display_name, enabled, env_seed_applied, updated_at, updated_by)
		VALUES (1, '', '', '', '', '', false, true, now(), $1)
		ON CONFLICT (id) DO UPDATE SET
			issuer = '', client_id = '', client_secret_ciphertext = '', redirect_url = '', display_name = '',
			enabled = false, updated_at = now(), updated_by = EXCLUDED.updated_by`,
		nullableUpdatedBy(updatedBy),
	)
	if err != nil {
		return fmt.Errorf("clear oidc provider: %w", err)
	}
	return nil
}

// SeedOIDCProviderFromEnv imports the environment configuration exactly once.
// It inserts only when the singleton row is missing and every required value is
// present, and reports whether it actually seeded. An existing row - including
// one an administrator blanked - is never modified.
func (s *Store) SeedOIDCProviderFromEnv(ctx context.Context, issuer, clientID, clientSecretCiphertext, redirectURL, displayName string) (bool, error) {
	if strings.TrimSpace(issuer) == "" ||
		strings.TrimSpace(clientID) == "" ||
		clientSecretCiphertext == "" ||
		strings.TrimSpace(redirectURL) == "" {
		return false, nil
	}
	ctx = normalizeContext(ctx)
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO oidc_provider(id, issuer, client_id, client_secret_ciphertext, redirect_url, display_name, enabled, env_seed_applied, updated_at)
		SELECT 1, $1, $2, $3, $4, $5, true, true, now()
		WHERE NOT EXISTS (SELECT 1 FROM oidc_provider WHERE id=1)`,
		issuer, clientID, clientSecretCiphertext, redirectURL, displayName,
	)
	if err != nil {
		return false, fmt.Errorf("seed oidc provider from environment: %w", err)
	}
	affected, err := tag.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("seed oidc provider row count: %w", err)
	}
	return affected > 0, nil
}

// nullableUpdatedBy keeps a zero administrator id out of the foreign key.
func nullableUpdatedBy(userID int64) any {
	if userID <= 0 {
		return nil
	}
	return userID
}
