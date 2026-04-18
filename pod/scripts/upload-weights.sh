#!/usr/bin/env bash
# pod/scripts/upload-weights.sh — one-shot MinIO weight upload (operator action).
#
# Run this ONCE (or when weights need rotating per D-06 versioning).
# Requires ~25 GB free disk + fast upstream + mc + jq + curl.
#
# Usage:
#   MINIO_ENDPOINT=https://... \
#   MINIO_ACCESS_KEY=... \
#   MINIO_SECRET_KEY=... \
#   MINIO_BUCKET=ifix-ai-weights \
#   ./pod/scripts/upload-weights.sh [--weights-version v1.0.0] [--workdir /tmp/weights-stage] [--hf-token TOKEN]
#
# At the end, prints three WEIGHTS_*_SHA256 values to paste into GH Secrets.

set -euo pipefail

: "${MINIO_ENDPOINT:?missing}" "${MINIO_ACCESS_KEY:?missing}" "${MINIO_SECRET_KEY:?missing}" "${MINIO_BUCKET:?missing}"

WEIGHTS_VERSION="v1.0.0"
WORKDIR="/tmp/ifix-weights-stage"
HF_TOKEN="${HF_TOKEN:-}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --weights-version) WEIGHTS_VERSION="$2"; shift 2;;
    --workdir)         WORKDIR="$2";         shift 2;;
    --hf-token)        HF_TOKEN="$2";        shift 2;;
    *) echo "unknown arg $1" >&2; exit 2;;
  esac
done

log() { printf '[%s] [upload-weights] %s\n' "$(date -Iseconds)" "$*" >&2; }

# --- prerequisites --------------------------------------------------------
for bin in mc jq curl sha256sum tar; do
  command -v "$bin" >/dev/null 2>&1 || { log "missing required binary: $bin"; exit 1; }
done

mkdir -p "${WORKDIR}"
cd "${WORKDIR}"

log "version=${WEIGHTS_VERSION} workdir=${WORKDIR}"

# --- configure mc alias ---------------------------------------------------
mc alias set ifix "${MINIO_ENDPOINT}" "${MINIO_ACCESS_KEY}" "${MINIO_SECRET_KEY}" >/dev/null
mc mb --ignore-existing "ifix/${MINIO_BUCKET}" >/dev/null

# --- helpers --------------------------------------------------------------
hf_download() {
  local repo="$1" filename="$2" dest="$3"
  local url="https://huggingface.co/${repo}/resolve/main/${filename}"
  local args=(-fsSL -o "${dest}" "${url}")
  if [[ -n "${HF_TOKEN}" ]]; then
    args=(-H "Authorization: Bearer ${HF_TOKEN}" "${args[@]}")
  fi
  log "HF get ${repo}/${filename}"
  curl "${args[@]}"
}

upload_with_sidecar() {
  local local_path="$1" s3_key="$2"
  log "computing sha256 ${local_path}"
  local sum
  sum="$(sha256sum "${local_path}" | awk '{print $1}')"
  log "uploading ${local_path} -> s3://${MINIO_BUCKET}/${s3_key} (${sum:0:12}...)"
  mc cp --quiet "${local_path}" "ifix/${MINIO_BUCKET}/${s3_key}"
  printf '%s' "${sum}" | mc pipe --quiet "ifix/${MINIO_BUCKET}/${s3_key}.sha256"
  printf '%s' "${sum}"
}

# --- 1) Qwen 3.5 27B Q4_K_M GGUF (single file) ---------------------------
log "=== Qwen 3.5 27B Q4_K_M ==="
QWEN_FILE="${WORKDIR}/qwen.gguf"
if [[ ! -f "${QWEN_FILE}" ]]; then
  hf_download "unsloth/Qwen3.5-27B-GGUF" "Qwen3.5-27B-Q4_K_M.gguf" "${QWEN_FILE}"
fi
QWEN_SHA=$(upload_with_sidecar "${QWEN_FILE}" "qwen3.5-27b-Q4_K_M/${WEIGHTS_VERSION}/model.gguf")

# --- 2) Whisper large-v3 (tarball of Systran/faster-whisper-large-v3) ----
log "=== Whisper large-v3 ==="
WHISPER_DIR="${WORKDIR}/whisper"
mkdir -p "${WHISPER_DIR}"
for f in model.bin config.json tokenizer.json vocabulary.json preprocessor_config.json; do
  if [[ ! -f "${WHISPER_DIR}/${f}" ]]; then
    hf_download "Systran/faster-whisper-large-v3" "${f}" "${WHISPER_DIR}/${f}" || log "note: ${f} not found (skipping)"
  fi
done
WHISPER_TAR="${WORKDIR}/whisper.tar.gz"
tar -C "${WHISPER_DIR}" -czf "${WHISPER_TAR}" .
WHISPER_SHA=$(upload_with_sidecar "${WHISPER_TAR}" "whisper-large-v3/${WEIGHTS_VERSION}/model.tar.gz")

# --- 3) BGE-M3 (HF cache tarball) ----------------------------------------
log "=== BGE-M3 ==="
BGE_DIR="${WORKDIR}/bge-m3"
mkdir -p "${BGE_DIR}"
for f in config.json tokenizer.json tokenizer_config.json sentencepiece.bpe.model \
         pytorch_model.bin model.safetensors sentence_bert_config.json; do
  if [[ ! -f "${BGE_DIR}/${f}" ]]; then
    hf_download "BAAI/bge-m3" "${f}" "${BGE_DIR}/${f}" || log "note: ${f} not found (skipping)"
  fi
done
BGE_TAR="${WORKDIR}/bge-m3.tar.gz"
tar -C "${BGE_DIR}" -czf "${BGE_TAR}" .
BGE_SHA=$(upload_with_sidecar "${BGE_TAR}" "bge-m3/${WEIGHTS_VERSION}/model.tar.gz")

# --- final instructions ---------------------------------------------------
cat <<EOF

====================================================================
  Upload complete. Paste these into GitHub Secrets for smoke.yml
  (Repo Settings > Secrets and variables > Actions > New repository secret)
====================================================================

  WEIGHTS_QWEN_SHA256    = ${QWEN_SHA}
  WEIGHTS_WHISPER_SHA256 = ${WHISPER_SHA}
  WEIGHTS_BGE_M3_SHA256  = ${BGE_SHA}

  Also set (one-time):
    MINIO_ENDPOINT  = ${MINIO_ENDPOINT}
    MINIO_BUCKET    = ${MINIO_BUCKET}
    MINIO_ACCESS_KEY = (the key you used above)
    MINIO_SECRET_KEY = (the secret you used above)

  Object keys already match smoke.yml defaults:
    qwen3.5-27b-Q4_K_M/${WEIGHTS_VERSION}/model.gguf
    whisper-large-v3/${WEIGHTS_VERSION}/model.tar.gz
    bge-m3/${WEIGHTS_VERSION}/model.tar.gz

  Next step: trigger smoke.yml manually:
    gh workflow run smoke.yml -f image_tag=develop
====================================================================
EOF
