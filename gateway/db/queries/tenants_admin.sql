-- name: GetTenantConfig :one
-- Hot-path: single PK lookup of full tenant config including new Phase 4 columns.
SELECT id, slug, name, data_class, status, mode,
       peak_window_start, peak_window_end, schedule_timezone,
       daily_quota_tokens, monthly_quota_tokens,
       daily_quota_audio_minutes, monthly_quota_audio_minutes,
       daily_quota_embeds, monthly_quota_embeds,
       rps_limit, rpm_limit,
       created_at, updated_at
FROM ai_gateway.tenants
WHERE id = $1;

-- name: ListTenantsForLoader :many
-- Bulk load at boot + on NOTIFY tenants_changed. Same columns as GetTenantConfig.
SELECT id, slug, name, data_class, status, mode,
       peak_window_start, peak_window_end, schedule_timezone,
       daily_quota_tokens, monthly_quota_tokens,
       daily_quota_audio_minutes, monthly_quota_audio_minutes,
       daily_quota_embeds, monthly_quota_embeds,
       rps_limit, rpm_limit
FROM ai_gateway.tenants
WHERE status = 'active'
ORDER BY slug;

-- name: UpdateTenantMode :exec
-- Used by `gatewayctl tenant set-mode`. CHECK constraint chk_sensitive_no_peak
-- rejects sensitive+peak at the DB layer (D-C1 path 2). The CLI also rejects
-- pre-DB (path 1) for a clearer error message.
UPDATE ai_gateway.tenants
SET mode = $2,
    peak_window_start = sqlc.narg('peak_window_start')::time,
    peak_window_end   = sqlc.narg('peak_window_end')::time,
    schedule_timezone = COALESCE(sqlc.narg('schedule_timezone')::text, schedule_timezone),
    updated_at = now()
WHERE slug = $1;

-- name: UpdateTenantQuota :exec
-- Partial UPDATE -- fields passed as NULL via sqlc.narg are left unchanged.
UPDATE ai_gateway.tenants
SET daily_quota_tokens          = COALESCE(sqlc.narg('daily_quota_tokens')::bigint,           daily_quota_tokens),
    monthly_quota_tokens        = COALESCE(sqlc.narg('monthly_quota_tokens')::bigint,         monthly_quota_tokens),
    daily_quota_audio_minutes   = COALESCE(sqlc.narg('daily_quota_audio_minutes')::int,       daily_quota_audio_minutes),
    monthly_quota_audio_minutes = COALESCE(sqlc.narg('monthly_quota_audio_minutes')::int,     monthly_quota_audio_minutes),
    daily_quota_embeds          = COALESCE(sqlc.narg('daily_quota_embeds')::int,              daily_quota_embeds),
    monthly_quota_embeds        = COALESCE(sqlc.narg('monthly_quota_embeds')::int,            monthly_quota_embeds),
    rps_limit                   = COALESCE(sqlc.narg('rps_limit')::int,                       rps_limit),
    rpm_limit                   = COALESCE(sqlc.narg('rpm_limit')::int,                       rpm_limit),
    updated_at                  = now()
WHERE slug = $1;

-- name: CountSensitivePeakInvariant :one
-- Boot-time defensive check (D-C1 path 3). The CHECK constraint should make
-- this impossible. If COUNT > 0, gateway os.Exit(1).
SELECT COUNT(*)::bigint AS n
FROM ai_gateway.tenants
WHERE mode = 'peak' AND data_class = 'sensitive';
