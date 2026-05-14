---
phase: 07-observability-dashboard-alerting
plan: 05
subsystem: infra
tags: [go, alerting, redis-pubsub, dedup, goroutine, bounded-worker, observability]

# Dependency graph
requires:
  - phase: 07-observability-dashboard-alerting
    provides: "07-04 — alert.Channel interface + Message struct + Severity type, redisx.AlertDedupKey namespace, obs.AlertSendsTotal counter"
  - phase: 07-observability-dashboard-alerting
    provides: "07-01 — build-tag-free Fake{Chatwoot,ClickUp,Brevo} recording fakes, obs.AlertDroppedTotal counter"
  - phase: 03-resilience-failover-chain
    provides: "redisx.BreakerEvent + BreakerEventsChannel, the canonical breaker/subscribe.go reconnect loop"
  - phase: 05-load-shedding
    provides: "redisx.ShedEvent + ShedEventsChannel, the shed/mirror.go MakePublishTransition bounded-worker pattern"
  - phase: 06-auto-provisioning-emergency-pod-vast-ai
    provides: "redisx.EmergEvent + EmergEventsChannel, emerg FSM state-string vocabulary, redisx.EmergStateKey mirror Hash"
provides:
  - "alert/severity.go — severityFor (event → tier + Message) + channelsFor (tier → channel matrix); pure, no I/O; SeverityInfo const added to client.go"
  - "alert/dedup.go — dedupShouldSend: Redis SET NX EX 300 fingerprint gate, fail-open for critical / fail-closed for warning+info"
  - "alert/alerter.go — Alerter.Run(ctx) goroutine (subscribes 3 Pub/Sub channels on one connection, classifies, dedups, fans out via bounded per-channel workers) + Alerter.ReconcileBoot(ctx)"
affects: [07-06]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Pure event-classification transform: severityFor switches on the Pub/Sub channel name, unmarshals the matching redisx.*Event, maps to a tier + a channel-agnostic Message with a stable timestamp-free fingerprint — the analog of breaker.stateFloat, zero I/O so the alerter is testable with synthetic events"
    - "Severity-dependent fail policy: a Redis error in the dedup gate fails OPEN for critical (a missed page is an outage) and CLOSED for warning/info (alert fatigue is the larger risk) — the decision is the bool return, no error propagated"
    - "Non-blocking fan-out: the Pub/Sub consume loop classifies + dedups + ENQUEUES onto bounded per-channel worker queues; Channel.Send is called ONLY inside a worker goroutine; a full queue bumps a drop counter instead of stalling the loop — mirrors shed/mirror.go's MakePublishTransition"
    - "Boot reconciliation for at-most-once Pub/Sub: ReconcileBoot reads the emergency FSM state mirror Hash on startup and replays a synthetic transition event through the same handle() path, so a restart mid-incident still pages — the dedup gate makes the replay idempotent against a fast restart"

key-files:
  created:
    - gateway/internal/alert/severity.go
    - gateway/internal/alert/severity_test.go
    - gateway/internal/alert/dedup.go
    - gateway/internal/alert/dedup_test.go
    - gateway/internal/alert/alerter.go
    - gateway/internal/alert/alerter_test.go
  modified:
    - gateway/internal/alert/client.go

key-decisions:
  - "Fingerprints deliberately exclude the event timestamp — breaker:<upstream>:<state>, shed:<upstream>:<state>, emerg:<type>:<state>. A breaker re-tripping for the same upstream MUST collide on one fingerprint or the SET NX dedup never collapses a flapping storm. Tested explicitly (TestSeverityFor_FingerprintStable re-publishes with a different SinceUnix and asserts the fingerprint is unchanged)."
  - "Primary-vs-fallback breaker criticality: a breaker OPEN for local-llm (the tier-0 GPU) is critical; the same OPEN for openrouter/openai is only warning — the fallback chain degrading is not a page, the primary going down is. The primaryLLMUpstream const isolates that name."
  - "emerg command events (force_provision_request / force_destroy_request) classify as info, not warning — they are operator intents the reconciler consumes, not incidents. Only Type==transition into emergency_provisioning/emergency_active (critical) or failed_over/recovering (warning) pages."
  - "The alerter test file adapts the 07-01 Fake* channels (which expose Send(ctx, title, body) — a pre-Channel-interface shape) into the Channel interface via a small mutex-guarded recordingChannel wrapper. The fakes are documented as NOT mutex-guarded and the alerter's per-channel workers run concurrently, so the wrapper adds the synchronization the -race build needs."
  - "ReconcileBoot scopes to the emergency FSM state Hash only (gw:emerg:state), not a full breaker-key scan. Pitfall 4's concrete risk is 'a restart during an active emergency incident silently un-pages' — the emergency FSM state is the authoritative incident signal; a per-breaker scan would add surface for marginal value. Documented in the ReconcileBoot doc comment."

patterns-established:
  - "Channel-name-discriminated Pub/Sub classification: one rdb.Subscribe(ctx, ch1, ch2, ch3) call, msg.Channel routes to the per-channel decoder — the multi-channel variant of breaker/subscribe.go, recommended by 07-RESEARCH.md over emerg/subscribe.go's two-consumer approach"
  - "Bounded per-channel worker isolation for a fan-out goroutine: NewAlerter indexes channels by Name() into per-channel buffered queues; Run starts one drain goroutine per channel; the consume loop's only interaction is a non-blocking `select { case q <- job: default: drop+count }`"

requirements-completed: [OBS-04, OBS-06]

# Metrics
duration: ~20min
completed: 2026-05-14
---

# Phase 7 Plan 05: Alerting Goroutine Core (Severity + Dedup + Alerter) Summary

**The OBS-04/06 brain — `severity.go` (pure event→tier→channel-matrix mapping with stable timestamp-free fingerprints), `dedup.go` (Redis `SET NX EX 300` fingerprint gate, fail-open for critical / fail-closed for warning+info), and `alerter.go` (the `Run(ctx)` goroutine that subscribes all three gateway Pub/Sub event streams on one connection, classifies, dedups, and fans out to Chatwoot/ClickUp/Brevo via bounded per-channel workers that never block the consume loop) — plus a `ReconcileBoot` that re-surfaces an active emergency incident after a mid-incident restart.**

## Performance

- **Duration:** ~20 min
- **Tasks:** 3 (all TDD)
- **Files modified:** 7 (6 created, 1 modified)

## Accomplishments

- **`severity.go` — pure classification.** `severityFor(channel, payload)` switches on the Pub/Sub channel name, unmarshals the matching `redisx.{Breaker,Shed,Emerg}Event`, and maps to a `Severity` tier + a channel-agnostic `Message`. Mapping per 07-CONTEXT.md: a `local-llm` breaker→open or an emergency FSM→`emergency_active`/`emergency_provisioning` is **critical**; a fallback-upstream breaker→open, a shed FSM→`on`, or an emergency FSM→`failed_over`/`recovering` is **warning**; every recovery/benign transition is **info**. `channelsFor(tier)` is the plain channel matrix: critical→`{chatwoot,clickup,brevo}`, warning→`{clickup,brevo}`, info→`{}`. Import block confirmed I/O-free (`encoding/json`, `fmt`, `redisx` only — no `net/http`, `database/sql`, or `redis`).
- **`dedup.go` — the 5-minute dedup gate.** `dedupShouldSend(ctx, rdb, sev, fingerprint)` does `SET NX` on `redisx.AlertDedupKey(fingerprint)` with a `5*time.Minute` TTL inside a 2s op timeout. First occurrence → `true` (key claimed); duplicate within the window → `false` (suppress external channels, the alerter still logs); Redis error → fail-OPEN for `SeverityCritical`, fail-CLOSED for warning/info (threat T-07-19's accepted tradeoff). The bool is the whole decision — no error return.
- **`alerter.go` — the `Run(ctx)` fan-out goroutine.** `NewAlerter(rdb, []Channel, log)` indexes channels by `Name()` into bounded per-channel worker queues (depth 64, matching `shed/mirror.go`). `Run` starts one drain goroutine per channel, then enters the canonical `breaker/subscribe.go` reconnect loop subscribing **all three channels on ONE `Subscribe(...)` call** — `msg.Channel` discriminates the payload. `handle()` classifies (malformed JSON → `log.Warn` + return, never crashes — T-07-17), dedups (duplicate → log + return), and for each channel in `channelsFor(sev)` does a **non-blocking enqueue**: a full queue bumps `obs.AlertDroppedTotal` and the loop continues (T-07-15 / Pitfall 5). `Channel.Send` is called **only inside `runWorker`**, never in the consume loop — verified by `git grep`. Each send increments `obs.AlertSendsTotal{channel,result}`.
- **`ReconcileBoot(ctx)` — restart-during-incident coverage.** Redis Pub/Sub is at-most-once with no replay; a transition into `emergency_active` that fired while the gateway was down is lost. `ReconcileBoot` reads the `gw:emerg:state` mirror Hash once at boot and, if the state is alert-worthy, synthesises the matching `EmergEvent` and pushes it through the identical `handle()` path. The dedup gate makes this idempotent — a fast restart that already alerted does not double-page (Pitfall 4 / T-07-18).
- **`SeverityInfo` const** added to `client.go` alongside the existing `SeverityCritical`/`SeverityWarning` — the third tier the channel matrix needs.
- **Zero new Go dependencies** — only stdlib + the already-vendored `redis/go-redis/v9` and `prometheus/client_golang` (the latter only in the test, via `testutil.ToFloat64`).

## Task Commits

Each task was committed atomically:

1. **Task 1: severity.go — event → tier → channel matrix** - `5ae7db0` (feat)
2. **Task 2: dedup.go — Redis SET NX EX 300 fingerprint dedup** - `71058f9` (feat)
3. **Task 3: alerter.go — the Run(ctx) goroutine** - `dc7f403` (feat)

**Plan metadata:** committed with this SUMMARY (docs: complete plan)

## Files Created/Modified

- `gateway/internal/alert/severity.go` - `severityFor` (3-channel switch → tier + `Message` with stable fingerprint) + `channelsFor` (tier → channel matrix); `primaryLLMUpstream` const, `emergCriticalStates`/`emergWarningStates` sets
- `gateway/internal/alert/severity_test.go` - 11-case table covering all three Pub/Sub sources × tier classification, malformed-JSON-returns-error, the channel matrix (critical=3, warning=2, info=0, unknown=0), and fingerprint stability (re-publish with a different timestamp → same fingerprint; different upstream → different fingerprint)
- `gateway/internal/alert/dedup.go` - `dedupShouldSend` (SET NX EX, fail-open critical / fail-closed warning+info), `dedupTTL`/`dedupOpTimeout` consts
- `gateway/internal/alert/dedup_test.go` - first/duplicate, TTL expiry (miniredis `FastForward`), fail-open-critical + fail-closed-warning/info (closed miniredis simulates the outage)
- `gateway/internal/alert/alerter.go` - `Alerter` struct + `NewAlerter` + `Run(ctx)` (one Subscribe, canonical reconnect loop) + `handle` (classify→dedup→bounded enqueue) + `runWorker` (the sole `Channel.Send` callsite) + `ReconcileBoot`
- `gateway/internal/alert/alerter_test.go` - 8 tests covering all six plan behaviors + 2 ReconcileBoot cases; a mutex-guarded `recordingChannel` adapts the 07-01 `Fake*` channels to the `Channel` interface for the `-race` build
- `gateway/internal/alert/client.go` - `SeverityInfo` const added (the third tier)

## Decisions Made

- **Fingerprints exclude the event timestamp.** `breaker:<upstream>:<state>`, `shed:<upstream>:<state>`, `emerg:<type>:<state>` — deterministic for the same logical incident. A breaker re-tripping for `local-llm` re-publishes a `BreakerEvent` with a fresh `SinceUnix`; if `SinceUnix` were in the fingerprint, every flap would dodge the dedup gate. `TestSeverityFor_FingerprintStable` asserts this directly.
- **Primary-vs-fallback breaker criticality.** A breaker OPEN for `local-llm` (tier-0 GPU) is critical; the same OPEN for `openrouter`/`openai` is warning. The fallback chain degrading is the chain *doing its job* — paging WhatsApp for it is alert noise. The `primaryLLMUpstream` const isolates the name for a future config-driven override.
- **emerg command events are info-tier.** `force_provision_request`/`force_destroy_request` `EmergEvent`s are operator intents the reconciler consumes, not incidents — classifying them as info means they are still logged but never page. Only `Type=="transition"` events page, gated on the `State` string.
- **The test adapts the 07-01 fakes via a wrapper.** `testsupport.go`'s `Fake*` channels expose `Send(ctx, title, body)` (a pre-`Channel`-interface shape from Wave-0) and are documented as NOT mutex-guarded. The alerter's per-channel workers run concurrently, so `alerter_test.go` wraps each fake in a mutex-guarded `recordingChannel` that satisfies `Channel` and threads `Message.Title`/`Body` through the fake's signature. This keeps the Wave-0 fakes usable without modifying them.
- **`ReconcileBoot` scopes to the emergency FSM state only.** Pitfall 4's concrete risk is a restart during an *active emergency incident* silently un-paging. The `gw:emerg:state` Hash is the authoritative incident signal; a full `gw:breaker:*` key scan would add code surface for marginal value (a breaker that is still open will re-publish on the next probe anyway). Documented in the `ReconcileBoot` doc comment.

## Deviations from Plan

None - plan executed exactly as written. All three tasks, the file list, every acceptance criterion, and the threat model were satisfied as specified. The plan's `read_first` notes already flagged that the 07-01 `testsupport.go` fakes use the pre-`Channel` `Send(ctx, title, body)` shape ("alerter tests inject these, not real clients") — the `recordingChannel` adapter is the anticipated thin adapter the 07-04-SUMMARY's "a thin adapter ... lets these fakes stand in" note predicted, not a deviation.

## Issues Encountered

- **The drop-counter test needed distinct fingerprints under a blocked channel.** `TestAlerter_FullQueueIncrementsDropCounter` must push more than `channelQueueDepth` events that each reach the fan-out (no dedup collapse) while chatwoot's worker is blocked. The breaker channel's only critical fingerprint is `breaker:local-llm:open` (one value) — every event would dedup-collapse. Resolved by driving the test through the **emerg channel**, varying the event `Type` (`transition-0`, `transition-1`, …) while holding `State=="emergency_active"`: `severityFor` classifies on `State` (stays critical) but the fingerprint is `emerg:<type>:<state>` (distinct per event). The test feeds `handle()` directly with the worker goroutines started manually, isolating the drop path from the Pub/Sub loop.
- **No analogous issue for the other tests.** miniredis v2.37.0 supports real `PUBLISH`/`Subscribe` (confirmed by the existing `emerg/subscribe_test.go`), so the breaker/shed/emerg publish→classify→fan-out path tests run against a real Pub/Sub round-trip.

## User Setup Required

None directly from this plan. The alerter consumes the `Channel` clients (Chatwoot/ClickUp/Brevo) built in 07-04 from the 13 optional Config fields 07-01 added; external-service credentials remain operator prerequisites tracked in 07-01-SUMMARY's "User Setup Required" and the Phase 7 HUMAN-UAT plan. The alerter is not yet *wired into* `main.go` — `NewAlerter` construction + `go alerter.Run(ctx)` + the `ReconcileBoot` call land in 07-06, which is the planned interface-first ordering.

## Threat Surface

No new threat surface beyond the plan's `<threat_model>`. All five registered threats are mitigated as designed:

- **T-07-15 (DoS — alerter consume loop)** — `handle()` classifies + dedups + does a **non-blocking** `select { case q <- job: default: }` enqueue; `Channel.Send` is called only inside `runWorker` (verified by `git grep "\.Send(" alerter.go` → one hit, line 251). A dead Chatwoot blocks its own worker, not the consume loop. `TestAlerter_BlockingSendDoesNotStallClassification` proves clickup+brevo still deliver while chatwoot's `Send` blocks; `TestAlerter_FullQueueIncrementsDropCounter` proves a full queue bumps `obs.AlertDroppedTotal` rather than stalling.
- **T-07-16 (DoS — alert fatigue)** — `dedupShouldSend` does `SET NX EX 300`; a repeated fingerprint inside the window returns `false` and the alerter skips all external channels (still logs). `TestAlerter_DuplicateIsDeduped` publishes the same critical event twice and asserts each channel receives exactly one `Send`.
- **T-07-17 (Tampering — malformed Pub/Sub payload)** — `severityFor` returns an error for unparseable JSON or an unknown channel; `handle()` logs a WARN and `return`s. `TestAlerter_MalformedJSONSurvived` publishes raw garbage onto the breaker channel and asserts a subsequent valid event still fans out — the loop survived. `TestSeverityFor_MalformedJSON` covers the error return directly.
- **T-07-18 (Repudiation — missed critical during boot/restart)** — `ReconcileBoot` reads `gw:emerg:state` on startup and replays a synthetic transition through `handle()` if the FSM is mid-incident. `TestAlerter_ReconcileBootSurfacesActiveIncident` writes an `emergency_active` mirror Hash and asserts `ReconcileBoot` surfaces a critical alert; `TestAlerter_ReconcileBootBenignStateNoAlert` asserts a `healthy` state surfaces nothing.
- **T-07-19 (Information Disclosure — dedup fail-mode)** — `dedupShouldSend` on a Redis error returns `sev == SeverityCritical`: fail-OPEN for critical, fail-CLOSED for warning/info. `TestDedupShouldSend_FailOpenCritical` and `TestDedupShouldSend_FailClosedWarningInfo` (closed miniredis) cover both branches.

## Known Stubs

None. `severity.go`, `dedup.go`, and `alerter.go` are full implementations. The alerter is not yet *constructed and spawned* in `main.go` — that wiring (`NewAlerter` + `go alerter.Run(ctx)` + `ReconcileBoot`, spawned EARLY before the publishing subsystems per Pitfall 4) is plan 07-06, the planned interface-first composition-root ordering, not an incomplete deliverable of this plan.

## Next Phase Readiness

- **07-06 unblocked.** `NewAlerter(rdb, []Channel, log)`, `Alerter.Run(ctx)`, and `Alerter.ReconcileBoot(ctx)` exist and build. `main.go` can build the three concrete clients from `config.Config`, type-assert them to `Channel`, hand the slice to `NewAlerter`, call `ReconcileBoot` once, and `go alerter.Run(rootCtx)` early in the boot sequence (before `breakerSet.Subscribe` / the shed + emerg subsystems start publishing — Pitfall 4 ordering).
- **Verification green.** `cd gateway && go build ./...` exits 0; `go test ./internal/alert/ -count=1 -race` passes (whole package, ~15s with the race detector); `go vet ./internal/alert/` clean; `severity.go` import block confirmed I/O-free; `git grep "\.Send(" internal/alert/alerter.go` shows the single worker-goroutine callsite.
- **No blockers.**

## Self-Check: PASSED

All 6 created files exist on disk; the 1 modified file (`client.go`) builds. All 3 task commits (`5ae7db0`, `71058f9`, `dc7f403`) are reachable in git history. `go build ./...` + `go test ./internal/alert/ -race` are green.

---
*Phase: 07-observability-dashboard-alerting*
*Completed: 2026-05-14*
