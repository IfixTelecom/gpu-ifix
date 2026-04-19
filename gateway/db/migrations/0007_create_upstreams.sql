-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

CREATE TABLE IF NOT EXISTS ai_gateway.upstreams (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name              TEXT NOT NULL UNIQUE,
    role              TEXT NOT NULL,
    tier              INT NOT NULL,
    url_env           TEXT NOT NULL,
    auth_bearer_env   TEXT,
    enabled           BOOLEAN NOT NULL DEFAULT TRUE,
    weight            INT,
    circuit_config    JSONB NOT NULL DEFAULT '{}'::jsonb,
    last_probe_at     TIMESTAMPTZ,
    last_probe_ms     INT,
    last_probe_status TEXT,
    last_probe_error  TEXT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (role IN ('llm', 'stt', 'embed')),
    CHECK (last_probe_status IS NULL OR last_probe_status IN ('ok', 'failed', 'timeout')),
    UNIQUE (role, tier)
);

CREATE INDEX IF NOT EXISTS idx_upstreams_enabled_role_tier
    ON ai_gateway.upstreams (enabled, role, tier);

COMMENT ON TABLE ai_gateway.upstreams IS 'Runtime source-of-truth for multi-upstream dispatcher (Phase 3 D-D2). Hot-reloaded via LISTEN upstreams_changed.';
COMMENT ON COLUMN ai_gateway.upstreams.url_env IS 'Env var NAME whose value is the upstream URL. Gateway resolves os.Getenv(url_env) at load.';
COMMENT ON COLUMN ai_gateway.upstreams.auth_bearer_env IS 'Env var NAME whose value is the Bearer token for upstream Auth header. NULL = no auth injected.';
COMMENT ON COLUMN ai_gateway.upstreams.weight IS 'Phase 5 load-shedding weight. NULL in Phase 3.';
COMMENT ON COLUMN ai_gateway.upstreams.circuit_config IS 'JSONB {failures:int, cooldown_s:int} overriding defaults. Empty = use BREAKER_* env defaults.';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS ai_gateway.upstreams CASCADE;
-- +goose StatementEnd
