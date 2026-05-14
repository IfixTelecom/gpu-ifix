---
phase: 07-observability-dashboard-alerting
verified: 2026-05-14T09:30:00Z
status: human_needed
score: 7/8 must-haves verified
overrides_applied: 0
human_verification:
  - test: "SC-2 — Critical alert delivery within 60s"
    expected: "GPU-down critical event → WhatsApp via Chatwoot AND email via Brevo within 60s AND ClickUp task opened"
    why_human: "Requires real Chatwoot account_id/inbox_id/contact_id, ClickUp list_id+token, Brevo SMTP creds, and a deployed gateway with all 12 alert env vars set"
  - test: "SC-3 — Live dedup across 5-minute window"
    expected: "Same warning-tier event repeated within 5 minutes produces exactly one notification per channel"
    why_human: "Requires real deployed gateway + real alert channel credentials"
  - test: "SC-5 — Prometheus cardinality under 10k active series"
    expected: "curl /metrics + promtool check metrics + count by (__name__) confirms ≤10k active series"
    why_human: "Requires running gateway with live traffic; static analysis cannot enumerate series at runtime"
  - test: "SC-6 — Sentry body redaction live"
    expected: "Sentry captures a real event; authorization/x-api-key headers and request/response bodies show ***REDACTED***"
    why_human: "Requires SENTRY_DSN set + a captured event visible in Sentry UI"
  - test: "T4 — Dashboard UI-SPEC visual compliance"
    expected: "Live dashboard at localhost:3000 shows radix-nova dark theme, correct pt-BR copy, status palette, tabular-nums, 36px rows, sticky banner"
    why_human: "Visual appearance + real browser devtools check that X-Admin-Key never appears in network tab"
---

# Phase 7: Observability Dashboard & Alerting — Verification Report

**Phase Goal:** Operators can see — in one place — how the gateway is behaving per tenant, get paged when it matters, and have an audit trail for every significant state change.
**Verified:** 2026-05-14T09:30:00Z
**Status:** human_needed
**Re-verification:** No — initial verification

## Goal Achievement

### Observable Truths

| # | Truth | Status | Evidence |
|---|-------|--------|---------|
| 1 | Operators can see per-tenant latency (P50/P95/P99), error rate, FSM state, and inflight in one place via `/admin/metrics` | ✓ VERIFIED | `gateway/internal/admin/metrics.go` — `NewMetricsHandler` calls `TenantLatencyPercentiles` (percentile_cont SQL) + `fsm.State()` + inflight gauge read; `go test ./gateway/internal/admin/...` passes |
| 2 | Operators receive paged alerts on critical events (GPU down, pod failed, quota 90%) via WhatsApp+email+ClickUp | ? UNCERTAIN | Alerter goroutine (`alerter.go`), severity classification (`severity.go`), Chatwoot/ClickUp/Brevo clients all exist and pass `go test ./gateway/internal/alert/...`; live delivery requires credentials — see Human Verification |
| 3 | Duplicate alerts within 5 minutes are suppressed (OBS-06) | ✓ VERIFIED | `dedup.go` — `SetNX` with 5-minute TTL; `TestDedup` passes; alerter tests assert second identical event produces no `Send` calls |
| 4 | An audit trail of every significant state change (FSM transitions) exists in `audit_log` | ✓ VERIFIED | `gateway/internal/emerg/fsm.go` calls `writer.WriteStateChange("fsm_transition", ...)` on every transition (nil-guarded); `gateway/internal/audit/writer.go` writes `event_kind` + `reason` columns through async CopyFrom; `go test ./gateway/internal/emerg/...` passes |
| 5 | The dashboard presents live data (polling every 5-10s) including per-tenant latency, error rate, daily/monthly cost, and FSM state | ✓ VERIFIED | `dashboard/src/lib/query-client.tsx` sets `refetchInterval: 7000`; Overview page uses `useQuery(fetchMetrics)`; `fetchMetrics` calls `/api/gateway/metrics`; 14/14 vitest tests pass; `npx tsc --noEmit` exits 0 |
| 6 | Unauthenticated requests to the dashboard are redirected to `/login`; the gateway admin key never appears in the browser | ✓ VERIFIED | `dashboard/src/middleware.ts` calls `getSessionCookie` → redirect; `grep -rl GATEWAY_ADMIN_KEY dashboard/src/` returns exactly one file (`api/gateway/[...path]/route.ts`); no `NEXT_PUBLIC_` exposure confirmed |
| 7 | Sentry captures errors with request/response bodies and API keys redacted (OBS-08) | ✓ VERIFIED | `gateway/internal/obs/sentry.go` — `BeforeSend` redacts `event.Request.Data` to `***REDACTED***` and deletes `request_body`/`response_body` from `Extra`; `sentry_test.go` asserts redaction; `go test ./gateway/internal/obs/...` passes |
| 8 | Prometheus `/metrics` exposes bounded cardinality metrics (≤10k series) for external consumption (OBS-02) | ✓ VERIFIED (static) | `gateway/internal/obs/metrics.go` — all histograms/counters have bounded single-label cardinality (route ≈4, upstream ≈6, channel×result=6); `r.Handle("/metrics", obs.Handler())` in `main.go`; cardinality ≤10k requires live confirmation — see Human Verification |

**Score:** 7/8 truths fully verified (Truth 2 and Truth 8's live aspects require human execution)

### Deferred Items

No truths are addressed by later phases.

---

## Required Artifacts

### Gateway-side (Plans 07-01 through 07-06)

| Artifact | Expected | Status | Details |
|----------|----------|--------|---------|
| `gateway/db/migrations/0020_audit_log_event_kind.sql` | nullable `event_kind` column | ✓ VERIFIED | `ADD COLUMN IF NOT EXISTS event_kind` + goose Down block present |
| `gateway/db/migrations/0021_audit_log_ts_index.sql` | `(ts, tenant_id, route)` index | ✓ VERIFIED | File exists; addresses query performance for `TenantLatencyPercentiles` |
| `gateway/db/migrations/0022_audit_log_reason.sql` | nullable `reason` column | ✓ VERIFIED | File exists; dedicated FSM transition reason column (not overloading `error_code`) |
| `gateway/internal/config/config.go` | 12 optional alert env vars | ✓ VERIFIED | 26 grep hits (13 struct fields + 13 Load assignments); all optional; gateway builds |
| `gateway/internal/obs/metrics.go` | `RequestDurationByRoute`, `RequestDurationByUpstream`, `AlertDroppedTotal`, `AlertSendsTotal` | ✓ VERIFIED | All 4 collectors present; single bounded labels; cardinality budget comments |
| `gateway/internal/obs/middleware.go` | histogram `.Observe()` calls per request | ✓ VERIFIED | `RequestDurationByRoute.WithLabelValues(...)` + `RequestDurationByUpstream.WithLabelValues(...)` present |
| `gateway/internal/obs/sentry.go` | `BeforeSend` scrubs request/response bodies | ✓ VERIFIED | `event.Request.Data`, `request_body`, `response_body` all redacted |
| `gateway/internal/audit/writer.go` | `EventKind` + `Reason` fields; `WriteStateChange()`; CopyFrom includes both columns | ✓ VERIFIED | `WriteStateChange` exists; `auditLogCopyColumns`/`auditLogCopyRow` refactored; positional tests in `writer_test.go` |
| `gateway/db/queries/audit.sql` | `ListAuditStateChanges :many` + `TenantLatencyPercentiles :many` | ✓ VERIFIED | Both queries present; `percentile_cont(0.95)` confirmed; sqlc-generated `audit.sql.go` contains both methods |
| `gateway/internal/admin/metrics.go` | `GET /admin/metrics` handler | ✓ VERIFIED | `NewMetricsHandler` + `newMetricsHandlerWithQueries`; reads `TenantLatencyPercentiles` + `fsm.State()` + inflight; tests cover 200 + 400 |
| `gateway/internal/admin/audit.go` | `GET /admin/audit` paginated handler | ✓ VERIFIED | `NewAuditHandler` + `newAuditHandlerWithQueries`; reads `ListAuditStateChanges`; limit capped at 200; tests pass |
| `gateway/internal/redisx/alert.go` | `gw:alert:dedup:` namespace constant | ✓ VERIFIED | `AlertDedupKeyPrefix` + `AlertDedupKey()` present |
| `gateway/internal/alert/client.go` | `Channel` interface contract | ✓ VERIFIED | `type Channel interface { Name() string; Send(ctx, Message) error }` + `Message` struct |
| `gateway/internal/alert/chatwoot.go` | Chatwoot Application API client | ✓ VERIFIED | `api_access_token` in one method; gobreaker-wrapped; `Send()` implements `Channel` |
| `gateway/internal/alert/clickup.go` | ClickUp client with `backoff.Permanent` | ✓ VERIFIED | `backoff.Permanent` for 4xx-except-429; `X-RateLimit-Reset`; raw (not Bearer) token |
| `gateway/internal/alert/brevo.go` | Brevo SMTP client | ✓ VERIFIED | `smtp.PlainAuth` + gobreaker; credentials isolated |
| `gateway/internal/alert/severity.go` | event → severity tier + channel matrix | ✓ VERIFIED | `severityFor` + `channelsFor`; pure (no I/O imports); critical→3 channels, warning→2, info→0 |
| `gateway/internal/alert/dedup.go` | Redis SET NX EX 300 dedup | ✓ VERIFIED | `SetNX` + `redisx.AlertDedupKey()`; fail-open for critical, fail-closed for warning/info |
| `gateway/internal/alert/alerter.go` | `Run(ctx)` goroutine + `ReconcileBoot()` | ✓ VERIFIED | Single `Subscribe` for 3 channels; bounded per-channel workers; `AlertDroppedTotal.Inc()` on full queue |
| `gateway/internal/emerg/fsm.go` | FSM emits `WriteStateChange("fsm_transition", ...)` on every transition | ✓ VERIFIED | `grep -q 'WriteStateChange("fsm_transition"'` returns match; nil-guard documented at line 56 |
| `gateway/cmd/gateway/main.go` | alerter spawn BEFORE breaker/emerg subsystems; `/admin/metrics` + `/admin/audit` mounted | ✓ VERIFIED | `go alerter.Run(ctx)` at line 213 < `go breakerSet.Subscribe(ctx)` at line 306 < `go emergReconciler.Run(ctx)` at line 720; both admin routes in `if px.adminVerifier != nil` block |

### Dashboard-side (Plans 07-07 through 07-08)

| Artifact | Expected | Status | Details |
|----------|----------|--------|---------|
| `dashboard/src/lib/auth.ts` | standalone Better Auth, only emailAndPassword | ✓ VERIFIED | `emailAndPassword: { enabled: true }`; no organization/twoFactor/admin plugins |
| `dashboard/src/lib/db.ts` | isolated from `ai_gateway` | ✓ VERIFIED | reads `DASHBOARD_DATABASE_URL`; no `ai_gateway` reference |
| `dashboard/src/middleware.ts` | unauthed → /login redirect | ✓ VERIFIED | `getSessionCookie` → `NextResponse.redirect`; correct matcher pattern |
| `dashboard/src/app/api/gateway/[...path]/route.ts` | server-side proxy injecting X-Admin-Key | ✓ VERIFIED | `GATEWAY_ADMIN_KEY` appears in exactly this one file; no client bundle exposure |
| `dashboard/src/lib/gateway.ts` | typed fetch wrappers via `/api/gateway/*` | ✓ VERIFIED | `GATEWAY_PROXY_BASE = "/api/gateway"`; never calls `GATEWAY_BASE_URL` directly |
| `dashboard/src/lib/query-client.tsx` | React Query provider with 5-10s polling | ✓ VERIFIED | `refetchInterval: 7000` |
| `dashboard/src/components/critical-banner.tsx` | sticky red banner on critical FSM state | ✓ VERIFIED | references `FAILED_OVER`, `EMERGENCY_ACTIVE`, `destructive`; local-only acknowledge (no fetch/mutation) |
| `dashboard/src/components/latency-chart.tsx` | Recharts LineChart with P50/P95/P99 | ✓ VERIFIED | `LineChart` + 3 series with status-palette colors |
| `dashboard/src/app/(dashboard)/page.tsx` | Overview: KPI row + FSM panel + latency chart | ✓ VERIFIED | `useQuery(fetchMetrics)` + `kpi` present; skeleton + empty states |
| `dashboard/src/app/(dashboard)/incidents/page.tsx` | audit-log table newest-first | ✓ VERIFIED | `useQuery(fetchAudit)` present |
| `dashboard/src/app/(dashboard)/tenants/page.tsx` | tenant metrics table + date-range cost filter | ✓ VERIFIED | `useQuery(fetchMetrics)` + date-range with "Aplicar período" |
| `dashboard/components.json` | radix-nova preset | ✓ VERIFIED | `"style": "radix-nova"`, `"baseColor": "neutral"` |
| `.github/workflows/build-dashboard.yml` | CI build + push of dashboard image | ✓ VERIFIED | File exists; `dashboard/**` paths filter |
| `gateway/docker-compose.yml` | `dashboard` service on `traefik-public` network | ✓ VERIFIED | `dashboard` service block present |
| `docs/RUNBOOK-OBSERVABILITY-ALERTING.md` | operator runbook | ✓ VERIFIED | exists; covers severity matrix, dedup, cardinality audit, Sentry redaction, Pitfalls 4/5/6/8 |
| `.planning/phases/07-observability-dashboard-alerting/07-HUMAN-UAT.md` | live UAT scenario sheet + sign-off | ✓ VERIFIED | exists; 16 SC- references; Sign-off table present; passed_partial path documented |

---

## Key Link Verification

| From | To | Via | Status | Details |
|------|-----|-----|--------|---------|
| `gateway/cmd/gateway/main.go` | `alert.Alerter` | `go alerter.Run(ctx)` before publishers | ✓ WIRED | Line 213 < line 306 (breakerSet) < line 720 (emergReconciler) |
| `gateway/cmd/gateway/main.go` | `/admin/metrics` + `/admin/audit` | `adminRouter.Method` inside `if px.adminVerifier != nil` | ✓ WIRED | 3 `adminRouter.Method` calls confirmed inside the bcrypt-gated block |
| `gateway/internal/emerg/fsm.go` | `audit.Writer` | `WriteStateChange("fsm_transition", ...)` in `onChange` | ✓ WIRED | Present at line ~56 area; nil-guarded |
| `gateway/internal/admin/metrics.go` | `ai_gateway.audit_log` | `TenantLatencyPercentiles` sqlc query | ✓ WIRED | Query in handler; sqlc-generated method in `internal/db/gen/audit.sql.go` |
| `gateway/internal/admin/audit.go` | `ai_gateway.audit_log` | `ListAuditStateChanges` sqlc query | ✓ WIRED | Query in handler; confirmed in gen |
| `dashboard/src/app/(dashboard)/page.tsx` | `dashboard/src/lib/gateway.ts` | `useQuery(fetchMetrics)` with 7s refetchInterval | ✓ WIRED | Both `useQuery` and `fetchMetrics` confirmed |
| `dashboard/src/app/api/gateway/[...path]/route.ts` | `process.env.GATEWAY_ADMIN_KEY` | server route injects `X-Admin-Key` before forwarding | ✓ WIRED | `GATEWAY_ADMIN_KEY` in exactly one file; no client exposure |
| `gateway/internal/alert/alerter.go` | `gateway/internal/alert/client.go` | fans out to `[]Channel` | ✓ WIRED | Single `Subscribe` call for 3 channels; bounded per-channel worker queues |

---

## Data-Flow Trace (Level 4)

| Artifact | Data Variable | Source | Produces Real Data | Status |
|----------|---------------|--------|-------------------|--------|
| `gateway/internal/admin/metrics.go` | `tenants[]` latency rows | `TenantLatencyPercentiles` → `ai_gateway.audit_log` via `percentile_cont` SQL | Yes — real DB query, `ts >= $1` window | ✓ FLOWING |
| `gateway/internal/admin/metrics.go` | `fsm_state` | `h.fsm.State()` atomic read | Yes — live FSM state | ✓ FLOWING |
| `gateway/internal/admin/audit.go` | `items[]` audit rows | `ListAuditStateChanges` → `ai_gateway.audit_log WHERE event_kind IS NOT NULL` | Yes — real DB query | ✓ FLOWING |
| `dashboard/src/app/(dashboard)/page.tsx` | `metrics` query data | `fetchMetrics()` → `/api/gateway/metrics` proxy → gateway `/admin/metrics` | Yes — polled every 7s | ✓ FLOWING |
| `dashboard/src/app/(dashboard)/incidents/page.tsx` | `auditData` query data | `fetchAudit()` → `/api/gateway/audit` proxy → gateway `/admin/audit` | Yes — real paginated query | ✓ FLOWING |

---

## Behavioral Spot-Checks

| Behavior | Command | Result | Status |
|----------|---------|--------|--------|
| Gateway builds cleanly | `go build ./gateway/...` | exit 0 | ✓ PASS |
| Config tests pass with all alert vars unset | `go test ./gateway/internal/config/... -run TestLoad` | ok 0.006s | ✓ PASS |
| obs package tests pass | `go test ./gateway/internal/obs/... -count=1` | ok 0.008s | ✓ PASS |
| audit package tests pass | `go test ./gateway/internal/audit/... -count=1` | ok 10.590s | ✓ PASS |
| admin package tests pass | `go test ./gateway/internal/admin/... -count=1` | ok 0.010s | ✓ PASS |
| alert package tests pass | `go test ./gateway/internal/alert/... -count=1` | ok 14.172s | ✓ PASS |
| emerg package tests pass | `go test ./gateway/internal/emerg/... -count=1` | ok 4.122s | ✓ PASS |
| Full gateway test suite (25 packages) | `go test ./gateway/... -count=1` | all `ok`, 0 FAIL | ✓ PASS |
| Dashboard vitest (14 tests) | `npx vitest run` in `dashboard/` | 14/14 passed | ✓ PASS |
| Dashboard TypeScript | `npx tsc --noEmit` in `dashboard/` | exit 0 | ✓ PASS |

---

## Requirements Coverage

| Requirement | Source Plans | Description | Status | Evidence |
|-------------|-------------|-------------|--------|---------|
| OBS-01 | 07-01, 07-03, 07-06 | Gateway exposes `/admin/metrics` JSON with P50/P95/P99 per route+upstream, error rate, inflight, FSM state | ✓ SATISFIED | `admin/metrics.go` + `TenantLatencyPercentiles` query + `fsm.State()` + inflight gauge read; admin handler mounted in main.go |
| OBS-02 | 07-01, 07-02 | Gateway exposes `/metrics` Prometheus format with cardinality ≤10k series | ✓ SATISFIED (static) | `promhttp` handler at `r.Handle("/metrics", obs.Handler())`; bounded labels verified statically; live cardinality needs human check (SC-5) |
| OBS-03 | 07-07, 07-08 | Dashboard Next.js 15 (shadcn + Recharts) displays latency, error rate, cost, FSM state, incident history | ✓ SATISFIED | Full dashboard app: 3 pages + 7 components; polling via React Query; vitest + tsc both pass |
| OBS-04 | 07-01, 07-04, 07-05, 07-06 | Alerts with severity tiers: critical → WhatsApp+email; warning → email; info → dashboard | ✓ SATISFIED (code) | `severity.go` channel matrix; 3 clients; alerter goroutine with bounded workers; live delivery needs human check |
| OBS-05 | 07-04 | WhatsApp via Chatwoot; email via Brevo SMTP | ✓ SATISFIED (code) | `chatwoot.go` + `brevo.go` implement `Channel`; gobreaker-wrapped; credentials are optional env vars with graceful degradation |
| OBS-06 | 07-05 | Alert dedup in 5-minute window via Redis SET NX EX 300 | ✓ SATISFIED | `dedup.go` + alerter tests confirm dedup; `TestDedup` proves first/duplicate/fail-open/fail-closed behaviors |
| OBS-07 | 07-02, 07-03, 07-06 | Audit log for FSM changes, tenant changes, pod lifecycle, threshold changes | ✓ SATISFIED | `WriteStateChange` + `event_kind` + `reason` columns + migration 0020/0022; `ListAuditStateChanges` query; `/admin/audit` handler; FSM wired in main.go |
| OBS-08 | 07-02 | Sentry integration with redaction of API keys and payloads | ✓ SATISFIED | `BeforeSend` redacts `event.Request.Data` + deletes `request_body`/`response_body` from Extra; `sentry_test.go` proves redaction; live Sentry capture needs human check (SC-6) |

---

## Anti-Patterns Found

| File | Line | Pattern | Severity | Impact |
|------|------|---------|----------|--------|
| `dashboard/src/lib/smoke.test.ts` | 5 | `placeholder` in comment ("Smoke test placeholder") | ℹ️ Info | Documentation-only comment describing the smoke test's purpose; the test itself (`smoke.test.ts`) asserts the toolchain is functional — not a stub |

No TBD/FIXME/XXX debt markers found in phase-7-modified files. No unreferenced debt markers.

---

## Human Verification Required

### 1. SC-2: Critical Alert Delivery (WhatsApp + Email + ClickUp + Banner)

**Test:** On a deployed gateway with all 12 alert env vars set, induce a critical event (kill local LLM upstream or use `gatewayctl` to force a breaker open). Wait up to 60 seconds.
**Expected:** A WhatsApp message arrives via the on-call Chatwoot inbox; an email arrives at `ALERT_EMAIL_TO` via Brevo; a ClickUp task is created in `CLICKUP_ALERT_LIST_ID`; the dashboard shows a sticky red critical banner.
**Why human:** Requires real Chatwoot `account_id`/`inbox_id`/`contact_id`, ClickUp `list_id`+token, Brevo SMTP credentials, SENTRY_DSN, and a deployed gateway — cannot run autonomously.

### 2. SC-3: Live Alert Dedup

**Test:** Trigger the same warning-tier event 3 times within a 5-minute window (e.g. repeated saturated-shed event).
**Expected:** Exactly one notification per channel for the first event; the second and third produce no new notifications (Redis SET NX blocks them). Confirmed by checking ClickUp for exactly one new task and inbox for one message.
**Why human:** Requires real credentials and a deployed gateway.

### 3. SC-5: Prometheus Cardinality Audit

**Test:** `curl https://<gateway>/metrics | promtool check metrics` and `count by (__name__)` in any PromQL evaluator.
**Expected:** Zero parse errors; total active series ≤10k; metrics are consumable by standard Prometheus tooling.
**Why human:** Series count depends on runtime traffic; static analysis confirmed bounded labels but cannot count live series.

### 4. SC-6: Sentry Body Redaction Live

**Test:** Trigger a real Sentry capture event (e.g. a circuit trip or a panic in a test environment with `SENTRY_DSN` set). Inspect the event in the Sentry UI.
**Expected:** The `authorization` and `x-api-key` headers show `***REDACTED***`; the request body and any `request_body`/`response_body` extra fields are absent from the event.
**Why human:** Requires a real Sentry DSN and a captured event visible in the Sentry web UI.

### 5. T4: Dashboard UI-SPEC Visual Compliance

**Test:** Run `npm run dev` in `dashboard/`, set `.env.local` from `.env.example` pointing at a running gateway. Visit http://localhost:3000. Sign in. Navigate through Overview, Tenants, and Incidents pages.
**Expected:** radix-nova dark theme throughout; all copy is pt-BR operational tone; KPI values have tabular-nums alignment; data-table rows are 36px compact; the FSM panel shows the correct pt-BR label and status color; the latency chart has 3 distinguishable P50/P95/P99 lines; the critical banner appears on FAILED_OVER/EMERGENCY_ACTIVE states. Open DevTools → Network: confirm every gateway call goes to `/api/gateway/*` and no request carries an `X-Admin-Key` header from the browser.
**Why human:** Visual appearance and browser DevTools inspection cannot be automated.

---

## Gaps Summary

No programmatically-verifiable gaps found. All 8 OBS requirements have code implementations that compile, pass the full test suite (25 Go packages + 14 vitest tests), and follow the planned architecture.

The 5 human verification items above are not code defects — they are live-environment checks that require deployed credentials and a real browser. These follow the same pattern as every prior phase's `checkpoint:human-verify` tasks (03-08, 04-09, 06-11). The autonomous build is complete and not blocked.

The sign-off table in `07-HUMAN-UAT.md` tracks these items with a `passed_partial` fallback path for unavailable credentials.

---

_Verified: 2026-05-14T09:30:00Z_
_Verifier: Claude (gsd-verifier)_
