ALTER TABLE providers ADD COLUMN IF NOT EXISTS health_status TEXT NOT NULL DEFAULT 'available';
ALTER TABLE providers ADD COLUMN IF NOT EXISTS cooldown_until TIMESTAMPTZ;
ALTER TABLE providers ADD COLUMN IF NOT EXISTS last_verified_at TIMESTAMPTZ;
ALTER TABLE providers ADD COLUMN IF NOT EXISTS max_concurrency INTEGER NOT NULL DEFAULT 0 CHECK (max_concurrency >= 0);
ALTER TABLE providers ADD COLUMN IF NOT EXISTS success_count BIGINT NOT NULL DEFAULT 0;
ALTER TABLE providers ADD COLUMN IF NOT EXISTS failure_count BIGINT NOT NULL DEFAULT 0;
ALTER TABLE providers ADD COLUMN IF NOT EXISTS last_error TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_providers_health ON providers(active, health_status, cooldown_until);
