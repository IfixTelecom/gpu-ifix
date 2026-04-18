---
status: partial
phase: 01-gpu-pod-image-smoke-test
source: [01-VERIFICATION.md]
started: 2026-04-18T00:45:00Z
updated: 2026-04-18T00:45:00Z
---

## Current Test

[awaiting human testing on Vast.ai 4090]

## Tests

### 1. Cold-start ≤5 min on fresh Vast.ai 4090
expected: GET :8000/health, :8001/health, :8002/health all return HTTP 200 within 5 minutes of pod creation (image pull 1-2 min + parallel weight download from MinIO 2-3 min, per D-04 budget).
result: [pending]
trigger: `gh workflow run smoke.yml -f image_tag=develop` (after GH Secrets + MinIO weights are set up)

### 2. Smoke-load D-19 gates green on real RTX 4090
expected: `smoke-report.json` shows `gates.all_passed == true`, `vram_peak_gb <= 21.0`, `tool_call_valid == true`, `errors == []`, `llm_p95_ttft_ms <= 3000`. Workload: 2 concurrent 8k-token chats + 1 long Whisper + 1 batch-of-10 embeddings (D-17). Validates POD-05 (Qwen patched template tool-calling), POD-06 (max_model_len=16384), POD-07 (VRAM ≥3 GB headroom under load).
result: [pending]
trigger: Same `smoke.yml` run produces `smoke-report.json` as workflow artifact; archive to `.planning/phases/01-gpu-pod-image-smoke-test/baseline/smoke-report-{sha}.json` after green run.

## Summary

total: 2
passed: 0
issues: 0
pending: 2
skipped: 0
blocked: 0

## Prerequisites (must complete before these tests can run)

- [ ] GH Secrets set on repo: `VAST_AI_API_KEY`, `MINIO_ENDPOINT`, `MINIO_ACCESS_KEY`, `MINIO_SECRET_KEY`, `MINIO_BUCKET`, `WEIGHTS_QWEN_SHA256`, `WEIGHTS_WHISPER_SHA256`, `WEIGHTS_BGE_M3_SHA256`
- [ ] MinIO bucket `ifix-ai-weights` provisioned per `.planning/MINIO-SETUP.md`
- [ ] Weights uploaded to MinIO via `pod/scripts/upload-weights.sh` (one-shot, ~22 GB)
- [ ] Vast.ai credit balance ≥ $20 (alert threshold per 01-08-PLAN.md user_setup)
- [ ] `ghcr.io/ifixtelecom/ifix-ai-pod:develop` + `:develop-health-bridge` built by `build-pod.yml` (pushed to GHCR)

## Gaps

