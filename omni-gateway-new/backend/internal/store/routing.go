package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/zjvtit/omni-gateway/internal/model"
)

// CreateAlias inserts a model alias.
func (s *Store) CreateAlias(ctx context.Context, name string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO model_aliases(name, active) VALUES($1, FALSE) RETURNING id`, name).Scan(&id)
	return id, err
}

// GetAlias fetches an alias by name.
func (s *Store) GetAlias(ctx context.Context, name string) (*model.ModelAlias, error) {
	a := &model.ModelAlias{}
	err := s.pool.QueryRow(ctx, `
		SELECT id, name, active, created_at FROM model_aliases WHERE name=$1`, name,
	).Scan(&a.ID, &a.Name, &a.Active, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

// ListAliases returns all aliases.
func (s *Store) ListAliases(ctx context.Context) ([]*model.ModelAlias, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, active, created_at FROM model_aliases ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.ModelAlias
	for rows.Next() {
		a := &model.ModelAlias{}
		if err := rows.Scan(&a.ID, &a.Name, &a.Active, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

var ErrAliasNotReady = errors.New("alias has no enabled route to an enabled provider")

// SetAliasActive toggles alias active state.
func (s *Store) SetAliasActive(ctx context.Context, id int64, active bool) error {
	tag, err := s.pool.Exec(ctx, `UPDATE model_aliases SET active=$1 WHERE id=$2 AND (NOT $1 OR active OR EXISTS(SELECT 1 FROM route_entries r JOIN providers p ON p.id=r.provider_id WHERE r.alias_id=$2 AND r.active AND p.active))`, active, id)
	if err != nil {
		return err
	}
	rowsAffected, _ := tag.RowsAffected()
	if rowsAffected == 0 {
		var exists bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM model_aliases WHERE id=$1)`, id).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return ErrAliasNotReady
		}
		return ErrNotFound
	}
	return nil
}

// ErrAliasConflict is returned when renaming an alias onto an existing name.
var ErrAliasConflict = errors.New("alias name already exists")

// RenameAlias changes the exposed name of an alias. Route entries keep pointing
// at the same alias id, so an in-flight request is unaffected.
func (s *Store) RenameAlias(ctx context.Context, id int64, name string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE model_aliases SET name=$1 WHERE id=$2`, name, id)
	if err != nil {
		var state interface{ SQLState() string }
		if errors.As(err, &state) && state.SQLState() == "23505" {
			return ErrAliasConflict
		}
		return err
	}
	rowsAffected, _ := tag.RowsAffected()
	if rowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteAlias removes an alias. Route entries (and any group grants) cascade.
func (s *Store) DeleteAlias(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM model_aliases WHERE id=$1`, id)
	if err != nil {
		return err
	}
	rowsAffected, _ := tag.RowsAffected()
	if rowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// AddRouteEntry inserts a route entry.
func (s *Store) AddRouteEntry(ctx context.Context, aliasID, providerID int64, providerModel string, priority int) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO route_entries(alias_id, provider_id, provider_model, priority)
		VALUES($1,$2,$3,$4) RETURNING id`,
		aliasID, providerID, providerModel, priority).Scan(&id)
	return id, err
}

// ListRouteEntries returns ordered route entries for an alias.
func (s *Store) ListRouteEntries(ctx context.Context, aliasID int64) ([]*model.RouteEntry, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, alias_id, provider_id, provider_model, priority, active
		FROM route_entries WHERE alias_id=$1 ORDER BY priority ASC`, aliasID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.RouteEntry
	for rows.Next() {
		r := &model.RouteEntry{}
		if err := rows.Scan(&r.ID, &r.AliasID, &r.ProviderID, &r.ProviderModel, &r.Priority, &r.Active); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteRouteEntry removes a route entry.
func (s *Store) DeleteRouteEntry(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM route_entries WHERE id=$1`, id)
	return err
}

// SetRouteEntryActive toggles a route entry's active state.
func (s *Store) SetRouteEntryActive(ctx context.Context, id int64, active bool) error {
	_, err := s.pool.Exec(ctx, `UPDATE route_entries SET active=$1 WHERE id=$2`, active, id)
	return err
}
