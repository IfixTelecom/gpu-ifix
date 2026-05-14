# Phase 7: Observability — Dashboard & Alerting - Context

**Gathered:** 2026-05-14
**Status:** Ready for planning

<domain>
## Phase Boundary

Operators can see — in one place — how the gateway is behaving per tenant, get paged when it matters, and have an audit trail for every significant state change. Delivers OBS-01..OBS-08: `/admin/metrics` JSON, `/metrics` Prometheus (cardinality-bounded), a Next.js dashboard, severity-tiered alerting (Chatwoot + ClickUp + Brevo email), alert deduplication, audit_log coverage of state changes, and Sentry redaction.

Out of this phase: client app integrations (Phase 8/9), production load/chaos testing and DNS/TLS (Phase 10), SSO hardening (Phase 10 PRD-06).

</domain>

<decisions>
## Implementation Decisions

### Dashboard — Scope, Hosting & Auth
- Dashboard lives as a new `dashboard/` app in this monorepo — deployed alongside the gateway, single GitHub webhook (standard Ifix Docker Compose + Portainer flow).
- Auth: standalone **Better Auth** instance, configured following the **converseai-v4 pattern** (same `better-auth` library + conventions/plugins as converseai-v4). ~4 Ifix admins. Its own instance — not a shared session with converseai-v4.
- Data source: the dashboard reads the gateway's `/admin/metrics` JSON endpoint + `audit_log` via the gateway admin API (admin-key auth — the `/admin` sub-router + admin-key middleware already exist from Phase 4).
- Live updates: polling via React Query (`refetch` every 5–10s). SC-1 only requires "live" — polling is the simplest sufficient mechanism.
- UI stack follows the converseai-v4 standard: Next.js 15 (App Router), shadcn, Recharts.

### Alerting — Provider, Channels, Severity
- Critical alert delivery: **Chatifix/Chatwoot API** (WhatsApp message) — NOT Evolution API direct.
- Critical + warning alerts also **open a task in ClickUp**. ClickUp integration reuses the resilient SDK pattern from `cobrancas-api` (rate-limiter + circuit-breaker + retry).
- Email: **Brevo SMTP** (standard Ifix).
- Alerting logic runs **in the gateway (Go)** as a goroutine consuming the Redis Pub/Sub events that already flow (breaker events, FSM transitions) — no separate alerter service.
- Deduplication (OBS-06): a Redis key with a 5-minute TTL per alert fingerprint.
- Channel matrix by severity tier:
  - **critical**: Chatwoot message + ClickUp task + email
  - **warning**: ClickUp task + email
  - **info**: dashboard banner only

### Metrics, Prometheus & Audit Coverage
- `/admin/metrics` JSON (OBS-01): a new handler on the existing `/admin` sub-router (Phase 4) behind admin-key middleware. Returns aggregated JSON — P50/P95/P99 per route + upstream, error rate, inflight, saturation, current FSM state.
- Prometheus cardinality (OBS-02): audit the existing collectors in `gateway/internal/obs/metrics.go`; ensure every label is bounded (tenant_id ~6, route, status) and there are zero unbounded labels (no `request_id`). Target: ≤10k active series.
- Audit log (OBS-07): extend the existing `gateway/internal/audit/writer.go` with explicit rows for FSM transitions, tenant activate/deactivate, pod spin-up/shutdown, and threshold changes. Reuse the existing writer + `audit_log` table — no new table.
- Sentry redaction (OBS-08): verify and extend the `BeforeSend` hook in `gateway/internal/obs/sentry.go` to scrub `authorization`, `x-api-key`, and request/response bodies.

</decisions>

<code_context>
## Existing Code Insights

### Reusable Assets
- `gateway/internal/obs/` — `metrics.go` (Prometheus collectors via promauto + `promhttp.Handler()`), `sentry.go`, `middleware.go`, `version.go`. `/metrics` route already wired in `gateway/cmd/gateway/main.go:842` (unauthenticated).
- `gateway/internal/audit/` — `writer.go` + `middleware.go` already write `audit_log` rows; `auditctx/` carries audit context (incl. shed + override helpers from Phases 4–5).
- `/admin` sub-router + admin-key middleware — added in Phase 4 (`04-06`/`04-07`); `/admin/metrics` JSON attaches here.
- Redis Pub/Sub event streams already published: `gw:breaker:events` (Phase 3), FSM transition events (Phases 5–6). The alerting goroutine subscribes to these.
- ClickUp SDK pattern in `cobrancas-api/src/lib/clickup/` (separate repo — reference for the resilient client: AdaptiveRateLimiter, CircuitBreaker, retry).
- converseai-v4 `better-auth` setup (separate repo — reference for the dashboard auth configuration).

### Established Patterns
- Go gateway: chi v5 + `httputil.ReverseProxy` + slog; config via env vars validated at boot.
- Redis Pub/Sub for cross-component events + Redis mirror for hot state.
- Deploy: Docker Compose + Portainer + GitHub webhook; images `ghcr.io/ifixtelecom/...`.
- Next.js 15 + shadcn + Recharts is the converseai-v4 dashboard standard.
- External API clients follow the resilient-client pattern (rate-limit, circuit-breaker, retry, structured logging).

### Integration Points
- Dashboard ↔ gateway admin API (`/admin/metrics` JSON + audit_log read endpoint, admin-key auth).
- Alerting goroutine ↔ Redis Pub/Sub (breaker events, FSM transitions).
- Alerting ↔ external APIs: Chatwoot, ClickUp, Brevo SMTP.
- Audit writes ↔ `audit_log` Postgres table (shared DO cluster, `ai_gateway` schema).
- Sentry ↔ gateway panics / circuit trips / provisioning failures.

</code_context>

<specifics>
## Specific Ideas

- Dashboard UI follows the converseai-v4 standard exactly: Next.js 15 App Router, shadcn components, Recharts for charts.
- Better Auth must be configured "like converseai" — same library and plugin conventions as converseai-v4's auth package.
- ClickUp task creation should reuse `cobrancas-api`'s resilient SDK approach (rate-limiter + circuit-breaker + retry), not a naive HTTP call.
- Chatwoot is the delivery path for WhatsApp alerts (the Chat Ifix / Chatwoot system), reached via its API.

</specifics>

<deferred>
## Deferred Ideas

- **SSO for the dashboard** — deferred to Phase 10 (PRD-06). Phase 7 ships Better Auth email/password; SSO is the production-hardening upgrade.
- **02-09 cold-storage audit export** (Parquet → MinIO + retention DROP) — carried over from Phase 2 as optional. Deferral condition (audit_log > ~60 days of production traffic) is not met — production integrations are Phases 8–9. Re-evaluate in Phase 10 or once real traffic accumulates; not in Phase 7 scope.

</deferred>
