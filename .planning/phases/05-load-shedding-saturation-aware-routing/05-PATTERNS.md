# Phase 5: Load Shedding (Saturation-aware Routing) - Pattern Map

**Mapped:** 2026-04-23
**Files analyzed:** 28 (18 new, 10 modified)
**Analogs found:** 27 / 28 (96 %)

## File Classification

### Novos arquivos

| New File | Role | Data Flow | Closest Analog | Match Quality |
|----------|------|-----------|----------------|---------------|
| `gateway/internal/shed/fsm.go` | state-machine | event-driven (tick) | `gateway/internal/breaker/breaker.go` | role-match (adapt 4-state FSM) |
| `gateway/internal/shed/fsm_test.go` | test | — | `gateway/internal/breaker/breaker_test.go` | exact |
| `gateway/internal/shed/set.go` | registry | in-memory | `gateway/internal/breaker/breaker.go` (Set struct) | exact |
| `gateway/internal/shed/set_test.go` | test | — | `gateway/internal/breaker/breaker_test.go` | exact |
| `gateway/internal/shed/middleware.go` | middleware | request-response | `gateway/internal/schedule/middleware.go` | exact |
| `gateway/internal/shed/middleware_test.go` | test | — | `gateway/internal/schedule/middleware_test.go` | exact |
| `gateway/internal/shed/mirror.go` | namespace const | — | `gateway/internal/breaker/mirror.go` | exact |
| `gateway/internal/shed/subscribe.go` | pub-sub consumer | event-driven | `gateway/internal/breaker/subscribe.go` | exact |
| `gateway/internal/shed/subscribe_test.go` | test | — | `gateway/internal/breaker/breaker_test.go` | role-match |
| `gateway/internal/shed/errors.go` | sentinel errors | — | `gateway/internal/breaker/errors.go` | exact |
| `gateway/internal/shed/tick.go` | goroutine loop | event-driven | `gateway/internal/upstreams/probe.go` (ticker loop) | role-match |
| `gateway/internal/shed/inflight.go` | registry | in-memory counters | `gateway/internal/breaker/breaker.go` (map+RWMutex) | role-match |
| `gateway/internal/shed/latency.go` | ring buffer | in-memory | sem analog direto no repo (hand-rolled, RESEARCH Pattern 2) | no-analog (novel) |
| `gateway/internal/dcgm/scraper.go` | http poller | request-response | `gateway/internal/upstreams/probe.go` | role-match (ticker + http client) |
| `gateway/internal/dcgm/scraper_test.go` | test | — | `gateway/internal/upstreams/health_test.go` | role-match |
| `gateway/internal/dcgm/errors.go` | sentinel errors | — | `gateway/internal/breaker/errors.go` | exact |
| `gateway/internal/redisx/shed.go` | redis helpers | pub-sub / Hash | `gateway/internal/redisx/breaker.go` | exact |
| `gateway/internal/redisx/shed_test.go` | test | — | `gateway/internal/redisx/breaker_test.go` | exact |
| `gateway/cmd/gatewayctl/shed.go` | cli subcommand | request-response | `gateway/cmd/gatewayctl/upstreams.go` | exact |
| `gateway/cmd/gatewayctl/thresholds.go` | cli subcommand | request-response | `gateway/cmd/gatewayctl/upstreams.go` (runUpstreamsUpdate JSONB merge) | exact |
| `gateway/cmd/gatewayctl/tenants_shed.go` | cli subcommand | request-response | `gateway/cmd/gatewayctl/tenant.go` (runTenantSetQuota partial UPDATE) | exact |
| `gateway/db/migrations/0016_evolve_tenants_shedding_limits.sql` | migration | DDL + seed | `gateway/db/migrations/0013_evolve_tenants_schedule_quota.sql` | exact |
| `gateway/db/migrations/0017_evolve_upstreams_shed_thresholds.sql` | migration | UPDATE JSONB | `gateway/db/migrations/0008_seed_upstreams.sql` | role-match |
| `gateway/db/migrations/0018_audit_log_shed_values.sql` | migration (conditional) | docs/ENUM ADD | — (see 03-audit_log schema) | partial |
| `gateway/internal/integration_test/shed_saturation_test.go` | test | — | `gateway/internal/integration_test/breaker_state_machine_test.go` | exact |
| `gateway/internal/integration_test/shed_hysteresis_test.go` | test | — | `gateway/internal/integration_test/breaker_state_machine_test.go` | exact |
| `gateway/internal/integration_test/shed_hot_reload_test.go` | test | — | `gateway/internal/integration_test/hot_reload_test.go` | exact |
| `gateway/internal/integration_test/shed_fairness_test.go` | test | — | `gateway/internal/integration_test/breaker_state_machine_test.go` | role-match |

### Arquivos modificados

| Modified File | Role | Data Flow | Change Type |
|----------|------|-----------|------|
| `gateway/internal/upstreams/types.go` | types | — | Estender `CircuitConfig` com 5 campos `Shed*` + evolve `parseCircuitConfig` |
| `gateway/internal/tenants/config.go` | types | — | Estender `TenantConfig` com 4 campos (`LocalInflightMax{LLM,STT,Embed}` + `PriorityTier`) |
| `gateway/internal/tenants/loader.go` | loader | CRUD | Refresh popula novos campos do sqlc row |
| `gateway/internal/proxy/dispatcher.go` | controller | request-response | Adicionar precedência `breaker → shed → tier-0` (~15 linhas em `NewDispatcher`) |
| `gateway/internal/auditctx/override.go` | context helper | — | Adicionar `WithShedDecision` / `ShedDecisionFromContext` + constante `UpstreamShedSaturatedValue` etc. |
| `gateway/internal/obs/metrics.go` | collectors | — | Adicionar ~11 `promauto` collectors (gauges + counters) |
| `gateway/internal/config/config.go` | env loader | — | Adicionar 5 env vars (`DCGM_EXPORTER_URL`, `SHED_LATENCY_RING_SIZE`, `SHED_TICK_INTERVAL_MS`, `SHED_DCGM_SCRAPE_INTERVAL_MS`, `SHED_DCGM_TIMEOUT_MS`) |
| `gateway/cmd/gateway/main.go` | entrypoint | — | Wire FSM ticker + dcgmScraper goroutines + middleware insertion entre `schedule` e handlers |
| `gateway/db/queries/tenants_admin.sql` | sqlc | — | Estender `ListTenantsForLoader` + `GetTenantConfig` + novo `UpdateTenantShedLimits` |
| `gateway/db/queries/upstreams.sql` | sqlc | — | Nenhuma (JSONB é opaco para sqlc; gatewayctl faz merge in-Go) |

## Pattern Assignments

### `gateway/internal/shed/fsm.go` (state-machine, event-driven)

**Primary analog:** `gateway/internal/breaker/breaker.go` (lockless hot path + OnStateChange callback + goroutine publish)

**Secondary analog:** hand-rolled pattern (RESEARCH.md Pattern 1) — `gobreaker` tem 3 estados binários, não cobre 4-state com evaluator externo.

**Package doc pattern** (`breaker.go` lines 1-9):
```go
// Package breaker (breaker.go) wraps sony/gobreaker/v2 circuit breakers
// per upstream and exposes a cross-replica overlay (remoteOpen) so a
// peer's OPEN transition propagated via Pub/Sub causes the local
// dispatcher to short-circuit without first having to fail itself.
//
// Authoritative state per process is the in-process *gobreaker.CircuitBreaker;
// Redis is a mirror, never the source of truth. Redis-down does NOT
// stop breakers from operating (CONTEXT.md D-D1).
package breaker
```

**Imports (hot path + obs + redisx)** (`breaker.go` lines 11-24):
```go
import (
    "context"
    "errors"
    "log/slog"
    "net/http"
    "sync"
    "time"

    "github.com/redis/go-redis/v9"
    "github.com/sony/gobreaker/v2"

    "github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
    "github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)
```

**OnStateChange callback (log + metric + goroutine publish)** (`breaker.go` lines 141-153):
```go
OnStateChange: func(n string, from, to gobreaker.State) {
    log.Info("breaker state change",
        "from", from.String(),
        "to", to.String(),
        "at", time.Now().Format(time.RFC3339),
    )
    obs.BreakerState.WithLabelValues(n).Set(stateFloat(to))
    if from == gobreaker.StateClosed && to == gobreaker.StateOpen {
        obs.BreakerTripsTotal.WithLabelValues(n).Inc()
    }
    // Mirror to Redis (best-effort; DO NOT block the state machine).
    go s.publishTransition(n, to)
},
```

**Apply to shed FSM:** Na transição de estado do `shed.FSM.transition` (PATTERN 1 do RESEARCH), replicar exatamente esse shape: `log.Info → obs.GatewayShedState.Set → go s.publishTransition`. A `atomic.Int32` + `atomic.Int64` substituem o gobreaker interno (CONTEXT D-C1 + RESEARCH Pattern 1). Usar log field `module=SHED_FSM`.

**Sentry breadcrumb hook:** RESEARCH referencia Sentry em transições — usar `sentry.AddBreadcrumb(...)` dentro do log block (padrão já presente em outros pontos; ver `obs.Init` no main).

---

### `gateway/internal/shed/set.go` (registry, in-memory)

**Analog:** `gateway/internal/breaker/breaker.go` lines 38-97 (Set struct + NewSet + Rebuild + Get)

**Struct pattern** (`breaker.go` lines 41-49):
```go
type Set struct {
    rdb *redis.Client
    log *slog.Logger
    opt Options

    mu         sync.RWMutex
    cbs        map[string]*gobreaker.CircuitBreaker[*http.Response]
    remoteOpen map[string]time.Time // state reported by other replicas via Pub/Sub
}
```

**Rebuild pattern (hot-reload driven)** (`breaker.go` lines 70-88):
```go
func (s *Set) Rebuild(names []string) {
    s.mu.Lock()
    defer s.mu.Unlock()
    want := make(map[string]bool, len(names))
    for _, n := range names {
        want[n] = true
    }
    for n := range s.cbs {
        if !want[n] {
            delete(s.cbs, n)
            delete(s.remoteOpen, n)
        }
    }
    for _, n := range names {
        if _, ok := s.cbs[n]; !ok {
            s.cbs[n] = s.newBreaker(n)
        }
    }
}
```

**Apply to shed:** `shed.Set` guarda `map[string]*FSM` + `remoteState map[string]State` (analog direto ao `remoteOpen`). `Rebuild` recebe `loader.Names()` após NOTIFY upstreams_changed — mesma orquestração já conectada em `gateway/cmd/gateway/main.go` linhas 279-284. FSMs existentes preservam estado; novos entram em `StateOff`.

---

### `gateway/internal/shed/subscribe.go` (pub-sub consumer, event-driven)

**Analog exato:** `gateway/internal/breaker/subscribe.go` (integral — 56 linhas).

**Reconnect-with-backoff pattern** (`subscribe.go` lines 19-56):
```go
func (s *Set) Subscribe(ctx context.Context) {
    log := s.log.With("subsystem", "subscribe")
    for {
        if err := ctx.Err(); err != nil {
            return
        }
        ps := redisx.SubscribeBreakerEvents(ctx, s.rdb)
        ch := ps.Channel()
        drained := false
        for !drained {
            select {
            case <-ctx.Done():
                _ = ps.Close()
                return
            case msg, ok := <-ch:
                if !ok {
                    drained = true
                    break
                }
                var ev redisx.BreakerEvent
                if err := json.Unmarshal([]byte(msg.Payload), &ev); err != nil {
                    log.Warn("malformed breaker event", "payload", msg.Payload, "err", err)
                    continue
                }
                s.applyRemoteEvent(ev)
                log.Debug("applied remote breaker event",
                    "upstream", ev.Upstream, "state", ev.State)
            }
        }
        _ = ps.Close()
        log.Warn("pubsub channel closed; reconnecting")
        select {
        case <-ctx.Done():
            return
        case <-time.After(1 * time.Second):
        }
    }
}
```

**Apply to shed:** Substituir `redisx.SubscribeBreakerEvents` por `redisx.SubscribeShedEvents`, `BreakerEvent` por `ShedEvent`, e `applyRemoteEvent` deve setar `shed.Set.remoteState[upstream] = state` (sem window de cooldown — shed não tem "timeout", FSM local continua seu curso). RESEARCH Pitfall 3 recomenda adicionar reconcile periódico (HGETALL a cada 30s) — decisão de implementar em subscribe.go ou em um reconcile.go separado fica para planner.

---

### `gateway/internal/shed/errors.go` (sentinel errors)

**Analog:** `gateway/internal/breaker/errors.go` (integral).

**Pattern** (`errors.go` lines 10-22):
```go
var (
    // ErrBreakerOpen is returned by Set.Execute when the upstream's
    // gobreaker is OPEN. The dispatcher (internal/proxy/dispatcher.go)
    // wraps this into either a tier-1 fallback (normal tenant per D-A1)
    // or a sensitive retry loop (sensitive tenant per D-B1).
    // Maps to HTTP 503 with code "upstream_unavailable" when surfaced.
    ErrBreakerOpen = errors.New("breaker: circuit open")

    // ErrUpstreamUnavailable means every tier (primary + fallback) is
    // OPEN for the requested role. Surfaced as HTTP 503 with OpenAI
    // envelope code "upstream_unavailable" per CONTEXT.md D-C4.
    ErrUpstreamUnavailable = errors.New("breaker: all upstreams unavailable")
)
```

**Apply to shed:** Criar `ErrShedOn`, `ErrTenantCapExceeded`, `ErrSensitiveSaturated`, `ErrAllChatUpstreamsSaturated`, `ErrShedForceTTLExceeded`, `ErrShedConfigInvalid`, `ErrDCGMScrapeFailed`. Prefixo `"shed: "` + wire-code entre parenteses no comentário.

---

### `gateway/internal/shed/middleware.go` (middleware, request-response)

**Analog exato:** `gateway/internal/schedule/middleware.go` (109 linhas).

**Imports pattern** (`middleware.go` lines 15-27):
```go
import (
    "log/slog"
    "net/http"
    "time"

    "github.com/google/uuid"

    "github.com/ifixtelecom/gpu-ifix/gateway/internal/auditctx"
    "github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
    "github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
    "github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
    "github.com/ifixtelecom/gpu-ifix/gateway/internal/tenants"
)
```

**Middleware factory + fallthrough pattern** (`middleware.go` lines 53-74):
```go
func Middleware(loader *tenants.Loader, log *slog.Logger) func(http.Handler) http.Handler {
    if log == nil {
        log = slog.Default()
    }
    log = log.With("module", "SCHEDULE")
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            ac, ok := auth.FromContext(r.Context())
            if !ok {
                next.ServeHTTP(w, r)
                return
            }
            tenantID, perr := uuid.Parse(ac.TenantID)
            if perr != nil {
                next.ServeHTTP(w, r)
                return
            }
            cfg, err := loader.Get(tenantID)
            if err != nil {
                next.ServeHTTP(w, r)
                return
            }
            ctx := r.Context()
            // ...
        })
    }
}
```

**Fail-fast sensitive 503 pattern** (`middleware.go` lines 88-97):
```go
if cfg.DataClass == "sensitive" {
    log.Error("sensitive tenant in peak mode at request time; CHECK constraint bypassed",
        "tenant", cfg.Slug)
    obs.GatewayScheduleRouting.WithLabelValues(cfg.Slug, "blocked_sensitive_peak").Inc()
    w.Header().Set("Retry-After", "30")
    httpx.WriteOpenAIError(w, http.StatusServiceUnavailable,
        "service_unavailable", "upstream_unavailable_for_sensitive_tenant",
        "Sensitive tenant cannot be routed to external providers.")
    return
}
```

**Override-via-auditctx pattern** (`middleware.go` lines 98-105):
```go
ctx = auditctx.WithUpstreamOverride(ctx, name)
obs.GatewayScheduleRouting.WithLabelValues(cfg.Slug, "off_hours_external").Inc()
log.Debug("schedule override",
    "tenant", cfg.Slug, "upstream", name)
// ...
next.ServeHTTP(w, r.WithContext(ctx))
```

**Apply to shed middleware (D-B4 full decision tree):**
1. Extrair `ac, cfg` via mesmo fallthrough pattern.
2. Ler tier pré-decidido pelo schedule: `auditctx.UpstreamOverrideFromContext(ctx)` — se == `"openrouter-chat"` ou `"openai-*"` (tier-1), `shed_decision="skipped_peak_offhours"` + increment `GatewayShedDecisions{reason="skipped_peak_offhours"}` + `next`.
3. Resolver o upstream tier-0 para o role (via `loader.Resolve(role, 0)` — mas role vem da route; usar `classifyRoute(r.URL.Path)` no estilo `quota/enforcer.go` lines 294-304).
4. `fsmState := shedSet.State(upstream)` + `tenantInflight := registry.TenantInflight(upstream, tenantID)`.
5. FSM=OFF OR tenantInflight < cap → `registry.Inc(upstream, tenantID); defer registry.Dec(...); next.ServeHTTP(...)`.
6. FSM=ON + inflight >= cap + `cfg.DataClass=="sensitive"` → 503 D-B3 usando **exatamente** o `httpx.WriteOpenAIError` + `Retry-After: 5` + stamp `auditctx.WithUpstreamOverride(ctx, "shed_blocked_sensitive")`.
7. FSM=ON + inflight >= cap + normal → `auditctx.WithUpstreamOverride(ctx, "openrouter-chat")` **e** `auditctx.WithShedDecision(ctx, "shed_saturated")` — dispatcher lê override e dispatcha como tier-1 (same pattern `dispatchOverride` em dispatcher.go linhas 201-235).

---

### `gateway/internal/shed/tick.go` (goroutine loop, event-driven)

**Analog:** `gateway/internal/upstreams/probe.go` (ticker+ctx pattern — não lido aqui mas referenciado via `go probe.Run(ctx)` em `main.go` linha 274).

**Ticker loop pattern** (derivado de RESEARCH Pattern 5):
```go
func RunTicker(ctx context.Context, d TickerDeps, log *slog.Logger) {
    log = log.With("module", "SHED_FSM")
    t := time.NewTicker(1 * time.Second)
    defer t.Stop()
    for {
        select {
        case <-ctx.Done():
            log.Info("FSM ticker stopping")
            return
        case now := <-t.C:
            d.Set.ForEach(func(upstream string, fsm *FSM) { ... })
        }
    }
}
```

**Apply:** Replicar exatamente o shape `time.NewTicker + for/select + ctx.Done → return`. Iterar sobre `shed.Set.ForEach` (helper similar a breaker.Snapshot).

---

### `gateway/internal/shed/inflight.go` (registry, in-memory counters)

**Analog:** `gateway/internal/breaker/breaker.go` lines 41-49 (Set struct padrão `map + RWMutex`) + RESEARCH Pattern 4 (populate-once RWMutex + hot-path atomic).

**Apply:** Exactly o código de RESEARCH Pattern 4 (`InflightRegistry`). Operações `Inc`/`Dec` chamadas em `shed.middleware.go` via `defer`.

---

### `gateway/internal/shed/latency.go` (ring buffer)

**No analog in repo.** Hand-rolled, RESEARCH Pattern 2.

**Apply:** Copiar literalmente o `LatencyRing` de RESEARCH Pattern 2 (`[]uint32` + `atomic.Uint64` index + `sort.Slice` on P95 read). Documentar race benigna (RESEARCH Pitfall 2) com comentário.

---

### `gateway/internal/dcgm/scraper.go` (http poller)

**No direct analog** para HTTP+expfmt, mas o shape goroutine+ticker+ctx é o mesmo de `upstreams/probe.go` (referenciado em main.go lines 267-274).

**Apply:** Copiar literalmente RESEARCH Pattern 3 (`Scraper` struct + `Run(ctx)` + `scrape(ctx)` + fail-open). Usar `expfmt.NewTextParser(model.UTF8Validation)` + `parser.TextToMetricFamilies(resp.Body)` (já dep indireta no go.mod, confirmar promote para direct). Gauge exposto: `obs.GatewayVramUsedMiB` (unidade MiB — atenção RESEARCH Pitfall 1 e **renomear campo JSONB para `shed_vram_used_mib`** per RESEARCH recomendação).

---

### `gateway/internal/redisx/shed.go` (redis helpers)

**Analog exato:** `gateway/internal/redisx/breaker.go` (integral, 71 linhas).

**Package doc + channel const pattern** (`breaker.go` lines 1-26):
```go
// Package redisx (breaker.go): helpers for the cross-replica circuit
// breaker mirror introduced by Phase 3 plan 03-03 (CONTEXT.md D-D1).
// ...
const breakerEventsChannel = "gw:breaker:events"
```

**Event struct + WriteState + Publish + Subscribe pattern** (`breaker.go` lines 31-70):
```go
type BreakerEvent struct {
    Upstream  string `json:"upstream"`
    State     string `json:"state"`      // "closed" | "half-open" | "open"
    SinceUnix int64  `json:"since_unix"` // time.Now().Unix() at transition
    Reason    string `json:"reason,omitempty"`
}

func WriteBreakerState(ctx context.Context, rdb *redis.Client, name, state string, sinceUnix int64) error {
    ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
    defer cancel()
    return rdb.HSet(ctx, "gw:breaker:"+name, map[string]any{
        "state":      state,
        "since_unix": sinceUnix,
    }).Err()
}

func PublishBreakerEvent(ctx context.Context, rdb *redis.Client, ev BreakerEvent) error {
    ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
    defer cancel()
    payload, err := json.Marshal(ev)
    if err != nil {
        return err
    }
    return rdb.Publish(ctx, breakerEventsChannel, payload).Err()
}

func SubscribeBreakerEvents(ctx context.Context, rdb *redis.Client) *redis.PubSub {
    return rdb.Subscribe(ctx, breakerEventsChannel)
}
```

**Apply to shed.go:** Replicar 1:1 com:
- `const shedEventsChannel = "gw:shed:events"`
- `type ShedEvent { Upstream, State (off|armed|on|recovering), SinceUnix, Signals{Inflight, P95Ms, VramMiB}, Reason }`
- `WriteShedState` → Hash `gw:shed:{upstream}`
- `PublishShedEvent`, `SubscribeShedEvents`, `ShedEventsChannel()`
- **Novo:** `WriteShedForce(ctx, rdb, name, state, ttl)` com `rdb.Set(ctx, "gw:shed:force:"+name, state, ttl)` + `GetShedForce(ctx, rdb, name)` — shadow state para operator override (D-C5).
- Timeout 2s em todos.

---

### `gateway/cmd/gatewayctl/shed.go` (cli subcommand)

**Analog exato:** `gateway/cmd/gatewayctl/upstreams.go` (linhas 28-46 dispatcher + 51-95 list subcommand).

**Dispatcher pattern** (`upstreams.go` lines 28-46):
```go
func runUpstreams(ctx context.Context, args []string, log *slog.Logger) int {
    if len(args) == 0 {
        fmt.Fprintln(os.Stderr, "Usage: gatewayctl upstreams list|update|enable|disable [flags]")
        return 2
    }
    switch args[0] {
    case "list":
        return runUpstreamsList(ctx, args[1:], log)
    case "update":
        return runUpstreamsUpdate(ctx, args[1:], log)
    case "enable":
        return runUpstreamsSetEnabled(ctx, args[1:], log, true)
    case "disable":
        return runUpstreamsSetEnabled(ctx, args[1:], log, false)
    default:
        fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", args[0])
        return 2
    }
}
```

**Table output pattern** (`upstreams.go` lines 68-89 — use `text/tabwriter`):
```go
tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
fmt.Fprintln(tw, "NAME\tROLE\tTIER\tENABLED\t...")
for _, r := range rows {
    fmt.Fprintf(tw, "%s\t%s\t%d\t%v\t...\n", ...)
}
if err := tw.Flush(); err != nil { ... }
```

**Apply:** 
- `runShed` dispatcher com `state | force`.
- `runShedState` lê Redis `gw:shed:*` keys + optionally `/admin/shed-debug` — output via tabwriter.
- `runShedForce` faz `redisx.WriteShedForce(name, "off" or "on", ttl)` — aceita `--upstream`, `--ttl` (parse via `time.ParseDuration`), `--state`.

---

### `gateway/cmd/gatewayctl/thresholds.go` (cli subcommand)

**Analog exato:** `gateway/cmd/gatewayctl/upstreams.go` runUpstreamsUpdate (linhas 100-183) — **especialmente** o JSONB merge pattern.

**JSONB merge pattern** (`upstreams.go` lines 149-168):
```go
if *ccFailures > 0 || *ccCooldown > 0 {
    // Merge the new values into the existing JSONB so unrelated
    // fields are preserved (e.g. future Phase 5 saturation thresholds).
    merged := map[string]any{}
    if len(row.CircuitConfig) > 0 {
        _ = json.Unmarshal(row.CircuitConfig, &merged)
    }
    if *ccFailures > 0 {
        merged["failures"] = *ccFailures
    }
    if *ccCooldown > 0 {
        merged["cooldown_s"] = *ccCooldown
    }
    buf, err := json.Marshal(merged)
    if err != nil {
        fmt.Fprintf(os.Stderr, "marshal circuit_config: %v\n", err)
        return 1
    }
    params.CircuitConfig = buf
}
```

**Apply:** Flags `--upstream X --inflight N --p95-ms N --vram-mib N --arm-s N --recover-s N`. Merge:
```go
merged["shed_inflight_max"] = *inflight
merged["shed_p95_ms"] = *p95
merged["shed_vram_used_mib"] = *vramMib // <-- MiB not bytes
merged["shed_arm_seconds"] = *arm
merged["shed_recover_seconds"] = *recover
```
Chamar `q.UpdateUpstreamAdmin` — NOTIFY dispara automaticamente (migration 0009). Incrementa `obs.GatewayShedThresholdsChanged` na sequência (via listener no gateway, não aqui).

---

### `gateway/cmd/gatewayctl/tenants_shed.go` (cli subcommand)

**Analog exato:** `gateway/cmd/gatewayctl/tenant.go` runTenantSetQuota (linhas 212-296) — partial UPDATE com `sqlc.narg` + flag sentinel `-1 = unchanged`.

**Partial UPDATE pattern** (`tenant.go` lines 250-290):
```go
params := gen.UpdateTenantQuotaParams{Slug: *slug}
anyFlag := false
if *dailyTokens >= 0 {
    params.DailyQuotaTokens = pgtype.Int8{Int64: *dailyTokens, Valid: true}
    anyFlag = true
}
// ... repeat for each optional flag
if !anyFlag {
    fmt.Fprintln(os.Stderr, "at least one quota/limit flag required")
    return 2
}
if err := q.UpdateTenantQuota(ctx, params); err != nil {
    fmt.Fprintf(os.Stderr, "update failed: %v\n", err)
    return 1
}
```

**Apply:** Flags `--tenant X --llm N --stt N --embed N --tier {S|A|B}`. Usa `gen.UpdateTenantShedLimitsParams` (novo sqlc query — extended `tenants_admin.sql`) com `pgtype.Int4`/`pgtype.Text`. Validação pre-DB:`priorityTier` deve ser `S|A|B`; `*llm>=0`, etc. NOTIFY `tenants_changed` dispara via trigger estendido (novo migration 0016 estende `tenants_update_notify` `WHEN` clause).

---

### `gateway/db/migrations/0016_evolve_tenants_shedding_limits.sql`

**Analog exato:** `gateway/db/migrations/0013_evolve_tenants_schedule_quota.sql` (117 linhas).

**Structural pattern** (`0013_...sql` lines 1-43):
```sql
-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

ALTER TABLE ai_gateway.tenants
    ADD COLUMN IF NOT EXISTS mode               TEXT NOT NULL DEFAULT '24/7'
        CHECK (mode IN ('24/7', 'peak')),
    ADD COLUMN IF NOT EXISTS peak_window_start  TIME,
    ADD COLUMN IF NOT EXISTS peak_window_end    TIME,
    ADD COLUMN IF NOT EXISTS schedule_timezone  TEXT NOT NULL DEFAULT 'America/Sao_Paulo';

ALTER TABLE ai_gateway.tenants
    ADD COLUMN IF NOT EXISTS daily_quota_tokens          BIGINT NOT NULL DEFAULT 10000000,
    ...;
```

**NOTIFY trigger extension pattern** (`0013_...sql` lines 71-89 — CRITICAL: nova migration deve estender as colunas monitoradas):
```sql
CREATE TRIGGER tenants_update_notify
AFTER UPDATE ON ai_gateway.tenants
FOR EACH ROW
WHEN (pg_trigger_depth() = 0 AND (
    NEW.mode IS DISTINCT FROM OLD.mode
    OR NEW.peak_window_start IS DISTINCT FROM OLD.peak_window_start
    ...
    OR NEW.rpm_limit IS DISTINCT FROM OLD.rpm_limit
    OR NEW.data_class IS DISTINCT FROM OLD.data_class
))
EXECUTE FUNCTION ai_gateway.notify_tenants_changed();
```

**Apply Phase 5 migration 0016:**
```sql
-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

ALTER TABLE ai_gateway.tenants
    ADD COLUMN IF NOT EXISTS local_inflight_max_llm   INT NOT NULL DEFAULT 4,
    ADD COLUMN IF NOT EXISTS local_inflight_max_stt   INT NOT NULL DEFAULT 2,
    ADD COLUMN IF NOT EXISTS local_inflight_max_embed INT NOT NULL DEFAULT 8,
    ADD COLUMN IF NOT EXISTS priority_tier            TEXT NOT NULL DEFAULT 'A'
        CHECK (priority_tier IN ('S','A','B'));

-- Seed 6 known tenants (CONTEXT D-B1/D-B2):
UPDATE ai_gateway.tenants SET local_inflight_max_llm=4, priority_tier='A'
    WHERE slug='converseai';
UPDATE ai_gateway.tenants SET local_inflight_max_stt=2, priority_tier='S'
    WHERE slug='telefonia'; -- etc.

-- Extend NOTIFY trigger (DROP + CREATE + expand WHEN to include 4 new cols):
DROP TRIGGER IF EXISTS tenants_update_notify ON ai_gateway.tenants;
CREATE TRIGGER tenants_update_notify
AFTER UPDATE ON ai_gateway.tenants
FOR EACH ROW
WHEN (pg_trigger_depth() = 0 AND (
    NEW.mode IS DISTINCT FROM OLD.mode
    ... (all Phase 4 cols)
    OR NEW.local_inflight_max_llm IS DISTINCT FROM OLD.local_inflight_max_llm
    OR NEW.local_inflight_max_stt IS DISTINCT FROM OLD.local_inflight_max_stt
    OR NEW.local_inflight_max_embed IS DISTINCT FROM OLD.local_inflight_max_embed
    OR NEW.priority_tier IS DISTINCT FROM OLD.priority_tier
))
EXECUTE FUNCTION ai_gateway.notify_tenants_changed();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- (reverse ALTER + re-create original trigger OR delegate to operator)
-- +goose StatementEnd
```

---

### `gateway/db/migrations/0017_evolve_upstreams_shed_thresholds.sql`

**Analog:** `gateway/db/migrations/0008_seed_upstreams.sql` (INSERT ON CONFLICT). Mas esta é UPDATE JSONB — sem analog direto; operação única: UPDATE `circuit_config` jsonb_set para tier-0 upstreams.

**Pattern (novo, derivado):**
```sql
-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- Seed shed thresholds on tier-0 upstreams (tier-1 kept as-is; shed NEVER
-- runs against tier-1 — see CONTEXT.md D-C4).
UPDATE ai_gateway.upstreams
SET circuit_config = circuit_config
    || jsonb_build_object(
        'shed_inflight_max',     8,
        'shed_p95_ms',           2000,
        'shed_vram_used_mib',    21504, -- 21 GB expressed in MiB (RESEARCH Pitfall 1)
        'shed_arm_seconds',      30,
        'shed_recover_seconds',  60
    )
WHERE name = 'local-llm';

UPDATE ai_gateway.upstreams
SET circuit_config = circuit_config
    || jsonb_build_object(
        'shed_inflight_max',     16,
        'shed_p95_ms',           500,
        'shed_vram_used_mib',    21504,
        'shed_arm_seconds',      30,
        'shed_recover_seconds',  60
    )
WHERE name = 'local-embed';

UPDATE ai_gateway.upstreams
SET circuit_config = circuit_config
    || jsonb_build_object(
        'shed_inflight_max',     4,
        'shed_p95_ms',           3000, -- Whisper slower by design
        'shed_vram_used_mib',    21504,
        'shed_arm_seconds',      30,
        'shed_recover_seconds',  60
    )
WHERE name = 'local-stt';
-- +goose StatementEnd
```

---

### `gateway/db/migrations/0018_audit_log_shed_values.sql` (conditional)

**No direct analog** — conditional migration que só gera DDL se coluna `audit_log.upstream` for ENUM. Na maioria dos casos é TEXT (verificar Fase 2 schema). Plan deve confirmar durante execute. Se TEXT, migration é apenas comentário `-- docs-only` sem DDL.

---

## Pattern Assignments — Modified Files

### `gateway/internal/upstreams/types.go` (struct extension)

**Current** (`types.go` lines 39-72):
```go
type CircuitConfig struct {
    Failures  uint32        `json:"failures,omitempty"`
    Cooldown  time.Duration `json:"-"`
    CooldownS int           `json:"cooldown_s,omitempty"` // DB stores seconds
}

func parseCircuitConfig(raw []byte) CircuitConfig {
    var cc CircuitConfig
    if len(raw) == 0 {
        return cc
    }
    if err := json.Unmarshal(raw, &cc); err != nil {
        return CircuitConfig{}
    }
    cc.Cooldown = time.Duration(cc.CooldownS) * time.Second
    return cc
}
```

**Apply Phase 5 extension:**
```go
type CircuitConfig struct {
    Failures  uint32        `json:"failures,omitempty"`
    Cooldown  time.Duration `json:"-"`
    CooldownS int           `json:"cooldown_s,omitempty"`

    // Phase 5 — saturation thresholds (D-A4). Zero values mean "use shed defaults".
    // Unit note: vram is in MiB (DCGM_FI_DEV_FB_USED native unit; RESEARCH Pitfall 1).
    ShedInflightMax   int           `json:"shed_inflight_max,omitempty"`
    ShedP95Ms         int           `json:"shed_p95_ms,omitempty"`
    ShedVramUsedMiB   int64         `json:"shed_vram_used_mib,omitempty"`
    ShedArm           time.Duration `json:"-"`
    ShedArmSeconds    int           `json:"shed_arm_seconds,omitempty"`
    ShedRecover       time.Duration `json:"-"`
    ShedRecoverSeconds int          `json:"shed_recover_seconds,omitempty"`
}

func parseCircuitConfig(raw []byte) CircuitConfig {
    var cc CircuitConfig
    if len(raw) == 0 {
        return cc
    }
    if err := json.Unmarshal(raw, &cc); err != nil {
        return CircuitConfig{}
    }
    cc.Cooldown = time.Duration(cc.CooldownS) * time.Second
    cc.ShedArm = time.Duration(cc.ShedArmSeconds) * time.Second
    cc.ShedRecover = time.Duration(cc.ShedRecoverSeconds) * time.Second
    return cc
}
```

---

### `gateway/internal/tenants/config.go` (struct extension)

**Current** (`config.go` lines 22-47) — TenantConfig. **Apply** adicionar 4 campos:
```go
// Phase 5 — fairness per-tenant hard caps (D-B1 / D-B2).
LocalInflightMaxLLM   int
LocalInflightMaxSTT   int
LocalInflightMaxEmbed int
PriorityTier          string // "S" | "A" | "B" (metadata-only in v1)
```

---

### `gateway/internal/tenants/loader.go` (loader.Refresh extension)

**Current** (`loader.go` lines 87-106) Refresh constrói `TenantConfig{...}`. **Apply:** adicionar assignments:
```go
LocalInflightMaxLLM:   int(r.LocalInflightMaxLlm),
LocalInflightMaxSTT:   int(r.LocalInflightMaxStt),
LocalInflightMaxEmbed: int(r.LocalInflightMaxEmbed),
PriorityTier:          r.PriorityTier,
```
Pressupõe sqlc regenerado após evolução de `ListTenantsForLoader` em `tenants_admin.sql` (adicionar 4 colunas ao SELECT).

---

### `gateway/internal/proxy/dispatcher.go` (precedence extension, ~15 lines)

**Current decision tree** (dispatcher.go lines 134-187).

**Insertion point:** Após resolver `t0State` (linhas 134-137) e antes de `if t0State == gobreaker.StateClosed`:
```go
// Phase 5 — shed middleware already stamped shed_decision on ctx IF
// FSM=ON + tenant cap exceeded. Dispatcher honours the stamped decision
// EXCEPT when breaker.open (breaker wins — D-C4).
if override := auditctx.UpstreamOverrideFromContext(r.Context()); override != "" &&
    override != UpstreamBlockedSensitiveValue {
    // schedule override (existing) OR shed_saturated override (new — same wire).
    cfg.dispatchOverride(w, r, override, log)
    return
}
```
Nota: como shed middleware já roda **antes** do dispatcher na chain (entre schedule e handlers — main.go buildRouter), o `auditctx.UpstreamOverrideFromContext` check existente (linhas 94-98) já cobre o caso shed_saturated. **Zero refactor structural** — shed middleware stampa "openrouter-chat" no override e dispatcher usa caminho existente `dispatchOverride`.

**Real net change:** adicionar enum `UpstreamShedSaturatedValue = "shed_saturated"` como constante + reconhecer também como "não dispatchar tier-0" no `dispatchOverride`. E tratamento de tier-1 indisponível (D-D1) via nova envelope `all_chat_upstreams_saturated`:
```go
if cb, found := cfg.Breaker.Get(name); found && cb != nil && cb.State() == gobreaker.StateOpen {
    // existing: off_hours_upstream_unavailable
    // NEW for shed: if shed_decision in ctx == shed_saturated, use all_chat_upstreams_saturated
    if auditctx.ShedDecisionFromContext(r.Context()) == "shed_saturated" {
        w.Header().Set("Retry-After", "30")
        httpx.WriteOpenAIError(w, http.StatusServiceUnavailable,
            "service_unavailable", "all_chat_upstreams_saturated", "Primary saturated and secondary unavailable.")
        return
    }
    // ... existing off-hours path
}
```

---

### `gateway/internal/auditctx/override.go` (extension)

**Current** (`override.go` lines 1-69) — já tem `WithUpstreamOverride` + `WithBillingUpstream`.

**Apply Phase 5:** Adicionar `WithShedDecision` + `ShedDecisionFromContext` seguindo EXATAMENTE o mesmo shape:
```go
type shedDecisionKey struct{}

func WithShedDecision(parent context.Context, decision string) context.Context {
    return context.WithValue(parent, shedDecisionKey{}, decision)
}

func ShedDecisionFromContext(ctx context.Context) string {
    if v, ok := ctx.Value(shedDecisionKey{}).(string); ok {
        return v
    }
    return ""
}
```

E constantes novas (mirror do `UpstreamBlockedSensitiveValue` em dispatcher.go line 50):
```go
const (
    UpstreamShedSaturatedValue     = "shed_saturated"
    UpstreamShedBlockedSensitiveValue = "shed_blocked_sensitive"
    UpstreamShedTier1UnavailableValue = "shed_tier1_unavailable"
)
```

---

### `gateway/internal/obs/metrics.go` (extension ~11 collectors)

**Pattern** (metrics.go lines 56-71 — BreakerState gauge + BreakerTripsTotal counter):
```go
var BreakerState = promauto.NewGaugeVec(
    prometheus.GaugeOpts{
        Name: "gateway_breaker_state",
        Help: "Current circuit breaker state per upstream. 0=closed, 1=half-open, 2=open.",
    },
    []string{"upstream"},
)

var BreakerTripsTotal = promauto.NewCounterVec(
    prometheus.CounterOpts{
        Name: "gateway_breaker_trips_total",
        Help: "Count of CLOSED to OPEN transitions per upstream.",
    },
    []string{"upstream"},
)
```

**Apply Phase 5** — adicionar 11 collectors (CONTEXT D-D4):
```go
var GatewayInflight = promauto.NewGaugeVec(prometheus.GaugeOpts{
    Name: "gateway_inflight",
    Help: "In-flight requests per upstream (atomic counter).",
}, []string{"upstream"})

var GatewayInflightTenant = promauto.NewGaugeVec(prometheus.GaugeOpts{
    Name: "gateway_inflight_tenant",
    Help: "In-flight requests per (upstream, tenant).",
}, []string{"upstream", "tenant"})

var GatewayShedState = promauto.NewGaugeVec(prometheus.GaugeOpts{
    Name: "gateway_shed_state",
    Help: "Current shed FSM state per upstream. 0=off, 1=armed, 2=on, 3=recovering.",
}, []string{"upstream"})

var GatewayShedDecisions = promauto.NewCounterVec(prometheus.CounterOpts{
    Name: "gateway_shed_decisions_total",
    Help: "Shedding decisions taken, labeled by upstream and reason.",
}, []string{"upstream", "reason"})

var GatewayShedTransitions = promauto.NewCounterVec(prometheus.CounterOpts{
    Name: "gateway_shed_transitions_total",
    Help: "FSM transitions, labeled by upstream and from→to.",
}, []string{"upstream", "from", "to"})

var GatewayShedMirrorFailures = promauto.NewCounter(prometheus.CounterOpts{
    Name: "gateway_shed_mirror_failures_total",
    Help: "Redis HSET/PUBLISH failures when mirroring shed state.",
})

var GatewayVramUsedMiB = promauto.NewGauge(prometheus.GaugeOpts{
    Name: "gateway_vram_used_mib",
    Help: "VRAM framebuffer used in MiB (from DCGM_FI_DEV_FB_USED).",
})

var GatewayP95RequestMs = promauto.NewGaugeVec(prometheus.GaugeOpts{
    Name: "gateway_p95_request_ms",
    Help: "Current P95 request duration in ms per upstream (ring buffer).",
}, []string{"upstream"})

var GatewayDcgmScrapeFailures = promauto.NewCounterVec(prometheus.CounterOpts{
    Name: "gateway_dcgm_scrape_failures_total",
    Help: "DCGM scrape failures, labeled by reason.",
}, []string{"reason"})

var GatewayShedThresholdsChanged = promauto.NewCounterVec(prometheus.CounterOpts{
    Name: "gateway_shed_thresholds_changed_total",
    Help: "Count of circuit_config JSONB hot-reloads per upstream.",
}, []string{"upstream"})

var GatewayShedForceActive = promauto.NewGaugeVec(prometheus.GaugeOpts{
    Name: "gateway_shed_force_active",
    Help: "1 when operator override (gw:shed:force:{upstream}) is set; 0 otherwise.",
}, []string{"upstream"})
```

---

### `gateway/internal/config/config.go` (env extension)

**Pattern** (config.go lines 61-71 — Phase 3 probe+breaker tuning):
```go
ProbeIntervalSeconds       int // PROBE_INTERVAL_SECONDS (default 10)
ProbeBudgetSeconds         int // PROBE_BUDGET_SECONDS (default 5)
BreakerConsecutiveFailures int // BREAKER_CONSECUTIVE_FAILURES (default 3)
BreakerCooldownSeconds     int // BREAKER_COOLDOWN_SECONDS (default 30)
```

**Load() pattern** (config.go lines 147-150):
```go
ProbeIntervalSeconds:       atoiOr(os.Getenv("PROBE_INTERVAL_SECONDS"), 10),
ProbeBudgetSeconds:         atoiOr(os.Getenv("PROBE_BUDGET_SECONDS"), 5),
BreakerConsecutiveFailures: atoiOr(os.Getenv("BREAKER_CONSECUTIVE_FAILURES"), 3),
BreakerCooldownSeconds:     atoiOr(os.Getenv("BREAKER_COOLDOWN_SECONDS"), 30),
```

**Apply Phase 5:**
```go
// Phase 5 — shedding runtime tuning (CONTEXT D-A3/Claude's Discretion).
DCGMExporterURL           string // DCGM_EXPORTER_URL (required if pod :9400 runs; defaults to http://local-llm-host:9400/metrics)
ShedLatencyRingSize       int    // SHED_LATENCY_RING_SIZE (default 200)
ShedTickIntervalMs        int    // SHED_TICK_INTERVAL_MS (default 1000)
ShedDcgmScrapeIntervalMs  int    // SHED_DCGM_SCRAPE_INTERVAL_MS (default 5000)
ShedDcgmTimeoutMs         int    // SHED_DCGM_TIMEOUT_MS (default 2000)
```
E em Load():
```go
DCGMExporterURL:          envOr("DCGM_EXPORTER_URL", ""),
ShedLatencyRingSize:      atoiOr(os.Getenv("SHED_LATENCY_RING_SIZE"), 200),
ShedTickIntervalMs:       atoiOr(os.Getenv("SHED_TICK_INTERVAL_MS"), 1000),
ShedDcgmScrapeIntervalMs: atoiOr(os.Getenv("SHED_DCGM_SCRAPE_INTERVAL_MS"), 5000),
ShedDcgmTimeoutMs:        atoiOr(os.Getenv("SHED_DCGM_TIMEOUT_MS"), 2000),
```
Todas opcionais (sem adicionar a `requiredOrder` — gateway boota mesmo sem DCGM em modo 2-of-3 reduzido).

---

### `gateway/cmd/gateway/main.go` (wiring)

**Pattern — Phase 3 wiring block** (main.go lines 239-284) mostra o shape: construir loader, construir subsystem, subscribe goroutine, probe goroutine, ListenAndReload goroutine com onReload rebuild.

**Pattern — Middleware chain mount** (main.go lines 601-626):
```go
r.Group(func(pg chi.Router) {
    pg.Use(obs.RequestsMiddleware(log))
    if verifier != nil {
        pg.Use(auth.Middleware(verifier, log))
    }
    if px.auditWriter != nil {
        pg.Use(audit.Middleware(px.auditWriter, log))
    }
    if px.rdb != nil && px.tenantsLoader != nil {
        pg.Use(quota.RateLimitMiddleware(...))
    }
    if px.quotaChecker != nil && px.tenantsLoader != nil {
        pg.Use(quota.QuotaMiddleware(...))
    }
    if px.tenantsLoader != nil {
        pg.Use(schedule.Middleware(px.tenantsLoader, log))
    }
    // ... mount routes
})
```

**Apply Phase 5 — middleware insertion entre schedule e handlers (CONTEXT D-B4):**
```go
if px.tenantsLoader != nil {
    pg.Use(schedule.Middleware(px.tenantsLoader, log))
}
// NEW PHASE 5: shed middleware (between schedule and tokencount/dispatcher)
if px.shedSet != nil && px.inflightRegistry != nil {
    pg.Use(shed.Middleware(px.shedSet, px.inflightRegistry, px.tenantsLoader, px.upstreamLoader, log))
}
```

**Wiring block Phase 5 (insertion depois do `breakerSet.Subscribe` e antes do schedule Middleware mount):**
```go
// Phase 5 — shed FSM set + inflight registry + latency rings + DCGM scraper.
latencyRings := shed.NewLatencyRings(loader.Names(), cfg.ShedLatencyRingSize)
inflightRegistry := shed.NewInflightRegistry(loader.Names())
shedSet := shed.NewSet(rdb, log, loader.Names())
go shedSet.Subscribe(ctx)

var scraper *dcgm.Scraper
if cfg.DCGMExporterURL != "" {
    scraper = dcgm.New(cfg.DCGMExporterURL,
        time.Duration(cfg.ShedDcgmScrapeIntervalMs)*time.Millisecond, log)
    go scraper.Run(ctx)
}

go shed.RunTicker(ctx, shed.TickerDeps{
    Set:          shedSet,
    Inflight:     inflightRegistry,
    Latency:      latencyRings,
    DCGM:         scraper,
    ThresholdSrc: func(name string) shed.Thresholds { ... from loader.Get(name).CircuitConfig ... },
}, log)
```

E estender o bloco `upstreams.ListenAndReload` onReload (main.go lines 279-284) para também chamar `shedSet.Rebuild(loader.Names())` (paralelo ao `breakerSet.Rebuild`).

---

### `gateway/db/queries/tenants_admin.sql` (sqlc extension)

**Apply:**
1. `ListTenantsForLoader` — adicionar 4 colunas ao SELECT.
2. `GetTenantConfig` — adicionar 4 colunas ao SELECT.
3. Novo query `UpdateTenantShedLimits :exec` — partial UPDATE com `sqlc.narg` (seguindo pattern de `UpdateTenantQuota` linhas 37-49):
```sql
-- name: UpdateTenantShedLimits :exec
UPDATE ai_gateway.tenants
SET local_inflight_max_llm   = COALESCE(sqlc.narg('local_inflight_max_llm')::int,   local_inflight_max_llm),
    local_inflight_max_stt   = COALESCE(sqlc.narg('local_inflight_max_stt')::int,   local_inflight_max_stt),
    local_inflight_max_embed = COALESCE(sqlc.narg('local_inflight_max_embed')::int, local_inflight_max_embed),
    priority_tier            = COALESCE(sqlc.narg('priority_tier')::text,           priority_tier),
    updated_at               = now()
WHERE slug = $1;
```

---

## Shared Patterns

### Authentication / Tenant Resolution (applies to shed middleware)

**Source:** `gateway/internal/quota/enforcer.go` lines 73-96 + `gateway/internal/schedule/middleware.go` lines 60-74.

```go
ac, ok := auth.FromContext(r.Context())
if !ok {
    httpx.WriteOpenAIError(w, http.StatusUnauthorized, "authentication_error", "no_api_key", "...")
    return // OR next.ServeHTTP if behaving as soft middleware
}
tenantID, perr := uuid.Parse(ac.TenantID)
if perr != nil {
    next.ServeHTTP(w, r) // defensive
    return
}
cfg, err := loader.Get(tenantID)
if err != nil {
    next.ServeHTTP(w, r) // snapshot missing
    return
}
```

**Apply to shed middleware:** Idêntico. Shed é soft-middleware (skip em erro, não bloqueia auth — essa já foi confirmada pelo auth middleware acima na chain).

---

### Error handling / 503 Envelope (applies to sensitive block + tier1 unavailable)

**Source:** `gateway/internal/httpx/envelope.go` (`WriteOpenAIError`) + `gateway/internal/proxy/dispatcher.go` lines 268-284 (writeSensitiveBlock) + `gateway/internal/schedule/middleware.go` lines 88-97.

```go
w.Header().Set("Retry-After", "5")  // sensitive D-B3
// OR
w.Header().Set("Retry-After", "30") // tier-1 unavailable D-D1
httpx.WriteOpenAIError(w, http.StatusServiceUnavailable,
    "service_unavailable", "upstream_saturated_for_sensitive_tenant",
    "Primary upstream is saturated; sensitive-data tenants cannot be routed to external providers.")
```

**Apply ALL new 503 paths** (D-B3, D-D1) devem:
1. Set `Retry-After` header (5s sensitive, 30s tier1).
2. Use `httpx.WriteOpenAIError` com `type=service_unavailable` + `code` específico.
3. Stamp `auditctx.WithUpstreamOverride(ctx, "shed_blocked_sensitive")` ou `"shed_tier1_unavailable"`.
4. Increment metric apropriada em `obs.*`.

---

### Logging / slog module convention

**Source:** `gateway/internal/breaker/breaker.go` linha 55 (`log.With("module", "BREAKER")`), `gateway/internal/schedule/middleware.go` linha 57 (`log.With("module", "SHEDULE")`), `gateway/internal/tenants/loader.go` linha 55 (`log.With("module", "TENANTS")`).

**Apply:** Novos módulos em Phase 5:
- `module=SHED` (shed.middleware.go, shed.set.go)
- `module=SHED_FSM` (shed.fsm.go, shed.tick.go)
- `module=SHED_MIRROR` (shed.mirror.go via publishTransition — match breaker)
- `module=SHED_LISTEN` (não há listen novo — reusa upstreams+tenants)
- `module=DCGM` (dcgm.scraper.go)

---

### Audit integration via auditctx override

**Source:** `gateway/internal/auditctx/override.go` (todo o arquivo) + `gateway/internal/proxy/dispatcher.go` line 278 (in-place mutation pattern):
```go
*r = *r.WithContext(auditctx.WithUpstreamOverride(r.Context(), UpstreamBlockedSensitiveValue))
```

**Apply:** Shed middleware usa mesma técnica para stampar `"shed_saturated"` / `"shed_blocked_sensitive"` / `"shed_tier1_unavailable"` no audit row. Dispatcher lê via `UpstreamOverrideFromContext` — **zero mudança estrutural** no audit.Middleware.

**IMPORTANT (HIGH-02 caveat from dispatcher.go lines 270-278):** A mutação in-place `*r = *r.WithContext(...)` é safe SÓ se audit.Middleware lê `r.Context()` **sequencialmente** após `next.ServeHTTP(aw, r)` retornar. Se essa invariante mudar, substituir por header-based ou sync.Map. Documentar no shed middleware.

---

### Cross-replica mirror (shed ↔ breaker identical pattern)

**Source:** `gateway/internal/breaker/breaker.go` lines 211-253 (publishTransition + applyRemoteEvent) + `gateway/internal/redisx/breaker.go` (inteiro).

**Apply:** 100% replicação estrutural:
1. `go s.publishTransition(name, newState)` no OnStateChange (não bloquear hot path).
2. `redisx.WriteShedState` HSET `gw:shed:{upstream}` (Hash) + 2s timeout.
3. `redisx.PublishShedEvent` PUB `gw:shed:events` (JSON payload).
4. Subscriber usa mesma reconnect-with-backoff loop.
5. Fallback silencioso com métrica `GatewayShedMirrorFailures` se Redis down (CONTEXT D-C3 — "mesma filosofia do breaker mirror").

**Boot rehydration (NEW — RESEARCH Pitfall 3 mitigação):** Ao criar `shed.Set`, **antes** de subscribir, fazer `HGETALL gw:shed:{upstream}` para cada nome e setar `remoteState` inicial — evita boot em "OFF" enquanto outra réplica está em ON. Novo em relação ao breaker.

---

### Integration tests via testcontainers + mock upstream

**Source:** `gateway/internal/integration_test/setup_test.go` (TestMain + setupContainers) + `gateway/internal/integration_test/breaker_state_machine_test.go` (mock server + `newCountingMockWithHandler` helper).

**Pattern 1 — testcontainers harness reuse:**
```go
//go:build integration

func TestIntegration_ShedStateMachine(t *testing.T) {
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()
    _, rdb := freshSchema(t, ctx)
    // ... test body
}
```

**Pattern 2 — mock upstream with controllable behavior:**
```go
var hits atomic.Int64
mock := newCountingMockWithHandler(t, func(w http.ResponseWriter, _ *http.Request) {
    // controlled latency/status per test
})
```

**Apply Phase 5 tests:** Tests vivem em `gateway/internal/integration_test/shed_*_test.go` reusando `freshSchema` + `newCountingMockWithHandler`. Adicionar helper `newControlledLatencyMock(duration, statusFn)` para SC-2 (oscillação).

**Vegeta load gen** (RESEARCH §Standard Stack) é opt-in — adicionar SÓ ao SC-1 e SC-4; SC-2 funciona com goroutine loops simples.

---

## No Analog Found

| File | Role | Reason |
|------|------|--------|
| `gateway/internal/shed/latency.go` | ring buffer lockless | Novel — hand-rolled per RESEARCH Pattern 2. Sem analog no repo (nenhuma feature atual precisa de P95 in-process). |
| `gateway/db/migrations/0018_audit_log_shed_values.sql` | conditional ENUM ADD | Depends on current `audit_log.upstream` column type. Se TEXT, só é doc-only (sem DDL). Plan deve verificar durante execute. |

**DCGM scraper** tem analog parcial (`upstreams/probe.go` shape goroutine+ticker), mas parser `expfmt` e fail-open são novos. Documentado como "role-match" acima, não como "no-analog".

## Metadata

**Analog search scope:** `gateway/internal/breaker/`, `gateway/internal/upstreams/`, `gateway/internal/tenants/`, `gateway/internal/schedule/`, `gateway/internal/quota/`, `gateway/internal/proxy/`, `gateway/internal/obs/`, `gateway/internal/redisx/`, `gateway/internal/config/`, `gateway/internal/auditctx/`, `gateway/internal/httpx/`, `gateway/internal/integration_test/`, `gateway/cmd/gateway/`, `gateway/cmd/gatewayctl/`, `gateway/db/migrations/`, `gateway/db/queries/`.

**Files scanned/read:** 22 (breaker.go, mirror.go, subscribe.go, errors.go, redisx/breaker.go, upstreams/types.go, upstreams/loader.go, upstreams/listen.go, tenants/config.go, tenants/loader.go, tenants/listen.go, schedule/middleware.go, schedule/policy.go, proxy/dispatcher.go, auditctx/override.go, obs/metrics.go, obs/middleware.go, config/config.go, httpx/envelope.go, cmd/gateway/main.go, cmd/gatewayctl/upstreams.go, cmd/gatewayctl/tenant.go, db/migrations/0013_*.sql, db/migrations/0008_*.sql, db/migrations/0009_*.sql, db/queries/tenants_admin.sql, integration_test/setup_test.go, integration_test/breaker_state_machine_test.go, redisx/client.go, quota/enforcer.go, breaker/breaker_test.go).

**Pattern extraction date:** 2026-04-23.
