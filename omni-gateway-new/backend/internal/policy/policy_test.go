package policy

import (
	"context"
	"testing"
	"time"

	"github.com/zjvtit/omni-gateway/internal/billing"
	"github.com/zjvtit/omni-gateway/internal/model"
)

type fakeStore struct {
	rateRules   map[string][]*model.RateLimit
	budgetRules []*model.BudgetRule
	cost        model.Money
	credits     model.Money
}

func (s *fakeStore) ListRateLimits(_ context.Context, scope string, scopeID int64) ([]*model.RateLimit, error) {
	return s.rateRules[scope+":"+stringValue(scopeID)], nil
}

func (s *fakeStore) ListBudgetRules(context.Context, int64) ([]*model.BudgetRule, error) {
	return s.budgetRules, nil
}

func (s *fakeStore) SumLedgerInWindow(context.Context, int64, int64) (model.Money, model.Credits, error) {
	return s.cost, s.credits, nil
}

func TestAcquireEnforcesRPMAndReleasesConcurrency(t *testing.T) {
	store := &fakeStore{rateRules: map[string][]*model.RateLimit{
		"user:7": {{ID: 1, Scope: "user", ScopeID: 7, RPM: 1, Concurrency: 1, Active: true}},
	}}
	service := NewService(store)
	service.now = func() time.Time { return time.Unix(100, 0) }
	release, err := service.Acquire(context.Background(), 7, 8, "chat")
	if err != nil {
		t.Fatalf("first Acquire returned error: %v", err)
	}
	if _, err := service.Acquire(context.Background(), 7, 8, "chat"); err != ErrRateLimited {
		t.Fatalf("second Acquire error = %v, want ErrRateLimited", err)
	}
	release()
	if _, err := service.Acquire(context.Background(), 7, 8, "chat"); err != ErrRateLimited {
		t.Fatalf("RPM window did not remain enforced: %v", err)
	}
}

func TestObserveUsageEnforcesTPMOnNextRequest(t *testing.T) {
	store := &fakeStore{rateRules: map[string][]*model.RateLimit{"user:7": {{Scope: "user", ScopeID: 7, TPM: 5, Active: true}}}}
	service := NewService(store)
	service.now = func() time.Time { return time.Unix(200, 0) }
	service.ObserveUsage(context.Background(), 7, 8, "chat", model.Usage{Meters: map[string]int64{"input": 3, "output": 2}})
	if _, err := service.Acquire(context.Background(), 7, 8, "chat"); err != ErrRateLimited {
		t.Fatalf("Acquire error = %v, want ErrRateLimited", err)
	}
	service.now = func() time.Time { return time.Unix(261, 0) }
	if _, err := service.Acquire(context.Background(), 7, 8, "chat"); err != nil {
		t.Fatalf("expired TPM window still rejected request: %v", err)
	}
}

func TestAcquireFiltersModelRulesAndIgnoresExhaustedBudget(t *testing.T) {
	store := &fakeStore{rateRules: map[string][]*model.RateLimit{
		"model:0": {
			{ID: 1, Scope: "model", ModelID: "other", RPM: 1},
			{ID: 2, Scope: "model", ModelID: "chat", RPM: 1},
		},
	}, budgetRules: []*model.BudgetRule{{Active: true, WindowSecs: 60, CostLimit: modelMoneyPtr("10")}}, cost: model.MustMoney("10")}
	service := NewService(store)
	initial, err := service.Acquire(context.Background(), 1, 2, "chat")
	if err != nil {
		t.Fatalf("historical exhausted budget blocked admission: %v", err)
	}
	initial()
	release, err := service.Acquire(context.Background(), 1, 2, "chat")
	if err != nil {
		t.Fatalf("chat rule should allow first request: %v", err)
	}
	release()
	if _, err := service.Acquire(context.Background(), 1, 2, "other"); err != nil {
		t.Fatalf("other model was incorrectly rate limited: %v", err)
	}
}

func (s *fakeStore) unused() {}

func modelMoneyPtr(value string) *model.Money {
	money := model.MustMoney(value)
	return &money
}

type policyPriceStore struct {
	price *model.PriceVersion
}

func (s *policyPriceStore) GetLatestPriceVersion(context.Context, string) (*model.PriceVersion, error) {
	return s.price, nil
}

type policyLedgerStore struct {
	store *fakeStore
}

func (s *policyLedgerStore) InsertLedgerEntries(_ context.Context, entries []*model.LedgerEntry) error {
	for _, entry := range entries {
		s.store.cost = s.store.cost.Add(entry.Cost)
		s.store.credits = s.store.credits.Add(entry.Credits)
	}
	return nil
}

func TestZeroRateSettlementsKeepIndependentLedgersWithoutBudgetAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, costRate, creditRate, wantCost, wantCredits string
	}{
		{name: "free", costRate: "0", creditRate: "0", wantCost: "0", wantCredits: "0"},
		{name: "cost only", costRate: "0.2", creditRate: "0", wantCost: "1", wantCredits: "0"},
		{name: "credits only", costRate: "0", creditRate: "0.3", wantCost: "0", wantCredits: "1.5"},
		{name: "paid", costRate: "0.2", creditRate: "0.3", wantCost: "1", wantCredits: "1.5"},
	} {
		for _, persisted := range []bool{false, true} {
			t.Run(tc.name+"/"+map[bool]string{false: "pending", true: "persisted"}[persisted], func(t *testing.T) {
				ctx := context.Background()
				store := &fakeStore{cost: model.ZeroMoney, credits: model.ZeroCredits, budgetRules: []*model.BudgetRule{{Active: true, WindowSecs: 3600, CostLimit: modelMoneyPtr("10"), CreditLimit: modelMoneyPtr("10")}}}
				counters := NewMemoryCounters()
				service := NewServiceWithCounters(store, counters)
				var ledgers billing.LedgerStore
				if persisted {
					ledgers = &policyLedgerStore{store: store}
				}
				settlement := billing.NewService(&policyPriceStore{price: &model.PriceVersion{Version: 1,
					Cost:    map[string]model.Money{"input": model.MustMoney(tc.costRate), "output": model.MustMoney(tc.costRate)},
					Credits: map[string]model.Credits{"input": model.MustCredits(tc.creditRate), "output": model.MustCredits(tc.creditRate)},
				}}, ledgers)
				defer settlement.Close(ctx)
				result, err := settlement.Settle(ctx, "budget-request", 7, 8, "chat", "", 9, model.Usage{Meters: map[string]int64{"input": 3, "output": 2, "total": 5}}, true)
				if err != nil || result == nil || result.Persisted != persisted || result.ZeroCreditMeters != 0 || result.Cost.String() != tc.wantCost || result.Credits.String() != tc.wantCredits {
					t.Fatalf("settlement=%+v err=%v", result, err)
				}
				if !result.Persisted {
					// Repeated reports of one failed write must reserve a charge once.
					for i := 0; i < 2; i++ {
						service.RecordPendingSettlement(ctx, 7, result.SettlementID, result.Cost, result.Credits)
					}
				}
				if tc.name == "free" {
					for i := 0; i < 10; i++ {
						service.RecordPendingSettlement(ctx, 7, "free-"+stringValue(int64(i)), model.ZeroMoney, model.ZeroCredits)
						service.RecordCharge(ctx, 7, model.ZeroMoney, model.ZeroCredits)
					}
					pendingCost, pendingCredits, ids := counters.PendingCharges(7, 3600, time.Now().UTC())
					if !pendingCost.IsZero() || !pendingCredits.IsZero() || len(ids) != 0 {
						t.Fatalf("free usage created a pending reservation: cost=%s credits=%s settlements=%v", pendingCost, pendingCredits, ids)
					}
				}
				remainingCost := model.MustMoney("10").Sub(result.Cost)
				remainingCredits := model.MustCredits("10").Sub(result.Credits)
				for _, probe := range []struct {
					cost    model.Money
					credits model.Credits
				}{
					{cost: remainingCost, credits: model.ZeroCredits},
					{cost: model.ZeroMoney, credits: remainingCredits},
				} {
					if err := service.CheckUsage(ctx, 7, probe.cost, probe.credits); err != nil {
						t.Fatalf("retired budget affected completed usage: %v", err)
					}
				}
				if err := service.CheckUsage(ctx, 7, remainingCost.Sub(model.MustMoney("0.001")), remainingCredits.Sub(model.MustCredits("0.001"))); err != nil {
					t.Fatalf("free ledger consumed budget or settlement was counted twice: %v", err)
				}
				release, err := service.Acquire(ctx, 7, 8, "chat")
				if err != nil {
					t.Fatalf("settlement unexpectedly exhausted budget: %v", err)
				}
				release()
			})
		}
	}
}

func TestFreeUsageStillEnforcesRPMTPMAndConcurrency(t *testing.T) {
	for _, kind := range []string{"RPM", "TPM", "concurrency"} {
		t.Run(kind, func(t *testing.T) {
			rule := &model.RateLimit{Scope: "user", ScopeID: 7, Active: true}
			switch kind {
			case "RPM":
				rule.RPM = 1
			case "TPM":
				rule.TPM = 6
			case "concurrency":
				rule.Concurrency = 1
			}
			ctx := context.Background()
			service := NewService(&fakeStore{rateRules: map[string][]*model.RateLimit{"user:7": {rule}}})
			settlement := billing.NewService(&policyPriceStore{price: &model.PriceVersion{Version: 1,
				Cost:    map[string]model.Money{"input": model.ZeroMoney, "output": model.ZeroMoney},
				Credits: map[string]model.Credits{"input": model.ZeroCredits, "output": model.ZeroCredits},
			}}, nil)
			defer settlement.Close(ctx)
			recordUsage := func() {
				usage := model.Usage{Meters: map[string]int64{"input": 3, "output": 2, "total": 5}}
				result, err := settlement.Settle(ctx, "free-request", 7, 8, "chat", "", 9, usage, true)
				if err != nil || result == nil || !result.Cost.IsZero() || !result.Credits.IsZero() || result.Unpriced || result.ZeroCreditMeters != 0 {
					t.Fatalf("free settlement=%+v err=%v", result, err)
				}
				service.ObserveUsage(ctx, 7, 8, "chat", usage)
				service.RecordPendingSettlement(ctx, 7, result.SettlementID, result.Cost, result.Credits)
			}
			release, err := service.Acquire(ctx, 7, 8, "chat")
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			if kind == "concurrency" {
				if _, err := service.Acquire(ctx, 7, 8, "chat"); err != ErrRateLimited {
					t.Fatalf("free in-flight request bypassed concurrency: %v", err)
				}
			}
			recordUsage()
			release()
			if kind == "RPM" {
				if _, err := service.Acquire(ctx, 7, 8, "chat"); err != ErrRateLimited {
					t.Fatalf("free request bypassed RPM: %v", err)
				}
				return
			}
			releaseNext, err := service.Acquire(ctx, 7, 8, "chat")
			if err != nil {
				t.Fatalf("released concurrency or 5 tokens below TPM=6 rejected: %v", err)
			}
			defer releaseNext()
			if kind == "TPM" {
				recordUsage()
				releaseNext()
				if _, err := service.Acquire(ctx, 7, 8, "chat"); err != ErrRateLimited {
					t.Fatalf("10 free tokens bypassed TPM=6: %v", err)
				}
			}
		})
	}
}

func TestHistoricalZeroAndExhaustedBudgetsDoNotAffectAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, costLimit, creditLimit, cost, credits string
	}{
		{name: "zero cost limit", costLimit: "0", creditLimit: "10", cost: "0", credits: "0"},
		{name: "zero credit limit", costLimit: "10", creditLimit: "0", cost: "0", credits: "0"},
		{name: "exhausted cost", costLimit: "10", creditLimit: "10", cost: "10", credits: "0"},
		{name: "exhausted credits", costLimit: "10", creditLimit: "10", cost: "0", credits: "10"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := NewService(&fakeStore{cost: model.MustMoney(tc.cost), credits: model.MustCredits(tc.credits),
				budgetRules: []*model.BudgetRule{{Active: true, WindowSecs: 3600, CostLimit: modelMoneyPtr(tc.costLimit), CreditLimit: modelMoneyPtr(tc.creditLimit)}},
			})
			release, err := service.Acquire(context.Background(), 7, 8, "free-model")
			if err != nil {
				t.Fatalf("historical budget blocked admission: %v", err)
			}
			release()
		})
	}
}
