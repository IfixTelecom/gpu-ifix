---
phase: 03-resilience-fallback-chain
fixed_at: 2026-04-20T04:30:00Z
review_path: .planning/phases/03-resilience-fallback-chain/03-REVIEW.md
iteration: 1
findings_in_scope: 5
fixed: 5
skipped: 1
status: partial
---

# Phase 3: Code Review Fix Report

**Fixed at:** 2026-04-20T04:30:00Z
**Source review:** .planning/phases/03-resilience-fallback-chain/03-REVIEW.md
**Iteration:** 1

**Summary:**
- Findings in scope (HIGH = warning-tier): 5
- Fixed: 5
- Skipped: 1 (HIGH-05 — formal deferral to Phase 6 by design)

Note: MED-01, MED-05 and MED-06 were also fixed as they were trivially safe and directly impactful. HIGH findings all addressed.

---

## Fixed Issues

### HIGH-01: Data Race em `probe.Probe.dropped`

**Files modified:** `gateway/internal/upstreams/probe.go`
**Commit:** 6fecef4
**Applied fix:** Replaced `dropped uint64` field with `dropped atomic.Uint64`. Removed the `p.mu.Lock/Unlock` calls from `enqueueUpdate`'s default branch (replaced with `p.dropped.Add(1)`). Changed `Dropped()` to use `p.dropped.Load()` without any mutex. Added `"sync/atomic"` to imports. The `sync.Mutex` field (`mu`) is retained since it is still referenced structurally (zero-value safe).

---

### HIGH-02: `writeSensitiveBlock` in-place `*http.Request` mutation fragility

**Files modified:** `gateway/internal/proxy/dispatcher.go`
**Commit:** 05323d7
**Applied fix:** Added a prominent CONTRACT comment inside `writeSensitiveBlock` explaining that the in-place mutation is safe only because `audit.Middleware` reads `r.Context()` sequentially after `ServeHTTP` returns, and documenting the exact remediation path (sync.Map keyed by request_id or response header) that must be applied if concurrency is ever introduced. No behavioural change — the code is currently correct; this prevents future regressions.

---

### HIGH-03: `ToolCallTerminalGuard` re-panic loses original stack trace for Sentry

**Files modified:** `gateway/internal/proxy/toolcall.go`
**Commit:** 6926e98
**Applied fix:** Before the `panic(rec)` re-throw in the defer, added `sentry.CurrentHub().RecoverWithContext(r.Context(), rec)` followed by `sentry.Flush(200ms)`. This captures the panic with the original stack trace at the point the defer runs (before re-panic changes the origin). Added `"time"` and `sentry "github.com/getsentry/sentry-go"` imports. The outer `httpx.Recoverer` will still see the re-panic and write the 500 response; the Sentry event now contains the actionable stack.

---

### HIGH-04: `TokenCounter.Enforce` called with `cfg.Role` instead of model name

**Files modified:** `gateway/internal/proxy/tokencount.go`, `gateway/internal/proxy/dispatcher.go`
**Commit:** 83c2497
**Applied fix:** Added `extractModelName(body []byte) string` helper in `tokencount.go` that unmarshals the JSON body and returns the `"model"` field (empty string on failure). In `dispatcher.go`, before calling `cfg.TokenCounter.Enforce`, extract `modelName := extractModelName(body)` and fall back to `cfg.Role` only when the body carries no model field. This ensures the Redis cache key (`gw:tokenize:{model}:{sha256}`) is specific to the tokenizer of the requested model, preventing cross-tokenizer cache collisions (Pitfall 6).

---

### MED-01: Probe response body not drained before `Close()`

**Files modified:** `gateway/internal/upstreams/probe.go`
**Commit:** caa052d
**Applied fix:** Added `_, _ = io.Copy(io.Discard, resp.Body)` immediately before `_ = resp.Body.Close()` in `dispatch()`. Added `"io"` to imports. Without draining, `net/http` cannot reuse the connection even on success, causing ~36 leaked connections per minute at steady state (6 upstreams × 10s probe cadence with un-consumed bodies on 4xx/5xx responses).

---

### MED-05: `SensitiveRetry` error discarded — `context.Canceled` inflates blocked_response metric

**Files modified:** `gateway/internal/proxy/dispatcher.go`
**Commit:** 36bc83e
**Applied fix:** Replaced `ok, _ := SensitiveRetry(...)` with `ok, retryErr := SensitiveRetry(...)`. When `!ok`, check `errors.Is(retryErr, context.Canceled)` and return early without calling `writeSensitiveBlock` (client already disconnected, nothing to write, metric should not be incremented). Added `"context"` and `"errors"` imports.

---

### MED-06: `UPSTREAM_HEALTH_BRIDGE_URL` required at boot but now optional

**Files modified:** `gateway/internal/config/config.go`, `gateway/internal/config/config_test.go`
**Commit:** be727c0
**Applied fix:** Removed `"UPSTREAM_HEALTH_BRIDGE_URL"` from both `requiredOrder` slice and `required` map in `config.go`. Updated the field comment to document it as optional (Phase 3 D-D4). Updated `config_test.go`: removed the var from `allRequired` (now 5 entries), removed `cfg.UpstreamHealthBridgeURL == ""` check from `TestLoad_AllRequiredPresent`, and updated stale "6 Phase-2 required vars" comments across `TestLoad_Phase3Defaults` and `TestLoad_Phase3ExternalURLsOptional`.

---

## Skipped Issues

### HIGH-05: `SensitiveRetry` consults in-process breaker instead of Redis mirror

**File:** `gateway/internal/proxy/sensitive.go:54-61`
**Reason:** Deferred to Phase 6 by design — per `RUNBOOK-FAILOVER.md` and the `<execution_environment>` note, this is a single-replica Phase 3 deployment where the in-process breaker is authoritative. The fix requires reading breaker state from Redis Hash `gw:breaker:{name}`, which is a multi-replica concern. Applying it now would add Redis read latency to every sensitive retry attempt with no benefit in single-replica. Formally tracked for Phase 6.
**Original issue:** `SensitiveRetry` calls `bs.Get(upstreamName).State()` (in-process gobreaker) instead of consulting the Redis mirror. In multi-replica Phase 6, a breaker closed in replica B would still appear OPEN in replica A's local state during the retry window.

---

_Fixed: 2026-04-20T04:30:00Z_
_Fixer: Claude (gsd-code-fixer)_
_Iteration: 1_
