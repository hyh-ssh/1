-- 0001_init.sql — initial schema for the LLM gateway.

-- Users (admins and business users)
CREATE TABLE IF NOT EXISTS users (
    id            BIGSERIAL PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE,
    display_name  TEXT NOT NULL DEFAULT '',
    role          TEXT NOT NULL DEFAULT 'user',
    password_hash TEXT NOT NULL,
    active        BOOLEAN NOT NULL DEFAULT TRUE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- API keys (gateway credentials)
CREATE TABLE IF NOT EXISTS api_keys (
    id          BIGSERIAL PRIMARY KEY,
    user_id     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    key_prefix  TEXT NOT NULL,
    key_hash    TEXT NOT NULL UNIQUE,
    key_suffix  TEXT NOT NULL,
    active      BOOLEAN NOT NULL DEFAULT TRUE,
    expires_at  TIMESTAMPTZ,
    model_allow TEXT[] NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_api_keys_user ON api_keys(user_id);
CREATE INDEX IF NOT EXISTS idx_api_keys_hash ON api_keys(key_hash);

-- Providers (classified by interface type)
CREATE TABLE IF NOT EXISTS providers (
    id            BIGSERIAL PRIMARY KEY,
    name          TEXT NOT NULL,
    type          TEXT NOT NULL,
    base_url      TEXT NOT NULL,
    api_key_enc   TEXT NOT NULL DEFAULT '',
    extra_headers JSONB NOT NULL DEFAULT '{}',
    timeout_ms    INTEGER NOT NULL DEFAULT 30000,
    active        BOOLEAN NOT NULL DEFAULT TRUE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Provider models (upstream models)
CREATE TABLE IF NOT EXISTS provider_models (
    id           BIGSERIAL PRIMARY KEY,
    provider_id  BIGINT NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
    model_id     TEXT NOT NULL,
    capabilities JSONB NOT NULL DEFAULT '{}',
    active       BOOLEAN NOT NULL DEFAULT TRUE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (provider_id, model_id)
);
CREATE INDEX IF NOT EXISTS idx_provider_models_provider ON provider_models(provider_id);

-- Model aliases (names exposed to callers)
CREATE TABLE IF NOT EXISTS model_aliases (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    active     BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Route entries (ordered upstream targets per alias)
CREATE TABLE IF NOT EXISTS route_entries (
    id             BIGSERIAL PRIMARY KEY,
    alias_id       BIGINT NOT NULL REFERENCES model_aliases(id) ON DELETE CASCADE,
    provider_id    BIGINT NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
    provider_model TEXT NOT NULL,
    priority       INTEGER NOT NULL DEFAULT 0,
    active         BOOLEAN NOT NULL DEFAULT TRUE
);
CREATE INDEX IF NOT EXISTS idx_route_entries_alias ON route_entries(alias_id, priority);

-- Price versions (immutable snapshots)
CREATE TABLE IF NOT EXISTS price_versions (
    id         BIGSERIAL PRIMARY KEY,
    model_id   TEXT NOT NULL,
    version    INTEGER NOT NULL,
    cost       JSONB NOT NULL DEFAULT '{}',
    sale       JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (model_id, version)
);
CREATE INDEX IF NOT EXISTS idx_price_versions_model ON price_versions(model_id, version DESC);

-- Budget rules (rolling windows per user)
CREATE TABLE IF NOT EXISTS budget_rules (
    id          BIGSERIAL PRIMARY KEY,
    user_id     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    window_secs BIGINT NOT NULL,
    cost_limit  NUMERIC,
    sale_limit  NUMERIC,
    active      BOOLEAN NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_budget_rules_user ON budget_rules(user_id);

-- Rate limits
CREATE TABLE IF NOT EXISTS rate_limits (
    id          BIGSERIAL PRIMARY KEY,
    scope       TEXT NOT NULL,
    scope_id    BIGINT NOT NULL,
    model_id    TEXT NOT NULL DEFAULT '',
    rpm         BIGINT NOT NULL DEFAULT 0,
    tpm         BIGINT NOT NULL DEFAULT 0,
    concurrency BIGINT NOT NULL DEFAULT 0,
    active      BOOLEAN NOT NULL DEFAULT TRUE
);
CREATE INDEX IF NOT EXISTS idx_rate_limits_scope ON rate_limits(scope, scope_id);

-- Requests (top-level gateway calls)
CREATE TABLE IF NOT EXISTS requests (
    id                  TEXT PRIMARY KEY,
    user_id             BIGINT NOT NULL,
    api_key_id          BIGINT NOT NULL,
    api                 TEXT NOT NULL,
    streaming           BOOLEAN NOT NULL DEFAULT FALSE,
    model_alias         TEXT NOT NULL,
    status              TEXT NOT NULL,
    status_code         INTEGER NOT NULL DEFAULT 0,
    error_type          TEXT NOT NULL DEFAULT '',
    error_summary       TEXT NOT NULL DEFAULT '',
    usage               JSONB NOT NULL DEFAULT '{}',
    cost                NUMERIC NOT NULL DEFAULT 0,
    sale                NUMERIC NOT NULL DEFAULT 0,
    total_latency_ms    BIGINT NOT NULL DEFAULT 0,
    upstream_latency_ms BIGINT NOT NULL DEFAULT 0,
    ttft_ms             BIGINT NOT NULL DEFAULT 0,
    output_tokens_per_sec DOUBLE PRECISION NOT NULL DEFAULT 0,
    client_ip           TEXT NOT NULL DEFAULT '',
    user_agent          TEXT NOT NULL DEFAULT '',
    tags                JSONB NOT NULL DEFAULT '{}',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_requests_user_time ON requests(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_requests_alias_time ON requests(model_alias, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_requests_created ON requests(created_at DESC);

-- Attempts (one upstream call per request, for failover)
CREATE TABLE IF NOT EXISTS attempts (
    id             BIGSERIAL PRIMARY KEY,
    request_id     TEXT NOT NULL REFERENCES requests(id) ON DELETE CASCADE,
    provider_id    BIGINT NOT NULL,
    provider_model TEXT NOT NULL,
    priority       INTEGER NOT NULL DEFAULT 0,
    status         TEXT NOT NULL,
    status_code    INTEGER NOT NULL DEFAULT 0,
    error_type     TEXT NOT NULL DEFAULT '',
    error_summary  TEXT NOT NULL DEFAULT '',
    usage          JSONB NOT NULL DEFAULT '{}',
    cost           NUMERIC NOT NULL DEFAULT 0,
    latency_ms     BIGINT NOT NULL DEFAULT 0,
    ttft_ms        BIGINT NOT NULL DEFAULT 0,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_attempts_request ON attempts(request_id);

-- Usage ledger (immutable)
CREATE TABLE IF NOT EXISTS usage_ledger (
    id             BIGSERIAL PRIMARY KEY,
    request_id     TEXT NOT NULL,
    user_id        BIGINT NOT NULL,
    api_key_id     BIGINT NOT NULL,
    model_alias    TEXT NOT NULL,
    provider_id    BIGINT NOT NULL,
    provider_model TEXT NOT NULL,
    meter          TEXT NOT NULL,
    quantity       BIGINT NOT NULL,
    cost           NUMERIC NOT NULL DEFAULT 0,
    sale           NUMERIC NOT NULL DEFAULT 0,
    price_version  BIGINT NOT NULL DEFAULT 0,
    estimated      BOOLEAN NOT NULL DEFAULT FALSE,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_ledger_user_time ON usage_ledger(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_ledger_request ON usage_ledger(request_id);

-- Audit log
CREATE TABLE IF NOT EXISTS audit_log (
    id          BIGSERIAL PRIMARY KEY,
    actor_id    BIGINT NOT NULL DEFAULT 0,
    actor_name  TEXT NOT NULL DEFAULT '',
    action      TEXT NOT NULL,
    resource    TEXT NOT NULL,
    resource_id TEXT NOT NULL DEFAULT '',
    detail      TEXT NOT NULL DEFAULT '',
    ip          TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_log(created_at DESC);

-- Content log (optional, encrypted bodies)
CREATE TABLE IF NOT EXISTS content_log (
    request_id  TEXT PRIMARY KEY,
    prompt      BYTEA,
    response    BYTEA,
    truncated   BOOLEAN NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Hourly / daily aggregates
CREATE TABLE IF NOT EXISTS aggregates (
    id          BIGSERIAL PRIMARY KEY,
    bucket      TEXT NOT NULL, -- 'hour' | 'day'
    period      TIMESTAMPTZ NOT NULL,
    user_id     BIGINT NOT NULL,
    api_key_id  BIGINT NOT NULL,
    model_alias TEXT NOT NULL,
    provider_id BIGINT NOT NULL,
    provider_model TEXT NOT NULL,
    api         TEXT NOT NULL,
    status      TEXT NOT NULL,
    requests    BIGINT NOT NULL DEFAULT 0,
    success     BIGINT NOT NULL DEFAULT 0,
    input_tokens  BIGINT NOT NULL DEFAULT 0,
    output_tokens BIGINT NOT NULL DEFAULT 0,
    total_tokens  BIGINT NOT NULL DEFAULT 0,
    cost        NUMERIC NOT NULL DEFAULT 0,
    sale        NUMERIC NOT NULL DEFAULT 0,
    latency_sum BIGINT NOT NULL DEFAULT 0,
    ttft_sum    BIGINT NOT NULL DEFAULT 0,
    UNIQUE (bucket, period, user_id, api_key_id, model_alias, provider_id, provider_model, api, status)
);
CREATE INDEX IF NOT EXISTS idx_aggregates_period ON aggregates(bucket, period);

-- Sessions (server-side auth sessions)
CREATE TABLE IF NOT EXISTS sessions (
    id         TEXT PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    csrf_token TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);
