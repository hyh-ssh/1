-- Stable attempt identities also make retries after an uncertain commit safe.
-- Every statement is individually idempotent so the file can be replayed (for
-- example when a migration is re-run against a schema it already touched).
ALTER TABLE attempts ADD COLUMN IF NOT EXISTS settlement_id TEXT;
ALTER TABLE attempts ADD COLUMN IF NOT EXISTS settlement_error TEXT NOT NULL DEFAULT '';
UPDATE attempts SET settlement_id = 'legacy-attempt-' || id::text WHERE settlement_id IS NULL;
ALTER TABLE attempts ALTER COLUMN settlement_id SET NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_attempts_settlement ON attempts(settlement_id);

-- Historical rows lack a reliable attempt link; keep them explicitly distinct.
ALTER TABLE usage_ledger ADD COLUMN IF NOT EXISTS settlement_id TEXT;
UPDATE usage_ledger SET settlement_id = 'legacy-ledger-' || id::text WHERE settlement_id IS NULL;
ALTER TABLE usage_ledger ALTER COLUMN settlement_id SET NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_ledger_settlement_meter ON usage_ledger(settlement_id, meter);
CREATE INDEX IF NOT EXISTS idx_attempts_provider_time ON attempts(provider_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_attempts_model_time ON attempts(provider_model, created_at DESC);
