package gateway

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/zjvtit/omni-gateway/internal/adapter"
)

type routeState struct {
	active   int
	failures int
	until    time.Time
	failedAt time.Time
}

// acquireRoute rechecks availability immediately before dispatch: requests
// resolving simultaneously must share capacity and cooldown state.
func (s *Service) acquireRoute(ctx context.Context, route resolvedRoute) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.routeMu.Lock()
	if s.routeStates == nil {
		s.routeStates = make(map[int64]*routeState)
	}
	state := s.routeStates[route.providerID]
	if state == nil {
		state = &routeState{}
		s.routeStates[route.providerID] = state
	}
	if state.until.After(time.Now()) || (route.maxConcurrency > 0 && state.active >= route.maxConcurrency) {
		s.routeMu.Unlock()
		return nil, ErrNoRoute
	}
	state.active++
	s.routeMu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { s.routeMu.Lock(); state.active--; s.routeMu.Unlock() }) }, nil
}

func (s *Service) markProviderHealthy(ctx context.Context, id int64) {
	s.routeMu.Lock()
	if state := s.routeStates[id]; state != nil && !state.until.After(time.Now()) {
		state.failures = 0
	}
	s.routeMu.Unlock()
	if health, ok := s.store.(providerHealthStore); ok {
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		if err := health.MarkProviderHealthy(writeCtx, id); err != nil {
			slog.Warn("provider health persistence failed", "provider_id", id)
		}
	}
}

func (s *Service) markProviderFailure(ctx context.Context, id int64, err error) {
	if ctx.Err() != nil || !isRetryable(err) {
		return
	}
	s.routeMu.Lock()
	if s.routeStates == nil {
		s.routeStates = make(map[int64]*routeState)
	}
	state := s.routeStates[id]
	if state == nil {
		state = &routeState{}
		s.routeStates[id] = state
	}
	state.failures = min(state.failures+1, 7)
	delay := min(5*time.Second*time.Duration(1<<uint(state.failures-1)), 5*time.Minute)
	var upstream *adapter.Error
	if errors.As(err, &upstream) && upstream.RetryAfter > 0 {
		delay = min(upstream.RetryAfter, 5*time.Minute)
	}
	until := time.Now().Add(delay)
	state.failedAt = time.Now()
	if until.After(state.until) {
		state.until = until
	}
	s.routeMu.Unlock()
	if health, ok := s.store.(providerHealthStore); ok {
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		if err := health.MarkProviderCooldown(writeCtx, id, until); err != nil {
			slog.Warn("provider cooldown persistence failed", "provider_id", id)
		}
	}
}
