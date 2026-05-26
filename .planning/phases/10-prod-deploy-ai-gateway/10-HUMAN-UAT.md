# Phase 10 — Live HUMAN-UAT: Prod-Deploy ai-gateway + Cascade Close

**Phase gate.** These 11 scenarios prove the ai-gateway production stack deploys end-to-end on the live `ai-gateway.converse-ai.app` + `ai-dashboard.converse-ai.app` hostnames, then cascade-close 4 prior phase deferrals (Phase 02 SC-5 step 7, Phase 03 SC-1, Phase 04 SC-1+SC-2+SC-4, Phase 05 SC-4/SC-5) that were blocked by the absence of a live prod deploy. **Requires real OpenRouter + OpenAI API spend (≤ $0.10 expected) → autonomous mode cannot satisfy it; an operator must run it. Zero Vast/GPU spend (D-08 reuses keys; no primary pod is provisioned in this UAT).**

**Engine:** Wave 0–3 artifacts (compose stack file + env contract + 5 deploy scripts + edge Traefik route YAML + RUNBOOK + release checklist) drive the operator from preflight → bootstrap-postgres → cut-release tag → first /health 200 over the new hostname → 11 live scenarios → 4 cascade-close commits.

| Header | Value |
|---|---|
| **Phase** | 10 — prod-deploy-ai-gateway |
| **Date** | 2026-05-MM (operator fills in) |
| **Status** | in_progress |
| **Operator** | __________ |
| **Expected wall time** | 2-3 h (incl. Pre-UAT preconditions + RUNBOOK Steps 1-7 + 11 scenarios + 4 cascade-close commits) |
| **Expected $ spend (target)** | ≤ $0.10 (OpenRouter + OpenAI combined; S8 vegeta burst dominates ~$0.025; S1/S4 chat ~$0.001 each; S3 whisper ~$0.006; S5 rate-limit burst ~$0.005) |
| **R2 hard abort criterion (cumulative)** | $2.00 — STOP and triage |
| **R2 hard abort criterion (per-call)** | $0.05 for any single S1/S4/S8 call — STOP and investigate |
| **Vast / GPU spend** | $0 (no primary pod provisioned this UAT; D-08 shared-key invariant means the existing dev pod keys are reused under the prod stack but no new Vast lifecycle is opened) |
| **OPENROUTER_HTTP_REFERER** | `ifix-uat-100-<OPERATOR_INITIALS>` (traceability on OpenRouter dashboard) |

**Pre-flight (operator):**
- Read `gateway/docs/RUNBOOK-DEPLOY.md` Steps 1-7 (first-time bring-up procedure executed BETWEEN Gates B and F below).
- Read `.planning/phases/10-prod-deploy-ai-gateway/10-05-RELEASE-CHECKLIST.md` (operator pre-cut checklist for `cut-release.sh`).
- Creds in `~/.claude/CLAUDE.md`: `CF_API_TOKEN` (Cloudflare DNS); ops-claude `~/.git-credentials` (GitHub PAT for git push); `vps-ifix-vm` + `n8n-ia-vm` SSH aliases (Hetzner Tailscale subnet route).
- SSH aliases: `vps-ifix-vm` (edge Traefik + dashboard host); `n8n-ia-vm` (gateway prod host with colocated Infinity embed tier-0).
- DigitalOcean Postgres console: `doadmin` connection string for `bootstrap-postgres.sh`.
- Sentry org `Ifix`: create new project `ifix-ai-gateway-prod` BEFORE Step 3 of RUNBOOK; copy DSN into `/opt/ai-gateway-prod/.env`.
- All Wave 0–3 plans (10-01..10-05) shipped to `develop` and the v1.0.0 tag will be cut during Pre-UAT Gate C (`cut-release.sh`).

**Budget guard:** OpenRouter Qwen completions ~$0.002 each at default size; Whisper ~$0.006/min audio; embed ~$0.00002/k tokens; S8 vegeta burst 150×$0.0002 ≈ $0.025. Keep prompts tiny (≤ 50 tokens completion). The CLEANUP section is MANDATORY — orphan FORCED_OPEN breakers prevent the prod stack from recovering.

> **REDACT BEFORE PASTING:** Replace all `Authorization: Bearer sk-...` strings with `Authorization: Bearer <REDACTED>`. Replace all real OpenAI/OpenRouter/Cloudflare tokens with `<REDACTED-OPENAI-KEY>` / `<REDACTED-OPENROUTER-KEY>` / `<REDACTED-CF-TOKEN>`. NEVER paste a real key into this markdown. UAT evidence is committed to git — leaked tokens require rotation.

---

## Pre-UAT Preconditions and Operator Safeguards (R2 — BLOCKING)

> **STOP. Do NOT proceed to S1 until ALL 6 gates below show PASS.** This is the FIRST checkpoint of Plan 10-06 — the executor pauses here. Each gate has its own sign-off line. If any gate FAILs, the operator addresses the underlying issue (apply env var, run migration, etc.) and re-runs that gate before continuing.

Between Gate B and Gate F, the operator executes `gateway/docs/RUNBOOK-DEPLOY.md` Steps 1-7 (first-time bring-up): create Sentry project; populate `/opt/ai-gateway-prod/.env` from `.env.prod.example`; `docker compose up -d` against `gateway/docker-compose.prod.yml`; smoke `/health` on the internal address; flip DNS via Gate E; first https probe via Gate F.

### Gate A — Egress IP + capacity (HARD GATE) — ~3 min

`scripts/deploy/preflight.sh` exits 0. The script asserts: egress IP is `162.55.92.154`; free RAM ≥ 4 GB; disk on `/` ≥ 20 GB free; `intra` Docker network attachable; internal Traefik service discoverable.

```bash
ssh n8n-ia-vm 'cd /opt/ai-gateway-prod 2>/dev/null || mkdir -p /opt/ai-gateway-prod && cd /opt/ai-gateway-prod; bash <(curl -fsSL file:///dev/stdin) < scripts/deploy/preflight.sh' \
  < scripts/deploy/preflight.sh
# OR (when scripts are rsync'd locally on n8n-ia-vm):
ssh n8n-ia-vm 'cd /opt/ai-gateway-prod && bash scripts/deploy/preflight.sh'
# Expected: script exits 0; trailing line "preflight OK"; prints egress IP + free -h + df -h / + intra-net status + internal-Traefik-discovery probe PASS
```

Operator pastes preflight output's last 20 lines into the Evidence box (Pitfall 6 — VM 101 capacity gate: if disk > 80% used OR free mem < 4 G, scale VM 101 vCPU/RAM/disk in Proxmox BEFORE proceeding; preflight will FAIL on those thresholds).

- **Sign-off Gate A:** [ ] PASS [ ] FAIL · **Evidence (preflight last 20 lines, egress IP, free -h, df -h /):** ______ · **Operator:** __________ · **Date:** __________

---

### Gate B — DO Postgres databases bootstrapped (HARD GATE) — ~5 min

Both `bd_ai_gateway_prod` and `bd_ai_dashboard_prod` must exist on the DO cluster `db-grupoifix-do-user-7520351-0.j.db.ondigitalocean.com:25060`. Pitfall 2 — schema collision dev↔prod: the script creates SEPARATE databases (not just schemas) so production goose migrations run against an empty DB. Pitfall 10 — DO trusted-source: confirm IP `162.55.92.154` is in the Trusted Sources allowlist BEFORE running the script.

```bash
# 1) doadmin DSN from DO console (Cluster → Connection Details → doadmin user)
export DO_ADMIN_DSN='postgresql://doadmin:<REDACTED>@db-grupoifix-do-user-7520351-0.j.db.ondigitalocean.com:25060/defaultdb?sslmode=require'

# 2) Run bootstrap (idempotent — re-run safely)
bash scripts/deploy/bootstrap-postgres.sh
# Expected: script exits 0; both databases listed; per-database "SELECT current_database()" probe returns the DB name.

# 3) Verify both DBs exist + are empty
psql "$DO_ADMIN_DSN" -tAc "SELECT datname FROM pg_database WHERE datname LIKE 'bd_ai_%_prod' ORDER BY datname;"
# Expected:
#   bd_ai_dashboard_prod
#   bd_ai_gateway_prod
psql "${DO_ADMIN_DSN/defaultdb/bd_ai_gateway_prod}" -tAc "SELECT current_database();"
# Expected: bd_ai_gateway_prod
psql "${DO_ADMIN_DSN/defaultdb/bd_ai_dashboard_prod}" -tAc "SELECT current_database();"
# Expected: bd_ai_dashboard_prod
```

- **Sign-off Gate B:** [ ] PASS [ ] FAIL · **Evidence (psql output for both DBs, bootstrap-postgres.sh last line):** ______ · **Operator:** __________ · **Date:** __________

---

### Gate C — GHA :v1.0.0 image green + cut-release tag (HARD GATE) — ~10 min

The `:v1.0.0` tag must be cut on the `main` branch (after a guarded develop→main merge via `cut-release.sh`) and the resulting GitHub Actions workflow must produce a green `ghcr.io/ifixtelecom/ifix-ai-gateway:v1.0.0` image. Sub-step: same for `ifix-ai-dashboard:v1.0.0`.

```bash
# 1) Pre-cut checklist (read once)
cat .planning/phases/10-prod-deploy-ai-gateway/10-05-RELEASE-CHECKLIST.md

# 2) Cut the release (guarded develop → main merge + v1.0.0 tag push)
bash scripts/deploy/cut-release.sh v1.0.0
# Expected: script confirms develop is green on CI, fast-forwards main, tags v1.0.0, pushes; exits 0.

# 3) Confirm Actions green for the v1.0.0 build (poll for ≤8 min)
gh run list --limit 3 --workflow build-gateway.yml --branch main --json conclusion,status,headBranch,event,createdAt
# Expected (top row): {"conclusion":"success","status":"completed","event":"push","headBranch":"main",...}
gh run list --limit 3 --workflow build-dashboard.yml --branch main --json conclusion,status,headBranch,event,createdAt
# Expected (top row): same shape, conclusion=success

# 4) Pull both images on the operator workstation (validates GHCR push completed)
docker pull ghcr.io/ifixtelecom/ifix-ai-gateway:v1.0.0
docker pull ghcr.io/ifixtelecom/ifix-ai-dashboard:v1.0.0
# Expected: both pulls complete with "Status: Downloaded newer image" or "Status: Image is up to date"
```

- **Sign-off Gate C:** [ ] PASS [ ] FAIL · **Evidence (cut-release.sh tail + gh run list top row for both workflows + docker pull final status):** ______ · **Operator:** __________ · **Date:** __________

---

### Gate D — Edge Traefik route loaded (HARD GATE) — ~5 min

Copy `artifacts/ai-gateway-prod.yml` to the edge Traefik dynamic-config directory on `vps-ifix-vm`; tail the edge Traefik logs for the `router added` line confirming the YAML parsed and the router activated. Pitfall 3 reminder: DNS is NOT yet flipped — the new routers point at an internal upstream (`http://ai-gateway-prod.intra:8080` / `http://ai-dashboard-prod.intra:3000`); external traffic still hits the dev stack until Gate E runs.

Pitfall 9 (T-10-06-09): YAML parse failure on Traefik file-provider hot-reload. Validate YAML BEFORE the scp; the edge Traefik watch picks up only well-formed YAML.

```bash
# 1) Local YAML parse pre-flight
python3 -c "import yaml; yaml.safe_load(open('.planning/phases/10-prod-deploy-ai-gateway/artifacts/ai-gateway-prod.yml'))"
# Expected: no output (success); on ParseError, fix YAML before proceeding

# 2) Copy to edge Traefik dynamic dir
scp .planning/phases/10-prod-deploy-ai-gateway/artifacts/ai-gateway-prod.yml \
  vps-ifix-vm:/home/pedro/projetos/pedro/infra/traefik-dynamic/

# 3) Tail edge Traefik logs for "router added" / "configuration loaded" on the new routers
ssh vps-ifix-vm 'docker logs -f --tail 0 $(docker ps -q -f name=traefik) 2>&1 \
  | grep -iE "ai-gateway-prod|ai-dashboard-prod|configuration loaded" | head -10'
# Expected (within 5s): "router added router=ai-gateway-prod@file" + "router added router=ai-dashboard-prod@file"
# (Ctrl-C after both lines appear)

# 4) Sanity-check the routers are visible via Traefik API (if exposed) OR via the dynamic file
ssh vps-ifix-vm 'cat /home/pedro/projetos/pedro/infra/traefik-dynamic/ai-gateway-prod.yml | head -20'
```

- **Sign-off Gate D:** [ ] PASS [ ] FAIL · **Evidence (python yaml.safe_load exit code + edge Traefik "router added" log lines for both routers):** ______ · **Operator:** __________ · **Date:** __________

---

### Gate E — DNS resolves (HARD GATE) — ~3 min

`scripts/deploy/cf-dns-create.sh` creates `A` records `ai-gateway.converse-ai.app` and `ai-dashboard.converse-ai.app` both pointing at the edge IP `162.55.92.154` with Cloudflare proxied=OFF, TTL 300. Pitfall 11 — CF token scope: token must have `Zone:Read` + `DNS:Edit` for `converse-ai.app` (zone ID `0e779b74b86957bdb628d646dbf33978`).

```bash
# 1) Apply records (idempotent — script checks for existing records first)
export CF_API_TOKEN='<REDACTED-CF-TOKEN>'   # from ~/.claude/CLAUDE.md Cloudflare API Token block
bash scripts/deploy/cf-dns-create.sh
# Expected: script exits 0; final stanza "Created/Updated record ai-gateway.converse-ai.app → 162.55.92.154" + same for dashboard

# 2) Resolve from public DNS (1.1.1.1) — wait up to 60s for TTL propagation
dig +short ai-gateway.converse-ai.app @1.1.1.1
# Expected: 162.55.92.154
dig +short ai-dashboard.converse-ai.app @1.1.1.1
# Expected: 162.55.92.154

# 3) Cloudflare dashboard cross-check (optional but recommended)
# Visit https://dash.cloudflare.com → converse-ai.app → DNS → confirm both records present, proxied=OFF, TTL 300
```

- **Sign-off Gate E:** [ ] PASS [ ] FAIL · **Evidence (dig output for both hostnames + cf-dns-create.sh last line):** ______ · **Operator:** __________ · **Date:** __________

---

### Gate F — TLS cert issued (HARD GATE) — ~5 min

First HTTPS probe over the new hostname forces edge Traefik to obtain a Let's Encrypt certificate via ACME (HTTP-01 challenge). Pitfall 4: ACME can take 30-90 seconds; if Gate F is hit BEFORE the cert is issued, the curl returns `SSL_ERROR_BAD_CERT_DOMAIN` or `503`. Pitfall 5: stale-cert mid-UAT — assert BOTH `HTTP/2 200` AND `acme.json` contains both hostnames before signing.

```bash
# 1) First HTTPS probe — gateway
curl -sS -I https://ai-gateway.converse-ai.app/health | head -1
# Expected: HTTP/2 200    (after ACME completes; may take 30-90s on first probe — retry 3× with 20s sleep if needed)

# 2) First HTTPS probe — dashboard
curl -sS -I https://ai-dashboard.converse-ai.app/ | head -1
# Expected: HTTP/2 200 (dashboard root) or HTTP/2 302 (redirect to login) — both acceptable

# 3) Verify acme.json contains both hostnames (Pitfall 5 — stale-cert mid-UAT)
ssh vps-ifix-vm "docker exec infra-traefik-1 cat /letsencrypt/acme.json \
  | jq '.letsencrypt.Certificates[].domain.main' -r" \
  | grep -E 'ai-(gateway|dashboard)\.converse-ai\.app'
# Expected: BOTH "ai-gateway.converse-ai.app" AND "ai-dashboard.converse-ai.app" present
```

- **Sign-off Gate F:** [ ] PASS [ ] FAIL · **Evidence (curl -I HTTP/2 line for both hostnames + acme.json jq output showing both):** ______ · **Operator:** __________ · **Date:** __________

---

> **GATE SUMMARY — operator types "all gates passed" to acknowledge before proceeding to S1.**
>
> **Master sign-off line:** Gate A [ ] · Gate B [ ] · Gate C [ ] · Gate D [ ] · Gate E [ ] · Gate F [ ] — **ALL PASS:** [ ] **Operator:** ______ **Date:** ______

---

## Scenario S1 — Chat E2E under prod hostname (~3 min, ~$0.001)

**Goal:** Send `POST https://ai-gateway.converse-ai.app/v1/chat/completions {"model":"qwen"}`. Receive HTTP 200 with real LLM completion routed through OpenRouter tier-1 (primary pod is asleep this UAT; D-08 shared-key invariant means OpenRouter answers).

**Cascades:** Phase 02 SC-5 step 7 — original 02-UAT-2026-05-23.md step 7 returned 503 because primary FSM=asleep + OpenRouter env vars absent. With Phase 06.9 + Phase 10 prod stack, this MUST return HTTP 200.

### Setup

```bash
# Mint a fresh tenant key for converseai during RUNBOOK Step 7 — or reuse the key from there
# (provision-tenants.sh --mint-keys provisions all 6 tenants and prints each Bearer key)
export CONVERSEAI_KEY='ifix_sk_<REDACTED>'   # from provision-tenants.sh output
```

### Probe

```bash
curl -sS -X POST https://ai-gateway.converse-ai.app/v1/chat/completions \
  -H "Authorization: Bearer ${CONVERSEAI_KEY}" \
  -H "Content-Type: application/json" \
  -d '{"model":"qwen","messages":[{"role":"user","content":"PING"}],"max_tokens":10}' \
  | jq '{model:.model, provider:(.provider // "n/a"), content:.choices[0].message.content, finish:.choices[0].finish_reason, rid:.id, usage:.usage}'
# Expected: HTTP 200 + non-empty content + model field shows a slug ending in (e.g.) "deepseek/deepseek-v4-flash-20260423" (OpenRouter provider chosen at request time, e.g. Novita/AtlasCloud/SiliconFlow rotating)
```

### Expected

- HTTP status: 200
- `choices[0].message.content`: non-empty string
- `model`: a real OpenRouter slug (not the literal `qwen`) — confirms director rewrite worked end-to-end
- `id`: starts with `gen-...` (OpenRouter request ID) — also returned as `X-Request-ID` header
- `usage.total_tokens`: > 0 (≤ 50 — keep prompts tiny per budget guard)

### Common failure modes

- **HTTP 503 `no fallback configured`:** primary FSM not asleep AND breaker not OPEN → manually `gatewayctl breaker force-open --upstream local-llm --reason 10_uat_s1` first; OR confirm `.env` has `UPSTREAM_LLM_OPENROUTER_AUTH_BEARER` populated.
- **HTTP 404 + HTML body:** Phase 06.9 regression — model-rewrite not active. Inspect `gatewayctl model-alias list` for the `(qwen, openrouter-chat, ...)` row.
- **HTTP 401/402 from OpenRouter:** token rotated mid-UAT — re-confirm `UPSTREAM_LLM_OPENROUTER_AUTH_BEARER` in `/opt/ai-gateway-prod/.env`.

### Evidence box (REDACTED)

```
HTTP status: ____
X-Request-ID: ____
Response (redacted):
{"model":"____","provider":"____","content":"____","finish":"stop","rid":"gen-<REDACTED>","usage":{"total_tokens":____}}
Cost estimate: $____ (total_tokens × OpenRouter pricing for chosen provider)
```

- **Sign-off S1:** [ ] PASS [ ] FAIL · **Operator:** __________ · **Date:** __________ · **Evidence (REDACTED):** ______

---

## Scenario S2 — Tier-0 embed via colocated Infinity (~1 min, ~$0.000)

**Goal:** Send `POST https://ai-gateway.converse-ai.app/v1/embeddings {"model":"bge-m3","input":[...]}`. Receive HTTP 200 with `data[*].embedding` arrays of length 1024 from the colocated Infinity tier-0 on `n8n-ia-vm` (NOT OpenAI tier-1). This verifies `UPSTREAM_EMBED_URL=http://10.10.10.20:7997` is wired correctly in the prod compose file.

**Cascades:** none (verifies wiring only — Phase 09 already validated embed under prod-shaped routing). Important sanity check that the colocated Infinity path actually serves under the prod hostname.

### Setup

No special setup — Infinity has been live on `n8n-ia-vm:7997` since Phase 09. The prod gateway's `.env` should set `UPSTREAM_EMBED_URL=http://10.10.10.20:7997` so the dispatcher picks `local-embed` over `openai-embed`.

### Probe

```bash
curl -sS -X POST https://ai-gateway.converse-ai.app/v1/embeddings \
  -H "Authorization: Bearer ${CONVERSEAI_KEY}" \
  -H "Content-Type: application/json" \
  -d '{"model":"bge-m3","input":["ifix telecom suporte","segunda via boleto"]}' \
  | jq '{model:.model, n:(.data|length), dim0:(.data[0].embedding|length), dim1:(.data[1].embedding|length)}'
# Expected: {"model":"bge-m3","n":2,"dim0":1024,"dim1":1024}
```

### Expected

- HTTP status: 200
- `model`: `bge-m3` (NOT `text-embedding-3-small` — confirms LOCAL tier-0 served, not OpenAI tier-1)
- `n`: 2
- `dim0` + `dim1`: both 1024 (BGE-M3 parity invariant)

### Common failure modes

- **`model == "text-embedding-3-small"`:** Infinity is unreachable from the prod gateway container → `local-embed` breaker tripped → tier-1 took over. Inspect: `ssh n8n-ia-vm 'curl -sS http://10.10.10.20:7997/health'` from the gateway container's network namespace.
- **`dim0 == 1536`:** dispatch went to OpenAI WITHOUT the `dimensions=1024` parameter — Phase 06.9 regression on the embed director.

### Evidence box

```
HTTP status: ____
jq summary: {"model":"____","n":____,"dim0":____,"dim1":____}
```

- **Sign-off S2:** [ ] PASS [ ] FAIL · **Operator:** __________ · **Date:** __________ · **Evidence:** ______

---

## Scenario S3 — Whisper STT tier-1 (~2 min, ~$0.006)

**Goal:** Force-open `local-stt` breaker. Send `POST https://ai-gateway.converse-ai.app/v1/audio/transcriptions` with a small WAV + `model=whisper`. Receive HTTP 200 with `{"text":"..."}` from OpenAI's `whisper-1` (model rewritten in multipart form data without corrupting audio bytes).

**Cascades:** none (verifies wiring only — Phase 06.9 S2 validated under dev).

### Setup

```bash
# Copy probe.wav fixture to operator workstation (already in repo at gateway/internal/upstreams/testdata/)
cp gateway/internal/upstreams/testdata/probe.wav /tmp/probe.wav
file /tmp/probe.wav
# Expected: RIFF (little-endian) data, WAVE audio, ...

# Force-open the local-stt breaker
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl breaker force-open \
  --upstream local-stt --ttl 5m --reason "10_uat_s3"'
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl breaker list' | grep local-stt
# Expected: local-stt row with state=FORCED_OPEN + TTL countdown
```

### Probe

```bash
curl -sS -X POST https://ai-gateway.converse-ai.app/v1/audio/transcriptions \
  -H "Authorization: Bearer ${CONVERSEAI_KEY}" \
  -F 'file=@/tmp/probe.wav' \
  -F 'model=whisper' \
  | jq
# Expected: {"text":"..."}    (whisper transcription of probe.wav)
```

### Expected

- HTTP status: 200
- Body shape `{"text":"<transcription>"}` (probe.wav contents)

### Common failure modes

- **HTTP 400 from OpenAI:** `model=whisper` not rewritten to `whisper-1` on multipart → Phase 06.9 regression on whisper director.
- **HTTP 401/404 from OpenAI:** OpenAI key in `.env` rotated/invalid — refresh `UPSTREAM_STT_OPENAI_AUTH_BEARER`.

### Cleanup

```bash
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl breaker force-close --upstream local-stt'
```

### Evidence box

```
HTTP status: ____
Response: {"text":"____"}
```

- **Sign-off S3:** [ ] PASS [ ] FAIL · **Operator:** __________ · **Date:** __________ · **Evidence:** ______

---

## Scenario S4 — Force-open primary breaker → tier-1 chat 200 (~3 min, ~$0.001)

**Goal:** With `local-llm` breaker FORCED_OPEN, send 2 consecutive chat requests. Both must return HTTP 200, and audit_log MUST show `upstream=openrouter-chat` for both request_ids.

**Cascades:** Phase 03 SC-1 — original 03-VERIFICATION.md SC-1 was marked PASS via Phase 06.9 S4 against the DEV URL; this re-verifies the same chain under the PROD hostname.

### Setup

```bash
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl breaker force-open \
  --upstream local-llm --ttl 5m --reason "10_uat_s4"'
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl breaker list' | grep local-llm
# Expected: local-llm row state=FORCED_OPEN
```

### Probe

```bash
RIDS=()
for i in 1 2; do
  RESP=$(curl -sS -i -X POST https://ai-gateway.converse-ai.app/v1/chat/completions \
    -H "Authorization: Bearer ${CONVERSEAI_KEY}" \
    -H "Content-Type: application/json" \
    -d '{"model":"qwen","messages":[{"role":"user","content":"S4 probe '"$i"'"}],"max_tokens":5}')
  RID=$(echo "$RESP" | grep -i '^x-request-id:' | awk '{print $2}' | tr -d '\r')
  CODE=$(echo "$RESP" | grep -E '^HTTP/' | tail -1 | awk '{print $2}')
  echo "Probe $i: HTTP $CODE  X-Request-ID=$RID"
  RIDS+=("$RID")
done
echo "Request IDs: ${RIDS[*]}"

# Confirm audit_log shows upstream=openrouter-chat for both
psql "$AI_GATEWAY_PG_DSN" -tAc \
  "SELECT request_id, upstream FROM ai_gateway.audit_log WHERE request_id IN ('${RIDS[0]}','${RIDS[1]}')"
# Expected: 2 rows, both upstream=openrouter-chat
```

### Expected

- Both probes: HTTP 200
- psql query returns 2 rows, both with `upstream=openrouter-chat`

### Cleanup

```bash
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl breaker force-close --upstream local-llm'
```

### Evidence box

```
Probe 1: HTTP ____  X-Request-ID=____
Probe 2: HTTP ____  X-Request-ID=____
audit_log rows:
  ____|openrouter-chat
  ____|openrouter-chat
```

- **Sign-off S4:** [ ] PASS [ ] FAIL · **Operator:** __________ · **Date:** __________ · **Evidence:** ______

---

## Scenario S5 — Rate-limit burst (~3 min, ~$0.005)

**Goal:** With a tenant configured `rps=5`, send 10 parallel chat requests. Observe 3-5 HTTP 429 responses with `Retry-After: 1` + `X-RateLimit-Limit-Requests: 5` + decrementing `X-RateLimit-Remaining-Requests` chain; Prometheus `gateway_rate_limit_rejected_total{tenant="uat10-test",window="rps"}` increments accordingly.

**Cascades:** Phase 04 SC-1 — original 04-VERIFICATION.md SC-1 was LIVE PASS on 2026-05-23 against the DEV URL; this re-verifies under the PROD hostname.

### Setup

```bash
# 1) Create test tenant (if not already done via provision-tenants.sh)
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl tenant create --name "uat10-test" --slug "uat10-test"'
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl tenant set-quota uat10-test --rps 5'
UAT10_KEY=$(ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl key create --tenant uat10-test --data-class normal' | grep -oE 'ifix_sk_[a-z0-9]+')
echo "UAT10_KEY=$UAT10_KEY (record this — printed once)"
```

### Probe

```bash
# 10 parallel curls; collect HTTP codes + Retry-After + X-RateLimit-* headers
for i in $(seq 1 10); do
  (curl -sS -o /dev/null -i -X POST https://ai-gateway.converse-ai.app/v1/chat/completions \
    -H "Authorization: Bearer ${UAT10_KEY}" \
    -H "Content-Type: application/json" \
    -d '{"model":"qwen","messages":[{"role":"user","content":"burst probe '"$i"'"}],"max_tokens":3}' \
    | grep -iE '^(HTTP/|x-ratelimit-|retry-after)' ) &
done | sort -u
wait

# Verify Prometheus counter increased
curl -sS https://ai-gateway.converse-ai.app/metrics \
  | grep 'gateway_rate_limit_rejected_total{tenant="uat10-test"'
# Expected: a counter ≥ 3 with window="rps"
```

### Expected

- 5 HTTP 200 + 3-5 HTTP 429
- 429 responses carry `Retry-After: 1` + `X-RateLimit-Limit-Requests: 5` + `X-RateLimit-Remaining-Requests` decreasing 4→3→2→1→0
- Prometheus `gateway_rate_limit_rejected_total{tenant="uat10-test",window="rps"}` increment matches the 429 count

### Evidence box

```
HTTP code counts: 200=____ 429=____
Sample 429 headers: Retry-After=____ X-RateLimit-Limit-Requests=____ X-RateLimit-Remaining-Requests=____,____,____,____
Prometheus counter: gateway_rate_limit_rejected_total{tenant="uat10-test",window="rps"} = ____
```

- **Sign-off S5:** [ ] PASS [ ] FAIL · **Operator:** __________ · **Date:** __________ · **Evidence:** ______

---

## Scenario S6 — billing_events row inserted (~1 min, $0)

**Goal:** Query `ai_gateway.billing_events` and confirm at least one row per S1/S2/S3/S4/S5 chat request landed within the last 15 minutes.

**Cascades:** Phase 04 SC-2 — original SC-2 deferred billing_events row inspection (MCP postgres-grupo-ifix rejected). This is the first live validation under the prod DB.

### Setup

No setup — S1/S2/S3/S4/S5 already generated billing rows.

### Probe

```bash
psql "$AI_GATEWAY_PG_DSN" -tAc \
  "SELECT COUNT(*), MAX(created_at) FROM ai_gateway.billing_events WHERE created_at > now() - interval '15 minutes'"
# Expected: count > 0; MAX(created_at) is within the last 15 min

# Inspect a sample row (last one)
psql "$AI_GATEWAY_PG_DSN" -tAc \
  "SELECT request_id, tenant_id, upstream, tokens_input, tokens_output, cost_usd, created_at \
   FROM ai_gateway.billing_events ORDER BY created_at DESC LIMIT 1"
# Expected: 1 row with a real upstream value (local-llm / openrouter-chat / local-embed / openai-whisper) + non-zero tokens
```

### Expected

- COUNT(*) > 0
- MAX(created_at) within last 15 min
- Sample row has non-zero `tokens_input` + non-zero `cost_usd`

### Evidence box

```
Count: ____  Latest: ____
Sample: rid=____ tenant=____ upstream=____ tokens_in=____ tokens_out=____ cost=$____
```

- **Sign-off S6:** [ ] PASS [ ] FAIL · **Operator:** __________ · **Date:** __________ · **Evidence:** ______

---

## Scenario S7 — Peak schedule routing (~3 min, ~$0.001)

**Goal:** Configure tenant `uat10-test` in peak mode with off-hours window covering current time; send a chat request; observe gateway logs show `module=SCHEDULE upstream=openrouter-chat decision=off_hours_external`; response served by OpenRouter tier-1.

**Cascades:** Phase 04 SC-4 — original SC-4 LIVE PASS on 2026-05-23 against DEV URL; this re-verifies under PROD hostname.

### Setup

```bash
# Pick a 1-hour window covering NOW (operator computes — example assumes 22:00-23:00 BRT)
NOW_HOUR_BRT=$(TZ=America/Sao_Paulo date +%H)
NEXT_HOUR_BRT=$((10#$NOW_HOUR_BRT + 1))
WINDOW="${NOW_HOUR_BRT}-${NEXT_HOUR_BRT}"   # e.g. "22-23"

ssh n8n-ia-vm "docker exec ifix-ai-gateway /gatewayctl tenant set-mode uat10-test peak --window ${WINDOW}"
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl tenant show uat10-test' | grep -E 'mode|window'
# Expected: mode=peak window=$WINDOW
```

### Probe

```bash
# Capture request_id from response
RID=$(curl -sS -i -X POST https://ai-gateway.converse-ai.app/v1/chat/completions \
  -H "Authorization: Bearer ${UAT10_KEY}" \
  -H "Content-Type: application/json" \
  -d '{"model":"qwen","messages":[{"role":"user","content":"S7 schedule"}],"max_tokens":5}' \
  | grep -i '^x-request-id:' | awk '{print $2}' | tr -d '\r')
echo "RID=$RID"

# Find SCHEDULE decision log for this request_id (tail logs and grep)
ssh n8n-ia-vm "docker logs ai-gateway-prod --tail 500 2>&1 \
  | grep \"$RID\" \
  | grep -iE 'module=SCHEDULE|module=DISPATCHER|decision=off_hours'"
# Expected: at least one line with module=SCHEDULE + upstream=openrouter-chat + decision=off_hours_external

# Also check Prometheus counter
curl -sS https://ai-gateway.converse-ai.app/metrics \
  | grep 'gateway_schedule_routing_total{decision="off_hours_external"'
# Expected: counter ≥ 1
```

### Expected

- HTTP 200 + LLM completion from OpenRouter
- Gateway log shows `module=SCHEDULE decision=off_hours_external upstream=openrouter-chat` for the RID
- Prometheus `gateway_schedule_routing_total{decision="off_hours_external"}` ≥ 1

### Cleanup

```bash
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl tenant set-mode uat10-test 24/7'
```

### Evidence box

```
RID: ____
Log line: module=SCHEDULE upstream=____ decision=____
Prometheus counter: gateway_schedule_routing_total{decision="off_hours_external"} = ____
```

- **Sign-off S7:** [ ] PASS [ ] FAIL · **Operator:** __________ · **Date:** __________ · **Evidence:** ______

---

## Scenario S8 — vegeta burst 5 RPS × 30s (~1 min, ~$0.025)

**Goal:** With `local-llm` breaker FORCED_OPEN, run `vegeta attack -duration=30s -rate=5` → 150 requests, all served by OpenRouter tier-1. Expected ≥ 99% HTTP 200 (149/150 acceptable per 06.9 S6 precedent — 1 vegeta-client-side timeout tolerated).

**Cascades:** Phase 05 SC-4/SC-5 — Phase 06.9 S6 closed SC-1 against DEV; this verifies SC-4 (anti-starvation under shed) + SC-5 (DCGM scrape evidence) implicitly under PROD by exercising the same load shape against the prod hostname.

### Setup

```bash
# 1) Force-open the breaker (same as S4)
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl breaker force-open \
  --upstream local-llm --ttl 5m --reason "10_uat_s8"'

# 2) Prepare vegeta target + body
cat > /tmp/body.json <<EOF
{"model":"qwen","messages":[{"role":"user","content":"PING"}],"max_tokens":5}
EOF

cat > /tmp/targets.txt <<EOF
POST https://ai-gateway.converse-ai.app/v1/chat/completions
Authorization: Bearer ${CONVERSEAI_KEY}
Content-Type: application/json
@/tmp/body.json
EOF

# 3) Optional — confirm vegeta installed
vegeta -version
# If missing: go install github.com/tsenart/vegeta/v12@latest
```

### Probe

```bash
vegeta attack -duration=30s -rate=5 -targets=/tmp/targets.txt \
  | vegeta report -type='hist[0,500ms,1s,2s,5s]'
# Expected:
#   Requests      [total, rate, throughput]  150, 5.03, ~4.95
#   Success       [ratio]                    ≥ 99% (149/150 or 150/150)
#   Status Codes  [code:count]               200:149 + 0:1  (client-side timeout)  OR  200:150
#   Bucket histogram showing most calls < 2s
```

### Expected

- 150 total requests
- ≥ 99% success ratio (149/150 with 1 vegeta-client-side timeout acceptable per 06.9 S6 precedent — 150/150 ideal)
- ALL non-timeout responses HTTP 200
- Status Codes show 200:149 or 200:150; 0 upstream 502s

### Cleanup

```bash
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl breaker force-close --upstream local-llm'
```

### Evidence box

```
Requests total: ____
Success ratio: ____% (target ≥99%)
Status codes: 200=____ other=____
Latency P95: ____ms  P99: ____ms
```

- **Sign-off S8:** [ ] PASS [ ] FAIL · **Operator:** __________ · **Date:** __________ · **Evidence:** ______

---

## Cascade-Close Commits (executed AFTER S1-S8 pass)

<!-- Task 1B will append the 4 commit stanzas (Phase 02 / Phase 03 / Phase 04 / Phase 05) here. -->

---

## Cleanup (MANDATORY)

> **Run ALL of this regardless of pass/fail.** Orphan FORCED_OPEN breakers prevent the prod stack from recovering normally.

```bash
# 1) Restore EVERY breaker forced open during S3 / S4 / S8 (and any scenario that used force-open)
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl breaker force-close --upstream local-llm   || true'
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl breaker force-close --upstream local-stt   || true'
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl breaker force-close --upstream local-embed || true'

# 2) Verify NO FORCED_OPEN rows remain
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl breaker list | grep -i forced'
# Expected: NO OUTPUT (empty)

# 3) Restore uat10-test tenant to 24/7 mode (S7 cleanup re-assert)
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl tenant set-mode uat10-test 24/7 || true'

# 4) Confirm primary FSM state returned to pre-UAT value
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl primary state'
# Expected: same state as before S1 (typically asleep — no primary pod provisioned this UAT)

# 5) Confirm docker compose ps is healthy
ssh n8n-ia-vm 'docker compose -f /opt/ai-gateway-prod/docker-compose.yml ps'

# 6) Final spend check
#    OpenRouter: https://openrouter.ai/activity — filter by referer "ifix-uat-100-<your-initials>"
#    OpenAI:     https://platform.openai.com/usage — same day usage breakdown
#    Cloudflare: no spend (DNS API calls free)
```

**Verification (all must hold):**
- `gatewayctl breaker list | grep -i forced` returns empty.
- `gatewayctl primary state` returns the pre-UAT value (no inadvertent primary FSM mutation).
- `docker compose ps` healthy + no orphan containers.
- `gatewayctl tenant show uat10-test` returns `mode=24/7`.

- **Record:** breakers cleared: yes/no · primary state unchanged: yes/no · uat10-test back to 24/7: yes/no · **total OpenRouter spend = $______** · **total OpenAI spend = $______** · **combined total = $______**
- **Sign-off CLEANUP:** [ ] PASS [ ] FAIL · **Operator:** __________ · **Date:** __________
