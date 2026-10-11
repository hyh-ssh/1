package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zjvtit/omni-gateway/internal/model"
	"github.com/zjvtit/omni-gateway/internal/policy"
)

func productLegacyStore(t *testing.T) *Store {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL is required for product migration/history tests")
	}
	db, err := sql.Open("postgres", raw)
	if err != nil {
		t.Fatal("invalid test DB")
	}
	schema := fmt.Sprintf("product_history_%d", time.Now().UnixNano())
	ctx := context.Background()
	if _, err = db.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		db.Close()
		t.Fatal("create isolated history schema failed")
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error("drop history schema failed")
		}
		db.Close()
	})
	dsn, err := urlpkgWithSearchPath(raw, schema)
	if err != nil {
		t.Fatal("invalid test URL")
	}
	s, err := New(ctx, dsn)
	if err != nil {
		t.Fatal("open history store failed")
	}
	t.Cleanup(s.Close)
	names, err := migrationNames(migrationsFS)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name >= "0010_product_observability.sql" {
			break
		}
		data, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.pool.Exec(ctx, string(data)); err != nil {
			t.Fatal(err)
		}
	}
	return s
}
func historyIDs(t *testing.T) func(int64, error) int64 {
	return func(id int64, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
}

func TestProductForwardMigrationPreservesHistoricalRowsAndUnknownAudit(t *testing.T) {
	s := productLegacyStore(t)
	ctx := context.Background()
	must := historyIDs(t)
	user := must(s.CreateUser(ctx, &model.User{Username: "historical", PasswordHash: "fixture-placeholder", Active: true, Role: model.RoleUser}))
	key := must(s.CreateAPIKey(ctx, &model.APIKey{UserID: user, Name: "legacy-key", KeyHash: "fixture-hash", Active: true, ModelAllow: []string{"legacy-active"}}))
	if _, err := s.pool.Exec(ctx, `INSERT INTO model_aliases(name,active) VALUES('legacy-active',TRUE)`); err != nil {
		t.Fatal(err)
	}
	a, err := s.GetAlias(ctx, "legacy-active")
	if err != nil {
		t.Fatal(err)
	}
	mg := &model.ModelGroup{Name: "history-classification", Active: true, AliasIDs: []int64{a.ID}}
	mg.ID = must(s.CreateModelGroup(ctx, mg))
	ug := &model.UserGroup{Name: "history-membership", Active: true, UserIDs: []int64{user}, ModelGroupIDs: []int64{mg.ID}}
	ug.ID = must(s.CreateUserGroup(ctx, ug))
	zero := model.ZeroMoney
	must(s.CreateBudgetRule(ctx, &model.BudgetRule{UserID: user, WindowSecs: 60, CostLimit: &zero, CreditLimit: &zero, Active: true}))
	must(s.CreatePriceVersion(ctx, &model.PriceVersion{ModelID: "legacy-active", Version: 1, Cost: map[string]model.Money{"custom": model.MustMoney("0.000000000000000000001")}, Credits: map[string]model.Credits{"custom": model.MustCredits("0.000000000000000000001")}}))
	r := &model.Request{ID: "legacy-request", UserID: user, APIKeyID: key, API: "chat_completions", ModelAlias: a.Name, Status: model.StatusSuccess, StatusCode: 200, Usage: model.Usage{Meters: map[string]int64{"custom": 7}, Estimated: true}}
	if err := s.InsertRequest(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO attempts(request_id,provider_id,provider_model,status,settlement_id) VALUES($1,1,'legacy-model','success','legacy-attempt')`, r.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertLedgerEntries(ctx, []*model.LedgerEntry{{SettlementID: "legacy-attempt", RequestID: r.ID, UserID: user, APIKeyID: key, ModelAlias: a.Name, ProviderID: 1, ProviderModel: "legacy-model", Meter: "custom", Quantity: 7, PriceVersion: 1, Estimated: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO audit_log(actor_id,actor_name,action,resource,detail) VALUES($1,'fixture','PATCH','providers','legacy detail untouched')`, user); err != nil {
		t.Fatal(err)
	}
	oidc, err := s.GetOrCreateOIDCUser(ctx, "https://fixture.invalid", "historical-subject", &model.User{Username: "legacy-sso"})
	if err != nil {
		t.Fatal(err)
	}
	// Capture complete old row values, not just counts. Credentials stay in local
	// memory and are compared without ever being printed in a failure message.
	snapshot := func() map[string]string {
		result := map[string]string{}
		for _, table := range []string{"users", "api_keys", "model_aliases", "model_groups", "model_group_aliases", "user_groups", "user_group_members", "user_group_model_groups", "budget_rules", "price_versions", "usage_ledger", "requests", "oidc_identities"} {
			var value string
			query := `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM ` + table + ` t`
			if err := s.pool.QueryRow(ctx, query).Scan(&value); err != nil {
				t.Fatal(err)
			}
			result[table] = value
		}
		return result
	}
	before := snapshot()
	data, err := migrationsFS.ReadFile("migrations/0010_product_observability.sql")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err = s.pool.Exec(ctx, string(data)); err != nil {
			t.Fatal(err)
		}
	}
	after := snapshot()
	for table, value := range before {
		if after[table] != value {
			t.Fatalf("forward migration rewrote historical %s", table)
		}
	}
	audits, err := s.ListAuditFiltered(ctx, model.AuditFilter{Result: "unknown"})
	if err != nil || len(audits) != 1 || audits[0].Detail != "legacy detail untouched" || audits[0].Before != nil || audits[0].After != nil || audits[0].Result != "unknown" {
		t.Fatal("legacy audit fabricated or lost", err)
	}
	// A later upstream price with the same numeric version cannot become the
	// supposed source of a historical alias-priced ledger and label it free.
	must(s.CreatePriceVersion(ctx, &model.PriceVersion{ModelID: "legacy-model", Version: 1, Cost: map[string]model.Money{"custom": model.ZeroMoney}, Credits: map[string]model.Credits{"custom": model.ZeroCredits}}))
	persisted, err := s.GetRequest(ctx, r.ID)
	if err != nil || persisted.SettlementStatus != "settled" {
		t.Fatal("tiny positive historical price misclassified as free", err)
	}
	unchanged, err := s.GetAlias(ctx, "legacy-active")
	if err != nil || !unchanged.Active {
		t.Fatal("anomalous legacy alias was auto-disabled", err)
	}
	local, err := s.GetUserByID(ctx, oidc.ID)
	if err != nil || local.PasswordLoginEnabled {
		t.Fatal("OIDC local login changed", err)
	}
	release, err := policy.NewService(s).Acquire(ctx, user, key, a.Name)
	if err != nil {
		t.Fatal("old zero budget blocked admission", err)
	}
	release()
	changedKey, err := s.GetAPIKeyByID(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	changedKey.UserID = oidc.ID
	if err := s.UpdateAPIKey(ctx, changedKey); !errors.Is(err, ErrKeyOwnerImmutable) {
		t.Fatal("store silently accepted owner reassignment", err)
	}
	stored, err := s.GetAPIKeyByID(ctx, key)
	if err != nil || stored.UserID != user {
		t.Fatal("key owner changed", err)
	}
	draft := must(s.CreateAlias(ctx, "new-draft"))
	if err := s.SetAliasActive(ctx, draft, true); !errors.Is(err, ErrAliasNotReady) {
		t.Fatal("draft without real route enabled", err)
	}
}

func TestProductAuditFiltersSafetyAndLegacyPagination(t *testing.T) {
	s := groupTestStore(t)
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		result := "success"
		if i%2 == 1 {
			result = "failure"
		}
		if err := s.InsertAudit(ctx, &model.AuditEntry{ActorID: 7, ActorName: "actor", Action: "PATCH", Resource: "providers", ResourceID: fmt.Sprint(i), Result: result, Before: map[string]any{"active": true, "api_key": "fixture-secret", "password_hash": "fixture-hash", "body": map[string]any{"secret": "nested"}, "name": map[string]any{"secret": "nested"}}, After: map[string]any{"active": false, "client_secret": "fixture-secret"}}); err != nil {
			t.Fatal(err)
		}
	}
	filter := model.AuditFilter{ActorID: 7, Resource: "providers", Action: "PATCH", Result: "failure", Limit: 1}
	page1, err := s.ListAuditFiltered(ctx, filter)
	if err != nil || len(page1) != 1 {
		t.Fatal(err)
	}
	filter.Offset = 1
	page2, err := s.ListAuditFiltered(ctx, filter)
	if err != nil || len(page2) != 1 || page1[0].ID == page2[0].ID {
		t.Fatal("audit filters/pagination disagree", err)
	}
	bytes, err := json.Marshal(append(page1, page2...))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"fixture-secret", "fixture-hash", "nested", "api_key", "password_hash", "client_secret", "body"} {
		if strings.Contains(string(bytes), secret) {
			t.Fatal("sensitive audit field persisted")
		}
	}
	filter.From = page1[0].CreatedAt
	filter.To = page1[0].CreatedAt
	filter.Offset = 0
	exact, err := s.ListAuditFiltered(ctx, filter)
	if err != nil || len(exact) != 1 || exact[0].ID != page1[0].ID {
		t.Fatal("audit inclusive timestamp bounds wrong", err)
	}
}

func TestProductPostgresRetiredBudgetKeepsRPMTPMAndConcurrency(t *testing.T) {
	s := groupTestStore(t)
	ctx := context.Background()
	must := historyIDs(t)
	user := must(s.CreateUser(ctx, &model.User{Username: "limited", PasswordHash: "fixture-placeholder", Role: model.RoleUser, Active: true}))
	key := must(s.CreateAPIKey(ctx, &model.APIKey{UserID: user, Name: "key", KeyHash: "fixture-rate-hash", Active: true}))
	zero := model.ZeroMoney
	must(s.CreateBudgetRule(ctx, &model.BudgetRule{UserID: user, WindowSecs: 60, CostLimit: &zero, CreditLimit: &zero, Active: true}))
	for _, kind := range []string{"RPM", "TPM", "concurrency"} {
		t.Run(kind, func(t *testing.T) {
			rule := &model.RateLimit{Scope: "user", ScopeID: user, ModelID: kind, Active: true}
			switch kind {
			case "RPM":
				rule.RPM = 1
			case "TPM":
				rule.TPM = 5
			case "concurrency":
				rule.Concurrency = 1
			}
			must(s.CreateRateLimit(ctx, rule))
			p := policy.NewService(s)
			first, err := p.Acquire(ctx, user, key, kind)
			if err != nil {
				t.Fatal("retired zero budget blocked first admission", err)
			}
			if kind == "concurrency" {
				if _, err := p.Acquire(ctx, user, key, kind); !errors.Is(err, policy.ErrRateLimited) {
					t.Fatal("concurrency no longer enforced", err)
				}
			}
			first()
			p.ObserveUsage(ctx, user, key, kind, model.Usage{Meters: map[string]int64{"input": 3, "output": 2, "total": 5}})
			p.RecordCharge(ctx, user, model.ZeroMoney, model.ZeroCredits)
			p.RecordSettlementFailure(ctx, user)
			second, err := p.Acquire(ctx, user, key, kind)
			if kind == "concurrency" {
				if err != nil {
					t.Fatal("released concurrency/retired settlement block refused admission", err)
				}
				second()
			} else if !errors.Is(err, policy.ErrRateLimited) {
				t.Fatal("RPM/TPM no longer enforced", err)
			}
		})
	}
}

func TestProductPasswordAtomicSessionAndStaleProof(t *testing.T) {
	s := groupTestStore(t)
	ctx := context.Background()
	must := historyIDs(t)
	user := must(s.CreateUser(ctx, &model.User{Username: "local", PasswordHash: "old-fixture-hash", Role: model.RoleUser, Active: true}))
	other := must(s.CreateUser(ctx, &model.User{Username: "other-local", PasswordHash: "other-fixture-hash", Role: model.RoleUser, Active: true}))
	if err := s.CreateSession(ctx, "own-session", user, "csrf", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(ctx, "foreign-session", other, "csrf", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, keep := range []string{"missing-session", "foreign-session"} {
		if err := s.ChangeOwnPassword(ctx, user, "old-fixture-hash", "new-fixture-hash", keep); !errors.Is(err, ErrNotFound) {
			t.Fatal("invalid session accepted", err)
		}
	}
	if err := s.ChangeOwnPassword(ctx, user, "stale-fixture-hash", "new-fixture-hash", "own-session"); !errors.Is(err, ErrNotFound) {
		t.Fatal("stale current-password proof accepted", err)
	}
	if err := s.ChangeOwnPassword(ctx, user, "old-fixture-hash", "new-fixture-hash", "own-session"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.GetSession(ctx, "foreign-session"); err != nil {
		t.Fatal("other account session revoked", err)
	}
	stored, err := s.GetUserByID(ctx, user)
	if err != nil || stored.PasswordHash != "new-fixture-hash" || !stored.PasswordLoginEnabled {
		t.Fatal("atomic password write not applied", err)
	}
}
