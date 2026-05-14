# Phase 9: Client Integration — Sensitive Tenants - Pattern Map

**Mapped:** 2026-05-14
**Files analyzed:** 5 (1 extended, 4 new)
**Analogs found:** 4 strong / 5 (1 no-analog: LGPD docs)

## Scope note

Phase 9 delivers **gpu-ifix-side artifacts only**. The 4 client app repos
(`fallback-register-ramais-nextbilling`, `cobrancas-api`, `campanhas-chatifix`,
`voice-api`) are separate sibling repos and are NOT edited — `base_url`/env-var
switches are operator HUMAN-UAT actions. Phase 9 EXTENDS the
`scripts/integration-smoke/` directory created by Phase 8.

## File Classification

| New/Modified File | Role | Data Flow | Closest Analog | Match Quality |
|-------------------|------|-----------|----------------|---------------|
| `scripts/integration-smoke/provision-tenants.sh` (EXTEND, or sibling `provision-tenants-sensitive.sh`) | config / seed script | batch (idempotent CLI wrapper) | itself (Phase 8 08-01) | exact (self-extension) |
| `scripts/integration-smoke/smoke-sensitive-failover.py` (NEW) | test / smoke script | request-response + induced-failure assertion | `scripts/integration-smoke/smoke-chat-ifix.py` + `sensitive_block_test.go` | role-match (compose two analogs) |
| `scripts/integration-smoke/sensitive-failover-report-schema.json` (NEW) | config / schema | n/a (JSON Schema) | `scripts/integration-smoke/report-schema.json` | exact |
| `gateway/docs/RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md` (NEW) | docs / runbook | n/a | `gateway/docs/RUNBOOK-CLIENT-INTEGRATION.md` + `RUNBOOK-FAILOVER.md` | exact |
| `gateway/docs/LGPD-SUBPROCESSORS.md` + `gateway/docs/LGPD-REVIEW-CHECKLIST.md` (NEW) | docs / compliance | n/a | none (no compliance docs exist) — structurally a RUNBOOK | no-analog (structure-only) |
| `.planning/phases/09-.../09-HUMAN-UAT.md` (NEW) + closing `09-NN-PLAN.md` (`autonomous: false`) | docs / UAT plan | n/a | `08-HUMAN-UAT.md` + `08-04-PLAN.md` | exact |

## Pattern Assignments

### `scripts/integration-smoke/provision-tenants.sh` — EXTEND (config, batch)

**Analog:** itself — `scripts/integration-smoke/provision-tenants.sh` (Phase 8 08-01). Same file, extended with the 4 Phase 9 tenants. A sibling script `provision-tenants-sensitive.sh` is an acceptable alternative if the planner prefers isolation; either way it copies this file's structure verbatim.

**What changes vs Phase 8:** the Phase 8 file hardcodes `DATA_CLASS="normal"` as a single scalar and 2 tenants. Phase 9 needs **per-tenant `data_class`** (telefonia + cobrancas = `sensitive`; campanhas + voice-api = `normal`) and **per-tenant quotas** for cobrancas + campanhas. The `TENANT_SLUGS` / `TENANT_NAMES` parallel-array idiom must gain a parallel `TENANT_DATA_CLASS` array; the scalar `DATA_CLASS` is removed.

**Existing parallel-array tenant model to extend** (`provision-tenants.sh:115-120`):
```bash
# --- tenant model (08-CONTEXT.md `## Decisions`) --------------------------
TENANT_SLUGS=("converseai" "chat-ifix")
TENANT_NAMES=("ConverseAI v4" "Chat Ifix")
DATA_CLASS="normal"
```
Phase 9 shape — add a parallel `TENANT_DATA_CLASS` array, drop the scalar:
```bash
TENANT_SLUGS=("telefonia" "cobrancas" "campanhas" "voice-api")
TENANT_NAMES=("Telefonia / NextBilling" "Cobranças" "Campanhas" "voice-api")
TENANT_DATA_CLASS=("sensitive" "sensitive" "normal" "normal")
```

**Idempotent tenant-create loop to copy** (`provision-tenants.sh:122-145`) — the `GW_RC==1` + `grep -qF "tenant slug '$slug' already exists"` exact-message match is the load-bearing idempotency signal; reuse it unchanged. Pass per-tenant data_class via the loop index:
```bash
for i in "${!TENANT_SLUGS[@]}"; do
  slug="${TENANT_SLUGS[$i]}"; name="${TENANT_NAMES[$i]}"
  run_gatewayctl tenant create --name "$name" --slug "$slug"
  # ... existing GW_RC==0 / GW_RC==1+grep-exists / else-fail branches ...
done
```

**Key-mint pattern to copy** (`provision-tenants.sh:166-205`) — the `--mint-keys` opt-in gate, `parse_key` (asserts exactly 1 `^key=` line), `parse_id`, and the secret-once stdout block. Phase 9 mints with **per-tenant** `--data-class`:
```bash
run_gatewayctl key create --tenant telefonia --data-class sensitive
run_gatewayctl key create --tenant cobrancas --data-class sensitive
run_gatewayctl key create --tenant campanhas --data-class normal
run_gatewayctl key create --tenant voice-api --data-class normal
```

**Per-tenant quota — NEW step (SC2), analog is `gatewayctl tenant set-quota`** (`gateway/cmd/gatewayctl/tenant.go:216-260`). Add a quota loop after tenant-create, before/independent of `--mint-keys` (set-quota is an idempotent UPDATE — safe to always run). Available flags (all `-1` = unchanged): `--daily-tokens`, `--monthly-tokens`, `--daily-audio-minutes`, `--monthly-audio-minutes`, `--daily-embeds`, `--monthly-embeds`, `--rps`, `--rpm`. Only cobrancas + campanhas get quotas per CONTEXT:
```bash
run_gatewayctl tenant set-quota --tenant cobrancas --daily-tokens <N> --rpm <N>
run_gatewayctl tenant set-quota --tenant campanhas --daily-tokens <N> --rpm <N>
```
Wrap each in the existing `run_gatewayctl` helper so `--dry-run` and `GW_RC`/`GW_OUT` capture work uniformly. `set-quota` exits 1 on unknown slug (`tenant %q not found`) — treat non-zero as fatal (NOT the idempotency-OK case; the tenant must already exist from step 1).

**Header comment block** (`provision-tenants.sh:1-46`) — rewrite the module docstring to describe the 4-tenant mixed-data-class model + the quota step; keep the same Idempotency / Secrets / Usage / Env sections.

---

### `scripts/integration-smoke/smoke-sensitive-failover.py` — NEW (test, request-response + induced-failure)

**Primary analog:** `scripts/integration-smoke/smoke-chat-ifix.py` — copy the whole script skeleton.
**Behavioral analog:** `gateway/internal/integration_test/sensitive_block_test.go` — what to assert.

**Module docstring + structure to copy** (`smoke-chat-ifix.py:1-40`) — triple-quote module doc explaining target + SC + the distinct-exit-code table; `from __future__ import annotations`; imports `argparse, asyncio, dataclasses, json, os, subprocess, sys, time` + `httpx, structlog`; `SCHEMA_VERSION = "1.0.0"`; `log = structlog.get_logger().bind(module="SMOKE_SENSITIVE_FAILOVER")`.

**Config + CLI pattern** (`smoke-chat-ifix.py:69-110`) — `@dataclasses.dataclass class Config`, `parse_args() -> Config`, `--gateway-url`/`--api-key`/`--out` each defaulting to `SMOKE_*` env vars. **Secret-once discipline:** the sensitive tenant key (telefonia or cobrancas) comes ONLY from `--api-key` / `SMOKE_API_KEY` — no committed default, argparse-error + no network call if absent. Phase 9 also needs an induced-failure trigger arg (e.g. `--induce-failure-via` — how the smoke trips the tier-0 breaker; planner decides: a gatewayctl/admin call, or a documented operator pre-step).

**Report-write + schema-validate + git_sha + exit-code tail to copy** (`smoke-chat-ifix.py` last ~70 lines) — build `report` dict, validate against the schema with `Draft202012Validator(...).validate(report)` (warn-don't-fail on mismatch), `Path(cfg.out_path).write_text(json.dumps(report, indent=2, sort_keys=True))`, optional `git_sha` via `git rev-parse --short HEAD`, then `code = exit_code_for_gates(...)`; `main()` does `sys.exit(asyncio.run(main_async(cfg)))`.

**Exit-code contract** (`smoke-chat-ifix.py:30-40` shape) — `0` all gates passed, distinct non-zero per gate, `6` multiple, `1` fallback/unexpected. Phase 9 gates derive from `sensitive_block_test.go`:

**The assertions to encode as gates** (from `sensitive_block_test.go:34-152`) — the smoke must verify RES-08 end-to-end against the deployed gateway:
1. **fail-closed gate** — sensitive request during induced upstream failure returns `503` with envelope code `upstream_unavailable_for_sensitive_tenant` and `Retry-After: 30` (test lines 104-115).
2. **never-external gate** — the request is NEVER proxied to OpenAI/OpenRouter. In-process the test asserts `tier1.hits.Load() == 0`; the smoke's black-box equivalent is asserting the response/audit shows `upstream='blocked_sensitive'` and no external-provider signature (test lines 116-119, 137-143).
3. **audit gate** — an `audit_log` row exists for the request with `upstream = 'blocked_sensitive'` (`audit.UpstreamBlockedSensitive`), AND **no `audit_log_content` row** for that request_id — D-B2, sensitive content is never persisted (test lines 128-152). The smoke needs read access to the gateway DB (DSN env var, same as `provision-tenants.sh`'s `AI_GATEWAY_PG_DSN`) OR an admin audit endpoint — planner decides; `gateway/internal/admin/audit.go` exists as an admin-side audit reader.
4. (optional) **streaming fail-fast gate** — sensitive + `stream:true` 503s in <500ms with no retry-loop pre-flight (`sensitive_block_test.go:154-210`, D-B4).

**Request-id correlation pattern** (`sensitive_block_test.go:96-127`) — pull the gateway-issued id from the `X-Request-ID` response header, then query `audit_log WHERE request_id=$1`. The smoke should capture `X-Request-ID` from its httpx response and use it for the audit lookup.

**The RES-08 contract being verified — server side** (`gateway/internal/proxy/sensitive.go:1-67`): sensitive tenants get a bounded 3-attempt retry (200ms/800ms/3s ~4s) that awaits a CLOSED breaker transition and **MUST NOT route to external upstreams**; exhaustion → `ErrSensitiveRetryExhausted` → caller maps to 503. The smoke is the black-box proof of this.

---

### `scripts/integration-smoke/sensitive-failover-report-schema.json` — NEW (config, JSON Schema)

**Analog:** `scripts/integration-smoke/report-schema.json` (and `chat-ifix-report-schema.json`).

**Pattern to copy** (`report-schema.json` in full):
```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://ifixtelecom.com.br/schemas/integration-smoke/sensitive-failover-report/1.0.0",
  "title": "Sensitive Failover Smoke Report",
  "version": "1.0.0",
  "type": "object",
  "additionalProperties": false,
  "required": ["schema_version", "started_at", "finished_at", "target", "errors", "gates"],
  "properties": {
    "schema_version": { "type": "string", "const": "1.0.0" },
    "started_at": { "type": "string", "format": "date-time" },
    "finished_at": { "type": "string", "format": "date-time" },
    "target": {
      "type": "object",
      "required": ["gateway_url", "tenant"],
      "properties": {
        "gateway_url": { "type": "string", "format": "uri" },
        "tenant": { "type": "string" }
      },
      "additionalProperties": false
    },
    "git_sha": { "type": "string", "pattern": "^[0-9a-f]{7,40}$" }
    /* + per-check objects: fail_closed, never_external, audit_decision,
       (optional) streaming_fail_fast — each {status_code, ok, ...} like the
       `chat` / `chat_stream` blocks; + "gates" object mirroring the per-check
       booleans + "all_passed" */
  }
}
```
Keep `additionalProperties: false` on every sub-object (the Phase 8 schemas enforce this strictly), `schema_version` as a `const`, `git_sha` optional with the `^[0-9a-f]{7,40}$` pattern, and a `gates` object whose keys are the per-check booleans plus `all_passed` — exactly the `report-schema.json` `gates` shape.

---

### `gateway/docs/RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md` — NEW (docs, runbook)

**Analog:** `gateway/docs/RUNBOOK-CLIENT-INTEGRATION.md` (Phase 8 08-04) — same domain, copy its full section skeleton. Secondary: `gateway/docs/RUNBOOK-FAILOVER.md` for the rollback-procedure shape.

**Location:** `gateway/docs/` — the dominant convention (5 of 5 runbooks live there).

**Section skeleton to mirror** (`RUNBOOK-CLIENT-INTEGRATION.md` headers):
```
# <Title> — Telefonia + Cobranças + Campanhas + voice-api
**Read this when:** trigger bullets (incl. "needs to be rolled back")
## Mental Model (30 seconds)   — the two-knobs table + ASCII diagram, adapted
                                 to the data-class split (sensitive NEVER
                                 fails over external; normal does)
## Quick Diagnosis (~2 minutes) — numbered curl / psql / docker exec block;
                                 add a sensitive-block check (audit_log
                                 upstream='blocked_sensitive')
## Incident Response by Symptom — `### Symptom N` blocks (Likely cause /
                                 Diagnose / Mitigate / Recovery)
## ROLLBACK procedure          — per-app `### To roll back <app>` subsections
## Required Env Vars           — `| Var | Required | Purpose |` table
## Escalation
## Related Docs
```

**The load-bearing ROLLBACK pattern to copy** (`RUNBOOK-CLIENT-INTEGRATION.md:270-371`) — per-app `### To roll back <app>` subsections, each a numbered env-var revert + redeploy + verify procedure. Phase 9 needs **4** such subsections (telefonia, cobrancas, campanhas, voice-api). SC4 target: **under 5 minutes per app** — same bar as Phase 8. The verify step is a `psql` audit_log row-count that must go to 0 for the tenant slug.

**Required Env Vars table pattern** (`RUNBOOK-CLIENT-INTEGRATION.md:371` / `RUNBOOK-FAILOVER.md:387-413`) — `| Var | Required | Purpose |`.

**Sensitive-specific content NOT in the Phase 8 runbook** — must add: a Symptom for "sensitive tenant getting 503s during upstream outage" (this is EXPECTED RES-08 behavior, not a bug — cross-ref `RUNBOOK-FAILOVER.md` Symptom 3 "Sensitive tenant reports 503s"); a note that sensitive tenants CANNOT be set to `peak` mode (the `chk_sensitive_no_peak` triple-defense, `sensitive_peak_reject_test.go`).

---

### `gateway/docs/LGPD-SUBPROCESSORS.md` + `gateway/docs/LGPD-REVIEW-CHECKLIST.md` — NEW (docs, compliance)

**Analog:** NONE — no compliance/disclosure docs exist in `docs/` or `gateway/docs/` (only RUNBOOKs + `CONVENTIONS.md`). Structurally treat as a RUNBOOK-style markdown doc: top-level `#` title, dated, sectioned with `##`.

**Content requirements (from 09-CONTEXT.md `## Decisions` + `## Specific Ideas`):**
- `LGPD-SUBPROCESSORS.md` — a sub-processor disclosure document that **explicitly lists OpenAI, OpenRouter, and Vast.ai** as sub-processadores, with what data class each can receive. Key fact to encode: `data_class: sensitive` tenants (telefonia, cobrancas) are NEVER proxied to OpenAI/OpenRouter — only `normal` tenants (campanhas, voice-api) can reach external sub-processors during failover. Vast.ai hosts the GPU pod (the local upstream).
- `LGPD-REVIEW-CHECKLIST.md` — an operator checklist (`- [ ]` items, same checkbox idiom as `08-HUMAN-UAT.md` `## Prerequisites`) gating sensitive-tenant production activation. The actual legal sign-off is an **external gate** — the checklist is the artifact, the signature is obtained from Ifix legal and captured as a checkpoint in the HUMAN-UAT plan.

**Cross-references:** both docs link to `RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md` and to the RES-08 mechanism. Add to the `## Related Docs` sections of the runbooks.

---

### `.planning/phases/09-.../09-HUMAN-UAT.md` + closing `09-NN-PLAN.md` (`autonomous: false`) — NEW (docs, UAT plan)

**Plan analog:** `.planning/phases/08-client-integration-converseai-chat-ifix/08-04-PLAN.md`.
**Produced-artifact analog:** `.planning/phases/08-client-integration-converseai-chat-ifix/08-HUMAN-UAT.md` (also 07-09 / 06-11).

**Plan frontmatter pattern to copy** (`08-04-PLAN.md:1-37`) — `type: execute`, `autonomous: false`, `depends_on:` the autonomous Phase 9 plans, `files_modified:` listing the new runbook + the `09-HUMAN-UAT.md`, `must_haves:` with `truths:` / `artifacts:` (path/provides/contains) / `key_links:` (from/to/via/pattern). The objective must state the autonomous Phase 9 plans (seed-script extension, sensitive smoke, schema, runbook, LGPD docs) ship the gpu-ifix-side artifacts and are **NOT blocked** by this plan or the un-deployed gateway — the doubly-deferred framing (gateway deploy is blocked on Phase 6 emerg integration tests).

**HUMAN-UAT.md structure to copy** (`08-HUMAN-UAT.md` headers):
```
--- (frontmatter: status: pending, phase, source, started/updated/operator/
     date_executed blanks, final_status: pending) ---
# Phase 9 — Human UAT (Live Sensitive-Tenant Integration)
intro: live integration framing + "autonomous build NOT blocked" paragraph
## Prerequisites          — `- [ ]` checkbox list (gateway-deployed gate,
                            provision-tenants run, per-app env switch,
                            LGPD sign-off obtained)
## Current Test / ## Tests
### N. UAT-N — <name> (SC-N)  — Pre-conditions / Steps (numbered bash) /
                                Expected / pass-fail
## Summary
## Sign-off               — table
## Final Sign-off
## passed_partial fallback — the deploy/credential-blocked fallback note
## Gaps
```

**UAT scenarios for Phase 9** (mirror `08-HUMAN-UAT.md` UAT-1..4 shape) — env switch + production smoke per app (telefonia, cobrancas, campanhas, voice-api); the **sensitive-failover smoke run** (`smoke-sensitive-failover.py` against a sensitive tenant key, asserting `exit 0` + `gates.all_passed`); the **rollback drill** timed <5 min per app against `RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md`; and a **blocking checkpoint** for the external LGPD legal sign-off (operator attaches the signed-off `LGPD-REVIEW-CHECKLIST.md`).

**passed_partial fallback** (`08-HUMAN-UAT.md:319-337`) — copy verbatim: if the gateway-not-deployed gate fails, every UAT is `passed_partial`, blocker noted in the Sign-off table Notes column.

## Shared Patterns

### gatewayctl-wrapper idempotency discipline
**Source:** `scripts/integration-smoke/provision-tenants.sh:78-110` (the `run_gatewayctl` helper + `GW_OUT`/`GW_RC` capture) and `:122-145` (the exact-message `grep -qF` idempotency branch).
**Apply to:** the extended `provision-tenants.sh`. Every `gatewayctl` call goes through `run_gatewayctl` so `--dry-run` and combined-output capture work uniformly. Idempotency is signalled ONLY by gatewayctl's EXACT stderr message matched with `grep -F` — never an unanchored substring.

### secret-once discipline
**Source:** `provision-tenants.sh:166-258` (raw keys printed to stdout EXACTLY ONCE, never to `log()`/stderr) and `smoke-chat-ifix.py:21-26` (tenant key only via `--api-key`/`SMOKE_API_KEY`, no committed default).
**Apply to:** the extended seed script (4 tenant keys + admin key) and `smoke-sensitive-failover.py`. No secret is ever committed, logged to a redirectable stream, or given a default.

### smoke-script contract: exit-code + schema-validated JSON report
**Source:** `smoke-chat-ifix.py` (distinct non-zero exit per gate, `6` = multiple, `1` = fallback) + `report-schema.json` / `chat-ifix-report-schema.json` (`additionalProperties: false`, `schema_version` const, `gates` object with `all_passed`).
**Apply to:** `smoke-sensitive-failover.py` + `sensitive-failover-report-schema.json`. The HUMAN-UAT asserts on `exit 0` + `gates.all_passed == true`.

### RUNBOOK section skeleton
**Source:** `gateway/docs/RUNBOOK-CLIENT-INTEGRATION.md` + `RUNBOOK-FAILOVER.md` — Title + "Read this when" / Mental Model (30s) / Quick Diagnosis (~2 min) / Incident Response by Symptom / ROLLBACK / Required Env Vars table / Escalation / Related Docs.
**Apply to:** `RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md`; the LGPD docs reuse the title+dated+`##`-sectioned markdown shell.

### HUMAN-UAT deferral pattern
**Source:** `08-04-PLAN.md` (`autonomous: false` closing plan) + `08-HUMAN-UAT.md` (frontmatter with `final_status: pending`, `## Prerequisites` checkboxes, numbered `### N. UAT-N` blocks, Sign-off table, `passed_partial` fallback). Established across 03-08 / 04-09 / 06-11 / 07-09 / 08-04.
**Apply to:** the Phase 9 closing plan + `09-HUMAN-UAT.md`.

## No Analog Found

| File | Role | Data Flow | Reason |
|------|------|-----------|--------|
| `gateway/docs/LGPD-SUBPROCESSORS.md` | docs / compliance | n/a | No compliance/legal/disclosure docs exist anywhere in the repo (`docs/` has only `CONVENTIONS.md` + a RUNBOOK; `gateway/docs/` is all RUNBOOKs). Structure-only guidance: reuse the RUNBOOK markdown shell; content is net-new from 09-CONTEXT.md. |
| `gateway/docs/LGPD-REVIEW-CHECKLIST.md` | docs / compliance | n/a | Same — no checklist-style compliance doc exists. Reuse the `08-HUMAN-UAT.md` `## Prerequisites` `- [ ]` checkbox idiom for the checklist items. |

## Metadata

**Analog search scope:** `scripts/integration-smoke/`, `gateway/docs/`, `docs/`, `gateway/internal/proxy/`, `gateway/internal/integration_test/`, `gateway/internal/audit/`, `gateway/cmd/gatewayctl/`, `.planning/phases/08-*`
**Files scanned:** ~25 (read in full or targeted: provision-tenants.sh, report-schema.json, requirements.txt, smoke-chat-ifix.py [head+tail], proxy/sensitive.go, sensitive_block_test.go, sensitive_peak_reject_test.go [head], tenant.go [set-quota], RUNBOOK-CLIENT-INTEGRATION.md [head+headers], RUNBOOK-FAILOVER.md [headers], 08-04-PLAN.md [head], 08-HUMAN-UAT.md [head+headers])
**Pattern extraction date:** 2026-05-14
