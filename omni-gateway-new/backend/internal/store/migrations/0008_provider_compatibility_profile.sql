-- 0008_provider_compatibility_profile.sql — legacy providers keep generic adapter behavior.
-- IF NOT EXISTS allows a migration file to be replayed without changing existing profiles.
ALTER TABLE providers ADD COLUMN IF NOT EXISTS compatibility_profile TEXT NOT NULL DEFAULT 'generic'
    CHECK (compatibility_profile IN ('generic', 'deepseek')
           AND (compatibility_profile <> 'deepseek' OR type = 'openai_compatible'));
