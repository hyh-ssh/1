package store

import (
	"context"
	"strings"
	"time"
)

// MarkProviderCooldown prevents an unhealthy provider from being selected
// until the supplied time. It is safe to call from best-effort request paths.
func (s *Store) MarkProviderCooldown(ctx context.Context, providerID int64, until time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE providers SET health_status='cooldown', cooldown_until=GREATEST(cooldown_until,$1), failure_count=failure_count+1, last_error='retryable upstream failure', updated_at=now() WHERE id=$2`, until.UTC(), providerID)
	return err
}

// MarkProviderHealthy clears a provider cooldown after a successful request.
func (s *Store) MarkProviderHealthy(ctx context.Context, providerID int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE providers SET success_count=success_count+1,
		health_status=CASE WHEN cooldown_until > now() THEN health_status ELSE 'available' END,
		last_error=CASE WHEN cooldown_until > now() THEN last_error ELSE '' END,
		cooldown_until=CASE WHEN cooldown_until > now() THEN cooldown_until ELSE NULL END, updated_at=now() WHERE id=$1`, providerID)
	return err
}

// RecordProviderVerification saves an explicit administrator probe result. A
// failed probe keeps the reason in last_error, so the console can explain why the
// provider is cooling down instead of showing a generic failure.
func (s *Store) RecordProviderVerification(ctx context.Context, id int64, healthy bool, reason string) error {
	if healthy {
		reason = ""
	} else if strings.TrimSpace(reason) == "" {
		reason = "verification failed"
	}
	_, err := s.pool.Exec(ctx, `UPDATE providers SET last_verified_at=now(),
		health_status=CASE WHEN $2 THEN 'available' ELSE 'cooldown' END,
		cooldown_until=CASE WHEN $2 THEN NULL ELSE now()+interval '30 seconds' END,
		last_error=CASE WHEN $2 THEN '' ELSE $3 END, updated_at=now() WHERE id=$1`, id, healthy, reason)
	return err
}
