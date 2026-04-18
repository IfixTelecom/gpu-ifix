-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

CREATE TABLE IF NOT EXISTS ai_gateway.model_aliases (
    alias       TEXT PRIMARY KEY,
    upstream    TEXT NOT NULL CHECK (upstream IN ('llm', 'stt', 'embed')),
    target      TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Seed the three defaults documented in CONTEXT.md (model resolver in
-- Plan 02-05 reads this table at boot and hot-reloads on LISTEN/NOTIFY
-- in Phase 5; Phase 2 read-at-boot is sufficient).
INSERT INTO ai_gateway.model_aliases (alias, upstream, target) VALUES
    ('qwen',    'llm',   'qwen'),
    ('whisper', 'stt',   'Systran/faster-whisper-large-v3'),
    ('bge-m3',  'embed', 'BAAI/bge-m3')
ON CONFLICT (alias) DO NOTHING;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS ai_gateway.model_aliases;
-- +goose StatementEnd
