-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- Phase 06.7 — relax the upstreams role CHECK to admit 'tts' and seed the two
-- tts upstream rows the loader resolves (Resolve('tts', tier)).
-- Source: 06.7-RESEARCH.md §Code Examples + 06.7-PATTERNS.md 0024 section + D-09..D-12.
-- Migration number = 0024 (computed at execution time per reviews consensus action #10:
--   LAST_NUM=$(ls gateway/db/migrations/ | sort -V | tail -1 | grep -oE '^[0-9]+'); NEXT_NUM=$(printf "%04d" $((10#$LAST_NUM + 1)))
-- Latest migration in tree at exec time: 0023_primary_lifecycles.sql).
--
-- Engine note (06.7-WAVE0-GATES GATE 1): the GPU TTS engine is Chatterbox
-- Multilingual (Kani reverted). This migration is engine-agnostic — it only
-- plumbs the 'tts' role + the two upstream rows; the tier-0 row is the dynamic
-- override slot the primary pod writes, tier-1 is the static voice-api Piper.
--
-- Idempotency: DROP CONSTRAINT IF EXISTS + ON CONFLICT (name) DO NOTHING make a
-- re-run safe (T-06.7-07). tts:0 and tts:1 do not violate UNIQUE(role, tier)
-- (0007:23) because no other row uses ('tts', *).
ALTER TABLE ai_gateway.upstreams DROP CONSTRAINT IF EXISTS upstreams_role_check;
ALTER TABLE ai_gateway.upstreams ADD CONSTRAINT upstreams_role_check
    CHECK (role IN ('llm','stt','embed','tts'));

INSERT INTO ai_gateway.upstreams (name, role, tier, url_env, auth_bearer_env) VALUES
    ('local-tts',       'tts', 0, 'UPSTREAM_TTS_URL',       NULL),
    ('voice-api-piper', 'tts', 1, 'UPSTREAM_TTS_PIPER_URL', NULL)
ON CONFLICT (name) DO NOTHING;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- Remove the two seeded tts rows, then re-narrow the CHECK back to the original
-- three roles.
--
-- WARNING (operator): re-narrowing the CHECK to ('llm','stt','embed') will FAIL
-- if ANY tts row remains beyond the two seeded rows below. We delete the two
-- seeded tts rows first, but an environment that added EXTRA user-created tts
-- rows (e.g. an additional tts tier or a renamed tts upstream) must MANUALLY
-- clean those extra tts rows before running a full down-migration, otherwise the
-- ADD CONSTRAINT statement will error with a constraint-violation on the lingering
-- tts row. Dev-acceptable: the standard seed rows are removed automatically here.
DELETE FROM ai_gateway.upstreams
    WHERE name IN ('local-tts','voice-api-piper');

ALTER TABLE ai_gateway.upstreams DROP CONSTRAINT IF EXISTS upstreams_role_check;
ALTER TABLE ai_gateway.upstreams ADD CONSTRAINT upstreams_role_check
    CHECK (role IN ('llm','stt','embed'));
-- +goose StatementEnd
