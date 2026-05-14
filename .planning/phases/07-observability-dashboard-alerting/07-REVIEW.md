---
phase: 07-observability-dashboard-alerting
reviewed: 2026-05-14T00:00:00Z
depth: standard
files_reviewed: 56
files_reviewed_list:
  - dashboard/Dockerfile
  - dashboard/.env.example
  - dashboard/src/app/api/auth/[...all]/route.ts
  - dashboard/src/app/api/gateway/[...path]/route.ts
  - dashboard/src/app/(dashboard)/incidents/page.tsx
  - dashboard/src/app/(dashboard)/layout.tsx
  - dashboard/src/app/(dashboard)/page.tsx
  - dashboard/src/app/(dashboard)/tenants/page.tsx
  - dashboard/src/app/globals.css
  - dashboard/src/app/layout.tsx
  - dashboard/src/app/login/page.tsx
  - dashboard/src/components/app-sidebar.tsx
  - dashboard/src/components/audit-table.tsx
  - dashboard/src/components/critical-banner.test.tsx
  - dashboard/src/components/critical-banner.tsx
  - dashboard/src/components/fsm-panel.test.tsx
  - dashboard/src/components/fsm-panel.tsx
  - dashboard/src/components/kpi-card.tsx
  - dashboard/src/components/latency-chart.tsx
  - dashboard/src/components/stale-indicator.tsx
  - dashboard/src/components/tenant-table.tsx
  - dashboard/src/hooks/use-mobile.ts
  - dashboard/src/lib/auth-client.ts
  - dashboard/src/lib/auth.ts
  - dashboard/src/lib/db.ts
  - dashboard/src/lib/format.ts
  - dashboard/src/lib/fsm.ts
  - dashboard/src/lib/gateway.test.ts
  - dashboard/src/lib/gateway.ts
  - dashboard/src/lib/query-client.tsx
  - dashboard/src/lib/schema.ts
  - dashboard/src/lib/smoke.test.ts
  - dashboard/src/lib/utils.ts
  - dashboard/src/middleware.ts
  - dashboard/src/test-setup.ts
  - dashboard/vitest.config.ts
  - gateway/cmd/gateway/main.go
  - gateway/cmd/gateway/main_test.go
  - gateway/db/migrations/0020_audit_log_event_kind.sql
  - gateway/db/queries/audit.sql
  - gateway/internal/admin/audit.go
  - gateway/internal/admin/audit_test.go
  - gateway/internal/admin/metrics.go
  - gateway/internal/admin/metrics_test.go
  - gateway/internal/alert/alerter.go
  - gateway/internal/alert/alerter_test.go
  - gateway/internal/alert/brevo.go
  - gateway/internal/alert/chatwoot.go
  - gateway/internal/alert/clickup.go
  - gateway/internal/alert/client.go
  - gateway/internal/alert/dedup.go
  - gateway/internal/alert/dedup_test.go
  - gateway/internal/alert/doc.go
  - gateway/internal/alert/severity.go
  - gateway/internal/alert/severity_test.go
  - gateway/internal/alert/testsupport.go
  - gateway/internal/audit/writer.go
  - gateway/internal/audit/writer_test.go
  - gateway/internal/config/config.go
  - gateway/internal/config/config_test.go
  - gateway/internal/db/migrate_test.go
  - gateway/internal/emerg/fsm.go
  - gateway/internal/obs/metrics.go
  - gateway/internal/obs/middleware.go
  - gateway/internal/obs/middleware_test.go
  - gateway/internal/obs/sentry.go
  - gateway/internal/obs/sentry_test.go
  - gateway/internal/redisx/alert.go
  - gateway/docker-compose.yml
  - .github/workflows/build-dashboard.yml
findings:
  critical: 3
  warning: 9
  info: 6
  total: 18
status: issues_found
---

# Phase 7: Code Review Report

**Reviewed:** 2026-05-14
**Depth:** standard
**Files Reviewed:** 56 (sqlc-generated `db/gen/` and shadcn `ui/` primitives excluded as auto-generated)
**Status:** issues_found

## Summary

Phase 7 ships the Go gateway observability/alerting extensions (obs metrics + latency middleware, Sentry redaction, the new `alert` package with Chatwoot/ClickUp/Brevo channels + alerter goroutine + Redis dedup, the `/admin/metrics` + `/admin/audit` JSON handlers, audit `WriteStateChange`) plus a greenfield Next.js 15 dashboard.

The **alerting subsystem is the strongest part of the submission**: the non-blocking consume loop, per-channel bounded worker queues, the fail-open/fail-closed dedup policy, per-service circuit breakers, and secret-isolation discipline are all correct and well-tested. The Sentry redaction hook is complete and covered. The gateway admin-key never reaches the browser bundle — the server-side proxy boundary holds.

However, the **dashboard ↔ gateway API contract is fundamentally broken**: the dashboard's `gateway.ts` TypeScript interfaces describe response shapes that the Go `/admin/metrics` and `/admin/audit` handlers do not emit. Every dashboard page would render empty or crash at runtime. This is a BLOCKER — the dashboard cannot work against the gateway it ships alongside. Two more BLOCKERs concern the audit-log SQL query referencing a non-existent partition index and a dropped JSON field, and the `/admin/usage` endpoint the dashboard calls being mounted without the query the dashboard sends.

## Critical Issues

### CR-01: Dashboard ↔ gateway API contract mismatch — every page renders empty or crashes

**File:** `dashboard/src/lib/gateway.ts:43-71`, `gateway/internal/admin/metrics.go:43-68`, `gateway/internal/admin/audit.go:44-64`

**Issue:** The dashboard's typed fetch wrappers describe response shapes the gateway handlers never produce.

`/admin/metrics` — the dashboard `MetricsResponse` (gateway.ts:43-51) expects:
```
{ window, generated_at, tenants[], by_route[], by_upstream[], inflight: number, fsm_state }
```
The Go `MetricsResponse` (metrics.go:43-49) actually emits:
```
{ window, fsm_state, tenants[], inflight: InflightRow[] }
```
There is no `generated_at`, no `by_route`, no `by_upstream`, and `inflight` is an **array of `{upstream, inflight}` objects**, not a number. The Overview page reads `data.by_route` (page.tsx:108,113), `data.inflight` as a scalar (page.tsx:94), and `data.window` — `data.by_route.length` throws `TypeError: Cannot read properties of undefined`. The per-tenant rows also disagree: Go emits `tenant_id` as a raw UUID string (`row.TenantID.String()`, metrics.go:134) while the UI renders it directly as a human label.

`/admin/audit` — the dashboard `AuditResponse` (gateway.ts:66-71) expects `{ rows[], limit, offset, total }` with `AuditRow = {id, ts, event_kind, tenant_id, actor, detail}`. The Go `AuditResponse` (audit.go:44-48) emits `{ items[], limit, offset }` — **no `rows`, no `total`**, and `AuditRow` (audit.go:53-64) is `{ts, request_id, tenant_id, route, method, upstream, status_code, latency_ms, error_code, event_kind}` — no `id`, no `actor`, no `detail`. `AuditTable` keys rows on `row.id` (audit-table.tsx:94 — `undefined` for every row, React key collision), reads `row.actor` and `row.detail` (always `undefined`), and the pager math uses `total` (audit-table.tsx:68 — `canNext` is always `0 < undefined` = false). The incidents page reads `data.rows` (incidents/page.tsx:70) — always `undefined`, so the table always shows the empty state.

The `gateway.test.ts` unit tests pass only because they assert against **hand-written mock payloads that match the TS types, not the Go handler output** — they never exercise the real contract.

**Fix:** Pick one shape and make both sides agree. Either change the Go handlers to emit `rows`/`total`/`by_route`/`by_upstream`/`generated_at` and a scalar `inflight`, or change the dashboard interfaces + components to consume `items`, the `InflightRow[]` array, and drop `total`/`actor`/`detail`. The Go side already has the FSM state and per-tenant percentiles; `by_route`/`by_upstream` need either a new SQL aggregation or derivation. Add a contract test that feeds an actual Go-handler JSON fixture through the TS parser.

### CR-02: `/admin/metrics` SQL claims an index that does not exist for its scan

**File:** `gateway/db/queries/audit.sql:22-40`, `gateway/db/migrations/0020_audit_log_event_kind.sql`

**Issue:** `TenantLatencyPercentiles` filters `WHERE ts >= $1` then `GROUP BY tenant_id, route`, and the comment claims "The `ts >= $1` scan is served by `idx_audit_log_tenant_ts`". That index is `(tenant_id, ts)` — it cannot serve a predicate that has **no `tenant_id` equality** and only a `ts` range; Postgres will fall back to a per-partition sequential scan of `audit_log`. `audit_log` is the highest-volume table in the system (every proxied request writes a row). Under real traffic the dashboard polls this every 7s (query-client.tsx:20), and each poll triggers a full scan of the current partition. This is a latent production performance failure, not a style issue — but more concretely it is a **correctness risk**: migration 0020's own header (lines 13-20) admits the partition-roll automation is deferred, so once the calendar passes Jul 2026 `audit_log` has no partition for "now", and inserts (and this scan) hit the partitioned-parent default behavior. The query as written has no `ts <` upper bound either, so a misconfigured large `?window` (capped at 24h by the handler — metrics.go:41) still scans 24h of every request row.

**Fix:** Add a dedicated index `CREATE INDEX idx_audit_log_ts ON ai_gateway.audit_log (ts)` (or `(ts, tenant_id, route)` to also serve the GROUP BY) in a migration, and correct the comment in `audit.sql`. Separately, confirm the partition-roll limitation is tracked as a hard blocker before this query ships to a long-lived deploy, not just a STATE.md "Open Todo".

### CR-03: `WriteStateChange` audit rows are written with columns the dashboard query never selects, and the FSM stuffs the transition reason into `error_code`

**File:** `gateway/internal/emerg/fsm.go:330-341`, `gateway/db/queries/audit.sql:9-20`, `gateway/internal/admin/audit.go:53-64`

**Issue:** Two coupled defects make the incident-history feed misleading:

1. The emergency FSM writes its transition reason into `audit.Event.ErrorCode` (fsm.go:339) — "The transition reason rides ErrorCode". `ListAuditStateChanges` then selects `error_code` (audit.sql:15) and the handler emits it as `AuditRow.ErrorCode` (audit.go:156). A genuine 500-class request row also populates `error_code`. A dashboard or operator filtering/aggregating audit rows by `error_code` now cannot distinguish "a request failed with this error" from "the FSM transitioned because of this reason" — the column has two incompatible meanings. The dashboard's own `AuditRow` type (gateway.ts:56-63) does not even have an `error_code` field, so the reason is silently dropped on the client anyway (see CR-01).

2. `WriteStateChange` for `fsm_transition` sets `Route`, `Method`, `Upstream`, `ErrorCode` but leaves `RequestID` as `uuid.Nil` (the FSM never sets it — fsm.go:331-340). The `dbFlusher.Flush` CopyFrom writes `request_id` un-nullable (writer.go:229 — `e.RequestID` raw, not `nullableUUID`). If `audit_log.request_id` has a `NOT NULL` constraint or is part of the partition/PK, every FSM-transition flush either writes an all-zeros UUID (colliding across every transition) or the whole batch INSERT fails — taking down **all** audit logging for that batch, including ordinary request rows, because `Flush` is one transaction (writer.go:218-277). The audit_test.go `WriteStateChange` tests use a `fakeFlusher` that never exercises the real CopyFrom, so this is untested.

**Fix:** Add a dedicated nullable `reason` / `detail` column to `audit_log` (or a JSONB `detail`) instead of overloading `error_code`; have `WriteStateChange` populate it. Generate a fresh `uuid.New()` for state-change rows' `request_id` (or make the column nullable and route it through `nullableUUID`). Add a flusher test that runs an actual `WriteStateChange` Event through `dbFlusher.Flush` against a real/embedded Postgres to catch the NOT NULL / PK interaction.

## Warnings

### WR-01: `/admin/usage` is mounted but the dashboard sends a query the handler may not accept

**File:** `dashboard/src/lib/gateway.ts:165-171`, `gateway/cmd/gateway/main.go:786,999`

**Issue:** The dashboard's `fetchUsage` calls `/api/gateway/usage?tenant=<slug>&from=<date>&to=<date>` (gateway.ts:170). The Go `/admin/usage` route is wired (main.go:999) via `admin.NewUsageHandler`, but `usage.go` was not in the review set, so the param contract (`tenant` slug vs id, `from`/`to` format, whether a missing range 400s) is unverified. The dashboard `UsageResponse` type (gateway.ts:76-113) was hand-authored "mirrors usage.go UsageResponse" — given CR-01 shows the same hand-authored approach already diverged for metrics+audit, the usage contract is suspect. The tenants page's cost panel is dead weight if this also mismatches.

**Fix:** Verify `gateway/internal/admin/usage.go` accepts exactly `tenant`/`from`/`to` with the formats the dashboard sends, and that `UsageResponse` matches field-for-field. Add the same contract fixture test as CR-01.

### WR-02: `readInflight` reads `GatewayInflight` gauge sized for a buffered channel that can silently truncate

**File:** `gateway/internal/admin/metrics.go:167-190`

**Issue:** `readInflight` does `ch := make(chan prometheus.Metric, 256)` then `obs.GatewayInflight.Collect(ch)` then `close(ch)`. `Collect` is synchronous and writes every child series into the channel; if `GatewayInflight` ever has more than 256 label series, `Collect` **blocks forever** on a full channel (the close happens after Collect returns) — a deadlocked request goroutine, not a truncation. The comment says "~6 upstream series, far under the buffer", but `GatewayInflight` is labelled by `upstream` and the upstream set is hot-reloadable (`shedInflight.AddUpstream`, main.go:494) — there is no enforced ceiling. A bounded-but-generous buffer is not a correctness guarantee.

**Fix:** Use the documented `prometheus.Gatherer` pattern, or run `Collect` in a goroutine and close the channel from there so a full channel cannot deadlock the request: `go func(){ obs.GatewayInflight.Collect(ch); close(ch) }()`.

### WR-03: `ReconcileBoot` dedup gate makes a mid-incident restart silently NOT re-page when Redis is healthy

**File:** `gateway/internal/alert/alerter.go:286-331`, `gateway/internal/alert/dedup.go:69-87`

**Issue:** `ReconcileBoot` synthesises an emergency event and runs it through `handle()` → `dedupShouldSend`. The dedup key is `gw:alert:dedup:emerg:transition:<state>` with a 5-minute TTL. The documented intent (alerter.go:274-278) is "a fast restart does not double-page". But consider the actual incident timeline: the emergency FSM entered `emergency_active` and the *original live alerter* alerted and claimed the dedup key. The gateway then restarts. If the restart completes **within 5 minutes** (the common case — container restarts are fast), `ReconcileBoot` finds the key still set and **suppresses the alert**. The operator's on-call WhatsApp/ClickUp/email now has a 5-minute-old page for an incident that is *still active and unacknowledged after a gateway crash* — arguably the single most important moment to re-surface it. The "fast restart does not double-page" goal and the "mid-incident restart still pages" goal (T-07-18) are in direct tension and the dedup gate resolves it the wrong way for crash-during-incident.

**Fix:** `ReconcileBoot` should bypass the dedup gate for an active critical state (or use a distinct, shorter-lived "boot reconcile" fingerprint), or fail-open unconditionally for `emergCriticalStates`. The double-page annoyance is strictly less bad than a silenced active incident after a crash — the dedup.go comment itself makes exactly this argument for the fail-open case.

### WR-04: `ReconcileBoot` builds a synthetic event whose fingerprint does not match the live event's fingerprint

**File:** `gateway/internal/alert/alerter.go:316-322`, `gateway/internal/alert/severity.go:185`

**Issue:** Related to WR-03 but a distinct bug. The live emerg path publishes `EmergEvent{Type: "transition", State: ...}` and `severityForEmerg` fingerprints it as `emerg:transition:<state>` (severity.go:185). `ReconcileBoot` synthesises `redisx.EmergEvent{Type: "transition", State: stateName}` — so the fingerprint *does* match for `transition`. However, `ReconcileBoot` only sets `ev.Reason` when `lifecycle_id` is non-empty (alerter.go:320-322) and never sets `ev.LifecycleID` or `ev.SinceUnix`. Since the fingerprint deliberately excludes timestamp and reason, the fingerprints align — but the dedup behavior is then exactly WR-03. More importantly: if a future change makes the live emerg publisher emit a `Type` other than `"transition"` for the active-state event, the synthetic reconcile event silently stops deduplicating *and* stops matching, and nobody notices because there is no test asserting the live-vs-synthetic fingerprint equality. The test `TestAlerter_ReconcileBootSurfacesActiveIncident` only works because it runs against a *fresh* miniredis with no pre-existing dedup key.

**Fix:** Add a test that claims the dedup key first (simulating the live alerter having already paged), then runs `ReconcileBoot`, and asserts the desired behavior (which per WR-03 should be: it still pages). Also assert `severityForEmerg` of the synthetic event yields the identical fingerprint a live transition event would.

### WR-05: Brevo SMTP channel sends credentials over a connection with no TLS guarantee

**File:** `gateway/internal/alert/brevo.go:108-151`

**Issue:** `Send` calls `c.sendMail(addr, auth, c.from, c.to, body)` where `sendMail` defaults to `net/smtp.SendMail` and `auth` is `smtp.PlainAuth("", c.user, c.pass, c.host)`. `smtp.PlainAuth` refuses to send the password unless the connection is TLS *or* the host is `localhost` — which means against a relay that does not advertise STARTTLS the send fails with `unencrypted connection` (good), but `net/smtp.SendMail` does opportunistic STARTTLS only — it does **not verify** that STARTTLS actually succeeded before `PlainAuth` runs; `SendMail` will STARTTLS if offered. The real gap: there is no `tls.Config` with `ServerName` pinning, and no enforcement that the relay *must* be encrypted. The package doc claims "host:587, STARTTLS" but nothing in the code enforces port 587 or rejects a plaintext relay. A MITM that strips the STARTTLS capability advertisement causes `SendMail` to... still refuse (PlainAuth guard) — so the password does not leak — but the alert silently fails instead. That is acceptable for secret-safety but the doc comment overstates the security posture.

**Fix:** Document the actual behavior accurately, or construct the SMTP conversation explicitly with `tls.Config{ServerName: c.host}` and require STARTTLS so a stripped-capability MITM is a hard error rather than relying on the `PlainAuth` localhost/TLS guard as an accidental backstop.

### WR-06: `proxyGet` discards the gateway/proxy error body, so every dashboard error shows the same misleading message

**File:** `dashboard/src/lib/gateway.ts:130-146`, `dashboard/src/app/api/gateway/[...path]/route.ts:54-65`

**Issue:** The server-side proxy returns structured error envelopes — `configuration_error` (500), `upstream_unreachable` (502) — with distinct `message`/`type` fields (route.ts:29-65). `proxyGet` throws `new GatewayError(res.status, "Não foi possível carregar as métricas do gateway.")` (gateway.ts:139-142) — a **hardcoded string**, ignoring the actual envelope. So a 500 "GATEWAY_ADMIN_KEY not configured" and a 502 "gateway unreachable" and a 401 "admin key invalid" all surface to the operator as the identical "could not load metrics" text. The incidents page error copy even tells the operator to check "se a admin-key está válida" (incidents/page.tsx:60-62) — but the page literally cannot tell a bad key from a down gateway from an unconfigured proxy. For an operator-debugging tool whose entire job is incident triage, this erases the most useful diagnostic signal.

**Fix:** In `proxyGet`, parse the JSON body on `!res.ok` and surface `body.error.message` / `body.error.type` in the thrown `GatewayError`. Render that message in the page error states instead of the generic string.

### WR-07: `StaleIndicator` and `CriticalBanner` timers desync because the indicator never resets on refetch

**File:** `dashboard/src/components/stale-indicator.tsx:18-35`, `dashboard/src/components/critical-banner.tsx:43-60`

**Issue:** `StaleIndicator` runs a 1s `setInterval` that recomputes `now`, and derives `seconds = round((now - updatedAt)/1000)`. `updatedAt` is React Query's `dataUpdatedAt`. This is correct *only* if the query actually refetches and `dataUpdatedAt` advances. With `refetchInterval: 7000` (query-client.tsx:31) but the tab backgrounded, browsers throttle `setInterval` AND React Query may defer the refetch — the indicator can show "Atualizado há 4s" while the underlying data is minutes stale, because `setInterval` was throttled to fire less often so `now` lags. The displayed staleness is computed from a throttled clock, not wall time at render. Minor, but this is a *staleness indicator* — its entire purpose is to be trustworthy when data is stale, which is exactly when the tab is likely backgrounded.

**Fix:** Compute `seconds` from `Date.now()` directly inside the render (or in a `requestAnimationFrame`-driven tick) rather than from a `setInterval`-captured `now` — `Date.now()` is never throttled, only the *frequency of re-render* is, and a stale render showing a fresh `Date.now() - updatedAt` is still correct.

### WR-08: `isoDate` uses `toISOString()` which shifts the operator's selected calendar day by their UTC offset

**File:** `dashboard/src/app/(dashboard)/tenants/page.tsx:46-49`

**Issue:** `isoDate(d) = d.toISOString().slice(0, 10)`. The `react-day-picker` `Calendar` returns `Date` objects at **local midnight**. For an operator in `America/Sao_Paulo` (UTC−3), local midnight `2026-05-14T00:00:00−03:00` → `toISOString()` → `2026-05-14T03:00:00Z` → `.slice(0,10)` → `"2026-05-14"` — OK by luck for negative offsets at midnight. But for the *end* of a range the user picks "2026-05-14" and may expect inclusive end-of-day; and any operator in a positive-offset timezone (or if the picker ever returns a non-midnight time) gets the previous day. The whole codebase is timezone-sensitive (`America/Sao_Paulo` is threaded everywhere in the gateway) — silently shifting the cost-report date range by a UTC offset produces wrong cost numbers for the boundary days.

**Fix:** Format the local date components directly: `` `${d.getFullYear()}-${String(d.getMonth()+1).padStart(2,'0')}-${String(d.getDate()).padStart(2,'0')}` `` — never round-trip a calendar-day selection through UTC.

### WR-09: `dashboard/src/lib/db.ts` Proxy swallows the property type, breaking `instanceof` and method-bound calls

**File:** `dashboard/src/lib/db.ts:52-58`

**Issue:** `db` is `new Proxy({} as DrizzleClient, { get(_t, prop, receiver) { return Reflect.get(getClient(), prop, receiver) } })`. `Reflect.get(target, prop, receiver)` with `receiver` set to the **proxy** means any getter on the real drizzle client runs with `this` = the proxy, not the real client — and any method retrieved this way is *unbound*; when Better Auth's `drizzleAdapter` does `const { select } = db` or passes `db.query` around, `this` is lost. drizzle's client mostly uses arrow-bound internals so it may happen to work today, but this is a fragile pattern that will break opaquely on a drizzle minor bump. The lazy-init goal (defer Pool construction past `next build`) is valid; the Proxy implementation is the wrong tool.

**Fix:** Drop the Proxy. Export a `getDb()` function and call it at the top of each route handler / in the Better Auth `database` factory, or use a module-level lazy getter that returns the real client object directly (`let _db; export function db() { return _db ??= drizzle(...) }`). If a property-access shape is required, at minimum pass the real client (not the proxy) as the `Reflect.get` receiver.

## Info

### IN-01: Dashboard auth allows unrestricted self-signup — any visitor can create an operator account

**File:** `dashboard/src/lib/auth.ts:18-26`, `dashboard/src/lib/auth-client.ts:11`

**Issue:** `betterAuth({ emailAndPassword: { enabled: true } })` with no `disableSignUp` and no allow-list. `auth-client.ts` even re-exports `signUp`. Better Auth's `/api/auth/sign-up/email` route is live and unauthenticated (the middleware explicitly excludes `/api/auth`, middleware.ts:28). Anyone who reaches the dashboard URL can `POST /api/auth/sign-up/email` and self-provision an operator login, then see every tenant's latency/cost/failover state. The comment says "~4 Ifix admins" but nothing enforces that. Classified Info rather than Critical only because the dashboard is intended to sit behind Traefik on an internal domain — but "internal domain" is not an auth control.

**Fix:** Set `emailAndPassword: { enabled: true, disableSignUp: true }` and seed the ~4 operator accounts via the Better Auth CLI / a one-time script, or add an email allow-list in a `before` hook. Remove the `signUp` re-export from `auth-client.ts` so it cannot be called from a future component.

### IN-02: `formatDetail` in `audit-table.tsx` renders a field that the gateway never sends

**File:** `dashboard/src/components/audit-table.tsx:42-47,110`

**Issue:** `formatDetail(detail)` iterates `Object.entries(detail)` of `AuditRow.detail` — a field that does not exist on the gateway's `AuditRow` (see CR-01). Once CR-01 is resolved one way or the other, this helper is either dead code or needs rewriting. Flagging so it is not left as a no-op `"—"` renderer.

**Fix:** Resolve as part of CR-01.

### IN-03: `itoa` hand-rolled in `alerter_test.go` duplicates `strconv.Itoa`

**File:** `gateway/internal/alert/alerter_test.go:352-373`

**Issue:** A 20-line hand-rolled integer-to-string "to keep the test import block minimal". `strconv` is already a standard, zero-cost import used across the gateway. The hand-rolled version is more code to maintain and review for no benefit.

**Fix:** `import "strconv"` and use `strconv.Itoa`.

### IN-04: `channelJob` struct wraps a single field for no reason

**File:** `gateway/internal/alert/alerter.go:64-66`

**Issue:** `type channelJob struct { msg Message }` — a one-field wrapper. `chan channelJob` could just be `chan Message`. The wrapper adds an indirection with no current purpose and no comment explaining a planned extension.

**Fix:** Use `chan Message` directly, or add a comment naming the future field that justifies the struct.

### IN-05: `Dockerfile` copies `.env.example` is avoided but `.npmrc` with `legacy-peer-deps` masks a real dependency conflict

**File:** `dashboard/Dockerfile:19-23`

**Issue:** The Dockerfile comment documents that `.npmrc` carries `legacy-peer-deps` because "better-auth's optional @sveltejs/kit peer collides with @vitejs/plugin-react's vite peer". `legacy-peer-deps` globally disables peer-dependency resolution for the *entire* install — it does not narrowly fix that one collision, it silences every future peer mismatch too. A genuinely incompatible transitive bump will now install silently and fail at runtime instead of at `npm ci`.

**Fix:** Prefer a targeted `overrides` block in `package.json` for the specific conflicting peer, or move `@vitejs/plugin-react` (a vitest-only devDependency) such that it does not collide. If `legacy-peer-deps` must stay, add a `npm ls` sanity step to CI.

### IN-06: `severity.go` hardcodes `primaryLLMUpstream = "local-llm"` while the gateway treats the upstream set as hot-reloadable config

**File:** `gateway/internal/alert/severity.go:41`

**Issue:** `const primaryLLMUpstream = "local-llm"` decides whether a breaker-open event is critical (page WhatsApp) or merely warning. But the upstream catalog is DB-driven and hot-reloadable (`upstreams.Loader`, `loader.Names()`, main.go:294,481). If an operator ever renames the tier-0 upstream row, or adds a second primary, the alerter silently downgrades a real GPU-down event to warning-tier and the on-call operator is never WhatsApp-paged. A wire-level constant encoding a business-critical routing decision should at least be config-driven or asserted against the loader at boot.

**Fix:** Source the primary upstream name from config (e.g. a `PRIMARY_LLM_UPSTREAM` env var defaulting to `local-llm`) or have `buildAlertChannels`/the alerter validate at boot that `local-llm` exists in `loader.Names()` and log a loud WARN if not.

---

_Reviewed: 2026-05-14_
_Reviewer: Claude (gsd-code-reviewer)_
_Depth: standard_
