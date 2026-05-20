# Embed service — 24/7 CPU (Infinity / multilingual-e5-large)

Phase 06.7 Plan 06 (D-03). Stands up the embedding service **off the GPU
primary pod** so RAG survives the pod sleeping off-peak. The gateway resolves
embed to this host as a **static tier-0 upstream** (no FSM / no dynamic
override — D-03), via the seed row `local-embed` in
`gateway/db/migrations/0008_seed_upstreams.sql`
(`embed, tier 0, url_env=UPSTREAM_EMBED_URL`).

This deploys the **serving service + static wiring ONLY**.

> **Phase 8 deferral (D-04):** the converseai corpus re-embed, the
> OpenAI → gateway embed cutover, and the pgvector dimension migration
> (1536 → 1024) are **Phase 8 (INT-01) — NOT in scope here.** Do not migrate
> any application's embedding column or re-embed any corpus as part of this
> deploy. This plan only makes the gateway *able* to serve embeddings 24/7.

---

## Target host (GATE 4)

**Co-located on `n8n-ia-vm`** — `10.10.10.20`, VMID 101. **No dedicated VM.**

GATE 4 (`06.7-WAVE0-GATES.md`) locked this after measuring n8n-ia-vm capacity
on 2026-05-20: 11 GiB RAM available + 28 GB disk free, 6 cores. The Infinity +
`intfloat/multilingual-e5-large` int8-ONNX footprint is ~600 MB model +
1–2 GB runtime (≈2–3 GB RAM, ≈2–3 GB disk), which fits alongside the live
n8n / rabbitmq / redis / postgres / traefik / portainer load. The container is
capped at **4 GB** (`mem_limit: 4g`) so it can never starve the shared host
(threat T-06.7-11).

SSH: `ssh n8n-ia-vm` (alias in `~/.ssh/config` on ops-claude).

---

## Model & engine

- **Model (LOCKED, GATE 4):** `intfloat/multilingual-e5-large` — **1024-dim**.
- **Engine:** Infinity, image `michaelf34/infinity:0.0.77` **SHA-pinned** to
  `sha256:11e8b3921b9f1a58965afaad4a844c435c9807cbc82c51e47cb147b7d977fc88`
  (the digest already vetted in Phase 6.6 — `06.6-WAVE0-GATES.md` Decision 1,
  threat T-06.7-SC).
- **CPU + ONNX int8** (`--engine optimum --device cpu --dtype int8`). **No GPU
  device reservation** — this is CPU 24/7 (D-03). `restart: unless-stopped`
  keeps it alive across reboots/crashes.

Same engine + flags as the old primary-pod child
(`pod/primary/supervisord.conf [program:infinity]`, removed per D-03) — only
the host (CPU n8n-ia-vm vs GPU pod) and the model
(`multilingual-e5-large` vs the pod's `bge-m3`) differ.

---

## Served endpoint

Infinity 0.0.77 natively serves **`POST /embeddings`** (NOT `/v1/embeddings` —
verified in Phase 6.6 UAT 14 from its OpenAPI). We run it with
**`--url-prefix /v1`**, so on this deploy it answers:

- `POST http://<embed-host>:7997/v1/embeddings` → `{ data: [{ embedding: [1024 floats] }] }`
- `GET  http://<embed-host>:7997/v1/health` → `200`

---

## Path mismatch resolution (REVIEWS consensus action #5)

**The problem.** The gateway's client-facing route is `/v1/embeddings`. The
gateway embed proxy preserves the request path **1:1** — see
`gateway/internal/proxy/director.go` `BuildDirector`, which sets only
`r.URL.Scheme`/`r.URL.Host` and explicitly **leaves `r.URL.Path` unchanged**
("pod routes mirror the gateway's /v1/... paths 1:1"). So a request to the
gateway `/v1/embeddings` is forwarded to the upstream as **`/v1/embeddings`**.
A bare Infinity (serving `/embeddings`) would therefore return **404** on the
forwarded path even though a direct `curl .../embeddings` works.

**Chosen approach — configure Infinity to serve `/v1` (no gateway code change).**
We pass `--url-prefix /v1` to `infinity_emb v2`. Infinity then serves
`POST /v1/embeddings` and `GET /v1/health`, which is exactly what the
1:1-preserving director forwards. This keeps **Plan 06 deploy-only** — no edit
to `gateway/internal/proxy/*` or to the seed row is required; `UPSTREAM_EMBED_URL`
just points at the host root and the existing director does the rest.

**Alternative (NOT chosen) — gateway-side rewrite.** Add a
`/v1/embeddings → /embeddings` path rewrite to the embed upstream's director
(e.g. a dedicated director that strips the `/v1` prefix for the embed proxy)
and run Infinity bare on `/embeddings`. Rejected for Plan 06 because it
requires a Go change to `gateway/internal/proxy`, which is out of this plan's
file scope (`files_modified` is only the two `deploy/embed/*` files). If a
future plan needs Infinity bare on `/embeddings` for some other reason, that
rewrite is the path — but it belongs to a gateway plan, not this deploy.

> **Verify the chosen approach at deploy (Task 2):** the human-action gate
> MUST confirm the gateway `/v1/embeddings` **client route** returns a
> 1024-dim vector end-to-end — NOT only a direct `/v1/embeddings` curl against
> the service. If `--url-prefix /v1` is not honored by 0.0.77 in practice,
> fall back to the gateway-side rewrite (alternative above) in a follow-up
> gateway plan.

---

## Deploy via Portainer

Standard Ifix flow (`CLAUDE.md` Dev Environment): Portainer stack on the
target host's Portainer.

1. **Portainer → Stacks → Add stack** on `n8n-ia-vm`'s Portainer.
   - Name: `ai-gateway-embed`.
   - Build method: **Repository** (this repo) pointing at
     `deploy/embed/docker-compose.yml` on the `develop` branch, OR paste the
     compose contents into the **Web editor**.
2. Deploy the stack. First boot downloads `multilingual-e5-large` (~600 MB)
   into the `ai-gateway-embed-model-cache` named volume; `start_period: 180s`
   on the healthcheck covers that one-time download. Subsequent restarts reuse
   the cache (no re-download).
3. Confirm the container is **healthy** (Portainer container state) and the
   restart policy is `unless-stopped` (it stays 24/7).

Dev-cycle alternative (no Portainer):

```bash
ssh n8n-ia-vm 'cd /opt/ai-gateway-embed && docker compose -f docker-compose.yml up -d'
```

---

## Wire the gateway (UPSTREAM_EMBED_URL)

Set this **exact value** in the **`ai-gateway-dev`** Portainer stack env
(the gateway stack on `vps-ifix-vm`), then redeploy/recreate the gateway
container so it picks up the env:

```
UPSTREAM_EMBED_URL=http://10.10.10.20:7997
```

- Host = `n8n-ia-vm` NAT IP `10.10.10.20` (GATE 4 host), port `7997`.
- **Point at the host root** (`http://10.10.10.20:7997`), with **no `/v1`
  path suffix** — the gateway appends the client route `/v1/embeddings`
  itself (1:1 director), and Infinity serves it because of `--url-prefix /v1`.
- This is a **pod IP? NO.** It MUST be the static embed host, not a Vast pod
  IP — the whole point of D-03 is decoupling embed from the GPU pod schedule.

The seed row `local-embed` (`embed, tier 0, UPSTREAM_EMBED_URL`) already exists
(migration 0008), so no DB change is needed — just the env value.

---

## Verification (Task 2 — human-action gate, runs on the live host)

1. **Direct (service answers /v1/embeddings):**
   ```bash
   curl -s http://10.10.10.20:7997/v1/embeddings \
     -H 'Content-Type: application/json' \
     -d '{"input":"ping","model":"multilingual-e5-large"}' | jq '.data[0].embedding | length'
   # expect: 1024
   ```
2. **Gateway client route (THE REVIEWS #5 check — exercises the path 1:1 +
   static tier-0 row, NOT a direct hit):**
   ```bash
   curl -s -H 'X-API-Key: <key>' https://<gateway>/v1/embeddings \
     -H 'Content-Type: application/json' \
     -d '{"input":"ping","model":"e5"}' | jq '.data[0].embedding | length'
   # expect: 1024  (proves the gateway /v1/embeddings reaches Infinity)
   ```
3. **Gateway resolves embed tier-0 to the embed host (not a pod IP):**
   ```bash
   docker exec ai-gateway-dev /gatewayctl upstreams   # or the gateway probe
   # expect: embed tier-0 → http://10.10.10.20:7997
   ```
4. **24/7 survives a restart:**
   ```bash
   ssh n8n-ia-vm 'docker restart ai-gateway-embed && sleep 20 && \
     curl -fsS http://localhost:7997/v1/health && echo OK'
   # expect: 200 / OK  (restart: unless-stopped + cached model = fast recover)
   ```

Record all four outputs in `06.7-06-SUMMARY.md`.
