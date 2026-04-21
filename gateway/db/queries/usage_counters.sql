-- name: GetUsageCountersToday :one
-- Hot-path quota check: PK read on (tenant_id, date) where date is today in
-- America/Sao_Paulo. Returns zero-value row if no rows exist (caller falls
-- through to per-tenant default quota).
SELECT tokens_in, tokens_out, audio_seconds, embeds_count,
       cost_local_phantom_brl, cost_external_brl, requests_count
FROM ai_gateway.usage_counters
WHERE tenant_id = $1
  AND date = (now() AT TIME ZONE 'America/Sao_Paulo')::date;

-- name: GetUsageCountersMonth :one
-- Monthly quota check: SUM rows for the calendar month containing today
-- in America/Sao_Paulo.
SELECT
    COALESCE(SUM(tokens_in), 0)::bigint            AS tokens_in,
    COALESCE(SUM(tokens_out), 0)::bigint           AS tokens_out,
    COALESCE(SUM(audio_seconds), 0)::bigint        AS audio_seconds,
    COALESCE(SUM(embeds_count), 0)::bigint         AS embeds_count,
    COALESCE(SUM(requests_count), 0)::bigint       AS requests_count
FROM ai_gateway.usage_counters
WHERE tenant_id = $1
  AND date >= date_trunc('month', (now() AT TIME ZONE 'America/Sao_Paulo')::date)
  AND date <  date_trunc('month', (now() AT TIME ZONE 'America/Sao_Paulo')::date) + interval '1 month';

-- name: ResetUsageCountersForReconcile :exec
-- Used by `gatewayctl billing reconcile --apply`: rewrites today's counter row
-- from the SUM(billing_events). Idempotent; safe to call repeatedly.
INSERT INTO ai_gateway.usage_counters
    (tenant_id, date, tokens_in, tokens_out, audio_seconds, embeds_count,
     cost_local_phantom_brl, cost_external_brl, requests_count)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (tenant_id, date) DO UPDATE SET
    tokens_in              = EXCLUDED.tokens_in,
    tokens_out             = EXCLUDED.tokens_out,
    audio_seconds          = EXCLUDED.audio_seconds,
    embeds_count           = EXCLUDED.embeds_count,
    cost_local_phantom_brl = EXCLUDED.cost_local_phantom_brl,
    cost_external_brl      = EXCLUDED.cost_external_brl,
    requests_count         = EXCLUDED.requests_count;
