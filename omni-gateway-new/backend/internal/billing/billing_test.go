package billing

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/zjvtit/omni-gateway/internal/model"
	"github.com/zjvtit/omni-gateway/internal/store"
)

type fakePriceStore struct {
	prices map[string]*model.PriceVersion
}

func (s *fakePriceStore) GetLatestPriceVersion(_ context.Context, modelID string) (*model.PriceVersion, error) {
	price, ok := s.prices[modelID]
	if !ok {
		return nil, store.ErrNotFound
	}
	return price, nil
}

type fakeLedgerStore struct {
	mu      sync.Mutex
	entries []*model.LedgerEntry
	done    chan struct{}
}

func (s *fakeLedgerStore) InsertLedgerEntries(_ context.Context, entries []*model.LedgerEntry) error {
	s.mu.Lock()
	s.entries = append(s.entries, entries...)
	s.mu.Unlock()
	close(s.done)
	return nil
}

func TestSettleUsesVersionedPricesAndWritesLedger(t *testing.T) {
	ledgers := &fakeLedgerStore{done: make(chan struct{})}
	service := NewService(&fakePriceStore{prices: map[string]*model.PriceVersion{
		"provider-model": {
			ID:      12,
			Version: 3,
			ModelID: "provider-model",
			Cost:    map[string]model.Money{"input": model.MustMoney("0.002"), "output": model.MustMoney("0.004")},
			Credits: map[string]model.Money{"input": model.MustMoney("0.005"), "output": model.MustMoney("0.010")},
		},
	}}, ledgers)

	result, err := service.Settle(context.Background(), "req-1", 2, 3, "chat", "provider-model", 4, model.Usage{Meters: map[string]int64{"input": 1000, "output": 100}}, true)
	if err != nil {
		t.Fatalf("Settle returned error: %v", err)
	}
	if result.Cost.String() != "2.4" || result.Credits.String() != "6" {
		t.Fatalf("unexpected totals: cost=%s credits=%s", result.Cost, result.Credits)
	}
	select {
	case <-ledgers.done:
	case <-time.After(2 * time.Second):
		t.Fatal("ledger was not persisted")
	}
	ledgers.mu.Lock()
	defer ledgers.mu.Unlock()
	if len(ledgers.entries) != 2 {
		t.Fatalf("got %d ledger entries, want 2", len(ledgers.entries))
	}
	for _, entry := range ledgers.entries {
		if entry.RequestID != "req-1" || entry.PriceVersion != 3 || entry.ModelAlias != "chat" || entry.ProviderID != 4 {
			t.Fatalf("unexpected ledger context: %#v", entry)
		}
	}
}

func TestSettleChargesCostButNotCreditsForFailedAttempt(t *testing.T) {
	service := NewService(&fakePriceStore{prices: map[string]*model.PriceVersion{
		"alias": {Version: 1, Cost: map[string]model.Money{"output": model.MustMoney("0.01")}, Credits: map[string]model.Credits{"output": model.MustCredits("0.02")}},
	}}, nil)
	result, err := service.Settle(context.Background(), "req-2", 1, 1, "alias", "missing-price", 1, model.Usage{Meters: map[string]int64{"output": 2}}, false)
	if err != nil {
		t.Fatalf("Settle returned error: %v", err)
	}
	if result.Cost.String() != "0.02" || !result.Credits.IsZero() {
		t.Fatalf("unexpected failed-attempt totals: cost=%s credits=%s", result.Cost, result.Credits)
	}
}

func TestSettleFlagsUnpricedWhenNoPriceVersionMatches(t *testing.T) {
	// Billable usage with no matching price version must not invent a
	// zero-amount ledger row: it reports Unpriced so the missing configuration
	// stays observable.
	service := NewService(&fakePriceStore{prices: map[string]*model.PriceVersion{}}, nil)
	result, err := service.Settle(context.Background(), "req-unpriced", 1, 2, "alias", "provider-model", 3, model.Usage{Meters: map[string]int64{"input": 1000}}, true)
	if err != nil {
		t.Fatalf("Settle returned error: %v", err)
	}
	if !result.Unpriced {
		t.Fatal("expected Unpriced when no price version matches")
	}
	if len(result.Entries) != 0 {
		t.Fatalf("expected no ledger rows for an unpriced attempt, got %d", len(result.Entries))
	}
	if !result.Cost.IsZero() || !result.Credits.IsZero() {
		t.Fatalf("unpriced attempt must not carry totals: cost=%s credits=%s", result.Cost, result.Credits)
	}
	if result.SettlementID == "" {
		t.Fatal("expected a settlement id even when unpriced")
	}
}

func TestSettleTreatsEmptyUsageAsNothingToBill(t *testing.T) {
	// A failed attempt with no observed usage has nothing to price. That is not
	// a pricing misconfiguration, so it must not be counted as unpriced.
	service := NewService(&fakePriceStore{prices: map[string]*model.PriceVersion{}}, nil)
	result, err := service.Settle(context.Background(), "req-empty", 1, 2, "alias", "provider-model", 3, model.NewUsage(), false)
	if err != nil {
		t.Fatalf("Settle returned error: %v", err)
	}
	if result.Unpriced {
		t.Fatal("empty usage must not be reported as unpriced")
	}
	if len(result.Entries) != 0 {
		t.Fatalf("expected no ledger rows, got %d", len(result.Entries))
	}
}

func TestSettleWithoutPriceStoreFlagsBillableUsage(t *testing.T) {
	service := NewService(nil, nil)
	result, err := service.Settle(context.Background(), "req-no-store", 1, 2, "alias", "provider-model", 3, model.Usage{Meters: map[string]int64{"output": 10}}, true)
	if err != nil {
		t.Fatalf("Settle returned error: %v", err)
	}
	if !result.Unpriced {
		t.Fatal("expected Unpriced when no price store is configured and usage is billable")
	}
}

func TestSettleKeepsPricedAttemptsOffTheUnpricedPath(t *testing.T) {
	service := NewService(&fakePriceStore{prices: map[string]*model.PriceVersion{
		"alias": {Version: 2, Cost: map[string]model.Money{"input": model.MustMoney("0.001")}, Credits: map[string]model.Credits{"input": model.MustCredits("0.0002")}},
	}}, nil)
	result, err := service.Settle(context.Background(), "req-priced", 1, 2, "alias", "provider-model", 3, model.Usage{Meters: map[string]int64{"input": 100}}, true)
	if err != nil {
		t.Fatalf("Settle returned error: %v", err)
	}
	if result.Unpriced {
		t.Fatal("priced attempt must not be flagged unpriced")
	}
	if result.Cost.String() != "0.1" || len(result.Entries) != 1 {
		t.Fatalf("unexpected priced settlement: %+v", result)
	}
	if result.ZeroCreditMeters != 0 {
		t.Fatalf("a charged attempt must not be counted as zero-credit: %+v", result)
	}
}

func TestSettleCountsBillableMetersThatChargeNoCredits(t *testing.T) {
	// Two ways a priced, successful attempt can charge nothing: the price version
	// declares no credit rate for the meter, or the rate is so small that the
	// three-decimal amount scale quantizes it away. Both still accrue upstream
	// cost, so both must be counted instead of passing silently.
	service := NewService(&fakePriceStore{prices: map[string]*model.PriceVersion{
		"alias": {Version: 4,
			Cost:    map[string]model.Money{"input": model.MustMoney("0.001"), "output": model.MustMoney("0.002")},
			Credits: map[string]model.Credits{"input": model.MustCredits("0.0000001")},
		},
	}}, nil)
	result, err := service.Settle(context.Background(), "req-zero-credits", 1, 2, "alias", "provider-model", 3, model.Usage{Meters: map[string]int64{"input": 10, "output": 5}}, true)
	if err != nil {
		t.Fatalf("Settle returned error: %v", err)
	}
	if result.Unpriced {
		t.Fatal("a matched price version must not be reported as unpriced")
	}
	if result.ZeroCreditMeters != 2 {
		t.Fatalf("zero-credit meters = %d, want 2 (one absent rate, one quantized away)", result.ZeroCreditMeters)
	}
	if !result.Credits.IsZero() || result.Cost.String() != "0.02" {
		t.Fatalf("unexpected totals: cost=%s credits=%s", result.Cost, result.Credits)
	}
	if len(result.Entries) != 2 {
		t.Fatalf("got %d ledger entries, want 2", len(result.Entries))
	}
}

func TestSettleDoesNotCountCreditsItDidCharge(t *testing.T) {
	service := NewService(&fakePriceStore{prices: map[string]*model.PriceVersion{
		"alias": {Version: 5,
			Cost:    map[string]model.Money{"input": model.MustMoney("0.000002")},
			Credits: map[string]model.Credits{"input": model.MustCredits("0.0002")},
		},
	}}, nil)
	// 11 * 0.0002 = 0.0022 credits, which survives the three-decimal scale.
	result, err := service.Settle(context.Background(), "req-paid", 1, 2, "alias", "provider-model", 3, model.Usage{Meters: map[string]int64{"input": 11}}, true)
	if err != nil {
		t.Fatalf("Settle returned error: %v", err)
	}
	if result.ZeroCreditMeters != 0 || result.Credits.String() != "0.002" {
		t.Fatalf("charged attempt: credits=%s zero-credit meters=%d", result.Credits, result.ZeroCreditMeters)
	}
	// A failed attempt charges no credits by design, so it must not be counted.
	failed, err := service.Settle(context.Background(), "req-paid-failed", 1, 2, "alias", "provider-model", 3, model.Usage{Meters: map[string]int64{"input": 11}}, false)
	if err != nil {
		t.Fatalf("Settle returned error: %v", err)
	}
	if failed.ZeroCreditMeters != 0 || !failed.Credits.IsZero() {
		t.Fatalf("failed attempt: credits=%s zero-credit meters=%d, want zero credits and no count", failed.Credits, failed.ZeroCreditMeters)
	}
}

func TestSettleIgnoresMetersNeitherLedgerPrices(t *testing.T) {
	// Adapters report a "total" meter next to the billed ones, and no price version
	// declares a rate for it in either ledger. Observed in the e2e run: counting
	// that meter made the zero-credit signal fire on every single request, which
	// would make it worthless. It must stay uncounted, while the ledger still
	// records the observed meter with zero amounts in both ledgers.
	service := NewService(&fakePriceStore{prices: map[string]*model.PriceVersion{
		"alias": {Version: 6,
			Cost:    map[string]model.Money{"input": model.MustMoney("0.000002")},
			Credits: map[string]model.Credits{"input": model.MustCredits("0.0002")},
		},
	}}, nil)
	result, err := service.Settle(context.Background(), "req-total-meter", 1, 2, "alias", "provider-model", 3, model.Usage{Meters: map[string]int64{"input": 11, "total": 11}}, true)
	if err != nil {
		t.Fatalf("Settle returned error: %v", err)
	}
	if result.ZeroCreditMeters != 0 {
		t.Fatalf("zero-credit meters = %d, want 0: an unrated aggregate is not billable", result.ZeroCreditMeters)
	}
	if result.Credits.String() != "0.002" {
		t.Fatalf("credits = %s, want 0.002 (11 * 0.0002)", result.Credits)
	}
	var totalRows int
	for _, entry := range result.Entries {
		if entry.Meter != "total" {
			continue
		}
		totalRows++
		if !entry.Cost.IsZero() || !entry.Credits.IsZero() {
			t.Fatalf("unrated total row = cost %s credits %s, want both zero", entry.Cost, entry.Credits)
		}
	}
	if totalRows != 1 {
		t.Fatalf("total meter rows = %d, want 1", totalRows)
	}
}

func TestSettleExplicitZeroRatesPreserveUsageAndLedgerContext(t *testing.T) {
	for _, tc := range []struct {
		name, costRate, creditRate, wantCost, wantCredits string
		wantZeroCreditMeters                              int
	}{
		{name: "free", costRate: "0", creditRate: "0", wantCost: "0", wantCredits: "0"},
		{name: "cost only", costRate: "0.002", creditRate: "0", wantCost: "2.2", wantCredits: "0"},
		{name: "credits only", costRate: "0", creditRate: "0.005", wantCost: "0", wantCredits: "5.5"},
		{name: "paid", costRate: "0.002", creditRate: "0.005", wantCost: "2.2", wantCredits: "5.5"},
		{name: "missing credits with cost", costRate: "0.002", wantCost: "2.2", wantCredits: "0", wantZeroCreditMeters: 2},
		{name: "rounded credits with cost", costRate: "0.002", creditRate: "0.0000001", wantCost: "2.2", wantCredits: "0", wantZeroCreditMeters: 2},
		{name: "rounded credits without cost", costRate: "0", creditRate: "0.0000001", wantCost: "0", wantCredits: "0", wantZeroCreditMeters: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			price := &model.PriceVersion{Version: 7,
				Cost:    map[string]model.Money{"input": model.MustMoney(tc.costRate), "output": model.MustMoney(tc.costRate)},
				Credits: make(map[string]model.Credits),
			}
			if tc.creditRate != "" {
				price.Credits["input"] = model.MustCredits(tc.creditRate)
				price.Credits["output"] = model.MustCredits(tc.creditRate)
			}
			ledgers := &fakeLedgerStore{done: make(chan struct{})}
			service := NewService(&fakePriceStore{prices: map[string]*model.PriceVersion{"provider-model": price}}, ledgers)
			defer service.Close(context.Background())
			at := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
			usage := model.Usage{Meters: map[string]int64{"input": 1000, "output": 100, "total": 1100, "empty": 0, "negative": -1}, Estimated: true}
			ctx := WithAttempt(context.Background(), "settlement-zero-rates", at)
			result, err := service.Settle(ctx, "request-zero-rates", 2, 3, "alias", "provider-model", 4, usage, true)
			if err != nil {
				t.Fatal(err)
			}
			if !result.Persisted || result.Unpriced || result.SettlementID != "settlement-zero-rates" || result.Cost.String() != tc.wantCost || result.Credits.String() != tc.wantCredits || result.ZeroCreditMeters != tc.wantZeroCreditMeters {
				t.Fatalf("settlement=%+v, want cost=%s credits=%s diagnostics=%d", result, tc.wantCost, tc.wantCredits, tc.wantZeroCreditMeters)
			}
			ledgers.mu.Lock()
			defer ledgers.mu.Unlock()
			if len(result.Entries) != 3 || len(ledgers.entries) != 3 {
				t.Fatalf("positive usage lost: result rows=%d persisted rows=%d", len(result.Entries), len(ledgers.entries))
			}
			meters := make(map[string]bool)
			cost, credits := model.ZeroMoney, model.ZeroCredits
			for _, entry := range ledgers.entries {
				if meters[entry.Meter] || entry.Quantity <= 0 || entry.Quantity != usage.Meters[entry.Meter] || entry.SettlementID != result.SettlementID || !entry.CreatedAt.Equal(at) || entry.RequestID != "request-zero-rates" || entry.UserID != 2 || entry.APIKeyID != 3 || entry.ModelAlias != "alias" || entry.ProviderModel != "provider-model" || entry.ProviderID != 4 || entry.PriceVersion != 7 || !entry.Estimated {
					t.Fatalf("ledger context or quantity changed: %+v", entry)
				}
				meters[entry.Meter] = true
				cost = cost.Add(entry.Cost)
				credits = credits.Add(entry.Credits)
				if entry.Meter == "total" && (!entry.Cost.IsZero() || !entry.Credits.IsZero()) {
					t.Fatalf("unrated aggregate must not be charged: %+v", entry)
				}
			}
			if cost.String() != tc.wantCost || credits.String() != tc.wantCredits {
				t.Fatalf("persisted totals: cost=%s credits=%s", cost, credits)
			}
		})
	}
}
