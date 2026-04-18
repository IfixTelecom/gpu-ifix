-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- audit_log_content: NO FOREIGN KEY to audit_log(request_id).
-- Rationale (Codex review [LOW] 02-02 — document the intentional tradeoff):
--   1. LGPD schema-level isolation: content rows live ONLY for data_class='normal'
--      tenants (writer-enforced in 02-05). An FK would force a content row to exist
--      for every audit_log row, or fail inserts for sensitive tenants.
--   2. Partition independence: audit_log_content partitions can be dropped by the
--      retention job BEFORE audit_log partitions (or vice-versa) without cascade
--      penalty.
--   3. Content rows lag audit_log by the async-flush interval (D-B4); an FK would
--      require inserting audit_log FIRST then content, serializing the flusher.
-- The writer (gateway/internal/audit/writer.go in 02-05) is the ONLY control that
-- enforces orphan-free semantics — every content insert is preceded by an audit_log
-- insert in the same flush batch.
CREATE TABLE IF NOT EXISTS ai_gateway.audit_log_content (
    request_id UUID NOT NULL,
    ts         TIMESTAMPTZ NOT NULL,
    prompt     JSONB,
    response   JSONB,
    PRIMARY KEY (request_id, ts)
) PARTITION BY RANGE (ts);

CREATE INDEX IF NOT EXISTS idx_audit_log_content_ts ON ai_gateway.audit_log_content(ts);

-- Seed 3 monthly partitions mirroring audit_log.
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
        part_name := 'audit_log_content_' || to_char(start_m, 'YYYYMM');
        EXECUTE format(
            'CREATE TABLE IF NOT EXISTS ai_gateway.%I PARTITION OF ai_gateway.audit_log_content FOR VALUES FROM (%L) TO (%L)',
            part_name, start_m, end_m
        );
    END LOOP;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS ai_gateway.audit_log_content CASCADE;
-- +goose StatementEnd
