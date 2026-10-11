package store

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/zjvtit/omni-gateway/internal/model"
)

func auditResult(value string) string {
	switch value {
	case "success", "failure", "denied":
		return value
	}
	return "unknown"
}
func safeAuditJSON(value map[string]any) any {
	if value == nil {
		return nil
	}
	data, err := json.Marshal(model.SafeAuditSummary(value))
	if err != nil {
		return nil
	}
	return data
}
func (s *Store) ListAuditFiltered(ctx context.Context, filter model.AuditFilter) ([]*model.AuditEntry, error) {
	query := `SELECT id,actor_id,actor_name,action,resource,resource_id,detail,ip,created_at,result,resource_name,before_summary,after_summary FROM audit_log WHERE 1=1`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += " AND " + condition + "$" + strconv.Itoa(len(args))
	}
	if !filter.From.IsZero() {
		add("created_at>=", filter.From.UTC())
	}
	if !filter.To.IsZero() {
		add("created_at<=", filter.To.UTC())
	}
	if filter.ActorID != 0 {
		add("actor_id=", filter.ActorID)
	}
	if filter.Resource != "" {
		add("resource=", filter.Resource)
	}
	if filter.Action != "" {
		add("action=", filter.Action)
	}
	if filter.Result != "" {
		add("result=", filter.Result)
	}
	limit := filter.Limit
	if limit < 1 || limit > 200 {
		limit = 50
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}
	query += " ORDER BY created_at DESC,id DESC LIMIT $" + strconv.Itoa(len(args)+1) + " OFFSET $" + strconv.Itoa(len(args)+2)
	args = append(args, limit, offset)
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.AuditEntry{}
	for rows.Next() {
		a := &model.AuditEntry{}
		var before, after []byte
		if err := rows.Scan(&a.ID, &a.ActorID, &a.ActorName, &a.Action, &a.Resource, &a.ResourceID, &a.Detail, &a.IP, &a.CreatedAt, &a.Result, &a.ResourceName, &before, &after); err != nil {
			return nil, err
		}
		if len(before) > 0 {
			if err := json.Unmarshal(before, &a.Before); err != nil {
				return nil, err
			}
		}
		if len(after) > 0 {
			if err := json.Unmarshal(after, &a.After); err != nil {
				return nil, err
			}
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
