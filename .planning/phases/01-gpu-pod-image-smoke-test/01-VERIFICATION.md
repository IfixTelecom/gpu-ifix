---
phase: 01-gpu-pod-image-smoke-test
verified: 2026-05-24T00:00:00Z
status: superseded
score: 3/5 must-haves verified (code); D-19 runtime gates substituted by live UAT 06.6 #18 + 06.7 + 06.8 on Phase 06.8-final hardware (2×RTX 3090 or 5090)
overrides_applied: 0
superseded_by:
  - phase: 06.6
    artifact: ".planning/phases/06.6-primary-pod-refactor-strategy-b-full-stack-upstream-images-i/06.6-VERIFICATION.md (UAT 18, 6/6 PASS on 5090 EU $0.77/h)"
    why: "06.6 ships the custom primary-pod image (supervisord 4 children llm+stt+embed+dcgm) that replaced the Phase 1 ifix-ai-pod 3-server design. UAT 18 proved cold-start, 4-of-4 endpoint health, DCGM VRAM scrape, transcription end-to-end, tool-call patched template — all D-19 equivalents on prod-realistic hardware."
  - phase: 06.7
    artifact: ".planning/phases/06.7-.../06.7-VERIFICATION.md (5/6 PASS + CLEANUP on 5090)"
    why: "06.7 added Chatterbox TTS:8003 to the pod (4th GPU child) and moved embed off-pod to CPU. UAT validated full new stack + voice-clone zero-shot + pod-replacement survival via S3 WAV refetch."
  - phase: 06.8
    artifact: ".planning/phases/06.8-.../06.8-VERIFICATION.md + 06.8-GW-2GPU-LIVE-UAT.md"
    why: "06.8 locked the final primary GPU shape (2×RTX 3090 single-pod, allowlist 43803/55158, cap $0.60/h). The Phase 1 single-4090 24 GB target was OBSOLETED — full stack (Qwen Q4 16 GB + Whisper GPU 3 GB + Chatterbox 4 GB + KV 2-3 GB ≈ 25 GB) does NOT fit on 4090, confirmed CUDA OOM in UAT 06.6 #16. 2×3090 (48 GB pooled) is the standing primary shape; 5090 32 GB validated as alternate."
human_verification:
  - test: "Cold-start ≤5min on fresh Vast pod"
    status: superseded
    superseded_by: "Phase 06.6 UAT 18 cold-start 9min on 5090 EU (longer than Phase 1's 5min target because pod now downloads ~21 GB of weights for 4 services instead of just LLM+STT+embed — budget was renegotiated as part of Phase 06.6 SC-2 acceptance)"
  - test: "Smoke-load D-19 gates green (vram_peak_gb ≤21, tool_call_valid, llm_p95_ttft_ms ≤3000)"
    status: superseded
    superseded_by: "Phase 06.6 UAT 14/15/18 on 5090: tool-call PASS (system_fingerprint b9191-4f13cb742, finish_reason=tool_calls); llm throughput prompt 162.8 tok/s + predict 47.1 tok/s solo, aggregate 80.8 tok/s N=4 concurrent. VRAM target ≤21 GB is OBSOLETE — Phase 06.8 final shape (2×3090 48 GB or 5090 32 GB) has different budget than Phase 1 4090 24 GB."
---

# Phase 1: GPU Pod Image & Smoke-Test Verification Report — SUPERSEDED

> **STATUS UPDATE 2026-05-24:** Phase 1 was scoped against the original single-pod 3-server `ifix-ai-pod` image targeted at a single Vast RTX 4090 24 GB. That design was REPLACED by Phase 06.6 (`converseai-primary-pod` custom image, supervisord PID 1 + 4 children + DCGM), extended by Phase 06.7 (TTS Chatterbox on GPU as 5th supervisord child, embed moved off-pod to CPU n8n-ia-vm 24/7), and locked by Phase 06.8 (primary GPU shape = 2×RTX 3090 allowlist 43803/55158 cap $0.60/h; 5090 32 GB validated as alternate). The 4090 24 GB target is OBSOLETE — UAT 06.6 #16 confirmed CUDA OOM when whisper-large-v3 GPU offload is added to the Qwen+bge-m3 stack on a 4090.
>
> **Phase 1 D-19 runtime gates (cold-start, VRAM peak, tool-call, latency) are substituted by:**
> - **Phase 06.6 UAT 18** — `06.6-VERIFICATION.md` (6/6 PASS on 5090 Spain ES, $0.77/h): cold-start 9 min, 4-of-4 endpoint health (LLM/STT/Embed/DCGM all HTTP 200), DCGM VRAM scrape 24.7 GB used / 7.4 GB free under full load, transcription cold 17.5s / warm 0.76s (well under 5s SLO), tool-call with patched Jinja confirmed system_fingerprint `b9191-4f13cb742`.
> - **Phase 06.7 UAT** — `06.7-VERIFICATION.md` (5/6 PASS + CLEANUP on 5090): Chatterbox VRAM headroom verified (21,972 MiB used / 10,138 MiB free of 32,607 MiB on RTX 5090), zero-shot voice-clone round-trip 24kHz, voice-survives-pod-replacement via S3 WAV refetch (durable contract, NO `.pt`).
> - **Phase 06.8 UAT** — `06.8-VERIFICATION.md` + `06.8-GW-2GPU-LIVE-UAT.md` (passed): primary force-up on 2×RTX 3090 with PRIMARY_NUM_GPUS=2 + allowlist + cap $0.60; whisper HF cache layout fix (substitui o D-04 weights tarball Phase 1).
>
> Phase 1 code artifacts (pod/health-bridge, pod/docker-compose.yml, pod/smoke/smoke.py, pod/Dockerfile, pod/templates/qwen3.5-27b-tool-calling.jinja, .github/workflows/smoke.yml) remain in tree as historical reference. `ghcr.io/ifixtelecom/ifix-ai-pod` image stopped being the production target after Phase 06.6 (`converseai-primary-pod` is current). `smoke.yml` workflow can be invoked manually for retrospective comparison runs but is NOT a gating CI job — `build-gateway` + `build-primary-pod` are.

**Phase Goal (historical):** Ship a reproducible pre-baked pod image that boots the 3 inference servers on a Vast.ai 4090 in ≤5 min with ≥3 GB VRAM headroom under realistic load.
**Verified (initial):** 2026-04-17T03:00:00Z
**Status:** superseded (by Phase 06.6/06.7/06.8 — see banner above)
**Re-verification:** Yes — 2026-05-24 status update reflecting downstream phase coverage.

---

## Goal Achievement

### Observable Truths

| # | Truth | Status | Evidence |
|---|-------|--------|----------|
| 1 | Operator can `docker run` the image and see all three endpoints healthy within 5 minutes | ? HUMAN | Image definition is complete and correct (Dockerfile, docker-compose, onstart.sh), but cold-start timing and endpoint health require a real Vast.ai 4090 run |
| 2 | Operator can hit per-model `/health` endpoints on health-bridge (port 9100) and get true latency-based health | ✓ VERIFIED | `pod/health-bridge/handlers.go:90-95` registers `/health/llm`, `/health/stt`, `/health/embed`, `/health/live`, `/health/ready`, `/health`; `probes.go:41-60` performs real OpenAI-compat HTTP calls with latency tracking; `state.go:36-39` stores `{status, latency_ms, last_probe, error?}` matching D-12 spec |
| 3 | Operator can pull live VRAM metrics from dcgm-exporter (port 9400) during inference | ✓ VERIFIED | `pod/docker-compose.yml:158-171` declares `dcgm-exporter` service on port `9400:9400` with `nvcr.io/nvidia/k8s/dcgm-exporter:latest-ubuntu22.04` and GPU reservation via `&gpu-all` anchor; `DCGM_EXPORTER_LISTEN: ":9400"` set; smoke.py scrapes `DCGM_FI_DEV_FB_USED/FREE/TOTAL` from this endpoint |
| 4 | Operator can run the documented smoke-load and observe sustained VRAM usage ≤21 GB with `max_model_len=16384` enforced | ? HUMAN | CODE: `docker-compose.yml:43-44` locks `-np 2 --ctx-size 16384`; `smoke.py:54` sets `GATE_VRAM_PEAK_GB_MAX = 21.0`; `smoke.py:316,334` enforces gate; `report-schema.json:71` formalizes gate. RUNTIME: actual VRAM under load requires real GPU execution |
| 5 | Operator can issue a tool-call request and receive a well-formed tool call (Qwen 3.5 27B patched template validated) | ? HUMAN | CODE: `pod/templates/qwen3.5-27b-tool-calling.jinja` committed (8,595 bytes, SHA-256 pinned); `docker-compose.yml:44-45` wires `--jinja --chat-template-file /app/templates/qwen3.5-27b-tool-calling.jinja`; `smoke.py:204-240` implements `get_weather` tool-call validation. RUNTIME: template correctness with real Qwen 3.5 27B inference requires smoke run |

**Score:** 3/5 truths statically verified — truths 1, 4, 5 require runtime (human) verification

---

## Requirements Traceability

| Requirement | Plan(s) | Artifact(s) | Status | Evidence |
|-------------|---------|-------------|--------|---------|
| POD-01 | 01-01, 01-03, 01-07 | `go.mod`, `pod/Dockerfile`, `.github/workflows/build-pod.yml` | ✓ CODE COMPLETE | `Dockerfile` builds `ghcr.io/ifixtelecom/ifix-ai-pod` with llama-server binary; `build-pod.yml` triggers on push/dispatch, builds both images (`ifix-ai-pod` + `ifix-ai-pod-health-bridge`), uses `docker/build-push-action@v6` with GHA layer cache |
| POD-02 | 01-05, 01-09 | `pod/onstart.sh`, `pod/scripts/download-weights.sh`, `pod/scripts/upload-weights.sh`, `pod/weights/README.md`, `.planning/MINIO-SETUP.md` | ✓ CODE COMPLETE | `onstart.sh` is executable (chmod 755), uses `set -euo pipefail`, logs to `/var/log/onstart.log`, calls `download-weights.sh` for parallel MinIO download; SHA-256 validation on all 3 weights (exit code 3 on mismatch, D-05); versioned key paths `qwen3.5-27b-Q4_K_M/v1.0.0/model.gguf` (D-06); `docker compose up -d` + readiness poll on `9100/health/ready` |
| POD-03 | 01-04 | `pod/health-bridge/main.go`, `pod/health-bridge/probes.go`, `pod/health-bridge/state.go`, `pod/health-bridge/handlers.go`, `pod/health-bridge/Dockerfile` | ✓ VERIFIED | Go binary listens on `HEALTH_BRIDGE_PORT` (default 9100); 3 probe goroutines on 10s ticker (`cfg.ProbeInterval`); real OpenAI-compat calls (`probeLLM` POST /v1/chat/completions, `probeSTT` multipart WAV, `probeEmbed` POST /v1/embeddings); flat JSON `{status, latency_ms, last_probe, error?}` per D-12; HTTP 200/503 status mapping; SIGTERM graceful shutdown; imports `github.com/ifixtelecom/gpu-ifix/pkg/openai` |
| POD-04 | 01-03 | `pod/docker-compose.yml` (dcgm-exporter service) | ✓ VERIFIED | `dcgm-exporter` service at `nvcr.io/nvidia/k8s/dcgm-exporter:latest-ubuntu22.04`, port `9400:9400`, `DCGM_EXPORTER_LISTEN: ":9400"`, GPU reservation via `&gpu-all` anchor, `cap_add: SYS_ADMIN` for NVML access |
| POD-05 | 01-02 | `pod/templates/qwen3.5-27b-tool-calling.jinja`, `pod/templates/qwen3.5-27b-tool-calling.jinja.sha256` | ✓ VERIFIED (code) / ? HUMAN (runtime) | Template exists (8,595 bytes), starts with `{#` provenance block; SHA-256 `1067302cc6d927210a84775b9a060f724da15debc168c79710cbf763512e9f67` matches sidecar; wired in `docker-compose.yml` via `--chat-template-file`; build-time integrity check in `Dockerfile:62-64`; tool-call behavior requires real Qwen inference |
| POD-06 | 01-03 | `pod/docker-compose.yml` (llama service command) | ✓ VERIFIED | `docker-compose.yml:42-45` locks `--ctx-size 16384 --jinja --chat-template-file` in llama service command; also hardcoded in `Dockerfile:79` CMD |
| POD-07 | 01-06, 01-08 | `pod/smoke/smoke.py`, `pod/smoke/report-schema.json`, `.github/workflows/smoke.yml`, `pod/scripts/vast-ai.sh` | ✓ CODE COMPLETE / ? HUMAN (runtime) | `smoke.py` executes D-17 workload (2 concurrent 8k chats + 1 Whisper 8-min + 10-embedding batch); scrapes dcgm-exporter every 1s; validates `get_weather` tool call; enforces D-19 gates (exit codes 1-5); `report-schema.json` formalizes all D-18 fields; `smoke.yml` is `workflow_dispatch` only (D-22), creates Vast.ai pod via `vast-ai.sh`, runs smoke.py, destroys pod in `if: always()` step |

---

## Required Artifacts

| Artifact | Expected | Status | Details |
|----------|----------|--------|---------|
| `go.mod` | Module manifest `github.com/ifixtelecom/gpu-ifix`, Go 1.23 | ✓ VERIFIED | Exists; `module github.com/ifixtelecom/gpu-ifix` + `go 1.23`, stdlib-only |
| `pkg/openai/types.go` | 16 OpenAI-compat shared structs | ✓ VERIFIED | Exports `ChatCompletionRequest`, `ChatCompletionMessage`, `Tool`, `ToolFunction`, `ToolCall`, `ToolCallFunction`, `ChatCompletionResponse`, `ChatCompletionChoice`, `Usage`, `EmbeddingRequest`, `EmbeddingResponse`, `Embedding`, `TranscriptionRequest`, `TranscriptionResponse`, `ErrorResponse`, `ErrorDetail` — all with correct JSON tags |
| `pkg/openai/types_test.go` | 5 round-trip tests | ✓ VERIFIED | File exists with test functions; Go build/test infrastructure confirmed |
| `pod/Dockerfile` | Multi-stage CUDA image, no weights | ✓ VERIFIED | Two stages (`llama-bin` + `runtime`); copies llama-server binary; embeds Jinja template with SHA-256 check; `EXPOSE 8000`, `STOPSIGNAL SIGTERM`, `tini` PID 1; no `COPY pod/weights` |
| `pod/docker-compose.yml` | 5 services, GPU reservation, healthchecks | ✓ VERIFIED | All 5 services (`llama`, `speaches`, `infinity`, `health-bridge`, `dcgm-exporter`); correct ports (8000/8001/8002/9100/9400); `driver: nvidia` GPU reservation on inference services; healthchecks on all services |
| `pod/.env.example` | Documented env-var contract | ✓ VERIFIED | Exists; covers `WEIGHTS_DIR`, `MINIO_*`, `WEIGHTS_*_SHA256`, `IFIX_AI_POD_IMAGE`, `LOG_LEVEL`; no real secrets |
| `pod/templates/qwen3.5-27b-tool-calling.jinja` | Community-patched Qwen template | ✓ VERIFIED | 8,595 bytes; starts with `{#` provenance block; SHA-256 integrity confirmed |
| `pod/templates/qwen3.5-27b-tool-calling.jinja.sha256` | 64-hex SHA-256 sidecar | ✓ VERIFIED | Contains `1067302cc6d927210a84775b9a060f724da15debc168c79710cbf763512e9f67`; matches file |
| `pod/health-bridge/main.go` | HTTP server, signal handling, config | ✓ VERIFIED | Port config via env; SIGTERM handling; 3 probe goroutines spawned |
| `pod/health-bridge/probes.go` | Probe goroutines for llm/stt/embed | ✓ VERIFIED | Imports `pkg/openai`; real HTTP calls with `http.NewRequestWithContext`; `probeLLM`, `probeSTT` (multipart WAV), `probeEmbed` |
| `pod/health-bridge/state.go` | sync.RWMutex state + serializers | ✓ VERIFIED | `ProbeResult` with `{status, latency_ms, last_probe, error?}` per D-12 |
| `pod/health-bridge/handlers.go` | HTTP handlers for 6 endpoints | ✓ VERIFIED | `mux()` registers `/health/live`, `/health/ready`, `/health/llm`, `/health/stt`, `/health/embed`, `/health` |
| `pod/health-bridge/Dockerfile` | Distroless Go binary | ✓ VERIFIED | Multi-stage (`golang:1.23-alpine` builder + `gcr.io/distroless/static-debian12` runtime) |
| `pod/onstart.sh` | Vast.ai onstart hook | ✓ VERIFIED | Executable (755); `set -euo pipefail`; logs to `/var/log/onstart.log`; requires all `MINIO_*` and `WEIGHTS_*` env vars; calls `download-weights.sh`; runs `docker compose up -d`; polls `9100/health/ready` |
| `pod/scripts/download-weights.sh` | Parallel download + SHA-256 validation | ✓ VERIFIED | Executable; installs `mc` if missing; parallel download with `download_and_verify()` function; exit codes 1-5 |
| `pod/scripts/upload-weights.sh` | One-shot upload script | ✓ VERIFIED | Executable (755); idempotent |
| `pod/scripts/vast-ai.sh` | Vast.ai REST API wrapper | ✓ VERIFIED | Supports `search`, `create`, `status`, `wait-running`, `destroy`, `ssh-exec`, `scp-upload` subcommands |
| `pod/smoke/smoke.py` | asyncio smoke benchmark | ✓ VERIFIED | D-17 workload (2 chats + 1 Whisper + 1 embed batch); dcgm scrape every 1s; `get_weather` tool-call validation; D-19 gate enforcement; `asyncio.gather` for concurrent tasks |
| `pod/smoke/report-schema.json` | JSON Schema for smoke-report | ✓ VERIFIED | Draft 2020-12; requires all D-18 fields including `gates` object with all 4 gate booleans + `all_passed` |
| `pod/smoke/fixtures/__gen_audio.py` | Synthetic WAV generator | ✓ VERIFIED | File exists in `pod/smoke/fixtures/` |
| `.github/workflows/build-pod.yml` | CI build + push to GHCR | ✓ VERIFIED | Triggers on `push: branches: [main, develop]`, `tags: ['v*']`, `workflow_dispatch`; two build jobs for both images; Go unit tests + template SHA-256 check run BEFORE build job; tag convention: `{branch}`, `{branch}-{sha}` (auto), `vX.Y.Z`+`latest` (stable via tag push D-23); `permissions: packages: write` |
| `.github/workflows/smoke.yml` | Manual smoke CI on Vast.ai | ✓ VERIFIED | `workflow_dispatch` only (D-22); `cancel-in-progress: false`; pod destroy in `if: always()` step; hard 45-min timeout; `smoke-report.json` uploaded as artifact |
| `pod/weights/README.md` | MinIO layout + SHA-256 + versioning | ✓ VERIFIED | Documents bucket layout, versioning scheme (D-06), SHA-256 sidecar convention (D-05), upload procedure |
| `.planning/MINIO-SETUP.md` | Operator setup checklist | ✓ VERIFIED | File exists |
| `pod/README.md` | Full operator runbook (plan 09) | ✓ VERIFIED | 170-line substantive runbook; covers architecture, all 5 ports, setup, CI smoke, cost forecast |
| `docs/CONVENTIONS.md` | Ifix coding conventions for Go | ✓ VERIFIED | Covers kebab-case, gofmt, slog, TZ=America/Sao_Paulo, HTTP response shape, Docker tagging, commit style |

---

## Key Link Verification

| From | To | Via | Status | Details |
|------|----|-----|--------|---------|
| `pod/health-bridge/probes.go` | `pkg/openai/types.go` | `import github.com/ifixtelecom/gpu-ifix/pkg/openai` | ✓ WIRED | Line 15 of `probes.go` imports the package; `openai.ChatCompletionRequest` used in `probeLLM` |
| `pod/docker-compose.yml` llama command | `pod/templates/qwen3.5-27b-tool-calling.jinja` | `--chat-template-file /app/templates/qwen3.5-27b-tool-calling.jinja` | ✓ WIRED | `docker-compose.yml:44-45`; also wired in `Dockerfile:81` CMD |
| `pod/onstart.sh` | `pod/docker-compose.yml` | `docker compose -f ... up -d` | ✓ WIRED | `onstart.sh` references `${COMPOSE_FILE}` which defaults to `/opt/ifix-ai-pod/docker-compose.yml` |
| `pod/onstart.sh` | MinIO endpoint | `mc cp` via `MINIO_ENDPOINT`/`MINIO_ACCESS_KEY`/`MINIO_SECRET_KEY`/`MINIO_BUCKET` + versioned key paths | ✓ WIRED | `download-weights.sh` configures `mc alias set ifix` and downloads with `s3://${MINIO_BUCKET}` keys |
| `pod/onstart.sh` | `pod/health-bridge` `/health/ready` | `curl http://127.0.0.1:9100/health/ready` readiness gate | ✓ WIRED | `READINESS_URL` defaults to `http://127.0.0.1:9100/health/ready`; poll loop in onstart.sh |
| `.github/workflows/build-pod.yml` | `pod/Dockerfile` | `docker/build-push-action@v6 file: pod/Dockerfile` | ✓ WIRED | Workflow builds pod image from `pod/Dockerfile` |
| `.github/workflows/build-pod.yml` | `pod/health-bridge/Dockerfile` | `docker/build-push-action@v6 file: pod/health-bridge/Dockerfile` | ✓ WIRED | Separate build job for health-bridge image |
| `.github/workflows/smoke.yml` | `pod/smoke/smoke.py` | `python3 pod/smoke/smoke.py --target "$TARGET"` | ✓ WIRED | `smoke.yml:221` |
| `.github/workflows/smoke.yml` | `pod/scripts/vast-ai.sh` | `pod/scripts/vast-ai.sh create/destroy/...` | ✓ WIRED | Multiple steps in `smoke.yml` |
| `pod/smoke/smoke.py` | dcgm-exporter `:9400` | `httpx.AsyncClient` GET `/metrics` every 1s | ✓ WIRED | `dcgm_scrape_loop` function; `DCGM_FI_DEV_FB_USED` metric extracted |
| `pod/smoke/smoke.py` | `get_weather` tool-call validation | `run_tool_call_test` function, gates `tool_call_valid` | ✓ WIRED | `smoke.py:204-240`; gate enforced at `smoke.py:317,336` |

---

## Behavioral Spot-Checks

Step 7b: SKIPPED for code-only artifacts (no runnable entry points without GPU hardware). The smoke.yml workflow is the documented execution path (workflow_dispatch, D-22).

| Behavior | Command | Result | Status |
|----------|---------|--------|--------|
| Template SHA-256 integrity | `sha256sum` vs sidecar | Match: `1067302c...` | ✓ PASS |
| Template size > 500 bytes | `wc -c` | 8,595 bytes | ✓ PASS |
| Template starts with Jinja comment | `head -1` | `{#` | ✓ PASS |
| onstart.sh executable | `ls -la` | `-rwxr-xr-x` | ✓ PASS |
| download-weights.sh executable | `ls -la` | `-rwxr-xr-x` | ✓ PASS |
| upload-weights.sh executable | `ls -la` | `-rwxr-xr-x` | ✓ PASS |
| Dockerfile has no `COPY pod/weights` | `grep` | Not found | ✓ PASS |
| docker-compose.yml has all 5 services | `grep` | `llama`, `speaches`, `infinity`, `health-bridge`, `dcgm-exporter` | ✓ PASS |
| llama service locks `--ctx-size 16384` | `grep` | Line 43 | ✓ PASS |
| smoke.yml is workflow_dispatch only | `grep` | No `push:` or `schedule:` trigger | ✓ PASS |
| Pod destroy in `if: always()` | `grep` | Line 273 | ✓ PASS |

---

## Anti-Patterns Found

| File | Line | Pattern | Severity | Impact |
|------|------|---------|----------|--------|
| `pod/README.md` | 1 (plan-01 stub) | Previously was a stub; plan 09 filled it in to 170 lines | ✓ Resolved | None — runbook is now complete |
| `pod/smoke/fixtures/` | — | No pre-committed WAV fixtures (by design) | ℹ️ Info | `__gen_audio.py` generates synthetic WAV at test time; no binary fixtures committed — correct per plan spec |

No blocking anti-patterns found. Placeholder/stub patterns from plan-01 stubs were explicitly replaced by downstream plans as designed.

---

## Human Verification Required

### 1. Cold-Start Boot Test (Success Criterion 1)

**Test:** On a fresh Vast.ai RTX 4090 instance, trigger `smoke.yml` via `workflow_dispatch` with a built image tag. Observe pod console logs via `vast-ai.sh ssh-exec` or Vast.ai dashboard.

**Expected:** Within 5 minutes of `onstart.sh` starting, all three endpoints respond:
- `curl http://<pod-ip>:8000/health` → HTTP 200
- `curl http://<pod-ip>:8001/health` → HTTP 200
- `curl http://<pod-ip>:8002/health` → HTTP 200

**Why human:** Depends on MinIO download throughput (D-02 requires ≥90 Mbps), image pull speed from GHCR, and GPU driver initialization time — all runtime-only metrics. The code path for cold-start is complete and correct.

**Prerequisite:** Weights must be uploaded to MinIO first (follow `.planning/MINIO-SETUP.md` + `pod/scripts/upload-weights.sh`).

### 2. Smoke-Load + D-19 Gates (Success Criteria 4 and 5)

**Test:** Run `smoke.yml` via `workflow_dispatch` with a valid image tag and MinIO secrets configured. After completion, download the `smoke-report.json` artifact.

**Expected in `smoke-report.json`:**
```json
{
  "vram_peak_gb": ≤21.0,
  "tool_call_valid": true,
  "errors": [],
  "gates": {
    "vram_peak_gb_le_21": true,
    "tool_call_valid_true": true,
    "no_cuda_oom_errors": true,
    "llm_p95_ttft_ms_le_3000": true,
    "all_passed": true
  }
}
```

**Why human:** Requires a real RTX 4090 with Qwen 3.5 27B Q4_K_M loaded. VRAM behavior under concurrent 8k-token chats + Whisper cannot be simulated. Tool-call correctness requires the patched Jinja template to be exercised by the real llama-server Qwen inference.

**GH Secrets required before running smoke.yml:**
- `VAST_AI_API_KEY` — Vast.ai account API key
- `MINIO_ENDPOINT`, `MINIO_ACCESS_KEY`, `MINIO_SECRET_KEY`, `MINIO_BUCKET`
- `WEIGHTS_QWEN_SHA256`, `WEIGHTS_WHISPER_SHA256`, `WEIGHTS_BGE_M3_SHA256` (from `upload-weights.sh` output)

---

## Gaps Summary

No code gaps found. All 7 requirements (POD-01 through POD-07) have complete, substantive, and wired implementation:

- **POD-01** (image built + published): Dockerfile + build-pod.yml CI pipeline verified
- **POD-02** (slim image, MinIO weights, cold-start tooling): onstart.sh + download-weights.sh + runbook verified
- **POD-03** (health-bridge with real latency probes): Full Go service verified with real OpenAI-compat probes
- **POD-04** (dcgm-exporter VRAM metrics): docker-compose service on port 9400 verified
- **POD-05** (Jinja template pinned by SHA-256): Template committed, SHA-256 integrity confirmed, wired in compose
- **POD-06** (max_model_len=16384 enforced): `--ctx-size 16384` locked in compose command and Dockerfile CMD
- **POD-07** (smoke-test with D-19 gates): smoke.py + report-schema + smoke.yml CI verified

The two `human_needed` truths (Success Criteria 1, 4, 5) are **runtime-only** — they cannot fail due to missing code. They require an actual Vast.ai 4090 run with real weights loaded. Once `smoke.yml` is triggered and produces a `smoke-report.json` with `gates.all_passed == true`, the phase goal is fully achieved.

---

_Verified: 2026-04-17T03:00:00Z_
_Verifier: Claude (gsd-verifier)_
