package store

import (
	"context"
	"database/sql"
	"errors"
)

var ErrRouteOrder = errors.New("route_ids must contain every route of the alias exactly once")

// ReorderRoutes replaces priorities atomically and rejects stale or cross-alias
// route lists. The alias lock also serializes inserts referencing this alias.
func (s *Store) ReorderRoutes(ctx context.Context, aliasID int64, ids []int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id int64
	if err = tx.QueryRow(ctx, `SELECT id FROM model_aliases WHERE id=$1 FOR UPDATE`, aliasID).Scan(&id); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT id FROM route_entries WHERE alias_id=$1 FOR UPDATE`, aliasID)
	if err != nil {
		return err
	}
	remaining := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		remaining[id] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(ids) != len(remaining) {
		return ErrRouteOrder
	}
	for _, id := range ids {
		if !remaining[id] {
			return ErrRouteOrder
		}
		delete(remaining, id)
	}
	for i, id := range ids {
		if _, err := tx.Exec(ctx, `UPDATE route_entries SET priority=$1 WHERE id=$2 AND alias_id=$3`, (i+1)*10, id, aliasID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
