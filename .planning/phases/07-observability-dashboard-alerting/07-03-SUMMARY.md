---
phase: 07-observability-dashboard-alerting
plan: 03
subsystem: gateway-admin-api
tags: [go, sqlc, postgres, prometheus, admin-api, observability, percentile]

# Dependency graph
requires:
  - phase: 07-observability-dashboard-alerting
    plan: 01
    provides: ListAuditStateChanges :many sqlc query + migration 0020 audit_log.event_kind; obs latency histograms
  - phase: 06-auto-provisioning-emergency-pod-vast-ai
    provides: emerg.FSM 7-state machine + FSM.State() lockless atomic read
  - phase: 04-tenant-auth-quotas
    provides: admin sub-router + X-Admin-Key bcrypt middleware; UsageHandler handler shape
provides:
  - TenantLatencyPercentiles :many sqlc query — per-tenant/route P50/P95/P99 + error_rate via percentile_cont
  - GET /admin/metrics JSON handler (MetricsHandler) — OBS-01 aggregated dashboard data source
  - GET /admin/audit paginated JSON handler (AuditHandler) — OBS-07 audit_log state-change feed
affects: [07-06]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Admin read-API handler shape (3rd instance): query-interface isolation + dual constructor (public *gen.Queries / private test ctor) + OpenAI error envelope on bad input + GatewayAdminRequests increment on every branch — cloned from UsageHandler"
    - "Native Postgres percentiles for the dashboard: percentile_cont over audit_log.latency_ms grouped by tenant_id/route — true P50/P95/P99 with zero Prometheus-cardinality cost (07-RESEARCH Pitfall 1)"
    - "In-process gauge read: GaugeVec.Collect into a buffered prometheus.Metric channel, Write each into dto.Metric, read value + label — bounded, lockless, no goroutine"

key-files:
  created:
    - gateway/internal/admin/metrics.go
    - gateway/internal/admin/metrics_test.go
    - gateway/internal/admin/audit.go
    - gateway/internal/admin/audit_test.go
  modified:
    - gateway/db/queries/audit.sql
    - gateway/internal/db/gen/audit.sql.go
    - gateway/internal/db/gen/querier.go

key-decisions:
  - "error_rate gets an explicit ::float8 cast in the SQL — without it sqlc inferred int32 for `count(*) FILTER(...)::float / NULLIF(count(*),0)`, which would truncate the rate to 0 or 1 (Rule 1 bug, caught before commit)"
  - "?limit above 200 is clamped, not rejected — a large page request is not hostile, just bounded (threat T-07-08); negative/non-numeric limit & offset ARE rejected with a 400"
  - "discardLog() is redefined inside package admin (metrics_test.go) — the existing helper in middleware_test.go is package admin_test and not visible to the internal test files that need newMetricsHandlerWithQueries/newAuditHandlerWithQueries"
  - "nullable Postgres text columns (upstream, error_code, event_kind) render as JSON null via *string + a pgTextPtr helper, not as empty string"

requirements-completed: [OBS-01, OBS-07]

# Metrics
duration: ~12min
completed: 2026-05-14
---

# Phase 7 Plan 03: Admin Read API — /admin/metrics + /admin/audit Summary

**Two new admin HTTP handlers the observability dashboard polls: `GET /admin/metrics` (OBS-01 — per-tenant P50/P95/P99 + error rate from a native Postgres `percentile_cont` query, plus FSM state and per-upstream inflight) and `GET /admin/audit` (OBS-07 — paginated newest-first `audit_log` state-change feed), both cloning the `UsageHandler` contract exactly.**

## Performance

- **Duration:** ~12 min
- **Tasks:** 3
- **Files modified:** 7 (4 created, 3 modified)

## Accomplishments

- **`TenantLatencyPercentiles` query** — added `-- name: TenantLatencyPercentiles :many` to `db/queries/audit.sql`: `percentile_cont(0.50/0.95/0.99) WITHIN GROUP (ORDER BY latency_ms)` plus `count(*)` and a `status_code >= 500` error-rate ratio, grouped by `tenant_id, route`, scanning a `ts >= $1` window. `sqlc generate` produced the `TenantLatencyPercentiles` method + `TenantLatencyPercentilesRow{ TenantID, Route, P50, P95, P99 float64, Requests int64, ErrorRate float64 }`. This computes true per-tenant percentiles in Postgres — zero Prometheus-cardinality cost (07-RESEARCH Pitfall 1).
- **`GET /admin/metrics` handler** — `gateway/internal/admin/metrics.go` `MetricsHandler` clones the `UsageHandler` shape: a `metricsQueries` interface listing only `TenantLatencyPercentiles`, a struct holding `q metricsQueries` + `fsm *emerg.FSM` + `log`, the `NewMetricsHandler` / `newMetricsHandlerWithQueries` dual constructor. `ServeHTTP` validates an optional `?window` Go-duration param (default 5m, capped 24h) — bad input returns `httpx.WriteOpenAIError` 400 `invalid_query_param` + a `/admin/metrics 4xx` increment before the query runs. Success path emits a typed `MetricsResponse` with the per-tenant percentile rows, `fsm_state` from `h.fsm.State().String()`, and `inflight` read from the `GatewayInflight` gauge, then a `/admin/metrics 2xx` increment.
- **`GET /admin/audit` handler** — `gateway/internal/admin/audit.go` `AuditHandler` clones the same shape: an `auditQueries` interface (only `ListAuditStateChanges`), the dual constructor. `ServeHTTP` parses `?limit` (default 50, clamped at 200) and `?offset` (default 0, rejects negative) — bad input → 400 envelope + `/admin/audit 4xx`. Success emits `{ items, limit, offset }` preserving the query's `ORDER BY ts DESC` (the handler does not re-sort); nullable Postgres text columns render as JSON `null` via `*string`.

## Task Commits

Each task was committed atomically:

1. **Task 1: TenantLatencyPercentiles query** — `a344580` (feat)
2. **Task 2: GET /admin/metrics JSON handler** — `18ff5eb` (feat)
3. **Task 3: GET /admin/audit paginated JSON handler** — `e0737bd` (feat)

## Files Created/Modified

- `gateway/db/queries/audit.sql` — `TenantLatencyPercentiles :many` percentile query appended
- `gateway/internal/db/gen/audit.sql.go` — sqlc-generated `TenantLatencyPercentiles` method + `TenantLatencyPercentilesRow`
- `gateway/internal/db/gen/querier.go` — `TenantLatencyPercentiles` added to the `Querier` interface
- `gateway/internal/admin/metrics.go` — `MetricsHandler`, `metricsQueries` interface, dual constructor, `readInflight` gauge reader, `fsmStateString` helper
- `gateway/internal/admin/metrics_test.go` — `fakeMetricsQueries`; tests for 200 JSON shape, 400 envelope on bad `?window`, custom window math; package-internal `discardLog()`
- `gateway/internal/admin/audit.go` — `AuditHandler`, `auditQueries` interface, dual constructor, `pgTextPtr` nullable-column helper
- `gateway/internal/admin/audit_test.go` — `fakeAuditQueries`; tests for 200 paginated newest-first order, `?limit` cap at 200, 400 envelope on bad `limit`/`offset`

## Decisions Made

- **`error_rate` needs an explicit `::float8` cast.** sqlc's first pass inferred `int32` for `count(*) FILTER (WHERE status_code >= 500)::float / NULLIF(count(*),0)` — which would truncate every error rate to 0 or 1. Wrapping the whole expression in `(...)::float8` made sqlc generate `ErrorRate float64`. Caught by reading the generated row type before committing Task 1 (see Deviations).
- **`?limit` above 200 is clamped, not rejected.** A caller asking for a big page is not hostile — it just needs bounding (threat T-07-08, "a hostile caller cannot request an unbounded result set"). Negative or non-numeric `limit`/`offset` *are* rejected with a 400, since those are malformed, not merely large.
- **`discardLog()` redefined inside `package admin`.** `metrics_test.go` and `audit_test.go` are `package admin` (internal) so they can call the private `newMetricsHandlerWithQueries`/`newAuditHandlerWithQueries` test constructors. The existing `discardLog()` in `middleware_test.go` is `package admin_test` — a different test package, no shared symbols — so a package-internal copy is required, defined once in `metrics_test.go` and reused by `audit_test.go`.
- **Nullable columns render as JSON `null`.** `upstream`, `error_code`, `event_kind` are nullable `pgtype.Text`; a `pgTextPtr` helper converts them to `*string` so an unset column serializes as `null`, not `""`.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] `error_rate` truncated to int32 by sqlc type inference**
- **Found during:** Task 1 (TenantLatencyPercentiles query)
- **Issue:** The plan's action specified `count(*) FILTER (WHERE status_code >= 500)::float / NULLIF(count(*),0) AS error_rate`. After the first `sqlc generate`, the generated `TenantLatencyPercentilesRow.ErrorRate` field was `int32` — sqlc did not propagate the `::float` through the division by a `bigint` `NULLIF`. An `int32` error rate would always be 0 (for any rate < 1.0) or 1, silently destroying the OBS-01 per-tenant error-rate signal.
- **Fix:** Wrapped the whole ratio expression in an explicit `(...)::float8` cast. Re-ran `sqlc generate`; `ErrorRate` is now `float64`.
- **Files modified:** `gateway/db/queries/audit.sql`, `gateway/internal/db/gen/audit.sql.go` (regenerated)
- **Verification:** `grep -A9 'type TenantLatencyPercentilesRow' internal/db/gen/audit.sql.go` shows `ErrorRate float64`; `go build ./internal/db/...` exits 0.
- **Committed in:** `a344580` (folded into the Task 1 commit — the cast is part of getting the query right, not a separate fix).

---

**Total deviations:** 1 auto-fixed (1 bug)
**Impact on plan:** None on scope — the fix is a one-token SQL cast that makes the generated type match the plan's stated acceptance criterion ("the generated row type exposes ... `ErrorRate` fields" — implicitly as a usable numeric type).

## Issues Encountered

- `sqlc` is not on `PATH`; installed at `/home/pedro/go/bin/sqlc` (v1.30.0, matching the existing generated headers). Invoked via the absolute path from `gateway/` — `sqlc generate` regenerated cleanly both times.

## Threat Surface

No new threat surface beyond the plan's `<threat_model>`. The four registered threats are handled as designed:
- **T-07-07 (Spoofing):** Both handlers are plain `http.Handler`s with no auth logic of their own — they will mount inside the existing `if px.adminVerifier != nil` block under `admin.Middleware` (wiring is plan 07-06's job, not this plan). No new auth path.
- **T-07-08 (Tampering):** `?window` (metrics) and `?limit`/`?offset` (audit) are validated before use. Bad input returns `httpx.WriteOpenAIError` 400 and never reaches the SQL layer. `?window` is capped at 24h; `?limit` is clamped at 200 — a hostile caller cannot drive an unbounded scan or result set.
- **T-07-09 (Information Disclosure):** `ListAuditStateChanges` (from 07-01) selects only `audit_log` metadata columns — never `audit_log_content`. The `AuditRow` struct mirrors exactly those metadata columns; no prompt/response content is reachable through this handler.
- **T-07-10 (DoS, accepted):** `TenantLatencyPercentiles` scans a bounded `ts >= $1` window (default 5 min, max 24h) backed by `idx_audit_log_tenant_ts`, grouped by the ~6-tenant cardinality. Low-volume internal admin endpoint.

## Known Stubs

None. Both handlers are fully wired to real data sources (`TenantLatencyPercentiles` + `ListAuditStateChanges` sqlc queries, the `emerg.FSM`, the `GatewayInflight` gauge). They are not yet *mounted* on the admin router — that wiring is explicitly plan 07-06's scope per the plan's `<threat_model>` T-07-07 note ("Wiring done in plan 07-06") — but the handlers themselves are complete and tested, not stubs.

## Next Phase Readiness

- **07-06 unblocked.** The two handlers exist with the `UsageHandler`-identical constructor signature (`New*Handler(q *gen.Queries, ... , log *slog.Logger)`), so 07-06's router wiring is a drop-in next to the existing `adminUsageHandler := admin.NewUsageHandler(...)` line in `cmd/gateway/main.go`. `NewMetricsHandler` additionally takes the `*emerg.FSM` already constructed in `main.go`.
- **No blockers.** `go build ./...` exits 0; `go vet ./internal/admin/` clean; `go test ./internal/admin/ ./internal/db/... -count=1 -race` green.

## Self-Check: PASSED

All 4 created files exist on disk; all 3 task commits (`a344580`, `18ff5eb`, `e0737bd`) are reachable in git history. `go build ./...`, `go vet ./internal/admin/`, and `go test ./internal/admin/ ./internal/db/... -race` all pass.

---
*Phase: 07-observability-dashboard-alerting*
*Completed: 2026-05-14*
