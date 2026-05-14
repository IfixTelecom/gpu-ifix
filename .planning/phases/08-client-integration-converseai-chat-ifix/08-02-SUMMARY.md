---
phase: 08-client-integration-converseai-chat-ifix
plan: 02
subsystem: integration-smoke
tags: [smoke-test, int-01, gateway-contract, openai-compat]
requires:
  - "gateway /v1/chat/completions + /v1/embeddings (Phase 2)"
  - "converseai tenant key (provisioned via provision-tenants.sh, plan 08-01)"
provides:
  - "scripts/integration-smoke/smoke-converseai.py — INT-01 gateway contract smoke (chat/streaming/tool-calls/embeddings)"
  - "scripts/integration-smoke/report-schema.json — committed JSON Schema for the converseai smoke report, reusable by 08-03"
  - "scripts/integration-smoke/requirements.txt — trimmed Python deps for the integration-smoke scripts"
affects:
  - "Phase 8 HUMAN-UAT plan — runs smoke-converseai.py against the dev gateway and asserts on its JSON report + exit code"
tech-stack:
  added: []
  patterns:
    - "smoke-report + distinct-exit-code contract (mirrors pod/smoke/smoke.py)"
    - "gateway request auth via Authorization: Bearer on httpx.AsyncClient"
    - "secret-once discipline — no committed default api key, argparse error if absent"
key-files:
  created:
    - scripts/integration-smoke/smoke-converseai.py
    - scripts/integration-smoke/report-schema.json
    - scripts/integration-smoke/requirements.txt
  modified: []
decisions:
  - "Embed model alias is `bge-m3` (confirmed against gateway/internal/proxy + gateway/db/README.md — resolves to BAAI/bge-m3); chat alias is `qwen`"
  - "Throwaway __verify_gates.py used to satisfy the verify gate, then deleted (plan left this to executor's call) — directory ships only the 3 deliverables"
metrics:
  duration: ~10m
  completed: 2026-05-14
---

# Phase 8 Plan 02: ConverseAI Integration Smoke Test Summary

INT-01 gateway contract smoke — `smoke-converseai.py` exercises chat / SSE streaming / tool-calls / embeddings against the gateway with the `converseai` tenant key sent as `Authorization: Bearer`, emits a schema-valid JSON report, and maps gate failures to distinct non-zero exit codes.

## What Was Built

### Task 1 — `report-schema.json` + `requirements.txt` (commit `a9fb59d`)
- **`scripts/integration-smoke/report-schema.json`** — JSON Schema (Draft 2020-12), `$id` `https://ifixtelecom.com.br/schemas/integration-smoke/converseai-report/1.0.0`, `additionalProperties: false`. Required top-level keys: `schema_version` (const `"1.0.0"`), `started_at`/`finished_at` (date-time), `target` (`gateway_url` uri + `tenant` string — **not** the api key), `chat`, `chat_stream`, `tool_call`, `embeddings`, `errors`, `gates`. The `gates` sub-object requires `chat_ok`/`streaming_flushes`/`tool_call_valid`/`embeddings_ok`/`all_passed` (all boolean), `additionalProperties: false`. Optional `git_sha` (hex pattern).
- **`scripts/integration-smoke/requirements.txt`** — trimmed to `httpx`, `numpy`, `structlog`, `jsonschema`. Drops the metrics-parser dep from the pod smoke (no DCGM scrape in the gateway smoke). Header notes it covers both `smoke-converseai.py` and `smoke-chat-ifix.py` (plan 08-03).

### Task 2 — `smoke-converseai.py` (commit `9925469`)
- Mirrors `pod/smoke/smoke.py`: module docstring + entry-point convention, `@dataclasses.dataclass Config`, `parse_args()` with argparse + env-var fallbacks + `--fast`, `apply_gates()` / `exit_code_for_gates()` bitmask-to-distinct-code pattern, the report-build + schema-validate + `Path.write_text(json.dumps(..., indent=2, sort_keys=True))` block, `main()`.
- **The one structural change vs the pod smoke:** a single `httpx.AsyncClient(headers={"Authorization": f"Bearer {cfg.api_key}"})` carries the `converseai` tenant key on every request — the gateway requires auth.
- Exercises all 4 INT-01 surfaces:
  - `run_chat()` — non-streaming POST `/v1/chat/completions`, returns `{status_code, ok, raw_error_body?}`.
  - `run_chat_stream()` — streaming POST with `"stream": True`, `aiter_lines()`, TTFT measurement, `data: [DONE]` sentinel; `flushed` is True when ≥2 SSE chunks arrived incrementally (evidence the gateway flushes — `FlushInterval: -1`).
  - `run_tool_call_test()` — the `get_weather` payload + `tool_calls`/`type==function`/`function.name`/`arguments`-is-valid-JSON/`location`-present assertions, copied verbatim from `pod/smoke/smoke.py` lines 204-254.
  - `run_embeddings()` — batched POST `/v1/embeddings` via `asyncio.gather` (10, or 3 under `--fast`), p95 via `np.percentile`.
- `parse_args()` calls `ap.error(...)` and exits non-zero (no network call) when neither `--api-key` nor `SMOKE_API_KEY` is provided — secret-once discipline, no committed default.
- Exit codes: 0 all gates pass; 2/3/4/5 for a single failing gate (chat/streaming/tool-call/embeddings); 6 multiple; 1 fallback.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking issue] requirements.txt comment tripped the verify gate**
- **Found during:** Task 1
- **Issue:** The original `requirements.txt` header comment contained the word "prometheus-client" ("Drops prometheus-client ..."). The plan's verify gate asserts `! grep -qi 'prometheus' requirements.txt` — a case-insensitive match that the comment text triggered, failing the gate.
- **Fix:** Reworded the comment to describe the trim without naming the dropped package ("the metrics-parser dep is gone").
- **Files modified:** `scripts/integration-smoke/requirements.txt`
- **Commit:** `a9fb59d`

**2. [Rule 3 - Blocking issue] importlib of a hyphenated module broke dataclass type resolution**
- **Found during:** Task 2 (verify gate)
- **Issue:** The throwaway `__verify_gates.py` imports `smoke-converseai.py` via `importlib.util` (the hyphen blocks a plain `import`). With `from __future__ import annotations` + `@dataclasses.dataclass`, dataclass type resolution looks the module up in `sys.modules` by `__module__` — and the module wasn't registered there, raising `AttributeError: 'NoneType' object has no attribute '__dict__'`.
- **Fix:** Register the module in `sys.modules` before `exec_module()` in `__verify_gates.py`. This is throwaway-test-harness code only — `smoke-converseai.py` itself is unaffected (it runs as a normal script).
- **Files modified:** `scripts/integration-smoke/__verify_gates.py` (throwaway, deleted after verify passed)
- **Commit:** n/a (file deleted, not committed — see Decisions)

## Verification Results

All plan `<verification>` items pass:
- `smoke-converseai.py` compiles (`py_compile`) and refuses to run without an api key (`ap.error`, exit 2, no network call).
- `report-schema.json` is valid JSON Schema; a well-formed report validates, a report missing `gates` raises `ValidationError`, an unknown top-level key raises `ValidationError` (`additionalProperties: false`).
- `requirements.txt` exists and drops the prometheus-client dep.
- Gate logic verified by the throwaway import-and-assert check: all-pass → `all_passed True` + exit 0; tool-call invalid → exit 4; chat non-200 → exit 2; streaming not flushed → exit 3; embeddings failed → exit 5; multiple → exit 6; distinct codes per single-gate failure; the built report dict validates against `report-schema.json`.

`smoke-converseai.py` is 387 lines (min_lines 150 satisfied). The live run against a deployed gateway is a Phase 8 HUMAN-UAT action — the gateway is not deployed yet (build-gateway blocked on Phase 6 emerg integration tests, per 08-CONTEXT.md Deferred Ideas).

## Threat Mitigations Applied

- **T-08-05 (tenant key as a committed default):** `parse_args()` has no hardcoded key — `--api-key` defaults to `os.getenv("SMOKE_API_KEY")` and `ap.error(...)` exits non-zero when absent. The script cannot run (or be committed) with a baked-in credential.
- **T-08-06 (report leaking the key):** `report-schema.json` `target` carries `gateway_url` + `tenant` only; the api key is not a schema field and `additionalProperties: false` rejects any report that adds it.
- **T-08-07 (gateway error envelope silently passing):** every surface captures `status_code` + a truncated `raw_error_body` on non-200; `apply_gates()` keys `chat_ok` off `chat.ok` (which is `status_code == 200`), so a 401/429/503 flips the gate and yields a distinct non-zero exit code.

## Known Stubs

None. Both deliverables are complete and runnable; the only "not yet" is the live gateway run, which is an out-of-scope HUMAN-UAT action gated on gateway deployment (documented in 08-CONTEXT.md, not a stub).

## Self-Check: PASSED

- `scripts/integration-smoke/smoke-converseai.py` — FOUND
- `scripts/integration-smoke/report-schema.json` — FOUND
- `scripts/integration-smoke/requirements.txt` — FOUND
- Commit `a9fb59d` (Task 1) — FOUND
- Commit `9925469` (Task 2) — FOUND
