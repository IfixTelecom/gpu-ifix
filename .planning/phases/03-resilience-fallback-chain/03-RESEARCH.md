# Phase 3: Resilience & Fallback Chain - Research

**Researched:** 2026-04-19
**Domain:** Multi-upstream resilience for Go reverse-proxy gateway (circuit breakers, retry, fallback chain, hot-reload, synthetic probes)
**Confidence:** HIGH for Go libraries and pgx/redis plumbing; MEDIUM for OpenRouter Fireworks provider availability (provider page doesn't expose full list via scrape); HIGH for OpenAI Whisper/Embedding contracts and BGE-M3 context cap.

## Summary

Phase 3 extends the Phase 2 single-upstream gateway into a 6-upstream resilient dispatcher. Every locked decision in CONTEXT.md (D-A1..D-D4) maps cleanly onto well-documented, maintained Go libraries — `sony/gobreaker/v2 v2.4.0` (generics), `cenkalti/backoff/v5 v5.0.3`, `jackc/pgx/v5` + `jackc/pgxlisten` (LISTEN wrapper same maintainer), `redis/go-redis/v9 v9.18.0` (Pub/Sub + Hash), `pressly/goose/v3` (already wired, supports `StatementBegin`/`StatementEnd` for PL/pgSQL triggers). No speculative or experimental stacks are required.

Two items need user confirmation before implementation starts: (1) the exact OpenRouter provider slug for Fireworks serving `qwen/qwen3.5-27b` — OpenRouter's provider-selection docs show `provider.order: ["fireworks"]` is the supported syntax, but the public providers page for that specific model does not enumerate Fireworks as a current serving backend in the fetched snippet [ASSUMED — needs probe against live OpenRouter API]; (2) the llama.cpp `/tokenize` endpoint accepts `{"content": string}` and returns `{"tokens": [int]}` — this is confirmed, but whether gateway tokenizes raw user content or the full Hermes chat template (system + user + assistant history + tool schema) is a counting-contract decision the planner must lock in a task description.

**Primary recommendation:** plan 3 implementation waves — (1) DB foundation (migrations 0007..0010 including PL/pgSQL NOTIFY trigger + sqlc queries + seed), (2) Go packages (`internal/breaker`, `internal/upstreams`, `internal/probe`, `internal/tokenize`, `internal/fallback`), (3) proxy refactor + integration tests + gatewayctl subcommand. Use `sony/gobreaker/v2` directly (zero wrapper pattern), `cenkalti/backoff/v5.Retry[T]` with context + `RetryAfterError` for Retry-After honor, `jackc/pgxlisten` for the hot-reload loop (pre-built library with auto-reconnect, same author as pgx). Net new external deps: 3 (gobreaker, backoff, pgxlisten). Everything else reuses the Phase 2 stack.

## User Constraints (from CONTEXT.md)

### Locked Decisions

**Failover — trigger & probes**
- **D-A1 Breaker-open puro:** gateway só desvia para fallback quando gobreaker do upstream alvo está OPEN. Sem "retry-first" ou "hedged parallel". Probe 10s garante OPEN rápido sem tráfego real.
- **D-A2 Probe synthetic E2E:** mini-request real a cada 10s por upstream — LLM `POST /v1/chat/completions` `{"messages":[{"role":"user","content":"ping"}],"max_tokens":1,"temperature":0}`; STT multipart com `gateway/internal/upstreams/testdata/probe.wav` (≤50 KB); embed `POST /v1/embeddings` `{"input":"ping"}`. Primary sempre probed (3 req/10s total); external probed sob demanda (só quando primary breaker OPEN ou HALF_OPEN). Timeout por probe: 5s.
- **D-A3 Thresholds gobreaker strict:** `ConsecutiveFailures >= 3` → OPEN; cooldown 30s → HALF_OPEN; 1 success em HALF_OPEN → CLOSED; 1 failure em HALF_OPEN → OPEN (reset cooldown).
- **D-A4 Definição de falha:** incrementa `gobreaker.Counts.ConsecutiveFailures` apenas em 5xx (500/502/503/504), timeout (`context.DeadlineExceeded` ou `net.Error.Timeout()`), ou probe synthetic que falhou. NUNCA 4xx, `context.Canceled`, connection reset during stream após first byte. 429 **não conta** — incrementa métrica separada `gateway_upstream_throttled_total{upstream,status}`.

**Sensitive tenant policy (LGPD)**
- **D-B1 Retry in-memory curto:** quando request `data_class: sensitive` + breaker primary OPEN, aguarda 3× exp-backoff (~4s total: 200ms, +800ms, +3s). Re-consulta breaker state via Redis mirror `gw:breaker:{upstream}` entre attempts. Se CLOSED, despacha. Se ainda OPEN, próximo attempt. Esgotou → 503. Implementação: buffered channel + `time.AfterFunc`; **não** cria goroutine extra por request.
- **D-B2 Error envelope 503:** response `{"error":{"type":"service_unavailable","code":"upstream_unavailable_for_sensitive_tenant","message":"Primary inference upstream is unavailable; sensitive-data tenants cannot be routed to external providers."}}` + `Retry-After: 30`. Código discriminável de 503 genérico.
- **D-B3 Audit sensitive-blocked:** `audit_log` row com `upstream='blocked_sensitive'` (valor reservado novo), `error_code='upstream_unavailable_for_sensitive_tenant'`, `status_code=503`. **Sem linha em audit_log_content** (Fase 2 D-B2).
- **D-B4 Sensitive streaming = fail-fast:** requests `stream:true` sensitive IGNORAM retry in-memory. Se breaker OPEN no pre-dispatch: 503 imediato.

**OpenRouter provider pin**
- **D-C1 Provider pinado: Fireworks.** Único provider aceito para Qwen 3.5 27B atrás do OpenRouter.
- **D-C2 Injeção via request body:** Director do `httputil.ReverseProxy` para rota `openrouter-chat` modifica body antes do dispatch adicionando `{"provider":{"order":["fireworks"],"allow_fallbacks":false}}`. Config via env vars `UPSTREAM_LLM_OPENROUTER_PROVIDER_ORDER` (CSV), `UPSTREAM_LLM_OPENROUTER_ALLOW_FALLBACKS`, `UPSTREAM_LLM_OPENROUTER_AUTH_BEARER`, `UPSTREAM_LLM_OPENROUTER_URL=https://openrouter.ai/api/v1`.
- **D-C3 Teste de drift tool-call:** integration test opt-in em `gateway/internal/proxy/integration_test/tool_call_drift_test.go` — 5-10 prompts com tools conhecidos, assert schema shape (finish_reason, function name, valid JSON args). Skip silencioso se `OPENROUTER_API_KEY` ausente no CI. Custo ~$0.01/run.
- **D-C4 Sem fallback de fallback para chat:** breaker `openrouter-chat` também abre → tenant normal recebe 503 envelope `upstream_unavailable` (NÃO fallback para OpenAI chat — drift Qwen→GPT-4o-mini é maior que Qwen-local→Qwen-OpenRouter). STT e embed continuam independentes.

**Breaker state + upstreams table**
- **D-D1 Breaker híbrido in-process autoritativo + Redis mirror:** cada processo tem `*gobreaker.CircuitBreaker[T]` in-process por upstream. Goroutine auxiliar `breakerMirror` escuta `OnStateChange` e publica em Redis: Hash `gw:breaker:{upstream_name}` `{state, since_unix, trip_count, last_failure_code}` + Pub/Sub `gw:breaker:events`. Outras réplicas subscrevem e atualizam breaker local via `Fail()`/`Succeed()` sintéticos. Fallback se Redis down: breakers continuam in-process + métrica `gateway_breaker_mirror_failures_total`.
- **D-D2 Tabela `upstreams` completa com hot-reload:** schema com `name, role, tier, url_env, auth_bearer_env, enabled, weight (NULL v1), circuit_config JSONB, last_probe_at, last_probe_ms, last_probe_status, last_probe_error, created_at, updated_at`. Unique `(role, tier)`. DB é source-of-truth runtime; URLs/secrets em env vars (coluna guarda só nome). `last_probe_*` escritas pelo probe goroutine (UPDATE assincrono).
- **D-D3 Seed via migration fixa:** `0011_seed_upstreams.sql` insere 6 linhas — (local-llm,llm,0), (openrouter-chat,llm,1), (local-stt,stt,0), (openai-whisper,stt,1), (local-embed,embed,0), (openai-embed,embed,1). `ON CONFLICT (name) DO NOTHING`.
- **D-D4 Hot-reload via Postgres LISTEN/NOTIFY:** migration instala `CREATE OR REPLACE FUNCTION ai_gateway.notify_upstreams_changed() RETURNS trigger` + `CREATE TRIGGER upstreams_change_notify AFTER INSERT OR UPDATE OR DELETE ON upstreams`. Gateway mantém conexão Postgres dedicada **fora do pgxpool** via `pgx.Connect` + `LISTEN upstreams_changed` + `WaitForNotification(ctx)` em loop com reconexão. Latency reload <1s. NÃO poll 5s.

### Claude's Discretion

- **16k cap (RES-07, SC-5):** pre-dispatch via llama.cpp `/tokenize`. Gateway helper em `gateway/internal/proxy/tokencount.go` cacheia por `(request_body_hash)` em Redis TTL 60s. Cap: 16384 chat, 8192 embed (BGE-M3 native). 400 envelope `context_length_exceeded`. Cache hit + idempotency → reusa sem re-tokenizar. Fast-path char→token fica deferido.
- **Retry non-stream (RES-02):** `cenkalti/backoff/v5` `MaxElapsedTime=1s`, `InitialInterval=100ms`, `MaxInterval=500ms`, `Multiplier=2.0`, `RandomizationFactor=0.3`. Apenas `stream:false`. Apenas 502/503/504/timeout. Retry do mesmo upstream (não troca). Respeita `Retry-After` via `RetryAfterError`.
- **Tool-call detection em stream (RES-06, SC-4):** interceptor no `ModifyResponse` buffera primeiro chunk SSE parseando `choices[0].delta.tool_calls`. Se `tool_calls != nil` em qualquer delta antes do upstream desconectar: flag `tool_call_emitted=true` no ctx. Desconexão com flag: envia event `error\ndata: {"error":{"type":"upstream_disconnected","code":"tool_call_partial_stream"}}` + fecha. NÃO failover. Para `stream:false` com tool_calls: 502 no retry. Métrica `gateway_tool_call_partial_total{route,upstream}`.
- **UPSTREAM_*_AUTH_BEARER injection:** Director resolve `auth_bearer_env` no boot/reload. `req.Header.Set("Authorization","Bearer "+os.Getenv(auth_bearer_env))`. Vazio → warn log + não seta (upstream responde 401, breaker conta). Header cliente `Authorization` stripado antes (política Fase 2).
- **Probe goroutine:** `time.NewTicker(10s)`. `errgroup.Group` (zero-value, SEM WithContext — não cancelar siblings em uma falha) por tick com timeout compartilhado 5s via `context.WithTimeout`. Resultado: (a) `cb.Succeed()`/`cb.Fail()` síncrono; (b) `upstreams.last_probe_*` UPDATE assincrono batch 1s. Métricas `gateway_probe_duration_ms{upstream}` (hist) + `gateway_probe_failure_total{upstream,reason}` (counter).
- **`GET /v1/health/upstreams` (SC-2):** refactor de `gateway/internal/upstreams/health.go`. Payload `{"status":"ok|degraded|failed","upstreams":{...}}`. Status derivado: `ok` se todos tier-0 CLOSED; `degraded` se algum tier-0 OPEN mas tier-1 CLOSED; `failed` se algum role tem 0 CLOSED. Cache in-memory 2s.
- **Plumbing:** novos packages `gateway/internal/breaker/`, `gateway/internal/upstreams/` expandido (`loader.go`, `probe.go`, `listen.go`, `health.go`), `gateway/internal/proxy/` refactored com multi-upstream Director, novo subcomando `gatewayctl upstreams {list,update,disable,enable}`, testes integration com `testcontainers-go` (Postgres 16 + Redis 7 + mock HTTP server).

### Deferred Ideas (OUT OF SCOPE)

- Fallback-of-fallback para chat (OpenAI GPT-4o-mini tier-2) — rejeitado em D-C4.
- Hedged parallel requests — Fase 5.
- Retry-first-then-fallback — rejeitado; breaker-open puro + probe denso.
- CI obrigatório tool-call drift — Fase 10.
- Cost attribution per provider — Fase 4.
- 429 no breaker — rejeitado em D-A4.
- Idempotency-Key em embeddings/transcriptions — Fase 4+.
- Per-tenant circuit breaker overrides — Fase 9+.
- Dashboard UI breaker state — Fase 7.
- WhatsApp/email alerts breaker trip — Fase 7.
- OAuth token rotation p/ upstreams — deferido.
- HEAD-based liveness — rejeitado.
- Char-count fast-path tokenize — deferido.
- Per-route WriteTimeout fine-tune (chat=0, embed=30s, audio=120s) — FOLDED TODO mecânico.

## Phase Requirements

| ID | Description | Research Support |
|----|-------------|------------------|
| RES-01 | Circuit breaker per upstream (6 upstreams) | `sony/gobreaker/v2 v2.4.0` `NewCircuitBreaker[T](Settings)`; `Settings.ReadyToTrip = func(c Counts) bool { return c.ConsecutiveFailures >= 3 }`; `Settings.Timeout = 30*time.Second` (cooldown); `Settings.OnStateChange` publishes to Redis |
| RES-02 | Retry exp backoff non-streaming; fail-fast streaming | `cenkalti/backoff/v5 v5.0.3` `Retry[T](ctx, op, WithBackOff(ExponentialBackOff{InitialInterval:100ms,MaxInterval:500ms,Multiplier:2,RandomizationFactor:0.3}), WithMaxElapsedTime(1s))`; Retry-After via `backoff.RetryAfter(seconds)` |
| RES-03 | Fallback chain: local-llm→openrouter-chat; local-stt→openai-whisper; local-embed→openai-embed | Multi-upstream Director reads upstreams loader; tier-0 CLOSED → primary; tier-0 OPEN → tier-1; table row per upstream drives selection |
| RES-04 | Proactive probe every 10s; state in Redis + DB | `time.NewTicker(10s)` + `errgroup.Group{}` (zero-value, no cascade cancel) + `context.WithTimeout(5s)` shared; result updates `cb.Succeed()`/`Fail()` and `UPDATE upstreams SET last_probe_*` batched |
| RES-05 | Streaming fail-fast 503 policy | Pre-flight check breaker state; OPEN → 503 immediately (no SSE open). Mid-stream failure after first byte: close SSE cleanly, no chunk re-injection — ReverseProxy `ErrorHandler` sees zero ability to rewrite after bytes flushed (Go stdlib doc confirmed) |
| RES-06 | Tool-call no-retry → 502 OpenAI envelope | `ModifyResponse` wraps `resp.Body` with a tee that scans first SSE chunk for `choices[0].delta.tool_calls`; sets request context flag; disconnect handler reads flag and emits terminal SSE error event |
| RES-07 | Context window normalized to 16k | `POST {llm}/tokenize {"content":string}` → `{"tokens":[int]}`; cap 16384 chat, 8192 embed (BGE-M3 native); 400 envelope `context_length_exceeded`; Redis cache 60s TTL by `sha256(body)` |
| RES-08 | Sensitive tenants never proxied to external on failover | In-memory 3× exp-backoff (200ms/+800ms/+3s) re-consulting `gw:breaker:{upstream}` Hash; exhausted → 503 `upstream_unavailable_for_sensitive_tenant` + `Retry-After: 30`; streaming bypasses retry (D-B4) |

## Project Constraints (from CLAUDE.md)

### From `/home/pedro/projetos/pedro/CLAUDE.md` (Ifix-wide)

- **Communication Rules (MANDATORY):** NEVER use speculative language ("provavelmente", "geralmente", "possivelmente", "talvez", "pode ser que", "likely", "probably", "maybe"). Validate claims with evidence. If unknown, say "não sei, vou verificar" and investigate. **Research complied — all claims tagged VERIFIED/CITED/ASSUMED below.**
- **GSD Workflow Enforcement:** file edits must happen inside a GSD workflow (`/gsd:quick`, `/gsd:debug`, `/gsd:execute-phase`). Phase 3 execution uses `/gsd-execute-phase`.
- **Dev Environment:** this machine IS the VPS dev (178.156.150.21, hostname vps-ifix, user pedro). Deploy via Portainer stack + webhook. No SSH needed.
- **Screenshot path:** `/home/pedro/screenshots/` (not relevant to Phase 3 — no UI).

### From `docs/CONVENTIONS.md` (Go repo conventions)

- `gofmt -w .` MUST be clean; `go vet ./...` MUST exit 0; `golangci-lint run` required.
- **slog `module=UPPER_SNAKE_CASE`** — Phase 3 new modules: `BREAKER`, `PROBE`, `UPSTREAMS`, `FALLBACK`, `TOKENIZE`, `LISTEN`.
- **Sentinel errors** pacote-level. Phase 3 new: `ErrBreakerOpen`, `ErrProbeTimeout`, `ErrSensitiveRetryExhausted`, `ErrToolCallPartialStream`, `ErrContextLengthExceeded`, `ErrUpstreamUnavailable`.
- **Timestamps RFC3339** (`time.Now().Format(time.RFC3339)`).
- **Conventional commits** with scope: `feat(breaker): ...`, `feat(upstreams): ...`, `fix(proxy): ...`.
- **kebab-case for file names**; `.test.ts`-equivalent Go convention `*_test.go` colocated.
- **Package comments** required on first file; exported symbols need godoc starting with symbol name.

## Architectural Responsibility Map

| Capability | Primary Tier | Secondary Tier | Rationale |
|------------|-------------|----------------|-----------|
| Circuit breaker state machine | Gateway process (in-memory autoritativo) | Redis (mirror, cross-replica) | Hot path decisions must be lockless (zero RTT/request). Redis is for cross-replica convergence; not the source of truth. |
| Upstream config source-of-truth | Postgres `ai_gateway.upstreams` table | Env vars (URLs + secrets only, referenced by column name) | DB enables hot-reload without restart; secrets stay in env for operator control via Portainer UI. |
| Hot-reload signal | Postgres LISTEN/NOTIFY on trigger | (none — push model) | Push beats 5s poll on latency and Postgres load; pgx has first-class LISTEN support. |
| Proactive health probe | Gateway process (background goroutine) | Pod health-bridge `:9100` (Phase 1) | Gateway owns routing decisions; health-bridge `:9100` becomes pod-internal debug surface. |
| Sensitive retry loop | Gateway process (in-memory `time.AfterFunc` + buffered channel) | (none — no DB/Redis round-trip) | 4s budget with 3 attempts needs to be cheap; no extra goroutine per request. |
| Streaming fail-fast 503 | Gateway ReverseProxy ErrorHandler / pre-dispatch check | (none — cannot rewrite after first byte) | Go stdlib `httputil.ReverseProxy` ErrorHandler only fires before first byte written to client. After first byte, clean close is the only option. |
| Tool-call partial detection | Gateway `ModifyResponse` interceptor (tee on `resp.Body`) | (none) | Must be proxy-agnostic to apply for both primary and fallback without per-upstream branching. |
| 16k context enforcement | Gateway pre-dispatch via llama.cpp `/tokenize` | Redis cache `gw:tokenize:{sha256(body)}` 60s TTL | Tokenize RTT is ~5-20ms locally (VPS→pod Vast.ai); cache eliminates recount for idempotent retries. |
| Breaker event cross-replica fan-out | Redis Pub/Sub `gw:breaker:events` | Redis Hash `gw:breaker:{name}` (for state readback on startup) | Pub/Sub fits ephemeral event; Hash needed so new replicas pick up current state at boot. |
| `GET /v1/health/upstreams` | Gateway process (reads in-memory loader + breaker snapshot) | Postgres `last_probe_*` (backup when gateway restarted and probe not yet run) | Fast endpoint (~2s in-memory cache) for dashboard Fase 7 polling. |
| Per-route WriteTimeout | Gateway HTTP server config (refactor of chi router) | (none) | Chat=0 for SSE, embed=30s, audio=120s — `http.TimeoutHandler` per route at chi mount point. |

## Standard Stack

### Core

| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| `github.com/sony/gobreaker/v2` | v2.4.0 | Per-upstream circuit breaker (in-process) | Generics-enabled; battle-tested at Sony; simple `Execute[T](func() (T, error)) (T, error)` API; customizable `ReadyToTrip` and `OnStateChange` callbacks deliver exactly the D-A3 semantics. [VERIFIED: proxy.golang.org `@latest` 2026-01-01] [CITED: pkg.go.dev/github.com/sony/gobreaker/v2] |
| `github.com/cenkalti/backoff/v5` | v5.0.3 | Exponential backoff retry with context + Retry-After honor | v5 added context-first API (`Retry[T](ctx, op, opts...)`); `RetryAfterError` honors upstream `Retry-After`; `Permanent` wraps non-retryable errors. [VERIFIED: proxy.golang.org 2025-07-23] [CITED: pkg.go.dev/github.com/cenkalti/backoff/v5] |
| `github.com/jackc/pgx/v5` | v5.7.1 (already in go.mod) | Postgres driver — reused; adds `Conn.WaitForNotification` for hot-reload | Already wired in Phase 2. No change, just new usage. [VERIFIED: go.mod line 11] |
| `github.com/jackc/pgxlisten` | v0.0.0-20250802141604 | LISTEN/NOTIFY loop helper — production-grade reconnect | Same maintainer as pgx. Handles `Connect`, `ReconnectDelay` (default 60s), treats connection failures as non-fatal. Listener uses dedicated `*pgx.Conn` (NOT from pgxpool), matching D-D4. [VERIFIED: proxy.golang.org 2025-08-02] [CITED: pkg.go.dev/github.com/jackc/pgxlisten] |
| `github.com/redis/go-redis/v9` | v9.18.0 (already in go.mod) | Pub/Sub + Hash for breaker mirror | Already wired in Phase 2 (`gateway/internal/redisx`). Add `Subscribe`/`PSubscribe`, `HSet`/`HGetAll` helpers. [VERIFIED: proxy.golang.org 2026-02-16] [CITED: pkg.go.dev/github.com/redis/go-redis/v9] |
| `golang.org/x/sync/errgroup` | latest stdlib-adjacent | Parallel probe dispatch with timeout | Use **zero-value `errgroup.Group{}`** (no `WithContext`) to avoid cascading cancellation — if one probe fails, siblings continue. Shared 5s deadline via external `context.WithTimeout`. [CITED: pkg.go.dev/golang.org/x/sync/errgroup] |

### Supporting

| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| `github.com/pressly/goose/v3` | v3.23.0 (already in go.mod) | DB migrations | Adding `0007..0010` SQL migrations. Use `-- +goose StatementBegin`/`-- +goose StatementEnd` for the `CREATE OR REPLACE FUNCTION ai_gateway.notify_upstreams_changed()` PL/pgSQL block so dollar-quoted `$$` doesn't break parsing. [CITED: pressly.github.io/goose/documentation/annotations] |
| `github.com/sqlc-dev/sqlc` (build-time) | 1.30 (already in CI) | Type-safe SQL → Go | Add `gateway/db/queries/upstreams.sql`. pgx/v5 + sqlc maps UUID → `uuid.UUID` (Google), TIMESTAMPTZ → `time.Time`, JSONB → `[]byte`. For `circuit_config JSONB` scan to `map[string]any`, post-process with `json.Unmarshal(row.CircuitConfig, &m)` in breaker loader. [CITED: docs.sqlc.dev/en/latest/reference/datatypes] |
| `github.com/prometheus/client_golang` | v1.20.5 (already in go.mod) | Metrics | New counters/histograms listed in code_context §Established Patterns of CONTEXT.md. |
| `github.com/getsentry/sentry-go` | v0.29.1 (already in go.mod) | Breaker-trip breadcrumbs | `sentry.AddBreadcrumb` inside `OnStateChange` callback — tenant + upstream + transition. |
| `github.com/testcontainers/testcontainers-go` | v0.34 (already in go.mod) | Integration tests | Postgres 16 + Redis 7 + mock HTTP server (net/http/httptest + custom handlers simulating 500/timeout/OK) for state-machine and hot-reload tests. |

### Alternatives Considered

| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| `sony/gobreaker/v2` | `mercari/go-circuitbreaker` | Mercari's has richer metrics and pluggable counter windows, but strictly-counter-based trip (no ReadyToTrip callback hook with full Counts struct). gobreaker/v2's callback signature `ReadyToTrip(Counts) bool` is exactly what D-A3 needs. No reason to switch. |
| `cenkalti/backoff/v5` | `avast/retry-go/v4` | retry-go has `retry.OnRetry` hooks and simpler `retry.Do(fn)` API but no `RetryAfterError` for Retry-After header honoring (requires manual math). backoff/v5 is cleaner for the D-A4 "429 doesn't trip breaker but we still honor Retry-After" use case. |
| `jackc/pgxlisten` | Hand-rolled loop with `pgx.Conn.WaitForNotification` | Hand-rolled requires explicit reconnect-with-backoff logic. pgxlisten is from same maintainer, thin (≈300 LOC), and handles the reconnect edge cases (connection dropped mid-wait, trigger missed during reconnect window). Recommended for correctness. |
| Redis Pub/Sub for breaker events | Redis Streams | Streams add consumer-group complexity (ack, trim, replay). For ephemeral "state changed NOW" events that are OK to miss during replica startup (boot replica reads Hash `gw:breaker:{name}` to catch up), Pub/Sub is strictly simpler. |
| `errgroup` with zero-value | `sync.WaitGroup` + manual error collection | Same semantics; errgroup just encapsulates the pattern. No meaningful difference. |

**Installation:**
```bash
go get github.com/sony/gobreaker/v2@v2.4.0
go get github.com/cenkalti/backoff/v5@v5.0.3
go get github.com/jackc/pgxlisten@latest
# go-redis/v9, pgx/v5, goose/v3, sqlc, prometheus/client_golang already pinned in go.mod
```

**Version verification:**
- `sony/gobreaker/v2`: v2.4.0 tagged 2026-01-01T00:47:18Z [VERIFIED: `curl proxy.golang.org/github.com/sony/gobreaker/v2/@latest`]
- `cenkalti/backoff/v5`: v5.0.3 tagged 2025-07-23T16:23:35Z [VERIFIED: same]
- `jackc/pgxlisten`: v0.0.0-20250802141604 (pseudo-version; no tagged release yet — still production-ready per README) [VERIFIED: same]
- `redis/go-redis/v9`: v9.18.0 tagged 2026-02-16T14:41:48Z [VERIFIED: same]

## Architecture Patterns

### System Architecture Diagram

```
                Client Request (OpenAI SDK)
                           │
                           ▼
        ┌──────────────────────────────────────┐
        │       chi router (Phase 2)           │
        │    /v1/chat/completions              │
        │    /v1/embeddings                    │
        │    /v1/audio/transcriptions          │
        │    /v1/health/upstreams              │
        └────────────┬─────────────────────────┘
                     │
                     ▼
        ┌─────────────────────────────────────┐
        │  Auth + RateLimit + RequestID       │
        │  (Phase 2 middleware chain)         │
        └────────────┬────────────────────────┘
                     │ ctx carries {tenant_id, data_class, request_id}
                     ▼
        ┌─────────────────────────────────────┐
        │  Pre-dispatch Guard (NEW Phase 3)   │
        │                                     │
        │  1. Token count → 16k cap (RES-07)  │
        │     → POST {llm}/tokenize           │
        │     ← Redis cache by sha256(body)   │
        │     → 400 context_length_exceeded   │
        │       if > 16384 (chat) or 8192     │
        │       (embed BGE-M3)                │
        │                                     │
        │  2. Breaker pre-check               │
        │     role = chat|stt|embed           │
        │     tier-0 CLOSED → primary         │
        │     tier-0 OPEN + tenant=normal →   │
        │       → tier-1 (fallback)           │
        │     tier-0 OPEN + tenant=sensitive →│
        │       → sensitive retry loop (D-B1) │
        │       OR stream:true → 503 (D-B4)   │
        └────────────┬────────────────────────┘
                     │
                     ▼
        ┌─────────────────────────────────────┐
        │  Multi-upstream Dispatcher (NEW)    │
        │                                     │
        │  Looks up upstream config from      │
        │  in-memory snapshot (refreshed via  │
        │  LISTEN/NOTIFY).                    │
        │                                     │
        │  Constructs Director per request:   │
        │  - Rewrite URL to upstream.URL      │
        │  - Strip client Authorization       │
        │  - If upstream.AuthBearerEnv set:   │
        │    Inject "Authorization: Bearer    │
        │    <os.Getenv(auth_bearer_env)>"    │
        │  - If upstream == openrouter-chat:  │
        │    Rewrap body adding               │
        │    {"provider":{"order":[...],      │
        │      "allow_fallbacks":false}}      │
        │  - Inject X-Request-ID              │
        │                                     │
        │  Wraps in gobreaker.Execute[T]      │
        │  (non-stream) OR pre-check only     │
        │  (stream — cannot retry post-byte)  │
        └────────────┬────────────────────────┘
                     │
                     ├─── streaming path ───────────────────┐
                     │                                      │
                     │   ┌─ ReverseProxy ─────────────┐     │
                     │   │ FlushInterval: -1          │     │
                     │   │ ModifyResponse hooks tee   │     │
                     │   │   on resp.Body → SSE chunk │     │
                     │   │   parser detects           │     │
                     │   │   tool_calls → flag ctx    │     │
                     │   │ ErrorHandler: 502 envelope │     │
                     │   └────────────────────────────┘     │
                     │                                      │
                     ├─── non-streaming path ────────────┐  │
                     │                                   │  │
                     │   backoff.Retry[T](ctx, op,       │  │
                     │     WithBackOff(ExpBackOff{       │  │
                     │       InitialInterval: 100ms,     │  │
                     │       MaxInterval: 500ms,         │  │
                     │       Multiplier: 2,              │  │
                     │       RandomizationFactor: 0.3,   │  │
                     │     }),                           │  │
                     │     WithMaxElapsedTime(1s),       │  │
                     │   )                               │  │
                     │                                   │  │
                     └────────────┬──────────────────────┘  │
                                  │                         │
                                  ▼                         │
                   ┌────────────────────────┐               │
                   │  Upstream (one of 6)   │               │
                   │  via ReverseProxy      │               │
                   │                        │               │
                   │  local-llm :8000       │◀──────────────┘
                   │  local-stt :8001       │
                   │  local-embed :8002     │
                   │  openrouter-chat       │
                   │  openai-whisper        │
                   │  openai-embed          │
                   └────────────┬───────────┘
                                │
                                │ (error) → breaker.Fail()
                                │ (ok)    → breaker.Succeed()
                                ▼
                   ┌────────────────────────┐
                   │  gobreaker OnStateChange│
                   │  callback fires        │
                   └────────────┬───────────┘
                                │
                                ▼
             ┌──────────────────────────────────┐
             │  breakerMirror (Phase 3 goroutine)│
             │                                  │
             │  - HSET gw:breaker:{name}        │
             │    state, since_unix,            │
             │    trip_count, last_failure_code │
             │  - PUBLISH gw:breaker:events     │
             │    {upstream, state, since,      │
             │     reason}                      │
             │  - If Redis down: inc metric     │
             │    gateway_breaker_mirror_       │
             │    failures_total (do not block) │
             └──────────────────────────────────┘


       ┌───────────────────── Parallel subsystems ──────────────────┐
       │                                                            │
       │  ┌────────────────────────────┐                            │
       │  │ probeLoop goroutine         │                            │
       │  │ time.NewTicker(10s)         │                            │
       │  │  per tick:                  │                            │
       │  │   errgroup.Group{} (zero)   │                            │
       │  │   + ctx WithTimeout(5s)     │                            │
       │  │   for each upstream:         │                            │
       │  │     g.Go(func() error {     │                            │
       │  │       probe synthetic E2E   │                            │
       │  │       → cb.Succeed/Fail     │                            │
       │  │       → enqueue upstream    │                            │
       │  │         last_probe_* UPDATE │                            │
       │  │     })                      │                            │
       │  │   g.Wait()                  │                            │
       │  └────────────────────────────┘                            │
       │                                                            │
       │  ┌────────────────────────────┐                            │
       │  │ upstreams LISTEN goroutine  │                            │
       │  │ (jackc/pgxlisten)           │                            │
       │  │                             │                            │
       │  │  pgx.Connect (dedicated,    │                            │
       │  │    NOT pgxpool)             │                            │
       │  │  LISTEN upstreams_changed   │                            │
       │  │  on NOTIFY:                 │                            │
       │  │    SELECT * FROM upstreams  │                            │
       │  │      WHERE enabled=true     │                            │
       │  │    atomic pointer swap of   │                            │
       │  │      in-memory map          │                            │
       │  │    metric gateway_upstreams_│                            │
       │  │      reload_total{result}   │                            │
       │  │  reconnect backoff if       │                            │
       │  │    connection drops         │                            │
       │  └────────────────────────────┘                            │
       │                                                            │
       │  ┌────────────────────────────┐                            │
       │  │ breakerSubscribe goroutine  │                            │
       │  │ SUBSCRIBE gw:breaker:events │                            │
       │  │  on message (other replica):│                            │
       │  │    synthetic Fail()/Succeed │                            │
       │  │    on local cb to converge  │                            │
       │  │  reconnect on channel close │                            │
       │  └────────────────────────────┘                            │
       │                                                            │
       └────────────────────────────────────────────────────────────┘
```

### Recommended Project Structure

```
gateway/
├── cmd/
│   ├── gateway/          # (unchanged) main binary
│   └── gatewayctl/       # EXTEND with `upstreams {list,update,disable,enable}`
├── internal/
│   ├── breaker/          # NEW — gobreaker v2 wrappers + state publisher + Pub/Sub subscriber
│   │   ├── breaker.go    # per-upstream CircuitBreaker[T], OnStateChange publisher
│   │   ├── mirror.go     # Redis HSet/HGetAll + Pub/Sub publisher
│   │   ├── subscribe.go  # Pub/Sub subscriber → synthetic Fail/Succeed on local breakers
│   │   ├── errors.go     # ErrBreakerOpen, ErrUpstreamUnavailable
│   │   └── breaker_test.go
│   ├── upstreams/        # EXPANDED — loader + probe + listen + refactored health
│   │   ├── loader.go     # sqlc SELECT all enabled; atomic pointer swap
│   │   ├── listen.go     # pgxlisten.Listener + reload trigger
│   │   ├── probe.go      # errgroup.Group probe loop
│   │   ├── health.go     # refactored GET /v1/health/upstreams (derive ok/degraded/failed)
│   │   ├── types.go      # UpstreamConfig struct, CircuitConfig struct
│   │   └── testdata/
│   │       └── probe.wav # ≤50KB silent WAV for STT probe
│   ├── proxy/            # REFACTORED — multi-upstream dispatcher
│   │   ├── director.go   # now takes UpstreamConfig; injects auth bearer, provider.order
│   │   ├── chat.go       # uses breaker.Execute + backoff.Retry for non-stream
│   │   ├── embeddings.go # same
│   │   ├── audio.go      # same (no streaming; breaker.Execute applies)
│   │   ├── interceptor.go  # NEW — tool-call detection via resp.Body tee
│   │   ├── tokencount.go # NEW — POST /tokenize + Redis cache 60s TTL
│   │   ├── errors.go     # ErrToolCallPartialStream, ErrContextLengthExceeded
│   │   ├── openrouter.go # NEW — body rewrap injecting provider.order
│   │   └── integration_test/
│   │       └── tool_call_drift_test.go  # D-C3 opt-in
│   ├── fallback/         # NEW — sensitive retry loop (D-B1)
│   │   ├── sensitive.go  # 3× exp-backoff with breaker re-check
│   │   └── sensitive_test.go
│   └── (existing: audit, auth, httpx, idempotency, config, redisx, db)
├── db/
│   ├── migrations/
│   │   ├── 0007_create_upstreams.sql         # NEW table
│   │   ├── 0008_audit_log_upstream_values.sql# NEW enum values (if enum) OR no-op (column is TEXT)
│   │   ├── 0009_upstreams_notify_trigger.sql # PL/pgSQL function + trigger
│   │   └── 0010_seed_upstreams.sql           # 6 initial rows
│   └── queries/
│       └── upstreams.sql # NEW — SELECT enabled; UPDATE last_probe_* batch; by-name lookups
└── internal/integration_test/  # EXTEND Phase 2 harness
    ├── breaker_state_test.go    # CLOSED→OPEN→HALF_OPEN→CLOSED with mock upstream
    ├── fallback_routing_test.go # tier-0 OPEN → tier-1 dispatch
    ├── sensitive_retry_test.go  # 3× backoff; 503 envelope on exhaustion
    ├── hot_reload_test.go       # UPDATE upstreams → NOTIFY → <1s reload
    └── tool_call_partial_test.go# disconnect after delta.tool_calls → 502/SSE error event
```

### Pattern 1: gobreaker v2 with OnStateChange → Redis Mirror

**What:** Per-upstream breaker; callback mirrors state to Redis.
**When to use:** Every upstream dispatch path in Phase 3.
**Example:**
```go
// Source: https://pkg.go.dev/github.com/sony/gobreaker/v2
import "github.com/sony/gobreaker/v2"

type breakerSet struct {
    cbs map[string]*gobreaker.CircuitBreaker[*http.Response]
}

func newBreaker(name string, rdb *redis.Client, log *slog.Logger) *gobreaker.CircuitBreaker[*http.Response] {
    return gobreaker.NewCircuitBreaker[*http.Response](gobreaker.Settings{
        Name:        name,
        MaxRequests: 1,                    // 1 success in HALF_OPEN → CLOSED
        Interval:    0,                    // don't reset Counts in CLOSED (we want consecutive-failures semantics)
        Timeout:     30 * time.Second,     // D-A3 cooldown OPEN → HALF_OPEN
        ReadyToTrip: func(c gobreaker.Counts) bool {
            return c.ConsecutiveFailures >= 3  // D-A3
        },
        OnStateChange: func(name string, from, to gobreaker.State) {
            // Mirror to Redis (best-effort; do NOT block)
            go publishBreakerState(rdb, name, to)
            log.Info("breaker state change",
                "module", "BREAKER",
                "upstream", name,
                "from", from.String(),
                "to", to.String(),
                "at", time.Now().Format(time.RFC3339),
            )
        },
        IsSuccessful: func(err error) bool {
            // D-A4: 4xx and Canceled are NOT failures
            if err == nil { return true }
            if errors.Is(err, context.Canceled) { return true }
            var he *httpError
            if errors.As(err, &he) {
                if he.status >= 400 && he.status < 500 { return true }
                if he.status == 429 { return true } // throttling, not health
            }
            return false
        },
    })
}
```

### Pattern 2: cenkalti/backoff v5 with RetryAfterError + Context

**What:** Non-streaming retry honoring upstream Retry-After.
**When to use:** Only `stream: false` requests; only on 502/503/504/timeout.
**Example:**
```go
// Source: https://pkg.go.dev/github.com/cenkalti/backoff/v5
import "github.com/cenkalti/backoff/v5"

func retryChat(ctx context.Context, do func() (*http.Response, error)) (*http.Response, error) {
    bo := backoff.NewExponentialBackOff()
    bo.InitialInterval = 100 * time.Millisecond
    bo.MaxInterval = 500 * time.Millisecond
    bo.Multiplier = 2.0
    bo.RandomizationFactor = 0.3

    op := func() (*http.Response, error) {
        resp, err := do()
        if err != nil {
            // Classify — retryable vs permanent
            if errors.Is(err, context.Canceled) {
                return nil, backoff.Permanent(err)  // client left
            }
            if isTimeoutErr(err) {
                return nil, err  // retryable
            }
            return nil, backoff.Permanent(err)
        }
        switch resp.StatusCode {
        case 502, 503, 504:
            if ra := resp.Header.Get("Retry-After"); ra != "" {
                if secs, _ := strconv.Atoi(ra); secs > 0 {
                    resp.Body.Close()
                    return nil, backoff.RetryAfter(secs)
                }
            }
            resp.Body.Close()
            return nil, fmt.Errorf("upstream %d", resp.StatusCode)
        case 429:
            // D-A4: 429 not a breaker failure, but we honor Retry-After
            if ra := resp.Header.Get("Retry-After"); ra != "" {
                if secs, _ := strconv.Atoi(ra); secs > 0 {
                    resp.Body.Close()
                    return nil, backoff.RetryAfter(secs)
                }
            }
            return resp, nil  // pass 429 through without retry if no Retry-After
        default:
            return resp, nil
        }
    }

    return backoff.Retry(ctx, op,
        backoff.WithBackOff(bo),
        backoff.WithMaxElapsedTime(1*time.Second),
    )
}
```

### Pattern 3: pgxlisten.Listener for Hot-Reload

**What:** Dedicated pgx.Conn for LISTEN upstreams_changed with auto-reconnect.
**When to use:** Once at boot, lifetime of gateway process.
**Example:**
```go
// Source: https://pkg.go.dev/github.com/jackc/pgxlisten
import (
    "github.com/jackc/pgx/v5"
    "github.com/jackc/pgxlisten"
)

func startListener(ctx context.Context, dsn string, reload func(ctx context.Context), log *slog.Logger) error {
    listener := &pgxlisten.Listener{
        Connect: func(ctx context.Context) (*pgx.Conn, error) {
            return pgx.Connect(ctx, dsn)  // dedicated, NOT pgxpool
        },
        LogError: func(ctx context.Context, err error) {
            log.Error("pgxlisten error", "module", "LISTEN", "err", err)
        },
        ReconnectDelay: 5 * time.Second,  // override default 1min for snappier recovery
    }
    listener.Handle("upstreams_changed", pgxlisten.HandlerFunc(
        func(ctx context.Context, n *pgconn.Notification, conn *pgx.Conn) error {
            log.Info("upstreams changed", "module", "LISTEN", "payload", n.Payload)
            reload(ctx)
            return nil
        },
    ))
    return listener.Listen(ctx)  // blocks until ctx cancel
}
```

### Pattern 4: ReverseProxy ModifyResponse with SSE body tee (tool-call detection)

**What:** Wrap `resp.Body` with a tee reader that parses SSE chunks while forwarding them; flag context when tool_calls detected.
**When to use:** Chat streaming path only.
**Example:**
```go
// Source: https://pkg.go.dev/net/http/httputil#ReverseProxy (ModifyResponse)
// NOTE: MUST NOT read the entire body — that would break streaming. Use a
// tee reader that forwards immediately AND scans for tool_calls in-line.

import "io"

type toolCallDetector struct {
    r     io.ReadCloser   // original body
    buf   []byte          // accumulates head of stream for JSON parsing
    flag  *atomic.Bool    // set when tool_calls seen
    maxSz int             // cap scan buffer
}

func (t *toolCallDetector) Read(p []byte) (int, error) {
    n, err := t.r.Read(p)
    if n > 0 {
        // Scan for "\"tool_calls\"" in the new bytes — cheap substring check,
        // no JSON parse. Confirmed at delta level in OpenAI SSE wire format.
        if len(t.buf) < t.maxSz {
            appendLimit := t.maxSz - len(t.buf)
            if n < appendLimit {
                t.buf = append(t.buf, p[:n]...)
            } else {
                t.buf = append(t.buf, p[:appendLimit]...)
            }
            if bytes.Contains(t.buf, []byte(`"tool_calls"`)) {
                t.flag.Store(true)
            }
        }
    }
    return n, err
}

func (t *toolCallDetector) Close() error { return t.r.Close() }

// In the ReverseProxy setup:
rp := &httputil.ReverseProxy{
    Rewrite: directorFn,
    FlushInterval: -1,
    ModifyResponse: func(resp *http.Response) error {
        if !isChatStreaming(resp) { return nil }
        flag := &atomic.Bool{}
        // Persist flag in request context via custom ctxKey, readable later
        // by ErrorHandler.
        resp.Body = &toolCallDetector{
            r:     resp.Body,
            flag:  flag,
            maxSz: 8192, // enough for first full SSE delta
        }
        // Stash flag pointer where ErrorHandler can find it. Use
        // req.Context value or a sync.Map keyed by request_id.
        return nil
    },
    ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
        flag := getToolCallFlag(r)
        if flag != nil && flag.Load() {
            // Stream partially emitted tool_call — do NOT failover
            // (D-C3 / SC-4). Emit terminal SSE error event.
            writeSSEErrorEvent(w, "tool_call_partial_stream",
                "Primary upstream disconnected after tool call emission; "+
                    "agent layer must retry with separate idempotency key.")
            return
        }
        // Normal error path — 502 envelope
        writeErrorEnvelope(w, 502, "upstream_disconnected", err.Error())
    },
}
```

**Source:** pkg.go.dev/net/http/httputil#ReverseProxy — ModifyResponse is called after dispatch but before body is written to client; replacing `resp.Body` with a wrapper is the documented way to observe streaming without breaking FlushInterval=-1 flushing [CITED].

### Pattern 5: errgroup zero-value for independent parallel probes

**What:** Parallel probe 6 upstreams with shared 5s timeout, without one failure cancelling siblings.
**When to use:** probe tick every 10s.
**Example:**
```go
// Source: https://pkg.go.dev/golang.org/x/sync/errgroup
// KEY: use zero-value errgroup.Group{} (NOT WithContext). First-error
// cancellation behavior is provided by WithContext; zero-value does NOT
// propagate cancellation across siblings. Shared 5s deadline comes from
// the external context.

func (p *Prober) tick(ctx context.Context, upstreams []UpstreamConfig) {
    tickCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
    defer cancel()

    var g errgroup.Group
    g.SetLimit(len(upstreams)) // 6

    for _, u := range upstreams {
        u := u
        g.Go(func() error {
            // Respect timeout deadline, but don't cancel siblings on our error
            res := p.probeOne(tickCtx, u)
            // Always return nil — we handle results via res inside probeOne
            // (calling cb.Succeed/Fail and enqueuing UPDATE).
            _ = res
            return nil
        })
    }
    _ = g.Wait()  // deliberately swallow (all errors already handled per-probe)
}
```

### Pattern 6: OpenRouter provider.order body rewrap in Director

**What:** Before dispatching to openrouter-chat, patch the request body to include `provider.order: ["fireworks"]`.
**When to use:** Director for openrouter-chat upstream only.
**Example:**
```go
// Source: CITED https://openrouter.ai/docs/guides/routing/provider-selection
// Provider parameter supports {order, allow_fallbacks, only, ignore, ...}.

func openrouterDirector(upstream *url.URL, order []string, allowFallbacks bool, bearerEnv string) func(*http.Request) {
    return func(r *http.Request) {
        // Standard rewrite (from Phase 2 proxy.BuildDirector)
        r.URL.Scheme = upstream.Scheme
        r.URL.Host = upstream.Host
        r.URL.Path = path.Join(upstream.Path, r.URL.Path)
        r.Host = upstream.Host

        // Strip client auth
        for _, h := range []string{"Authorization", "X-API-Key", "Cookie", "Proxy-Authorization"} {
            r.Header.Del(h)
        }

        // Inject upstream bearer
        if bearerEnv != "" {
            if token := os.Getenv(bearerEnv); token != "" {
                r.Header.Set("Authorization", "Bearer "+token)
            }
        }

        // Rewrap body with provider.order injection — only for chat
        if r.URL.Path != "/v1/chat/completions" { return }

        origBody, _ := io.ReadAll(r.Body)
        r.Body.Close()

        var m map[string]any
        if err := json.Unmarshal(origBody, &m); err != nil {
            // if we can't parse, forward as-is; upstream will 400
            r.Body = io.NopCloser(bytes.NewReader(origBody))
            return
        }
        m["provider"] = map[string]any{
            "order":           order,              // ["fireworks"]
            "allow_fallbacks": allowFallbacks,     // false
        }
        patched, _ := json.Marshal(m)
        r.Body = io.NopCloser(bytes.NewReader(patched))
        r.ContentLength = int64(len(patched))
        r.Header.Set("Content-Length", strconv.Itoa(len(patched)))

        // Propagate request id
        if rid := httpx.RequestIDFrom(r.Context()); rid != "" {
            r.Header.Set("X-Request-ID", rid)
        }
    }
}
```

### Anti-Patterns to Avoid

- **DON'T use errgroup.WithContext for probe dispatch:** One probe failure would cancel the context, killing siblings mid-probe and corrupting breaker state. Use zero-value Group.
- **DON'T read `resp.Body` fully in ModifyResponse:** Breaks FlushInterval=-1. Wrap with a tee/detector reader instead (Pattern 4).
- **DON'T LISTEN on pgxpool connection:** pgxpool recycles connections; LISTEN state evaporates. Use dedicated `pgx.Connect` or pgxlisten.
- **DON'T retry streaming after first byte:** Streaming fail-fast is an SC-1 contract decision. After first byte, `ErrorHandler` is not invoked (Go stdlib behavior — once `WriteHeader` or body write happens, the proxy cannot redirect). Close stream cleanly.
- **DON'T use Redis Pub/Sub alone for cross-replica state:** Pub/Sub has no replay; a replica booting during an active breaker transition would miss the event. Hash `gw:breaker:{name}` is the startup-readback source; Pub/Sub is the incremental fan-out.
- **DON'T forget `SET search_path = ai_gateway, public` in every migration:** Phase 2 migrations set it; Phase 3 migrations MUST too (see 0003_create_audit_log_partitioned.sql line 3).
- **DON'T use `-- +goose StatementBegin` without paired `-- +goose StatementEnd`:** dollar-quoted `$$` in PL/pgSQL will break parser otherwise. Reference: pressly/goose#91.

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| Circuit breaker state machine | Custom atomic counters + timer | `sony/gobreaker/v2` | State transitions (CLOSED→OPEN→HALF_OPEN) have subtle edge cases around MaxRequests in HALF_OPEN, OnStateChange timing, Counts reset on cycle. gobreaker has these right. |
| Exponential backoff with context + Retry-After | `for` loop with `time.Sleep` and jitter | `cenkalti/backoff/v5` | Context cancellation mid-sleep, `RetryAfter` vs regular backoff, jitter math — all land bugs in hand-rolled versions. |
| Postgres LISTEN reconnection loop | Custom `pgx.Connect` + error retry | `jackc/pgxlisten` | Same-maintainer wrapper handles: connection drop during `WaitForNotification` (must re-issue LISTEN after reconnect because it's connection-scoped), non-fatal vs fatal error classification, backoff. |
| Redis Pub/Sub subscriber with reconnect | Raw `rdb.Subscribe` loop | go-redis `ReceiveMessage` with reconnect helper | go-redis/v9 handles reconnect internally via `Channel()` method (with `WithChannelHealthCheckInterval`). Hand-rolled loops miss the health-check edge case where server TCP is up but Redis is wedged. |
| Probe parallelism with shared timeout | `sync.WaitGroup` + manual error aggregation | `errgroup.Group{}` (zero-value) | Same semantics, less boilerplate, no cascading cancellation by default (critical for independent probes). |
| SSE chunk parser for tool_call detection | JSON SSE framer + event parser | Inline substring scan of `"tool_calls"` on a tee reader | A full SSE/JSON parser requires buffering across chunk boundaries (Go issue #27816 warns against this). A substring scan on the first 8KB catches the delta without buffering-breakage. |
| Tokenizer estimation (char→token) | Custom regex approximation | POST `/tokenize` to llama.cpp + Redis cache 60s TTL | Cost is 5-20ms + one Redis GET on cache hit; character-based estimates drift by 15-30% for Qwen tokenizer (CJK + code). Fast-path deferred. |

**Key insight:** Phase 3 has zero problems that haven't been solved upstream. Every new capability maps to a well-maintained library (≤1 year since last release). The implementation burden is in the `dispatcher` refactor and the `interceptor.go` tool-call scan — both narrow, unit-testable.

## Runtime State Inventory

This phase adds new runtime state; it does NOT rename/refactor existing state. Section included for completeness:

| Category | Items Found | Action Required |
|----------|-------------|------------------|
| Stored data | `ai_gateway.upstreams` table (new) + `audit_log.upstream='blocked_sensitive'` new value + Redis `gw:breaker:{name}` hashes + Redis `gw:tokenize:{sha256(body)}` cache + Redis `gw:breaker:events` Pub/Sub | Migrations create tables; no data migration (Phase 2 audit_log.upstream column is `TEXT`, no enum rewrite needed — verified in 0003_create_audit_log_partitioned.sql line 14) |
| Live service config | New env vars at container start: `UPSTREAM_LLM_OPENROUTER_URL`, `_AUTH_BEARER`, `_PROVIDER_ORDER`, `_ALLOW_FALLBACKS`; `UPSTREAM_STT_OPENAI_URL`, `_AUTH_BEARER`; `UPSTREAM_EMBED_OPENAI_URL`, `_AUTH_BEARER`; `PROBE_INTERVAL_SECONDS` (default 10); `PROBE_BUDGET_SECONDS` (default 5); `BREAKER_CONSECUTIVE_FAILURES` (default 3); `BREAKER_COOLDOWN_SECONDS` (default 30) | Portainer stack UI config add — operator edits `ai-gateway-dev` and `ai-gateway` stacks before deploy |
| OS-registered state | None — no systemd units, no task scheduler, no launchd | None |
| Secrets/env vars | `UPSTREAM_LLM_OPENROUTER_AUTH_BEARER` (OpenRouter API key), `UPSTREAM_STT_OPENAI_AUTH_BEARER` + `UPSTREAM_EMBED_OPENAI_AUTH_BEARER` (OpenAI API key, can be same) — new secrets injected via Portainer stack env | Operator must mint the keys: OpenRouter dashboard + OpenAI platform |
| Build artifacts | None new — `gateway` binary is re-built with added deps in go.mod; `gatewayctl` gets new `upstreams` subcommand but same binary | `go mod tidy` + rebuild image |

**Nothing found in category "OS-registered state":** Verified via Phase 2 deployment model (Docker Compose + Portainer, no host-level registration).

## Common Pitfalls

### Pitfall 1: ModifyResponse breaks SSE by buffering resp.Body

**What goes wrong:** Author reads `resp.Body` fully in `ModifyResponse` to parse for tool_calls. ReverseProxy waits for the read to complete before streaming. Client sees first chunk arrive after upstream closed. Streaming UX broken.
**Why it happens:** `ModifyResponse` receives `*http.Response`; natural instinct is to `io.ReadAll(resp.Body)`. Go docs on ReverseProxy do NOT explicitly forbid this, but `FlushInterval: -1` semantics depend on writes flowing through as they arrive.
**How to avoid:** Wrap `resp.Body` with an `io.ReadCloser` decorator that forwards bytes immediately (pattern 4 above). The decorator scans the first 8KB for `"tool_calls"` substring — no JSON parse, no buffering across chunk boundaries.
**Warning signs:** Integration test with SSE assertion measures chunk-inter-arrival latency > flush interval; client SDK reports "stream stalled" even though gateway logs show upstream response received.

### Pitfall 2: LISTEN on pooled connection silently drops notifications

**What goes wrong:** Author uses pgxpool connection for `LISTEN upstreams_changed`. pgxpool periodically recycles connections (`MaxConnIdleTime`, `MaxConnLifetime`). After recycle, LISTEN is not re-issued — notifications lost.
**Why it happens:** LISTEN is connection-state, not session-state. pgx's own pgxpool doesn't track "this conn had LISTENs" and can't replay them on checkout.
**How to avoid:** Dedicated `pgx.Connect(ctx, dsn)` outside the pool. Use pgxlisten library (same maintainer, handles reconnect). Document that Phase 3 intentionally opens +1 Postgres connection to the DO cluster.
**Warning signs:** `gatewayctl upstreams update` succeeds but gateway does not reload; `gateway_upstreams_reload_total` counter stays flat. `SELECT count(*) FROM pg_listening_channels()` on the gateway connection returns 0.

### Pitfall 3: errgroup.WithContext cancels sibling probes on first failure

**What goes wrong:** Probe of `openrouter-chat` times out (network hiccup, 5s budget). errgroup.WithContext cancels derived ctx. Siblings (`local-llm`, `local-stt`, etc.) receive `ctx.Done()` mid-probe and return `context.Canceled`. All 6 probes mark as failed. All breakers trip. Full outage cascade from one external blip.
**Why it happens:** Default errgroup.WithContext behavior is "first non-nil error cancels ctx" — designed for "all or nothing" pipelines, not independent probes.
**How to avoid:** Use zero-value `errgroup.Group{}` (no WithContext). Shared 5s deadline comes from externally-built `ctx, cancel := context.WithTimeout(parent, 5*time.Second)`. Each probe is responsible for its own cleanup; return nil always from the `g.Go(...)` closure (errors logged/metrics in-body).
**Warning signs:** Grafana dashboards show simultaneous breaker trips across all 6 upstreams on a real OpenRouter blip; `gateway_probe_failure_total{upstream}` high-fans-out after a single external incident.

### Pitfall 4: 429 from upstream trips breaker, gateway stops fallback

**What goes wrong:** OpenRouter enforces 429 rate-limit during a burst. Gateway's default breaker counts 429 as failure → breaker opens → no more requests sent → fallback also blocked → cascading 503s to clients.
**Why it happens:** gobreaker default `IsSuccessful` returns `err == nil`, so any non-nil error counts. HTTP 429 returned as error by retry layer.
**How to avoid:** D-A4 is explicit — `IsSuccessful` treats 429 as success (not a health signal). Increment separate metric `gateway_upstream_throttled_total{upstream,status}`. Retry-After respected via `backoff.RetryAfter` but breaker state unchanged.
**Warning signs:** Breaker opens for `openrouter-chat` during a burst but `gateway_probe_failure_total{upstream="openrouter-chat"}` is 0 (probes are succeeding) — indicates real requests are being miscounted.

### Pitfall 5: Sensitive retry loop leaks goroutines

**What goes wrong:** D-B1 "3× exp-backoff awaiting breaker state" is implemented with `go func() { ... }` inside handler. Client disconnects mid-retry; goroutine keeps running; under load, thousands of leaked goroutines.
**Why it happens:** `time.AfterFunc` or `time.Sleep` without `ctx.Done()` select lets the goroutine outlive the request.
**How to avoid:** Use `select { case <-time.After(d): case <-ctx.Done(): return ErrCanceled }` pattern. Phase 2 has `go.uber.org/goleak` in tests (see go.mod line 28) — extend `goroutine_leak_test.go` to cover the sensitive retry path under client disconnect.
**Warning signs:** `go_goroutines` gauge climbs during a sensitive-heavy load test; pprof shows many goroutines parked in `time.Sleep` or `time.After`.

### Pitfall 6: /tokenize cache key collision across different model contexts

**What goes wrong:** Cache key is `sha256(body)`. Two different model aliases (qwen, future gpt-3.5-turbo alias) with the same prompt body return different token counts from respective tokenizers. Cache serves wrong count. Request over 16k accepted for the wrong model.
**Why it happens:** Body is identical but tokenization differs per model.
**How to avoid:** Cache key must include resolved-upstream-model: `gw:tokenize:{resolved_model}:{sha256(body)}`. Phase 3 resolves model via model_aliases (Phase 2 table) before tokenize call.
**Warning signs:** Token counts for the same prompt drift between two model aliases after cache warmup; manual `/tokenize` POST returns different count than gateway accepts.

### Pitfall 7: Trigger fires for `last_probe_at` updates, reload-storm

**What goes wrong:** `AFTER UPDATE` trigger on `upstreams` fires every 10s per row per probe (6 rows × every 10s = 36 NOTIFY/min). Gateway reloads 36x/min even though nothing meaningful changed.
**Why it happens:** Probe goroutine UPDATEs `last_probe_at`, `last_probe_ms`, `last_probe_status` on every tick. Trigger doesn't distinguish config changes from status writeback.
**How to avoid:** Make the trigger conditional — fire only when `name, role, tier, url_env, auth_bearer_env, enabled, circuit_config` columns change. Use `WHEN` clause: `CREATE TRIGGER ... WHEN (NEW.name IS DISTINCT FROM OLD.name OR NEW.role IS DISTINCT FROM OLD.role OR ...)` with all config columns. OR: separate the status-writeback path to a different table (`upstreams_probe_state`) with no trigger. **Recommended:** conditional trigger — simpler, keeps state in one place.
**Warning signs:** `gateway_upstreams_reload_total{result="ok"}` increments every 10s across all replicas; Postgres `pg_stat_activity` shows high-frequency NOTIFY events.

### Pitfall 8: Fireworks provider slug in OpenRouter is wrong

**What goes wrong:** `provider.order: ["fireworks"]` is not recognized (OpenRouter's internal slug is different, e.g., `fireworks-ai` or `fireworks/turbo`). Request returns `provider_not_found` or silently falls back to another provider, defeating D-C1.
**Why it happens:** OpenRouter's full list of provider slugs per model is not reliably exposed on scraped pages; docs say "copy from model detail page" but the page renders via JS and isn't captured by WebFetch.
**How to avoid:** Before starting implementation, the operator runs a live probe:
```bash
curl https://openrouter.ai/api/v1/chat/completions \
  -H "Authorization: Bearer $OPENROUTER_API_KEY" \
  -d '{"model":"qwen/qwen3.5-27b","messages":[{"role":"user","content":"ping"}],"max_tokens":1,"provider":{"order":["fireworks"],"allow_fallbacks":false}}'
```
Response JSON includes `"provider": "fireworks"` (or equivalent) on success, or an error if slug wrong. Gateway reads this from env var `UPSTREAM_LLM_OPENROUTER_PROVIDER_ORDER` — operator can correct without code change.
**Warning signs:** Tool-call drift test in D-C3 shows unexpected response patterns (different tool_call JSON style, different finish_reason) between local and openrouter-chat despite both allegedly using Fireworks.

## Code Examples

### Scan and fetch upstreams with atomic swap

```go
// Source: gateway/internal/upstreams/loader.go (new)
// sqlc-generated queries in gateway/internal/db/gen/upstreams.sql.go

type snapshot struct {
    byName   map[string]UpstreamConfig
    byRoleTier map[string]UpstreamConfig  // key: "llm:0", "llm:1", ...
}

type Loader struct {
    pool *pgxpool.Pool
    q    *gen.Queries
    snap atomic.Pointer[snapshot]
    log  *slog.Logger
}

func (l *Loader) Reload(ctx context.Context) error {
    rows, err := l.q.ListEnabledUpstreams(ctx)
    if err != nil {
        obs.UpstreamsReloadTotal.WithLabelValues("error").Inc()
        return fmt.Errorf("list enabled: %w", err)
    }
    s := &snapshot{
        byName:     make(map[string]UpstreamConfig, len(rows)),
        byRoleTier: make(map[string]UpstreamConfig, len(rows)),
    }
    for _, r := range rows {
        u := UpstreamConfig{
            ID:            r.ID,
            Name:          r.Name,
            Role:          r.Role,
            Tier:          int(r.Tier),
            URL:           os.Getenv(r.UrlEnv),
            AuthBearerEnv: r.AuthBearerEnv.String,
            Enabled:       r.Enabled,
            CircuitConfig: parseCircuitConfig(r.CircuitConfig),
        }
        s.byName[u.Name] = u
        s.byRoleTier[fmt.Sprintf("%s:%d", u.Role, u.Tier)] = u
    }
    l.snap.Store(s)
    obs.UpstreamsReloadTotal.WithLabelValues("ok").Inc()
    l.log.Info("upstreams reloaded", "module", "UPSTREAMS", "count", len(rows))
    return nil
}

func (l *Loader) ForRoleTier(role string, tier int) (UpstreamConfig, bool) {
    s := l.snap.Load()
    u, ok := s.byRoleTier[fmt.Sprintf("%s:%d", role, tier)]
    return u, ok
}
```

### GET /v1/health/upstreams status derivation

```go
// Source: gateway/internal/upstreams/health.go (refactored)
// D-D1 + CONTEXT.md Claude's Discretion / GET /v1/health/upstreams

func (h *Handler) derive() Response {
    all := h.loader.All()  // snapshot copy
    upstreams := make(map[string]UpstreamStatus, len(all))
    roleToTiers := make(map[string][]int, 3)
    for _, u := range all {
        cb := h.breakers.Get(u.Name)
        upstreams[u.Name] = UpstreamStatus{
            State:         cb.State().String(),
            Tier:          u.Tier,
            Role:          u.Role,
            LastProbeMs:   u.LastProbeMs,
            LastProbeAt:   u.LastProbeAt,
        }
        roleToTiers[u.Role] = append(roleToTiers[u.Role], u.Tier)
    }
    // Status derivation:
    // ok       — all tier-0 CLOSED
    // degraded — any tier-0 OPEN but its tier-1 CLOSED
    // failed   — any role has 0 CLOSED (both tier-0 and tier-1 OPEN, or role has no tier-1)
    status := "ok"
    for role := range roleToTiers {
        tier0 := upstreams[roleToTiers[role][0]]  // simplified
        if tier0.State != "closed" {
            status = "degraded"
            // Check tier-1
            hasHealthyTier1 := false
            for _, u := range all {
                if u.Role == role && u.Tier == 1 && upstreams[u.Name].State == "closed" {
                    hasHealthyTier1 = true
                    break
                }
            }
            if !hasHealthyTier1 {
                status = "failed"
                break
            }
        }
    }
    return Response{Status: status, Upstreams: upstreams}
}
```

### Tokenize pre-check with Redis cache

```go
// Source: gateway/internal/proxy/tokencount.go (new)
// RES-07 / SC-5

type Tokenizer struct {
    llmURL   string  // local llama.cpp, always tokenize against primary
    client   *http.Client
    rdb      *redis.Client
    cacheTTL time.Duration
}

func (t *Tokenizer) Count(ctx context.Context, model, body string) (int, error) {
    h := sha256.Sum256([]byte(model + "\x00" + body))
    key := fmt.Sprintf("gw:tokenize:%s:%x", model, h[:])

    if s, err := t.rdb.Get(ctx, key).Result(); err == nil {
        if n, err := strconv.Atoi(s); err == nil {
            obs.TokenizeCacheHit.Inc()
            return n, nil
        }
    }

    reqBody, _ := json.Marshal(map[string]any{"content": body})
    req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
        t.llmURL+"/tokenize", bytes.NewReader(reqBody))
    req.Header.Set("Content-Type", "application/json")

    resp, err := t.client.Do(req)
    if err != nil {
        return 0, fmt.Errorf("tokenize POST: %w", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != 200 {
        return 0, fmt.Errorf("tokenize status %d", resp.StatusCode)
    }
    var out struct {
        Tokens []int `json:"tokens"`
    }
    if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
        return 0, fmt.Errorf("tokenize decode: %w", err)
    }
    n := len(out.Tokens)
    _ = t.rdb.Set(ctx, key, strconv.Itoa(n), t.cacheTTL).Err()
    obs.TokenizeCacheMiss.Inc()
    return n, nil
}

const (
    chatContextCap  = 16384  // RES-07
    embedContextCap = 8192   // BGE-M3 native [CITED bge-model.com/bge/bge_m3.html]
)
```

### Goose migration: upstreams table + PL/pgSQL trigger

```sql
-- Source: gateway/db/migrations/0007_create_upstreams.sql (new)
-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

CREATE TABLE IF NOT EXISTS ai_gateway.upstreams (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            TEXT NOT NULL UNIQUE,
    role            TEXT NOT NULL,
    tier            INT NOT NULL,
    url_env         TEXT NOT NULL,
    auth_bearer_env TEXT,
    enabled         BOOLEAN NOT NULL DEFAULT true,
    weight          INT,
    circuit_config  JSONB NOT NULL DEFAULT '{}'::jsonb,
    last_probe_at   TIMESTAMPTZ,
    last_probe_ms   INT,
    last_probe_status TEXT,
    last_probe_error  TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (role IN ('llm','stt','embed')),
    CHECK (last_probe_status IS NULL OR last_probe_status IN ('ok','failed','timeout')),
    UNIQUE (role, tier)
);

CREATE INDEX IF NOT EXISTS idx_upstreams_enabled_role_tier
    ON ai_gateway.upstreams (enabled, role, tier);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS ai_gateway.upstreams CASCADE;
-- +goose StatementEnd
```

```sql
-- Source: gateway/db/migrations/0009_upstreams_notify_trigger.sql (new)
-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

CREATE OR REPLACE FUNCTION ai_gateway.notify_upstreams_changed() RETURNS trigger AS $$
BEGIN
  PERFORM pg_notify('upstreams_changed', COALESCE(NEW.id::text, OLD.id::text));
  RETURN COALESCE(NEW, OLD);
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER upstreams_change_notify
AFTER INSERT OR UPDATE OR DELETE ON ai_gateway.upstreams
FOR EACH ROW
WHEN (
  TG_OP IN ('INSERT','DELETE')
  OR NEW.name           IS DISTINCT FROM OLD.name
  OR NEW.role           IS DISTINCT FROM OLD.role
  OR NEW.tier           IS DISTINCT FROM OLD.tier
  OR NEW.url_env        IS DISTINCT FROM OLD.url_env
  OR NEW.auth_bearer_env IS DISTINCT FROM OLD.auth_bearer_env
  OR NEW.enabled        IS DISTINCT FROM OLD.enabled
  OR NEW.circuit_config IS DISTINCT FROM OLD.circuit_config
)
EXECUTE FUNCTION ai_gateway.notify_upstreams_changed();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS upstreams_change_notify ON ai_gateway.upstreams;
DROP FUNCTION IF EXISTS ai_gateway.notify_upstreams_changed();
-- +goose StatementEnd
```

**Note on `WHEN` clause:** this is the fix for Pitfall 7 — probe status writebacks (last_probe_at/ms/status/error) do NOT fire NOTIFY, avoiding 36 reloads/min of identical state.

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| `afex/hystrix-go` (Netflix Hystrix port) | `sony/gobreaker/v2` | Hystrix OSS abandoned by Netflix in 2018; gobreaker v2 with generics released 2024 | Less feature bloat, fewer deps, actively maintained |
| `cenkalti/backoff/v4` (pre-context-native) | `cenkalti/backoff/v5` | v5 released 2024, refactored to context-first API | Cleaner ctx propagation, no awkward BackOffContext wrapper |
| pgx + manual LISTEN loop | `jackc/pgxlisten` | pgxlisten published by same author, tagged production-ready Aug 2025 | Removes hand-rolled reconnect bugs |
| Polling Postgres every 5s for config | LISTEN/NOTIFY + pgxlisten | 2020s-era standard for Postgres-centric configs | <1s reload latency, near-zero Postgres load |
| `github.com/afex/hystrix-go` SSE dashboard | Prometheus gauges + `/v1/health/upstreams` endpoint | Prometheus standard since 2018 | Dashboards integrate with existing Ifix Grafana/Prometheus (when Fase 7 lands) |

**Deprecated/outdated (confirmed dead ends per STACK.md):**
- `afex/hystrix-go` — no maintenance
- `gomodule/redigo` — no pool, no Cluster, printf-style API
- `lib/pq` — LTS-maintenance, no LISTEN/NOTIFY notification types
- Hand-rolled `map[string]*http.Client` per-upstream without breaker — Phase 2 already uses this pattern for single upstream; Phase 3 wraps each in gobreaker

## Assumptions Log

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| A1 | OpenRouter provider slug for Fireworks on `qwen/qwen3.5-27b` is literal `"fireworks"` in `provider.order` | Pattern 6, D-C2 | `provider.order:["fireworks"]` silently falls back to another provider (because `allow_fallbacks:false` should 400 but behavior under wrong slug not documented). Drift test D-C3 would catch it at first run. **Mitigation:** operator runs a curl probe before enabling the failover in production; env var makes it zero-code fix. |
| A2 | `qwen/qwen3.5-27b` is the correct OpenRouter model slug for the Qwen 3.5 27B fallback | D-C2 | Wrong slug → 404 from OpenRouter, breaker opens, fallback chain unreachable. **Mitigation:** same curl probe; this is Operator UAT item. |
| A3 | llama.cpp `/tokenize` endpoint exists on the Phase 1 pod LLM server | RES-07 token counting | If missing, 16k cap enforcement fails → cannot guarantee SC-5. **Mitigation:** Phase 1 uses `ghcr.io/ggml-org/llama.cpp:server-cuda` which documents `/tokenize` in README master branch [CITED]. Verification is a single `curl {UPSTREAM_LLM_URL}/tokenize -d '{"content":"hello"}'` at Phase 3 bring-up. |
| A4 | Gateway tokenizes raw user content (not full Hermes chat template) for the 16k cap | RES-07 | If gateway counts only user content but llama.cpp enforces the cap against rendered template, under-counting lets oversized prompts through. **Mitigation:** Phase 3 plan should include a task to validate by sending a prompt at 16380 user tokens + a 500-token system + tools schema — expect llama.cpp to still respect `max_model_len=16384` and the gateway's pre-check to have been too lenient. Likely need to include full rendered template in tokenize call (via POST `/v1/chat/template` if available, OR compute template client-side and send to `/tokenize`). **This is a counting-contract lock decision for the planner.** |
| A5 | The `ai_gateway.upstreams` UNIQUE constraint on `(role, tier)` holds for tier-2+ additions in future phases | D-D2 | If Fase 5+ introduces tier-2 (OpenAI GPT-4o-mini for chat), need to ensure UNIQUE allows it — current schema DOES (it's `UNIQUE(role, tier)`, permitting (llm,0), (llm,1), (llm,2)). Verified. |
| A6 | pgxlisten v0.0.0-20250802141604 is production-stable despite pseudo-version | Standard Stack | If library has critical bugs, fallback to hand-rolled `pgx.Conn.WaitForNotification + reconnect`. **Mitigation:** maintainer is jackc (pgx author); README claims production use; Phase 2 integration tests cover reload flow. |
| A7 | OpenAI Whisper `/v1/audio/transcriptions` accepts the same multipart shape as the local Speaches server (file + model + language + prompt + response_format) | RES-03 STT fallback | If fields differ, gateway Director needs shape translation for openai-whisper route. **Mitigation:** both are OpenAI-compat; Speaches documentation claims 1:1. Integration test validates. |
| A8 | OpenAI embeddings `text-embedding-3-small` accepts the same request shape as BGE-M3 on Infinity (`{"input": [string], "model": "..."}`) | RES-03 embed fallback | If response dimension differs (1536 vs 1024), client apps may crash. **Mitigation:** RES-07 already documents BGE-M3 is 1024-dim; OpenAI text-embedding-3-small is 1536-dim by default but supports `dimensions` parameter. Gateway must pin `dimensions=1024` in fallback Director for shape parity. **Flag: add task "Pin embed fallback dimensions=1024 in Director" to planner.** |

## Open Questions

1. **Tokenize counting: raw content vs full chat template?**
   - What we know: llama.cpp `/tokenize` takes `{"content": string}` + returns `{"tokens": [int]}`.
   - What's unclear: whether the gateway should tokenize just `user` content (simpler, lower RTT) or fully-rendered chat messages + tool schema (accurate but needs template access).
   - Recommendation: start with **full rendered prompt** — gateway serializes all messages + tool schema to a string identical to what llama.cpp Hermes template would produce, sends to `/tokenize`. Accept ~20ms RTT + 60s Redis cache. Defer char→token fast-path per CONTEXT.md.

2. **Fireworks slug verification — when and how?**
   - What we know: OpenRouter docs confirm the `provider.order: ["provider_slug"]` syntax. "fireworks" is the conventional marketing name.
   - What's unclear: the literal slug used by OpenRouter's router (could be `fireworks`, `fireworks-ai`, `fireworks/turbo`, etc.).
   - Recommendation: Phase 3 bring-up task — operator runs one curl against `openrouter.ai/api/v1/chat/completions` with `provider.order:["fireworks"]` and inspects returned `provider` field in response. If wrong, env var fix is ≤60 seconds.

3. **Should `openai-whisper` fallback use GPT-4o-transcribe or `whisper-1`?**
   - What we know: OpenAI `/v1/audio/transcriptions` accepts both model slugs [CITED]. `whisper-1` is the legacy-but-stable choice; `gpt-4o-transcribe` is newer with different pricing.
   - What's unclear: which better matches Speaches's word-error-rate on Brazilian Portuguese for Cobranças/Telefonia use case.
   - Recommendation: Fase 3 ships `whisper-1` (matches Phase 1 Speaches baseline). Swap via env var later in Fase 9 integration if drift observed.

4. **Probe /tokenize endpoint availability in smoke-test?**
   - What we know: Phase 1 smoke.yml validates `/v1/chat/completions`, `/v1/audio/transcriptions`, `/v1/embeddings`.
   - What's unclear: whether `/tokenize` is smoke-tested; llama.cpp server master has it but the pinned image tag might not.
   - Recommendation: Fase 3 plan includes a task "Verify `/tokenize` on pinned pod image; bump pod image if missing".

## Environment Availability

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| Go module proxy (`proxy.golang.org`) | Adding gobreaker/v2, backoff/v5, pgxlisten | ✓ | — | Use vendored deps or GOPROXY=direct |
| `sony/gobreaker/v2` on proxy.golang.org | RES-01 | ✓ | v2.4.0 (2026-01-01) | `mercari/go-circuitbreaker` or `slok/goresilience` — both viable |
| `cenkalti/backoff/v5` on proxy.golang.org | RES-02 | ✓ | v5.0.3 (2025-07-23) | `avast/retry-go/v4` (lacks RetryAfterError; must honor manually) |
| `jackc/pgxlisten` on proxy.golang.org | D-D4 | ✓ | v0.0.0-20250802141604 | Hand-roll with `pgx.Conn.WaitForNotification` + backoff — ~100 LOC |
| Postgres 16 with `CREATE FUNCTION ... LANGUAGE plpgsql` privilege for `ai_gateway_app` role | D-D4 trigger installation | Unknown — depends on DO managed cluster grants | — | Create trigger as `ai_gateway_admin` role (distinct from runtime role); grant EXECUTE back to `ai_gateway_app` for DML |
| Redis Pub/Sub support | D-D1 breaker mirror | ✓ (infra-redis, container on traefik-public network per /home/pedro/projetos/pedro/CLAUDE.md) | Redis 7 | If Pub/Sub blocked for any reason, fall back to Redis Streams (consumer group `gw:breaker`) |
| `/tokenize` endpoint on pinned pod image `ghcr.io/ifixtelecom/ifix-ai-pod:develop` | RES-07 | Unknown — depends on llama.cpp version in image | — | Ship fast-path char→token estimate; defer tokenize to later phase (not recommended) |
| OpenRouter API reachable from VPS | D-C1 | Unknown — Traefik/firewall egress for `openrouter.ai:443` must be verified | — | Block: fallback chain non-functional |
| OpenAI API reachable from VPS | RES-03 STT/embed fallback | ✓ (per 03-CONTEXT.md Integration Points: "egress já permitido: ConverseAI v4 usa OpenAI hoje") | — | — |

**Missing dependencies with no fallback:**
- Postgres `CREATE FUNCTION` privilege — if `ai_gateway_app` lacks it, migrations fail at 0009. Needs coordination with DO cluster admin OR distinct migrations role.
- `/tokenize` on pod — SC-5 depends on it. Must verify Phase 1 image includes llama.cpp version supporting the endpoint.

**Missing dependencies with fallback:**
- `jackc/pgxlisten` — hand-roll if package has issues (well-understood pattern)
- Redis Pub/Sub — Streams alternative viable

## Validation Architecture

### Test Framework

| Property | Value |
|----------|-------|
| Framework | Go stdlib `testing` + `testcontainers-go` (already wired in Phase 2) |
| Config file | `gateway/internal/integration_test/setup_test.go` (shared TestMain) |
| Quick run command | `go test -race ./gateway/internal/breaker/... ./gateway/internal/upstreams/... ./gateway/internal/proxy/... ./gateway/internal/fallback/... -count=1` |
| Full suite command | `go test -race ./gateway/... -count=1` |

### Phase Requirements → Test Map

| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| RES-01 | Breaker trips after 3 consecutive failures; reopens after 30s cooldown; 1 success in HALF_OPEN closes | unit | `go test -race ./gateway/internal/breaker/... -run TestCircuitBreakerStateMachine` | ❌ Wave 0 |
| RES-02 | Retry 3× with 100ms/200ms/400ms backoff on 502/503/504; honors Retry-After; no retry on 4xx | unit | `go test -race ./gateway/internal/proxy/... -run TestRetryClassification` | ❌ Wave 0 |
| RES-03 | When tier-0 OPEN, router selects tier-1 upstream for same role | integration | `go test -race ./gateway/internal/integration_test/... -run TestFallbackRouting` | ❌ Wave 0 |
| RES-04 | Probe goroutine updates breaker state and `upstreams.last_probe_*` within 5s budget | integration | `go test -race ./gateway/internal/integration_test/... -run TestProbeLoop` | ❌ Wave 0 |
| RES-05 | Stream request with breaker OPEN returns 503 pre-flight; mid-stream disconnect closes cleanly without failover | integration | `go test -race ./gateway/internal/integration_test/... -run TestStreamingFailFast` | ❌ Wave 0 |
| RES-06 | Stream with `tool_calls` in delta + disconnect emits SSE error event; does NOT failover; 502 for non-stream | integration | `go test -race ./gateway/internal/integration_test/... -run TestToolCallPartial` | ❌ Wave 0 |
| RES-07 | 20k-token prompt rejected pre-dispatch with 400 envelope `context_length_exceeded` | integration | `go test -race ./gateway/internal/integration_test/... -run TestContextWindowEnforcement` | ❌ Wave 0 |
| RES-08 | Sensitive tenant + breaker OPEN → 3× in-memory retry; exhausted → 503 `upstream_unavailable_for_sensitive_tenant` + `Retry-After: 30`; streaming bypasses retry | integration | `go test -race ./gateway/internal/integration_test/... -run TestSensitiveRetryLoop` | ❌ Wave 0 |
| SC-1 | Kill local-llm → within ≤10s chat requests succeed via openrouter-chat | e2e (manual, opt-in with `GATEWAY_E2E_UPSTREAM_URL`) | `go test -tags=e2e -run TestFailoverTiming ./gateway/internal/integration_test/...` | ❌ Wave 0 |
| SC-2 | `GET /v1/health/upstreams` shows 6 upstreams with state+tier+role+last_probe_ms | integration | `go test -race ./gateway/internal/integration_test/... -run TestHealthUpstreamsEndpoint` | ❌ Wave 0 |
| SC-3 | sensitive request during primary OPEN → 503 + audit row `upstream='blocked_sensitive'` | integration | `go test -race ./gateway/internal/integration_test/... -run TestSensitiveAuditRow` | ❌ Wave 0 |
| SC-4 | tool-call mid-failover → gateway 502, never retries tool | integration | subset of TestToolCallPartial | ❌ Wave 0 |
| SC-5 | 20k prompt rejected same way on primary and fallback | integration | subset of TestContextWindowEnforcement | ❌ Wave 0 |
| D-D4 | Admin UPDATE upstreams row → gateway reloads in <1s (trigger + LISTEN) | integration | `go test -race ./gateway/internal/integration_test/... -run TestHotReload` | ❌ Wave 0 |
| D-D1 | Breaker state change on one replica → Pub/Sub → other replica's local breaker converges within 1s | integration | `go test -race ./gateway/internal/integration_test/... -run TestBreakerMirror` | ❌ Wave 0 |
| Per-route WriteTimeout | chat=0 for SSE, embeddings=30s, audio=120s | unit | `go test -race ./gateway/internal/config/... -run TestPerRouteWriteTimeout` | ❌ Wave 0 |

### Sampling Rate
- **Per task commit:** `go test -race ./gateway/internal/breaker/... ./gateway/internal/upstreams/... ./gateway/internal/proxy/... ./gateway/internal/fallback/... -count=1` — ~5s warm
- **Per wave merge:** `go test -race ./gateway/... -count=1` — ~30s warm (adds integration_test)
- **Phase gate:** Full suite green + manual SC-1 timing drill on dev VPS before `/gsd-verify-work`

### Wave 0 Gaps
- [ ] `gateway/internal/breaker/breaker_test.go` — covers RES-01 (state machine)
- [ ] `gateway/internal/breaker/mirror_test.go` — covers D-D1 (Redis Pub/Sub fan-out)
- [ ] `gateway/internal/upstreams/loader_test.go` — covers D-D2 (sqlc + atomic swap)
- [ ] `gateway/internal/upstreams/listen_test.go` — covers D-D4 (pgxlisten loop)
- [ ] `gateway/internal/upstreams/probe_test.go` — covers RES-04 (errgroup probe)
- [ ] `gateway/internal/upstreams/health_test.go` — covers SC-2 (status derivation)
- [ ] `gateway/internal/proxy/interceptor_test.go` — covers RES-06 (tool-call tee reader)
- [ ] `gateway/internal/proxy/tokencount_test.go` — covers RES-07 (tokenize + Redis cache)
- [ ] `gateway/internal/proxy/openrouter_test.go` — covers D-C2 (body rewrap with provider.order)
- [ ] `gateway/internal/fallback/sensitive_test.go` — covers RES-08 / D-B1 (3× retry loop)
- [ ] `gateway/internal/integration_test/breaker_state_test.go` — integration state machine
- [ ] `gateway/internal/integration_test/fallback_routing_test.go` — tier-0 OPEN → tier-1
- [ ] `gateway/internal/integration_test/sensitive_retry_test.go` — exhaustion → 503 + audit
- [ ] `gateway/internal/integration_test/hot_reload_test.go` — UPDATE → NOTIFY → <1s reload
- [ ] `gateway/internal/integration_test/tool_call_partial_test.go` — disconnect after tool_calls delta
- [ ] `gateway/internal/integration_test/streaming_failfast_test.go` — pre-flight 503 + no mid-stream retry
- [ ] `gateway/internal/integration_test/context_window_test.go` — 20k prompt → 400
- [ ] `gateway/internal/integration_test/health_upstreams_test.go` — endpoint status derivation
- [ ] `gateway/internal/proxy/integration_test/tool_call_drift_test.go` — opt-in E2E (D-C3)
- [ ] Mock HTTP upstream helper in `gateway/internal/integration_test/mock_upstream.go` — simulates 500/timeout/OK/delta-tool_calls

*(None of these exist today — all 20 are Wave 0 scaffolding, then per-requirement impl in subsequent waves.)*

## Security Domain

### Applicable ASVS Categories

| ASVS Category | Applies | Standard Control |
|---------------|---------|-----------------|
| V2 Authentication | yes | Phase 2 API key auth (argon2id in Postgres, Redis 60s cache) is reused; Phase 3 adds no new client-facing auth surface |
| V3 Session Management | no | Stateless per-request auth; no sessions |
| V4 Access Control | yes | `data_class='sensitive'` policy — D-B1..D-B4 enforce LGPD-aware routing. Never cross to external on sensitive. Test: sensitive tenant + primary OPEN → 503, no external HTTP call. |
| V5 Input Validation | yes | Phase 2 chi + json.Decode is reused. Phase 3 adds body rewrap (Pattern 6) for openrouter-chat — must preserve original client JSON validity; additionally validate `provider.order` CSV env var at config load to prevent injection |
| V6 Cryptography | no | No new crypto — Phase 2 argon2id + Better Auth–style secret handling is reused; upstream auth is Bearer-token pass-through |
| V7 Error Handling | yes | OpenAI envelope for all 502/503/400 responses; never leak upstream error message verbatim in the `message` field (can contain stack traces from OpenAI/OpenRouter). Wrap with canned text per CONTEXT.md envelopes. |
| V8 Data Protection | yes | No prompt/response logged for sensitive even on failover blocking (D-B3: audit_log row YES, audit_log_content row NO). Inherited from Phase 2 D-B2 pattern. |
| V9 Communication | yes | Gateway → OpenRouter and → OpenAI use HTTPS (per env var URLs). Bearer token in header (standard). Egress firewall rule for `openrouter.ai:443` needs operator verification. |
| V13 Configuration | yes | Secrets in env vars (NOT in DB); DB column `auth_bearer_env` stores only the env var NAME. Rotation via Portainer stack env edit + restart. |

### Known Threat Patterns for Go Reverse-Proxy Gateway + LLM Upstream

| Pattern | STRIDE | Standard Mitigation |
|---------|--------|---------------------|
| Upstream bearer token leak via logs | Information Disclosure | Phase 2 slog redactor + Sentry `denyList` covers `Authorization`. Reused. |
| Client-supplied Authorization passed to upstream | Elevation of Privilege | Phase 2 `proxy.clientAuthHeaders` strips `Authorization`, `X-API-Key`, `Cookie`, `Proxy-Authorization` in Director before Phase 3 injects upstream bearer. |
| SSRF via model alias pointing at internal IP | Tampering / Information Disclosure | Phase 2 resolves model aliases from `model_aliases` table (operator-controlled, not client-supplied). Phase 3 inherits. Additionally: `upstreams.url_env` reads from env vars (not client-controllable). |
| Tool-call replay during failover (SC-4) | Tampering | D-C3 tool-call detection returns 502 instead of retrying. Agent layer (ConverseAI) must use separate idempotency key. |
| PII leak to OpenAI/OpenRouter (LGPD) | Information Disclosure | D-B1..D-B4: sensitive tenants NEVER fail over to external. 503 with discriminable error code. Audit row `blocked_sensitive`. |
| DoS via oversized prompt | DoS | Phase 2 `MaxBodyBytes: 25 MiB` already enforced. Phase 3 adds token-count rejection at 16k. |
| Slow-client DoS on non-stream routes | DoS | FOLDED TODO — per-route WriteTimeout (chat=0 SSE, embed=30s, audio=120s) |
| Breaker state poisoning via Redis | Tampering | Breaker is in-process authoritative; Redis is mirror. Malicious Redis writer can desync replica but not cause local false-failover. Redis access is VPN-internal (traefik-public container network). |
| Postgres NOTIFY payload injection | Tampering | Trigger emits `pg_notify('upstreams_changed', id::text)` — payload is a UUID string only. Gateway does not exec based on payload content, just triggers SELECT. |

## Sources

### Primary (HIGH confidence)
- Context7 `/sony/gobreaker` — gobreaker v2 API (NewCircuitBreaker[T], Settings, Counts, Execute, OnStateChange) [CITED]
- pkg.go.dev/github.com/cenkalti/backoff/v5 — backoff v5 API (Retry[T], WithBackOff, RetryAfterError, Permanent, ExponentialBackOff fields) [CITED]
- Context7 `/websites/pkg_go_dev_github_com_jackc_pgx_v5` — pgx LISTEN/NOTIFY (WaitForNotification) [CITED]
- pkg.go.dev/github.com/jackc/pgxlisten — pgxlisten API (Listener.Connect, ReconnectDelay, Handle) [CITED]
- Context7 `/redis/go-redis` — go-redis v9 Subscribe/ReceiveMessage, PublishBreakerEvent, Channel options [CITED]
- pkg.go.dev/net/http/httputil — ReverseProxy struct (Rewrite vs Director, ModifyResponse ownership, ErrorHandler state, FlushInterval=-1) [CITED]
- pkg.go.dev/golang.org/x/sync/errgroup — errgroup.Group vs WithContext, SetLimit, zero-value behavior [CITED]
- openrouter.ai/docs/guides/routing/provider-selection — provider field shape, order, allow_fallbacks, pin syntax [CITED]
- github.com/ggml-org/llama.cpp/blob/master/tools/server/README.md — llama-server `/tokenize` endpoint contract [CITED]
- bge-model.com/bge/bge_m3.html — BGE-M3 max sequence length 8192 [CITED]
- pressly.github.io/goose/documentation/annotations — StatementBegin/StatementEnd for PL/pgSQL [CITED]
- proxy.golang.org `@latest` endpoints for all libs — version verification [VERIFIED]
- /home/pedro/projetos/pedro/gpu-ifix/.planning/phases/03-resilience-fallback-chain/03-CONTEXT.md — all decisions D-A1..D-D4 + deferred [VERIFIED local]
- /home/pedro/projetos/pedro/gpu-ifix/gateway/internal/upstreams/health.go + proxy/director.go — existing Phase 2 code [VERIFIED local]

### Secondary (MEDIUM confidence)
- openrouter.ai/qwen/qwen3.5-27b — model exists, 262k ctx, Fireworks availability inferred but not confirmed in fetched content [CITED with caveat]
- fireworks.ai/blog/qwen-3 — Fireworks serves Qwen3-235B-A22B with tool calls; 27B variant not explicitly named in 2025 blog post [CITED]
- OpenAI docs (search-only, /docs/ pages 403 on WebFetch) — `/v1/audio/transcriptions` multipart shape with file/model/language/response_format; `/v1/embeddings` with `dimensions` param [CITED via search results]

### Tertiary (LOW confidence — flagged in Assumptions Log)
- Exact Fireworks provider slug on OpenRouter (A1) — inferred from general OpenRouter docs; not directly confirmed per model
- Exact gateway tokenize contract (raw vs templated) (A4) — no authoritative source; must be locked by planner via test
- `/tokenize` availability on the pinned pod image tag (A3) — depends on llama.cpp version baked into `ghcr.io/ifixtelecom/ifix-ai-pod:develop`

## Metadata

**Confidence breakdown:**
- Standard stack (gobreaker, backoff, pgxlisten, go-redis, goose, sqlc): HIGH — all Go libraries with recent releases, same-maintainer pedigree, Context7/pkg.go.dev direct citations
- Architecture patterns (breaker state machine, probe loop, hot-reload, tool-call detection): HIGH — all map to documented APIs; Pattern 4 (tee reader) is a Go idiom for SSE middleware
- OpenRouter provider routing: HIGH on syntax, MEDIUM on Fireworks slug specifics — requires live probe
- llama.cpp /tokenize: HIGH on endpoint shape, MEDIUM on availability-in-pinned-image
- OpenAI Whisper/embeddings contracts: HIGH on multipart shape, 1536-dim default, `dimensions` parameter
- BGE-M3 context cap (8192): HIGH — explicitly documented by BAAI
- LGPD / sensitive tenant policy: HIGH — Phase 2 patterns reused verbatim
- Goose + PL/pgSQL + LISTEN/NOTIFY: HIGH — multiple primary sources confirm StatementBegin/End + conditional trigger pattern

**Research date:** 2026-04-19
**Valid until:** 2026-05-19 (30 days — stable stack; OpenRouter model slugs change monthly but env-var-config makes it zero-code swap)
