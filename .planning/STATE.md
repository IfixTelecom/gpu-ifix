---
gsd_state_version: 1.0
milestone: v1.0
milestone_name: milestone
status: executing
last_updated: "2026-04-19T00:00:34.065Z"
progress:
  total_phases: 10
  completed_phases: 1
  total_plans: 18
  completed_plans: 15
  percent: 83
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

Phase: 02 (gateway-core-multi-tenant-auth) — EXECUTING
Plan: 6 of 9 complete (Wave 5 done)

- **Phase:** Phase 2 execution in progress. Waves 1–5 complete (02-01..02-06). 02-06 shipped the Idempotency-Key middleware (Stripe semantics) on POST /v1/chat/completions non-streaming with Redis `SET NX EX` first-writer-wins, IN_FLIGHT sentinel at `gw:idem:<tenant>:<key>`, 30s wait budget for losers, 24h entry TTL, immediate 422 on hash mismatch (even vs in-flight), 409 + `Retry-After: 5` on wait-poll timeout, 502 → Abort (don't cache). Audit replay flag propagated via `audit.IdempotencyReplayedSetter` interface assertion on the response writer. 32 unit tests pass under `-race` (8 hash + 11 store + 13 middleware including 10-goroutine concurrent serialization asserting upstream hit count == 1).
- **Reviews cycle (2026-04-18):** `/gsd-review --phase 2 --all` invoked Codex (Gemini/OpenCode/Qwen/Cursor/CodeRabbit missing; Claude skipped for independence). `02-REVIEWS.md` committed with 4 HIGH/MEDIUM + 2 LOW concerns. `/gsd-plan-phase 2 --reviews` revised 8/9 plans across 2 iterations. All 02-05 Codex concerns resolved at implementation time (see 02-05-SUMMARY.md). Codex [MEDIUM] 02-06 concern (incomplete serialization) resolved by this wave — see SUMMARY confirmations.
- **Plan:** next wave is 02-07 (integration test: idempotency + audit end-to-end with real Postgres via testcontainers).
- **Status:** Executing Phase 02
- **Progress:** [████████░░] 83%

## Performance Metrics

- **Phases completed:** 1 / 10
- **Plans completed:** 16 / 18 (9 in Phase 1 + 6 executed in Phase 2 waves 1–5 + 1 optional staging plan)
- **v1 requirements covered by plans:** 21 / 70 (POD-01..POD-07 from Phase 1 + GW-01..GW-10, TEN-01, TEN-02, TEN-08, TEN-09 newly planned in Phase 2)
- **Plan 02-05:** duration 820s, 2 tasks, 14 files created, 1 file modified, 28 tests added
- **Plan 02-06:** duration 1100s, 2 tasks, 8 files created, 1 file modified, 32 tests added (19 hash+store + 13 middleware, all -race clean)

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

- [ ] Phase 3: Revisit per-route WriteTimeout (chat=0 for SSE, embeddings=30s, audio=120s) to restore slow-client-DoS defense on non-streaming routes (introduced by 02-01 config.go; acceptable for Phase 2 because Phase 4 adds rate-limiting)
- [ ] Phase 4: Wire request instrumentation middleware that calls `obs.RequestsTotal.WithLabelValues(route, status).Inc()` on the proxy layer (02-04 responsibility; the counter is already registered by 02-01)
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

- **Last session:** 2026-04-19T00:00:30.736Z
- **Next session should:** Continue `/gsd-execute-phase 2` with Wave 5 (02-06 idempotency middleware). 02-05 exported `audit.IdempotencyReplayedSetter` interface for the replay-path flag propagation; 02-06 type-asserts the ResponseWriter and calls `SetIdempotencyReplayed(true)` on replays. 02-07 will then integration-test the `SELECT idempotency_replayed FROM ai_gateway.audit_log` assertion end-to-end. Wave 7 `02-08-PLAN.md` (Dockerfile + build-gateway.yml + Portainer stack) is `autonomous: false` — human-verify first live deploy.

---

*State created: 2026-04-17*
