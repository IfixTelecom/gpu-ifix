-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- prices: per (model, provider, unit) with valid_from/valid_to history.
CREATE TABLE IF NOT EXISTS ai_gateway.prices (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    model         TEXT NOT NULL,
    provider      TEXT NOT NULL,
    unit          TEXT NOT NULL CHECK (unit IN ('input_token', 'output_token', 'audio_second', 'embed_request')),
    unit_cost_usd NUMERIC(12,8) NOT NULL,
    valid_from    TIMESTAMPTZ NOT NULL DEFAULT now(),
    valid_to      TIMESTAMPTZ,
    notes         TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (model, provider, unit, valid_from)
);

CREATE INDEX IF NOT EXISTS idx_prices_active
    ON ai_gateway.prices (model, provider, unit) WHERE valid_to IS NULL;

-- fx_rates: USD<->BRL with valid_from/valid_to history.
CREATE TABLE IF NOT EXISTS ai_gateway.fx_rates (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    currency_pair TEXT NOT NULL,                     -- 'USD/BRL'
    rate          NUMERIC(10,6) NOT NULL,
    valid_from    TIMESTAMPTZ NOT NULL DEFAULT now(),
    valid_to      TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (currency_pair, valid_from)
);

CREATE INDEX IF NOT EXISTS idx_fx_rates_active
    ON ai_gateway.fx_rates (currency_pair) WHERE valid_to IS NULL;

-- NOTIFY trigger pattern (mirror 0009_upstreams_notify_trigger).
CREATE OR REPLACE FUNCTION ai_gateway.notify_prices_changed() RETURNS trigger AS $$
BEGIN
    PERFORM pg_notify('prices_changed', COALESCE(NEW.id::text, OLD.id::text));
    RETURN COALESCE(NEW, OLD);
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER prices_insert_delete_notify
AFTER INSERT OR DELETE ON ai_gateway.prices
FOR EACH ROW
WHEN (pg_trigger_depth() = 0)
EXECUTE FUNCTION ai_gateway.notify_prices_changed();

CREATE TRIGGER prices_update_notify
AFTER UPDATE ON ai_gateway.prices
FOR EACH ROW
WHEN (pg_trigger_depth() = 0 AND (
    NEW.unit_cost_usd IS DISTINCT FROM OLD.unit_cost_usd
    OR NEW.valid_to IS DISTINCT FROM OLD.valid_to
))
EXECUTE FUNCTION ai_gateway.notify_prices_changed();

CREATE OR REPLACE FUNCTION ai_gateway.notify_fx_changed() RETURNS trigger AS $$
BEGIN
    PERFORM pg_notify('fx_changed', COALESCE(NEW.id::text, OLD.id::text));
    RETURN COALESCE(NEW, OLD);
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER fx_insert_delete_notify
AFTER INSERT OR DELETE ON ai_gateway.fx_rates
FOR EACH ROW
WHEN (pg_trigger_depth() = 0)
EXECUTE FUNCTION ai_gateway.notify_fx_changed();

CREATE TRIGGER fx_update_notify
AFTER UPDATE ON ai_gateway.fx_rates
FOR EACH ROW
WHEN (pg_trigger_depth() = 0 AND (
    NEW.rate IS DISTINCT FROM OLD.rate
    OR NEW.valid_to IS DISTINCT FROM OLD.valid_to
))
EXECUTE FUNCTION ai_gateway.notify_fx_changed();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS fx_update_notify ON ai_gateway.fx_rates;
DROP TRIGGER IF EXISTS fx_insert_delete_notify ON ai_gateway.fx_rates;
DROP FUNCTION IF EXISTS ai_gateway.notify_fx_changed();
DROP TRIGGER IF EXISTS prices_update_notify ON ai_gateway.prices;
DROP TRIGGER IF EXISTS prices_insert_delete_notify ON ai_gateway.prices;
DROP FUNCTION IF EXISTS ai_gateway.notify_prices_changed();
DROP TABLE IF EXISTS ai_gateway.fx_rates;
DROP TABLE IF EXISTS ai_gateway.prices;
-- +goose StatementEnd
