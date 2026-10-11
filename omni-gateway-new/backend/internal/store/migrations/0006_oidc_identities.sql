-- 0006_oidc_identities.sql — stable OIDC subject-to-user bindings.

CREATE TABLE IF NOT EXISTS oidc_identities (
    issuer        TEXT NOT NULL,
    subject       TEXT NOT NULL,
    user_id       BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_login_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (issuer, subject)
);

CREATE INDEX IF NOT EXISTS idx_oidc_identities_user ON oidc_identities(user_id);

-- Accounts provisioned by OIDC sign in through the provider. The column
-- defaults to TRUE so every pre-existing row and every account created by the
-- admin API keeps its password login; the OIDC insert path sets it to FALSE
-- explicitly. IF NOT EXISTS keeps this file replayable.
ALTER TABLE users ADD COLUMN IF NOT EXISTS password_login_enabled BOOLEAN NOT NULL DEFAULT TRUE;
