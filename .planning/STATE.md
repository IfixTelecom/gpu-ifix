---
gsd_state_version: 1.0
milestone: v1.0
milestone_name: milestone
status: executing
last_updated: "2026-04-18T12:10:00.000Z"
progress:
  total_phases: 10
  completed_phases: 1
  total_plans: 18
  completed_plans: 9
  percent: 10
---

# STATE: ifix-ai-gateway

> Project memory. Single source of truth for "where am I now?"
> Updated on phase/plan transitions.

## Project Reference

- **Project:** ifix-ai-gateway
- **Core Value:** Nenhuma aplicação da Ifix sente quando a GPU cai. Failover invisível.
- **Current Milestone:** v1 — Ship the first working gateway with pod + auth + failover + auto-provisioning + 6 app integrations
- **Granularity:** fine (10 phases)
- **Mode:** yolo

## Current Position

Phase: 2 (Gateway Core + Multi-tenant Auth) — READY TO EXECUTE
Plan: 9 plans staged in 7 waves

- **Phase:** Phase 2 planning complete — `02-PATTERNS.md` (40 files mapped) + 9 `02-NN-PLAN.md` files committed. Checker iteration 1 surfaced 2 blockers (depends_on on 02-04; audit replay flag propagation B2) + 3 warnings (wave consistency on 02-09; fmtSscan stub in 02-05; broken goose placeholder in 02-02) + 1 info (itoa helper in 02-04). All 6 fixes applied. Integration_04b regression test added in 02-07 asserting `SELECT idempotency_replayed FROM ai_gateway.audit_log` after replay.
- **Plan:** run `/gsd-execute-phase 2` next (recommend `/clear` first; waves 1-2 are autonomous; wave 7 `02-08-PLAN.md` is `autonomous: false` — requires human-verify on first live Portainer deploy).
- **Status:** Phase 2 plans ready; execution pending
- **Progress:** `[█─────────]` 1/10 phases complete (10%)

## Performance Metrics

- **Phases completed:** 1 / 10
- **Plans completed:** 9 / 18 (9 executed in Phase 1 + 9 staged in Phase 2)
- **v1 requirements covered by plans:** 21 / 70 (POD-01..POD-07 from Phase 1 + GW-01..GW-10, TEN-01, TEN-02, TEN-08, TEN-09 newly planned in Phase 2)

## Accumulated Context

### Key Decisions (from research + PROJECT)

- Gateway language: Go (chi v5 + stdlib `httputil.ReverseProxy` + slog)
- LLM server: `llama.cpp` native (not `llama-cpp-python`)
- STT server: `speaches-ai/speaches` (not custom FastAPI)
- Embedding server: `michaelf34/infinity` (not `sentence-transformers`)
- Saturation signal: composite (inflight + P95 + VRAM + hysteresis), not GPU util alone
- Primary GPU: Vast.ai RTX 4090 (cost) with emergency Vast.ai pod failover (not RunPod Secure)
- LLM model: Qwen 3.5 27B Q4_K_M GGUF, fixed both primary and OpenRouter fallback
- Deploy: Docker Compose + Portainer + webhook GitHub (standard Ifix)
- Postgres: shared DO cluster with dedicated `ai_gateway` schema
- Pre-baked pod Docker image (`ghcr.io/ifixtelecom/ifix-ai-pod`, slim ~2 GB) with weights downloaded from Ifix MinIO at boot via `onstart.sh` (revised by Phase 1 per D-01/D-02/D-04 — image stays small, weights versioned by key path with SHA-256 integrity D-05)

### Open Todos (for upcoming phases)

- [ ] Phase 1 HUMAN-UAT: Validate Qwen 3.5 27B patched Jinja template on real Vast.ai pod (tool-call correctness — blocked on smoke.yml run)
- [ ] Phase 1 HUMAN-UAT: Empirical VRAM ceiling under load (2×8k-token chats + 1 long Whisper — blocked on smoke.yml run)
- [ ] Phase 1 HUMAN-UAT: Cold-start ≤5 min on fresh Vast.ai 4090 (blocked on smoke.yml run)
- [ ] Phase 3: Confirm OpenRouter upstream provider for Qwen 3.5 27B (Together? Fireworks? DeepInfra?)
- [ ] Phase 5: Tune saturation thresholds (inflight N, P95 ms, VRAM GB) from Phase 1 baseline
- [ ] Phase 6: Timeboxed (3h) Vast.ai REST API spike before committing the phase scope
- [ ] Phase 7: Confirm Ifix WhatsApp provider (Evolution API / Z-API / Chatwoot / proprietary)
- [ ] Phase 7: Choose dashboard auth (Better Auth instance vs shared with ConverseAI vs SSO)
- [ ] Phase 9: Obtain LGPD review sign-off from Ifix legal before activating sensitive tenants

### Blockers

None at present. Roadmap is ready for planning.

## Session Continuity

- **Last session:** 2026-04-18T12:10:00Z
- **Next session should:** Run `/clear` then `/gsd-execute-phase 2` to execute Phase 2 plans (9 plans, 7 waves). Wave 1 `02-01-PLAN.md` (scaffold) + `02-02-PLAN.md` (schema+sqlc) run first in parallel. Wave 7 `02-08-PLAN.md` (Dockerfile + build-gateway.yml + Portainer stack) is `autonomous: false` — human-verify first live deploy. Separately, set up GH Secrets + MinIO per `.planning/MINIO-SETUP.md` and run `smoke.yml` workflow_dispatch to close Phase 1 HUMAN-UAT items.

---

*State created: 2026-04-17*
