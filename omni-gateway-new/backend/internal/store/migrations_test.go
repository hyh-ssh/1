package store

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// TestMigrationsAreIndividuallyIdempotent replays every embedded migration file
// twice inside a throwaway schema. Migrate only runs files that are not yet
// recorded in schema_migrations, so this guards the case where the same SQL is
// applied again: a partially applied file, a manually replayed file, or a
// schema that was migrated before the file gained new statements.
func TestMigrationsAreIndividuallyIdempotent(t *testing.T) {
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

	schema := fmt.Sprintf("migrations_idempotent_%d", time.Now().UnixNano())
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
	// statement, so migrations that use unqualified table names hit the
	// throwaway schema instead of public.
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire connection: %v", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SET search_path TO "+quotedSchema); err != nil {
		t.Fatalf("set search_path: %v", err)
	}

	names, err := migrationNames(migrationsFS)
	if err != nil {
		t.Fatalf("list migrations: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("no embedded migrations found")
	}

	for _, name := range names {
		sqlBytes, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for pass := 1; pass <= 2; pass++ {
			if _, err := conn.ExecContext(ctx, string(sqlBytes)); err != nil {
				t.Fatalf("migration %s failed on pass %d: %v", name, pass, err)
			}
		}
	}
}

// TestMigrationNamesFiltersEmbeddedEntries guards the loader against entries the
// //go:embed wildcard can pick up: macOS AppleDouble sidecars ("._name.sql"),
// non-SQL files and directories. Only real .sql files survive, sorted by name.
func TestMigrationNamesFiltersEmbeddedEntries(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations/0002_second.sql":  {Data: []byte("SELECT 2;")},
		"migrations/0001_first.sql":   {Data: []byte("SELECT 1;")},
		"migrations/._0001_first.sql": {Data: []byte{0x00, 0x05, 0x16, 0x07}},
		"migrations/README.md":        {Data: []byte("# migrations")},
		"migrations/subdir":           {Mode: fs.ModeDir},
	}
	got, err := migrationNames(fsys)
	if err != nil {
		t.Fatalf("migrationNames: %v", err)
	}
	want := []string{"0001_first.sql", "0002_second.sql"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("migrationNames = %v, want %v", got, want)
	}
}

// TestEmbeddedMigrationsAreSortedSQL asserts the real embedded filesystem only
// exposes sorted .sql names, so Migrate applies them deterministically.
func TestEmbeddedMigrationsAreSortedSQL(t *testing.T) {
	names, err := migrationNames(migrationsFS)
	if err != nil {
		t.Fatalf("migrationNames: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("no embedded migrations found")
	}
	if !sort.StringsAreSorted(names) {
		t.Fatalf("migrations not sorted: %v", names)
	}
	for _, name := range names {
		if strings.HasPrefix(name, "._") || !strings.HasSuffix(name, ".sql") {
			t.Fatalf("unexpected migration entry %q", name)
		}
	}
}
