---
plan: 05
phase: 2
title: Audit writer + SSE tee (via ProxyResponseInterceptor) + model alias resolver + /v1/health/upstreams
status: complete
completed_at: "2026-04-18"
requirements_addressed: [GW-05, GW-07, GW-08, TEN-02]
wave: 4
depends_on: [02-02-PLAN.md, 02-03-PLAN.md, 02-04-PLAN.md]
tech_stack:
  added:
    - "go.uber.org/goleak@v1.3.0 (test-only — SSE goroutine leak detection per Codex review [HIGH/MEDIUM] 02-05)"
  patterns:
    - "async buffered writer (channel 1000 + 500-row/1s flusher)"
    - "io.ReadCloser tee (no goroutines, idempotent Close)"
    - "ProxyResponseInterceptor plug-in (decoupled from httputil.ReverseProxy internals)"
    - "composite (alias, upstream) key for alias resolution"
    - "5s in-memory cache for upstream health fan-out"
key_files:
  created:
    - "gateway/internal/audit/writer.go (267 LOC)"
    - "gateway/internal/audit/tee.go (88 LOC)"
    - "gateway/internal/audit/interceptor.go (62 LOC)"
    - "gateway/internal/audit/middleware.go (266 LOC)"
    - "gateway/internal/audit/writer_test.go (209 LOC)"
    - "gateway/internal/audit/tee_test.go (119 LOC)"
    - "gateway/internal/audit/interceptor_test.go (97 LOC)"
    - "gateway/internal/audit/middleware_test.go (211 LOC)"
    - "gateway/internal/models/resolver.go (102 LOC)"
    - "gateway/internal/models/rewrite.go (78 LOC)"
    - "gateway/internal/models/resolver_test.go (86 LOC)"
    - "gateway/internal/models/rewrite_test.go (128 LOC)"
    - "gateway/internal/upstreams/health.go (97 LOC)"
    - "gateway/internal/upstreams/health_test.go (146 LOC)"
  modified:
    - "gateway/cmd/gateway/main.go (audit writer Run goroutine, AuditInterceptor wiring, resolver boot-refresh + Start, audit.Middleware on /v1/*, models.Handler on chat+embed, upstreams.NewHealthHandler replacing scaffold 501)"
    - "go.mod / go.sum (goleak added)"
decisions:
  - "SSE capture via ProxyResponseInterceptor (formal extension point from 02-04), NOT direct ReverseProxy.ModifyResponse mutation — Codex review [HIGH/MEDIUM] 02-05"
  - "Whisper duration parsing uses json.Unmarshal into whisperPartial struct (Codex review [HIGH] 02-05 — replaces fragile fmt.Sscan)"
  - "Model resolver uses (alias, upstream) composite key — Codex review [MEDIUM] 02-05 — future-proofs cross-upstream alias collision"
  - "TeeBody is synchronous — no goroutines — so client-disconnect paths cannot leak. Regression guarded by TestTee_NoGoroutineLeakOnClientDisconnect with goleak.VerifyNone."
  - "IdempotencyReplayed flag propagates from Plan 02-06 middleware to audit via IdempotencyReplayedSetter interface on the ResponseWriter wrapper (not ctx.WithValue, which would not propagate back)"
  - "Audio route skips request-body capture; only multipart metadata (filename/mime/size/language/duration) persisted per D-B6 (raw audio is PII)"
metrics:
  duration_s: 820
  tasks_completed: 2
  files_created: 14
  files_modified: 2
  tests_added: 28
---

# Phase 2 Plan 05: Audit writer + SSE tee + model alias resolver + /v1/health/upstreams — Summary

One-liner: **Async audit-log subsystem + SSE tee (decoupled from ReverseProxy via formal ProxyResponseInterceptor), model-alias rewriter, and cached upstream health aggregator — delivering Phase 2 success criteria #3 and #5.**

## What shipped

- **Audit writer** (`gateway/internal/audit/writer.go`): channel-buffered (1000) async flusher with 500-row/1s triggers (D-B4); `Enqueue` is non-blocking with drop metric fallback; ctx-cancel drains remaining events before return; production `dbFlusher` does `pgx.CopyFrom(audit_log) + row-by-row InsertAuditLogContent(audit_log_content)` in one transaction and skips content for `data_class != "normal"` (LGPD-at-schema-default, D-B2).
- **TeeBody** (`gateway/internal/audit/tee.go`): io.ReadCloser wrapper that buffers up to 128 KB (D-B5) and marks `Truncated=true` beyond the cap while continuing to pass through Read. Zero goroutines spawned; `Close` is idempotent and fires `onClose` exactly once.
- **AuditInterceptor** (`gateway/internal/audit/interceptor.go`): implements `proxy.ProxyResponseInterceptor` — wraps SSE response bodies in TeeBody via the formal extension contract from 02-04, **without** mutating `ReverseProxy.ModifyResponse` (Codex review [HIGH/MEDIUM] 02-05 decoupling). For non-SSE responses, Intercept is a no-op — the middleware ResponseWriter wrapper handles body capture directly.
- **audit.Middleware** (`gateway/internal/audit/middleware.go`): chi middleware that captures request body (up to 128 KB, only for `data_class=normal`, skipped on audio route), wraps ResponseWriter for status + body, enqueues Event on return. Exports `IdempotencyReplayedSetter` interface so Plan 02-06 can mark replays. Whisper metadata extraction uses `json.Unmarshal` into a typed struct (Codex review [HIGH] 02-05).
- **Resolver** (`gateway/internal/models/resolver.go`): in-memory map keyed by `(alias, upstream)` composite; `Refresh(ctx)` reads `model_aliases` table; `Start(ctx)` runs a 60s ticker that swaps the map under `sync.RWMutex`. `Resolve` on unknown alias returns the alias itself (pod decides — forward-compat).
- **RewriteJSONModel** (`gateway/internal/models/rewrite.go`): parses JSON into `map[string]json.RawMessage` so every field except `model` passes through byte-for-byte; `Handler` wraps an inner handler to rewrite the request body before it reaches the proxy.
- **Upstreams health handler** (`gateway/internal/upstreams/health.go`): fans out to `{UPSTREAM_HEALTH_BRIDGE_URL}/health` with a 2s probe budget; caches the upstream body verbatim for 5s; returns 503 + `{status:"failed", services:{}}` on unreachable upstream.
- **main.go wiring**: spawns `auditWriter.Run` goroutine; creates `AuditInterceptor` and passes it to `NewChatProxy` via the variadic slot; calls `resolver.Refresh(ctx)` fail-fast at boot then `resolver.Start(ctx)`; mounts `audit.Middleware` on the authed `/v1/*` group; wraps chat + embeddings in `models.Handler`; replaces scaffold 501 for `/v1/health/upstreams` with the real aggregator.

## Tasks

| Task | Description | TDD |
|------|-------------|-----|
| 1 | audit writer + tee + interceptor + middleware | RED `d8103dc` → GREEN `05804e9` |
| 2 | resolver + rewrite + upstreams health + main.go wiring | RED `502e3d7` → GREEN `03b7d94` |

## Commits

- `d8103dc` test(02-05): add failing tests for audit writer/tee/interceptor/middleware
- `05804e9` feat(02-05): implement audit writer/tee/interceptor/middleware
- `502e3d7` test(02-05): add failing tests for models resolver/rewrite + upstreams health
- `03b7d94` feat(02-05): implement resolver + rewrite + upstreams health + wire main.go

## Quality gates

- `gofmt -l gateway/` → empty
- `go vet ./gateway/...` → clean
- `go build ./gateway/...` → clean
- `go test ./gateway/... -count=1 -race` → all packages green
  (audit 4.6s, models 1.0s, upstreams 6.5s, cmd/gateway 0.03s, everything else unchanged)

## Audit Enqueue non-blocking benchmark

`TestWriter_EnqueueNeverBlocks` drives 100 enqueues against a buffer-of-5 writer with no flusher running. All 100 calls complete in under 10 ms (measured; assertion `<100ms`); 95 events drop (buffer=5 retains first 5). Hot path never awaits DB.

## 128 KB cap + truncated flag behavior

`TestTee_PassesThroughFullBodyToReader` pushes 200 KB through the tee; reader sees all 200 KB bytes; `Captured()` returns exactly 128 KB plus `truncated=true`. `TestMiddleware_CapturesAtMost128KB` makes the same assertion end-to-end through the middleware path.

## Model alias refresh interval

`refreshInterval = 60 * time.Second`. Boot does a fail-fast `Refresh(ctx)` so operators catch schema/seed problems immediately; `Start(ctx)` launches a ticker goroutine that exits on ctx cancel. Lookups use `sync.RWMutex` read lock so concurrent `Resolve` calls never block each other.

## Health handler cache TTL + probe budget

- `cacheTTL = 5 * time.Second` (confirmed by `TestHealthHandler_Cache5Seconds`: 3 rapid requests hit upstream exactly 1×)
- `probeBudget = 2 * time.Second` + 500ms client overhead
- Failure envelope: 503 + `{status:"failed",services:{}}` (confirmed by `TestHealthHandler_UpstreamUnreachable`)

## Deviations from plan

None at the code/contract level. All Codex [HIGH/MEDIUM]/[HIGH]/[MEDIUM] 02-05 revisions honored:
- [HIGH/MEDIUM] audit↔ReverseProxy coupling: AuditInterceptor implements `proxy.ProxyResponseInterceptor`; `grep "rp.ModifyResponse =" gateway/internal/audit/` returns empty.
- [HIGH/MEDIUM] goroutine leak risk on mid-SSE abort: `TestTee_NoGoroutineLeakOnClientDisconnect` uses `goleak.VerifyNone(t, goleak.IgnoreCurrent())` and passes. TeeBody spawns no goroutines.
- [HIGH] Whisper JSON parsing: `json.Unmarshal` into `whisperPartial` typed struct; `grep "fmt.Sscan" gateway/internal/audit/*.go` returns only a comment referencing the old approach, no code.
- [MEDIUM] composite (alias, upstream) key: `TestResolver_SameAliasDifferentUpstreams` asserts the two distinct targets resolve independently.

The plan's draft `models.Handler(resolver, upstream, inner)` signature matched Codex review [MEDIUM] 02-05 exactly.

### Minor implementation detail

- Writer test hook (`enqueueHook`): plan text suggested "test-only interface swap" — I implemented it as an optional function field on `*Writer` that tests can set to capture Events without running `Run`. This keeps production `Enqueue` one-path (`select {}` + drop on full) while enabling `middleware_test.go` to assert what the middleware produced. Identical observable behavior to the plan's "minimal interface" suggestion; simpler wiring.
- `auditResponseWriter.WriteHeader` is guarded against double calls and also detects SSE on first `Write` when no explicit `WriteHeader` runs — handles handlers that skip the explicit status and go straight to `Write`.

## Handoff to 02-06

- `audit.Writer` stable and running as a goroutine.
- `audit.IdempotencyReplayedSetter` interface exported — Plan 02-06's idempotency middleware type-asserts the `http.ResponseWriter` and calls `SetIdempotencyReplayed(true)` on the replay path. `audit.Middleware` reads the flag **after** `next.ServeHTTP` returns (D-T-02-05-06 mitigation in the threat register).
- `models.Resolver` stable; Plan 02-06 (idempotency) does NOT touch it — but future plans (tool calls, function bodies) may want to read `Resolve(alias, upstream)` themselves.
- `/v1/health/upstreams` is live; Plan 02-08 Portainer stack will smoke-test it during deploy verification.

## TDD Gate Compliance

- RED gate commits: `d8103dc` (task 1), `502e3d7` (task 2)
- GREEN gate commits: `05804e9` (task 1), `03b7d94` (task 2)
- REFACTOR: none needed — green passed on first implementation pass.

## Self-Check: PASSED

Files verified present:
- gateway/internal/audit/writer.go, tee.go, interceptor.go, middleware.go + *_test.go: FOUND
- gateway/internal/models/resolver.go, rewrite.go + *_test.go: FOUND
- gateway/internal/upstreams/health.go, health_test.go: FOUND
- gateway/cmd/gateway/main.go: modified (audit.NewWriter, auditWriter.Run, audit.Middleware, models.Handler, upstreams.NewHealthHandler, audit.NewAuditInterceptor all present)

Commits verified in `git log --oneline`:
- `d8103dc` FOUND
- `05804e9` FOUND
- `502e3d7` FOUND
- `03b7d94` FOUND

Decoupling gate: `grep "rp.ModifyResponse =" gateway/internal/audit/` → empty (audit does not mutate ReverseProxy internals).

Whisper gate: `grep "fmt.Sscan" gateway/internal/audit/middleware.go` → only a single comment line (no code).

Composite-key gate: `TestResolver_SameAliasDifferentUpstreams` passes.

Goleak gate: `TestTee_NoGoroutineLeakOnClientDisconnect` passes under `-race`.
