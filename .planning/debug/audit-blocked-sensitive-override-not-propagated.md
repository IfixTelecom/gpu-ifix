---
slug: audit-blocked-sensitive-override-not-propagated
status: root_cause_found
goal: find_root_cause_only
tdd_mode: false
trigger: phase-11-smoke-sensitive-failover-audit-decision-gate-2of4-fail
started_at: 2026-05-28T00:00:00Z
resolved_at: 2026-05-28T11:30:00Z
specialist_hint: go
---

# Audit `blocked_sensitive` Override Not Propagated — Root Cause Report

## Status

**root_cause_found** — Commit `7814678` patched the **schedule middleware** sensitive-peak short-circuit path, but the smoke does NOT exercise that path. The 4 prod smoke attempts (post-deploy at 10:59:54Z and 10:59:58Z UTC + the earlier runs at 01:22–02:53Z) all flow through the **dispatcher `writeSensitiveBlock`** path. On that path, `shed.Middleware.trackAndPass` (Branch 07 — the common case when FSM ≠ StateOn) creates a **new `*http.Request` value** via `next.ServeHTTP(w, r.WithContext(ctx))` (`gateway/internal/shed/middleware.go:271`). The dispatcher's `*r = *r.WithContext(...)` in-place mutation at `gateway/internal/proxy/dispatcher.go:365` then writes to that *new* request's struct — invisible to `audit.Middleware`, which still holds the **original** `*http.Request` pointer it captured before `next.ServeHTTP(aw, r)` ran. The audit middleware reads `r.Context()` after handler return (`gateway/internal/audit/middleware.go:78`), finds no override, and falls back to `upstreamForRoute("/v1/chat/completions") == "llm"`. This is exactly Phase 3 review HIGH-02's documented fragility, with `shed.trackAndPass` being the specific interposition that breaks the contract. Schedule middleware's own fix (commit `7814678`) is correct in principle — but **the schedule sensitive-peak branch is never reached** by the smoke (no peak window, no tier-1 override resolution for the sensitive tenant). All 8 sensitive 503 audit rows captured since the prod cutover have `upstream='llm'`. No code edit performed.

## Summary (1 paragraph)

The smoke `scripts/integration-smoke/smoke-sensitive-failover.py` induces tier-0 LLM breaker open via operator-prestep, then sends a sensitive-tenant POST `/v1/chat/completions` (non-stream, then stream). The middleware chain in prod is `obs → auth → audit → ratelimit → quota → schedule → shed → idempotency → wrapWithTimeout → dispatcher` (per `gateway/cmd/gateway/main.go:1183–1245`). For a sensitive tenant with no peak override and shed FSM not in StateOn, control reaches `shed.trackAndPass` (`gateway/internal/shed/middleware.go:259–272`), which calls `next.ServeHTTP(w, r.WithContext(ctx))` — creating a brand-new `*http.Request` struct (call it `r₁`) on top of the existing pointer the audit middleware captured (`r₀`). The dispatcher's `writeSensitiveBlock` (`gateway/internal/proxy/dispatcher.go:355–371`) then executes `*r₁ = *r₁.WithContext(auditctx.WithUpstreamOverride(r₁.Context(), UpstreamBlockedSensitiveValue))` — assigning into the `r₁` struct's memory, not `r₀`'s. When the audit middleware's deferred read fires (`gateway/internal/audit/middleware.go:78` — `auditctx.UpstreamOverrideFrom(r.Context())`), it operates on `r₀`'s pristine context. The override is absent; the route default `"llm"` is recorded. Commit `7814678` fixed only the schedule middleware's analogous case, which the smoke never reaches because the test tenant has no peak window configured. The integration test `gateway/internal/integration_test/sensitive_block_test.go:88` wires audit middleware **directly** around the dispatcher (`audit.Middleware(...)(disp)`), bypassing shed entirely — so the test PASSES in CI while production FAILS. Root cause is the `shed.trackAndPass` `r.WithContext(ctx)` interposition between audit's pointer capture and the dispatcher's in-place mutation, combined with a test harness that does not mirror the production middleware chain.

## Symptom Timeline (UTC)

| Time (UTC) | Source | Event |
|------------|--------|-------|
| 2026-05-28 00:25:44 BRT (03:25:44Z) | git | commit `7814678` — schedule middleware adds `*r = *r.WithContext(auditctx.WithUpstreamOverride(...))` on sensitive-peak short-circuit |
| 2026-05-28 07:30:31 BRT (10:30:31Z) | ghcr | image build `de86b519604e31cf2fba4bb4511c7f32208c720e` (PR #7 merged to develop) — contains the schedule middleware fix |
| 2026-05-28 10:59:24Z | docker | `ifix-ai-gateway` container restarted on n8n-ia-vm with new image |
| 2026-05-28 10:59:48Z | gateway logs | local-llm breaker closed → open (operator pre-step trip) |
| 2026-05-28 10:59:58Z | gateway logs + audit_log | sensitive POST `/v1/chat/completions` → 503 (`SensitiveRetry` exhausted, 4179 ms); audit row records `upstream='llm'` (request_id `019e6e3d-a66a-70f2-83ef-70a9b397da7e`) |
| 2026-05-28 10:59:59Z | gateway logs + audit_log | sensitive POST `/v1/chat/completions` stream:true → 503 (streaming fail-fast, 441 ms); audit row records `upstream='llm'` (request_id `019e6e3d-b6c0-7b3f-8502-54101c2a3930`) |
| (prior runs) 01:22–02:53Z | audit_log | 6 earlier sensitive 503 rows — all `upstream='llm'`, same path |

## Evidence Gathered

### E1 — Running container image is `de86b519`, contains commit `7814678`

```
$ ssh n8n-ia-vm 'docker inspect ifix-ai-gateway --format "{{.Image}} {{.Config.Labels}}"'
sha256:72dbcd4204ec37a2cfb102efa22dc594eb9acfafa40ff2801285a1aaebf1e0a6
  ... org.opencontainers.image.revision:de86b519604e31cf2fba4bb4511c7f32208c720e
      org.opencontainers.image.created:2026-05-28T07:30:31-03:00
      com.docker.compose.project:ai-gateway-prod
$ ssh n8n-ia-vm 'docker inspect ifix-ai-gateway --format "{{.Created}} {{.State.StartedAt}}"'
2026-05-28T10:59:19.805976679Z 2026-05-28T10:59:24.904046345Z

$ git show de86b51:gateway/internal/schedule/middleware.go | grep -n "blocked_sensitive\|WithUpstreamOverride"
91:    obs.GatewayScheduleRouting.WithLabelValues(cfg.Slug, "blocked_sensitive_peak").Inc()
93:    // upstream="blocked_sensitive" on every RES-08 503 path so
100:   *r = *r.WithContext(auditctx.WithUpstreamOverride(r.Context(),
101:       "blocked_sensitive"))
108:   ctx = auditctx.WithUpstreamOverride(ctx, name)
```

Container was restarted at 10:59:24Z, the smoke ran at 10:59:54–58Z — so the new binary IS the one that processed the failing requests. The `7814678` source IS in the build tree.

### E2 — All sensitive 503 audit rows record `upstream='llm'`, not `'blocked_sensitive'`

```
$ ssh n8n-ia-vm 'docker run --rm postgres:16-alpine psql "<AI_GATEWAY_PG_DSN>" -c \
  "SELECT ts, request_id, route, status_code, upstream, data_class \
   FROM ai_gateway.audit_log \
   WHERE ts >= now() - interval ''24 hours'' \
     AND route = ''/v1/chat/completions'' \
     AND data_class = ''sensitive'' \
   ORDER BY ts DESC;"'

              ts               |              request_id              | status_code | upstream | data_class
-------------------------------+--------------------------------------+-------------+----------+------------
 2026-05-28 10:59:58.785035+00 | 019e6e3d-b6c0-7b3f-8502-54101c2a3930 |         503 | llm      | sensitive  ← post-deploy
 2026-05-28 10:59:54.60231+00  | 019e6e3d-a66a-70f2-83ef-70a9b397da7e |         503 | llm      | sensitive  ← post-deploy
 2026-05-28 02:53:31.291702+00 | 019e6c80-591b-774e-a7d5-0b6e549024ac |         503 | llm      | sensitive
 2026-05-28 02:53:27.109674+00 | 019e6c80-48c5-76d9-b726-1d93b7896bdf |         503 | llm      | sensitive
 2026-05-28 01:26:22.235945+00 | 019e6c30-8f1b-7a9d-b7c6-f75858dfc82f |         503 | llm      | sensitive
 2026-05-28 01:26:18.053271+00 | 019e6c30-7ec4-7bab-80fb-67d8cd1b9ae6 |         503 | llm      | sensitive
 2026-05-28 01:24:04.012786+00 | 019e6c2e-7257-79b7-872b-8866c3b1154c |         503 | llm      | sensitive
 2026-05-28 01:22:44.516694+00 | 019e6c2d-3b4f-7543-8643-ce42f478c2df |         503 | llm      | sensitive
(8 rows)
```

`upstream='llm'` is the route default (`upstreamForRoute("/v1/chat/completions") == "llm"` in `gateway/internal/audit/middleware.go:156–158`). The smoke captured the request_id (`019e6e3d-a66a-70f2-...`), the audit row EXISTS, the data_class is correctly `sensitive` — only the `upstream` column is wrong. The 2/4 framing in the report is the operator's smoke-run accounting (4 invocations, 2 audit_decision gate failures), but every individual sensitive 503 row in the table records `upstream='llm'` — i.e. 0/8 correct.

### E3 — Gateway logs confirm the dispatcher `writeSensitiveBlock` path fired (not the schedule sensitive-peak path)

```
$ ssh n8n-ia-vm 'docker logs ifix-ai-gateway --since 30m 2>&1 | grep -E "breaker.*local-llm|019e6e3d-a66a|019e6e3d-b6c0|shed routed|schedule override|sensitive tenant in peak"'

10:59:48Z BREAKER  local-llm closed → open
10:59:58Z request  019e6e3d-a66a-... POST /v1/chat/completions status=503 latency_ms=4179
10:59:59Z request  019e6e3d-b6c0-... POST /v1/chat/completions status=503 latency_ms=441
```

Key absences (intentionally not in the output):

- **No** "sensitive tenant in peak mode at request time" log line → `schedule.Middleware`'s sensitive-peak branch (`gateway/internal/schedule/middleware.go:88–107`, the only place the new fix from `7814678` activates) NEVER FIRED.
- **No** "shed routed to tier-1" / "shed blocked sensitive tenant" log lines → `shed.Middleware`'s explicit override branches (Branches 09 / 10a / 10b, which DO use `*r = *r.WithContext(ctx)`) NEVER FIRED. Shed FSM was `healthy` (`/admin/metrics`: `fsm_state:"healthy"`), so `fsm.State() != StateOn` → `trackAndPass` (Branch 07) was taken silently.

Latency = 4179 ms on the non-streaming request matches the `SensitiveRetry` bounded retry (~4s) in `gateway/internal/proxy/sensitive.go`. Latency = 441 ms on the streaming request matches the D-B4 streaming fail-fast path. Both terminate in `cfg.writeSensitiveBlock(w, r)` (`gateway/internal/proxy/dispatcher.go:235` and `:248`).

### E4 — Middleware chain ordering: `audit` captures `r` BEFORE `shed.trackAndPass` creates the new `*Request`

```
$ grep -nE "pg.Use\(.*\.Middleware|chatHandler.*idempotency|chatHandler = px" \
    /home/pedro/projetos/pedro/gpu-ifix/gateway/cmd/gateway/main.go

1183  pg.Use(obs.RequestsMiddleware(log))
1186  pg.Use(auth.Middleware(verifier, log))
1189  pg.Use(audit.Middleware(px.auditWriter, log))           ← captures r₀ here
1196  pg.Use(quota.RateLimitMiddleware(...))
1199  pg.Use(quota.QuotaMiddleware(...))
1202  pg.Use(schedule.Middleware(px.tenantsLoader, log))
1213  pg.Use(shed.Middleware(shed.MiddlewareDeps{...}, log))
1229  chatHandler := px.chat
1236  chatHandler = idempotency.Middleware(px.idemStore, log)(chatHandler)
1245  mount(http.MethodPost, "/v1/chat/completions", chatHandler)
```

`audit.Middleware` is mounted at line 1189 and reads `r.Context()` **after** `next.ServeHTTP(aw, r)` returns:

```go
// gateway/internal/audit/middleware.go:44-80
44  return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {   ← r₀ captured
        ...
71      next.ServeHTTP(aw, r)                                                  ← r₀ passed down
        ...
78      if override := auditctx.UpstreamOverrideFrom(r.Context()); override != "" {
79          upstream = override
80      }
```

### E5 — `shed.trackAndPass` interposes a NEW `*http.Request` between audit and dispatcher

```
$ grep -nE "trackAndPass|next.ServeHTTP" /home/pedro/projetos/pedro/gpu-ifix/gateway/internal/shed/middleware.go

176     d.trackAndPass(w, r, next, t0.Name, tenantID)        ← Branch 07: FSM ≠ StateOn
188     d.trackAndPass(w, r, next, t0.Name, tenantID)        ← Branch 08: under cap
271     next.ServeHTTP(w, r.WithContext(ctx))                ← creates r₁ = new *http.Request
```

`trackAndPass` body (`gateway/internal/shed/middleware.go:259–272`):

```go
259  func (d MiddlewareDeps) trackAndPass(...) {
...
269      ctx := auditctx.WithShedDecision(r.Context(), "passed")
270      obs.GatewayShedDecisions.WithLabelValues(upstream, "passed").Inc()
271      next.ServeHTTP(w, r.WithContext(ctx))   // ← r.WithContext returns NEW *http.Request
272  }
```

`http.Request.WithContext` (stdlib `net/http/request.go`) returns a SHALLOW COPY:

> WithContext returns a shallow copy of r with its context changed to ctx.

So `r.WithContext(ctx)` allocates a new `http.Request` struct (call it `r₁`), copies the field values from `*r`, then sets `r₁.ctx = ctx`. The audit middleware's `r` (which is `r₀`) still points at the original struct in memory; `r₁` is a distinct allocation.

### E6 — Dispatcher's `writeSensitiveBlock` mutates `r₁`, not `r₀`

```
$ grep -nE "writeSensitiveBlock|\*r = \*r\.WithContext" \
    /home/pedro/projetos/pedro/gpu-ifix/gateway/internal/proxy/dispatcher.go

159     *r = *r.WithContext(auditctx.WithUpstreamOverride(r.Context(),     ← Phase 5 shed_saturated branch
                auditctx.UpstreamShedTier1UnavailableValue))
235     cfg.writeSensitiveBlock(w, r)                                       ← retry-exhaust path
248     cfg.writeSensitiveBlock(w, r)                                       ← streaming fail-fast
365     *r = *r.WithContext(auditctx.WithUpstreamOverride(r.Context(),
                UpstreamBlockedSensitiveValue))                             ← THE in-place mutation
```

`writeSensitiveBlock` (`gateway/internal/proxy/dispatcher.go:355–371`) receives `r` from its caller (the dispatcher closure), which received it from `idempotency.Middleware` (which passes through), which received it from `shed.trackAndPass` — and `shed.trackAndPass` passed `r₁` (the new request), not `r₀`. So when line 365 executes `*r = *r.WithContext(...)`, it overwrites the `r₁` struct's memory. `r₀`'s `ctx` field is untouched.

### E7 — Audit middleware reads `r₀.Context()`, sees no override, defaults to `"llm"`

`gateway/internal/audit/middleware.go:77–80`:

```go
77   upstream := upstreamForRoute(r.URL.Path)                       // = "llm" for /v1/chat/completions
78   if override := auditctx.UpstreamOverrideFrom(r.Context()); override != "" {
79       upstream = override
80   }
```

`r` here is the same `r₀` audit captured at line 44. `r₀.Context()` returns its original context — auth-stamped, request-id-stamped, but NEVER had `auditctx.WithUpstreamOverride` applied (that derivation happened on `r₁`'s context tree). `UpstreamOverrideFrom` returns `""`. `upstream` stays at the route default `"llm"`. The audit row records `upstream='llm'`. ❌

### E8 — Why the integration test passes: it bypasses the production middleware chain

```
$ grep -n "audit.Middleware\|httpx.RequestID\|wrapped :=" \
    /home/pedro/projetos/pedro/gpu-ifix/gateway/internal/integration_test/sensitive_block_test.go

88   wrapped := httpx.RequestID(audit.Middleware(auditWriter, discardLogger())(disp))
95   wrapped.ServeHTTP(rw, r)
```

The integration test wires **audit middleware directly around the dispatcher** — no `quota`, no `schedule`, no `shed`, no `idempotency`. Without `shed.trackAndPass` interposing `r.WithContext(ctx)`, the dispatcher's `writeSensitiveBlock` mutates the SAME `*http.Request` audit captured. Test passes (`upstream='blocked_sensitive'` is read back correctly), production fails. Test harness gap, not a regression.

### E9 — Schedule middleware's commit-`7814678` fix is correct, but unreachable from the smoke

The new `*r = *r.WithContext(...)` block at `gateway/internal/schedule/middleware.go:100–101` lives inside the `if cfg.DataClass == "sensitive"` branch (line 88), which is itself nested inside `if name := upstreamForTier(tier); name != ""` (line 77). `upstreamForTier` only returns a non-empty string when `tier == Tier1` (peak off-hours, line 35–40). The smoke runs against the `cobrancas` sensitive tenant during normal working hours with no peak window override configured, so `DecideUpstreamTier` returns `Tier0` and `upstreamForTier` returns `""`. The fix never fires. The fact that schedule sits ABOVE shed in the chain (line 1202 < 1213) means that *if* schedule did fire its sensitive-peak branch, it would correctly stamp the override on `r₀` (since shed wouldn't have interposed yet). But for the dispatcher-path scenario the smoke exercises, the chain order is irrelevant — the dispatcher always runs after shed's `trackAndPass` interposition.

## Root Cause

`shed.Middleware.trackAndPass` calls `next.ServeHTTP(w, r.WithContext(ctx))` (`gateway/internal/shed/middleware.go:271`), which creates a NEW `*http.Request` value (`r₁`). The dispatcher's `writeSensitiveBlock` (`gateway/internal/proxy/dispatcher.go:355–371`) writes the `blocked_sensitive` audit override via `*r = *r.WithContext(...)` — but this mutates the `r₁` struct, not the `r₀` struct that `audit.Middleware` captured for its post-handler read. The audit middleware reads `r₀.Context()`, finds no override, and writes the route default `upstream='llm'` into `audit_log`. Phase 3 review HIGH-02 explicitly anticipated this fragility ("if `audit.Middleware` ever moves the `r.Context()` read into a separate goroutine, this in-place mutation MUST be replaced…" — `dispatcher.go:357–364`); the actual breakage is a different invariant from the same family: any middleware between `audit` and the in-place-mutating handler that does `r.WithContext(...)` rather than `*r = *r.WithContext(...)` invalidates the pointer-aliasing assumption. `shed.trackAndPass` is exactly such a middleware.

## Why the Smoke Sees `upstream='llm'` (path-by-path)

1. Smoke POST `/v1/chat/completions` (data_class=sensitive, non-stream).
2. Chain `obs → auth → audit(captures r₀) → ratelimit → quota → schedule → shed → idempotency → dispatcher`.
3. `schedule.Middleware`: tenant has no peak window → `upstreamForTier(Tier0) == ""` → falls through to line 115 `next.ServeHTTP(w, r.WithContext(ctx))` with `ctx = r.Context()` unchanged from input. (This ALSO creates a new request, but with the SAME context value — irrelevant for this bug.)
4. `shed.Middleware`: FSM `healthy` (not StateOn) → Branch 07 → `trackAndPass` → `next.ServeHTTP(w, r.WithContext(WithShedDecision(...,"passed")))` → creates `r₁`. **This is the interposition that breaks the contract.**
5. `idempotency.Middleware`: passes `r₁` through unchanged.
6. `dispatcher.NewDispatcher` handler: receives `r₁`. tier-0 breaker forced-open, `sensitive == true`, streaming branch or retry-exhaust → both call `cfg.writeSensitiveBlock(w, r₁)`.
7. `writeSensitiveBlock`: `*r₁ = *r₁.WithContext(auditctx.WithUpstreamOverride(r₁.Context(), "blocked_sensitive"))`. The `r₁` struct's `ctx` field is now the derived context. Returns 503 wire response.
8. Control unwinds. `audit.Middleware` reads `r₀.Context()` — `r₀.ctx` was NEVER touched. `UpstreamOverrideFrom(r₀.Context()) == ""`. `upstream` = route default `"llm"`. Audit row enqueued with `upstream='llm'`. ❌

## Specialist Hint

`go` — Go stdlib `http.Request.WithContext` shallow-copy semantics + middleware-chain pointer-aliasing invariant. Phase 3 review HIGH-02 contract should be tightened to forbid any middleware between audit and a writer-using-in-place-mutation from calling `r.WithContext(ctx)` (must use `*r = *r.WithContext(ctx)` instead) OR — better — replace the in-place-mutation pattern entirely with a sync map keyed by `request_id`, or a `ResponseWriter`-side setter (mirroring the `IdempotencyReplayedSetter` interface at `gateway/internal/audit/middleware.go:25–27`, which was introduced for exactly this reason — see the godoc: "This avoids ctx.WithValue() mutation, which does NOT propagate back to the outer middleware's captured r reference").

## Fix Options (NOT applied — diagnose-only mode)

1. **Replace `r.WithContext` with in-place mutation in shed.trackAndPass** (minimal patch, mirrors the existing pattern):
   ```diff
   -    ctx := auditctx.WithShedDecision(r.Context(), "passed")
   -    obs.GatewayShedDecisions.WithLabelValues(upstream, "passed").Inc()
   -    next.ServeHTTP(w, r.WithContext(ctx))
   +    ctx := auditctx.WithShedDecision(r.Context(), "passed")
   +    obs.GatewayShedDecisions.WithLabelValues(upstream, "passed").Inc()
   +    *r = *r.WithContext(ctx)
   +    next.ServeHTTP(w, r)
   ```
   Same fix needed at `gateway/internal/shed/middleware.go:150` (Branch 04 "schedule already overrode" — currently also uses `r.WithContext`). The schedule middleware's pass-through line 115 has the same shape (`next.ServeHTTP(w, r.WithContext(ctx))`) but does not write any override in that branch, so the bug is latent there.

2. **Use the ResponseWriter-setter pattern instead** (more robust; aligns with `IdempotencyReplayedSetter`): add `UpstreamOverrideSetter` interface on `auditResponseWriter`; `writeSensitiveBlock` type-asserts and calls `aw.SetUpstreamOverride("blocked_sensitive")`. No `r` mutation needed. This is the contract-correct fix and is what the audit middleware godoc at `gateway/internal/audit/middleware.go:108–113` already documents as the right pattern.

3. **Add a `httptest`-driven integration test that mirrors the production chain** (`audit → schedule → shed → dispatcher`) — would have caught the regression in CI. The current `sensitive_block_test.go:88` skips schedule + shed, masking the prod chain's `r.WithContext` interposition.

## What was NOT done (scope honored)

- No source edits.
- No new smoke runs triggered.
- No DB writes (queries were read-only `SELECT`).
- No checkpoint required — DSN + container access were sufficient.

## Files cited (paths only — no inline code beyond evidence excerpts)

- `gateway/internal/audit/middleware.go` (lines 25–27, 44, 71, 77–80, 108–113, 156–158)
- `gateway/internal/auditctx/override.go` (lines 25–29 godoc on the in-place mutation contract)
- `gateway/internal/proxy/dispatcher.go` (lines 142–171, 226–253, 355–371)
- `gateway/internal/schedule/middleware.go` (lines 35–40, 77, 88–107, 115)
- `gateway/internal/shed/middleware.go` (lines 113, 147–151, 174–178, 192–250, 259–272)
- `gateway/cmd/gateway/main.go` (lines 1183–1245, middleware chain)
- `gateway/internal/integration_test/sensitive_block_test.go` (line 88, test harness gap)
- `scripts/integration-smoke/smoke-sensitive-failover.py` (the smoke that surfaced the gap)
- Image: `ghcr.io/ifixtelecom/ifix-ai-gateway:latest-dev` @ `sha256:72dbcd42…` rev `de86b519604e31cf2fba4bb4511c7f32208c720e`

## Resolution

**root_cause_found** — handed off to the orchestrator for fix planning. No code edit performed.
