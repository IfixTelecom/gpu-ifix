# Integration Smoke — Phase 8 Client Integration

**Status:** Holds the **gpu-ifix-side** Phase 8 integration artifacts — the tenant
provisioning seed script, the per-app smoke-test scripts, the smoke report
schema, and the WhatsApp audio fixtures. The client-app `base_url`/env-var
switches live in the sibling repos (`converseai-v4`, `campanhas-chatifix`) and
are operator HUMAN-UAT actions — they are **not** part of this directory.

## Files

| File | Purpose | Added by |
|---|---|---|
| `provision-tenants.sh` | Idempotent seed script — wraps `gatewayctl tenant/key/admin-key create` to provision the 2 Phase-8 tenants (`converseai`, `chat-ifix`) | 08-01 |
| `README.md` | This file | 08-01 |
| `smoke-converseai.py` | INT-01 smoke — chat / streaming / tool-calls / embeddings against the gateway `/v1/*` with the `converseai` tenant key | 08-02 |
| `smoke-chat-ifix.py` | INT-02 smoke — WhatsApp-audio Whisper transcription against `/v1/audio/transcriptions`, latency + quality within ±10% of a recorded baseline | 08-03 |
| `report-schema.json` | JSON Schema the smoke scripts validate their report output against | 08-02 / 08-03 |
| `fixtures/` | Real WhatsApp audio sample + baseline transcript/latency for the chat-ifix smoke | 08-03 |

## Provisioning the tenants

`provision-tenants.sh` wraps the compiled `gatewayctl` CLI to seed the two
Phase-8 tenants in the gateway DB. It is **idempotent** for the tenant rows —
re-running it on already-provisioned tenants is a safe no-op (a "slug already
exists" from `gatewayctl tenant create` is treated as success).

Step 1 — create the tenants (re-runnable any time):

```bash
AI_GATEWAY_PG_DSN=postgres://USER:PASS@HOST:PORT/DB \
  ./scripts/integration-smoke/provision-tenants.sh --gatewayctl /path/to/gatewayctl
```

Step 2 — mint the API keys (run **exactly once**):

```bash
AI_GATEWAY_PG_DSN=postgres://USER:PASS@HOST:PORT/DB \
  ./scripts/integration-smoke/provision-tenants.sh --gatewayctl /path/to/gatewayctl --mint-keys
```

`gatewayctl key create` / `admin-key create` are **not** idempotent — each call
mints a brand-new row. The `--mint-keys` flag is the explicit opt-in that gates
those steps, so a routine re-run of the script does not spray duplicate keys.
Pass `--mint-keys` once, the first time, then copy the three raw keys it prints
(2 tenant keys + 1 dashboard admin key) into the respective Portainer stack env
vars. The keys are shown **once** and are never re-derivable.

Flags:

- `--gatewayctl PATH` — path to the compiled `gatewayctl` binary, or a wrapper
  such as a `docker exec ifix-ai-gateway /gatewayctl` shim. Default: `gatewayctl`
  on `PATH`.
- `--mint-keys` — opt-in: also mint the 2 tenant keys + the dashboard admin key.
- `--dry-run` — print the `gatewayctl` commands that would run, execute nothing,
  touch no DB.

Env:

- `AI_GATEWAY_PG_DSN` (required) — Postgres DSN the wrapped `gatewayctl` reads to
  reach the gateway DB.

## Scope

This directory delivers **gpu-ifix-side artifacts only** (08-CONTEXT.md
`## Decisions`). It provisions the gateway-side identity (tenants + keys) and
ships the smoke scripts that exercise the gateway `/v1/*` endpoints.

It does **not** edit the client apps. The actual `base_url` / `api_key` env-var
switches inside `converseai-v4` (`apps/api` Elysia + `agents/` Python, sharing
the one `converseai` tenant) and `campanhas-chatifix` (the Chat Ifix backend)
happen in those sibling repos as operator HUMAN-UAT actions — and the live
validation is additionally gated on the gateway being deployed.

## See also

- `docs/RUNBOOK-CLIENT-INTEGRATION.md` — rollback runbook (per-app env-var diff +
  redeploy command), produced by plan 08-04.
- The Phase 8 HUMAN-UAT plan (`08-HUMAN-UAT.md`) — live env-var switch, production
  smoke runs, the timed rollback drill, and the dashboard cross-check; produced
  by plan 08-04.
