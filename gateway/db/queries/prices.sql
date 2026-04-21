-- name: ListActivePrices :many
-- Hot-path: load all currently-active prices at boot and on NOTIFY prices_changed.
SELECT id, model, provider, unit, unit_cost_usd, valid_from, valid_to, notes, created_at
FROM ai_gateway.prices
WHERE valid_to IS NULL
ORDER BY model, provider, unit;

-- name: ListAllPrices :many
-- For `gatewayctl prices list --all` -- historical view.
SELECT id, model, provider, unit, unit_cost_usd, valid_from, valid_to, notes, created_at
FROM ai_gateway.prices
ORDER BY model, provider, unit, valid_from DESC;

-- name: ExpireActivePrice :exec
-- Called inside the `gatewayctl prices set` transaction before InsertPrice.
-- Closes any currently-active row for (model, provider, unit) by setting valid_to=now().
UPDATE ai_gateway.prices
SET valid_to = now()
WHERE model = $1
  AND provider = $2
  AND unit = $3
  AND valid_to IS NULL;

-- name: InsertPrice :one
-- Inserts a new active price row. Combined with ExpireActivePrice in one txn,
-- this performs an atomic price swap. Returns the new row's id.
INSERT INTO ai_gateway.prices (model, provider, unit, unit_cost_usd, notes)
VALUES ($1, $2, $3, $4, sqlc.narg('notes')::text)
RETURNING id, model, provider, unit, unit_cost_usd, valid_from;
