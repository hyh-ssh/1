package store

import (
	"context"
	"fmt"
	"time"
)

// requestSettlementSQL is shared by list projection, filters and aggregates so
// pagination cannot disagree with the displayed state. New attempts carry the
// rate-derived state; legacy ledgers are classified against their immutable
// price version, never today's latest price or a rounded zero amount.
// Legacy rows store a numeric version but not the exact price model identity.
// If multiple snapshots could match, all must prove zero rates before calling
// that historical usage free; a newly added upstream price cannot rewrite it.
func requestSettlementSQL(alias string) string {
	id := alias + ".id"
	legacy := `SELECT l.id, p.cost AS rates_cost, p.credits AS rates_credits FROM usage_ledger l
 LEFT JOIN price_versions p ON p.version=l.price_version
 AND (p.model_id=l.provider_model OR p.model_id=l.model_alias)
 WHERE l.request_id=` + id + ` AND l.quantity>0`
	valid := `rates_cost IS NOT NULL AND rates_cost <> '{}'::jsonb AND rates_credits IS NOT NULL AND rates_credits <> '{}'::jsonb
 AND NOT EXISTS(SELECT 1 FROM jsonb_object_keys(rates_cost) k WHERE NOT rates_credits ? k)`
	creditsZero := `NOT EXISTS(SELECT 1 FROM jsonb_each_text(rates_credits) c WHERE c.value::numeric <> 0)`
	costZero := `NOT EXISTS(SELECT 1 FROM jsonb_each_text(rates_cost) c WHERE c.value::numeric <> 0)`
	return fmt.Sprintf(`CASE
 WHEN EXISTS(SELECT 1 FROM attempts a WHERE a.request_id=%[1]s AND (a.settlement_status='failed' OR (a.settlement_error<>'' AND a.settlement_error<>'unpriced'))) THEN 'failed'
 WHEN EXISTS(SELECT 1 FROM attempts a WHERE a.request_id=%[1]s AND (a.settlement_error='unpriced' OR a.settlement_status='unpriced')) THEN 'unpriced'
 WHEN EXISTS(SELECT 1 FROM attempts a WHERE a.request_id=%[1]s AND a.settlement_status='settled') THEN 'settled'
 WHEN EXISTS(SELECT 1 FROM attempts a WHERE a.request_id=%[1]s AND a.settlement_status='credits_free') THEN 'credits_free'
 WHEN EXISTS(SELECT 1 FROM attempts a WHERE a.request_id=%[1]s AND a.settlement_status='free') THEN 'free'
 WHEN EXISTS(SELECT 1 FROM usage_ledger l WHERE l.request_id=%[1]s AND l.quantity>0) THEN
 CASE WHEN NOT EXISTS(SELECT 1 FROM (%[2]s) history WHERE NOT (%[3]s AND %[4]s AND %[5]s)) THEN 'free'
 WHEN NOT EXISTS(SELECT 1 FROM (%[2]s) history WHERE NOT (%[3]s AND %[4]s)) THEN 'credits_free'
 ELSE 'settled' END
 ELSE 'none' END`, id, legacy, valid, creditsZero, costZero)
}

// FirstSuccessfulRequestAt excludes mere model-list reads and failures. It is
// scoped to both the account and selected key, independent of last_used_at.
func (s *Store) FirstSuccessfulRequestAt(ctx context.Context, userID, keyID int64) (*time.Time, error) {
	var first *time.Time
	err := s.pool.QueryRow(ctx, `SELECT min(created_at) FROM requests WHERE user_id=$1 AND api_key_id=$2 AND status='success' AND api<>'models'`, userID, keyID).Scan(&first)
	return first, err
}
