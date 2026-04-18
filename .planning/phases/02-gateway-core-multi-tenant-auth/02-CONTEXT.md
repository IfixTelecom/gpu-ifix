# Phase 2: Gateway Core + Multi-tenant Auth - Context

**Gathered:** 2026-04-18
**Status:** Ready for planning

<domain>
## Phase Boundary

Entrega um binário Go único (`chi v5` + `httputil.ReverseProxy` + `slog` + `jackc/pgx v5` + `redis/go-redis v9`) rodando em VPS Ifix 4 vCPU dedicada que:

1. Recebe chamadas OpenAI-compat em `/v1/chat/completions` (non-stream + SSE streaming), `/v1/embeddings` e `/v1/audio/transcriptions` (multipart)
2. Autentica via API key multi-tenant (`Authorization: Bearer` ou `X-API-Key`) com `data_class` (`normal|sensitive`) expostos no request context
3. Emite `X-Request-ID` único por request, logs estruturados `slog` NDJSON com o mesmo ID, e linha em `audit_log` Postgres (+ prompt/response em `audit_log_content` quando `data_class=normal`)
4. Faz pass-through de tool/function calling em formato OpenAI e suporta `Idempotency-Key` em `/v1/chat/completions` não-streaming
5. Resolve alias de modelo (`model: "qwen"` → versão atual) e roteia para **um único pod primário** via env vars
6. Expõe `GET /health` (gateway) e `GET /v1/health/upstreams` (derivado do pod health-bridge `:9100`)
7. Deploya via Docker Compose + Portainer + webhook GitHub no padrão Ifix

**Fora de escopo desta phase:** circuit breakers, retries e fallback chain (Fase 3); rate-limit, quotas e cost-attribution runtime (Fase 4); load shedding (Fase 5); auto-provisioning emergencial (Fase 6); dashboard/alertas/métricas Prometheus ricos (Fase 7); integração das apps cliente (Fases 8-9); DNS público `gateway.ifix.com.br` + TLS end-to-end + admin SSO (Fase 10).

</domain>

<decisions>
## Implementation Decisions

### API keys — formato, storage, rotação, bootstrap

- **D-A1:** Formato `ifix_sk_<32 char base32>`. Prefixo fixo `ifix_sk_` facilita secret-scanning (GitHub/GitLab) e distinção em logs. Base32 url-safe sem caracteres ambíguos (0/O/1/l). Entropia efetiva: 160 bits.
- **D-A2:** Storage: `argon2id` do segredo completo + `ifix_sk_` + last-4 chars em claro para preview visual (`ifix_sk_****abcd`). Parâmetros argon2id padrão OWASP 2026. Cache Redis do resultado de verificação com TTL 60s (chave: `apikey:{sha256(full_key)}` → `{tenant_id, data_class, status}`) elimina custo argon2 no hot path.
- **D-A3:** Admin surface **Fase 2 é mínima**: binário separado `cmd/gatewayctl` no monorepo com subcomandos `tenant create`, `key create --tenant X --data-class {normal|sensitive}`, `key revoke <id>`, `migrate up/down`. Migrations fazem seed de tenant inicial (`converseai`). Endpoints admin REST **deferidos para Fase 7** (consumidos pelo Dashboard Next.js).
- **D-A4:** Tabela `api_keys` com FK `tenant_id` + `status enum('active','revoked')`. **Múltiplas keys ativas por tenant** (rollover zero-downtime): admin cria nova → app migra env var → admin revoga antiga. Colunas: `id UUID`, `tenant_id`, `key_hash`, `key_prefix` (ex: `ifix_sk_****abcd`), `status`, `data_class`, `created_at`, `revoked_at NULLABLE`, `last_used_at NULLABLE`.
- **D-A5:** Gateway aceita `Authorization: Bearer <key>` **e** `X-API-Key: <key>` (TEN-01). Ordem de tentativa: `Authorization` > `X-API-Key`. Sem ambos ou inválidos: 401 com envelope OpenAI (`pkg/openai.ErrorResponse`).

### Audit log — escopo, storage, retenção

- **D-B1:** Captura **metadata de todo request** (qualquer tenant) em `audit_log`: `ts`, `request_id`, `tenant_id`, `api_key_id`, `data_class`, `route`, `method`, `upstream`, `status_code`, `latency_ms`, `tokens_in`, `tokens_out`, `cost_brl NULLABLE` (calculado Fase 4), `error_code NULLABLE`, `idempotency_replayed BOOL`, `stream BOOL`, `truncated BOOL`.
- **D-B2:** Prompt/response **completos** vão para tabela separada `audit_log_content` (PK `request_id`, colunas `prompt JSONB`, `response JSONB`, `ts`). **Row só é inserida quando `data_class=normal`.** Tenants `sensitive` (Telefonia, Cobranças) ficam **apenas** com metadata — pol\u00edtica LGPD é default no schema, não opcional em c\u00f3digo.
- **D-B3:** Particionamento mensal `PARTITION BY RANGE (ts)` nas duas tabelas. Retenção: **90 dias hot** no DO Postgres + **1 ano cold** em MinIO Ifix (export mensal para Parquet via job agendado, parti\u00e7\u00f5es >90d dropadas do Postgres). Job de export é parte desta fase (`cmd/gatewayctl audit export-month`).
- **D-B4:** Escrita **ass\u00edncrona** via canal bufferizado + goroutine flush em batch. Buffer: 1000 linhas. Flush trigger: 500 rows acumulados OU 1s decorrido. Fail-safe: se buffer encher, **drop com incremento de m\u00e9trica `gateway_audit_dropped_total`** e log warn — hot path **nunca** bloqueia em Postgres.
- **D-B5:** Streaming SSE: `httputil.ReverseProxy.ModifyResponse` + tee writer acumula chunks na mem\u00f3ria; no close concatena e persiste `response` como texto completo. **Cap 128 KB por resposta**; excedente trunca e marca `truncated=true`. Aceit\u00e1vel pra 4 vCPU: N streams concorrentes × avg 8-12 KB.
- **D-B6:** Whisper: **sem armazenamento do \u00e1udio cru** (PII de voz). Metadata em `audit_log`: `audio_filename`, `audio_mime`, `audio_size_bytes`, `audio_duration_s` (do response do pod), `audio_language NULLABLE`. Em `audit_log_content` (só `normal`): `response.text` (transcri\u00e7\u00e3o).
- **D-B7:** Middleware `slog` redacta automaticamente headers sens\u00edveis (`Authorization`, `X-API-Key`, `Cookie`, `Proxy-Authorization`) substituindo por `***REDACTED***` antes de emitir qualquer record. Aplica em todo log path, não só em erros. Alinha com Sentry Go SDK `BeforeSend` hook (padrão Ifix) para duplicar a prote\u00e7\u00e3o.

### Idempotency-Key — sem\u00e2ntica (TEN-09)

- **D-C1:** Escopo `(tenant_id, key)`. Redis key: `idem:{tenant_id}:{idempotency_key}`. Tenants diferentes podem reusar a mesma string.
- **D-C2:** Valor cacheado: JSON `{status, headers_whitelist, body, request_hash, stored_at}`. `request_hash = SHA-256` do body normalizado (ordena keys JSON antes de hashear). TTL **24 horas** após primeira escrita (padrão Stripe/OpenAI). Estimativa de footprint: ~10k keys/dia × 4 KB = 40 MB Redis (trivial no Redis Ifix compartilhado).
- **D-C3:** Replay semantics:
  - Mesma key + mesmo `request_hash` → retorna response cacheado com header adicional `X-Idempotency-Replayed: true` + `audit_log.idempotency_replayed=true`.
  - Mesma key + `request_hash` diferente → **HTTP 422 Unprocessable Entity** com envelope OpenAI `{error: {message: "Idempotency-Key conflict: body differs from original request", type: "idempotency_conflict", code: "idempotency_key_reused_with_different_body"}}`.
- **D-C4:** Escopo: **apenas `POST /v1/chat/completions` em modo não-streaming**. Se cliente mandar `Idempotency-Key` com `stream: true`, retorna 400 `{error: {message: "Idempotency-Key not supported on streaming requests", code: "idempotency_key_unsupported_stream"}}`. `/v1/embeddings` e `/v1/audio/transcriptions` não lêem o header nesta fase (embedding é determinístico; multipart hashing fica Fase 3+).
- **D-C5:** Idempotency-Key coexiste com `data_class`: mesmo cliente sensitive pode usar; replay retorna o response cacheado (que por D-B2 não tem content persistido em `audit_log_content` para sensitive, mas **o valor cacheado no Redis inclui body** — necessário pro replay funcionar).

### Fundamentos da camada de dados

- **D-D1:** Migrations via `pressly/goose`. Arquivos em `gateway/db/migrations/*.sql` (SQL puro), embedados no binário do gateway com `//go:embed` + aplicados no boot (ou via `gatewayctl migrate up`). Cada migration em uma transa\u00e7\u00e3o. Naming: `NNNN_description.sql` (ex: `0001_create_tenants.sql`).
- **D-D2:** Queries via `sqlc` (type-safe codegen de SQL → Go). Arquivos em `gateway/db/queries/*.sql`, gera\u00e7\u00e3o via `sqlc generate` no CI (validada pre-commit). Output em `gateway/internal/db/gen/`. Zero ORM runtime.
- **D-D3:** Upstream routing via **env vars** na Fase 2 (`UPSTREAM_LLM_URL`, `UPSTREAM_STT_URL`, `UPSTREAM_EMBED_URL`, `UPSTREAM_HEALTH_BRIDGE_URL`). `GET /v1/health/upstreams` na Fase 2 reporta os 3 upstreams locais derivados dos env vars + status via pod health-bridge `:9100`. Tabela `upstreams` (com circuit state, priority, last_probe) é **introduzida na Fase 3** quando o fallback chain precisa persistir estado por upstream.
- **D-D4:** Isolamento no DO Postgres compartilhado: schema dedicado `ai_gateway` + role dedicada `ai_gateway_app` com `GRANT` apenas no schema. DSN único via env `AI_GATEWAY_PG_DSN`. Migrations setam `search_path = ai_gateway` no header de cada arquivo.
- **D-D5:** Schema inicial (Fase 2): `tenants`, `api_keys`, `audit_log` (partitioned), `audit_log_content` (partitioned), `model_aliases` (mapa `model: "qwen"` → URL/identificador upstream), `usage_counters` (esqueleto vazio — Fase 4 popula). Zero `billing_events` nesta fase (Fase 4).

### Plumbing (Claude's Discretion)

- Layout do monorepo: `gateway/cmd/gateway/main.go`, `gateway/cmd/gatewayctl/main.go`, `gateway/internal/{auth,audit,idempotency,proxy,config,httpx,obs}`, `gateway/db/{migrations,queries}`, `gateway/internal/db/gen/` (saída `sqlc`). `pkg/openai` já existe.
- `X-Request-ID`: UUIDv7 (ordenação temporal ajuda queries em `audit_log`). Se cliente mandar header `X-Request-ID`, aceita se formato válido e **escopo é dele** (logs carregam tanto `request_id` gerado quanto `client_request_id`); senão gateway gera.
- HTTP server timeouts: `ReadHeaderTimeout: 10s`, `ReadTimeout: 60s` (whisper multipart precisa), `WriteTimeout: 0` (streaming livre), `IdleTimeout: 120s`, `MaxHeaderBytes: 1 MiB`. Request body max: 25 MB (bate com OpenAI API para áudio Whisper).
- `httputil.ReverseProxy`: `FlushInterval: -1` obrigatório no transport customizado (SSE). `ErrorHandler` retorna 502 com envelope OpenAI. Request-ID propagado como `X-Request-ID` ao pod.
- Sentry Go SDK habilitado desde dia-1 (padrão Ifix), DSN via env `SENTRY_DSN`; redaction hooks para `Authorization`/`X-API-Key`.
- Prometheus scaffold: `/metrics` existe mas expõe **apenas** contadores básicos na Fase 2 (`gateway_requests_total{route,status}`, `gateway_audit_dropped_total`). Cardinality budget rico fica Fase 7.
- Traefik: gateway expõe HTTP puro na VPS em `:8080`; TLS + DNS `gateway.ifix.com.br` são Fase 10 (PRD-07). Fase 2 roda acessível na rede privada Ifix (ou via port-forward p/ testes).
- Testes: unit para auth/idempotency/audit-buffer. Integration com `testcontainers-go` (Postgres 16 + Redis 7) para migrations + auth end-to-end + idempotency replay. E2E batendo em pod de teste via env var `GATEWAY_E2E_UPSTREAM_URL` (opt-in, rodável manualmente). `go test ./...` verde no CI.
- Postgres pool: `pgxpool.Config` padrão `MaxConns: 10`, `MaxConnIdleTime: 5min`. Conexões não compartilhadas entre áreas de escrita hot (audit flusher tem pool dedicado ou canal separado via mesmo pool).

### Folded Todos

Nenhum todo promovido — os opens em STATE.md pertencem a Fases 3/5/6/7/9, não à Fase 2.

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Project docs (internal)

- `.planning/PROJECT.md` — Vision, Core Value ("failover invisível"), constraints gerais (Go + Docker Compose + Portainer), Key Decisions table
- `.planning/REQUIREMENTS.md` §Gateway — Core HTTP (Go) — requirements GW-01..GW-10 (fonte de escopo)
- `.planning/REQUIREMENTS.md` §Multi-tenant — auth rows (TEN-01, TEN-02, TEN-08, TEN-09 — todas Fase 2)
- `.planning/REQUIREMENTS.md` §Out of Scope — regras explícitas (não construir PII redaction centralizada, não SSO, não Fiber, etc.)
- `.planning/ROADMAP.md` §Phase 2 — Goal, Depends-on (Phase 1), Success Criteria 1-5
- `.planning/STATE.md` — Decisões travadas + open todos que tocam Fase 3/5/6/7/9
- `.planning/phases/01-gpu-pod-image-smoke-test/01-CONTEXT.md` — Decisões D-13 (structs OpenAI compartilhadas) e D-10..D-12 (health-bridge :9100 contract)

### Repo conventions (internal)

- `docs/CONVENTIONS.md` — gofmt/go vet/golangci-lint obrigatórios, slog `module=`, RFC3339, sentinel errors, conventional commits com scope
- `pkg/openai/types.go` — tipos OpenAI-compat compartilhados (Chat, Embedding, Transcription, Error) — gateway **importa este módulo**, não redefine tipos
- `/home/pedro/projetos/pedro/CLAUDE.md` — convenções Ifix-wide (kebab-case, Bun/TS para outras apps, Sentry pattern, Better Auth pattern)

### Research bundle (internal)

- `.planning/research/SUMMARY.md` — resumo executivo; confirma stack Go + chi + pgx + go-redis para Fase 2
- `.planning/research/STACK.md` §Gateway HTTP — chi v5 + httputil.ReverseProxy + slog; por que NÃO Fiber
- `.planning/research/ARCHITECTURE.md` §Gateway components — subsistemas (router, auth, dispatcher, metrics)
- `.planning/research/PITFALLS.md` §Pitfall 5 — LGPD em failover (justifica D-B1/D-B2 data_class-aware audit)
- `.planning/research/PITFALLS.md` §Pitfall 9 — Goroutine leaks em streams longos (justifica ModifyResponse cleanup em D-B5)
- `.planning/research/PITFALLS.md` §Pitfall 10 — Connection pool HTTP (informa pgxpool config)

### Upstream components (HIGH confidence)

- https://github.com/go-chi/chi — chi v5 (router, middlewares padrão)
- https://pkg.go.dev/net/http/httputil#ReverseProxy — ReverseProxy + FlushInterval: -1 para SSE
- https://github.com/jackc/pgx — pgx v5 (driver Postgres, pgxpool)
- https://github.com/redis/go-redis — go-redis v9 (Redis client)
- https://github.com/pressly/goose — goose (migrations, go:embed compatible)
- https://sqlc.dev/ — sqlc (SQL→Go type-safe codegen)
- https://pkg.go.dev/log/slog — slog (structured logging stdlib)
- https://pkg.go.dev/golang.org/x/crypto/argon2 — argon2id reference impl (ou `github.com/alexedwards/argon2id` para ergonomia)
- https://docs.sentry.io/platforms/go/ — Sentry Go SDK (padrão Ifix)

### External reference (ecosystem context)

- https://stripe.com/docs/api/idempotent_requests — Idempotency-Key semantics (referência para D-C1..D-C3)
- https://platform.openai.com/docs/api-reference — contratos de request/response OpenAI-compat a espelhar
- https://owasp.org/www-project-proactive-controls/v4/en/c7-enforce-access-controls — argon2id / secret storage patterns

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets

- **`pkg/openai/types.go`** (181 linhas) já define `ChatCompletionRequest/Response`, `EmbeddingRequest/Response`, `TranscriptionRequest/Response`, `ToolCall`, `ErrorResponse`, `ErrorDetail`. **Gateway importa como-é**, não reimplementa. Pod health-bridge também consome este pacote — single source of truth do contrato.
- **`pod/health-bridge/main.go` + `probes.go`** — padrão de probe HTTP interno + agregação de status; gateway pode espelhar o mesmo shape de response em `GET /v1/health/upstreams` (consistência de operação).
- **`docs/CONVENTIONS.md`** — convenções já travadas. Gateway **não** redocumenta style guide, segue o existente.
- **`pod/health-bridge/handlers.go`** — padrão de handler Go com `http.ResponseWriter` + JSON encoding + 503 em unhealthy; base estilística para handlers do gateway.

### Established Patterns

- **Monorepo Go** (`go.mod` em `/` com `module github.com/ifixtelecom/gpu-ifix`) — gateway vive em `gateway/` paralelo a `pod/`, compartilha `pkg/`.
- **Docker image per subsystem** no padrão Ifix: `ghcr.io/ifixtelecom/ifix-ai-pod` existe; gateway publica `ghcr.io/ifixtelecom/ifix-ai-gateway` com tags `{branch}`, `{branch}-{sha}`, `latest` + promoção manual pra `v1.0.0`.
- **GitHub Actions + webhook Portainer** — Fase 1 tem `build-pod.yml`; Fase 2 adiciona `build-gateway.yml` (mesma estrutura) + Portainer stack `ai-gateway-dev`/`ai-gateway-prod`.
- **slog NDJSON** com atributo `module` em `UPPER_SNAKE_CASE` — gateway usa `module=GATEWAY`, `module=AUTH`, `module=AUDIT`, `module=IDEM`, `module=PROXY`.
- **Sentinel errors** pacote-level (`ErrUpstreamDown` existe em `pod/health-bridge`) — gateway define `ErrInvalidAPIKey`, `ErrRevokedAPIKey`, `ErrIdempotencyConflict`, etc.

### Integration Points

- **Fase 1 pod** (`ghcr.io/ifixtelecom/ifix-ai-pod`): gateway consome LLM `:8000`, STT `:8001`, embed `:8002`, health-bridge `:9100`, dcgm-exporter `:9400` (pra Fase 5 usar via gateway). **Fase 2 só bate em 8000/8001/8002 + 9100 derivado.**
- **DO Postgres compartilhado**: schema novo `ai_gateway` + role `ai_gateway_app`. Coordenar com outras apps Ifix para evitar conflito de schema name.
- **Redis** compartilhado da infra Ifix (mesmo padrão de `converseai-v4`). Key prefix `gw:` para todas as chaves do gateway (`gw:apikey:*`, `gw:idem:*`).
- **MinIO Ifix**: job de export de auditoria (cold storage 1 ano) escreve em bucket `ifix-ai-gateway-audit-cold` com estrutura `{year}/{month}/audit_log_{YYYYMM}.parquet` e `audit_log_content_{YYYYMM}.parquet`.
- **Fase 3 (futuro próximo)**: vai consumir `audit_log` (para decisão de fallback LGPD), estender `upstreams` table, adicionar circuit breakers por upstream. Schema atual precisa aguentar isso sem refactor grande — por isso `audit_log` já tem `data_class` e `upstream` colunas.

</code_context>

<specifics>
## Specific Ideas

- **Política LGPD é default de schema, não opção de código** (D-B2). `audit_log_content` tem row **ausente** para `sensitive`; middleware não tem flag "should_log_content" — simplesmente consulta `data_class` do api_key e decide escrita do content. Isso mata a chance de regressão futura onde alguém "esquece" de redactar.
- **Idempotency-Key casa com Stripe, não OpenAI** (D-C2). OpenAI não tem idempotency-key público; adotamos o padrão do ecossistema fintech (Stripe) porque é o que apps cliente vão provavelmente usar (especialmente Cobranças/Campanhas) e porque é quem documentou melhor as edge cases (collision, TTL, replay).
- **`gatewayctl` é primeira superfície admin**. Fase 7 adiciona dashboard + endpoints REST; Fases 3-6 podem usar `gatewayctl` pra seed de dados de teste. Vale investir em subcomandos bem nomeados desde o início.
- **Fase 2 deploya sem HTTPS direto**. TLS/DNS é Fase 10 (PRD-07). Apps cliente que quiserem testar integração na Fase 2 vão via IP privado Ifix ou tunnel interno. Isso é intencional: entrega valor incremental sem bloquear em DNS público.
- **Single upstream rige `httputil.ReverseProxy`** com `Director` fixo (3 proxies: um por rota OpenAI). Refactor para multi-upstream com seleção dinâmica é mudança contida em `gateway/internal/proxy/` quando Fase 3 entrar.
- **Redis key prefix `gw:` coexiste com outros produtos Ifix** no mesmo Redis compartilhado (converseai-v4 usa seu próprio namespace). Documentar no README qual namespace o gateway ocupa.

</specifics>

<deferred>
## Deferred Ideas

- **Admin REST endpoints** (`POST /admin/tenants`, `/admin/keys`) — **deferido para Fase 7** (dashboard Next.js consome).
- **Tabela `upstreams`** com circuit state, priority, last_probe — **Fase 3** introduz quando fallback chain precisa persistir estado por upstream.
- **Idempotency-Key em `/v1/embeddings` e `/v1/audio/transcriptions`** — deferido. Embeddings são determinísticos (mesmo input → mesmo output), cache é responsabilidade da app cliente. Whisper multipart exige hashing do body grande — reconsiderar se algum app cliente pedir.
- **Prometheus metrics ricas (P50/P95/P99, labels de tenant, upstream latency histograms)** — Fase 7 entrega conforme OBS-01/OBS-02. Fase 2 expõe stub.
- **HTTPS/TLS + DNS `gateway.ifix.com.br`** — Fase 10 (PRD-07) com Cloudflare.
- **SSO / Better Auth para admin surface** — Fase 10 (PRD-06).
- **Model alias → versão via tabela com priority/versioning** — Fase 2 usa mapa simples; reconsiderar se Fase 8/9 exigir modelo diferente por tenant (não está no requirements atual).
- **Export format da auditoria cold** — Parquet é recomendação; alternativa JSONL gzip se Parquet provar custoso. Decide em runtime do primeiro export.
- **Separate `billing_events` writes** — esqueleto `usage_counters` existe na Fase 2 mas populado pela Fase 4. Gateway **não** calcula custo nesta fase.

### Reviewed Todos (not folded)

Nenhum todo aberto era relevante ao Fase 2.

</deferred>

---

*Phase: 02-gateway-core-multi-tenant-auth*
*Context gathered: 2026-04-18*
