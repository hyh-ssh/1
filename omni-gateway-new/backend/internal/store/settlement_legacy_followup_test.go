package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/zjvtit/omni-gateway/internal/model"
)

func TestCoverageFollowupLegacySettlementRequiresAllZeroCandidateSnapshots(t *testing.T) {
	for _, tc := range []struct {
		name, aliasCost, aliasCredits, providerCost, providerCredits string
		addProvider                                                  bool
		initialStatus, afterStatus                                   string
	}{
		{name: "one complete zero snapshot is free", aliasCost: "0", aliasCredits: "0", initialStatus: "free", afterStatus: "free"},
		{name: "positive cost and zero credits", aliasCost: "0.2", aliasCredits: "0", initialStatus: "credits_free", afterStatus: "credits_free"},
		{name: "all matching numeric versions prove free", aliasCost: "0", aliasCredits: "0", providerCost: "0", providerCredits: "0", initialStatus: "free", afterStatus: "free"},
		{name: "one matching cost rate is positive", aliasCost: "0", aliasCredits: "0", providerCost: "1e-1000", providerCredits: "0", initialStatus: "credits_free", afterStatus: "credits_free"},
		{name: "one matching credit rate is positive despite rounded amount", aliasCost: "0", aliasCredits: "0", providerCost: "0", providerCredits: "1e-1000", initialStatus: "settled", afterStatus: "settled"},
		{name: "later zero upstream snapshot cannot relabel historical paid usage free", aliasCost: "1e-1000", aliasCredits: "1e-1000", providerCost: "0", providerCredits: "0", addProvider: true, initialStatus: "settled", afterStatus: "settled"},
		{name: "new positive candidate requires conservative legacy classification", aliasCost: "0", aliasCredits: "0", providerCost: "1e-1000", providerCredits: "0", addProvider: true, initialStatus: "free", afterStatus: "credits_free"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Seed before 0010: neither the request nor its attempt has a new
			// rate-derived settlement status. Only immutable legacy ledgers and
			// numeric version references can establish the projected state.
			s := productLegacyStore(t)
			ctx := context.Background()
			makePrice := func(modelID, cost, credits string, version int) int64 {
				t.Helper()
				id, err := s.CreatePriceVersion(ctx, &model.PriceVersion{ModelID: modelID, Version: version,
					Cost:    map[string]model.Money{"custom_audio_seconds": model.MustMoney(cost)},
					Credits: map[string]model.Credits{"custom_audio_seconds": model.MustCredits(credits)}})
				if err != nil {
					t.Fatal(err)
				}
				return id
			}
			oldPriceMax := makePrice("legacy-alias", tc.aliasCost, tc.aliasCredits, 41)
			if tc.providerCost != "" && !tc.addProvider {
				oldPriceMax = makePrice("legacy-upstream", tc.providerCost, tc.providerCredits, 41)
			}
			at := time.Now().UTC().Truncate(time.Microsecond)
			usage := model.Usage{Meters: map[string]int64{"custom_audio_seconds": 7}, Estimated: true}
			cost := model.MustMoney(tc.aliasCost).Mul(model.NewMoneyInt(7))
			credits := model.RoundCredits(model.MustCredits(tc.aliasCredits).Mul(model.NewCreditsInt(7)))
			request := &model.Request{ID: "legacy-followup-request", UserID: 3, APIKeyID: 4, API: "chat_completions", ModelAlias: "legacy-alias", Status: model.StatusSuccess, StatusCode: 200, Usage: usage, Cost: cost, Credits: credits, CreatedAt: at}
			if err := s.InsertRequest(ctx, request); err != nil {
				t.Fatal(err)
			}
			if _, err := s.pool.Exec(ctx, `INSERT INTO attempts(request_id,provider_id,provider_model,status,status_code,settlement_id,usage,cost,created_at) VALUES($1,9,'legacy-upstream','success',200,'legacy-followup-attempt',$2,$3,$4)`, request.ID, `{"meters":{"custom_audio_seconds":7},"estimated":true}`, cost, at); err != nil {
				t.Fatal(err)
			}
			if err := s.InsertLedgerEntries(ctx, []*model.LedgerEntry{{SettlementID: "legacy-followup-attempt", RequestID: request.ID, UserID: 3, APIKeyID: 4, ModelAlias: request.ModelAlias, ProviderID: 9, ProviderModel: "legacy-upstream", Meter: "custom_audio_seconds", Quantity: 7, PriceVersion: 41, Estimated: true, Cost: cost, Credits: credits, CreatedAt: at}}); err != nil {
				t.Fatal(err)
			}
			oldRows := func() map[string]string {
				t.Helper()
				rows := map[string]string{}
				for table, query := range map[string]string{
					"requests":     `SELECT jsonb_agg(to_jsonb(t) ORDER BY id)::text FROM requests t`,
					"attempts":     `SELECT jsonb_agg(to_jsonb(t)-'settlement_status' ORDER BY id)::text FROM attempts t`,
					"usage_ledger": `SELECT jsonb_agg(to_jsonb(t) ORDER BY id)::text FROM usage_ledger t`,
				} {
					if err := s.pool.QueryRow(ctx, query).Scan(&rowsValueSink{target: rows, key: table}); err != nil {
						t.Fatal(err)
					}
				}
				var prices string
				if err := s.pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(t) ORDER BY id)::text FROM price_versions t WHERE id<=$1`, oldPriceMax).Scan(&prices); err != nil {
					t.Fatal(err)
				}
				rows["price_versions"] = prices
				return rows
			}
			before := oldRows()
			migration, err := migrationsFS.ReadFile("migrations/0010_product_observability.sql")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.pool.Exec(ctx, string(migration)); err != nil {
				t.Fatal(err)
			}
			assertProjection := func(want string) {
				t.Helper()
				got, err := s.GetRequest(ctx, request.ID)
				if err != nil {
					t.Fatal(err)
				}
				if got.SettlementStatus != want {
					t.Fatalf("legacy status=%s want=%s", got.SettlementStatus, want)
				}
				listed, err := s.ListRequests(ctx, model.StatsFilter{RequestID: request.ID, SettlementStatus: want})
				if err != nil || len(listed) != 1 || listed[0].SettlementStatus != want {
					t.Fatal("legacy status filtering disagrees with detail projection", err)
				}
				attempts, err := s.ListAttempts(ctx, request.ID)
				if err != nil || len(attempts) != 1 || attempts[0].SettlementStatus != "" {
					t.Fatal("legacy attempt received fabricated status", err)
				}
				ledger, err := s.ListLedgerEntries(ctx, request.ID)
				if err != nil || len(ledger) != 1 || ledger[0].Quantity != 7 || ledger[0].PriceVersion != 41 || !ledger[0].Cost.Equal(cost) || !ledger[0].Credits.Equal(credits) {
					t.Fatal("historical ledger amount or context changed", err)
				}
			}
			assertProjection(tc.initialStatus)
			if !reflect.DeepEqual(before, oldRows()) {
				t.Fatal("0010 migration rewrote historical rows")
			}
			if tc.addProvider {
				makePrice("legacy-upstream", tc.providerCost, tc.providerCredits, 41)
			}
			// Latest prices and unrelated models are not the legacy price source.
			makePrice("legacy-alias", "1", "1", 42)
			makePrice("unrelated-model", "1", "1", 41)
			assertProjection(tc.afterStatus)
			if !reflect.DeepEqual(before, oldRows()) {
				t.Fatal("adding new snapshots retroactively rewrote historical request, attempt, ledger, or price rows")
			}
		})
	}
}

// A small Scan sink keeps the snapshot maps local without exposing row values
// (including old metadata) in failure output.
type rowsValueSink struct {
	target map[string]string
	key    string
}

func (s *rowsValueSink) Scan(value any) error {
	switch v := value.(type) {
	case string:
		s.target[s.key] = v
	case []byte:
		s.target[s.key] = string(v)
	}
	return nil
}
