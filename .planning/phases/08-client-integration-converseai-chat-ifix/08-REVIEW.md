---
phase: 08-client-integration-converseai-chat-ifix
reviewed: 2026-05-14T00:00:00Z
depth: standard
files_reviewed: 6
files_reviewed_list:
  - scripts/integration-smoke/provision-tenants.sh
  - scripts/integration-smoke/smoke-converseai.py
  - scripts/integration-smoke/smoke-chat-ifix.py
  - scripts/integration-smoke/report-schema.json
  - scripts/integration-smoke/chat-ifix-report-schema.json
  - scripts/integration-smoke/requirements.txt
findings:
  critical: 0
  warning: 0
  info: 5
  total: 5
status: clean
---

# Phase 8: Code Review Report

**Reviewed:** 2026-05-14
**Depth:** standard
**Files Reviewed:** 6
**Status:** clean

## Summary

Re-review (iteration 1 of the `--auto` fix loop). The prior `08-REVIEW.md` raised 2 BLOCKER + 7 WARNING + 5 INFO findings against the three Phase-8 integration-smoke artifacts. The `gsd-code-fixer` applied fixes for all 9 BLOCKER+WARNING findings. This pass re-reads the current code (now including both committed JSON schemas and `requirements.txt`), cross-checks the bash CLI contract against the committed `gatewayctl` binary via `strings`, and traces every emitted report field against its `additionalProperties: false` schema.

**Verdict: all 2 BLOCKERs and all 7 WARNINGs are resolved, and the fixes introduced no new defects.** The 5 INFO items from the prior review remain valid non-blocking observations and are carried forward unchanged below — they are out of scope for the `--auto` fix loop and do not affect the `clean` status.

### BLOCKER verification

- **CR-01 (streaming result carries `status_code` + unflushed-200 diagnostic + schema):** RESOLVED. `run_chat_stream` now returns `status_code` on all three return paths — success (`smoke-converseai.py:191`, bound from `r.status_code` at `:170`), non-200 (`:167`), and exception (`:184`, hard `-1`). The `status_code` variable is provably bound on every path that reaches the success `return` (the non-200 path returns at `:163` before the binding; an exception is caught at `:179` and returns its own dict). `main_async` (`:364-374`) now synthesises a diagnostic from `status_code` + `chunks` whenever `flushed` is False and there is no `raw_error_body`, so an exit-3 streaming failure can no longer surface an empty `errors` array. `report-schema.json` `chat_stream` (`:52-58`) gained `status_code` in both `required` and `properties`.

- **CR-02 (anchored idempotency grep):** RESOLVED. `provision-tenants.sh:118` is now `grep -qF "tenant slug '$slug' already exists"`. Binary inspection confirms `gatewayctl` emits the exact literal `error: tenant slug '%s' already exists`, and that the binary *also* contains the coincidental substrings `-- already exists from migration 0002.` and `file already exists` — precisely the loose-match collisions the prior review flagged. The anchored fixed-string match with the interpolated slug correctly distinguishes the intended idempotency signal from unrelated exit-1 failures.

### WARNING verification

- **WR-01 (`parse_key` single-line assert):** RESOLVED. `provision-tenants.sh:147-152` asserts `grep -c '^key='` equals exactly 1 before extracting. Binary `strings` confirms the mint block is exactly `key=%s\nid=%s\nprefix=%s` (one `^key=` line) for both `key create` and `admin-key create` — the happy path passes the assertion. `grep -c` always emits a numeric count, so the `[[ -eq ]]` test is safe.
- **WR-02 (`parse_id` orphan-audit logging):** RESOLVED. `parse_id` (`:158-160`) extracts the non-secret `id=` and is logged to stderr after each of the three mints (`:172`, `:181`, `:190`), leaving an audit trail of created rows on a mid-sequence failure. The raw `key=` value is never logged.
- **WR-03 (GATEWAYCTL bash array):** RESOLVED. `GATEWAYCTL` is now an array: default `(gatewayctl)` at `:56`, populated via `IFS=' ' read -r -a` at `:62`, first element used for the `command -v` precheck at `:76`, executed as `"${GATEWAYCTL[@]}" "$@"` at `:102`, and rendered with `"${GATEWAYCTL[*]}"` in the dry-run preview at `:96`. All expansions are correct.
- **WR-04 (numpy dropped):** RESOLVED. `numpy` is removed from `requirements.txt`; the percentile is now an inline exact-rank `_p95` helper (`smoke-converseai.py:255-268`). Verified: empty list returns `-1` (valid `integer` per schema), 1-element returns that element, 3-element returns `s[2]` — correct nearest-rank.
- **WR-05 (latency_ratio null sentinel + schema):** RESOLVED. `smoke-chat-ifix.py:376-381` uses an explicit `latency_evaluable` boolean carry instead of an `inf`/`1e9` sentinel; the report writes literal `null` when latency is unevaluable (`:412-414`); `chat-ifix-report-schema.json:66` accepts `["number", "null"]`; `apply_gates` (`:289-291`) fails the latency gate on `None`.
- **WR-06 (guarded fixture/baseline reads):** RESOLVED. Both `fixture_file.read_bytes()` and `json.loads(...read_text())` are wrapped in try/except (`smoke-chat-ifix.py:342-351`), catching `OSError` / `json.JSONDecodeError` and failing with a clear `log.error` + `return 1` instead of an escaping traceback.
- **WR-07 (WER docstring):** RESOLVED. `normalize_text` (`:142-150`) and `word_error_rate` (`:174-182`) both carry explicit docstring notes on punctuation-driven word-count sensitivity and the textbook-WER limitations.

### No-new-bugs verification

- **Schema conformance under `additionalProperties: false`:** Every field emitted by both reports was traced against its schema. `smoke-converseai` — `chat` (`status_code/ok/raw_error_body`), `chat_stream` (`ttft_ms/chunks/flushed/status_code/raw_error_body`), `tool_call` (`valid/errors`), `embeddings` (`p95_ms/successes/errors`) and all top-level keys are present in `report-schema.json`. `smoke-chat-ifix` — `transcription` (`status_code/ok/latency_s/text/raw_error_body`), `comparison` (`wer/latency_ratio`) match the schema. The `baseline` sub-object is the one risk point: `smoke-chat-ifix.py:421-426` temporarily adds `wer_threshold` + `latency_tolerance` for `apply_gates` to read, then `:430-431` `pop`s both *before* schema validation (`:451`) and the write (`:456`) — the emitted `baseline` carries only the three schema-allowed keys. Correct.
- **Bash array conversion:** No word-splitting regressions; `command -v` still prechecks only the leading executable (documented limitation, unchanged).
- **`parse_key` assertion vs. happy path:** Confirmed the real `gatewayctl` mint block yields exactly one `^key=` line, so the new assertion does not break provisioning. Tenant-create output (`id=%s slug=%s name=%q`) has zero `^key=` lines and is never passed to `parse_key`; `parse_id` is only called on key-mint output where `^id=` appears exactly once.

## Info

These items are unchanged from the prior review — valid non-blocking observations, out of scope for the `--auto` fix loop, and not gating the `clean` status.

### IN-01: `--dry-run` output goes to stdout, intermixing with the would-be secret block channel

**File:** `scripts/integration-smoke/provision-tenants.sh:95-96`
**Issue:** Under `--dry-run`, `run_gatewayctl` prints `[dry-run] would run: ...` to **stdout**. The final real-run secret block also goes to stdout. The two modes are mutually exclusive at runtime so the risk is low, but stdout is overloaded as both "human dry-run preview" and "machine-copyable secrets".
**Fix:** Consider sending `[dry-run]` lines to stderr to keep stdout reserved strictly for the secret block.

### IN-02: `git_sha` best-effort block silently swallows all exceptions

**File:** `scripts/integration-smoke/smoke-converseai.py:404-405`, `scripts/integration-smoke/smoke-chat-ifix.py:442-443`
**Issue:** `except Exception: pass` around the `git rev-parse` call. Intentional (`git_sha` is optional per schema), but a bare `pass` with no log line makes a misconfigured CI checkout (detached HEAD, wrong `cwd`) invisible.
**Fix:** A one-line `log.debug("git_sha unavailable", err=...)` would aid debugging without changing behaviour.

### IN-03: Schema-validation failure only `log.warning`s, then writes a non-conforming report anyway

**File:** `scripts/integration-smoke/smoke-converseai.py:408-413`, `scripts/integration-smoke/smoke-chat-ifix.py:445-454`
**Issue:** If the report fails its committed JSON schema, the script logs a warning and writes it regardless ("for debugging"). Defensible for local debugging, but in CI a schema-nonconforming report could let the HUMAN-UAT asserter assert against a malformed document.
**Fix:** Consider an env-gated strict mode (`SMOKE_STRICT_SCHEMA=1` → `sys.exit(1)` on validation error).

### IN-04: `run_chat` non-streaming gate only checks HTTP 200, never inspects the body

**File:** `scripts/integration-smoke/smoke-converseai.py:117-131`, `:299`
**Issue:** `run_chat` returns `ok: True` purely on `status_code == 200`; it never verifies the response contains a `choices[].message.content`. A gateway returning `200` with an empty/malformed OpenAI envelope passes the `chat_ok` gate. The streaming and tool-call paths do inspect the body; the non-streaming path is the odd one out.
**Fix:** Consider asserting `body["choices"][0]["message"]["content"]` is a non-empty string.

### IN-05: `subprocess` imported in both smokes solely for the optional git_sha call

**File:** `scripts/integration-smoke/smoke-converseai.py:41`, `scripts/integration-smoke/smoke-chat-ifix.py:46`
**Issue:** Minor — `subprocess` is a stdlib import used only for the best-effort `git rev-parse`. Fine to keep, just noting it is the only use; if `git_sha` is dropped the import goes with it.

---

_Reviewed: 2026-05-14_
_Reviewer: Claude (gsd-code-reviewer)_
_Depth: standard_
