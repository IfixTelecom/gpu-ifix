-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

INSERT INTO ai_gateway.upstreams (name, role, tier, url_env, auth_bearer_env) VALUES
    ('local-llm',       'llm',   0, 'UPSTREAM_LLM_URL',            NULL),
    ('openrouter-chat', 'llm',   1, 'UPSTREAM_LLM_OPENROUTER_URL', 'UPSTREAM_LLM_OPENROUTER_AUTH_BEARER'),
    ('local-stt',       'stt',   0, 'UPSTREAM_STT_URL',            NULL),
    ('openai-whisper',  'stt',   1, 'UPSTREAM_STT_OPENAI_URL',     'UPSTREAM_STT_OPENAI_AUTH_BEARER'),
    ('local-embed',     'embed', 0, 'UPSTREAM_EMBED_URL',          NULL),
    ('openai-embed',    'embed', 1, 'UPSTREAM_EMBED_OPENAI_URL',   'UPSTREAM_EMBED_OPENAI_AUTH_BEARER')
ON CONFLICT (name) DO NOTHING;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM ai_gateway.upstreams
    WHERE name IN ('local-llm','openrouter-chat','local-stt','openai-whisper','local-embed','openai-embed');
-- +goose StatementEnd
