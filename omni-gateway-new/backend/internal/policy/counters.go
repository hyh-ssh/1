package policy

import (
	"sync"
	"time"

	"github.com/zjvtit/omni-gateway/internal/model"
)

// BucketLimit is one admission bucket's rolling limits. A zero limit means the
// dimension is unlimited and is not checked.
type BucketLimit struct {
	Key         string
	RPM         int64
	TPM         int64
	Concurrency int64
}

// CounterStore owns every piece of policy state that lives inside the gateway
// process: the rolling request and token windows, the active-concurrency
// counters, the charges reserved for in-flight settlements, and the unresolved
// settlement-failure flags.
//
// It exists so that the single-instance boundary documented in
// docs/PROJECT_ANALYSIS.md and README.md has exactly one replacement point: a
// deployment that needs cross-instance coordination supplies a shared
// implementation to NewServiceWithCounters instead of the default
// NewMemoryCounters. V1 deliberately ships no distributed implementation, so
// budget and rate-limit state remains per process.
//
// Implementations must be safe for concurrent use. The methods are exported so
// that an implementation may live outside this package.
type CounterStore interface {
	// AllowRequest checks the rolling window of every bucket and, only when all
	// of them permit the request, atomically reserves one request slot and one
	// active-concurrency slot per bucket. It returns ErrRateLimited when any
	// limit is already exhausted, and reserves nothing in that case.
	AllowRequest(buckets []BucketLimit, now time.Time) error
	// Release returns the active-concurrency slots reserved by a successful
	// AllowRequest. It must be safe to call more than once for the same keys.
	Release(keys []string)
	// AddTokens records observed token usage against the given buckets for the
	// next rolling token window.
	AddTokens(keys []string, now time.Time, count int64)
	// ReserveCharge records a charge that has not been persisted yet, or whose
	// persistence acknowledgement was lost. Re-reserving a non-empty
	// settlementID is a no-op.
	ReserveCharge(userID int64, settlementID string, cost model.Money, credits model.Credits, now time.Time)
	// PendingCharges returns the reserved charges inside the window together
	// with the non-empty settlement ids they carry.
	PendingCharges(userID, windowSecs int64, now time.Time) (cost model.Money, credits model.Credits, ids []string)
	// LockBudget returns a held lock that guards the check-and-reserve sequence
	// for one user's rolling budget. The caller must call Unlock exactly once.
	LockBudget(userID int64) sync.Locker
	// MarkSettlementFailure records that a settlement failed to persist.
	MarkSettlementFailure(userID int64)
	// SettlementFailed reports whether an unresolved settlement failure still
	// blocks the user's budgeted requests.
	SettlementFailed(userID int64) bool
}

// memoryCounters is the default CounterStore. It keeps all policy state in the
// gateway process, which matches the V1 single-instance deployment model.
type memoryCounters struct {
	mu      sync.Mutex
	buckets map[string]*bucket

	budgetMu           sync.Mutex
	budgetLocks        map[int64]*sync.Mutex
	charges            map[int64][]chargeEvent
	settlementFailures map[int64]bool
}

// NewMemoryCounters returns the default process-local CounterStore.
func NewMemoryCounters() CounterStore {
	return &memoryCounters{
		buckets:            make(map[string]*bucket),
		budgetLocks:        make(map[int64]*sync.Mutex),
		charges:            make(map[int64][]chargeEvent),
		settlementFailures: make(map[int64]bool),
	}
}

type bucket struct {
	requests []time.Time
	tokens   []tokenEvent
	active   int64
}

type tokenEvent struct {
	at    time.Time
	count int64
}

type chargeEvent struct {
	at      time.Time
	id      string
	cost    model.Money
	credits model.Credits
}

func (c *memoryCounters) AllowRequest(buckets []BucketLimit, now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	keys := make([]string, 0, len(buckets))
	for _, limit := range buckets {
		state := c.buckets[limit.Key]
		if state == nil {
			state = &bucket{}
			c.buckets[limit.Key] = state
		}
		state.requests = keepRecent(state.requests, now)
		state.tokens = keepRecentTokens(state.tokens, now)
		if limit.RPM > 0 && int64(len(state.requests)) >= limit.RPM {
			return ErrRateLimited
		}
		if limit.TPM > 0 && tokenTotal(state.tokens) >= limit.TPM {
			return ErrRateLimited
		}
		if limit.Concurrency > 0 && state.active >= limit.Concurrency {
			return ErrRateLimited
		}
		keys = append(keys, limit.Key)
	}
	for _, key := range keys {
		state := c.buckets[key]
		state.requests = append(state.requests, now)
		state.active++
	}
	return nil
}

func (c *memoryCounters) Release(keys []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, key := range keys {
		if state := c.buckets[key]; state != nil && state.active > 0 {
			state.active--
		}
	}
}

func (c *memoryCounters) AddTokens(keys []string, now time.Time, count int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, key := range keys {
		state := c.buckets[key]
		if state == nil {
			state = &bucket{}
			c.buckets[key] = state
		}
		state.tokens = append(keepRecentTokens(state.tokens, now), tokenEvent{at: now, count: count})
	}
}

func (c *memoryCounters) ReserveCharge(userID int64, settlementID string, cost model.Money, credits model.Credits, now time.Time) {
	c.budgetMu.Lock()
	defer c.budgetMu.Unlock()
	if settlementID != "" {
		for _, event := range c.charges[userID] {
			if event.id == settlementID {
				return
			}
		}
	}
	c.charges[userID] = append(c.charges[userID], chargeEvent{at: now, id: settlementID, cost: cost, credits: credits})
}

func (c *memoryCounters) PendingCharges(userID, windowSecs int64, now time.Time) (model.Money, model.Credits, []string) {
	cutoff := now.Add(-time.Duration(windowSecs) * time.Second)
	c.budgetMu.Lock()
	defer c.budgetMu.Unlock()
	cost, credits := model.ZeroMoney, model.ZeroCredits
	var ids []string
	for _, charge := range c.charges[userID] {
		if charge.at.Before(cutoff) {
			continue
		}
		if charge.id != "" {
			ids = append(ids, charge.id)
		}
		cost = cost.Add(charge.cost)
		credits = credits.Add(charge.credits)
	}
	return cost, credits, ids
}

func (c *memoryCounters) LockBudget(userID int64) sync.Locker {
	c.budgetMu.Lock()
	lock := c.budgetLocks[userID]
	if lock == nil {
		lock = &sync.Mutex{}
		c.budgetLocks[userID] = lock
	}
	c.budgetMu.Unlock()
	lock.Lock()
	return lock
}

func (c *memoryCounters) MarkSettlementFailure(userID int64) {
	c.budgetMu.Lock()
	c.settlementFailures[userID] = true
	c.budgetMu.Unlock()
}

func (c *memoryCounters) SettlementFailed(userID int64) bool {
	c.budgetMu.Lock()
	defer c.budgetMu.Unlock()
	return c.settlementFailures[userID]
}

func keepRecent(values []time.Time, now time.Time) []time.Time {
	cutoff := now.Add(-time.Minute)
	index := 0
	for index < len(values) && !values[index].After(cutoff) {
		index++
	}
	return values[index:]
}

func keepRecentTokens(values []tokenEvent, now time.Time) []tokenEvent {
	cutoff := now.Add(-time.Minute)
	index := 0
	for index < len(values) && !values[index].at.After(cutoff) {
		index++
	}
	return values[index:]
}

func tokenTotal(values []tokenEvent) int64 {
	var total int64
	for _, value := range values {
		total += value.count
	}
	return total
}
