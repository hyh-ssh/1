package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/lib/pq"
	"github.com/zjvtit/omni-gateway/internal/model"
)

// CreateAPIKey inserts a new API key.
func (s *Store) CreateAPIKey(ctx context.Context, k *model.APIKey) (int64, error) {
	modelAllow := k.ModelAllow
	if modelAllow == nil {
		modelAllow = []string{}
	}
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO api_keys(user_id, name, key_prefix, key_hash, key_suffix, active, expires_at, model_allow)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`,
		k.UserID, k.Name, k.KeyPrefix, k.KeyHash, k.KeySuffix, k.Active, k.ExpiresAt, pq.Array(modelAllow),
	).Scan(&id)
	return id, err
}

// GetAPIKeyByHash fetches an API key by its hash.
func (s *Store) GetAPIKeyByHash(ctx context.Context, hash string) (*model.APIKey, error) {
	k := &model.APIKey{}
	err := s.pool.QueryRow(ctx, `
		SELECT id, user_id, name, key_prefix, key_hash, key_suffix, active, expires_at, model_allow, created_at, last_used_at
		FROM api_keys WHERE key_hash=$1`, hash,
	).Scan(&k.ID, &k.UserID, &k.Name, &k.KeyPrefix, &k.KeyHash, &k.KeySuffix, &k.Active, &k.ExpiresAt, pq.Array(&k.ModelAllow), &k.CreatedAt, &k.LastUsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return k, err
}

// GetAPIKeyByID fetches a key for administrator updates.
func (s *Store) GetAPIKeyByID(ctx context.Context, id int64) (*model.APIKey, error) {
	k := &model.APIKey{}
	err := s.pool.QueryRow(ctx, `SELECT id, user_id, name, key_prefix, key_hash, key_suffix, active, expires_at, model_allow, created_at, last_used_at FROM api_keys WHERE id=$1`, id).Scan(&k.ID, &k.UserID, &k.Name, &k.KeyPrefix, &k.KeyHash, &k.KeySuffix, &k.Active, &k.ExpiresAt, pq.Array(&k.ModelAllow), &k.CreatedAt, &k.LastUsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return k, err
}

// ListAPIKeysByUser returns all keys for a user.
func (s *Store) ListAPIKeysByUser(ctx context.Context, userID int64) ([]*model.APIKey, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, name, key_prefix, key_hash, key_suffix, active, expires_at, model_allow, created_at, last_used_at
		FROM api_keys WHERE user_id=$1 ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.APIKey
	for rows.Next() {
		k := &model.APIKey{}
		if err := rows.Scan(&k.ID, &k.UserID, &k.Name, &k.KeyPrefix, &k.KeyHash, &k.KeySuffix, &k.Active, &k.ExpiresAt, pq.Array(&k.ModelAllow), &k.CreatedAt, &k.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

var ErrKeyOwnerImmutable = errors.New("API key ownership cannot be changed")

// UpdateAPIKey updates mutable key fields.
func (s *Store) UpdateAPIKey(ctx context.Context, k *model.APIKey) error {
	modelAllow := k.ModelAllow
	if modelAllow == nil {
		modelAllow = []string{}
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE api_keys SET name=$1, active=$2, expires_at=$3, model_allow=$4 WHERE id=$5 AND user_id=$6`,
		k.Name, k.Active, k.ExpiresAt, pq.Array(modelAllow), k.ID, k.UserID)
	if err != nil {
		return err
	}
	n, _ := tag.RowsAffected()
	if n == 0 {
		stored, err := s.GetAPIKeyByID(ctx, k.ID)
		if err != nil {
			return err
		}
		if stored.UserID != k.UserID {
			return ErrKeyOwnerImmutable
		}
	}
	return nil
}

// TouchAPIKey updates last_used_at.
func (s *Store) TouchAPIKey(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE api_keys SET last_used_at=now() WHERE id=$1`, id)
	return err
}

// DeleteAPIKey removes a key.
func (s *Store) DeleteAPIKey(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM api_keys WHERE id=$1`, id)
	return err
}
