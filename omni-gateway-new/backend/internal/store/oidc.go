package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"

	gatewaycrypto "github.com/zjvtit/omni-gateway/internal/crypto"
	"github.com/zjvtit/omni-gateway/internal/model"
)

// GetUserByOIDC returns the local account bound to an exact issuer/subject
// pair. Email and username are intentionally not part of the lookup.
func (s *Store) GetUserByOIDC(ctx context.Context, issuer, subject string) (*model.User, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("store is not configured")
	}
	user := &model.User{}
	err := s.pool.QueryRow(ctx, `
		SELECT u.id, u.username, u.display_name, u.role, u.password_hash, u.active, u.password_login_enabled, u.created_at, u.updated_at
		FROM oidc_identities i JOIN users u ON u.id=i.user_id
		WHERE i.issuer=$1 AND i.subject=$2`, issuer, subject).Scan(
		&user.ID, &user.Username, &user.DisplayName, &user.Role, &user.PasswordHash,
		&user.Active, &user.PasswordLoginEnabled, &user.CreatedAt, &user.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	_, _ = s.pool.Exec(ctx, `UPDATE oidc_identities SET last_login_at=now() WHERE issuer=$1 AND subject=$2`, issuer, subject)
	return user, nil
}

// GetOrCreateOIDCUser resolves an OIDC identity and, when it is first seen,
// creates a normal active user and binds the identity in the same transaction.
// The new account cannot sign in with a password (password_login_enabled is
// FALSE) until an administrator sets one, and the store writes a syntactically
// valid Argon2id hash of a random, undisclosed placeholder so that refusal comes
// from the flag alone rather than from password verification rejecting a
// malformed hash. The identity table's primary key is the final concurrency
// guard: a racing first login never gets attached to a different local account.
func (s *Store) GetOrCreateOIDCUser(ctx context.Context, issuer, subject string, user *model.User) (*model.User, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("store is not configured")
	}
	if issuer == "" || subject == "" || user == nil {
		return nil, errors.New("OIDC identity is incomplete")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	resolved, found, err := tx.getOIDCUser(ctx, issuer, subject)
	if err != nil {
		return nil, err
	}
	if found {
		if _, err := tx.Exec(ctx, `UPDATE oidc_identities SET last_login_at=now() WHERE issuer=$1 AND subject=$2`, issuer, subject); err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return resolved, nil
	}

	// The placeholder credential is generated here instead of being accepted
	// from the caller: a well-formed Argon2id hash of a value nobody knows keeps
	// password verification on its normal path, so the password_login_enabled
	// check is the only thing refusing the login. A malformed hash would make
	// VerifyPassword return before running Argon2id, which both bypasses the
	// flag's cost and lets a caller tell OIDC-provisioned accounts apart by
	// response latency.
	placeholderHash, err := oidcPlaceholderPasswordHash()
	if err != nil {
		return nil, err
	}

	var id int64
	insertUser := func(username string) error {
		return tx.QueryRow(ctx, `
			INSERT INTO users(username, display_name, role, password_hash, active, password_login_enabled)
			VALUES($1,$2,$3,$4,$5,FALSE)
			ON CONFLICT (username) DO NOTHING
			RETURNING id`, username, user.DisplayName, string(model.RoleUser), placeholderHash, true).Scan(&id)
	}

	insertErr := insertUser(user.Username)
	if errors.Is(insertErr, sql.ErrNoRows) {
		// Two different situations produce a conflict.
		//
		// First, a concurrent first login for this same subject may have
		// inserted the row already; re-resolving after that conflicting insert
		// has completed returns the winner. An unrelated username collision is
		// never treated as an account-linking signal.
		resolved, found, getErr := tx.getOIDCUser(ctx, issuer, subject)
		if getErr != nil {
			return nil, getErr
		}
		if found {
			if err := tx.Commit(ctx); err != nil {
				return nil, err
			}
			return resolved, nil
		}
		// Otherwise the requested name is already held by a different account
		// (for example a locally created account whose username equals the
		// IdP's preferred_username). Keep the requested name as the readable
		// stem and append a deterministic suffix derived from the identity, so
		// the new account is always provisioned instead of failing the login.
		fallback := user.Username + "-" + oidcIdentitySuffix(issuer, subject)
		if err := insertUser(fallback); err != nil {
			return nil, err
		}
		user.Username = fallback
	} else if insertErr != nil {
		return nil, insertErr
	}
	user.ID = id
	user.Role = model.RoleUser
	user.Active = true
	user.PasswordHash = placeholderHash
	user.PasswordLoginEnabled = false

	var boundID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO oidc_identities(issuer, subject, user_id)
		VALUES($1,$2,$3)
		ON CONFLICT (issuer, subject) DO NOTHING
		RETURNING user_id`, issuer, subject, id).Scan(&boundID)
	if errors.Is(err, sql.ErrNoRows) {
		// Another transaction won the identity race. Remove only the user this
		// transaction just inserted, then return the winner after committing the
		// transaction so the identity is not lost.
		if _, deleteErr := tx.Exec(ctx, `DELETE FROM users WHERE id=$1`, id); deleteErr != nil {
			return nil, deleteErr
		}
		resolved, found, getErr := tx.getOIDCUser(ctx, issuer, subject)
		if getErr != nil {
			return nil, getErr
		}
		if !found {
			return nil, errors.New("OIDC identity race could not be resolved")
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return resolved, nil
	}
	if err != nil {
		return nil, err
	}
	if boundID != id {
		return nil, errors.New("OIDC identity binding mismatch")
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return user, nil
}

// oidcIdentitySuffix returns a short, deterministic token derived from the
// issuer/subject pair. It disambiguates a requested username that an unrelated
// account already holds, without introducing randomness: the same identity
// always resolves to the same suffixed name.
func oidcIdentitySuffix(issuer, subject string) string {
	sum := sha256.Sum256([]byte(issuer + "\x00" + subject))
	return hex.EncodeToString(sum[:])[:6]
}

// oidcPlaceholderPasswordHash returns a valid Argon2id encoding of a random
// value for a freshly provisioned OIDC account. The value is never revealed, so
// no submitted password can match it; the encoding is what keeps password
// verification on its normal Argon2id path for these accounts.
func oidcPlaceholderPasswordHash() (string, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", err
	}
	return gatewaycrypto.HashPassword(hex.EncodeToString(secret))
}

func (tx *xormTx) getOIDCUser(ctx context.Context, issuer, subject string) (*model.User, bool, error) {
	user := &model.User{}
	err := tx.QueryRow(ctx, `
		SELECT u.id, u.username, u.display_name, u.role, u.password_hash, u.active, u.password_login_enabled, u.created_at, u.updated_at
		FROM oidc_identities i JOIN users u ON u.id=i.user_id
		WHERE i.issuer=$1 AND i.subject=$2`, issuer, subject).Scan(
		&user.ID, &user.Username, &user.DisplayName, &user.Role, &user.PasswordHash,
		&user.Active, &user.PasswordLoginEnabled, &user.CreatedAt, &user.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return user, true, nil
}
