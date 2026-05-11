---
status: resolved
resolved_at: 2026-05-11T21:24:30Z
plan: 05-01
gates: [A, B, C]
---

# Phase 5 — Wave 0 Operator Gates

Operator decisions blocking migrations 0016/0017/0018 (Plan 05-02) and DCGM scraping (Plan 05-04).

Resolved as part of Plan 05-01 Task 3 (`checkpoint:human-action`).

---

## Gate A — `audit_log.upstream` column type

**RESEARCH:** Open Question 2 / Pitfall 8.

### Method

Resolved from migration source (no live DB query needed). MCP postgres disconnected this session; code is authoritative for schema.

### Evidence

`gateway/db/migrations/0003_create_audit_log_partitioned.sql:13`:

```sql
CREATE TABLE IF NOT EXISTS ai_gateway.audit_log (
    ...
    upstream              TEXT,    -- no CHECK constraint
    ...
);
```

Audit confirmed no later migration adds a CHECK or ENUM on `audit_log.upstream`:

```bash
grep -rn "ALTER TABLE.*audit_log\|CREATE.*audit_log" gateway/db/migrations/*.sql
# → only 0003 (CREATE) and 0004 (audit_log_content, separate table)
```

Only `model_aliases.upstream` has a CHECK (`'llm'|'stt'|'embed'`) — that is a different table, used for routing class, not audit values.

### Decision

| Field | Value |
|---|---|
| Column type | `TEXT` (no CHECK, no ENUM) |
| Action for migration 0018 | **docs-only** (skip DDL) |

### Migration 0018 spec (Plan 05-02 Task 1)

File: `gateway/db/migrations/0018_audit_log_shed_values.sql`

Contents: docs-only migration with no executable DDL. Goose still requires a Up/Down body, so emit a `SELECT 1;` no-op marker plus an inline `-- +goose Up` block comment explaining the new audit values are accepted by virtue of TEXT being unconstrained.

Allowed shed values written by gateway:
- `shed_saturated`
- `shed_blocked_sensitive`
- `shed_tier1_unavailable`

(Validation that operator does not introduce a CHECK later: Plan 05-08 integration test reads back rows and asserts the literal strings round-trip.)

---

## Gate B — Tenant slugs

**RESEARCH:** Open Question 1 / Assumption A1.

### Method

Operator chose default-only path — do not seed shed limits per tenant.

### Decision

| Field | Value |
|---|---|
| Migration 0016 seed UPDATE | **skip** (no seeded per-tenant caps) |
| Post-deploy workflow | Operator runs `docker exec ifix-ai-gateway /gatewayctl tenant set-shed-limits --slug <slug> ...` per Plan 05-07 |

### Migration 0016 spec (Plan 05-02 Task 1)

`gateway/db/migrations/0016_evolve_tenants_shedding_limits.sql`:

- Add 4 hard-cap columns to `ai_gateway.tenants` (per RESEARCH; nullable, defaults populated per priority tier policy at column-default level).
- Add `priority_tier` SMALLINT column with default per RESEARCH (e.g. 50 for unknown/mid).
- Extend `tenants_notify_trigger` (originally from migration 0009 family) so changes to the new columns publish to the existing `tenants_updated` NOTIFY channel — `tenants.Loader.Refresh` already subscribes.
- **No `UPDATE` statements** seeding per-tenant caps. Operator is the source of truth for production caps.

This avoids the "wrong slug" risk entirely — column defaults are inert until an operator sets a real limit, so the system boots safely whether tenant slugs are `voice-api` or `voice_api`.

---

## Gate C — `DCGM_EXPORTER_URL`

**RESEARCH:** Open Question 3 / Assumption A2.

### Method

Operator confirms: no Vast.ai pod active at gate-resolution time. Phase 5 must boot fail-open.

### Decision

| Field | Value |
|---|---|
| `DCGM_EXPORTER_URL` (Portainer stack env) | `""` (empty) |
| VRAM signal | **disabled at boot** (CONTEXT D-A3 — fail-open policy) |
| FSM inputs at boot | `inflight` + `p95` only (composite signal still operational) |
| Operator unblock path | Set `DCGM_EXPORTER_URL=http://<pod>:9400/metrics` in Portainer stack env, restart container — `dcgm.Scraper.Run(ctx)` reads from env at startup; no rebuild needed |

### Plan 05-04 implementation note

`dcgm.Scraper.Run(ctx)` must short-circuit gracefully when `cfg.DCGMExporterURL == ""`:

```go
// pseudocode for Plan 05-04
if cfg.DCGMExporterURL == "" {
    slog.Info("dcgm.scraper: disabled (DCGM_EXPORTER_URL unset) — FSM will operate without VRAM signal")
    return nil // goroutine exits cleanly; ReadMiB() returns 0
}
```

`shed.FSM.Evaluate` must treat `ReadMiB() == 0` as "VRAM signal unavailable" (skip the VRAM threshold check, use inflight+P95 only).

### Connectivity smoke

Deferred — not runnable without a live pod. Operator runs `curl -sS --max-time 3 http://<pod>:9400/metrics | grep ^DCGM_FI_DEV_FB_USED` post-deploy as part of the Plan 05-04 verify step.

---

## Resume signal

`approved` — all 3 gates resolved, no `blocked:` items.

Plan 05-01 may now mark its checkpoint task complete and write SUMMARY.md. Plan 05-02 may proceed with migrations 0016 / 0017 / 0018 per the specs above. Plan 05-04 inherits the `DCGM_EXPORTER_URL=""` fail-open behavior.
