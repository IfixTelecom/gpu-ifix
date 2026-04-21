---
phase: 04-multi-tenant-quotas-billing-schedule-routing
plan: 08
subsystem: testing
tags:
  - phase-04
  - integration-tests
  - testcontainers
  - sc-1
  - sc-2
  - sc-3
  - sc-4
  - sc-5
  - wave-5

# Dependency graph
requires:
  - phase: 04-multi-tenant-quotas-billing-schedule-routing/04-06
    provides: "Live middleware chain (quota.RateLimitMiddleware + QuotaMiddleware, schedule.Middleware, dispatcher override branch, idempotency.WithReplay/IsReplay ctx helpers, main.go Phase 4 wiring) + obs collectors"
  - phase: 04-multi-tenant-quotas-billing-schedule-routing/04-07
    provides: "gatewayctl tenant set-mode + billing reconcile subcommands exercised by the sensitive-peak and reconcile tests"
provides:
  - "13 integration test files under gateway/internal/integration_test/ covering Phase 4 SC-1..SC-5 + D-A..D-D decision invariants against testcontainers Postgres 16 + Redis 7"
  - "Shared seedPhase4 fixture (phase4_fixtures.go) for downstream plan reuse — tenants, api-key, prices, fx, admin-key"
  - "gatewayctl-binary-from-test-helper (buildGatewayctl) that future CLI integration tests can reuse"
affects:
  - "04-09 HUMAN-UAT — foundation ready for live Sentry + chaos drills; 2 tests explicitly deferred (streaming usage-chunk end-to-end) are documented t.Skip with UAT plan pointer"

# Tech tracking
tech-stack:
  added:
    - "(none — reuses existing testcontainers-go v0.34 + go-redis v9 + pgx v5 + pgxlisten already in gateway go.mod)"
  patterns:
    - "Fixtures-helper idiom: seedPhase4(t, ctx, pool) returns a seededPhase4 struct with all identifiers; mirrors seedTenantAndKey + seedTenant from Phase 2"
    - "Hot-reload integration pattern: NOTIFY via SQL UPDATE → pgxlisten goroutine → loader.Refresh → poll snapshot with 5s deadline (mirror upstreams_listen_test.go)"
    - "gatewayctl binary build per-test via exec.Command(gobinary, build, -o, bin, ./gateway/cmd/gatewayctl) + temp dir; env injection for DSN"
    - "Lua-bucket atomicity assertion via Stripe-canonical continuous-refill math — allowed ≤ capacity + elapsed_ms × refill_rate"
    - "Dispatcher-override path test via direct auditctx.WithUpstreamOverride ctx injection + panic-proxy that fails the test if dispatched"
    - "Schedule decisions tested against a real-clock window computed relative to nowSP so no fake clock is needed — robust across any test run hour"

key-files:
  created:
    - "gateway/internal/integration_test/phase4_fixtures.go — seedPhase4 shared fixture"
    - "gateway/internal/integration_test/quota_atomic_test.go — SC-5 1000-goroutine bucket test (TestRateLimitAtomic1000Concurrent)"
    - "gateway/internal/integration_test/quota_rollover_test.go — SC-1 daily rollover in America/Sao_Paulo (TestQuotaDailyRolloverBRT)"
    - "gateway/internal/integration_test/sensitive_peak_reject_test.go — D-C1 triple-defense (TestSensitivePeakRejectGatewayctl / CheckConstraint / BootTimeInvariant)"
    - "gateway/internal/integration_test/admin_usage_test.go — SC-3 response shape (TestAdminUsageResponseShape) + D-D3 bcrypt auth (TestAdminUsageAuthBCrypt)"
    - "gateway/internal/integration_test/middleware_chain_test.go — D-D1 chain order + replay semantics (TestMiddlewareChainRateLimitBeforeQuota)"
    - "gateway/internal/integration_test/billing_partial_test.go — SC-2 final + partial paths (TestBillingFlushNonStream, TestBillingFlushPartialSource, TestBillingFlushStreamWithUsageInjection t.Skip)"
    - "gateway/internal/integration_test/billing_idempotent_replay_test.go — Pitfall 7 idempotency (TestBillingIdempotentReplay)"
    - "gateway/internal/integration_test/billing_reconcile_test.go — D-D4 gatewayctl reconcile (TestBillingReconcileDrift)"
    - "gateway/internal/integration_test/prices_hot_reload_test.go — D-B3 NOTIFY prices/fx (TestPricesHotReload, TestFXHotReload)"
    - "gateway/internal/integration_test/tenants_hot_reload_test.go — D-C4 NOTIFY tenants (TestTenantsHotReload, TestTenantsHotReloadMode)"
    - "gateway/internal/integration_test/schedule_peak_offhours_test.go — SC-4 routing decision (TestSchedulePeakOffHours, TestSchedulePeakInHours, TestSchedule24x7AlwaysLocal)"
    - "gateway/internal/integration_test/off_hours_external_down_test.go — D-C2 no-fallback-of-fallback (TestOffHoursExternalDown)"
  modified: []

key-decisions:
  - "Lua-bucket atomicity assertion rewritten as continuous-refill bounds check. The plan's 'exactly 100 allowed, 900 denied' assertion is wrong for a Stripe-canonical bucket: refill accrues at rps/1000 tokens/ms during the test wall-clock (1000 goroutines take ~80ms → ~8 extra tokens refilled). The test now asserts allowed ∈ [100, 100 + floor(elapsed_ms × 0.1) + 1]. This is the correct invariant — Redis Lua is single-threaded so any allowed count in that window proves atomicity."
  - "seedPhase4 reuses the freshSchema-seeded 'converseai' tenant rather than INSERT-ing a new one. freshSchema's TRUNCATE+reseed already guarantees the default row exists; seedPhase4 just fetches its UUID and adds 'cobrancas' (sensitive). Prevents slug-unique-violation flakes."
  - "Streaming usage-chunk end-to-end test (TestBillingFlushStreamWithUsageInjection) is explicitly t.Skip'd and deferred to Plan 04-09 HUMAN-UAT. The director injection half is unit-tested in proxy/stream_options_test.go; the interceptor extraction half is unit-tested in proxy/interceptor_usage_test.go; the DB flush half is integration-tested here by TestBillingFlushNonStream + TestBillingFlushPartialSource. The only thing NOT covered by a reliable Go test is the full SSE cold-path against a mock upstream that inspects request body for stream_options — that needs gateway_e2e wiring, which is HUMAN-UAT scope."
  - "Sensitive+peak boot-time invariant test (Path 3) uses DROP CONSTRAINT + UPDATE + (deferred restore) to create the violating row instead of the plan's SET session_replication_role='replica' approach. session_replication_role only disables triggers, not CHECK constraints; DROP+restore is the cleanest way to stage the invariant-violating state. The test defers the restore so subsequent tests see a clean schema."
  - "Off-hours-external-down test wires the production proxy.Dispatcher + breaker.Set against mock upstreams + a panic-proxy for BOTH tier-0 and tier-1. If the dispatcher ever falls through the override branch to tier-0 (which would violate D-C2), the panic-proxy fails the test immediately."
  - "Schedule middleware tests avoid fake clocks by dynamically computing windows relative to the current SP hour — (now+2h..now+4h) is guaranteed off-hours, (now-1h..now+1h) is guaranteed in-hours. This keeps the tests hermetic across any wall clock while still exercising the real clock path used in production."

patterns-established:
  - "Integration test fixtures pattern: seed helper returns a struct exposing all identifiers; downstream assertions reference the struct fields rather than re-querying by slug"
  - "Sub-process CLI integration pattern: build per-test via exec.Command + temp dir + env injection, assert on exit code + stderr contents"
  - "NOTIFY hot-reload roundtrip test skeleton: start listener goroutine → sleep 500ms for LISTEN → UPDATE via pool.Exec → poll snapshot with 5s deadline → listenCancel + listenDone drain (mirror upstreams_listen_test.go)"

requirements-completed:
  - TEN-03
  - TEN-04
  - TEN-05
  - TEN-06
  - TEN-07

# Metrics
duration: 12min
completed: 2026-04-21
---

# Phase 04 Plan 08: Integration Tests (testcontainers PG16 + Redis 7) — Phase 4 SC-1..SC-5 + D-A..D-D Invariants Summary

**13 integration test files covering all Phase 4 success criteria end-to-end against testcontainers Postgres 16 + Redis 7: 1000-concurrent Lua bucket atomicity, daily quota rollover BRT, sensitive+peak triple-defense (gatewayctl + CHECK + boot-time), SSE billing flush idempotency, NOTIFY-driven hot-reload for prices/fx/tenants, gatewayctl billing reconcile with --apply, peak off-hours dispatcher override + no-fallback-of-fallback 503 envelope.**

## Performance

- **Duration:** ~12 min (executor)
- **Started:** 2026-04-21T10:58:18Z
- **Completed:** 2026-04-21T11:10:14Z
- **Tasks:** 2 / 2 (both `type=auto`; no checkpoints)
- **Files created:** 13 (1 fixtures + 12 test files)
- **Files modified:** 0
- **Commits:** 2 atomic (test × 2) + this SUMMARY commit

## Accomplishments

- **SC-1** (rate-limit + quota enforcement under concurrent load): `TestRateLimitAtomic1000Concurrent` spawns 1000 goroutines against the live Lua bucket with `rps=100`. Allowed count is bounded by the Stripe-canonical refill math (`100 ≤ allowed ≤ 100 + elapsed_ms × refill_rate + 1`). `TestQuotaDailyRolloverBRT` confirms yesterday's over-limit row never blocks today's request.
- **SC-2** (billing_events row per request): `TestBillingFlushNonStream` + `TestBillingFlushPartialSource` cover both final and partial source paths through the async Flusher + CTE INSERT. `TestBillingIdempotentReplay` proves Pitfall 7 mitigation — triple-enqueue of the same (request_id, ts) pair results in exactly 1 billing_events row AND exactly 1 usage_counters increment.
- **SC-3** (GET /admin/usage response shape): `TestAdminUsageResponseShape` asserts every SC-3 field is present with non-zero values after seeding 3 billing_events rows; `TestAdminUsageAuthBCrypt` covers missing/invalid/valid X-Admin-Key paths.
- **SC-4** (peak off-hours routes to OpenRouter): `TestSchedulePeakOffHours` drives schedule.Middleware with a dynamic window and asserts `upstream_override="openrouter-chat"` is written to ctx. `TestSchedulePeakInHours` + `TestSchedule24x7AlwaysLocal` cover the complementary paths. `TestOffHoursExternalDown` covers the 503 envelope when the override target's breaker is OPEN.
- **SC-5** (1000 concurrent goroutines): same as SC-1 first bullet.
- **D-B3 / D-C4** (hot-reload): `TestPricesHotReload`, `TestFXHotReload`, `TestTenantsHotReload`, `TestTenantsHotReloadMode` each exercise the NOTIFY → pgxlisten → loader.Refresh roundtrip with a 5s deadline.
- **D-D4** (gatewayctl billing reconcile): `TestBillingReconcileDrift` builds the real gatewayctl binary and drives it against the testcontainer DB — drift detection without `--apply` exits 1 with DRIFT message; `--apply` rewrites usage_counters from the authoritative SUM.
- **D-C1 triple-defense**: 3 separate tests cover the gatewayctl pre-DB rejection, the CHECK constraint, and the loader's boot-time invariant check.
- **D-D1 chain order**: `TestMiddlewareChainRateLimitBeforeQuota` proves rate-limit-before-quota ordering (429 from rate-limit NEVER touches the quota checker) AND the replay-skips-rate-limit / replay-still-consumes-quota contract.
- **Full suite runtime**: 61 seconds warm against the shared testcontainers from Phase 2's setup_test.go. Cold-start adds ~5s for the first test (PG image pull cached + Redis pull cached; testcontainers reuses).

## Task Commits

1. **Task 1: SC-1/SC-3/SC-5 + sensitive+peak + middleware chain** — `9218c06` (test) — 6 files, 811 insertions
2. **Task 2: SC-2/SC-4 + hot-reload + reconcile + off-hours-block** — `7b91a06` (test) — 7 files, 856 insertions

Total diff: 13 files, 1667 insertions, 0 deletions.

## Files Created/Modified

### Created (13)

**Fixtures**
- `gateway/internal/integration_test/phase4_fixtures.go` — `seedPhase4` helper (tenants, api-key, prices, fx, admin-key) + `numericFromString/numericToFloat/bigRatToFloat` convenience helpers.

**SC-1 / SC-5**
- `gateway/internal/integration_test/quota_atomic_test.go` — `TestRateLimitAtomic1000Concurrent` (1000 goroutines vs Lua bucket with Stripe-canonical refill math).
- `gateway/internal/integration_test/quota_rollover_test.go` — `TestQuotaDailyRolloverBRT` (yesterday/today transition).

**D-C1 triple-defense**
- `gateway/internal/integration_test/sensitive_peak_reject_test.go` — 3 sub-tests (gatewayctl / CHECK constraint / boot-time invariant).

**SC-3**
- `gateway/internal/integration_test/admin_usage_test.go` — `TestAdminUsageResponseShape` + `TestAdminUsageAuthBCrypt`.

**D-D1 chain order + replay**
- `gateway/internal/integration_test/middleware_chain_test.go` — `TestMiddlewareChainRateLimitBeforeQuota`.

**SC-2 billing**
- `gateway/internal/integration_test/billing_partial_test.go` — `TestBillingFlushNonStream` + `TestBillingFlushPartialSource` + `TestBillingFlushStreamWithUsageInjection` (t.Skip → 04-09 UAT).
- `gateway/internal/integration_test/billing_idempotent_replay_test.go` — `TestBillingIdempotentReplay`.

**D-D4 reconcile**
- `gateway/internal/integration_test/billing_reconcile_test.go` — `TestBillingReconcileDrift` (gatewayctl subprocess).

**D-B3 / D-C4 hot-reload**
- `gateway/internal/integration_test/prices_hot_reload_test.go` — `TestPricesHotReload` + `TestFXHotReload`.
- `gateway/internal/integration_test/tenants_hot_reload_test.go` — `TestTenantsHotReload` + `TestTenantsHotReloadMode`.

**SC-4 schedule**
- `gateway/internal/integration_test/schedule_peak_offhours_test.go` — `TestSchedulePeakOffHours` + `TestSchedulePeakInHours` + `TestSchedule24x7AlwaysLocal`.
- `gateway/internal/integration_test/off_hours_external_down_test.go` — `TestOffHoursExternalDown`.

### Modified (0)

No production Go files were touched — this plan is purely additive integration tests.

## Public API Quick Reference (fixtures for 04-09 and future plans)

```go
// gateway/internal/integration_test
type seededPhase4 struct {
    ConverseAITenantID uuid.UUID
    CobrancasTenantID  uuid.UUID // sensitive
    ConverseAIAPIKeyID uuid.UUID
    ConverseAIAPIKey   string // raw key
    AdminKeyRaw        string // raw X-Admin-Key
}
func seedPhase4(t *testing.T, ctx context.Context, pool *pgxpool.Pool) seededPhase4
func buildGatewayctl(t *testing.T) string  // returns absolute path to compiled binary
```

## Decisions Made

See the `key-decisions` frontmatter section for the 6 decisions driving test shape (Lua refill math, seed reuse, stream t.Skip, DROP CONSTRAINT bypass, panic-proxy, dynamic windows).

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Lua bucket assertion corrected for continuous-refill semantics**
- **Found during:** Task 1 (first run of `TestRateLimitAtomic1000Concurrent`)
- **Issue:** The plan's assertion was `count(200)==100 ± 0; count(429)==900`. This is wrong for a Stripe-canonical continuous-refill bucket: the bucket accrues tokens at `rps/1000 = 0.1 tokens/ms` during test wall-clock. Test elapsed ~84ms → 8 extra tokens refilled → 106 allowed / 894 denied. Redis Lua is single-threaded; any drift within the refill window is correct, not a violation.
- **Fix:** Rewrote the assertion as continuous-refill bounds: `100 ≤ allowed ≤ 100 + floor(elapsed_ms × 0.1) + 1`. Added a `gate` channel so all goroutines start as simultaneously as possible, minimizing the drift window.
- **Files modified:** `gateway/internal/integration_test/quota_atomic_test.go`
- **Verification:** `go test -count=1 -run TestRateLimitAtomic1000Concurrent` — passes consistently with allowed ∈ [100, 108].
- **Committed in:** `9218c06` (Task 1 commit)

**2. [Rule 1 - Bug] admin_usage test used UTC-truncated midnight instead of SP-midnight**
- **Found during:** Task 1 (first run of `TestAdminUsageResponseShape`)
- **Issue:** `time.Now().In(loc).Truncate(24 * time.Hour)` does not give SP-midnight — `time.Truncate` operates in the underlying clock (typically UTC). Seeded rows at `todayMidnight+1h..3h` fell partly outside the handler's SP-day filter; only 2 of 3 rows counted.
- **Fix:** Construct SP-midnight via `time.Date(y, m, d, 0, 0, 0, 0, loc)` and place seed rows at hours 4/5/6 SP to guarantee they fall inside the today filter regardless of test start time.
- **Files modified:** `gateway/internal/integration_test/admin_usage_test.go`
- **Verification:** `go test -count=1 -run TestAdminUsageResponseShape` — passes; summary.tokens_in = 300, tokens_out = 600, requests = 3.
- **Committed in:** `9218c06` (Task 1 commit)

**3. [Rule 3 - Blocking] sensitive+peak boot-time test bypass approach**
- **Found during:** Task 1 (writing `TestSensitivePeakRejectBootTimeInvariant`)
- **Issue:** The plan proposed `SET session_replication_role = replica` to bypass the CHECK constraint. This only disables triggers, not CHECK constraints — so the UPDATE still fails.
- **Fix:** `ALTER TABLE ... DROP CONSTRAINT chk_sensitive_no_peak` → UPDATE → assert loader detects; defer restore via `UPDATE mode=24/7` + re-ADD CONSTRAINT. The defer runs even on t.Fatal, keeping subsequent tests clean.
- **Files modified:** `gateway/internal/integration_test/sensitive_peak_reject_test.go`
- **Verification:** `go test -count=1 -run TestSensitivePeakReject` — all 3 sub-tests pass.
- **Committed in:** `9218c06` (Task 1 commit)

**4. [Rule 3 - Blocking] Plan-specified package `integration_test` vs actual `integration`**
- **Found during:** Task 1 (writing phase4_fixtures.go)
- **Issue:** The plan's file headers use `package integration_test`. The actual package in the existing Phase 2/3 tests is `package integration` (see `setup_test.go` line 3). Mixed packages in the same directory would not build.
- **Fix:** All new test files use `package integration` matching the established convention.
- **Files modified:** all 13 new files.
- **Committed in:** `9218c06` + `7b91a06`.

**5. [Rule 3 - Blocking] Plan-specified helpers already existed under different names**
- **Found during:** Task 1 (writing quota_atomic_test.go — plan referenced an un-defined `injectAuthContext(tenantID, h)` helper)
- **Issue:** The plan's snippet assumed a helper taking `uuid.UUID`. Phase 2 already established `injectAuthWithID(next, tenantID_string, dc)` in idempotency_flow_test.go. Adding a third helper would be redundant.
- **Fix:** Used the existing `injectAuthWithID(chain, seed.ConverseAITenantID.String(), auth.DataClassNormal)` helper.
- **Files modified:** all tests that needed auth injection (quota_atomic_test.go, middleware_chain_test.go, schedule_peak_offhours_test.go, off_hours_external_down_test.go).
- **Committed in:** `9218c06` + `7b91a06`.

### Rule 4 Items

None — no architectural changes proposed.

### Out-of-Scope Findings (Not Fixed)

- **Pre-existing gofmt drift** in `gateway/internal/proxy/toolcall_test.go` and `gateway/internal/proxy/sensitive_block_test.go` (observed by `gofmt -l ./internal/integration_test/...`). Documented as out-of-scope in 04-05 + 04-06 summaries; left untouched per scope boundary. These files are NOT in this plan's `files_modified` list.

### Authentication Gates

None — all changes are test code + shared container helpers. No new env vars; testcontainers-go auto-provisions PG16 + Redis 7 images.

## Threat Flags

No new threat surface beyond the plan's register (T-04-31, T-04-32). Both accepted-or-mitigated per the plan:

- T-04-31 (testcontainer cold-start DoS): mitigated — per-test `freshSchema` is cheap (~50ms); shared containers across tests; full suite 61s warm.
- T-04-32 (CHECK bypass in test): mitigated — only `TestSensitivePeakRejectBootTimeInvariant` bypasses the CHECK, and it does so via DROP+UPDATE+deferred-restore in an ephemeral testcontainer DB. Production DB role gates this.

## Next Phase Readiness

- **Plan 04-09 (HUMAN-UAT):** foundation complete. All 5 Phase 4 success criteria have automated integration coverage. The one explicitly deferred test (`TestBillingFlushStreamWithUsageInjection`) is t.Skip'd and documented; 04-09 adds the live Sentry-breadcrumb inspection + chaos-drill UAT that completes the end-to-end picture.
- **All 5 TEN-03..TEN-07 requirements:** exercised by at least one test in this plan. Combined with 04-04 + 04-05 + 04-06 summaries, they represent the full Phase 4 surface.
- **Future plans (Phase 5+):** the `seedPhase4` + `buildGatewayctl` helpers are reusable. `seedPhase4` is minimal (only what Phase 4 needs); extend in-place (or introduce `seedPhase5` alongside) for later phases.

## Self-Check: PASSED

### File existence

- FOUND: gateway/internal/integration_test/phase4_fixtures.go
- FOUND: gateway/internal/integration_test/quota_atomic_test.go
- FOUND: gateway/internal/integration_test/quota_rollover_test.go
- FOUND: gateway/internal/integration_test/sensitive_peak_reject_test.go
- FOUND: gateway/internal/integration_test/billing_partial_test.go
- FOUND: gateway/internal/integration_test/billing_idempotent_replay_test.go
- FOUND: gateway/internal/integration_test/billing_reconcile_test.go
- FOUND: gateway/internal/integration_test/prices_hot_reload_test.go
- FOUND: gateway/internal/integration_test/tenants_hot_reload_test.go
- FOUND: gateway/internal/integration_test/admin_usage_test.go
- FOUND: gateway/internal/integration_test/middleware_chain_test.go
- FOUND: gateway/internal/integration_test/schedule_peak_offhours_test.go
- FOUND: gateway/internal/integration_test/off_hours_external_down_test.go

### Commit existence

- FOUND: 9218c06 test(04-08): Phase 4 SC-1/SC-3/SC-5 + sensitive+peak triple-defense + middleware chain
- FOUND: 7b91a06 test(04-08): Phase 4 SC-2/SC-4 + hot-reload + reconcile + off-hours-block

### Acceptance greps (spot check)

- `grep -c "func TestRateLimitAtomic1000Concurrent\|func TestQuotaDailyRolloverBRT\|func TestSensitivePeakReject\|func TestAdminUsage\|func TestMiddlewareChain" gateway/internal/integration_test/*.go` → 8 hits (6 func decl + 2 sub-test references; spot ok)
- `grep -c "source='partial'\|\"partial\"" gateway/internal/integration_test/billing_partial_test.go` → 3 (contract + field string in struct + assertion)
- `grep -c "CheckSensitivePeakInvariant" gateway/internal/integration_test/sensitive_peak_reject_test.go` → 1 (direct method call)
- `grep -c "ListenAndReload" gateway/internal/integration_test/prices_hot_reload_test.go gateway/internal/integration_test/tenants_hot_reload_test.go` → 4 (2 per file, prices+fx and tenants+mode test pairs)
- `go test -tags integration -count=1 -timeout 600s ./internal/integration_test/...` → `ok ... 60.940s` (full suite green)

---

*Phase: 04-multi-tenant-quotas-billing-schedule-routing*
*Plan: 08 (Wave 5 — solo integration tests)*
*Completed: 2026-04-21*
