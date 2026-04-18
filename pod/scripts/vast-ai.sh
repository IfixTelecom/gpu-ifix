#!/usr/bin/env bash
# pod/scripts/vast-ai.sh — Bash wrapper around Vast.ai REST API.
# Used by .github/workflows/smoke.yml (D-22) to orchestrate ephemeral pods.
#
# Usage:
#   pod/scripts/vast-ai.sh search [--max-price N] [--gpu "RTX 4090"]
#   pod/scripts/vast-ai.sh create --offer-id N --image TAG --env-file PATH --onstart PATH
#   pod/scripts/vast-ai.sh status --instance-id N
#   pod/scripts/vast-ai.sh wait-running --instance-id N [--timeout-seconds N]
#   pod/scripts/vast-ai.sh destroy --instance-id N
#   pod/scripts/vast-ai.sh ssh-exec --ssh-host HOST --ssh-port PORT --cmd "..."
#   pod/scripts/vast-ai.sh scp-upload --ssh-host HOST --ssh-port PORT --src LOCAL --dest REMOTE
#
# Env:
#   VAST_AI_API_KEY (required)
#   VAST_BASE (optional, default https://vast.ai/api/v0)

set -euo pipefail

: "${VAST_AI_API_KEY:?missing}"
VAST_BASE="${VAST_BASE:-https://vast.ai/api/v0}"

log() { printf '[%s] [vast-ai] %s\n' "$(date -Iseconds)" "$*" >&2; }

# --- subcommand dispatch -------------------------------------------------
cmd="${1:-}"; shift || true

api() {
  # api METHOD PATH [JSON_BODY]
  local method="$1" path="$2" body="${3:-}"
  local url="${VAST_BASE}${path}"
  local args=(-sS -X "${method}" -H "Authorization: Bearer ${VAST_AI_API_KEY}")
  if [[ -n "${body}" ]]; then
    args+=(-H "Content-Type: application/json" --data-raw "${body}")
  fi
  curl "${args[@]}" "${url}"
}

case "${cmd}" in

  search)
    # search [--max-price N] [--gpu "RTX 4090"]
    MAX_PRICE="0.40"
    GPU="RTX 4090"
    while [[ $# -gt 0 ]]; do
      case "$1" in
        --max-price) MAX_PRICE="$2"; shift 2;;
        --gpu) GPU="$2"; shift 2;;
        *) log "unknown arg $1"; exit 2;;
      esac
    done
    # Vast.ai search accepts a "q" JSON filter.
    Q=$(jq -cn --arg gpu "$GPU" --argjson max "$MAX_PRICE" '{
      gpu_name:$gpu, num_gpus:1, disk_space:{gte:50},
      reliability:{gte:0.98}, inet_down:{gte:200},
      rentable:true, verified:{eq:true},
      dph_total:{lte:$max},
      order:[["dph_total","asc"]], limit:5
    }')
    api GET "/bundles/?q=${Q}"
    ;;

  create)
    # create --offer-id N --image TAG --env-file PATH --onstart PATH
    OFFER=""; IMAGE=""; ENV_FILE=""; ONSTART=""
    while [[ $# -gt 0 ]]; do
      case "$1" in
        --offer-id)  OFFER="$2"; shift 2;;
        --image)     IMAGE="$2"; shift 2;;
        --env-file)  ENV_FILE="$2"; shift 2;;
        --onstart)   ONSTART="$2"; shift 2;;
        *) log "unknown arg $1"; exit 2;;
      esac
    done
    : "${OFFER:?}" "${IMAGE:?}" "${ENV_FILE:?}" "${ONSTART:?}"
    # Build env map from env-file (KEY=VALUE, one per line, comments allowed)
    ENV_JSON=$(awk -F= '!/^#/ && NF>=2 {gsub(/"/,"\\\"",$2); printf "\"%s\":\"%s\",", $1, $2}' "$ENV_FILE" \
               | sed 's/,$//' | awk '{print "{" $0 "}"}')
    ONSTART_B64=$(base64 -w0 "$ONSTART")
    BODY=$(jq -cn --arg img "$IMAGE" --argjson env "$ENV_JSON" --arg onstart_b64 "$ONSTART_B64" '{
      client_id:"me",
      image:$img,
      env:$env,
      onstart:"echo \($onstart_b64) | base64 -d > /root/onstart.sh && chmod +x /root/onstart.sh && /root/onstart.sh",
      runtype:"ssh",
      disk:60,
      label:"ifix-smoke"
    }')
    api PUT "/asks/${OFFER}/" "${BODY}"
    ;;

  status)
    # status --instance-id N
    ID=""
    while [[ $# -gt 0 ]]; do
      case "$1" in
        --instance-id) ID="$2"; shift 2;;
        *) log "unknown arg $1"; exit 2;;
      esac
    done
    : "${ID:?}"
    api GET "/instances/${ID}/"
    ;;

  wait-running)
    # wait-running --instance-id N --timeout-seconds N
    ID=""; TIMEOUT=900
    while [[ $# -gt 0 ]]; do
      case "$1" in
        --instance-id)      ID="$2"; shift 2;;
        --timeout-seconds)  TIMEOUT="$2"; shift 2;;
        *) log "unknown arg $1"; exit 2;;
      esac
    done
    : "${ID:?}"
    DEADLINE=$(( $(date +%s) + TIMEOUT ))
    while [[ $(date +%s) -lt ${DEADLINE} ]]; do
      S=$(api GET "/instances/${ID}/" || true)
      ACTUAL=$(printf '%s' "$S" | jq -r '.instances.actual_status // .instances[0].actual_status // empty')
      SSH_HOST=$(printf '%s' "$S" | jq -r '.instances.ssh_host // .instances[0].ssh_host // empty')
      SSH_PORT=$(printf '%s' "$S" | jq -r '.instances.ssh_port // .instances[0].ssh_port // empty')
      IP=$(printf '%s' "$S" | jq -r '.instances.public_ipaddr // .instances[0].public_ipaddr // empty')
      log "instance ${ID}: actual_status=${ACTUAL}  ip=${IP}  ssh=${SSH_HOST}:${SSH_PORT}"
      if [[ "${ACTUAL}" == "running" && -n "${SSH_HOST}" && -n "${SSH_PORT}" ]]; then
        printf '%s' "$S"
        exit 0
      fi
      sleep 15
    done
    log "timeout waiting for instance ${ID} to reach running"
    exit 1
    ;;

  destroy)
    ID=""
    while [[ $# -gt 0 ]]; do
      case "$1" in
        --instance-id) ID="$2"; shift 2;;
        *) log "unknown arg $1"; exit 2;;
      esac
    done
    : "${ID:?}"
    log "destroying instance ${ID}"
    api DELETE "/instances/${ID}/"
    ;;

  ssh-exec)
    # ssh-exec --ssh-host HOST --ssh-port PORT --cmd "..."
    SSH_HOST=""; SSH_PORT=""; CMD=""
    while [[ $# -gt 0 ]]; do
      case "$1" in
        --ssh-host) SSH_HOST="$2"; shift 2;;
        --ssh-port) SSH_PORT="$2"; shift 2;;
        --cmd)      CMD="$2"; shift 2;;
        *) log "unknown arg $1"; exit 2;;
      esac
    done
    : "${SSH_HOST:?}" "${SSH_PORT:?}" "${CMD:?}"
    ssh -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=/tmp/known_hosts \
        -p "${SSH_PORT}" "root@${SSH_HOST}" "${CMD}"
    ;;

  scp-upload)
    # scp-upload --ssh-host HOST --ssh-port PORT --src LOCAL --dest REMOTE
    SSH_HOST=""; SSH_PORT=""; SRC=""; DEST=""
    while [[ $# -gt 0 ]]; do
      case "$1" in
        --ssh-host) SSH_HOST="$2"; shift 2;;
        --ssh-port) SSH_PORT="$2"; shift 2;;
        --src)      SRC="$2"; shift 2;;
        --dest)     DEST="$2"; shift 2;;
        *) log "unknown arg $1"; exit 2;;
      esac
    done
    : "${SSH_HOST:?}" "${SSH_PORT:?}" "${SRC:?}" "${DEST:?}"
    scp -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=/tmp/known_hosts \
        -P "${SSH_PORT}" -r "${SRC}" "root@${SSH_HOST}:${DEST}"
    ;;

  *)
    cat >&2 <<EOF
usage:
  $0 search [--max-price N] [--gpu GPU_NAME]
  $0 create --offer-id N --image TAG --env-file PATH --onstart PATH
  $0 status --instance-id N
  $0 wait-running --instance-id N [--timeout-seconds N]
  $0 destroy --instance-id N
  $0 ssh-exec --ssh-host HOST --ssh-port PORT --cmd "..."
  $0 scp-upload --ssh-host HOST --ssh-port PORT --src L --dest R

env: VAST_AI_API_KEY (required), VAST_BASE (optional, default https://vast.ai/api/v0)
EOF
    exit 2
    ;;
esac
