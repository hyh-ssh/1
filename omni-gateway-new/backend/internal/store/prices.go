package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/zjvtit/omni-gateway/internal/model"
)

var ErrPriceConflict = errors.New("price version already exists")

// CreatePriceVersion inserts a new price version.
func (s *Store) CreatePriceVersion(ctx context.Context, p *model.PriceVersion) (int64, error) {
	cost, _ := json.Marshal(p.Cost)
	credits, _ := json.Marshal(p.Credits)
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO price_versions(model_id, version, cost, credits)
		VALUES($1,$2,$3,$4) RETURNING id`,
		p.ModelID, p.Version, cost, credits).Scan(&id)
	if err != nil {
		var state interface{ SQLState() string }
		if errors.As(err, &state) && state.SQLState() == "23505" {
			return 0, ErrPriceConflict
		}
	}
	return id, err
}

// GetLatestPriceVersion returns the highest version for a model.
func (s *Store) GetLatestPriceVersion(ctx context.Context, modelID string) (*model.PriceVersion, error) {
	p := &model.PriceVersion{}
	var cost, credits []byte
	err := s.pool.QueryRow(ctx, `
		SELECT id, model_id, version, cost, credits, created_at
		FROM price_versions WHERE model_id=$1 ORDER BY version DESC LIMIT 1`, modelID,
	).Scan(&p.ID, &p.ModelID, &p.Version, &cost, &credits, &p.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(cost, &p.Cost); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(credits, &p.Credits); err != nil {
		return nil, err
	}
	return p, nil
}

// ListPriceVersions returns all versions for a model.
func (s *Store) ListPriceVersions(ctx context.Context, modelID string) ([]*model.PriceVersion, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, model_id, version, cost, credits, created_at
		FROM price_versions WHERE model_id=$1 ORDER BY version DESC`, modelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.PriceVersion
	for rows.Next() {
		p := &model.PriceVersion{}
		var cost, credits []byte
		if err := rows.Scan(&p.ID, &p.ModelID, &p.Version, &cost, &credits, &p.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(cost, &p.Cost); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(credits, &p.Credits); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListAllPriceVersions returns every immutable price snapshot.
func (s *Store) ListAllPriceVersions(ctx context.Context) ([]*model.PriceVersion, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, model_id, version, cost, credits, created_at FROM price_versions ORDER BY model_id, version DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.PriceVersion
	for rows.Next() {
		price := &model.PriceVersion{}
		var cost, credits []byte
		if err := rows.Scan(&price.ID, &price.ModelID, &price.Version, &cost, &credits, &price.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(cost, &price.Cost); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(credits, &price.Credits); err != nil {
			return nil, err
		}
		out = append(out, price)
	}
	return out, rows.Err()
}
