# Architecture Research

**Domain:** Multi-tenant AI inference gateway with local-GPU primary + cloud-LLM failover + auto-provisioned emergency GPU
**Researched:** 2026-04-17
**Confidence:** HIGH (backed by Envoy AI Gateway, Bifrost, LiteLLM, Vast.ai docs, DCGM-Exporter, Cloudflare Tunnel)

---

## Standard Architecture

### System Overview

```
                                  External Clients (ConverseAI, Chat Ifix, Telefonia, etc.)
                                  OpenAI-compatible SDKs pointing at gateway.ifix.com.br
                                                 │
                                                 ▼
┌──────────────────────────────────────────────────────────────────────────────────────────────┐
│                    EDGE (optional) — Cloudflare DNS + TLS                                    │
│              A record gateway.ifix.com.br → VPS IP (or Cloudflare Tunnel)                    │
└──────────────────────────────────────────────────────────────────────────────────────────────┘
                                                 │
                                                 ▼
┌──────────────────────────────────────────────────────────────────────────────────────────────┐
│                     VPS (4 vCPU, Docker Compose, Portainer)                                  │
│                                                                                              │
│   ┌────────────────────────────────── gateway (Go) ─────────────────────────────────────┐    │
│   │                                                                                      │   │
│   │  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌─────────┐  │   │
│   │  │ HTTP     │→ │  Auth    │→ │  Rate    │→ │  Router  │→ │Dispatcher│→ │ Metrics │  │   │
│   │  │ Server   │  │ (apikey) │  │ Limiter  │  │ (policy) │  │ (proxy)  │  │ Emitter │  │   │
│   │  └──────────┘  └──────────┘  └──────────┘  └──────────┘  └──────────┘  └─────────┘  │   │
│   │                                               │                                      │   │
│   │          ┌─────────── Circuit Breakers ──────┼──────── Health Probes ───────┐        │   │
│   │          │  (per upstream: local LLM, local  │   periodic GET /health       │        │   │
│   │          │   STT, local embed, OpenRouter,   │   + latency/error windows)   │        │   │
│   │          │   OpenAI, emergency pod)          │                              │        │   │
│   │          └────────────────────────────────────┴──────────────────────────────┘        │   │
│   │                                                                                      │   │
│   │  ┌─────────────── Failover State Machine (in Redis, watched by leader) ────────┐     │   │
│   │  │  PRIMARY_HEALTHY ↔ DEGRADED ↔ FAILED_OVER ↔ EMERGENCY_PROVISIONING ↔ ...     │     │   │
│   │  └──────────────────────────────────────────────────────────────────────────────┘     │   │
│   │                                                                                      │   │
│   │  ┌─────────────── Provisioner (goroutine, leader-only) ─────────────────────────┐    │   │
│   │  │  Vast.ai REST client │ onstart bootstrap │ readiness loop │ teardown timer   │    │   │
│   │  └───────────────────────────────────────────────────────────────────────────────┘    │   │
│   │                                                                                      │   │
│   │  ┌─────────────── Alert Dispatcher (goroutine) ─────────────────────────────────┐    │   │
│   │  │  WhatsApp (Evolution/Z-API)  │  Email (Brevo SMTP via /email pkg)             │    │   │
│   │  └───────────────────────────────────────────────────────────────────────────────┘    │   │
│   └──────────────────────────────────────────────────────────────────────────────────────┘   │
│                                                                                              │
│   ┌───────────── dashboard (Next.js 15 App Router) ─────────────────────────────────────┐    │
│   │  /admin → metrics panels, pod list, failover timeline, cost dashboard, API keys CRUD │   │
│   │  Reads directly from Postgres + Redis; writes config rows that gateway hot-reloads.  │   │
│   └──────────────────────────────────────────────────────────────────────────────────────┘   │
└──────────────────────────────────────────────────────────────────────────────────────────────┘
                  │                         │                              │
                  │ (SQL)                   │ (TCP)                        │ (HTTPS)
                  ▼                         ▼                              ▼
┌────────────────────────┐   ┌─────────────────────────┐   ┌─────────────────────────────────┐
│  Postgres (DO managed) │   │  Redis (local Docker or │   │  External LLM/STT/Embed APIs    │
│  schema: ai_gateway    │   │  managed)               │   │  OpenRouter / OpenAI            │
│  - api_keys            │   │  - rate-limit counters  │   │                                 │
│  - quota_ledger        │   │  - circuit state        │   └─────────────────────────────────┘
│  - audit_log           │   │  - pod registry         │
│  - billing_events      │   │  - failover state       │
│  - pods (history)      │   │  - leader-election lock │
│  - alerts              │   │  - pub/sub: config-reload│
│  - config (versioned)  │   │  - recent latencies     │
└────────────────────────┘   └─────────────────────────┘

                          GPU data plane (inference pods)
                                      │
        ┌─────────────────────────────┼──────────────────────────────┐
        ▼                             ▼                              ▼
┌──────────────────────┐   ┌──────────────────────┐    ┌──────────────────────┐
│  Vast.ai PRIMARY pod │   │  Vast.ai EMERGENCY   │    │  (optional) local    │
│  (persistent, 24/7)  │   │  pod (on-demand)     │    │  CPU fallback nodes  │
│                      │   │                      │    │  (out of scope v1)   │
│ :8000 llama.cpp Qwen │   │  same 3-svc layout   │    │                      │
│ :8001 Whisper FastAPI│   │  (booted from pre-   │    │                      │
│ :8002 BGE-M3 FastAPI │   │   built Docker image │    │                      │
│ :9100 health-bridge  │   │   + onstart script)  │    │                      │
│ (optional sidecar)   │   │                      │    │                      │
└──────────────────────┘   └──────────────────────┘    └──────────────────────┘
```

### Component Responsibilities

| Component | Responsibility | Typical Implementation |
|-----------|----------------|------------------------|
| **HTTP Server** (Go) | Terminate TLS (or let CF handle it), parse OpenAI-compatible routes `/v1/chat/completions`, `/v1/embeddings`, `/v1/audio/transcriptions` | `net/http` + `chi` or `gin`; `httputil.ReverseProxy` with `FlushInterval=-1` for SSE |
| **Auth** | Resolve `Authorization: Bearer <apikey>` → tenant row; attach tenant ctx to request | Hashed key lookup in Postgres, 60s LRU cache in-process, Redis invalidation channel |
| **Rate Limiter** | Enforce per-tenant RPM/TPM/concurrent limits; return 429 with `Retry-After` | Redis `INCR` + `EXPIRE` sliding-window or token-bucket; `github.com/redis/go-redis/v9` |
| **Router** | Policy decision: local vs OpenRouter vs emergency; respects mode (24/7 / pico-vale), load-shedding threshold, circuit state | Pure Go function reading from in-memory config snapshot; no I/O in hot path |
| **Dispatcher** | Actual proxy: opens upstream connection, streams response back, handles retries/timeouts/fallback chain | `httputil.ReverseProxy` per upstream with custom `Transport` (no idle-timeout for streams); wraps fallback in a for-loop |
| **Circuit Breaker** | Per-upstream state (closed/half-open/open) based on error rate + latency window | `sony/gobreaker` or `mercari/go-circuitbreaker`; state persisted to Redis for cross-restart continuity |
| **Health Probes** | Periodic GET on each upstream's `/health` + a trivial real inference every N minutes | Background goroutine; writes to Redis `hash:upstream_health` |
| **Failover State Machine** | Single source of truth for "which upstream is authoritative right now"; drives Router decisions | FSM library (e.g. `looplab/fsm`) or hand-rolled; current state in Redis; transitions logged to Postgres `audit_log` |
| **Provisioner** | Leader-only goroutine. Watches FSM; when state enters `EMERGENCY_PROVISIONING`, calls Vast.ai API to search offers → place bid → inject SSH key → run onstart script → poll readiness → register pod in Redis pool → transition FSM | Vast.ai REST API (JSON over HTTPS) using `net/http`; NO official Go SDK exists, wrap Python CLI or reimplement from docs |
| **Alert Dispatcher** | Consumes FSM transitions + circuit events; rate-limited alert emission to WhatsApp/email | Goroutine with inbox channel; dedupe via Redis `SET NX` with TTL |
| **Metrics Emitter** | Expose `/metrics` (Prometheus text format) for dashboard's pull; structured JSON logs | `prometheus/client_golang`; counters for requests/errors/cost; histograms for latency |
| **Dashboard (Next.js)** | Admin UI: live metrics via polling of gateway `/metrics` + Postgres reads via direct query or thin API; API-key CRUD (writes `api_keys` table + publishes `config-reload` to Redis) | Next.js 15 App Router, TanStack Query for polling, shadcn/ui components, Recharts |
| **Health-Bridge (pod sidecar)** | *Optional.* Small Python/Go process on GPU pod aggregating nvidia-smi + each model-server's `/health` into a single endpoint the gateway probes | Single goroutine + `nvidia-smi --query-gpu=...` exec every 5s; reduces gateway fan-out |

---

## Recommended Project Structure

```
ifix-ai-gateway/
├── cmd/
│   ├── gateway/              # main binary: cmd/gateway/main.go
│   └── provisioner-admin/    # CLI helper to force-provision / teardown pods (ops)
├── internal/
│   ├── config/               # env + dynamic config types, versioned schema
│   ├── auth/                 # API key resolution, tenant context
│   ├── ratelimit/            # Redis token-bucket / sliding window
│   ├── router/               # policy decision (pure functions, 100% unit-testable)
│   ├── upstream/             # upstream interface + implementations:
│   │   ├── local_llm.go      #   local Qwen (llama.cpp)
│   │   ├── local_stt.go      #   local Whisper
│   │   ├── local_embed.go    #   local BGE-M3
│   │   ├── openrouter.go     #   OpenRouter Qwen 3.5 27B
│   │   ├── openai_stt.go     #   OpenAI Whisper API
│   │   └── openai_embed.go   #   OpenAI text-embedding-3-small
│   ├── dispatcher/           # ReverseProxy wrappers, fallback chain, streaming
│   ├── breaker/              # gobreaker wrappers, Redis persistence
│   ├── health/               # probe loop, latency window, VRAM sampling
│   ├── fsm/                  # failover state machine
│   ├── provisioner/          # Vast.ai client + lifecycle manager
│   ├── vastai/               # Vast.ai REST API client (no official Go SDK)
│   ├── alerts/               # dispatcher + dedupe + WhatsApp/email adapters
│   ├── metrics/              # prometheus registry + custom collectors
│   ├── store/                # Postgres (sqlc-generated or pgx+Goqu) + Redis wrapper
│   ├── leader/               # redislock-based leader election
│   └── server/               # HTTP server wiring, middleware chain
├── migrations/               # Postgres SQL migrations (goose or atlas)
├── scripts/
│   ├── vastai-onstart.sh     # bootstrap script injected into emergency pod
│   └── build-pod-image.sh    # builds pre-baked Docker image w/ models
├── pod-image/                # Dockerfile for the GPU pod itself
│   ├── Dockerfile            # FROM vastai/pytorch; bakes llama.cpp + Whisper + BGE
│   ├── start-llm.sh
│   ├── start-stt.sh
│   ├── start-embed.sh
│   └── health-bridge.py      # optional sidecar
├── dashboard/                # Next.js 15 app
│   ├── src/app/              # App Router pages
│   ├── src/lib/api.ts        # typed client against gateway /admin/* endpoints
│   └── ...
├── docker-compose.yml        # gateway + dashboard + redis (prod uses DO Postgres)
├── docker-compose.dev.yml    # + pgvector-enabled Postgres for local
└── .github/workflows/        # build images, push to ghcr.io, trigger Portainer webhook
```

### Structure Rationale

- **`cmd/gateway/`** single binary keeps deploy trivial (one container, one healthcheck). A provisioner-only binary was considered but rejected: the provisioner needs the same FSM/Redis/config code, forking it just doubles the dep graph.
- **`internal/upstream/`** as pluggable interface (`type Upstream interface { Forward(ctx, w, r) error }`) lets the Router treat "local Qwen" and "OpenRouter Qwen" symmetrically. Adding a new provider = one file.
- **`internal/vastai/`** is isolated so it can be swapped (e.g., future RunPod/TensorDock support) without touching provisioner logic.
- **`pod-image/`** is source-of-truth for what runs on the GPU; versioned alongside gateway so a gateway release pins a compatible image tag. Critical for reproducibility during emergency provisioning.
- **`dashboard/`** kept as separate package (not a pkg inside gateway) because it's Next.js — different runtime, different deploy cadence. Hits the same Redis + Postgres.

---

## Architectural Patterns

### Pattern 1: Monolithic Control + Data Plane (RECOMMENDED for v1)

**What:** One Go binary = HTTP traffic (data plane) + provisioning logic (control plane) + alert dispatch. Leader-elected via Redis lock so only one gateway replica runs provisioner/alert loops. Traffic serving runs on all replicas.

**When to use:** Single VPS, 4 vCPU, <5 replicas, traffic measured in dozens of RPS, team of 1-3 engineers. Exactly the ifix-ai-gateway v1 scope.

**Trade-offs:**
- (+) Simplest mental model, one codebase, one deploy pipeline, no IPC/protocol design.
- (+) State consistency is easy: provisioner reads the same Redis/Postgres the router reads.
- (+) Leader election (Redlock via `go-redsync`) scales horizontally to HA pair when needed.
- (−) Restart latency affects both planes simultaneously. Mitigated by graceful shutdown + leader re-election (~5s).
- (−) A memory leak in provisioner would take down traffic — defensive: bound all goroutines, use pprof, enforce GOMEMLIMIT.

**Example:**
```go
// cmd/gateway/main.go
func main() {
    cfg := config.MustLoad()
    rdb := redis.NewClient(cfg.RedisOpts)
    db := store.MustConnect(cfg.PostgresDSN)

    srv := server.New(cfg, rdb, db)          // data plane
    leader := leader.NewRedlock(rdb, "gw:leader", 15*time.Second)

    ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer cancel()

    // traffic always-on (all replicas)
    go srv.Run(ctx)

    // control-plane loops run ONLY on leader
    go leader.RunWhenElected(ctx, func(lctx context.Context) {
        provisioner.Run(lctx, cfg, rdb, db)   // Vast.ai lifecycle
        alerts.Run(lctx, cfg, rdb, db)        // WhatsApp/email dispatcher
        health.RunProbes(lctx, cfg, rdb)      // periodic probes (one shared probe set)
    })
    <-ctx.Done()
}
```

### Pattern 2: Split Control-Plane / Data-Plane (deferred until needed)

**What:** Two services. `gateway-data` = thin Go reverse proxy, pure traffic. `gateway-control` = separate service owning provisioning, FSM, alerts, config. They share Redis + Postgres; control plane pushes config snapshots via Redis pub/sub.

**When to use:** You hit >1000 RPS OR you want independent scaling (10 data-plane replicas on spot VPS, 1 control-plane on stable VPS), OR your provisioner has heavy workload (e.g., managing a fleet of pods). This is what Envoy AI Gateway does.

**Trade-offs:**
- (+) Data plane can scale horizontally without re-electing provisioner.
- (+) Blast radius: provisioner bug doesn't touch traffic.
- (−) Doubles operational surface (two images, two health checks, two sets of envs).
- (−) Requires config-propagation protocol (pub/sub + version stamps) to avoid split-brain.
- (−) Overkill for a 4 vCPU single-VPS deployment.

**Recommendation:** Keep Pattern 1 for v1. Design the code so upgrading to Pattern 2 is an hour of repackaging (`internal/provisioner/`, `internal/alerts/`, `internal/fsm/` already separated from `internal/dispatcher/` — just build two `cmd/` entrypoints when needed).

### Pattern 3: Thin Gateway + Smart Inference Pods

**What:** Pod runs its own intelligent router (decides when to self-throttle, reports "I'm saturated" proactively via push). Gateway is dumb proxy.

**When to use:** Pods are cattle-like and their behavior varies per model. Common in Kubernetes-native setups with custom model servers.

**Trade-offs vs chosen design:**
- (+) Gateway stays simple; scaling decisions localized to each pod.
- (−) Push-based reporting duplicates health endpoints the gateway already needs.
- (−) Smart pod logic = custom Python/Go in each model server image = more maintenance.
- (−) Pods are externally-managed (Vast.ai) — can't guarantee they run a custom sidecar reliably.

**Verdict:** REJECT. Ifix pods are Vast.ai-rented commodity hardware; keep them dumb (just the 3 model servers + optional lightweight health-bridge that only *aggregates* metrics, doesn't make routing decisions). All intelligence stays in the gateway.

### Pattern 4 (chosen): Fat Gateway + Dumb Inference Pods

**What:** Gateway owns routing, breakers, failover, saturation detection (via pulling metrics from pods). Pods run just the three model servers with a minimal health-bridge sidecar.

**When to use:** Limited per-pod customization (managed GPU market), single-operator gateway, need to switch providers (OpenRouter/OpenAI) as cleanly as switching pods.

**Trade-offs:**
- (+) One place to reason about routing policy.
- (+) Pods are replaceable Docker images; emergency provisioning just runs the same image on a new host.
- (+) Provider-fallback logic (local→OpenRouter) and pod-fallback logic (primary→emergency) share the same code path.
- (−) Gateway becomes a bigger process. Mitigated by small surface per component and leader-only heavy loops.

---

## Data Flow

### Request Flow — LLM Chat Completions (with SSE streaming)

```
[App SDK]  POST /v1/chat/completions  {"stream":true, ...}
    ↓  (HTTPS)
[VPS ingress: Docker Traefik or direct bind on :443]
    ↓
[gateway: HTTP server]  parse route, bind handler
    ↓
[Auth middleware]  validate Bearer key → tenant row (Redis cache, fallback Postgres)
    ↓  on miss/expired → load + cache; on invalid → 401
[Rate limiter]  Redis: INCR tenant:rpm → check; INCR tenant:tpm (estimated) → check
    ↓  exceeded → 429 + Retry-After
[Router]  read FSM state from in-memory snapshot (refreshed every 1s from Redis)
    │    decision tree:
    │      - mode pico/vale & outside hours → upstream=OPENROUTER
    │      - FSM=PRIMARY_HEALTHY & GPU util < 85% → upstream=LOCAL_LLM
    │      - FSM=DEGRADED/FAILED_OVER → upstream=OPENROUTER
    │      - FSM=EMERGENCY_ACTIVE → upstream=EMERGENCY_POD
    ↓
[Dispatcher]  httputil.ReverseProxy with FlushInterval=-1 (immediate per-write flush)
    │    Transport: custom; no ResponseHeaderTimeout on body; IdleConnTimeout=90s
    ↓
[Upstream: llama.cpp server :8000 on Vast.ai pod]  streams SSE `data: {...}\n\n` chunks
    ↓  (chunks flow back through ReverseProxy)
[Dispatcher]  on first-byte timeout OR mid-stream error:
    │    if idempotent (≤1 retry, no tokens yet received) → fallback to OpenRouter
    │    else → forward error; increment breaker; emit metric
    ↓
[Metrics emitter]  non-blocking counter.Add + histogram.Observe at request end
[Audit/Billing]  async goroutine writes to Postgres audit_log + billing_events
    ↓
[Client]  receives SSE stream, unaware which upstream served it
```

**Critical detail:** `httputil.ReverseProxy` auto-detects streaming responses (`Content-Length: -1` or `Transfer-Encoding: chunked`) and flushes immediately. Set `FlushInterval: -1` as belt-and-suspenders. Do NOT set a server-level `WriteTimeout` < expected max chat duration (disable or set to 30min). Reference: Go issue golang/go#27816.

### Request Flow — Audio Transcription (long synchronous request)

```
[App SDK]  POST /v1/audio/transcriptions  multipart (10-min audio ~15MB)
    ↓
[Auth → RateLimit → Router]  (same path; router picks LOCAL_STT or OPENAI_WHISPER)
    ↓
[Dispatcher]  multipart passthrough via ReverseProxy; server ReadTimeout disabled for this route
    ↓
[Upstream: Whisper FastAPI :8001]  single POST, may take 30-90s for 10-min audio
    │    Pattern A (synchronous): server returns full JSON after inference completes
    │    Pattern B (SSE partial segments): faster-whisper streaming — server emits segments as they're transcribed
    ↓
[Dispatcher]  holds connection; no buffering (stream=true); client sees result when server returns
    ↓
[Client]  receives full transcription JSON (or SSE segments if Pattern B)
```

**Critical detail:** Long-running requests (minutes) require:
1. Gateway HTTP server: `ReadTimeout`=0 on transcription route, or use `http.TimeoutHandler` with generous 10m cap.
2. ReverseProxy `Transport`: no `ResponseHeaderTimeout` after headers arrive (but DO set header-timeout of ~30s; if model-server doesn't start responding in 30s, fail fast).
3. Client SDKs must also raise their timeouts (publish this as integration doc).

### Request Flow — Embeddings (short, batch)

```
[App SDK]  POST /v1/embeddings  {"input": ["text1", "text2", ...], "model": "bge-m3"}
    ↓
[Auth → RateLimit → Router]  (LOCAL_EMBED or OPENAI_EMBED)
    ↓
[Dispatcher]  ReverseProxy; 10s timeout total
    ↓
[Upstream: BGE-M3 FastAPI :8002]  single POST, returns JSON array of vectors in <1s
    ↓
[Client]  gets response
```

### State Management

```
┌─────────────────────────── Writes ───────────────────────────┐
│                                                              │
│  Dashboard → Postgres (config, api_keys)                     │
│       ↓ (on commit) publish Redis channel "config:reload"    │
│                                                              │
│  Gateway leader → Postgres (audit, billing, pods, alerts)    │
│                 → Redis (fsm state, pod registry)            │
│                                                              │
│  Gateway any    → Redis (rate-limit counters, latency obs)   │
│                 → Postgres audit (async batched)             │
│                                                              │
└──────────────────────────────────────────────────────────────┘

┌─────────────────────────── Reads ────────────────────────────┐
│                                                              │
│  Router (hot path) → in-memory snapshot (refreshed 1Hz from  │
│                      Redis for FSM, from Postgres for config)│
│                                                              │
│  Auth (hot path)  → in-memory LRU (60s TTL); miss → Postgres │
│  Rate limiter     → Redis direct (Lua script for atomicity)  │
│                                                              │
│  Dashboard (live) → Gateway /metrics + Postgres reads        │
│                                                              │
└──────────────────────────────────────────────────────────────┘
```

### Key Data Flows

1. **Config reload:** Dashboard writes Postgres → publishes `config:reload` on Redis pub/sub → gateway subscribers re-fetch config snapshot → atomic pointer swap of in-memory snapshot. No SIGHUP, no restart. Trade-off: 1-2s propagation delay across replicas is acceptable.
2. **Failover transition:** Health probe detects primary unhealthy → writes breaker open in Redis → FSM-watcher goroutine (leader only) advances state → publishes `fsm:changed` on Redis → all replicas refresh snapshot → Router starts routing to OpenRouter within ~1s.
3. **Emergency pod registration:** Provisioner (leader only) finishes readiness probe → HSETs Redis `pods:active` with pod ID, IP, ports, capabilities → advances FSM to `EMERGENCY_ACTIVE` → publishes `fsm:changed` → replicas see new upstream and start routing.
4. **Audit/billing:** Each request completes → handler pushes to buffered channel → background flusher batches 100 rows / 5s → single INSERT to Postgres (reduces DB write pressure).

---

## Failover State Machine

### States

| State | Meaning | Upstream Choice |
|-------|---------|-----------------|
| `PRIMARY_HEALTHY` | Primary pod responding, util <85%, latency within SLO | local |
| `PRIMARY_DEGRADED` | Elevated latency or GPU util 85-95%; shedding overflow | mix: some local, some OpenRouter |
| `FAILED_OVER` | Primary breaker open (N consecutive failures or timeout) | OpenRouter / OpenAI |
| `EMERGENCY_PROVISIONING` | Triggered spin-up; Vast.ai pod creating | OpenRouter / OpenAI (cover gap) |
| `EMERGENCY_ACTIVE` | Emergency pod healthy and serving | emergency pod primarily |
| `RECOVERING` | Primary came back; verifying 5 min stability | still emergency/fallback; probing primary |
| `COOLDOWN` | Primary stable 5 min; grace-period countdown before tearing down emergency | primary |
| `OFF_HOURS` | pico/vale mode active outside 08-22h | OpenRouter (all apps in this mode) |
| `MAINTENANCE` | Manual operator state | all apps fall back to configured alt |

### Transitions

```
                             [operator sets OFF_HOURS schedule or MAINTENANCE] → override
                                                           │
                                                           ▼
PRIMARY_HEALTHY ─── health probe fails (N×) ──────────▶ FAILED_OVER
      │                                                    │
      │                                              2 min still failed
      │                                                    ▼
      │                                          EMERGENCY_PROVISIONING
      │                                                    │
      │                                           pod passes 3 probes
      │                                                    ▼
      │                                          EMERGENCY_ACTIVE ◀────┐
      │                                                    │             │
      ▼                                                    │             │
PRIMARY_DEGRADED  (GPU util >85%, shed 30%) ───────▶   (primary probes) │
      │                                                    │             │
      │ ←─── util drops <75% for 2 min ────────── primary healthy 5 min │
      │                                                    ▼             │
      └──────── util recovers ──────────▶ PRIMARY_HEALTHY RECOVERING ────┘
                                                           │
                                                    5 min of stability
                                                           ▼
                                                       COOLDOWN (5 min grace)
                                                           │
                                                    grace ends, tear down
                                                           ▼
                                                  PRIMARY_HEALTHY
```

### Triggers (rules)

| From → To | Trigger | Who Evaluates |
|-----------|---------|---------------|
| HEALTHY → DEGRADED | GPU util >85% for 30s OR p95 latency > 2× baseline for 60s | Health probe writes metric → leader FSM tick |
| DEGRADED → HEALTHY | Util <75% and latency recovered for 2 min | Leader FSM tick |
| HEALTHY/DEGRADED → FAILED_OVER | 5 consecutive health failures OR breaker OPEN | Breaker callback → FSM tick |
| FAILED_OVER → EMERGENCY_PROVISIONING | Sustained 2 min (guard: never repeat within 10 min) | Leader FSM tick (after cooldown check) |
| EMERGENCY_PROVISIONING → EMERGENCY_ACTIVE | Provisioner reports pod ready (3 consecutive probes OK) | Provisioner |
| EMERGENCY_ACTIVE → RECOVERING | Primary probes green for 60s | Leader FSM tick |
| RECOVERING → COOLDOWN | Primary green 5 min consecutive | Leader FSM tick |
| COOLDOWN → PRIMARY_HEALTHY | 5 min grace + pod teardown confirmed | Provisioner |
| any → FAILED_OVER (rollback) | Emergency pod also fails | Breaker callback |
| any → MAINTENANCE | Manual operator action | Dashboard write |

### Coordination (multi-replica)

Leader election via Redis lock (`go-redsync`): key `ai-gateway:leader`, TTL 15s, refresh every 5s. Only the leader runs FSM-advance loop, provisioner, alert dispatcher. Non-leaders serve traffic and read FSM state from Redis (refreshed 1Hz). Loss of leader → next refresh window (max ~15s) elects new leader. Traffic routing unaffected because FSM state lives in Redis, not in leader process memory.

---

## Auto-Provisioning Lifecycle

### Model-Weight Strategy Decision

| Strategy | Cold-start time | Cost | Reliability | Verdict |
|----------|----------------|------|-------------|---------|
| Re-download from HuggingFace on every provision | 10-20 min (Qwen 27B Q4=~16GB, Whisper ~3GB, BGE ~2GB ≈ 21GB over internet) | Free bandwidth but GPU billed while downloading | HF sometimes rate-limits or returns 503 | REJECT — too slow |
| Vast.ai persistent volume (pay while idle) | <1 min (models already on disk) | ~$0.10/GB/month idle (~$2-3/month for 21GB) + volume mount delay | Good, but ties you to one datacenter | AVOID for emergency pod (emergency is ephemeral by design) |
| Pre-built Docker image with models baked in | 3-5 min (image pull is huge ~25-30GB, but pulled once then fast on re-provision if host cached) | Free ghcr storage; image build is slow but once-off | Best — reproducible, version-pinned | **RECOMMENDED** |
| S3 staging (weights on Cloudflare R2, onstart pulls in parallel) | 5-8 min (parallel download 21GB @ typical Vast.ai bw) | R2 egress free to most regions; small storage cost | Good; gateway controls version | Second choice, or use if image is too big |

**Chosen:** Pre-built Docker image, tag-versioned (`ghcr.io/ifixtelecom/ifix-ai-pod:v1.2.3`). Build cadence: only when model set changes. Image contains Qwen Q4_K_M GGUF + Whisper large-v3 + BGE-M3 + all three servers + health-bridge. Size ~28GB uncompressed; Docker layer caching on repeat pulls to same host makes re-provisioning fast.

Fallback plan: if image exceeds Vast.ai registry pull limits or breaks frequently, switch to S3/R2-staged weights pattern.

### Lifecycle

```
  Trigger: FSM → EMERGENCY_PROVISIONING
                        │
                        ▼
   [Provisioner.spinUp(ctx)]
                        │
     1. Search offers: POST /api/v0/search/asks with query:
        gpu_name=RTX 4090, dph_total<=0.40, reliability>0.95,
        disk_space>=60, inet_down>=500, datacenter=optional hot regions
                        │
     2. Pick cheapest eligible offer → POST /api/v0/asks/{id}/
        body includes:
          - image: "ghcr.io/ifixtelecom/ifix-ai-pod:v1.2.3"
          - env: GATEWAY_PUBKEY=<gateway's public API token>
                 TENANT=emergency
                 HEALTH_TOKEN=<random>
          - onstart: script that registers back to gateway with its URL
          - ssh_key: (injected for debug; otherwise skip)
          - disk: 60 GB
                        │
     3. Poll GET /api/v0/instances/{id} until actual_status=running (max 5min)
        record pod_id, ssh_host, ssh_port, public_ipaddr, ports_open
                        │
     4. Spawn goroutine: probe pod's /health (via gateway's exposed tunnel OR
        direct public IP:port if accessible)
          - try 3× with 30s interval
          - on success: advance to step 5
          - on 5 min timeout: kill pod, log FAILED, alert operator, FSM→FAILED_OVER
                        │
     5. Register in Redis: HSET pods:active emergency <pod meta>
        Persist to Postgres pods table (status=active, provisioned_at=now)
        Advance FSM → EMERGENCY_ACTIVE
        Send alert: "Emergency pod ACTIVE, hourly cost $X"
                        │
   [serving…]
                        │
     6. FSM → COOLDOWN → 5 min grace timer
                        │
     7. [Provisioner.tearDown(pod_id)]
          - DELETE /api/v0/instances/{id}
          - HDEL pods:active emergency
          - UPDATE pods SET status=destroyed, destroyed_at=now
          - Advance FSM → PRIMARY_HEALTHY
          - Send alert with duration + estimated cost
```

### Guardrails (non-negotiable)

- **Price cap:** reject any offer with `dph_total > CONFIG.MAX_HOURLY_USD` (default $0.40). If no offer passes, alert operator and remain in `FAILED_OVER`.
- **Single-pod lock:** Redis `SETNX pods:emergency:lock NX EX 7200`. If a second trigger fires while one is provisioning/running, skip. Log it.
- **Global daily budget:** cron job at 00:00 UTC sums `billing_events.provisioning_cost` for the day. If projected >$X, block new provisions until reset.
- **Manual kill switch:** dashboard button → writes `MAINTENANCE` mode → provisioner tears down on next tick.

### Provisioning Dependencies

Before you can auto-provision, these must exist:
1. Docker image published and pullable from a public or token-authenticated registry.
2. Vast.ai API key stored in gateway secrets (env `VAST_AI_KEY`).
3. A Vast.ai "template" ID OR all params passed inline.
4. Gateway must expose a callback URL (or tunnel) for the emergency pod to call home (or gateway polls pod). Simpler to poll from gateway.
5. A published pricing ceiling validated against current market.

---

## Scaling Considerations

| Scale | Architecture Adjustments |
|-------|--------------------------|
| 0-10 RPS (initial) | Single VPS, single gateway replica, local Redis in Docker Compose, DO managed Postgres. Provisioner/traffic same process. This is v1. |
| 10-100 RPS | Upgrade VPS to 8 vCPU OR run 2 gateway replicas with HAProxy/Caddy sticky-less round-robin; Redis moves to DO managed; leader election kicks in properly. |
| 100-500 RPS | Split control plane out (Pattern 2). Add read replicas for Postgres. Pin Redis to dedicated instance. Consider Envoy/Bifrost if Go implementation caps out. |
| 500+ RPS | Re-architect: multi-region, Envoy AI Gateway or Bifrost as data plane, custom provisioner as separate service. Leave this as explicit re-evaluation point. |

### Scaling Priorities (what breaks first)

1. **Redis CPU on rate-limit Lua scripts** — mitigate with pipelining + optional in-process leaky bucket for coarse filtering before Redis.
2. **Postgres connection saturation** — pool carefully (pgbouncer or `pgx/v5` pool); audit writes must batch.
3. **ReverseProxy file descriptor exhaustion on streaming** — tune `ulimit -n` and `MaxIdleConnsPerHost`; observe `go_net_conns` metric.
4. **Vast.ai API rate limits** during provisioning storms — cache offer searches for 30s; exponential backoff on 429.
5. **Single VPS bandwidth** — 10-min audio uploads are ~15MB each; 100 concurrent = 1.5GB peak. Typical 4vCPU VPS has >500 Mbps, enough, but measure.

---

## Anti-Patterns

### Anti-Pattern 1: Per-request health check

**What people do:** On every incoming request, gateway calls the local model server's `/health` before deciding to use it.

**Why it's wrong:** Triples request load on model servers; adds latency equal to probe RTT; creates a feedback loop when the model server is already slow (it's slow AT health-check too).

**Do this instead:** Periodic background health probes every 5-10s write into a shared map (protected by RWMutex) or Redis. Router reads the latest snapshot — zero-latency decision.

### Anti-Pattern 2: Silent buffering on streaming

**What people do:** Use default `httputil.ReverseProxy` without `FlushInterval` configured. SSE chunks accumulate in intermediate buffers, arrive in bursts.

**Why it's wrong:** Client perceives the stream as broken / slow; defeats the whole point of streaming UX.

**Do this instead:** Set `FlushInterval: -1` explicitly. Verify with `curl -N`. Do not apply any `gzip` middleware on streaming routes. Confirmed behavior in Go 1.22+ — `FlushInterval=-1` means "flush after every write".

### Anti-Pattern 3: Retrying non-idempotent streaming after partial response

**What people do:** "I got a 500 mid-stream, let me retry the whole request on fallback provider."

**Why it's wrong:** Client already received 40% of a chat response. Retrying means 2x tokens visible, duplicated billing, confused client.

**Do this instead:** Retry only if zero bytes sent to client. Gateway tracks `bytesWritten` on the response writer; if >0, do not fall back — forward the error or close stream cleanly.

### Anti-Pattern 4: Polling Vast.ai API tightly

**What people do:** While waiting for pod to boot, poll `/instances/{id}` every 2 seconds.

**Why it's wrong:** Vast.ai enforces rate limits. Under a provisioning storm (e.g., flapping primary) you'll hit 429 and lose the pod instance tracking.

**Do this instead:** Poll with exponential backoff starting at 10s, cap 30s. Total provisioning budget 5 minutes. If a pod isn't running by then, destroy it and fall back.

### Anti-Pattern 5: Storing API keys plaintext in Postgres

**What people do:** `api_keys.key VARCHAR` with literal bearer token.

**Why it's wrong:** DB leak → every tenant compromised. Dump of audit_log may also leak.

**Do this instead:** Store `key_prefix` (first 8 chars, for display + lookup) + `key_hash` (argon2id or bcrypt of full token). On lookup: extract prefix from request, fetch candidate rows, bcrypt-compare. Hot path still O(1) via unique prefix index.

### Anti-Pattern 6: Tight coupling between dashboard and gateway process

**What people do:** Dashboard Next.js API routes call gateway internal functions directly.

**Why it's wrong:** Dashboard can only deploy together; crash in dashboard takes down gateway or vice versa.

**Do this instead:** Dashboard talks to Postgres and Redis directly (read-mostly) and hits gateway's small `/admin/*` endpoints only for imperative actions (force-teardown pod, force-reload config). Loose coupling = independent deploys.

---

## Integration Points

### External Services

| Service | Integration Pattern | Notes |
|---------|---------------------|-------|
| Vast.ai REST API | JSON over HTTPS, bearer token in header | No official Go SDK; reimplement client from https://docs.vast.ai/api-reference/. Cache auth; retry 5xx w/ backoff; hard-fail 4xx (except 429). |
| OpenRouter | OpenAI-compatible, bearer token | `base_url: https://openrouter.ai/api/v1`, model id `qwen/qwen-2.5-72b-instruct` (verify exact Qwen 3.5 27B model ID currently listed). Streams SSE natively. |
| OpenAI Whisper API | POST multipart to `https://api.openai.com/v1/audio/transcriptions` | No stream, single JSON response. Same path as local STT — just swap upstream URL. |
| OpenAI Embeddings | POST to `https://api.openai.com/v1/embeddings` | Batching: send up to 2048 inputs per request; returns JSON. |
| Postgres (DO) | pgx v5 pool | Dedicated schema `ai_gateway`; separate connection pool (10-20 conns) from other ifix apps if sharing cluster. SSL required. |
| Redis | go-redis v9 | Use dedicated logical DB number to avoid key collisions; `CLUSTER` not needed at this scale. |
| WhatsApp (Evolution API / Z-API / Chatwoot) | HTTP webhook | Simple POST with text body; retry 2× on 5xx. TBD which provider based on current ifix usage. |
| Brevo SMTP | `net/smtp` or `gomail/v2` | Fire-and-forget send; log failures. |
| Sentry (optional) | `getsentry/sentry-go` | Capture panics, breaker trips, provisioning failures. |
| Cloudflare (optional) | DNS A record or Cloudflare Tunnel (`cloudflared`) | Tunnel lets you skip public IP exposure; outbound-only; free tier sufficient. |

### Internal Boundaries

| Boundary | Communication | Notes |
|----------|---------------|-------|
| gateway ↔ GPU pod (LLM/STT/Embed) | HTTP/1.1 reverse proxy | Direct TCP to pod's public IP:port (Vast.ai exposes mapped ports). Use pod's public IP from `/instances/{id}` response. |
| gateway ↔ gateway (HA) | None directly; via Redis | State in Redis (FSM, pod registry); leader lock via Redlock. |
| gateway ↔ dashboard | Dashboard reads Postgres+Redis directly; calls gateway `/admin/*` for actions | Dashboard auth via Better Auth (same stack as ConverseAI); admin role enforced. |
| gateway ↔ existing ifix apps | Apps consume gateway via OpenAI SDK w/ `base_url=https://gateway.ifix.com.br/v1` | Apps only change `base_url` + `api_key` env; no SDK swap. Backward-compatible migration. |
| provisioner ↔ Vast.ai | HTTPS + bearer | All calls in leader-only goroutine; isolated `internal/vastai/` package. |

---

## Deployment Topology

### V1 — Single VPS (recommended start)

```
Internet
  │
  ▼
gateway.ifix.com.br (A record → VPS public IP, OR cloudflared tunnel)
  │
  ▼
VPS (4 vCPU, 8GB RAM, Hetzner / DO / other)
  ├─ Traefik (reverse proxy, Let's Encrypt auto-TLS)   [optional — can bind gateway directly :443]
  ├─ gateway (Go binary, Docker image)                  :8080 internal
  ├─ dashboard (Next.js, Docker)                        :3000 internal
  └─ redis (Docker)                                     :6379 internal, localhost-only

External:
  ├─ Postgres  → DO managed cluster, same region as VPS
  ├─ Primary GPU pod → Vast.ai (RTX 4090, ~$0.35/h)
  └─ Emergency pod   → Vast.ai (on-demand)
```

### V2 — HA pair (when needed, after v1 validates)

Add second VPS in same region; put Caddy/HAProxy in front (Cloudflare load balancing is simpler at small scale). Both gateways share the same Redis + Postgres. Redlock picks one leader for control-plane loops. Still single region (the GPU pods and Postgres are single-region anyway — multi-region doesn't buy much until you have multi-region GPUs).

### V3 — Behind Cloudflare (optional upgrade)

DNS via Cloudflare + cloudflared tunnel from VPS. Benefits: DDoS protection, TLS termination at edge, skip public IP exposure. No inbound ports needed on VPS. Zero Trust policies can gate dashboard access (but the gateway API itself must be public because apps call it from their own servers).

### DNS strategy

- `gateway.ifix.com.br` → production VPS (traffic endpoint)
- `gateway-dev.ifix.com.br` → dev VPS (this machine)
- `dashboard-ai.ifix.com.br` → prod dashboard
- TTL 300s (5 min) so failover of VPS itself (rare, manual) is quick.

### Postgres placement

DO managed Postgres, same region as VPS (Frankfurt or NYC depending on ifix infra current). Schema `ai_gateway`. Reuses existing DO cluster per PROJECT.md constraint. SSL required; connection string in Portainer env.

---

## Suggested Build Order

Explicit dependency chain for phased rollout:

### Phase 1 — Foundation (can be built/tested in isolation)
1. **Pod Docker image** (`pod-image/Dockerfile`): Qwen llama.cpp + Whisper FastAPI + BGE FastAPI + health-bridge, all starting via supervisord or tini. *Dependency:* none. *Testable:* `docker run --gpus all ... && curl :8000/v1/models`.
2. **Manual primary pod launch**: bring up the pod on Vast.ai by hand, verify all 3 model servers respond. *Dependency:* (1).
3. **Postgres schema + migrations**: `api_keys`, `quota_ledger`, `audit_log`, `billing_events`, `pods`, `alerts`, `config`. *Dependency:* none. *Testable:* migrate up/down cleanly.

### Phase 2 — Gateway MVP (single upstream, no failover)
4. **HTTP server + Auth + Rate limiter**: straight-through proxy to local LLM only. *Dependency:* (3). *Testable:* curl gateway → receives Qwen response.
5. **Router + Dispatcher for all 3 endpoints** (chat, embed, transcribe), all proxying to primary pod. *Dependency:* (4). *Testable:* existing ifix apps can be pointed at gateway with one env change.
6. **Metrics emitter + structured logs**. *Dependency:* (4). *Testable:* `/metrics` returns counters.

### Phase 3 — Resilience (add breakers + provider fallback)
7. **Circuit breakers + health probes**: per-upstream state in Redis. *Dependency:* (5). *Testable:* kill primary pod, gateway detects failure.
8. **OpenRouter/OpenAI upstream adapters**: same `Upstream` interface. *Dependency:* (5). *Testable:* force breaker open, gateway routes to OpenRouter.
9. **Failover state machine (without emergency pod yet)**: states HEALTHY/DEGRADED/FAILED_OVER only. *Dependency:* (7)+(8). *Testable:* FSM transitions under simulated failures.

### Phase 4 — Auto-provisioning (the hard part)
10. **Vast.ai REST client** (search, create, status, destroy). *Dependency:* (3). *Testable:* dry-run creates a pod, waits for running, destroys it.
11. **Provisioner with leader election**. *Dependency:* (9)+(10). *Testable:* force FSM → EMERGENCY_PROVISIONING, observe pod lifecycle.
12. **Emergency pod integration into FSM** (EMERGENCY_PROVISIONING / _ACTIVE / RECOVERING / COOLDOWN). *Dependency:* (11). *Testable:* chaos-drill: kill primary, new pod serves traffic within 5 min.

### Phase 5 — Observability + Ops
13. **Alert dispatcher** (WhatsApp + email). *Dependency:* (9)+(12). *Testable:* trigger a state change, alert arrives.
14. **Dashboard v1**: live metrics, pod list, FSM timeline, API-key CRUD. *Dependency:* (3)+(6). *Testable:* browse dashboard while running chaos drill.
15. **Quotas + billing events**. *Dependency:* (4)+(6). *Testable:* rate-limited tenant blocked, cost accumulates correctly.

### Phase 6 — Production rollout
16. **Apps migration**: flip ConverseAI, Chat Ifix, etc. to use gateway base_url (one app at a time). *Dependency:* (13)+(14)+(15). *Testable:* traffic from real app flows through gateway without regression.

**Critical path for "failover invisible" core value:** Phases 1 → 2 → 3 → 4. Observability (5) can lag by a week. Dashboard (14) is nice-to-have once you have reliable alerts (13).

---

## Sources

- [Envoy AI Gateway — System Architecture Overview](https://aigateway.envoyproxy.io/docs/concepts/architecture/system-architecture/)
- [Envoy AI Gateway — Control Plane Explained](https://aigateway.envoyproxy.io/docs/concepts/architecture/control-plane/)
- [Envoy AI Gateway — Provider Fallback](https://aigateway.envoyproxy.io/docs/0.4/capabilities/traffic/provider-fallback/)
- [Understanding Gateway API Split Architecture: Control Plane vs. Data Plane — NGINX](https://blog.nginx.org/blog/understanding-gateway-api-split-architecture-control-plane-vs-data-plane)
- [API Gateway Control Plane vs. Data Plane — API7.ai](https://api7.ai/learning-center/api-gateway-guide/api-gateway-control-plane-vs-data-plane)
- [Bifrost Architecture and LiteLLM comparison (Maxim AI)](https://www.getmaxim.ai/articles/top-5-enterprise-llm-gateways-in-2026/)
- [Retries, Fallbacks, and Circuit Breakers in LLM Apps — Portkey](https://portkey.ai/blog/retries-fallbacks-and-circuit-breakers-in-llm-apps/)
- [Implementing Circuit Breakers for LLM Services in Go](https://dasroot.net/posts/2026/02/implementing-circuit-breakers-for-llm-services-in-go/)
- [Vast.ai API Reference — Introduction](https://docs.vast.ai/api-reference/introduction)
- [Vast.ai — Create Instance API](https://docs.vast.ai/api-reference/instances/create-instance)
- [Vast.ai — Creating a Custom Template](https://docs.vast.ai/creating-a-custom-template)
- [Vast.ai — Docker Execution Environment](https://docs.vast.ai/documentation/instances/templates/docker-environment)
- [Vast.ai Base Image (github)](https://github.com/vast-ai/base-image)
- [Go httputil.ReverseProxy — pkg docs](https://pkg.go.dev/net/http/httputil)
- [Go issue #27816 — ReverseProxy streaming behavior](https://github.com/golang/go/issues/27816)
- [NVIDIA DCGM Exporter](https://github.com/NVIDIA/dcgm-exporter)
- [nvidia_gpu_exporter (nvidia-smi based)](https://github.com/utkuozdemir/nvidia_gpu_exporter)
- [Redis Distributed Locks (official guide)](https://redis.io/docs/latest/develop/clients/patterns/distributed-locks/)
- [go-redsync (Redlock in Go)](https://github.com/go-redsync/redsync)
- [Cloudflare Tunnel — Cloudflare One docs](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/)
- [faster-whisper FastAPI streaming server (nirnaim)](https://github.com/nirnaim/faster-whisper-server)
- [vLLM OpenAI-Compatible Server docs](https://docs.vllm.ai/en/stable/serving/openai_compatible_server/)
- [llama.cpp server README](https://github.com/ggml-org/llama.cpp/blob/master/tools/server/README.md)

---
*Architecture research for: AI inference gateway with local GPU + cloud failover + auto-provisioning*
*Researched: 2026-04-17*
