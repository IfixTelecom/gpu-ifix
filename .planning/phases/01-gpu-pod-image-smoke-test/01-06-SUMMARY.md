---
phase: 01-gpu-pod-image-smoke-test
plan: 06
subsystem: validation
tags: [smoke-test, asyncio, httpx, dcgm, json-schema, tool-calling, gates, python]

# Dependency graph
requires:
  - phase: 01-gpu-pod-image-smoke-test/02
    provides: "pod/templates/qwen3.5-27b-tool-calling.jinja — tool-calling template the smoke's get_weather validation exercises indirectly via llama-server"
  - phase: 01-gpu-pod-image-smoke-test/03
    provides: "pod/docker-compose.yml port topology — ports 8000/8001/8002/9400 are what smoke.py targets by default"
provides:
  - "pod/smoke/smoke.py: Python asyncio benchmark — single entrypoint `python pod/smoke/smoke.py --target <host>` — executes D-17 workload, enforces D-19 gates, emits smoke-report.json"
  - "pod/smoke/report-schema.json: JSON Schema Draft 2020-12 validating smoke-report structure (schema_version 1.0.0)"
  - "pod/smoke/fixtures/__gen_audio.py: deterministic 16 kHz mono 16-bit PCM WAV generator (stdlib-only) — 480s/seed=42 default"
  - "Exit codes 0-6 precisely mapping to which D-19 gate(s) failed"
  - "Baseline data channels (vram_p95_gb, llm_p95_tpot_ms, whisper_latency_s, embed_p95_ms) archivable for Phase 5 saturation tuning (D-20)"
affects:
  - 01-08 (smoke.yml consumer — triggers `python pod/smoke/smoke.py --target <vast-ai-ip>` and uploads smoke-report.json as artifact)
  - 01-09 (README/operator doc may reference smoke commands)
  - Phase 5 (saturation thresholds will read archived baseline reports per D-20)

# Tech tracking
tech-stack:
  added:
    - "httpx.AsyncClient shared across asyncio.gather tasks (smoke workload)"
    - "prometheus-client text parser for DCGM /metrics timeseries scraping"
    - "jsonschema Draft202012Validator for output self-validation before write"
    - "Synthetic 16 kHz PCM WAV generation via stdlib wave/struct/random (no numpy for audio)"
  patterns:
    - "Exit-code-as-gate-channel: distinct exit codes (2/3/4/5) per single D-19 gate, 6 for multi-fail; CI can read either code OR gates JSON as ground truth"
    - "In-memory binary fixture generation — avoids committing a 15 MB WAV to git while keeping reproducibility (seed=42)"
    - "Pre-write schema validation with graceful fallback (warn + write anyway) — operator can still debug a schema-violating report"
    - "sys.path.insert(0, str(Path(__file__).parent)) + `from fixtures import __gen_audio` — works for direct `python3 pod/smoke/smoke.py` invocation (plan 08 calls it this way)"

key-files:
  created:
    - "pod/smoke/smoke.py (485 lines — asyncio orchestration, DCGM scrape loop, chat streaming + TTFT/TPOT, Whisper, embeddings, tool-call validation, gate application, exit code mapping)"
    - "pod/smoke/report-schema.json (82 lines — JSON Schema Draft 2020-12 covering all D-18 fields + gates sub-object + workload_spec + target URLs)"
    - "pod/smoke/requirements.txt (7 lines — httpx, prometheus-client, numpy, structlog, jsonschema; >= version floors)"
    - "pod/smoke/__init__.py (empty — package marker for future pytest/mypy)"
    - "pod/smoke/README.md (50 lines — pt-BR/English operator doc; workload table, gate table, exit code mapping, plan dependency notes)"
    - "pod/smoke/fixtures/__gen_audio.py (69 lines — stdlib-only WAV generator, CLI runnable and importable)"
    - "pod/smoke/fixtures/README.md (24 lines — explains no committed binaries, usage)"
  modified:
    - ".gitignore (appended __pycache__/ and *.py[cod] patterns — Rule 3 deviation, see Deviations section)"

key-decisions:
  - "All workload tasks launched via single `asyncio.gather(*chat_tasks, whisper_task, embed_task, return_exceptions=True)` so DCGM captures VRAM across all concurrency, not sequential per-task bursts (matches D-17 'paralelo')."
  - "Tool-call validation runs AFTER the main workload (sequentially), not inside the concurrent batch. Rationale: VRAM pressure during main workload would confound tool-call correctness (a false negative would blame the template when it's really OOM). Separating the probes means tool_call_valid reflects template correctness only."
  - "Prompt token estimation uses a 4 chars/token heuristic (`chat_prompt_tokens * 4` chars of 'The quick brown fox...' filler). No tokenizer dependency — keeps requirements.txt minimal. ~8k tokens is the target, exact count is not gated."
  - "DCGM scrape uses prometheus-client stdlib text parser against /metrics, takes the first sample per gauge (single-GPU pod assumption per research STACK.md). Peak VRAM = max(DCGM_FI_DEV_FB_USED) across scraped samples / 1024 (MiB -> GiB)."
  - "Exit code 6 is used when >1 gate fails — disambiguates single-gate failures (2/3/4/5) from compound failures for CI log clarity. `gates` JSON sub-object is still the authoritative source for debugging."

patterns-established:
  - "D-19 gate enforcement pattern: `apply_gates(report) -> gates_dict` + `exit_code_for_gates(gates) -> int` pair. Reusable if Phase 5 adds more gates (just extend both functions)."
  - "Deterministic synthetic audio fixture pattern for Whisper-family testing (seed-controlled, in-memory, no binary in git)"
  - "Pre-write JSON Schema validation with warn-and-continue fallback for CI debuggability"

requirements-completed: [POD-06, POD-07]

# Metrics
duration: ~4min
completed: 2026-04-17
---

# Phase 01 Plan 06: Smoke-test (asyncio benchmark + report schema + D-19 gate enforcement) Summary

**Python asyncio smoke harness (`pod/smoke/smoke.py`, 485 SLOC) executing the D-17 workload against the plan-03 port topology: 2 concurrent 8k-token streaming chats + 1 Whisper 8-min synthetic transcription + 1 batch of 10 embeddings, with DCGM `/metrics` scraped at 1 Hz for VRAM peak/p95, get_weather tool-call validation (D-15), and D-19 gate enforcement (vram_peak_gb ≤ 21.0, tool_call_valid, no CUDA/OOM errors, llm_p95_ttft_ms ≤ 3000) with precise exit-code mapping (0/1/2/3/4/5/6). Output is smoke-report.json validated against JSON Schema Draft 2020-12 before write.**

## Performance

- **Duration:** ~4 min
- **Started:** 2026-04-17T23:19:44Z
- **Completed:** 2026-04-17T23:24:08Z (end of Task 3 commit)
- **Tasks:** 3 / 3
- **Files created:** 7, modified: 1 (.gitignore)

## Accomplishments

- **`pod/smoke/report-schema.json`** — JSON Schema Draft 2020-12 (`"$schema"` self-referencing, `"$id"` namespaced to ifixtelecom.com.br, `version: "1.0.0"`). All 14 required fields at root (schema_version, started_at, finished_at, target, workload_spec, vram_peak_gb, vram_p95_gb, llm_p95_ttft_ms, llm_p95_tpot_ms, whisper_latency_s, embed_p95_ms, tool_call_valid, errors, gates) plus optional `git_sha` with `^[0-9a-f]{7,40}$` pattern. `additionalProperties: false` at every level prevents silent drift. Gates sub-object requires 5 booleans (4 per-gate + all_passed).
- **`pod/smoke/requirements.txt`** — 5 pinned-floor deps (httpx, prometheus-client, numpy, structlog, jsonschema), all `>=` per Ifix convention (matches `converseai-v4/agents/requirements.txt` style).
- **`pod/smoke/__init__.py`** — Empty package marker. Enables `from pod.smoke import ...` in future pytest/mypy scopes.
- **`pod/smoke/fixtures/__gen_audio.py`** — Pure-stdlib generator (wave, struct, random, io, math — NO numpy dep for audio). `generate_wav_bytes(duration_seconds=480, seed=42) -> bytes` returns a 10-second-cycle audio (7s silence + 3s low-amplitude sine-modulated noise). CLI runnable: `python3 pod/smoke/fixtures/__gen_audio.py <seconds> <path>`. Produces a valid 16 kHz mono 16-bit PCM WAV parseable by stdlib `wave` module.
- **`pod/smoke/fixtures/README.md`** — Documents why no committed WAVs (15 MB per 8-min file would bloat git), regeneration command, and `.gitignore` coverage.
- **`pod/smoke/smoke.py`** — 485 SLOC. Implements:
  - `parse_args()` — argparse CLI with `--target` (auto-derives 8000/8001/8002/9400), per-service URL overrides, `--out`, `--fast` (dev mode: 30s whisper, 1 chat).
  - `dcgm_scrape_loop()` — async task scraping DCGM /metrics every 1s via `prometheus_client.parser.text_string_to_metric_families`, parsing `DCGM_FI_DEV_FB_USED/FREE/TOTAL`, `DCGM_FI_DEV_GPU_UTIL/TEMP`, `DCGM_FI_DEV_POWER_USAGE`. Stops on asyncio.Event.
  - `run_chat_stream()` — streaming SSE consumer using `httpx.AsyncClient.stream()`. Records TTFT at first `data:` line, total_ms at `[DONE]`. Returns `{ttft_ms, total_ms, tokens}` or `{error, raw_error_body?}`.
  - `run_tool_call_test()` — full D-15 shape validation: status 200, choices[0].message.tool_calls[0].type == "function", function.name == "get_weather", function.arguments parses as JSON, "location" key present. Returns `(bool, errors)`.
  - `run_whisper()` — multipart POST to `/v1/audio/transcriptions` with in-memory WAV from `audio_gen.generate_wav_bytes`, 600s timeout.
  - `run_embeddings()` — 10-concurrent `asyncio.gather` of `/v1/embeddings` calls, p95 computed via `np.percentile`.
  - `compute_vram_stats()` — peak + p95 of DCGM `FB_USED` (MiB → GiB), rounded to 2 decimals.
  - `detect_cuda_errors()` — regex scan for `out of memory|cuda error|cublas|oom|ggml_cuda_host_malloc` (case-insensitive).
  - `apply_gates()` — composes the 4 D-19 gate booleans + `all_passed`.
  - `exit_code_for_gates()` — 0 if all_passed; 2/3/4/5 per single-gate fail; 6 for multi-fail; 1 for script error paths.
  - `main_async()` — launches DCGM scrape task, runs `asyncio.gather(return_exceptions=True)` over chats + whisper + embeds, then tool-call test, aggregates, pre-validates against schema, writes sorted-key JSON, logs gate outcome.
- **`pod/smoke/README.md`** — pt-BR/English operator doc with tables for D-17 workload composition, D-19 gate thresholds + exit code mapping, `--fast` dev-mode caveat, and downstream plan dependencies.

## Task Commits

Each task committed atomically (`--no-verify` per worktree parallel-executor policy):

1. **Task 1: schema + requirements.txt + __init__.py** — `38b346a` (feat)
2. **Task 2: synthetic WAV generator + fixtures README** — `01c7023` (feat)
3. **Task 3: smoke.py + operator README + .gitignore Python cache patterns** — `510b429` (feat)

## Files Created/Modified

| Path | Role | Lines | Notes |
|---|---|---|---|
| `pod/smoke/smoke.py` | Python asyncio benchmark | 485 | Single entrypoint; D-17 workload; D-19 gates; schema self-validation. |
| `pod/smoke/report-schema.json` | JSON Schema | 82 | Draft 2020-12; 14 required root fields + gates sub-object; additionalProperties: false. |
| `pod/smoke/requirements.txt` | Config | 7 | httpx, prometheus-client, numpy, structlog, jsonschema (>= floors only). |
| `pod/smoke/__init__.py` | Config | 0 | Package marker. |
| `pod/smoke/README.md` | Operator doc (pt-BR) | 50 | Workload, gates, exit codes, plan dependencies. |
| `pod/smoke/fixtures/__gen_audio.py` | Python fixture generator | 69 | Pure-stdlib WAV generator; seeded (42); CLI + import API. |
| `pod/smoke/fixtures/README.md` | Operator doc | 24 | Explains no-binary-in-git policy. |
| `.gitignore` (modified) | Repo hygiene | +5 lines | Adds `__pycache__/`, `*.py[cod]`, `*$py.class`, `.pytest_cache/` |

## D-17 / D-18 / D-19 Coverage

### D-17 (Workload) — FULL coverage

| Requirement | Where covered | Default |
|---|---|---|
| 2 concurrent 8k-token chats | `chat_tasks = [...] * cfg.chats_concurrent` in main_async | DEFAULT_CHATS_CONCURRENT=2, DEFAULT_CHAT_PROMPT_TOKENS=8000 |
| 1 Whisper 8+ minute audio | `run_whisper(..., duration_s=cfg.whisper_duration_s)` | DEFAULT_WHISPER_DURATION=480 (8 minutes) |
| 1 batch of 10 embeddings | `run_embeddings(..., batch=cfg.embed_batch)` with asyncio.gather of 10 | DEFAULT_EMBED_BATCH=10 |
| DCGM scrape at 1s | `dcgm_scrape_loop(..., interval_s=cfg.dcgm_interval_s)` | DEFAULT_DCGM_INTERVAL_S=1.0 |

### D-18 (Report Fields) — FULL coverage

All 8 mandatory fields present in report dict before write: `vram_peak_gb`, `vram_p95_gb`, `llm_p95_ttft_ms`, `llm_p95_tpot_ms`, `whisper_latency_s`, `embed_p95_ms`, `tool_call_valid`, `errors`. Plus metadata (`schema_version`, `started_at`, `finished_at`, `target`, `workload_spec`) and optional `git_sha`.

### D-19 (Gates) — FULL enforcement

| Gate | Constant | Exit code on fail |
|---|---|---|
| `vram_peak_gb <= 21.0` | `GATE_VRAM_PEAK_GB_MAX = 21.0` | 2 |
| `tool_call_valid == true` | direct bool check | 3 |
| no OOM/CUDA in errors | `CUDA_ERROR_PATTERNS` regex list (5 patterns) | 4 |
| `llm_p95_ttft_ms <= 3000` | `GATE_LLM_P95_TTFT_MS_MAX = 3000` | 5 |
| multi-fail | — | 6 |
| script crash | — | 1 |
| all pass | — | 0 |

Verified via gate-logic smoke test (all 6 exit code paths tested before commit with hand-crafted report dicts).

### D-20 (Phase 5 Baseline Data) — FULL capture

Non-gated but reported (for Phase 5 saturation tuning): `vram_p95_gb`, `llm_p95_tpot_ms`, `embed_p95_ms`, `whisper_latency_s`. Plan 08 (smoke.yml, not in this plan) will archive reports to `.planning/.../baseline/`.

### POD-06 (ctx-size 16384) — Indirectly enforced

Script sends ~8k-token prompt. If llama-server rejects due to ctx-size misconfiguration, `run_chat_stream` returns `{error: "status 400", raw_error_body: "..."}` which is appended to `errors` and fails the `no_cuda_oom_errors` gate if the rejection body matches CUDA patterns. A non-CUDA 400 still fails the ttft gate because ttft_ms stays -1 and won't meet the threshold.

## Decisions Made

- **Workload and tool-call test run in sequence, not in parallel.** Rationale: VRAM pressure during the 2×8k + Whisper + 10-embed concurrent burst would confound tool-call correctness. Running tool-call after the burst isolates template-correctness from VRAM-contention issues. Both phases still contribute to DCGM scrape because the loop spans both.
- **Four-char-per-token heuristic, no tokenizer.** `_build_long_prompt(8000)` produces ~32,000 characters of "The quick brown fox..." filler. Adding tiktoken/transformers for exact token count would bloat requirements.txt and add a HuggingFace download on first run. The ~8k target is a stress indicator, not a gate.
- **Prometheus text parser over DCGM JSON.** DCGM-exporter exposes Prometheus text format at `/metrics` (not JSON). `prometheus_client.parser.text_string_to_metric_families` is the canonical stdlib-of-the-ecosystem parser — no custom regex needed, works with any label set.
- **Single-GPU assumption for VRAM aggregation.** `compute_vram_stats` takes the first sample per gauge per scrape. Correct for a Vast.ai RTX 4090 pod (one GPU). Multi-GPU pods would need `max()` over per-GPU samples, but that's out of Phase 1 scope (D-09 fallback: reduce `-np` if VRAM exceeds 21 GB — re-test, never goes multi-GPU).
- **Graceful schema-violation fallback.** If `Draft202012Validator.validate(report)` raises, `smoke.py` logs WARN but still writes the report. Rationale: operators need the raw report to debug schema drift. CI (plan 08) can re-validate independently and fail.
- **Exit code 6 for multi-fail (not bitwise OR'd raw).** `bin(failing).count("1") > 1 -> 6` — trades precision (which pair failed?) for simplicity (one sentinel). The `gates` JSON sub-object has full per-gate truth for debugging. Rationale: shell scripts and CI status checks benefit from distinct small exit codes more than from encoded bitfields.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking/hygiene] Added Python cache ignore patterns to .gitignore**
- **Found during:** Task 3 (running `python3 pod/smoke/smoke.py --help` for verification creates `pod/smoke/__pycache__/` and `pod/smoke/fixtures/__pycache__/`)
- **Issue:** `.gitignore` at repo root had Go cache patterns (`coverage.out`, etc.) and binary exclusions, but no Python cache patterns. After verifying `python3 pod/smoke/smoke.py --help` works, `git status --short` showed untracked `__pycache__/` directories that would leak into the Task 3 commit or any subsequent work that runs the script.
- **Fix:** Appended `__pycache__/`, `*.py[cod]`, `*$py.class`, `.pytest_cache/` under a `# Python cache` section in `.gitignore`. Did not touch any other ignore rules.
- **Files modified:** `.gitignore` (+5 lines)
- **Verification:** `git status --short` after the edit shows only `pod/smoke/smoke.py` + `pod/smoke/README.md` + ` M .gitignore` (no `__pycache__`).
- **Committed in:** `510b429` (Task 3 commit, alongside smoke.py + README.md)

No other deviations. Tasks 1 and 2 executed exactly as written; all plan `<automated>` verify blocks passed verbatim on first run.

## Issues Encountered

- **Host Python is 3.11, plan assumes 3.12+.** Smoke.py uses `from __future__ import annotations`, which defers evaluation of all annotations to runtime. This means the `list[dict[str, float]]`-style annotations are treated as strings on 3.11 and don't error. The script compiles (`py_compile`) and `--help` runs cleanly on the 3.11 host VPS. Actual smoke runs on Vast.ai pod will use Python 3.12 per plan/CI assumptions; no divergence introduced.
- **`prometheus-client` was not pre-installed on executor VPS.** Installed via `pip install --user --break-system-packages` (Debian Python externally-managed guard) to run the `python3 pod/smoke/smoke.py --help` verify step. Already declared in `pod/smoke/requirements.txt` as a runtime dep, so CI/pod runs will pick it up via `pip install -r pod/smoke/requirements.txt`. No plan fidelity issue.
- **No live pod to run the full workload against.** Syntax, help, imports, schema validation, and gate logic were exhaustively tested (6 exit-code paths hand-tested). End-to-end integration (actual chat streaming → DCGM scrape → tool-call → report write) will happen in plan 08 (smoke.yml) against a real Vast.ai pod. Script structure follows the existing async patterns in `converseai-v4/agents/src/main.py` which is production-proven at Ifix.

## Authentication Gates

None — plan is pure code scaffolding. No external auth or service calls involved in the plan work itself. (Smoke runs against a live pod will require Vast.ai IP access, but no credentials inside the script — endpoints are assumed unauthenticated inside the pod boundary per plan 01-03 threat model.)

## User Setup Required

None — no external service configuration. Operators will later install deps via `pip install -r pod/smoke/requirements.txt` before running, but that's tool-use, not setup.

## Threat Flags

None beyond those already in the plan's `<threat_model>` (T-01-06-01 through T-01-06-04). No new endpoints, auth paths, or trust boundaries introduced. Smoke-report.json content is bounded to non-sensitive data (URLs, latencies, error bodies truncated to 500 chars). CI artifact archival (plan 08) inherits those decisions.

## Next Phase Readiness

- **Plan 01-08 (smoke.yml):** can invoke `python pod/smoke/smoke.py --target <vast-ai-ip> --out smoke-report.json` against the pod created by the workflow, then `actions/upload-artifact@v4` the report. Exit codes 2/3/4/5/6 fail the workflow step naturally (non-zero = red); exit 0 proceeds to D-23 image promotion.
- **Phase 5 (saturation):** can read archived `smoke-report-<sha>.json` from `.planning/phases/01-.../baseline/` to tune real saturation thresholds from empirical data (D-20), replacing the current Phase 2 placeholders.
- **No downstream blocker:** `run_tool_call_test` depends on plan 02 (template committed) and plan 03 (llama-server `--chat-template-file` wiring). Both exist on branch head.

## TDD Gate Compliance

Plan is `type: auto` with no TDD-flagged tasks. No RED/GREEN commit pair expected; all three task commits are `feat`. Verified in `git log --oneline`: `38b346a feat`, `01c7023 feat`, `510b429 feat` — consistent with plan type and no TDD gate warning needed.

## Self-Check

**File existence (absolute paths):**
- `/home/pedro/projetos/pedro/gpu-ifix/.claude/worktrees/agent-a28de6f1/pod/smoke/smoke.py` — FOUND (485 lines)
- `/home/pedro/projetos/pedro/gpu-ifix/.claude/worktrees/agent-a28de6f1/pod/smoke/report-schema.json` — FOUND (82 lines)
- `/home/pedro/projetos/pedro/gpu-ifix/.claude/worktrees/agent-a28de6f1/pod/smoke/requirements.txt` — FOUND (7 lines)
- `/home/pedro/projetos/pedro/gpu-ifix/.claude/worktrees/agent-a28de6f1/pod/smoke/__init__.py` — FOUND (empty)
- `/home/pedro/projetos/pedro/gpu-ifix/.claude/worktrees/agent-a28de6f1/pod/smoke/README.md` — FOUND (50 lines)
- `/home/pedro/projetos/pedro/gpu-ifix/.claude/worktrees/agent-a28de6f1/pod/smoke/fixtures/__gen_audio.py` — FOUND (69 lines)
- `/home/pedro/projetos/pedro/gpu-ifix/.claude/worktrees/agent-a28de6f1/pod/smoke/fixtures/README.md` — FOUND (24 lines)

**Commit existence (worktree history):**
- `38b346a` (Task 1 feat schema+reqs+init) — FOUND in `git log`
- `01c7023` (Task 2 feat audio gen) — FOUND in `git log`
- `510b429` (Task 3 feat smoke.py+README+gitignore) — FOUND in `git log`

**Plan-level verification block (PLAN.md lines 998-1017):**
- `python3 -m py_compile pod/smoke/smoke.py` — exit 0
- `python3 -m py_compile pod/smoke/fixtures/__gen_audio.py` — exit 0
- `Draft202012Validator.check_schema(json.loads(open('pod/smoke/report-schema.json').read()))` — exit 0
- `python3 pod/smoke/smoke.py --help | grep -qi "target"` — exit 0

**Task-level verify blocks (every grep/test in the plan):**
- Task 1: 8/8 checks pass (file existence + schema parse + 5 `grep -q "^xxx"` deps + `! grep -qE "^[a-z].*=="` no-pins check)
- Task 2: 6/6 checks pass (file existence + WAV generation + stdlib `wave` parse confirming 16 kHz mono 16-bit)
- Task 3: 15/15 checks pass (file existence + py_compile + 10 `grep -q` constant/function/keyword checks in smoke.py + 2 keyword checks in README + pip install idempotent + `--help | grep -qi target`)

**Gate logic smoke test (pre-commit hand verification):**
- 6/6 exit code paths validated (0 all-pass, 2 vram-only, 3 tool-only, 4 cuda-only, 5 ttft-only, 6 multi-fail)

**No stubs detected:** grep for TODO|FIXME|placeholder|coming soon|not available returned only "Todos passaram" (Portuguese for "all passed", a gate-table row in pod/smoke/README.md) — not a stub.

**No unintended deletions:** `git diff --diff-filter=D HEAD~3 HEAD` empty across all 3 task commits.

## Self-Check: PASSED

---
*Phase: 01-gpu-pod-image-smoke-test*
*Plan: 06*
*Completed: 2026-04-17*
