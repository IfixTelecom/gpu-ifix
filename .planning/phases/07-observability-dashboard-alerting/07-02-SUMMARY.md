---
phase: 07-observability-dashboard-alerting
plan: 02
subsystem: gateway-observability
tags: [go, prometheus, histogram, sentry, audit, redaction, tdd]

# Dependency graph
requires:
  - phase: 07-observability-dashboard-alerting
    plan: 01
    provides: "obs.RequestDurationByRoute + obs.RequestDurationByUpstream histograms, audit_log.event_kind migration 0020"
  - phase: 02-gateway-http-go
    provides: "obs.RequestsMiddleware (folded-TODO request instrumentation), obs.Init Sentry BeforeSend, audit.Writer async batch writer, auditctx context helpers"
  - phase: 03-resilience-failover
    provides: "auditctx.WithBillingUpstream / WithUpstreamOverride — the factual-upstream context keys the histogram label reuses"
provides:
  - "RequestDurationByRoute + RequestDurationByUpstream now Observed on every request through RequestsMiddleware (OBS-02)"
  - "obs.beforeSend — package-level testable Sentry scrub hook that redacts request bodies + drops request_body/response_body from Extra (OBS-08)"
  - "audit.Event.EventKind field + audit.Writer.WriteStateChange() helper + event_kind in the dbFlusher CopyFrom column list (OBS-07)"
affects: [07-03, 07-05, 07-06]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Histogram label reuse — obs/middleware.go resolves the upstream label the same way audit/middleware.go does (route-derived default + auditctx context override) so /metrics and audit_log label a request identically"
    - "Testable hook extraction — the inline Sentry BeforeSend closure is pulled into a package-level beforeSend function so it can be unit-tested without a live DSN; Init only wires it"
    - "Additive struct field for a nullable column — Event.EventKind defaults to \"\" which nullableString maps to SQL NULL, so per-request callers compile and write unchanged"

key-files:
  created:
    - gateway/internal/obs/sentry_test.go
  modified:
    - gateway/internal/obs/middleware.go
    - gateway/internal/obs/middleware_test.go
    - gateway/internal/obs/sentry.go
    - gateway/internal/audit/writer.go
    - gateway/internal/audit/writer_test.go

key-decisions:
  - "obs/middleware.go did NOT previously resolve an upstream (RequestsTotal only uses {route,status}). The plan's 'reuse the values the middleware already resolves' was interpreted faithfully by mirroring audit.Middleware's bounded resolution: route-derived default (llm/embed/stt) overridden by auditctx.WithBillingUpstream / WithUpstreamOverride. obs importing the zero-dependency auditctx package introduces no import cycle (auditctx imports only stdlib context)."
  - "beforeSend extracted as a package-level function rather than left inline — the plan's acceptance criteria require sentry_test.go to assert post-BeforeSend state, which is only reachable without a live DSN if the hook is callable directly."
  - "EventKind zero-value maps to SQL NULL via the existing nullableString helper (not empty string) — consistent with the nullable event_kind column from migration 0020 and the WHERE event_kind IS NOT NULL filter in the 07-01 ListAuditStateChanges query."

requirements-completed: [OBS-02, OBS-07, OBS-08]

# Metrics
duration: ~12min
completed: 2026-05-14
---

# Phase 7 Plan 02: Extend obs + audit Packages In Place Summary

**The three "EXTEND self" edits: RequestsMiddleware now Observes both Phase 7 latency histograms with single bounded labels; the Sentry BeforeSend hook (now a testable package-level `beforeSend`) redacts request bodies and drops request/response bodies from Extra; `audit.Writer` gains an `EventKind` field + `WriteStateChange()` helper writing `event_kind` through the existing async batch writer.**

## Performance

- **Duration:** ~12 min
- **Tasks:** 3 (all `tdd="true"` — RED/GREEN per task)
- **Files modified:** 6 (1 created, 5 modified)

## Accomplishments

- **Latency histograms wired (OBS-02)** — `obs.RequestsMiddleware` now computes `elapsedMs` (float64 ms) and calls `RequestDurationByRoute.WithLabelValues(route).Observe(elapsedMs)` + `RequestDurationByUpstream.WithLabelValues(upstreamLabel(r)).Observe(elapsedMs)` next to the existing `RequestsTotal` increment. The route label is the chi route template; the upstream label is resolved by the new `upstreamLabel` helper — a route-derived default (`llm`/`embed`/`stt`) overridden by `auditctx.BillingUpstreamFrom` / `UpstreamOverrideFrom`. Both labels are bounded; no tenant label (Pitfall 1).
- **Sentry body scrub (OBS-08)** — the inline `BeforeSend` closure is now the package-level `beforeSend` function; `Init` wires it via `BeforeSend: beforeSend`. It redacts `event.Request.Data` to `***REDACTED***` when non-empty and `delete`s `request_body` / `response_body` from `event.Extra`. Header redaction via `httpx.IsSensitiveKey` and `Cookies` clearing are unchanged — no sensitive-key list duplicated.
- **Audit state-change path (OBS-07)** — `Event` gains an additive `EventKind string` field; `Writer.WriteStateChange(kind, ev)` stamps `EventKind`, defaults a zero `TS` to `time.Now()`, and forwards to the existing non-blocking `Enqueue` (no second goroutine, no second channel). `event_kind` is added to the `dbFlusher.Flush` `pgx.CopyFrom` column list and per-row value slice (`nullableString(e.EventKind)` → SQL NULL for per-request rows).

## Task Commits

Each task was committed atomically (TDD: RED test + GREEN implementation folded into one commit per task, since the tests and the wiring touch the same two files):

1. **Task 1: record latency histograms in request middleware** — `aefa52b` (feat)
2. **Task 2: scrub request/response bodies in Sentry BeforeSend** — `30ce409` (feat)
3. **Task 3: audit EventKind field + WriteStateChange helper** — `deda36e` (feat)

## Files Created/Modified

- `gateway/internal/obs/middleware.go` — `RequestsMiddleware` times the request, Observes both histograms; new `upstreamLabel(r)` + `upstreamForRoute(path)` helpers resolve the bounded upstream label
- `gateway/internal/obs/middleware_test.go` — three new tests (`ObservesRouteHistogram`, `ObservesUpstreamHistogram`, `UpstreamHonorsContextOverride`) + a `histCount` HistogramVec `_count` reader helper
- `gateway/internal/obs/sentry.go` — `BeforeSend` closure extracted to package-level `beforeSend`; body + Extra scrub added
- `gateway/internal/obs/sentry_test.go` — **created** — four tests (`RedactsRequestBody`, `DropsExtraBodies`, `PreservesHeaderCookieScrub`, `NilSafe`)
- `gateway/internal/audit/writer.go` — `Event.EventKind` field; `Writer.WriteStateChange` helper; `event_kind` in the CopyFrom column list + value slice
- `gateway/internal/audit/writer_test.go` — four new tests (`WriteStateChangeSetsEventKind`, `WriteStateChangeAllKinds`, `WriteStateChangeDefaultsTS`, `EnqueueZeroEventKindAdditive`)

## Decisions Made

- **Upstream label resolution mirrors `audit.Middleware`.** `obs/middleware.go` did not previously resolve an upstream — `RequestsTotal` only uses `{route, status}`. The plan said "reuse whatever route-template and upstream values the middleware already resolves"; since no upstream resolution existed, the faithful reading was to reproduce the audit middleware's bounded resolution (`upstreamForRoute` default + `auditctx` context override). `obs` importing the zero-dependency `auditctx` package introduces no import cycle — `auditctx` imports only stdlib `context`, and nothing in `auditctx` imports `obs` (verified). This keeps `/metrics` histograms and `audit_log` rows labelling the same request with the same upstream value.
- **`beforeSend` extracted to package level.** The plan's Task 2 acceptance criteria require `sentry_test.go` to assert post-`BeforeSend` state. An inline closure inside `Init()` is only reachable through `sentry.Init` with a live DSN. Extracting it to a package-level `beforeSend(event, hint)` function — wired by `Init` as `BeforeSend: beforeSend` — makes it directly callable in tests with no DSN. Behavior is identical to the prior closure plus the new body scrub.
- **`EventKind` zero-value → SQL NULL.** `nullableString(e.EventKind)` (not a bare string) is used in the CopyFrom value slice so per-request rows write `event_kind = NULL`, consistent with the nullable `event_kind` column from migration 0020 and the `WHERE event_kind IS NOT NULL` filter in 07-01's `ListAuditStateChanges` query.

## Deviations from Plan

None — plan executed exactly as written. All three tasks followed the RED/GREEN TDD flow (RED test fails as expected, GREEN implementation passes); no auto-fixes, no blocking issues, no architectural decisions.

## Threat Surface

No new threat surface beyond the plan's `<threat_model>`. All three registered threats are mitigated as designed:

- **T-07-04 (Information Disclosure — Sentry body leak):** `beforeSend` now redacts `event.Request.Data` to `***REDACTED***` and deletes `request_body`/`response_body` from `Extra`. `sentry_test.go::TestSentryBeforeSend_RedactsRequestBody` asserts a planted `sk-live-...` secret is absent from `Request.Data` post-hook; `TestSentryBeforeSend_DropsExtraBodies` asserts both body keys are gone from `Extra`.
- **T-07-05 (Repudiation — audit trail):** `WriteStateChange` writes append-only `audit_log` rows with `event_kind` set, reusing the existing async writer — no new failure surface. Four kinds (`fsm_transition`, `tenant_activate`, `pod_lifecycle`, `threshold_change`) round-trip through the writer in `writer_test.go`.
- **T-07-06 (Information Disclosure — histogram cardinality):** `RequestDurationByRoute` and `RequestDurationByUpstream` each carry exactly one bounded label (`route` or `upstream`). `upstreamLabel` only ever returns route-derived defaults or `auditctx` context-stamped values — all bounded sets, never `tenant` or `request_id`. `TestRequestsMiddleware_ObservesRouteHistogram` documents the route-template requirement.

## Known Stubs

None — all three edits are fully wired and tested. `RequestsMiddleware` is already mounted in `cmd/gateway/main.go` (the `/v1/*` group, mounted FIRST per the HI-04 fix), so the histogram `Observe` calls are live on every request. `beforeSend` is wired into `obs.Init`. `WriteStateChange` is a public helper ready for the alerter (07-05) and tenant/pod lifecycle hooks to call — that consumer wiring is those plans' scope, not a stub.

## Next Phase Readiness

- **07-03 (admin metrics handler)** — the latency histograms are now populated, so the admin handler's per-route p50/p95/p99 panels have real data; per-tenant percentiles still come from `audit_log` SQL (07-01's `ListAuditStateChanges`), not a Prometheus label.
- **07-05 (alerter)** — `audit.Writer.WriteStateChange` exists and is tested for all four kinds; the alerter can call it directly.
- **07-06 (dashboard wiring)** — `gateway_request_duration_ms_by_{route,upstream}` series are emitted on `/metrics`.
- **No blockers.** `go build ./...` exits 0; `go vet ./internal/obs/ ./internal/audit/` clean; `go test ./internal/obs/ ./internal/audit/ -count=1 -race` green.

## Self-Check: PASSED

- Created file `gateway/internal/obs/sentry_test.go` exists on disk.
- All five modified files exist on disk.
- All three commits reachable in git history: `aefa52b`, `30ce409`, `deda36e`.
- `go build ./...` exits 0; race test suite green.

---
*Phase: 07-observability-dashboard-alerting*
*Completed: 2026-05-14*
