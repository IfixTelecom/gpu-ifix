-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- Schedule routing columns (D-C1)
ALTER TABLE ai_gateway.tenants
    ADD COLUMN IF NOT EXISTS mode               TEXT NOT NULL DEFAULT '24/7'
        CHECK (mode IN ('24/7', 'peak')),
    ADD COLUMN IF NOT EXISTS peak_window_start  TIME,
    ADD COLUMN IF NOT EXISTS peak_window_end    TIME,
    ADD COLUMN IF NOT EXISTS schedule_timezone  TEXT NOT NULL DEFAULT 'America/Sao_Paulo';

-- Quota columns (TEN-04, D-D Plumbing)
ALTER TABLE ai_gateway.tenants
    ADD COLUMN IF NOT EXISTS daily_quota_tokens          BIGINT NOT NULL DEFAULT 10000000,
    ADD COLUMN IF NOT EXISTS monthly_quota_tokens        BIGINT NOT NULL DEFAULT 300000000,
    ADD COLUMN IF NOT EXISTS daily_quota_audio_minutes   INTEGER NOT NULL DEFAULT 600,
    ADD COLUMN IF NOT EXISTS monthly_quota_audio_minutes INTEGER NOT NULL DEFAULT 18000,
    ADD COLUMN IF NOT EXISTS daily_quota_embeds          INTEGER NOT NULL DEFAULT 100000,
    ADD COLUMN IF NOT EXISTS monthly_quota_embeds        INTEGER NOT NULL DEFAULT 3000000,
    ADD COLUMN IF NOT EXISTS rps_limit                   INTEGER NOT NULL DEFAULT 20,
    ADD COLUMN IF NOT EXISTS rpm_limit                   INTEGER NOT NULL DEFAULT 600;

-- data_class column on tenants: defensive ADD so chk_sensitive_no_peak below
-- can reference it even if Phase 2 never added it to the tenants table
-- (Phase 2's data_class lives on api_keys; Phase 4 introduces a tenant-level
-- classification per D-C1 triple-defense). Default 'normal' preserves
-- existing behavior; 'sensitive' must be set explicitly via operator flow.
DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'ai_gateway'
          AND table_name = 'tenants'
          AND column_name = 'data_class'
    ) THEN
        ALTER TABLE ai_gateway.tenants
            ADD COLUMN data_class ai_gateway.data_class NOT NULL DEFAULT 'normal';
    END IF;
END $$;

-- Status column for ListTenantsForLoader filter (WHERE status='active').
-- Defensive ADD — created here rather than in an earlier migration to keep
-- this plan self-contained.
DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'ai_gateway'
          AND table_name = 'tenants'
          AND column_name = 'status'
    ) THEN
        ALTER TABLE ai_gateway.tenants
            ADD COLUMN status TEXT NOT NULL DEFAULT 'active'
                CHECK (status IN ('active', 'disabled'));
    END IF;
END $$;

-- LGPD invariant (D-C1 path 2 of triple-defense). Allowed only AFTER existing
-- rows received default mode='24/7' from the prior ALTER (which they did --
-- backfill is atomic with ADD COLUMN ... DEFAULT NOT NULL in PG11+).
ALTER TABLE ai_gateway.tenants
    DROP CONSTRAINT IF EXISTS chk_sensitive_no_peak;
ALTER TABLE ai_gateway.tenants
    ADD CONSTRAINT chk_sensitive_no_peak
        CHECK ((mode = '24/7') OR (data_class = 'normal'));

-- NOTIFY trigger (mirror 0009 pattern). Watches all config columns; probe-style
-- writeback columns excluded via WHEN clause to prevent reload-storm.
CREATE OR REPLACE FUNCTION ai_gateway.notify_tenants_changed() RETURNS trigger AS $$
BEGIN
    PERFORM pg_notify('tenants_changed', COALESCE(NEW.id::text, OLD.id::text));
    RETURN COALESCE(NEW, OLD);
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS tenants_insert_delete_notify ON ai_gateway.tenants;
DROP TRIGGER IF EXISTS tenants_update_notify ON ai_gateway.tenants;

CREATE TRIGGER tenants_insert_delete_notify
AFTER INSERT OR DELETE ON ai_gateway.tenants
FOR EACH ROW
WHEN (pg_trigger_depth() = 0)
EXECUTE FUNCTION ai_gateway.notify_tenants_changed();

CREATE TRIGGER tenants_update_notify
AFTER UPDATE ON ai_gateway.tenants
FOR EACH ROW
WHEN (pg_trigger_depth() = 0 AND (
    NEW.mode IS DISTINCT FROM OLD.mode
    OR NEW.peak_window_start IS DISTINCT FROM OLD.peak_window_start
    OR NEW.peak_window_end IS DISTINCT FROM OLD.peak_window_end
    OR NEW.schedule_timezone IS DISTINCT FROM OLD.schedule_timezone
    OR NEW.daily_quota_tokens IS DISTINCT FROM OLD.daily_quota_tokens
    OR NEW.monthly_quota_tokens IS DISTINCT FROM OLD.monthly_quota_tokens
    OR NEW.daily_quota_audio_minutes IS DISTINCT FROM OLD.daily_quota_audio_minutes
    OR NEW.monthly_quota_audio_minutes IS DISTINCT FROM OLD.monthly_quota_audio_minutes
    OR NEW.daily_quota_embeds IS DISTINCT FROM OLD.daily_quota_embeds
    OR NEW.monthly_quota_embeds IS DISTINCT FROM OLD.monthly_quota_embeds
    OR NEW.rps_limit IS DISTINCT FROM OLD.rps_limit
    OR NEW.rpm_limit IS DISTINCT FROM OLD.rpm_limit
    OR NEW.data_class IS DISTINCT FROM OLD.data_class
))
EXECUTE FUNCTION ai_gateway.notify_tenants_changed();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS tenants_update_notify ON ai_gateway.tenants;
DROP TRIGGER IF EXISTS tenants_insert_delete_notify ON ai_gateway.tenants;
DROP FUNCTION IF EXISTS ai_gateway.notify_tenants_changed();

ALTER TABLE ai_gateway.tenants
    DROP CONSTRAINT IF EXISTS chk_sensitive_no_peak;
ALTER TABLE ai_gateway.tenants
    DROP COLUMN IF EXISTS mode,
    DROP COLUMN IF EXISTS peak_window_start,
    DROP COLUMN IF EXISTS peak_window_end,
    DROP COLUMN IF EXISTS schedule_timezone,
    DROP COLUMN IF EXISTS daily_quota_tokens,
    DROP COLUMN IF EXISTS monthly_quota_tokens,
    DROP COLUMN IF EXISTS daily_quota_audio_minutes,
    DROP COLUMN IF EXISTS monthly_quota_audio_minutes,
    DROP COLUMN IF EXISTS daily_quota_embeds,
    DROP COLUMN IF EXISTS monthly_quota_embeds,
    DROP COLUMN IF EXISTS rps_limit,
    DROP COLUMN IF EXISTS rpm_limit;
-- Note: data_class and status columns are not dropped in Down to avoid
-- cascading data loss if the operator later re-ups. They are defensive-added
-- and harmless to leave in place.
-- +goose StatementEnd
