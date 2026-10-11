package billing

import (
	"context"
	"testing"

	"github.com/zjvtit/omni-gateway/internal/model"
)

func TestRateDerivedSettlementStatusesPreserveCustomMeters(t *testing.T) {
	for _, tc := range []struct{ name, cost, credit, status string }{
		{"explicit custom free", "0", "0", "free"},
		{"credits waived", "0.1234567890123456789", "0", "credits_free"},
		{"tiny positive credits", "0", "0.000000000000000000001", "settled"},
		{"tiny positive cost", "0.000000000000000000001", "0.000000000000000000001", "settled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			price := &model.PriceVersion{ModelID: "upstream", Version: 17, Cost: map[string]model.Money{"custom_audio": model.MustMoney(tc.cost)}, Credits: map[string]model.Credits{"custom_audio": model.MustCredits(tc.credit)}}
			ledger := &fakeLedgerStore{done: make(chan struct{})}
			service := NewService(&fakePriceStore{prices: map[string]*model.PriceVersion{"upstream": price}}, ledger)
			defer service.Close(context.Background())
			result, err := service.Settle(context.Background(), "custom", 1, 2, "alias", "upstream", 3, model.Usage{Meters: map[string]int64{"custom_audio": 7}, Estimated: true}, true)
			if err != nil || result.SettlementStatus != tc.status || !result.Persisted || len(result.Entries) != 1 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			entry := result.Entries[0]
			if entry.Quantity != 7 || entry.Meter != "custom_audio" || entry.PriceVersion != 17 || !entry.Estimated || !entry.Cost.Equal(model.MustMoney(tc.cost).Mul(model.NewMoneyInt(7))) {
				t.Fatalf("custom precision or quantity lost: %+v", entry)
			}
			if tc.status == "free" && result.ZeroCreditMeters != 0 {
				t.Fatal("explicit free counted as anomaly")
			}
			if tc.credit != "0" && result.ZeroCreditMeters != 1 {
				t.Fatal("tiny positive credits incorrectly treated as explicit free")
			}
		})
	}
}

func TestUnpricedCustomUsageNeverBecomesFree(t *testing.T) {
	price := &model.PriceVersion{Version: 1, Cost: map[string]model.Money{"input": model.ZeroMoney}, Credits: map[string]model.Credits{"input": model.ZeroCredits}}
	service := NewService(&fakePriceStore{prices: map[string]*model.PriceVersion{"alias": price}}, nil)
	defer service.Close(context.Background())
	result, err := service.Settle(context.Background(), "unknown-meter", 1, 2, "alias", "", 3, model.Usage{Meters: map[string]int64{"input": 5, "custom_audio": 7, "total": 5}}, true)
	if err != nil || result.SettlementStatus != "unpriced" || len(result.Entries) != 3 {
		t.Fatalf("unrated custom meter was called free or lost: %+v %v", result, err)
	}
	none, err := service.Settle(context.Background(), "no-positive", 1, 2, "missing", "", 3, model.Usage{Meters: map[string]int64{"input": 0}}, true)
	if err != nil || none.Unpriced || none.SettlementStatus != "none" || len(none.Entries) != 0 {
		t.Fatalf("zero quantity status=%+v %v", none, err)
	}
}

func TestCoverageFollowupExactOneEMinus1000CustomSettlement(t *testing.T) {
	ctx := context.Background()
	rate := model.MustMoney("1e-1000")
	if !rate.IsPositive() || rate.Exponent() != -1000 {
		t.Fatal("exact 1e-1000 fixture did not retain its positive decimal exponent")
	}
	price := &model.PriceVersion{ModelID: "upstream", Version: 31,
		Cost:    map[string]model.Money{"custom_audio_seconds": rate},
		Credits: map[string]model.Credits{"custom_audio_seconds": model.MustCredits("1e-1000")},
	}
	if status := model.PriceStatus(price); status != "priced" {
		t.Fatalf("positive 1e-1000 rates classified as %s", status)
	}
	ledger := &fakeLedgerStore{done: make(chan struct{})}
	service := NewService(&fakePriceStore{prices: map[string]*model.PriceVersion{"upstream": price}}, ledger)
	defer service.Close(ctx)
	result, err := service.Settle(ctx, "exact-tiny-custom", 1, 2, "alias", "upstream", 3,
		model.Usage{Meters: map[string]int64{"custom_audio_seconds": 7}, Estimated: true}, true)
	if err != nil {
		t.Fatal(err)
	}
	wantCost := model.MustMoney("7e-1000")
	if result.SettlementStatus != "settled" || result.Unpriced || !result.Persisted || result.ZeroCreditMeters != 1 ||
		!result.Cost.IsPositive() || !result.Cost.Equal(wantCost) || !result.Credits.IsZero() {
		t.Fatal("tiny positive custom settlement lost exact cost, misreported free, or mishandled rounded credits")
	}
	if len(result.Entries) != 1 || len(ledger.entries) != 1 {
		t.Fatal("positive custom quantity did not produce exactly one persisted ledger entry")
	}
	for _, entry := range []*model.LedgerEntry{result.Entries[0], ledger.entries[0]} {
		if entry.Meter != "custom_audio_seconds" || entry.Quantity != 7 || entry.PriceVersion != 31 || !entry.Estimated ||
			!entry.Cost.IsPositive() || !entry.Cost.Equal(wantCost) || !entry.Credits.IsZero() {
			t.Fatal("tiny-positive ledger lost quantity, immutable version, usage provenance, or exact amount")
		}
	}
}
