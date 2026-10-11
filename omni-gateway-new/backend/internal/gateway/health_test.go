package gateway

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zjvtit/omni-gateway/internal/adapter"
)

func TestProviderCapacityCooldownAndRecovery(t *testing.T) {
	s := NewService(nil, nil, nil)
	defer s.Close(context.Background())
	r := resolvedRoute{providerID: 1, maxConcurrency: 1}
	release, err := s.acquireRoute(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.acquireRoute(context.Background(), r); !errors.Is(err, ErrNoRoute) {
		t.Fatal("capacity exceeded")
	}
	release()
	release()
	s.markProviderFailure(context.Background(), 1, &adapter.Error{Type: "rate_limit", StatusCode: 429, RetryAfter: time.Minute})
	if until := s.routeStates[1].until; time.Until(until) < 59*time.Second {
		t.Fatal("Retry-After ignored")
	}
	if _, err := s.acquireRoute(context.Background(), r); !errors.Is(err, ErrNoRoute) {
		t.Fatal("cooldown ignored")
	}
	s.markProviderHealthy(context.Background(), 1)
	if _, err := s.acquireRoute(context.Background(), r); !errors.Is(err, ErrNoRoute) {
		t.Fatal("concurrent success erased cooldown")
	}
	s.routeStates[1].until = time.Now().Add(-time.Second)
	release, err = s.acquireRoute(context.Background(), r)
	if err != nil {
		t.Fatal("cooldown did not expire")
	}
	release()
	s.markProviderHealthy(context.Background(), 1)
	if s.routeStates[1].failures != 0 {
		t.Fatal("success did not reset backoff")
	}
}

func TestProviderBackoffAndCancellation(t *testing.T) {
	s := NewService(nil, nil, nil)
	defer s.Close(context.Background())
	for i := 0; i < 10; i++ {
		s.markProviderFailure(context.Background(), 1, &adapter.Error{Type: "server", StatusCode: 503})
	}
	if d := time.Until(s.routeStates[1].until); d > 5*time.Minute || d < 299*time.Second {
		t.Fatalf("backoff=%s", d)
	}
	s.markProviderFailure(context.Background(), 2, &adapter.Error{Type: "auth", StatusCode: 401})
	if s.routeStates[2] != nil {
		t.Fatal("auth incorrectly cooled down")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.acquireRoute(ctx, resolvedRoute{providerID: 3}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored")
	}
	s.markProviderFailure(ctx, 3, &adapter.Error{Type: "connection"})
	if s.routeStates[3] != nil {
		t.Fatal("canceled client poisoned health")
	}
}
