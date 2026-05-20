-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- Phase 06.7 — voice catalog (D-09 / D-10). One row per cloned voice: metadata
-- in Postgres, the reference WAV in MinIO (s3_key). Engine-agnostic — per the
-- Chatterbox zero-shot flow (06.7-WAVE0-GATES GATE 1, D-08 revised) there is NO
-- speaker-embedding .pt column; the gateway re-fetches the reference WAV from S3
-- and Chatterbox clones zero-shot, so the catalog stores only voice_id/s3_key.
-- Source: 06.7-RESEARCH.md §Voice Catalog (D-09/D-10) + §Security Domain + 06.7-PATTERNS.md 0025.
--
-- Migration number = 0025 (computed at execution time:
--   LAST_NUM=$(ls gateway/db/migrations/ | sort -V | tail -1 | grep -oE '^[0-9]+'); NEXT_NUM=$(printf "%04d" $((10#$LAST_NUM + 1)))
-- Latest migration in tree at exec time: 0024_upstreams_tts_role.sql).
--
-- TENANT IDENTITY (evidence for the chosen tenant_id type — must_haves truth #3):
--   Verified convention across the ai_gateway schema is UUID FK -> tenants(id):
--     - 0002_create_api_keys.sql:15      tenant_id UUID NOT NULL REFERENCES ai_gateway.tenants(id) ON DELETE CASCADE
--     - 0006_create_usage_counters_skeleton.sql:9  tenant_id UUID NOT NULL REFERENCES ai_gateway.tenants(id) ON DELETE CASCADE
--     - 0010_create_billing_events.sql:8  tenant_id UUID NOT NULL REFERENCES ai_gateway.tenants(id)
--     - 0003_create_audit_log_partitioned.sql:8   tenant_id UUID NOT NULL
--   tenants.id (0001_create_tenants.sql:7) is UUID PRIMARY KEY DEFAULT gen_random_uuid().
--   The auth context (internal/auth/apikey.go:165) stringifies this UUID for transport,
--   but the persisted/joined identity is UUID everywhere — so voices.tenant_id is UUID
--   with an FK to tenants(id) ON DELETE CASCADE to keep operational reporting/billing
--   consistent and to inherit tenant-deletion cleanup (06.7-PATTERNS used TEXT as a
--   placeholder; the verified schema convention is UUID FK, which this matches).
--
-- WARNING (Phase 8 / INT-01 dim migration): Phase 8 will run a pgvector dimension
-- migration (1536 -> 1024) for the corpus re-embed (multilingual-e5-large is 1024-dim,
-- 06.7-WAVE0-GATES GATE 4). That is UNRELATED to this voices table (no vector column
-- here) but flagged per REVIEWS so the next developer is aware the embedding dimension
-- is changing in a later phase.
--
-- Idempotency (T-06.7-07): CREATE TABLE IF NOT EXISTS + CREATE INDEX IF NOT EXISTS
-- make re-runs safe; Down provided.
CREATE TABLE IF NOT EXISTS ai_gateway.voices (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL REFERENCES ai_gateway.tenants(id) ON DELETE CASCADE,
    label       TEXT NOT NULL,
    s3_key      TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_voices_tenant ON ai_gateway.voices (tenant_id);

COMMENT ON TABLE ai_gateway.voices IS
    'Phase 06.7 voice catalog (D-09). One row per cloned voice; reference WAV in MinIO (s3_key). Zero-shot engine (Chatterbox) — no .pt speaker-embedding column.';
COMMENT ON COLUMN ai_gateway.voices.s3_key IS
    'MinIO object key of the reference WAV. Derived from the server-generated voice UUID, never client input (path-traversal mitigation, Plan 07 / T-06.7-03).';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SET search_path = ai_gateway, public;

DROP INDEX IF EXISTS ai_gateway.idx_voices_tenant;
DROP TABLE IF EXISTS ai_gateway.voices CASCADE;
-- +goose StatementEnd
