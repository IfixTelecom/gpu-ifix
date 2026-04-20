-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

CREATE OR REPLACE FUNCTION ai_gateway.notify_upstreams_changed() RETURNS trigger AS $$
BEGIN
    PERFORM pg_notify('upstreams_changed', COALESCE(NEW.id::text, OLD.id::text));
    RETURN COALESCE(NEW, OLD);
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS upstreams_change_notify ON ai_gateway.upstreams;

-- WHEN clause filters out pure probe writebacks (Pitfall 7). Fire ONLY
-- when a config column actually changed. INSERT/DELETE always fires
-- (no OLD/NEW comparison possible) -- handled by the OR-chain below.
CREATE TRIGGER upstreams_change_notify
AFTER INSERT OR UPDATE OR DELETE ON ai_gateway.upstreams
FOR EACH ROW
WHEN (
    -- INSERT: OLD is NULL -> comparisons are NULL -> fire (we want it)
    -- DELETE: NEW is NULL -> comparisons are NULL -> fire (we want it)
    -- UPDATE: fire only if any of the config columns differ
    pg_trigger_depth() = 0 AND (
        TG_OP IN ('INSERT', 'DELETE')
        OR NEW.name IS DISTINCT FROM OLD.name
        OR NEW.role IS DISTINCT FROM OLD.role
        OR NEW.tier IS DISTINCT FROM OLD.tier
        OR NEW.url_env IS DISTINCT FROM OLD.url_env
        OR NEW.auth_bearer_env IS DISTINCT FROM OLD.auth_bearer_env
        OR NEW.enabled IS DISTINCT FROM OLD.enabled
        OR NEW.weight IS DISTINCT FROM OLD.weight
        OR NEW.circuit_config IS DISTINCT FROM OLD.circuit_config
    )
)
EXECUTE FUNCTION ai_gateway.notify_upstreams_changed();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS upstreams_change_notify ON ai_gateway.upstreams;
DROP FUNCTION IF EXISTS ai_gateway.notify_upstreams_changed();
-- +goose StatementEnd
