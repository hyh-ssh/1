package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// 0007 adds supports_responses to a providers table that already exists, so the
// upgrade must leave every row that predates the file opted out: the gateway
// then refuses /v1/responses with 501 instead of forwarding calls to an
// upstream that may not implement it. The file is replayed twice as well, the
// shape a partially applied or manually replayed file takes.
func TestProviderResponsesMigrationKeepsExistingRowsOptedOutAndReplays(t *testing.T) {
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

	schema := fmt.Sprintf("provider_responses_%d", time.Now().UnixNano())
	quotedSchema := `"` + strings.ReplaceAll(schema, `"`, `""`) + `"`
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	defer func() {
		if _, err := admin.ExecContext(context.Background(), "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop schema: %v", err)
		}
	}()

	// The search_path option keeps every pooled connection inside the throwaway
	// schema, so the store's own SQL exercises the upgraded tables.
	dsn, err := urlpkgWithSearchPath(url, schema)
	if err != nil {
		t.Fatalf("build schema DSN: %v", err)
	}
	s, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("open schema store: %v", err)
	}
	defer s.Close()

	// Apply every embedded migration that predates the opt-in file, exactly the
	// schema a deployed gateway runs when 0007 first lands.
	const responsesMigration = "0007_provider_responses_api.sql"
	names, err := migrationNames(migrationsFS)
	if err != nil {
		t.Fatalf("list migrations: %v", err)
	}
	responsesAt := -1
	for i, name := range names {
		if name == responsesMigration {
			responsesAt = i
			break
		}
	}
	if responsesAt < 0 {
		t.Fatalf("%s is missing from the embedded migrations", responsesMigration)
	}
	apply := func(name string) {
		t.Helper()
		sqlBytes, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if _, err := s.pool.Exec(ctx, string(sqlBytes)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	for _, name := range names[:responsesAt] {
		apply(name)
	}

	// A provider row that exists before the opt-in column: the deployed case.
	var providerID int64
	if err := s.pool.QueryRow(ctx, `INSERT INTO providers(name, type, base_url) VALUES('legacy-provider', 'openai_compatible', 'https://legacy.example') RETURNING id`).Scan(&providerID); err != nil {
		t.Fatalf("insert pre-existing provider: %v", err)
	}

	// The opt-in file is replayed twice as well, the shape a partially applied
	// or manually replayed file takes.
	for pass := 1; pass <= 2; pass++ {
		sqlBytes, err := migrationsFS.ReadFile("migrations/" + responsesMigration)
		if err != nil {
			t.Fatalf("read %s: %v", responsesMigration, err)
		}
		if _, err := s.pool.Exec(ctx, string(sqlBytes)); err != nil {
			t.Fatalf("%s failed on pass %d: %v", responsesMigration, pass, err)
		}
	}

	// Files newer than the opt-in one carry columns the store's SQL already
	// selects — 0008 adds compatibility_profile, which GetProvider reads — so
	// they land before the upgraded table is read back through the store. The
	// row inserted above still predates 0007, which is what this test asserts.
	for _, name := range names[responsesAt+1:] {
		apply(name)
	}

	provider, err := s.GetProvider(ctx, providerID)
	if err != nil {
		t.Fatalf("GetProvider after 0007: %v", err)
	}
	if provider.SupportsResponses {
		t.Fatalf("a provider that existed before 0007 has supports_responses=true, so the upgrade enables /v1/responses without an operator opting in")
	}

	provider.SupportsResponses = true
	if err := s.UpdateProvider(ctx, provider); err != nil {
		t.Fatalf("UpdateProvider: %v", err)
	}
	updated, err := s.GetProvider(ctx, providerID)
	if err != nil {
		t.Fatalf("GetProvider after opt-in: %v", err)
	}
	if !updated.SupportsResponses {
		t.Fatalf("UpdateProvider stored supports_responses=true but GetProvider read back false, so no provider can be opted in")
	}
}
