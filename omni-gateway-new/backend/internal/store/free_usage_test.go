package store

import (
	"context"
	"testing"
	"time"

	"github.com/zjvtit/omni-gateway/internal/model"
)

func TestPostgresZeroRateUsagePersistsIdempotentlyAndRemainsInStats(t *testing.T) {
	for _, tc := range []struct {
		name, costRate, creditRate, wantCost, wantCredits string
	}{
		{name: "free", costRate: "0", creditRate: "0", wantCost: "0", wantCredits: "0"},
		{name: "cost only", costRate: "0.01", creditRate: "0", wantCost: "0.15", wantCredits: "0"},
		{name: "credits only", costRate: "0", creditRate: "0.2", wantCost: "0", wantCredits: "3"},
		{name: "paid", costRate: "0.01", creditRate: "0.2", wantCost: "0.15", wantCredits: "3"},
	} {
		for _, estimated := range []bool{false, true} {
			t.Run(tc.name+"/"+map[bool]string{false: "reported", true: "estimated"}[estimated], func(t *testing.T) {
				// groupTestStore creates and drops an independent temporary schema.
				s := groupTestStore(t)
				ctx := context.Background()
				before, err := s.Stats(ctx, model.StatsFilter{})
				if err != nil || before.Requests != 0 || before.TotalTokens != 0 || !before.Cost.IsZero() || !before.Credits.IsZero() {
					t.Fatalf("unexpected initial stats: %+v err=%v", before, err)
				}
				price := &model.PriceVersion{ModelID: "provider-model", Version: 7,
					Cost:    map[string]model.Money{"input": model.MustMoney(tc.costRate), "output": model.MustMoney(tc.costRate)},
					Credits: map[string]model.Credits{"input": model.MustCredits(tc.creditRate), "output": model.MustCredits(tc.creditRate)},
				}
				priceID, err := s.CreatePriceVersion(ctx, price)
				if err != nil {
					t.Fatal(err)
				}
				gotPrice, err := s.GetLatestPriceVersion(ctx, "provider-model")
				if err != nil || gotPrice.ID != priceID || gotPrice.Version != 7 || len(gotPrice.Cost) != 2 || len(gotPrice.Credits) != 2 {
					t.Fatalf("price snapshot=%+v err=%v", gotPrice, err)
				}
				for _, meter := range []string{"input", "output"} {
					costRate, costOK := gotPrice.Cost[meter]
					creditRate, creditOK := gotPrice.Credits[meter]
					if !costOK || !creditOK || costRate.String() != tc.costRate || creditRate.String() != tc.creditRate {
						t.Fatalf("explicit rate lost for %s: %+v", meter, gotPrice)
					}
				}

				at := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
				usage := model.Usage{Meters: map[string]int64{"input": 10, "output": 5, "total": 15}, Estimated: estimated}
				cost, credits := model.MustMoney(tc.wantCost), model.MustCredits(tc.wantCredits)
				request := &model.Request{ID: "zero-rate-request", UserID: 3, APIKeyID: 4, API: "chat_completions", ModelAlias: "alias",
					Status: model.StatusSuccess, StatusCode: 200, Usage: usage, Cost: cost, Credits: credits, TotalLatencyMS: 100, UpstreamLatencyMS: 80, CreatedAt: at,
				}
				attempt := &model.Attempt{RequestID: request.ID, SettlementID: "zero-rate-settlement", ProviderID: 9, ProviderModel: "provider-model",
					Status: "success", StatusCode: 200, Usage: usage, Cost: cost, LatencyMS: 80, CreatedAt: at,
				}
				entries := make([]*model.LedgerEntry, 0, 3)
				for meter, quantity := range usage.Meters {
					entries = append(entries, &model.LedgerEntry{SettlementID: attempt.SettlementID, RequestID: request.ID, UserID: 3, APIKeyID: 4, ModelAlias: "alias", ProviderID: 9, ProviderModel: "provider-model",
						Meter: meter, Quantity: quantity, Cost: gotPrice.Cost[meter].Mul(model.NewMoneyInt(quantity)), Credits: model.RoundCredits(gotPrice.Credits[meter].Mul(model.NewCreditsInt(quantity))), PriceVersion: 7, Estimated: estimated, CreatedAt: at,
					})
				}
				for i := 0; i < 2; i++ {
					if err := s.InsertRequest(ctx, request); err != nil {
						t.Fatal(err)
					}
					if err := s.InsertAttempt(ctx, attempt); err != nil {
						t.Fatal(err)
					}
					if err := s.InsertLedgerEntries(ctx, entries); err != nil {
						t.Fatal(err)
					}
				}

				gotRequest, err := s.GetRequest(ctx, request.ID)
				if err != nil || gotRequest.Status != model.StatusSuccess || gotRequest.Usage.Get("input") != 10 || gotRequest.Usage.Get("output") != 5 || gotRequest.Usage.TotalTokens() != 15 || gotRequest.Usage.Estimated != estimated || !gotRequest.Cost.Equal(cost) || !gotRequest.Credits.Equal(credits) || gotRequest.TotalLatencyMS != 100 || !gotRequest.CreatedAt.Equal(at) {
					t.Fatalf("request usage or zero amounts lost: %+v err=%v", gotRequest, err)
				}
				attempts, err := s.ListAttempts(ctx, request.ID)
				if err != nil || len(attempts) != 1 {
					t.Fatalf("duplicate or missing attempts: %+v err=%v", attempts, err)
				}
				if got := attempts[0]; got.SettlementID != attempt.SettlementID || got.Usage.TotalTokens() != 15 || got.Usage.Estimated != estimated || !got.Cost.Equal(cost) || got.LatencyMS != 80 || !got.CreatedAt.Equal(at) || got.SettlementError != "" {
					t.Fatalf("attempt context lost: %+v", got)
				}
				ledger, err := s.ListLedgerEntries(ctx, request.ID)
				if err != nil || len(ledger) != 3 {
					t.Fatalf("duplicate or missing zero-amount ledger: %+v err=%v", ledger, err)
				}
				seen := make(map[string]bool)
				for _, entry := range ledger {
					if entry.ID <= 0 || seen[entry.Meter] || entry.Quantity != usage.Get(entry.Meter) || entry.Quantity <= 0 || entry.SettlementID != attempt.SettlementID || entry.RequestID != request.ID || entry.UserID != 3 || entry.APIKeyID != 4 || entry.ModelAlias != "alias" || entry.ProviderID != 9 || entry.ProviderModel != "provider-model" || entry.PriceVersion != 7 || entry.Estimated != estimated || !entry.CreatedAt.Equal(at) {
						t.Fatalf("ledger context lost: %+v", entry)
					}
					seen[entry.Meter] = true
					wantMeterCost := gotPrice.Cost[entry.Meter].Mul(model.NewMoneyInt(entry.Quantity))
					wantMeterCredits := model.RoundCredits(gotPrice.Credits[entry.Meter].Mul(model.NewCreditsInt(entry.Quantity)))
					if !entry.Cost.Equal(wantMeterCost) || !entry.Credits.Equal(wantMeterCredits) {
						t.Fatalf("zero or priced amounts changed: %+v", entry)
					}
				}
				budgetCost, budgetCredits, err := s.SumLedgerInWindow(ctx, 3, 3600)
				if err != nil || !budgetCost.Equal(cost) || !budgetCredits.Equal(credits) {
					t.Fatalf("budget totals duplicated or free usage charged: cost=%s credits=%s err=%v", budgetCost, budgetCredits, err)
				}
				budgetCost, budgetCredits, err = s.SumLedgerInWindowExcludingSettlements(ctx, 3, 3600, []string{attempt.SettlementID})
				if err != nil || !budgetCost.IsZero() || !budgetCredits.IsZero() {
					t.Fatalf("settlement exclusion changed: cost=%s credits=%s err=%v", budgetCost, budgetCredits, err)
				}

				for _, filter := range []model.StatsFilter{{}, {UserID: 3, APIKeyID: 4, ModelAlias: "alias"}} {
					stats, err := s.Stats(ctx, filter)
					if err != nil {
						t.Fatal(err)
					}
					wantEstimated := int64(0)
					if estimated {
						wantEstimated = 1
					}
					if stats.Requests != 1 || stats.Success != 1 || stats.Errors != 0 || stats.InputTokens != 10 || stats.OutputTokens != 5 || stats.TotalTokens != 15 || stats.EstimatedCount != wantEstimated || !stats.Cost.Equal(cost) || !stats.Credits.Equal(credits) || stats.P95LatencyMS != 100 || stats.FailoverCount != 0 || stats.SettlementErrors != 0 {
						t.Fatalf("stats hid free usage or double-counted total: %+v", stats)
					}
					if len(stats.Routes) != 1 || stats.Routes[0].Attempts != 1 || stats.Routes[0].Success != 1 || !stats.Routes[0].Cost.Equal(cost) || !stats.Routes[0].Credits.Equal(credits) {
						t.Fatalf("zero-amount route omitted or duplicated: %+v", stats.Routes)
					}
					if len(stats.Trend) != 1 || stats.Trend[0].Requests != 1 || stats.Trend[0].Success != 1 || !stats.Trend[0].Cost.Equal(cost) || !stats.Trend[0].Credits.Equal(credits) {
						t.Fatalf("zero-amount trend omitted or duplicated: %+v", stats.Trend)
					}
				}
			})
		}
	}
}
