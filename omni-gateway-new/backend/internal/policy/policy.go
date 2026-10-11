package policy

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/zjvtit/omni-gateway/internal/model"
)

var (
	ErrRateLimited    = errors.New("request rate limited")
	ErrBudgetExceeded = errors.New("budget exceeded")
)

// Store supplies persistent rate-limit configuration. Historical budgets are
// deliberately not part of the runtime policy contract.
type Store interface {
	ListRateLimits(context.Context, string, int64) ([]*model.RateLimit, error)
}

type clock func() time.Time

// Service enforces RPM, TPM, and concurrency limits.
//
// Configuration and historical totals come from Store; everything that has to
// change on every request lives in the CounterStore, which is the single
// replacement point for the V1 single-instance boundary. See the CounterStore
// documentation in counters.go.
type Service struct {
	store    Store
	counters CounterStore
	now      clock
}

// NewService builds a policy service on the default process-local counters.
func NewService(store Store) *Service {
	return NewServiceWithCounters(store, NewMemoryCounters())
}

// NewServiceWithCounters builds a policy service on an explicit CounterStore.
// Pass NewMemoryCounters() for the V1 single-instance behaviour; a shared
// implementation replaces it when a deployment needs cross-instance
// coordination. A nil counters value falls back to NewMemoryCounters().
func NewServiceWithCounters(store Store, counters CounterStore) *Service {
	if counters == nil {
		counters = NewMemoryCounters()
	}
	return &Service{
		store:    store,
		counters: counters,
		now:      func() time.Time { return time.Now().UTC() },
	}
}

// Acquire performs RPM, TPM, and concurrency admission checks.
// The returned callback is idempotent and releases concurrency counters.
func (s *Service) Acquire(ctx context.Context, userID, apiKeyID int64, modelAlias string) (func(), error) {
	if s == nil || s.store == nil {
		return func() {}, nil
	}

	rules, err := s.loadRateRules(ctx, userID, apiKeyID)
	if err != nil {
		return nil, err
	}
	limits := mergedRateLimits(rules, modelAlias)
	keys := make([]string, 0, len(limits))
	for _, limit := range limits {
		keys = append(keys, limit.Key)
	}
	if err := s.counters.AllowRequest(limits, s.now()); err != nil {
		return nil, err
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			s.counters.Release(keys)
		})
	}, nil
}

// ObserveUsage records actual token consumption for the next TPM admission
// window. This is intentionally local to the single process, matching the
// single-instance V1 deployment model documented in docs/PROJECT_ANALYSIS.md
// and README.md; a shared CounterStore is the replacement point.
func (s *Service) ObserveUsage(ctx context.Context, userID, apiKeyID int64, modelAlias string, usage model.Usage) {
	if s == nil || s.store == nil {
		return
	}
	count := usage.TotalTokens()
	if count <= 0 {
		return
	}
	rules, err := s.loadRateRules(ctx, userID, apiKeyID)
	if err != nil {
		return
	}
	limits := mergedRateLimits(rules, modelAlias)
	keys := make([]string, 0, len(limits))
	for _, limit := range limits {
		if limit.TPM <= 0 {
			continue
		}
		keys = append(keys, limit.Key)
	}
	s.counters.AddTokens(keys, s.now(), count)
}

// Retired budget accounting hooks remain source-compatible no-ops. Charges
// live only in the immutable ledgers, not in unbounded runtime reservations.
func (s *Service) RecordCharge(context.Context, int64, model.Money, model.Credits) {}
func (s *Service) RecordPendingSettlement(context.Context, int64, string, model.Money, model.Credits) {
}
func (s *Service) RecordSettlementFailure(context.Context, int64) {}

// CheckUsage remains a no-op for callers compiled against the former budget
// policy. Neither historical rules nor settlement failures affect admission.
func (s *Service) CheckUsage(context.Context, int64, model.Money, model.Credits) error {
	return nil
}

func (s *Service) reservedCharges(userID, windowSecs int64) (model.Money, model.Credits) {
	cost, credits, _ := s.counters.PendingCharges(userID, windowSecs, s.now())
	return cost, credits
}

func (s *Service) loadRateRules(ctx context.Context, userID, apiKeyID int64) ([]*model.RateLimit, error) {
	rules := make([]*model.RateLimit, 0)
	for _, scope := range []struct {
		name string
		id   int64
	}{
		{name: "user", id: userID},
		{name: "key", id: apiKeyID},
		{name: "model", id: 0},
	} {
		items, err := s.store.ListRateLimits(ctx, scope.name, scope.id)
		if err != nil {
			return nil, err
		}
		rules = append(rules, items...)
	}
	return rules, nil
}

// Multiple configured rules can refer to the same counter bucket. Merge only
// the effective runtime limits, keeping the strictest finite value per dimension
// and preserving rule configuration and first-seen bucket order.
func mergedRateLimits(rules []*model.RateLimit, modelAlias string) []BucketLimit {
	limits := make([]BucketLimit, 0, len(rules))
	indices := make(map[string]int, len(rules))
	for _, rule := range rules {
		if !appliesToModel(rule, modelAlias) {
			continue
		}
		key := bucketKey(rule)
		index, exists := indices[key]
		if !exists {
			index = len(limits)
			indices[key] = index
			limits = append(limits, BucketLimit{Key: key})
		}
		limit := &limits[index]
		limit.RPM = strictestNonzeroLimit(limit.RPM, rule.RPM)
		limit.TPM = strictestNonzeroLimit(limit.TPM, rule.TPM)
		limit.Concurrency = strictestNonzeroLimit(limit.Concurrency, rule.Concurrency)
	}
	return limits
}

func strictestNonzeroLimit(current, candidate int64) int64 {
	if candidate > 0 && (current == 0 || candidate < current) {
		return candidate
	}
	return current
}

func appliesToModel(rule *model.RateLimit, modelAlias string) bool {
	return rule != nil && rule.Active && (rule.ModelID == "" || rule.ModelID == modelAlias)
}

func bucketKey(rule *model.RateLimit) string {
	return rule.Scope + ":" + stringValue(rule.ScopeID) + ":" + rule.ModelID
}

func stringValue(value int64) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var buf [20]byte
	i := len(buf)
	for value > 0 {
		i--
		buf[i] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
