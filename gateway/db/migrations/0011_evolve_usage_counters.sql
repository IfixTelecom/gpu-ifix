-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

ALTER TABLE ai_gateway.usage_counters
    ADD COLUMN IF NOT EXISTS audio_seconds          BIGINT        NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS embeds_count           BIGINT        NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS cost_local_phantom_brl NUMERIC(10,4) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS cost_external_brl      NUMERIC(10,4) NOT NULL DEFAULT 0;

COMMENT ON COLUMN ai_gateway.usage_counters.audio_seconds IS
    'Cumulative audio seconds processed per (tenant, date). UPSERT delta in billing flush CTE.';
COMMENT ON COLUMN ai_gateway.usage_counters.cost_local_phantom_brl IS
    'Notional OpenRouter-equivalent BRL cost; populated even when upstream was tier-0 local GPU.';
COMMENT ON COLUMN ai_gateway.usage_counters.cost_external_brl IS
    'Real BRL cost when upstream was tier-1+. 0 when upstream was tier-0 local.';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE ai_gateway.usage_counters
    DROP COLUMN IF EXISTS audio_seconds,
    DROP COLUMN IF EXISTS embeds_count,
    DROP COLUMN IF EXISTS cost_local_phantom_brl,
    DROP COLUMN IF EXISTS cost_external_brl;
-- +goose StatementEnd
