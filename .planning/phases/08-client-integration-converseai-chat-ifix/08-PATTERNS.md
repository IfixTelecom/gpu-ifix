# Phase 8: Client Integration — ConverseAI + Chat Ifix - Pattern Map

**Mapped:** 2026-05-14
**Files analyzed:** 6 new files (1 seed script, 1 runbook, 3 smoke artifacts, 1 HUMAN-UAT plan)
**Analogs found:** 6 / 6

> Scope note: Phase 8 delivers **gpu-ifix-side artifacts only**. The client apps
> (`converseai-v4`, `campanhas-chatifix`) are separate sibling repos and are NOT
> edited by this phase — no analogs needed there. The `base_url`/env-var switch
> inside those repos is an operator HUMAN-UAT action.

## File Classification

| New File | Role | Data Flow | Closest Analog | Match Quality |
|----------|------|-----------|----------------|---------------|
| `scripts/integration-smoke/provision-tenants.sh` | seed script (idempotent) | batch / CLI-wrapping | `pod/scripts/vast-ai.sh` (shell structure) + `pod/scripts/upload-weights.sh` (one-shot operator script) | role-match |
| `docs/RUNBOOK-CLIENT-INTEGRATION.md` | runbook (doc) | n/a | `gateway/docs/RUNBOOK-FAILOVER.md` + `gateway/docs/RUNBOOK-EMERGENCY-POD.md` | exact |
| `scripts/integration-smoke/smoke-converseai.py` | smoke-test script | request-response + streaming | `pod/smoke/smoke.py` | exact |
| `scripts/integration-smoke/smoke-chat-ifix.py` | smoke-test script | file-I/O (multipart audio) + transform | `pod/smoke/smoke.py` (`run_whisper`) | exact |
| `scripts/integration-smoke/fixtures/` (WhatsApp audio fixture) | test fixture | file-I/O | `pod/smoke/fixtures/__gen_audio.py` + `pod/smoke/fixtures/README.md` | role-match |
| `.planning/phases/08-.../08-NN-PLAN.md` (HUMAN-UAT, `autonomous:false`) | plan (doc) | n/a | `.planning/phases/07-.../07-09-PLAN.md` + `06-HUMAN-UAT.md` produced artifact | exact |

> Note: there is **no new `gatewayctl` Go code** in Phase 8 — the seed script
> *wraps the existing compiled `gatewayctl tenant create` / `key create` /
> `admin-key create` subcommands*. The Go files below are read-only references
> so the planner knows the exact CLI surface, flags, exit codes, and stdout
> shape the script must parse.

---

## Pattern Assignments

### `scripts/integration-smoke/provision-tenants.sh` (seed script, idempotent CLI-wrapper)

**Analogs:** `pod/scripts/vast-ai.sh` (bash structure, arg-parse, `log()`, subcommand dispatch), `pod/scripts/upload-weights.sh` (one-shot operator script, prereq checks, "paste these into GH Secrets" output convention).

**CLI surface this script wraps** — from `gateway/cmd/gatewayctl/main.go` lines 56-83 the dispatcher; the three relevant subcommands:

`gatewayctl tenant create` — `gateway/cmd/gatewayctl/tenant.go` lines 56-89:
```go
// runTenantCreate flags: --name (required), --slug (required, url-safe)
// stdout on success: "id=<uuid> slug=<slug> name=\"<name>\""
// exit 1 + stderr "error: tenant slug '<slug>' already exists" on pg 23505 unique_violation
// exit 2 on missing/empty flags
```
**Idempotency hook:** the unique_violation → exit 1 path IS the idempotency signal. The seed script must treat "slug already exists" as success (already-provisioned), not failure — grep stderr for `already exists` and continue.

`gatewayctl key create` — `gateway/cmd/gatewayctl/key.go` lines 33-97:
```go
// flags: --tenant <slug> (required), --data-class normal|sensitive (default "normal")
// stdout on success (lines 87-88):
//   key=<raw>\nid=<uuid>\nprefix=<prefix>\ntenant=<slug>\ndata_class=<class>
// IMPORTANT (lines 85-86): raw key printed to stdout ONCE — never logged.
// exit 1 if tenant not found; exit 2 on bad flags / bad --data-class
```
**Caveat for idempotency:** `key create` is NOT idempotent — every call mints a new key row. The seed script must guard the key-create step (e.g. only mint if a sentinel file / env var is absent, or make key-minting a separate explicit subcommand) so re-running the script does not spray duplicate keys. Document this in the script header.

`gatewayctl admin-key create` — `gateway/cmd/gatewayctl/admin_key.go` lines 74-137:
```go
// flag: --label <label> (required)
// stdout on success (lines 129-130):
//   key=<raw>\nid=<uuid>\nprefix=<prefix>\nlabel=<label>
// raw key printed ONCE — never logged. Same non-idempotent caveat as key create.
```

**Bash structure to copy** — `pod/scripts/vast-ai.sh` lines 1-39:
```bash
#!/usr/bin/env bash
# scripts/integration-smoke/provision-tenants.sh — <one-line purpose>
#
# Usage:
#   ./scripts/integration-smoke/provision-tenants.sh [--gatewayctl PATH] [--dry-run]
#
# Env: <required env vars>
set -euo pipefail

log() { printf '[%s] [provision-tenants] %s\n' "$(date -Iseconds)" "$*" >&2; }
```

**Prereq-check pattern** — `pod/scripts/upload-weights.sh` lines 16, 38-41:
```bash
: "${AI_GATEWAY_PG_DSN:?missing}"            # fail-fast on missing required env
for bin in <required binaries>; do
  command -v "$bin" >/dev/null 2>&1 || { log "missing required binary: $bin"; exit 1; }
done
```

**Arg-parse loop** — `pod/scripts/vast-ai.sh` lines 45-51 / 66-74:
```bash
while [[ $# -gt 0 ]]; do
  case "$1" in
    --gatewayctl) GATEWAYCTL="$2"; shift 2;;
    --dry-run)    DRY_RUN=1;       shift 1;;
    *) log "unknown arg $1"; exit 2;;
  esac
done
```

**Tenant model (from 08-CONTEXT.md `## Decisions`):** exactly two tenants — `converseai` (covers both converseai-v4 api + agents) and `chat-ifix`. `data_class=normal` for both. The script seeds both, idempotently.

---

### `docs/RUNBOOK-CLIENT-INTEGRATION.md` (runbook)

**Analogs:** `gateway/docs/RUNBOOK-FAILOVER.md` (full structure), `gateway/docs/RUNBOOK-EMERGENCY-POD.md` (referenced as structural template by every prior phase's HUMAN-UAT plan).

> Location decision: prior gateway-subsystem runbooks live in `gateway/docs/`
> (`RUNBOOK-FAILOVER.md`, `RUNBOOK-EMERGENCY-POD.md`, `RUNBOOK-QUOTAS-BILLING.md`,
> `RUNBOOK-OBSERVABILITY-ALERTING.md`). The Phase 7 plan also wrote a copy to
> top-level `docs/` (`docs/RUNBOOK-OBSERVABILITY-ALERTING.md`). Planner should
> pick one — `gateway/docs/` is the dominant convention (4 of 5 runbooks).

**Section structure to copy** — `gateway/docs/RUNBOOK-FAILOVER.md`:

1. **Title + "Read this when:"** bullet list of trigger conditions (lines 1-14).
2. **Mental Model (30 seconds)** — a compact diagram + the moving parts (lines 16-88). For Phase 8: the env-var contract — `base_url` → `gateway.ifix.com.br/v1`, `api_key` → tenant key; converseai-v4 has TWO consumers (`apps/api` Elysia + `agents/` Python) on ONE `converseai` tenant; chat-ifix backend lives in the `campanhas-chatifix` repo.
3. **Quick Diagnosis (~2 minutes)** — ordered `curl` / `psql` / `docker exec` commands (lines 90-141). For Phase 8: `curl` the gateway `/v1/*` with each tenant key, check `audit_log` rows appear per tenant, check the dashboard tenant-table.
4. **Incident Response by Symptom** — `### Symptom N` blocks, each with **Likely cause / Diagnose / Mitigate / Recovery** (lines 143-332).
5. **The ROLLBACK procedure** — this is the core Phase 8 deliverable (SC3, <5 min). Mirror the precise "To rotate a bearer secret" numbered procedure at lines 376-384:
   ```
   **To roll back ConverseAI v4:**
   1. In Portainer stack `converseai-v4-dev`, revert env vars:
      OPENAI_BASE_URL / ANTHROPIC_BASE_URL → direct provider URL
      OPENAI_API_KEY → direct provider key
      (agents/: the LangChain base_url env var → direct)
   2. Redeploy: <exact Portainer redeploy / webhook command>
   3. Verify: <exact curl / health check>
   ```
   Same shape for campanhas-chatifix via its own deploy flow. Each app gets a concrete env-var diff + redeploy command + verify step. The runbook states the <5-min budget and that the procedure must be DRILLED in the HUMAN-UAT, not just written.
6. **Required Env Vars** table — `gateway/docs/RUNBOOK-FAILOVER.md` lines 387-413 (the `| Var | Required | Purpose |` table). For Phase 8: the per-app `*_BASE_URL` / `*_API_KEY` vars and their gateway-vs-direct values.
7. **Escalation** — lines 483-488 (1st responder / escalation contact / per-severity comms).
8. **Related Docs** — lines 491-499 cross-link to 08-CONTEXT.md, the smoke scripts, the HUMAN-UAT plan.

**Deploy facts to bake in** (from CLAUDE.md `## Dev Environment`): converseai-v4 → Portainer stack `converseai-v4-dev` + GitHub webhook on `develop`; campanhas-chatifix → its own deploy flow; env vars set via Portainer stack UI, never committed.

---

### `scripts/integration-smoke/smoke-converseai.py` (smoke-test script, request-response + streaming)

**Analog:** `pod/smoke/smoke.py` — exact match. The Phase 8 script is the same shape, retargeted from the raw pod endpoints to the gateway `/v1/*` with a per-tenant API key, and it covers chat / streaming / tool-calls / embeddings (the 4 INT-01 surfaces).

**Module header + entry-point convention** — `pod/smoke/smoke.py` lines 1-13, 478-485:
```python
"""scripts/integration-smoke/smoke-converseai.py — INT-01 gateway contract smoke.

Entry point:
    python scripts/integration-smoke/smoke-converseai.py --gateway-url https://... --api-key <converseai key> --out report.json
"""
def main() -> None:
    cfg = parse_args()
    code = asyncio.run(main_async(cfg))
    sys.exit(code)
```

**CLI + Config dataclass** — `pod/smoke/smoke.py` lines 69-121: `@dataclasses.dataclass class Config`, `parse_args()` with `argparse`, env-var fallbacks (`os.getenv("SMOKE_TARGET")`), a `--fast` dev flag. Phase 8 needs `--gateway-url`, `--api-key` (the `converseai` tenant key — pass via env `SMOKE_API_KEY`, never a committed default), `--out`.

**Auth header pattern (the key difference vs the pod smoke):** the pod smoke hits raw endpoints with no auth. The gateway requires the tenant key. Per `gateway/README.md` line 53-54 the gateway redacts `Authorization` + `X-API-Key` — send the key as `Authorization: Bearer <key>` (OpenAI-SDK-compatible) on the `httpx.AsyncClient`:
```python
async with httpx.AsyncClient(headers={"Authorization": f"Bearer {cfg.api_key}"}) as client:
```

**Streaming SSE chat** — copy `run_chat_stream()` lines 167-199 verbatim (POST `/v1/chat/completions` with `"stream": True`, `client.stream(...)`, `aiter_lines()`, TTFT measurement, `data: [DONE]` sentinel).

**Tool-call validation** — copy `run_tool_call_test()` lines 204-254 verbatim (the `get_weather` function payload, the `tool_calls` / `function` / `arguments`-is-JSON-string assertions).

**Embeddings** — copy `run_embeddings()` lines 274-291 (POST `/v1/embeddings`, batched via `asyncio.gather`, p95 via `np.percentile`).

**Error-body capture for non-200** — lines 185-187, 226-227, 281-282: capture `r.status_code` + truncated body into `raw_error_body` so a gateway 401/429/503 envelope is visible in the report.

**Report + gates + exit-code contract** — lines 315-353, 422-475: build a `report` dict, `apply_gates()`, write JSON with `Path(cfg.out_path).write_text(json.dumps(report, indent=2, sort_keys=True))`, validate against a `report-schema.json` (see `pod/smoke/report-schema.json`), map gate failures to distinct exit codes via `exit_code_for_gates()`. Phase 8 gates: chat 200 + streaming flushes, tool-call well-formed, embeddings 200, per-tenant `audit_log` row written.

**Deps** — `pod/smoke/requirements.txt` (`httpx`, `numpy`, `structlog`, `jsonschema`, `prometheus_client`). Phase 8 likely drops `prometheus_client` (no DCGM scrape) — trim to what's used.

---

### `scripts/integration-smoke/smoke-chat-ifix.py` (smoke-test script, multipart file-I/O + transform)

**Analog:** `pod/smoke/smoke.py` `run_whisper()` lines 259-271 — exact match for the multipart transcription call.

**Multipart transcription request** — copy `run_whisper()` shape:
```python
async def run_whisper(client, url, ...):
    files = {"file": ("probe.wav", wav_bytes, "audio/wav")}
    data = {"model": "Systran/faster-whisper-large-v3"}
    start = time.monotonic()
    r = await client.post(url + "/v1/audio/transcriptions", files=files, data=data, timeout=600.0)
    total_s = time.monotonic() - start
    if r.status_code != 200:
        return {"error": f"status {r.status_code}", "raw_error_body": r.text[:500]}
    return {"latency_s": total_s, "text": r.json().get("text", "")}
```
Same `Authorization: Bearer <chat-ifix key>` header pattern as the converseai smoke (gateway-auth, not raw-pod).

**INT-02 specifics (from 08-CONTEXT.md `## Decisions` + `## Specific Ideas`):** unlike the pod smoke which generates synthetic audio, this script ships a **real WhatsApp audio fixture** and compares against a **recorded baseline** — latency AND transcription quality within **±10%** (SC2). So the script needs:
- a baseline file (committed JSON: expected latency_s + expected/reference transcript) — analog: `pod/smoke/report-schema.json` for the "committed schema/baseline" convention.
- a quality comparison (e.g. word-error-rate or normalized-similarity against the reference transcript) — this is NEW logic with no direct analog; the `detect_cuda_errors` / `apply_gates` lines 307-327 give the "compute a metric → boolean gate" shape to mirror.
- a `±10%` gate for latency, mirroring `apply_gates()` threshold-comparison style.

**Report + exit-code contract** — identical to `smoke-converseai.py` above (copy `pod/smoke/smoke.py` lines 422-475).

---

### `scripts/integration-smoke/fixtures/` (WhatsApp audio fixture + README)

**Analogs:** `pod/smoke/fixtures/__gen_audio.py` + `pod/smoke/fixtures/README.md`.

**Key divergence from the analog:** `pod/smoke/fixtures/README.md` lines 12-14 explain the pod smoke deliberately **commits NO binary WAVs** — it generates synthetic audio in-memory because an 8-min WAV is ~15 MB and `.gitignore` excludes `pod/smoke/fixtures/*.wav`. Phase 8 INT-02 needs a **real WhatsApp audio sample** (synthetic noise cannot validate transcription *quality* against a baseline). So Phase 8 must either:
- commit a SHORT real audio fixture (a few seconds of real speech — small enough for git), OR
- store it like a weight (MinIO `ai-gateway` bucket, downloaded by the script) — analog: `pod/scripts/download-weights.sh`.

The planner should call this out as a decision. Whichever path: ship a `fixtures/README.md` mirroring `pod/smoke/fixtures/README.md` (the `| File | Purpose |` table + the "why / how to regenerate" sections) documenting provenance, format (WhatsApp Opus/OGG vs decoded WAV), and the baseline transcript+latency it pairs with.

**README structure to copy** — `pod/smoke/fixtures/README.md` lines 1-24: `**Status:**` line, `## Files` table, a `## Why ...` rationale section, a `## Regenerating ...` section.

---

### `.planning/phases/08-.../08-NN-PLAN.md` — HUMAN-UAT plan (`autonomous: false`)

**Analogs:** `.planning/phases/07-observability-dashboard-alerting/07-09-PLAN.md` (the plan), the produced artifacts `06-HUMAN-UAT.md` / `07-HUMAN-UAT.md`. Prior precedents: 03-08, 04-09, 06-11, 07-09.

**Frontmatter pattern** — copy `07-09-PLAN.md` lines 1-31 verbatim-shaped:
```yaml
---
phase: 08-client-integration-converseai-chat-ifix
plan: NN
type: execute
wave: <last>
depends_on: [<the autonomous 08 plans>]
files_modified:
  - .planning/phases/08-.../08-HUMAN-UAT.md
  - docs/RUNBOOK-CLIENT-INTEGRATION.md   # or gateway/docs/...
autonomous: false
requirements: [INT-01, INT-02]
must_haves:
  truths: [ ... ]
  artifacts:
    - path: ".planning/phases/08-.../08-HUMAN-UAT.md"
      provides: "live-verification scenario sheet + sign-off for SC1/SC2/SC3/SC4"
      contains: "Sign-off"
  key_links: [ ... ]
---
```

**Body structure** — copy `07-09-PLAN.md` lines 33-174:
- `<objective>` — explain this is the phase-closing live-credential verification that cannot run autonomously; the autonomous plans (08-01..) ship only the gpu-ifix-side artifacts and are NOT blocked. Note the additional gate: the gateway itself is not deployed yet (build-gateway blocked on Phase 6 emerg integration tests) — per 08-CONTEXT.md `## Deferred Ideas`.
- `<execution_context>` + `<context>` `@`-includes — lines 40-54.
- `<interfaces>` — name the analog files to mirror (lines 56-72 pattern).
- `<tasks>` — two `type="auto"` tasks (write the runbook, write the HUMAN-UAT sheet) + one `type="checkpoint:human-verify" gate="blocking"` task (lines 77-139). Each auto task has `<read_first>` / `<action>` / `<verify><automated>` / `<acceptance_criteria>` / `<done>`.
- `<threat_model>` — Trust Boundaries + STRIDE register (lines 143-158).
- `<verification>` + `<success_criteria>` + `<output>` (lines 160-174).

**The produced `08-HUMAN-UAT.md` artifact structure** — copy `06-HUMAN-UAT.md` lines 1-90:
- YAML frontmatter: `status`, `phase`, `source`, `estimated_total_cost_brl`, `operator: ___`, `date_executed: ___`, `final_status: pending`.
- `## Prerequisites` — checkbox list: gateway deployed, env vars set in each Portainer stack, the two tenants provisioned (run `provision-tenants.sh`), tenant keys captured.
- `## Tests` — numbered `### N. UAT-N — <name> (SC-N)` blocks, each with **Pre-conditions / Steps (fenced bash) / Expected / pass-fail**. Phase 8 scenarios: S1 converseai env-var switch + run `smoke-converseai.py` against dev (SC1); S2 chat-ifix env-var switch + run `smoke-chat-ifix.py`, transcription within ±10% (SC2); S3 the rollback drill, timed <5 min (SC3); S4 open the Phase 7 dashboard, confirm `converseai` + `chat-ifix` appear as separate tenants with independent latency/cost panels (SC4 — verification, not new code).
- A **Sign-off table** — Result / Date / Operator / Notes per scenario + an overall phase-status line (`passed` / `passed_partial` / `human_needed`).
- The `passed_partial` fallback note — credentials/deploy unavailable → `passed_partial`, autonomous build still green (07-09-PLAN.md lines 111, 134).

---

## Shared Patterns

### Idempotent CLI-wrapping (seed script)
**Source:** `gateway/cmd/gatewayctl/tenant.go` lines 77-84 (the `pg 23505 unique_violation` → exit 1 path).
**Apply to:** `provision-tenants.sh`.
The "already exists" stderr + exit 1 from `gatewayctl tenant create` is the idempotency signal — re-running the seed treats it as success. `key create` / `admin-key create` are NOT idempotent (each call mints a new row) — the script must guard those steps.

### Secret-once stdout discipline
**Source:** `gateway/cmd/gatewayctl/key.go` lines 85-95, `gateway/cmd/gatewayctl/admin_key.go` lines 127-135.
**Apply to:** `provision-tenants.sh`, both smoke scripts, the HUMAN-UAT sheet.
Raw API keys are printed to stdout exactly once and NEVER logged. The seed script must surface the key to the operator without writing it to a log file; smoke scripts take the key via `--api-key` / env var, never a committed default; the HUMAN-UAT sheet references env var NAMES, never values (07-09-PLAN.md threat T-07-33).

### Gateway request auth header
**Source:** `gateway/README.md` lines 53-54 (redacted headers: `Authorization`, `X-API-Key`).
**Apply to:** both smoke scripts.
Send the per-tenant key as `Authorization: Bearer <key>` on the `httpx.AsyncClient` — OpenAI-SDK-compatible, and the gateway redacts it from logs. This is the one structural change vs `pod/smoke/smoke.py`, which hits unauthenticated raw pod endpoints.

### Smoke-report + exit-code contract
**Source:** `pod/smoke/smoke.py` lines 315-353 (`apply_gates`, `exit_code_for_gates`), 459-475 (schema-validate + write), `pod/smoke/report-schema.json`.
**Apply to:** both smoke scripts.
Build a `report` dict → `apply_gates()` → write `json.dumps(..., indent=2, sort_keys=True)` → validate against a committed `report-schema.json` → map gate failures to distinct non-zero exit codes. Lets the HUMAN-UAT scenarios assert on a machine-readable artifact.

### RUNBOOK section skeleton
**Source:** `gateway/docs/RUNBOOK-FAILOVER.md` (Read-this-when → Mental Model → Quick Diagnosis → Symptom blocks → operator commands → Required Env Vars table → Escalation → Related Docs).
**Apply to:** `RUNBOOK-CLIENT-INTEGRATION.md`.
The rollback procedure is the load-bearing section — mirror the numbered "To rotate a bearer secret" procedure (lines 376-384): per-app env-var diff + exact redeploy command + verify step.

### HUMAN-UAT deferral pattern
**Source:** `07-09-PLAN.md` (whole file), `06-HUMAN-UAT.md` (produced artifact).
**Apply to:** the Phase 8 `autonomous:false` plan + its `08-HUMAN-UAT.md`.
Autonomous plans ship gpu-ifix-side artifacts and stay green; the live `base_url` switch + production smoke + rollback drill + dashboard cross-check are gated behind a blocking `checkpoint:human-verify` task with a sign-off table and a `passed_partial` fallback.

## No Analog Found

| File / sub-feature | Role | Data Flow | Reason |
|--------------------|------|-----------|--------|
| transcription-quality comparison logic in `smoke-chat-ifix.py` | smoke sub-feature | transform | No prior smoke compares transcription *quality* against a recorded baseline — `pod/smoke/smoke.py` only checks Whisper returned 200 + non-empty text. The ±10% quality gate (word-error-rate / similarity vs reference transcript) is new logic; mirror only the "compute metric → boolean gate" *shape* from `apply_gates()` (smoke.py lines 315-327). |
| real WhatsApp audio fixture storage decision | test fixture | file-I/O | `pod/smoke/fixtures/` deliberately commits NO binaries (synthetic in-memory generation). Phase 8 needs a *real* speech sample for quality validation — planner must decide: commit a short real clip vs MinIO-host it like a weight (`pod/scripts/download-weights.sh` analog). |

## Metadata

**Analog search scope:** `gateway/cmd/gatewayctl/`, `gateway/docs/`, `docs/`, `pod/scripts/`, `pod/smoke/`, `pod/smoke/fixtures/`, `.github/workflows/`, `.planning/phases/{03,04,06,07}/`
**Files scanned:** ~25 (6 read in full: `tenant.go`, `admin_key.go`, `key.go`, `main.go`, `pod/smoke/smoke.py`, `gateway/docs/RUNBOOK-FAILOVER.md`, `07-09-PLAN.md`; targeted reads: `__gen_audio.py`, `fixtures/README.md`, `vast-ai.sh`, `upload-weights.sh`, `onstart.sh`, `migrate.go`, `06-HUMAN-UAT.md`, `smoke.yml`, ROADMAP Phase 8)
**Pattern extraction date:** 2026-05-14
</content>
</invoke>
