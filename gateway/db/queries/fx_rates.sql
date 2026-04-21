-- name: GetCurrentFX :one
-- Returns the currently-active fx rate for a pair (e.g., 'USD/BRL').
SELECT id, currency_pair, rate, valid_from, valid_to, created_at
FROM ai_gateway.fx_rates
WHERE currency_pair = $1
  AND valid_to IS NULL;

-- name: ListAllFX :many
SELECT id, currency_pair, rate, valid_from, valid_to, created_at
FROM ai_gateway.fx_rates
ORDER BY currency_pair, valid_from DESC;

-- name: ExpireActiveFX :exec
UPDATE ai_gateway.fx_rates
SET valid_to = now()
WHERE currency_pair = $1
  AND valid_to IS NULL;

-- name: InsertFX :one
INSERT INTO ai_gateway.fx_rates (currency_pair, rate)
VALUES ($1, $2)
RETURNING id, currency_pair, rate, valid_from;
