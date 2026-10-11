package store

import (
	"context"
	"time"
)

// RefreshAggregates rebuilds hourly and daily request aggregates from the
// immutable request detail table for the supplied window. It is idempotent and
// safe to run from the single-process maintenance loop.
func (s *Store) RefreshAggregates(ctx context.Context, since time.Time) error {
	for _, bucket := range []string{"hour", "day"} {
		periodExpression := "date_trunc('" + bucket + "', created_at)"
		_, err := s.pool.Exec(ctx, `
			INSERT INTO aggregates(bucket, period, user_id, api_key_id, model_alias, provider_id, provider_model, api, status,
				requests, success, input_tokens, output_tokens, total_tokens, cost, credits, latency_sum, ttft_sum)
			SELECT $1, `+periodExpression+`, user_id, api_key_id, model_alias, 0, '', api, status,
				count(*), count(*) FILTER (WHERE status='success'),
				COALESCE(SUM(COALESCE((usage->'meters'->>'input')::bigint,0)),0),
				COALESCE(SUM(COALESCE((usage->'meters'->>'output')::bigint,0)),0),
				COALESCE(SUM(COALESCE((usage->'meters'->>'total')::bigint,0)),0),
				COALESCE(SUM(cost),0), COALESCE(SUM(credits),0),
				COALESCE(SUM(total_latency_ms),0), COALESCE(SUM(ttft_ms),0)
			FROM requests
			WHERE created_at >= $2
			GROUP BY `+periodExpression+`, user_id, api_key_id, model_alias, api, status
			ON CONFLICT (bucket, period, user_id, api_key_id, model_alias, provider_id, provider_model, api, status)
			DO UPDATE SET requests=EXCLUDED.requests, success=EXCLUDED.success,
				input_tokens=EXCLUDED.input_tokens, output_tokens=EXCLUDED.output_tokens,
				total_tokens=EXCLUDED.total_tokens, cost=EXCLUDED.cost, credits=EXCLUDED.credits,
				latency_sum=EXCLUDED.latency_sum, ttft_sum=EXCLUDED.ttft_sum`, bucket, since)
		if err != nil {
			return err
		}
	}
	return nil
}
