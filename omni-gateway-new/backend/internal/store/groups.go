package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/lib/pq"
	"github.com/zjvtit/omni-gateway/internal/model"
)

var (
	ErrGroupConflict = errors.New("group name already exists")
	ErrInvalidGroup  = errors.New("invalid group or group relations")
)

// ModelGroupStore and UserGroupStore are split interfaces for optional
// dependency injection and HTTP test doubles.
type ModelGroupStore interface {
	ListModelGroups(context.Context) ([]*model.ModelGroup, error)
	GetModelGroup(context.Context, int64) (*model.ModelGroup, error)
	CreateModelGroup(context.Context, *model.ModelGroup) (int64, error)
	UpdateModelGroup(context.Context, *model.ModelGroup) error
	DeleteModelGroup(context.Context, int64) error
}
type UserGroupStore interface {
	ListUserGroups(context.Context) ([]*model.UserGroup, error)
	GetUserGroup(context.Context, int64) (*model.UserGroup, error)
	CreateUserGroup(context.Context, *model.UserGroup) (int64, error)
	UpdateUserGroup(context.Context, *model.UserGroup) error
	DeleteUserGroup(context.Context, int64) error
}

const modelGroupSelect = `SELECT g.id, g.name, g.active, g.created_at, g.updated_at,
	ARRAY(SELECT alias_id FROM model_group_aliases WHERE model_group_id=g.id ORDER BY alias_id)
	FROM model_groups g`

const userGroupSelect = `SELECT g.id, g.name, g.active, g.created_at, g.updated_at,
	ARRAY(SELECT user_id FROM user_group_members WHERE user_group_id=g.id ORDER BY user_id),
	ARRAY(SELECT model_group_id FROM user_group_model_groups WHERE user_group_id=g.id ORDER BY model_group_id)
	FROM user_groups g`

type rowScanner interface{ Scan(...any) error }

func scanModelGroup(row rowScanner) (*model.ModelGroup, error) {
	g := &model.ModelGroup{}
	err := row.Scan(&g.ID, &g.Name, &g.Active, &g.CreatedAt, &g.UpdatedAt, pq.Array(&g.AliasIDs))
	if err != nil {
		return nil, groupStoreError(err)
	}
	return g, nil
}

func scanUserGroup(row rowScanner) (*model.UserGroup, error) {
	g := &model.UserGroup{}
	err := row.Scan(&g.ID, &g.Name, &g.Active, &g.CreatedAt, &g.UpdatedAt, pq.Array(&g.UserIDs), pq.Array(&g.ModelGroupIDs))
	if err != nil {
		return nil, groupStoreError(err)
	}
	return g, nil
}

// ListModelGroups returns groups with their complete alias memberships.
func (s *Store) ListModelGroups(ctx context.Context) ([]*model.ModelGroup, error) {
	rows, err := s.pool.Query(ctx, modelGroupSelect+` ORDER BY g.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*model.ModelGroup, 0)
	for rows.Next() {
		g, err := scanModelGroup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// GetModelGroup returns a group and its alias IDs.
func (s *Store) GetModelGroup(ctx context.Context, id int64) (*model.ModelGroup, error) {
	return scanModelGroup(s.pool.QueryRow(ctx, modelGroupSelect+` WHERE g.id=$1`, id))
}

// CreateModelGroup creates the group and all relations atomically.
func (s *Store) CreateModelGroup(ctx context.Context, g *model.ModelGroup) (int64, error) {
	if g == nil || !validGroup(g.Name, g.AliasIDs) {
		return 0, ErrInvalidGroup
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	err = tx.QueryRow(ctx, `INSERT INTO model_groups(name, active) VALUES($1,$2)
		RETURNING id, created_at, updated_at`, strings.TrimSpace(g.Name), g.Active).Scan(&g.ID, &g.CreatedAt, &g.UpdatedAt)
	if err == nil {
		err = replaceModelGroupAliases(ctx, tx, g.ID, g.AliasIDs)
	}
	if err != nil {
		return 0, groupStoreError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, groupStoreError(err)
	}
	return g.ID, nil
}

// UpdateModelGroup replaces mutable fields and the full alias relation set.
func (s *Store) UpdateModelGroup(ctx context.Context, g *model.ModelGroup) error {
	if g == nil || g.ID <= 0 || !validGroup(g.Name, g.AliasIDs) {
		return ErrInvalidGroup
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	err = tx.QueryRow(ctx, `UPDATE model_groups SET name=$1, active=$2, updated_at=now()
		WHERE id=$3 RETURNING updated_at`, strings.TrimSpace(g.Name), g.Active, g.ID).Scan(&g.UpdatedAt)
	if err == nil {
		err = replaceModelGroupAliases(ctx, tx, g.ID, g.AliasIDs)
	}
	if err != nil {
		return groupStoreError(err)
	}
	return groupStoreError(tx.Commit(ctx))
}

type txExecutor interface {
	Exec(context.Context, string, ...any) (sql.Result, error)
}

func replaceModelGroupAliases(ctx context.Context, tx txExecutor, id int64, aliases []int64) error {
	if _, err := tx.Exec(ctx, `DELETE FROM model_group_aliases WHERE model_group_id=$1`, id); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO model_group_aliases(model_group_id, alias_id)
		SELECT $1, unnest($2::bigint[])`, id, pq.Array(aliases))
	return err
}

// DeleteModelGroup removes the group and its grants. User memberships remain
// restricted even if their last model group has been deleted.
func (s *Store) DeleteModelGroup(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM model_groups WHERE id=$1`, id)
	if err != nil {
		return groupStoreError(err)
	}
	rowsAffected, _ := tag.RowsAffected()
	if rowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// ListUserGroups returns groups with all user and model-group memberships.
func (s *Store) ListUserGroups(ctx context.Context) ([]*model.UserGroup, error) {
	rows, err := s.pool.Query(ctx, userGroupSelect+` ORDER BY g.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*model.UserGroup, 0)
	for rows.Next() {
		g, err := scanUserGroup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// GetUserGroup returns a group and its complete relation sets.
func (s *Store) GetUserGroup(ctx context.Context, id int64) (*model.UserGroup, error) {
	return scanUserGroup(s.pool.QueryRow(ctx, userGroupSelect+` WHERE g.id=$1`, id))
}

// CreateUserGroup creates the group and both relation sets atomically.
func (s *Store) CreateUserGroup(ctx context.Context, g *model.UserGroup) (int64, error) {
	if g == nil || !validGroup(g.Name, g.UserIDs, g.ModelGroupIDs) {
		return 0, ErrInvalidGroup
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	err = tx.QueryRow(ctx, `INSERT INTO user_groups(name, active) VALUES($1,$2)
		RETURNING id, created_at, updated_at`, strings.TrimSpace(g.Name), g.Active).Scan(&g.ID, &g.CreatedAt, &g.UpdatedAt)
	if err == nil {
		err = replaceUserGroupRelations(ctx, tx, g)
	}
	if err != nil {
		return 0, groupStoreError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, groupStoreError(err)
	}
	return g.ID, nil
}

// UpdateUserGroup replaces mutable fields and both complete relation sets.
func (s *Store) UpdateUserGroup(ctx context.Context, g *model.UserGroup) error {
	if g == nil || g.ID <= 0 || !validGroup(g.Name, g.UserIDs, g.ModelGroupIDs) {
		return ErrInvalidGroup
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	err = tx.QueryRow(ctx, `UPDATE user_groups SET name=$1, active=$2, updated_at=now()
		WHERE id=$3 RETURNING updated_at`, strings.TrimSpace(g.Name), g.Active, g.ID).Scan(&g.UpdatedAt)
	if err == nil {
		err = replaceUserGroupRelations(ctx, tx, g)
	}
	if err != nil {
		return groupStoreError(err)
	}
	return groupStoreError(tx.Commit(ctx))
}

func replaceUserGroupRelations(ctx context.Context, tx txExecutor, g *model.UserGroup) error {
	if _, err := tx.Exec(ctx, `DELETE FROM user_group_members WHERE user_group_id=$1`, g.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO user_group_members(user_group_id, user_id)
		SELECT $1, unnest($2::bigint[])`, g.ID, pq.Array(g.UserIDs)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM user_group_model_groups WHERE user_group_id=$1`, g.ID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO user_group_model_groups(user_group_id, model_group_id)
		SELECT $1, unnest($2::bigint[])`, g.ID, pq.Array(g.ModelGroupIDs))
	return err
}

// DeleteUserGroup explicitly removes its memberships. Users with no remaining
// memberships revert to the legacy API-key-only access policy.
func (s *Store) DeleteUserGroup(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM user_groups WHERE id=$1`, id)
	if err != nil {
		return groupStoreError(err)
	}
	rowsAffected, _ := tag.RowsAffected()
	if rowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// GetUserModelAccess computes group restrictions in one database snapshot.
// Membership in disabled groups still restricts access; only active user
// groups, model groups and aliases contribute grants. No membership preserves
// existing users' access, and explicit removal of all memberships restores it.
func (s *Store) GetUserModelAccess(ctx context.Context, userID int64) (*model.ModelAccess, error) {
	access := &model.ModelAccess{}
	err := s.pool.QueryRow(ctx, `SELECT
		EXISTS(SELECT 1 FROM user_group_members WHERE user_id=$1),
		ARRAY(SELECT DISTINCT a.name FROM user_group_members m
			JOIN user_groups ug ON ug.id=m.user_group_id AND ug.active
			JOIN user_group_model_groups gm ON gm.user_group_id=ug.id
			JOIN model_groups mg ON mg.id=gm.model_group_id AND mg.active
			JOIN model_group_aliases ma ON ma.model_group_id=mg.id
			JOIN model_aliases a ON a.id=ma.alias_id AND a.active
		WHERE m.user_id=$1 ORDER BY a.name)
		FROM users WHERE id=$1`, userID).Scan(&access.Restricted, pq.Array(&access.Models))
	if err != nil {
		return nil, groupStoreError(err)
	}
	return access, nil
}

func validGroup(name string, relations ...[]int64) bool {
	name = strings.TrimSpace(name)
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 200 || strings.ContainsRune(name, 0) {
		return false
	}
	for _, ids := range relations {
		if len(ids) > 10000 {
			return false
		}
		seen := make(map[int64]bool, len(ids))
		for _, id := range ids {
			if id <= 0 || seen[id] {
				return false
			}
			seen[id] = true
		}
	}
	return true
}

func groupStoreError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	var state interface{ SQLState() string }
	if errors.As(err, &state) {
		switch state.SQLState() {
		case "23505":
			return ErrGroupConflict
		case "23503", "23514":
			return ErrInvalidGroup
		}
	}
	return err
}
