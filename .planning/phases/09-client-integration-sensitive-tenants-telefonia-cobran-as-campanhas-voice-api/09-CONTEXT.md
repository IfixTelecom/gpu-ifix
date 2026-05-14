# Phase 9: Client Integration — Sensitive Tenants (Telefonia, Cobranças, Campanhas, voice-api) - Context

**Gathered:** 2026-05-14
**Status:** Ready for planning

<domain>
## Phase Boundary

Integrate the LGPD-sensitive and remaining business workloads behind the data-class-aware failover policy: Telefonia/NextBilling (call audio transcription), Cobranças + Campanhas (LLM personalization + embeddings), and voice-api (LLM script generation). Delivers INT-03, INT-04, INT-05 — with documented LGPD review before sensitive tenants go live in production.

This phase delivers the **gpu-ifix-side artifacts** (tenant provisioning, sensitive-failover smoke test, rollback runbook, LGPD disclosure + checklist, HUMAN-UAT plan). The `base_url`/env-var switches inside the client app repos are operator HUMAN-UAT actions; the LGPD legal sign-off is an external gate. Live validation is additionally gated on the gateway being deployed (currently blocked on Phase 6 emerg integration tests).

Out of scope: production load/chaos testing — Phase 10.

</domain>

<decisions>
## Implementation Decisions

### Tenants, data_class & Repo Boundary
- **Four tenants**, each provisioned in the gateway:
  - `telefonia` — **`data_class: sensitive`** (call audio is PII)
  - `cobrancas` — **`data_class: sensitive`** (financial / collections data; RES-08 names Cobranças sensitive)
  - `campanhas` — **`data_class: normal`** (marketing personalization, not LGPD-sensitive PII — external failover is allowed)
  - `voice-api` — **`data_class: normal`** (LLM script generation; TTS stays local CPU)
- Repo boundary: **same as Phase 8** — the gpu-ifix repo delivers the provisioning script, the sensitive-failover smoke test, the rollback runbook, and the LGPD documentation. The `base_url`/env-var switches inside the client repos are operator actions (HUMAN-UAT), not edited by this phase's plans.
- Client app repos (siblings, NOT edited here): `fallback-register-ramais-nextbilling` (Telefonia/NextBilling), `cobrancas-api` (Cobranças), `campanhas-chatifix` (Campanhas), `voice-api`.

### Sensitive Failover Verification & LGPD
- SC1 verification: a **smoke test that exercises the sensitive-class failover path** — induce an upstream failure and assert the request **queues or fails closed**, an `audit_log` row records the decision, and it is **never proxied to OpenAI/OpenRouter**. Reuses the Phase 3 sensitive-class machinery (RES-08).
- LGPD: the gpu-ifix repo delivers a **sub-processor disclosure document** (lists OpenAI, OpenRouter, Vast.ai as sub-processadores) plus an **LGPD review checklist**. The actual legal sign-off is an **external gate** — the operator obtains it from Ifix legal before activating sensitive tenants in production; captured as a checkpoint in the HUMAN-UAT plan.
- A final **HUMAN-UAT plan (`autonomous: false`)** covers the live integration: env switch in each client repo, production smoke per app, the rollback drill, and the LGPD sign-off. Mirrors the 03-08 / 04-09 / 06-11 / 07-09 / 08-04 pattern.
- Per-tenant quotas (SC2 — Cobranças + Campanhas) are provisioned via `gatewayctl tenant set-quota` in the seed script (Phase 4 quota machinery).

</decisions>

<code_context>
## Existing Code Insights

### Reusable Assets
- `scripts/integration-smoke/` — created in Phase 8: `provision-tenants.sh` (idempotent `gatewayctl` wrapper), `smoke-converseai.py` / `smoke-chat-ifix.py` (gateway contract smokes), `report-schema.json`, `requirements.txt`, `fixtures/`. Phase 9 extends this directory — the provisioning script gains the 4 sensitive/normal tenants, a new sensitive-failover smoke is added.
- Phase 3 sensitive-class failover policy (RES-08): `data_class: sensitive` requests are never proxied to OpenAI/OpenRouter during failover — they queue (short retry) or return a controlled error, and an `audit_log` row records the decision. The Phase 9 sensitive smoke verifies this end-to-end.
- `gatewayctl tenant` (create, set-mode, set-quota — Phase 2/4), `gatewayctl key` / `admin-key create` (Phase 2/4). Phase 4 quota machinery for SC2.
- Prior HUMAN-UAT plans (03-08, 04-09, 06-11, 07-09, 08-04) + the RUNBOOK pattern (`gateway/docs/RUNBOOK-*.md`) — structural templates.

### Established Patterns
- Client apps are **separate sibling repos** — not part of gpu-ifix. Integration is an env-var change (`base_url` → gateway, `api_key` → tenant key); OpenAI-compat contract means no client code rewrite.
- HUMAN-UAT / live-validation deferral is the established per-phase pattern when success criteria need a deployed environment.
- The smoke-script + exit-code + schema-validated-report contract from Phase 8 is the template for the new sensitive smoke.

### Integration Points
- gpu-ifix seed script ↔ `gatewayctl` ↔ gateway DB (`tenants`, `api_keys`, quota config).
- Sensitive smoke ↔ gateway `/v1/*` with a `data_class: sensitive` tenant key + an induced-failure trigger ↔ `audit_log` (assert the queue/fail-closed decision row).
- Client apps (Telefonia/NextBilling, Cobranças, Campanhas, voice-api) ↔ gateway `base_url` — operator-configured env vars, validated in HUMAN-UAT.
- LGPD disclosure doc ↔ the three external sub-processors (OpenAI, OpenRouter, Vast.ai).

</code_context>

<specifics>
## Specific Ideas

- Telefonia/NextBilling = call-audio Whisper transcription, `sensitive`. Cobranças = LLM personalization + embedding lookups, `sensitive`. Campanhas = LLM + embedding, `normal`. voice-api = LLM script generation only (TTS stays on local CPU), `normal`.
- The sensitive smoke must verify Phase 3's RES-08 behavior end-to-end: induced upstream failure → request queues or fails closed → `audit_log` records the decision → never reaches an external provider.
- The LGPD sub-processor disclosure must explicitly list **OpenAI, OpenRouter, and Vast.ai**.
- SC4 rollback playbook target: under 5 minutes, per app — same bar as Phase 8.

</specifics>

<deferred>
## Deferred Ideas

- **Live integration validation** — the env-var switch in each client repo + validation against real traffic. Deferred to the Phase 9 HUMAN-UAT plan; additionally gated on the gateway being deployed (build-gateway currently blocked on Phase 6 emerg integration tests — separate debug session).
- **LGPD legal sign-off** — an external gate. The operator obtains documented sign-off from Ifix legal before activating the `sensitive` tenants in production; gpu-ifix ships the disclosure doc + checklist, not the signature.
- **Production load + chaos testing** of the integrated sensitive apps — Phase 10.

</deferred>
