package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/zjvtit/omni-gateway/internal/model"
)

// InsertRequest inserts a request record.
func (s *Store) InsertRequest(ctx context.Context, r *model.Request) error {
	if r == nil || r.ID == "" {
		return errors.New("request id is required")
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	usage, _ := json.Marshal(r.Usage)
	tags, _ := json.Marshal(r.Tags)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO requests(id, user_id, api_key_id, api, streaming, model_alias, status, status_code,
			error_type, error_summary, usage, cost, credits, total_latency_ms, upstream_latency_ms,
			ttft_ms, output_tokens_per_sec, client_ip, user_agent, tags, created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)
		ON CONFLICT (id) DO NOTHING`,
		r.ID, r.UserID, r.APIKeyID, r.API, r.Streaming, r.ModelAlias, string(r.Status), r.StatusCode,
		r.ErrorType, r.ErrorSummary, usage, r.Cost, r.Credits, r.TotalLatencyMS, r.UpstreamLatencyMS,
		r.TTFTMS, r.OutputTokensPerSec, r.ClientIP, r.UserAgent, tags, r.CreatedAt)
	return err
}

// InsertAttempt inserts an attempt record.
func (s *Store) InsertAttempt(ctx context.Context, a *model.Attempt) error {
	if a == nil || a.SettlementID == "" {
		return errors.New("attempt settlement id is required")
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC()
	}
	usage, _ := json.Marshal(a.Usage)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO attempts(request_id, provider_id, provider_model, priority, status, status_code,
			error_type, error_summary, usage, cost, latency_ms, ttft_ms, created_at, settlement_id, settlement_error, settlement_status)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
		ON CONFLICT (settlement_id) DO NOTHING`,
		a.RequestID, a.ProviderID, a.ProviderModel, a.Priority, a.Status, a.StatusCode,
		a.ErrorType, a.ErrorSummary, usage, a.Cost, a.LatencyMS, a.TTFTMS, a.CreatedAt, a.SettlementID, a.SettlementError, a.SettlementStatus)
	return err
}

// InsertLedgerEntries inserts multiple ledger entries in one statement.
func (s *Store) InsertLedgerEntries(ctx context.Context, entries []*model.LedgerEntry) error {
	if len(entries) == 0 {
		return nil
	}
	for _, e := range entries {
		if e == nil || e.SettlementID == "" || e.Meter == "" {
			return errors.New("ledger settlement id and meter are required")
		}
		if e.CreatedAt.IsZero() {
			e.CreatedAt = time.Now().UTC()
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, e := range entries {
		if _, err := tx.Exec(ctx, `
			INSERT INTO usage_ledger(settlement_id, request_id, user_id, api_key_id, model_alias, provider_id,
				provider_model, meter, quantity, cost, credits, price_version, estimated, created_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
			ON CONFLICT (settlement_id, meter) DO NOTHING`,
			e.SettlementID, e.RequestID, e.UserID, e.APIKeyID, e.ModelAlias, e.ProviderID,
			e.ProviderModel, e.Meter, e.Quantity, e.Cost, e.Credits, e.PriceVersion, e.Estimated, e.CreatedAt); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// InsertAudit inserts an audit entry.
func (s *Store) InsertAudit(ctx context.Context, a *model.AuditEntry) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO audit_log(actor_id, actor_name, action, resource, resource_id, detail, ip, result, resource_name, before_summary, after_summary)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		a.ActorID, a.ActorName, a.Action, a.Resource, a.ResourceID, a.Detail, a.IP, auditResult(a.Result), a.ResourceName, safeAuditJSON(a.Before), safeAuditJSON(a.After))
	return err
}

// ListAudit returns recent audit entries.
func (s *Store) ListAudit(ctx context.Context, limit int) ([]*model.AuditEntry, error) {
	return s.ListAuditFiltered(ctx, model.AuditFilter{Limit: limit})
}
