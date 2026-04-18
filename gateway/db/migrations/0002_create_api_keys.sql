-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

DO $$ BEGIN
    CREATE TYPE ai_gateway.api_key_status AS ENUM ('active', 'revoked');
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

DO $$ BEGIN
    CREATE TYPE ai_gateway.data_class AS ENUM ('normal', 'sensitive');
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

CREATE TABLE IF NOT EXISTS ai_gateway.api_keys (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        UUID NOT NULL REFERENCES ai_gateway.tenants(id) ON DELETE CASCADE,
    -- key_lookup_hash: SHA-256 of the full raw key (including `ifix_sk_` prefix).
    -- FAST non-cryptographic lookup hash. MUST NOT replace argon2id for credential
    -- verification. Its only job is to narrow the candidate set from O(N_active_keys)
    -- to 0-or-1 row so the argon2id verify on the hot path runs at most once per
    -- request regardless of key count (Codex review [HIGH] 02-03).
    key_lookup_hash  BYTEA NOT NULL,
    key_hash         TEXT NOT NULL,
    key_prefix       TEXT NOT NULL,
    status           ai_gateway.api_key_status NOT NULL DEFAULT 'active',
    data_class       ai_gateway.data_class NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at       TIMESTAMPTZ,
    last_used_at     TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_api_keys_tenant           ON ai_gateway.api_keys(tenant_id);
CREATE INDEX IF NOT EXISTS idx_api_keys_status_tenant    ON ai_gateway.api_keys(status, tenant_id);
CREATE INDEX IF NOT EXISTS idx_api_keys_key_prefix       ON ai_gateway.api_keys(key_prefix);
-- UNIQUE index on key_lookup_hash: a single SELECT ... WHERE key_lookup_hash = $1
-- returns at most one row. argon2id.ComparePasswordAndHash is called on that one
-- row's key_hash — never on a scan. Codex review [HIGH] 02-03.
CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_lookup_hash ON ai_gateway.api_keys(key_lookup_hash);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS ai_gateway.api_keys;
DROP TYPE IF EXISTS ai_gateway.api_key_status;
DROP TYPE IF EXISTS ai_gateway.data_class;
-- +goose StatementEnd
