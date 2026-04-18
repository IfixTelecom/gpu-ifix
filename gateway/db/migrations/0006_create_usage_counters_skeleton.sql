-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- Skeleton only. Phase 4 populates rows, adds columns for cost_brl_total,
-- and may re-shape this into billing_events. Shipping the skeleton now
-- guarantees the schema is stable across early Phase-2 integration tests.
CREATE TABLE IF NOT EXISTS ai_gateway.usage_counters (
    tenant_id       UUID NOT NULL REFERENCES ai_gateway.tenants(id) ON DELETE CASCADE,
    date            DATE NOT NULL,
    tokens_in       BIGINT NOT NULL DEFAULT 0,
    tokens_out      BIGINT NOT NULL DEFAULT 0,
    requests_count  BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (tenant_id, date)
);

CREATE INDEX IF NOT EXISTS idx_usage_counters_date ON ai_gateway.usage_counters(date);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS ai_gateway.usage_counters;
-- +goose StatementEnd
