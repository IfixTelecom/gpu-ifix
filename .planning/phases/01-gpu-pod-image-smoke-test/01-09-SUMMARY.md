---
phase: 01-gpu-pod-image-smoke-test
plan: 09
subsystem: operator-docs

tags: [runbook, minio, operator, weights-upload, sha256, github-secrets, bash, documentation, phase-closure, d-01, d-02, d-05, d-06, d-19, d-22, d-23]

# Dependency graph
requires:
  - phase: 01-gpu-pod-image-smoke-test/01
    provides: "pod/README.md stub (this plan replaces it with the full runbook); repo layout convention referenced in the runbook"
  - phase: 01-gpu-pod-image-smoke-test/03
    provides: "pod/docker-compose.yml + pod/.env.example (key names MINIO_*, WEIGHTS_*_KEY, WEIGHTS_*_SHA256) reused verbatim in upload-weights.sh output and MINIO-SETUP.md"
  - phase: 01-gpu-pod-image-smoke-test/05
    provides: "pod/scripts/download-weights.sh — upload-weights.sh mirrors its `mc alias set` + `mc cp` convention; SHA-256 computed post-upload feeds the exact contract that download-weights.sh validates (sidecar + env var)"
  - phase: 01-gpu-pod-image-smoke-test/06
    provides: "pod/smoke/smoke.py and its D-19 gates (vram_peak_gb<=21, tool_call_valid, no CUDA errors, p95_ttft<=3s) — referenced in pod/README.md §Troubleshooting for gate-by-gate triage"
  - phase: 01-gpu-pod-image-smoke-test/07
    provides: ".github/workflows/build-pod.yml — referenced in MINIO-SETUP.md step 9 as a prereq (candidate image must exist before smoke run)"
  - phase: 01-gpu-pod-image-smoke-test/08
    provides: ".github/workflows/smoke.yml — the 8 GH Secrets listed in MINIO-SETUP.md step 8 match its `env:` block; default object keys in smoke.yml match upload-weights.sh output keys"
provides:
  - "pod/scripts/upload-weights.sh: one-shot idempotent operator script (mode 755) that downloads weights from HuggingFace, computes post-upload SHA-256, uploads to MinIO with sidecars, and prints 3 WEIGHTS_*_SHA256 values for GH Secrets paste"
  - "pod/weights/README.md: operator doc explaining MinIO layout (bucket=ifix-ai-weights), D-06 versioned key paths (v1.0.0 segment), D-05 SHA-256 sidecar convention, upload procedure, rotation scenarios, and troubleshooting table"
  - "pod/README.md: full runbook superseding the plan-01 stub — covers 5-service architecture, one-time setup pointer to MINIO-SETUP.md, local-dev flow, CI smoke via smoke.yml, D-23 stable-tag promotion, D-20 baseline archival, gate-by-gate troubleshooting, complete file-provenance repo layout"
  - ".planning/MINIO-SETUP.md: 10-item numbered operator checklist — each step has a bash verification command with expected output; enforces D-02 >=90 Mbps throughput gate (step 4); lists all 8 GH secrets required by smoke.yml (step 8); ships with a failure-recovery table and credential-rotation section"
  - "Fix: POD-02 text in REQUIREMENTS.md corrected from drifted 'weights embutidos' to the real hybrid model (D-01 slim image + D-02 MinIO mirror + D-04 onstart hydration + D-05 SHA-256)"
  - "Phase 1 is content-complete: operator has a single reproducible path from 'empty bucket' -> 'gh workflow run smoke.yml' -> green D-19 gates -> D-23 stable tag"
affects:
  - "Phase 1 retrospective (execute-phase will consume these docs + SUMMARY for the POD-XX coverage + D-XX coverage + gate readiness report)"
  - "Phase 5 (Load Shedding) — pod/README.md §Baseline archival documents the D-20 handoff path (smoke-report.json -> .planning/phases/01-.../baseline/) that Phase 5 consumes for real thresholds"
  - "Phase 6 (Auto-provisioning) — the stable-tag promotion path (D-23) documented in pod/README.md is the contract Phase 6 relies on when instantiating emergency pods from `ghcr.io/ifixtelecom/ifix-ai-pod:v1.0.0`"

# Tech tracking
tech-stack:
  added:
    - "mc (MinIO client) alias/cp/pipe/mb/ls/rm as the operator-side weight mover (mirrors plan 05 convention)"
    - "HuggingFace curl-based weight fetch with optional Bearer token (for Unsloth Qwen, Systran faster-whisper-large-v3, BAAI bge-m3)"
    - "gh workflow run + gh secret list + gh run watch as the operator UX for smoke trigger (MINIO-SETUP.md step 10)"
  patterns:
    - "Post-upload SHA-256 computation (operator machine is the trust root) + sidecar mirror — any future drift is detected by download-weights.sh:3 on every pod boot"
    - "Versioned S3-like key paths (qwen3.5-27b-Q4_K_M/v1.0.0/model.gguf) so weight rollback is decoupled from image rollback (D-06)"
    - "Fail-fast Bash skeleton: set -euo pipefail + required-binary preflight loop + `${VAR:?missing}` env guards (mirrors download-weights.sh)"
    - "10-item numbered checklist with per-step verification command + expected output (operator-proof runbook convention)"
    - "Runbook cross-references by plan number (# plan 03, # plan 05, etc.) in the repo-layout tree — makes provenance obvious when drift happens"

key-files:
  created:
    - "pod/scripts/upload-weights.sh (133 lines, mode 755, shebang #!/usr/bin/env bash, set -euo pipefail). Required env: MINIO_ENDPOINT, MINIO_ACCESS_KEY, MINIO_SECRET_KEY, MINIO_BUCKET. Optional flags: --weights-version (default v1.0.0), --workdir (default /tmp/ifix-weights-stage), --hf-token. Idempotent: skips already-downloaded HF files in workdir. Prints 3 WEIGHTS_*_SHA256 + paste-ready block for GH Secrets."
    - "pod/weights/README.md (77 lines, pt-BR). Sections: Layout no bucket, Versionamento (D-06), Checksums (D-05), Upload inicial (with requisitos operador + procedimento), Rotação de weights (3 scenarios), Troubleshooting table."
    - ".planning/MINIO-SETUP.md (146 lines). 10 numbered checklist items, each a `- [ ] **N. ...**` with a fenced bash block + Expected: line. Credential-handling preamble (T-01-09-02 shell-history mitigation). Post-checklist section (baseline archival + stable tag). Credential-rotation section (T-01-09-05 mitigation, 4-step). Failure-recovery table (step x mode x action)."
  modified:
    - "pod/README.md (from 37-line stub to 173-line full runbook). Sections: Imagens publicadas, Arquitetura em 1 minuto (5-service table), Setup inicial (pointer to MINIO-SETUP.md, 5-step summary), Executar localmente (dev), Executar em Vast.ai (CI, 6-step flow), Promoção de imagem estável (D-23), Arquivamento do baseline (D-20), Troubleshooting (5-row gate-to-hypothesis-to-action table), Layout do repo (file-by-file plan provenance)."
    - ".planning/REQUIREMENTS.md (POD-02 text only). Before: 'Imagem inclui weights embutidos ... para cold-start <=5 min'. After: 'Imagem magra (~2 GB) ... weights baixados do MinIO Ifix via onstart.sh ... cold-start <=5 min (decisões D-01, D-02, D-04; integridade via SHA-256 por D-05)'. POD-01, POD-03..POD-07 untouched."

key-decisions:
  - "upload-weights.sh stays operator-side (runs on the Ifix engineer's laptop/VPS), not inside CI — the HF-to-MinIO transit crosses trust boundaries a CI runner shouldn't sit on (see threat T-01-09-01). CI only consumes the resulting SHA-256 via GH Secrets."
  - "SHA-256 is computed POST-upload (on the staged file that was uploaded) rather than pre-upload — this matches exactly what download-weights.sh validates (the bytes delivered by MinIO), so there is no double-hashing semantics gap."
  - "Optional sidecar upload (.sha256 alongside the object) is a backup mirror — the canonical source of truth for the CI pipeline is GH Secrets. Two-source design prevents the sidecar from being silently modified to match a tampered object."
  - "MINIO-SETUP.md uses a checklist (not prose) — operator setup is the highest-risk step in Phase 1 (requires infra credentials + throughput verification); per-step verification commands give the operator immediate feedback instead of a single end-to-end failure at step 10."
  - "pod/README.md supersedes the stub — the stub's 'run /gsd-plan-phase 1 execution' language was a placeholder; the runbook is now the entry point any Ifix engineer can pick up without consulting .planning/."

metrics:
  duration: "6.1 min"
  completed: "2026-04-18"
---

# Phase 1 Plan 9: Operator Runbook — MinIO Weight Upload + Pod Operation + Baseline Archival Summary

Produced the four operator-facing artifacts that close Phase 1: a one-shot MinIO upload helper (`pod/scripts/upload-weights.sh`), a weights-mirror explainer (`pod/weights/README.md`), a 10-step setup checklist (`.planning/MINIO-SETUP.md`), and the full pod runbook (`pod/README.md` superseding the plan-01 stub). Also corrected pre-existing drift in `REQUIREMENTS.md` POD-02 text that claimed "weights embutidos" despite the locked hybrid-weights decisions (D-01/D-02/D-04). With these artifacts, an operator with GH Secrets access can go from "empty MinIO bucket" to "green `smoke.yml` D-19 gates" to "D-23 stable `v1.0.0` tag" without reading any `.planning/` files.

## Tasks Completed

| Task | Name | Commit | Key files |
|---|---|---|---|
| 1 | upload-weights.sh one-shot uploader with SHA-256 output | e80a467 | pod/scripts/upload-weights.sh |
| 2 | pod/weights/README.md + full pod/README.md runbook | e613010 | pod/weights/README.md, pod/README.md |
| 3 | .planning/MINIO-SETUP.md 10-item operator checklist | 63a112f | .planning/MINIO-SETUP.md |
| 4 | Correct POD-02 hybrid-weights drift in REQUIREMENTS.md | fcdde4d | .planning/REQUIREMENTS.md |

## What Was Built

### 1. pod/scripts/upload-weights.sh (new, mode 755, 133 lines)

Operator-side script that:
- Validates 4 required env vars (`MINIO_ENDPOINT`, `MINIO_ACCESS_KEY`, `MINIO_SECRET_KEY`, `MINIO_BUCKET`) via `${VAR:?missing}` guards
- Preflights 5 required binaries (mc, jq, curl, sha256sum, tar)
- Downloads from 3 canonical HF repos (`unsloth/Qwen3.5-27B-GGUF`, `Systran/faster-whisper-large-v3`, `BAAI/bge-m3`) with optional Bearer token for rate-limit elevation
- Tars Whisper/BGE-M3 directories (single-file GGUF for Qwen is uploaded as-is)
- Uploads to MinIO via `mc cp`, writes `.sha256` sidecar via `mc pipe`
- Prints paste-ready block with `WEIGHTS_QWEN_SHA256`, `WEIGHTS_WHISPER_SHA256`, `WEIGHTS_BGE_M3_SHA256` values + MinIO config + the `gh workflow run smoke.yml` next-step command
- Idempotent via `--workdir` cache (re-runs skip already-downloaded HF files)
- Versioned via `--weights-version` flag (default `v1.0.0` matches D-06 convention)

### 2. pod/weights/README.md (new, 77 lines, pt-BR)

Explains the MinIO mirror to any operator/engineer:
- Bucket layout (6 objects — 3 payloads + 3 sidecars)
- D-06 versioning semantics (v1.0.0 segment enables rollback independent of image)
- D-05 checksum invariant (two sources of truth: GH Secrets + sidecar)
- Upload procedure (disk/throughput/binary requirements + step-by-step)
- 3 rotation scenarios (patch, downgrade, credential rotation)
- 4-row troubleshooting table

### 3. .planning/MINIO-SETUP.md (new, 146 lines)

10-item numbered checklist with bash verification + Expected line per step:
1. MinIO endpoint public HTTPS reachability
2. Bucket `ifix-ai-weights` with `private` policy
3. Service account has PutObject/GetObject/ListBucket (echo-roundtrip probe)
4. Download throughput >=90 Mbps sustained (D-02 gate) via `dd` + `time mc cp` roundtrip
5. HuggingFace reachable (partial-range probe, 401 -> set HF_TOKEN)
6. Run `./pod/scripts/upload-weights.sh --weights-version v1.0.0` (references D-05 + D-06)
7. Confirm 6 objects present (3 payloads + 3 sidecars)
8. Populate 8 GH Secrets (VAST_AI_API_KEY, MINIO_*, WEIGHTS_*_SHA256) + verify with `gh secret list | wc -l = 8`
9. build-pod.yml has a successful run (prereq for smoke image tag)
10. Trigger smoke.yml manually (D-19 evaluation, D-22 auto-teardown)

Also includes a credential-rotation section (T-01-09-05 mitigation, 90-day cadence) and a failure-recovery table mapping each step's failure mode to its recovery action.

### 4. pod/README.md (replaces plan-01 stub, 173 lines)

Supersedes the 37-line stub. Sections:
- Images published (2 images + tag conventions)
- 5-service architecture table
- Setup pointer to MINIO-SETUP.md + 5-step summary
- Local-dev flow (docker build + compose + smoke.py --fast + down -v)
- CI smoke flow (gh workflow run + 6-step description)
- D-23 stable-tag promotion (git tag + push -> build-pod.yml release trigger)
- D-20 baseline archival (gh run download -> copy -> git commit)
- 5-row troubleshooting table keyed by D-19 gate exit codes
- Complete repo layout with per-file plan provenance (`# plan NN` comments)

### 5. REQUIREMENTS.md POD-02 fix

Before:
> POD-02: Imagem inclui weights embutidos (Qwen 3.5 27B Q4_K_M GGUF, Whisper large-v3, BGE-M3) para cold-start <=5 min na Vast.ai

After:
> POD-02: Imagem magra (~2 GB) publicada em `ghcr.io/ifixtelecom/ifix-ai-pod`; weights (Qwen 3.5 27B Q4_K_M GGUF, Whisper large-v3, BGE-M3) baixados do MinIO Ifix via `onstart.sh` no boot do pod — cold-start <=5 min (decisões D-01, D-02, D-04; integridade validada via SHA-256 por D-05)

POD-01, POD-03..POD-07 untouched. The old drifted phrase came from an earlier STATE.md entry that itself reflected a pre-research plan (weights-in-image) — this phase's decisions flipped that in D-01/D-02 (weights on MinIO) without REQUIREMENTS.md being updated at the time. Now synchronized.

## Verification Performed

| Check | Result |
|---|---|
| `bash -n pod/scripts/upload-weights.sh` | pass |
| `shellcheck -S error` | not installed in worktree; syntax check is authoritative |
| All cross-referenced paths exist (pod/Dockerfile, pod/docker-compose.yml, pod/onstart.sh, pod/smoke/smoke.py, pod/health-bridge/Dockerfile, pod/templates/qwen3.5-27b-tool-calling.jinja, pod/scripts/{download,upload,vast-ai}.sh, .github/workflows/{build-pod,smoke}.yml) | 11/11 present |
| 10 `- [ ] **N.` items in MINIO-SETUP.md | 10/10 |
| D-XX coverage: D-01, D-02, D-04, D-05, D-06 in weights/README.md; D-19, D-20, D-22, D-23 in pod/README.md; D-02, D-05, D-06, D-19, D-22, D-23 in MINIO-SETUP.md | all confirmed |
| Old drifted phrase "weights embutidos" removed from REQUIREMENTS.md | confirmed absent |

## Deviations from Plan

None from Rules 1-4. The plan was executed exactly as written, including:
- Script content verbatim from the plan (one 4-character minor difference: `${HF_TOKEN:-}` default already in the plan's shown code was kept)
- Two markdown documents verbatim (pod/weights/README.md, MINIO-SETUP.md)
- pod/README.md content produced verbatim then one cosmetic adjustment (layout tree flattened to use absolute `pod/Dockerfile # plan 03` lines) to satisfy the plan's automated verify regex `pod/Dockerfile.*# plan 03`
- REQUIREMENTS.md POD-02 text per plan's exact prescription

No architectural decisions required; no auth gates; no blockers.

## Authentication Gates

None encountered. This plan's autonomous work (creating script + docs) requires no external credentials. The plan is labelled `autonomous: false` because its RUN-TIME target is operator execution (against MinIO + HuggingFace + GitHub Secrets), but the FILES themselves are pure source changes.

## Phase 1 Closure Status

With this plan's artifacts merged:

- All 9 Phase 1 plans have SUMMARY.md
- All 7 POD-XX requirements have a corresponding implementation path (POD-01..POD-07 mapped across plans 01..08; POD-02 drift fixed)
- All 27 locked decisions (D-01..D-27) have artifact coverage
- D-19 gates are enforceable — operator runs MINIO-SETUP.md checklist -> `gh workflow run smoke.yml` -> D-19 gates -> D-23 stable tag
- D-20 baseline archival handoff to Phase 5 is documented

Phase 1 is content-complete. The Phase 1 retrospective (quantitative POD-XX / D-XX coverage tables + gate readiness assessment) will be produced by execute-phase after orchestrator merges this wave.

## Self-Check: PASSED

- pod/scripts/upload-weights.sh: FOUND (mode 755, bash -n clean)
- pod/weights/README.md: FOUND (77 lines)
- pod/README.md: FOUND (173 lines, superseded the stub)
- .planning/MINIO-SETUP.md: FOUND (146 lines, 10 checklist items)
- .planning/REQUIREMENTS.md: MODIFIED (POD-02 text corrected; POD-01, POD-03..POD-07 untouched per diff)
- All 4 commit hashes present in git log: e80a467 (task 1), e613010 (task 2), 63a112f (task 3), fcdde4d (task 4) — confirmed via `git log --oneline`
- All 11 cross-referenced files from the verification block exist on disk
