# gateway/db — schema `ai_gateway`

Schema dedicado no cluster DO Postgres compartilhado (CONTEXT.md D-D4).
Role: `ai_gateway_app` com `GRANT USAGE ON SCHEMA ai_gateway` + `GRANT
SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA ai_gateway`.

## Migrations

- Format: goose SQL (`-- +goose Up/Down`, `-- +goose StatementBegin/End`).
- Naming: `NNNN_description.sql` (4-digit zero-padded).
- Header: `SET search_path = ai_gateway, public;` em toda migration.
- Embedded: `//go:embed migrations/*.sql` em `gateway/internal/db/migrate.go`.
- Aplicação: via `gatewayctl migrate --dir up` OU no boot do gateway (env
  `AI_GATEWAY_MIGRATE_ON_BOOT=true` — default false a partir da Plan 02-08).
- **Goose bookkeeping:** a tabela `ai_gateway.goose_db_version` vive no schema
  dedicado porque o pool `AfterConnect` força `search_path = ai_gateway, public`.
  Tooling externo (psql, DBeaver) que ignora `search_path` deve qualificar como
  `ai_gateway.goose_db_version`. Validação:
  `psql -c "\dt ai_gateway.goose_db_version"` após `migrate up` (Codex review
  [MEDIUM] 02-02 — documentar explicitamente).

## Particionamento (automação)

- Partições mensais de `audit_log` e `audit_log_content` são criadas por
  `db.EnsurePartitions(ctx, pool, now, nMonths)` em
  `gateway/internal/db/partitions.go` (Codex review [LOW] 02-02).
- Chamada em **todo boot do gateway** após `db.Up` e antes de expor o HTTP
  server (rolling window N=3 meses). Chamada também por `gatewayctl migrate up`
  após a migração aplicar. Idempotente: `CREATE TABLE IF NOT EXISTS`.
- Substitui a dependência exclusiva de 02-09 para criar partições; 02-09
  continua responsável por drop de partições >90d + export Parquet.

## Retenção

- Hot (Postgres DO): 90 dias (D-B3).
- Cold (MinIO `ifix-ai-gateway-audit-cold`): 1 ano, Parquet mensal.
- Export + drop de partições >90d: `gatewayctl audit export-month YYYY-MM`
  (Plan 02-09). Operação manual na Fase 2; cron Kestra a partir da Fase 4.

## Partições

- `audit_log` e `audit_log_content`: `PARTITION BY RANGE (ts)`, PK composto
  `(request_id, ts)` (Postgres exige que a coluna de particionamento esteja
  na PK). Migração seed cria 3 partições mensais (mês atual + 2 seguintes).

## Seeds

- `tenants`: slug `converseai` (tenant zero para bootstrap).
- `model_aliases`: `qwen → llm/qwen`, `whisper → stt/Systran/faster-whisper-large-v3`,
  `bge-m3 → embed/BAAI/bge-m3`.

## Out of scope (Fase 2)

- `billing_events` → Fase 4.
- `upstreams` table com circuit state → Fase 3.
- Admin REST endpoints → Fase 7 (dashboard Next.js).
