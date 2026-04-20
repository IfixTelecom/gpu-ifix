---
status: partial
phase: 03-resilience-fallback-chain
source: [03-VERIFICATION.md, 03-08-PLAN.md Task 2]
started: 2026-04-20T01:30:00Z
updated: 2026-04-20T01:30:00Z
---

## Current Test

[awaiting human testing on Vast.ai pod + Sentry + live OpenRouter]

## Tests

### 1. SC-1 LIVE — Real pod kill failover ≤10s (BLOCKING for SC-1 full PASS)

expected: After killing `llama-server` on a live Vast.ai pod, new chat requests succeed via OpenRouter (`upstream='openrouter-chat'`) with `t_failover ≤ 10s` measured from kill timestamp to first successful audit row.
result: [pending]
how-to-run: See `03-08-PLAN.md` Task 1 Scenario A — full bash script with 120-iteration probe loop + audit_log query.
prereqs:
  - Live Vast.ai pod with `llama-server` reachable
  - Gateway dev URL: `gateway-dev.ifixtelecom.com.br`
  - `OPENROUTER_API_KEY` active (env var)
  - `TEST_API_KEY` for gateway dev (env var)

### 2. Sentry breadcrumbs on breaker transitions (recommended)

expected: After a real breaker trip (CLOSED→OPEN or OPEN→HALF_OPEN→CLOSED), Sentry events for the breaker package show `category:breaker` breadcrumbs with `upstream`, `from`, and `to` fields. NO PII (api keys, tenant tokens, request bodies) in the breadcrumb data.
result: [pending]
how-to-run: Re-run pod-kill from Test 1, then open Sentry project `ifix-ai-gateway-dev`, filter by `category:breaker`. Inspect first 5 events.
prereqs:
  - Sentry dashboard credentials for `ifix-ai-gateway-dev` project (read-only sufficient)

### 3. D-C3 tool-call drift on live OpenRouter Novita (optional)

expected: `go test -tags=e2e -run=ToolCallDrift ./internal/proxy/integration_test/... -count=1` exits 0. Confirms Novita actually emits `tool_calls` in the OpenAI-compatible response shape that `ToolCallTerminalGuard` expects (avoids silent drift if Novita changes provider behavior).
result: [pending]
how-to-run:
  ```bash
  cd /home/pedro/projetos/pedro/gpu-ifix/gateway
  export OPENROUTER_API_KEY=<from 03-WAVE0-GATES.md>
  go test -tags=e2e -run=ToolCallDrift ./internal/proxy/integration_test/... -count=1 -timeout=60s
  ```
  Cost ≈ $0.01.
prereqs:
  - `OPENROUTER_API_KEY` with cota ativa
  - Skippable if key revoked/expired — will surface in Phase 7 observability

## Summary

total: 3
passed: 0
issues: 0
pending: 3
skipped: 0
blocked: 0

## Gaps

(none — all integration tests in dev environment passed; only items above require live production environment that is not currently provisioned)
