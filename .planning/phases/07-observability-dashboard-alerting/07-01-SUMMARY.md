---
phase: 07-observability-dashboard-alerting
plan: 01
subsystem: infra
tags: [go, prometheus, sqlc, goose, postgres, config, alerting, observability]

# Dependency graph
requires:
  - phase: 02-gateway-http-go
    provides: config.Load() optional-env-var pattern (SentryDSN precedent), obs/metrics.go promauto collectors, audit_log partitioned table + audit.sql, goose migration chain
  - phase: 06-auto-provisioning-emergency-pod-vast-ai
    provides: emergency FSM states + emergency_lifecycles audit table (Phase 7 dashboard visualizes these)
provides:
  - 13 optional Phase 7 alert Config fields (Chatwoot/ClickUp/Brevo) parsed in Load(), none fail-boot
  - Migration 0020 — additive nullable audit_log.event_kind TEXT column (goose up/down, idempotent)
  - ListAuditStateChanges :many sqlc query + generated method/Row type for the 07-03 admin handler
  - obs.RequestDurationByRoute + obs.RequestDurationByUpstream bounded latency histograms
  - obs.AlertDroppedTotal plain back-pressure counter
  - internal/alert package — doc.go + build-tag-free FakeChatwoot/FakeClickUp/FakeBrevo recording fakes
affects: [07-02, 07-03, 07-04, 07-05, 07-06]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Additive optional-env-var extension — new alert vars mirror SentryDSN: parsed in Load(), never in requiredOrder, empty = channel disabled with WARN"
    - "Wave-0 contract scaffolding — env vars / migration / query / collectors / test fakes laid before downstream plans consume them, so 07-02..07-06 run against fixed contracts"
    - "Interface-first test support — recording fakes shipped before the Channel interface (07-04) so concrete-client + alerter plans share stable doubles"

key-files:
  created:
    - gateway/db/migrations/0020_audit_log_event_kind.sql
    - gateway/internal/alert/doc.go
    - gateway/internal/alert/testsupport.go
  modified:
    - gateway/internal/config/config.go
    - gateway/internal/config/config_test.go
    - gateway/db/queries/audit.sql
    - gateway/internal/db/gen/audit.sql.go
    - gateway/internal/db/gen/models.go
    - gateway/internal/db/gen/querier.go
    - gateway/internal/db/migrate_test.go
    - gateway/internal/obs/metrics.go

key-decisions:
  - "Latency histograms keep exactly one label each (route OR upstream) — no tenant×route×upstream cross; per-tenant percentiles come from audit_log SQL in 07-03, keeping cardinality ~105 series"
  - "alert recording fakes are self-contained (Send(ctx, title, body string) error) rather than referencing the Channel/Message types — those are defined later by 07-04, so 07-01 cannot depend on them"
  - "testsupport.go is build-tag-free (production-adjacent test support) so both 07-04 and 07-05 can import the same fakes without a _test.go visibility wall"

patterns-established:
  - "Phase 7 alert env vars: optional Config strings, parsed in Load(), never in requiredOrder — boot never fails on a missing alert credential"
  - "Additive nullable migration: ADD COLUMN IF NOT EXISTS on the partitioned parent + matching DROP COLUMN IF EXISTS Down block, idempotent under AI_GATEWAY_MIGRATE_ON_BOOT"

requirements-completed: [OBS-01, OBS-02, OBS-04, OBS-05, OBS-07]

# Metrics
duration: ~15min
completed: 2026-05-14
---

# Phase 7 Plan 01: Wave-0 Observability + Alerting Scaffolding Summary

**Laid every Phase 7 contract downstream plans consume: 13 optional Chatwoot/ClickUp/Brevo alert env vars, the additive `audit_log.event_kind` migration 0020 + `ListAuditStateChanges` sqlc query, two bounded latency histograms + an alert-drop counter in obs/metrics.go, and the build-tag-free alert-package recording fakes.**

## Performance

- **Duration:** ~15 min
- **Tasks:** 3 (plus one auto-fix follow-up commit)
- **Files modified:** 11 (3 created, 8 modified)

## Accomplishments

- **Config contract** — 13 typed `Config` fields for the three alert channels, all parsed in `Load()` from the `CHATWOOT_/CLICKUP_/BREVO_/ALERT_EMAIL_` env vars, none added to `requiredOrder`. Three new tests (`TestLoad_Phase7Defaults/FromEnv/NotRequired`) prove defaults, overrides, and the threat T-07-01 guard that no alert var appears in the required-var error.
- **DB contract** — migration `0020_audit_log_event_kind.sql` adds a nullable `event_kind TEXT` column to the partitioned `audit_log` parent (additive, idempotent, goose up/down). `audit.sql` gains `ListAuditStateChanges :many`; `sqlc generate` produced the method, `ListAuditStateChangesParams`/`Row` types, and added `EventKind` to the `AiGatewayAuditLog` model.
- **Metrics contract** — `obs/metrics.go` gains `RequestDurationByRoute` + `RequestDurationByUpstream` (`gateway_request_duration_ms_by_{route,upstream}`, 9 buckets 25ms-10s, one label each) and `AlertDroppedTotal` (`gateway_alert_dropped_total` plain counter), all under a cardinality-budget header.
- **Alert package** — new `internal/alert` package with `doc.go` and `testsupport.go` exporting `FakeChatwoot`/`FakeClickUp`/`FakeBrevo` recording fakes (Sent slice + structured Calls + configurable Err + Name/Reset), build-tag-free so 07-04 and 07-05 share them.

## Task Commits

Each task was committed atomically:

1. **Task 1: Config — add 12 optional alert env vars** - `43cfe28` (feat)
2. **Task 2: Migration 0020 + ListAuditStateChanges query** - `ae78e77` (feat)
   - Auto-fix follow-up: **sync TestEmbedFS_HasAllMigrations** - `0f61aa9` (fix)
3. **Task 3: obs/metrics.go collectors + alert package test support** - `5b0e047` (feat)

## Files Created/Modified

- `gateway/internal/config/config.go` - 13 Phase 7 alert `Config` fields + their `Load()` parsing (optional; not in `requiredOrder`)
- `gateway/internal/config/config_test.go` - `TestLoad_Phase7Defaults/FromEnv/NotRequired` + `phase7OptionalEnv`/`clearPhase7` helpers
- `gateway/db/migrations/0020_audit_log_event_kind.sql` - additive nullable `audit_log.event_kind` column, goose up/down, Pitfall 8 partition-window note
- `gateway/db/queries/audit.sql` - `ListAuditStateChanges :many` paginated read (`WHERE event_kind IS NOT NULL`)
- `gateway/internal/db/gen/audit.sql.go` - sqlc-generated `ListAuditStateChanges` method + Params/Row types
- `gateway/internal/db/gen/models.go` - `EventKind pgtype.Text` added to `AiGatewayAuditLog`
- `gateway/internal/db/gen/querier.go` - `ListAuditStateChanges` added to the `Querier` interface
- `gateway/internal/db/migrate_test.go` - `want` list synced to 0020 (also closed the pre-existing 0019 gap)
- `gateway/internal/obs/metrics.go` - `RequestDurationByRoute`, `RequestDurationByUpstream`, `AlertDroppedTotal` + cardinality-budget header
- `gateway/internal/alert/doc.go` - package doc for the Phase 7 alerting goroutine
- `gateway/internal/alert/testsupport.go` - `FakeChatwoot`/`FakeClickUp`/`FakeBrevo` recording fakes + `FakeCall` struct

## Decisions Made

- **Histograms keep one label each.** `RequestDurationByRoute` and `RequestDurationByUpstream` each carry a single label; no `tenant × route × upstream` cross. Per-tenant percentiles will be computed from `audit_log` SQL aggregation in plan 07-03, which keeps the Phase 7 metric baseline at ~105 series — far under the OBS-02 10k ceiling.
- **Fakes are self-contained.** The plan's `read_first` notes the `Channel` interface + `Message` struct are defined later by 07-04 (`client.go`). The recording fakes therefore expose `Send(ctx, title, body string) error` (matching the plan's `FakeChatwoot{ Sent []string; Err error }` example) instead of referencing types that do not exist yet — 07-04/07-05 adapt them to the real `Channel` once it lands. Each fake also has `Name()` matching the planned `Channel.Name()` contract.
- **`testsupport.go` is build-tag-free.** Per the plan action ("shared production-adjacent test support, like other in-repo `__test-helpers__` analogs"), so both 07-04's concrete-client tests and 07-05's alerter tests import the same doubles without a `_test.go` visibility wall.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Synced `TestEmbedFS_HasAllMigrations` want list to include 0019 + 0020**
- **Found during:** Task 2 (Migration 0020 + ListAuditStateChanges query)
- **Issue:** `gateway/internal/db/migrate_test.go` hard-codes the expected embedded-migration list, which stopped at `0018`. Phase 6's `0019_emergency_lifecycles.sql` was never appended (pre-existing tech debt explicitly logged in STATE.md Open Todos), and adding `0020` in this plan made `go test ./internal/db/` fail with `expected 18 migrations embedded, got 20`.
- **Fix:** Appended `0019_emergency_lifecycles.sql` and `0020_audit_log_event_kind.sql` to the `want` slice and updated the doc comment. The 0019 entry is pre-existing debt but was bundled because the list is one contiguous edit and leaving it half-fixed would still fail the test.
- **Files modified:** `gateway/internal/db/migrate_test.go`
- **Verification:** `go test ./internal/db/ -count=1 -run TestEmbedFS_HasAllMigrations` → ok
- **Committed in:** `0f61aa9` (separate fix commit, tied to Task 2)

---

**Total deviations:** 1 auto-fixed (1 blocking)
**Impact on plan:** The auto-fix was required to keep the db test suite green after adding migration 0020. It also incidentally closed the pre-existing 0019 gap from STATE.md Open Todos — no scope creep, the change is a four-line list append.

## Issues Encountered

- `sqlc` is not on `PATH`; it is installed at `$(go env GOPATH)/bin/sqlc` (v1.30.0, matching the existing generated headers). Invoked via the absolute path from the `gateway/` directory — `sqlc generate` regenerated cleanly.

## Threat Surface

No new threat surface beyond the plan's `<threat_model>`. T-07-01 (alert credentials) is handled as designed — the new `Config` fields are plain strings, `config.go` does not log, and `TestLoad_Phase7NotRequired` is the explicit guard that no alert var leaks into the required-var error string. T-07-02 / T-07-03 (migration 0020 on shared `audit_log`) are handled: the Up block is purely additive (`ADD COLUMN IF NOT EXISTS` on the partitioned parent, no DROP/RENAME of existing columns), the Down block guards with `DROP COLUMN IF EXISTS`, and the Pitfall 8 partition window is documented in the migration header.

## Known Stubs

None — this is a Wave-0 contract plan. The alert recording fakes in `testsupport.go` are intentionally call-recording doubles (their reason for existing); they are not stubs that block any Phase 7 goal. The real Chatwoot/ClickUp/Brevo clients land in plan 07-04, the alerter in 07-05, and the dashboard wiring in 07-06.

## User Setup Required

**External services require manual configuration** before the alert channels are functional (consumed starting plan 07-04/07-05). The plan frontmatter `user_setup` block lists the env vars:
- **Chatwoot** — `CHATWOOT_API_URL`, `CHATWOOT_API_TOKEN`, `CHATWOOT_ONCALL_ACCOUNT_ID`, `CHATWOOT_ONCALL_INBOX_ID`, `CHATWOOT_ONCALL_CONTACT_ID` (critical-tier WhatsApp delivery, OBS-05)
- **ClickUp** — `CLICKUP_API_TOKEN`, `CLICKUP_ALERT_LIST_ID` (critical + warning alert tasks, OBS-04)
- **Brevo** — `BREVO_SMTP_HOST`, `BREVO_SMTP_PORT`, `BREVO_SMTP_USER`, `BREVO_SMTP_PASS`, `ALERT_EMAIL_TO`, `ALERT_EMAIL_FROM` (critical + warning alert email, OBS-05)

All are optional — the gateway boots and runs normally with every one unset; each disabled channel logs a single WARN at startup (WARN logging lands with the clients in 07-04/07-05).

## Next Phase Readiness

- **07-02..07-06 unblocked.** The contracts are in place: the 13 alert env vars exist before any client reads them, migration 0020 + `ListAuditStateChanges` exist before the 07-03 admin handler consumes them, the latency histograms + drop counter exist before the middleware records them, and the alert-package fakes exist before 07-04/07-05 write client/alerter tests.
- **No blockers.** Whole gateway compiles (`go build ./...`), `go vet` clean on all touched packages, `go test ./internal/config/ ./internal/obs/ ./internal/db/` green.

## Self-Check: PASSED

All 3 created files exist on disk; all 5 commits (`43cfe28`, `ae78e77`, `0f61aa9`, `5b0e047`, `5938f8f`) are reachable in git history. Working tree clean.

---
*Phase: 07-observability-dashboard-alerting*
*Completed: 2026-05-14*
