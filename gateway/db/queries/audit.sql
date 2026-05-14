-- name: InsertAuditLogContent :exec
-- Only called for data_class = 'normal' rows. Sensitive tenants leave
-- this table untouched (CONTEXT.md D-B2). Batch metadata inserts for
-- audit_log use pgx.CopyFrom directly (see gateway/internal/audit).
INSERT INTO ai_gateway.audit_log_content (request_id, ts, prompt, response)
VALUES ($1, $2, $3, $4)
ON CONFLICT (request_id, ts) DO NOTHING;

-- name: ListAuditStateChanges :many
-- Phase 7 — paginated read for the observability dashboard's state-change
-- feed (consumed by the admin handler in plan 07-03). Returns only rows
-- tagged with a non-NULL event_kind (FSM/state-change audit rows added by
-- migration 0020); ordinary request rows are excluded. ts DESC + LIMIT/OFFSET
-- keeps the page compact; the idx_audit_log_tenant_ts index serves the sort.
SELECT ts, request_id, tenant_id, route, method, upstream, status_code,
       latency_ms, error_code, event_kind
FROM ai_gateway.audit_log
WHERE event_kind IS NOT NULL
ORDER BY ts DESC
LIMIT $1 OFFSET $2;
