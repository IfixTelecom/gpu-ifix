# Phase 2: Gateway Core + Multi-tenant Auth - Discussion Log

> **Audit trail only.** Do not use as input to planning, research, or execution agents.
> Decisions are captured in `02-CONTEXT.md` — this log preserves the alternatives considered.

**Date:** 2026-04-18
**Phase:** 02-gateway-core-multi-tenant-auth
**Areas discussed:** API keys, Audit log, Idempotency-Key, Fundamentos da camada de dados

---

## Selection — Áreas cinzentas

| Área | Discutida |
|------|-----------|
| API keys — formato, storage e admin | ✓ |
| Audit log — escopo e retenção | ✓ |
| Idempotency-Key — semântica (TEN-09) | ✓ |
| Fundamentos da camada de dados | ✓ |

---

## API keys

### Formato

| Option | Description | Selected |
|--------|-------------|----------|
| `ifix_sk_<32 char base32>` | Prefixo facilita secret-scanning; base32 url-safe; 160 bits de entropia. | ✓ |
| `sk-ifix-<uuid v4>` | Familiar p/ usuários OpenAI; 122 bits. | |
| JWT HS256 com claims | Self-contained; denylist complicada. | |
| Random 32 bytes hex | Sem prefixo; secret-scanning mais difícil. | |

### Storage

| Option | Description | Selected |
|--------|-------------|----------|
| argon2id + prefix/last4 em claro | Hash one-way + preview visual; cache Redis 60s para perf. | ✓ |
| HMAC-SHA256 com master key | Rápido mas master key é ponto único de falha. | |
| AES-GCM reversível | Permite "ver chave de novo" — vaza em DB+master leak. | |

### Admin na Fase 2

| Option | Description | Selected |
|--------|-------------|----------|
| CLI `cmd/gatewayctl` + seed migrations | Minimalista; REST admin vai com dashboard Fase 7. | ✓ |
| REST admin já na Fase 2 | Mais trabalho agora; retrabalho com dashboard depois. | |
| Só SQL seed | Sem ergonomia. | |

### Rotação

| Option | Description | Selected |
|--------|-------------|----------|
| Múltiplas keys ativas por tenant (status enum) | Rollover zero-downtime. | ✓ |
| Uma key destrutiva | Força window de indisponibilidade. | |
| `expires_at` + auto-renew scheduled | Overhead pra 4 admins Ifix. | |

---

## Audit log

### Escopo

| Option | Description | Selected |
|--------|-------------|----------|
| Só metadata | LGPD-safe default. | |
| Metadata + hash prompt/response | Falso-seguro; hashes curtos colidem. | |
| Metadata + prompt/response completos (só `normal`) | Rico p/ debug; `sensitive` fica só metadata. | ✓ |
| Metadata + sampling 1% | Zona cinza de auditoria. | |

### Retenção

| Option | Description | Selected |
|--------|-------------|----------|
| Partition mensal + 90d hot / 1 ano cold em MinIO | Parquet export mensal, parti\u00e7\u00f5es antigas dropadas. | ✓ |
| Tabela única + DELETE cron 90d | Vacuum lento em DO Postgres. | |
| Append forever | Consome disco do DO compartilhado. | |
| Parti\u00e7\u00e3o + 30d hot | Retenção curta demais p/ post-mortem. | |

### Escrita

| Option | Description | Selected |
|--------|-------------|----------|
| Async buffered channel + batch flush | Hot path nunca bloqueia; fail-safe drop + métrica. | ✓ |
| Síncrono no handler | Adiciona latência do DO em todo request. | |
| Redis Stream + worker separado | Moving part a mais na Fase 2. | |

### Redação logs

| Option | Description | Selected |
|--------|-------------|----------|
| Middleware slog redige Authorization/X-API-Key/Cookie | Proteção defense-in-depth com Sentry hooks. | ✓ |
| Só Sentry scrubbers | Regressão silenciosa se alguém logar header. | |
| Logs completos em dev | Vira hábito, acaba voltando p/ redação. | |

### Conteúdo — localização

| Option | Description | Selected |
|--------|-------------|----------|
| Tabela separada `audit_log_content` (só `normal`) | Política LGPD é schema-level; partition independente. | ✓ |
| Mesma tabela com colunas nullable | Row bloat via TOAST. | |
| MinIO + `content_ref` | Duas stores a sincronizar. | |

### Streaming — captura

| Option | Description | Selected |
|--------|-------------|----------|
| Buffer chunks + concatena no close (cap 128 KB + flag truncated) | Cobre maioria do tráfego; memória aceitável em 4 vCPU. | ✓ |
| Não armazenar streaming | Perde ~80% do tráfego. | |
| Primeiros 2k tokens | Trunca final que contém tool_call. | |

### Whisper — áudio

| Option | Description | Selected |
|--------|-------------|----------|
| Não armazenar áudio; só metadata + transcript | Voz é PII pesada; transcript serve debug. | ✓ |
| MinIO + `audio_ref` | Policy/retention separada p/ áudios. | |
| Primeiros 10s de áudio | Ainda guarda voz identificável. | |

---

## Idempotency-Key

### Escopo

| Option | Description | Selected |
|--------|-------------|----------|
| Por tenant (`tenant_id` + key) | Isolamento default; padrão Stripe/OpenAI. | ✓ |
| Por API key (tenant + api_key_id + key) | Rollover de key quebra replay. | |
| Global gateway-wide | Colisão entre tenants. | |

### Storage + TTL

| Option | Description | Selected |
|--------|-------------|----------|
| Response body+status+headers no Redis, TTL 24h | Padrão Stripe; 40 MB Redis trivial. | ✓ |
| Marcador in-flight/done sem body | Previne dup mas cliente ainda retry. | |
| Body + TTL 1h | Backoff longo ultrapassa 1h. | |

### Colisão

| Option | Description | Selected |
|--------|-------------|----------|
| 422 Unprocessable Entity + envelope OpenAI | Detecta bug do cliente; padrão Stripe. | ✓ |
| Aceita como request novo | Silencia bug. | |
| Retorna response cacheado mesmo c/ body diferente | Inaceitável. | |

### Endpoints

| Option | Description | Selected |
|--------|-------------|----------|
| Só `/v1/chat/completions` não-streaming (400 se stream) | Foco em tool-call retry (RES-06). | ✓ |
| Todos POST | Embedding + Whisper hashing wasteful. | |
| Todos POST incluindo streaming | UX esquisita. | |

---

## Fundamentos da camada de dados

### Migrations

| Option | Description | Selected |
|--------|-------------|----------|
| `pressly/goose` | SQL-first; embed no binário; transação por migration. | ✓ |
| `golang-migrate/migrate` | CLI-first; menos ergonômico como lib. | |
| `ariga/atlas` | Declarativo HCL; overkill p/ ~5 tabelas. | |

### Queries

| Option | Description | Selected |
|--------|-------------|----------|
| `sqlc` — type-safe codegen de SQL | SQL first-class; compile-time check. | ✓ |
| Raw `pgx` + mappers hand-written | Boilerplate; regressões em runtime. | |
| ORM (bun/gorm/ent) | Scaffolding rápido; SQL não-óbvio. | |

### Upstream routing

| Option | Description | Selected |
|--------|-------------|----------|
| Env vars na Fase 2 — refactor p/ tabela `upstreams` na Fase 3 | YAGNI; refactor de 1 arquivo depois. | ✓ |
| Tabela `upstreams` já na Fase 2 (1 row) | Cerimônia sem benefício; Fase 3 evolui schema anyway. | |
| YAML config file | 2 fontes de verdade (DB+file). | |

---

## Claude's Discretion

- Versão de Go: mantém 1.23 (go.mod atual).
- Layout do módulo: `gateway/cmd/{gateway,gatewayctl}/main.go`, `gateway/internal/{auth,audit,idempotency,proxy,config,httpx,obs}`.
- Request ID: UUIDv7 gerado no middleware; header de cliente `X-Request-ID` preservado como `client_request_id`.
- Timeouts HTTP: `ReadHeaderTimeout: 10s`, `ReadTimeout: 60s`, `WriteTimeout: 0`, `IdleTimeout: 120s`, body max 25 MB.
- Sentry SDK habilitado dia-1 com redação de headers/payload (padrão Ifix).
- Prometheus `/metrics` stub com contadores básicos.
- Traefik e DNS público ficam Fase 10.
- Testes: unit + integration (`testcontainers-go`) + e2e opt-in via env var.
- pgxpool: `MaxConns: 10`, `MaxConnIdleTime: 5min`.

## Deferred Ideas

- REST admin endpoints → Fase 7.
- Tabela `upstreams` → Fase 3.
- Idempotency-Key em embeddings/transcriptions → reconsiderar se algum cliente pedir.
- Prometheus ricas + tenant/P95 histograms → Fase 7.
- HTTPS/TLS + DNS público → Fase 10.
- SSO/Better Auth admin → Fase 10.
- Export format (Parquet vs JSONL.gz) → decide em runtime do primeiro export.
- Model aliases com versioning/tenant-specific → reconsiderar em Fase 8/9 se necessário.
