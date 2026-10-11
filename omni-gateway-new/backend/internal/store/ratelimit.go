package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/zjvtit/omni-gateway/internal/model"
)

// CreateRateLimit inserts a rate limit.
func (s *Store) CreateRateLimit(ctx context.Context, r *model.RateLimit) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO rate_limits(scope, scope_id, model_id, rpm, tpm, concurrency, active)
		VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
		r.Scope, r.ScopeID, r.ModelID, r.RPM, r.TPM, r.Concurrency, r.Active).Scan(&id)
	return id, err
}

// ListAllRateLimits returns all rules for administrator dashboards.
func (s *Store) ListAllRateLimits(ctx context.Context) ([]*model.RateLimit, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, scope, scope_id, model_id, rpm, tpm, concurrency, active FROM rate_limits ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.RateLimit
	for rows.Next() {
		rule := &model.RateLimit{}
		if err := rows.Scan(&rule.ID, &rule.Scope, &rule.ScopeID, &rule.ModelID, &rule.RPM, &rule.TPM, &rule.Concurrency, &rule.Active); err != nil {
			return nil, err
		}
		out = append(out, rule)
	}
	return out, rows.Err()
}

// GetRateLimit fetches one rule for administrator updates.
func (s *Store) GetRateLimit(ctx context.Context, id int64) (*model.RateLimit, error) {
	rule := &model.RateLimit{}
	err := s.pool.QueryRow(ctx, `SELECT id, scope, scope_id, model_id, rpm, tpm, concurrency, active FROM rate_limits WHERE id=$1`, id).Scan(&rule.ID, &rule.Scope, &rule.ScopeID, &rule.ModelID, &rule.RPM, &rule.TPM, &rule.Concurrency, &rule.Active)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return rule, err
}

// ListRateLimits returns all rate limits for a scope.
func (s *Store) ListRateLimits(ctx context.Context, scope string, scopeID int64) ([]*model.RateLimit, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, scope, scope_id, model_id, rpm, tpm, concurrency, active
		FROM rate_limits WHERE scope=$1 AND scope_id=$2 ORDER BY id`, scope, scopeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.RateLimit
	for rows.Next() {
		r := &model.RateLimit{}
		if err := rows.Scan(&r.ID, &r.Scope, &r.ScopeID, &r.ModelID, &r.RPM, &r.TPM, &r.Concurrency, &r.Active); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpdateRateLimit updates a rate limit.
func (s *Store) UpdateRateLimit(ctx context.Context, r *model.RateLimit) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE rate_limits SET scope=$1, scope_id=$2, model_id=$3, rpm=$4, tpm=$5, concurrency=$6, active=$7 WHERE id=$8`,
		r.Scope, r.ScopeID, r.ModelID, r.RPM, r.TPM, r.Concurrency, r.Active, r.ID)
	return err
}

// DeleteRateLimit removes a rate limit.
func (s *Store) DeleteRateLimit(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM rate_limits WHERE id=$1`, id)
	return err
}
