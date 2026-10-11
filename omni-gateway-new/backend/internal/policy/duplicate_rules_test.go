package policy

import (
	"context"
	"reflect"
	"testing"

	"github.com/zjvtit/omni-gateway/internal/model"
)

func duplicateRuleService(rules ...*model.RateLimit) *Service {
	service := NewService(&fakeStore{rateRules: map[string][]*model.RateLimit{"user:7": rules}})
	service.now = fixedClock(1000)
	return service
}

func TestDuplicateRulesAllowTenRequestsAtRPMTen(t *testing.T) {
	service := duplicateRuleService(
		&model.RateLimit{ID: 1, Scope: "user", ScopeID: 7, RPM: 10, Active: true},
		&model.RateLimit{ID: 2, Scope: "user", ScopeID: 7, RPM: 10, Active: true},
	)
	for i := 0; i < 10; i++ {
		release, err := service.Acquire(context.Background(), 7, 8, "chat")
		if err != nil {
			t.Fatalf("request %d of RPM=10 rejected by duplicate counting: %v", i+1, err)
		}
		release()
	}
	if _, err := service.Acquire(context.Background(), 7, 8, "chat"); err != ErrRateLimited {
		t.Fatalf("eleventh request must be rate limited: %v", err)
	}
}

func TestDuplicateRulesObserveTokensOnce(t *testing.T) {
	service := duplicateRuleService(
		&model.RateLimit{ID: 1, Scope: "user", ScopeID: 7, TPM: 10, Active: true},
		&model.RateLimit{ID: 2, Scope: "user", ScopeID: 7, TPM: 10, Active: true},
	)
	ctx := context.Background()
	for _, count := range []int64{3, 4} {
		service.ObserveUsage(ctx, 7, 8, "chat", model.Usage{Meters: map[string]int64{"input": count, "total": count}})
		release, err := service.Acquire(ctx, 7, 8, "chat")
		if err != nil {
			t.Fatalf("seven total tokens below TPM=10 counted more than once: %v", err)
		}
		release()
	}
	service.ObserveUsage(ctx, 7, 8, "chat", model.Usage{Meters: map[string]int64{"input": 3, "total": 3}})
	if _, err := service.Acquire(ctx, 7, 8, "chat"); err != ErrRateLimited {
		t.Fatalf("ten actual tokens must exhaust TPM=10: %v", err)
	}
}

func TestDuplicateRulesDoNotHalveConcurrencyOrDoubleRelease(t *testing.T) {
	service := duplicateRuleService(
		&model.RateLimit{ID: 1, Scope: "user", ScopeID: 7, Concurrency: 4, Active: true},
		&model.RateLimit{ID: 2, Scope: "user", ScopeID: 7, Concurrency: 4, Active: true},
	)
	ctx := context.Background()
	releases := make([]func(), 0, 4)
	for i := 0; i < 4; i++ {
		release, err := service.Acquire(ctx, 7, 8, "chat")
		if err != nil {
			t.Fatalf("slot %d of concurrency=4 rejected by duplicate reservation: %v", i+1, err)
		}
		releases = append(releases, release)
		defer release()
	}
	if _, err := service.Acquire(ctx, 7, 8, "chat"); err != ErrRateLimited {
		t.Fatalf("fifth simultaneous request must be refused: %v", err)
	}
	releases[0]()
	releases[0]()
	replacement, err := service.Acquire(ctx, 7, 8, "chat")
	if err != nil {
		t.Fatalf("one released slot was not available: %v", err)
	}
	defer replacement()
	if _, err := service.Acquire(ctx, 7, 8, "chat"); err != ErrRateLimited {
		t.Fatalf("idempotent release freed more than one slot: %v", err)
	}
}

func TestDuplicateRulesMergeStrictestNonzeroLimitsWithoutChangingConfiguration(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(map[bool]string{false: "original order", true: "reversed order"}[reverse], func(t *testing.T) {
			rules := []*model.RateLimit{
				{ID: 1, Scope: "user", ScopeID: 7, RPM: 10, TPM: 0, Concurrency: 4, Active: true},
				{ID: 2, Scope: "user", ScopeID: 7, RPM: 0, TPM: 20, Concurrency: 0, Active: true},
				{ID: 3, Scope: "user", ScopeID: 7, RPM: 5, TPM: 12, Concurrency: 3, Active: true},
				{ID: 4, Scope: "user", ScopeID: 7, RPM: 0, TPM: 0, Concurrency: 0, Active: true},
			}
			if reverse {
				for i, j := 0, len(rules)-1; i < j; i, j = i+1, j-1 {
					rules[i], rules[j] = rules[j], rules[i]
				}
			}
			rules = append(rules, nil,
				&model.RateLimit{ID: 5, Scope: "user", ScopeID: 7, RPM: 1, TPM: 1, Concurrency: 1, Active: false},
				&model.RateLimit{ID: 6, Scope: "user", ScopeID: 7, ModelID: "other", RPM: 1, TPM: 1, Concurrency: 1, Active: true},
			)
			before := make([]model.RateLimit, 0, len(rules))
			for _, rule := range rules {
				if rule != nil {
					before = append(before, *rule)
				}
			}
			counters := &recordingCounters{}
			service := NewServiceWithCounters(&fakeStore{rateRules: map[string][]*model.RateLimit{
				"user:7":  rules,
				"key:8":   {{ID: 7, Scope: "key", ScopeID: 8, TPM: 30, Active: true}},
				"model:0": {{ID: 8, Scope: "model", ModelID: "chat", TPM: 40, Active: true}},
			}}, counters)
			release, err := service.Acquire(context.Background(), 7, 8, "chat")
			if err != nil {
				t.Fatal(err)
			}
			release()
			release()
			service.ObserveUsage(context.Background(), 7, 8, "chat", model.Usage{Meters: map[string]int64{"input": 3}})
			want := []BucketLimit{
				{Key: "user:7:", RPM: 5, TPM: 12, Concurrency: 3},
				{Key: "key:8:", TPM: 30},
				{Key: "model:0:chat", TPM: 40},
			}
			if !reflect.DeepEqual(counters.allowedWith, want) {
				t.Fatalf("strictest limits or bucket isolation wrong: got=%+v want=%+v", counters.allowedWith, want)
			}
			keys := []string{"user:7:", "key:8:", "model:0:chat"}
			if !reflect.DeepEqual(counters.releasedWith, keys) || !reflect.DeepEqual(counters.tokensWith, keys) {
				t.Fatalf("release/token observation must use each applicable bucket once: release=%v tokens=%v", counters.releasedWith, counters.tokensWith)
			}
			after := make([]model.RateLimit, 0, len(rules))
			for _, rule := range rules {
				if rule != nil {
					after = append(after, *rule)
				}
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("merging mutated stored rule configuration")
			}
		})
	}
}
