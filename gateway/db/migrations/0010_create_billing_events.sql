-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

CREATE TABLE IF NOT EXISTS ai_gateway.billing_events (
    request_id              UUID         NOT NULL,
    ts                      TIMESTAMPTZ  NOT NULL,
    tenant_id               UUID         NOT NULL REFERENCES ai_gateway.tenants(id),
    api_key_id              UUID,
    route                   TEXT         NOT NULL,
    upstream                TEXT         NOT NULL,
    model                   TEXT         NOT NULL,
    tokens_in               INTEGER      NOT NULL DEFAULT 0,
    tokens_out              INTEGER      NOT NULL DEFAULT 0,
    audio_seconds           REAL         NOT NULL DEFAULT 0,
    embeds_count            INTEGER      NOT NULL DEFAULT 0,
    cost_local_brl          NUMERIC(10,6) NOT NULL DEFAULT 0,
    cost_local_phantom_brl  NUMERIC(10,6) NOT NULL DEFAULT 0,
    cost_external_brl       NUMERIC(10,6) NOT NULL DEFAULT 0,
    currency                TEXT         NOT NULL DEFAULT 'BRL',
    source                  TEXT         NOT NULL,  -- 'final' | 'partial'
    created_at              TIMESTAMPTZ  NOT NULL DEFAULT now(),
    PRIMARY KEY (request_id, ts)
) PARTITION BY RANGE (ts);

COMMENT ON TABLE ai_gateway.billing_events IS
    'Append-only billing events; one row per completed request. PK includes ts (partition key). Idempotent INSERT via ON CONFLICT (request_id, ts) DO NOTHING.';

CREATE INDEX IF NOT EXISTS idx_billing_events_tenant_ts
    ON ai_gateway.billing_events (tenant_id, ts DESC);

-- Seed monthly partitions for current month + next 2 months (3 total).
DO $$
DECLARE
    start_m DATE;
    end_m   DATE;
    part    TEXT;
BEGIN
    FOR i IN 0..2 LOOP
        start_m := DATE_TRUNC('month', CURRENT_DATE) + (i || ' months')::INTERVAL;
        end_m   := start_m + INTERVAL '1 month';
        part    := 'billing_events_' || to_char(start_m, 'YYYYMM');
        EXECUTE format(
            'CREATE TABLE IF NOT EXISTS ai_gateway.%I PARTITION OF ai_gateway.billing_events FOR VALUES FROM (%L) TO (%L)',
            part, start_m, end_m
        );
    END LOOP;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS ai_gateway.billing_events CASCADE;
-- +goose StatementEnd
