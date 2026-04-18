-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

CREATE TABLE IF NOT EXISTS ai_gateway.audit_log (
    ts                    TIMESTAMPTZ NOT NULL,
    request_id            UUID NOT NULL,
    tenant_id             UUID NOT NULL,
    api_key_id            UUID,
    data_class            ai_gateway.data_class NOT NULL,
    route                 TEXT NOT NULL,
    method                TEXT NOT NULL,
    upstream              TEXT,
    status_code           SMALLINT NOT NULL,
    latency_ms            INTEGER NOT NULL,
    tokens_in             INTEGER,
    tokens_out            INTEGER,
    cost_brl              NUMERIC(10, 4),
    error_code            TEXT,
    idempotency_replayed  BOOLEAN NOT NULL DEFAULT FALSE,
    stream                BOOLEAN NOT NULL DEFAULT FALSE,
    truncated             BOOLEAN NOT NULL DEFAULT FALSE,
    -- Whisper metadata (D-B6 — audio stays out of content table but
    -- envelope metadata is safe to persist)
    audio_filename        TEXT,
    audio_mime            TEXT,
    audio_size_bytes      BIGINT,
    audio_duration_s      REAL,
    audio_language        TEXT,
    PRIMARY KEY (request_id, ts)
) PARTITION BY RANGE (ts);

CREATE INDEX IF NOT EXISTS idx_audit_log_tenant_ts  ON ai_gateway.audit_log(tenant_id, ts DESC);
CREATE INDEX IF NOT EXISTS idx_audit_log_request_id ON ai_gateway.audit_log(request_id);
CREATE INDEX IF NOT EXISTS idx_audit_log_api_key    ON ai_gateway.audit_log(api_key_id) WHERE api_key_id IS NOT NULL;

-- Seed partitions for the current month and the next 2 months so that
-- the first inserts land somewhere. Plan 02-09's gatewayctl command
-- rolls new partitions forward and drops ones older than 90 days.
DO $$
DECLARE
    m DATE;
    start_m DATE;
    end_m DATE;
    part_name TEXT;
BEGIN
    FOR i IN 0..2 LOOP
        m := DATE_TRUNC('month', CURRENT_DATE) + (i || ' months')::INTERVAL;
        start_m := m;
        end_m := m + INTERVAL '1 month';
        part_name := 'audit_log_' || to_char(start_m, 'YYYYMM');
        EXECUTE format(
            'CREATE TABLE IF NOT EXISTS ai_gateway.%I PARTITION OF ai_gateway.audit_log FOR VALUES FROM (%L) TO (%L)',
            part_name, start_m, end_m
        );
    END LOOP;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS ai_gateway.audit_log CASCADE;
-- +goose StatementEnd
