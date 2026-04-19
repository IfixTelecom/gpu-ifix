---
phase: 03-resilience-fallback-chain
plan: 01
subsystem: infra
tags: [go-modules, gobreaker, backoff, pgxlisten, sentinel-errors, scaffolding, wave-0]

# Dependency graph
requires:
  - phase: 02-gateway-core-multi-tenant-auth
    provides: gateway/internal/proxy/errors.go pattern, gateway/internal/upstreams package, sentinel-errors layout
provides:
  - "github.com/sony/gobreaker/v2 v2.4.0 pinned in go.mod (consumed by future gateway/internal/breaker/breaker.go)"
  - "github.com/cenkalti/backoff/v5 v5.0.3 pinned in go.mod (consumed by future gateway/internal/proxy/dispatcher.go)"
  - "github.com/jackc/pgxlisten v0.0.0-20250802141604-12b92425684c pinned in go.mod (consumed by future gateway/internal/upstreams/listen.go)"
  - "gateway/internal/breaker package created with sentinel errors ErrBreakerOpen, ErrUpstreamUnavailable"
  - "gateway/internal/upstreams/errors.go with ErrProbeTimeout, ErrUpstreamNotFound"
  - "Three new sentinels appended to gateway/internal/proxy/errors.go: ErrSensitiveRetryExhausted, ErrToolCallPartialStream, ErrContextLengthExceeded"
  - "gateway/internal/upstreams/testdata/probe.wav (32044 bytes, RIFF WAVE PCM 16-bit mono 16kHz, 1s silence) for STT synthetic E2E probes"
affects: [03-02, 03-03, 03-04, 03-05, 03-06, 03-07, 03-08]

# Tech tracking
tech-stack:
  added:
    - sony/gobreaker/v2 v2.4.0
    - cenkalti/backoff/v5 v5.0.3
    - jackc/pgxlisten v0.0.0-20250802141604-12b92425684c
  patterns:
    - "Wave 0 dep-pinning via blank-import scaffold file (scaffold_imports.go) — pattern documented inline; file deleted when downstream code lands"
    - "Sentinel errors extension pattern: append to existing errors.go without re-declaring package or imports"
    - "Binary test fixtures committed to gateway/internal/<pkg>/testdata/ (Go convention)"

key-files:
  created:
    - gateway/internal/breaker/errors.go
    - gateway/internal/breaker/scaffold_imports.go
    - gateway/internal/upstreams/errors.go
    - gateway/internal/upstreams/testdata/probe.wav
  modified:
    - go.mod
    - go.sum
    - gateway/internal/proxy/errors.go

key-decisions:
  - "Used scaffold_imports.go with three blank `_` imports in the new breaker package to keep go mod tidy from stripping the pinned deps before downstream waves consume them. File documents itself for deletion."
  - "Plan said `cd gateway && go get ...` but go.mod lives at the repo root (gateway/ is a sub-tree without its own module). Adjusted commands to run from /home/pedro/projetos/pedro/gpu-ifix; build and vet still target ./... transitively covering gateway/."
  - "jackc/pgxlisten pseudo-version v0.0.0-20250802141604-f2ebc03ec6b7 in plan was invalid (unknown revision). Per plan's explicit fallback instruction, used @latest which resolved to v0.0.0-20250802141604-12b92425684c (same date, different commit hash)."
  - "probe.wav generated via pure-Go fallback (ffmpeg/sox unavailable on this host). Output: 32044 bytes, RIFF WAVE Microsoft PCM 16-bit mono 16kHz, 1s of zero samples. sha256: 643f8a8dc8bd9c19225afffad2becfec5426180b3749cb208abdf1a6c8354efc"

patterns-established:
  - "Phase 3 sentinel-error placement: each new package owns its own errors.go starting with the canonical `var ErrXxx = errors.New(\"<pkg>: <message>\")` form + godoc string explaining HTTP status mapping and CONTEXT.md decision reference."

requirements-completed: [RES-01, RES-02, RES-03, RES-04, RES-06, RES-07, RES-08]

# Metrics
duration: ~4min 23s
completed: 2026-04-19
---

# Phase 3 Plan 01: Wave 0 Scaffolding Summary

**Pinned Phase 3 resilience deps (gobreaker v2, backoff v5, pgxlisten), seeded sentinel-error files for breaker / upstreams / proxy packages, and committed the 32 KB silent WAV fixture for STT synthetic probes — all required by the Wave-0-Gaps test inventory before downstream waves can compile.**

## Performance

- **Duration:** ~4 min 23 s
- **Started:** 2026-04-19T23:13:26Z
- **Completed:** 2026-04-19T23:17:49Z (Task 1 only; Task 2 paused at operator-action checkpoint)
- **Tasks:** 1 of 2 executed (Task 2 deferred — see "Checkpoint Reached" below)
- **Files modified/created:** 7 (3 created in Phase 3 packages, 1 binary fixture, 1 scaffold-imports file, 2 module-graph files)

## Accomplishments

- **Three Phase 3 deps pinned in go.mod with exact versions** (verified via `grep`):
  - `github.com/sony/gobreaker/v2 v2.4.0`
  - `github.com/cenkalti/backoff/v5 v5.0.3`
  - `github.com/jackc/pgxlisten v0.0.0-20250802141604-12b92425684c`
- **Sentinel errors created and ready to be referenced by Wave 0 test inventory:**
  - `breaker.ErrBreakerOpen`, `breaker.ErrUpstreamUnavailable`
  - `upstreams.ErrProbeTimeout`, `upstreams.ErrUpstreamNotFound`
  - `proxy.ErrSensitiveRetryExhausted`, `proxy.ErrToolCallPartialStream`, `proxy.ErrContextLengthExceeded`
- **probe.wav fixture committed** at the canonical path (`gateway/internal/upstreams/testdata/probe.wav`); valid `RIFF WAVE Microsoft PCM, 16 bit, mono 16000 Hz`; 32044 bytes (well within the 10240–51200 byte band).
- **Module compiles and vets clean:** `go build ./...` and `go vet ./...` both exit 0 with no stderr after the changes.

## Task Commits

1. **Task 1: Add Phase 3 Go dependencies + scaffold sentinel errors + probe fixture** — `eddc840` (feat)
2. **Task 2: Operator-verifiable Wave 0 gates (Fireworks slug + /tokenize availability)** — DEFERRED, awaiting operator action

_Plan metadata commit:_ produced after this SUMMARY is staged.

## Files Created/Modified

- `gateway/internal/breaker/errors.go` (created) — Package godoc + `ErrBreakerOpen`, `ErrUpstreamUnavailable` sentinels per CONTEXT.md D-A1 / D-C4.
- `gateway/internal/breaker/scaffold_imports.go` (created) — Blank `_` imports for the three new deps; documents its own deletion contract once the real consumers land in waves 2–4.
- `gateway/internal/upstreams/errors.go` (created) — `ErrProbeTimeout`, `ErrUpstreamNotFound` sentinels per D-A4 / D-D2 (no package-godoc — sibling `health.go` already owns it).
- `gateway/internal/upstreams/testdata/probe.wav` (created) — 32044-byte silent WAV PCM probe fixture; sha256 `643f8a8dc8bd9c19225afffad2becfec5426180b3749cb208abdf1a6c8354efc`.
- `gateway/internal/proxy/errors.go` (modified) — Appended three new sentinels (`ErrSensitiveRetryExhausted`, `ErrToolCallPartialStream`, `ErrContextLengthExceeded`) below the existing `ErrUpstreamUnreachable`. The `ErrorHandler` function and original sentinel are unchanged.
- `go.mod` (modified) — Three new `require` lines (direct).
- `go.sum` (modified) — Hash entries for the three new modules + their transitive deps; also picked up minor upgrades for `golang.org/x/crypto`, `golang.org/x/sync`, `golang.org/x/text` pulled in by `pgxlisten`.

## Decisions Made

- **Scaffold-imports pattern for dep pinning** — Without any consumer code, `go mod tidy` strips newly-added deps. The plan's verification grep insists they appear in `go.mod`. Resolved by adding `gateway/internal/breaker/scaffold_imports.go` containing only blank `_` imports of the three deps, with a self-documenting godoc comment listing which downstream file (one per dep) is responsible for deleting an entry as it adds the real consumer. This preserves Wave 0's contract without polluting the implementation surface.
- **Repo-root `go.mod` instead of plan's `gateway/`** — Plan instructions consistently say `cd gateway && go ...`, but the actual module is `github.com/ifixtelecom/gpu-ifix` rooted at the repo top, with `gateway/` as a sub-tree. All `go get`, `go build ./...`, `go vet ./...` invocations were run from the repo root; results identical because `./...` matches `gateway/...` recursively.
- **`jackc/pgxlisten@latest` fallback used** — Plan-listed pseudo-version `v0.0.0-20250802141604-f2ebc03ec6b7` was rejected by Go proxy ("unknown revision"). Per plan's explicit fallback instruction, ran `go get github.com/jackc/pgxlisten@latest`, which resolved to `v0.0.0-20250802141604-12b92425684c` (same date, different commit hash from the plan's draft).

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] go.mod lives at repo root, not in gateway/**
- **Found during:** Task 1, sub-step 1 (`cd gateway && go get ...`)
- **Issue:** Plan repeatedly said `cd gateway && go ...` but `gateway/` has no `go.mod`. The single module file is at `/home/pedro/projetos/pedro/gpu-ifix/go.mod`. `cd gateway && go get ...` would fail with "go.mod not found" or, worse, walk up and silently mutate the wrong path expectations.
- **Fix:** Ran every Go command from the repo root (`/home/pedro/projetos/pedro/gpu-ifix/.claude/worktrees/agent-aed2ab58`). `go build ./...` and `go vet ./...` from the root cover `gateway/...` transitively, so the plan's verification semantics are preserved.
- **Files modified:** None (operational adjustment only).
- **Verification:** Both `go build ./...` and `go vet ./...` exited 0 from the repo root.
- **Committed in:** Implicit in `eddc840`.

**2. [Rule 3 - Blocking] `go mod tidy` strips unused deps; scaffold blank imports needed**
- **Found during:** Task 1, sub-step 6 (post-build verification grep on go.mod).
- **Issue:** Plan instructs `go mod tidy` after `go get`, then verifies with `grep 'sony/gobreaker/v2 v2.4.0' go.mod`. Without any importer, `tidy` removes the new deps entirely. First grep returned 0 matches → blocker.
- **Fix:** Created `gateway/internal/breaker/scaffold_imports.go` with three blank `_` imports of the new deps and a godoc comment explaining the file is Wave-0 scaffolding and which downstream file is the deletion-trigger for each dep. After re-running `go mod tidy`, all three deps appeared in `go.mod` as direct requires with the correct pinned versions.
- **Files modified:** `gateway/internal/breaker/scaffold_imports.go` (created).
- **Verification:** `grep -E '(sony/gobreaker|cenkalti/backoff/v5|jackc/pgxlisten)' go.mod` returns the three exact-version lines; `go build ./...` and `go vet ./...` still exit 0.
- **Committed in:** `eddc840` (Task 1).

**3. [Rule 3 - Blocking] Plan's pgxlisten pseudo-version invalid**
- **Found during:** Task 1, sub-step 1.
- **Issue:** `go get github.com/jackc/pgxlisten@v0.0.0-20250802141604-f2ebc03ec6b7` failed with `invalid version: unknown revision f2ebc03ec6b7`.
- **Fix:** Used the plan's documented fallback (`go get github.com/jackc/pgxlisten@latest`) which resolved to `v0.0.0-20250802141604-12b92425684c` (same date, different short commit hash). Pinned in `go.mod` and reflected in this SUMMARY's frontmatter / decisions.
- **Files modified:** `go.mod`, `go.sum` (already covered by Task 1 commit).
- **Verification:** Module resolves; `grep jackc/pgxlisten go.mod` matches.
- **Committed in:** `eddc840` (Task 1).

**4. [Rule 3 - Blocking] No ffmpeg or sox available on host; pure-Go WAV generator used**
- **Found during:** Task 1, sub-step 5.
- **Issue:** `command -v ffmpeg` and `command -v sox` both returned exit 1 in the worktree environment.
- **Fix:** Followed the plan's explicit "Pure Go fallback" branch. Wrote `/tmp/genwav.go` (a small `package main` that emits a 44-byte WAV header + 32000 silent samples) and ran it via `go run /tmp/genwav.go <out>`. Output: 32044 bytes, RIFF WAVE Microsoft PCM 16-bit mono 16kHz — verified with `file(1)`.
- **Files modified:** `gateway/internal/upstreams/testdata/probe.wav` (created).
- **Verification:** `file probe.wav` reports the expected RIFF/WAVE/PCM signature; `wc -c` returns 32044 (within plan-required 10240–51200 band).
- **Committed in:** `eddc840` (Task 1).

---

**Total deviations:** 4 auto-fixed (all Rule 3 — blocking issues handled with the plan's documented fallbacks or the obvious mechanical fix). Zero behavior or scope changes.
**Impact on plan:** None. All four deviations were either anticipated by the plan (sub-step fallbacks for ffmpeg/sox and pgxlisten pseudo-version) or pure operational adjustments (path layout, scaffold-imports file). No new requirements introduced; no scope creep.

## Issues Encountered

- None during planned work.

## Checkpoint Reached (Task 2)

Task 2 is `type="checkpoint:human-action" gate="blocking"`. Both gates require operator credentials / external infrastructure that is not reachable from this executor's environment:

- **Gate A (OpenRouter Fireworks slug):** Requires `OPENROUTER_API_KEY` (not set in this shell) and a live `POST /v1/chat/completions` against `openrouter.ai`. Operator (Pedro) must mint or paste the key, run the curl, and record the canonical model slug + provider field in `.planning/phases/03-resilience-fallback-chain/03-WAVE0-GATES.md`.
- **Gate B (llama.cpp `/tokenize` endpoint):** Requires a running pod (Vast.ai or local `docker run ghcr.io/ifixtelecom/ifix-ai-pod:develop`). No pod is reachable from this worktree (`curl localhost:8000/tokenize` → connection refused; no matching docker container running). Operator must spin up a pod, run the curl, and record the response shape + image tag.

Both gates are blocking — wave 4 (proxy multi-upstream + tokencount guard) cannot proceed until they resolve. See `<resume-signal>` in the plan: operator types `"approved"` after writing `03-WAVE0-GATES.md` with both gates PASS, or `"blocked: <reason>"` if either fails.

## User Setup Required

**Operator action required for Task 2 (Wave 0 gates).** No `USER-SETUP.md` was generated by this plan; the gate steps are described in detail inside `03-01-PLAN.md` itself (Task 2 `<how-to-verify>` block). Summary of what the operator must do:

1. Mint / locate an `OPENROUTER_API_KEY` (https://openrouter.ai/keys).
2. Run the Gate A curl against `https://openrouter.ai/api/v1/chat/completions` with the Qwen 3.5 27B + Fireworks pin and record the canonical model slug + provider field.
3. Spin up a pod (`docker run ghcr.io/ifixtelecom/ifix-ai-pod:develop` locally or via Vast.ai) and run the Gate B curl against `http://<pod>:8000/tokenize`.
4. Create `.planning/phases/03-resilience-fallback-chain/03-WAVE0-GATES.md` filling the template provided in the plan.
5. Resume execution by typing `approved` (or `blocked: <reason>`).

## Next Phase Readiness

- **Wave 0 scaffolding code is in place.** Downstream test files in waves 2+ that reference `breaker.ErrBreakerOpen`, `upstreams.ErrProbeTimeout`, `proxy.ErrSensitiveRetryExhausted`, `proxy.ErrToolCallPartialStream`, `proxy.ErrContextLengthExceeded`, or `gateway/internal/upstreams/testdata/probe.wav` will now compile.
- **Three pinned deps available** for `gateway/internal/breaker/breaker.go`, `gateway/internal/proxy/dispatcher.go`, and `gateway/internal/upstreams/listen.go` to consume.
- **`gateway/internal/breaker/scaffold_imports.go` is technical debt that the implementation waves MUST clean up.** Each subsequent plan that adds a real consumer for one of the three deps should remove that dep's blank import. Once all three are gone, the file MUST be deleted entirely.
- **Two operator gates remain blocking before Wave 4 can ship the OpenRouter Director and the tokencount guard.** The Phase 3 orchestrator must collect Pedro's `03-WAVE0-GATES.md` artifact before unblocking 03-04 / 03-05 (or whichever plan touches OpenRouter and `/tokenize`).

## Self-Check: PASSED

- File checks:
  - `gateway/internal/breaker/errors.go` — FOUND
  - `gateway/internal/breaker/scaffold_imports.go` — FOUND
  - `gateway/internal/upstreams/errors.go` — FOUND
  - `gateway/internal/upstreams/testdata/probe.wav` — FOUND (32044 bytes)
  - `gateway/internal/proxy/errors.go` — FOUND (modified, 3 new sentinels)
  - `go.mod` / `go.sum` — FOUND (modified, 3 new deps pinned)
- Commit checks:
  - `eddc840` — FOUND in `git log` (Task 1)
- Build / vet:
  - `go build ./...` exit 0
  - `go vet ./...` exit 0
- Acceptance criteria (15 plan checks): all PASS (verified by aggregate grep run before commit).

## TDD Gate Compliance

N/A — plan is `type: execute` (not TDD). Wave 0 ships scaffolding only; tests against these sentinels are written in subsequent waves per `03-VALIDATION.md` Wave 0 inventory.

---
*Phase: 03-resilience-fallback-chain*
*Plan: 01*
*Completed (Task 1): 2026-04-19*
*Task 2: PAUSED awaiting operator gates (see Checkpoint Reached)*
