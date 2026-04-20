# Phase 4: Multi-tenant Quotas, Billing & Schedule Routing - Context

**Gathered:** 2026-04-20
**Status:** Ready for planning

<domain>
## Phase Boundary

Transforma o gateway multi-upstream resiliente da Fase 3 em um gateway multi-tenant com isolamento econômico:

1. **Rate-limit per-key** atomic via Redis Lua (RPS + RPM no mesmo script) cobrindo SC-1 + SC-5 (TEN-03)
2. **Quotas diárias e mensais** por dimensão (tokens_in+out de LLM, audio_seconds de STT, embeds_count) com bloqueio atomic; rollover em America/Sao_Paulo (TEN-04)
3. **Token counting + custo BRL** por request, gravado em nova tabela `billing_events` append-only particionada (idempotência por `request_id`); inclui `cost_local_phantom_brl` (notional OpenRouter-equivalent) além de `cost_external_brl` (TEN-06, SC-2)
4. **Endpoint admin `GET /admin/usage`** com breakdown diário + summary, autenticado via `X-Admin-Key` bcrypt em DB; retorna `{tenant, tokens_in, tokens_out, audio_seconds, embeds_count, cost_local_brl, cost_local_phantom_brl, cost_external_brl, cost_total_brl}` por dia e summary do range (TEN-07, SC-3)
5. **Schedule routing per-tenant** (`mode='24/7'` ou `mode='peak'` com `peak_window_start/end` + `schedule_timezone`); off-hours em peak roteia direto para tier-1 OpenRouter (Fireworks pin Fase 3 D-C1) sem checar tier-0 local (TEN-05, SC-4)
6. **Sensitive + peak é inválido** — CHECK constraint no DB + validação em `gatewayctl`; gateway faz fail-fast em boot se inconsistência for detectada via SQL bruto
7. **Middleware chain ordenado:** `auth → idempotency → rate-limit → quota → schedule → tokencount → dispatcher → billing-flush (ModifyResponse hook + defer)`
8. **Tabela `ai_gateway.prices`** com hot-reload via `LISTEN/NOTIFY` (mesmo pattern de upstreams Fase 3 D-D4) — operador edita preço/fx via `gatewayctl prices` sem deploy
9. **Suite completa de subcomandos `gatewayctl`:** `tenant set-mode|set-quota`, `prices set|list|set-fx`, `usage report`, `admin-key create|revoke`
10. **Wire de instrumentação básica** (`obs.RequestsTotal.WithLabelValues(route,status).Inc()` no proxy layer — folded de STATE.md TODO Phase 4)
11. **Per-route `WriteTimeout`** restored (chat=0 para SSE, embeddings=30s, audio=120s) — slow-client-DoS defense agora justificada por rate-limit existente (folded de STATE.md TODO Phase 3)

**Fora de escopo desta phase:**
- Load shedding baseado em saturação (inflight + P95 + VRAM) e per-tenant inflight fairness → Fase 5 (Pitfall 9)
- Auto-provisioning emergency pod via Vast.ai → Fase 6
- Dashboard Next.js + Prometheus histograms ricos por tenant + WhatsApp/email alerts → Fase 7 (Pitfall 13/14)
- Cost reconciliation com fatura externa OpenRouter/OpenAI → Fase 7 (Pitfall 8)
- Integração das apps cliente (ConverseAI, Chat Ifix, Telefonia, Cobranças, Campanhas, voice-api) → Fases 8-9
- Better Auth/SSO no admin endpoint → Fase 10 (PRD-06); v1 usa `X-Admin-Key` bcrypt
- HTTPS/TLS + DNS público `gateway.ifix.com.br` → Fase 10 (PRD-07)
- Cache semântico, request shadowing, prompt caching cross-provider → v2 (REQUIREMENTS §v2)

</domain>

<decisions>
## Implementation Decisions

### Rate-limit + quota semantics (TEN-03, TEN-04, SC-1, SC-5)

- **D-A1 (Token bucket Lua atomic):** Rate-limit usa script Lua atomic em Redis. Cobre RPS e RPM no mesmo `EVALSHA` (refill por timestamp). Chave namespaced: `gw:rate:{tenant_id}:{route_class}:{window}` onde `route_class ∈ {chat, embed, stt}` e `window ∈ {rps, rpm}`. Padrão Stripe/Cloudflare. Bate SC-5 (1000 concurrent zero over-use) trivialmente — Lua é single-threaded em Redis. P95 esperado <1ms.
- **D-A2 (Fail policy híbrida em Redis down):**
  - **Rate-limit fail-open** — se `EVALSHA` retornar erro de transporte, deixa request passar; preserva core-value "failover invisível" durante incidente Redis curto. Métrica `gateway_rate_limit_check_failures_total{reason}`.
  - **Quota fail-closed** — se lookup de quota falhar (Redis ou Postgres `usage_counters` cache), retorna 503 `{error:{type:'service_unavailable', code:'quota_check_unavailable', message:'Quota check unavailable; refusing to risk runaway external cost.'}}`. Pitfall 8 — sem visibilidade de custo, parar é melhor que torrar OpenRouter/OpenAI.
  - Audit log entry com `upstream='quota_check_unavailable'` (valor reservado novo).
- **D-A3 (Timezone do rollover diário):** `America/Sao_Paulo` (alinhado com convenção Ifix `cobrancas-api`/CLAUDE.md TZ-aware logger). "Daily" para usuário = 00:00 horário Brasília. Mesma TZ usada por `peak_window` (TEN-05) — uma única `*time.Location` carregada no boot via `time.LoadLocation("America/Sao_Paulo")`.
- **D-A4 (Error codes discriminados por período + dimensão):**
  - Rate-limit: `rate_limit_rps`, `rate_limit_rpm` (com header `Retry-After: <seconds>`)
  - Quota: `quota_exceeded_daily_tokens`, `quota_exceeded_daily_audio_minutes`, `quota_exceeded_daily_embeds`, `quota_exceeded_monthly_tokens`, `quota_exceeded_monthly_audio_minutes`, `quota_exceeded_monthly_embeds`
  - Envelope OpenAI completo (`type='rate_limit_error'` para 429, `type='insufficient_quota'` para quota exceeded). Permite apps cliente (Cobranças, Campanhas) decidir programaticamente: pausar campanha (`monthly_*`) vs degradar para template menor (`daily_tokens`).

### Billing schema & cost model (TEN-06, TEN-07, SC-2)

- **D-B1 (Nova tabela `billing_events` append-only + `usage_counters` como cache):**
  ```sql
  CREATE TABLE ai_gateway.billing_events (
    request_id            UUID NOT NULL,
    ts                    TIMESTAMPTZ NOT NULL,
    tenant_id             UUID NOT NULL REFERENCES ai_gateway.tenants(id),
    api_key_id            UUID,
    route                 TEXT NOT NULL,                      -- 'chat'|'embed'|'stt'
    upstream              TEXT NOT NULL,                      -- 'local-llm'|'openrouter-chat'|...
    model                 TEXT NOT NULL,                      -- resolved model name
    tokens_in             INTEGER NOT NULL DEFAULT 0,
    tokens_out            INTEGER NOT NULL DEFAULT 0,
    audio_seconds         REAL    NOT NULL DEFAULT 0,
    embeds_count          INTEGER NOT NULL DEFAULT 0,
    cost_local_brl        NUMERIC(10,6) NOT NULL DEFAULT 0,   -- always 0 (GPU is fixed-cost)
    cost_local_phantom_brl NUMERIC(10,6) NOT NULL DEFAULT 0,  -- notional OpenRouter-equivalent
    cost_external_brl     NUMERIC(10,6) NOT NULL DEFAULT 0,
    currency              TEXT NOT NULL DEFAULT 'BRL',
    source                TEXT NOT NULL,                      -- 'final'|'partial' (abnormal close)
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (request_id, ts)
  ) PARTITION BY RANGE (ts);
  CREATE INDEX idx_billing_events_tenant_ts ON ai_gateway.billing_events (tenant_id, ts DESC);
  ```
  - Particionamento mensal (mesmo padrão `audit_log` Fase 2 D-B3). Retenção: indefinida hot (compliance/exec exige histórico anual; tabela é append-only e magra ~200B/row). Re-evaluate em Fase 7 quando se medir crescimento real.
  - **Idempotência:** `INSERT ... ON CONFLICT (request_id, ts) DO NOTHING` — retries não duplicam. Pitfall 8.
  - **`usage_counters` evolui** (Fase 2 D-D5 deixou skeleton): adiciona `audio_seconds BIGINT`, `embeds_count BIGINT`, `cost_local_phantom_brl NUMERIC(10,4)`, `cost_external_brl NUMERIC(10,4)`. Funciona como **cache diário** atualizado on-emission (UPDATE incremental). Hot path de quota check faz `SELECT * FROM usage_counters WHERE tenant_id=$1 AND date=CURRENT_DATE AT TIME ZONE 'America/Sao_Paulo'` (1 lookup, índice PK) — não SUM rolling. Reconciliação noturna (`gatewayctl billing reconcile`) compara contra `SUM(billing_events) GROUP BY tenant_id, date` e corrige drift (alarme se >0.1%).

- **D-B2 (Streaming accounting on-emission + flush no fim):** Pitfall 8 mitigation.
  - **In-process counter por request** (`requestUsage struct{tokensIn, tokensOut, audioSeconds atomic int64}`) — incrementa atomic em cada SSE delta parsed pelo `ModifyResponse` interceptor (extends Fase 3 tool-call interceptor).
  - **Flush único no fim:** `[DONE]` event OU `defer { flushBilling() }` em abnormal close (cliente desconectou, upstream cortou). Single `INSERT ON CONFLICT DO NOTHING` em `billing_events` com `source='final'` ou `source='partial'`.
  - **Update incremental em `usage_counters`** dentro da mesma transação do INSERT, via `INSERT ... ON CONFLICT (tenant_id, date) DO UPDATE SET tokens_in = usage_counters.tokens_in + EXCLUDED.tokens_in, ...` — atomic accumulator; quota check enxerga consumo do request anterior.
  - Cliente desconecta no token 50? DB grava 50 com `source='partial'`. Atende SC-2 (billing_events row para todo request completed) E protege contra under-reporting silencioso.
  - **Non-streaming:** counter populado a partir do response.usage retornado (OpenAI/OpenRouter envia; local llama-server também) ou tokenizado via `TokenCounter` (Fase 3) se ausente.

- **D-B3 (Price table em DB + hot-reload via LISTEN/NOTIFY):**
  ```sql
  CREATE TABLE ai_gateway.prices (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    model         TEXT NOT NULL,                              -- 'qwen-3.5-27b'|'whisper-large-v3'|'bge-m3'|'text-embedding-3-small'|'gpt-whisper-1'
    provider      TEXT NOT NULL,                              -- 'local'|'openrouter-fireworks'|'openai'
    unit          TEXT NOT NULL,                              -- 'input_token'|'output_token'|'audio_second'|'embed_request'
    unit_cost_usd NUMERIC(12,8) NOT NULL,
    valid_from    TIMESTAMPTZ NOT NULL DEFAULT now(),
    valid_to      TIMESTAMPTZ,                                -- NULL = currently active
    notes         TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (model, provider, unit, valid_from)
  );
  ```
  - **fx USD→BRL:** tabela paralela `ai_gateway.fx_rates (currency_pair TEXT, rate NUMERIC(10,6), valid_from, valid_to)`. Operador atualiza semanalmente: `gatewayctl prices set-fx --usd-brl 5.10`.
  - **Hot-reload:** mesmo pattern de Fase 3 D-D4 — trigger `notify_prices_changed` em `prices` e `fx_rates`, gateway escuta canal `prices_changed` via conexão pgx dedicada e atomic-swap do in-memory map.
  - **Seed inicial (migration):** preços OpenAI/OpenRouter conforme PROJECT.md Custos de referência (Apr 2026): `openai-text-embedding-3-small` $0.02/1M tokens, `openai-whisper-1` $0.006/min, `openrouter-fireworks-qwen-3.5-27b` $0.00012/input + $0.00060/output (verificar valores atuais via Fireworks Apr 2026 antes de execute). fx default `USD/BRL=5.10`.

- **D-B4 (cost_local phantom dual-column):**
  - `cost_local_brl` é **sempre 0** (Vast.ai 4090 é fixed-cost mensal, ~$84/mês ÷ tokens emitidos = unit cost incalculável on-the-fly e volátil).
  - `cost_local_phantom_brl = tokens × price[openrouter-fireworks-qwen-3.5-27b]` — calculado on-emission usando preços do tier-1 equivalente. Resposta de `/admin/usage` SC-3 expõe ambas; reports executivos podem responder "GPU economizou X reais este mês = Σ(cost_local_phantom) − GPU_monthly_cost".
  - Para STT: phantom = `audio_seconds × price[openai-whisper-1]/60`. Para embed: `embeds_count × tokens_per_embed_avg × price[openai-text-embedding-3-small]`.
  - `cost_external_brl` é o real cobrado quando upstream foi tier-1 (OpenRouter/OpenAI). `cost_local_phantom_brl=0` quando upstream NÃO foi tier-0; `cost_external_brl=0` quando upstream foi tier-0.

### Schedule routing peak/24-7 (TEN-05, SC-4)

- **D-C1 (Config em colunas de `tenants`):** Schema migration adiciona:
  ```sql
  ALTER TABLE ai_gateway.tenants
    ADD COLUMN mode TEXT NOT NULL DEFAULT '24/7' CHECK (mode IN ('24/7', 'peak')),
    ADD COLUMN peak_window_start TIME,                       -- NULL para mode='24/7'; default seed '08:00'
    ADD COLUMN peak_window_end   TIME,                       -- default seed '22:00'
    ADD COLUMN schedule_timezone TEXT NOT NULL DEFAULT 'America/Sao_Paulo',
    ADD CONSTRAINT chk_sensitive_no_peak CHECK ((mode = '24/7') OR (data_class = 'normal'));
  ```
  - **Defesa em profundidade contra sensitive+peak:**
    1. `gatewayctl tenant set-mode --tenant cobrancas --mode peak` rejeita com `cannot set peak mode for sensitive tenant (LGPD policy: external providers blocked)` antes de tocar DB.
    2. CHECK constraint impede INSERT/UPDATE bruto via SQL.
    3. **Boot-time validation:** gateway inicia `SELECT id, slug FROM tenants WHERE mode='peak' AND data_class='sensitive'` — se row encontrada (CHECK foi bypass via `SET session_replication_role = replica` ou similar), `slog.Error` + `os.Exit(1)`. Falha-rápida defensiva.
  - 6 tenants apenas — nenhum precisa múltiplas janelas (Cobranças "pular almoço" etc.). Tabela dedicada deferida.

- **D-C2 (Off-hours roteia direto para tier-1 OpenRouter):**
  - Schedule middleware decide pre-dispatch: se `mode='peak'` E `time.Now().In(tz)` está fora do `[peak_window_start, peak_window_end)` → seleciona `tier=1` na role correta (`openrouter-chat` para LLM). **Skipa tier-0 local mesmo se breaker CLOSED** — GPU pode estar desligada (justificativa de peak mode é desligar Vast.ai à noite para economia).
  - Reusa Director `openrouter-chat` + Fireworks pin (Fase 3 D-C1, D-C2). Sem drift de modelo (Qwen 27B em ambos).
  - **Se OpenRouter também down em off-hours:** 503 `{error:{type:'service_unavailable', code:'off_hours_upstream_unavailable', message:'Tenant in peak mode and off-hours external upstream unavailable.'}}` (não cai pra OpenAI direct chat — viola Fase 3 D-C4 "sem fallback de fallback para chat" e decisão "Qwen fixo" do PROJECT.md).
  - **STT/embed em off-hours:** rota independente, mantém fallback chain Fase 3 normal (`local-stt → openai-whisper`, `local-embed → openai-text-embedding-3-small`). Não viola "GPU desligada à noite" porque STT/embed locais já estão DOWN se VPS GPU foi suspensa — breaker abre rápido.

- **D-C3 (Sensitive + peak é inválido na criação):** Resolvido em D-C1 via CHECK constraint + gatewayctl validation + boot-time fail-fast. Coerente com Fase 3 D-B1 (sensitive nunca proxia para external). Documentar no runbook (Fase 7/10).

- **D-C4 (Cache check schedule pre-dispatch in-memory):**
  - **Loader pattern Fase 3 D-D4:** novo `gateway/internal/tenants/loader.go` carrega `SELECT id, slug, data_class, mode, peak_window_start, peak_window_end, schedule_timezone, daily_quota_*, monthly_quota_*, rps_limit, rpm_limit FROM tenants` no boot. Atomic-swap `map[uuid.UUID]TenantConfig` com RWMutex.
  - **Hot-reload via NOTIFY canal `tenants_changed`** (trigger novo) — operador edita via `gatewayctl tenant set-*`, gateway recarrega <1s.
  - **Decisão de schedule em request hot path:** `cfg.Mode == "peak" && !inWindow(time.Now().In(cfg.tz), cfg.start, cfg.end)`. Zero RTT. Decisão gravada em request context para audit log + billing (`audit_log.upstream` reflete `openrouter-chat` mesmo se primary CLOSED).
  - `time.Location` carregada uma vez em boot por gateway process; reusada em loader Refresh (não recria cada request).

### Middleware chain & admin surface

- **D-D1 (Ordem do middleware chain):**
  ```
  Request → routerMiddleware (chi route)
         → authMiddleware (X-API-Key/Bearer → tenant_id, data_class, api_key_id no ctx)
         → idempotencyMiddleware (replay cacheado retorna early — Fase 2 D-C1..C5)
         → rateLimitMiddleware (Lua atomic; 429 rate_limit_{rps,rpm})
         → quotaMiddleware (lookup usage_counters + tenant.daily_quota_*; 429 quota_exceeded_*)
         → scheduleMiddleware (decide upstream tier baseado em mode + window; grava decision em ctx)
         → tokencountMiddleware (Fase 3 — só pra chat/embed; rejeita context_length_exceeded pre-dispatch)
         → dispatcher (Director + ReverseProxy + breaker check; ModifyResponse intercepta tool-call + token usage)
         → billingFlush (defer ou onComplete; UPSERT billing_events + usage_counters)
  ```
  - **Idempotency replay AINDA consome quota** (não rate-limit) — alinhado a Stripe ("toda chamada é cobrável; idempotência é proteção contra retry, não isenção"). Replay cache hit ainda incrementa `usage_counters` com `source='replay'` para rastreabilidade. Nota explícita em audit (`idempotency_replayed=true`).
  - **Rate-limit consome SEM consumir quota** — RPS/RPM rejeições não decrementam quota (cliente não foi atendido).
  - **Schedule depois de quota:** se quota exceeded, retorna 429 sem decidir upstream (não vaza informação sobre routing pra cliente bloqueado). Schedule só roda se chega no dispatcher.

- **D-D2 (Endpoint admin reporting `GET /admin/usage`):**
  - Query string: `?tenant=<slug-or-uuid>&from=<ISO-date>&to=<ISO-date>&granularity=day|month` (default `day`)
  - Response shape:
    ```json
    {
      "tenant": {"id": "uuid", "slug": "converseai", "name": "ConverseAI", "data_class": "normal", "mode": "24/7"},
      "range": {"from": "2026-04-01", "to": "2026-04-30", "granularity": "day", "timezone": "America/Sao_Paulo"},
      "summary": {
        "tokens_in": 1234567, "tokens_out": 543210,
        "audio_seconds": 3600, "embeds_count": 1500,
        "cost_local_brl": 0.0,
        "cost_local_phantom_brl": 12.34,
        "cost_external_brl": 5.67,
        "cost_total_brl": 5.67,
        "requests_count": 8910
      },
      "rows": [
        {"date": "2026-04-01", "tokens_in": ..., "cost_total_brl": ...},
        ...
      ]
    }
    ```
  - Fonte: `SELECT FROM billing_events WHERE tenant_id=$1 AND ts BETWEEN $2 AND $3 GROUP BY date_trunc(...)` — **não** `usage_counters` (que é cache potencialmente fora de sync). Reports são autoridade.
  - Atende SC-3 fields exatos.

- **D-D3 (Auth admin: X-Admin-Key bcrypt em DB + bootstrap env):**
  ```sql
  CREATE TABLE ai_gateway.admin_keys (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    key_hash    TEXT NOT NULL,                                -- bcrypt cost 10 (mais barato que argon2 pra admin path low-frequency)
    key_prefix  TEXT NOT NULL,                                -- 'ifix_admin_****abcd' display
    label       TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'active',               -- 'active'|'revoked'
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at  TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ
  );
  ```
  - **Bootstrap:** migration insere row com `key_hash` derivado de env `AI_GATEWAY_ADMIN_KEY_BOOTSTRAP` (operador define no Portainer stack); se env vazio em produção, gera key random + log warn `ROTATE THIS KEY IMMEDIATELY: ifix_admin_<hex>` no boot (idempotente: só insere se tabela vazia).
  - **Header:** `X-Admin-Key: ifix_admin_<key>`. Middleware `adminAuthMiddleware` em todas rotas `/admin/*`. Cache Redis 60s (mesmo TTL de api_keys cache Fase 2 D-A2).
  - **Rotação:** `gatewayctl admin-key create --label 'Pedro 2026-04'` imprime key uma vez, hash em DB. `gatewayctl admin-key revoke --id <uuid>` ou `--label X`.
  - Substituído por Better Auth/SSO em Fase 10 (PRD-06). Tabela `admin_keys` permanece para fallback; coluna `auth_method` opcional indica `bcrypt|sso`.

- **D-D4 (Suite completa `gatewayctl`):**
  - `gatewayctl tenant set-mode --tenant <slug> --mode {24/7|peak} [--window 08-22] [--tz America/Sao_Paulo]`
  - `gatewayctl tenant set-quota --tenant <slug> --daily-tokens N --monthly-tokens N --daily-audio-minutes N --monthly-audio-minutes N --daily-embeds N --monthly-embeds N --rps N --rpm N`
  - `gatewayctl prices set --model X --provider Y --unit Z --usd N` (insere row com `valid_from=now()`; row anterior fica com `valid_to=now()`)
  - `gatewayctl prices list [--active]`
  - `gatewayctl prices set-fx --usd-brl N`
  - `gatewayctl billing reconcile [--from --to]` — compara `SUM(billing_events) GROUP BY tenant,date` vs `usage_counters`; alarme se drift >0.1%; opcionalmente `--apply` corrige `usage_counters`.
  - `gatewayctl usage report --tenant X --from Y --to Z [--format json|table]` — chama `/admin/usage` localmente via socket interno (admin key reusa env bootstrap em CLI).
  - `gatewayctl admin-key create --label X` / `gatewayctl admin-key revoke --id|--label X` / `gatewayctl admin-key list`

### Claude's Discretion

- **Lua script shape (token bucket):** Decisão técnica clássica. Padrão Stripe/Cloudflare: `EVALSHA` com `KEYS={bucket_key}`, `ARGV={now_ms, capacity, refill_rate, refill_interval_ms, requested_tokens}`. Retorna `{allowed_bool, remaining_tokens, reset_ms}`. Hot reload do script via `SCRIPT LOAD` no boot (cache `script_sha`).
- **Headers `X-RateLimit-*` shape (OpenAI-compat):** `X-RateLimit-Limit-Requests`, `X-RateLimit-Limit-Tokens`, `X-RateLimit-Remaining-Requests`, `X-RateLimit-Remaining-Tokens`, `X-RateLimit-Reset-Requests`, `X-RateLimit-Reset-Tokens`. Calculados a partir do response do Lua script + lookup quota. Em response 200 e 429.
- **Quota defaults seed:** Migration insere defaults conservadores per-tenant (requirements não especifica): `daily_tokens=10M`, `monthly_tokens=300M`, `daily_audio_minutes=600`, `monthly_audio_minutes=18000`, `daily_embeds=100k`, `monthly_embeds=3M`, `rps=20`, `rpm=600`. Operador refina via `gatewayctl tenant set-quota` antes de Fase 8/9 ativarem apps em prod. Documentar no runbook.
- **Pricing seed values:** Conferir Fireworks Apr 2026 official pricing antes de migration:
  - `qwen3-27b-instruct` Fireworks: ~$0.20/1M input, ~$0.60/1M output (verificar)
  - `text-embedding-3-small`: $0.02/1M tokens (PROJECT.md confirma)
  - `whisper-1` OpenAI: $0.006/min audio (PROJECT.md confirma)
  - fx default `USD/BRL=5.10` (operator atualiza na primeira semana de operação real)
  - Plan deve incluir `gsd-phase-researcher` task: confirmar pricing atual via Fireworks + OpenRouter + OpenAI APIs antes de migration.
- **Audit log entries para rejeições:** Mesmo padrão Fase 3 D-B3:
  - Rate-limit reject: `audit_log` row com `upstream='rate_limited'`, `error_code='rate_limit_rps'|'rate_limit_rpm'`, `status_code=429`. Sem `audit_log_content` (request rejeitado pre-dispatch).
  - Quota exceeded: `upstream='quota_exceeded'`, `error_code='quota_exceeded_*'`, `status_code=429`.
  - Quota check unavailable: `upstream='quota_check_unavailable'`, `error_code='quota_check_unavailable'`, `status_code=503`.
  - Off-hours external down: `upstream='off_hours_blocked'`, `error_code='off_hours_upstream_unavailable'`, `status_code=503`.
  - Bate consistência com `data_class='sensitive' + upstream='blocked_sensitive'` da Fase 3.
- **Per-route WriteTimeout restoration (folded TODO):** Refactor mecânico durante execute. `chat=0` (SSE), `embeddings=30s`, `audio=120s` (Whisper multipart pode demorar). Configurável via env vars `GATEWAY_WRITE_TIMEOUT_CHAT_S` etc. com defaults sensatos.
- **Wire `obs.RequestsTotal.Inc()` (folded TODO):** Adicionar middleware `metricsMiddleware` ao final do chain (executa em response). `route` é route template chi (ex: `/v1/chat/completions`); `status` é status class (`2xx`/`4xx`/`5xx`) — bate cardinality budget Phase 7 (Pitfall 13).
- **Plumbing:**
  - Novo package `gateway/internal/quota/` — `lua.go` (script + EVALSHA), `bucket.go` (config struct), `enforcer.go` (middleware), `errors.go` (sentinel: `ErrRateLimitRPS`, `ErrRateLimitRPM`, `ErrQuotaExceededDailyTokens`, etc.).
  - Novo package `gateway/internal/billing/` — `accountant.go` (in-process counters + flush), `prices.go` (loader hot-reload + USD→BRL conversion), `events.go` (sqlc UPSERT helpers), `usage.go` (`usage_counters` UPSERT in same txn).
  - Novo package `gateway/internal/tenants/` — `loader.go` (carrega tenants config + LISTEN/NOTIFY `tenants_changed`), `config.go` (struct), expandir migrações com colunas mode/quota.
  - Novo package `gateway/internal/schedule/` — `policy.go` (decisão pre-dispatch), `window.go` (helper `inWindow(now, start, end, tz)`).
  - Novo package `gateway/internal/admin/` — handlers `/admin/usage` + middleware `adminAuthMiddleware`, reusa pattern Fase 2 envelope OpenAI errors.
  - `gateway/db/migrations/`: `0010_create_billing_events.sql` (partitioned + seed partitions próximos 3 meses), `0011_evolve_usage_counters.sql` (ADD COLUMNs), `0012_create_prices_and_fx.sql` (+ trigger NOTIFY `prices_changed`), `0013_evolve_tenants_schedule_quota.sql` (+ trigger NOTIFY `tenants_changed` + CHECK sensitive+peak), `0014_create_admin_keys.sql`, `0015_seed_prices_and_quotas.sql`.
  - `gateway/db/queries/`: `billing.sql`, `usage_counters.sql`, `prices.sql`, `fx_rates.sql`, `tenants_admin.sql`, `admin_keys.sql`.
  - Métricas Prometheus novas (continuação da convenção Fase 3 obs/metrics.go): `gateway_rate_limit_rejected_total{tenant,window}`, `gateway_quota_rejected_total{tenant,dimension,period}`, `gateway_quota_check_failures_total{reason}`, `gateway_billing_flush_total{source}`, `gateway_billing_flush_failures_total{reason}`, `gateway_schedule_routing_total{tenant,decision}` (`decision=local|off_hours_external`), `gateway_admin_requests_total{route,status}`, `gateway_prices_reload_total{result}`, `gateway_tenants_reload_total{result}`.
  - Testes integration testcontainers-go: scenarios para (a) rate-limit Lua atomic com 1000 goroutines concorrentes (SC-5 explicit), (b) quota daily rollover na boundary 00:00 BRT, (c) sensitive+peak rejection (gatewayctl + DB CHECK + boot-time validation 3 paths), (d) streaming abnormal close gravando billing partial, (e) reconcile drift detection, (f) hot-reload prices + tenants.

### Folded Todos

- **[STATE.md] Phase 4: Wire request instrumentation middleware that calls `obs.RequestsTotal.WithLabelValues(route, status).Inc()` on the proxy layer** → resolvido em D-D1 (último middleware do chain) e Plumbing (metricsMiddleware).
- **[STATE.md] Phase 3 → Phase 4: Revisit per-route WriteTimeout (chat=0 for SSE, embeddings=30s, audio=120s) to restore slow-client-DoS defense on non-streaming routes** → resolvido em "Claude's Discretion / Per-route WriteTimeout restoration". Refactor mecânico durante execute desta fase.

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Project docs (internal)

- `.planning/PROJECT.md` — Vision, Core Value ("failover invisível"), Custos de referência (Apr 2026 — informa pricing seed), Key Decisions table (Qwen fixo, GPU 4090 4 vCPU, Postgres DO + Redis Ifix)
- `.planning/REQUIREMENTS.md` §Multi-tenant — TEN-03..TEN-07 (fonte de escopo desta phase)
- `.planning/REQUIREMENTS.md` §Out of Scope — regras explícitas (sem PII redaction centralizada, sem SSO, sem K8s)
- `.planning/ROADMAP.md` §Phase 4 — Goal, Depends-on (Phase 2 + 3), Success Criteria SC-1..SC-5
- `.planning/STATE.md` — Open todos foldados aqui (instrumentation + WriteTimeout); Phase 5/6/7/9 todos referenciados como out-of-scope
- `.planning/phases/02-gateway-core-multi-tenant-auth/02-CONTEXT.md` — Decisões Fase 2 que Fase 4 estende: D-A4 (`api_keys.data_class`), D-B1 (`audit_log` shape com `upstream` + `tokens_in/out` + `cost_brl`), D-B4 (audit pipeline async batched write — Fase 4 reusa para billing flush), D-B6 (Whisper metadata: `audio_duration_s` é fonte de `audio_seconds` em billing), D-D2 (sqlc + goose patterns), D-D5 (`usage_counters` skeleton — Fase 4 evolui), D-A3 (`gatewayctl` é admin surface primária — Fase 4 expande)
- `.planning/phases/03-resilience-fallback-chain/03-CONTEXT.md` — Decisões Fase 3 que Fase 4 reusa: D-A1 (breaker-open puro pre-dispatch — interage com schedule routing), D-B1..B4 (sensitive policy — Fase 4 herda CHECK constraint sensitive+peak), D-C1..C2 (Fireworks pin via OpenRouter — Fase 4 off-hours também usa), D-C4 (sem fallback de fallback — informa off-hours error), D-D2 (tabela `upstreams` + LISTEN/NOTIFY pattern — Fase 4 replica para `prices_changed` + `tenants_changed`), D-D4 (LISTEN/NOTIFY mecânica — `pgxlisten`)
- `.planning/phases/01-gpu-pod-image-smoke-test/01-CONTEXT.md` — Decisões D-13 (`pkg/openai` shared types — Fase 4 estende com `RateLimitErrorCode`, `QuotaExceededErrorCode` constants)

### Repo conventions (internal)

- `docs/CONVENTIONS.md` — gofmt/go vet/golangci-lint obrigatórios, slog `module=UPPER_SNAKE_CASE`, RFC3339, sentinel errors, conventional commits com scope
- `pkg/openai/types.go` — tipos OpenAI-compat compartilhados; Fase 4 adiciona `RateLimitErrorCode`, `InsufficientQuotaErrorCode` constants e estende `ErrorDetail` se necessário (compat com `Retry-After` header já é via http.Header)
- `/home/pedro/projetos/pedro/CLAUDE.md` — convenções Ifix-wide (TZ-aware logger pattern de `cobrancas-api`, kebab-case, Sentry/Better Auth conventions)

### Research bundle (internal)

- `.planning/research/SUMMARY.md` — resumo executivo
- `.planning/research/STACK.md` §Multi-tenant — `redis/go-redis v9` Lua scripting; `golang.org/x/time/rate` é alternativa in-process (rejeitada por não ser cross-process — Fase 6 multi-replica precisa Redis-backed)
- `.planning/research/STACK.md` §Telemetry — `prometheus/client_golang` para novas métricas
- `.planning/research/FEATURES.md` §Rate limiting per API key (RPM + TPM, Redis token bucket, headers OpenAI-compat) — informa D-A1 + D-D Plumbing headers
- `.planning/research/FEATURES.md` §Token counting + Cost attribution + Phantom cost — informa D-B1, D-B4
- `.planning/research/FEATURES.md` §Schedule-based routing (peak 08-22h, América/São_Paulo, per-tenant config) — informa D-C1
- `.planning/research/FEATURES.md` §Usage quotas per tenant (daily Redis + monthly Postgres, soft+hard thresholds) — informa D-A2 fail policy
- `.planning/research/PITFALLS.md` §Pitfall 8 — Per-app billing inaccuracy with streams (justifica D-B2 on-emission accounting + idempotency-by-request_id D-B1 + reconcile D-D4)
- `.planning/research/PITFALLS.md` §Pitfall 9 — Noisy-neighbor (informa por que per-tenant inflight fairness fica em Fase 5, não Fase 4)
- `.planning/research/PITFALLS.md` §Pitfall 13 — Cardinality explosion (informa label budget de novas métricas)
- `.planning/research/PITFALLS.md` §Pitfall 7 — OpenRouter rate-limited durante incidente (informa por que off-hours sem fallback-of-fallback é OK em peak mode — operador deve dimensionar tier OpenRouter)
- `.planning/research/ARCHITECTURE.md` §Phase boundaries — Fase 4 ("Quotas + billing events") referenciado explicitamente

### Existing code (internal)

- `gateway/internal/audit/` — pipeline async batched write (writer.go) — Fase 4 replica pattern para billing flush
- `gateway/internal/proxy/tokencount.go` — Fase 3 TokenCounter; Fase 4 reusa para token counting em billing accountant (extract usage de response.usage OR fallback tokenize)
- `gateway/internal/proxy/interceptor.go` — Fase 3 SSE interceptor (tool-call detection); Fase 4 estende para token counting on-emission (parse `delta.usage` ou contar via tokenizer)
- `gateway/internal/upstreams/loader.go` + `listen.go` — Fase 3 hot-reload pattern; Fase 4 replica para `prices` + `tenants` + `admin_keys` (loaders independentes ou um config bus unificado — Claude decide na execute)
- `gateway/internal/auth/cache.go` — Fase 2 Redis cache pattern (60s TTL); Fase 4 reusa para `admin_keys` cache + tenants config quando Redis disponível
- `gateway/internal/idempotency/` — Fase 2 idempotency (Stripe semantics); Fase 4 garante que replay consome quota mas não rate-limit (D-D1)
- `gateway/internal/redisx/` — go-redis v9 client; Fase 4 adiciona Lua script loading + EVALSHA helpers
- `gateway/internal/obs/metrics.go` — Fase 2/3 prom collectors; Fase 4 adiciona collectors listados em "Plumbing"
- `gateway/internal/integration_test/` — testcontainers harness; Fase 4 adiciona scenarios SC-1..SC-5
- `gateway/db/migrations/` — goose migrations sequenciais; Fase 4 adiciona 0010..0015
- `gateway/db/queries/` — sqlc; Fase 4 adiciona arquivos listados em Plumbing
- `gateway/cmd/gatewayctl/` — Fase 2/3 admin CLI; Fase 4 adiciona subcomandos D-D4

### Upstream components (HIGH confidence)

- https://redis.io/commands/eval — Redis EVAL/EVALSHA semantics (Lua atomic guarantees)
- https://redis.io/docs/latest/develop/reference/eval-intro — Lua scripting in Redis
- https://github.com/redis/go-redis — go-redis v9 (`Client.Eval`, `Client.ScriptLoad`)
- https://stripe.com/docs/rate-limits — Token bucket model semantics + headers
- https://platform.openai.com/docs/guides/rate-limits — `X-RateLimit-*` headers shape (Fase 4 espelha)
- https://github.com/jackc/pgx — pgx v5 LISTEN/NOTIFY (mesmo padrão Fase 3 D-D4)
- https://github.com/pressly/goose — goose migrations
- https://sqlc.dev/ — sqlc type-safe codegen
- https://golang.org/pkg/time/#LoadLocation — `time.Location` para America/Sao_Paulo (DST handling embutido)
- https://pkg.go.dev/golang.org/x/crypto/bcrypt — bcrypt para admin_keys (cost 10 = ~50ms — aceitável para admin path low-frequency)

### External reference (ecosystem context)

- https://openrouter.ai/docs/models — Fireworks pricing for Qwen 3.5 27B (verificar pricing seed)
- https://platform.openai.com/docs/api-reference/audio — Whisper pricing reference (PROJECT.md cita $0.006/min)
- https://platform.openai.com/docs/api-reference/embeddings — text-embedding-3-small pricing ($0.02/1M tokens)
- https://medium.com/@khalilsayed/system-design-multi-tenant-rate-limiting-service-32c63ade5ec7 — multi-tenant rate-limiting reference (citado em PITFALLS.md §9)

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets

- **`gateway/internal/audit/writer.go`** — pipeline async batched (D-B4 Fase 2): canal bufferizado 1000 + flush 500 rows ou 1s. **Fase 4 replica esse pattern para billing flush** (mesmo trade-off: hot path nunca bloqueia em Postgres). Refactor candidato: extrair `internal/dbflush/` package compartilhado.
- **`gateway/internal/proxy/tokencount.go`** — Fase 3 TokenCounter via llama.cpp `/tokenize` + Redis cache `gw:tokenize:*` (60s TTL). **Fase 4 reusa para counting em billing**: prefere `response.usage` (OpenAI/OpenRouter padrão; llama-server retorna em `usage`); cai pra `TokenCounter.Enforce()` se ausente. Mesma cache key.
- **`gateway/internal/proxy/interceptor.go`** — Fase 3 SSE interceptor (ModifyResponse) que detecta tool-call partial. **Fase 4 estende interceptor para parse `delta.usage`** dos chunks SSE (OpenRouter/Fireworks emite usage em final chunk; local llama-server pode emitir parcial). Mesmo `requestUsage` struct.
- **`gateway/internal/upstreams/loader.go` + `listen.go`** — pattern para hot-reload via Postgres LISTEN/NOTIFY. **Fase 4 replica 3x** (prices, fx_rates, tenants — ou unifica em `internal/config/loader.go` consolidando todos os configs hot-reloadable; Claude decide na execute).
- **`gateway/internal/auth/cache.go`** — Redis cache pattern com TTL 60s + invalidation hooks. **Fase 4 reusa para `admin_keys`** (verify path low-frequency mas merece cache).
- **`gateway/internal/idempotency/`** — Stripe semantics (escopo `tenant_id+key`, 24h TTL, request hash). **Fase 4 NÃO modifica**, mas wire em chain D-D1: idempotency replay consome quota mas não rate-limit (decisão chain order).
- **`gateway/internal/redisx/`** — go-redis v9 client. **Fase 4 adiciona helpers** `LoadScript(ctx, name) → sha`, `EvalSha(ctx, sha, keys, args)` e centraliza Lua scripts em `gateway/internal/quota/scripts/*.lua` embedados via `//go:embed`.
- **`gateway/internal/obs/metrics.go`** — pattern de `promauto.NewCounterVec`. **Fase 4 adiciona collectors** listados em D-D Plumbing.
- **`gateway/db/migrations/0006_create_usage_counters_skeleton.sql`** — skeleton existente (tenant_id, date, tokens_in/out, requests_count, PK composite, idx_date). **Fase 4 evolui via ALTER TABLE** (não reescreve) — D-B1 mostra colunas adicionais.
- **`gateway/db/migrations/0001_create_tenants.sql`** — tabela existente. **Fase 4 ALTER ADD COLUMN** mode/peak_window_start/peak_window_end/schedule_timezone + CHECK constraint (D-C1) + colunas de quota (`daily_quota_tokens BIGINT`, `monthly_quota_tokens BIGINT`, `daily_quota_audio_minutes INT`, `monthly_quota_audio_minutes INT`, `daily_quota_embeds INT`, `monthly_quota_embeds INT`, `rps_limit INT`, `rpm_limit INT`).
- **`gateway/cmd/gatewayctl/`** — pattern de subcomando estabelecido (Fase 2 D-A3). **Fase 4 adiciona** subcomandos D-D4.
- **`pkg/openai/types.go`** — `ErrorResponse`/`ErrorDetail` shape OpenAI-compat. **Fase 4 adiciona constants** dos novos error codes.

### Established Patterns

- **slog NDJSON** com `module=UPPER_SNAKE_CASE` — novos módulos: `module=QUOTA`, `module=BILLING`, `module=PRICES`, `module=SCHEDULE`, `module=ADMIN`, `module=TENANTS_LOADER`.
- **Sentinel errors pacote-level** — Fase 4 define em `gateway/internal/quota/errors.go`, `gateway/internal/billing/errors.go`, `gateway/internal/schedule/errors.go`, `gateway/internal/admin/errors.go`.
- **`UPPER_SNAKE_CASE` env vars** com defaults sensatos no struct config: `AI_GATEWAY_USD_BRL_RATE_DEFAULT=5.10`, `AI_GATEWAY_QUOTA_FAIL_OPEN=false`, `AI_GATEWAY_RATE_LIMIT_FAIL_OPEN=true`, `AI_GATEWAY_ADMIN_KEY_BOOTSTRAP=ifix_admin_<random>`, `GATEWAY_WRITE_TIMEOUT_CHAT_S=0`, `GATEWAY_WRITE_TIMEOUT_EMBED_S=30`, `GATEWAY_WRITE_TIMEOUT_AUDIO_S=120`.
- **goose migrations sequenciais** numeradas + `SET search_path = ai_gateway` no header.
- **Conventional commits scope** `feat(04)`, `chore(04)` etc.
- **testcontainers-go** com Postgres 16 + Redis 7 — Fase 4 estende com Lua script loading + concurrent goroutine harness (SC-5).
- **`Sentry Go SDK`** breadcrumbs — Fase 4 adiciona em quota/rate-limit rejections + billing flush failures.

### Integration Points

- **DO Postgres (`ai_gateway` schema):** novas tabelas `billing_events` (partitioned), `prices`, `fx_rates`, `admin_keys`; ALTER em `tenants` + `usage_counters`. Triggers `notify_prices_changed`, `notify_tenants_changed`. Conexões LISTEN dedicadas (fora do `pgxpool`) — replicar Fase 3 D-D4 mas considerar consolidar em uma única `listenConn` multiplexando vários `LISTEN` channels (pgx suporta).
- **Redis Ifix (namespace `gw:`):** novas chaves:
  - `gw:rate:{tenant_id}:{route_class}:{rps|rpm}` — token bucket
  - `gw:quota:{tenant_id}:{date|month}:cache` — opcional cache de quota check (Fase 5+ se overhead medido for problema)
  - `gw:admin:{key_hash}` — cache verify do admin key (60s TTL)
  - Lua script SHA cacheado em memória do gateway (não em Redis após primeiro `SCRIPT LOAD`)
- **Egress externo:** Inalterado de Fase 3 (OpenRouter + OpenAI). Off-hours em peak mode aumenta volume — operador deve confirmar tier OpenRouter cobre carga peak (Pitfall 7).
- **Fase 5 consume:** `usage_counters.requests_count` + per-tenant inflight (introduzido aqui via prom counter `gateway_inflight{tenant}`) → composite saturation signal.
- **Fase 6 consume:** Quota threshold breach pode ser trigger adicional para spin-up emergency (operador escolhe; Fase 6 decide se inclui).
- **Fase 7 consume:**
  - `billing_events` direto (dashboard cost panels per tenant SC do roadmap §Phase 7)
  - `audit_log.upstream IN ('rate_limited','quota_exceeded','quota_check_unavailable','off_hours_blocked')` para alertas WhatsApp/email
  - `gateway_quota_rejected_total{tenant,dimension,period}` Prometheus para alert "tenant 90% daily quota"
  - Admin endpoint `/admin/usage` — dashboard consome até PRD-06 substituir auth
- **Fase 8/9 consume:** Apps cliente devem honrar `Retry-After` header em 429 e tratar codes discriminados de quota (`pausar campanha vs degradar template`).

</code_context>

<specifics>
## Specific Ideas

- **Quota rollover atomic é crítico (SC-1):** "atomicamente sob carga concorrente" significa que o decremento do bucket/incremento do counter está dentro do mesmo `EVALSHA` Lua (rate-limit) ou da mesma transaction Postgres com `SELECT ... FOR UPDATE` (quota daily). 1000 goroutines → exatamente N permitidos onde N=quota. Cobertura de teste explícita em SC-5: spawning 1000 goroutines hitting `/v1/chat/completions` para tenant com `rps_limit=100` → 100 goroutines retornam 200, 900 retornam 429 com Retry-After.

- **Pitfall 8 mitigation é dual-defense (D-B1 + D-B2):** `billing_events` append-only com `INSERT ON CONFLICT (request_id, ts) DO NOTHING` garante idempotência em retries (gateway mesmo retry interno + cliente retry após 5xx). Streaming on-emission counter + flush no `defer` garante que disconnect-cedo não sub-reporta. Reconcile noturno (`gatewayctl billing reconcile`) compara `usage_counters` (cache) vs `billing_events` (autoridade) e detecta drift sistemático ou crashes que comeram flush.

- **`cost_local_phantom_brl` é estratégico, não contábil:** Tornar visível no `/admin/usage` SC-3 (cost_local + cost_local_phantom + cost_external) permite à diretoria Ifix responder "a GPU economizou X reais este mês" sem código adicional na Fase 7. A coluna existe nas linhas individuais de `billing_events` (não só agregação) — análise futura pode identificar quais tenants justificam a GPU vs quais podem migrar para OpenRouter direto.

- **Sensitive + peak triple-defense (D-C3):** Defesa em camadas porque uma única defesa pode falhar — gatewayctl pode ser bypass via SQL admin; CHECK constraint pode ser bypass via `SET session_replication_role = replica`; mas fail-fast no boot pega qualquer estado inconsistente. Coerente com filosofia Fase 2 D-B2 ("LGPD é default de schema, não opção de código").

- **Hot-reload de prices é alavanca operacional:** Quando OpenRouter ou Fireworks anunciar mudança de preço (típico anúncio com 7 dias antecedência), operador faz `gatewayctl prices set --model qwen --provider openrouter --unit input_token --usd <novo>` — gateway recarrega <1s, billing seguinte usa novo preço. Sem deploy. fx semanal idem (`gatewayctl prices set-fx --usd-brl 5.10`). Auditoria histórica preservada via `valid_from/valid_to`.

- **Admin key bootstrap honesto (D-D3):** Migration que gera key random + log warn é **defensiva**, mas deploy seguro é setar `AI_GATEWAY_ADMIN_KEY_BOOTSTRAP` no Portainer stack ANTES de subir gateway. Documentar no runbook (Fase 7/10) e checklist deploy. Padrão Vault/dotenv para apps Ifix.

- **Loader unificado vs múltiplos:** Fase 3 tem `upstreams/loader.go`. Fase 4 adiciona prices, tenants, admin_keys. Decisão deferida para execute: 4 loaders independentes (mais isolado, mais boilerplate) vs 1 `internal/config/registry.go` que multiplexa LISTEN para vários canais e dispara reload do componente correspondente (DRY mas acoplado). Plano deve avaliar trade-off durante research/planning.

- **Migration ordering matters:** `0010_create_billing_events.sql` ANTES de `0011_evolve_usage_counters.sql` porque o flush de billing pode escrever em ambas dentro da mesma transação, e usage_counters precisa das colunas novas para reconciliation queries. `0013_evolve_tenants_schedule_quota.sql` (CHECK constraint sensitive+peak) DEPOIS de seed default tenants (já com `data_class='normal'`), senão CHECK falha em rows existentes.

- **Streaming `delta.usage`:** OpenRouter/Fireworks emite `usage` em chunk final do stream (`finish_reason='stop'`). llama-server local emite `usage` em chunk final também (verificar). Se algum upstream NÃO emitir `usage`, fallback é `TokenCounter` post-hoc (re-tokenizar prompt + tentar contar response — caro mas raro). Fase 4 implementa primeiro caminho via `delta.usage`; fallback fica como TODO de Fase 5 se measured.

</specifics>

<deferred>
## Deferred Ideas

- **Per-tenant inflight quotas + fairness queue** — Fase 5 (Load Shedding) responsabilidade explícita per ROADMAP SC-4 ("uma tenant burst não starve outras tenants"). Fase 4 introduz `gateway_inflight{tenant}` métrica que Fase 5 vai consumir.
- **Fall-of-fallback off-hours para OpenAI direct chat** — rejeitado em D-C2 (mesma justificativa Fase 3 D-C4: drift Qwen→GPT é proibitivo; viola decisão "Qwen fixo" PROJECT.md).
- **Per-tenant pricing override** — v1 usa price global por (model, provider, unit). Reconsiderar se Fase 8/9 exigir tarifas diferenciadas por contrato (fora do escopo v1; nenhum app cliente Ifix é faturável para terceiros nesta milestone).
- **Auto-rotate USD/BRL fx via API externa** — Fase 4 manual via CLI. Reconsiderar em Fase 7/10 (cron job consultando BCB ou similar) se operador reportar fricção semanal.
- **Cost reconciliation contra fatura externa** (OpenRouter monthly invoice vs `SUM(billing_events.cost_external_brl)`) — Fase 7 (dashboard) ou Fase 10 (GA hardening). Fase 4 deixa esquema pronto via `cost_external_brl`.
- **Quota soft-warning thresholds** (alerta 80%, bloqueio 100%) — Fase 4 implementa só hard block. Fase 7 adiciona soft warnings via dashboard + WhatsApp (consome `gateway_quota_used_ratio{tenant,dimension,period}` métrica nova).
- **Multiple peak windows per tenant** (ex: 08-12 + 14-18) — D-C1 deferiu para Fase 9 caso Cobranças/Campanhas pedir.
- **Idempotency-Key em `/v1/embeddings` e `/v1/audio/transcriptions`** — continua deferido (Fase 2 D-C4, Fase 3 reafirmou).
- **Per-route P95/P99 latency histograms com label per-tenant** — Fase 7 (cardinality budget OBS-02). Fase 4 mantém apenas `gateway_requests_total{route,status}` sem tenant label.
- **Better Auth/SSO no admin endpoint** — Fase 10 (PRD-06). Fase 4 entrega bcrypt admin_keys table + bootstrap pattern.
- **Webhook on quota breach** (admin_url ping) — Fase 7 alerts.
- **Audit append-only export para MinIO cold storage** — Fase 2 D-B3 mencionou `gatewayctl audit export-month`; Fase 7/10 implementa para `audit_log` + `billing_events` ambos.
- **Phantom cost para STT/embed local** — Fase 4 implementa OK (D-B4). Reconsiderar pricing se BGE-M3 não tiver paridade exata com text-embedding-3-small (dimensões/qualidade diferentes; comparação é heurística).
- **Char-count fast-path para token estimation** — Fase 3 deferiu; Fase 4 mantém deferido (TokenCounter Fase 3 já é fail-open + cached).
- **Per-app circuit breaker** (trip por tenant noisy) — Pitfall 9 sugere; Fase 5 considera. Fase 4 só rate-limit + quota (não trip por erro).

### Reviewed Todos (not folded)

- **[STATE.md] Phase 1 HUMAN-UAT** (smoke.yml run, Jinja tool-call validation, VRAM ceiling, cold-start) — bloqueia execute mas não Fase 4 specifically.
- **[STATE.md] Phase 5: Tune saturation thresholds** — Fase 5.
- **[STATE.md] Phase 6: Vast.ai REST API spike** — Fase 6.
- **[STATE.md] Phase 7: Confirm Ifix WhatsApp provider** — Fase 7.
- **[STATE.md] Phase 7: Choose dashboard auth (Better Auth vs SSO)** — Fase 7/10.
- **[STATE.md] Phase 9: Obtain LGPD review sign-off** — Fase 9. CHECK constraint sensitive+peak (D-C1) é defesa técnica; LGPD aprovação ainda é necessária.
- **[STATE.md] Phase 2 SC-5 PARTIAL (post-push checklist)** — pré-requisito de Fase 2; usuário precisa fechar.

</deferred>

---

*Phase: 04-multi-tenant-quotas-billing-schedule-routing*
*Context gathered: 2026-04-20*
