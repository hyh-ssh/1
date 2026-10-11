package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/zjvtit/omni-gateway/internal/model"
)

// CreateProvider inserts a provider.
func (s *Store) CreateProvider(ctx context.Context, p *model.Provider) (int64, error) {
	// Older callers construct Provider without a profile; preserve generic behavior.
	if p.CompatibilityProfile == "" {
		p.CompatibilityProfile = model.ProviderCompatibilityGeneric
	}
	eh, _ := json.Marshal(p.ExtraHeader)
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO providers(name, type, compatibility_profile, base_url, api_key_enc, extra_headers, timeout_ms, active, max_concurrency, supports_responses)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id`,
		p.Name, string(p.Type), string(p.CompatibilityProfile), p.BaseURL, p.APIKeyEnc, eh, p.TimeoutMS, p.Active, p.MaxConcurrency, p.SupportsResponses,
	).Scan(&id)
	return id, err
}

// GetProvider fetches a provider by id.
func (s *Store) GetProvider(ctx context.Context, id int64) (*model.Provider, error) {
	p := &model.Provider{}
	var eh []byte
	err := s.pool.QueryRow(ctx, `
		SELECT id, name, type, compatibility_profile, base_url, api_key_enc, extra_headers, timeout_ms, active, health_status, cooldown_until, last_verified_at, created_at, updated_at, max_concurrency, success_count, failure_count, last_error, supports_responses
		FROM providers WHERE id=$1`, id,
	).Scan(&p.ID, &p.Name, &p.Type, &p.CompatibilityProfile, &p.BaseURL, &p.APIKeyEnc, &eh, &p.TimeoutMS, &p.Active, &p.HealthStatus, &p.CooldownUntil, &p.LastVerifiedAt, &p.CreatedAt, &p.UpdatedAt, &p.MaxConcurrency, &p.SuccessCount, &p.FailureCount, &p.LastError, &p.SupportsResponses)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(eh, &p.ExtraHeader); err != nil {
		return nil, err
	}
	return p, nil
}

// ListProviders returns all providers.
func (s *Store) ListProviders(ctx context.Context) ([]*model.Provider, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, type, compatibility_profile, base_url, api_key_enc, extra_headers, timeout_ms, active, health_status, cooldown_until, last_verified_at, created_at, updated_at, max_concurrency, success_count, failure_count, last_error, supports_responses
		FROM providers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.Provider
	for rows.Next() {
		p := &model.Provider{}
		var eh []byte
		if err := rows.Scan(&p.ID, &p.Name, &p.Type, &p.CompatibilityProfile, &p.BaseURL, &p.APIKeyEnc, &eh, &p.TimeoutMS, &p.Active, &p.HealthStatus, &p.CooldownUntil, &p.LastVerifiedAt, &p.CreatedAt, &p.UpdatedAt, &p.MaxConcurrency, &p.SuccessCount, &p.FailureCount, &p.LastError, &p.SupportsResponses); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(eh, &p.ExtraHeader); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdateProvider updates mutable provider fields.
func (s *Store) UpdateProvider(ctx context.Context, p *model.Provider) error {
	if p.CompatibilityProfile == "" {
		p.CompatibilityProfile = model.ProviderCompatibilityGeneric
	}
	eh, _ := json.Marshal(p.ExtraHeader)
	_, err := s.pool.Exec(ctx, `
		UPDATE providers SET name=$1, type=$2, compatibility_profile=$3, base_url=$4, api_key_enc=$5, extra_headers=$6, timeout_ms=$7, active=$8, max_concurrency=$9, supports_responses=$10, updated_at=now()
		WHERE id=$11`,
		p.Name, string(p.Type), string(p.CompatibilityProfile), p.BaseURL, p.APIKeyEnc, eh, p.TimeoutMS, p.Active, p.MaxConcurrency, p.SupportsResponses, p.ID)
	return err
}

// DeleteProvider removes a provider.
func (s *Store) DeleteProvider(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM providers WHERE id=$1`, id)
	return err
}

// UpsertProviderModel inserts or updates a provider model.
func (s *Store) UpsertProviderModel(ctx context.Context, providerID int64, modelID string, caps model.Capabilities) error {
	capsJSON, _ := json.Marshal(caps)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO provider_models(provider_id, model_id, capabilities)
		VALUES($1,$2,$3)
		ON CONFLICT (provider_id, model_id) DO UPDATE SET capabilities=$3`,
		providerID, modelID, capsJSON)
	return err
}

// ListProviderModels returns models for a provider.
func (s *Store) ListProviderModels(ctx context.Context, providerID int64) ([]*model.ProviderModel, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, provider_id, model_id, capabilities, active, created_at
		FROM provider_models WHERE provider_id=$1 ORDER BY model_id`, providerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.ProviderModel
	for rows.Next() {
		m := &model.ProviderModel{}
		var caps []byte
		if err := rows.Scan(&m.ID, &m.ProviderID, &m.ModelID, &caps, &m.Active, &m.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(caps, &m.Capabilities); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// GetProviderModel retrieves the exact upstream model (including its capabilities)
// for a route's provider ID and provider model name.
func (s *Store) GetProviderModel(ctx context.Context, providerID int64, modelID string) (*model.ProviderModel, error) {
	m := &model.ProviderModel{}
	var caps []byte
	err := s.pool.QueryRow(ctx, `
		SELECT id, provider_id, model_id, capabilities, active, created_at
		FROM provider_models WHERE provider_id=$1 AND model_id=$2`, providerID, modelID,
	).Scan(&m.ID, &m.ProviderID, &m.ModelID, &caps, &m.Active, &m.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(caps, &m.Capabilities); err != nil {
		return nil, err
	}
	return m, nil
}

// DiscoverProviderModel records only existence. Listing upstream model IDs
// confirms no feature capabilities, and must not overwrite manual declarations.
func (s *Store) DiscoverProviderModel(ctx context.Context, providerID int64, modelID string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO provider_models(provider_id,model_id,capabilities) VALUES($1,$2,'{}'::jsonb) ON CONFLICT (provider_id,model_id) DO NOTHING`, providerID, modelID)
	return err
}
