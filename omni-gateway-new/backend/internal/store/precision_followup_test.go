package store

import (
	"context"
	"testing"
	"time"

	"github.com/zjvtit/omni-gateway/internal/model"
)

func TestCoverageFollowupPostgresOneEMinus1000CustomCostRoundTrip(t *testing.T) {
	// Use the same per-test schema isolation as free_usage_test.go.
	s := groupTestStore(t)
	ctx := context.Background()
	rate := model.MustMoney("1e-1000")
	price := &model.PriceVersion{ModelID: "upstream", Version: 31,
		Cost:    map[string]model.Money{"custom_audio_seconds": rate},
		Credits: map[string]model.Credits{"custom_audio_seconds": model.MustCredits("1e-1000")},
	}
	if _, err := s.CreatePriceVersion(ctx, price); err != nil {
		t.Fatal(err)
	}
	storedPrice, err := s.GetLatestPriceVersion(ctx, price.ModelID)
	if err != nil {
		t.Fatal(err)
	}
	if !storedPrice.Cost["custom_audio_seconds"].Equal(rate) || !storedPrice.Credits["custom_audio_seconds"].Equal(rate) || model.PriceStatus(storedPrice) != "priced" {
		t.Fatal("JSONB price snapshot lost the exact 1e-1000 positive rates")
	}
	at := time.Now().UTC().Truncate(time.Microsecond)
	usage := model.Usage{Meters: map[string]int64{"custom_audio_seconds": 7}, Estimated: true}
	cost := storedPrice.Cost["custom_audio_seconds"].Mul(model.NewMoneyInt(7))
	credits := model.RoundCredits(storedPrice.Credits["custom_audio_seconds"].Mul(model.NewCreditsInt(7)))
	wantCost := model.MustMoney("7e-1000")
	if !cost.IsPositive() || !cost.Equal(wantCost) || !credits.IsZero() {
		t.Fatal("exact tiny cost or legitimate credit quantization was lost before persistence")
	}
	if err := s.InsertLedgerEntries(ctx, []*model.LedgerEntry{{SettlementID: "tiny-custom-attempt", RequestID: "tiny-custom-request", UserID: 3, APIKeyID: 4, ModelAlias: "alias", ProviderID: 9, ProviderModel: price.ModelID,
		Meter: "custom_audio_seconds", Quantity: 7, PriceVersion: 31, Estimated: true, CreatedAt: at, Cost: cost, Credits: credits}}); err != nil {
		t.Fatal(err)
	}
	request := &model.Request{ID: "tiny-custom-request", UserID: 3, APIKeyID: 4, API: "chat_completions", ModelAlias: "alias",
		Status: model.StatusSuccess, StatusCode: 200, Usage: usage, Cost: cost, Credits: credits, CreatedAt: at}
	if err := s.InsertRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertAttempt(ctx, &model.Attempt{RequestID: request.ID, SettlementID: "tiny-custom-attempt", ProviderID: 9, ProviderModel: price.ModelID,
		Status: "success", StatusCode: 200, Usage: usage, Cost: cost, SettlementStatus: "settled", CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
	entries, err := s.ListLedgerEntries(ctx, request.ID)
	if err != nil || len(entries) != 1 {
		t.Fatalf("tiny custom ledger roundtrip: rows=%d err=%v", len(entries), err)
	}
	entry := entries[0]
	if !entry.Cost.IsPositive() || !entry.Cost.Equal(wantCost) || !entry.Credits.IsZero() || entry.Quantity != 7 || entry.PriceVersion != 31 || entry.Meter != "custom_audio_seconds" || !entry.Estimated {
		t.Fatal("NUMERIC tiny-cost readback lost positive amount, quantity, meter, version, or provenance")
	}
	var numericColumn bool
	var databasePositive bool
	var databaseExact bool
	if err := s.pool.QueryRow(ctx, `SELECT pg_typeof(cost)='numeric'::regtype, cost>0, cost=$2::numeric FROM usage_ledger WHERE request_id=$1`, request.ID, "7e-1000").Scan(&numericColumn, &databasePositive, &databaseExact); err != nil {
		t.Fatal(err)
	}
	if !numericColumn || !databasePositive || !databaseExact {
		t.Fatal("real PostgreSQL NUMERIC column failed exact 7e-1000 persistence")
	}
	gotRequest, err := s.GetRequest(ctx, request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !gotRequest.Cost.Equal(wantCost) || !gotRequest.Cost.IsPositive() || !gotRequest.Credits.IsZero() || gotRequest.SettlementStatus != "settled" {
		t.Fatal("request projection confused positive rates with rounded zero credits")
	}
	attempts, err := s.ListAttempts(ctx, request.ID)
	if err != nil || len(attempts) != 1 || !attempts[0].Cost.Equal(wantCost) || attempts[0].Usage.Get(entry.Meter) != 7 {
		t.Fatal("tiny cost or custom quantity missing from persisted attempt")
	}
}
