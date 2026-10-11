package billing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zjvtit/omni-gateway/internal/background"
	"github.com/zjvtit/omni-gateway/internal/model"
)

type retryLedger struct {
	calls      int
	entries    map[string]*model.LedgerEntry
	failAlways bool
}

func (s *retryLedger) InsertLedgerEntries(_ context.Context, entries []*model.LedgerEntry) error {
	s.calls++
	for _, e := range entries {
		key := e.SettlementID + ":" + e.Meter
		if _, exists := s.entries[key]; !exists {
			copy := *e
			s.entries[key] = &copy
		}
	}
	if s.calls == 1 || s.failAlways {
		return errors.New("commit acknowledgement lost")
	}
	return nil
}

func TestSettlementRetryPreservesIdentityPriceAndTime(t *testing.T) {
	ledger := &retryLedger{entries: make(map[string]*model.LedgerEntry)}
	s := NewService(&fakePriceStore{prices: map[string]*model.PriceVersion{"alias": {Version: 3, Cost: map[string]model.Money{"input": model.MustMoney("0.2")}, Credits: map[string]model.Money{"input": model.MustMoney("0.5")}}}}, ledger)
	defer s.Close(context.Background())
	at := time.Now().Add(-time.Hour).UTC()
	ctx := WithAttempt(context.Background(), "attempt-1", at)
	result, err := s.Settle(ctx, "request", 1, 2, "alias", "", 3, model.Usage{Meters: map[string]int64{"input": 2}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Persisted || result.SettlementID != "attempt-1" || ledger.calls != 2 || len(ledger.entries) != 1 {
		t.Fatalf("result=%+v calls=%d rows=%d", result, ledger.calls, len(ledger.entries))
	}
	e := ledger.entries["attempt-1:input"]
	if !e.CreatedAt.Equal(at) || e.PriceVersion != 3 || e.Cost.String() != "0.4" || e.Credits.String() != "1" {
		t.Fatalf("entry=%+v", e)
	}
	if s.PersistenceStats().Retried != 1 {
		t.Fatal("retry counter missing")
	}
}

func TestSettlementFailureReturnsCalculatedTotals(t *testing.T) {
	ledger := &retryLedger{entries: make(map[string]*model.LedgerEntry), failAlways: true}
	s := NewService(&fakePriceStore{prices: map[string]*model.PriceVersion{"alias": {Version: 1, Cost: map[string]model.Money{"input": model.MustMoney("2")}, Credits: map[string]model.Money{"input": model.MustMoney("3")}}}}, ledger)
	result, err := s.Settle(context.Background(), "request", 1, 2, "alias", "", 3, model.Usage{Meters: map[string]int64{"input": 2}}, true)
	if err == nil || result == nil || result.Persisted || result.Cost.String() != "4" || result.Credits.String() != "6" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if err := s.Close(context.Background()); !errors.Is(err, background.ErrFailed) {
		t.Fatalf("close=%v", err)
	}
}

func TestDistinctAttemptsHaveDistinctSettlementIDs(t *testing.T) {
	s := NewService(&fakePriceStore{prices: map[string]*model.PriceVersion{}}, nil)
	defer s.Close(context.Background())
	a, _ := s.Settle(context.Background(), "request", 1, 2, "alias", "", 3, model.NewUsage(), false)
	b, _ := s.Settle(context.Background(), "request", 1, 2, "alias", "", 3, model.NewUsage(), true)
	if a.SettlementID == "" || a.SettlementID == b.SettlementID {
		t.Fatalf("ids=%q %q", a.SettlementID, b.SettlementID)
	}
}

func TestZeroAmountSettlementRetriesAndReportsPersistenceFailures(t *testing.T) {
	for _, failAlways := range []bool{false, true} {
		t.Run(map[bool]string{false: "retry is idempotent", true: "failure stays visible"}[failAlways], func(t *testing.T) {
			ledger := &retryLedger{entries: make(map[string]*model.LedgerEntry), failAlways: failAlways}
			s := NewService(&fakePriceStore{prices: map[string]*model.PriceVersion{"alias": {
				Version: 8,
				Cost:    map[string]model.Money{"input": model.ZeroMoney, "output": model.ZeroMoney},
				Credits: map[string]model.Credits{"input": model.ZeroCredits, "output": model.ZeroCredits},
			}}}, ledger)
			defer s.Close(context.Background())
			at := time.Now().Add(-time.Hour).UTC()
			ctx := WithAttempt(context.Background(), "free-attempt", at)
			usage := model.Usage{Meters: map[string]int64{"input": 10, "output": 5, "total": 15}, Estimated: true}
			result, err := s.Settle(ctx, "free-request", 1, 2, "alias", "", 3, usage, true)
			if (err != nil) != failAlways || result == nil {
				t.Fatalf("result=%+v err=%v failAlways=%v", result, err, failAlways)
			}
			if result.Persisted == failAlways || result.SettlementID != "free-attempt" || result.Unpriced || result.ZeroCreditMeters != 0 || !result.Cost.IsZero() || !result.Credits.IsZero() || len(result.Entries) != 3 || ledger.calls < 2 || len(ledger.entries) != 3 {
				t.Fatalf("free settlement skipped persistence or retry: result=%+v writes=%d rows=%d", result, ledger.calls, len(ledger.entries))
			}
			for meter, quantity := range usage.Meters {
				entry := ledger.entries["free-attempt:"+meter]
				if entry == nil || entry.Quantity != quantity || entry.RequestID != "free-request" || entry.SettlementID != "free-attempt" || entry.PriceVersion != 8 || !entry.Estimated || !entry.CreatedAt.Equal(at) || !entry.Cost.IsZero() || !entry.Credits.IsZero() {
					t.Fatalf("retry lost free usage context: %+v", entry)
				}
			}
			if stats := s.PersistenceStats(); stats.Retried == 0 || (stats.Failed > 0) != failAlways {
				t.Fatalf("free settlement persistence counters=%+v", stats)
			}
			closeErr := s.Close(context.Background())
			if (failAlways && !errors.Is(closeErr, background.ErrFailed)) || (!failAlways && closeErr != nil) {
				t.Fatalf("close=%v failAlways=%v", closeErr, failAlways)
			}
		})
	}
}
