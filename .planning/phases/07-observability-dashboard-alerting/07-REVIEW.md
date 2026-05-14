---
phase: 07-observability-dashboard-alerting
reviewed: 2026-05-14T00:00:00Z
depth: standard
files_reviewed: 25
files_reviewed_list:
  - dashboard/src/app/(dashboard)/incidents/page.tsx
  - dashboard/src/app/(dashboard)/page.tsx
  - dashboard/src/app/(dashboard)/tenants/page.tsx
  - dashboard/src/components/audit-table.tsx
  - dashboard/src/components/critical-banner.test.tsx
  - dashboard/src/components/latency-chart.tsx
  - dashboard/src/components/stale-indicator.tsx
  - dashboard/src/lib/db.ts
  - dashboard/src/lib/gateway.test.ts
  - dashboard/src/lib/gateway.ts
  - gateway/db/migrations/0021_audit_log_ts_index.sql
  - gateway/db/migrations/0022_audit_log_reason.sql
  - gateway/db/queries/audit.sql
  - gateway/internal/admin/audit.go
  - gateway/internal/admin/metrics.go
  - gateway/internal/alert/alerter.go
  - gateway/internal/alert/alerter_test.go
  - gateway/internal/alert/brevo.go
  - gateway/internal/audit/writer.go
  - gateway/internal/db/gen/audit.sql.go
  - gateway/internal/db/gen/models.go
  - gateway/internal/db/gen/querier.go
  - gateway/internal/db/migrate_test.go
  - gateway/internal/emerg/fsm.go
  - gateway/internal/emerg/fsm_test.go
findings:
  critical: 0
  warning: 2
  info: 2
  total: 4
status: issues_found
---

# Phase 7: Code Review Report (Re-review — iteration 1)

**Reviewed:** 2026-05-14
**Depth:** standard
**Files Reviewed:** 25 (the files touched by fix commits `74b9d3f^..HEAD`)
**Status:** issues_found

## Summary

This is a re-review of the 12 fix commits the gsd-code-fixer applied against the prior 07-REVIEW.md (3 BLOCKER + 9 WARNING). All three BLOCKERs are **resolved**, and the fixes are correct — not papered over:

- **CR-01** (dashboard ↔ gateway contract): the `gateway.ts` TypeScript interfaces now mirror the Go handlers field-for-field — `MetricsResponse` is `{window, fsm_state, tenants[], inflight: InflightRow[]}`, `AuditResponse` is `{items[], limit, offset}`, and `AuditRow` carries `request_id/route/method/status_code/latency_ms/error_code/event_kind/reason`. The dropped aggregates are honestly derived client-side (`latencyByRoute`, `totalInflight`) instead of being faked into the type. Every consumer was updated: `page.tsx` derives `byRoute`/`inflight`, `audit-table.tsx` keys rows on `request_id` and infers `canNext` from page fullness, `incidents/page.tsx` reads `data.items`, `tenant-table.tsx`/`latency-chart.tsx` consume the corrected types. The unit tests now assert against the actual Go-handler JSON shapes (with comments stating so explicitly).
- **CR-02** (SQL index): migration `0021_audit_log_ts_index.sql` adds `idx_audit_log_ts ON ai_gateway.audit_log (ts, tenant_id, route)` — leading `ts` serves the `ts >= $1` range and the trailing columns serve the `GROUP BY`. The misleading comment in `audit.sql` was corrected. `CREATE INDEX ... ON` a partitioned parent does propagate to all partitions, so the migration claim is accurate.
- **CR-03** (FSM audit reason): migration `0022_audit_log_reason.sql` adds a dedicated nullable `reason TEXT` column. The FSM now writes the transition reason into `audit.Event.Reason` (not `ErrorCode`), and `WriteStateChange` mints a fresh `uuid.New()` whenever the caller leaves `RequestID == uuid.Nil` — eliminating the all-zeros-UUID PK collision. sqlc-generated code (`audit.sql.go`, `models.go`, `querier.go`) was regenerated consistently; `migrate_test.go` was updated to expect 0021/0022.

All 9 WARNINGs were addressed and the fixes are correct (see Verification below). No regressions or new bugs were introduced in the migrations, the sqlc-regenerated code, or the alerter concurrency changes.

Two residual issues remain — both pre-existing and surfaced (not caused) by the CR-01 fix making the contract honest — plus two minor info items. None block ship.

## Verification of prior findings

**BLOCKERs — all resolved:**
- CR-01 — RESOLVED. Types + all consumers + tests aligned to the real Go contract.
- CR-02 — RESOLVED. Dedicated `(ts, tenant_id, route)` index added; comment corrected.
- CR-03 — RESOLVED. Dedicated `reason` column; `WriteStateChange` mints non-nil `request_id`.

**WARNINGs — all addressed:**
- WR-01 — RESOLVED. `gateway.test.ts` `fetchUsage` test now documents the verified `usage.go` contract (`tenant`/`from`/`to`, `YYYY-MM-DD`, 400 on missing) and asserts the full `UsageResponse` shape field-for-field.
- WR-02 — RESOLVED. `readInflight` runs `obs.GatewayInflight.Collect(ch)` in its own goroutine and closes the channel from there; the drain `for range` keeps the buffer moving, so a series count above the buffer can never deadlock the request goroutine.
- WR-03 — RESOLVED. `ReconcileBoot` now passes `bypassDedup = emergCriticalStates[stateName]` into a new `handleEvent`; an active critical state re-pages unconditionally, warning states still dedup. `TestAlerter_ReconcileBootRePagesActiveCriticalDespiteDedupKey` proves it.
- WR-04 — RESOLVED. The new test pre-claims the dedup key and asserts `severityForEmerg` of the synthetic event yields the identical fingerprint a live transition event would (`liveMsg.Fingerprint == synthMsg.Fingerprint`).
- WR-05 — RESOLVED. `brevo.go`'s package doc was rewritten to describe the actual posture accurately (opportunistic STARTTLS, no ServerName pinning, PlainAuth guard as the credential-safety backstop, stripped-capability MITM ⇒ silently failed alert, not leaked password).
- WR-06 — RESOLVED. `proxyGet` parses the JSON error envelope and surfaces `body.error.message`/`type` in the thrown `GatewayError`; all three pages render `error.message` when the error is a `GatewayError`.
- WR-07 — RESOLVED. `StaleIndicator` computes `seconds` from `Date.now()` at render time; the `setInterval` is now only a re-render trigger (`setTick`), so a throttled background tab no longer desyncs the displayed staleness.
- WR-08 — RESOLVED. `isoDate` formats local date components directly (`getFullYear/getMonth/getDate`) instead of round-tripping through `toISOString()`.
- WR-09 — RESOLVED (sufficiently). The `db` Proxy now forwards `Reflect.get(real, prop, real)` — the real client is the receiver, so getters see the correct `this` and methods stay bound to the real drizzle client, never the proxy. A `getDb()` accessor was also added and documented as the preferred path for new code.

## Warnings

### WR-10: Per-tenant UI renders raw tenant UUIDs as human labels

**File:** `dashboard/src/app/(dashboard)/tenants/page.tsx:159-163`, `dashboard/src/components/tenant-table.tsx` (tenant column)

**Issue:** The CR-01 fix correctly made the contract honest: the gateway's `TenantLatencyRow` only carries `tenant_id` (a raw UUID) — there is no `slug` or `name`. But the dashboard still renders that UUID directly as the operator-facing label: the tenants-page `Select` shows `{id}` as each option's text, and `tenant-table.tsx` renders `tenant_id` raw in the Tenant column. An operator triaging an incident sees `8f1c0d2e-4a5b-...` instead of `converseai`. This was part of the original CR-01 description ("the UI renders it directly as a human label"); the type mismatch is fixed but the UX defect it described is not — it was merely surfaced honestly. Not a crash, so WARNING, not BLOCKER.

**Fix:** Either (a) extend the Go `TenantLatencyPercentiles` query + `TenantLatencyRow` to also emit `slug`/`name` (a join on `ai_gateway.tenants`), or (b) have the dashboard fetch the tenant catalog once and map UUID → slug client-side for display. Option (a) keeps the dashboard a pure consumer.

### WR-11: `dbFlusher.Flush` CopyFrom path for state-change rows is still untested

**File:** `gateway/internal/audit/writer.go:236-300`, `gateway/internal/emerg/fsm_test.go:336-387`

**Issue:** CR-03's prior fix recommendation explicitly asked for "a flusher test that runs an actual `WriteStateChange` Event through `dbFlusher.Flush` against a real/embedded Postgres to catch the NOT NULL / PK interaction." The code fix is correct — `WriteStateChange` mints `uuid.New()` for `request_id`, and `dbFlusher.Flush` now writes the new `reason` column in the CopyFrom column list (positionally aligned: `event_kind` then `reason` at the end of both the `rows` slice and the `[]string{...}` column list — verified consistent). But `writer_test.go` was not in the change set and `fsm_test.go` still exercises only `fakeStateChangeWriter`, which never touches the real `CopyFrom`. The 24-column CopyFrom slice ordering is correct by inspection, but a positional CopyFrom is exactly the kind of code where a future column insertion silently misaligns values with no compile error. The risk is now low (the all-zeros-UUID collision is fixed), so WARNING.

**Fix:** Add a `dbFlusher.Flush` test that runs a real `WriteStateChange` Event (with `event_kind` + `reason` set, `request_id` minted) through an embedded/containerized Postgres and asserts the row round-trips with `reason` populated and `request_id` non-nil. This also locks the CopyFrom column ordering against future drift.

## Info

### IN-07: `itoa` still hand-rolled in `alerter_test.go` (prior IN-03, not addressed)

**File:** `gateway/internal/alert/alerter_test.go:353-374`

**Issue:** The prior review's IN-03 noted the 20-line hand-rolled `itoa` duplicates `strconv.Itoa`. The fixer addressed all BLOCKER+WARNING findings but (correctly, per scope) left INFO items; `alerter_test.go` still carries the hand-rolled `itoa`, and `strconv` is now anyway imported elsewhere. Carried forward for visibility only.

**Fix:** `import "strconv"` in the test and delete `itoa`.

### IN-08: `channelJob` single-field wrapper retained (prior IN-04, not addressed)

**File:** `gateway/internal/alert/alerter.go:64-67`

**Issue:** `type channelJob struct { msg Message }` is still a one-field wrapper over `Message`; `chan channelJob` could be `chan Message`. Unchanged by the fixes (the WR-03 fix added `handleEvent` but kept the wrapper). Carried forward for visibility only.

**Fix:** Use `chan Message` directly, or add a comment naming the planned second field.

---

_Reviewed: 2026-05-14_
_Reviewer: Claude (gsd-code-reviewer)_
_Depth: standard (re-review iteration 1)_
