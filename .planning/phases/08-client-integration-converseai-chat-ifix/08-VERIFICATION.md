---
phase: 08-client-integration-converseai-chat-ifix
verified: 2026-05-14T15:00:00Z
status: human_needed
score: 6/8 must-haves verified (2 SC1/SC2/SC3/SC4 live-validation truths require deployed gateway + operator action)
overrides_applied: 0
human_verification:
  - test: "Execute UAT-1 — ConverseAI v4 smoke against deployed dev gateway"
    expected: "smoke-converseai.py exits 0; report gates.all_passed == true (chat / streaming / tool-call / embeddings)"
    why_human: "Requires the dev gateway deployed (blocked on Phase 6 emerg integration tests) and the converseai-v4 env-var switch applied via Portainer"
  - test: "Execute UAT-2 — Chat Ifix transcription smoke against deployed dev gateway"
    expected: "smoke-chat-ifix.py exits 0; WER <= 0.10 AND latency_ratio <= 1.10 AND gates.all_passed == true"
    why_human: "Requires the dev gateway deployed and the campanhas-chatifix backend base_url/key switched"
  - test: "Execute UAT-3 — Rollback drill, timed <5 minutes"
    expected: "Both apps fully rolled back to direct providers (env vars reverted, redeployed, verified) in under 5 min; measured time recorded in 08-HUMAN-UAT.md sign-off"
    why_human: "Requires the gateway to have been activated first (UAT-1/UAT-2 must pass), then actively exercising the RUNBOOK-CLIENT-INTEGRATION.md ROLLBACK procedure end-to-end with a stopwatch"
  - test: "Execute UAT-4 — Dashboard shows converseai + chat-ifix as separate tenants"
    expected: "Phase 7 dashboard tenant-table shows both slugs with independent latency/cost panels populated from UAT-1/UAT-2 traffic"
    why_human: "Requires live traffic through the gateway to populate the dashboard metrics; the Phase 7 code (tenant-table.tsx) exists but cannot be verified against new tenants without live traffic"
---

# Phase 8: Client Integration — ConverseAI + Chat Ifix — Verification Report

**Phase Goal:** The first two production workloads (chat+agents on ConverseAI v4; audio transcription on Chat Ifix) run through the gateway, validating the OpenAI-compat contract with real traffic.
**Verified:** 2026-05-14T15:00:00Z
**Status:** human_needed
**Re-verification:** No — initial verification

---

## Goal Achievement

Phase 8's goal has two layers:

1. **Autonomous layer** (gpu-ifix-side artifacts): the seed script, smoke scripts, audio fixture, runbook, and UAT scenario sheet — fully verifiable on disk without a deployed gateway.
2. **Live-validation layer** (SC1-SC4 success criteria): requires the gateway deployed + operator-run env-var switches in the client repos. This is the established HUMAN-UAT deferral pattern used by Phases 3, 4, 6, and 7.

Both layers are assessed below. The autonomous layer is **fully verified**. The live-validation layer is **pending operator execution** of `08-HUMAN-UAT.md`.

### Observable Truths

| # | Truth | Status | Evidence |
|---|-------|--------|----------|
| 1 | An operator can run `provision-tenants.sh` to idempotently create the `converseai` + `chat-ifix` tenants in the gateway DB | VERIFIED | File exists at 227 lines, 0755, `bash -n` passes; `--dry-run` prints `would run: gatewayctl tenant create` for both tenants; `grep 'already exists'` (4 hits), `--mint-keys` (10 hits), `converseai`+`chat-ifix` (17 hits), `--dry-run` (4 hits) all confirmed |
| 2 | Re-running `provision-tenants.sh` does not error and does not mint duplicate tenant rows | VERIFIED | Idempotency: anchored `grep -qF "tenant slug '$slug' already exists"` match confirmed; key-mint gated behind `--mint-keys` explicit opt-in; re-runs without `--mint-keys` only run the idempotent tenant-create step |
| 3 | The script surfaces each tenant's raw API key to the operator exactly once via stdout heredoc, without writing it to a log file | VERIFIED | Pattern confirmed: raw keys captured into shell vars, emitted only via stdout heredoc, never passed to `log()` which writes to stderr |
| 4 | An operator can run `smoke-converseai.py` against the gateway with the converseai tenant key and get a machine-readable JSON report covering chat / streaming / tool-calls / embeddings | VERIFIED | `py_compile` passes; 433 lines (> 150 minimum); `Authorization: Bearer` header on AsyncClient (3 hits); `/v1/chat/completions` + `/v1/embeddings` (7 hits); `apply_gates` + `exit_code_for_gates` at lines 298+314; refuses to run without `--api-key` (confirmed via live invocation returning argparse error) |
| 5 | An operator can run `smoke-chat-ifix.py` against the gateway with the chat-ifix tenant key and transcribe a real WhatsApp audio fixture with ±10% WER + latency gates | VERIFIED | `py_compile` passes; 476 lines (> 140 minimum); `Authorization: Bearer` header (4 hits); `/v1/audio/transcriptions` multipart POST (4 hits); `word_error_rate` hand-rolled DP at line 164; `normalize_text` at line 135; `apply_gates` + `exit_code_for_gates` at lines 270+302; refuses to run without `--api-key` (confirmed via live invocation) |
| 6 | A real (short) WhatsApp audio fixture + its baseline transcript are committed and re-runnable in CI | VERIFIED | `whatsapp-sample.ogg` exists, 44917 bytes (< 200 KB), tracked (git check-ignore exit 1 = not ignored); `whatsapp-sample.baseline.json` has non-empty transcript, `baseline_latency_s`, `wer_threshold=0.1`, `latency_tolerance=0.1`; `.gitignore` allow-rule in place |
| 7 | SC1/SC2: ConverseAI v4 + Chat Ifix actually route through the gateway with real traffic (env-var switches applied, gateway deployed) | HUMAN NEEDED | Gateway not yet deployed (blocked on Phase 6 emerg integration tests). `08-HUMAN-UAT.md` UAT-1 and UAT-2 are the verification vehicle; `final_status: pending`, `operator: ___` — not yet executed |
| 8 | SC3/SC4: Rollback drill timed < 5 min + dashboard shows both tenants as separate rows | HUMAN NEEDED | SC3 requires UAT-3 to be executed against a live switched deployment. SC4 requires live gateway traffic to populate the Phase 7 dashboard. Both are in the `08-HUMAN-UAT.md` sign-off table, not yet filled |

**Score:** 6/8 truths verified (truths 7+8 require human execution of UAT scenarios)

---

### Required Artifacts

| Artifact | Expected | Status | Details |
|----------|----------|--------|---------|
| `scripts/integration-smoke/provision-tenants.sh` | Idempotent seed script wrapping gatewayctl | VERIFIED | 227 lines, 0755, `bash -n` clean, all required patterns present |
| `scripts/integration-smoke/README.md` | Operator usage doc | VERIFIED | Contains `provision-tenants`, `idempotent`, `scope`, smoke script references |
| `scripts/integration-smoke/smoke-converseai.py` | INT-01 gateway smoke (chat/stream/tool/embed) | VERIFIED | 433 lines (> 150), `py_compile` passes, all 4 surfaces implemented |
| `scripts/integration-smoke/report-schema.json` | JSON Schema for converseai smoke report | VERIFIED | Draft 2020-12, `additionalProperties: false`, all required fields present including `gates` sub-object |
| `scripts/integration-smoke/requirements.txt` | Trimmed Python deps (no prometheus-client) | VERIFIED | Lists `httpx`, `structlog`, `jsonschema`; no `numpy` (WR-04 fix); no `prometheus-client` |
| `scripts/integration-smoke/smoke-chat-ifix.py` | INT-02 Whisper transcription smoke | VERIFIED | 476 lines (> 140), `py_compile` passes, WER + latency gates, `word_error_rate` hand-rolled |
| `scripts/integration-smoke/chat-ifix-report-schema.json` | JSON Schema for chat-ifix transcription report | VERIFIED | Draft 2020-12, `additionalProperties: false`, `transcription`/`baseline`/`comparison`/`gates` in required |
| `scripts/integration-smoke/fixtures/whatsapp-sample.ogg` | Real WhatsApp audio clip < 200 KB | VERIFIED | 44917 bytes, git-tracked (not ignored), present |
| `scripts/integration-smoke/fixtures/whatsapp-sample.baseline.json` | Recorded baseline transcript + latency | VERIFIED | `transcript` non-empty, `baseline_latency_s` present, `wer_threshold=0.1`, `latency_tolerance=0.1` |
| `scripts/integration-smoke/fixtures/README.md` | Fixture provenance doc | VERIFIED | References `whatsapp-sample`, documents provenance/format/no-PII + baseline fields |
| `gateway/docs/RUNBOOK-CLIENT-INTEGRATION.md` | Operator runbook with <5-min ROLLBACK | VERIFIED | 24307 bytes; ROLLBACK section found; `5-min` found; `converseai-v4-dev` found; `campanhas-chatifix` found; Required Env Vars/BASE_URL found |
| `.planning/phases/08-client-integration-converseai-chat-ifix/08-HUMAN-UAT.md` | Live-verification scenario sheet + sign-off table | VERIFIED (document exists, content complete; execution pending) | 20+ UAT- references, Sign-off found, Prerequisites found, passed_partial found, smoke-converseai and smoke-chat-ifix found |

---

### Key Link Verification

| From | To | Via | Status | Details |
|------|----|-----|--------|---------|
| `provision-tenants.sh` | `gatewayctl tenant/key/admin-key create` | subprocess invocation | VERIFIED | 27 occurrences of `gatewayctl`; 16 occurrences of `tenant create`/`key create`/`admin-key create` patterns |
| `provision-tenants.sh` | Gateway DB tenants/api_keys/admin_keys | gatewayctl writes rows; script parses stdout | VERIFIED | Idempotency logic present; stdout parsed via `parse_key`/`parse_id` helpers |
| `smoke-converseai.py` | Gateway `/v1/chat/completions` and `/v1/embeddings` | httpx.AsyncClient POST with `Authorization: Bearer` | VERIFIED | 3 occurrences of `Authorization`/`Bearer`; 7 occurrences of `/v1/chat/completions`/`/v1/embeddings` |
| `smoke-converseai.py` | `report-schema.json` | jsonschema validation before writing report | VERIFIED | `report-schema` pattern referenced; jsonschema validation block present |
| `smoke-chat-ifix.py` | Gateway `/v1/audio/transcriptions` | httpx.AsyncClient multipart POST with `Authorization: Bearer` | VERIFIED | 4 occurrences of `Authorization`/`Bearer`; 4 occurrences of `/v1/audio/transcriptions` |
| `smoke-chat-ifix.py` | `fixtures/whatsapp-sample.baseline.json` | loads baseline + gates live result within ±10% | VERIFIED | `baseline`/`whatsapp-sample` references present; `word_error_rate` + `normalize_text` helpers at lines 135+164 |
| `08-HUMAN-UAT.md` | `provision-tenants.sh` + `smoke-converseai.py` + `smoke-chat-ifix.py` | UAT scenarios invoke the seed script + both smoke scripts | VERIFIED | `provision-tenants` found; `smoke-converseai` found; `smoke-chat-ifix` found |
| `RUNBOOK-CLIENT-INTEGRATION.md` | `converseai-v4` + `campanhas-chatifix` client repos | Rollback procedure documents per-app env-var revert + redeploy | VERIFIED | `base_url`/`BASE_URL` found; `Portainer` found; numbered ROLLBACK procedures for both apps confirmed |

---

### Data-Flow Trace (Level 4)

Not applicable for this phase. All artifacts are scripts/documents that do not render dynamic data from a database. The smoke scripts produce JSON reports from live network calls (deferred to HUMAN-UAT), not from a wired data source that can be verified statically.

---

### Behavioral Spot-Checks

| Behavior | Command | Result | Status |
|----------|---------|--------|--------|
| `provision-tenants.sh` passes bash syntax | `bash -n provision-tenants.sh` | exit 0 | PASS |
| `provision-tenants.sh --dry-run` prints would-run commands without executing | `AI_GATEWAY_PG_DSN=postgres://x bash scripts/integration-smoke/provision-tenants.sh --dry-run` | Printed `[dry-run] would run: gatewayctl tenant create` for both tenants | PASS |
| `smoke-converseai.py` compiles | `python3 -m py_compile smoke-converseai.py` | exit 0 | PASS |
| `smoke-converseai.py` refuses to run without api-key | `python3 smoke-converseai.py --gateway-url https://x` | argparse error: `--api-key or SMOKE_API_KEY required` | PASS |
| `smoke-chat-ifix.py` compiles | `python3 -m py_compile smoke-chat-ifix.py` | exit 0 | PASS |
| `smoke-chat-ifix.py` refuses to run without api-key | `python3 smoke-chat-ifix.py --gateway-url https://x` | argparse error: `--api-key or SMOKE_API_KEY required` | PASS |
| `report-schema.json` is valid JSON with required structure | `python3 -c "..."` | `additionalProperties: false`; `gates` in required; chat/chat_stream/tool_call/embeddings in required | PASS |
| `chat-ifix-report-schema.json` is valid JSON with required structure | `python3 -c "..."` | `additionalProperties: false`; transcription/baseline/comparison/gates in required | PASS |
| `whatsapp-sample.ogg` exists, is non-empty, under 200 KB, not git-ignored | stat + git check-ignore | 44917 bytes; git check-ignore exit 1 (not ignored) | PASS |
| `baseline.json` has required fields | `python3 -c "..."` | transcript non-empty, baseline_latency_s present, wer_threshold=0.1 | PASS |

---

### Probe Execution

Step 7c: SKIPPED — no `scripts/*/tests/probe-*.sh` found for Phase 8. The phase's verification mechanism is the `08-HUMAN-UAT.md` blocking checkpoint (Task 3 of plan 08-04), not a standalone probe script.

---

### Requirements Coverage

| Requirement | Source Plan | Description | Status | Evidence |
|-------------|------------|-------------|--------|----------|
| INT-01 | 08-01-PLAN.md, 08-02-PLAN.md, 08-04-PLAN.md | ConverseAI v4 (api Elysia + agents Python) pointing to gateway via `base_url`; rollback documented | PARTIAL — autonomous artifacts complete, live validation pending | `provision-tenants.sh` seeds `converseai` tenant; `smoke-converseai.py` exercises all 4 surfaces; `RUNBOOK-CLIENT-INTEGRATION.md` documents rollback; live activation deferred to `08-HUMAN-UAT.md` |
| INT-02 | 08-01-PLAN.md, 08-03-PLAN.md, 08-04-PLAN.md | Chat Ifix migrated to gateway for Whisper transcription of WhatsApp audios | PARTIAL — autonomous artifacts complete, live validation pending | `provision-tenants.sh` seeds `chat-ifix` tenant; `smoke-chat-ifix.py` with committed fixture + ±10% gates; `RUNBOOK-CLIENT-INTEGRATION.md` documents rollback; live activation deferred to `08-HUMAN-UAT.md` |

**Note on REQUIREMENTS.md status:** Both INT-01 and INT-02 remain marked `Pending` in REQUIREMENTS.md. This is correct — the requirements describe live production workloads routing through the gateway, which requires the HUMAN-UAT scenarios to pass. The gpu-ifix-side artifacts are the necessary pre-conditions, not the requirement fulfillment itself. INT-01 and INT-02 will be marked Complete after the `08-HUMAN-UAT.md` sign-off is recorded.

---

### Anti-Patterns Found

| File | Line | Pattern | Severity | Impact |
|------|------|---------|----------|--------|
| `gateway/docs/RUNBOOK-CLIENT-INTEGRATION.md` | 302, 343, 389 | `> CONFIRM:` notes on exact env-var names in converseai-v4 + campanhas-chatifix repos | INFO | Not a blocker — these are explicit operator-confirmation prompts in the runbook (standard practice when the exact sibling-repo env-var names cannot be read without accessing the client repos). No TBD/FIXME/XXX markers present; the `CONFIRM:` pattern is a documented runbook convention, not an unresolved placeholder |

No `TBD`, `FIXME`, or `XXX` markers found in any Phase 8 modified file. The 5 INFO items from the code review (`08-REVIEW.md`) are non-blocking observations about code style (dry-run stdout channel, `except Exception: pass` on git_sha, warn-then-write schema validation, `run_chat` body inspection, unused subprocess import). None of these are debt markers and none block the phase goal.

---

### Human Verification Required

The SC1-SC4 success criteria from ROADMAP Phase 8 all require a deployed live gateway plus operator-executed env-var switches in the client repos (`converseai-v4` and `campanhas-chatifix`). The gateway is additionally not yet deployed (blocked on Phase 6 emergency-pod integration test failures — a separate debug session). The `08-HUMAN-UAT.md` scenario sheet is the vehicle for all four scenarios.

#### 1. UAT-1 — ConverseAI v4 env-var switch + smoke (SC1)

**Test:** Deploy the dev gateway; set `converseai-v4-dev` Portainer stack env vars (`OPENAI_BASE_URL` / `ANTHROPIC_BASE_URL` / `agents/` LangChain base_url) to the gateway URL, set `OPENAI_API_KEY` to the `converseai` tenant key; redeploy; run `python scripts/integration-smoke/smoke-converseai.py --gateway-url <dev gateway> --api-key <converseai key> --out /tmp/converseai-report.json`
**Expected:** Exit code 0; `report.gates.all_passed == true` — chat, SSE streaming (flushed), tool-call validation, embeddings all pass
**Why human:** Requires live deployed gateway (currently blocked) and Portainer env-var switch in the converseai-v4-dev stack

#### 2. UAT-2 — Chat Ifix transcription smoke ±10% (SC2)

**Test:** Switch the `campanhas-chatifix` backend base_url/key to the gateway; run `python scripts/integration-smoke/smoke-chat-ifix.py --gateway-url <dev gateway> --api-key <chat-ifix key> --out /tmp/chat-ifix-report.json`
**Expected:** Exit code 0; `gates.all_passed == true` — transcription status 200, WER <= 0.10, latency_ratio <= 1.10. Note: if `baseline_latency_s` in `whatsapp-sample.baseline.json` was a conservative estimate (the `baseline_latency_note` key is present indicating this), re-measure against the real direct integration first and update the baseline before gating
**Why human:** Requires live deployed gateway and `campanhas-chatifix` env-var switch

#### 3. UAT-3 — Rollback drill, timed <5 min (SC3)

**Test:** Start a stopwatch; execute the `gateway/docs/RUNBOOK-CLIENT-INTEGRATION.md` ROLLBACK procedure for both apps (confirm env-var names via the `> CONFIRM:` prompts, revert, redeploy via Portainer, run the verify `curl`); stop the stopwatch; record measured time in sign-off table
**Expected:** Both apps fully rolled back to direct providers with the verify step passing in under 5 minutes; measured time recorded
**Why human:** SC3 requires the rollback to be actively drilled end-to-end on a live switched deployment — cannot be simulated

#### 4. UAT-4 — Dashboard per-tenant cross-check (SC4)

**Test:** After UAT-1 and UAT-2 generate traffic, open the Phase 7 dashboard and confirm `converseai` and `chat-ifix` appear as separate tenant rows in the tenant-table with independent latency (P50/P95/P99) and cost panels
**Expected:** Both tenant slugs visible in the dashboard with metrics populated from UAT-1/UAT-2 traffic
**Why human:** Requires live traffic through the gateway to populate the dashboard. The Phase 7 `tenant-table.tsx` component exists and was verified in Phase 7 to render per-tenant data — but cannot be verified against the new Phase 8 tenants without actual requests

---

### Gaps Summary

No gaps found in the autonomous (gpu-ifix-side) portion of Phase 8. All 10 scripts/document/fixture/schema artifacts are verified on disk, passing syntax checks and content validation. The code review (08-REVIEW.md) recorded 2 BLOCKERs + 7 WARNINGs, all fixed and re-verified clean in 08-REVIEW-FIX.md. Five INFO items remain as non-blocking observations.

The `human_needed` status reflects that SC1-SC4 (the ROADMAP Phase 8 success criteria) require a deployed live gateway and operator action — the same deferred-UAT pattern documented for Phases 3, 4, 6, and 7. There are no functional gaps in the artifacts that were within this phase's autonomous delivery scope.

When the gateway is deployed and the Phase 6 emerg integration test blocker is resolved, the operator should execute `08-HUMAN-UAT.md` UAT-1 through UAT-4 and fill the sign-off table. A re-verification pass after sign-off can close this to `passed`.

---

_Verified: 2026-05-14T15:00:00Z_
_Verifier: Claude (gsd-verifier)_
