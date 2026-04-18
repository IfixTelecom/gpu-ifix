-- +goose Up
-- +goose StatementBegin
CREATE SCHEMA IF NOT EXISTS ai_gateway;
SET search_path = ai_gateway, public;

CREATE TABLE IF NOT EXISTS ai_gateway.tenants (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    slug       TEXT NOT NULL UNIQUE,
    name       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_tenants_slug ON ai_gateway.tenants(slug);

-- Seed initial tenant per CONTEXT.md D-A3 so the bootstrap admin flow
-- (gatewayctl key create --tenant converseai ...) has a target on first boot.
INSERT INTO ai_gateway.tenants (slug, name) VALUES ('converseai', 'ConverseAI')
ON CONFLICT (slug) DO NOTHING;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS ai_gateway.tenants;
-- +goose StatementEnd
