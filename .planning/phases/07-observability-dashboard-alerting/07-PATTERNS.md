# Phase 7: Observability — Dashboard & Alerting - Pattern Map

**Mapped:** 2026-05-14
**Files analyzed:** 19 new/modified (13 Go gateway-side, 6 representative dashboard-side)
**Analogs found:** 13 / 13 Go files have in-repo analogs · dashboard files have no in-repo analog (greenfield — use converseai-v4 + RESEARCH.md)

## File Classification

### Gateway side (Go — extends existing packages)

| New/Modified File | Role | Data Flow | Closest Analog | Match Quality |
|-------------------|------|-----------|----------------|---------------|
| `gateway/internal/admin/metrics.go` | controller (handler) | request-response | `gateway/internal/admin/usage.go` | exact (same sub-router, same struct shape) |
| `gateway/internal/admin/audit.go` | controller (handler) | request-response (paginated read) | `gateway/internal/admin/usage.go` | exact |
| `gateway/internal/alert/alerter.go` | service (goroutine) | event-driven (pub-sub consume) | `gateway/internal/breaker/subscribe.go` + `gateway/internal/emerg/subscribe.go` | exact (canonical reconnect loop) |
| `gateway/internal/alert/severity.go` | utility | transform | `gateway/internal/breaker/breaker.go` (`stateFloat` mapping) | role-match |
| `gateway/internal/alert/dedup.go` | utility | request-response (Redis) | `gateway/internal/redisx/emerg.go` (Redis helper funcs) | role-match |
| `gateway/internal/alert/chatwoot.go` | service (external client) | request-response (outbound HTTP) | `gateway/internal/emerg/vast/client.go` | exact (HTTP client + auth-header isolation + error classification) |
| `gateway/internal/alert/clickup.go` | service (external client) | request-response (outbound HTTP) | `gateway/internal/emerg/vast/client.go` + `gateway/internal/proxy/retry.go` + `gateway/internal/breaker/breaker.go` | exact |
| `gateway/internal/alert/brevo.go` | service (external client) | request-response (SMTP) | `gateway/internal/emerg/vast/client.go` (client struct + constructor shape) | role-match (net/smtp, no in-repo SMTP analog) |
| `gateway/internal/redisx/alert.go` (channel consts + dedup helper, if split out) | utility (Redis helper) | pub-sub | `gateway/internal/redisx/emerg.go` | exact |
| `gateway/internal/obs/metrics.go` (EXTEND) | config (collector decls) | — | existing collectors in same file (`ProbeDurationMs` histogram pattern) | exact (self) |
| `gateway/internal/obs/middleware.go` (EXTEND) | middleware | request-response | existing `gateway/internal/obs/middleware.go` | exact (self) |
| `gateway/internal/obs/sentry.go` (EXTEND) | config (hook) | event-driven | existing `gateway/internal/obs/sentry.go` `BeforeSend` | exact (self) |
| `gateway/internal/audit/writer.go` (EXTEND) | service (writer) | batch (async DB write) | existing `gateway/internal/audit/writer.go` | exact (self) |
| `gateway/db/migrations/0020_audit_log_event_kind.sql` | migration | — | `gateway/db/migrations/0018_audit_log_shed_values.sql` / `0019_emergency_lifecycles.sql` | exact |
| `gateway/db/queries/audit.sql` (EXTEND) | query | CRUD (read) | existing `gateway/db/queries/audit.sql` + `gateway/db/queries/emergency_lifecycles.sql` | exact |
| `gateway/cmd/gateway/main.go` (EXTEND — wire alerter goroutine + 2 admin routes) | config (composition root) | — | existing `main.go` goroutine spawns + admin router block | exact (self) |

### Dashboard side (greenfield `dashboard/` Next.js 15 app — no in-repo analog)

| New File | Role | Data Flow | Analog | Match Quality |
|----------|------|-----------|--------|---------------|
| `dashboard/src/lib/auth.ts` | config (auth) | — | converseai-v4 `packages/auth/` (separate repo) | no in-repo analog — use RESEARCH.md §"Better Auth standalone instance" |
| `dashboard/src/app/api/auth/[...all]/route.ts` | route | request-response | converseai-v4 (separate repo) | no in-repo analog — RESEARCH.md code example |
| `dashboard/src/app/api/gateway/[...path]/route.ts` | route (server proxy) | request-response | — | no analog — RESEARCH.md §Anti-Patterns ("X-Admin-Key stays server-side") |
| `dashboard/src/middleware.ts` | middleware | request-response | converseai-v4 (separate repo) | no in-repo analog — RESEARCH.md code example |
| `dashboard/src/components/*` (kpi-card, latency-chart, fsm-panel, critical-banner) | component | — | converseai-v4 `apps/web/` shadcn components (separate repo) | no in-repo analog — follow 07-UI-SPEC.md |
| `dashboard/components.json` + `globals.css` | config | — | `/home/pedro/projetos/pedro/converseai-v4/apps/web/components.json` + `globals.css` | reference verbatim (per 07-UI-SPEC.md) |

> **Dashboard note for the planner:** The `dashboard/` app is greenfield. There is no Go-style analog in `gpu-ifix`. The pattern source is the converseai-v4 repo (separate, at `/home/pedro/projetos/pedro/converseai-v4/`) plus the verbatim code examples in `07-RESEARCH.md` lines 471-505 and the visual contract in `07-UI-SPEC.md`. Do not search `gpu-ifix` for dashboard analogs — there are none.

## Pattern Assignments

### `gateway/internal/admin/metrics.go` (controller, request-response)

**Analog:** `gateway/internal/admin/usage.go`

Clone the `UsageHandler` shape exactly: a struct with an injected sqlc-query interface + `*slog.Logger`, a public `NewXHandler(*gen.Queries, *slog.Logger)` constructor, a private `newXHandlerWithQueries(iface, logger)` test constructor, and a `ServeHTTP`. Register under `/admin` in `buildRouter` next to the existing `/usage` mount.

**Package doc + imports** (`usage.go` lines 1-27):
```go
// Package admin (metrics.go): GET /admin/metrics handler. ...
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)
```

**Query-interface isolation + dual constructor** (`usage.go` lines 83-113) — copy this pattern so tests inject a fake without a real pgxpool:
```go
type usageQueries interface {
	GetTenantBySlug(ctx context.Context, slug string) (gen.GetTenantBySlugRow, error)
	// ... only the sqlc methods this handler calls
}
type UsageHandler struct {
	q   usageQueries
	log *slog.Logger
}
func NewUsageHandler(q *gen.Queries, log *slog.Logger) *UsageHandler {
	if log == nil { log = slog.Default() }
	return &UsageHandler{q: q, log: log.With("module", "ADMIN_USAGE")}
}
func newUsageHandlerWithQueries(q usageQueries, log *slog.Logger) *UsageHandler { /* test ctor */ }
```

**Query-param validation + error envelope + metric increment** (`usage.go` lines 115-148) — every bad-input branch writes `httpx.WriteOpenAIError` AND increments `obs.GatewayAdminRequests.WithLabelValues("/admin/metrics", "4xx").Inc()`:
```go
if tenantArg == "" || from == "" || to == "" {
	httpx.WriteOpenAIError(w, http.StatusBadRequest,
		"invalid_request_error", "missing_query_param",
		"Required query params: ...")
	obs.GatewayAdminRequests.WithLabelValues("/admin/metrics", "4xx").Inc()
	return
}
```

**JSON response shape + success path** (`usage.go` lines 30-67, 236-239) — typed response structs with `json:` tags, then:
```go
w.Header().Set("Content-Type", "application/json")
w.WriteHeader(http.StatusOK)
_ = json.NewEncoder(w).Encode(resp)
obs.GatewayAdminRequests.WithLabelValues("/admin/metrics", "2xx").Inc()
```

**Phase 7-specific data sources** (NOT from the analog — from `07-RESEARCH.md`):
- Per-tenant P50/P95/P99 + error rate: a new sqlc query against `ai_gateway.audit_log` using `percentile_cont` (RESEARCH.md lines 432-446) — NOT a Prometheus label.
- FSM state: `emergFSM.State()` — `gateway/internal/emerg/fsm.go:194` exposes `func (f *FSM) State() State`. The handler needs the `*emerg.FSM` injected into its struct.
- Inflight: read the `obs.GatewayInflight` gauge.

---

### `gateway/internal/admin/audit.go` (controller, request-response, paginated read)

**Analog:** `gateway/internal/admin/usage.go` (same structure as `metrics.go` above).

Same handler shape. The differences are Phase 7-specific:
- New sqlc query in `gateway/db/queries/audit.sql` — a paginated `SELECT ... FROM ai_gateway.audit_log WHERE event_kind IS NOT NULL ORDER BY ts DESC LIMIT $1 OFFSET $2` (the incident-history read path). Follow the `:many` query style in `gateway/db/queries/emergency_lifecycles.sql`.
- Validate `limit`/`offset` query params the same way `usage.go` validates `from`/`to` (lines 124-148).

---

### `gateway/internal/alert/alerter.go` (service goroutine, event-driven / pub-sub)

**Analog:** `gateway/internal/breaker/subscribe.go` (canonical) + `gateway/internal/emerg/subscribe.go` (multi-channel variant).

**The canonical reconnect-with-1s-backoff Pub/Sub loop** (`breaker/subscribe.go` lines 19-56) — copy this skeleton verbatim, it is explicitly documented in-repo as "the canonical Pub/Sub-with-redis-go pattern in this codebase":
```go
func (s *Set) Subscribe(ctx context.Context) {
	log := s.log.With("subsystem", "subscribe")
	for {
		if err := ctx.Err(); err != nil { return }
		ps := redisx.SubscribeBreakerEvents(ctx, s.rdb)
		ch := ps.Channel()
		drained := false
		for !drained {
			select {
			case <-ctx.Done():
				_ = ps.Close()
				return
			case msg, ok := <-ch:
				if !ok { drained = true; break }
				var ev redisx.BreakerEvent
				if err := json.Unmarshal([]byte(msg.Payload), &ev); err != nil {
					log.Warn("malformed breaker event", "payload", msg.Payload, "err", err)
					continue   // malformed JSON drops the event, NEVER crashes the loop
				}
				s.applyRemoteEvent(ev)
			}
		}
		_ = ps.Close()
		log.Warn("pubsub channel closed; reconnecting")
		select {
		case <-ctx.Done(): return
		case <-time.After(1 * time.Second):
		}
	}
}
```

**Multi-channel subscription:** the alerter subscribes to all three channels on one connection. `emerg/subscribe.go` shows two separate consumers, but per RESEARCH.md a single `rdb.Subscribe(ctx, "gw:breaker:events", "gw:shed:events", "gw:emerg:events")` is correct — `msg.Channel` discriminates. Existing channel-name constants/helpers to reuse:
- `redisx.SubscribeBreakerEvents` / `redisx.BreakerEventsChannel()` — `gateway/internal/redisx/breaker.go:64,70`
- `redisx.SubscribeShedEvents` / `redisx.ShedEventsChannel` — `gateway/internal/redisx/shed.go:152,38`
- `redisx.SubscribeEmergEvents` / `redisx.EmergEventsChannel` — `gateway/internal/redisx/emerg.go:139,39`

**Boot-ordering invariant** — `emerg/subscribe.go` lines 16-19 document it: spawn the alerter goroutine BEFORE the subsystems that publish (Pub/Sub is at-most-once, no replay). Mirror this in `main.go` wiring.

**Do NOT block the consume loop on a slow external send** — RESEARCH.md Pitfall 5. The `handle()` body should classify + dedup + enqueue to a bounded per-channel worker. The in-repo precedent is Phase 5's `MakePublishTransition` bounded-worker pattern (`gateway/internal/shed/`).

---

### `gateway/internal/alert/severity.go` (utility, transform)

**Analog:** `gateway/internal/breaker/breaker.go` `stateFloat` (lines 158-167) — a small pure mapping function.

This is a pure `event → severity tier` + `severity → channel matrix` map. No I/O. The channel matrix from CONTEXT.md: `critical` → Chatwoot+ClickUp+email, `warning` → ClickUp+email, `info` → none. Keep it a plain switch/map like `stateFloat`.

---

### `gateway/internal/alert/dedup.go` (utility, Redis request-response)

**Analog:** `gateway/internal/redisx/emerg.go` — the small-function Redis-helper style (e.g. `WriteEmergState` lines 103-116).

**Pattern:** `SET NX EX 300` fingerprint dedup (RESEARCH.md lines 257-268):
```go
ok, err := rdb.SetNX(ctx, "gw:alert:dedup:"+fingerprint, "1", 5*time.Minute).Result()
if err != nil { /* fail-OPEN for critical, fail-CLOSED for warning/info */ }
if !ok { return /* duplicate within window — skip external send, still log */ }
```
Use the existing 2-second `redisOpTimeout` convention from `redisx/shed.go` (referenced in `emerg.go` lines 53-56). If you add channel-name or key-prefix constants, put them in a new `gateway/internal/redisx/alert.go` following the `emerg.go` file-header + const-block style.

---

### `gateway/internal/alert/chatwoot.go` (service, external HTTP client)

**Analog:** `gateway/internal/emerg/vast/client.go`

**Client struct + fixed-timeout constructor** (`vast/client.go` lines 75-115):
```go
const httpTimeout = 30 * time.Second   // package-level const, not magic number
type Client struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
}
func NewClient(apiKey string) *Client {
	return &Client{apiKey: apiKey, baseURL: defaultBaseURL,
		httpClient: &http.Client{Timeout: httpTimeout}}
}
func NewClientWithBaseURL(apiKey, baseURL string) *Client { /* test ctor */ }
```

**Auth-header isolation** (`vast/client.go` lines 327-333) — the API token touches `http.Request` in exactly ONE greppable method, never flows to logs/errors:
```go
func (c *Client) setAuthHeader(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
}
```
For Chatwoot the header is `api_access_token: <token>` (RESEARCH.md line 291) — same one-method isolation pattern.

**Error-body parsing with bounded read + secret-free errors** (`vast/client.go` lines 335-375) — `io.LimitReader(resp.Body, 16*1024)`, JSON-decode the envelope, map status → sentinel error; the returned error NEVER includes the URL or any header.

**Per-request metric increment** (`vast/client.go` lines 306-313) — `started` before the call, then `strconv.Itoa(resp.StatusCode)` after. Add a `gateway_alert_*` counter following this shape.

**Chatwoot endpoint** (RESEARCH.md Pattern 4, lines 288-305): `POST {CHATWOOT_API_URL}/api/v1/accounts/{account_id}/conversations` with body `{inbox_id, contact_id, status:"open", message:{content:"..."}}`.

---

### `gateway/internal/alert/clickup.go` (service, external HTTP client)

**Analog:** `gateway/internal/emerg/vast/client.go` (client/auth/error structure as above) + `gateway/internal/proxy/retry.go` (backoff) + `gateway/internal/breaker/breaker.go` (gobreaker).

**Backoff retry with `Permanent` classification** (`proxy/retry.go` lines 32, 49-83) — the `cenkalti/backoff/v5` pattern already vendored and used in-repo:
```go
import "github.com/cenkalti/backoff/v5"

bo := backoff.NewExponentialBackOff()
wrap := func() (*http.Response, error) {
	resp, err := /* do request */
	if /* 4xx except 429 */ {
		return nil, backoff.Permanent(err)   // STOP — ClickUp 401 is non-retryable (RESEARCH Pitfall 6)
	}
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		return nil, backoff.RetryAfter(secs)
	}
	return resp, nil
}
_, err := backoff.Retry(ctx, wrap,
	backoff.WithBackOff(bo),
	backoff.WithMaxElapsedTime(/* alert budget */))
```

**Circuit breaker per external service** (`breaker/breaker.go` lines 20, 131-156) — `sony/gobreaker/v2`, one breaker per client:
```go
import "github.com/sony/gobreaker/v2"

cb := gobreaker.NewCircuitBreaker[*http.Response](gobreaker.Settings{
	Name:        "clickup",
	ReadyToTrip: func(c gobreaker.Counts) bool { /* ... */ },
	OnStateChange: func(n string, from, to gobreaker.State) { /* ... */ },
})
```

**ClickUp facts** (RESEARCH.md Pattern 3, lines 281-284): `POST https://api.clickup.com/api/v2/list/{list_id}/task`, auth = raw token in `Authorization` (NOT `Bearer`), rate-limit headers `X-RateLimit-Remaining`/`X-RateLimit-Reset`. The `AdaptiveRateLimiter` is a small custom struct — there is no in-repo Go analog; build it minimally per RESEARCH.md line 279.

---

### `gateway/internal/alert/brevo.go` (service, SMTP client)

**Analog:** `gateway/internal/emerg/vast/client.go` — only for the client-struct + constructor shape. There is **no in-repo SMTP analog** — this is the one Go file with only a partial-match analog.

Use Go stdlib `net/smtp` (RESEARCH.md Pattern 5, lines 307-314): `net/smtp.SendMail(addr, smtp.PlainAuth("", user, pass, host), from, to, msg)`. Wrap the send in a `gobreaker` breaker + short `backoff` retry, same as `clickup.go`. Constructor takes the SMTP env config struct.

---

### `gateway/internal/obs/metrics.go` (EXTEND — collector declarations)

**Analog:** the existing collectors in the same file — specifically `ProbeDurationMs` (lines 84-91), a `HistogramVec` with explicit buckets and ONE bounded label:
```go
var ProbeDurationMs = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "gateway_probe_duration_ms",
	Help:    "...",
	Buckets: []float64{50, 100, 250, 500, 1000, 2500, 5000},
}, []string{"upstream"})
```

**Phase 7 additions** (RESEARCH.md lines 416-428) — TWO narrow histograms, never cross `tenant × route × upstream`:
```go
var RequestDurationByRoute = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name: "gateway_request_duration_ms_by_route", Buckets: []float64{25,50,100,250,500,1000,2500,5000,10000},
}, []string{"route"})
var RequestDurationByUpstream = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name: "gateway_request_duration_ms_by_upstream", Buckets: []float64{25,50,100,250,500,1000,2500,5000,10000},
}, []string{"upstream"})
```
Also add a `gateway_alert_dropped_total` counter (RESEARCH.md Pitfall 5) following the `AuditDroppedTotal` plain-counter shape (lines 27-32). Every new collector needs the cardinality-budget comment in its doc — see the Phase 5/6 budget headers (lines 240-244, 376-389).

---

### `gateway/internal/obs/middleware.go` (EXTEND — record the new histogram)

**Analog:** existing `gateway/internal/obs/middleware.go` (the file already records `RequestsTotal`). Add `.Observe()` calls for the two new histograms in the same place the request is already timed.

---

### `gateway/internal/obs/sentry.go` (EXTEND — `BeforeSend` body scrub)

**Analog:** the existing `BeforeSend` hook in the same file (lines 28-39).

**Current** (headers + cookies only):
```go
BeforeSend: func(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
	if event.Request != nil {
		for k := range event.Request.Headers {
			if httpx.IsSensitiveKey(k) {
				event.Request.Headers[k] = "***REDACTED***"
			}
		}
		event.Request.Cookies = ""
	}
	return event
},
```
**Phase 7 extension** (RESEARCH.md lines 449-468) — also scrub `event.Request.Data` (request body) and `event.Extra`:
```go
if event.Request.Data != "" {
	event.Request.Data = "***REDACTED***"
}
delete(event.Extra, "request_body")
delete(event.Extra, "response_body")
```
Reuse the existing `httpx.IsSensitiveKey` helper — already imported in this file.

---

### `gateway/internal/audit/writer.go` (EXTEND — `EventKind` field + state-change helper)

**Analog:** the existing `Writer` in the same file.

**Add an `EventKind` field to the `Event` struct** (lines 30-69) following the additive-extension precedent already in the file — see lines 56-68 where Phase 4 fields were added with the explicit comment "additive; existing Phase 2/3 callers continue to compile with zero-value defaults".

**Add a `WriteStateChange()` helper** that constructs an `Event{EventKind: "fsm_transition" | "tenant_activate" | "pod_lifecycle" | "threshold_change", ...}` and calls the existing non-blocking `Enqueue` (lines 113-133). Do NOT add a second writer or a second channel — the existing async batch writer (lines 141-182) and `dbFlusher` (lines 193-248) handle the write. The `dbFlusher.Flush` `CopyFrom` column list (lines 214-224) must gain the new `event_kind` column.

---

### `gateway/db/migrations/0020_audit_log_event_kind.sql` (migration)

**Analog:** `gateway/db/migrations/0019_emergency_lifecycles.sql` (goose up/down DDL structure) and `0018_audit_log_shed_values.sql` (the docs-style migration).

**Goose file structure** (from `0019`):
```sql
-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- Phase 7 — audit_log.event_kind for state-change rows (OBS-07).
ALTER TABLE ai_gateway.audit_log ADD COLUMN IF NOT EXISTS event_kind TEXT;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE ai_gateway.audit_log DROP COLUMN IF EXISTS event_kind;
-- +goose StatementEnd
```
Nullable column, no backfill (existing rows get `NULL` — RESEARCH.md line 351). Migration number `0020` — `0019` is the highest existing. **Planner flag:** RESEARCH.md Pitfall 8 — `audit_log` is month-partitioned and the partition-roll automation was deferred; confirm the partition window covers the Phase 7 test/ship horizon.

---

### `gateway/db/queries/audit.sql` (EXTEND — read query for `/admin/audit`)

**Analog:** existing `gateway/db/queries/audit.sql` (the `-- name: ... :exec` annotation style) + `gateway/db/queries/emergency_lifecycles.sql` for a `:many` paginated read.

Add a `-- name: ListAuditStateChanges :many` query — paginated `SELECT` ordered by `ts DESC` with `LIMIT`/`OFFSET`. sqlc regenerates `gateway/internal/db/gen` — the `metrics.go`/`audit.go` handlers consume the generated method via their query-interface (the `usageQueries`-style isolation pattern).

---

### `gateway/cmd/gateway/main.go` (EXTEND — composition root wiring)

**Analog:** the existing wiring in the same file.

**Goroutine spawn** — follow the existing pattern (`main.go:278` `go breakerSet.Subscribe(ctx)`, `main.go:682` `go emergReconciler.Run(ctx)`). Spawn `go alerter.Run(ctx)` EARLY (before breaker/shed/emerg subsystems publish — RESEARCH.md Pitfall 4).

**Admin route registration** — extend the `if px.adminVerifier != nil` block (`main.go:946-953`):
```go
adminRouter := chi.NewRouter()
adminRouter.Use(admin.Middleware(px.adminVerifier, log))
adminRouter.Method(http.MethodGet, "/usage", px.adminUsageHandler)
adminRouter.Method(http.MethodGet, "/metrics", px.adminMetricsHandler)  // NEW
adminRouter.Method(http.MethodGet, "/audit", px.adminAuditHandler)      // NEW
r.Mount("/admin", adminRouter)
```
Add `adminMetricsHandler` / `adminAuditHandler` fields to the proxy struct next to `adminUsageHandler` (`main.go:88`), construct them next to `adminUsageHandler := admin.NewUsageHandler(...)` (`main.go:748`). Note `/metrics` (Prometheus, unauthenticated) at `main.go:842` is distinct from `/admin/metrics` (JSON, admin-key-gated) — do not confuse the two.

## Shared Patterns

### External HTTP client resilience (Chatwoot, ClickUp, Brevo)
**Source:** `gateway/internal/emerg/vast/client.go` (client struct/ctor/auth-isolation/error-parsing) + `gateway/internal/proxy/retry.go` (backoff) + `gateway/internal/breaker/breaker.go` (gobreaker)
**Apply to:** all three `gateway/internal/alert/{chatwoot,clickup,brevo}.go` files
- Fixed package-level timeout const, never a magic number (`vast/client.go:79`)
- API token isolated in one greppable `setAuthHeader`-style method (`vast/client.go:327-333`)
- Errors never carry the URL or any header; bounded `io.LimitReader` body reads (`vast/client.go:343-375`)
- `cenkalti/backoff/v5` for retry with `backoff.Permanent` for non-retryable 4xx (`proxy/retry.go:49-83`)
- `sony/gobreaker/v2` — one breaker per external service (`breaker/breaker.go:131-156`)
- Both libs are already in `go.mod` — RESEARCH.md is explicit: do NOT add new HTTP-client libraries.

### Pub/Sub consume loop
**Source:** `gateway/internal/breaker/subscribe.go` (lines 19-56) — documented in-repo as the canonical pattern
**Apply to:** `gateway/internal/alert/alerter.go`
- `for { ... }` outer reconnect loop, `redisx.Subscribe*` helper, `ps.Channel()`, inner `select` on `ctx.Done()` + `msg`
- Malformed JSON → `log.Warn` + `continue`, NEVER crash the loop
- Channel drop → `ps.Close()` + 1s `time.After` backoff + reconnect
- `ctx` cancel exits cleanly

### Admin HTTP handler
**Source:** `gateway/internal/admin/usage.go`
**Apply to:** `gateway/internal/admin/metrics.go`, `gateway/internal/admin/audit.go`
- Struct with injected query-interface (not concrete `*gen.Queries`) + `*slog.Logger`
- Dual constructor: public `NewXHandler(*gen.Queries, *slog.Logger)` + private `newXHandlerWithQueries(iface, logger)` for tests
- `ServeHTTP` validates query params first; every error branch = `httpx.WriteOpenAIError(...)` + `obs.GatewayAdminRequests.WithLabelValues("/admin/<route>", "<class>").Inc()`
- Success = set `Content-Type: application/json`, `WriteHeader(200)`, `json.NewEncoder(w).Encode(resp)`, `... "2xx").Inc()`
- Typed response structs with `json:` tags

### Redis helper module
**Source:** `gateway/internal/redisx/emerg.go`
**Apply to:** `gateway/internal/alert/dedup.go` (and `gateway/internal/redisx/alert.go` if channel consts are split out)
- File-header doc explaining the key namespace + mirror philosophy
- Channel/key names as a const block, exposed via getter functions
- Helpers take `ctx` + `*redis.Client`, wrap in the shared 2s `redisOpTimeout`, return error on nil client (fail-loud wiring bug)

### Additive struct extension
**Source:** `gateway/internal/audit/writer.go` lines 56-68 (Phase 4 added fields with a "zero-value defaults keep old callers compiling" comment)
**Apply to:** `gateway/internal/audit/writer.go` `EventKind` field, `gateway/internal/config/config.go` new alert env vars
- New fields are additive, documented, zero-value-safe
- New optional env vars: empty = feature disabled with a `WARN` log, never fail-boot (config pattern at `config.go:84` `SentryDSN` — "optional; empty = Sentry disabled")

### Goose migration
**Source:** `gateway/db/migrations/0019_emergency_lifecycles.sql`
**Apply to:** `gateway/db/migrations/0020_audit_log_event_kind.sql`
- `-- +goose Up` / `-- +goose StatementBegin` ... `-- +goose StatementEnd`, matching `Down` block
- `SET search_path = ai_gateway, public;`
- `IF NOT EXISTS` / `IF EXISTS` guards; sequential migration number

## No Analog Found

| File | Role | Data Flow | Reason |
|------|------|-----------|--------|
| `dashboard/src/lib/auth.ts` | config (auth) | — | Better Auth has no Go-side analog in `gpu-ifix`; pattern is converseai-v4 (separate repo) + RESEARCH.md code example lines 471-487 |
| `dashboard/src/app/api/auth/[...all]/route.ts` | route | request-response | No Next.js code in `gpu-ifix`; RESEARCH.md lines 488-493 |
| `dashboard/src/app/api/gateway/[...path]/route.ts` | route (server proxy) | request-response | Greenfield; the contract is "X-Admin-Key never reaches the browser" (RESEARCH.md Anti-Patterns, lines 323) |
| `dashboard/src/middleware.ts` | middleware | request-response | No Next.js middleware in `gpu-ifix`; RESEARCH.md lines 494-505 |
| `dashboard/src/components/*` | component | — | No React in `gpu-ifix`; follow `07-UI-SPEC.md` (component inventory, color, typography) + converseai-v4 shadcn components |
| `dashboard/components.json`, `globals.css`, `Dockerfile`, `.github/workflows/build-dashboard.yml` | config / build | — | Greenfield; reference converseai-v4 `apps/web/` verbatim + existing `gateway/docker-compose.yml` + `.github/workflows/build-gateway.yml` for the CI/compose shape |

> The entire `dashboard/` app is greenfield. The planner should source dashboard patterns from `07-UI-SPEC.md`, `07-RESEARCH.md` (verbatim code examples in §"Better Auth standalone instance" and §"Code Examples"), and the converseai-v4 repo at `/home/pedro/projetos/pedro/converseai-v4/apps/web/` — NOT from `gpu-ifix`.

## Brevo SMTP — partial analog only

`gateway/internal/alert/brevo.go` is the one Go file without a strong analog: `gpu-ifix` has no SMTP client. Use Go stdlib `net/smtp` per RESEARCH.md Pattern 5. The only transferable in-repo pattern is the `vast/client.go` client-struct + constructor + config-injection shape. RESEARCH.md is the authority for the `net/smtp.SendMail` call shape.

## Metadata

**Analog search scope:** `gateway/internal/{admin,obs,audit,breaker,emerg,redisx,shed,proxy,config}/`, `gateway/cmd/gateway/`, `gateway/db/{migrations,queries}/`
**Files scanned:** ~25 Go files read in full or targeted; directory listings of 12 packages
**Go libs confirmed in `go.mod` (no new deps for the gateway):** `cenkalti/backoff/v5`, `sony/gobreaker/v2`, `redis/go-redis/v9`, `prometheus/client_golang`, `getsentry/sentry-go` — all already used in-repo by Phases 2-6
**Pattern extraction date:** 2026-05-14
