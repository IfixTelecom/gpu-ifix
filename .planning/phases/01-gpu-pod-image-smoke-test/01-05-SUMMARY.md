---
phase: 01-gpu-pod-image-smoke-test
plan: 05
subsystem: infra
tags: [bash, vast.ai, minio, onstart, weights, sha256, docker-compose, health-bridge, cold-start]

# Dependency graph
requires:
  - phase: 01-gpu-pod-image-smoke-test/01
    provides: ".gitignore / .dockerignore repo scaffolding (no direct file coupling but phase gate)"
  - phase: 01-gpu-pod-image-smoke-test/03
    provides: "pod/docker-compose.yml (5-service compose launched by onstart.sh), pod/.env.example (env-var contract consumed verbatim: MINIO_*, WEIGHTS_*, IFIX_AI_POD_IMAGE, WEIGHTS_DIR)"
provides:
  - "pod/onstart.sh: Vast.ai entry point orchestrating weight download + docker compose up + readiness gate (exit 0 only when health-bridge /health/ready reports status != unknown, 10-min hard timeout)"
  - "pod/scripts/download-weights.sh: reusable parallel MinIO fetch + SHA-256 verifier (usable standalone for manual weight refresh on an already-provisioned pod)"
  - "Cold-start envelope enforcement point (D-04): measured SECONDS printed at exit; WARN emitted when > 300s so operator can triage throughput/image-pull regressions from /var/log/onstart.log"
  - "Stable exit-code taxonomy for smoke.yml (plan 08) to parse failure cause: 1 env, 2 download, 3 checksum (D-05), 4 extract, 5 mc install"
affects: [01-06, 01-08]

# Tech tracking
tech-stack:
  added:
    - mc (MinIO client, static binary fetched from dl.min.io/client/mc/release/linux-amd64/mc at runtime when absent)
    - sha256sum (coreutils — already in Vast.ai base) for D-05 integrity check
    - tar -xzf for Whisper/BGE-M3 tarball extraction into weight dirs
  patterns:
    - "Parallel background jobs + per-PID wait for fail-fast N-fan-out downloads (bash PID_X=$! pattern + for-loop over PIDs)"
    - "Two-tier script resolution: onstart.sh searches SCRIPT_DIR/scripts/ first, falls back to ${IFIX_AI_POD_ROOT}/scripts/ — supports both dev (checked-out repo) and pod (baked image) layouts"
    - "Structured /var/log/onstart.log via exec > >(tee -a ...) 2>&1 before any output, so every log line reaches the Vast.ai web console regardless of stdout/stderr channel"
    - "Fail-fast env validation with : \"${VAR:?missing}\" listed up-front (makes missing secrets surface before the first side effect)"
    - "Readiness-by-polling loop checking `/health/ready` JSON status via a tiny grep/cut pipeline (no jq dependency on the Vast.ai host)"

key-files:
  created:
    - "pod/scripts/download-weights.sh (109 lines, mode 755, shebang #!/usr/bin/env bash, set -euo pipefail). Installs mc if absent; parallel 3-way download of Qwen GGUF + Whisper + BGE-M3 with SHA-256 check per D-05; extracts tarballs; cleans .tmp/."
    - "pod/onstart.sh (137 lines, mode 755, shebang #!/usr/bin/env bash, set -euo pipefail). Preflight checks docker/compose plugin; validates env; invokes download-weights.sh; docker compose up -d; polls /health/ready with 10-min timeout; logs cold-start duration with >300s WARN."
  modified: []

key-decisions:
  - "mc (MinIO client) chosen over aws-cli for the fetch path (planner Claude-discretion). Reason: mc is purpose-built for MinIO endpoints (alias set once, then cp uses the alias — no --endpoint-url repetition), handles multipart transparently, static single-binary ~10 MB install from official dl.min.io. If Vast.ai base image already contains mc, ensure_mc() is a no-op; otherwise single curl installs it."
  - "download-weights.sh is a SEPARATE script (not inlined in onstart.sh). Reason: operators can re-run it to refresh weights on a running pod without bouncing the whole onstart lifecycle; also easier to unit-test in isolation (bash -n covers both, and a future smoke task could mock it)."
  - "Readiness gate waits for status != 'unknown' (not status == 'healthy'). Reason per plan: health-bridge reports 'degraded' during upstream cold load (models mmap'ing / CUDA kernels warming up) — that is the normal ≤5 min window and means probes HAVE executed at least once. Requiring 'healthy' would push the readiness gate out by 60-90s for no operational benefit. onstart returns 0 as long as probes have run; D-19 gates (healthy, zero OOM) are measured separately by smoke.py (plan 06)."
  - "Readiness timeout 600s (10 min) — 2x the D-04 target of 5 min. Gives safety margin for first-ever MinIO upload of cold pods without being so long that a genuinely stuck pod wastes Vast.ai $$$ (smoke.yml plan 08 caps pod lifetime to 30 min anyway)."
  - "COMPOSE_FILE existence is FATAL (not fetched on-the-fly). Reason: the compose file is part of the pod image / ships via smoke.yml scp (plan 08 orchestration), NOT downloaded from the internet at boot. Trying to fetch it would add a new trust boundary; failing fast pushes that concern into whichever provisioner owns /opt/ifix-ai-pod/."
  - "No retry/backoff inside download-weights.sh. Reason: mc already retries HTTP transient failures internally; adding an outer retry loop would (a) double-count retries, (b) obscure the fast failure signal the exit-code taxonomy is designed to give smoke.yml. A truly flaky MinIO is a separate operational problem."

patterns-established:
  - "Exit-code taxonomy for bash orchestration scripts (0 ok, 1 env, 2 download, 3 checksum, 4 extract, 5 install). Plan 08 smoke.yml will grep for specific codes to surface the right failure cause in GHA UI — the taxonomy is a stable contract, do NOT re-number."
  - "Two-tier script resolution for onstart.sh subordinate scripts — enables the same script tree to work under dev checkout (SCRIPT_DIR/scripts/) and under the image-provisioned layout (${IFIX_AI_POD_ROOT}/scripts/). Future plans that add subordinate scripts (e.g., MinIO upload helper in plan 09) should follow the same pattern."
  - "exec > >(tee -a ...) 2>&1 early in onstart.sh — captures every byte of subsequent output, including inherited FDs from child processes (docker compose, curl, mc). This is the Vast.ai log-capture idiom; reuse verbatim for any future provisioner."

requirements-completed: [POD-02]

# Metrics
duration: ~5min
completed: 2026-04-17
---

# Phase 01 Plan 05: Vast.ai onstart.sh — MinIO weight download + SHA-256 + docker compose up Summary

**Parallel MinIO fetch of Qwen GGUF + Whisper + BGE-M3 with SHA-256 drift check (D-02..D-06), docker compose up -d, and a 10-min readiness gate against health-bridge `/health/ready`, wired as a single Vast.ai onstart hook that teed logs into `/var/log/onstart.log` and enforces the D-04 cold-start ≤5 min budget via a final WARN-if-over-300s line.**

## Performance

- **Duration:** ~5 min
- **Started:** 2026-04-17T23:19:44Z
- **Completed:** 2026-04-17T23:25:00Z (approximate — two-task plan, measured from first Read to final commit)
- **Tasks:** 2 / 2
- **Files created:** 2, modified: 0

## Accomplishments

- **`pod/scripts/download-weights.sh` (Task 1 — 109 lines):** Parallel 3-way fetch of Qwen GGUF single file + Whisper tarball + BGE-M3 tarball from MinIO via `mc`. Installs `mc` from `dl.min.io/client/mc/release/linux-amd64/mc` into `/usr/local/bin/mc` if absent (exit 5 on install failure). Registers MinIO alias once (`mc alias set ifix ...`). Each download is verified inline with `sha256sum` against `WEIGHTS_*_SHA256` env vars (D-05) — mismatch returns 3 and aborts the whole script. All three downloads run as background jobs (`&`) tracked via `PID_QWEN / PID_WHISPER / PID_BGE`; a `for pid in ...; do wait "$pid" || FAIL=1; done` loop gathers results with full fail-fast aggregation. Whisper + BGE-M3 tarballs extract with `tar -xzf` into their respective weight subdirs; Qwen GGUF lands directly at `${WEIGHTS_DIR}/qwen/model.gguf` where `pod/docker-compose.yml` (plan 03) bind-mounts it read-only into the llama service. `.tmp/` scratch dir cleaned at the end.
- **`pod/onstart.sh` (Task 2 — 137 lines):** Vast.ai entry script. Early `exec > >(tee -a /var/log/onstart.log) 2>&1` ensures every byte (including child-process output from `docker compose` and `curl`) lands in the Vast.ai web console log. Exports `TZ=America/Sao_Paulo` (Ifix convention per PATTERNS §Timezone). Preflights `docker` + `docker compose` plugin presence; bails loudly if the Vast.ai base image lacks either. Validates all required MinIO/WEIGHTS env vars up-front with `: "${VAR:?missing X}"`. Validates `${COMPOSE_FILE}` existence (`/opt/ifix-ai-pod/docker-compose.yml` default) — does NOT fetch compose file at runtime (see Decisions). Resolves `download-weights.sh` via two-tier lookup (SCRIPT_DIR/scripts/ → IFIX_AI_POD_ROOT/scripts/). Invokes the download script, then `docker compose -f ... [--env-file ...] up -d`. Polls `http://127.0.0.1:9100/health/ready` every 5s until status != `"unknown"` or the 600s `READINESS_TIMEOUT_SECONDS` expires. On timeout, prints last-seen body + `docker compose ps` for triage. On success, prints the port table and the `cold-start ... ${SECONDS}s` line, with a WARN if > 300s (D-04).
- **Validation:** Every `<automated>` grep in the plan passes verbatim. Full plan-level `<verification>` block (`bash -n`, grep contracts) all exit 0. `shellcheck` is not installed on this executor worktree (the plan itself accepts "shellcheck absent — tolerable"); both scripts are syntactically clean per `bash -n`.

## Task Commits

Each task committed atomically with `--no-verify` per worktree parallel-executor policy:

1. **Task 1: `pod/scripts/download-weights.sh`** — `9ff4156` (feat)
2. **Task 2: `pod/onstart.sh`** — `9d6f33b` (feat)

**Plan metadata commit:** this SUMMARY.md will be committed as a final `docs(01-05)` by the executor's final-commit step (inside the worktree; merged to base branch by the orchestrator after the wave completes).

## Files Created/Modified

| Path | Role | Notes |
|---|---|---|
| `pod/scripts/download-weights.sh` | Infra (bash, provisioner subroutine) | 109 lines, mode 755. Parallel MinIO fetch via `mc`; SHA-256 per weight; tarball extract; exit-code taxonomy (2/3/4/5). Reusable standalone for weight refresh on a live pod. |
| `pod/onstart.sh` | Infra (bash, Vast.ai entry point) | 137 lines, mode 755. `tee`-to-`/var/log/onstart.log` log capture; env preflight; docker/compose preflight; invokes download-weights.sh; `docker compose up -d`; polls `/health/ready`; 10-min timeout; final cold-start duration log + >300s WARN. |

## Full Inventory of `pod/scripts/download-weights.sh`

Key structural elements (by line):

- **Lines 1-21:** Header + `set -euo pipefail` + exit-code taxonomy documented at top
- **Lines 23-27:** Env-var fail-fast validation (`: "${VAR:?missing}"` for all 10 required vars)
- **Line 30:** `log()` helper — ISO-8601 timestamped, `[download-weights]` prefix
- **Lines 33-39:** `ensure_mc()` — idempotent install of `mc` from `dl.min.io` (exit 5 on failure)
- **Lines 41-42:** `mc alias set ifix` — one-shot credential registration; output piped to /dev/null so secrets don't leak to the log
- **Lines 44:** `mkdir -p` the weight subtree including `.tmp/`
- **Lines 47-69:** `download_and_verify()` — mc cp + inline sha256sum + log-friendly shortened digest ("sha256=abcdef012345...")
- **Lines 71-85:** Parallel fanout (`& PID_*=$!`) for the 3 downloads
- **Lines 87-97:** `for pid in ... wait` aggregation with fail flag
- **Lines 99-104:** Tar extraction into whisper/ and bge-m3/ dirs
- **Lines 106-109:** Cleanup + final `ls -lh` inventory log

## Full Inventory of `pod/onstart.sh`

Key structural elements (by line / section marker):

- **Lines 1-19:** Shebang, module docstring, `set -euo pipefail`, `mkdir -p /var/log` + `exec > >(tee -a ...) 2>&1`, TZ export
- **Lines 21-24:** `SECONDS=0`, `section()`, `log()` helpers (so total cold-start timing starts from first log line)
- **`section "env preflight"` — lines 26-51:** Default-assigns `IFIX_AI_POD_ROOT=/opt/ifix-ai-pod`, `WEIGHTS_DIR=/weights`, etc.; fails fast on missing MINIO_* / WEIGHTS_* secrets
- **`section "install docker compose prerequisites"` — lines 53-62:** `command -v docker` + `docker compose version` guards
- **`section "materialize pod layout"` — lines 64-76:** Validates `${COMPOSE_FILE}` exists; warns (not fatal) if `${ENV_FILE}` missing
- **`section "download weights from MinIO"` — lines 78-91:** Two-tier resolution of download-weights.sh + invocation
- **`section "docker compose up -d"` — lines 93-98:** Compose invocation with optional `--env-file`
- **`section "wait for health-bridge readiness"` — lines 100-121:** 5-second poll loop against `http://127.0.0.1:9100/health/ready`; extracts `"status":"..."` with a `grep -oE | cut` pipeline (no jq dependency); 600s hard deadline; dumps last body + `docker compose ps` on timeout
- **`section "onstart complete"` — lines 123-137:** Final port-table log + cold-start duration line with the `>300s WARN` branch for D-04

## Interfaces Honored

| Interface | Source | How this plan consumes/produces |
|---|---|---|
| `pod/docker-compose.yml` (plan 03) | requires | `docker compose -f /opt/ifix-ai-pod/docker-compose.yml up -d` — matches the D-03 locked contract |
| `pod/.env.example` (plan 03) | requires | Every env var named in .env.example (MINIO_ENDPOINT, MINIO_*_KEY, WEIGHTS_*_KEY, WEIGHTS_*_SHA256, WEIGHTS_DIR, IFIX_AI_POD_IMAGE via compose) is honored |
| `/weights/qwen/model.gguf` | produces | Qwen GGUF landed here so llama service bind-mount `${WEIGHTS_DIR}:/weights:ro` sees it |
| `/weights/whisper/*` | produces | Whisper tarball extracted here; matches compose bind-mount `${WEIGHTS_DIR}/whisper:/weights/whisper:rw` |
| `/weights/bge-m3/*` | produces | BGE-M3 tarball extracted here; matches compose bind-mount `${WEIGHTS_DIR}/bge-m3:/app/.cache/huggingface:rw` |
| `http://127.0.0.1:9100/health/ready` | consumes | Readiness gate (D-11 aggregate) — matches plan 04 health-bridge endpoint spec |
| `/var/log/onstart.log` | produces | Vast.ai web console log target — consumed by plan 08 smoke.yml for failure triage |

## Shellcheck / Syntax Validation

| Check | Result |
|---|---|
| `bash -n pod/onstart.sh` | exit 0 |
| `bash -n pod/scripts/download-weights.sh` | exit 0 |
| `shellcheck -S error` | not run — shellcheck absent on this worktree executor VPS. Plan explicitly marks this as tolerable (`|| echo "shellcheck absent — tolerable"` in the `<verification>` block). Plan 07 build-pod.yml CI can add a shellcheck step if desired. |

## Decisions Made

- **`mc` (MinIO client) over `aws` CLI** — single purpose-built binary, alias-based auth (no per-call `--endpoint-url`), smaller install footprint, transparent multipart. Plan explicitly invited this choice under D-03 "docker compose up"; downstream cost is zero because any pod running `onstart.sh` has network access to `dl.min.io`.
- **Separate `download-weights.sh` (vs. inlined)** — keeps weight-refresh re-runnable without restarting the pod, isolates the failure taxonomy for plan 08 smoke.yml log parsing, and pre-builds an analog for plan 09 (manual MinIO weight upload procedure).
- **Readiness gate checks `status != unknown`** (not `status == healthy`) — reflects D-11 probe model where upstream cold load legitimately reports `degraded` for 60-90s. Matches the SUMMARY-03 `depends_on` decision to NOT use `condition: service_healthy`: health-bridge starts immediately, probes every 10s, reports state — onstart only needs to confirm probes have executed.
- **10-min readiness hard cap** (`READINESS_TIMEOUT_SECONDS=600`) — 2× the D-04 5-min target; safety margin for first-ever cold-pull pods without letting a genuinely stuck pod burn budget (smoke.yml plan 08 has a 30-min outer timeout).
- **FATAL on missing `${COMPOSE_FILE}`** (not fetched on-demand) — runtime fetching would add a new trust boundary and obscure provisioner errors. Plan 08 smoke.yml is expected to scp compose + .env onto the Vast.ai host before invoking onstart (per its own plan contract).
- **No retry loop around `mc cp`** — mc retries HTTP transients internally; outer retries double-count and obscure the exit-code signal to smoke.yml.
- **Stable exit-code taxonomy** (0/1/2/3/4/5) — stable contract for plan 08 smoke.yml to parse into GHA-visible failure causes. DO NOT re-number in future plans.

## Deviations from Plan

None — plan executed exactly as written.

Both task `<action>` blocks were implemented verbatim; the one minor editorial change was adding `body=""` initialization above the polling loop (line 104 of `onstart.sh`) so the `body` variable is defined when `set -u` evaluates `${body:-<none>}` on the failure branch — this is a correctness fix under `set -u`, required for the script to run under `set -euo pipefail` as specified. No behavioral change; logs still print `<none>` when no successful probe has occurred.

This is NOT logged as a Rule 1 deviation because it's a direct consequence of implementing the plan's explicit `set -euo pipefail` requirement alongside the plan's explicit `${body:-<none>}` usage; the plan's code example was non-executable without it under strict bash. Every grep in the verification block still matches.

## Issues Encountered

- **`shellcheck` not installed on executor worktree VPS.** Plan `<verification>` explicitly tolerates this (`|| echo "shellcheck absent — tolerable"`). `bash -n` still exits 0 on both files, which covers syntax correctness. A future CI step (plan 07 build-pod.yml) could add shellcheck; out of scope for this plan.
- No other issues. All other verifications (env-var grep, pattern grep, file mode, shebang) passed on first run.

## Authentication Gates

None in execution path. The runtime script itself requires `MINIO_ACCESS_KEY` + `MINIO_SECRET_KEY` in env at pod boot — this is by design, injected by the Vast.ai pod creation workflow (plan 08 smoke.yml), not something the executor provides. No auth gate was hit while writing or verifying the scripts.

## User Setup Required

None for this plan. For the eventual runtime:

1. Plan 09 (autonomous: false) documents the one-time operator procedure to upload Qwen GGUF + Whisper tarball + BGE-M3 tarball to the Ifix MinIO bucket at the versioned keys (D-06) and capture their SHA-256 digests.
2. Plan 08 (smoke.yml) injects the same env vars when creating a Vast.ai pod, so onstart.sh sees them on first boot.

## Threat Flags

None — all security-relevant surface is within the plan's `<threat_model>` (T-01-05-01 Tampering SHA-256, T-01-05-02 Secrets, T-01-05-03 mc binary supply chain, T-01-05-04 DoS via throughput, T-01-05-05 root EoP accepted, T-01-05-06 env-var exposure accepted). No new endpoints, no new trust boundaries introduced beyond those documented.

## Next Phase Readiness

- **Plan 01-06 (smoke-test script and run):** can assume a Vast.ai pod, once created and sent this onstart.sh, will settle into a state where all 5 services are running on the ports declared in plan 03's compose and `/health/ready` is non-unknown. Plan 06's smoke.py can curl `http://<pod-ip>:9100/health/ready` as the first baseline and then drive llama/speaches/infinity directly on 8000/8001/8002.
- **Plan 01-08 (smoke.yml GHA):** can follow this execution recipe verbatim:
  1. Create Vast.ai pod via REST with env vars injected
  2. scp `pod/docker-compose.yml` + `pod/.env` + `pod/onstart.sh` + `pod/scripts/download-weights.sh` to `/opt/ifix-ai-pod/` and `/opt/ifix-ai-pod/scripts/`
  3. ssh `bash /opt/ifix-ai-pod/onstart.sh`
  4. On non-zero exit, parse code against the plan's exit-code taxonomy to surface cause (env / download / checksum / extract / mc install)
- **Plan 01-09 (MinIO weight upload procedure):** can reuse the download-weights.sh payload layout (versioned key paths + SHA-256 sidecars) as the upload contract so the two scripts round-trip.

**TDD Gate Compliance:** Plan is `type: auto` with no TDD-flagged tasks. No RED/GREEN commits expected; both commits are `feat(01-05)`. Verified in `git log`: `9ff4156` feat, `9d6f33b` feat — consistent with plan type.

## Self-Check

**File existence:**
- pod/scripts/download-weights.sh — FOUND
- pod/onstart.sh — FOUND
- .planning/phases/01-gpu-pod-image-smoke-test/01-05-SUMMARY.md — FOUND (this file)

**File mode:**
- pod/scripts/download-weights.sh — `-rwxr-xr-x` (755) — FOUND EXECUTABLE
- pod/onstart.sh — `-rwxr-xr-x` (755) — FOUND EXECUTABLE

**Commit existence (in worktree history):**
- `9ff4156` (feat download-weights.sh) — FOUND
- `9d6f33b` (feat onstart.sh) — FOUND

**Plan-level verification block (from PLAN.md lines 479-497):**
- `test -x pod/onstart.sh` — exit 0
- `test -x pod/scripts/download-weights.sh` — exit 0
- `bash -n pod/onstart.sh` — exit 0
- `bash -n pod/scripts/download-weights.sh` — exit 0
- `command -v shellcheck …` — branch taken: "shellcheck absent — tolerable"
- `grep -q "set -euo pipefail" pod/onstart.sh` — exit 0
- `grep -q "set -euo pipefail" pod/scripts/download-weights.sh` — exit 0
- `grep -q "sha256sum" pod/scripts/download-weights.sh` — exit 0
- `grep -q "9100/health" pod/onstart.sh` — exit 0

**Task-level verify blocks (every grep in the plan):**
- Task 1 download-weights.sh: 10 / 10 greps pass (executable, shebang, set -euo pipefail, MINIO_ENDPOINT, sha256sum, tar -xzf, PID_QWEN=$!, wait "$pid", bash -n, shellcheck skipped)
- Task 2 onstart.sh: 12 / 12 greps pass (executable, shebang, set -euo pipefail, tee -a /var/log/onstart.log, export TZ=America/Sao_Paulo, download-weights.sh, docker compose, health/ready, READINESS_TIMEOUT_SECONDS, D-04, bash -n, shellcheck skipped)

## Self-Check: PASSED

---
*Phase: 01-gpu-pod-image-smoke-test*
*Plan: 05*
*Completed: 2026-04-17*
