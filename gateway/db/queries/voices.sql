-- name: CreateVoice :one
-- Insert a catalog row for a newly cloned voice (Plan 07 voicesCreate handler).
-- tenant_id is sourced from the authenticated context (auth.MustFromContext), NEVER
-- from the request body (D-10 / ASVS V4). s3_key is the server-derived MinIO key.
-- id is supplied by the handler (server-generated UUID) so it MATCHES the UUID
-- embedded in s3_key (<prefix>/<id>.wav) — the pod fetches <prefix>/<voice_id>.wav
-- keyed on this id, so the row id and the S3 object key MUST share the same UUID
-- (Plan 05 zero-shot fetch contract). Generating the id server-side (not via the
-- DB default) is what guarantees that consistency.
INSERT INTO ai_gateway.voices (id, tenant_id, label, s3_key)
VALUES ($1, $2, $3, $4)
RETURNING id, tenant_id, label, s3_key, created_at;

-- name: ListVoicesByTenant :many
-- Tenant-scoped catalog list (Plan 07 voicesList). Filters by tenant_id so a caller
-- only ever sees its own voices, never cross-tenant (T-06.7-06 Information Disclosure).
SELECT id, tenant_id, label, s3_key, created_at
FROM ai_gateway.voices
WHERE tenant_id = $1
ORDER BY created_at DESC;

-- name: GetVoiceForTenant :one
-- Fetch a single voice scoped to the caller's tenant. Requires BOTH the voice id AND
-- tenant_id so a guessed/leaked UUID from another tenant cannot be read (T-06.7-06).
SELECT id, tenant_id, label, s3_key, created_at
FROM ai_gateway.voices
WHERE id = $1 AND tenant_id = $2;

-- name: DeleteVoiceForTenant :exec
-- Delete a voice scoped to the caller's tenant (Plan 07 voicesDelete; the handler also
-- removes the S3 object). tenant_id in the WHERE prevents cross-tenant deletion.
DELETE FROM ai_gateway.voices
WHERE id = $1 AND tenant_id = $2;
