package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	gatewaycrypto "github.com/zjvtit/omni-gateway/internal/crypto"
	"github.com/zjvtit/omni-gateway/internal/model"
)

const (
	oidcPasswordLoginIssuer  = "https://issuer.example/oidc"
	oidcPasswordLoginSubject = "oidc-subject-1"

	oidcPlaceholderIssuer         = "https://issuer.example/oidc-placeholder"
	oidcPlaceholderSubject        = "oidc-placeholder-subject-1"
	oidcPlaceholderSecondSubject  = "oidc-placeholder-subject-2"
	oidcPlaceholderCallerSupplied = "caller-supplied-token-must-not-be-stored"
	oidcPlaceholderFirstUsername  = "oidc-placeholder"
	oidcPlaceholderSecondUsername = "oidc-placeholder-2"
)

// oidcPasswordLoginTestStore reuses the PostgreSQL harness from groups_test.go:
// every test gets a private, fully migrated schema and is skipped when
// TEST_DATABASE_URL is unset.
func oidcPasswordLoginTestStore(t *testing.T) *Store {
	t.Helper()
	return groupTestStore(t)
}

// An account that the OIDC path created must not be reachable with a password,
// and SetUserPassword must switch that back on. Both halves fail silently when
// the column is not threaded through every statement: the OIDC insert leaves a
// placeholder hash that looks like a credential, and an administrator's new
// password is stored but refused at sign-in.
func TestOIDCUserPasswordLoginFlagFollowsSetUserPassword(t *testing.T) {
	ctx := context.Background()
	s := oidcPasswordLoginTestStore(t)

	created, err := s.GetOrCreateOIDCUser(ctx, oidcPasswordLoginIssuer, oidcPasswordLoginSubject, &model.User{
		Username:     "oidc-user",
		DisplayName:  "OIDC User",
		Role:         model.RoleUser,
		PasswordHash: "placeholder-not-a-credential",
		Active:       true,
	})
	if err != nil {
		t.Fatalf("GetOrCreateOIDCUser: %v", err)
	}
	if created.PasswordLoginEnabled {
		t.Fatalf("OIDC-created user returned password_login_enabled=true")
	}

	// Every read path has to carry the column; the INSERT's return value alone
	// does not prove the stored row is right.
	for _, read := range []struct {
		name string
		get  func() (*model.User, error)
	}{
		{name: "GetUserByOIDC", get: func() (*model.User, error) {
			return s.GetUserByOIDC(ctx, oidcPasswordLoginIssuer, oidcPasswordLoginSubject)
		}},
		{name: "GetUserByUsername", get: func() (*model.User, error) {
			return s.GetUserByUsername(ctx, "oidc-user")
		}},
		{name: "GetUserByID", get: func() (*model.User, error) {
			return s.GetUserByID(ctx, created.ID)
		}},
	} {
		user, err := read.get()
		if err != nil {
			t.Fatalf("%s: %v", read.name, err)
		}
		if user.PasswordLoginEnabled {
			t.Fatalf("%s returned password_login_enabled=true for an OIDC user", read.name)
		}
	}
	listed, err := s.ListUsers(ctx)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	for _, user := range listed {
		if user.ID == created.ID && user.PasswordLoginEnabled {
			t.Fatalf("ListUsers returned password_login_enabled=true for an OIDC user")
		}
	}

	// A second OIDC sign-in resolves the existing identity instead of inserting;
	// it must keep the flag off.
	resolved, err := s.GetOrCreateOIDCUser(ctx, oidcPasswordLoginIssuer, oidcPasswordLoginSubject, &model.User{
		Username:     "oidc-user",
		DisplayName:  "OIDC User",
		Role:         model.RoleUser,
		PasswordHash: "placeholder-not-a-credential",
		Active:       true,
	})
	if err != nil {
		t.Fatalf("GetOrCreateOIDCUser (existing identity): %v", err)
	}
	if resolved.PasswordLoginEnabled {
		t.Fatalf("an existing OIDC identity returned password_login_enabled=true")
	}

	if err := s.SetUserPassword(ctx, created.ID, "argon2id-replacement"); err != nil {
		t.Fatalf("SetUserPassword: %v", err)
	}
	after, err := s.GetUserByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetUserByID after SetUserPassword: %v", err)
	}
	if !after.PasswordLoginEnabled {
		t.Fatalf("SetUserPassword left password_login_enabled=false, so the new password is unusable")
	}
	if after.PasswordHash != "argon2id-replacement" {
		t.Fatalf("password_hash = %q, want the value SetUserPassword stored", after.PasswordHash)
	}

}

// The OIDC creation path must store a syntactically valid Argon2id hash rather
// than an opaque token. VerifyPassword rejects a malformed encoding before it
// runs Argon2id, so with a malformed placeholder the login handler never
// reaches its password_login_enabled gate and answers faster than a wrong
// password does — an account-source signal. The store derives the placeholder
// itself, so a caller cannot reintroduce a malformed row.
func TestOIDCCreatedUserStoresValidArgon2PlaceholderHash(t *testing.T) {
	ctx := context.Background()
	s := oidcPasswordLoginTestStore(t)

	created, err := s.GetOrCreateOIDCUser(ctx, oidcPlaceholderIssuer, oidcPlaceholderSubject, &model.User{
		Username:     oidcPlaceholderFirstUsername,
		DisplayName:  "OIDC Placeholder",
		Role:         model.RoleUser,
		PasswordHash: oidcPlaceholderCallerSupplied,
		Active:       true,
	})
	if err != nil {
		t.Fatalf("GetOrCreateOIDCUser: %v", err)
	}
	if created.PasswordLoginEnabled {
		t.Fatalf("OIDC-created user returned password_login_enabled=true")
	}
	if created.PasswordHash == "" || created.PasswordHash == oidcPlaceholderCallerSupplied {
		t.Fatalf("returned password_hash = %q, want a store-generated placeholder hash", created.PasswordHash)
	}

	stored, err := s.GetUserByUsername(ctx, oidcPlaceholderFirstUsername)
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if stored.PasswordLoginEnabled {
		t.Fatalf("stored password_login_enabled=true for an OIDC-created user")
	}
	if stored.PasswordHash != created.PasswordHash {
		t.Fatalf("stored password_hash = %q, returned password_hash = %q", stored.PasswordHash, created.PasswordHash)
	}
	if !strings.HasPrefix(stored.PasswordHash, "$argon2id$") {
		t.Fatalf("stored password_hash = %q, want an Argon2id encoding", stored.PasswordHash)
	}
	matched, err := gatewaycrypto.VerifyPassword("any-password-a-caller-submits", stored.PasswordHash)
	if err != nil {
		t.Fatalf("stored password_hash is not usable by VerifyPassword, so the login gate would be bypassed by the fast format-rejection path: %v", err)
	}
	if matched {
		t.Fatalf("the store-generated placeholder hash accepted a submitted password")
	}

	// The placeholder is per-account: two OIDC users must not share a hash.
	second, err := s.GetOrCreateOIDCUser(ctx, oidcPlaceholderIssuer, oidcPlaceholderSecondSubject, &model.User{
		Username: oidcPlaceholderSecondUsername,
		Role:     model.RoleUser,
		Active:   true,
	})
	if err != nil {
		t.Fatalf("GetOrCreateOIDCUser (second account): %v", err)
	}
	if second.PasswordHash == stored.PasswordHash {
		t.Fatalf("two OIDC accounts share password_hash %q", stored.PasswordHash)
	}
	if _, err := gatewaycrypto.VerifyPassword("any-password-a-caller-submits", second.PasswordHash); err != nil {
		t.Fatalf("second account password_hash is not usable by VerifyPassword: %v", err)
	}
}

// Accounts created through CreateUser (the administrator API and the first-boot
// administrator) keep password login. The Go struct's zero value is false, so
// this only holds while the INSERT writes TRUE itself.
func TestCreateUserEnablesPasswordLoginForAdministratorCreatedAccounts(t *testing.T) {
	ctx := context.Background()
	s := oidcPasswordLoginTestStore(t)

	// PasswordLoginEnabled is deliberately left unset here.
	id, err := s.CreateUser(ctx, &model.User{Username: "seeded-admin", Role: model.RoleAdmin, PasswordHash: "hash", Active: true})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	user, err := s.GetUserByID(ctx, id)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if !user.PasswordLoginEnabled {
		t.Fatalf("CreateUser left password_login_enabled=false, so the administrator's password is unusable")
	}
	if !user.Active {
		t.Fatalf("CreateUser stored active=false, want true")
	}
}

// The password_login_enabled column is added by 0006 to a users table that
// already exists, so the upgrade must backfill every existing row to TRUE —
// otherwise every account in a deployed database loses password login. The file
// is replayed twice as well, the shape a partially applied or manually replayed
// file takes.
func TestOIDCPasswordLoginMigrationBackfillsExistingRowsAndReplays(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL migration integration tests")
	}
	ctx := context.Background()
	admin, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	defer admin.Close()
	if err := admin.PingContext(ctx); err != nil {
		t.Fatalf("ping admin connection: %v", err)
	}

	schema := fmt.Sprintf("oidc_password_login_%d", time.Now().UnixNano())
	quotedSchema := `"` + strings.ReplaceAll(schema, `"`, `""`) + `"`
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	defer func() {
		if _, err := admin.ExecContext(context.Background(), "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop schema: %v", err)
		}
	}()

	db, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatalf("open connection: %v", err)
	}
	defer db.Close()
	// A single pinned connection keeps the search_path in effect for every
	// statement, so the unqualified table names land in the throwaway schema.
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire connection: %v", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SET search_path TO "+quotedSchema); err != nil {
		t.Fatalf("set search_path: %v", err)
	}

	base, err := migrationsFS.ReadFile("migrations/0001_init.sql")
	if err != nil {
		t.Fatalf("read 0001_init.sql: %v", err)
	}
	if _, err := conn.ExecContext(ctx, string(base)); err != nil {
		t.Fatalf("apply 0001_init.sql: %v", err)
	}
	// A row that exists before the OIDC migration: the deployed case.
	if _, err := conn.ExecContext(ctx, `INSERT INTO users(username, display_name, role, password_hash, active) VALUES('legacy', 'Legacy', 'user', 'legacy-hash', TRUE)`); err != nil {
		t.Fatalf("insert pre-existing user: %v", err)
	}

	oidc, err := migrationsFS.ReadFile("migrations/0006_oidc_identities.sql")
	if err != nil {
		t.Fatalf("read 0006_oidc_identities.sql: %v", err)
	}
	for pass := 1; pass <= 2; pass++ {
		if _, err := conn.ExecContext(ctx, string(oidc)); err != nil {
			t.Fatalf("0006_oidc_identities.sql failed on pass %d: %v", pass, err)
		}
	}

	var enabled bool
	if err := conn.QueryRowContext(ctx, `SELECT password_login_enabled FROM users WHERE username='legacy'`).Scan(&enabled); err != nil {
		t.Fatalf("read backfilled password_login_enabled: %v", err)
	}
	if !enabled {
		t.Fatalf("a user that existed before 0006 has password_login_enabled=false, so the upgrade locks deployed accounts out")
	}

	var nullable, defaultValue string
	if err := conn.QueryRowContext(ctx, `
		SELECT is_nullable, column_default FROM information_schema.columns
		WHERE table_schema=$1 AND table_name='users' AND column_name='password_login_enabled'`, schema).Scan(&nullable, &defaultValue); err != nil {
		t.Fatalf("inspect password_login_enabled column: %v", err)
	}
	if nullable != "NO" {
		t.Fatalf("password_login_enabled is nullable, want NOT NULL")
	}
	if !strings.Contains(defaultValue, "true") {
		t.Fatalf("password_login_enabled default = %q, want TRUE", defaultValue)
	}
}
