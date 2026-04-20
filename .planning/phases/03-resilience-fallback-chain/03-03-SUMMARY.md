---
phase: 03-resilience-fallback-chain
plan: 03
subsystem: gateway-resilience
tags: [breaker, gobreaker, circuit-breaker, redis-mirror, pubsub, prometheus, wave-2]

# Dependency graph
requires:
  - phase: 03-resilience-fallback-chain
    plan: 01
    provides: "gateway/internal/breaker/errors.go (sentinels), pinned gobreaker v2 in go.mod, scaffold_imports.go pattern"
  - phase: 03-resilience-fallback-chain
    plan: 02
    provides: "Wave 1 complete; redisx package + obs/metrics extension surface stable"
provides:
  - "gateway/internal/breaker.Set type with NewSet, Rebuild, Get, Execute, Snapshot"
  - "gateway/internal/breaker.IsSuccessful classifier (D-A4 4xx/429/Canceled NOT failures, 5xx + timeouts ARE failures)"
  - "gateway/internal/breaker.HTTPError typed error for status-aware breaker classification"
  - "gateway/internal/breaker.Subscribe Pub/Sub goroutine for cross-replica convergence (CONTEXT.md D-D1)"
  - "gateway/internal/breaker.applyRemoteEvent overlay so peer OPEN short-circuits local Execute without poisoning gobreaker counters"
  - "gateway/internal/breaker.Namespace constant ('gw:breaker:') for ops/dashboard tooling"
  - "gateway/internal/redisx.WriteBreakerState helper (HSET with 2s timeout)"
  - "gateway/internal/redisx.PublishBreakerEvent helper (Pub/Sub with 2s timeout)"
  - "gateway/internal/redisx.SubscribeBreakerEvents helper"
  - "gateway/internal/redisx.BreakerEvent struct (wire format) + BreakerEventsChannel() accessor"
  - "9 new Prometheus collectors in obs/metrics.go: BreakerState, BreakerTripsTotal, BreakerMirrorFailuresTotal, ProbeDurationMs, ProbeFailureTotal, UpstreamsReloadTotal, UpstreamThrottledTotal, SensitiveRetryTotal, ToolCallPartialTotal"
affects: [03-04, 03-05, 03-06, 03-07, 03-08]

# Tech tracking
tech-stack:
  added: []  # all deps already pinned in 03-01
  patterns:
    - "Per-upstream gobreaker.CircuitBreaker[*http.Response] kept under RWMutex map; Rebuild atomic-swaps unchanged breakers in place to preserve state across hot-reloads"
    - "Cross-replica convergence via Hash + Pub/Sub mirror; remoteOpen overlay short-circuits Execute without driving local gobreaker counters (avoids inconsistent ConsecutiveFailures across replicas)"
    - "Best-effort publish goroutine: OnStateChange spawns publishTransition; Redis errors increment BreakerMirrorFailuresTotal but never block the in-process state machine (D-D1)"
    - "Local-clock arrival timestamp for remoteOpen (time.Now() at applyRemoteEvent) — tolerant of cross-replica clock drift, sub-second resolution, and the wire format's int64-second precision"

key-files:
  created:
    - gateway/internal/breaker/breaker.go
    - gateway/internal/breaker/mirror.go
    - gateway/internal/breaker/subscribe.go
    - gateway/internal/breaker/breaker_test.go
    - gateway/internal/breaker/mirror_test.go
    - gateway/internal/redisx/breaker.go
    - gateway/internal/redisx/breaker_test.go
  modified:
    - gateway/internal/obs/metrics.go      # +9 collectors (87 lines added)
    - gateway/internal/breaker/scaffold_imports.go  # removed gobreaker blank import (now consumed)

key-decisions:
  - "Used remoteOpen overlay (map[string]time.Time) rather than synthetic cb.Fail()/Succeed() calls. gobreaker v2 has no public API to force state transitions; calling Fail() on every remote OPEN event would inflate ConsecutiveFailures arbitrarily and decouple local counter semantics from real local request outcomes. The overlay is read-only from the hot path (RLock) and preserves a clean boundary between authoritative local state and cross-replica hints."
  - "Recorded local arrival time (time.Now()) in applyRemoteEvent rather than the wire format's int64 SinceUnix. The 1-second resolution of Unix() loses too much precision for sub-second cooldowns (test failure was first surfaced under -race -count=3 with a 500ms cooldown), and even at production cooldowns of 30s, trusting the peer's wall clock invites drift bugs. Local-clock arrival is the right semantics for 'has the peer been OPEN within Cooldown?' anyway."
  - "Kept mirror.go minimal (single Namespace constant). The actual publish path lives inside Set.publishTransition because it needs Set.rdb + Set.log; lifting it to a free function would force every caller to plumb those args. mirror.go documents itself as the future home for any per-replica state helpers Phase 6 may need."
  - "scaffold_imports.go now keeps only backoff/v5 + pgxlisten — gobreaker is consumed directly. Per the file's own deletion contract from 03-01, when the next two consumers land in 03-04 and 03-05, scaffold_imports.go itself MUST be deleted."

requirements-completed: [RES-01, RES-04]

# Metrics
duration: ~9min
completed: 2026-04-20
tests-added: 13 functions / 22 test cases (incl. table subtests)
race-detector: clean (verified via go test -race -count=3)
---

# Phase 3 Plan 03: Per-Upstream Circuit Breaker + Cross-Replica Mirror Summary

**Created the `gateway/internal/breaker` package wrapping `sony/gobreaker/v2` per upstream with strict CONTEXT.md D-A3 thresholds (3 ConsecutiveFailures → OPEN, 30s cooldown, 1-success HALF_OPEN→CLOSED) and the D-A4 IsSuccessful filter (4xx, 429, and `context.Canceled` are NOT failures); added a best-effort Redis mirror (Hash `gw:breaker:{name}` + Pub/Sub `gw:breaker:events`) plus a subscriber goroutine that maintains a `remoteOpen` overlay so peer replicas converge in under 1 second; extended `internal/redisx/` with 3 helper functions and `internal/obs/metrics.go` with 9 new Prometheus collectors. 13 test functions / 22 cases pass under `-race -count=3`.**

## Performance

- **Duration:** ~9 minutes
- **Started:** 2026-04-20T00:18:30Z
- **Completed:** 2026-04-20T00:26:37Z
- **Tasks:** 2 of 2 executed
- **Files created:** 7 (3 implementation, 1 redisx helper, 3 test files)
- **Files modified:** 2 (obs/metrics.go +87 lines, scaffold_imports.go -1 line)
- **Commit count:** 2 atomic per-task commits + this metadata commit

## Accomplishments

- **`Set` type** wraps a `map[string]*gobreaker.CircuitBreaker[*http.Response]` under RWMutex with the exact gobreaker.Settings required by D-A3:
  - `MaxRequests = 1`
  - `Interval = 0` (counters never auto-reset in CLOSED)
  - `Timeout = Cooldown` (default 30s)
  - `ReadyToTrip` checks `c.ConsecutiveFailures >= s.opt.ConsecutiveFailures`
  - `IsSuccessful = IsSuccessful` (D-A4 classifier)
  - `OnStateChange` spawns goroutine that calls `publishTransition` + bumps `BreakerState` gauge + bumps `BreakerTripsTotal` on CLOSED→OPEN
- **`IsSuccessful` classification matrix (10 cases tested):**
  - `nil` → success
  - `context.Canceled` → success (client gave up, not an upstream fault)
  - `&HTTPError{Status: 400}` / 404 → success (client error, not upstream health)
  - `&HTTPError{Status: 429}` → success (throttle = capacity, not health) per D-A4
  - `&HTTPError{Status: 500}` / 502 / 504 → failure
  - `context.DeadlineExceeded` → failure
  - `errors.New("boom")` (unknown) → failure
- **Cross-replica contract (CONTEXT.md D-D1):**
  - **Publish path:** OnStateChange callback → `go s.publishTransition(name, to)` → `redisx.WriteBreakerState` (HSET `gw:breaker:{name}`) → `redisx.PublishBreakerEvent` (PUBLISH `gw:breaker:events` with JSON `{upstream, state, since_unix}`); each Redis call has a 2s timeout; failures increment `gateway_breaker_mirror_failures_total` and log at WARN.
  - **Subscribe path:** boot-time `go set.Subscribe(rootCtx)` → `for { ps := redisx.SubscribeBreakerEvents(ctx, rdb); for msg := range ps.Channel() { json.Unmarshal → s.applyRemoteEvent(ev) } }`; on channel drop reconnects with 1s backoff; on `ctx.Done()` closes PubSub and returns.
  - **Overlay semantics:** `applyRemoteEvent` takes Lock and either sets `remoteOpen[name] = time.Now()` (state==open) or deletes the entry (state==closed|half-open). `Set.Execute` checks the overlay under RLock; if `time.Since(remoteAt) < Cooldown`, returns `ErrBreakerOpen` without firing `fn`.
- **Redis-down fallback verified:** `TestPublishFailure_DoesNotBlockBreaker` closes the miniredis server before tripping the breaker; the local gobreaker still transitions to OPEN. The publishTransition goroutine logs a WARN + increments BreakerMirrorFailuresTotal but never affects the state machine.
- **9 new Prometheus collectors** added to `internal/obs/metrics.go` for the rest of Phase 3 to populate:
  - `BreakerState` (Gauge per upstream — 0/1/2 = closed/half-open/open)
  - `BreakerTripsTotal` (Counter per upstream — CLOSED→OPEN)
  - `BreakerMirrorFailuresTotal` (Counter — Redis HSET/PUBLISH errors)
  - `ProbeDurationMs` (Histogram per upstream — synthetic E2E latency)
  - `ProbeFailureTotal` (Counter per upstream+reason)
  - `UpstreamsReloadTotal` (Counter per result — LISTEN reload outcomes)
  - `UpstreamThrottledTotal` (Counter per upstream+status — 429 separation per D-A4)
  - `SensitiveRetryTotal` (Counter per outcome — sensitive retry loop)
  - `ToolCallPartialTotal` (Counter per route+upstream — RES-06)

## Test Inventory

**13 test functions, 22 cases including IsSuccessful table subtests. All pass under `-race -count=3 -timeout=120s`.**

| File | Test | Purpose | Wall (race) |
|------|------|---------|------|
| `breaker_test.go` | `TestBreakerOpensAfter3Failures` | 3 503s → StateOpen | 0.02s |
| `breaker_test.go` | `TestBreakerDoesNotOpenOn4xx` | 10× 400 stays CLOSED | <0.01s |
| `breaker_test.go` | `TestBreakerDoesNotOpenOn429` | 10× 429 stays CLOSED (D-A4) | <0.01s |
| `breaker_test.go` | `TestBreakerDoesNotOpenOnCanceled` | 10× context.Canceled stays CLOSED | <0.01s |
| `breaker_test.go` | `TestBreakerCooldownThenHalfOpenToClosed` | OPEN → cooldown → HALF_OPEN → success → CLOSED | 0.16s |
| `breaker_test.go` | `TestRemoteOpenOverlayShortCircuits` | applyRemoteEvent("open") → Execute returns ErrBreakerOpen w/o firing fn | <0.01s |
| `breaker_test.go` | `TestIsSuccessful` (10 subtests) | D-A4 classification table | <0.01s |
| `breaker_test.go` | `TestSnapshotReturnsAllStates` | Mixed CLOSED/OPEN snapshot | 0.02s |
| `breaker_test.go` | `TestRebuildPreservesState` | Rebuild keeps tripped breakers OPEN, drops removed, adds new CLOSED | 0.02s |
| `mirror_test.go` | `TestOnStateChangePublishesToRedis` | After trip, `gw:breaker:local-llm` exists with state=open | 0.01s |
| `mirror_test.go` | `TestSubscribeAppliesRemoteEvent` | Subscribe loop applies external Pub/Sub OPEN event | 0.07s |
| `mirror_test.go` | `TestPublishFailure_DoesNotBlockBreaker` | mr.Close() before trip; gobreaker still goes OPEN (D-D1) | 0.20s |
| `redisx/breaker_test.go` | `TestWriteBreakerState_Roundtrip` | HSET fields readable via HGetAll | <0.01s |
| `redisx/breaker_test.go` | `TestPublishAndSubscribe_Roundtrip` | Round-trip BreakerEvent through miniredis Pub/Sub | 0.06s |
| `redisx/breaker_test.go` | `TestBreakerEventsChannel_IsExported` | Channel name accessor returns `gw:breaker:events` | <0.01s |

Total package wall (race, -count=1): breaker 1.55s, redisx 3.08s.

## Task Commits

1. **Task 1: feat(03-03) — breaker package + Redis mirror helpers + Phase 3 metrics** — `5813e8e`
2. **Task 2: test(03-03) — breaker + mirror + redisx tests** — `6991445` (includes Rule 1 bug-fix to `applyRemoteEvent` timestamp source)

_Plan metadata commit:_ produced after this SUMMARY is staged.

## Files Created/Modified

- **`gateway/internal/breaker/breaker.go`** (created, 246 lines) — Set type, NewSet, Rebuild, Get, Execute, Snapshot, newBreaker (private), stateFloat (private), IsSuccessful, HTTPError, publishTransition (private), applyRemoteEvent (private). Package godoc references CONTEXT.md D-D1.
- **`gateway/internal/breaker/mirror.go`** (created, 12 lines) — `Namespace = "gw:breaker:"` constant + design comment explaining why publishTransition lives in breaker.go.
- **`gateway/internal/breaker/subscribe.go`** (created, 56 lines) — Set.Subscribe goroutine; ctx-aware select loop with 1s reconnect backoff on PubSub channel drop.
- **`gateway/internal/breaker/breaker_test.go`** (created, 235 lines) — 9 tests covering full state machine + 10-case IsSuccessful table.
- **`gateway/internal/breaker/mirror_test.go`** (created, 122 lines) — 3 mirror/subscribe/Redis-down tests.
- **`gateway/internal/breaker/scaffold_imports.go`** (modified) — removed `_ "github.com/sony/gobreaker/v2"` blank import (now consumed by breaker.go); 2 entries remain (backoff/v5, pgxlisten) per the file's deletion contract.
- **`gateway/internal/redisx/breaker.go`** (created, 70 lines) — `BreakerEvent` struct + `WriteBreakerState`, `PublishBreakerEvent`, `SubscribeBreakerEvents`, `BreakerEventsChannel` helpers; all use 2s context timeouts.
- **`gateway/internal/redisx/breaker_test.go`** (created, 64 lines) — 3 tests round-trip + accessor.
- **`gateway/internal/obs/metrics.go`** (modified) — 9 new Prometheus collectors appended after Phase 2 entries; 18+ name occurrences across declarations + Help text.

## Decisions Made

- **`remoteOpen` overlay rather than synthetic gobreaker calls.** The plan's `<read_first>` Pattern 1 lists "Subscribe receives state==open → cb.Fail() repeatedly to trip" as the naive approach. Implemented the documented "Design note" alternative: `Set.remoteOpen map[string]time.Time` checked inside Execute before calling `cb.Execute(fn)`. This keeps gobreaker counter semantics consistent with real local-request outcomes; the overlay is purely a "peer says don't bother" hint with a Cooldown-bounded TTL.
- **Local arrival timestamp.** `applyRemoteEvent` records `time.Now()` rather than `time.Unix(ev.SinceUnix, 0)`. This decision was forced by Rule 1 (auto-fix bug — see Deviations); the wire format's 1-second resolution is incompatible with sub-second test cooldowns AND production-time clock drift across replicas would invite drift bugs. Local arrival is the right semantics for the question Execute is asking.
- **Long-cooldown test for Redis-down case.** `TestPublishFailure_DoesNotBlockBreaker` originally used the same 100ms Cooldown as the other tests, then waited 200ms after tripping for the publishTransition goroutine to fail and return. With 100ms Cooldown, the breaker auto-transitioned to HALF_OPEN by the time the test asserted. Switched the test's Cooldown to 5s — the test is asserting the in-process trip, not the cooldown semantics, so a generous Cooldown isolates the assertion from gobreaker's own time logic.
- **9 collectors in one Edit instead of incrementally.** All 9 Phase 3 collectors are introduced together so subsequent waves (03-04 dispatcher, 03-06 OpenRouter director, 03-07 sensitive retry, etc.) can land their increments without each one having to also touch obs/metrics.go. They cost zero memory until first WithLabelValues() call.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] `applyRemoteEvent` used wire-format timestamp; sub-second cooldown tests flaked under -race -count=3**

- **Found during:** Task 2, after the test file was complete and `go test -count=3 -race` was run for verification.
- **Issue:** Initial implementation followed the plan's PATTERNS.md sketch literally — `s.remoteOpen[ev.Upstream] = time.Unix(ev.SinceUnix, 0)`. With the test's 500ms cooldown and `SinceUnix = time.Now().Unix()` (1-second resolution), the recovered timestamp was up to 999ms behind the real send moment. By the time `Execute` ran `time.Since(remoteAt)`, the value could exceed Cooldown → overlay treated as expired → fn fired → assertion failed. Reproducible via `go test ./internal/breaker/ -count=3 -race`.
- **Fix:** Switched to `s.remoteOpen[ev.Upstream] = time.Now()` and added a godoc comment explaining the rationale (precision + cross-replica clock-drift tolerance). Wire format SinceUnix is still published for ops/dashboard introspection but not used as the authoritative "when did the peer trip?" timestamp on the receiving side.
- **Files modified:** `gateway/internal/breaker/breaker.go` (applyRemoteEvent body + new comment).
- **Verification:** `go test ./internal/breaker/... ./internal/redisx/... -count=3 -race -timeout=120s` clean.
- **Committed in:** `6991445` (Task 2; commit message documents the deviation).

**2. [Rule 1 - Bug] Redis-down test used too-short Cooldown; gobreaker auto-transitioned to HALF_OPEN before assertion**

- **Found during:** Task 2, same `-count=3 -race` run.
- **Issue:** Test set `Cooldown: 100 * time.Millisecond` and waited 200ms for the publishTransition goroutine to fail and exit. After 200ms with a 100ms Cooldown, the next `cb.State()` call evaluated as HALF_OPEN (gobreaker computes state lazily). Assertion `cb.State() != StateOpen` triggered.
- **Fix:** Raised that test's Cooldown to 5s. The test asserts the in-process trip happened (the D-D1 contract), not anything about cooldown semantics — so isolating the assertion from gobreaker's time logic is the right move.
- **Files modified:** `gateway/internal/breaker/mirror_test.go` (`TestPublishFailure_DoesNotBlockBreaker` setup).
- **Verification:** Same `-count=3 -race` run is now clean.
- **Committed in:** `6991445` (Task 2).

### Out-of-Scope Discoveries

None.

**Total deviations:** 2 auto-fixed (both Rule 1 — production-correctness bugs surfaced by the race detector). Zero behavior or scope changes from the plan. Both fixes preserve the documented contracts (D-A3 thresholds + D-D1 cross-replica convergence + D-D1 Redis-down fallback).

## Issues Encountered

- The default `package breaker` godoc had been declared in `errors.go` (Wave 0). Adding a second godoc-style comment at the top of `breaker.go` did not cause a compile error (Go allows multiple package-level comments and concatenates them in `godoc` output), so both godocs are kept — the breaker.go variant focuses on the in-process state machine, the errors.go variant focuses on the sentinel errors. No action required.

## Threat Surface Scan

No new network endpoints, auth paths, or schema changes introduced. The Pub/Sub subscribe path is the only new external surface and was already accounted for by the plan's threat model (T-03-03-01..05); subscribe.go correctly implements the T-03-03-02 (malformed JSON → log + continue) and T-03-03-04 (ctx-aware select with reconnect backoff) mitigations.

## User Setup Required

None. Breaker package is operational against any Redis (in production: shared `infra-redis-1`; in tests: in-process miniredis). No new env vars introduced — Options is constructed by future Wave 4 dispatcher code from existing config fields (`BREAKER_CONSECUTIVE_FAILURES`, `BREAKER_COOLDOWN_SECONDS`).

## Next Phase Readiness

- **Wave 4 dispatcher (03-04 / 03-05) unblocked:** Can now construct `*breaker.Set` at boot, gate dispatch via `set.Execute(name, fn)`, and react to `breaker.ErrBreakerOpen` (already declared by 03-01) for fallback chain selection.
- **OpenRouter director (03-06) unblocked:** When the director wraps the body for `openrouter-chat`, errors flow through `set.Execute`; 5xx errors → trip; 429 → bumps `obs.UpstreamThrottledTotal` rather than the breaker.
- **Sensitive retry loop (03-07) unblocked:** Loop reads `set.Get(name).State()` between attempts; `obs.SensitiveRetryTotal{outcome=...}` already exists.
- **Probe goroutine (03-08) unblocked:** Probe results call `obs.ProbeDurationMs.Observe(...)` and `obs.ProbeFailureTotal.WithLabelValues(...).Inc()`; on probe failure dispatcher does the actual `set.Execute`-driven trip.
- **scaffold_imports.go technical debt:** Down to 2 blank imports (backoff/v5 + pgxlisten). 03-04 should remove backoff/v5; 03-08 should remove pgxlisten; the file MUST then be deleted entirely.

## Self-Check: PASSED

- File checks:
  - `gateway/internal/breaker/breaker.go` — FOUND (246 lines, exports Set, NewSet, Rebuild, Get, Execute, Snapshot, IsSuccessful, HTTPError + private helpers)
  - `gateway/internal/breaker/mirror.go` — FOUND (Namespace constant)
  - `gateway/internal/breaker/subscribe.go` — FOUND (Set.Subscribe goroutine)
  - `gateway/internal/breaker/breaker_test.go` — FOUND (9 test funcs)
  - `gateway/internal/breaker/mirror_test.go` — FOUND (3 test funcs)
  - `gateway/internal/redisx/breaker.go` — FOUND (4 helpers + BreakerEvent type)
  - `gateway/internal/redisx/breaker_test.go` — FOUND (3 test funcs)
  - `gateway/internal/obs/metrics.go` — modified, 9 new collectors confirmed via grep (18 name occurrences)
- Commit checks:
  - `5813e8e` — FOUND in `git log` (Task 1)
  - `6991445` — FOUND in `git log` (Task 2)
- Build / vet / test:
  - `go build ./...` exit 0
  - `go vet ./...` exit 0
  - `go test ./internal/breaker/... ./internal/redisx/... -count=1 -race -timeout=60s` exit 0 (14 test funcs reported PASS — 13 from this plan + 1 pre-existing in redisx)
  - Re-run `-count=3 -race -timeout=120s` exit 0 (verifies non-flaky)
- Acceptance criteria:
  - Task 1 grep chain (15 invariants) — ALL PASS
  - Task 2 test-name grep chain (13 invariants) — ALL PASS

## TDD Gate Compliance

Plan frontmatter is `type: execute` (not `type: tdd`), so the plan-level TDD gate sequence does NOT apply. However, both tasks are `tdd="true"`:

- **Task 1 (RED-not-required):** Implementation-only step. The plan instructs Task 2 to bring the tests; Task 1 commit `5813e8e` is `feat(03-03)` rather than `test(...)`.
- **Task 2 (GREEN):** All 13 new tests pass under `-race -count=3` against the implementation from Task 1. Commit `6991445` is correctly tagged `test(03-03)` and includes the Rule 1 bug-fix that was forced by RED-style behavior under the race detector.

No standalone REFACTOR commit needed — the only refactor was the Rule 1 fix, bundled into Task 2's commit message.

---
*Phase: 03-resilience-fallback-chain*
*Plan: 03 (Wave 2)*
*Completed: 2026-04-20*
