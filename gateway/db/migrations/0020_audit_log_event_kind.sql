-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- Phase 7 — add a nullable event_kind discriminator to audit_log so the
-- observability dashboard can list FSM/state-change audit rows separately
-- from ordinary request rows (consumed by ListAuditStateChanges + the
-- admin handler in plan 07-03).
--
-- Additive + idempotent: ADD COLUMN IF NOT EXISTS leaves every existing row
-- untouched (event_kind = NULL) and is safe to re-run across restarts under
-- the AI_GATEWAY_MIGRATE_ON_BOOT flag (goose idempotency, 02-08 decision).
--
-- Pitfall 8 (partition window): migration 0003 seeded audit_log RANGE
-- partitions for the current month + the next 2 (May/Jun/Jul 2026 given the
-- 2026-05 ship horizon), which fully covers the Phase 7 test/ship window.
-- The partition-roll automation is deferred (originally Plan 02-09) and
-- remains a tracked limitation in STATE.md Open Todos — it is NOT a Phase 7
-- blocker. This ALTER touches the partitioned parent only; PostgreSQL
-- propagates the column to every existing and future partition.
ALTER TABLE ai_gateway.audit_log
    ADD COLUMN IF NOT EXISTS event_kind TEXT;

COMMENT ON COLUMN ai_gateway.audit_log.event_kind IS
    'Phase 7: optional discriminator for non-request audit rows (e.g. breaker/shed/emergency FSM state changes). NULL for ordinary request rows.';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SET search_path = ai_gateway, public;

ALTER TABLE ai_gateway.audit_log
    DROP COLUMN IF EXISTS event_kind;
-- +goose StatementEnd
