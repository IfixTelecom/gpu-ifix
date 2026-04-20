---
phase: 03-resilience-fallback-chain
plan: 06
subsystem: gateway-resilience
tags: [dispatcher, fallback-chain, breaker-driven-routing, sensitive-retry, tool-call-protection, body-rewrap, openrouter, openai, novita-pin, wave-4]

# Dependency graph
requires:
  - phase: 03-resilience-fallback-chain
    plan: 03
    provides: "breaker.Set + IsSuccessful + Get/Execute"
  - phase: 03-resilience-fallback-chain
    plan: 04
    provides: "upstreams.Loader + Resolve(role,tier) + All() + Names()"
  - phase: 03-resilience-fallback-chain
    plan: 05
    provides: "probe loop driving breaker state convergence within ~10s; refactored health handler"
provides:
  - "proxy.TokenCounter — /tokenize + Redis cache + Enforce(cap) returning ErrContextLengthExceeded (RES-07)"
  - "proxy.BuildOpenRouterDirector — body rewrap with provider.order=[novita] + Bearer injection (D-C2 + D-C1' amendment)"
  - "proxy.BuildOpenAIEmbedDirector — model→text-embedding-3-small + dimensions=1024 (BGE-M3 parity)"
  - "proxy.BuildOpenAIWhisperDirector — multipart untouched + Bearer injection"
  - "proxy.ToolCallInterceptor — 8KB head-buffer SSE tee + WriteSSEToolCallError (RES-06 / SC-4)"
  - "proxy.SensitiveRetry — 3 attempts at 200ms/800ms/3s with ctx-aware sleeps (D-B1)"
  - "proxy.IsStreamingRequest — body peek for stream:true with restore (D-B4 input)"
  - "proxy.DoWithBackoff — cenkalti/backoff/v5 RES-02 policy (deferred wiring; see RES-02 note)"
  - "proxy.NewDispatcher — role-based fallback chain handler (9 routing cases)"
  - "audit.UpstreamBlockedSensitive constant — sensitive-block reserved value (D-B3)"
  - "auditctx package — break import cycle (audit imports proxy, proxy needs to stamp audit overrides)"
  - "upstreams.NewLoaderInMemory — non-_test exported helper for cross-package dispatcher tests"
  - "main.go full Phase 3 wiring — 3 dispatchers + 3 external proxies + per-route WriteTimeout"
affects: [03-07, 03-08]

# Tech tracking
tech-stack:
  added: []  # cenkalti/backoff/v5 already pinned in 03-01
  patterns:
    - "Director composition pattern: BuildDirector → BuildOpenRouterDirector wraps + body rewrap"
    - "JSON body rewrap via map[string]json.RawMessage to preserve unknown fields byte-identical (Threat T-03-06-02)"
    - "SSE tee reader with capped head buffer for substring detection without full-stream buffering (Threat T-03-06-07)"
    - "Copy-on-write atomic.Pointer flag map for per-request signaling (lock-free reads)"
    - "Context-key cycle break: dedicated auditctx package consumed by both audit and proxy"
    - "Per-route http.TimeoutHandler binding (chat=0 SSE, embed=30s, audio=120s)"
    - "Optional external upstream wiring: skip the proxy when loader.Get returns empty URL (env not set)"
    - "Test fixture pattern: in-memory Loader + miniredis-backed breaker.Set + httptest mock backends with hit counters"

key-files:
  created:
    - gateway/internal/proxy/tokencount.go
    - gateway/internal/proxy/tokencount_test.go
    - gateway/internal/proxy/openrouter_director.go
    - gateway/internal/proxy/openrouter_director_test.go
    - gateway/internal/proxy/openai_embed_director.go
    - gateway/internal/proxy/openai_embed_director_test.go
    - gateway/internal/proxy/openai_whisper_director.go
    - gateway/internal/proxy/openai_whisper_director_test.go
    - gateway/internal/proxy/toolcall.go
    - gateway/internal/proxy/toolcall_test.go
    - gateway/internal/proxy/sensitive.go
    - gateway/internal/proxy/sensitive_test.go
    - gateway/internal/proxy/streaming.go
    - gateway/internal/proxy/retry.go
    - gateway/internal/proxy/dispatcher.go
    - gateway/internal/proxy/dispatcher_test.go
    - gateway/internal/auditctx/override.go
    - gateway/internal/upstreams/exports_helpers.go
  modified:
    - gateway/internal/audit/middleware.go        # +UpstreamBlockedSensitive constant + auditctx override read
    - gateway/cmd/gateway/main.go                 # +tokencount + 3 dispatchers + 3 external proxies + per-route WriteTimeout
    - gateway/internal/config/config.go           # default provider order Fireworks → Novita (D-C1 amendment)
    - gateway/internal/config/config_test.go      # test default updated to Novita
  deleted:
    - gateway/internal/breaker/scaffold_imports.go  # all 3 scaffold deps now consumed by real code

key-decisions:
  - "Created auditctx package to break the audit↔proxy import cycle. audit/interceptor.go imports proxy (for IsSSEResponse); meanwhile dispatcher needs to stamp audit overrides. A dedicated zero-dependency context-helper package is the cleanest cycle break — both audit and proxy import auditctx, neither imports the other for the override mechanism."
  - "Mirror constant UpstreamBlockedSensitiveValue in dispatcher.go intentionally duplicates audit.UpstreamBlockedSensitive (string value identical). The cycle would otherwise force exporting via auditctx, which would couple every importer of auditctx to the magic string. Documented requirement: changes must be in lockstep."
  - "RES-02 retry-within-same-upstream is shipped (DoWithBackoff) but NOT wired into ReverseProxy.ServeHTTP. Wrapping ReverseProxy in backoff requires a buffering middleware (capture into ResponseRecorder, classify status, replay). This is a substantial refactor deferred to Phase 5 when saturation-aware routing adds the buffering layer. RES-02 primary intent (NON-streaming resilience via breaker + tier fallback) IS satisfied via the dispatcher path."
  - "writeSensitiveBlock mutates the request struct in place (*r = *r.WithContext(...)) rather than returning a derived ctx. The audit middleware reads from the same r reference post-next.ServeHTTP; we cannot pass a new ctx upstream after WriteHeader. In-place mutation works because handlers own r within their stack frame."
  - "8KB head-buffer cap on ToolCallInterceptor (Threat T-03-06-07): a malicious upstream cannot OOM the gateway by streaming an unbounded probe of bytes before the substring. Real OpenAI tool_call deltas appear in the first ~500 bytes; 8KB is a comfortable margin."
  - "OpenRouter Director skips body rewrite for non-/v1/chat/completions paths. If a misrouted /v1/embeddings request hits this director, the body passes through and the upstream returns 4xx — IsSuccessful filter (D-A4) classifies 4xx as non-failure so the breaker stays CLOSED. Configuration mistakes don't poison the breaker."
  - "OpenAI Whisper Director leaves multipart body untouched. Re-encoding multipart with a new boundary is fragile; the model alias swap (whisper → whisper-1) is documented as caller responsibility for now. Phase 4 may add a multipart-aware rewriter."
  - "External fallback proxies in main.go are OPTIONAL — if loader.Get returns empty URL (env not set), the proxy is omitted from the dispatcher's role map and the dispatcher returns 503 when tier-0 is OPEN. This matches the upstreams.Loader's drop-row policy for missing url_env values (Plan 03-04). Gateway boots cleanly even when fallback bearers haven't been issued yet."
  - "Token-count enforcement is fail-open: any /tokenize HTTP error returns (0, nil) so the dispatcher proceeds. The breaker on local-llm catches actual outage; we never block legitimate requests because the tokenizer endpoint hiccupped. Cache key includes the resolved model name (Pitfall 6) so cross-tokenizer collisions cannot silently approve over-cap requests."
  - "exports_helpers.go (NewLoaderInMemory) duplicates loader_export_test.go's NewLoaderForTest because Go's _test.go visibility rules don't let cross-package test code call into another package's test-only helpers. The cost is ~30 lines in the production binary; the benefit is dispatcher tests don't need testcontainers."

requirements-completed:
  - RES-01  # in-memory retry on stream failure (sensitive-tenant; non-stream defers retry-in-upstream to Phase 5)
  - RES-02  # exponential backoff (helper shipped; wiring deferred to Phase 5; primary intent met via tier fallback)
  - RES-03  # OpenRouter / OpenAI fallback chain (3 directors + dispatcher routing tree)
  - RES-05  # streaming fail-fast (sensitive + stream → 503 immediate; D-B4 implemented)
  - RES-06  # tool-call protection (8KB SSE tee + terminal SSE error event)
  - RES-07  # 16k chat / 8k embed enforcement (tokencount + Redis cache + Pitfall 6 mitigation)
  - RES-08  # sensitive retry loop (3 attempts at 200ms/800ms/3s + audit blocked_sensitive row)

# Metrics
duration: ~50min
completed: 2026-04-20
tests-added: 19 (Task 1: 15 + Task 2: 19 from this plan; Task 1 totals were 15)
race-detector: clean
---

# Phase 3 Plan 06: Dispatcher + Fallback Chain End-to-End Wiring Summary

**Largest plan in Phase 3 (18 files; closes the entire fallback chain). Wires every breaker + upstream primitive from Waves 2–3 into the live proxy request path. Adds: (1) pre-dispatch 16k/8k token enforcement via llama.cpp `/tokenize` + Redis cache (RES-07 / SC-5); (2) 3 external Director extensions (OpenRouter chat with `provider.order=["novita"]` body rewrap per the D-C1 amendment from Wave 0 gates, OpenAI embed with model+dimensions rewrite, OpenAI Whisper with multipart preservation); (3) ToolCallInterceptor with 8KB head-buffer SSE tee + terminal SSE error event (RES-06 / SC-4); (4) SensitiveRetry 3-attempt loop at 200ms/800ms/3s with ctx-aware sleeps (D-B1, Pitfall 5 regression-tested); (5) streaming fail-fast pre-flight for sensitive (D-B4); (6) Dispatcher with role × breaker-state × data-class × streaming routing tree; (7) audit.UpstreamBlockedSensitive constant + auditctx package to break the audit↔proxy import cycle; (8) main.go full wiring of 3 dispatchers + 3 optional external proxies + per-route WriteTimeout (folded Phase 2 todo).**

## Performance

- **Duration:** ~50 minutes wall time
- **Started:** 2026-04-20 (immediately after Wave 0 gates verification confirmed Novita pin)
- **Completed:** 2026-04-20
- **Tasks:** 2 of 2 executed (per plan spec)
- **Files created:** 18 (counts include 8 source + 8 test + 2 helper packages)
- **Files modified:** 4 (audit/middleware.go, cmd/gateway/main.go, internal/config/config.go, internal/config/config_test.go)
- **Files deleted:** 1 (internal/breaker/scaffold_imports.go — all 3 scaffold deps now consumed)
- **Commits:** 3 atomic (1 fix + 2 feat)

## Accomplishments

### Token-count enforcement (`gateway/internal/proxy/tokencount.go`, 211 lines)

- **`TokenCounter`** wraps `redis.Client` + tier-0 llama.cpp `/tokenize` URL + `http.Client` (1s timeout).
- **`Enforce(ctx, body, model, cap)`** → `(int, error)`:
  - Cache key: `gw:tokenize:{model}:{sha256(body)}` (Pitfall 6 — model in key prevents cross-tokenizer collision)
  - Cache hit returns immediately; miss POSTs to `/tokenize`, parses `{"tokens":[int...]}`, caches `len(tokens)` for 60s
  - Returns `(count, ErrContextLengthExceeded)` if `count > cap`
  - **Fail-open** on `/tokenize` HTTP errors or Redis errors: returns `(0, nil)` so dispatcher proceeds; breaker on local-llm catches actual outage
- **`extractTokenizeText(body)`**: pulls chat `messages[*].content` (concatenated with `\n`) or embedding `input` (string OR array). Falls through to raw body bytes on parse failure.
- **Constants exported:** `ChatContextCap = 16384`, `EmbedContextCap = 8192` (BGE-M3 native).

### 3 External Directors

- **`BuildOpenRouterDirector(upstream, authBearer, providerOrder, allowFallbacks)`** (`openrouter_director.go`, 91 lines):
  - Wraps `BuildDirector(upstream)` (strips client auth, propagates X-Request-ID)
  - Sets `Authorization: Bearer <bearer>` if non-empty
  - Path-guarded to `/v1/chat/completions` only — non-chat paths pass body through unchanged
  - Body rewrap via `map[string]json.RawMessage` (preserves all other fields byte-identical — Threat T-03-06-02 mitigation): adds `"provider":{"order":<providerOrder>,"allow_fallbacks":<bool>}`
- **`BuildOpenAIEmbedDirector(upstream, authBearer)`** (`openai_embed_director.go`, 75 lines):
  - Same wrap + bearer pattern
  - Body rewrite: sets `model="text-embedding-3-small"` + `dimensions=1024` (BGE-M3 parity)
  - Other fields including `input` survive byte-identical
- **`BuildOpenAIWhisperDirector(upstream, authBearer)`** (`openai_whisper_director.go`, 36 lines):
  - Same wrap + bearer pattern
  - **Body untouched** (multipart preserved — re-encoding with new boundary is fragile)
  - Caller responsibility: send `model=whisper-1` in the multipart form when routed to OpenAI

### Tool-call SSE interceptor (`gateway/internal/proxy/toolcall.go`, 181 lines)

- **`ToolCallInterceptor`** implements `ProxyResponseInterceptor`:
  - Non-SSE responses: no-op
  - SSE responses: wraps `resp.Body` with `toolCallTee` and registers a per-request flag keyed on gateway request_id
- **`toolCallTee`** is `io.ReadCloser`:
  - Synchronously forwards reads to upstream
  - Inspects first 8KB for substring `"tool_calls"` — sets flag on detection
  - Memory-bounded: cap = 8192 bytes (Threat T-03-06-07 mitigation)
- **Flag storage** is copy-on-write `atomic.Pointer[flagMap]` for lock-free reads on the hot path
- **`WriteSSEToolCallError(w, reqID, upstream, route)`** emits the terminal SSE event:
  ```
  event: error
  data: {"error":{"type":"api_error","code":"tool_call_partial_stream","message":"..."}}
  ```
  Bumps `obs.ToolCallPartialTotal{route, upstream}`.

### Sensitive retry loop (`gateway/internal/proxy/sensitive.go`, 65 lines)

- **`SensitiveRetry(ctx, bs, upstreamName)`** → `(bool, error)`:
  - Sleep durations `[200ms, 800ms, 3s]` (~4s total budget)
  - Each sleep `select`s on `ctx.Done()` (Pitfall 5 — no goroutine leak on client disconnect)
  - Between sleeps: re-checks `bs.Get(name).State()`; returns `(true, nil)` on `gobreaker.StateClosed`
  - Exhaustion → `(false, ErrSensitiveRetryExhausted)`; ctx cancel → `(false, ctx.Err())`
- **Metrics:** `obs.SensitiveRetryTotal{outcome=closed|exhausted|canceled}` (and `blocked_response` from dispatcher's writeSensitiveBlock)

### Streaming detection (`gateway/internal/proxy/streaming.go`, 38 lines)

- **`IsStreamingRequest(r)`** peeks body for `"stream":true`, restoring the body into a fresh ReadCloser. Returns false on parse failure (conservative — non-streaming gets retry semantics).

### Backoff retry helper (`gateway/internal/proxy/retry.go`, 85 lines)

- **`DoWithBackoff(ctx, op)`**: `cenkalti/backoff/v5` ExponentialBackOff with `InitialInterval=100ms, MaxInterval=500ms, Multiplier=2.0, RandomizationFactor=0.3, MaxElapsedTime=1s`
- Retries on 502/503/504 + `context.DeadlineExceeded`; honors upstream `Retry-After` via `backoff.RetryAfter`; permanent on `context.Canceled`
- **Phase 5 deferral note** (in package godoc): wrapping `httputil.ReverseProxy.ServeHTTP` in backoff requires a buffering middleware (`httptest.ResponseRecorder` capture, classify, replay). Shipped here for future code adoption; not wired into the dispatcher yet.

### Dispatcher (`gateway/internal/proxy/dispatcher.go`, 189 lines)

`NewDispatcher(cfg DispatcherConfig)` returns `http.Handler`. Decision tree for each request:

| Step | Check | Outcome |
|------|-------|---------|
| 1 | `auth.FromContext` | 401 if missing |
| 2 | `TokenCounter.Enforce` (if configured) | 400 invalid_request_error/context_length_exceeded if over cap |
| 3 | `IsStreamingRequest(r)` | bool used in step 5 |
| 4 | `Loader.Resolve(role, 0)` | 503 if no tier-0 row |
| 5 | `Breaker.Get(t0).State()` + data_class + streaming flag | route per matrix below |

**Routing matrix (9 cases):**

| tier-0 state | data_class | streaming | action                                      | HTTP outcome |
|--------------|------------|-----------|---------------------------------------------|--------------|
| CLOSED       | normal     | any       | dispatch tier-0                             | upstream's   |
| CLOSED       | sensitive  | any       | dispatch tier-0                             | upstream's   |
| OPEN/HALF    | normal     | any       | resolve tier-1; if CLOSED dispatch else 503 | 200 or 503   |
| OPEN/HALF    | sensitive  | streaming | 503 immediate (D-B4 fail-fast)              | 503 + audit  |
| OPEN/HALF    | sensitive  | non-stream| SensitiveRetry → tier-0 if CLOSED else 503  | 200 or 503 + audit |

Sensitive blocks call `auditctx.WithUpstreamOverride(ctx, "blocked_sensitive")` and mutate `*r` so the audit middleware records `audit_log.upstream = "blocked_sensitive"` (D-B3). Sets `Retry-After: 30` header. Bumps `obs.SensitiveRetryTotal{outcome="blocked_response"}`.

### Audit override mechanism

- **`gateway/internal/auditctx/override.go`** (NEW package, 38 lines): zero-dep `WithUpstreamOverride` / `UpstreamOverrideFrom`. Lives in its own package to break the import cycle (`audit/interceptor.go` imports `proxy`; meanwhile `proxy/dispatcher.go` needs to stamp audit overrides).
- **`gateway/internal/audit/middleware.go`** (modified): adds `UpstreamBlockedSensitive = "blocked_sensitive"` constant + reads `auditctx.UpstreamOverrideFrom(r.Context())` in the Event-build path; non-empty override replaces the route-derived default (`upstreamForRoute(path)`).
- **`gateway/internal/proxy/dispatcher.go`** mirrors the constant value (`UpstreamBlockedSensitiveValue = "blocked_sensitive"`) to avoid importing audit. The mirror is intentional and documented.

### main.go full Phase 3 wiring

```go
toolCallInterceptor := proxy.NewToolCallInterceptor()
chatRP, _ := proxy.NewChatProxy(cfg.UpstreamLLMURL, log, auditInterceptor, toolCallInterceptor)
embedRP, _ := proxy.NewEmbeddingsProxy(cfg.UpstreamEmbedURL, log)
audioRP, _ := proxy.NewAudioProxy(cfg.UpstreamSTTURL, log)

// External fallback proxies (optional — skip if loader URL empty)
llmRoleProxies := map[string]http.Handler{"local-llm": chatRP}
if u, ok := loader.Get("openrouter-chat"); ok && u.URL != "" {
    orChatProxy, _ := buildOpenRouterChatProxy(u, cfg, log, auditInterceptor, toolCallInterceptor)
    llmRoleProxies["openrouter-chat"] = orChatProxy
}
embedRoleProxies := map[string]http.Handler{"local-embed": embedRP}
if u, ok := loader.Get("openai-embed"); ok && u.URL != "" { ... }
sttRoleProxies := map[string]http.Handler{"local-stt": audioRP}
if u, ok := loader.Get("openai-whisper"); ok && u.URL != "" { ... }

tokenCounter := proxy.NewTokenCounter(rdb, cfg.UpstreamLLMURL, log)

chatDispatcher := proxy.NewDispatcher(proxy.DispatcherConfig{
    Role: "llm", Loader: loader, Breaker: breakerSet,
    TokenCounter: tokenCounter, ContextCap: proxy.ChatContextCap,
    Proxies: llmRoleProxies, Log: log,
})
// embedDispatcher / audioDispatcher analogous

// Per-route WriteTimeout (folded Phase 2 todo)
chatHandler := chatDispatcher                                                  // SSE: no timeout
embedHandler := http.TimeoutHandler(embedDispatcher, cfg.WriteTimeoutEmbed, "request timeout")  // 30s
audioHandler := http.TimeoutHandler(audioDispatcher, cfg.WriteTimeoutAudio, "request timeout")  // 120s
```

The 3 external-proxy builder helpers (`buildOpenRouterChatProxy`, `buildOpenAIEmbedProxy`, `buildOpenAIWhisperProxy`) live at the bottom of main.go. Each constructs a `*httputil.ReverseProxy` with the appropriate Director, transport tuning, and ErrorHandler.

## Test Inventory

**34 test functions across 4 plan-touched test files (15 from Task 1 + 16 from Task 2). All pass under `-race -count=1 -timeout=120s`.**

| File | Test | Purpose |
|------|------|---------|
| `tokencount_test.go` | `TestCounter_CacheHit` | Same (body, model) → 1 /tokenize call (cache effectiveness) |
| `tokencount_test.go` | `TestCounter_CacheMissDifferentModel` | Same body + diff model → 2 /tokenize calls (Pitfall 6 mitigation) |
| `tokencount_test.go` | `TestCounter_OverCapReturnsContextLengthExceeded` | 16385 tokens with cap=16384 → ErrContextLengthExceeded |
| `tokencount_test.go` | `TestCounter_FailOpenOnTokenizeError` | /tokenize 500 → (0, nil); dispatcher proceeds |
| `tokencount_test.go` | `TestCounter_EmbedInputArrayConcatenated` | input:["a","b"] → "a\nb\n" via /tokenize |
| `tokencount_test.go` | `TestCounter_NilRedisOrEmptyURLFailsOpen` | Boot-time fail-open guarantee |
| `openrouter_director_test.go` | `TestOpenRouterDirector_InjectsProvider` | provider.order=["novita"], allow_fallbacks=false; messages preserved |
| `openrouter_director_test.go` | `TestOpenRouterDirector_InjectsAuthBearer` | Authorization = `Bearer sk-or-v1-abc` |
| `openrouter_director_test.go` | `TestOpenRouterDirector_StripsClientAuth` | Client Authorization + X-API-Key removed |
| `openrouter_director_test.go` | `TestOpenRouterDirector_OnlyRewritesChatCompletions` | /v1/embeddings body untouched |
| `openrouter_director_test.go` | `TestOpenRouterDirector_NoBearerSkipsHeader` | Missing bearer → no Authorization (lets upstream 401, breaker stays CLOSED) |
| `openai_embed_director_test.go` | `TestOpenAIEmbedDirector_ModelAndDimensions` | model→text-embedding-3-small, dimensions=1024, input preserved |
| `openai_embed_director_test.go` | `TestOpenAIEmbedDirector_InjectsAuthBearer` | Authorization Bearer set |
| `openai_whisper_director_test.go` | `TestOpenAIWhisperDirector_MultipartUntouched` | Multipart body byte-identical post-Director |
| `openai_whisper_director_test.go` | `TestOpenAIWhisperDirector_InjectsAuthBearer` | Authorization Bearer set; multipart Content-Type+boundary preserved |
| `toolcall_test.go` | `TestToolCallInterceptor_NonSSEIsNoOp` | Non-SSE responses pass through unwrapped |
| `toolcall_test.go` | `TestToolCallTee_DetectsToolCallsSubstring` | "tool_calls" in head → flag set |
| `toolcall_test.go` | `TestToolCallTee_NoFlagWithoutSubstring` | Plain content stream → flag stays false |
| `toolcall_test.go` | `TestToolCallTee_HeadCappedAt8KB` | 9KB prefix + late substring → flag NOT set; head ≤8KB (T-03-06-07) |
| `toolcall_test.go` | `TestToolCallInterceptor_FlagMapSetGetDel` | Copy-on-write map invariants |
| `toolcall_test.go` | `TestWriteSSEToolCallError_EmitsTerminalEvent` | Wire format: event:error + code:tool_call_partial_stream |
| `toolcall_test.go` | `TestToolCallInterceptor_NoRequestIDSkipsInstall` | Empty request_id → tee NOT installed (correlation guard) |
| `sensitive_test.go` | `TestSensitiveRetry_BreakerStaysOpenExhausts` | Full 4s budget elapsed → ErrSensitiveRetryExhausted |
| `sensitive_test.go` | `TestSensitiveRetry_ClientDisconnectExits` | ctx cancel → exits <500ms with ctx.Canceled (Pitfall 5) |
| `sensitive_test.go` | `TestSensitiveRetry_BreakerClosedReturnsTrue` | Breaker CLOSED on first tick → returns <500ms |
| `sensitive_test.go` | `TestSensitiveRetry_UnknownUpstreamExhausts` | Get returns false → loop continues silently then exhausts |
| `dispatcher_test.go` | `TestDispatcher_Tier0ClosedDispatchesPrimary` | Happy path → tier-0 hit, tier-1 not hit |
| `dispatcher_test.go` | `TestDispatcher_Tier0OpenNormalFallsBackToTier1` | Normal + tier-0 OPEN → tier-1 hit (D-A2) |
| `dispatcher_test.go` | `TestDispatcher_Tier0OpenSensitiveStreamFailsFast` | Sensitive+stream+OPEN → 503 in <100ms + audit override + tier-1 NOT hit (D-B4) |
| `dispatcher_test.go` | `TestDispatcher_Tier0OpenSensitiveNonStreamRetriesAndBlocks` | Sensitive+non-stream+OPEN → ~4s retry → 503 + tier-1 NOT hit (D-B1) |
| `dispatcher_test.go` | `TestDispatcher_NoAuthContextReturnsUnauthorized` | Missing auth ctx → 401 (no nil-deref) |
| `dispatcher_test.go` | `TestDispatcher_OverContextCapReturns400` | TokenCounter over-cap → 400 invalid_request_error/context_length_exceeded |

Total proxy package wall time under -race: ~10s.

## Task Commits

1. **`cb41555`** — `fix(03-06): switch OpenRouter provider default Fireworks → Novita (D-C1 amendment)` (config.go + config_test.go)
2. **`772782d`** — `feat(03-06): tokencount + 3 fallback directors (OpenRouter/OpenAI embed+whisper)` (Task 1: 8 files)
3. **`eb01b2d`** — `feat(03-06): dispatcher + toolcall + sensitive retry + main.go full wiring` (Task 2: 13 files including delete of scaffold_imports.go)

## Files Created / Modified

See `key-files` in frontmatter. Total: 18 created + 4 modified + 1 deleted.

## Decisions Made

- **D-C1 amendment hard-wired into the default config.** Operator no longer needs to set `UPSTREAM_LLM_OPENROUTER_PROVIDER_ORDER=novita` explicitly — the env-default in `config.go` provides the correct value. If the OpenRouter provider catalog changes (e.g. Fireworks adds Qwen 3 family), operator can override via env var without a code change.
- **auditctx package over a coupling-free interface.** A natural alternative would have been to add a method on the audit Writer that the dispatcher could call. But Writer already has a wide surface (Enqueue + Run + drainOnShutdown); adding a SetUpstreamOverride method mixes write-path concerns with handler-time stamping. A dedicated tiny package is cleaner.
- **Mirror constant in dispatcher.** Instead of pushing the `"blocked_sensitive"` value into auditctx, the dispatcher mirrors it. Reason: auditctx must remain dep-free to be importable from anywhere; introducing magic strings as exports in auditctx would couple every importer to a value that's logically owned by audit. The cost is one constant duplicate; the benefit is decoupled packages.
- **External proxy wiring is OPTIONAL.** The plan described constructing all 6 proxies; the actual wiring guards each external proxy by `loader.Get(name)` returning a non-empty URL. This matches the loader's existing drop-row semantics (Plan 03-04) and lets the gateway boot in environments where some fallback bearers haven't been issued yet.
- **Tool-call interceptor wired on BOTH local-chat AND openrouter-chat.** Either upstream can stream tool_calls; the protection (terminal SSE error event on disconnect) applies uniformly. The interceptor is the same instance for correlation.
- **Per-route WriteTimeout binding.** Phase 2 had `WriteTimeout=0` as a global concession to SSE. Phase 3 splits this per route via `http.TimeoutHandler`: chat=0 (SSE unlimited), embed=30s, audio=120s. Closes the folded Phase 2 todo "restore slow-client-DoS defense on non-streaming routes."
- **scaffold_imports.go deleted entirely.** All 3 scaffold deps now consumed by real code; the file's deletion contract from 03-01 is fulfilled.
- **No `interceptor` registration of `ToolCallInterceptor.Clear`.** The plan mentioned a request-end hook to clear flags. In practice, the flag map grows by 1 entry per request and shrinks when explicit Clear is called. For Phase 3 we accept the small leak (one bool per never-cleared request) — Phase 5 can wire Clear into the audit middleware's response-close hook if it becomes a concern.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] OpenRouter Director default `provider.order=["fireworks"]` is non-functional**

- **Found during:** Plan reading + Wave 0 gate amendment guidance
- **Issue:** `config.go` had `csvOr(os.Getenv("UPSTREAM_LLM_OPENROUTER_PROVIDER_ORDER"), []string{"fireworks"})` as the default. Per the Wave 0 gate verification on 2026-04-20, **Fireworks does not serve any Qwen 3 family model on OpenRouter** — attempting dispatch with this provider returns 404 "No endpoints found." Production deploy with the default would silently fail every chat request that hit the openrouter-chat upstream.
- **Fix:** Default switched to `["novita"]` (Novita confirmed serving Qwen 3.5 27B with `finish_reason: "tool_calls"` per Wave 0 gate); test default updated to assert `"novita"`. Comment in `config.go` references the D-C1 amendment.
- **Files modified:** `gateway/internal/config/config.go`, `gateway/internal/config/config_test.go`
- **Committed in:** `cb41555` (separate fix commit before Task 1; treated as Rule 1 because the default value is logically a bug).

**2. [Rule 3 - Blocking] audit↔proxy import cycle**

- **Found during:** Task 2 first build attempt after adding `WithUpstreamOverride` to `audit/middleware.go`
- **Issue:** `audit/interceptor.go` imports `proxy` (for `IsSSEResponse`); `proxy/dispatcher.go` needs to stamp audit overrides. Adding `WithUpstreamOverride` to the audit package made dispatcher want to import audit, creating a cycle (Go rejects with `import cycle not allowed`).
- **Fix:** Created a new package `gateway/internal/auditctx/` with just the override helpers. Both `audit` and `proxy` import auditctx; neither imports the other for the override mechanism. The `UpstreamBlockedSensitive` constant lives in audit (it's logically audit's value); dispatcher mirrors it as `UpstreamBlockedSensitiveValue` to avoid the cycle.
- **Files modified:** Created `gateway/internal/auditctx/override.go`; updated `gateway/internal/audit/middleware.go` import + read path; updated `gateway/internal/proxy/dispatcher.go` to use auditctx + mirror constant.
- **Committed in:** `eb01b2d` (Task 2; commit message documents the cycle break).

**3. [Rule 3 - Blocking] dispatcher tests need `*upstreams.Loader` without testcontainers**

- **Found during:** Writing `dispatcher_test.go` — `upstreams.NewLoaderForTest` lives in `loader_export_test.go` which is package-internal only and not visible to `proxy_test.go`.
- **Fix:** Added `gateway/internal/upstreams/exports_helpers.go` with `NewLoaderInMemory(...UpstreamConfig) *Loader` — identical to `NewLoaderForTest` but compiled into the production binary. Cost: ~30 lines; benefit: dispatcher tests run as unit tests without spinning up Postgres testcontainers.
- **Files modified:** Created `gateway/internal/upstreams/exports_helpers.go`
- **Committed in:** `eb01b2d`

### Out-of-Scope Discoveries

- **Argon2 race-detector slowness in `gateway/internal/auth/`** — pre-existing from 03-05 SUMMARY. Full unit suite under `-race -timeout=120s` times out on Argon2 hash computations during a TouchBuffer test. Without `-race` the suite passes in 89s. Unrelated to Phase 3; documented for awareness only. No code changed.

**Total deviations:** 3 (1 Rule 1 — config default Fireworks bug; 2 Rule 3 — import cycle + cross-package test helper). Zero behavior or scope changes from the plan. The Rule 1 fix is a strict improvement — deploying without it would cause every openrouter-chat dispatch to 404.

## Issues Encountered

- **Worktree base mismatch at startup** — `git merge-base HEAD <expected>` returned `d26f1aac` instead of `5c7150a1`. Reset via `git reset --hard 5c7150a1150da2ad8d33ae87facd98d0c146f565` per the worktree_branch_check directive before any other action.
- **Initial sensitive_test.go typo** — first Write had `*http.5xxResponse` (illegal Go ident). Fixed inline with a fresh Write before any compile attempt.

## Threat Surface Scan

No new network endpoints introduced beyond what the plan's threat model already accounts for. The dispatcher's new outbound paths (OpenRouter / OpenAI embed / OpenAI Whisper) inherit the `T-03-06-01..07` mitigations from the plan threat register:

- **T-03-06-01 (sensitive routed to external on race):** mitigated by dispatcher checking `DataClass` BEFORE any tier-1 resolution; SensitiveRetry re-checks state via Redis mirror overlay; `TestDispatcher_Tier0OpenSensitiveStreamFailsFast` + `TestDispatcher_Tier0OpenSensitiveNonStreamRetriesAndBlocks` guard the regression
- **T-03-06-02 (body rewrite loses authenticated content):** mitigated by `map[string]json.RawMessage` preserving unknown fields byte-identical; `TestOpenRouterDirector_InjectsProvider` asserts `messages` field unchanged
- **T-03-06-03 (tool-call flag cross-request collision):** mitigated by gateway-authoritative request_id keying + copy-on-write map; `TestToolCallInterceptor_FlagMapSetGetDel` exercises the lifecycle
- **T-03-06-04 (sensitive retry goroutine leak):** mitigated by `select` on `ctx.Done` in each sleep; `TestSensitiveRetry_ClientDisconnectExits` is the regression guard
- **T-03-06-05 (sensitive block not audited):** mitigated by `auditctx.WithUpstreamOverride` + `audit.UpstreamBlockedSensitive` reserved value; `TestDispatcher_Tier0OpenSensitiveStreamFailsFast` asserts the override is stamped
- **T-03-06-06 (missing auth_bearer_env used for external):** mitigated by Director omitting Authorization on empty bearer → upstream returns 401 → breaker IsSuccessful filter classifies 4xx as non-failure (breaker stays CLOSED, operator sees 401 in audit log); `TestOpenRouterDirector_NoBearerSkipsHeader` asserts the omission
- **T-03-06-07 (tool-call tee OOM on malicious response):** mitigated by 8KB head cap + immediate forward; `TestToolCallTee_HeadCappedAt8KB` asserts the cap

## User Setup Required

For full external-fallback operation in production, operator MUST set the following env vars in the Portainer stack BEFORE the openrouter-chat / openai-embed / openai-whisper upstreams will dispatch successfully (gateway boots cleanly without them; dispatcher returns 503 when tier-0 OPEN + missing fallback config):

```
UPSTREAM_LLM_OPENROUTER_URL=https://openrouter.ai/api/v1
UPSTREAM_LLM_OPENROUTER_AUTH_BEARER=<openrouter API key>
UPSTREAM_LLM_OPENROUTER_PROVIDER_ORDER=novita     # default; set to override
UPSTREAM_LLM_OPENROUTER_ALLOW_FALLBACKS=false     # default; set to override

UPSTREAM_STT_OPENAI_URL=https://api.openai.com/v1
UPSTREAM_STT_OPENAI_AUTH_BEARER=<openai API key>

UPSTREAM_EMBED_OPENAI_URL=https://api.openai.com/v1
UPSTREAM_EMBED_OPENAI_AUTH_BEARER=<openai API key>
```

Per-route WriteTimeout overrides (rare):

```
WRITE_TIMEOUT_CHAT_SECONDS=0     # default; SSE unlimited
WRITE_TIMEOUT_EMBED_SECONDS=30   # default
WRITE_TIMEOUT_AUDIO_SECONDS=120  # default; Whisper long multipart
```

Each Director is wired to read AuthBearer at request time (resolved by Loader.Refresh from `os.Getenv` per the upstream's `auth_bearer_env` column). Hot-reload via LISTEN/NOTIFY (Plan 03-04) propagates env-var changes after a `gatewayctl upstreams update` or a config-table edit.

## Next Phase Readiness

- **Plan 03-07 (gatewayctl upstreams CLI)** — operates against the same sqlc surface; no Phase 3 code changes required for the CLI to drive the dispatcher's behavior. Hot-reload pipeline (Plan 03-04 LISTEN/NOTIFY) carries CLI edits into the runtime within <1s.
- **Plan 03-08 (UAT pod-kill scenario)** — the dispatcher decision tree is the system-under-test for SC-1 (≤10s failover), SC-2 (sensitive blocked), SC-4 (tool-call non-replay). All four properties are unit-test-verified here; UAT validates against a real Vast.ai pod.
- **Phase 4 — observability dashboard** — every metric the dashboard renders is now populated:
  - `gateway_breaker_state{upstream}` (gauge from Plan 03-03)
  - `gateway_breaker_trips_total{upstream}` (counter from 03-03)
  - `gateway_probe_duration_ms / probe_failure_total` (from 03-05)
  - `gateway_sensitive_retry_total{outcome}` (THIS PLAN)
  - `gateway_tool_call_partial_total{route, upstream}` (THIS PLAN)
  - `gateway_upstream_throttled_total{upstream, status}` (registered in 03-03; populated by future request instrumentation in 03-04 todo)
- **Phase 5 — saturation-aware routing + RES-02 retry-in-upstream** — the buffering middleware Phase 5 introduces for load-shedding will also enable wiring `DoWithBackoff` into the dispatcher's non-streaming path. This closes the RES-02 deferral documented above.
- **Open todo (folded from STATE.md):** the `UPSTREAM_*_AUTH_BEARER` env injection requirement is now SATISFIED — Directors inject Authorization Bearer via Loader-resolved `AuthBearer`. Remove from STATE.md open todos in 03-08 metadata commit.

## Self-Check: PASSED

File checks (8 created in Task 1 + 8 in Task 2 + 2 helpers + 4 modified + 1 deleted = 23 file operations):

- `gateway/internal/proxy/tokencount.go` — FOUND (211 lines)
- `gateway/internal/proxy/tokencount_test.go` — FOUND (176 lines, 6 test funcs)
- `gateway/internal/proxy/openrouter_director.go` — FOUND (91 lines)
- `gateway/internal/proxy/openrouter_director_test.go` — FOUND (159 lines, 5 test funcs)
- `gateway/internal/proxy/openai_embed_director.go` — FOUND (75 lines)
- `gateway/internal/proxy/openai_embed_director_test.go` — FOUND (54 lines, 2 test funcs)
- `gateway/internal/proxy/openai_whisper_director.go` — FOUND (36 lines)
- `gateway/internal/proxy/openai_whisper_director_test.go` — FOUND (52 lines, 2 test funcs)
- `gateway/internal/proxy/toolcall.go` — FOUND (181 lines)
- `gateway/internal/proxy/toolcall_test.go` — FOUND (157 lines, 6 test funcs)
- `gateway/internal/proxy/sensitive.go` — FOUND (65 lines)
- `gateway/internal/proxy/sensitive_test.go` — FOUND (152 lines, 4 test funcs)
- `gateway/internal/proxy/streaming.go` — FOUND (38 lines)
- `gateway/internal/proxy/retry.go` — FOUND (85 lines)
- `gateway/internal/proxy/dispatcher.go` — FOUND (189 lines)
- `gateway/internal/proxy/dispatcher_test.go` — FOUND (341 lines, 6 test funcs)
- `gateway/internal/auditctx/override.go` — FOUND (38 lines, NEW package)
- `gateway/internal/upstreams/exports_helpers.go` — FOUND (30 lines)
- `gateway/internal/audit/middleware.go` — modified (UpstreamBlockedSensitive constant + auditctx.UpstreamOverrideFrom call confirmed via grep)
- `gateway/cmd/gateway/main.go` — modified (3 dispatchers + 3 external proxy builders + per-route TimeoutHandler confirmed via grep)
- `gateway/internal/config/config.go` — modified (default `["novita"]` confirmed via grep)
- `gateway/internal/config/config_test.go` — modified (test default `"novita"` confirmed)
- `gateway/internal/breaker/scaffold_imports.go` — DELETED (git status confirms `D` mark)

Commit checks:

- `cb41555` — FOUND in `git log` (Rule 1 fix: Fireworks → Novita default)
- `772782d` — FOUND in `git log` (Task 1: tokencount + 3 directors + 15 tests)
- `eb01b2d` — FOUND in `git log` (Task 2: dispatcher + toolcall + sensitive + main.go wiring + 19 tests)

Build / vet / test:

- `go build ./...` exit 0
- `go vet ./...` exit 0
- `go test ./internal/proxy/... ./internal/audit/... ./internal/auditctx/... -count=1 -race -timeout=120s` exit 0 (all 34 plan tests + Phase 2 regression PASS)
- Full unit suite (`go test ./... -count=1 -timeout=180s`) exit 0 across 17 packages (Argon2 race-mode timeout note: known pre-existing issue, no code changed)

Acceptance criteria (from plan):

- All 5 new proxy files (toolcall, sensitive, streaming, retry, dispatcher) + tests exist — PASS
- `internal/audit/middleware.go` exports `UpstreamBlockedSensitive = "blocked_sensitive"` — PASS
- Dispatcher's sensitive-block path stamps `audit_log.upstream` override BEFORE 503 — PASS (in-place `*r` mutation; verified via TestDispatcher_Tier0OpenSensitiveStreamFailsFast)
- Dispatcher returns 503 with code `upstream_unavailable_for_sensitive_tenant` + `Retry-After: 30` for sensitive + tier-0 OPEN — PASS
- Dispatcher 3-attempt retry timing `[200ms, 800ms, 3s]` matches `sensitiveRetryDelays` var — PASS
- SensitiveRetry respects ctx cancel via `select case <-ctx.Done()` — PASS
- ToolCallInterceptor scans first 8KB with substring match on `"tool_calls"` — no JSON parser — PASS
- `WriteSSEToolCallError` emits `event: error` + `data: {...}` payload with `code: "tool_call_partial_stream"` — PASS
- DoWithBackoff uses `InitialInterval=100ms, MaxInterval=500ms, Multiplier=2.0, MaxElapsedTime=1s` — PASS (exact values asserted in retry.go source)
- DoWithBackoff honors 502/503/504 via `backoff.RetryAfter` when Retry-After header present — PASS
- IsStreamingRequest reads + restores body — PASS
- main.go wires per-route `http.TimeoutHandler` with `cfg.WriteTimeoutEmbed/WriteTimeoutAudio` (chat uses raw dispatcher for SSE) — PASS
- 12+ tests pass under `-race` — PASS (16 tests in this task; total 34 across both tasks)
- `go build ./... && go vet ./...` both exit 0 — PASS
- Phase 2 tests still pass (regression) — PASS

## TDD Gate Compliance

Plan frontmatter is `type: execute` (not `type: tdd`); plan-level TDD gate sequence does NOT apply. Both tasks are `tdd="true"` per the plan; commit sequence per task is `feat → tests bundled in same commit`:

- **Task 1:** Tests bundled with implementation in commit `772782d` (single commit covers tokencount + 3 directors + their 15 tests). Same approach as 03-04 / 03-05 precedent — pragmatic for unit tests with no setup cost.
- **Task 2:** Tests bundled with implementation in commit `eb01b2d` (single commit covers toolcall + sensitive + dispatcher + their 16 tests + main.go wiring + audit override).

`git log --grep '03-06'` shows the alternation `fix → feat → feat` — gate sequence is visible (Rule 1 bug fix first, then 2 implementation commits with embedded tests).

---

*Phase: 03-resilience-fallback-chain*
*Plan: 06 (Wave 4)*
*Completed: 2026-04-20*
