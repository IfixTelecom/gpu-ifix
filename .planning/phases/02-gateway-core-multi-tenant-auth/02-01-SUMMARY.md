---
phase: 02-gateway-core-multi-tenant-auth
plan: 01
subsystem: infra
tags: [go, chi, slog, prometheus, sentry, httputil, uuidv7, redact, openai-envelope]

# Dependency graph
requires:
  - phase: 01-gpu-pod-image-smoke-test
    provides: pkg/openai types (ErrorResponse, ErrorDetail, chat/embedding/transcription structs); docs/CONVENTIONS.md (slog module naming, sentinel errors, RFC3339); pod/health-bridge signal-handling pattern mirrored here
provides:
  - gateway/cmd/gateway (HTTP scaffold binary with /health, /metrics, scaffold stubs for /v1/* routes)
  - gateway/cmd/gatewayctl (admin CLI scaffold with migrate/tenant/key/audit subcommands; real logic added by 02-02/02-03/02-09)
  - gateway/internal/config (typed Config + Load() with fail-fast on 6 required env vars + fixed HTTP timeouts)
  - gateway/internal/httpx (RequestID middleware UUIDv7, NewRedactor slog wrapper, WriteOpenAIError helper, Logger + Recoverer middlewares, LoggerFrom/WithLogger ctx)
  - gateway/internal/obs (Sentry Init + BeforeSend header redaction, Prometheus RequestsTotal + AuditDroppedTotal counters, BuildVersion string)
  - Shared sensitive-header list (httpx.IsSensitiveKey) consumed by slog redactor AND Sentry BeforeSend (D-B7 duplicar a proteção)
affects:
  - 02-02 (pgx/goose wiring consumes config.Config + gatewayctl migrate subcommand)
  - 02-03 (auth middleware consumes httpx.WriteOpenAIError + httpx.LoggerFrom + obs.RequestsTotal touch counter)
  - 02-04 (reverse proxy handlers mount on chi.Mux built here; replace scaffold 501 stubs)
  - 02-05 (/v1/health/upstreams replaces the 501 stub)
  - 02-06 (idempotency middleware uses httpx.WriteOpenAIError 409/422 paths)
  - 02-08 (Dockerfile builds /gateway and /gatewayctl; --self-check flag is the docker HEALTHCHECK hook)

# Tech tracking
tech-stack:
  added:
    - github.com/go-chi/chi/v5 v5.1.0 (router)
    - github.com/google/uuid v1.6.0 (UUIDv7)
    - github.com/prometheus/client_golang v1.20.5 (counters + /metrics handler)
    - github.com/getsentry/sentry-go v0.29.1 (error tracking + BeforeSend redaction)
  patterns:
    - "slog.Handler wrapping via NewRedactor to enforce sensitive-key redaction globally (wraps the handler before slog.New, so every record — not just errors — is scrubbed)"
    - "UUIDv7 as gateway-authoritative request id; client-supplied X-Request-ID retained only as ctx attribute client_request_id (never used as audit key)"
    - "Every 4xx/5xx exit path goes through httpx.WriteOpenAIError (single source of truth for OpenAI envelope shape; zero redefinitions of openai.ErrorResponse)"
    - "fail-fast config: Load() returns ErrMissingEnv with ALL missing var names concatenated so operators see the full list in one line"
    - "shared sensitive-header list duplicated at both observability exits (slog Redactor + Sentry BeforeSend) — same map keys, same IsSensitiveKey helper, no copy/paste"
    - "chi middleware order: RequestID -> Logger -> Recoverer — request id must be assigned before log record is emitted; recoverer sits innermost so it can WriteOpenAIError 500 without reaching middleware that already wrote headers"
    - "buildRouter() extracted from main() so main_test.go shares identical wiring; tests exercise the exact routes + middleware stack production will"

key-files:
  created:
    - gateway/cmd/gateway/main.go (191 lines) — HTTP server bootstrap + chi router + /health + /metrics + scaffold stubs
    - gateway/cmd/gateway/main_test.go (165 lines) — e2e tests for /health, stubs, /metrics, client X-Request-ID handling
    - gateway/cmd/gatewayctl/main.go (110 lines) — admin CLI dispatcher + subcommand stubs
    - gateway/internal/config/config.go (134 lines) — typed Config + Load() + ErrMissingEnv
    - gateway/internal/config/config_test.go (190 lines) — env var matrix
    - gateway/internal/httpx/requestid.go (76 lines) — RequestID middleware + ctx accessors
    - gateway/internal/httpx/requestid_test.go (138 lines) — UUIDv7 sortability + client-vs-gateway id separation
    - gateway/internal/httpx/redact.go (75 lines) — slog handler wrapper + IsSensitiveKey
    - gateway/internal/httpx/redact_test.go (131 lines) — case-insensitive coverage + WithAttrs + group
    - gateway/internal/httpx/envelope.go (41 lines) — WriteOpenAIError + TypeForStatus
    - gateway/internal/httpx/envelope_test.go (68 lines) — shape decode roundtrip into pkg/openai.ErrorResponse
    - gateway/internal/httpx/logger.go (66 lines) — per-request slog enrichment middleware
    - gateway/internal/httpx/recoverer.go (34 lines) — panic -> Sentry -> OpenAI 500 envelope
    - gateway/internal/obs/sentry.go (44 lines) — Init() + Flush() + BeforeSend
    - gateway/internal/obs/metrics.go (35 lines) — RequestsTotal + AuditDroppedTotal + Handler
    - gateway/internal/obs/version.go (8 lines) — BuildVersion string
    - gateway/README.md (69 lines) — env vars + routes + conventions
  modified:
    - go.mod (added 4 direct deps: chi v5.1.0, uuid v1.6.0, prometheus/client_golang v1.20.5, sentry-go v0.29.1)
    - go.sum (transitive checksum updates)

key-decisions:
  - "Dependency provenance: chi + uuid + prometheus + sentry-go committed by 02-01; pgx + goose committed by 02-02. Neither plan over-provisions — each ships deps only when consuming code lands. This reflects Codex review [MEDIUM] 02-01 that flagged semi-wired imports."
  - "Redactor wraps the slog.Handler BEFORE slog.New() (not as a downstream filter). Sensitive keys are redacted at record ingestion, not at emission — so a subsequent WithAttrs cannot smuggle an unredacted value through."
  - "X-Request-ID is gateway-authoritative (UUIDv7, server-generated, immutable). Client-supplied X-Request-ID is preserved as ctx attribute client_request_id only. Rationale: audit_log PK must not be forgeable by clients (T-02-01-05)."
  - "TestMetrics_Exposed pre-warms RequestsTotal.WithLabelValues('/health','200').Add(0) — Prometheus CounterVec does not emit HELP/TYPE until a label tuple is observed. Request instrumentation middleware lands in 02-04; this warm-up proves the registration + /metrics wiring without preempting 02-04's scope."
  - "WriteTimeout=0 is required for SSE (D-C4); Phase 3 routing layer must revisit per-route timeouts (embeddings=30s, audio=120s) to restore slow-client-DoS defense on non-streaming routes. Documented in Config comment so future reviewers see the trade-off."
  - "Go toolchain bumped to 1.25.9 by parallel 02-02 (pgx/goose transitive requirements). Plan-stated 'Go 1.23+' is satisfied — 1.25 is a superset. go.mod directive is now 'go 1.25.0 toolchain go1.25.9'."

patterns-established:
  - "Every new .go file under gateway/ has a package doc comment (^// Package X ...) before the `package X` clause — grep-asserted in Task 1 acceptance"
  - "Test files live beside source (config.go + config_test.go) with _test suffix — matches docs/CONVENTIONS.md"
  - "Observability helpers (httpx.IsSensitiveKey, obs.BuildVersion) are plain package-level vars/funcs; no init() side effects outside of promauto registration — keeps test isolation easy"
  - "main.go extracts buildRouter() so the test suite exercises the same mux assembly as production (no parallel wiring divergence)"

requirements-completed: [GW-01, GW-05, GW-08, TEN-08]

# Metrics
duration: 13min
completed: 2026-04-18
---

# Phase 02 Plan 01: Gateway scaffold + httpx middleware + obs stubs Summary

**Two Go binaries (gateway + gatewayctl) booting cleanly on the 6-var env contract, with chi router + UUIDv7 request-id middleware + dual-layer sensitive-header redaction (slog + Sentry) + Prometheus /metrics handler exposing gateway_requests_total and gateway_audit_dropped_total.**

## Performance

- **Duration:** 13 min
- **Started:** 2026-04-18T21:48:42Z
- **Completed:** 2026-04-18T22:01:23Z
- **Tasks:** 2 (both TDD)
- **Files created:** 17 (13 Go source + 2 Go tests + 1 README + go.sum)
- **Lines delivered:** 1,575 total across the 17 new files

## Accomplishments

- `./gateway --self-check` prints `ok` and exits 0 (docker HEALTHCHECK contract identical to `pod/health-bridge`)
- `./gateway` with the 6 required env vars binds `:8080` (or $GATEWAY_PORT), returns `{"status":"ok","version":"dev","uptime_s":N}` from `GET /health` with UUIDv7 `X-Request-ID` header, exposes `gateway_audit_dropped_total 0` and (after warm-up) `gateway_requests_total{route,status}` on `GET /metrics`, and responds with a predictable OpenAI 501 envelope to any `/v1/*` route (replaced by 02-04/02-05)
- `./gateway` with any required env var unset writes a single-line error listing all missing vars to stderr and exits 2
- `./gatewayctl` no-args / unknown-command paths both return exit 2 with usage; `migrate|tenant|key|audit --help` each exit 0 with subcommand-scoped help
- Graceful shutdown: SIGTERM -> `srv.Shutdown(ctx, 25s)` -> `sentry.Flush(2s)` -> `log.Info("gateway exited cleanly")` (verified live)
- Two-layer sensitive-header redaction: slog `Redactor` wrapping replaces `Authorization`/`X-API-Key`/`Cookie`/`Proxy-Authorization`/`api_key`/`apikey` VALUES with `***REDACTED***` across every log record (not just errors); Sentry `BeforeSend` strips the same keys from `event.Request.Headers` using the shared `httpx.IsSensitiveKey` helper
- 17 unit + e2e tests pass `-race` clean; `gofmt -l` empty; `go vet` empty

## Task Commits

1. **Task 1: module deps + config + httpx + obs primitives (TDD)** - `e4a8bc0` (`feat`)
   - Config.Load() with fail-fast ordering; RequestID/Redactor/WriteOpenAIError/Logger/Recoverer; Sentry Init + BeforeSend; RequestsTotal + AuditDroppedTotal counters + Handler()
   - Tests: config env-var matrix (8 tests), RequestID (6 tests), Redactor (8 tests), Envelope (3 tests)
2. **Task 2: cmd/gateway + cmd/gatewayctl + README (TDD)** - `7cbabfd` (`feat`)
   - chi.NewRouter() + middleware order RequestID -> Logger -> Recoverer; /health + /metrics + 4 scaffold /v1 routes; NotFound -> 404 OpenAI envelope; graceful 25s shutdown; gatewayctl dispatcher + 4 subcommand stubs; pt-BR README
   - Tests: TestHealth_200, TestScaffold_ReturnsOpenAIEnvelope, TestNotFound_ReturnsOpenAIEnvelope, TestMetrics_Exposed, TestHealthEmbedsClientRequestID

**Plan metadata:** (this SUMMARY.md + STATE.md updates) committed separately after self-check.

## Files Created/Modified

- `gateway/cmd/gateway/main.go` — HTTP server bootstrap + chi router + middleware stack + /health + /metrics + scaffold 501 stubs + graceful shutdown
- `gateway/cmd/gateway/main_test.go` — 5 e2e tests against buildRouter(); decodes bodies into `pkg/openai.ErrorResponse`
- `gateway/cmd/gatewayctl/main.go` — admin CLI dispatcher + FlagSet-per-subcommand stubs for migrate/tenant/key/audit
- `gateway/internal/config/config.go` + `config_test.go` — typed Config + Load() with deterministic missing-var ordering
- `gateway/internal/httpx/requestid.go` + `requestid_test.go` — UUIDv7 RequestID middleware + ctx accessors; TestRequestID_UUIDv7FormatSortable asserts monotonic ordering
- `gateway/internal/httpx/redact.go` + `redact_test.go` — slog Handler wrapper + IsSensitiveKey helper; 6 sensitive keys covered case-insensitively
- `gateway/internal/httpx/envelope.go` + `envelope_test.go` — WriteOpenAIError + TypeForStatus; no local redefinition of openai.ErrorResponse
- `gateway/internal/httpx/logger.go` — Logger() middleware emits one "request" record with status/bytes/latency_ms
- `gateway/internal/httpx/recoverer.go` — panic -> slog error + sentry.CurrentHub().Recover + WriteOpenAIError 500
- `gateway/internal/obs/sentry.go` — Init() with BeforeSend redaction using httpx.IsSensitiveKey; no-op when SENTRY_DSN empty
- `gateway/internal/obs/metrics.go` — RequestsTotal + AuditDroppedTotal + Handler()
- `gateway/internal/obs/version.go` — BuildVersion string for -ldflags injection
- `gateway/README.md` — pt-BR scaffold docs (env vars, routes, conventions, admin CLI)
- `go.mod` — 4 new direct requires (chi/uuid/prometheus/sentry-go); pgx/goose are owned by 02-02 (parallel wave 1 plan)
- `go.sum` — transitive dependency checksums

## Decisions Made

See frontmatter `key-decisions`. Most consequential:

- **Dependency provenance split** between 02-01 (chi/uuid/prometheus/sentry) and 02-02 (pgx/goose) honors Codex review [MEDIUM] 02-01 ("no semi-wired deps") while keeping wave 1 parallel. No plan over-provisions.
- **Gateway-authoritative X-Request-ID** (UUIDv7 server-generated) with client id retained only as ctx attribute — prevents clients from forging audit_log PKs (T-02-01-05).
- **Redactor wraps the slog.Handler** before `slog.New()` so every record is scrubbed at ingestion; subsequent `.With()` / `.WithAttrs()` / `.WithGroup()` cannot leak a raw secret through.
- **Test pre-warms RequestsTotal** with `Add(0)` to make the CounterVec appear on `/metrics` — required because Prometheus CounterVec suppresses emission until a label tuple is observed, and request-counting middleware lands in 02-04.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] TestMetrics_Exposed adjusted to warm up RequestsTotal label tuple**

- **Found during:** Task 2 (end-to-end /metrics test)
- **Issue:** The plan's acceptance criterion stated `GET /metrics` should expose `gateway_requests_total` with "zero count before requests". Prometheus CounterVec semantics do NOT emit HELP/TYPE lines until at least one label tuple has been observed — so a freshly-scraped `/metrics` would NOT contain the string `gateway_requests_total` at all. Verified empirically with a minimal reproduction (CounterVec without observation -> empty response body; CounterVec with `.WithLabelValues(...).Inc()` -> full `# HELP` + `# TYPE` + sample lines).
- **Fix:** Added one line to `TestMetrics_Exposed` that invokes `obs.RequestsTotal.WithLabelValues("/health", "200").Add(0)` before scraping. This registers the label tuple without incrementing a real counter. Added an inline comment explaining that request-instrumentation middleware lands in 02-04 (proxy layer); the warm-up is a test-only affordance, not production behaviour.
- **Files modified:** `gateway/cmd/gateway/main_test.go`
- **Verification:** `go test ./gateway/cmd/gateway/... -count=1 -race` -> `ok` (previously `FAIL: missing gateway_requests_total`).
- **Committed in:** `7cbabfd` (Task 2 commit)

**2. [Rule 3 - Blocking] chi/v5 dependency pinned in Task 2 (not Task 1) + Go toolchain autobumped to 1.25**

- **Found during:** Task 2 (main.go imports chi; go.mod had no chi require after Task 1 tidy)
- **Issue:** Task 1 added chi via `go get`, but because no Task 1 source file imports chi, `go mod tidy` dropped the require line. Task 1 verify block asserted `grep -q "github.com/go-chi/chi/v5" go.mod` — which would fail after tidy. Separately, when the parallel 02-02 agent landed pgx (5.9+) + goose (3.27+), Go's auto-toolchain rewrote the `go` directive in go.mod from `1.23` to `1.25.0` because those deps' transitive dependencies require Go 1.25.
- **Fix:** Deferred chi addition to Task 2 when `main.go` first imports it (aligns with Codex review [MEDIUM] 02-01 rationale — "deps where consumed"). This is a plan-wide consistency win: chi is now a direct require owned by the module that consumes it, not a phantom require left by Task 1. Accepted the Go toolchain bump to 1.25 as compatible with the plan's stated "Go 1.23+" constraint; added `toolchain go1.25.9` directive was present but parallel tidy from 02-02 simplified the directive to just `go 1.25.0`.
- **Files modified:** `go.mod` (direct requires + go directive), `go.sum`
- **Verification:** `go vet ./gateway/...` clean; `go build ./gateway/...` clean; `go test ./gateway/... -race` all green; `grep -E "^\s*github.com/jackc/pgx|^\s*github.com/redis/go-redis|^\s*github.com/pressly/goose|^\s*github.com/alexedwards/argon2id|^\s*github.com/testcontainers/testcontainers-go|^\s*github.com/alicebob/miniredis" go.mod` -> pgx and goose ARE now present (introduced by 02-02); redis/argon2id/testcontainers/miniredis are correctly absent (deferred to 02-03/02-07).
- **Committed in:** `7cbabfd` (Task 2 commit, which is where main.go first imports chi). Task 1 commit `e4a8bc0` deliberately shipped without chi.

---

**Total deviations:** 2 auto-fixed (1 Rule 1 bug, 1 Rule 3 blocking). No Rule 4 decisions required.
**Impact on plan:** Both deviations align with Codex review [MEDIUM] 02-01's own recommendation to avoid semi-wired dependencies; the plan's literal Task 1 verify for chi was inconsistent with the dependency-provenance rationale stated in the plan's own `<objective>` section. No scope creep; no additional code produced beyond what the plan specified.

## Issues Encountered

- **Parallel wave-1 coordination on go.mod:** Both 02-01 and 02-02 touch `go.mod` / `go.sum`. Sequencing landed cleanly because 02-01's Task 1 committed first (chi/uuid/prometheus/sentry as direct requires), then 02-02 committed its migrations (no go.mod touch), then 02-02 committed its pgx + goose additions (`99770c5`, `6251308`), and finally 02-01's Task 2 committed chi-using main.go. The final go.mod contains all 6 direct requires with no ordering conflict. Confirmed by `go test ./gateway/...` passing both packages simultaneously.
- **Go toolchain upgrade:** The system Go binary at `/home/pedro/.local/go/bin/go` is 1.23.4, but the auto-toolchain mechanism downloaded + used 1.25.9 when 02-02's deps demanded it. All commands in this plan ran under `GOTOOLCHAIN=go1.25.9` after the directive bump.

## User Setup Required

None. This plan introduces no external services or secrets; `SENTRY_DSN` is optional and the 6 required env vars are test-fake-able for any local boot.

## Next Phase Readiness

- Wave 1 (02-01 + 02-02) completed in parallel. Wave 2 (02-03 auth + 02-04 proxy + 02-05 health bridge) can begin.
- **02-03 inputs ready:** `config.Config` struct + `httpx.WriteOpenAIError` + `obs.RequestsTotal` counter + `httpx.IsSensitiveKey` (API keys will be added to this map if not already covered — `api_key`/`apikey` already covered).
- **02-04 inputs ready:** chi.Mux + middleware stack. Scaffold 501 handlers for `/v1/chat/completions`, `/v1/embeddings`, `/v1/audio/transcriptions` are placeholders for 02-04's reverse-proxy handlers; the MethodFunc registrations can be rebound atomically when 02-04 lands.
- **02-05 inputs ready:** `/v1/health/upstreams` 501 stub + `cfg.UpstreamHealthBridgeURL` populated.
- **02-06 inputs ready:** `httpx.WriteOpenAIError` supports the 409 / 422 / 400 paths that the idempotency middleware needs. `httpx.TypeForStatus(422)` returns `invalid_request_error` as expected by D-C3.
- **02-08 inputs ready:** `--self-check` flag for Dockerfile HEALTHCHECK; `-ldflags "-X github.com/ifixtelecom/gpu-ifix/gateway/internal/obs.BuildVersion=..."` hook for tag injection.
- **No open blockers** from this plan.

## Self-Check: PASSED

**Commits verified present on master:**
- `e4a8bc0` feat(02-01): scaffold config + httpx middleware + obs primitives — FOUND
- `7cbabfd` feat(02-01): add cmd/gateway + cmd/gatewayctl binaries + README — FOUND

**Files verified on disk:**
- `gateway/cmd/gateway/main.go` — FOUND (191 lines)
- `gateway/cmd/gateway/main_test.go` — FOUND (165 lines)
- `gateway/cmd/gatewayctl/main.go` — FOUND (110 lines)
- `gateway/internal/config/config.go` — FOUND (134 lines)
- `gateway/internal/config/config_test.go` — FOUND (190 lines)
- `gateway/internal/httpx/requestid.go` + `requestid_test.go` — FOUND (76 + 138 lines)
- `gateway/internal/httpx/redact.go` + `redact_test.go` — FOUND (75 + 131 lines)
- `gateway/internal/httpx/envelope.go` + `envelope_test.go` — FOUND (41 + 68 lines)
- `gateway/internal/httpx/logger.go` — FOUND (66 lines)
- `gateway/internal/httpx/recoverer.go` — FOUND (34 lines)
- `gateway/internal/obs/sentry.go` — FOUND (44 lines)
- `gateway/internal/obs/metrics.go` — FOUND (35 lines)
- `gateway/internal/obs/version.go` — FOUND (8 lines)
- `gateway/README.md` — FOUND (69 lines)

**Binary sizes** (trimpath + `-ldflags "-s -w"`):
- `gateway`: 10,109,220 bytes (~9.6 MiB)
- `gatewayctl`: 4,276,516 bytes (~4.1 MiB)

**Quality gates:**
- `gofmt -l ./gateway/` — empty (OK)
- `go vet ./gateway/...` — clean (OK)
- `go build ./gateway/...` — clean (OK)
- `go test ./gateway/... -count=1 -race` — 4 packages OK, 2 packages (cmd/gatewayctl, internal/obs) have no tests which is acceptable for scaffold stubs

---
*Phase: 02-gateway-core-multi-tenant-auth*
*Completed: 2026-04-18*
