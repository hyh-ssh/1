package store

import (
	"context"
	"strconv"

	"github.com/zjvtit/omni-gateway/internal/model"
)

// ListLedgerEntries returns immutable meter charges with their attempt identity.
func (s *Store) ListLedgerEntries(ctx context.Context, requestID string) ([]*model.LedgerEntry, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, settlement_id, request_id, user_id, api_key_id, model_alias, provider_id,
		provider_model, meter, quantity, cost, credits, price_version, estimated, created_at
		FROM usage_ledger WHERE request_id=$1 ORDER BY created_at, settlement_id, meter`, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*model.LedgerEntry, 0)
	for rows.Next() {
		entry := &model.LedgerEntry{}
		if err := rows.Scan(&entry.ID, &entry.SettlementID, &entry.RequestID, &entry.UserID, &entry.APIKeyID, &entry.ModelAlias, &entry.ProviderID, &entry.ProviderModel, &entry.Meter, &entry.Quantity, &entry.Cost, &entry.Credits, &entry.PriceVersion, &entry.Estimated, &entry.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, rows.Err()
}

func (s *Store) routeStats(ctx context.Context, conditions string, args []any, filter model.StatsFilter) ([]model.RouteStats, error) {
	query := `WITH selected AS (SELECT id, model_alias FROM requests WHERE ` + conditions + `)
		SELECT r.model_alias, a.provider_id, a.provider_model, count(*)::bigint,
		count(*) FILTER (WHERE a.status='success')::bigint,
		COALESCE(AVG(a.latency_ms),0),
		COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY a.latency_ms),0),
		COALESCE(SUM(a.cost),0), COALESCE(SUM(l.credits),0),
		COALESCE(SUM(a.cost) FILTER (WHERE a.status!='success'),0),
		count(*) FILTER (WHERE a.settlement_error!='')::bigint
		FROM selected r JOIN attempts a ON a.request_id=r.id
		LEFT JOIN LATERAL (SELECT SUM(credits) AS credits FROM usage_ledger WHERE settlement_id=a.settlement_id) l ON true
		WHERE 1=1`
	args = append([]any(nil), args...)
	if filter.ProviderID != 0 {
		args = append(args, filter.ProviderID)
		query += " AND a.provider_id=$" + strconv.Itoa(len(args))
	}
	if filter.ProviderModel != "" {
		args = append(args, filter.ProviderModel)
		query += " AND a.provider_model=$" + strconv.Itoa(len(args))
	}
	query += " GROUP BY r.model_alias, a.provider_id, a.provider_model ORDER BY r.model_alias, a.provider_id, a.provider_model"
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.RouteStats, 0)
	for rows.Next() {
		var route model.RouteStats
		if err := rows.Scan(&route.ModelAlias, &route.ProviderID, &route.ProviderModel, &route.Attempts, &route.Success, &route.AvgLatencyMS, &route.P95LatencyMS, &route.Cost, &route.Credits, &route.FailedCost, &route.SettlementErrors); err != nil {
			return nil, err
		}
		route.Errors = route.Attempts - route.Success
		if route.Attempts > 0 {
			route.SuccessRate = float64(route.Success) * 100 / float64(route.Attempts)
		}
		out = append(out, route)
	}
	return out, rows.Err()
}
