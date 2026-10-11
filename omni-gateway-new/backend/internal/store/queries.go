package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/lib/pq"
	"github.com/zjvtit/omni-gateway/internal/model"
)

// ListRequests returns request records using bounded, indexed filters.
func (s *Store) ListRequests(ctx context.Context, filter model.StatsFilter) ([]*model.Request, error) {
	query := `SELECT r.id, r.user_id, r.api_key_id, r.api, r.streaming, r.model_alias, r.status, r.status_code,
		r.error_type, r.error_summary, r.usage, r.cost, r.credits, r.total_latency_ms, r.upstream_latency_ms,
		r.ttft_ms, r.output_tokens_per_sec, r.client_ip, r.user_agent, r.tags, r.created_at, ` + requestSettlementSQL("r") + ` AS settlement_status FROM requests r WHERE 1=1`
	args := make([]any, 0, 8)
	add := func(condition string, value any) {
		args = append(args, value)
		query += " AND " + condition + "$" + strconv.Itoa(len(args))
	}
	if filter.RequestID != "" {
		add("r.id=", filter.RequestID)
	}
	if filter.SettlementStatus != "" {
		add("("+requestSettlementSQL("r")+")=", filter.SettlementStatus)
	}
	if filter.UserID != 0 {
		add("r.user_id=", filter.UserID)
	}
	if filter.APIKeyID != 0 {
		add("r.api_key_id=", filter.APIKeyID)
	}
	if filter.API != "" {
		add("r.api=", filter.API)
	}
	if filter.ProviderID != 0 {
		args = append(args, filter.ProviderID)
		query += " AND EXISTS (SELECT 1 FROM attempts a WHERE a.request_id = r.id AND a.provider_id=$" + strconv.Itoa(len(args)) + ")"
	}
	if filter.ProviderModel != "" {
		args = append(args, filter.ProviderModel)
		query += " AND EXISTS (SELECT 1 FROM attempts a WHERE a.request_id = r.id AND a.provider_model=$" + strconv.Itoa(len(args)) + ")"
	}
	if filter.ModelAlias != "" {
		add("r.model_alias=", filter.ModelAlias)
	}
	if filter.ModelGroupID != 0 {
		args = append(args, filter.ModelGroupID)
		query += " AND EXISTS (SELECT 1 FROM model_group_aliases g JOIN model_aliases m ON m.id=g.alias_id WHERE m.name=r.model_alias AND g.model_group_id=$" + strconv.Itoa(len(args)) + ")"
	}
	if filter.Status != "" {
		add("r.status=", string(filter.Status))
	}
	if !filter.From.IsZero() {
		add("r.created_at >= ", filter.From)
	}
	if !filter.To.IsZero() {
		add("r.created_at <= ", filter.To)
	}
	limit := filter.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}
	query += " ORDER BY r.created_at DESC, r.id DESC LIMIT $" + strconv.Itoa(len(args)+1) + " OFFSET $" + strconv.Itoa(len(args)+2)
	args = append(args, limit, offset)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.Request
	for rows.Next() {
		request := &model.Request{}
		var usage, tags []byte
		if err := rows.Scan(&request.ID, &request.UserID, &request.APIKeyID, &request.API, &request.Streaming, &request.ModelAlias, &request.Status, &request.StatusCode, &request.ErrorType, &request.ErrorSummary, &usage, &request.Cost, &request.Credits, &request.TotalLatencyMS, &request.UpstreamLatencyMS, &request.TTFTMS, &request.OutputTokensPerSec, &request.ClientIP, &request.UserAgent, &tags, &request.CreatedAt, &request.SettlementStatus); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(usage, &request.Usage); err != nil {
			return nil, err
		}
		if len(tags) > 0 && string(tags) != "null" {
			if err := json.Unmarshal(tags, &request.Tags); err != nil {
				return nil, err
			}
		}
		out = append(out, request)
	}
	return out, rows.Err()
}

// GetRequest fetches one request by its public request ID.
func (s *Store) GetRequest(ctx context.Context, id string) (*model.Request, error) {
	// Use a direct primary-key lookup so this path does not depend on list ordering.
	request := &model.Request{}
	var usage, tags []byte
	err := s.pool.QueryRow(ctx, `SELECT id, user_id, api_key_id, api, streaming, model_alias, status, status_code,
		error_type, error_summary, usage, cost, credits, total_latency_ms, upstream_latency_ms,
		ttft_ms, output_tokens_per_sec, client_ip, user_agent, tags, created_at, `+requestSettlementSQL("requests")+` AS settlement_status FROM requests WHERE id=$1`, id).
		Scan(&request.ID, &request.UserID, &request.APIKeyID, &request.API, &request.Streaming, &request.ModelAlias, &request.Status, &request.StatusCode, &request.ErrorType, &request.ErrorSummary, &usage, &request.Cost, &request.Credits, &request.TotalLatencyMS, &request.UpstreamLatencyMS, &request.TTFTMS, &request.OutputTokensPerSec, &request.ClientIP, &request.UserAgent, &tags, &request.CreatedAt, &request.SettlementStatus)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if err := json.Unmarshal(usage, &request.Usage); err != nil {
		return nil, err
	}
	if len(tags) > 0 && string(tags) != "null" {
		if err := json.Unmarshal(tags, &request.Tags); err != nil {
			return nil, err
		}
	}
	return request, nil
}

// ListAttempts returns all upstream attempts for a request.
func (s *Store) ListAttempts(ctx context.Context, requestID string) ([]*model.Attempt, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, request_id, provider_id, provider_model, priority, status, status_code,
		error_type, error_summary, usage, cost, latency_ms, ttft_ms, created_at, settlement_id, settlement_error, settlement_status FROM attempts WHERE request_id=$1 ORDER BY created_at, id`, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.Attempt
	for rows.Next() {
		attempt := &model.Attempt{}
		var usage []byte
		if err := rows.Scan(&attempt.ID, &attempt.RequestID, &attempt.ProviderID, &attempt.ProviderModel, &attempt.Priority, &attempt.Status, &attempt.StatusCode, &attempt.ErrorType, &attempt.ErrorSummary, &usage, &attempt.Cost, &attempt.LatencyMS, &attempt.TTFTMS, &attempt.CreatedAt, &attempt.SettlementID, &attempt.SettlementError, &attempt.SettlementStatus); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(usage, &attempt.Usage); err != nil {
			return nil, err
		}
		out = append(out, attempt)
	}
	return out, rows.Err()
}

// ListAPIKeys returns all API keys for administrator views without exposing
// the stored hash or raw secret.
func (s *Store) ListAPIKeys(ctx context.Context) ([]*model.APIKey, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, user_id, name, key_prefix, key_hash, key_suffix, active, expires_at, model_allow, created_at, last_used_at FROM api_keys ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.APIKey
	for rows.Next() {
		key := &model.APIKey{}
		if err := rows.Scan(&key.ID, &key.UserID, &key.Name, &key.KeyPrefix, &key.KeyHash, &key.KeySuffix, &key.Active, &key.ExpiresAt, pq.Array(&key.ModelAllow), &key.CreatedAt, &key.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, key)
	}
	return out, rows.Err()
}

// Stats returns aggregate request metrics for the dashboard.
func (s *Store) Stats(ctx context.Context, filter model.StatsFilter) (*model.StatsSummary, error) {
	conditions := []string{"1=1"}
	args := make([]any, 0, 6)
	add := func(condition string, value any) {
		args = append(args, value)
		conditions = append(conditions, condition+"$"+strconv.Itoa(len(args)))
	}
	if filter.RequestID != "" {
		add("id=", filter.RequestID)
	}
	if filter.SettlementStatus != "" {
		add("("+requestSettlementSQL("requests")+")=", filter.SettlementStatus)
	}
	if filter.UserID != 0 {
		add("user_id=", filter.UserID)
	}
	if filter.APIKeyID != 0 {
		add("api_key_id=", filter.APIKeyID)
	}
	if filter.API != "" {
		add("api=", filter.API)
	}
	if filter.ProviderID != 0 {
		args = append(args, filter.ProviderID)
		conditions = append(conditions, "EXISTS (SELECT 1 FROM attempts a WHERE a.request_id = requests.id AND a.provider_id=$"+strconv.Itoa(len(args))+")")
	}
	if filter.ProviderModel != "" {
		args = append(args, filter.ProviderModel)
		conditions = append(conditions, "EXISTS (SELECT 1 FROM attempts a WHERE a.request_id = requests.id AND a.provider_model=$"+strconv.Itoa(len(args))+")")
	}
	if filter.ModelAlias != "" {
		add("model_alias=", filter.ModelAlias)
	}
	if filter.ModelGroupID != 0 {
		args = append(args, filter.ModelGroupID)
		conditions = append(conditions, "EXISTS (SELECT 1 FROM model_group_aliases g JOIN model_aliases m ON m.id=g.alias_id WHERE m.name=requests.model_alias AND g.model_group_id=$"+strconv.Itoa(len(args))+")")
	}
	if filter.Status != "" {
		add("status=", string(filter.Status))
	}
	if !filter.From.IsZero() {
		add("created_at >= ", filter.From)
	}
	if !filter.To.IsZero() {
		add("created_at <= ", filter.To)
	}
	query := fmt.Sprintf(`SELECT count(*)::bigint,
		count(*) FILTER (WHERE status='success')::bigint,
		COALESCE(SUM(COALESCE((usage->'meters'->>'input')::bigint,0)),0)::bigint,
		COALESCE(SUM(COALESCE((usage->'meters'->>'output')::bigint,0)),0)::bigint,
		COALESCE(SUM(COALESCE((usage->'meters'->>'total')::bigint,0)),0)::bigint,
		COALESCE(SUM(cost),0), COALESCE(SUM(credits),0),
		COALESCE(AVG(total_latency_ms),0), COALESCE(AVG(ttft_ms) FILTER (WHERE streaming AND status='success'),0),
		COALESCE(SUM(CASE WHEN status='success' AND id IN (SELECT request_id FROM attempts GROUP BY request_id HAVING count(*) > 1) THEN 1 ELSE 0 END),0)::bigint,
		count(*) FILTER (WHERE (usage->>'estimated')::boolean IS TRUE)::bigint,
		COALESCE(percentile_cont(0.50) WITHIN GROUP (ORDER BY total_latency_ms),0),
		COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY total_latency_ms),0),
		COALESCE(percentile_cont(0.99) WITHIN GROUP (ORDER BY total_latency_ms),0),
		COALESCE(percentile_cont(0.50) WITHIN GROUP (ORDER BY ttft_ms) FILTER (WHERE streaming AND status='success'),0),
		COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY ttft_ms) FILTER (WHERE streaming AND status='success'),0),
		COALESCE(percentile_cont(0.99) WITHIN GROUP (ORDER BY ttft_ms) FILTER (WHERE streaming AND status='success'),0),
		COALESCE(AVG(upstream_latency_ms),0),
		COALESCE(AVG(output_tokens_per_sec) FILTER (WHERE status='success'),0)
		FROM requests WHERE %s`, strings.Join(conditions, " AND "))
	stats := &model.StatsSummary{}
	if err := s.pool.QueryRow(ctx, query, args...).Scan(&stats.Requests, &stats.Success, &stats.InputTokens, &stats.OutputTokens, &stats.TotalTokens, &stats.Cost, &stats.Credits, &stats.AvgLatencyMS, &stats.AvgTTFTMS, &stats.FailoverCount, &stats.EstimatedCount, &stats.P50LatencyMS, &stats.P95LatencyMS, &stats.P99LatencyMS, &stats.P50TTFTMS, &stats.P95TTFTMS, &stats.P99TTFTMS, &stats.AvgUpstreamLatencyMS, &stats.AvgOutputTokensPerSec); err != nil {
		return nil, err
	}
	stats.Errors = stats.Requests - stats.Success
	// No profit is derived here. Cost is in CNY while Credits is a credit
	// amount, so subtracting one from the other would produce a number with no
	// unit; the dashboard reports the two accounts side by side instead.
	if stats.Requests > 0 {
		stats.SuccessRate = float64(stats.Success) * 100 / float64(stats.Requests)
		stats.AvgCostPerRequest = stats.Cost.Div(model.NewMoneyInt(stats.Requests))
		stats.AvgCreditsPerRequest = model.RoundCredits(stats.Credits.Div(model.NewCreditsInt(stats.Requests)))
	}
	routes, err := s.routeStats(ctx, strings.Join(conditions, " AND "), args, filter)
	if err != nil {
		return nil, err
	}
	stats.Routes = routes
	if err := s.statsTrend(ctx, stats, strings.Join(conditions, " AND "), args); err != nil {
		return nil, err
	}
	for _, route := range routes {
		stats.FailedAttemptCost = stats.FailedAttemptCost.Add(route.FailedCost)
		stats.SettlementErrors += route.SettlementErrors
	}
	return stats, nil
}
