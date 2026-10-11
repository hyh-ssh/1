package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zjvtit/omni-gateway/internal/model"
)

func TestProviderCompatibilityMigrationBackfillsAndConstrainsProfiles(t *testing.T) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL migration integration tests")
	}
	ctx := context.Background()
	admin, err := sql.Open("postgres", dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if err := admin.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("provider_compatibility_%d", time.Now().UnixNano())
	quotedSchema := `"` + strings.ReplaceAll(schema, `"`, `""`) + `"`
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.ExecContext(context.Background(), "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop schema: %v", err)
		}
	}()
	dsn, err := urlpkgWithSearchPath(dbURL, schema)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	const migration = "0008_provider_compatibility_profile.sql"
	names, err := migrationNames(migrationsFS)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, name := range names {
		if name == migration {
			found = true
			break
		}
		contents, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.pool.Exec(ctx, string(contents)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	if !found {
		t.Fatalf("missing embedded migration %s", migration)
	}
	var legacyID int64
	if err := s.pool.QueryRow(ctx, `INSERT INTO providers(name, type, base_url) VALUES('legacy', 'anthropic', 'https://api.example.com') RETURNING id`).Scan(&legacyID); err != nil {
		t.Fatal(err)
	}
	contents, err := migrationsFS.ReadFile("migrations/" + migration)
	if err != nil {
		t.Fatal(err)
	}
	for pass := 1; pass <= 2; pass++ {
		if _, err := s.pool.Exec(ctx, string(contents)); err != nil {
			t.Fatalf("migration pass %d: %v", pass, err)
		}
	}
	legacy, err := s.GetProvider(ctx, legacyID)
	if err != nil || legacy.CompatibilityProfile != model.ProviderCompatibilityGeneric {
		t.Fatalf("legacy provider = %+v, error = %v; want generic", legacy, err)
	}
	var defaultProfile string
	if err := s.pool.QueryRow(ctx, `INSERT INTO providers(name, type, base_url) VALUES('new', 'gemini', 'https://api.example.com') RETURNING compatibility_profile`).Scan(&defaultProfile); err != nil || defaultProfile != "generic" {
		t.Fatalf("SQL default profile = %q, error = %v", defaultProfile, err)
	}
	for _, profile := range []string{"unknown", "deepseek"} {
		if _, err := s.pool.Exec(ctx, `UPDATE providers SET compatibility_profile=$1 WHERE id=$2`, profile, legacyID); err == nil {
			t.Fatalf("constraint accepted %q for anthropic provider", profile)
		}
	}
	if _, err := s.pool.Exec(ctx, `UPDATE providers SET type='openai_compatible', compatibility_profile='deepseek' WHERE id=$1`, legacyID); err != nil {
		t.Fatalf("allow DeepSeek with openai_compatible: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE providers SET type='anthropic' WHERE id=$1`, legacyID); err == nil {
		t.Fatal("constraint allowed a DeepSeek provider to switch to anthropic")
	}
}

func TestProviderCompatibilityStoreCRUDAndRouteModelLookup(t *testing.T) {
	s := groupTestStore(t)
	ctx := context.Background()
	p := &model.Provider{Name: "DeepSeek", Type: model.ProviderOpenAICompat, CompatibilityProfile: model.ProviderCompatibilityDeepSeek, BaseURL: "https://api.example.com/v1", TimeoutMS: 30000, Active: true}
	id, err := s.CreateProvider(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetProvider(ctx, id)
	if err != nil || got.CompatibilityProfile != model.ProviderCompatibilityDeepSeek {
		t.Fatalf("GetProvider = %+v, error = %v", got, err)
	}
	listed, err := s.ListProviders(ctx)
	if err != nil || len(listed) != 1 || listed[0].CompatibilityProfile != model.ProviderCompatibilityDeepSeek {
		t.Fatalf("ListProviders = %+v, error = %v", listed, err)
	}
	got.CompatibilityProfile = model.ProviderCompatibilityGeneric
	got.Type = model.ProviderAnthropic
	if err := s.UpdateProvider(ctx, got); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetProvider(ctx, id)
	if err != nil || got.CompatibilityProfile != model.ProviderCompatibilityGeneric || got.Type != model.ProviderAnthropic {
		t.Fatalf("GetProvider after update = %+v, error = %v", got, err)
	}
	other := &model.Provider{Name: "other", Type: model.ProviderOpenAICompat, BaseURL: "https://api.example.com/v1", Active: true}
	otherID, err := s.CreateProvider(ctx, other)
	if err != nil || other.CompatibilityProfile != model.ProviderCompatibilityGeneric {
		t.Fatalf("CreateProvider default = %+v, error = %v", other, err)
	}

	if err := s.UpsertProviderModel(ctx, id, "deepseek-reasoner", model.Capabilities{Reasoning: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertProviderModel(ctx, otherID, "deepseek-reasoner", model.Capabilities{Reasoning: false}); err != nil {
		t.Fatal(err)
	}
	upstream, err := s.GetProviderModel(ctx, id, "deepseek-reasoner")
	if err != nil || upstream.ModelID != "deepseek-reasoner" || upstream.ProviderID != id || !upstream.Capabilities.Reasoning || !upstream.Active {
		t.Fatalf("GetProviderModel = %+v, error = %v", upstream, err)
	}
	otherModel, err := s.GetProviderModel(ctx, otherID, "deepseek-reasoner")
	if err != nil || otherModel.Capabilities.Reasoning {
		t.Fatalf("GetProviderModel other provider = %+v, error = %v", otherModel, err)
	}
	if _, err := s.GetProviderModel(ctx, id, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing model error = %v, want ErrNotFound", err)
	}
	if _, err := s.GetProviderModel(ctx, id+otherID+100, "deepseek-reasoner"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing provider model error = %v, want ErrNotFound", err)
	}
	if err := s.UpsertProviderModel(ctx, id, "deepseek-reasoner", model.Capabilities{Reasoning: false}); err != nil {
		t.Fatal(err)
	}
	updated, err := s.GetProviderModel(ctx, id, "deepseek-reasoner")
	if err != nil || updated.Capabilities.Reasoning {
		t.Fatalf("updated reasoning = %+v, error = %v", updated, err)
	}
}
