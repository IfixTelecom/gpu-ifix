# Phase 4 UAT Results

**Tested by:** Pedro (operator)
**Phase-close date:** 2026-04-21
**Environment:** N/A — dev stack NOT yet deployed
**Gateway image:** N/A
**Container IDs:** N/A

---

## Deferral Notice

Phase 4 Plans 04-01..04-08 are COMPLETE with full `go test -tags integration -race` green (13 scenarios, 60.9s wall time against testcontainers Postgres 16 + Redis 7). SC-3 (admin /admin/usage shape) and SC-5 (rate-limit + quota middleware behavior) are proven by integration tests.

**SC-1, SC-2, SC-4 LIVE validation is deferred** — the `ai-gateway-dev` Portainer stack does not exist yet, and the outstanding items from Phase 2 02-08 SC-5 PARTIAL are still open:

- [ ] GitHub Secrets `PORTAINER_WEBHOOK_URL_DEV_GATEWAY` + `PORTAINER_WEBHOOK_URL_PROD_GATEWAY` not set (verified: `gh api repos/IfixTelecom/gpu-ifix/actions/secrets → total_count=0`)
- [ ] Portainer stack `ai-gateway-dev` not created (verified: `docker ps` returns no gateway container)
- [ ] Schema `ai_gateway` not created in any of the 6 DO clusters queried via MCP (`postgres-converseai`, `postgres-grupo-ifix`, `postgres-financeiro`, `postgres_clientes`, `postgres-sincron`, `postgres-defaultdb`)
- [ ] Traefik route for `gateway-dev.ifix.com.br` (or whatever domain) not provisioned — no DNS, no cert
- [ ] Vast.ai pod (`UPSTREAM_LLM_URL` target) not provisioned — Phase 1 HUMAN-UAT via `smoke.yml` also still pending per STATE.md

Once the above is closed (Phase 6 or earlier as a follow-up), the 6 UAT scenarios below should be executed against the real dev stack and this file updated with observations + timestamps. The `approved` resume-signal on Plan 04-09 Task 1 fires only after this file has the SC-1/SC-2/SC-4 sections filled in by a human operator running the actual curls.

**This deferral mirrors the Phase 2 02-08 SC-5 PARTIAL pattern.** Pre-Shipping rule: the Runbook (Task 2 output) is delivered and good-to-go; the Runbook is what an on-call needs. The live UAT is evidence of feature health, not the feature itself.

---

## Scenario 1 — SC-1 LIVE rate-limit headers (DEFERRED)

**Prerequisite unlocked:** `ai-gateway-dev` stack online.

**Steps:**
1. `docker exec ai-gateway-dev /gatewayctl tenant set-quota --tenant converseai --rps 5 --rpm 300`
2. Fire 10 parallel `POST /v1/chat/completions` against `https://gateway-dev.ifix.com.br` from a dev shell.
3. Record response codes + headers.
4. Restore: `--rps 20 --rpm 600`.

**To-record (operator fills):**
- [ ] First 5 responses: `200 OK` with `X-RateLimit-Limit-Requests: 5`, `X-RateLimit-Remaining-Requests: <n>`
- [ ] Next ~5 responses: `429` with body `{"error":{"type":"rate_limit_error","code":"rate_limit_rps","message":"..."}}` + header `Retry-After: 1`
- [ ] `gateway_rate_limit_rejected_total{tenant="converseai",window="rps"}` incremented by exactly 5 on `/metrics`
- Timestamp (UTC): `__FILL__`

---

## Scenario 2 — SC-4 LIVE peak-mode off-hours (DEFERRED)

**Prerequisite unlocked:** `ai-gateway-dev` stack online + a non-sensitive test tenant (`dev-peak`).

**Steps:**
1. `docker exec ai-gateway-dev /gatewayctl tenant set-mode --tenant dev-peak --mode peak --window 20-22`
2. Issue chat request at time outside 20:00-22:00 BRT.
3. `docker logs -f ai-gateway-dev 2>&1 | grep -E "module=SCHEDULE|module=DISPATCHER"`
4. Flip tenant to `24/7` + re-issue.

**To-record:**
- [ ] Off-hours: `module=SCHEDULE decision=off_hours_external tenant=dev-peak`
- [ ] Off-hours: `module=DISPATCHER upstream=openrouter-chat` (NOT `local-llm`)
- [ ] Response body has real Qwen completion (not 503)
- [ ] `gateway_schedule_routing_total{tenant="dev-peak",decision="off_hours_external"}` > 0
- [ ] 24/7: `module=SCHEDULE decision=local`
- Timestamp (UTC): `__FILL__`

---

## Scenario 3 — SC-4 edge: `503 off_hours_upstream_unavailable` (DEFERRED)

**Prerequisite unlocked:** Scenario 2 setup + ability to force `openrouter-chat` breaker OPEN.

**Steps:**
1. Force breaker OPEN: `docker exec ai-gateway-dev /gatewayctl upstreams disable --name openrouter-chat`
2. Issue chat at off-hours.
3. Verify no OpenAI direct chat attempt in logs (D-C4 invariant).
4. Restore: `upstreams enable --name openrouter-chat`

**To-record:**
- [ ] Response: `503` with body `{"error":{"type":"service_unavailable","code":"off_hours_upstream_unavailable","message":"..."}}`
- [ ] Logs: NO line mentioning `openai-chat` or `api.openai.com` during this scenario
- Timestamp (UTC): `__FILL__`

---

## Scenario 4 — gatewayctl admin loop + /admin/usage (DEFERRED)

**Prerequisite unlocked:** Fresh admin-key bootstrap path exercised.

**Steps:** (full command list in Plan 04-09 `how-to-verify`)
1. `admin-key create --label uat-2026-XX-XX`
2. `admin-key list` — verify new key present, status=active
3. Seed 10-20 chat/embed/audio requests across 2-3 days
4. `curl -H "X-Admin-Key: $KEY" /admin/usage?tenant=converseai&from=...&to=...&granularity=day`
5. `prices list` / `prices set-fx --usd-brl 5.15` / re-list to verify swap
6. `billing reconcile`
7. `admin-key revoke --label uat-2026-XX-XX`
8. Re-curl `/admin/usage` with revoked key — expect 401

**To-record:**
- [ ] `admin-key create` printed raw key once; never re-printed
- [ ] `prices list` output matches `gatewayctl usage report --format json` totals (cross-check)
- [ ] `prices set-fx` took effect in <1s (check `gateway_prices_reload_total{result=ok}` incremented)
- [ ] `billing reconcile` exit 0 (no drift, or drift < 0.1%)
- [ ] `/admin/usage` response has every SC-3 field (tokens_in, tokens_out, audio_seconds, embeds_count, cost_local_brl, cost_local_phantom_brl, cost_external_brl, cost_total_brl) per day + summary
- [ ] Revoked key returns `401 Unauthorized` with body `{"error":{"type":"authentication_error","code":"invalid_admin_key","message":"..."}}`
- Timestamp (UTC): `__FILL__`

---

## Scenario 5 — Sentry breadcrumbs (DEFERRED)

**Prerequisite unlocked:** Sentry org instance live + gateway stack pushing events.

**Steps:**
1. Trigger rate-limit 429 (reuse Scenario 1).
2. Trigger quota 429: `gatewayctl tenant set-quota --daily-tokens 10` + issue a chat that burns it.
3. Open Sentry, filter project=ai-gateway-dev, last 15 min.

**To-record:**
- [ ] Rate-limit 429 event visible, breadcrumb on the triggering request trace
- [ ] Quota 429 event visible with `error.code=quota_exceeded_daily_tokens`
- [ ] Headers `X-Admin-Key`, `Authorization`, `X-API-Key` all REDACTED in event payloads (Phase 2 `httpx.NewRedactor`)
- [ ] Request body content NOT captured (LGPD D-B2 Phase 2)
- Timestamp (UTC): `__FILL__`

---

## Scenario 6 — SC-2 LIVE `billing_events` rows (DEFERRED)

**Prerequisite unlocked:** After Scenarios 1, 2, and 4 have run real traffic.

**Steps:**
1. `psql $AI_GATEWAY_DATABASE_URL -c "SELECT request_id, source, tokens_in, tokens_out, cost_local_phantom_brl, cost_external_brl FROM ai_gateway.billing_events ORDER BY created_at DESC LIMIT 5;"`
2. Curl-killed-mid-stream test: `timeout 1 curl -N ... --stream` + wait 3s + re-query for `source='partial'` row.

**To-record:**
- [ ] ≥1 row with `source='final'` AND `cost_external_brl > 0` (from peak-mode OpenRouter in Scenario 2)
- [ ] ≥1 row with `source='final'` AND `cost_local_phantom_brl > 0` (from local-llm baseline)
- [ ] ≥1 row with `source='partial'` (from the curl-killed test — proves D-B2)
- [ ] Sum of last 5 rows' `tokens_in + tokens_out` is > 0 (interceptor extracted usage)
- psql output snapshot (paste with request_ids redacted):
  ```
  __FILL_5_ROWS__
  ```
- Timestamp (UTC): `__FILL__`

---

## Outstanding Issues / Surprises

_(none captured yet — section populated when live UAT runs)_

---

## Link to Wave 0 Operator Gates

`04-WAVE0-GATES.md` closed A1/A2/A5 with RESEARCH placeholder values. When this UAT runs, compare observed `cost_external_brl` (from Scenario 6) against the seed pricing; if drift > 10%, emit migration 0016 (`UPDATE prices`) and document the revalidation here.

---

## Sign-off

**Operator (post-deploy):** `__FILL__`
**Date:** `__FILL__`
**Phase-close sign-off:** Pedro, 2026-04-21 — runbook delivered; live UAT explicitly deferred pending stack deployment (mirrors Phase 2 SC-5 PARTIAL pattern).
