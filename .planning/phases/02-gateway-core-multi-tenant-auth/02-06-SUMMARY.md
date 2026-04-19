---
phase: 02
plan: 06
subsystem: gateway
tags:
  - idempotency
  - redis
  - middleware
  - ten-09
  - stripe-semantics
  - first-writer-wins
dependency_graph:
  requires:
    - 02-03 (redisx.NewClient, auth.AuthContext.TenantID, auth.Middleware)
    - 02-05 (audit.IdempotencyReplayedSetter interface, audit.Middleware)
    - 02-01 (httpx.WriteOpenAIError, httpx.RequestIDFrom)
  provides:
    - idempotency.Store (AcquireInFlight/Complete/Abort/Get/WaitForComplete)
    - idempotency.HashBody (canonical JSON sha256)
    - idempotency.Middleware (chi-compatible, chat-only)
    - idempotency.ErrConflict/ErrStreamingNotSupported/ErrUnsupportedRoute/ErrInFlightTimeout
  affects:
    - gateway/cmd/gateway/main.go (chat handler now wraps idempotency after auth+audit)
tech_stack:
  added: []
  patterns:
    - "SET NX EX first-writer-wins serialization at `gw:idem:<tenant>:<key>`"
    - "IN_FLIGHT sentinel format `IN_FLIGHT:<req_id>|hash=<sha256>` with 30s TTL"
    - "Completion overwrite replaces sentinel with JSON Entry via plain SET (24h TTL)"
    - "Loser wait-poll: 100ms interval up to 30s budget; immediate 422 on hash mismatch"
    - "Response-writer interface assertion (audit.IdempotencyReplayedSetter) — no ctx mutation"
key_files:
  created:
    - gateway/internal/idempotency/errors.go
    - gateway/internal/idempotency/hash.go
    - gateway/internal/idempotency/hash_test.go
    - gateway/internal/idempotency/store.go
    - gateway/internal/idempotency/store_test.go
    - gateway/internal/idempotency/middleware.go
    - gateway/internal/idempotency/middleware_test.go
    - gateway/internal/idempotency/export_test.go
  modified:
    - gateway/cmd/gateway/main.go
decisions:
  - "IN_FLIGHT sentinel lives at the SAME Redis key as the eventual Entry — no separate lock-key (Codex review [MEDIUM] 02-06 simplification)"
  - "waitPollBudget + waitPollInterval are package-level `var` (not `const`) so tests can shrink them via export_test.go shim"
  - "HeaderWhitelist deliberately OMITS X-Request-ID — replays carry the REPLAYING request's id, not the original's (T-02-06-08 mitigation)"
  - "5xx statuses trigger Store.Abort (DEL) instead of Store.Complete — don't cache transient upstream failures"
  - "Stream detection happens BEFORE Redis touch — `stream:true` + Idempotency-Key rejects with 400 immediately (D-C4)"
metrics:
  duration_seconds: 1100
  completed_date: "2026-04-18T23:55:00.000Z"
  tasks_completed: 2
  files_created: 8
  files_modified: 1
  tests_added: 32
---

# Phase 2 Plan 06: Idempotency-Key middleware Summary

Stripe-style idempotency-key contract on POST /v1/chat/completions (non-streaming), with first-writer-wins serialization via Redis `SET NX EX`, 30s in-flight wait budget for losers, 24h cached-entry TTL, and an audit-replay flag propagated via interface assertion.

## Package file sizes

| File                        | LOC | Purpose                                                      |
| --------------------------- | --- | ------------------------------------------------------------ |
| errors.go                   | 20  | 4 sentinel errors                                            |
| hash.go                     | 87  | Canonical-JSON sha256 body hash                              |
| store.go                    | 199 | Redis-backed Store (Get/AcquireInFlight/Complete/Abort/WaitForComplete) |
| middleware.go               | 288 | chi middleware (chat-only) + captureWriter                   |
| export_test.go              | 28  | Test-only shim: `SetWaitPollForTests`, `PlantInFlightForTests` |
| hash_test.go                | 103 | 8 hash tests                                                 |
| store_test.go               | 265 | 11 store tests (miniredis)                                   |
| middleware_test.go          | 623 | 13 middleware tests (miniredis + httptest)                   |
| **Total package**           | 1613| 19 files across prod + tests                                 |

## Test coverage (all `-race` clean)

**Unit tests — 32 total:**
- 8 hash tests: same-content-same-hash, key-reorder-invariant (nested), array-order-preserved, empty-body, invalid-json error, unicode
- 11 store tests: empty/inflight/completed kinds, SET NX EX first-wins, Complete overwrites sentinel, Abort clears, TTL 24h/30s FastForward, cross-tenant isolation, WaitForComplete success/conflict/timeout/aborted
- 13 middleware tests: no-header passthrough (zero Redis keys), cache hit/miss replay, 422 on different body, 400 on stream+key, 400 on unsupported route, cross-tenant isolation, 502 not cached, audit flag setter interface path, JSON key-order invariant, **concurrent serialization (10 goroutines → 1 upstream hit)**, concurrent hash-mismatch-422 (immediate, < 500ms), in-flight timeout → 409 + Retry-After: 5, 502-aborts-sentinel

## Microbench: replay vs fresh latency

Measured informally during TestMiddleware_CacheMissFollowedByHit_SameBody and TestMiddleware_ConcurrentSerialization runs on miniredis:

| Path    | Latency (approx, miniredis)             |
| ------- | --------------------------------------- |
| Fresh   | Bound by handler (test sleeps 200ms)    |
| Replay  | ~1-2ms per request (Redis GET + JSON decode + write) |
| Loser (wait-poll) | 100-200ms (one poll interval + handler finish) |

## Critical confirmations

**502 upstream failures DO NOT get cached** — verified by `TestMiddleware_Status502_NotCached` (retry hits upstream again, counter == 2) and `TestMiddleware_AbortOnUpstream502` (miniredis key absent after 502). `cacheable(status)` returns false for everything outside 2xx/400/422.

**`X-Request-ID` is NOT in HeaderWhitelist** — grepped `gateway/internal/idempotency/store.go`:

```go
var HeaderWhitelist = []string{
    "Content-Type",
    "Content-Length",
    "OpenAI-Organization",
    "OpenAI-Processing-Ms",
}
```

X-Request-ID is intentionally excluded so replays carry the replayer's UUIDv7 (from `httpx.RequestID` middleware), not the original winner's. Threat T-02-06-08 mitigated.

**First-writer-wins via real SET NX EX** — `TestMiddleware_ConcurrentSerialization` runs 10 goroutines against a handler that sleeps 200ms and asserts upstream hit counter = 1, AND all 10 goroutines return byte-identical response bodies. This is not a replay-only test; it enforces that exactly one request reaches the handler.

**Hash-mismatch on in-flight key returns 422 without waiting** — `TestMiddleware_ConcurrentHashMismatch422` holds the winner in a channel, sends a second request with a different body + same key, asserts 422 within 500ms (no 30s wait).

**In-flight timeout returns 409 + Retry-After: 5** — `TestMiddleware_InFlightTimeoutReturns409` plants a sentinel via `PlantInFlightForTests`, shrinks `waitPollBudget` to 300ms via `SetWaitPollForTests`, confirms the middleware returns 409 + Retry-After + `idempotency_key_in_progress` code after ~300ms.

## Wiring in cmd/gateway/main.go

Chat handler build order (router.go path):

```
auth.Middleware → audit.Middleware → idempotency.Middleware → models.Handler → chat reverse proxy
```

Idempotency is scoped to `/v1/chat/completions` only (D-C4). Embeddings and audio pass through unchanged. The `idemStore` is constructed once from the shared `rdb` Redis client (same connection pool as auth cache) and passed via the `proxies{}` struct.

## Deviations from Plan

### Auto-fixed issues

**1. [Rule 3 - Blocking] `t.Context()` not available on Go 1.23**

- **Found during:** Task 2 go vet after writing middleware_test.go
- **Issue:** `t.Context()` was added in Go 1.24; project uses `go 1.23.0` in go.mod. Initial test code drafted from Plan referenced this method.
- **Fix:** Replaced with `context.Background()` + added `"context"` import.
- **Files modified:** gateway/internal/idempotency/middleware_test.go
- **Commit:** 0d66b1c

**2. [Rule 1 - Bug] `r2` error not checked before use (go vet failure)**

- **Found during:** Task 2 go vet
- **Issue:** `TestMiddleware_JsonKeyOrderInvariant` discarded the HTTP client error before dereferencing `r2.Header`.
- **Fix:** Changed `r2, _ := http.DefaultClient.Do(req2)` → `r2, err := ...` with `t.Fatal(err)` check.
- **Files modified:** gateway/internal/idempotency/middleware_test.go
- **Commit:** 0d66b1c

**3. [Rule 3 - Blocking] waitPollBudget declared `const` prevented test-only override**

- **Found during:** Task 2 planning of `TestMiddleware_InFlightTimeoutReturns409`
- **Issue:** Plan specified `const waitPollBudget` but the test needs to shrink the budget to ~300ms so CI doesn't wait 30s.
- **Fix:** Changed `const` → `var` for `waitPollBudget` and `waitPollInterval`; added `export_test.go` with `SetWaitPollForTests` helper returning a restore function. Plant helper added for simulating a stuck winner without a second goroutine.
- **Files modified:** gateway/internal/idempotency/store.go, gateway/internal/idempotency/export_test.go
- **Commit:** 0d66b1c

### No Rule 4 (architectural) deviations required.

### No authentication gates encountered.

## Success Criteria — status

- [x] **TEN-09** — Idempotency-Key on POST /v1/chat/completions non-streaming (13 middleware tests confirm)
- [x] **TEN-08** — All 3 failure modes use OpenAI envelope (`httpx.WriteOpenAIError`)
- [x] First-writer-wins via SetNX (AcquireInFlight)
- [x] Cross-tenant isolation (TestMiddleware_CrossTenantIsolation + TestStore_KeyScopedByTenant)
- [x] 24h TTL for completed entries + 30s TTL for in-flight sentinel (TestStore_CompleteTTL24h, TestStore_InFlightTTL30s)
- [x] 1 MiB body cap (MaxBodySize constant)
- [x] Audit integration via IdempotencyReplayedSetter interface (TestMiddleware_AuditFlagSet)
- [x] 32 unit tests pass under -race (19 hash+store + 13 middleware)

## Self-Check: PASSED

Verified artifacts:
- `gateway/internal/idempotency/errors.go` — FOUND
- `gateway/internal/idempotency/hash.go` — FOUND
- `gateway/internal/idempotency/hash_test.go` — FOUND
- `gateway/internal/idempotency/store.go` — FOUND
- `gateway/internal/idempotency/store_test.go` — FOUND
- `gateway/internal/idempotency/middleware.go` — FOUND
- `gateway/internal/idempotency/middleware_test.go` — FOUND
- `gateway/internal/idempotency/export_test.go` — FOUND
- Commit cb7fd2d (task 1) — FOUND
- Commit 0d66b1c (task 2) — FOUND
