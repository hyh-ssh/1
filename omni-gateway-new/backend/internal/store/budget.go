package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/lib/pq"
	"github.com/zjvtit/omni-gateway/internal/model"
)

// CreateBudgetRule inserts a budget rule.
func (s *Store) CreateBudgetRule(ctx context.Context, r *model.BudgetRule) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO budget_rules(user_id, window_secs, cost_limit, credit_limit, active)
		VALUES($1,$2,$3,$4,$5) RETURNING id`,
		r.UserID, r.WindowSecs, r.CostLimit, r.CreditLimit, r.Active).Scan(&id)
	return id, err
}

// ListBudgetRules returns all rules for a user.
func (s *Store) ListBudgetRules(ctx context.Context, userID int64) ([]*model.BudgetRule, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, window_secs, cost_limit, credit_limit, active, created_at
		FROM budget_rules WHERE user_id=$1 ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.BudgetRule
	for rows.Next() {
		r := &model.BudgetRule{}
		if err := rows.Scan(&r.ID, &r.UserID, &r.WindowSecs, &r.CostLimit, &r.CreditLimit, &r.Active, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListAllBudgetRules returns rules for all users for administrator dashboards.
func (s *Store) ListAllBudgetRules(ctx context.Context) ([]*model.BudgetRule, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, user_id, window_secs, cost_limit, credit_limit, active, created_at FROM budget_rules ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.BudgetRule
	for rows.Next() {
		rule := &model.BudgetRule{}
		if err := rows.Scan(&rule.ID, &rule.UserID, &rule.WindowSecs, &rule.CostLimit, &rule.CreditLimit, &rule.Active, &rule.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, rule)
	}
	return out, rows.Err()
}

// GetBudgetRule fetches one rule for administrator updates.
func (s *Store) GetBudgetRule(ctx context.Context, id int64) (*model.BudgetRule, error) {
	rule := &model.BudgetRule{}
	err := s.pool.QueryRow(ctx, `SELECT id, user_id, window_secs, cost_limit, credit_limit, active, created_at FROM budget_rules WHERE id=$1`, id).Scan(&rule.ID, &rule.UserID, &rule.WindowSecs, &rule.CostLimit, &rule.CreditLimit, &rule.Active, &rule.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return rule, err
}

// UpdateBudgetRule updates a rule.
func (s *Store) UpdateBudgetRule(ctx context.Context, r *model.BudgetRule) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE budget_rules SET window_secs=$1, cost_limit=$2, credit_limit=$3, active=$4
		WHERE id=$5`,
		r.WindowSecs, r.CostLimit, r.CreditLimit, r.Active, r.ID)
	return err
}

// DeleteBudgetRule removes a rule.
func (s *Store) DeleteBudgetRule(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM budget_rules WHERE id=$1`, id)
	return err
}

// SumLedgerInWindow returns the total cost and credits for a user within a
// rolling window ending at now.
func (s *Store) SumLedgerInWindow(ctx context.Context, userID int64, windowSecs int64) (cost model.Money, credits model.Credits, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(cost),0), COALESCE(SUM(credits),0)
		FROM usage_ledger
		WHERE user_id=$1 AND created_at >= now() - ($2 * interval '1 second')`,
		userID, windowSecs).Scan(&cost, &credits)
	return
}

// SumLedgerInWindowExcludingSettlements prevents a pending reservation from
// double counting a commit whose acknowledgement was lost.
func (s *Store) SumLedgerInWindowExcludingSettlements(ctx context.Context, userID int64, windowSecs int64, ids []string) (cost model.Money, credits model.Credits, err error) {
	if len(ids) == 0 {
		return s.SumLedgerInWindow(ctx, userID, windowSecs)
	}
	err = s.pool.QueryRow(ctx, `SELECT COALESCE(SUM(cost),0), COALESCE(SUM(credits),0)
		FROM usage_ledger WHERE user_id=$1 AND created_at >= now() - ($2 * interval '1 second')
		AND NOT (settlement_id = ANY($3::text[]))`, userID, windowSecs, pq.Array(ids)).Scan(&cost, &credits)
	return
}
