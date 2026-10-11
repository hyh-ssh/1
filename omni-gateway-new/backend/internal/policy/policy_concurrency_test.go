package policy

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zjvtit/omni-gateway/internal/model"
)

// fixedClock returns a policy clock pinned to a single instant so that every
// request lands inside the same rolling window.
func fixedClock(seconds int64) clock {
	return func() time.Time { return time.Unix(seconds, 0) }
}

// TestAcquireAdmitsExactlyTheConfiguredRequestCountUnderConcurrentLoad proves
// the RPM window is counted exactly: with a limit of 5, no interleaving of 200
// concurrent admissions may let a sixth request through.
func TestAcquireAdmitsExactlyTheConfiguredRequestCountUnderConcurrentLoad(t *testing.T) {
	const limit = 5
	const goroutines = 200
	store := &fakeStore{rateRules: map[string][]*model.RateLimit{
		"user:7": {{ID: 1, Scope: "user", ScopeID: 7, RPM: limit, Active: true}},
	}}
	service := NewService(store)
	service.now = fixedClock(1000)

	var admitted int64
	releases := make([]func(), 0, goroutines)
	var releasesMu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := service.Acquire(context.Background(), 7, 8, "chat")
			if err != nil {
				if err != ErrRateLimited {
					t.Errorf("Acquire returned %v, want ErrRateLimited", err)
				}
				return
			}
			atomic.AddInt64(&admitted, 1)
			releasesMu.Lock()
			releases = append(releases, release)
			releasesMu.Unlock()
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt64(&admitted); got != limit {
		t.Fatalf("admitted %d requests, want exactly %d", got, limit)
	}
	for _, release := range releases {
		release()
	}
}

// TestConcurrencyLimitIsNeverExceededUnderParallelAcquire proves the active
// counter is reserved atomically: no more than Concurrency requests may hold a
// slot at the same time, even though every goroutine enters Acquire at once.
func TestConcurrencyLimitIsNeverExceededUnderParallelAcquire(t *testing.T) {
	const limit = 3
	const goroutines = 50
	store := &fakeStore{rateRules: map[string][]*model.RateLimit{
		"user:7": {{ID: 1, Scope: "user", ScopeID: 7, Concurrency: limit, Active: true}},
	}}
	service := NewService(store)
	service.now = fixedClock(2000)

	var admitted, active, peak int64
	attempted := &sync.WaitGroup{}
	finished := &sync.WaitGroup{}
	hold := make(chan struct{})
	attempted.Add(goroutines)
	finished.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer finished.Done()
			release, err := service.Acquire(context.Background(), 7, 8, "chat")
			attempted.Done()
			if err != nil {
				return
			}
			atomic.AddInt64(&admitted, 1)
			current := atomic.AddInt64(&active, 1)
			for {
				observed := atomic.LoadInt64(&peak)
				if current <= observed || atomic.CompareAndSwapInt64(&peak, observed, current) {
					break
				}
			}
			<-hold
			atomic.AddInt64(&active, -1)
			release()
		}()
	}
	attempted.Wait()
	// Every goroutine has now tried to acquire while the winners still hold
	// their slots, so no further admission can succeed.
	close(hold)
	finished.Wait()

	if got := atomic.LoadInt64(&admitted); got != limit {
		t.Fatalf("admitted %d requests, want exactly %d", got, limit)
	}
	if got := atomic.LoadInt64(&peak); got > limit {
		t.Fatalf("observed %d concurrently active requests, want at most %d", got, limit)
	}
}

// TestReleaseIsIdempotentUnderConcurrentCalls proves the released callback can
// be invoked many times from many goroutines without driving the active
// counter negative (which would silently disable the concurrency limit).
func TestReleaseIsIdempotentUnderConcurrentCalls(t *testing.T) {
	store := &fakeStore{rateRules: map[string][]*model.RateLimit{
		"user:7": {{ID: 1, Scope: "user", ScopeID: 7, Concurrency: 1, Active: true}},
	}}
	service := NewService(store)
	service.now = fixedClock(3000)

	release, err := service.Acquire(context.Background(), 7, 8, "chat")
	if err != nil {
		t.Fatalf("Acquire returned error: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release()
		}()
	}
	wg.Wait()

	// The single slot must be free exactly once: a further Acquire succeeds and
	// the one after that is rejected again.
	second, err := service.Acquire(context.Background(), 7, 8, "chat")
	if err != nil {
		t.Fatalf("slot was not released exactly once: %v", err)
	}
	if _, err := service.Acquire(context.Background(), 7, 8, "chat"); err != ErrRateLimited {
		t.Fatalf("third Acquire error = %v, want ErrRateLimited", err)
	}
	second()
}

// TestConcurrentReservationsOfDistinctSettlementsAreExact proves the reserved
// charge map has no lost updates: every distinct settlement id is counted once
// and the money total matches a serial execution.
func TestConcurrentReservationsOfDistinctSettlementsAreExact(t *testing.T) {
	const goroutines = 16
	const perGoroutine = 25
	cost := model.MustMoney("0.25")
	service := NewService(&fakeStore{})
	service.now = fixedClock(4000)

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				service.counters.ReserveCharge(7, fmt.Sprintf("attempt-%d-%d", g, i), cost, model.ZeroMoney, service.now())
			}
		}(g)
	}
	wg.Wait()

	_, _, ids := service.counters.PendingCharges(7, 3600, service.now())
	if len(ids) != goroutines*perGoroutine {
		t.Fatalf("recorded %d settlement ids, want %d", len(ids), goroutines*perGoroutine)
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("settlement id %q was reserved twice", id)
		}
		seen[id] = true
	}
	reservedCost, _ := service.reservedCharges(7, 3600)
	want := cost.Mul(model.MustMoney(fmt.Sprintf("%d", goroutines*perGoroutine)))
	if !reservedCost.Equal(want) {
		t.Fatalf("reserved cost = %s, want %s", reservedCost, want)
	}
}

// TestConcurrentReservationsOfTheSameSettlementAreDeduplicated proves the
// idempotency guard holds under concurrency, so a retried settlement cannot
// inflate the reserved amount and wrongly reject later requests.
func TestConcurrentReservationsOfTheSameSettlementAreDeduplicated(t *testing.T) {
	cost := model.MustMoney("1.5")
	service := NewService(&fakeStore{})
	service.now = fixedClock(5000)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			service.counters.ReserveCharge(7, "attempt-same", cost, model.ZeroMoney, service.now())
		}()
	}
	wg.Wait()

	reservedCost, _ := service.reservedCharges(7, 3600)
	if !reservedCost.Equal(cost) {
		t.Fatalf("reserved cost = %s, want %s", reservedCost, cost)
	}
}

// Historical settlement reservations must never revive retired budgets.
func TestHistoricalConcurrentReservationsCannotBlockAdmission(t *testing.T) {
	const goroutines = 10
	const perGoroutine = 2
	const each = "5"
	const limit = "100" // 10 * 2 * 5 == 100

	service := NewService(&fakeStore{budgetRules: []*model.BudgetRule{
		{Active: true, WindowSecs: 3600, CostLimit: modelMoneyPtr(limit)},
	}})
	service.now = fixedClock(6000)

	cost := model.MustMoney(each)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				service.counters.ReserveCharge(3, fmt.Sprintf("settle-%d-%d", g, i), cost, model.ZeroMoney, service.now())
			}
		}(g)
	}
	wg.Wait()

	first, err := service.Acquire(context.Background(), 3, 4, "chat")
	if err != nil {
		t.Fatalf("historical reservations blocked admission: %v", err)
	}
	first()

	// One cent below the limit the same request must still be admitted, which
	// proves the rejection above came from the exact reserved total rather than
	// from an over-count.
	service.store.(*fakeStore).budgetRules = []*model.BudgetRule{
		{Active: true, WindowSecs: 3600, CostLimit: modelMoneyPtr("100.01")},
	}
	release, err := service.Acquire(context.Background(), 3, 4, "chat")
	if err != nil {
		t.Fatalf("Acquire below the limit returned error: %v", err)
	}
	release()
}

// TestServicesShareCountersOnlyWhenGivenTheSameStore documents the V1
// single-instance boundary in executable form: admission state is shared only
// when both services were built on the same CounterStore.
func TestServicesShareCountersOnlyWhenGivenTheSameStore(t *testing.T) {
	store := &fakeStore{rateRules: map[string][]*model.RateLimit{
		"user:7": {{ID: 1, Scope: "user", ScopeID: 7, RPM: 1, Active: true}},
	}}
	shared := NewMemoryCounters()
	a := NewServiceWithCounters(store, shared)
	b := NewServiceWithCounters(store, shared)
	for _, service := range []*Service{a, b} {
		service.now = fixedClock(7000)
	}

	release, err := a.Acquire(context.Background(), 7, 8, "chat")
	if err != nil {
		t.Fatalf("first service Acquire returned error: %v", err)
	}
	if _, err := b.Acquire(context.Background(), 7, 8, "chat"); err != ErrRateLimited {
		t.Fatalf("second service sharing the same counters returned %v, want ErrRateLimited", err)
	}
	release()

	// Two default services keep independent counters: the V1 boundary.
	isolated := NewService(store)
	isolated.now = fixedClock(7000)
	other := NewService(store)
	other.now = fixedClock(7000)
	isolatedRelease, err := isolated.Acquire(context.Background(), 7, 8, "chat")
	if err != nil {
		t.Fatalf("isolated service Acquire returned error: %v", err)
	}
	otherRelease, err := other.Acquire(context.Background(), 7, 8, "chat")
	if err != nil {
		t.Fatalf("second isolated service shares state unexpectedly: %v", err)
	}
	isolatedRelease()
	otherRelease()
}

// TestNewServiceWithCountersFallsBackToMemoryCounters pins the nil-safety of
// the seam so an operator wiring an optional shared store cannot disable
// admission control by passing nil.
func TestNewServiceWithCountersFallsBackToMemoryCounters(t *testing.T) {
	store := &fakeStore{rateRules: map[string][]*model.RateLimit{
		"user:7": {{ID: 1, Scope: "user", ScopeID: 7, RPM: 1, Active: true}},
	}}
	service := NewServiceWithCounters(store, nil)
	service.now = fixedClock(8000)

	release, err := service.Acquire(context.Background(), 7, 8, "chat")
	if err != nil {
		t.Fatalf("Acquire returned error: %v", err)
	}
	if _, err := service.Acquire(context.Background(), 7, 8, "chat"); err != ErrRateLimited {
		t.Fatalf("nil counters disabled admission control: %v", err)
	}
	release()
}

// recordingCounters is an out-of-band CounterStore used to pin the contract
// between Service and the seam.
type recordingCounters struct {
	mu           sync.Mutex
	allowedWith  []BucketLimit
	releasedWith []string
	tokensWith   []string
	locks        int
}

func (c *recordingCounters) AllowRequest(buckets []BucketLimit, _ time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.allowedWith = append(c.allowedWith, buckets...)
	return nil
}

func (c *recordingCounters) Release(keys []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.releasedWith = append(c.releasedWith, keys...)
}

func (c *recordingCounters) AddTokens(keys []string, _ time.Time, _ int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tokensWith = append(c.tokensWith, keys...)
}

func (c *recordingCounters) ReserveCharge(int64, string, model.Money, model.Money, time.Time) {}

func (c *recordingCounters) PendingCharges(int64, int64, time.Time) (model.Money, model.Credits, []string) {
	return model.ZeroMoney, model.ZeroMoney, nil
}

func (c *recordingCounters) LockBudget(int64) sync.Locker {
	c.mu.Lock()
	c.locks++
	c.mu.Unlock()
	return &sync.Mutex{}
}

func (c *recordingCounters) MarkSettlementFailure(int64) {}

func (c *recordingCounters) SettlementFailed(int64) bool { return false }

// TestServiceDelegatesAdmissionStateToTheCounterStore proves the seam is real
// rather than documentary: the service must hand the resolved buckets and the
// matching release keys to whatever CounterStore it was given.
func TestServiceDelegatesAdmissionStateToTheCounterStore(t *testing.T) {
	store := &fakeStore{rateRules: map[string][]*model.RateLimit{
		"user:7": {
			{ID: 1, Scope: "user", ScopeID: 7, RPM: 10, TPM: 20, Concurrency: 2, Active: true},
			{ID: 2, Scope: "user", ScopeID: 7, ModelID: "other", RPM: 10, Active: true},
		},
		"model:0": {{ID: 3, Scope: "model", ScopeID: 0, ModelID: "chat", TPM: 5, Active: true}},
	}}
	counters := &recordingCounters{}
	service := NewServiceWithCounters(store, counters)
	service.now = fixedClock(9000)

	release, err := service.Acquire(context.Background(), 7, 8, "chat")
	if err != nil {
		t.Fatalf("Acquire returned error: %v", err)
	}
	release()

	want := []BucketLimit{
		{Key: "user:7:", RPM: 10, TPM: 20, Concurrency: 2},
		{Key: "model:0:chat", RPM: 0, TPM: 5, Concurrency: 0},
	}
	if len(counters.allowedWith) != len(want) {
		t.Fatalf("AllowRequest received %#v, want %#v", counters.allowedWith, want)
	}
	for i, limit := range want {
		if counters.allowedWith[i] != limit {
			t.Fatalf("bucket %d = %#v, want %#v", i, counters.allowedWith[i], limit)
		}
	}
	if len(counters.releasedWith) != len(want) {
		t.Fatalf("Release received %#v, want the %d admitted keys", counters.releasedWith, len(want))
	}
	for i, limit := range want {
		if counters.releasedWith[i] != limit.Key {
			t.Fatalf("released key %d = %q, want %q", i, counters.releasedWith[i], limit.Key)
		}
	}
}
