package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	gatewaycrypto "github.com/zjvtit/omni-gateway/internal/crypto"
	"github.com/zjvtit/omni-gateway/internal/model"
)

func TestPostgresProviderHealthOrderAndSecretRotation(t *testing.T) {
	s := groupTestStore(t)
	ctx := context.Background()
	old, _ := gatewaycrypto.NewCipher(strings.Repeat("11", 32))
	cipher, _ := gatewaycrypto.NewRotatingCipher(strings.Repeat("22", 32), []string{strings.Repeat("11", 32)})
	encrypted, _ := old.Encrypt("test secret")
	p := &model.Provider{Name: "upstream", Type: model.ProviderOpenAICompat, BaseURL: "https://example.com/v1", APIKeyEnc: encrypted, Active: true, MaxConcurrency: 2, TimeoutMS: 30000, SupportsResponses: true}
	id, err := s.CreateProvider(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkProviderCooldown(ctx, id, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkProviderHealthy(ctx, id); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetProvider(ctx, id)
	if err != nil || got.HealthStatus != "cooldown" || got.FailureCount != 1 || got.SuccessCount != 1 || got.MaxConcurrency != 2 || !got.SupportsResponses {
		t.Fatalf("health=%+v err=%v", got, err)
	}
	p.SupportsResponses = false
	p.ID = id
	if err := s.UpdateProvider(ctx, p); err != nil {
		t.Fatalf("update provider: %v", err)
	}
	if updated, err := s.GetProvider(ctx, id); err != nil || updated.SupportsResponses {
		t.Fatalf("supports_responses did not persist through UpdateProvider: %+v err=%v", updated, err)
	}
	if err := s.RecordProviderVerification(ctx, id, true, ""); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetProvider(ctx, id)
	if err != nil || got.HealthStatus != "available" || got.CooldownUntil != nil || got.LastVerifiedAt == nil {
		t.Fatalf("probe=%+v err=%v", got, err)
	}
	blocked := "upstream address is blocked by the network policy"
	if err := s.RecordProviderVerification(ctx, id, false, blocked); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetProvider(ctx, id)
	if err != nil || got.HealthStatus != "cooldown" || got.CooldownUntil == nil || got.LastError != blocked {
		t.Fatalf("failed probe=%+v err=%v", got, err)
	}
	a, _ := s.CreateAlias(ctx, "alias")
	b, _ := s.CreateAlias(ctx, "other")
	r1, _ := s.AddRouteEntry(ctx, a, id, "real-a", 10)
	r2, _ := s.AddRouteEntry(ctx, a, id, "real-b", 20)
	other, _ := s.AddRouteEntry(ctx, b, id, "real-c", 10)
	if err := s.ReorderRoutes(ctx, a, []int64{r2, r1}); err != nil {
		t.Fatal(err)
	}
	for _, ids := range [][]int64{{r1}, {r1, r1}, {r1, other}} {
		if err := s.ReorderRoutes(ctx, a, ids); !errors.Is(err, ErrRouteOrder) {
			t.Fatalf("invalid order accepted %v", err)
		}
	}
	routes, err := s.ListRouteEntries(ctx, a)
	if err != nil || routes[0].ID != r2 {
		t.Fatal("reorder rollback changed order")
	}
	if err := s.InsertContentLog(ctx, &model.ContentLog{RequestID: "content", Prompt: []byte(encrypted), Response: []byte(encrypted)}); err != nil {
		t.Fatal(err)
	}
	if err := s.RotateSecrets(ctx, cipher); err != nil {
		t.Fatal(err)
	}
	fresh, _ := gatewaycrypto.NewCipher(strings.Repeat("22", 32))
	got, _ = s.GetProvider(ctx, id)
	if text, err := fresh.Decrypt(got.APIKeyEnc); err != nil || text != "test secret" {
		t.Fatal("provider not rotated")
	}
	content, _ := s.GetContentLog(ctx, "content")
	if text, err := fresh.Decrypt(string(content.Prompt)); err != nil || text != "test secret" {
		t.Fatal("content not rotated")
	}
	if _, err := old.Decrypt(got.APIKeyEnc); err == nil {
		t.Fatal("still using old key")
	}
	before := got.APIKeyEnc
	if err := s.RotateSecrets(ctx, old); err == nil {
		t.Fatal("bad key accepted")
	}
	got, _ = s.GetProvider(ctx, id)
	if got.APIKeyEnc != before {
		t.Fatal("failed rotation was not atomic")
	}
}

func TestPostgresAccountingIdempotencyTimestampsAndAnalytics(t *testing.T) {
	s := groupTestStore(t)
	ctx := context.Background()
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	usage := model.NewUsage()
	usage.Set("input", 10)
	usage.Set("output", 5)
	usage.Set("total", 15)
	req := &model.Request{ID: "req", UserID: 3, APIKeyID: 4, API: "embeddings", ModelAlias: "alias", Status: model.StatusSuccess, StatusCode: 200, Usage: usage, Cost: model.NewMoneyInt(3), Credits: model.NewCreditsInt(5), TotalLatencyMS: 100, CreatedAt: at}
	a1 := &model.Attempt{RequestID: "req", SettlementID: "a1", ProviderID: 1, ProviderModel: "real", Status: "error", StatusCode: 503, Cost: model.NewMoneyInt(1), CreatedAt: at, LatencyMS: 20}
	a2 := &model.Attempt{RequestID: "req", SettlementID: "a2", ProviderID: 2, ProviderModel: "real", Status: "success", Cost: model.NewMoneyInt(2), CreatedAt: at.Add(time.Second), LatencyMS: 80}
	entries := []*model.LedgerEntry{{SettlementID: "a1", RequestID: "req", UserID: 3, APIKeyID: 4, ModelAlias: "alias", ProviderID: 1, ProviderModel: "real", Meter: "input", Quantity: 10, Cost: model.NewMoneyInt(1), Credits: model.ZeroCredits, PriceVersion: 1, CreatedAt: at}, {SettlementID: "a2", RequestID: "req", UserID: 3, APIKeyID: 4, ModelAlias: "alias", ProviderID: 2, ProviderModel: "real", Meter: "input", Quantity: 10, Cost: model.NewMoneyInt(2), Credits: model.NewCreditsInt(5), PriceVersion: 1, CreatedAt: at}}
	for i := 0; i < 2; i++ {
		if err := s.InsertRequest(ctx, req); err != nil {
			t.Fatal(err)
		}
		for _, a := range []*model.Attempt{a1, a2} {
			if err := s.InsertAttempt(ctx, a); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.InsertLedgerEntries(ctx, entries); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.GetRequest(ctx, "req")
	if err != nil || !got.CreatedAt.Equal(at) {
		t.Fatal("request timestamp not retained")
	}
	ledger, err := s.ListLedgerEntries(ctx, "req")
	if err != nil || len(ledger) != 2 || !ledger[0].CreatedAt.Equal(at) {
		t.Fatalf("ledger duplicates or timestamps: %v %v", ledger, err)
	}
	filteredCost, filteredCredits, err := s.SumLedgerInWindowExcludingSettlements(ctx, 3, 86400, []string{"a1"})
	if err != nil || !filteredCost.Equal(model.NewMoneyInt(2)) || !filteredCredits.Equal(model.NewMoneyInt(5)) {
		t.Fatalf("settlement exclusion cost=%s credits=%s err=%v", filteredCost, filteredCredits, err)
	}
	stats, err := s.Stats(ctx, model.StatsFilter{API: "embeddings"})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Requests != 1 || stats.FailoverCount != 1 || stats.P95LatencyMS != 100 || len(stats.Routes) != 2 || len(stats.Trend) != 1 || !stats.Credits.Equal(model.NewCreditsInt(5)) || !stats.Cost.Equal(model.NewMoneyInt(3)) || !stats.FailedAttemptCost.Equal(model.NewMoneyInt(1)) {
		t.Fatalf("bad stats %+v", stats)
	}
	stats, err = s.Stats(ctx, model.StatsFilter{API: "messages"})
	if err != nil || stats.Requests != 0 {
		t.Fatalf("API filter failed %+v %v", stats, err)
	}
	alias, err := s.CreateAlias(ctx, "alias")
	if err != nil {
		t.Fatal(err)
	}
	var group int64
	err = s.pool.QueryRow(ctx, `INSERT INTO model_groups(name) VALUES('g') RETURNING id`).Scan(&group)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(ctx, `INSERT INTO model_group_aliases(model_group_id,alias_id) VALUES($1,$2)`, group, alias); err != nil {
		t.Fatal(err)
	}
	stats, err = s.Stats(ctx, model.StatsFilter{ModelGroupID: group})
	if err != nil || stats.Requests != 1 {
		t.Fatalf("group filter failed: %v", err)
	}
	items, err := s.ListRequests(ctx, model.StatsFilter{API: "embeddings", ModelGroupID: group})
	if err != nil || len(items) != 1 {
		t.Fatalf("request filters %v", err)
	}
}
