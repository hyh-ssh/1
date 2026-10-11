-- Groups manage access to public aliases, independently of upstream providers.
CREATE TABLE IF NOT EXISTS model_groups (
    id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL UNIQUE CHECK (length(btrim(name)) BETWEEN 1 AND 200),
    active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS model_group_aliases (
    model_group_id BIGINT NOT NULL REFERENCES model_groups(id) ON DELETE CASCADE,
    alias_id BIGINT NOT NULL REFERENCES model_aliases(id) ON DELETE CASCADE,
    PRIMARY KEY (model_group_id, alias_id)
);
CREATE INDEX IF NOT EXISTS idx_model_group_aliases_alias ON model_group_aliases(alias_id);

CREATE TABLE IF NOT EXISTS user_groups (
    id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL UNIQUE CHECK (length(btrim(name)) BETWEEN 1 AND 200),
    active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS user_group_members (
    user_group_id BIGINT NOT NULL REFERENCES user_groups(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (user_group_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_user_group_members_user ON user_group_members(user_id);

CREATE TABLE IF NOT EXISTS user_group_model_groups (
    user_group_id BIGINT NOT NULL REFERENCES user_groups(id) ON DELETE CASCADE,
    model_group_id BIGINT NOT NULL REFERENCES model_groups(id) ON DELETE CASCADE,
    PRIMARY KEY (user_group_id, model_group_id)
);
CREATE INDEX IF NOT EXISTS idx_user_group_model_groups_model ON user_group_model_groups(model_group_id);
