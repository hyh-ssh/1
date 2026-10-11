-- 0009_oidc_provider.sql — the single primary OIDC provider, configured by an
-- administrator from the console.
--
-- The gateway supports exactly one primary sign-in provider, so the row is a
-- singleton: the CHECK constraint pins id to 1 and every read or write goes
-- through that row.
--
-- Environment variables only ever seed this row once, on the first startup that
-- finds it missing (env_seed_applied records that the seed was considered).
-- Clearing the configuration from the console therefore blanks the fields and
-- keeps the row, so a restart cannot silently seed the environment values back.
--
-- client_secret_ciphertext holds the client secret encrypted with the gateway
-- master key; the plaintext is never stored, returned to a browser, or logged.
-- IF NOT EXISTS keeps the file replayable; migrations only move forward.
CREATE TABLE IF NOT EXISTS oidc_provider (
    id SMALLINT PRIMARY KEY,
    issuer TEXT NOT NULL DEFAULT '',
    client_id TEXT NOT NULL DEFAULT '',
    client_secret_ciphertext TEXT NOT NULL DEFAULT '',
    redirect_url TEXT NOT NULL DEFAULT '',
    display_name TEXT NOT NULL DEFAULT '',
    enabled BOOLEAN NOT NULL DEFAULT FALSE,
    env_seed_applied BOOLEAN NOT NULL DEFAULT FALSE,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by BIGINT REFERENCES users(id) ON DELETE SET NULL,
    CONSTRAINT oidc_provider_singleton CHECK (id = 1)
);
