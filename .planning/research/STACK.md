# Stack Research

**Domain:** AI Inference Gateway (multi-tenant, OpenAI-compatible, with failover + auto-provisioned emergency GPU)
**Researched:** 2026-04-17
**Confidence:** HIGH (gateway + resilience + Go ecosystem), MEDIUM (Qwen3.5-27B-on-4090 specifics + Vast.ai Go integration)

---

## Executive Decision: Build in Go vs Reuse Existing Gateway

The biggest decision that *precedes* library selection is whether to build a custom Go gateway at all, given that Bifrost, LiteLLM, Portkey, OneAPI and vLLM router already exist.

| Option | Language | Fits Requirements? | Reason |
|--------|----------|-------------------|--------|
| **Build in Go** (PROJECT.md choice) | Go | YES | Full control over Vast.ai auto-provisioning, GPU-util-based load-shedding, 5-min cutback logic, multi-tenant quotas per app, OpenRouter+OpenAI-API+local-pod fallback chain. None of the existing gateways implement "spin up emergency GPU pod via Vast.ai API + tear it down 10 min after primary recovers" natively. |
| Bifrost (Go, Apache 2.0) | Go | PARTIAL | Has automatic failover, virtual keys with budgets, multi-tenant, Go-native performance (11 µs overhead @ 5k RPS). **Missing:** Vast.ai pod lifecycle automation, GPU-util-based load shedding, custom "local pod as provider" (it targets hosted APIs). Could be used as a *library* for provider routing; the spin-up logic would still be custom Go. |
| LiteLLM Proxy (Python) | Python | NO | Python runtime on 4 vCPU VPS wastes headroom; no Vast.ai hooks; the spin-up orchestration would still be external; Python GIL limits throughput under load. |
| Portkey, OneAPI, Kong AI Gateway | various | NO | SaaS/enterprise focus; don't solve GPU-pod lifecycle. |
| vLLM router (Python) | Python | NO | Designed for routing between vLLM instances; no OpenRouter/OpenAI external fallback, no Vast.ai automation. |

**Recommendation:** Build a custom Go gateway, but **steal architecture patterns from Bifrost** (adapter pattern per provider, struct-per-request config, per-key virtual budgets). Do **not** fork Bifrost — the emergency-pod state machine and GPU-util-based load-shedding are the core differentiation; embedding them in a third-party codebase would be a maintenance liability.

Confidence: **HIGH** — requirements (PROJECT.md lines 19–33) explicitly call for Vast.ai automation + invisible failover that no off-the-shelf tool delivers.

---

## Recommended Stack

### Gateway Runtime

| Technology | Version | Purpose | Why |
|------------|---------|---------|-----|
| **Go** | 1.23+ (stdlib `log/slog`, generics mature) | Gateway language | Static binary, 11 µs/request range possible, stdlib HTTP is production-grade. Matches PROJECT.md constraint. |
| **chi** (`github.com/go-chi/chi/v5`) | v5.x (last release Feb 2026) | HTTP router + middleware | Built on `net/http` — zero framework lock-in, composable middleware (per-app API key auth, rate-limit, logging, tenant context). Chi's middlewares ARE `net/http` handlers, so you can plug in Prometheus exporters, OTel wrappers, etc. without adapters. |

**Why chi over Gin/Fiber/Echo:**
- **Gin:** Fine framework, but wraps `net/http` with its own `gin.Context`. For a gateway that's mostly `io.Copy` between an incoming request and an upstream, you want `http.ResponseWriter`/`*http.Request` directly so `httputil.ReverseProxy` and streaming SSE work without impedance.
- **Fiber:** Built on `fasthttp`, NOT `net/http`. Breaks `httputil.ReverseProxy`, `http.Flusher` (SSE streaming), OpenTelemetry HTTP instrumentations. Disqualified for a gateway that *must* stream `text/event-stream` from upstream LLM servers.
- **Echo:** Similar story to Gin but less popular for this exact use case.
- **chi:** Radix-tree router, `net/http` native, zero external deps, production-proven at scale. The right pick for a reverse-proxy-heavy workload.

**Reverse-proxy layer:** Use stdlib `net/http/httputil.ReverseProxy` with a custom `Director` for request rewriting and `ModifyResponse` for error normalization. Do NOT reinvent proxying — `ReverseProxy` handles streaming SSE, WebSockets, `Transfer-Encoding: chunked`, and cancellation propagation correctly.

Confidence: **HIGH**

---

### Inference Servers on the GPU Pod

This is where the existing doc's choices need **critical review**. On a single RTX 4090 24 GB serving LLM + STT + embeddings concurrently, the math is tight.

#### LLM — Qwen 3.5 27B

**Doc says:** llama-cpp-python (Q4_K_M GGUF). **Keep this choice. Do not switch to vLLM.**

| Server | Fits on single RTX 4090 24GB with Qwen3.5-27B + Whisper + BGE-M3? | OpenAI compat | Verdict |
|--------|---------|---------------|---------|
| **llama.cpp `llama-server`** | YES — Q4_K_M weights ≈ 17 GB, leaves headroom for Whisper (~3 GB) + BGE-M3 (~1 GB) + KV cache | Built-in `/v1/chat/completions`, `/v1/completions`, tool-calling via `--jinja` and Qwen3-specific templates | **CHOSEN** |
| vLLM 0.10+ | NO (risky). Qwen3.5-27B-AWQ published launch configs use **tensor-parallel across 2 GPUs**; a single 4090 is "extremely tight and may not work reliably". Additionally there's [a known vLLM bug](https://github.com/vllm-project/vllm/issues/37080) where Qwen3.5 INT4 uses *more* VRAM than FP8 on L40S 48 GB | Full OpenAI API | Rejected — VRAM budget fails |
| SGLang | NO — same VRAM problem as vLLM; and not GGUF-native | Full | Rejected |
| TGI (HuggingFace) | — | Full | **Dead end** — HuggingFace put TGI into maintenance mode on Dec 11, 2025 and officially recommends vLLM or SGLang. Do not adopt for new projects. |
| Ollama | YES (wraps llama.cpp) | Partial (non-standard endpoint names, though `/v1` compat improved in late 2025) | Rejected — you want the raw llama.cpp server to minimize layers, and tool-calling/template control is more direct. |

**Revised recommendation:** Use **`llama.cpp`'s `llama-server` binary** directly (not the `llama-cpp-python` Python wrapper the doc mentions). The C++ binary is the same engine without Python overhead, has a better-maintained OpenAI-compatible HTTP surface, and exposes flags (`--jinja`, `--chat-template-file`, `--parallel`, `--batch-size`) that the Python bindings obscure.

- Image: `ghcr.io/ggml-org/llama.cpp:server-cuda` (official CUDA build)
- Model: `unsloth/Qwen3.5-27B-GGUF:Q4_K_M` (Unsloth's quant is the community standard and gets template fixes first)
- **Critical flags for tool calling:** `--jinja --chat-template-file <patched-qwen3.5-template>` — the stock GGUF template rejects the "developer" role and the `--chat-template chatml` fallback silently kills thinking mode. Plan to carry the patched template ([reference gist](https://gist.github.com/sudoingX/c2facf7d8f7608c65c1024ef3b22d431)) in the repo.

**DEPARTURE FROM PROJECT.md:** `llama-cpp-python` is the Python wrapper around llama.cpp. For a production server use the binary directly. The PROJECT.md "Tech stack — IA" constraint says "Stack do doc" — flag this to the user as a tactical substitution; architecturally identical (same GGUF, same C++ engine), just no Python layer.

Confidence: **HIGH** (VRAM math, TGI deprecation, template issues are all verified)

#### STT — Whisper large-v3

**Doc says:** `faster-whisper`. **Keep faster-whisper but use a server wrapper.**

| Option | Status | Verdict |
|--------|--------|---------|
| **Speaches** (`ghcr.io/speaches-ai/speaches`) | Successor to `fedirz/faster-whisper-server`; active; OpenAI-compatible `/v1/audio/transcriptions` and `/v1/audio/translations`; streaming; also ships TTS (Kokoro/Piper) if you later revive the TTS-on-GPU question | **CHOSEN** — zero custom code needed |
| `faster-whisper` Python + custom FastAPI | Doc's approach | Fallback if Speaches has a show-stopper bug; same engine underneath |
| WhisperX | Adds word-level alignment + speaker diarization, but **no longer actively maintained** (use `BetterWhisperX` fork if features needed) | Rejected — you don't need diarization for Chat Ifix / Telefonia use cases; added complexity + unmaintained |
| `whisper.cpp` | CPU-first, CUDA support added but Python ecosystem is weaker | Rejected — `faster-whisper` via CTranslate2 is ~4× faster than OpenAI Whisper and better supported on CUDA |

Use `large-v3` (or `large-v3-turbo` if latency is critical and you can accept a marginal WER hit for Portuguese).

Confidence: **HIGH**

#### Embeddings — BGE-M3

**Doc says:** `sentence-transformers`. **Switch to Infinity.**

| Option | Status | Verdict |
|--------|--------|---------|
| **Infinity** (`michaelf34/infinity`) | v0.0.77 (Aug 2025), supports BGE-M3 (dense retrieval), FlashAttention, dynamic batching, async tokenization, FP16 — ~2–3× faster than vanilla sentence-transformers on GPU, and ~0.5× VRAM. Ships OpenAI-compatible `/embeddings`. Docker-native | **CHOSEN** |
| `sentence-transformers` + FastAPI | Doc's approach; functional but no batching optimization; 2–3× slower | Rejected for production scale |
| TEI (HuggingFace Text Embeddings Inference) | Mature, Rust-based, good perf | Viable alternative if Infinity has issues; similar tradeoffs |

**Caveat on Infinity + BGE-M3:** per Infinity's own matrix, BGE-M3 is tested but the sparse-vector output is not supported — only dense retrieval. For the Ifix use case (RAG similarity over Portuguese text), dense is what you need. If you later want sparse or multi-vector/ColBERT retrieval, swap to `BGEM3FlagModel` directly.

**DEPARTURE FROM PROJECT.md:** Doc specifies sentence-transformers. Flag this swap — same model file (`BAAI/bge-m3`), different server wrapper, 2–3× throughput improvement with the same VRAM budget.

Confidence: **MEDIUM-HIGH** — Infinity is proven, but the specific BGE-M3 sparse disclaimer means confirm the dense-only mode matches your retrieval quality.

---

### Go Resilience & Control-Plane Libraries

| Library | Version | Purpose | Why |
|---------|---------|---------|-----|
| **`github.com/sony/gobreaker/v2`** | v2 (generics-enabled) | Circuit breaker | Simple API (`Execute(func() (T, error)) (T, error)`), battle-tested at Sony, actively maintained, has a v2 with generics. Ideal for wrapping each upstream (primary-LLM, primary-STT, primary-embed, OpenRouter, OpenAI Whisper, OpenAI embed, emergency-pod). Instantiate one breaker per upstream × per failure-mode. |
| **`github.com/cenkalti/backoff/v5`** | v5 | Exponential backoff + retry with context | Implements `backoff.Retry(op, bo)` with context cancellation, `RetryAfterError` (for honoring OpenRouter `Retry-After` headers), `WithMaxElapsedTime`. The Go standard for retries since v3. |
| **`golang.org/x/time/rate`** | current (stdlib-adjacent, last updated Feb 2026) | Token-bucket rate limiter | Allow/Reserve/Wait API, supports bursts (which is what you want for API keys — a user's first request shouldn't block for a token). Use one `*rate.Limiter` per API key, stored in an LRU. |

**What NOT to use:**
| Library | Why Reject | Use Instead |
|---------|------------|-------------|
| `afex/hystrix-go` | Netflix Hystrix port, **no visible maintenance activity**, Netflix itself deprecated Hystrix for `resilience4j` years ago. Feature-rich (request collapsing, SSE dashboard) but adopting an unmaintained resilience library is a trap. | `sony/gobreaker/v2` |
| `go-resilience` (various forks) | More scope than needed; `gobreaker + backoff + x/time/rate` covers all the patterns separately with less lock-in | Separate, focused libraries |
| `uber-go/ratelimit` | Leaky bucket — *forces* even spacing, no burst allowance. Good for rate-limiting *outbound* calls to OpenRouter to stay under quota, but wrong for inbound per-tenant rate limits where the first request should pass immediately | `x/time/rate` for inbound; `uber-go/ratelimit` only if you need strict-pacing on OpenRouter outbound |
| `didip/tollbooth` | Wraps `x/time/rate` with Gin/Echo-style middleware and extra HTTP-header conveniences. If you pick chi + stdlib, the wrapper overhead isn't worth it — write 15 lines yourself with `x/time/rate` | `x/time/rate` direct |

Confidence: **HIGH**

---

### Data Access (Postgres + Redis)

| Library | Version | Purpose | Why |
|---------|---------|---------|-----|
| **`github.com/jackc/pgx/v5`** | v5 (current, requires Go 1.25+ for latest) | Postgres driver + pool | The de facto Postgres driver in 2026. Native LISTEN/NOTIFY (useful for config hot-reload across gateway replicas), native `COPY`, first-class support for `pgvector`, `pgx/v5/stdlib` adapter available if you ever need `database/sql` compat. Generics support via `pgx.CollectRows(rows, pgx.RowToStructByName[T])`. |
| **`github.com/redis/go-redis/v9`** | v9 | Redis client | Typed API per Redis command (vs. redigo's printf-style), built-in connection pooling, Redis Cluster support, pipeline + transactions + pub/sub. The Redis Labs-recommended Go client. |
| **`github.com/sqlc-dev/sqlc`** (optional) | current | SQL → type-safe Go bindings | If schema is non-trivial (API keys, quotas, per-app budgets, audit logs), sqlc eliminates repetitive `rows.Scan` boilerplate. Pairs natively with pgx v5. Not mandatory — for a small schema, raw pgx is fine. |

**What NOT to use:**
| Library | Why Reject | Use Instead |
|---------|------------|-------------|
| `database/sql` + `lib/pq` | `lib/pq` is in long-term maintenance; pgx is faster and Postgres-native | pgx/v5 directly |
| `gomodule/redigo` | No native connection pooling, no Redis Cluster support, printf-style API | go-redis/v9 |
| GORM | ORM overhead, hides SQL (which you *want* visible in a gateway for auditing), pgx performs better for direct-SQL workloads | pgx + sqlc |

Confidence: **HIGH**

---

### External Provider SDKs (Go)

| Provider | Recommendation | Why |
|----------|----------------|-----|
| **OpenRouter** | **Write a minimal direct HTTP client** (50–100 lines with `net/http`). Do not use an SDK. | The OpenRouter API is OpenAI-compatible at the wire level — `POST /v1/chat/completions` with a different `base_url` + `Authorization: Bearer $OR_KEY` + `HTTP-Referer`/`X-Title` headers. Go SDKs are all unofficial or beta: the "official" `OpenRouterTeam/go-sdk` explicitly says "not yet ready for production use" and is Speakeasy-generated. Community SDKs (`revrost/go-openrouter`, `eduardolat/openroutergo`, `wojtess/openrouter-api-go`, `casibase/go-openrouter`) are all solo-maintainer projects. Writing your own saves you from bit-rot, keeps the request/response structs yours to evolve, and lets you share a `type ChatCompletionRequest` struct across local + OpenRouter + OpenAI. |
| **OpenAI (for Whisper + embed-3-small)** | Same — direct HTTP, share the request struct with local + OpenRouter paths | OpenAI's Go SDK (`openai/openai-go`) exists and is supported, but for a gateway you want the *same* struct driving local llama.cpp, OpenRouter, and OpenAI; writing it once is cleaner than adapting the SDK's typed calls. |
| **Vast.ai** | **Shell out to `vastai` CLI from Go** via `os/exec`, OR call the REST API directly | No official Go SDK. The official Python SDK/CLI (`vastai`, v0.4.0 Sept 2025) is well-maintained. The third-party `aalekhpatel07/go-client-vastai` has 4 total commits, 0 stars, effectively abandoned. The REST API is documented at https://docs.vast.ai/api-reference/introduction and stable. Recommend: call REST directly from Go; document the exact endpoints you use (`PUT /asks/{ask_id}/`, `DELETE /instances/{id}/`, `GET /instances/`) and build tests against a stubbed server. |

Confidence: **HIGH** (verified each SDK state)

---

### Observability

| Library | Version | Purpose | Why |
|---------|---------|---------|-----|
| **`log/slog`** (stdlib) | Go 1.23+ | Structured logging | Go standard as of 1.21. OpenTelemetry has an official bridge (`otelslog`). Zero-cost when OTel disabled. Don't fight the stdlib. |
| **`github.com/prometheus/client_golang`** | v1.21+ (current) | Prometheus metrics | The standard Go Prometheus SDK. Expose `/metrics` with counters (requests per tenant per provider), histograms (latency p50/p95/p99 per route), gauges (active circuit-breaker state, current upstream). |
| **OpenTelemetry Go** (`go.opentelemetry.io/otel`) | current | Distributed tracing (optional for v1) | If you want request tracing across gateway → local pod → OpenRouter, OTel is the answer. `otelhttp` middleware + `otelslog` bridge. Defer until after v1 ships unless Sentry integration (which the rest of the ifix stack uses) already handles this. |
| **Sentry** (`github.com/getsentry/sentry-go`) | v0.29+ | Error tracking | Matches ifix convention (converseai-v4 uses Sentry). Wrap panics, tag each event with `tenant_id` + `upstream_provider`. |
| **NVIDIA DCGM exporter** (`nvidia/dcgm-exporter`) | container | GPU health on the pod | Exposes `DCGM_FI_DEV_GPU_UTIL` (utilization %), `DCGM_FI_DEV_FB_USED` (VRAM used MB), `DCGM_FI_DEV_POWER_USAGE`. The gateway's load-shedding decision ("saturation by GPU util/VRAM" per PROJECT.md line 24) scrapes this endpoint from the Vast.ai pod. |

**Logging choice rationale (slog vs zerolog vs zap):**
- `slog` — stdlib, OTel bridge, "good enough" performance. **Pick this.**
- `zerolog` — ~2× faster than zap, zero-alloc, but custom API you'll re-learn. Justified only if logging is a measured hot path.
- `zap` — Uber's library, fastest when used with typed fields. Similar custom-API concern.

For a gateway that does a handful of log lines per request (auth, route decision, upstream resolved, status, latency), slog's throughput is not the bottleneck; network I/O to the upstream model server is. Use slog.

Confidence: **HIGH**

---

### Dashboard Frontend

| Tech | Version | Purpose | Why |
|------|---------|---------|-----|
| **Next.js 15** | 15.x | Dashboard UI | Matches `converseai-v4/apps/web/` convention in the Ifix monorepo (CLAUDE.md). App Router, server components for admin-heavy views, React Query for live metrics polling. |
| **shadcn/ui + Tailwind** | current | Component library | Same as converseai-v4 pattern |
| **Recharts** or **Tremor** | current | Charts (latency histograms, cost per app) | converseai-v4 already uses Recharts 2.15.4. Tremor is purpose-built for analytics dashboards if Recharts feels low-level. |
| **Eden Treaty** | n/a | (Skip) | Gateway is Go, not Elysia — use a hand-written OpenAPI client or direct `fetch`. |

Confidence: **HIGH**

---

### Health-Checking the GPU Pod

PROJECT.md specifies saturation detection by **GPU utilization / VRAM** (not queue depth). Here's the concrete probing plan:

| Signal | Source | Threshold suggestion | Purpose |
|--------|--------|---------------------|---------|
| **HTTP liveness** — `GET /health` | llama-server, Speaches, Infinity each have health endpoints (llama-server: `/health`; Speaches: `/health`; Infinity: `/health`) | Timeout 2 s; 3 consecutive failures = circuit open | Is the process alive? |
| **End-to-end latency** — synthetic `POST /v1/chat/completions` with a trivial prompt every 10 s | Your own probe | p95 > 5 s for 60 s = mark degraded | Does the *full* inference path work? |
| **GPU utilization** — `DCGM_FI_DEV_GPU_UTIL` | DCGM exporter on the Vast.ai pod, scraped via HTTP from the gateway | > 85% sustained 30 s = load-shed to OpenRouter | Saturation signal |
| **GPU memory used** — `DCGM_FI_DEV_FB_USED` | DCGM exporter | > 22 GB (out of 24 GB) = load-shed | VRAM pressure (KV cache exhaustion imminent) |
| **GPU temperature** — `DCGM_FI_DEV_GPU_TEMP` | DCGM exporter | > 85°C = warn, > 90°C = shed | Host thermal issue → Vast.ai instability indicator |

**What to probe, in priority order:** HTTP liveness (coarse) → end-to-end latency (correctness signal) → GPU util + VRAM (saturation). Don't probe throughput directly (tokens/sec) — it's expensive and a derivative of latency anyway.

**Circuit breaker state transitions** (per upstream, via gobreaker):
- `Closed → Open`: 5 consecutive failures OR synthetic-probe p95 > 5 s for 60 s
- `Open → HalfOpen`: after 30 s cooldown
- `HalfOpen → Closed`: 3 consecutive successful synthetic probes → **PLUS** 5-min health window before flipping the "primary is back" flag that triggers emergency-pod shutdown (PROJECT.md line 26)

Confidence: **HIGH**

---

## Installation

```bash
# Gateway (Go)
go get github.com/go-chi/chi/v5
go get github.com/sony/gobreaker/v2
go get github.com/cenkalti/backoff/v5
go get golang.org/x/time/rate
go get github.com/jackc/pgx/v5
go get github.com/redis/go-redis/v9
go get github.com/prometheus/client_golang/prometheus
go get github.com/getsentry/sentry-go
# stdlib: log/slog, net/http, net/http/httputil

# GPU Pod containers (docker-compose on the Vast.ai instance)
# ghcr.io/ggml-org/llama.cpp:server-cuda
# ghcr.io/speaches-ai/speaches:latest-cuda
# michaelf34/infinity:latest
# nvidia/dcgm-exporter:latest
```

---

## Alternatives Considered

| Recommended | Alternative | When to Use Alternative |
|-------------|-------------|-------------------------|
| Custom Go gateway | **Bifrost** (Go, Apache 2.0) | If the Vast.ai automation can be shipped as a separate sidecar and Bifrost handles only the provider routing. Worth revisiting in a future milestone if maintenance burden of custom code grows. |
| `llama-server` (C++ binary) | `llama-cpp-python` server | If you need Python-side pre/post-processing (custom tokenization, RAG injection at request time). For a pass-through gateway, the binary is simpler. |
| `llama.cpp` Q4_K_M GGUF | `vLLM 0.10 + AWQ` | **Only** when you upgrade to a 48 GB GPU (L40S, A6000, dual 3090). Then vLLM's paged-attention + continuous batching wins on throughput under 10+ concurrent users. Not viable on single 4090 24 GB. |
| Speaches (faster-whisper) | `vLLM` STT mode / `insanely-fast-whisper` | If audio throughput becomes the bottleneck — faster-whisper is batched but not as fast as vLLM's dedicated Whisper implementation for sustained high QPS. Revisit if Chat Ifix + Telefonia together exceed ~50 concurrent transcriptions. |
| Infinity (embeddings) | `TEI` (HuggingFace Text Embeddings Inference) | If Infinity has a BGE-M3-specific quirk at your batch size. TEI is Rust, similar throughput. |
| chi + stdlib `net/http` | Bifrost as a library | If you want pre-built provider adapters (Anthropic Claude, Gemini, Bedrock) for future expansion beyond the Qwen-only LLM path. |
| Direct REST to Vast.ai | `aalekhpatel07/go-client-vastai` | Never — the library is abandoned. |
| Direct HTTP to OpenRouter | `revrost/go-openrouter` community SDK | If you value someone else pre-validating the request schema. For a gateway that owns its own types, direct HTTP is cleaner. |

---

## What NOT to Use

| Avoid | Why | Use Instead |
|-------|-----|-------------|
| **TGI (Text Generation Inference)** | HuggingFace put it into maintenance mode Dec 11, 2025; officially recommends vLLM or SGLang for new deployments | llama.cpp (this project) or vLLM (larger GPU) |
| **vLLM on RTX 4090 for Qwen3.5-27B** | AWQ/GPTQ-INT4 variants don't reliably fit on 24 GB; known VRAM bug (#37080) where INT4 uses more memory than FP8; community launch configs use 2-GPU tensor-parallel | llama.cpp Q4_K_M (this project) |
| **Ollama as the primary server** | Adds a layer over llama.cpp; weaker control over chat-template flags (critical for Qwen3.5 tool calling) | llama.cpp `llama-server` directly |
| **Fiber (Go)** | Built on `fasthttp`, incompatible with `net/http` ecosystem (breaks `httputil.ReverseProxy`, `http.Flusher` for SSE streaming, OpenTelemetry HTTP instrumentations). Dealbreaker for a reverse proxy that must stream LLM tokens. | chi + stdlib |
| **afex/hystrix-go** | No visible maintenance, Netflix itself abandoned Hystrix years ago | sony/gobreaker/v2 |
| **gomodule/redigo** | No native pool, no Cluster support, printf-style API | redis/go-redis/v9 |
| **lib/pq** | Long-term maintenance mode; pgx is faster and more feature-complete | jackc/pgx/v5 |
| **WhisperX** | Original repo no longer actively maintained (BetterWhisperX fork exists); features (diarization) not needed for Ifix use cases | faster-whisper via Speaches |
| **`llama-cpp-python` (Python wrapper)** | Adds Python layer with no benefit for a pass-through HTTP server; maintainers of llama.cpp recommend `llama-server` binary for serving | `llama-server` binary (C++) |
| **Unofficial OpenRouter/Vast.ai Go SDKs** | All are solo-maintainer or abandoned; official OpenRouter Go SDK is beta and Speakeasy-generated (regenerates over manual fixes) | Direct HTTP with `net/http` |
| **GORM** | ORM overhead + hides the SQL that you want visible in audit-heavy paths; slower than raw pgx | pgx + sqlc (if you need typed queries) |
| **Zerolog / Zap** (as primary) | Custom APIs when slog covers 95% of needs in stdlib, with better OTel integration | log/slog |
| **TTS on the primary GPU** | Already excluded by PROJECT.md; VRAM budget doesn't tolerate it with LLM+STT+embed. Do not re-add. | voice-api on CPU (unchanged) |

---

## Stack Patterns by Variant

**If the Ifix team decides to scale beyond single-4090 later:**
- Swap llama.cpp for vLLM 0.10+ on the new GPU (48 GB+)
- Gateway code unchanged (OpenAI-compat endpoint is identical)
- Revisit Infinity vs TEI for embedding throughput
- Consider Bifrost as a library for multi-model routing (if Qwen-3.5-27B stops being the only LLM)

**If latency becomes the primary concern:**
- Measure where the p95 goes — likely inside llama.cpp generation, not the Go gateway
- Consider `Qwen3.5-14B-Instruct` as a faster second tier for latency-sensitive apps (Telefonia real-time), keep 27B for batch/chat
- Enable llama.cpp `--parallel N` + `--cont-batching` for concurrent requests
- Add Redis-backed response caching at the gateway for deterministic prompts

**If cost becomes the primary concern:**
- Enable OpenRouter for off-peak hours per-app (PROJECT.md line 27, "pico/vale" mode)
- Reduce emergency-pod price cap; accept longer degraded-state windows
- Add prompt-level cache at the gateway (semantic cache via embeddings; a Bifrost feature worth studying)

---

## Version Compatibility

| Package | Compatible With | Notes |
|---------|-----------------|-------|
| `chi/v5` | Go 1.18+ | Uses generics in some places; 1.23 recommended for slog. |
| `pgx/v5` latest | Go 1.25+ and Postgres 14+ | Digital Ocean Managed Postgres defaults to 16 — fine. |
| `go-redis/v9` | Go 1.19+, Redis 6+ | Test against your exact Redis version (ifix uses Redis 7). |
| `sony/gobreaker/v2` | Go 1.18+ (generics) | v1 still works; v2's generic `Execute[T]` is cleaner for typed responses. |
| `cenkalti/backoff/v5` | Go 1.18+ | v5 introduces context-aware `Retry(ctx, op, bo)` — use it; don't use v3/v4 patterns. |
| `llama.cpp server-cuda` | CUDA 12.x | Vast.ai 4090 instances default to CUDA 12.x; verify image tag matches host CUDA major version. |
| `Speaches` | PyTorch 2.3+, CUDA 12.x | Docker image bundles deps; pin a specific tag, not `:latest`. |
| `Infinity` v0.0.77 | PyTorch 2.3+, CUDA 12.x | Same — pin tag. |
| vLLM 0.17+ | Required for Qwen3.5's GDN architecture | If you ever *do* migrate to vLLM, anything below 0.17 cannot load Qwen3.5 correctly. |

---

## Critical 2026 Updates vs PROJECT.md / Doc

Flag these to the user before locking the stack:

1. **`llama-cpp-python` → `llama.cpp` `llama-server` binary.** Same engine, no Python layer, better-maintained OpenAI-compat HTTP surface. Trivial swap.
2. **`sentence-transformers` → `Infinity`.** 2–3× faster on GPU, same model (`BAAI/bge-m3`), OpenAI-compatible out of the box. Material improvement.
3. **Qwen3.5-27B template / tool-calling gotcha.** The default GGUF Jinja template rejects "developer" role and silently breaks thinking mode with `--chat-template chatml`. Carry a patched template in the repo; test tool-calling early.
4. **TGI is dead.** If anyone proposes TGI as a "standard HuggingFace option," it's in maintenance mode since Dec 11, 2025.
5. **vLLM on 4090 for Qwen3.5-27B doesn't work reliably.** The doc's llama.cpp choice is correct for this VRAM budget — do not let anyone "upgrade" to vLLM without upgrading the GPU first.
6. **No official or viable third-party Vast.ai Go SDK.** Plan to call REST directly; spike a 3-hour task on the REST contracts (`search offers`, `create instance`, `destroy instance`, `get instance status`) during Phase 1.

---

## Sources

### Context7
- `/gin-gonic/gin` — Gin framework docs (evaluated, rejected in favor of chi)
- `/jackc/pgx` and `/websites/pkg_go_dev_github_com_jackc_pgx_v5` — pgx v5 API + connection-pool patterns

### Official docs / repos
- https://github.com/go-chi/chi — chi v5, Feb 2026 release. HIGH confidence.
- https://github.com/sony/gobreaker — gobreaker v2 with generics. HIGH confidence.
- https://github.com/cenkalti/backoff/v5 — backoff v5 API. HIGH confidence.
- https://pkg.go.dev/golang.org/x/time/rate — stdlib-adjacent rate limiter, updated Feb 2026. HIGH confidence.
- https://github.com/jackc/pgx — pgx v5. HIGH confidence.
- https://github.com/redis/go-redis — go-redis v9. HIGH confidence.
- https://github.com/ggml-org/llama.cpp — `llama-server` binary + function-calling docs. HIGH confidence.
- https://github.com/speaches-ai/speaches — faster-whisper-server successor. HIGH confidence.
- https://github.com/michaelfeil/infinity — Infinity embedding server, v0.0.77. HIGH confidence.
- https://github.com/maximhq/bifrost — Bifrost gateway (evaluated as alternative). HIGH confidence.
- https://github.com/vast-ai/vast-cli — official Python SDK/CLI, v0.4.0 Sept 2025. HIGH confidence.
- https://docs.vast.ai/api-reference/introduction — Vast.ai REST API reference. HIGH confidence.
- https://github.com/OpenRouterTeam/go-sdk — Official Go SDK, beta, not production-ready (explicit README warning). HIGH confidence on "don't use it yet."
- https://github.com/NVIDIA/dcgm-exporter — GPU metrics for Prometheus. HIGH confidence.
- https://github.com/vllm-project/vllm/issues/37080 — Qwen3.5 INT4 VRAM bug. HIGH confidence on "don't use vLLM on 4090 for this model."
- https://huggingface.co/QuantTrio/Qwen3.5-27B-AWQ/discussions/1 — community launch configs (tensor-parallel 2 is typical). HIGH confidence.
- https://huggingface.co/unsloth/Qwen3.5-27B-GGUF — Unsloth's GGUF quants, community-standard source. HIGH confidence.

### Secondary / corroborating
- https://www.getmaxim.ai/blog/bifrost-a-drop-in-llm-proxy-40x-faster-than-litellm/ — performance claims for Bifrost. MEDIUM confidence (vendor blog; numbers self-reported).
- https://blog.premai.io/llm-inference-servers-compared-vllm-vs-tgi-vs-sglang-vs-triton-2026/ — TGI maintenance mode confirmation. MEDIUM confidence (corroborated by multiple sources).
- https://modal.com/blog/choosing-whisper-variants — Whisper variants comparison. MEDIUM confidence.
- https://www.bentoml.com/blog/a-guide-to-open-source-embedding-models — BGE-M3 and embedding servers landscape. MEDIUM confidence.
- https://gist.github.com/sudoingX/c2facf7d8f7608c65c1024ef3b22d431 — Qwen3.5 patched Jinja template. MEDIUM confidence (community gist; verify before merging into repo).

---

*Stack research for: AI inference gateway (multi-tenant, Go, Vast.ai GPU + cloud failover)*
*Researched: 2026-04-17*
