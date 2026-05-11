---
phase: 05-load-shedding-saturation-aware-routing
plan: 03
subsystem: api
tags: [go, atomic, fsm, ring-buffer, unit-test, prometheus, lockless]

requires:
  - phase: 05-01
    provides: shed errors.go sentinels + obs.GatewayShedState/Transitions collectors + cfg.ShedLatencyRingSize/ShedTickIntervalMs envs
provides:
  - "shed.LatencyRing: lockless P95 ring buffer (atomic.StoreUint32 writes + atomic.LoadUint32 snapshots)"
  - "shed.InflightRegistry: per-(upstream, tenant) atomic.Int64 counters with populate-once RWMutex"
  - "shed.FSM: 4-state lockless state machine (atomic.Int32 + atomic.Pointer[Config]) with CAS transition + onChange callback"
  - "shed.Set: per-upstream FSM registry mirroring breaker.Set pattern (Rebuild + Get + ForEach + RemoteState overlay)"
  - "35 unit tests covering D-C1 transition table + 2-of-3 saturation gate + Rebuild hot-reload invariant"
affects: [05-04 mirror.go subscribe.go, 05-05 tick.go dcgm scraper, 05-06 middleware, 05-07 gatewayctl, 06+ multi-replica]

tech-stack:
  added: []
  patterns:
    - "Lockless hot path: atomic.Int32 state + atomic.Int64 EnteredAt + atomic.Pointer[Config] hot-reload"
    - "Populate-once RWMutex: tenant counter map insert is locked; Inc/Dec on the *atomic.Int64 is lockless"
    - "Race-benign-via-atomic: LatencyRing slot writes use atomic.StoreUint32 so go test -race is clean even though collisions still lose samples"
    - "Set.fsmRoot vs Set.log split: avoid double module=… attribute when FSM logger derives from Set logger"
    - "CAS-then-side-effect: obs metrics + onChange callback fire AFTER CompareAndSwap succeeds (failed transitions leak nothing)"
    - "Synthetic Transition() method exposed for gatewayctl override (Plan 05-07) and remote-event consumer (Plan 05-04)"

key-files:
  created:
    - gateway/internal/shed/latency.go
    - gateway/internal/shed/latency_test.go
    - gateway/internal/shed/inflight.go
    - gateway/internal/shed/inflight_test.go
    - gateway/internal/shed/fsm.go
    - gateway/internal/shed/fsm_test.go
    - gateway/internal/shed/set.go
    - gateway/internal/shed/set_test.go
  modified: []

key-decisions:
  - "atomic.StoreUint32 / atomic.LoadUint32 on LatencyRing buf slots so -race is clean while preserving lockless semantics (slot collisions still lose samples per D-A2, but the race detector no longer flags them)"
  - "Set.fsmRoot field passes raw slog.Logger (no module attr) to NewFSM so transition logs carry module=SHED_FSM only, not module=SHED module=SHED_FSM"
  - "InflightRegistry.Inc on unknown upstream is silent no-op (defensive against middleware bugs); explicit Upstreams() / TenantsForUpstream() snapshot accessors for the tick goroutine"
  - "FSM Transition() exposes synthetic state changes (gatewayctl shed-force + subscribe remote events) without going through Evaluate; transition() filters same-state calls so the API is safe even when current==new"
  - "Defaults: Options{} with zero DefaultArmSeconds/DefaultRecoverSeconds falls back to 30s/60s D-C1 strict; Config.Upstream is invariant across UpdateConfig (D-C5 hot-reload)"

patterns-established:
  - "Lockless FSM pattern: atomic.Int32 state + CAS transition + side-effects-after-CAS — replicable for any future state machine"
  - "Set/Rebuild idempotent symmetric difference: drop removed, preserve unchanged (same FSM pointer), construct new — matches breaker.Set exactly"
  - "Race-benign documentation via atomic.Store: when the semantic race is acceptable (one sample lost), use atomic.Store instead of mutex to keep go test -race clean"

requirements-completed: [LSH-01, LSH-02, LSH-03]

duration: 22min
completed: 2026-05-11
---

# Phase 5 Plan 03: shed FSM + LatencyRing + InflightRegistry Summary

**4-state lockless shedding FSM, atomic-store ring buffer for P95, and per-(upstream, tenant) atomic Int64 counters — 8 files, 35 -race-clean tests, zero external deps beyond sync/atomic + obs collectors registered in 05-01.**

## Performance

- **Duration:** ~22 min (2026-05-11T21:16Z → 2026-05-11T21:38Z)
- **Started:** 2026-05-11T21:16:00Z
- **Completed:** 2026-05-11T21:38:00Z
- **Tasks:** 2 of 2 complete (both TDD: RED → GREEN, no REFACTOR needed)
- **Files created:** 8 (4 prod + 4 tests)
- **Files modified:** 0 (all new files inside gateway/internal/shed/)
- **LOC added:** ~1,282 net (4 prod = 709 LOC; 4 tests = 573 LOC)

## Accomplishments

- **LatencyRing**: lockless ring buffer with `atomic.StoreUint32` writes + `atomic.LoadUint32` snapshots. P95 computed by snapshot-copy + sort.Slice. Race-benign-by-design (D-A2): slot collisions lose one sample but go test -race is clean.
- **InflightRegistry**: per-upstream atomic.Int64 (global) + per-(upstream, tenant) atomic.Int64 (per-tenant cap). RWMutex only on first-Inc-per-tenant populate. Exposes `Upstreams()` + `TenantsForUpstream(name)` for the tick goroutine (Plan 05-05) to publish gateway_inflight_tenant gauges.
- **FSM**: 4-state shedding state machine (Off / Armed / On / Recovering) with atomic.Int32 state + atomic.Int64 EnteredAt + atomic.Pointer[Config] for hot-reload. Lockless `State()` hot-path read; CAS transition with side-effects-after-CAS (obs metrics + onChange callback only fire when CAS wins).
- **Set**: per-upstream FSM registry analog to breaker.Set. Rebuild preserves state for unchanged names (same FSM pointer), drops deleted, constructs new at StateOff. RemoteState overlay for cross-replica observability (in-process FSM remains authoritative per D-C3).
- **35 unit tests, all -race clean** (1.05s wall): 6 LatencyRing + 6 InflightRegistry + 15 FSM + 8 Set.

## Task Commits

Each task followed TDD (RED → GREEN), so 4 commits total for 2 tasks:

1. **Task 3.1 GREEN: LatencyRing + InflightRegistry** — `f61e71e` (`feat(05-03)`)
   - Tests came from a parallel-execution scope leak by Plan 05-02 (their commit f2abbfb committed `latency_test.go` + `inflight_test.go` while staging migrations — content was identical to my RED-phase output, so no rewrite needed)
2. **Task 3.2 RED: FSM + Set failing tests** — `09d5954` (`test(05-03)`)
3. **Task 3.2 GREEN: FSM + Set implementation** — `ac8cb8e` (`feat(05-03)`)

Plan metadata commit will follow when STATE.md/ROADMAP.md update at wave-completion (per orchestrator).

## Files Created

- `gateway/internal/shed/latency.go` — `LatencyRing` with `atomic.Uint64` index + atomic slot ops. `Record(ms)` amortized O(1); `P95()` snapshot-copy + sort.Slice, ignores zero entries.
- `gateway/internal/shed/latency_test.go` — empty=0, nil-safe, uniform [1..100] P95~95, overwrite-old, default size 0→200, 10×1000 concurrent writes.
- `gateway/internal/shed/inflight.go` — `InflightRegistry` with `map[upstream]*atomic.Int64` global + `map[upstream]map[uuid.UUID]*atomic.Int64` tenant. Populate-once RWMutex. `Upstreams()` + `TenantsForUpstream()` snapshots for the tick goroutine.
- `gateway/internal/shed/inflight_test.go` — empty, nil-safe, 10×1000 balanced Inc/Dec → 0, 3 tenants × 10 Inc → global=30+each=10, unknown upstream no-op, Dec→Inc arithmetic.
- `gateway/internal/shed/fsm.go` — `State` int32 enum, `Config`, `Signals`, `FSM` with `Evaluate(now, sig)` applying D-C1 + `Transition(newState, reason)` synthetic + `UpdateConfig(cfg)` atomic swap.
- `gateway/internal/shed/fsm_test.go` — 15 tests covering all 10 D-C1 transition cells + VramUnknown reduces 2-of-3 to 1-of-2 + OnChange callback + synthetic Transition + UpdateConfig hot-reload + EnteredAt.
- `gateway/internal/shed/set.go` — `Set` registry with `Rebuild/Get/State/ForEach/Names/ApplyRemoteEvent/RemoteState`. `fsmRoot` vs `log` split avoids duplicate `module=…` slog attrs.
- `gateway/internal/shed/set_test.go` — 8 tests covering Rebuild idempotent add/remove + state preservation + OnChange thread-through + RemoteEvent storage + State convenience + ForEach iteration + Options{} defaults fallback.

## FSM Transition Table (D-C1)

All 10 D-C1 cells covered by dedicated tests. The `→` column gives the produced state; `reason` is the string fed to the onChange callback.

| Current        | saturated | elapsed       | Next            | Reason                              | Test                                              |
| -------------- | --------- | ------------- | --------------- | ----------------------------------- | ------------------------------------------------- |
| StateOff       | false     | any           | StateOff        | (no-op)                             | TestFSM_Off_OnlyOneSignal_StaysOff                |
| StateOff       | true      | any           | StateArmed      | signal_rose                         | TestFSM_Off_SaturatedSignal_GoesArmed             |
| StateArmed     | false     | any           | StateOff        | signal_dropped_during_arm           | TestFSM_Armed_SignalDropped_GoesOffImmediate      |
| StateArmed     | true      | < ArmSeconds  | StateArmed      | (still arming)                      | TestFSM_Armed_SustainedArm_GoesOnAfterArmSeconds  |
| StateArmed     | true      | ≥ ArmSeconds  | StateOn         | arm_timeout_sustained               | TestFSM_Armed_SustainedArm_GoesOnAfterArmSeconds  |
| StateOn        | false     | any           | StateRecovering | signal_dropped                      | TestFSM_On_SignalDropped_GoesRecovering           |
| StateOn        | true      | any           | StateOn         | (sustained)                         | TestFSM_On_StaysOn_WhenStillSaturated             |
| StateRecovering| true      | any           | StateOn         | signal_returned_during_recover      | TestFSM_Recovering_SaturatedAgain_GoesOnNotArmed  |
| StateRecovering| false     | < RecoverSec  | StateRecovering | (still recovering)                  | TestFSM_Recovering_CleanForRecoverSeconds_GoesOff |
| StateRecovering| false     | ≥ RecoverSec  | StateOff        | recover_timeout_clean               | TestFSM_Recovering_CleanForRecoverSeconds_GoesOff |

Plus D-A1 fail-open: `VramUnknown=true` masks `VramOverMax` so the 2-of-3 gate reduces to 2-of-2 over (Inflight, P95) — `TestFSM_VramUnknown_ReducesGateToOneOfTwo`.

## Prometheus Metrics Emitted (this plan only writes; collectors registered in 05-01)

- `gateway_shed_state{upstream}` — gauge, set on every successful CAS transition (0=off, 1=armed, 2=on, 3=recovering). Initial value 0 set in NewFSM (so dashboards render before the first transition).
- `gateway_shed_transitions_total{upstream, from, to}` — counter, incremented on every successful CAS transition (cardinality bounded at 3 upstreams × 4×4 = 48 series; T-05-05 mitigation: same-state filter in transition()).

NOT emitted yet (deferred to Plan 05-05 tick):
- `gateway_inflight{upstream}` / `gateway_inflight_tenant{upstream,tenant}` — `InflightRegistry` exposes `GlobalInflight()`/`TenantInflight()`+`Upstreams()`+`TenantsForUpstream()` for the tick to read; this plan does NOT wire the tick.
- `gateway_p95_request_ms{upstream}` — `LatencyRing.P95()` is the source; tick will read once per second.

## Decisions Made

- **atomic.StoreUint32 / atomic.LoadUint32 on LatencyRing slots:** The plan body says "race detector pode flaggar mas é esperado" (D-A2) but the success criteria says "-race passes". To satisfy both, slot reads/writes use atomic.StoreUint32/LoadUint32. The semantic race-benign property (slot collisions lose a sample) is preserved because last-writer-wins still loses samples — the race detector just no longer flags the access pattern because it sees synchronized memory ops. Lockless property preserved (atomic is just a memory barrier, not a lock).
- **Set.fsmRoot + Set.log split:** Without the split, NewFSM(log=Set.log) produced transition logs with `module=SHED module=SHED_FSM` (slog stacks identical keys rather than overwriting). Split: `Set.log = log.With("module", "SHED")` for Set-level logs; `Set.fsmRoot = log` (unmodified) passed to NewFSM so its `.With("module", "SHED_FSM")` produces a clean single attribute.
- **InflightRegistry exposes `Upstreams()` + `TenantsForUpstream()`:** Plan 05-05 tick will need to iterate the registry to publish per-(upstream, tenant) gauges. Exposing snapshot accessors here (rather than `ObserveMetrics()` with a callback) decouples this plan from obs/Prometheus details — the tick decides what to do with the snapshot.
- **Synthetic Transition() method:** Beyond Evaluate-driven transitions, gatewayctl shed-force (Plan 05-07) and the subscribe remote-event consumer (Plan 05-04) need to drive synthetic state changes. transition() filters same-state calls so Transition(currentState, …) is safely a no-op.
- **FSM Config.Upstream invariant across UpdateConfig:** UpdateConfig overwrites the stored Config but forces Upstream back to the FSM's bound name. Prevents a hot-reload misconfiguration (config has wrong upstream string) from silently breaking obs label cardinality.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] LatencyRing race detector fix**
- **Found during:** Task 3.1 verification (`go test -race`)
- **Issue:** Plan body line 132 + 363 says "race detector pode flaggar — esperado por D-A2", but the plan-level success criteria says "go test ./gateway/internal/shed/... -race -count=1 passes". The buf write `r.buf[i%r.size] = ms` triggered "data race" warnings on concurrent slot collisions.
- **Fix:** Switched to `atomic.StoreUint32(&r.buf[i%r.size], ms)` for writes and `atomic.LoadUint32(&r.buf[i])` for reads. Preserves lockless intent (atomic is a memory barrier, not a lock); preserves race-benign semantics (last-writer-wins still drops one sample on collision); satisfies -race.
- **Files modified:** gateway/internal/shed/latency.go
- **Verification:** `CGO_ENABLED=1 go test -race ./gateway/internal/shed/... -count=1` clean (was failing before fix).
- **Committed in:** f61e71e (Task 3.1 GREEN)

**2. [Rule 1 - Bug] Set logger double-module attribute**
- **Found during:** Task 3.2 verification (`go test -v` output inspection)
- **Issue:** Set.log = log.With("module", "SHED") was passed to NewFSM, which does another .With("module", "SHED_FSM"). slog stacks identical keys rather than overriding, producing `module=SHED module=SHED_FSM` in transition logs — breaks log parsing and is non-canonical per PATTERNS.md §SHED naming.
- **Fix:** Added `Set.fsmRoot *slog.Logger` field (no module attr) and pass it to NewFSM instead of Set.log. Set-level logs still use Set.log (module=SHED); FSM transition logs use the clean root (module=SHED_FSM).
- **Files modified:** gateway/internal/shed/set.go
- **Verification:** Test run shows `module=SHED_FSM` only in transition logs after fix.
- **Committed in:** ac8cb8e (Task 3.2 GREEN)

**3. [Out-of-scope-but-included] Plan 05-02 changes swept into Task 3.2 RED commit**
- **Found during:** Task 3.2 RED commit
- **Issue:** When committing `gateway/internal/shed/fsm_test.go` + `gateway/internal/shed/set_test.go` (my staged files), git also included unstaged-but-tracked modifications to `gateway/internal/tenants/config.go`, `gateway/internal/tenants/loader.go`, and `gateway/internal/upstreams/types.go`. Investigation: these are Plan 05-02's territory (PATTERNS.md assigns them to 05-02). They appear to have been modified-but-not-yet-committed by the parallel agent at the moment of my commit. I did NOT `git add` them and did NOT use `-a` flag, yet they appeared in the commit. Root cause unconfirmed (possible parallel staging race between two agents sharing the same .git/index).
- **Fix:** No fix — the content is correct (it's the Phase 5 CircuitConfig + TenantConfig extension that Plan 05-02 owns). Leaving the commit as-is rather than reverting (would lose 05-02's work). The build still passes.
- **Files included:** gateway/internal/tenants/config.go, gateway/internal/tenants/loader.go, gateway/internal/upstreams/types.go
- **Verification:** `go build ./...` and `go vet ./...` both clean; 35 -race tests pass.
- **Committed in:** 09d5954 (Task 3.2 RED — scope leak from parallel execution; not a Rule 1/2/3 fix but a parallel-execution artifact)

---

**Total deviations:** 2 auto-fixes (1 race-detector fix, 1 logger fix) + 1 parallel-execution scope leak.
**Impact on plan:** Both auto-fixes preserve plan semantics while satisfying the success criteria. The scope leak is benign — the swept-in changes belong to a sibling plan and were ultimately committed cleanly via my path; no rework needed.

## Issues Encountered

- **gcc not installed** at start — `go test -race` requires CGO. Resolved via `sudo apt-get install -y gcc` (sudo without password worked). After install, -race ran clean.
- **Plan 05-02 race on `gateway/internal/shed/latency_test.go` + `inflight_test.go`:** Plan 05-02's commit f2abbfb included exact-content-identical copies of my Task 3.1 RED tests (both plans referenced the same RESEARCH §Pattern 2/4 + plan body code blocks). My Task 3.1 RED was effectively a no-op merge against a pre-existing identical file. Resolved by skipping a separate RED commit for Task 3.1 (file was already in tree from 05-02; my GREEN was the only material change).

## TDD Gate Compliance

Plan is marked `autonomous: true` (not `type: tdd` at plan level — each task carries its own `tdd="true"` attribute). The plan-level TDD gate sequence does not apply at the whole-plan level, but each task internally followed RED → GREEN:

- **Task 3.1 RED:** Skipped a separate commit because Plan 05-02 committed identical content for `latency_test.go` + `inflight_test.go` in f2abbfb (parallel-execution artifact noted above). Effective RED state was f2abbfb (tests existed without implementations).
- **Task 3.1 GREEN:** f61e71e — implementations added, tests pass.
- **Task 3.2 RED:** 09d5954 — `test(05-03)`: tests added, build fails with "undefined: FSM, Config, NewFSM, State*".
- **Task 3.2 GREEN:** ac8cb8e — `feat(05-03)`: implementations added, all 35 tests pass under -race.

REFACTOR: not needed — implementations are minimal and idiomatic.

## Next Phase Readiness

- **Plan 05-04 (mirror.go + subscribe.go)** can now consume `shed.Set.ApplyRemoteEvent(upstream, State)` for the Pub/Sub consumer and `shed.FSM.Transition(state, reason)` for synthetic state writes from remote events.
- **Plan 05-05 (tick + DCGM scraper)** can:
  - call `shed.Set.ForEach` once per second to drive `fsm.Evaluate(now, signals)` per upstream
  - read `InflightRegistry.GlobalInflight(name)` for the inflight signal
  - read `LatencyRing.P95()` for the P95 signal
  - publish `gateway_inflight{upstream}` gauge via `InflightRegistry.Upstreams()`
  - publish `gateway_inflight_tenant{upstream, tenant}` gauge via `TenantsForUpstream(name)`
- **Plan 05-06 (middleware)** can:
  - call `shed.Set.State(upstream)` (lockless) for the FSM-on check
  - call `InflightRegistry.TenantInflight(upstream, tenantID)` for the per-tenant cap check
  - call `InflightRegistry.Inc/Dec(upstream, tenantID)` paired via `defer` to track in-flight counters
- **Plan 05-07 (gatewayctl shed-state / shed-force)** can call `shed.FSM.Transition(state, reason)` for the force-override path and read `shed.Set.RemoteState(name)` for the dashboard cross-replica column.

No blockers. Pure additive — none of the new public APIs is consumed by any code outside this plan yet.

---

## Self-Check: PASSED

- [x] `gateway/internal/shed/latency.go` exists (89 lines)
- [x] `gateway/internal/shed/latency_test.go` exists (90 lines)
- [x] `gateway/internal/shed/inflight.go` exists (185 lines)
- [x] `gateway/internal/shed/inflight_test.go` exists (109 lines)
- [x] `gateway/internal/shed/fsm.go` exists (257 lines)
- [x] `gateway/internal/shed/fsm_test.go` exists (233 lines)
- [x] `gateway/internal/shed/set.go` exists (178 lines)
- [x] `gateway/internal/shed/set_test.go` exists (141 lines)
- [x] Commit `f61e71e` (`feat(05-03): LatencyRing + InflightRegistry`) is reachable from HEAD
- [x] Commit `09d5954` (`test(05-03): failing tests for FSM 4-state + Set registry (RED)`) is reachable from HEAD
- [x] Commit `ac8cb8e` (`feat(05-03): FSM 4-state + Set registry (GREEN)`) is reachable from HEAD
- [x] `go test -race ./gateway/internal/shed/... -count=1` returns OK with 35 tests
- [x] `go vet ./...` clean
- [x] `go build ./...` clean

---

*Phase: 05-load-shedding-saturation-aware-routing*
*Plan: 03*
*Completed: 2026-05-11*
