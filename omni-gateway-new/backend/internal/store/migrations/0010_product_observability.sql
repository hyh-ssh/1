-- Additive observability only. Existing audit summaries and outcomes stay
-- unknown; historical requests, identities, memberships and ledgers untouched.
ALTER TABLE attempts ADD COLUMN IF NOT EXISTS settlement_status TEXT NOT NULL DEFAULT '';
ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS result TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS resource_name TEXT NOT NULL DEFAULT '';
ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS before_summary JSONB;
ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS after_summary JSONB;
CREATE INDEX IF NOT EXISTS idx_audit_actor_time ON audit_log(actor_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_audit_resource_time ON audit_log(resource, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_requests_key_success ON requests(user_id,api_key_id,created_at) WHERE status='success' AND api <> 'models';
-- Only the default for new rows changes, never the state of existing aliases.
ALTER TABLE model_aliases ALTER COLUMN active SET DEFAULT FALSE;
