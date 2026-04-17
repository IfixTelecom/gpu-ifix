# Feature Research

**Domain:** AI inference gateway (multi-tenant, failover, GPU-aware load shedding)
**Researched:** 2026-04-17
**Confidence:** HIGH

## Feature Landscape

Research sources: LiteLLM Proxy, Portkey, Bifrost, Cloudflare AI Gateway, Helicone, OneAPI, GKE Inference Gateway (llm-d 0.3), vLLM metrics docs, llama.cpp server metrics docs, NVIDIA DCGM exporter. Our use case is narrower (single org, ~6 apps, fixed Qwen + Whisper + BGE-M3 stack) but the feature grammar is industry-standard.

### Table Stakes (Users Expect These)

Features our apps (ConverseAI v4, Chat Ifix, etc.) already get from OpenAI/Anthropic/OpenRouter today. If the gateway lacks these, integration fails because apps were built assuming they exist.

| Feature | Why Expected | Complexity | Notes |
|---------|--------------|------------|-------|
| **OpenAI-compatible endpoints** (`/v1/chat/completions`, `/v1/embeddings`, `/v1/audio/transcriptions`) | Every client app uses `openai` SDK or compatible; swapping `base_url` must work | MEDIUM | Must also accept all OpenAI request fields (temperature, top_p, max_tokens, stop, seed, response_format JSON mode, user) and preserve them on forward. Unknown fields = pass through to upstream. |
| **API key authentication** via `Authorization: Bearer <key>` | Standard OpenAI SDK header; must work with zero client tweaks | LOW | Store `sha256(key)` in Postgres (never plaintext); prefix `ifix_` for visual recognition; support key rotation without downtime (accept old+new during grace window). |
| **Streaming responses (SSE)** for chat completions | Chat UI blocks without streaming; every consumer already uses `stream: true` | MEDIUM | SSE framing: `data: {...}\n\n` + `data: [DONE]\n\n`. Must flush immediately (no buffering). Skip empty/comment events per W3C SSE spec to avoid client parse errors. |
| **Tool/function calling pass-through** | Agents in ConverseAI v4 rely on tool calls; gateway must not strip `tools` / `tool_choice` / `tool_calls` fields | MEDIUM | Pass through verbatim in both directions. In streaming mode, `tool_calls` deltas arrive incrementally — forward without rewriting. Validate Qwen 3.5 27B supports tool calls in the format llama.cpp exposes. |
| **Model alias mapping** (`qwen3-27b` → llama.cpp port 8000; `whisper-1` → FastAPI 8001) | Apps configured with friendly names; gateway decides backend | LOW | Stored in Postgres `model_aliases` table. Alias + tenant scope (some apps may override global alias). Used also for fallback target selection. |
| **Multi-provider fallback chain** (primary GPU → emergency pod → OpenRouter/OpenAI) | Core value of this project; every production gateway (Portkey, LiteLLM, Cloudflare AI Gateway) supports this | MEDIUM | Declarative chain per model. On 5xx / timeout / circuit-open, attempt next target with identical payload. Preserve user-facing latency by capping total attempts (e.g., max 3 hops, 60s budget). |
| **Automatic retries with exponential backoff** | Transient 5xx + network blips expected; apps don't retry themselves today | LOW | 3 attempts max, base 500ms, jitter. Only retry on: connection errors, 408, 429 (respect `Retry-After`), 500, 502, 503, 504. Never retry 4xx other than 408/429. |
| **Circuit breaker per backend** | Prevents retry storms killing a degraded backend; standard per Portkey/LiteLLM/Kong docs | MEDIUM | States: CLOSED → OPEN (5 consecutive failures) → HALF-OPEN (60s cooldown, 1 probe) → CLOSED. State in Redis (shared across gateway replicas). Probe = cheap request (1-token completion or `/health`). |
| **Rate limiting per API key** (requests/min + tokens/min) | Every gateway does this; prevents single app draining quota for others | MEDIUM | Token bucket in Redis (INCR with TTL). Two counters: RPM + TPM. Return 429 with `Retry-After` and `X-RateLimit-*` headers matching OpenAI format. |
| **Health check endpoints** (`/health`, `/ready`) | Portainer liveness + our own alerting depend on it | LOW | `/health` = gateway up (always 200 if process alive); `/ready` = can actually serve (checks at least one backend reachable per model family). |
| **Request/response logging** (audit trail) | Multi-tenant billing + debug require per-request trace; required by every gateway | MEDIUM | Async write to Postgres: `request_id`, `tenant_id`, `model`, `backend_served`, `prompt_tokens`, `completion_tokens`, `latency_ms`, `status_code`, `fallback_used`, `cost_usd`. Don't log prompt content by default (opt-in per tenant to reduce Postgres size + leak risk). |
| **Token counting** on both directions | Needed for cost attribution + TPM rate limit + quotas | MEDIUM | For Qwen local: count via `llama.cpp` tokenizer (exposed at `/tokenize` or compute locally with matching tokenizer). For OpenAI/OpenRouter: use `usage` object they return. For embeddings: input token count = tokenize(input). For STT: billed by audio seconds, not tokens — track separately. |
| **Cost attribution per request** | Apps need to know their spend; finance needs per-app breakdown | MEDIUM | Compute USD = tokens × price_table[backend][model][direction]. Store in log row. Free for GPU-served requests but still compute a *notional* cost (tokens × OpenRouter equivalent price) to compare "what we'd pay without GPU". |
| **Usage quotas per tenant** (daily/monthly budget in USD or tokens) | Prevent runaway cost on single misbehaving app | MEDIUM | Daily bucket in Redis + monthly bucket persisted in Postgres. Check on every request. Enforce soft (alert) + hard (block with 429 `quota_exceeded`) thresholds. |
| **Basic metrics** (Prometheus `/metrics`) | Standard for any Go service; alertmanager / dashboard scraping | LOW | Counters: `requests_total{tenant, model, backend, status}`, `tokens_total{tenant, model, direction}`. Histograms: `request_duration_seconds`. Gauges: `circuit_breaker_state`, `backend_healthy`. Keep cardinality bounded (no `request_id` labels). |
| **Request ID / correlation ID** | Debugging multi-hop failures (app → gateway → OpenRouter) requires traceable IDs | LOW | Generate UUID v4 per request. Echo in `X-Request-Id` response header. Log at every stage. Accept incoming `X-Request-Id` if client provides (for distributed tracing). |
| **Error response format matching OpenAI** (`{error: {message, type, code}}`) | Client SDKs parse this shape; non-compliance breaks error handling | LOW | Mimic OpenAI errors: `authentication_error`, `rate_limit_exceeded`, `model_not_found`, `insufficient_quota`, `server_error`. Include `request_id` in every error. |
| **Configuration hot-reload** (API keys, quotas, model aliases without restart) | Operational necessity: can't restart gateway to rotate a key | MEDIUM | Read from Postgres with short TTL cache (30s) in-memory. Admin mutations go to Postgres + publish Redis pub/sub for immediate invalidation. |
| **CORS headers** (if apps call directly from browser) | ConverseAI v4 web may call gateway from browser; voice-api less likely | LOW | Actually: prefer NO direct browser calls (API keys would leak). All apps should proxy server-side. Skip CORS in v1 — simpler and safer. |

### Differentiators (Competitive Advantage — Why Build vs Adopt LiteLLM)

These are the features that justify writing ifix-ai-gateway in Go instead of dropping in LiteLLM. Each one is either absent from off-the-shelf gateways, or our specific context (own GPU + Vast.ai) lets us do it better.

| Feature | Value Proposition | Complexity | Notes |
|---------|-------------------|------------|-------|
| **Load shedding by GPU saturation** (proactive, before hard failure) | Primary value prop — off-the-shelf gateways only react to failures, not to "about to fail" | HIGH | See "Load-Shedding Deep Dive" section below. Multi-signal: llama.cpp `n_busy_slots_per_decode`, queue depth, vLLM-style `kv_cache_usage_perc` equivalent, GPU util from DCGM/nvidia-smi. Threshold-based divert. |
| **Auto-provisioning emergency Vast.ai pod** | Unique to us — infrastructure failover, not just API failover | HIGH | Trigger: primary circuit OPEN for >60s OR sustained load-shed >5min. Vast.ai API (search offers, filter by GPU=4090, max price $0.40/h), rent, boot stack via cloud-init or image, register as backend target, route to it. Guardrails: max 1 emergency pod concurrent, total daily spend cap. |
| **Automatic cutback to primary** after recovery | Pair with emergency pod spin-up — otherwise emergency becomes permanent cost | MEDIUM | Primary `/health` green for 5 min + queue depth < threshold → mark primary as preferred → drain emergency (no new requests) → wait 5min grace for in-flight → terminate Vast.ai pod (API call) → log event. |
| **Schedule-based routing** (peak 08-22h on GPU; off-peak → OpenRouter) | Unique mode for our cost-sensitive apps; reduces GPU rental to pico hours only if desired | MEDIUM | Per-tenant config: `mode: "24-7" | "peak-off-peak"`, `peak_window: {start, end, tz}`. Cron-like check on each request. Off-peak = skip GPU, go straight to external. Needs timezone awareness (America/Sao_Paulo per CLAUDE.md). |
| **Cost-aware routing** (knapsack: tokens/s per $) | For STT specifically: Whisper self-hosted is free during GPU idle but slower; OpenAI Whisper API is $0.006/min but faster | LOW | Simple policy: if queue depth 0 AND audio <5min → route to GPU; else route to OpenAI API. Decoupled from failover logic — purely an optimization. |
| **Per-app cost attribution with "phantom cost"** (what we'd pay without GPU) | Executive reporting: "GPU saved us $X this month" | LOW | Build on top of token counting. Every GPU-served request gets both `actual_cost_usd: 0` and `external_equivalent_cost_usd: tokens × openrouter_price`. Dashboard aggregates monthly savings. |
| **Audio chunking for long Whisper requests** | Whisper has 30s internal window; long calls (phone recordings from Telefonia) fail without client-side chunking | MEDIUM | Split on silence when possible (webrtcvad or energy threshold), fallback to fixed 28s chunks with 2s overlap. Process chunks in parallel (bounded — GPU has limits). Stitch transcripts (dedupe overlap using longest-common-prefix). Return in OpenAI format with concatenated text + merged timestamps. |
| **Embedding batching** (collect requests, submit in batches) | BGE-M3 throughput scales linearly with batch up to GPU limit; individual calls waste ~80% of GPU | MEDIUM | Server-side micro-batching: accept request, park in batcher, flush when (size >= 32 OR wait > 50ms). Return each caller their slice. Recommended batch sizes per sentence-transformers docs: 32 for BGE-M3 on 4090. |
| **Fallback chain 3-levels deep** (primary GPU → emergency Vast.ai → external API) | Most gateways support 1-2 levels; we need 3 because the primary AND emergency could both fail (bad weekend) | LOW | Declarative chain per model in config. Timeout budget split across levels (e.g., 15s primary / 15s emergency / 30s external for chat). |
| **Real-time alerts via WhatsApp + email** (not Slack/Grafana) | Team lives on WhatsApp; Grafana requires extra infra to run and monitor | MEDIUM | Piggyback on existing ConverseAI infra? Or direct Brevo SMTP (email) + Twilio/direct-Z-API for WhatsApp. Alert triggers: primary circuit OPEN, emergency pod booted, daily spend > threshold, quota breach per tenant. Rate-limit alerts (don't spam on flap). |
| **Own dashboard (Next.js)** with live metrics per tenant | Saves ops cost of Grafana+Prometheus stack; single page for the 6 apps | MEDIUM | Pages: Overview (live requests/s, GPU util, active failover), Per-app (quota used, cost MTD, error rate), Admin (keys, aliases, quotas, modes). Pulls from Postgres + Redis. No need for full observability platform. |
| **Request shadowing for canary/migration** (send 1% to OpenRouter to compare quality) | When validating the primary Qwen vs OpenRouter Qwen produce similar outputs | LOW | Opt-in flag per route: send request to both, return primary, log both responses. Async diff job computes semantic similarity / length / cost delta. Out of scope for v1 but cheap to add later. |
| **Prompt/response caching** (exact match) | LLM traffic from Cobranças/Campanhas likely has repeated templated prompts (same offer to many customers) | LOW | Hash `(model, messages_normalized, temperature=0, max_tokens, tools)`. Store in Redis with TTL (e.g., 1h). Only for `temperature=0` requests (deterministic). Huge win for template-based apps. |
| **Multi-tenant config with operation modes** (24/7 vs peak/off-peak) per tenant | Each Ifix app has different economics: voice-api needs 24/7 reliability, Campanhas only runs 09-18h | LOW | Stored in `tenants` table. Validated on API key auth. Drives schedule-based router. |

### Anti-Features (Deliberately NOT Building in v1)

Features that every "enterprise AI gateway" brochure lists but would dilute our focus or are handled better elsewhere.

| Feature | Why Requested | Why Problematic | Alternative |
|---------|---------------|-----------------|-------------|
| **PII redaction / data masking** | "LLM gateways should scrub sensitive data" (Portkey, LiteLLM both have this) | Each app has different PII shapes (billing data, phone numbers, customer names); centralized redaction is lossy and wrong. Also requires another model (Presidio, LLM-based) → adds latency and cost. | Apps redact before sending. Gateway logs only metadata (token counts), not prompt text by default. Per-tenant opt-in to log prompts for debugging. |
| **Semantic caching** (vs exact caching) | Portkey / Helicone showcase 95% cost reduction | Threshold tuning is notoriously unreliable (research shows 0.85-0.92 "grey zone" where correct and incorrect hits overlap). Requires a vector DB (pgvector or Redis Vector) + embedding compute on every request. Risk of returning wrong answer to customer. | Exact-match caching only in v1. Revisit after observing actual prompt patterns — if 70%+ of traffic is identical, exact match is sufficient; if diverse, semantic cost > benefit. |
| **Advanced prompt engineering / templates / prompt registry** | Portkey, LangSmith, LangFuse all have prompt management | Not a gateway concern. Couples deployment of prompt changes to gateway deployment. Each app team owns their prompts. | Apps manage their own prompts. Gateway is transport. |
| **Multi-region deployment** (geo failover) | "Production gateways have US/EU/APAC" | Zero Ifix apps outside Brazil. Adds DNS, replication, consistency concerns with no payoff. | Single region (current VPS). Revisit if Ifix expands internationally. |
| **SSO (SAML, OIDC) for admin console** | Enterprise gateways have this | 3-4 admin users total. Password + 2FA is more than enough. | Better Auth (from converseai-v4 pattern) for admin console with basic password + TOTP. No SAML. |
| **Granular RBAC** (role-based permissions for admins) | "Separation of duties" | Tiny team, single admin role suffices. | Single admin role in v1. Add "read-only viewer" role if non-admin users need to see dashboard. |
| **Guardrails** (content moderation, jailbreak detection, hallucination scoring) | Portkey/Bifrost feature this prominently | Apps like ConverseAI agents need custom guardrails already (customer-specific); a generic moderation layer adds latency (50-200ms per check) and false positives. | Apps implement domain-specific validation. Gateway provides output to app; app decides if it's acceptable. |
| **Prompt caching at provider level** (OpenAI/Anthropic cache_control) | Saves cost on repeated system prompts | Qwen on llama.cpp doesn't support explicit cache_control; automatic prefix caching already exists in llama.cpp internals. OpenRouter passes it through. | Pass `cache_control` fields through to external providers (no gateway logic). Rely on llama.cpp's automatic prefix caching for GPU path. Not a v1 feature to build. |
| **Model catalog UI / fine-tuning pipeline** | "Self-service model deployment" | We have exactly 3 models, fixed. No fine-tuning in scope. | Model aliases in DB — static config. If a 4th model joins, add an alias row and restart (or hot-reload). |
| **Cross-language SDKs** (Python, TS, Go client libraries) | Portkey/LiteLLM ship their own SDKs | Apps use the official `openai` SDK with changed `base_url`. That's the whole point of OpenAI compatibility. | No SDK. `base_url` + `api_key` is the interface. Document it in README. |
| **OpenTelemetry / distributed tracing** | Modern observability | Adds 10-20% latency (if synchronous) or complexity (if async). Pino + structured logs + correlation IDs give 90% of the value. | `X-Request-Id` propagation + structured logs with trace IDs. Add OTel only if we start losing debugging ability. |
| **Response transformation / format conversion** (Chat → Responses API, Anthropic ↔ OpenAI) | LiteLLM's killer feature — 140+ provider normalizations | We have 3 known providers + fixed request shape (OpenAI format). No transformation needed. | Pass-through only. Keep OpenAI shape end-to-end. |

## Load-Shedding Deep Dive (GPU Saturation → External)

This is the key differentiator and deserves dedicated research. Question asked: "how do production gateways do this? What metrics do they probe?"

### How Production Systems Do It

**Pattern 1: Queue-depth based (vLLM / llm-d / GKE Inference Gateway)** — HIGH confidence, verified via vLLM Prometheus docs and llm-d 0.3 release notes.

GKE Inference Gateway and llm-d both expose metrics from the backend model server and route on those. The dominant signals are:

- `vllm:num_requests_waiting` — requests queued, cannot start yet. **This is the primary saturation signal.** If > 0 consistently → backend is overloaded.
- `vllm:num_requests_running` — currently executing.
- `vllm:gpu_cache_usage_perc` — KV-cache fill. Healthy < 0.90; > 0.95 means next request may get evicted or rejected.

**Pattern 2: Token-speed based** — measure `time_to_first_token_seconds` (TTFT) p95. If p95 > SLO (e.g., 2s for chat), start shedding.

**Pattern 3: External GPU metrics (DCGM)** — `DCGM_FI_DEV_GPU_UTIL` (GPU %), `DCGM_FI_PROF_SM_ACTIVE` (SM active cycles ratio), `DCGM_FI_DEV_FB_USED` (VRAM used). GPU util alone is **misleading** — 99% util can be healthy throughput; 50% util can coexist with queue backup. Best used as a *tie-breaker* on top of queue depth.

### What's Available in OUR Stack

The project uses **llama.cpp server** for LLM (Qwen) and **FastAPI custom servers** for Whisper + BGE-M3.

**llama.cpp server (`--metrics` flag):**
- `llamacpp:n_busy_slots_per_decode` — average busy slots per decode step. Divide by total slots to get utilization ratio.
- `llamacpp:prompt_tokens_total`, `llamacpp:tokens_predicted_total` — counters (compute rate yourself).
- `/slots` endpoint (default on) — per-slot state: running/idle, prompt length, processed tokens. Poll this for real-time depth.

llama.cpp does **not** expose a direct "num_requests_waiting" or "kv_cache_usage_perc" metric in its 2026 stable builds per the project's Prometheus metrics doc. Workarounds:
1. Poll `/slots` — count slots with state=processing vs idle.
2. Track our own inflight counter in the gateway (increment on dispatch, decrement on response).
3. Use HTTP connection count / 503s returned by llama.cpp as a crude signal.

**FastAPI servers (Whisper, BGE-M3):** no built-in metrics unless we instrument them. Gateway-side inflight tracking is the fallback.

**DCGM / nvidia-smi:** available on Vast.ai host (or via `nvidia-smi --query-gpu=utilization.gpu,memory.used --format=csv`). Scrape from gateway via SSH/SDK or run DCGM exporter in the GPU container.

### Recommended Load-Shedding Algorithm

Given the stack, the **realistic** approach is gateway-side inflight tracking + optional llama.cpp `/slots` poll:

```
State (per backend, per model):
  inflight          = int counter (atomic, incremented on dispatch)
  recent_latency_ms = ring buffer of last 20 request durations
  last_health_at    = timestamp of last /health 200

Decision on new request:
  IF last_health_at older than 30s → fail-over (circuit treats as OPEN)
  IF inflight >= SHED_HARD_THRESHOLD (e.g., 8 for chat; 16 for embeddings) → shed
  IF inflight >= SHED_SOFT_THRESHOLD (e.g., 6 for chat) AND p95(recent_latency_ms) > SLO → shed
  IF shed:
      log event (tenant, model, reason="saturation")
      route to next fallback target (OpenRouter / OpenAI)
  ELSE dispatch to GPU backend

Thresholds are per-model, configurable per-tenant.
```

**Key design decisions:**
- **Gateway-side inflight** is sufficient and simpler than scraping llama.cpp `/metrics`. Counter in Go sync/atomic. Reset on backend change.
- **Latency-based shed (p95 > SLO)** catches slow-but-not-yet-queued cases.
- **No separate "load shedder" service** — it's a few lines in the dispatch path.
- **DCGM scrape is optional** — add later if inflight/latency are insufficient. More complex because it needs access to GPU host (Vast.ai SSH).

**Why not queue-depth scraping from llama.cpp `/slots`?**
- Adds latency to every request (extra HTTP round-trip) OR runs on a 1s poll loop that can be stale.
- Inflight counter at gateway is always perfectly accurate for requests *this gateway* dispatched.
- If we run multiple gateway replicas in v2, then we'd need to aggregate — but for v1 single-replica this is exact.

**PROJECT.md says:** "Detecção de saturação por GPU util/VRAM (não queue depth)". This is a reasonable decision-level statement but needs refinement: GPU util alone is a poor signal (see Pattern 3 above). Recommend combining **inflight (primary) + GPU util (secondary sanity check)**. Flag for phase-level research / validation with team.

## Feature Dependencies

```
[OpenAI-compatible endpoints]
    └──requires──> [Model alias mapping]
    └──requires──> [Streaming support (SSE)]

[Multi-provider fallback chain]
    └──requires──> [Circuit breaker per backend]
    └──requires──> [Automatic retries with backoff]
    └──enhances──> [Request ID / correlation ID]

[Load shedding by GPU saturation]
    └──requires──> [Basic metrics (inflight counter)]
    └──requires──> [Multi-provider fallback chain] (needs a target to shed to)

[Auto-provisioning emergency Vast.ai pod]
    └──requires──> [Circuit breaker] (trigger signal)
    └──requires──> [Health check endpoints] (to know when to cut back)
    └──requires──> [Request/response logging] (for audit of spend on emergency pod)

[Automatic cutback to primary]
    └──requires──> [Auto-provisioning emergency pod] (obvious)
    └──requires──> [Health check endpoints] (dwell time on healthy)

[Schedule-based routing (peak/off-peak)]
    └──requires──> [Multi-tenant config with operation modes]
    └──enhances──> [Multi-provider fallback chain] (off-peak target = OpenRouter)

[Cost attribution per request]
    └──requires──> [Token counting]
    └──requires──> [Request/response logging]
    └──enhances──> [Usage quotas per tenant]

[Usage quotas per tenant]
    └──requires──> [API key authentication]
    └──requires──> [Cost attribution or token counting]
    └──requires──> [Rate limiting per key] (shared Redis infrastructure)

[Own dashboard (Next.js)]
    └──requires──> [Request/response logging] (source data)
    └──requires──> [Basic metrics] (live counters)
    └──requires──> [API key authentication] (admin vs viewer)

[Audio chunking for long Whisper]
    └──enhances──> [OpenAI-compatible endpoints] (audio/transcriptions)
    └──conflicts──> None

[Embedding batching]
    └──enhances──> [OpenAI-compatible endpoints] (embeddings)
    └──conflicts──> [Request ID propagation] — multiple requests share one backend call; each must get its own correlation ID logged

[Real-time alerts]
    └──requires──> [Basic metrics] (thresholds to alert on)
    └──requires──> [Request/response logging] (to attribute alert to tenant)

[Prompt/response caching (exact match)]
    └──requires──> [Redis] (already required for rate-limit + circuit-breaker)
    └──conflicts──> [Request shadowing] — cached responses skip the canary path
```

### Dependency Notes

- **Load shedding requires a fallback target.** Don't build load-shedding first; build fallback chain, then add the shedding heuristic on top.
- **Emergency pod spin-up requires circuit breaker signal as trigger.** The circuit must be implemented and tested before wiring it to Vast.ai API.
- **Cutback to primary requires health dwell time.** Implement health checks as a feature before auto-cutback can exist.
- **Cost attribution blocks quota enforcement.** You can't enforce "$10/day" without knowing per-request cost.
- **Embedding batching changes per-request semantics.** Log correlation IDs must survive the batch boundary; this is the main implementation hazard.

## MVP Definition

### Launch With (v1) — Minimum to have a working gateway replacing current direct API calls

- [ ] **OpenAI-compatible endpoints** (chat, embeddings, transcriptions) — without this, no app can integrate
- [ ] **API key authentication** per tenant — multi-tenant is core requirement
- [ ] **Streaming SSE for chat** — ConverseAI v4 chat UI depends on it
- [ ] **Tool/function calling pass-through** — agents depend on it
- [ ] **Model alias mapping** — so apps use friendly names
- [ ] **Multi-provider fallback chain** (primary GPU → OpenRouter / OpenAI) — core value prop
- [ ] **Automatic retries with exponential backoff** — transient-failure resilience
- [ ] **Circuit breaker per backend** — stop hammering a dead GPU
- [ ] **Rate limiting per API key** (RPM + TPM) — protect backends from single-app bursts
- [ ] **Health check endpoints** (`/health`, `/ready`) — deployability
- [ ] **Request/response logging** (async to Postgres) — audit + billing + debug
- [ ] **Token counting** — required for logging + quotas
- [ ] **Cost attribution per request** — monthly reporting
- [ ] **Usage quotas per tenant** — runaway cost protection
- [ ] **Basic metrics** (Prometheus `/metrics`) — basic observability
- [ ] **Request ID / correlation ID** — debuggability
- [ ] **Error response format matching OpenAI** — compatibility
- [ ] **Load shedding by GPU saturation** (inflight-based) — core differentiator
- [ ] **Schedule-based routing** (24/7 vs peak/off-peak) — core operational mode per PROJECT.md
- [ ] **Own dashboard (Next.js)** with overview + per-app breakdown — ops visibility
- [ ] **Real-time alerts via WhatsApp + email** — ops response

### Add After Validation (v1.x — once v1 is in production and we've learned)

- [ ] **Auto-provisioning emergency Vast.ai pod** — defer until we've observed primary's real failure rate; might not be needed daily
- [ ] **Automatic cutback to primary after emergency** — paired with previous
- [ ] **Audio chunking for long Whisper** — add when Telefonia/NextBilling hits 10+ min recordings
- [ ] **Embedding batching** — add if embedding traffic is high enough to matter (Cobranças use case)
- [ ] **Prompt/response caching (exact match)** — add if Campanhas traffic shows template repetition
- [ ] **Configuration hot-reload without restart** — operational nice-to-have, initially restart on change is fine
- [ ] **Cost-aware routing** (STT: short+idle → GPU, else → OpenAI) — optimization

### Future Consideration (v2+)

- [ ] **Request shadowing for canary** — only if we need to validate Qwen output quality vs OpenRouter
- [ ] **Multiple gateway replicas with shared state** — only if single VPS insufficient
- [ ] **Prompt engineering / templates** — rejected as anti-feature; revisit only if apps explicitly request
- [ ] **DCGM-based GPU util metrics** — supplement to inflight-based shedding if inflight proves insufficient
- [ ] **More model families** (Llama, Mixtral) — rejected per Out of Scope in PROJECT.md

## Feature Prioritization Matrix

| Feature | User Value | Implementation Cost | Priority |
|---------|------------|---------------------|----------|
| OpenAI-compatible endpoints | HIGH | MEDIUM | P1 |
| API key authentication | HIGH | LOW | P1 |
| Streaming SSE | HIGH | MEDIUM | P1 |
| Tool calling pass-through | HIGH | MEDIUM | P1 |
| Multi-provider fallback chain | HIGH | MEDIUM | P1 |
| Automatic retries | HIGH | LOW | P1 |
| Circuit breaker | HIGH | MEDIUM | P1 |
| Rate limiting per key | HIGH | MEDIUM | P1 |
| Request/response logging | HIGH | MEDIUM | P1 |
| Token counting | HIGH | MEDIUM | P1 |
| Cost attribution | HIGH | MEDIUM | P1 |
| Usage quotas | HIGH | MEDIUM | P1 |
| Load shedding by GPU saturation | HIGH | HIGH | P1 |
| Schedule-based routing | HIGH | MEDIUM | P1 |
| Own dashboard | HIGH | MEDIUM | P1 |
| Real-time alerts | HIGH | MEDIUM | P1 |
| Basic metrics | MEDIUM | LOW | P1 |
| Health checks | MEDIUM | LOW | P1 |
| Request ID | MEDIUM | LOW | P1 |
| Model alias mapping | MEDIUM | LOW | P1 |
| Error format compat | MEDIUM | LOW | P1 |
| Auto-emergency pod spin-up | HIGH | HIGH | P2 |
| Automatic cutback | HIGH | MEDIUM | P2 |
| Audio chunking | MEDIUM | MEDIUM | P2 |
| Embedding batching | MEDIUM | MEDIUM | P2 |
| Prompt/response caching | MEDIUM | LOW | P2 |
| Cost-aware routing | LOW | LOW | P2 |
| Config hot-reload | MEDIUM | MEDIUM | P2 |
| Request shadowing | LOW | LOW | P3 |
| DCGM metrics supplement | LOW | MEDIUM | P3 |
| PII redaction | — | — | Anti (do not build) |
| Semantic caching | — | — | Anti (do not build) |
| SSO / advanced RBAC | — | — | Anti (do not build) |
| Guardrails | — | — | Anti (do not build) |
| Multi-region | — | — | Anti (do not build) |
| Response transformation | — | — | Anti (do not build) |

**Priority key:**
- P1: Must have for launch (21 features)
- P2: Should have, add after validation (7 features)
- P3: Nice to have, v2+ (2 features)

## Competitor Feature Analysis

| Feature | LiteLLM Proxy | Portkey | Bifrost | Cloudflare AI Gateway | Helicone | Our Approach |
|---------|---------------|---------|---------|------------------------|----------|--------------|
| OpenAI-compatible API | Yes | Yes | Yes | Yes | Yes | Yes (required for zero-touch app integration) |
| Multi-provider fallback | Yes | Yes (composable chains) | Yes (governance) | Yes (auto-route on failure) | Yes | Yes (3-level: GPU → Vast.ai emergency → external) |
| Circuit breaker | Via retry config | Yes | Yes | Implicit | Yes | Yes, explicit per-backend, state in Redis |
| Rate limiting per key | Yes (virtual keys) | Yes | Yes | Yes | Yes | Yes (RPM + TPM) |
| Token counting | Yes | Yes | Yes | Yes | Yes | Yes |
| Cost tracking per tenant | Yes | Yes (team budgets) | Yes (virtual keys) | Yes (analytics) | Yes | Yes + phantom cost (what we'd pay without GPU) |
| Semantic cache | Yes | Yes (marquee feature) | Yes | No (exact only) | Yes | **No** (anti-feature — threshold unreliability) |
| Prompt caching (exact) | Yes | Yes | Yes | Yes | Yes (up to 95% cost reduction) | Yes (P2, temperature=0 only) |
| GPU saturation-aware routing | **No** | **No** (provider-level only) | Adaptive LB (provider health, not GPU) | **No** (no self-hosted context) | **No** | **Yes (our differentiator)** |
| Auto-provision infra on failure | **No** | **No** | **No** | **No** (managed) | **No** | **Yes (Vast.ai API integration)** |
| Schedule-based routing | Custom routing | Conditional routing | Conditional | **No** | **No** | Yes (native per-tenant mode) |
| Audio chunking for Whisper | Partial (forwards to provider) | Partial | Partial | Partial | Partial | **Yes (gateway-side chunking)** |
| Embedding batching | **No** (per-request) | **No** | **No** | **No** | **No** | **Yes (micro-batching window)** |
| Self-hosted | Yes (Python, GIL-limited) | Yes (OSS gateway) | Yes (Go) | No (Cloudflare-managed only) | Yes (Rust) | Yes (Go, matches Bifrost approach) |
| Performance ceiling | ~250-300 RPS/instance | N/A (managed) | 5000+ RPS, ~11µs overhead | N/A (edge network) | Competitive (Rust) | Target ~500 RPS on 4 vCPU (well above our 6-app load) |
| PII redaction | Yes | Yes | Yes (guardrails) | No | Yes | **No (anti-feature)** |

**Takeaway:** Our differentiators are the infra-aware features (GPU saturation, auto-spin-up, schedule). Off-the-shelf gateways treat providers as opaque black boxes. We control our primary and can do infrastructure-level routing, not just API-level routing. LiteLLM + Portkey have the best coverage for "generic" gateway needs but **none** of them know or care that our primary is a rented 4090 that sometimes saturates on sustained load.

## Sources

**Primary gateway products analyzed:**
- [LiteLLM Proxy GitHub](https://github.com/BerriAI/litellm) — HIGH confidence (official)
- [LiteLLM proxy docs](https://docs.litellm.ai/docs/simple_proxy) — HIGH
- [LiteLLM Review 2026 (TrueFoundry)](https://www.truefoundry.com/blog/a-detailed-litellm-review-features-pricing-pros-and-cons-2026) — MEDIUM (vendor blog)
- [Portkey Gateway GitHub](https://github.com/Portkey-AI/gateway) — HIGH
- [Portkey fallbacks docs](https://portkey.ai/docs/product/ai-gateway/fallbacks) — HIGH
- [Portkey features page](https://portkey.ai/features/ai-gateway) — HIGH
- [Bifrost GitHub](https://github.com/maximhq/bifrost) — HIGH
- [Bifrost vs LiteLLM (TrueFoundry)](https://www.truefoundry.com/blog/bifrost-vs-litellm) — MEDIUM
- [Cloudflare AI Gateway overview](https://developers.cloudflare.com/ai-gateway/) — HIGH (official)
- [Cloudflare AI Gateway features](https://developers.cloudflare.com/ai-gateway/features/) — HIGH
- [Helicone AI Gateway GitHub](https://github.com/Helicone/ai-gateway) — HIGH
- [Helicone prompt caching docs](https://docs.helicone.ai/gateway/concepts/prompt-caching) — HIGH
- [OneAPI GitHub](https://github.com/songquanpeng/one-api) — HIGH

**GPU saturation / load shedding:**
- [vLLM metrics docs](https://docs.vllm.ai/en/stable/design/metrics/) — HIGH (official)
- [vLLM Prometheus Metrics (NVIDIA Dynamo docs)](https://docs.nvidia.com/dynamo/latest/backends/vllm/prometheus.html) — HIGH
- [Monitor LLM Inference in Production 2026 (Glukhov)](https://www.glukhov.org/observability/monitoring-llm-inference-prometheus-grafana/) — MEDIUM
- [llm-d 0.3 blog](https://llm-d.ai/blog/llm-d-v0.3-expanded-hardware-faster-perf-and-igw-ga) — HIGH (official)
- [GKE Inference Gateway (Medium)](https://medium.com/google-cloud/inference-gateway-intelligent-load-balancing-for-llms-on-gke-6a7c1f46a59c) — MEDIUM (Google Cloud community)
- [NVIDIA DCGM Exporter](https://github.com/NVIDIA/dcgm-exporter) — HIGH (official)
- [Understanding GPU Performance: Utilization vs Saturation (arthurchiao)](https://arthurchiao.art/blog/understanding-gpu-performance/) — MEDIUM
- [llama.cpp server README](https://github.com/ggml-org/llama.cpp/blob/master/tools/server/README.md) — HIGH (official)
- [llama.cpp /metrics discussion #10325](https://github.com/ggml-org/llama.cpp/discussions/10325) — MEDIUM

**Specific feature patterns:**
- [Circuit breaker in LLM apps (Portkey blog)](https://portkey.ai/blog/retries-fallbacks-and-circuit-breakers-in-llm-apps/) — MEDIUM
- [Semantic caching pitfalls (TianPan)](https://tianpan.co/blog/2026-04-10-semantic-caching-llm-production) — MEDIUM
- [Whisper large-v3 HuggingFace card](https://huggingface.co/openai/whisper-large-v3) — HIGH
- [Whisper audio chunking (saytowords)](https://www.saytowords.com/en/blogs/Whisper-Audio-Chunking/) — MEDIUM
- [BGE-M3 HuggingFace card](https://huggingface.co/BAAI/bge-m3) — HIGH
- [Sentence Transformers efficiency docs](https://sbert.net/docs/sentence_transformer/usage/efficiency.html) — HIGH
- [Idempotency in API Gateways (Tyk)](https://tyk.io/blog/implementing-idempotency-protection-in-api-gateways/) — MEDIUM
- [Hierarchical budget controls for multi-tenant LLM gateway (DEV)](https://dev.to/pranay_batta/building-hierarchical-budget-controls-for-multi-tenant-llm-gateways-ceo) — MEDIUM
- [Traceloop: tracking LLM token usage per user](https://www.traceloop.com/blog/from-bills-to-budgets-how-to-track-llm-token-usage-and-cost-per-user) — MEDIUM
- [Portkey: tracking LLM token usage across providers/teams](https://portkey.ai/blog/tracking-llm-token-usage-across-providers-teams-and-workloads/) — MEDIUM

---
*Feature research for: ifix-ai-gateway (AI inference gateway, multi-tenant, GPU-aware failover)*
*Researched: 2026-04-17*
