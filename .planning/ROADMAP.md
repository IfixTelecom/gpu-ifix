### Phase 06.8: Multi-pod GPU topology + sizing + STT fix (INSERTED)

**Goal:** Make the gateway provision + health-poll the primary pod across multiple GPU topologies (preferring a 2×RTX 3090 single-pod, ~60% cheaper than a single 5090 with deeper Vast inventory) via runtime env (PRIMARY_NUM_GPUS=2 + allowlist), prove it end-to-end with a live force-up UAT, and fix the STT model-resolution bug (whisper tarball → HF-hub-cache layout + HF_HUB_CACHE) that blocks /v1/audio/transcriptions on every topology. Decides the GPU shape the SEED-002 emergency hot-standby will mirror, so it runs before SEED-002.
**Requirements**: STT-FIX, GW-2GPU, LADDER
**Depends on:** Phase 6, Phase 06.6 (primary pod Strategy B image + reconciler), Phase 06.7 (STT/speaches stack)
**Plans:** 5 plans (4 complete + 1 gap-closure)

Plans:

- [x] 06.8-01-PLAN.md — Wave 1: STT fix prep — regenerate whisper tarball in HF-hub-cache layout (upload-weights.sh) + HF_HUB_CACHE on [program:speaches] (supervisord.conf)
- [x] 06.8-02-PLAN.md — Wave 2: STT live-pod validation gate (rebuild image, spin pod, assert /v1/audio/transcriptions 200, propagate new SHA) — CLAUDE.md anti-blind-commit gate
- [x] 06.8-03-PLAN.md — Wave 3: Gateway 2×3090 live-UAT (A2 search pre-check + gatewayctl primary force-up + 4-endpoint health + nvidia-smi split) → SEED-002 shape input
- [x] 06.8-04-PLAN.md — Wave 1: Fallback topology ladder runbook + per-shape env presets (2×3090 → 5090 → Shape C deferred)
- [x] 06.8-05-PLAN.md — Wave 4 (gap closure): diagnose + fix the PRIMARY_VAST_MACHINE_ALLOWLIST steering bug (diagnose-first, operator-approval gate, minimal fix + unit test) → re-run 2×3090 force-up UAT targeting 43803 → markReady + STT 200 + nvidia-smi 2-GPU split

---

### Phase 06.9: OpenRouter model-rewrite per-upstream — close Phase 03 SC-1 fallback chain (INSERTED, promoted from SEED-004)

**Goal:** Fix the gateway dispatcher → tier-1 fallback model-name rewriting gap so `POST /v1/chat/completions {"model":"qwen"}` against ai-gateway-dev (with primary pod down) returns a real OpenRouter Qwen 3.5 completion instead of the current HTTP 404 "Not Found" HTML. Wave 0 Gate A (Phase 03, 2026-04-20) defined `UPSTREAM_LLM_OPENROUTER_MODEL=qwen/qwen3.5-27b` as the env var operator must set; Plan 03-06 implementation never wired it. Bug masked through Phase 04-08 because integration tests use a fake upstream that accepts any model name + live UAT was always deferred. Also surfaced same-shape gaps for openai-whisper (`UPSTREAM_STT_OPENAI_MODEL`) and openai-embed (`UPSTREAM_EMBED_OPENAI_MODEL`) — verify and bundle. Reference fix-path = SEED-004 Option B (schema-driven `model_aliases` PK widened to `(alias, upstream_name)`).

**Requirements:** OR-FIX (model rewrite per-upstream), STT-OAI-FIX (whisper), EMBED-OAI-FIX (embed), SC1-CLOSE (Phase 03 SC-1 live UAT closes via this fix)
**Depends on:** Phase 03 (fallback chain code in tree); Phase 06.8 (live primary FSM available for breaker-OPEN testing)
**Blocks:** Phase 02 SC-5 step 7 chat E2E; Phase 03 SC-1 live UAT; Phase 05 SC-1 full overflow; Phase 07 dashboard accuracy (tier-1 cost rows currently mislabeled when model never rewrote)
**Mode:** sequential (not MVP)
**Plans:** TBD (planned via /gsd-plan-phase 06.9)
**Cost:** zero Vast spend (testable via existing /opt/ai-gateway-dev/ + live OpenRouter direct); ~2-3h wall
