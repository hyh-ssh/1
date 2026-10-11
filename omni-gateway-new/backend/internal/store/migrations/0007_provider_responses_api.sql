-- 0007_provider_responses_api.sql — providers opt in to the OpenAI Responses API.
--
-- An OpenAI-compatible base URL says nothing about whether /v1/responses exists
-- there: vLLM, for one, serves /v1/chat/completions only. Without this opt-in the
-- gateway refuses POST /v1/responses with 501 responses_unsupported instead of
-- forwarding the call and reporting the upstream's 404 as a generic 502.
-- IF NOT EXISTS keeps the file replayable; migrations only move forward.
ALTER TABLE providers ADD COLUMN IF NOT EXISTS supports_responses BOOLEAN NOT NULL DEFAULT FALSE;
