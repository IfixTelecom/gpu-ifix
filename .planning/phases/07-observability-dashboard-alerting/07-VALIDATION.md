---
phase: 7
slug: observability-dashboard-alerting
status: draft
nyquist_compliant: true
wave_0_complete: false
created: 2026-05-14
---

# Phase 7 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | `go test` (gateway) + `vitest` (dashboard — installed in Wave 1 plan 07-07) |
| **Config file** | gateway: existing; dashboard: `dashboard/vitest.config.ts` (created by 07-07 Task 1) |
| **Quick run command** | `cd gateway && go test ./internal/alert/... ./internal/audit/... ./internal/obs/... ./internal/admin/... -count=1` |
| **Full suite command** | `cd gateway && go test ./... -count=1 -race` + `cd dashboard && npm run build && npx tsc --noEmit && npx vitest run` |
| **Estimated runtime** | gateway quick ~15-30s; gateway full+race ~60-120s; dashboard build+vitest ~60-90s |

---

## Sampling Rate

- **After every task commit:** Run the quick run command (gateway tasks) or `npx vitest run` (dashboard tasks)
- **After every plan wave:** Run the full suite command
- **Before `/gsd-verify-work`:** Full suite must be green
- **Max feedback latency:** < 120s (gateway full+race is the ceiling)

---

## Per-Task Verification Map

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| 07-01-T1 | 01 | 1 | OBS-04, OBS-05 | T-07-01 | Alert credentials are optional Config fields, never logged | build | `cd gateway && go build ./internal/config/ && go test ./internal/config/ -count=1 -run TestLoad` | gateway/internal/config/config.go | ⬜ pending |
| 07-01-T2 | 01 | 1 | OBS-07 | T-07-02, T-07-03 | Additive nullable migration; idempotent goose up/down | build | `cd gateway && grep -q "ADD COLUMN IF NOT EXISTS event_kind" db/migrations/0020_audit_log_event_kind.sql && grep -q ListAuditStateChanges internal/db/gen/*.go && go build ./internal/db/...` | gateway/db/migrations/0020_audit_log_event_kind.sql | ⬜ pending |
| 07-01-T3 | 01 | 1 | OBS-02 | T-07-06 | Bounded-label histograms, ≤1 label each | build | `cd gateway && go build ./internal/obs/ ./internal/alert/ && grep -q gateway_request_duration_ms_by_route internal/obs/metrics.go` | gateway/internal/obs/metrics.go, gateway/internal/alert/testsupport.go | ⬜ pending |
| 07-02-T1 | 02 | 2 | OBS-02 | T-07-06 | Latency histograms recorded with bounded labels only | unit (tdd) | `cd gateway && go test ./internal/obs/ -count=1 -run TestMiddleware` | gateway/internal/obs/middleware.go | ⬜ pending |
| 07-02-T2 | 02 | 2 | OBS-08 | T-07-04 | BeforeSend redacts request/response bodies | unit (tdd) | `cd gateway && go test ./internal/obs/ -count=1 -run TestSentry` | gateway/internal/obs/sentry.go | ⬜ pending |
| 07-02-T3 | 02 | 2 | OBS-07 | T-07-05 | State changes leave append-only audit_log rows | unit (tdd) | `cd gateway && go test ./internal/audit/ -count=1` | gateway/internal/audit/writer.go | ⬜ pending |
| 07-03-T1 | 03 | 2 | OBS-01 | T-07-09, T-07-10 | Percentiles in Postgres; metadata-only columns | build | `cd gateway && grep -q percentile_cont db/queries/audit.sql && grep -rq TenantLatencyPercentiles internal/db/gen/ && go build ./internal/db/...` | gateway/db/queries/audit.sql | ⬜ pending |
| 07-03-T2 | 03 | 2 | OBS-01 | T-07-07, T-07-08 | Admin-key gated; query params validated before SQL | unit | `cd gateway && go test ./internal/admin/ -count=1 -run TestMetrics` | gateway/internal/admin/metrics.go | ⬜ pending |
| 07-03-T3 | 03 | 2 | OBS-07 | T-07-07, T-07-08, T-07-09 | Paginated; limit capped; metadata-only columns | unit | `cd gateway && go test ./internal/admin/ -count=1 -run TestAudit` | gateway/internal/admin/audit.go | ⬜ pending |
| 07-04-T1 | 04 | 2 | OBS-04 | T-07-11 | Dedup namespace + Channel contract isolated | build | `cd gateway && go build ./internal/redisx/ ./internal/alert/ ./internal/obs/ && grep -q "type Channel interface" internal/alert/client.go` | gateway/internal/alert/client.go, gateway/internal/redisx/alert.go | ⬜ pending |
| 07-04-T2 | 04 | 2 | OBS-05 | T-07-11, T-07-12, T-07-14 | Token in one method; secret-free errors; per-client breaker | unit | `cd gateway && go test ./internal/alert/ -count=1 -run 'TestChatwoot\|TestBrevo'` | gateway/internal/alert/chatwoot.go, gateway/internal/alert/brevo.go | ⬜ pending |
| 07-04-T3 | 04 | 2 | OBS-04 | T-07-11, T-07-13 | 4xx-except-429 is backoff.Permanent (no retry storm) | unit | `cd gateway && go test ./internal/alert/ -count=1 -run TestClickUp` | gateway/internal/alert/clickup.go | ⬜ pending |
| 07-05-T1 | 05 | 3 | OBS-04 | T-07-17 | Pure transform; stable fingerprint | unit (tdd) | `cd gateway && go test ./internal/alert/ -count=1 -run TestSeverity` | gateway/internal/alert/severity.go | ⬜ pending |
| 07-05-T2 | 05 | 3 | OBS-06 | T-07-16, T-07-19 | SET NX EX 300; fail-open critical / fail-closed warning | unit (tdd) | `cd gateway && go test ./internal/alert/ -count=1 -run TestDedup` | gateway/internal/alert/dedup.go | ⬜ pending |
| 07-05-T3 | 05 | 3 | OBS-04, OBS-06 | T-07-15, T-07-17, T-07-18 | Consume loop never blocks; malformed JSON survives | unit (tdd) + race | `cd gateway && go test ./internal/alert/ -count=1 -race` | gateway/internal/alert/alerter.go | ⬜ pending |
| 07-06-T1 | 06 | 4 | OBS-04, OBS-05 | T-07-22, T-07-23 | Alerter spawned before publishers; empty var → WARN not fail-boot | build + boot | `cd gateway && go build ./cmd/gateway/ && go test ./cmd/gateway/ -count=1` | gateway/cmd/gateway/main.go | ⬜ pending |
| 07-06-T2 | 06 | 4 | OBS-01 | T-07-20 | Admin routes mounted only under the admin-key sub-router | build + boot | `cd gateway && go build ./cmd/gateway/ && go test ./cmd/gateway/ -count=1` | gateway/cmd/gateway/main.go | ⬜ pending |
| 07-06-T3 | 06 | 4 | OBS-07 | T-07-21 | FSM transitions write append-only fsm_transition rows | unit | `cd gateway && go test ./internal/emerg/ -count=1` | gateway/internal/emerg/fsm.go | ⬜ pending |
| 07-07-T1 | 07 | 1 | OBS-03 | T-07-28 | Pinned versions; no secrets in client bundle | build | `cd dashboard && npm install && npm run build && npx vitest run` | dashboard/package.json, dashboard/components.json | ⬜ pending |
| 07-07-T2 | 07 | 1 | OBS-03 | T-07-25, T-07-26, T-07-27 | Standalone Better Auth; ai_gateway-isolated DB; unauthed→/login | build + typecheck | `cd dashboard && npm run build && npx tsc --noEmit` | dashboard/src/lib/auth.ts, dashboard/src/middleware.ts | ⬜ pending |
| 07-07-T3 | 07 | 1 | OBS-03 | T-07-24 | X-Admin-Key only in the server proxy route | unit + build | `cd dashboard && npm run build && npx vitest run src/lib/gateway.test.ts` | dashboard/src/app/api/gateway/[...path]/route.ts | ⬜ pending |
| 07-08-T1 | 08 | 2 | OBS-03 | T-07-30, T-07-32 | Polling 5-10s; banner is read-only local state | component (vitest) | `cd dashboard && npx vitest run src/components/critical-banner.test.tsx && npm run build` | dashboard/src/components/critical-banner.tsx | ⬜ pending |
| 07-08-T2 | 08 | 2 | OBS-03 | T-07-29 | All fetches via the server proxy wrappers | component (vitest) | `cd dashboard && npx vitest run src/components/fsm-panel.test.tsx && npm run build` | dashboard/src/components/fsm-panel.tsx | ⬜ pending |
| 07-08-T3 | 08 | 2 | OBS-03 | T-07-31 | Audit table renders metadata-only columns | build + typecheck + vitest | `cd dashboard && npm run build && npx tsc --noEmit && npx vitest run` | dashboard/src/components/audit-table.tsx | ⬜ pending |
| 07-08-T4 | 08 | 2 | OBS-03 | T-07-29 | HUMAN-VERIFY — no X-Admin-Key in browser; UI-SPEC compliance | manual (checkpoint) | manual — see plan 07-08 Task 4 | — | ⬜ pending |
| 07-09-T1 | 09 | 5 | OBS-02, OBS-08 | T-07-33 | Runbook documents cardinality audit + Sentry redaction | doc | `test -f docs/RUNBOOK-OBSERVABILITY-ALERTING.md && grep -qi cardinality docs/RUNBOOK-OBSERVABILITY-ALERTING.md` | docs/RUNBOOK-OBSERVABILITY-ALERTING.md | ⬜ pending |
| 07-09-T2 | 09 | 5 | OBS-04, OBS-05 | T-07-33, T-07-34 | UAT sheet covers SC-2/3/5/6 + sign-off attribution | doc | `test -f .planning/phases/07-observability-dashboard-alerting/07-HUMAN-UAT.md && grep -c "SC-" .planning/phases/07-observability-dashboard-alerting/07-HUMAN-UAT.md` | .planning/phases/07-observability-dashboard-alerting/07-HUMAN-UAT.md | ⬜ pending |
| 07-09-T3 | 09 | 5 | OBS-04, OBS-05, OBS-08 | T-07-33, T-07-34, T-07-35 | HUMAN-UAT — live WhatsApp/email/ClickUp/Sentry/dashboard | manual (checkpoint) | manual — see plan 07-09 Task 3 | — | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

Wave 1 (07-01 + 07-07) IS the Wave 0 scaffolding for this phase — every later-wave plan programs against contracts created here. No `<automated>MISSING>` markers exist; all test infrastructure is created before it is consumed.

- **Gateway test fixtures** — `gateway/internal/alert/testsupport.go` (07-01 Task 3): shared `FakeChatwoot`, `FakeClickUp`, `FakeBrevo` recording fakes. Consumed by 07-04 (client tests) and 07-05 (alerter tests). Build-tag-free so both import it.
- **Gateway contracts** — config alert env vars + migration 0020 + `ListAuditStateChanges` query + the two latency histograms + `gateway_alert_dropped_total` (07-01): the typed surfaces 07-02..07-06 build against.
- **Dashboard test framework** — `dashboard/vitest.config.ts` + the pinned dependency set with the `react-is` override (07-07 Task 1): vitest must install + run before 07-08's component tests exist.
- **Dashboard contracts** — `dashboard/src/lib/gateway.ts` typed fetch wrappers + the `/api/gateway/*` server proxy + the Better Auth boundary (07-07): the contracts 07-08's components consume.

`wave_0_complete` flips to `true` once 07-01 and 07-07 are both GREEN.

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| WhatsApp + email delivered within 60s of a critical event | OBS-04, OBS-05 (SC-2) | Requires live Chatwoot + Brevo credentials + a real on-call routing target (07-RESEARCH.md Open Questions 1+3, Assumption A6 = highest risk) | Plan 07-09 Task 3, scenario S1 — induce a critical event, confirm WhatsApp + email arrive within 60s and the dashboard banner appears |
| ClickUp task opened for a critical/warning alert | OBS-04, OBS-05 | Requires a live ClickUp service token + a target list (07-RESEARCH.md Open Question 2) | Plan 07-09 Task 3, scenario S2 — trigger a critical alert, confirm a task appears in the target ClickUp list |
| Warning-tier event repeated within 5 min produces one notification | OBS-06 (SC-3) | Requires a deployed gateway with live channels to observe real dedup | Plan 07-09 Task 3, scenario S3 — repeat a warning event, confirm exactly one notification per channel |
| Prometheus `/metrics` stays under 10k active series | OBS-02 (SC-5) | Requires a deployed gateway under realistic load + Prometheus tooling | Plan 07-09 Task 3, scenario S4 — `curl /metrics` + `promtool check metrics` + a series-count query |
| Sentry redacts authorization / x-api-key / payload bodies | OBS-08 (SC-6) | Requires a live Sentry DSN + a captured event in the Sentry UI | Plan 07-09 Task 3, scenario S5 — trigger a captured event, confirm the redacted fields in Sentry |
| Live dashboard shows per-tenant latency + cost + FSM state | OBS-03 (SC-1) | Requires the deployed dashboard + a running gateway + Better Auth accounts | Plan 07-08 Task 4 + plan 07-09 Task 3 scenario S6 — sign in, confirm live polling and the no-`X-Admin-Key`-in-browser invariant |

Channels degrade gracefully to "log + dashboard banner only" when credentials are absent (gateway optional-feature pattern) — the autonomous build (07-01..07-08) is NOT blocked by missing credentials. The live-delivery success criteria are gated in plan 07-09, consistent with 03-08 / 04-09 / 06-11.

---

## Validation Sign-Off

- [x] All tasks have `<automated>` verify or Wave 0 dependencies (checkpoint tasks 07-08-T4 / 07-09-T3 are manual by design)
- [x] Sampling continuity: no 3 consecutive tasks without automated verify (every non-checkpoint task has an automated command)
- [x] Wave 0 covers all MISSING references (Wave 1 = 07-01 + 07-07 creates every contract; no MISSING markers)
- [x] No watch-mode flags (all commands use `-count=1` / `vitest run`, never `--watch`)
- [x] Feedback latency < 120s
- [x] `nyquist_compliant: true` set in frontmatter

**Approval:** approved (gsd-planner, 2026-05-14)
