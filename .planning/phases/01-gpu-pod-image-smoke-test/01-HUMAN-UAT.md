---
status: superseded
phase: 01-gpu-pod-image-smoke-test
source: [01-VERIFICATION.md]
started: 2026-04-18T00:45:00Z
updated: 2026-05-24T00:00:00Z
superseded_by: [06.6-VERIFICATION.md (UAT 18), 06.7-VERIFICATION.md (5/6 PASS + CLEANUP), 06.8-VERIFICATION.md + 06.8-GW-2GPU-LIVE-UAT.md]
---

## Current Test

[NÃO RODAR — superseded] Phase 1 alvo Vast.ai 4090 24 GB single-pod 3-server (`ifix-ai-pod` image) substituído por Phase 06.6 (`converseai-primary-pod` 4-server image) + Phase 06.7 (TTS Chatterbox + embed off-pod) + Phase 06.8 (primary GPU shape final = 2×RTX 3090 allowlist 43803/55158 cap $0.60/h; 5090 32 GB alternate). UAT 06.6 #16 confirmou CUDA OOM em 4090 — full stack (~25 GB) NÃO cabe em 24 GB.

## Tests

### 1. ~~Cold-start ≤5 min on fresh Vast.ai 4090~~ — SUPERSEDED
expected (original): GET :8000/health, :8001/health, :8002/health all return HTTP 200 within 5 minutes of pod creation (image pull 1-2 min + parallel weight download from MinIO 2-3 min, per D-04 budget).
result: superseded
substitute_evidence: 06.6 UAT 18 cold-start 9min on 5090 EU + 4-of-4 endpoint health (06.6-VERIFICATION.md `human_verification.UAT 18 S1`); 06.7 S1 Chatterbox load adds ~30-40s (5th supervisord child). 5-min target era D-04 budget Phase 1 single-LLM design; full 4-service stack pesa ~21 GB de weight downloads → Phase 06.6 SC-2 renegociou budget.

### 2. ~~Smoke-load D-19 gates green on real RTX 4090~~ — SUPERSEDED
expected (original): `smoke-report.json` shows `gates.all_passed == true`, `vram_peak_gb <= 21.0`, `tool_call_valid == true`, `errors == []`, `llm_p95_ttft_ms <= 3000`. Workload: 2 concurrent 8k-token chats + 1 long Whisper + 1 batch-of-10 embeddings (D-17). Validates POD-05 (Qwen patched template tool-calling), POD-06 (max_model_len=16384), POD-07 (VRAM ≥3 GB headroom under load).
result: superseded
substitute_evidence: VRAM ceiling ≤21 GB era 4090-specific (24 GB budget − ~3 GB headroom); Phase 06.8 opera com 2×3090 48 GB pooled OR 5090 32 GB → diferente budget. Tool-call PASS via patched Jinja confirmed em UAT 06.6 #12/14/18 (`system_fingerprint=b9191-4f13cb742, finish_reason=tool_calls`). LLM throughput prompt 162.8 tok/s solo + 80.8 tok/s aggregate N=4 (UAT 06.6 #15). Whisper warm 0.76s (UAT 06.6 #18, well under 5s SLO).

## Summary

total: 2
passed: 0
issues: 0
pending: 0
skipped: 0
blocked: 0
superseded: 2

## Prerequisites (must complete before these tests can run)

- [ ] GH Secrets set on repo: `VAST_AI_API_KEY`, `MINIO_ENDPOINT`, `MINIO_ACCESS_KEY`, `MINIO_SECRET_KEY`, `MINIO_BUCKET`, `WEIGHTS_QWEN_SHA256`, `WEIGHTS_WHISPER_SHA256`, `WEIGHTS_BGE_M3_SHA256`
- [ ] MinIO bucket `ifix-ai-weights` provisioned per `.planning/MINIO-SETUP.md`
- [ ] Weights uploaded to MinIO via `pod/scripts/upload-weights.sh` (one-shot, ~22 GB)
- [ ] Vast.ai credit balance ≥ $20 (alert threshold per 01-08-PLAN.md user_setup)
- [ ] `ghcr.io/ifixtelecom/ifix-ai-pod:develop` + `:develop-health-bridge` built by `build-pod.yml` (pushed to GHCR)

## Gaps

