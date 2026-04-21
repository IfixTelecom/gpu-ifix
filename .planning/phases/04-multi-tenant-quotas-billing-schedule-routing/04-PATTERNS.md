# Phase 4: Multi-tenant Quotas, Billing & Schedule Routing - Pattern Map

**Mapped:** 2026-04-20
**Files analyzed:** 36 (new + modified)
**Analogs found:** 36 / 36 (100% — Phase 2/3 cover every role; only `quota/scripts/token_bucket.lua` has no in-repo analog because Lua is a new file type)

## File Classification

### New Go packages (under `gateway/internal/`)

| New File | Role | Data Flow | Closest Analog | Match Quality |
|----------|------|-----------|----------------|---------------|
| `gateway/internal/quota/lua.go` | utility (Redis script wrapper) | request-response | `gateway/internal/redisx/client.go` (Redis client wiring) | role-match (no Lua precedent) |
| `gateway/internal/quota/scripts/token_bucket.lua` | utility (Lua source) | request-response | n/a (no Lua in repo) | none — use Stripe canonical (RESEARCH §Pattern 1) |
| `gateway/internal/quota/bucket.go` | model (config struct) | request-response | `gateway/internal/upstreams/types.go` (UpstreamConfig + parse helpers) | exact |
| `gateway/internal/quota/enforcer.go` | middleware (chi handler) | request-response | `gateway/internal/auth/apikey.go` (Verify + Middleware) + `gateway/internal/idempotency/middleware.go` (early-exit + envelope) | exact |
| `gateway/internal/quota/counters.go` | service (Postgres lookup + UPSERT helpers) | CRUD | `gateway/internal/audit/writer.go` `dbFlusher.Flush` (txn + sqlc Queries) | exact |
| `gateway/internal/quota/errors.go` | utility (sentinel errors) | n/a | `gateway/internal/breaker/errors.go` + `gateway/internal/idempotency/errors.go` | exact |
| `gateway/internal/billing/accountant.go` | service (in-process counter + SSE on-emission extractor) | streaming | `gateway/internal/proxy/toolcall.go` `toolCallTee` + `toolCallFlags` (atomic.Pointer copy-on-write per request) | exact |
| `gateway/internal/billing/prices.go` | model (in-memory snapshot + USD→BRL pure func) | request-response | `gateway/internal/upstreams/loader.go` `snapshot` (atomic.Pointer + Get/Resolve) | exact |
| `gateway/internal/billing/prices_loader.go` | service (Postgres select + atomic-swap) | CRUD | `gateway/internal/upstreams/loader.go` `Loader.Refresh` | exact |
| `gateway/internal/billing/fx_loader.go` | service (Postgres select + atomic-swap) | CRUD | `gateway/internal/upstreams/loader.go` `Loader.Refresh` | exact |
| `gateway/internal/billing/listen.go` | service (LISTEN/NOTIFY for prices_changed + fx_changed) | event-driven | `gateway/internal/upstreams/listen.go` `ListenAndReload` | exact |
| `gateway/internal/billing/flusher.go` | service (async batched DB writer) | batch | `gateway/internal/audit/writer.go` `Writer` + `Run` + `dbFlusher.Flush` | exact |
| `gateway/internal/billing/events.go` | service (sqlc UPSERT helpers for billing_events) | CRUD | `gateway/internal/audit/writer.go` `dbFlusher.Flush` lines 180-235 (CopyFrom + per-row UPSERT in same txn) | exact |
| `gateway/internal/billing/usage_counters.go` | service (sqlc UPSERT helpers for usage_counters) | CRUD | `gateway/internal/audit/writer.go` `dbFlusher.Flush` (per-row insert in same txn) | exact |
| `gateway/internal/billing/errors.go` | utility (sentinel errors) | n/a | `gateway/internal/breaker/errors.go` | exact |
| `gateway/internal/tenants/loader.go` | service (Postgres select + atomic-swap) | CRUD | `gateway/internal/upstreams/loader.go` (full file) | exact |
| `gateway/internal/tenants/listen.go` | service (LISTEN/NOTIFY for tenants_changed) | event-driven | `gateway/internal/upstreams/listen.go` | exact |
| `gateway/internal/tenants/config.go` | model (TenantConfig struct) | n/a | `gateway/internal/upstreams/types.go` `UpstreamConfig` | exact |
| `gateway/internal/tenants/errors.go` | utility (sentinel errors) | n/a | `gateway/internal/upstreams/errors.go` | exact |
| `gateway/internal/schedule/policy.go` | service (decide upstream tier pre-dispatch) | request-response | `gateway/internal/proxy/dispatcher.go` lines 105-120 (resolve tier-0 + state check) | role-match (decision-only, no I/O) |
| `gateway/internal/schedule/window.go` | utility (time-of-day helper) | n/a | n/a (pure func) | none — see RESEARCH §Pattern 5 |
| `gateway/internal/schedule/middleware.go` | middleware (chi handler injecting upstream override) | request-response | `gateway/internal/auth/apikey.go` `Middleware` (ctx propagation) | exact |
| `gateway/internal/schedule/errors.go` | utility (sentinel errors) | n/a | `gateway/internal/breaker/errors.go` | exact |
| `gateway/internal/admin/middleware.go` | middleware (X-Admin-Key bcrypt + Redis cache) | request-response | `gateway/internal/auth/apikey.go` (full Verifier+Middleware) + `gateway/internal/auth/cache.go` (Redis cache pattern) | exact |
| `gateway/internal/admin/usage.go` | controller (GET /admin/usage handler) | request-response | `gateway/internal/proxy/dispatcher.go` `NewDispatcher` http.HandlerFunc shape; envelope errors via `httpx.WriteOpenAIError` | role-match (handler shape + error envelope) |
| `gateway/internal/admin/errors.go` | utility (sentinel errors) | n/a | `gateway/internal/auth/errors.go` | exact |
| `gateway/internal/proxy/interceptor_usage.go` | service (SSE delta.usage extractor — extends Phase 3) | streaming | `gateway/internal/proxy/toolcall.go` `ToolCallInterceptor` + `toolCallTee.Read` | exact |

### New SQL artifacts

| New File | Role | Data Flow | Closest Analog | Match Quality |
|----------|------|-----------|----------------|---------------|
| `gateway/db/migrations/0010_create_billing_events.sql` | migration | n/a | `gateway/db/migrations/0003_create_audit_log_partitioned.sql` (PARTITION BY RANGE + seed 3 partitions DO-block) | exact |
| `gateway/db/migrations/0011_evolve_usage_counters.sql` | migration (ALTER) | n/a | n/a (first ALTER in repo) — follow goose `+goose Up/Down` shape from `0001_create_tenants.sql` | role-match |
| `gateway/db/migrations/0012_create_prices_and_fx.sql` | migration | n/a | `gateway/db/migrations/0007_create_upstreams.sql` + `0009_upstreams_notify_trigger.sql` (table + trigger) | exact |
| `gateway/db/migrations/0013_evolve_tenants_schedule_quota.sql` | migration (ALTER + CHECK + trigger) | n/a | `gateway/db/migrations/0009_upstreams_notify_trigger.sql` (NOTIFY trigger pattern) | exact |
| `gateway/db/migrations/0014_create_admin_keys.sql` | migration | n/a | `gateway/db/migrations/0007_create_upstreams.sql` (table shape) + `0001_create_tenants.sql` (simple table) | exact |
| `gateway/db/migrations/0015_seed_prices_and_quotas.sql` | migration (seed) | n/a | `gateway/db/migrations/0008_seed_upstreams.sql` (INSERT ... ON CONFLICT DO NOTHING) | exact |
| `gateway/db/queries/billing.sql` | sqlc queries | CRUD | `gateway/db/queries/upstreams.sql` (named queries with comments) | exact |
| `gateway/db/queries/usage_counters.sql` | sqlc queries | CRUD | `gateway/db/queries/upstreams.sql` (UpdateUpstreamAdmin sqlc.narg pattern) | exact |
| `gateway/db/queries/prices.sql` | sqlc queries | CRUD | `gateway/db/queries/upstreams.sql` | exact |
| `gateway/db/queries/fx_rates.sql` | sqlc queries | CRUD | `gateway/db/queries/upstreams.sql` | exact |
| `gateway/db/queries/tenants_admin.sql` | sqlc queries | CRUD | `gateway/db/queries/upstreams.sql` (UpdateUpstreamAdmin) + `admin.sql` (CreateTenant) | exact |
| `gateway/db/queries/admin_keys.sql` | sqlc queries | CRUD | `gateway/db/queries/auth.sql` (GetActiveKeyByLookupHash hot-path pattern) + `admin.sql` (RevokeAPIKey) | exact |

### New gatewayctl subcommands

| New File | Role | Data Flow | Closest Analog | Match Quality |
|----------|------|-----------|----------------|---------------|
| `gateway/cmd/gatewayctl/tenant.go` (extend with set-mode, set-quota) | controller (CLI subcommand) | CRUD | `gateway/cmd/gatewayctl/upstreams.go` `runUpstreamsUpdate` (flag.NewFlagSet + sqlc.narg + NOTIFY-triggering UPDATE) | exact |
| `gateway/cmd/gatewayctl/prices.go` (new) | controller (CLI subcommand) | CRUD | `gateway/cmd/gatewayctl/upstreams.go` (full file: list/update/enable/disable dispatch) | exact |
| `gateway/cmd/gatewayctl/billing.go` (new — reconcile, usage report) | controller (CLI subcommand) | CRUD + reporting | `gateway/cmd/gatewayctl/upstreams.go` `runUpstreamsList` (tabwriter) | exact |
| `gateway/cmd/gatewayctl/admin-key.go` (new — create, revoke, list) | controller (CLI subcommand) | CRUD | `gateway/cmd/gatewayctl/key.go` `runKeyCreate` + `runKeyRevoke` (generate + hash + insert + print-once) | exact |

### Modified files

| File | Modification | Closest Analog | Match Quality |
|------|--------------|----------------|---------------|
| `gateway/internal/config/config.go` | add `WriteTimeoutChat/Embed/Audio` enforcement (already declared lines 124-126), `AdminKeyBootstrap`, `QuotaFailOpen`, `RateLimitFailOpen`, `USDBRLDefault` env vars | self-analog (existing Config struct + `envOr/atoiOr/boolOr` helpers) | exact |
| `gateway/cmd/gateway/main.go` | wire new middleware chain (rate-limit → quota → schedule), billing flusher goroutine, listen for prices/tenants/admin_keys, mount `/admin/*` router | self-analog (existing wiring lines 226-256 for upstreams loader + listen + breaker; lines 162-171 for audit writer goroutine) | exact |
| `gateway/cmd/gatewayctl/main.go` | dispatch new subcommands `prices`, `billing`, `admin-key` | self-analog (existing switch lines 48-57) | exact |
| `gateway/internal/obs/metrics.go` | add 9 new collectors (rate_limit_rejected_total, quota_rejected_total, etc.) | self-analog (existing `promauto.NewCounterVec` lines 17-23, 65-71, 107-113) | exact |
| `pkg/openai/types.go` | add `RateLimitErrorCode`, `InsufficientQuotaErrorCode`, `RateLimitErrorType`, `InsufficientQuotaErrorType` constants | self-analog (file currently has only types; introduce const block) | role-match |
| `gateway/internal/audit/writer.go` Event struct | add fields for `AudioSeconds`, `EmbedsCount`, `Cost*BRL` (already have TokensIn/Out lines 41-42, AudioDurationS line 54) | self-analog | exact |

### Integration tests (new)

| New File | Role | Data Flow | Closest Analog | Match Quality |
|----------|------|-----------|----------------|---------------|
| `gateway/internal/integration_test/quota_atomic_test.go` (SC-5: 1000 goroutines) | test | request-response | `gateway/internal/integration_test/concurrent_idempotency_test.go` (concurrent goroutine harness) | exact |
| `gateway/internal/integration_test/quota_rollover_test.go` (00:00 BRT boundary) | test | n/a | `gateway/internal/integration_test/partition_automation_test.go` (date-boundary harness) | role-match |
| `gateway/internal/integration_test/sensitive_peak_reject_test.go` (3-path defense) | test | n/a | `gateway/internal/integration_test/sensitive_block_test.go` | exact |
| `gateway/internal/integration_test/billing_partial_test.go` (abnormal close → source='partial') | test | streaming | `gateway/internal/integration_test/tool_call_partial_test.go` (SSE abnormal close harness) | exact |
| `gateway/internal/integration_test/billing_reconcile_test.go` | test | CRUD | `gateway/internal/integration_test/audit_write_test.go` | exact |
| `gateway/internal/integration_test/prices_hot_reload_test.go` + `tenants_hot_reload_test.go` | test | event-driven | `gateway/internal/integration_test/upstreams_listen_test.go` (NOTIFY → loader.Refresh roundtrip) | exact |

---

## Pattern Assignments

### `gateway/internal/quota/enforcer.go` (middleware, request-response)

**Analog:** `gateway/internal/auth/apikey.go` (Verifier + Middleware) and `gateway/internal/idempotency/middleware.go` (early-exit envelope).

**Imports pattern** (auth/apikey.go lines 1-21):
```go
package quota

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auditctx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)
```

**Module logger pattern** (auth/apikey.go line 57):
```go
log: log.With("module", "QUOTA"),  // UPPER_SNAKE_CASE per CLAUDE.md
```

**chi-compatible middleware shape** (auth/apikey.go lines 199-239 — copy this shape exactly):
```go
func Middleware(enf *Enforcer, log *slog.Logger) func(http.Handler) http.Handler {
	if log == nil {
		log = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ac, ok := auth.FromContext(r.Context())
			if !ok {
				httpx.WriteOpenAIError(w, http.StatusUnauthorized,
					"authentication_error", "no_api_key",
					"Authenticated tenant required.")
				return
			}
			// ... rate-limit check via Lua, quota check via Postgres ...
			if err := enf.Check(r.Context(), ac.TenantID, routeClass(r.URL.Path)); err != nil {
				switch {
				case errors.Is(err, ErrRateLimitRPS):
					w.Header().Set("Retry-After", "1")
					httpx.WriteOpenAIError(w, http.StatusTooManyRequests,
						"rate_limit_error", "rate_limit_rps",
						"Rate limit exceeded: requests per second.")
					return
				case errors.Is(err, ErrQuotaExceededDailyTokens):
					httpx.WriteOpenAIError(w, http.StatusTooManyRequests,
						"insufficient_quota", "quota_exceeded_daily_tokens",
						"Daily token quota exceeded.")
					return
				}
				// ... other discriminated codes per D-A4 ...
			}
			next.ServeHTTP(w, r)
		})
	}
}
```

**Envelope error pattern** (httpx/envelope.go lines 14-22 — already centralized; just call it):
```go
httpx.WriteOpenAIError(w, status, errType, code, msg)
```

**Sentinel error pattern** (idempotency/errors.go lines 10-20 — exact shape):
```go
var (
	ErrRateLimitRPS              = errors.New("quota: rate limit exceeded (RPS)")
	ErrRateLimitRPM              = errors.New("quota: rate limit exceeded (RPM)")
	ErrQuotaExceededDailyTokens  = errors.New("quota: daily token quota exceeded")
	ErrQuotaExceededMonthlyTokens = errors.New("quota: monthly token quota exceeded")
	ErrQuotaCheckUnavailable     = errors.New("quota: check unavailable (fail-closed)")
	// ... all 6 quota dimensions × {daily, monthly} per D-A4
)
```

---

### `gateway/internal/quota/lua.go` (utility, request-response)

**Analog:** `gateway/internal/redisx/client.go` (Redis wiring) + `gateway/internal/proxy/tokencount.go` lines 86-105 (Redis Get/Set pattern).

**Embed Lua via go:embed** (no in-repo precedent for `//go:embed`; the structure is standard Go):
```go
package quota

import (
	_ "embed"
	"context"
	"github.com/redis/go-redis/v9"
)

//go:embed scripts/token_bucket.lua
var tokenBucketSrc string

var tokenBucketScript = redis.NewScript(tokenBucketSrc)

// Run executes the bucket script. redis.NewScript caches SHA in the *Script
// struct and uses EVALSHA → EVAL fallback transparently — no manual SCRIPT LOAD.
// (Redis go-redis README; RESEARCH §Pattern 1.)
func Run(ctx context.Context, rdb *redis.Client, keys []string, args ...any) ([]any, error) {
	res, err := tokenBucketScript.Run(ctx, rdb, keys, args...).Result()
	if err != nil {
		return nil, err
	}
	out, _ := res.([]any)
	return out, nil
}
```

**Cache key namespacing** (auth/cache.go lines 39-45 + tokencount.go line 80 — `gw:` prefix):
```go
// gw:rate:{tenant_id}:{route_class}:{rps|rpm}:{tokens|ts}
func bucketKey(tenantID, routeClass, window, suffix string) string {
	return "gw:rate:" + tenantID + ":" + routeClass + ":" + window + ":" + suffix
}
```

---

### `gateway/internal/billing/flusher.go` (service, batch)

**Analog:** `gateway/internal/audit/writer.go` (full file — copy structure, rename types).

**Async writer skeleton** (audit/writer.go lines 22-27, 78-84, 102-120, 128-169 — exact shape):
```go
const (
	bufferSize     = 1000
	flushBatchSize = 500
	flushInterval  = 1 * time.Second
)

type Event struct {
	TS                  time.Time
	RequestID           uuid.UUID
	TenantID            uuid.UUID
	APIKeyID            uuid.UUID
	Route               string
	Upstream            string
	Model               string
	TokensIn            int
	TokensOut           int
	AudioSeconds        float64
	EmbedsCount         int
	CostLocalBRL        float64
	CostLocalPhantomBRL float64
	CostExternalBRL     float64
	Source              string // "final" | "partial"
}

type Flusher struct {
	ch      chan Event
	fl      flusher
	log     *slog.Logger
	dropped atomic.Uint64
}

func NewFlusher(pool *pgxpool.Pool, log *slog.Logger) *Flusher {
	return &Flusher{
		ch:  make(chan Event, bufferSize),
		fl:  &dbFlusher{pool: pool, q: gen.New(pool)},
		log: log.With("module", "BILLING"),
	}
}

// Enqueue is the hot-path API. NEVER blocks (audit/writer.go lines 102-120).
func (f *Flusher) Enqueue(e Event) {
	select {
	case f.ch <- e:
	default:
		f.dropped.Add(1)
		obs.BillingFlushDroppedTotal.Inc()
	}
}

// Run drains until ctx cancel; same pattern as audit/writer.go lines 128-169.
func (f *Flusher) Run(ctx context.Context) { /* identical */ }
```

**Atomic txn pattern** (audit/writer.go lines 180-235): INSERT billing_events ON CONFLICT DO NOTHING + UPSERT usage_counters in the same `pool.BeginTx → defer Rollback → Commit` transaction.

---

### `gateway/internal/billing/accountant.go` (service, streaming)

**Analog:** `gateway/internal/proxy/toolcall.go` (full file — `ToolCallInterceptor` + `toolCallFlags` + `toolCallTee.Read` + flag-per-request indexed by `httpx.RequestIDFrom`).

**Per-request atomic counter, copy-on-write map** (toolcall.go lines 35-58, 95-126):
```go
type UsageInterceptor struct {
	usages *usageMap // copy-on-write atomic.Pointer
}

type requestUsage struct {
	tokensIn     atomic.Int64
	tokensOut    atomic.Int64
	audioSeconds atomic.Int64 // millis × 10 to avoid float
	embedsCount  atomic.Int64
}

type usageMap struct {
	mu sync.Mutex
	m  atomic.Pointer[map[string]*requestUsage]
}

func (u *UsageInterceptor) Intercept(resp *http.Response) error {
	if !proxy.IsSSEResponse(resp) {
		// Non-streaming: extract usage from response.usage on Close
		// via a TeeBody wrapper that JSON-decodes the buffered body.
		return nil
	}
	reqID := httpx.RequestIDFrom(resp.Request.Context())
	if reqID == "" {
		return nil
	}
	usage := &requestUsage{}
	u.set(reqID, usage)
	resp.Body = newUsageTee(resp.Body, usage) // parses SSE deltas for "usage"
	return nil
}
```

**SSE tee shape** (toolcall.go lines 132-161 — copy `toolCallTee`, replace substring scan with NDJSON parser for `data:` lines containing `"usage":{...}`):
```go
type usageTee struct {
	upstream io.ReadCloser
	usage    *requestUsage
	buf      bytes.Buffer
}

func (u *usageTee) Read(p []byte) (int, error) {
	n, err := u.upstream.Read(p)
	if n > 0 {
		u.buf.Write(p[:n])
		// Scan complete SSE events (terminated by \n\n); parse JSON;
		// if top-level "usage" present, atomic-store onto u.usage.
		// MUST tolerate BOTH OpenAI (separate final chunk) AND llama.cpp
		// (same chunk as finish_reason=stop) per RESEARCH §Pattern 3.
	}
	return n, err
}
```

**ProxyResponseInterceptor compliance** (proxy/interceptor.go lines 17-19):
```go
var _ proxy.ProxyResponseInterceptor = (*UsageInterceptor)(nil)
```

---

### `gateway/internal/billing/prices_loader.go` + `tenants/loader.go` (service, CRUD)

**Analog:** `gateway/internal/upstreams/loader.go` (full file — copy structure exactly; swap row type + map keys).

**Snapshot + atomic.Pointer + Refresh pattern** (upstreams/loader.go lines 19-128 — exact shape):
```go
package billing

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/jackc/pgx/v5/pgxpool"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

type pricesSnapshot struct {
	byKey map[priceKey]Price // (model, provider, unit) → Price
}

type priceKey struct{ Model, Provider, Unit string }

type PricesLoader struct {
	pool *pgxpool.Pool
	q    pricesQueries
	snap atomic.Pointer[pricesSnapshot]
	log  *slog.Logger
}

func NewPricesLoader(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) (*PricesLoader, error) {
	l := &PricesLoader{
		pool: pool,
		q:    gen.New(pool),
		log:  log.With("module", "PRICES"),
	}
	if err := l.Refresh(ctx); err != nil {
		return nil, fmt.Errorf("initial prices refresh: %w", err)
	}
	return l, nil
}

func (l *PricesLoader) Refresh(ctx context.Context) error {
	rows, err := l.q.ListActivePrices(ctx)
	if err != nil {
		obs.PricesReloadTotal.WithLabelValues("error").Inc()
		return fmt.Errorf("list active prices: %w", err)
	}
	s := &pricesSnapshot{byKey: make(map[priceKey]Price, len(rows))}
	for _, r := range rows {
		s.byKey[priceKey{r.Model, r.Provider, r.Unit}] = Price{
			UnitCostUSD: r.UnitCostUsd,
		}
	}
	l.snap.Store(s)
	obs.PricesReloadTotal.WithLabelValues("ok").Inc()
	l.log.Info("prices refreshed", "rows", len(s.byKey))
	return nil
}

// Get is lock-free (atomic.Pointer read).
func (l *PricesLoader) Get(model, provider, unit string) (Price, bool) {
	s := l.snap.Load()
	if s == nil {
		return Price{}, false
	}
	p, ok := s.byKey[priceKey{model, provider, unit}]
	return p, ok
}
```

`tenants/loader.go` is the same shape with `TenantConfig` replacing `Price`, keyed by `uuid.UUID` and `slug`.

---

### `gateway/internal/billing/listen.go` + `tenants/listen.go` (service, event-driven)

**Analog:** `gateway/internal/upstreams/listen.go` (full file — copy `ListenAndReload` exactly).

**LISTEN/NOTIFY pattern** (upstreams/listen.go lines 34-69 — exact shape):
```go
func ListenAndReload(ctx context.Context, dsn string, loader *PricesLoader, fxLoader *FXLoader, log *slog.Logger) error {
	log = log.With("module", "PRICES_LISTEN")
	listener := &pgxlisten.Listener{
		Connect: func(ctx context.Context) (*pgx.Conn, error) {
			return pgx.Connect(ctx, dsn)
		},
		LogError: func(_ context.Context, err error) {
			log.Warn("pgxlisten error", "err", err)
		},
		ReconnectDelay: 5 * time.Second,
	}
	listener.Handle("prices_changed", pgxlisten.HandlerFunc(
		func(ctx context.Context, n *pgconn.Notification, _ *pgx.Conn) error {
			log.Info("prices_changed NOTIFY received", "payload", n.Payload)
			if err := loader.Refresh(ctx); err != nil {
				log.Error("prices refresh after NOTIFY failed", "err", err)
				return nil // keep listener alive (upstreams/listen.go line 50-55)
			}
			if err := fxLoader.Refresh(ctx); err != nil {
				log.Error("fx refresh after NOTIFY failed", "err", err)
			}
			return nil
		},
	))
	err := listener.Listen(ctx)
	if err != nil && ctx.Err() == nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return ctx.Err()
}
```

**NOTE per RESEARCH §Pattern 4:** A single `pgxlisten.Listener` can multiplex N channels via repeated `listener.Handle(...)` calls. Phase 4 has 3 new channels (`tenants_changed`, `prices_changed`, `admin_keys_changed`). RESEARCH recommends keeping 3 separate `*pgxlisten.Listener` instances (1 per loader package) for isolation; alternative is consolidating to one shared listener — planner decides during execute. Either way, the per-channel handler shape above is identical.

---

### `gateway/internal/billing/events.go` + `usage_counters.go` (service, CRUD)

**Analog:** `gateway/internal/audit/writer.go` `dbFlusher.Flush` lines 180-235 (single txn with multi-table writes).

**Same-txn UPSERT pattern** (audit/writer.go lines 180-234 — adapt for billing_events + usage_counters):
```go
func (d *dbFlusher) Flush(ctx context.Context, batch []Event) error {
	tx, err := d.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := gen.New(tx)
	for _, e := range batch {
		// 1. INSERT billing_events ON CONFLICT (request_id, ts) DO NOTHING
		if err := q.InsertBillingEvent(ctx, gen.InsertBillingEventParams{
			RequestID: e.RequestID,
			Ts:        e.TS,
			TenantID:  e.TenantID,
			Route:     e.Route,
			Upstream:  e.Upstream,
			TokensIn:  int32(e.TokensIn),
			TokensOut: int32(e.TokensOut),
			// ... all cost columns ...
			Source: e.Source,
		}); err != nil {
			return err
		}
		// 2. UPSERT usage_counters in the SAME txn — atomic accumulator
		if err := q.UpsertUsageCounter(ctx, gen.UpsertUsageCounterParams{
			TenantID:  e.TenantID,
			Date:      e.TS, // Postgres casts TIMESTAMPTZ to DATE on insert via (now() AT TIME ZONE 'America/Sao_Paulo')::date
			TokensIn:  int64(e.TokensIn),
			TokensOut: int64(e.TokensOut),
			// ... all dimensions ...
		}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
```

---

### `gateway/internal/admin/middleware.go` (middleware, request-response)

**Analog:** `gateway/internal/auth/apikey.go` `Verifier` + `Verify` (lines 117-178) and `gateway/internal/auth/cache.go` (full file — Redis cache structure).

**Cache shape** (auth/cache.go lines 31-37, 55-82 — copy exactly, swap argon2 for bcrypt):
```go
const adminCacheTTL = 60 * time.Second

type adminCacheEntry struct {
	AdminKeyID string `json:"admin_key_id"`
	Status     string `json:"status"` // "active" | "revoked"
	Label      string `json:"label"`
	KeyPrefix  string `json:"key_prefix"`
}

func adminCacheKeyFor(rawKey string) string {
	sum := sha256.Sum256([]byte(rawKey))
	return "gw:admin:" + hex.EncodeToString(sum[:])
}
```

**Hot-path verify with cache** (auth/apikey.go lines 117-178 — copy verbatim, replace `argon2id.ComparePasswordAndHash` with `bcrypt.CompareHashAndPassword`):
```go
func (v *AdminVerifier) Verify(ctx context.Context, rawKey string) (AdminContext, error) {
	if rawKey == "" {
		return AdminContext{}, ErrMissingAdminKey
	}
	// 1. Positive cache fast path
	if hit, found, err := v.cacheGet(ctx, rawKey); err == nil && found {
		return hitToAdmin(hit)
	}
	// 2. DB lookup by key_lookup_hash (SHA-256 indexed)
	row, err := v.q.GetAdminKeyByLookupHash(ctx, sha256Bytes(rawKey))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AdminContext{}, ErrInvalidAdminKey
		}
		return AdminContext{}, fmt.Errorf("admin: db lookup: %w", err)
	}
	// 3. bcrypt compare (cost 10, ~86ms — RESEARCH §Standard Stack)
	if err := bcrypt.CompareHashAndPassword(row.KeyHash, []byte(rawKey)); err != nil {
		return AdminContext{}, ErrInvalidAdminKey
	}
	_ = v.cachePut(ctx, rawKey, adminCacheEntry{...})
	return hitToAdmin(...)
}
```

**Middleware envelope** (auth/apikey.go lines 199-239 — exact shape with admin error codes).

---

### `gateway/internal/admin/usage.go` (controller, request-response)

**Analog:** `gateway/internal/proxy/dispatcher.go` `NewDispatcher` lines 69-114 (HandlerFunc + auth check + envelope errors).

**Handler skeleton** (dispatcher.go lines 69-79 + envelope.go calls):
```go
func NewUsageHandler(pool *pgxpool.Pool, log *slog.Logger) http.Handler {
	log = log.With("module", "ADMIN")
	q := gen.New(pool)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. Parse query string: ?tenant=&from=&to=&granularity=
		// 2. Resolve tenant slug → uuid via q.GetTenantBySlug
		// 3. Query billing_events directly (NOT usage_counters — D-D2)
		rows, err := q.SumBillingEventsByDate(ctx, ...)
		if err != nil {
			httpx.WriteOpenAIError(w, http.StatusInternalServerError,
				"api_error", "billing_query_failed", "Could not query billing events.")
			return
		}
		// 4. Build response per CONTEXT.md D-D2 shape (lines 166-182)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(usageResponse{...})
	})
}
```

---

### `gateway/internal/schedule/middleware.go` + `policy.go` (middleware + service)

**Analog:** `gateway/internal/proxy/dispatcher.go` lines 105-120 (resolve + state check) + `gateway/internal/auditctx/override.go` (ctx propagation pattern for upstream override).

**Decision-only middleware** (no I/O — just reads loaded TenantConfig, time.Now().In(tz), and writes `ctx.upstream_override`):
```go
func Middleware(loader *tenants.Loader, log *slog.Logger) func(http.Handler) http.Handler {
	log = log.With("module", "SCHEDULE")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ac, _ := auth.FromContext(r.Context())
			cfg, ok := loader.Get(ac.TenantID)
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			ctx := r.Context()
			if cfg.Mode == "peak" && !inWindow(time.Now().In(cfg.TZ), cfg.PeakStart, cfg.PeakEnd) {
				ctx = auditctx.WithUpstreamOverride(ctx, "openrouter-chat")
				obs.ScheduleRoutingTotal.WithLabelValues(cfg.Slug, "off_hours_external").Inc()
			} else {
				obs.ScheduleRoutingTotal.WithLabelValues(cfg.Slug, "local").Inc()
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
```

**Pure window helper** (schedule/window.go — no in-repo analog; trivial):
```go
func inWindow(now time.Time, start, end time.Time) bool {
	nowMin := now.Hour()*60 + now.Minute()
	startMin := start.Hour()*60 + start.Minute()
	endMin := end.Hour()*60 + end.Minute()
	if startMin <= endMin {
		return nowMin >= startMin && nowMin < endMin
	}
	// Wrap-around (e.g. 22:00-06:00)
	return nowMin >= startMin || nowMin < endMin
}
```

---

### Migrations

#### `0010_create_billing_events.sql` (partitioned table)

**Analog:** `gateway/db/migrations/0003_create_audit_log_partitioned.sql` (lines 1-58 — exact shape: PARTITION BY RANGE (ts) + DO-block seeding 3 monthly partitions).

**Header pattern** (0003 lines 1-3):
```sql
-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;
```

**Partitioned table + indexes** (0003 lines 5-35 — adapt for billing_events):
```sql
CREATE TABLE IF NOT EXISTS ai_gateway.billing_events (
    request_id            UUID NOT NULL,
    ts                    TIMESTAMPTZ NOT NULL,
    tenant_id             UUID NOT NULL REFERENCES ai_gateway.tenants(id),
    -- ... all columns per CONTEXT.md D-B1 lines 55-75 ...
    PRIMARY KEY (request_id, ts)
) PARTITION BY RANGE (ts);

CREATE INDEX IF NOT EXISTS idx_billing_events_tenant_ts
    ON ai_gateway.billing_events (tenant_id, ts DESC);
```

**DO-block seed partitions** (0003 lines 40-57 — exact shape):
```sql
DO $$
DECLARE
    m DATE;
    start_m DATE;
    end_m DATE;
    part_name TEXT;
BEGIN
    FOR i IN 0..2 LOOP
        m := DATE_TRUNC('month', CURRENT_DATE) + (i || ' months')::INTERVAL;
        start_m := m;
        end_m := m + INTERVAL '1 month';
        part_name := 'billing_events_' || to_char(start_m, 'YYYYMM');
        EXECUTE format(
            'CREATE TABLE IF NOT EXISTS ai_gateway.%I PARTITION OF ai_gateway.billing_events FOR VALUES FROM (%L) TO (%L)',
            part_name, start_m, end_m
        );
    END LOOP;
END $$;
```

#### `0012_create_prices_and_fx.sql` + `0013_evolve_tenants_schedule_quota.sql` (NOTIFY trigger)

**Analog:** `gateway/db/migrations/0009_upstreams_notify_trigger.sql` lines 5-49 (NOTIFY trigger with WHEN clause + INSERT/DELETE + UPDATE split).

**NOTIFY function** (0009 lines 5-10 — copy and rename):
```sql
CREATE OR REPLACE FUNCTION ai_gateway.notify_prices_changed() RETURNS trigger AS $$
BEGIN
    PERFORM pg_notify('prices_changed', COALESCE(NEW.id::text, OLD.id::text));
    RETURN COALESCE(NEW, OLD);
END;
$$ LANGUAGE plpgsql;
```

**Split INSERT/DELETE vs UPDATE triggers** (0009 lines 22-48 — same pattern; UPDATE WHEN clause filters non-config-column changes per Pitfall 7):
```sql
CREATE TRIGGER prices_insert_delete_notify
AFTER INSERT OR DELETE ON ai_gateway.prices
FOR EACH ROW
WHEN (pg_trigger_depth() = 0)
EXECUTE FUNCTION ai_gateway.notify_prices_changed();

CREATE TRIGGER prices_update_notify
AFTER UPDATE ON ai_gateway.prices
FOR EACH ROW
WHEN (
    pg_trigger_depth() = 0 AND (
        NEW.unit_cost_usd IS DISTINCT FROM OLD.unit_cost_usd
        OR NEW.valid_to IS DISTINCT FROM OLD.valid_to
    )
)
EXECUTE FUNCTION ai_gateway.notify_prices_changed();
```

For `0013_evolve_tenants_schedule_quota.sql`, the `chk_sensitive_no_peak` CHECK constraint is added inline (CONTEXT.md D-C1 lines 117-122) and `tenants_changed` follows the same trigger split.

#### `0011_evolve_usage_counters.sql` (ALTER TABLE)

No prior ALTER migration exists in the repo. Use the standard goose `+goose Up/Down` shape from `0001_create_tenants.sql`:
```sql
-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

ALTER TABLE ai_gateway.usage_counters
    ADD COLUMN IF NOT EXISTS audio_seconds          BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS embeds_count           BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS cost_local_phantom_brl NUMERIC(10,4) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS cost_external_brl      NUMERIC(10,4) NOT NULL DEFAULT 0;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE ai_gateway.usage_counters
    DROP COLUMN IF EXISTS audio_seconds,
    DROP COLUMN IF EXISTS embeds_count,
    DROP COLUMN IF EXISTS cost_local_phantom_brl,
    DROP COLUMN IF EXISTS cost_external_brl;
-- +goose StatementEnd
```

#### `0015_seed_prices_and_quotas.sql`

**Analog:** `gateway/db/migrations/0008_seed_upstreams.sql` (full file — INSERT ... ON CONFLICT DO NOTHING).

```sql
INSERT INTO ai_gateway.prices (model, provider, unit, unit_cost_usd) VALUES
    ('qwen3.5-27b',                'openrouter-fireworks', 'input_token',  0.00000020),
    ('qwen3.5-27b',                'openrouter-fireworks', 'output_token', 0.00000060),
    ('text-embedding-3-small',     'openai',               'input_token',  0.00000002),
    ('whisper-1',                  'openai',               'audio_second', 0.00010000)
ON CONFLICT (model, provider, unit, valid_from) DO NOTHING;
```

**OPERATOR-GATED** per RESEARCH §Summary finding 1 — confirm Fireworks pricing live before applying.

---

### sqlc queries

**Analog:** `gateway/db/queries/upstreams.sql` (full file).

**Query header convention** (upstreams.sql lines 1-4 — comment block + query):
```sql
-- name: ListActivePrices :many
-- Hot-path load at boot and on LISTEN/NOTIFY (D-B3). Returns currently
-- active rows (valid_to IS NULL) for in-memory snapshot.
SELECT id, model, provider, unit, unit_cost_usd, valid_from, valid_to, notes, created_at
FROM ai_gateway.prices
WHERE valid_to IS NULL
ORDER BY model, provider, unit;
```

**sqlc.narg pattern for partial UPDATE** (upstreams.sql lines 47-55 — copy for `UpdateTenantQuota`):
```sql
-- name: UpdateTenantQuota :exec
-- Partial UPDATE — fields passed as NULL via sqlc.narg are left unchanged.
UPDATE ai_gateway.tenants
SET daily_quota_tokens   = COALESCE(sqlc.narg('daily_quota_tokens')::bigint, daily_quota_tokens),
    monthly_quota_tokens = COALESCE(sqlc.narg('monthly_quota_tokens')::bigint, monthly_quota_tokens),
    rps_limit            = COALESCE(sqlc.narg('rps_limit')::int, rps_limit),
    rpm_limit            = COALESCE(sqlc.narg('rpm_limit')::int, rpm_limit),
    -- ... all 6 quota dimensions ...
    updated_at           = NOW()
WHERE slug = sqlc.arg('slug');
```

**Boot-time validation query for sensitive+peak (D-C1 path 3):**
```sql
-- name: ListInvalidSensitivePeak :many
-- Boot-time defensive check: CHECK constraint should prevent this row from
-- existing. If returned, gateway os.Exit(1).
SELECT id, slug FROM ai_gateway.tenants
WHERE mode = 'peak' AND data_class = 'sensitive';
```

**`(now() AT TIME ZONE 'America/Sao_Paulo')::date` correction (RESEARCH finding 3):**
```sql
-- name: GetUsageCountersToday :one
-- Hot-path quota lookup. Uses the CORRECT timezone idiom — DATE has no
-- timezone, so AT TIME ZONE on CURRENT_DATE is invalid SQL.
SELECT tokens_in, tokens_out, audio_seconds, embeds_count
FROM ai_gateway.usage_counters
WHERE tenant_id = $1
  AND date = (now() AT TIME ZONE 'America/Sao_Paulo')::date;
```

---

### gatewayctl subcommands

**Analog:** `gateway/cmd/gatewayctl/upstreams.go` (full file — runX + flag.NewFlagSet + tabwriter + sqlc).

**Subcommand dispatch** (upstreams.go lines 28-46 — copy shape):
```go
func runPrices(ctx context.Context, args []string, log *slog.Logger) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: gatewayctl prices set|list|set-fx [flags]")
		return 2
	}
	switch args[0] {
	case "set":     return runPricesSet(ctx, args[1:], log)
	case "list":    return runPricesList(ctx, args[1:], log)
	case "set-fx":  return runPricesSetFX(ctx, args[1:], log)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", args[0])
		return 2
	}
}
```

**Flag parsing + loadAndPool + sqlc** (upstreams.go lines 100-183 — exact pattern):
```go
func runPricesSet(ctx context.Context, args []string, log *slog.Logger) int {
	fs := flag.NewFlagSet("prices set", flag.ExitOnError)
	model := fs.String("model", "", "model name (required)")
	provider := fs.String("provider", "", "provider (required)")
	unit := fs.String("unit", "", "input_token|output_token|audio_second|embed_request")
	usd := fs.Float64("usd", 0, "unit cost USD (required)")
	if err := fs.Parse(args); err != nil { return 2 }
	if *model == "" || *provider == "" || *unit == "" || *usd <= 0 {
		fs.Usage(); return 2
	}
	_, pool, err := loadAndPool(ctx, log)
	if err != nil { return 1 }
	defer pool.Close()
	q := gen.New(pool)
	// Insert new row with valid_from=now(); previous active row gets valid_to=now()
	// — single txn so the swap is atomic; trigger fires NOTIFY prices_changed.
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil { return 1 }
	defer func() { _ = tx.Rollback(ctx) }()
	if err := gen.New(tx).ExpireActivePrice(ctx, ...); err != nil { return 1 }
	if err := gen.New(tx).InsertPrice(ctx, ...); err != nil { return 1 }
	if err := tx.Commit(ctx); err != nil { return 1 }
	log.Info("price updated", "model", *model, "provider", *provider, "unit", *unit, "usd", *usd)
	return 0
}
```

**Print-once pattern for `gatewayctl admin-key create`** (key.go lines 86-95 — copy exactly; bcrypt instead of argon2):
```go
// IMPORTANT: print the raw key to stdout ONCE. Do NOT log.Info(raw, ...).
fmt.Printf("key=%s\nid=%s\nprefix=%s\nlabel=%s\n", raw, inserted.ID.String(), prefix, *label)
log.Info("admin key issued", "admin_key_id", inserted.ID.String(), "label", *label, "key_prefix", prefix)
```

---

### Wiring in `gateway/cmd/gateway/main.go`

**Analog:** existing main.go lines 226-256 (loader + breaker + listen wiring) and lines 162-171 (audit writer goroutine).

**New wiring (insert after existing loader at line 256):**
```go
// Phase 4 — tenants loader + listener (mirror upstreams pattern)
tenantsLoader, err := tenants.NewLoader(ctx, pool, log)
if err != nil {
	log.Error("tenants loader init failed", "err", err)
	os.Exit(2)
}
go func() {
	if err := tenants.ListenAndReload(ctx, cfg.PGDSN, tenantsLoader, nil, log); err != nil {
		log.Warn("tenants listener exited", "err", err)
	}
}()

// Phase 4 — prices + fx loaders (single shared listener)
pricesLoader, err := billing.NewPricesLoader(ctx, pool, log)
if err != nil { log.Error("prices loader init failed", "err", err); os.Exit(2) }
fxLoader, err := billing.NewFXLoader(ctx, pool, log)
if err != nil { log.Error("fx loader init failed", "err", err); os.Exit(2) }
go func() {
	if err := billing.ListenAndReload(ctx, cfg.PGDSN, pricesLoader, fxLoader, log); err != nil {
		log.Warn("prices listener exited", "err", err)
	}
}()

// Phase 4 — billing flusher (mirror audit writer goroutine pattern, lines 162-171)
billingFlusher := billing.NewFlusher(pool, log)
go billingFlusher.Run(ctx)

// Phase 4 — boot-time fail-fast validation (D-C1 path 3)
if invalid, _ := gen.New(pool).ListInvalidSensitivePeak(ctx); len(invalid) > 0 {
	for _, t := range invalid {
		log.Error("sensitive tenant in peak mode (LGPD invariant breach)", "tenant_id", t.ID, "slug", t.Slug)
	}
	os.Exit(1)
}

// Phase 4 — UsageInterceptor extends Phase 3 interceptor chain
usageInterceptor := proxy.NewUsageInterceptor(billingFlusher, pricesLoader, fxLoader, tenantsLoader, log)
// Pass into NewChatProxy alongside auditInterceptor + toolCallInterceptor (line 190):
chatRP, err := proxy.NewChatProxy(cfg.UpstreamLLMURL, log, auditInterceptor, toolCallInterceptor, usageInterceptor)

// Phase 4 — chi router middleware chain order (D-D1)
r.Use(auth.Middleware(verifier, log))
r.Use(idempotency.Middleware(idemStore, log))
r.Use(quota.Middleware(rateLimitEnforcer, log))      // NEW
r.Use(quota.QuotaMiddleware(quotaEnforcer, log))     // NEW
r.Use(schedule.Middleware(tenantsLoader, log))       // NEW
// dispatcher mounted next (existing)
r.Use(metricsMiddleware(log))                        // NEW (folded TODO — obs.RequestsTotal.Inc)

// Mount /admin/* with separate auth chain
adminRouter := chi.NewRouter()
adminRouter.Use(admin.Middleware(adminVerifier, log))
adminRouter.Get("/usage", admin.NewUsageHandler(pool, log).ServeHTTP)
r.Mount("/admin", adminRouter)
```

---

## Shared Patterns

### Module logger naming

**Source:** CLAUDE.md (project-wide convention) + `gateway/internal/audit/writer.go` line 84 + `gateway/internal/upstreams/loader.go` line 51 + every other package.

**Apply to:** Every new package's logger initialization.

```go
log: log.With("module", "QUOTA"),       // gateway/internal/quota/
log: log.With("module", "BILLING"),     // gateway/internal/billing/
log: log.With("module", "PRICES"),      // gateway/internal/billing/prices_loader.go
log: log.With("module", "TENANTS"),     // gateway/internal/tenants/
log: log.With("module", "SCHEDULE"),    // gateway/internal/schedule/
log: log.With("module", "ADMIN"),       // gateway/internal/admin/
log: log.With("module", "PRICES_LISTEN"), // listen.go
```

UPPER_SNAKE_CASE always; matches `AUDIT`, `UPSTREAMS`, `LISTEN`, `IDEM`, `AUTH`, `BREAKER`, `DISPATCHER`, `TOKENIZE` precedents.

### OpenAI error envelope

**Source:** `gateway/internal/httpx/envelope.go` lines 14-22 (`WriteOpenAIError`) + `pkg/openai/types.go` lines 125-136 (`ErrorResponse`/`ErrorDetail`).

**Apply to:** Every 4xx/5xx exit in quota, schedule, admin, billing middleware/handlers.

```go
httpx.WriteOpenAIError(w, http.StatusTooManyRequests,
	"rate_limit_error", "rate_limit_rps",
	"Rate limit exceeded: requests per second.")

httpx.WriteOpenAIError(w, http.StatusTooManyRequests,
	"insufficient_quota", "quota_exceeded_daily_tokens",
	"Daily token quota exceeded.")

httpx.WriteOpenAIError(w, http.StatusServiceUnavailable,
	"service_unavailable", "quota_check_unavailable",
	"Quota check unavailable; refusing to risk runaway external cost.")
```

`Retry-After` header set explicitly when applicable (idempotency/middleware.go line 144 precedent).

### Sentinel errors per package

**Source:** `gateway/internal/breaker/errors.go` + `gateway/internal/idempotency/errors.go` + `gateway/internal/auth/errors.go` + `gateway/internal/upstreams/errors.go`.

**Apply to:** `quota/errors.go`, `billing/errors.go`, `schedule/errors.go`, `admin/errors.go`, `tenants/errors.go`.

```go
package quota

import "errors"

var (
	// ErrRateLimitRPS — token bucket exhausted in 1s window. HTTP 429,
	// envelope code "rate_limit_rps". Header: Retry-After: 1.
	ErrRateLimitRPS = errors.New("quota: rate limit exceeded (RPS)")
	// ... 1 error per discriminated D-A4 code (8 total)
)
```

### Hot-reload via LISTEN/NOTIFY

**Source:** `gateway/internal/upstreams/listen.go` (full file — `pgxlisten.Listener` + dedicated `pgx.Conn` + `ReconnectDelay: 5*time.Second`).

**Apply to:** `billing/listen.go` (channels: prices_changed, fx_changed) + `tenants/listen.go` (channel: tenants_changed) + optional `admin/listen.go` (channel: admin_keys_changed).

Critical lines (listen.go 50-55 — handler MUST return nil on transient error to keep listener alive):
```go
return nil // returning nil keeps the listener alive; handler errors only logged
```

### atomic.Pointer copy-on-write snapshots

**Source:** `gateway/internal/upstreams/loader.go` lines 19-128 (`snapshot` + `atomic.Pointer[snapshot]` + `Refresh` rebuilds and `Store`s a fresh map).

**Apply to:** `billing/prices.go`, `billing/fx_loader.go`, `tenants/loader.go` — all in-memory configs hot-reloaded from Postgres.

Key invariant: never mutate an existing snapshot; always build a new one and swap. Reads are lock-free.

### Async batched DB writer

**Source:** `gateway/internal/audit/writer.go` (full file — channel buffer 1000, flush batch 500 OR 1s tick, ctx-cancel drains then exits).

**Apply to:** `billing/flusher.go`. Same shape; the flush function differs (UPSERT billing_events + UPSERT usage_counters in same txn vs CopyFrom audit_log).

### Conventional commits

**Source:** docs/CONVENTIONS.md + `gateway/db/migrations/` recent commits.

**Apply to:** All Phase 4 commits.

```
feat(04): add quota Lua token bucket
feat(04): wire billing flusher in main.go
chore(04): seed default prices in 0015 migration
test(04): SC-5 1000-goroutine atomic rate-limit harness
```

### testcontainers-go integration harness

**Source:** `gateway/internal/integration_test/setup_test.go` (full file — `TestMain` shared Postgres + Redis containers, `freshSchema` per test).

**Apply to:** All Phase 4 integration tests under `gateway/internal/integration_test/`.

Concurrent goroutine SC-5 harness: copy `concurrent_idempotency_test.go` (existing pattern with sync.WaitGroup + atomic counters).

NOTIFY hot-reload: copy `upstreams_listen_test.go` lines 53-67 (start listener in goroutine, sleep 500ms for LISTEN to register, then UPDATE row, then poll reloadCount with 5s deadline).

---

## No Analog Found

| File | Role | Data Flow | Reason |
|------|------|-----------|--------|
| `gateway/internal/quota/scripts/token_bucket.lua` | utility (Lua source) | n/a | First Lua script in repo. Use Stripe canonical pattern (RESEARCH §Pattern 1 + Code Examples). |
| `gateway/internal/schedule/window.go` `inWindow` | utility (pure func) | n/a | Trivial time-of-day comparison. No analog needed; RESEARCH §Pattern 5 has the snippet. |

Everything else has an exact or role-matching analog in Phase 2/3 code. The pattern surface is highly consistent — Phase 4 is fundamentally a replication of established mechanics (loader + listener + cache + envelope + flusher) applied to new domains (quota, billing, prices, tenants config).

---

## Metadata

**Analog search scope:**
- `gateway/internal/` (all packages)
- `gateway/cmd/gateway/` + `gateway/cmd/gatewayctl/`
- `gateway/db/migrations/` + `gateway/db/queries/`
- `pkg/openai/`
- `.planning/phases/02-*/` and `03-*/` for cross-references via CONTEXT.md `<canonical_refs>`

**Files scanned:** ~80 (Read calls on 24 critical files; Glob/Bash listing for the rest)

**Pattern extraction date:** 2026-04-20

**Key cross-cutting observation:** Phase 4 has zero novel architectural patterns — every new file mirrors an established Phase 2/3 analog. The planner can copy code directly from the cited line ranges and rename types. The only file requiring net-new design is `quota/scripts/token_bucket.lua` (covered by Stripe canonical in RESEARCH).

**RESEARCH corrections to flag for planner:**

1. **CONTEXT.md D-B1 SQL bug (RESEARCH finding 3):** `CURRENT_DATE AT TIME ZONE 'America/Sao_Paulo'` is invalid SQL. Use `(now() AT TIME ZONE 'America/Sao_Paulo')::date`. Apply correction in every `usage_counters` query and in `0011_evolve_usage_counters.sql` if any default-expression references it.

2. **Fireworks pricing seed (RESEARCH finding 1):** `qwen3.5-27b` is NOT in Fireworks' Apr 2026 catalog; OpenRouter aggregate is $0.195/1M input + $1.56/1M output. Operator must confirm pricing live before applying `0015_seed_prices_and_quotas.sql`. RESEARCH lines 13-14 mark this `[CITED]` but operator-gated.

3. **SSE dual-shape interceptor (RESEARCH finding 2):** llama.cpp emits `usage` in the same chunk as `finish_reason=stop`; OpenAI emits in a separate final chunk. The `usageTee` in `accountant.go` MUST tolerate both. Test fixtures should cover both.

4. **`pgxlisten` multiplexing (RESEARCH §Pattern 4):** Single `Listener` can `Handle` N channels. Phase 4 may keep 3 separate listeners (current Phase 3 pattern; cheaper diff) OR consolidate to one shared listener (DRY). Planner decides during execute; either is consistent with established conventions.
