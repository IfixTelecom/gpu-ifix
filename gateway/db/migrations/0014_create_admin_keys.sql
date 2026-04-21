-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

CREATE TABLE IF NOT EXISTS ai_gateway.admin_keys (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    key_lookup_hash BYTEA NOT NULL,                          -- SHA-256 of raw key (indexed lookup)
    key_hash        TEXT  NOT NULL,                          -- bcrypt hash (cost 10)
    key_prefix      TEXT  NOT NULL,                          -- 'ifix_admin_****abcd' display
    label           TEXT  NOT NULL,
    status          TEXT  NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'revoked')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at      TIMESTAMPTZ,
    last_used_at    TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_admin_keys_lookup_hash
    ON ai_gateway.admin_keys (key_lookup_hash);

CREATE INDEX IF NOT EXISTS idx_admin_keys_status
    ON ai_gateway.admin_keys (status) WHERE status = 'active';

COMMENT ON TABLE ai_gateway.admin_keys IS
    'Admin auth: bcrypt cost 10. SHA-256 lookup hash enables PK-style fetch before bcrypt verify (Phase 2 D-A2 pattern).';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS ai_gateway.admin_keys;
-- +goose StatementEnd
