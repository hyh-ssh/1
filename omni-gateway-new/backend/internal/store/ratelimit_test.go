package store

import (
	"context"
	"testing"

	"github.com/zjvtit/omni-gateway/internal/model"
)

// rateLimitTestStore returns the shared PostgreSQL harness defined in
// groups_test.go: every test gets a private schema and the whole test is
// skipped when TEST_DATABASE_URL is unset.
func rateLimitTestStore(t *testing.T) *Store {
	t.Helper()
	return groupTestStore(t)
}

// The admin API echoes back whatever scope, scope_id and model_id it was given,
// so the console reported "修改已保存。" while those three columns silently kept
// their previous values. These tests pin every mutable column to the database.
func TestUpdateRateLimitPersistsEveryMutableColumn(t *testing.T) {
	ctx := context.Background()
	s := rateLimitTestStore(t)

	id, err := s.CreateRateLimit(ctx, &model.RateLimit{Scope: "user", ScopeID: 1, ModelID: "chat", RPM: 10, TPM: 20, Concurrency: 1, Active: true})
	if err != nil {
		t.Fatalf("CreateRateLimit: %v", err)
	}

	updated := &model.RateLimit{ID: id, Scope: "key", ScopeID: 7, ModelID: "embed", RPM: 30, TPM: 40, Concurrency: 2, Active: false}
	if err := s.UpdateRateLimit(ctx, updated); err != nil {
		t.Fatalf("UpdateRateLimit: %v", err)
	}

	got, err := s.GetRateLimit(ctx, id)
	if err != nil {
		t.Fatalf("GetRateLimit: %v", err)
	}
	if got.Scope != "key" || got.ScopeID != 7 || got.ModelID != "embed" {
		t.Fatalf("target columns after update = scope %q, scope_id %d, model_id %q; want \"key\", 7, \"embed\"", got.Scope, got.ScopeID, got.ModelID)
	}
	if got.RPM != 30 || got.TPM != 40 || got.Concurrency != 2 || got.Active {
		t.Fatalf("meter columns after update = rpm %d, tpm %d, concurrency %d, active %t; want 30, 40, 2, false", got.RPM, got.TPM, got.Concurrency, got.Active)
	}

	listed, err := s.ListAllRateLimits(ctx)
	if err != nil {
		t.Fatalf("ListAllRateLimits: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("ListAllRateLimits returned %d rules, want 1", len(listed))
	}
	if listed[0].Scope != "key" || listed[0].ScopeID != 7 || listed[0].ModelID != "embed" {
		t.Fatalf("admin listing after update = scope %q, scope_id %d, model_id %q; want \"key\", 7, \"embed\"", listed[0].Scope, listed[0].ScopeID, listed[0].ModelID)
	}
}

// Clearing model_id switches a rule back to "every model", which the update path
// has to support: an empty string is a real value here, not an omitted field.
func TestUpdateRateLimitCanClearModelID(t *testing.T) {
	ctx := context.Background()
	s := rateLimitTestStore(t)

	id, err := s.CreateRateLimit(ctx, &model.RateLimit{Scope: "user", ScopeID: 3, ModelID: "chat", RPM: 10, Active: true})
	if err != nil {
		t.Fatalf("CreateRateLimit: %v", err)
	}
	if err := s.UpdateRateLimit(ctx, &model.RateLimit{ID: id, Scope: "user", ScopeID: 3, ModelID: "", RPM: 10, Active: true}); err != nil {
		t.Fatalf("UpdateRateLimit: %v", err)
	}
	got, err := s.GetRateLimit(ctx, id)
	if err != nil {
		t.Fatalf("GetRateLimit: %v", err)
	}
	if got.ModelID != "" {
		t.Fatalf("model_id after clearing = %q, want empty", got.ModelID)
	}
}

// The runtime loads model-scope rules with ListRateLimits("model", 0), so a model
// rule stored with a non-zero scope_id would never be applied. The admin API
// normalises scope_id to 0; this test documents the loader contract behind it.
func TestRateLimitModelScopeLoadsOnlyFromZeroScopeID(t *testing.T) {
	ctx := context.Background()
	s := rateLimitTestStore(t)

	loadable, err := s.CreateRateLimit(ctx, &model.RateLimit{Scope: "model", ScopeID: 0, ModelID: "chat", RPM: 5, Active: true})
	if err != nil {
		t.Fatalf("CreateRateLimit: %v", err)
	}
	unloadable, err := s.CreateRateLimit(ctx, &model.RateLimit{Scope: "model", ScopeID: 9, ModelID: "chat", RPM: 5, Active: true})
	if err != nil {
		t.Fatalf("CreateRateLimit: %v", err)
	}

	rules, err := s.ListRateLimits(ctx, "model", 0)
	if err != nil {
		t.Fatalf("ListRateLimits: %v", err)
	}
	found, stray := false, false
	for _, rule := range rules {
		switch rule.ID {
		case loadable:
			found = true
		case unloadable:
			stray = true
		}
	}
	if !found {
		t.Fatalf("model rule with scope_id 0 was not returned by ListRateLimits(model, 0)")
	}
	if stray {
		t.Fatalf("model rule with scope_id 9 is returned by ListRateLimits(model, 0); the loader contract changed")
	}
}
