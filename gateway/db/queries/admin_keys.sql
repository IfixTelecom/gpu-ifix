-- name: GetAdminKeyByLookupHash :one
-- Hot-path verify: PK-style fetch via SHA-256 lookup hash, then bcrypt verify
-- happens at the application layer (Phase 2 D-A2 pattern).
SELECT id, key_lookup_hash, key_hash, key_prefix, label, status,
       created_at, revoked_at, last_used_at
FROM ai_gateway.admin_keys
WHERE key_lookup_hash = $1
  AND status = 'active';

-- name: InsertAdminKey :one
-- Used by `gatewayctl admin-key create` and the boot-time bootstrap.
INSERT INTO ai_gateway.admin_keys
    (key_lookup_hash, key_hash, key_prefix, label, status)
VALUES ($1, $2, $3, $4, 'active')
RETURNING id, key_prefix, label, status, created_at;

-- name: RevokeAdminKey :exec
UPDATE ai_gateway.admin_keys
SET status = 'revoked', revoked_at = now()
WHERE id = $1;

-- name: ListAdminKeys :many
SELECT id, key_prefix, label, status, created_at, revoked_at, last_used_at
FROM ai_gateway.admin_keys
ORDER BY created_at DESC;

-- name: TouchAdminKeyLastUsed :exec
-- Updated periodically by middleware (low frequency; ok in hot path occasionally).
UPDATE ai_gateway.admin_keys
SET last_used_at = now()
WHERE id = $1;

-- name: CountActiveAdminKeys :one
-- Used by boot-time bootstrap: if 0, generate and INSERT a random admin key.
SELECT COUNT(*)::bigint AS n
FROM ai_gateway.admin_keys
WHERE status = 'active';
