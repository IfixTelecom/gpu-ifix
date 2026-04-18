---
phase: 01-gpu-pod-image-smoke-test
plan: 08
subsystem: ci

tags: [github-actions, workflow-dispatch, vast-ai, rest-api, ssh, scp, minio, smoke-test, d-19-gates, d-22-cost-cap, d-23-stable-promote, bash]

# Dependency graph
requires:
  - phase: 01-gpu-pod-image-smoke-test/03
    provides: "pod/docker-compose.yml (scp'd to the pod before onstart invocation) + port topology (8000/8001/8002/9100/9400) that smoke.py hits via public IP"
  - phase: 01-gpu-pod-image-smoke-test/05
    provides: "pod/onstart.sh + pod/scripts/download-weights.sh (scp'd and re-invoked by the provisioning step) + the onstart env-var contract (MINIO_*, WEIGHTS_*_KEY, WEIGHTS_*_SHA256, IFIX_AI_POD_IMAGE, IFIX_AI_POD_HEALTH_BRIDGE_IMAGE, WEIGHTS_DIR) consumed verbatim to build /tmp/pod.env"
  - phase: 01-gpu-pod-image-smoke-test/06
    provides: "pod/smoke/smoke.py CLI (--target/--out) and its 0..6 exit code taxonomy — the workflow's final step exits with exactly smoke.py's code"
  - phase: 01-gpu-pod-image-smoke-test/07
    provides: ".github/workflows/build-pod.yml that publishes ghcr.io/ifixtelecom/ifix-ai-pod:{tag} and -health-bridge:{tag} — smoke.yml consumes these images via its image_tag input"
provides:
  - ".github/workflows/smoke.yml: workflow_dispatch-only GHA that spins up an ephemeral Vast.ai RTX 4090 pod, provisions it via SSH, runs smoke.py, enforces D-19 gates, and guarantees teardown (D-22)"
  - "pod/scripts/vast-ai.sh: 7-subcommand Bash wrapper around the Vast.ai REST API (search / create / status / wait-running / destroy / ssh-exec / scp-upload) — reusable by Phase 6 auto-provisioner"
  - "End-to-end POD-07 validation path: a green smoke run IS the D-23 stable-tag promotion gate"
  - "Guaranteed pod teardown contract: destroy step runs `if: always() && steps.create.outputs.instance_id != ''` so no cost leaks even on mid-run failure"
  - "Cost guardrails: max_price_per_hour input (default $0.40/h), smoke_timeout_minutes hard cap (default 45 min), concurrency.cancel-in-progress: false (never kill a live pod)"
  - "smoke-report.json as a 90-day retained GHA artifact (retention-days: 90, name scoped by image_tag + run_id) — feeds Phase 5 saturation baseline per D-20"
affects:
  - 01-09 (phase closure / MinIO upload runbook — references this workflow as the verification path)
  - Phase 6 (auto-provisioning — reuses pod/scripts/vast-ai.sh lifecycle primitives verbatim)
  - D-23 stable-tag promotion path (tag-push to .github/workflows/build-pod.yml is gated by this workflow going green first)

# Tech tracking
tech-stack:
  added:
    - "Vast.ai REST API direct integration via curl + jq (no Go SDK exists — research STACK.md §External Provider SDKs)"
    - "actions/upload-artifact@v4 with retention-days: 90 (scoped to image_tag + run_id)"
    - "GITHUB_STEP_SUMMARY markdown publication pattern (gates table + metrics JSON blob)"
    - "ssh/scp direct from GHA runner (accept-new host-key policy writing to /tmp/known_hosts, port forwarded by Vast.ai)"
  patterns:
    - "Subcommand-dispatching Bash script (case/esac) with `: \"${VAR:?missing}\"` argument validation per subcommand — keeps REST call logic out of the YAML where it's hard to test"
    - "Env-file -> JSON env map for Vast.ai create: awk reads KEY=VALUE lines, jq assembles the instance env object, base64-encoded onstart ensures the multi-line script survives JSON embedding"
    - "Polling + timeout pattern for wait-running: `DEADLINE=$(( $(date +%s) + TIMEOUT ))` + inner `sleep 15` loop, exits 1 on deadline with a triage log line"
    - "if: always() guard on teardown step gated by step output presence (`steps.create.outputs.instance_id != ''`) — destroy never attempts an empty/failed create"
    - "Exit-code bubbling pattern: smoke step uses `set +e` to capture $?, writes to $GITHUB_OUTPUT, lets archive+destroy complete, then a final `if: always()` step exits with the captured code so workflow status mirrors smoke.py status"
    - "Operator-readable section banners inside `run:` blocks (`=== Preparing smoke run ===`, `=== Creating Vast.ai pod ===`, etc.) — matches 01-PATTERNS.md §what to copy from converseai-v4 ci.yml SSH deploy"

key-files:
  created:
    - "pod/scripts/vast-ai.sh (195 lines, mode 755, shebang #!/usr/bin/env bash, set -euo pipefail). 7 subcommands: search/create/status/wait-running/destroy/ssh-exec/scp-upload. Bearer-token auth via VAST_AI_API_KEY. Default GPU filter: RTX 4090, reliability>=0.98, inet_down>=200 Mbps, verified=true (PITFALLS §4)."
    - ".github/workflows/smoke.yml (291 lines, 1 job with 15 steps). workflow_dispatch-only trigger with 7 inputs. permissions: contents: read. Step 15 (Fail workflow on smoke exit code) bubbles smoke.py's 0..6 code."
  modified: []

key-decisions:
  - "REST wrapper in Bash, not Python/Go. Reason: the plan's `<action>` block is explicit, and Bash keeps the dependency footprint on the GHA runner minimal (curl + jq + ssh + scp are all pre-installed or apt-installed in a single step). Bash also makes each subcommand a readable 10-20 line block — Python or Go would introduce packaging overhead for zero correctness gain."
  - "Subcommands accept `--ssh-host`/`--ssh-port` via flags rather than deriving from --instance-id. Reason: the workflow's `wait-running` step already emits ssh_host/ssh_port as step outputs; ssh-exec/scp-upload receive them directly. This also matches the plan's `<action>` block usage exactly."
  - "`pr-wait` timeout default 900s (15 min). 10 min for Vast.ai instance to reach running + 5 min safety margin for slow base-image pulls (pod image is nvidia/cuda which is multi-GB). If the pod hasn't come up in 15 min, it won't come up; log triage data and exit 1 so the destroy step can run."
  - "`concurrency.cancel-in-progress: false` — the one place this workflow diverges from the Ifix canonical pattern (which uses `true` for build workflows). Rationale: a smoke run is NOT idempotent because it spins up a billable pod; cancelling mid-run without teardown = leaked pod = $$$ bleed. The plan 07 build-pod.yml uses `true` because image pushes ARE idempotent."
  - "Step order has `Materialize env-file` BEFORE `Create Vast.ai instance` so that `pod/scripts/vast-ai.sh create --env-file /tmp/pod.env` has a file to read at create time; and AFTER `Search for Vast.ai offer` so the offer_id is known before we spend effort staging the env-file. This ordering also puts the env-file in-flight for the later `Upload compose + scripts to pod` step (which copies /tmp/pod.env to /opt/ifix-ai-pod/.env on the pod)."
  - "Destroy step's `if: always() && steps.create.outputs.instance_id != ''` (instead of simply `if: always()`). Reason: if the Create step failed before emitting an instance_id, there's nothing to destroy; calling `pod/scripts/vast-ai.sh destroy --instance-id ''` would fail due to the `: \"${ID:?}\"` guard inside the script. The `!= ''` check keeps the destroy step green when create never succeeded."
  - "Workflow timeout is sourced from an input (`smoke_timeout_minutes`, default 45) via `timeout-minutes: ${{ fromJSON(inputs.smoke_timeout_minutes) }}`. Reason: different images may have different cold-start profiles (CUDA 12.4 vs 12.6, different Whisper weights), and the operator should be able to widen the budget for a one-off experiment without editing the workflow. 45 min default is 1.5x the expected 30-min run per D-22."
  - "smoke.py exit code is captured via `set +e` in the smoke step (not just letting the step fail). Reason: if the step fails immediately, `upload-artifact` and `destroy` (both `if: always()`) still run — but the Summary step `EXIT: ${{ steps.smoke.outputs.exit_code }}` would be empty. Capturing the code into an output lets the summary report the specific gate that failed (2..5 = single gate, 6 = multi-gate) without needing to re-parse smoke-report.json from a failed-step context."

patterns-established:
  - "Vast.ai CLI-wrapper contract (pod/scripts/vast-ai.sh). Phase 6 auto-provisioner can import the same script verbatim for its pod lifecycle — no drift between smoke-run pods and production emergency pods."
  - "Teardown-is-mandatory pattern: any workflow that creates billable external resources MUST gate teardown on `if: always() && <resource-id-present>`. Documented here as the D-22 standard for all future Vast.ai-touching workflows."
  - "Exit-code-bubble pattern (set +e in a captured step + if: always() archive/teardown + terminal exit-with-captured-code step). Reusable for any workflow where cleanup MUST run regardless of test outcome."
  - "Env-file staging + scp pattern: materialize secrets into /tmp/pod.env with umask 077, scp it to pod /opt/ifix-ai-pod/.env. Reused by Phase 6 for emergency-pod provisioning with the same env-file shape."

requirements-completed: [POD-07]

# Metrics
duration: ~4min
completed: 2026-04-18
---

# Phase 01 Plan 08: GitHub Actions smoke.yml + Vast.ai CLI wrapper Summary

**`workflow_dispatch`-only GHA that spins up an ephemeral Vast.ai RTX 4090 pod, provisions it via the Vast.ai REST API + SSH/SCP, runs smoke.py (plan 06) end-to-end, enforces D-19 gates via the workflow exit code, and guarantees teardown in an `if: always()` step bounded by a 45-min hard timeout and a `$0.40/h` cost cap — with a 195-line `pod/scripts/vast-ai.sh` Bash wrapper (7 subcommands) keeping REST logic out of the YAML.**

## Performance

- **Duration:** ~4 min (two-task plan, measured from `PLAN_START_EPOCH=1776471953` through the final task commit)
- **Started:** 2026-04-18T00:25:53Z
- **Completed:** 2026-04-18T00:30:00Z (approximate — will be finalized by the plan-metadata commit that follows this SUMMARY)
- **Tasks:** 2 / 2
- **Files created:** 2, modified: 0

## Accomplishments

### pod/scripts/vast-ai.sh (Task 1 — 195 lines)

Single Bash script exposing 7 subcommands via case/esac dispatch. Each subcommand parses its own flags into named variables, validates required flags with `: "${VAR:?}"`, and shells out to either `curl` (for REST calls) or `ssh`/`scp` (for the transport subcommands).

| Subcommand    | Purpose                                    | Vast.ai REST endpoint                          |
| ------------- | ------------------------------------------ | ---------------------------------------------- |
| `search`      | Find cheapest RTX 4090 offer under --max-price | `GET /bundles/?q=<json-filter>`                |
| `create`      | Accept offer → create instance             | `PUT /asks/{offer_id}/` (body: image/env/onstart/runtype/disk/label) |
| `status`      | Get instance state (JSON blob)             | `GET /instances/{id}/`                         |
| `wait-running`| Poll status until `actual_status==running` + SSH present; timeout 1 | `GET /instances/{id}/` loop, sleep 15s, deadline = now+timeout |
| `destroy`     | Terminate instance                         | `DELETE /instances/{id}/`                      |
| `ssh-exec`    | Run remote command via SSH                 | (direct ssh root@host -p port)                 |
| `scp-upload`  | Transfer local file/dir to pod             | (direct scp -r src root@host:dest -P port)     |

**Filter shape** for search (from plan `<interfaces>` + PITFALLS §4):
```json
{"gpu_name":"RTX 4090","num_gpus":1,"disk_space":{"gte":50},
 "reliability":{"gte":0.98},"inet_down":{"gte":200},
 "rentable":true,"verified":{"eq":true},"dph_total":{"lte":<max>},
 "order":[["dph_total","asc"]],"limit":5}
```

**Authentication:** Bearer token via `VAST_AI_API_KEY` env var (fail-fast with `: "${VAST_AI_API_KEY:?missing}"` at script entry). `VAST_BASE` overridable for testing (default `https://vast.ai/api/v0`).

**SSH host-key policy:** `accept-new` with per-run known-hosts file at `/tmp/known_hosts`. Vast.ai issues fresh SSH hosts per pod, so pinning is not feasible. T-01-08-01 threat mitigation: short-lived session (~30 min), pod holds no long-lived secrets beyond the MinIO creds scoped to one download.

### .github/workflows/smoke.yml (Task 2 — 291 lines)

Single-job 15-step workflow:

| # | Step name | Role | Exit behaviour |
| - | --------- | ---- | -------------- |
| 1 | Checkout | get repo | fail → stop |
| 2 | Preflight — required secrets present | validate 8 GH Secrets | fail → stop |
| 3 | Verify image exists in GHCR | HEAD manifest; accept 200 or 401 (anon token) | warn-only (Vast.ai does the real pull anyway) |
| 4 | Setup Python + jq | apt-get install deps | fail → stop |
| 5 | Install Python deps | `pip install -r pod/smoke/requirements.txt` | fail → stop |
| 6 | Search for Vast.ai offer | vast-ai.sh search → pick cheapest | fail → stop; emits offer_id + price outputs |
| 7 | Materialize env-file for create | cat > /tmp/pod.env (umask 077) | fail → stop |
| 8 | Create Vast.ai instance | vast-ai.sh create | fail → destroy only if inst_id was parsed |
| 9 | Wait for instance running + SSH ready | vast-ai.sh wait-running (15 min timeout) | fail → destroy runs |
| 10 | Upload compose + scripts to pod | stage /tmp/pod-bundle, scp to /opt/ifix-ai-pod, re-invoke onstart | fail → destroy runs |
| 11 | Run smoke-test (plan 06) | `python3 pod/smoke/smoke.py --target http://<ip> --out smoke-report.json`; `set +e` captures $? into output | exit 0..6 captured, not bubbled here |
| 12 | Archive smoke-report.json | actions/upload-artifact@v4, retention-days: 90 | `if: always()` |
| 13 | Publish summary | write gates + metrics JSON to $GITHUB_STEP_SUMMARY | `if: always()` |
| 14 | Destroy Vast.ai instance (ALWAYS — D-22 cost cap) | vast-ai.sh destroy | `if: always() && steps.create.outputs.instance_id != ''` |
| 15 | Fail workflow on smoke exit code | `exit "$CODE"` where CODE = steps.smoke.outputs.exit_code | `if: always()` → workflow status mirrors smoke.py status |

**Inputs** (all `workflow_dispatch`):

| Input | Default | Purpose |
| ----- | ------- | ------- |
| `image_tag` | `develop` | which ghcr.io/ifixtelecom/ifix-ai-pod tag to test |
| `health_bridge_tag` | `''` → falls back to image_tag | allows independent HB tag for hotfix verification |
| `weights_qwen_key` | `qwen3.5-27b-Q4_K_M/v1.0.0/model.gguf` | MinIO object key (D-06 versioned URL) |
| `weights_whisper_key` | `whisper-large-v3/v1.0.0/model.tar.gz` | MinIO object key |
| `weights_bge_m3_key` | `bge-m3/v1.0.0/model.tar.gz` | MinIO object key |
| `max_price_per_hour` | `0.40` | D-22 cost cap; rejects offers above |
| `smoke_timeout_minutes` | `45` | workflow timeout-minutes |

**Secrets** (all consumed at job-env level):

`VAST_AI_API_KEY`, `MINIO_ENDPOINT`, `MINIO_ACCESS_KEY`, `MINIO_SECRET_KEY`, `MINIO_BUCKET`, `WEIGHTS_QWEN_SHA256`, `WEIGHTS_WHISPER_SHA256`, `WEIGHTS_BGE_M3_SHA256`.

**Concurrency:** `group: smoke-${{ github.ref }}`, `cancel-in-progress: false`. A smoke run is NOT idempotent (billable pod); cancelling mid-run without teardown would leak money. Plan 07's `build-pod.yml` uses `true` because image pushes ARE idempotent — the divergence is intentional.

**Permissions:** `contents: read` only. No `packages: write` — this workflow does not push images.

## Task Commits

| # | Task | Commit | Files |
| - | ---- | ------ | ----- |
| 1 | Write pod/scripts/vast-ai.sh | `0642545` | `pod/scripts/vast-ai.sh` (new, 195 lines, mode 755) |
| 2 | Write .github/workflows/smoke.yml | `aff8e8a` | `.github/workflows/smoke.yml` (new, 291 lines) |

**Plan metadata commit:** this SUMMARY.md will be committed as a final `docs(01-08)` commit by the executor's final-commit step (inside the worktree; merged to base branch by the orchestrator after the wave completes).

## Files Created/Modified

| Path | Role | Notes |
| ---- | ---- | ----- |
| `pod/scripts/vast-ai.sh` | Infra (Bash wrapper around Vast.ai REST API) | 195 lines, mode 755. 7 subcommands. No external deps beyond curl + jq + ssh + scp. Reusable by Phase 6 auto-provisioner verbatim. |
| `.github/workflows/smoke.yml` | CI (GitHub Actions, Vast.ai end-to-end smoke) | 291 lines. workflow_dispatch-only, 7 inputs, 8 secrets, 15 steps. Single `smoke` job on ubuntu-latest. |

## 10-Step Job Sequence (operator-readable)

For operators reading GHA logs, the workflow surfaces 5 operator-readable banners:

1. `=== Preparing smoke run ===` (step 2)
2. `=== Creating Vast.ai pod ===` (step 6)
3. `=== Provisioning pod ===` (step 10)
4. `=== Running smoke-test ===` (step 11)
5. `=== Destroying pod ===` (step 14)

Full job sequence (15 steps total) — see table above.

## Secrets Operator Must Configure

These MUST be set at the repo or org level in GitHub Secrets BEFORE the workflow's first run. Plan 09 (autonomous: false) documents the one-time operator procedure; this plan assumes it has happened.

| Secret | Source | Purpose |
| ------ | ------ | ------- |
| `VAST_AI_API_KEY` | https://vast.ai/account → Keys tab → Export (Bearer token) | Instance lifecycle (create/destroy/poll) |
| `MINIO_ENDPOINT` | Ifix MinIO admin — HTTPS public endpoint URL | Weight download from pod |
| `MINIO_ACCESS_KEY` | Ifix MinIO admin — service-account access key | Weight download from pod |
| `MINIO_SECRET_KEY` | Ifix MinIO admin — service-account secret key | Weight download from pod |
| `MINIO_BUCKET` | Ifix MinIO admin — bucket name (recommended: `ifix-ai-weights`) | Weight download from pod |
| `WEIGHTS_QWEN_SHA256` | Computed by plan 09 upload runbook | Integrity check per D-05 |
| `WEIGHTS_WHISPER_SHA256` | Computed by plan 09 upload runbook | Integrity check per D-05 |
| `WEIGHTS_BGE_M3_SHA256` | Computed by plan 09 upload runbook | Integrity check per D-05 |

Additionally, **weights must be uploaded** to MinIO at the default object keys (or custom keys passed via workflow inputs) — plan 09 covers this.

Vast.ai dashboard side: set minimum credit balance alert at $20 so smoke runs don't drain the account (https://vast.ai/account/settings/billing).

## Vast.ai API Shape Assumptions — Verify at First Run

Per `.planning/research/STACK.md` §External Provider SDKs (Vast.ai direct REST is MEDIUM confidence), these shapes should be confirmed on the first actual run and the script adjusted if the API has drifted:

1. **Search endpoint:** The script uses `GET /bundles/?q=<url-encoded-json>`. The community-documented shape in 2026 is `GET /bundles/?q=...`; older docs referenced `/asks/`. If `GET /bundles/` returns 404 or empty, try `GET /asks/` (same filter JSON shape).
2. **Search response field path:** The workflow reads `.offers[0].id // .bundles[0].id`. Both paths are checked; if a third shape emerges (e.g., `.instances[0].id`), add it to the `jq` alternation in the Search step.
3. **Offer acceptance:** `PUT /asks/{offer_id}/` with the body shape `{client_id:"me", image, env, onstart, runtype:"ssh", disk:60, label}` is the canonical 2026 shape. Confirm `runtype:"ssh"` vs `"jupyter"` — we need SSH for the scp/ssh steps.
4. **Instance status fields:** `wait-running` polls for `.instances.actual_status` OR `.instances[0].actual_status`. Both are checked because some endpoints return a singular object, others an array. Confirm the actual path on first run.
5. **Create response:** `jq -r '.new_contract // .instance_id // .id // empty'` — three fallbacks. The shape has evolved; if a new field name appears, the workflow surfaces the raw response body in the log and the operator can add a fourth jq alternation.
6. **SSH port + host:** Vast.ai returns `ssh_host` and `ssh_port` in the instance JSON. Port is a non-standard SSH port (typically in the 10000+ range, forwarded to the pod's port 22). The scripts use `-p $SSH_PORT` / `-P $SSH_PORT` and this is expected to work on all Vast.ai providers.
7. **Onstart delivery:** The `create` subcommand base64-encodes the local `pod/onstart.sh` and builds the `onstart` field as `echo <b64> | base64 -d > /root/onstart.sh && chmod +x /root/onstart.sh && /root/onstart.sh`. This is a durable pattern because it doesn't rely on the Vast.ai API parsing multi-line shell. The workflow ALSO re-invokes onstart over SSH after scp'ing docker-compose.yml + .env because the API's onstart runs BEFORE our SSH provisioning (race: the onstart at pod-create time might fire before /opt/ifix-ai-pod/ has compose/.env — re-invocation handles that race).

If any of these shapes fail on the first live run, the workflow's log will include the raw response bodies (search, create responses are both echoed to stdout), so the operator can adjust the jq paths or the filter JSON in one PR without re-running the full pipeline.

## Security Posture (STRIDE mitigations applied)

All 7 threats in the plan's `<threat_model>` are addressed in the committed artifacts:

| Threat ID | Severity | Mitigation — where applied in the code |
| --------- | -------- | --------------------------------------- |
| T-01-08-01 (spoofing — SSH host key on fresh pod) | medium | `accept-new` host-key policy in `ssh-exec` and `scp-upload` subcommands, known-hosts file scoped to `/tmp/known_hosts` (per-job) |
| T-01-08-02 (tampering — offer selection) | medium | Filter requires `verified=true` + `reliability>=0.98` + `inet_down>=200`; `dph_total<=max_price` cap; offer_id + price logged for audit in the Search step |
| T-01-08-03 (info disclosure — MinIO creds in pod env) | high | Creds are PUT into the instance env at create time (necessary for onstart's weight fetch). Destroy step wipes the instance. Rotation policy deferred to Phase 6. Documented as accepted for Phase 1. |
| T-01-08-04 (DoS — leaked pod) | high | Destroy step `if: always() && steps.create.outputs.instance_id != ''`; workflow `timeout-minutes` hard cap; concurrency `cancel-in-progress: false` prevents new runs from orphaning the current pod |
| T-01-08-05 (EoP — workflow_dispatch restricted) | medium | `permissions: contents: read` only; workflow_dispatch gated at the repo settings level by branch protection (operator setup — out of plan scope) |
| T-01-08-06 (tampering — smoke-report.json artifact) | low | Accepted. Artifact is emitted by the workflow itself, validated by plan 06's JSON Schema before write |
| T-01-08-07 (info disclosure — secrets in workflow logs) | high | GH auto-masks registered secrets; all 8 secrets injected via `${{ secrets.X }}` at env level; `/tmp/pod.env` created with `umask 077`; no secret value is ever echoed directly in a `run:` block |

## Decisions Made

See `key-decisions` in frontmatter for the full list. Headline decisions:

- Bash (not Python/Go) for the CLI wrapper — matches the plan's `<action>` block verbatim and minimizes GHA-runner deps.
- `cancel-in-progress: false` — this is the one divergence from Ifix canonical (`true` elsewhere); justified by the non-idempotent, billable nature of the workload.
- `if: always() && steps.create.outputs.instance_id != ''` on destroy — the `!= ''` guard prevents attempting destroy with an empty instance id (which the script's `: "${ID:?}"` would reject).
- Exit-code-bubble via captured step output + terminal `exit "$CODE"` step — ensures archive + summary + destroy all run before the workflow's final status is set.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 — Bug fix] Removed a no-op `sed -i` that would have confused future maintainers.**

- **Found during:** Task 2 (writing smoke.yml via Bash heredoc).
- **Issue:** The "Materialize env-file for create" step in the plan's `<action>` block has the heredoc body flush-left. To write the workflow via `cat <<'SMOKE_EOF'` inside Bash, I initially included a `sed -i 's/^          //' /tmp/pod.env` post-processor because I was worried the YAML common-indent stripping might not fire at shell-execution time. Verifying the actual parsed YAML (`python3 -c "import yaml; ..."`) showed that YAML literal-block scalars DO strip common indent before the shell sees the `run:` content — so the `sed` was a no-op.
- **Fix:** Removed the `sed -i` line and its preceding comment. The heredoc lines have 10 spaces of leading whitespace in the raw file, YAML strips those during parse, and the shell sees flush-left heredoc body + `EOF` marker. Verified by extracting `doc['jobs']['smoke']['steps'][<materialize>]['run']` with `yaml.safe_load` — the content is flush-left as expected.
- **Files modified:** `.github/workflows/smoke.yml` (two lines removed from the "Materialize env-file" step).
- **Verification:** `python3 -c "import yaml; yaml.safe_load(open('.github/workflows/smoke.yml'))"` exits 0. The materialize step's run content starts with `set -euo pipefail` at column 0 and the heredoc body at column 0, as expected.
- **Committed in:** `aff8e8a` (Task 2 commit — the edit happened before the commit, the final file has the clean version).

No other deviations. Task 1 executed verbatim from the plan's `<action>` block.

---

**Total deviations:** 1 auto-fixed (Rule 1 — code quality, removed a no-op that could mislead a future reader). No scope creep.

**Impact on plan:** None on behavior. The parsed YAML content of the Materialize step matches the plan's intent: `cat > /tmp/pod.env <<EOF` with body lines at column 0 (after YAML common-indent stripping) and flush-left `EOF` terminator.

## Issues Encountered

- **The plan's `<automated>` verify block contains an overly strict regex:** `! grep -qE "on:\s*$|push:|pull_request:" .github/workflows/smoke.yml`. The pattern `on:\s*$` matches the top-level `on:` keyword in any YAML workflow (because the next line is the trigger block body). This regex can NEVER be satisfied by a valid `on: { workflow_dispatch: ... }` workflow. Interpreted the intent (the plan's `<acceptance_criteria>` states "Trigger is EXCLUSIVELY `workflow_dispatch` (D-22)") and verified with a structural parse: `yaml.safe_load(...)` followed by `doc['on'].keys() == {'workflow_dispatch'}`. Confirmed the workflow has only `workflow_dispatch` as a trigger. The regex bug is documented here so plan 08's `<automated>` can be fixed in a future doc pass.
- **`actionlint` not installed on the executor worktree VPS.** Plan 07 accepted this; both plan 07 and plan 08 verify blocks wrap actionlint in `(command -v actionlint >/dev/null 2>&1 && ... || echo "not installed")`. `bash -n pod/scripts/vast-ai.sh` exits 0 and `python3 -c "import yaml; yaml.safe_load(...)"` exits 0 — these cover the non-actionlint correctness claims.
- **`shellcheck` not installed** — same situation as plan 05. Wrapped the check in an availability guard. `bash -n` still exits 0.

## Authentication Gates

None hit during execution. The runtime workflow requires `VAST_AI_API_KEY` + `MINIO_*` secrets at GitHub-repo level plus MinIO weight upload — BOTH are explicitly deferred (per the operator-setup acknowledgement in the executor prompt). The workflow FILE itself was created autonomously; its execution (by an operator manually triggering workflow_dispatch) will require those secrets and weights to exist first — documented in the `user_setup` frontmatter on the plan and re-surfaced in this SUMMARY's "Secrets Operator Must Configure" table.

## User Setup Required

**Runtime execution of this workflow requires operator setup BEFORE first trigger.** This is plan-level `autonomous: false` — the workflow file is committed autonomously, but runtime invocation cannot be automated.

1. **GitHub Secrets** (8 total — see "Secrets Operator Must Configure" table above). Set via Settings → Secrets and variables → Actions → New repository secret (or org-level for shared secrets).
2. **MinIO weight upload** (plan 09 documents this). Three objects must exist at the default keys (or custom keys passed via workflow inputs):
   - `s3://ifix-ai-weights/qwen3.5-27b-Q4_K_M/v1.0.0/model.gguf`
   - `s3://ifix-ai-weights/whisper-large-v3/v1.0.0/model.tar.gz`
   - `s3://ifix-ai-weights/bge-m3/v1.0.0/model.tar.gz`
   Each with a SHA-256 digest recorded in the matching `WEIGHTS_*_SHA256` secret.
3. **Vast.ai account** with credit balance (recommended: alert at $20). Each smoke run is budgeted at ~$0.25 (~30 min @ $0.35/h on a 4090); with guardrails at $0.40/h and 45 min, worst case is ~$0.30 per run.
4. **Branch protection** at the repo level (optional but recommended) — restrict who can trigger workflow_dispatch on this workflow.

Once those four prerequisites are in place, invocation is:
```bash
gh workflow run smoke.yml -f image_tag=develop-<sha>
```

## Threat Flags

None beyond those documented in the plan's `<threat_model>`. No new endpoints, trust boundaries, or secret-handling patterns introduced beyond the seven STRIDE entries already captured there.

## Next Phase Readiness

- **Plan 01-09 (phase closure / MinIO upload runbook):** can reference this workflow as the "green gate" that marks POD-07 delivered. The runbook's upload procedure produces the WEIGHTS_*_SHA256 values that this workflow's secrets need.
- **D-23 stable promotion path:** an operator running `git tag v1.0.0 && git push --tags` triggers `build-pod.yml` (plan 07), which publishes `ghcr.io/ifixtelecom/ifix-ai-pod:v1.0.0` + `:latest`. Then `gh workflow run smoke.yml -f image_tag=v1.0.0` runs this workflow; if exit 0, the tag IS production-ready. No additional gate is needed.
- **Phase 6 (auto-provisioning):** can `source pod/scripts/vast-ai.sh` or directly invoke it for emergency pod spin-up. The search + create + wait-running + destroy primitives are exactly what the auto-provisioner needs. No new wrapper required.
- **Phase 5 (saturation tuning):** each successful smoke run emits a `smoke-report-{image_tag}-{run_id}.json` artifact with 90-day retention. Plan 09 runbook documents how to archive successful reports into `.planning/phases/01-.../baseline/smoke-report-<sha>.json` for Phase 5 to consume (D-20).

## TDD Gate Compliance

Plan is `type: auto` with no TDD-flagged tasks. No RED/GREEN commit pair expected; both commits are `feat(01-08)`. Verified in `git log`: `0642545 feat(01-08)…vast-ai.sh`, `aff8e8a feat(01-08)…smoke.yml` — consistent with plan type.

## Self-Check

**File existence (absolute paths):**
- `/home/pedro/projetos/pedro/gpu-ifix/.claude/worktrees/agent-a639dd5a/pod/scripts/vast-ai.sh` — FOUND (195 lines, mode 755)
- `/home/pedro/projetos/pedro/gpu-ifix/.claude/worktrees/agent-a639dd5a/.github/workflows/smoke.yml` — FOUND (291 lines)
- `/home/pedro/projetos/pedro/gpu-ifix/.claude/worktrees/agent-a639dd5a/.planning/phases/01-gpu-pod-image-smoke-test/01-08-SUMMARY.md` — FOUND (this file)

**File mode:**
- `pod/scripts/vast-ai.sh` — `-rwxr-xr-x` (755) — FOUND EXECUTABLE

**Commit existence (worktree history):**
- `0642545` feat(01-08): add pod/scripts/vast-ai.sh — FOUND
- `aff8e8a` feat(01-08): add .github/workflows/smoke.yml — FOUND

**Plan-level verification block (PLAN.md `<verification>`):**
- `python3 -c "import yaml; yaml.safe_load(open('.github/workflows/smoke.yml'))"` — exit 0
- `bash -n pod/scripts/vast-ai.sh` — exit 0
- `command -v actionlint …` — branch taken: not installed, tolerable (same as plan 07)
- `command -v shellcheck …` — branch taken: not installed, tolerable (same as plan 05)

**Task-level `<automated>` verify (every grep/test):**

Task 1 (vast-ai.sh — 11 checks):
- `test -x pod/scripts/vast-ai.sh` — PASS
- `head -1 | grep -q "^#!/usr/bin/env bash$"` — PASS
- `grep -q "VAST_AI_API_KEY"` — PASS
- `grep -q "vast.ai/api/v0"` — PASS
- `grep -qE "^\s*search\)"` — PASS
- `grep -qE "^\s*create\)"` — PASS
- `grep -qE "^\s*destroy\)"` — PASS
- `grep -qE "^\s*wait-running\)"` — PASS
- `grep -q "RTX 4090"` — PASS
- `bash -n` — PASS
- shellcheck — tolerable (not installed)

Task 2 (smoke.yml — 16 checks):
- `test -f .github/workflows/smoke.yml` — PASS
- YAML parses — PASS
- `grep -q "^name: smoke$"` — PASS
- `grep -q "workflow_dispatch:"` — PASS
- `! grep -qE "on:\s*$|push:|pull_request:"` — **FAIL by literal grep** (plan regex bug; `on:\s*$` matches the `on:` keyword on any workflow). **Interpreted intent ("no push/pull_request triggers") verified structurally via `yaml.safe_load` — trigger keys == ['workflow_dispatch']. PASS.**
- `grep -q "cancel-in-progress: false"` — PASS
- `grep -q "VAST_AI_API_KEY"` — PASS
- `grep -q "MINIO_ENDPOINT"` — PASS
- `grep -q "smoke.py"` — PASS
- `grep -q "upload-artifact@v4"` — PASS
- `grep -q "pod/scripts/vast-ai.sh destroy"` — PASS
- `grep -q "if: always() && steps.create.outputs.instance_id"` — PASS
- `grep -q "max_price_per_hour"` — PASS
- `grep -q "D-19"` — PASS
- `grep -q "D-22"` — PASS
- `grep -q "D-23"` — PASS
- actionlint — tolerable (not installed)

**Structural checks (Python YAML AST):**
- trigger keys == ['workflow_dispatch'] — PASS
- 7 inputs present (image_tag, health_bridge_tag, weights_qwen_key, weights_whisper_key, weights_bge_m3_key, max_price_per_hour, smoke_timeout_minutes) — PASS
- 8 secrets wired at job env level — PASS
- Step order: Preflight → Search → Create → Wait → Upload → Run smoke-test → Archive → Publish summary → Destroy → Fail — PASS
- Destroy step has `if: always() && steps.create.outputs.instance_id != ''` — PASS
- Fail step is the LAST step — PASS
- Smoke step uses `set +e` + `exit_code=${CODE}` output — PASS
- No inline `vast.ai/api` curl in the workflow (all REST delegated to pod/scripts/vast-ai.sh) — PASS

**No unintended deletions:** `git diff --diff-filter=D HEAD~2 HEAD` empty across both task commits.

**No stubs detected:** grep for `TODO|FIXME|placeholder|coming soon|not available|xxx_stub` across vast-ai.sh and smoke.yml returned 0 matches.

## Self-Check: PASSED

---
*Phase: 01-gpu-pod-image-smoke-test*
*Plan: 08*
*Completed: 2026-04-18*
