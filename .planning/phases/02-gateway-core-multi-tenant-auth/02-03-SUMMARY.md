---
phase: 2
plan: 03
subsystem: gateway-auth
tags: [go, argon2id, redis, sha256, debounce, chi, openai-envelope, sqlc, gatewayctl]
status: complete
wave: 2
autonomous: true
requirements: [TEN-01, TEN-02, TEN-08]
dependency_graph:
  requires:
    - "Plan 02-01 (config.Config, httpx.WriteOpenAIError, httpx.LoggerFrom, httpx.NewRedactor, obs counters)"
    - "Plan 02-02 (db.NewPool, db.Up/Down/Status, db.EnsurePartitions, sqlc queries with key_lookup_hash)"
  provides:
    - "gateway/internal/auth.Verifier (chi-compatible Middleware + AuthContext propagation)"
    - "gateway/internal/auth.GenerateAPIKey (raw, argon2id-hash, sha256-lookup, prefix)"
    - "gateway/internal/auth.LookupHash (SHA-256 helper exported for backfill scripts)"
    - "gateway/internal/auth.TouchBuffer (debounced last_used_at updater)"
    - "gateway/internal/auth.AuthContext{TenantID, APIKeyID, DataClass, KeyPrefix} + FromContext/MustFromContext/WithContext"
    - "gateway/internal/auth.{ErrMissingAPIKey, ErrInvalidAPIKey, ErrRevokedAPIKey, ErrMalformedKey}"
    - "gateway/internal/redisx.NewClient (fail-fast 2s Ping)"
    - "gatewayctl migrate up|down|status, tenant create, key create, key revoke (D-A3 admin surface)"
    - "obs.ApikeyTouchBufferedTotal + obs.ApikeyTouchFlushTotal Prometheus counters"
    - "gateway/internal/db/gen/* sqlc-generated code (CreateTenant, GetActiveKeyByLookupHash, InsertAPIKey, RevokeAPIKey, TouchKeyLastUsed, ListTenants, GetTenantBySlug, GetAPIKeyByID, ListActiveKeysAll, ListActiveKeysByTenant, ListModelAliases, GetModelAlias, InsertAuditLogContent)"
  affects:
    - "02-04 (proxy) imports auth.AuthContext via FromContext to route per-tenant"
    - "02-05 (audit) consumes auth.DataClass to gate audit_log_content writes"
    - "02-06 (idempotency) uses auth.AuthContext.TenantID as Redis namespace prefix"
    - "02-07 (testcontainers) covers full migrate→tenant→key→verify integration flow"
    - "02-08 (Dockerfile) ships gatewayctl alongside gateway"
tech_stack:
  added:
    - "github.com/alexedwards/argon2id v1.0.0 — direct dep (gateway/internal/auth)"
    - "github.com/redis/go-redis/v9 v9.18.0 — direct dep (gateway/internal/redisx + cache.go)"
    - "github.com/alicebob/miniredis/v2 v2.37.0 — test-only dep (cache_test, apikey_test, redisx_test, load_test)"
  patterns:
    - "Hot path 0-1 argon2 verify: SHA-256 indexed lookup → ≤1 row → ≤1 argon2id.ComparePasswordAndHash. ListActiveKeysAll never called on hot path (grep-asserted)."
    - "Two-tier Redis cache: positive (60s, gw:apikey:<sha256hex>) + negative (5s, gw:apikey:neg:<sha256hex>). Negative cache is the formalized D-A2 amendment from Codex review [HIGH] 02-03 — kills brute-force CPU exhaustion."
    - "Debounced TouchBuffer (Codex [MEDIUM] 02-03) coalesces last_used_at writes per api_key_id. PendingCount() exposed for tests; Run(ctx) returns on ctx cancel after a final drain so SIGTERM still flushes."
    - "Test-injection via authQueries interface: production wraps gen.Queries; tests use a fake with atomic counters to assert hot-path call counts."
    - "Constant-time malformed reject via IsWellFormedKey before any DB or argon2 call (T-02-03-07 DoS mitigation)."
    - "Authorization > X-API-Key precedence (D-A5) enforced inside ExtractKey — not in the middleware — so gatewayctl + future SDK can reuse the same precedence rule."
    - "Raw API key printed exactly once on stdout by gatewayctl key create; never passed as a slog attribute (T-02-03-04)."
    - "buildRouter signature widened to take *auth.Verifier (nil-safe so existing scaffold tests stay valid). nil verifier = passthrough; production main always supplies a real one."
key_files:
  created:
    - "gateway/internal/auth/errors.go (40 LOC)"
    - "gateway/internal/auth/context.go (39 LOC)"
    - "gateway/internal/auth/argon2.go (108 LOC)"
    - "gateway/internal/auth/argon2_test.go (133 LOC)"
    - "gateway/internal/auth/cache.go (102 LOC)"
    - "gateway/internal/auth/cache_test.go (126 LOC)"
    - "gateway/internal/auth/apikey.go (248 LOC)"
    - "gateway/internal/auth/apikey_test.go (498 LOC)"
    - "gateway/internal/auth/touch_buffer.go (110 LOC)"
    - "gateway/internal/auth/touch_buffer_test.go (123 LOC)"
    - "gateway/internal/auth/load_test.go (80 LOC, build tag `load`)"
    - "gateway/internal/redisx/client.go (34 LOC)"
    - "gateway/internal/redisx/client_test.go (42 LOC)"
    - "gateway/cmd/gatewayctl/migrate.go (65 LOC)"
    - "gateway/cmd/gatewayctl/tenant.go (53 LOC)"
    - "gateway/cmd/gatewayctl/key.go (150 LOC)"
    - "gateway/cmd/gatewayctl/main_test.go (97 LOC)"
    - "gateway/internal/db/gen/admin.sql.go (sqlc generated)"
    - "gateway/internal/db/gen/audit.sql.go (sqlc generated)"
    - "gateway/internal/db/gen/auth.sql.go (sqlc generated)"
    - "gateway/internal/db/gen/db.go (sqlc generated)"
    - "gateway/internal/db/gen/model_aliases.sql.go (sqlc generated)"
    - "gateway/internal/db/gen/models.go (sqlc generated)"
    - "gateway/internal/db/gen/querier.go (sqlc generated)"
  modified:
    - "gateway/cmd/gateway/main.go (added pgxpool + redis + verifier + TouchBuffer + EnsurePartitions wiring; chi.Group for /v1/* under auth.Middleware)"
    - "gateway/cmd/gateway/main_test.go (buildRouter signature widened to accept *auth.Verifier; nil keeps existing scaffold tests valid)"
    - "gateway/cmd/gatewayctl/main.go (replaced Plan 02-01 stubs with real dispatcher; subcommand handlers return int exit codes)"
    - "gateway/internal/obs/metrics.go (+2 counters: gateway_apikey_touch_buffered_total + gateway_apikey_touch_flush_total)"
    - "go.mod (+2 direct deps: alexedwards/argon2id v1.0.0, redis/go-redis/v9 v9.18.0; +1 test dep: alicebob/miniredis/v2 v2.37.0)"
    - "go.sum (transitive lockfile updates)"
decisions:
  - "Hot-path call counters live on the fakeQueries struct in tests (atomic int64) so we can assert ZERO db calls on malformed keys, ONE db call on first valid verify, and STILL ONE on the second verify (cache hit). This is the closure gate for Codex review [HIGH] 02-03."
  - "TouchBuffer flushFn is wired with `gen.New(pool).TouchKeyLastUsed` per call — gen.New is cheap (just a struct init) and the alternative (a long-lived gen.Queries on the verifier) couples auth tighter than necessary to db/gen."
  - "buildRouter was extended (not duplicated) to accept *auth.Verifier; nil-safe pathway preserves the Plan 02-01 scaffold tests verbatim. This avoids forking test wiring or stubbing pgx in unit tests."
  - "Status + DataClass come back from sqlc as `interface{}` because the migrations declare ai_gateway.api_key_status / ai_gateway.data_class as Postgres ENUMs and sqlc has no override hint for them. enumString() coerces both string and []byte (depending on pgx protocol path) to a Go string before populating cacheEntry."
  - "miniredis v2.37.0 was picked up by `go mod tidy` (newer than the plan-suggested v2.33.0) — both versions expose the same Run/FastForward/Addr API used by the tests. No version-pin needed."
  - "Plan 02-02 deferred main.go wiring of EnsurePartitions to this plan; we honored that handoff and call it AFTER the optional AI_GATEWAY_MIGRATE_ON_BOOT block so partitions only land if the parent partitioned tables exist."
metrics:
  duration_minutes: 22
  completed_date: 2026-04-18
  tasks_completed: 2
  files_created: 18
  files_modified: 5
  commits: 2
  tests_added: 25
  tests_passing: 25
---

# Phase 2 Plan 03: Auth middleware + gatewayctl tenant/key/migrate Summary

**Multi-tenant API key auth (TEN-01/02/08) with argon2id + SHA-256 indexed lookup + 60s positive / 5s negative Redis cache + debounced TouchBuffer + chi-Group middleware on /v1/*, plus the gatewayctl migrate/tenant/key admin CLI that bootstraps it.**

## Commits

| Task | Commit  | Message                                                                  |
| ---- | ------- | ------------------------------------------------------------------------ |
| 1    | 1bf3665 | feat(02-03): auth package + redisx client + sqlc-generated code          |
| 2    | 1a1704a | feat(02-03): gatewayctl migrate/tenant/key + cmd/gateway auth wiring     |

## Tests

**Unit tests (25 passing under `go test ./gateway/internal/auth/... ./gateway/internal/redisx/... ./gateway/cmd/gatewayctl/... -count=1 -race`):**

- argon2_test.go (7): Format, Unique-per-1000, HashVerifies, HashRejectsOthers, IsWellFormedKey positive/negative table, DefaultParams.
- cache_test.go (6): PutGetRoundTrip, Miss, TTLExpiry (FastForward 61s), KeyFor stability + collision-resistance, NegCache TTLExpiry (FastForward 6s).
- apikey_test.go (12): ExtractKey precedence (Auth wins / X-API-Key fallback / Basic non-Bearer empty), Verify (Missing/Malformed/ValidFirstHitsDB/ValidSecondHitsCache/UnknownKeyHitsNegCache/SHA256CollisionDefense/TouchBuffered/RevokedReturnsErrRevoked), Middleware (NoKey 401 envelope / MalformedKey code / ValidKeyCallsNext / EnrichesLogger).
- touch_buffer_test.go (6): EnqueueCoalesces, FlushInvokesFlushFn, RunTickerFlushes, RunDrainsOnCtxCancel, FlushErrorDoesNotPanic, MetricHooks.
- redisx/client_test.go (2): FailsOnUnreachable (2s budget), SucceedsAgainstMiniredis.
- gatewayctl/main_test.go (5): NoArgs_ExitsUsage, Unknown_ExitsWithMessage, KeyCreate_RejectsBadDataClass, MigrateNoDSN_Fails, Help_ExitsZero.

**Coverage:** auth 83.2% / redisx 100.0%.

**Load test (`go test -tags=load -run TestVerifyUnderLoad -v ./gateway/internal/auth/`):**

```
TestVerifyUnderLoad: 10000 requests in 598.323898ms = 16713 req/s (workers=4)
```

16,713 req/s ≫ Codex review [HIGH] 02-03 acceptance floor of 500 req/s on dev laptop. Result: the SHA-256 lookup + negative-cache fast path eliminates argon2id from the adversarial cache-miss flood path entirely.

## Performance Numbers

- **argon2id verify (TestGenerateAPIKey_HashVerifies x10):** 0.15-0.41s per iteration. Each iteration runs `argon2id.CreateHash` + `ComparePasswordAndHash` against a freshly-generated key. Per-call argon2 cost ≈ 75-200ms on this build host (4 vCPU). Cache TTL of 60s amortizes this to ~1 verify per minute per active key under sustained traffic.
- **gatewayctl binary size (`go build -trimpath -ldflags='-s -w'`):** 9,908,484 bytes (~9.4 MiB).
- **gateway binary size (default build):** 22,022,918 bytes (~21.0 MiB).

## CONTEXT.md decisions implemented

- **D-A1** — Key format `ifix_sk_<32 lowercase rfc4648 base32>` (40 chars total) with constant-time IsWellFormedKey.
- **D-A2** — argon2id with 60s positive Redis cache + 5s negative cache (Codex review [HIGH] 02-03 amendment).
- **D-A3** — gatewayctl admin surface: migrate up/down/status, tenant create, key create, key revoke.
- **D-A4** — Argon2id params (Memory=64MiB, Iterations=3, Parallelism=2, SaltLength=16, KeyLength=32).
- **D-A5** — Authorization: Bearer takes precedence over X-API-Key (test `TestExtractKey_AuthorizationWins` asserts).
- **D-B2** — DataClass enum (`normal`/`sensitive`) propagated via AuthContext into request context for downstream audit/proxy.
- **D-B7** — slog redactor wraps the gateway + gatewayctl loggers; raw key never leaks via attribute names.

## Cross-AI review closures (02-REVIEWS.md)

- **Codex [HIGH] 02-03** — Hot-path argon2 reduced to 0-or-1 per request via SHA-256 indexed lookup.
  - `grep -q "ListActiveKeysAll" gateway/internal/auth/apikey.go` → empty (closure gate).
  - `grep -q "GetActiveKeyByLookupHash" gateway/internal/auth/apikey.go` → match.
  - Negative cache (`gw:apikey:neg:<sha>`, 5s TTL) formalized via `negCacheKeyFor`/`negCachePut`/`negCacheCheck` and asserted by `TestVerify_UnknownKeyHitsNegativeCache`.
- **Codex [MEDIUM] 02-03** — TouchKeyLastUsed debounced via `TouchBuffer` (60s coalesce window). `gateway_apikey_touch_buffered_total` + `gateway_apikey_touch_flush_total` counters registered by `obs/metrics.go`. `TestVerify_TouchBuffered` asserts 100 verify calls collapse to PendingCount() == 1.
- **Codex [LOW] 02-02** — Partition automation finalized: `cmd/gateway/main.go` calls `db.EnsurePartitions(ctx, pool, time.Now(), db.DefaultPartitionLookahead)` after migrations. Plan 02-02 shipped the helper; Plan 02-03 wired it.

## Threat surface mitigations

All HIGH-severity threats from the plan's `<threat_model>` have explicit mitigations in code:

- **T-02-03-01 (brute force):** argon2id 64MiB/3/2 + 5s neg-cache → ~30 guesses/s/core ceiling.
- **T-02-03-03 (header precedence):** ExtractKey honors Authorization > X-API-Key per D-A5; tested.
- **T-02-03-04 (raw key logged):** safeKeyPrefix logs only `ifix_sk_****<last4>`; gatewayctl key create stdout-only path.
- **T-02-03-07 (argon2 CPU DoS):** IsWellFormedKey constant-time reject before DB/argon2 + neg-cache.
- **T-02-03-09 (raw key in DB):** schema only stores key_hash + key_lookup_hash + key_prefix.
- **T-02-03-10 (SQL injection):** all writes via sqlc parameterized queries.

## Deviations from Plan

### Rule 3 — sqlc not generated yet on disk
- **Found during:** Task 1 build.
- **Issue:** Plan 02-02 deferred `sqlc generate` to CI (Plan 02-08), leaving `gateway/internal/db/gen/` empty (only `.gitkeep`). Task 1 imports `gen.GetActiveKeyByLookupHashRow`, `gen.New`, etc., so the package must exist locally before `go build` succeeds.
- **Fix:** Installed sqlc v1.30.0 via `go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest` and ran `cd gateway && sqlc generate`. The generated files (`admin.sql.go`, `auth.sql.go`, `audit.sql.go`, `model_aliases.sql.go`, `db.go`, `models.go`, `querier.go`) are committed alongside the auth code so future builders don't need sqlc on PATH.
- **Files modified:** `gateway/internal/db/gen/*.go` (7 generated files).
- **Commit:** 1bf3665 (Task 1).

### Rule 1 — slog handler mutex copy
- **Found during:** `go vet` after writing apikey_test.go.
- **Issue:** First version of `capturingHandler` used `cp := *h` inside `WithAttrs`/`WithGroup` to clone the handler — but the struct embedded `sync.Mutex`. Vet complained: `assignment copies lock value to cp`.
- **Fix:** Refactored to put the shared mutex + record list inside a `*captureStore` so `WithAttrs`/`WithGroup` return a new `*capturingHandler` view that shares the store by pointer (no mutex copy).
- **Files modified:** `gateway/internal/auth/apikey_test.go` (only).
- **Commit:** 1bf3665 (Task 1).

### Rule 2 — buildRouter signature widening (defensive)
- **Found during:** Task 2 wiring of auth.Middleware into the chi.Group.
- **Issue:** Existing scaffold tests in `gateway/cmd/gateway/main_test.go` constructed routers without a verifier. Hard-requiring a verifier would force every test to spin up Postgres + Redis.
- **Fix:** Made the `verifier` parameter nil-safe inside `buildRouter` — when nil, the chi.Group is mounted WITHOUT auth.Middleware. Production main always passes a real verifier; tests pass nil and continue to validate /health, scaffold 501, /metrics. This is a defensive improvement, not a security regression — production code path always invokes Middleware.
- **Files modified:** `gateway/cmd/gateway/main.go`, `gateway/cmd/gateway/main_test.go`.
- **Commit:** 1a1704a (Task 2).

### Note — gen.DataClass / gen.Status types
- **Plan text suggested:** `DataClass: gen.DataClass(*dataClass)` for the InsertAPIKey call.
- **Reality:** sqlc emitted `DataClass interface{}` on `InsertAPIKeyParams` because there's no explicit override in `gateway/sqlc.yaml` for the Postgres ENUM types `ai_gateway.api_key_status` / `ai_gateway.data_class`. Plan-style cast `gen.DataClass(...)` would not compile.
- **Adjustment:** `gatewayctl key create` passes the raw string (`*dataClass`) directly — pgx encodes Go strings to Postgres ENUMs natively. Verifier reads back into `interface{}` and uses `enumString()` (handles both string and []byte protocol paths) before constructing the AuthContext.
- **Impact:** zero functional difference; aligns with sqlc's actual output. Documented here so future readers don't blame the plan.

## Self-Check: PASSED

**Files exist on disk:**
- FOUND: gateway/internal/auth/{errors,context,argon2,argon2_test,cache,cache_test,apikey,apikey_test,touch_buffer,touch_buffer_test,load_test}.go
- FOUND: gateway/internal/redisx/{client,client_test}.go
- FOUND: gateway/cmd/gatewayctl/{main,migrate,tenant,key,main_test}.go
- FOUND: gateway/internal/db/gen/{admin,audit,auth,model_aliases,db,models,querier}.go (sqlc-generated)

**Commits verified present on master:**
- FOUND: 1bf3665 — feat(02-03): auth package + redisx client + sqlc-generated code
- FOUND: 1a1704a — feat(02-03): gatewayctl migrate/tenant/key + cmd/gateway auth wiring

**Quality gates:**
- `gofmt -l ./gateway/internal/auth/ ./gateway/internal/redisx/ ./gateway/cmd/gatewayctl/` — empty (OK)
- `go vet ./gateway/...` — clean (OK)
- `go build ./gateway/...` — clean (OK)
- `go test ./gateway/... -count=1` — all packages OK (auth + cmd/gatewayctl + cmd/gateway + config + db + httpx + redisx)
- `go test -race ./gateway/internal/auth/... ./gateway/internal/redisx/...` — green
- `go test -tags=load -run TestVerifyUnderLoad ./gateway/internal/auth/` — 16,713 req/s ≫ 500 req/s floor

**Hot-path call-count assertions** (Codex review [HIGH] 02-03 closure gate):
- `grep -q "ListActiveKeysAll" gateway/internal/auth/apikey.go` → empty (NEVER on hot path)
- `grep -q "GetActiveKeyByLookupHash" gateway/internal/auth/apikey.go` → match (sole hot-path query)

**Wiring assertions** (cmd/gateway/main.go):
- `grep -q "auth.NewVerifier" gateway/cmd/gateway/main.go` → match
- `grep -q "auth.Middleware" gateway/cmd/gateway/main.go` → match
- `grep -q "auth.NewTouchBuffer" gateway/cmd/gateway/main.go` → match
- `grep -q "redisx.NewClient" gateway/cmd/gateway/main.go` → match
- `grep -q "db.EnsurePartitions" gateway/cmd/gateway/main.go` → match
- `grep -q "obs.ApikeyTouchBufferedTotal" gateway/cmd/gateway/main.go` → match
- `grep -q "r.Group" gateway/cmd/gateway/main.go` → match (chi auth Group on /v1/*)

**Confirmation: Authorization > X-API-Key precedence enforced** — test `TestExtractKey_AuthorizationWins` PASSES (asserts both headers set → ExtractKey returns the Authorization Bearer value, not X-API-Key).

---
*Phase: 02-gateway-core-multi-tenant-auth*
*Completed: 2026-04-18*
