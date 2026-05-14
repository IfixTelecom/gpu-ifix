---
phase: 09-client-integration-sensitive-tenants-telefonia-cobran-as-campanhas-voice-api
verified: 2026-05-14T16:00:00Z
status: human_needed
score: 8/8 autonomous must-haves verified; 4 live-execution SCs deferred to human operator
overrides_applied: 0
human_verification:
  - test: "UAT-1 — Telefonia sensitive-failover smoke (SC1)"
    expected: "smoke-sensitive-failover.py exits 0 with gates.all_passed=true: fail_closed 503 + upstream_unavailable_for_sensitive_tenant + Retry-After:30; never_external audit_log upstream=blocked_sensitive; audit_decision row found + zero audit_log_content rows"
    why_human: "Requires deployed gateway (blocked on Phase 6 emerg tests), live telefonia tenant key, and a trippable tier-0 upstream"
  - test: "UAT-2 — Cobranças + Campanhas quotas + cost (SC2)"
    expected: "Per-tenant quotas enforced (cobrancas 2M/120rpm; campanhas 5M/300rpm); cost-per-request reported in dashboard; cobrancas never_external smoke exits 0 with all_passed"
    why_human: "Requires deployed gateway, live cobrancas+campanhas tenant keys, real LLM/embedding traffic"
  - test: "UAT-3 — voice-api LLM-via-gateway (SC3)"
    expected: "LLM script-generation routes through gateway (audit_log row under voice-api); TTS stays on local CPU (no gateway row for TTS)"
    why_human: "Requires deployed gateway, live voice-api tenant key, running voice-api instance"
  - test: "UAT-4 — Per-app rollback drill timed <5 min each (SC4)"
    expected: "Each of the 4 RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md rollback procedures completes in under 5 minutes, verified by psql audit_log row-count reaching 0 per tenant slug"
    why_human: "Requires deployed gateway + all 4 apps wired; only an operator can execute and time the drill"
  - test: "LGPD legal sign-off (SC4 external gate / PRD-05)"
    expected: "Ifix legal signs LGPD-REVIEW-CHECKLIST.md Sign-off table; signed copy attached to 09-HUMAN-UAT.md Final Sign-off; sensitive tenants NOT activated in production without this signature"
    why_human: "External legal review — cannot be obtained programmatically; BLOCKING gate per ROADMAP SC4 / PRD-05"
---

# Phase 9: Client Integration — Sensitive Tenants Verification Report

**Phase Goal:** Integrate the LGPD-sensitive and remaining business workloads behind the data-class-aware failover policy, with legal sign-off before turning on sensitive tenants in production.
**Verified:** 2026-05-14T16:00:00Z
**Status:** human_needed
**Re-verification:** No — initial verification (overwrites 09-04-plan-level stub)

## Goal Achievement

Phase 9's gpu-ifix-side artifacts (autonomous plans 09-01..09-03) are fully verified on disk. The phase goal's live-execution conditions — deployed gateway, operator env-var switches in the 4 client repos, external LGPD legal sign-off — are deferred to human operators per the established per-phase deferred-gate pattern (Phases 1-8). This matches the documented double-gate in 09-CONTEXT.md and 09-04-PLAN.md.

### Observable Truths — Autonomous Layer (09-01..09-03)

| # | Truth | Status | Evidence |
|---|-------|--------|----------|
| A1 | `provision-tenants.sh` provisions 4 Phase-9 tenants with per-tenant data_class (telefonia+cobrancas=sensitive, campanhas+voice-api=normal), applies per-tenant quotas to cobrancas+campanhas, keeps the idempotency branch, guards key-minting behind `--mint-keys` | VERIFIED | bash -n passes; TENANT_DATA_CLASS array present; set-quota step confirmed; idempotency branch confirmed; 327 lines (>140 min) |
| A2 | `--dry-run` prints tenant-create + set-quota calls and executes nothing against the DB | VERIFIED | `AI_GATEWAY_PG_DSN=postgres://x bash provision-tenants.sh --dry-run` prints dry-run output with set-quota lines; exit 0 |
| A3 | `smoke-sensitive-failover.py` compiles, encodes fail_closed + never_external + audit_decision gates keyed off `X-Request-ID`, uses `blocked_sensitive` audit lookup, emits schema-validated JSON report with distinct per-gate exit codes | VERIFIED | py_compile passes; all 7 content patterns confirmed; --help verified; psycopg in requirements.txt |
| A4 | `sensitive-failover-report-schema.json` is valid JSON Schema draft 2020-12 with additionalProperties:false everywhere, schema_version const 1.0.0, git_sha optional pattern, and gates object requiring fail_closed/never_external/audit_decision/all_passed | VERIFIED | Python assertion checks on schema structure — all pass |
| A5 | `RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md` has 4 per-app `### To roll back` subsections with env-var-revert + redeploy + psql audit_log verify, <5-min budget documented, expected sensitive-503 symptom, and no-peak-mode note | VERIFIED | 4x `### To roll back` confirmed; blocked_sensitive, 5-min, chk_sensitive_no_peak/peak, Required Env Vars all present |
| A6 | `LGPD-SUBPROCESSORS.md` explicitly lists OpenAI, OpenRouter, Vast.ai with data-class scope and the sensitive-never-external guarantee | VERIFIED | All 3 sub-processors confirmed; never/RES-08/blocked_sensitive terms present |
| A7 | `LGPD-REVIEW-CHECKLIST.md` has `- [ ]` checkbox items gating sensitive-tenant production activation including the `smoke-sensitive-failover.py` pass item and a `## Sign-off` table for Ifix legal | VERIFIED | checkboxes, sign-off section, smoke-sensitive-failover reference all confirmed |
| A8 | `09-HUMAN-UAT.md` has YAML frontmatter (status:pending, final_status:pending), Prerequisites checkboxes (gateway-deployed gate + 4 env-switch prereqs + LGPD gate), 4 UAT scenarios (SC1-SC4), Sign-off table, Final Sign-off LGPD blocking section, and passed_partial fallback | VERIFIED | All grep checks pass; 14 UAT- occurrences; provision-tenants and smoke-sensitive-failover cross-references confirmed |

**Autonomous score: 8/8**

### Observable Truths — Live-Execution Layer (require deployed gateway + operator)

| # | Truth (ROADMAP SC) | Status | Evidence |
|---|--------------------|--------|----------|
| L1 | SC1: Telefonia sensitive-failover smoke exits 0 with gates.all_passed against the deployed gateway | DEFERRED | Requires deployed gateway (blocked on Phase 6 emerg integration tests) + live telefonia key |
| L2 | SC2: Cobranças + Campanhas quotas enforced; cost-per-request reported in dashboard | DEFERRED | Requires deployed gateway + live cobrancas+campanhas keys + real traffic |
| L3 | SC3: voice-api LLM script-generation routes through gateway; TTS stays on local CPU | DEFERRED | Requires deployed gateway + live voice-api key + running voice-api instance |
| L4 | SC4: 4 per-app rollback drills completed <5 min each; LGPD legal sign-off attached | DEFERRED | Requires deployed gateway + operator time + external Ifix legal signature |

**Deferred to:** `09-HUMAN-UAT.md` Task 2 (checkpoint:human-verify, gate=blocking)

### Required Artifacts

| Artifact | Expected | Status | Details |
|----------|----------|--------|---------|
| `scripts/integration-smoke/provision-tenants.sh` | Idempotent 4-tenant seed + quotas + per-tenant data_class key minting | VERIFIED | 327 lines, executable (0755), bash -n passes |
| `scripts/integration-smoke/README.md` | Updated for Phase-9 4-tenant model + quota step + sensitive-failover smoke | VERIFIED | data_class, smoke-sensitive-failover, telefonia, idempotent, scope all present |
| `scripts/integration-smoke/smoke-sensitive-failover.py` | RES-08 black-box smoke — 3 mandatory gates + exit-code contract + schema-validated report | VERIFIED | 848 lines, py_compile passes, all content checks pass |
| `scripts/integration-smoke/sensitive-failover-report-schema.json` | JSON Schema draft 2020-12 for smoke report with gates.all_passed | VERIFIED | All schema assertions pass |
| `gateway/docs/RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md` | 4 per-app <5-min rollback procedures + sensitive-specific content | VERIFIED | 31,912 bytes; 4x `### To roll back` confirmed |
| `gateway/docs/LGPD-SUBPROCESSORS.md` | LGPD sub-processor disclosure listing OpenAI/OpenRouter/Vast.ai | VERIFIED | 5,622 bytes; all 3 sub-processors + never-external guarantee confirmed |
| `gateway/docs/LGPD-REVIEW-CHECKLIST.md` | LGPD review checklist + legal sign-off table | VERIFIED | 3,666 bytes; checkboxes + sign-off confirmed |
| `.planning/phases/09-.../09-HUMAN-UAT.md` | Live-verification scenario sheet + blocking LGPD legal sign-off gate | VERIFIED | 23,271 bytes; all structural checks pass |

### Key Link Verification

| From | To | Via | Status | Details |
|------|-----|-----|--------|---------|
| `provision-tenants.sh` | gatewayctl tenant create / key create / set-quota / admin-key create | `run_gatewayctl` helper; `mint_tenant_key` function | VERIFIED | `mint_tenant_key` calls `key create --tenant "$slug" --data-class "$data_class"`; set-quota wired after tenant-create before --mint-keys gate |
| `provision-tenants.sh` | gateway DB with per-tenant data_class + quota config | gatewayctl writes rows; script parses stdout | VERIFIED | TENANT_DATA_CLASS drives per-tenant --data-class; QUOTA_TENANTS/QUOTA_DAILY_TOKENS/QUOTA_RPM arrays drive set-quota for cobrancas+campanhas |
| `smoke-sensitive-failover.py` | gateway /v1/chat/completions (sensitive tenant key) | httpx POST during induced tier-0 failure | VERIFIED | httpx request confirmed; --induce-failure-via operator-prestep polls /v1/health/upstreams before evaluating gates |
| `smoke-sensitive-failover.py` | ai_gateway.audit_log + audit_log_content | psycopg direct DB read on X-Request-ID | VERIFIED | psycopg confirmed in requirements.txt; SELECT upstream + SELECT COUNT(*) patterns confirmed in code |
| `RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md` | 4 client sibling repos rollback procedures | env-var revert + redeploy + psql audit_log verify | VERIFIED | BASE_URL + Required Env Vars table + 4x rollback subsections with psql verify confirmed |
| `LGPD-SUBPROCESSORS.md` | RES-08 sensitive-block mechanism + RUNBOOK | Mechanism section cross-reference | VERIFIED | RES-08/blocked_sensitive cross-reference confirmed |
| `09-HUMAN-UAT.md` | provision-tenants.sh + smoke-sensitive-failover.py + RUNBOOK + LGPD-REVIEW-CHECKLIST.md | UAT steps invoke scripts; rollback drill executes runbook; Final Sign-off attaches checklist | VERIFIED | All 4 artifact cross-references confirmed |

### Data-Flow Trace (Level 4)

Not applicable. Phase 9 artifacts are operator scripts and documentation, not data-rendering UI components. The smoke script's data flow (httpx POST to gateway → X-Request-ID capture → psycopg audit_log query → JSON report write) is verified structurally via Level 3 key-link checks above.

### Behavioral Spot-Checks

| Behavior | Command | Result | Status |
|----------|---------|--------|--------|
| `provision-tenants.sh` syntax valid | `bash -n scripts/integration-smoke/provision-tenants.sh` | exit 0 | PASS |
| `--dry-run` prints set-quota without DB hit | `AI_GATEWAY_PG_DSN=postgres://x bash provision-tenants.sh --dry-run 2>&1` | dry-run output contains set-quota; exit 0 | PASS |
| `smoke-sensitive-failover.py` compiles | `python3 -m py_compile smoke-sensitive-failover.py` | exit 0 | PASS |
| JSON schema structurally valid | Python assertions on schema properties | all assertions pass | PASS |
| Live UAT-1..UAT-4 execution | Operator runs 09-HUMAN-UAT.md | Not executed — gateway not deployed | SKIP (human_needed) |

### Probe Execution

No probe-*.sh scripts declared or found for Phase 9. Step 7c: SKIPPED (no runnable probes for this phase).

### Requirements Coverage

| Requirement | Source Plans | Description | Status | Evidence |
|-------------|-------------|-------------|--------|----------|
| INT-03 | 09-01, 09-02, 09-03, 09-04 | Telefonia/NextBilling migrated to gateway with data_class:sensitive | PARTIAL — autonomous VERIFIED; live activation DEFERRED | provision-tenants.sh seeds telefonia as sensitive; smoke encodes RES-08 proof; RUNBOOK has telefonia rollback; UAT-1 drills SC1. Live execution requires deployed gateway. |
| INT-04 | 09-01, 09-02, 09-03, 09-04 | Cobranças + Campanhas migrated to gateway with LLM/embedding + quotas | PARTIAL — autonomous VERIFIED; live activation DEFERRED | provision-tenants.sh seeds cobrancas (sensitive) + campanhas (normal) with per-tenant quotas; smoke covers cobrancas sensitive proof; RUNBOOK has cobrancas+campanhas rollback; UAT-2 drills SC2. |
| INT-05 | 09-01, 09-03, 09-04 | voice-api uses gateway for LLM script-generation; TTS stays on local CPU | PARTIAL — autonomous VERIFIED; live activation DEFERRED | provision-tenants.sh seeds voice-api as normal; RUNBOOK has voice-api rollback; UAT-3 drills SC3. Live execution requires deployed gateway. |

REQUIREMENTS.md marks INT-03, INT-04, INT-05 as "Pending" — correct, as this reflects live production activation which is deferred to the HUMAN-UAT. All 3 requirements are claimed by Phase 9 plans and no orphaned requirements exist.

### Anti-Patterns Found

| File | Line | Pattern | Severity | Impact |
|------|------|---------|----------|--------|
| (none) | — | No TBD/FIXME/XXX debt markers found in any Phase 9 modified file | — | — |

4 INFO items from 09-REVIEW.md (IN-01..IN-04) are non-blocking cosmetic/defensive improvements carrying forward — none gate this phase.

### Human Verification Required

#### 1. UAT-1 — Telefonia Sensitive-Failover Smoke (SC1)

**Test:** Deploy gateway to `ai-gateway-dev` Portainer stack; run `provision-tenants.sh --mint-keys`; switch `fallback-register-ramais-nextbilling` STT config to gateway; follow smoke's operator pre-step to trip tier-0; run `python scripts/integration-smoke/smoke-sensitive-failover.py --gateway-url <dev> --api-key <telefonia-key> --pg-dsn <DSN> --out /tmp/report.json`
**Expected:** Exit 0; `jq '.gates.all_passed' report.json` = `true`; fail_closed/never_external/audit_decision gates all pass
**Why human:** Requires deployed gateway (currently blocked on Phase 6 emergency-pod integration tests) + live tenant key + trippable local-llm upstream

#### 2. UAT-2 — Cobranças + Campanhas Quotas + Cost (SC2)

**Test:** Switch `cobrancas-api` + `campanhas-chatifix` base_url/api_key to gateway; drive LLM-personalization + embedding traffic; re-run smoke with cobrancas key; check Phase 7 dashboard / `/admin/usage` for cost-per-tenant
**Expected:** Per-tenant quotas enforced (2M/120rpm cobrancas; 5M/300rpm campanhas); cost-per-request reported per tenant; cobrancas smoke exits 0 all_passed
**Why human:** Requires deployed gateway + live keys + real traffic + running dashboard

#### 3. UAT-3 — voice-api LLM-via-Gateway (SC3)

**Test:** Switch voice-api LLM script-gen config to gateway URL + voice-api key; trigger a script-generation call; verify audit_log has a `voice-api` row AND TTS has no gateway row
**Expected:** LLM script generation works via gateway; TTS unaffected on local CPU
**Why human:** Requires deployed gateway + live voice-api key + running voice-api instance

#### 4. UAT-4 — Per-App Rollback Drill Timed <5 Min Each (SC4)

**Test:** For each of 4 apps, start stopwatch; execute `### To roll back <app>` from `gateway/docs/RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md`; verify psql `audit_log` row-count reaches 0; record time
**Expected:** All 4 apps fully rolled back in under 5 minutes each
**Why human:** Requires deployed gateway + all 4 apps wired; only an operator can execute, time, and verify

#### 5. LGPD Legal Sign-Off — BLOCKING External Gate (SC4 / PRD-05)

**Test:** Ifix legal reviews and signs `gateway/docs/LGPD-REVIEW-CHECKLIST.md` Sign-off table; operator attaches signed copy to `09-HUMAN-UAT.md ## Final Sign-off`
**Expected:** Signed checklist attached; Final Sign-off table filled with Reviewer/Role/Date/approval-reference
**Why human:** External legal review is a human gate. Sensitive tenants (telefonia, cobrancas) MUST NOT be activated in production without this signature. ROADMAP SC4 / PRD-05.

### Gaps Summary

No gaps. All autonomous artifacts are fully implemented and verified on disk. The `human_needed` status is structural — it reflects the established deferred-gate pattern used by every prior phase. The live-execution items are gated on:

1. The gateway being deployed to `ai-gateway-dev` (currently blocked on Phase 6 emergency-pod integration tests — separate debug session)
2. The operator running env-var switches in the 4 client sibling repos (not edited by gpu-ifix per 09-CONTEXT.md boundary decision)
3. External Ifix legal sign-off on LGPD-REVIEW-CHECKLIST.md

None of these are defects in Phase 9 artifacts. The `09-HUMAN-UAT.md` `## passed_partial fallback` documents the path when the gateway is not yet deployed or the LGPD signature is pending.

---

_Verified: 2026-05-14T16:00:00Z_
_Verifier: Claude (gsd-verifier)_
