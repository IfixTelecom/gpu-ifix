---
phase: 3
slug: resilience-fallback-chain
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2026-04-19
---

# Phase 3 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.
> Surfaces enumerated from RESEARCH.md §Validation Architecture.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | go test (stdlib) + testcontainers-go (Phase 2 harness) |
| **Config file** | `gateway/Makefile` (test targets), `gateway/internal/integration_test/main_test.go` (TestMain bootstrap) |
| **Quick run command** | `cd gateway && go test ./internal/breaker/... ./internal/upstreams/... ./internal/proxy/...` |
| **Full suite command** | `cd gateway && go test -tags=integration ./...` |
| **Estimated runtime** | ~60s quick, ~180s full (testcontainers boot Postgres+Redis once) |

---

## Sampling Rate

- **After every task commit:** Run quick command for the package(s) touched
- **After every plan wave:** Run full suite (includes integration_test/)
- **Before `/gsd-verify-work`:** Full suite must be green + manual smoke per §Manual-Only Verifications
- **Max feedback latency:** ~60s for unit, ~180s for integration

---

## Per-Task Verification Map

> Filled out by planner during PLAN.md generation. Every NEW system surface
> from RESEARCH.md §Validation Architecture must appear here with at least
> one automated test.

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| {N}-01-01 | 01 | 1 | RES-{XX} | — | {expected secure behavior or "N/A"} | unit | `{command}` | ✅ / ❌ W0 | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

> Test scaffolding the planner MUST include in Wave 0 plans. Pulled from
> RESEARCH.md §Wave 0 Gaps (20-item inventory).

- [ ] `gateway/internal/breaker/breaker_test.go` — state machine transitions (CLOSED→OPEN→HALF_OPEN→CLOSED), `OnStateChange` callback, `Counts.ConsecutiveFailures` threshold
- [ ] `gateway/internal/breaker/mirror_test.go` — Redis HSET/HGETALL roundtrip, Pub/Sub publish + subscribe with mock Redis
- [ ] `gateway/internal/upstreams/loader_test.go` — DB load → in-memory map → atomic swap; tier/role uniqueness assertions
- [ ] `gateway/internal/upstreams/listen_test.go` — Postgres NOTIFY → reload (testcontainers Postgres + dedicated `pgx.Conn`)
- [ ] `gateway/internal/upstreams/probe_test.go` — synthetic E2E probe per role; errgroup independence (one fail must NOT cancel siblings); 5s shared timeout
- [ ] `gateway/internal/upstreams/health_test.go` — `/v1/health/upstreams` payload shape; `status` derivation (ok/degraded/failed); 2s in-memory cache
- [ ] `gateway/internal/proxy/dispatcher_test.go` — tier-0 CLOSED → primary; tier-0 OPEN → tier-1; tier-1 OPEN → 503 envelope; sensitive tenant never goes external
- [ ] `gateway/internal/proxy/sensitive_test.go` — 3-attempt retry loop (200ms → 800ms → 3s); breaker state re-check between attempts; envelope `upstream_unavailable_for_sensitive_tenant`
- [ ] `gateway/internal/proxy/streaming_test.go` — pre-flight 503 when breaker OPEN; mid-stream `io.ErrUnexpectedEOF` does NOT failover (post-first-byte invariant)
- [ ] `gateway/internal/proxy/toolcall_test.go` — `ModifyResponse` interceptor scans first 8KB for `"tool_calls"`; 502 envelope when stream interrupted with flag set; metric `gateway_tool_call_partial_total` increments
- [ ] `gateway/internal/proxy/tokencount_test.go` — `/tokenize` call + Redis cache hit; over-cap test at 16380/16385 boundary; embed cap 8192; envelope `context_length_exceeded`
- [ ] `gateway/internal/proxy/openrouter_director_test.go` — body rewrap injects `provider.order:["fireworks"]` + `allow_fallbacks:false`; `Authorization: Bearer` header injected from `auth_bearer_env`; client `Authorization` header stripped
- [ ] `gateway/internal/proxy/openai_embed_director_test.go` — body rewrap pins `dimensions: 1024` (BGE-M3 parity); model swap to `text-embedding-3-small`
- [ ] `gateway/internal/proxy/openai_whisper_director_test.go` — multipart preserved; model swap to `whisper-1`; auth header injected
- [ ] `gateway/internal/proxy/writetimeout_test.go` — chat=0 (no WriteTimeout for SSE); embed=30s; audio=120s
- [ ] `gateway/internal/audit/upstream_enum_test.go` — `blocked_sensitive` enum value accepted; row written without content (`audit_log_content` skipped)
- [ ] `gateway/internal/integration_test/breaker_state_machine_test.go` — full breaker lifecycle with mock upstream HTTP server (testcontainers + httptest)
- [ ] `gateway/internal/integration_test/fallback_routing_test.go` — kill primary mock → ≤10s observed failover (SC-1); per role/tier dispatch verified
- [ ] `gateway/internal/integration_test/sensitive_block_test.go` — sensitive tenant request during primary OPEN → 3 retries → 503 envelope + audit row with `upstream='blocked_sensitive'`
- [ ] `gateway/internal/integration_test/hot_reload_test.go` — UPDATE upstreams row → NOTIFY → in-memory map updated < 1s

*Source: RESEARCH.md §Validation Architecture + §Wave 0 Gaps*

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| OpenRouter Fireworks slug works for `qwen/qwen3.5-27b` (or current Qwen 3.x 27B model id) | RES-03 | Provider list page is JS-rendered; canonical slug only verifiable against live OpenRouter API with real key | `curl -X POST https://openrouter.ai/api/v1/chat/completions -H "Authorization: Bearer $OPENROUTER_API_KEY" -H "Content-Type: application/json" -d '{"model":"qwen/qwen3.5-27b","provider":{"order":["fireworks"],"allow_fallbacks":false},"messages":[{"role":"user","content":"ping"}],"max_tokens":1}'` — expect 200 with `provider:"fireworks"` in response metadata. If 404 or different provider, update env + test against alternative slugs (`qwen/qwen-3.5-27b`, `qwen/qwen3-27b-instruct`). |
| llama.cpp `/tokenize` endpoint available on `ghcr.io/ifixtelecom/ifix-ai-pod:develop` | RES-07, SC-5 | Depends on llama.cpp version baked into pod image; image build is operator-controlled | On VPS dev: `curl -X POST http://<pod-ip>:8000/tokenize -H "Content-Type: application/json" -d '{"content":"ping"}'` — expect 200 with `{"tokens":[int]}`. If 404, escalate to pod image rebuild before merging Phase 3. |
| ≤10s observed failover end-to-end against real local pod (SC-1) | RES-01, RES-04 | Probe loop + breaker timing requires real LLM process; integration test only mocks the HTTP layer | On VPS dev: send chat request, `kill -TERM` llama.cpp pod, observe next request in ≤10s succeeds via OpenRouter (per `request_id` in `audit_log.upstream='openrouter-chat'`). Document timestamp delta in PR. |
| Cross-replica breaker convergence < 1s (Phase 6 prereq) | RES-04 | Requires 2 gateway replicas; Fase 3 dev runs single replica | Defer to Phase 6 entrance criteria — Phase 3 only validates single-process semantics. |
| Sentry breadcrumbs appear on breaker `OnStateChange` transitions | RES-04 | Requires Sentry org access for the dev project | After triggering a breaker open in dev, check Sentry breadcrumbs for `module=BREAKER from=closed to=open upstream=local-llm`. |

---

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies (planner fills `<acceptance_criteria>` per task)
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references (20-item inventory above)
- [ ] No watch-mode flags (`-watch`, `-loop`) in test commands — go test is one-shot
- [ ] Feedback latency < 60s for unit, < 180s for integration
- [ ] `nyquist_compliant: true` set in frontmatter (after planner fills the verification map)

**Approval:** pending
