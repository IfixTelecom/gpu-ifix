# Runbook — Phase 4 Multi-Tenant Quotas, Billing & Schedule Routing

**Phase 4 (`ifix-ai-gateway`) isolation layer.** Read this when:

- A tenant reports `429` with `error.code` starting with `rate_limit_*` or `quota_exceeded_*`
- Alert fires on `gateway_rate_limit_rejected_total` or `gateway_quota_rejected_total` spike
- `/admin/usage` numbers don't match what the finance team expects
- `gateway_billing_flush_failures_total > 0` for more than 1 min
- A peak-mode tenant gets `503 off_hours_upstream_unavailable`
- Post-incident review of a quota/billing event

Sibling runbook: [`RUNBOOK-FAILOVER.md`](./RUNBOOK-FAILOVER.md) — the Phase 3 upstream failover layer. Phase 4 sits ON TOP of Phase 3; many Phase 4 failures cascade from a Phase 3 upstream going OPEN.

Phase 7 will replace most manual diagnosis with a dashboard + alerts; until then, diagnose → mitigate → verify using the cycles below.

---

## Mental Model (30 seconds)

```
Request ────▶ auth ────▶ idempotency ────▶ rate-limit (Redis Lua, atomic RPS+RPM)
                                              │
                                              ▼ hit: 429 rate_limit_*
                                            quota (Postgres usage_counters, fail-closed)
                                              │
                                              ▼ hit: 429 quota_exceeded_*
                                            schedule (tenants.mode + window → route)
                                              │
                                              ▼ peak + off-hours: force tier-1
                                            tokencount (interceptor)
                                              │
                                              ▼
                                            dispatcher ────▶ local-llm / openrouter
                                              │
                                              ▼ response body
                                            billing-flush (async; same-txn CTE
                                              INSERT billing_events + UPSERT usage_counters)
```

**Every request ends up writing a row** to `ai_gateway.billing_events` (source = `final` on 200 OK, `partial` on abnormal close, `reject` on 429/503). The flusher's async channel buffer protects the hot path — if Postgres is degraded, requests still succeed but `gateway_billing_flush_dropped_total` rises.

**Three fail-policy knobs** (env vars, hot-reload NOT supported — require restart):

- `AI_GATEWAY_RATE_LIMIT_FAIL_OPEN=true` (default) — Redis Lua transport errors pass the request through. Incremented on `gateway_rate_limit_check_failures_total`.
- `AI_GATEWAY_QUOTA_FAIL_OPEN=false` (default) — Postgres usage_counters lookup failures REJECT with 503 `quota_check_unavailable`. Fail-closed is intentional (D-A2): cheaper to refuse than to torch external quota.
- `AI_GATEWAY_MIGRATE_ON_BOOT=true` (default) — goose runs all pending migrations on container start. Idempotent.

**Sensitive tenants cannot be peak-mode.** CHECK constraint `chk_sensitive_no_peak` (migration 0013) + CLI pre-validation in `gatewayctl tenant set-mode` + boot-time invariant check in `main.go` (gateway `os.Exit(1)` on breach). Triple defense per D-C1.

---

## At-a-Glance Severity Matrix

| Symptom                                          | Likely cause                                                      | First response                                            |
|--------------------------------------------------|-------------------------------------------------------------------|-----------------------------------------------------------|
| `429 rate_limit_rps` surge for one tenant        | Tenant burst exceeds `rps_limit`                                  | `gatewayctl tenant set-quota --tenant X --rps 50` (temporary) |
| `429 rate_limit_rpm`                             | Sustained traffic above `rpm_limit`                               | Raise `--rpm` OR investigate runaway client loop          |
| `429 quota_exceeded_daily_tokens` for one tenant | Daily allocation exhausted                                        | Raise `--daily-tokens` OR wait for 00:00 BRT rollover     |
| `429 quota_exceeded_daily_audio_minutes`         | STT audio quota exhausted                                         | Raise `--daily-audio-minutes`                             |
| `429 quota_exceeded_daily_embeds`                | Embedding quota exhausted                                         | Raise `--daily-embeds`                                    |
| `503 quota_check_unavailable`                    | Postgres `usage_counters` lookup failed (fail-closed D-A2)        | Check Postgres health; do NOT flip `QUOTA_FAIL_OPEN=true` casually |
| `503 off_hours_upstream_unavailable`             | Tenant in peak mode off-hours AND OpenRouter (tier-1) breaker OPEN | Check Fireworks/OpenRouter status; decision D-C2 is **no fallback to OpenAI direct**; flip tenant to `mode=24/7` temporarily |
| Cost numbers wrong in `/admin/usage`             | Seed price drift OR `fx_rates` stale                              | `gatewayctl prices list` + `prices set-fx` to correct    |
| `billing_events` growing but `usage_counters` stale | Flusher channel back-pressure OR DB contention                  | Check `gateway_billing_flush_failures_total`; run reconcile |
| `Unauthorized` on `/admin/usage`                 | Missing/revoked `X-Admin-Key`                                     | Rotate via `gatewayctl admin-key create`                  |
| Gateway boots then exits immediately             | Sensitive+peak invariant breach OR tzdata missing                 | Check logs for `tenants: sensitive+peak invariant breach` or `failed to load time.Location` |

---

## Routine Operations

All commands run against the dev stack via `docker exec ai-gateway-dev /gatewayctl ...`. Prod stack is `ai-gateway-prod`.

### Adjust a tenant's rate-limit + quota

```bash
docker exec ai-gateway-dev /gatewayctl tenant set-quota \
  --tenant converseai \
  --rps 50 --rpm 1500 \
  --daily-tokens 20000000 --monthly-tokens 600000000 \
  --daily-audio-minutes 1200 --monthly-audio-minutes 36000 \
  --daily-embeds 200000 --monthly-embeds 6000000
```

Flags omitted from the command are **left unchanged** (partial UPDATE via `sqlc.narg`). Takes effect in `<1s` via `NOTIFY tenants_changed` — no gateway restart needed.

### Switch a tenant to peak mode (with window + timezone)

```bash
# 24/7 mode — always route to local tier-0 when breaker allows:
docker exec ai-gateway-dev /gatewayctl tenant set-mode --tenant X --mode 24/7

# Peak mode — route to local tier-0 inside window, tier-1 (OpenRouter) outside:
docker exec ai-gateway-dev /gatewayctl tenant set-mode --tenant X \
  --mode peak --window 08-22 --tz America/Sao_Paulo

# INVALID — CLI rejects BEFORE the DB CHECK constraint triggers (D-C1 path 1):
docker exec ai-gateway-dev /gatewayctl tenant set-mode --tenant cobrancas --mode peak
# Error: cannot set peak mode for sensitive tenant cobrancas (LGPD RES-08)
```

Windows support wrap-around (e.g. `--window 22-08` means 22h → 08h next day). Always includes start, excludes end. Timezone defaults to `America/Sao_Paulo`.

**Triple-defense against sensitive+peak:**
1. CLI rejects pre-DB (`gatewayctl tenant set-mode`).
2. SQL `CHECK (NOT (data_class='sensitive' AND mode='peak'))` constraint on `ai_gateway.tenants`.
3. Boot-time `CheckSensitivePeakInvariant()` in `main.go` — `os.Exit(1)` if ANY row violates at startup.

If you see the gateway process exiting with log `tenants: sensitive+peak invariant breach` at boot, the DB was edited out-of-band (e.g. `SET session_replication_role=replica`). Fix the offending row BEFORE the next deploy; the gateway will not boot while the invariant is broken.

### Rotate admin keys

```bash
# Create a new key — the raw key is printed ONCE; copy to 1Password immediately:
docker exec ai-gateway-dev /gatewayctl admin-key create --label "2026-04-21-pedro"

# List keys (shows key_prefix + label + status + last_used_at — never the raw key):
docker exec ai-gateway-dev /gatewayctl admin-key list

# Revoke a key by label:
docker exec ai-gateway-dev /gatewayctl admin-key revoke --label "old-label"
```

Cache TTL is 60s — revoked keys stop working within 60s. Storage: `ai_gateway.admin_keys`, `key_hash` is bcrypt-hashed (cost 10), `lookup_hash` is SHA-256 of the prefix for fast lookup.

**Bootstrap key path:** if `AI_GATEWAY_ADMIN_KEY_BOOTSTRAP` env is set, that key is accepted on startup ONLY. If absent, the gateway generates a random key, inserts it with label `bootstrap-<timestamp>`, and logs a **WARN**: `admin bootstrap key generated — rotate immediately`. Either way, rotate after first login.

### Update a price or FX rate

```bash
# After OpenRouter/Fireworks announces a price change:
docker exec ai-gateway-dev /gatewayctl prices set \
  --model qwen3.5-27b --provider openrouter-fireworks --unit input_token \
  --usd 0.00000025 --notes "OpenRouter email 2026-05-01"

# Verify:
docker exec ai-gateway-dev /gatewayctl prices list

# FX rate (weekly / on FX volatility):
docker exec ai-gateway-dev /gatewayctl prices set-fx --usd-brl 5.12
```

Takes effect in `<1s` via `NOTIFY prices_changed` / `NOTIFY fx_changed`. Old rows get `valid_to=now()`; new rows get `valid_from=now()` — history is append-only.

**Seed-source authority:** The initial seed values (migration 0015) came from `.planning/phases/04-multi-tenant-quotas-billing-schedule-routing/04-WAVE0-GATES.md` which used RESEARCH placeholder values ($0.195/1M input, $1.56/1M output for qwen3.5-27b; $0.006/min for whisper-1; 5.10 BRL/USD). **These should be revalidated against live dashboards AT LEAST weekly** or when OpenRouter/OpenAI notifies a change. Run reconcile (below) to detect drift between seeded prices and observed external invoices.

### Run billing reconcile

```bash
# Dry-run (no changes):
docker exec ai-gateway-dev /gatewayctl billing reconcile --from 2026-04-19 --to 2026-04-21

# Apply drift corrections if above threshold:
docker exec ai-gateway-dev /gatewayctl billing reconcile --from 2026-04-19 --to 2026-04-21 --apply
```

Threshold: 0.1% drift between `sum(billing_events)` and `usage_counters` aggregates. `--apply` rewrites `usage_counters` from the authoritative (append-only) `billing_events`. Safe to run frequently; drift typically caused by transient flusher back-pressure.

### Pull a per-tenant usage report

```bash
# Table output (operator eyeballing):
docker exec ai-gateway-dev /gatewayctl usage report \
  --tenant converseai --from 2026-04-01 --to 2026-04-30 --format table

# Programmatic:
docker exec ai-gateway-dev /gatewayctl usage report \
  --tenant converseai --from 2026-04-01 --to 2026-04-30 --format json | jq

# Via HTTP (requires X-Admin-Key):
curl -sS -H "X-Admin-Key: $ADMIN_KEY" \
  "https://gateway-dev.ifix.com.br/admin/usage?tenant=converseai&from=2026-04-01&to=2026-04-30&granularity=day" | jq
```

Response fields (per SC-3): `tokens_in`, `tokens_out`, `audio_seconds`, `embeds_count`, `cost_local_brl`, `cost_local_phantom_brl` (notional OpenRouter-equivalent for local traffic), `cost_external_brl` (real tier-1 charges), `cost_total_brl`. Granularity: `day` (required) + `summary` (aggregate across range).

---

## Incident Playbooks

### Incident: one tenant hammering rate-limit → legitimate requests rejected

**Symptoms:** `gateway_rate_limit_rejected_total{tenant=X}` spikes; tenant complains about 429s.

1. Verify the window via `/metrics` — `window="rps"` vs `window="rpm"` tells you whether it's a burst (rps) or sustained (rpm) pattern.
2. Check upstream tier-0 load: `gateway_inflight_requests` + `gateway_p95_latency` — is the limit preventing a saturation cascade?
3. If burst is **legitimate** (overnight batch, migration, etc.): raise RPS/RPM via `gatewayctl tenant set-quota`.
4. If burst is **unexpected** (runaway client loop): contact tenant owner; do NOT raise the limit to mask the bug.
5. **Do NOT disable rate-limit globally.** The `RATE_LIMIT_FAIL_OPEN=true` flag only kicks in on Redis transport errors — it's a safety valve, not a bypass switch. If you need to disable rate-limit for a specific tenant (emergency only), set `--rps 999999 --rpm 999999` rather than touching the global knob.

### Incident: `quota_check_unavailable` 503s (fail-closed)

**Symptoms:** `gateway_quota_check_failures_total` incrementing; users see `503 service_unavailable` with `code=quota_check_unavailable`.

1. Check Postgres health: `docker logs infra-postgres | tail -100`; `pg_isready -h <host>`.
2. Check gateway logs for the specific query failing — `GetUsageCountersToday` vs `GetUsageCountersMonth`. Partitioned tables can degrade if a partition is missing.
3. If Postgres is **transient-degraded** (<5 min): wait. Fail-closed is correct behavior — refusing to serve is cheaper than torching external quota.
4. If Postgres is **down for >5 min**: emergency override — set `AI_GATEWAY_QUOTA_FAIL_OPEN=true` in Portainer and restart the stack. This lets requests through WITHOUT quota enforcement. **Reset to `false` within 1 hour of PG recovery.** Document the override window in the incident log — every minute open is unquota'd spend.
5. Post-recovery: run `gatewayctl billing reconcile --apply` to true-up `usage_counters` from `billing_events`.

### Incident: `billing_events` table growing but `usage_counters` lagging

**Symptoms:** `/admin/usage` totals lag behind `gatewayctl billing reconcile` output by more than 5 min.

1. Check `gateway_billing_flush_dropped_total` — non-zero means the flusher channel buffer (1000) filled up. Root cause is usually DB-side contention.
2. Check `gateway_billing_flush_failures_total{reason}` — `flush` = CTE insert failed; `txn` = transaction management error; `conflict` = UPSERT conflict (should never trigger on `billing_events` since request_id is unique).
3. Check Postgres deadlock log for `usage_counters` UPSERT contention (Pitfall 2). If hot: the next iteration is per-tenant flush sharding — file a ticket for Phase 5.
4. Immediate mitigation: run `gatewayctl billing reconcile --apply` — rewrites `usage_counters` from authoritative `billing_events`.
5. Log the drift magnitude; anything above 1% means the flusher needs operator attention (channel sizing, batch interval, or per-tenant sharding).

### Incident: `503 off_hours_upstream_unavailable`

**Symptoms:** Peak-mode tenant with a chat request at off-hours gets 503 `code=off_hours_upstream_unavailable`.

1. Check OpenRouter/Fireworks status: https://fireworks.ai/status (or operator's preferred).
2. Check `gateway_breaker_state{upstream="openrouter-chat"}` — if OPEN, external provider is unreachable.
3. **Decision is intentional** (D-C2 + Phase 3 D-C4): no fall-of-fallback to OpenAI direct chat. Keeping the model fixed at Qwen 3.5 27B is a product invariant (tool-call behavior + cost model both depend on it).
4. Mitigation: temporarily flip the affected tenant to `mode=24/7` — the GPU may still be running, and 24/7 mode routes to tier-0 local first:
   ```bash
   docker exec ai-gateway-dev /gatewayctl tenant set-mode --tenant X --mode 24/7
   ```
5. After provider recovers: flip back to peak mode. Run a test request to confirm `decision=local` shows in the SCHEDULE log lines again.

### Incident: `gatewayctl prices set` fails with "unknown provider" at cost-calc time

**Symptoms:** Flusher logs `billing: price missing for model/provider/unit` and billing_events rows have `cost_*_brl=0`.

1. Root cause: `prices` table has a row for (model, provider, unit), but the dispatcher is routing to a DIFFERENT provider. Happens when `UPSTREAM_LLM_OPENROUTER_PROVIDER_ORDER` env changed but `prices` table wasn't updated.
2. Check dispatcher config: `docker exec ai-gateway-dev env | grep UPSTREAM_LLM_OPENROUTER_PROVIDER_ORDER`. **As of 04-09 UAT**, that variable defaults to `["novita"]` — Phase 4 Wave 0 gate assumed Fireworks but we haven't revalidated against live OpenRouter yet. If OpenRouter is actually routing through Novita, our `prices` rows are filed under `provider=openrouter-fireworks` → miss → fallback to `cost=0`.
3. Fix:
   ```bash
   # Insert the missing price row for the actual provider:
   docker exec ai-gateway-dev /gatewayctl prices set \
     --model qwen3.5-27b --provider openrouter-novita --unit input_token --usd 0.00000020 \
     --notes "aligning with UPSTREAM_LLM_OPENROUTER_PROVIDER_ORDER"
   ```
4. Long-term fix: revalidate `04-WAVE0-GATES.md` A1/A2 against live OpenRouter, pick one provider order, and align `prices` + `UPSTREAM_LLM_OPENROUTER_PROVIDER_ORDER`. File a PR in Phase 4 follow-up.

### Incident: gateway `os.Exit(1)` at boot with `tenants: sensitive+peak invariant breach`

**Symptoms:** Container restart loop; first log line after migrations is the invariant breach message.

1. Query the offending rows:
   ```sql
   SELECT slug, mode, data_class FROM ai_gateway.tenants WHERE data_class='sensitive' AND mode='peak';
   ```
2. Someone edited the row bypassing the CHECK constraint (e.g. `SET session_replication_role=replica` then UPDATE). This is the boot-time last-line defense — it's working as designed.
3. Fix the row:
   ```sql
   UPDATE ai_gateway.tenants SET mode='24/7' WHERE slug='<offending>';
   ```
4. Restart the stack. Document the root cause in the incident log — how did someone bypass the CHECK?

### Incident: clock skew → rollover fires at wrong time

**Symptoms:** Daily quota rollover hits midnight UTC instead of midnight America/Sao_Paulo (off by 3h).

1. Root cause: gateway binary lost its tzdata. This shouldn't happen — Phase 4 embeds `_ "time/tzdata"` in the binary (see `gateway/cmd/gateway/main.go`). Distroless image has no OS tzdata — tzdata MUST come from the binary.
2. Verify: `docker exec ai-gateway-dev /gateway --version 2>&1` (or inspect tzdata at build time). If binary was built without the blank import, all rollovers are UTC-based.
3. Fix: rebuild from `develop` — build-gateway.yml should pick up the embed. If not, manual rebuild: `docker build --no-cache -f gateway/Dockerfile .`
4. Post-recovery: any rows created with the wrong rollover window are recoverable via `gatewayctl billing reconcile --apply` (reconcile recomputes day aggregates from request timestamps, which are correct).

---

## Deferred / Known Gaps

### Cross-replica quota coherence

Today's gateway runs **single-replica**. Redis (infra-redis-1) holds the rate-limit buckets, so rate-limit is already correct across replicas. BUT Postgres `usage_counters` is the source of truth for quota — multi-replica coherence is implicit (Postgres serializes UPSERTs).

**When we go multi-replica (Phase 6):**
- Rate-limit: unchanged (Redis is shared).
- Quota: add a Postgres advisory lock around the UPSERT path if contention becomes measurable. Currently a Phase 6 TODO.
- Flusher: each replica has its own flusher goroutine writing to the same `billing_events` table. UPSERTs on `usage_counters` are serialized by Postgres; no explicit coordination needed.

### UAT SC-1, SC-2, SC-4 live-validation deferred

Phase 4 ships with SC-3 and SC-5 proven by integration tests (13-file `gateway/internal/integration_test/` suite — 60s wall time against testcontainers PG16 + Redis 7). SC-1 (429 headers to a real client), SC-2 (real cost_external_brl from OpenRouter traffic), SC-4 (live peak-mode routing in logs) require a live dev stack which didn't exist at phase close (see `04-UAT-RESULTS.md`). UAT will close once the Portainer stack `ai-gateway-dev` is deployed.

### Price/FX seed was placeholder

`04-WAVE0-GATES.md` used RESEARCH values ($0.195/1M input, $1.56/1M output for qwen3.5-27b; $0.006/min for whisper-1; 5.10 BRL/USD). Live OpenRouter dashboard not consulted. **Revalidate before first billed customer**; emit migration 0016 (`UPDATE prices`) + `set-fx` if drift `>10%`.
