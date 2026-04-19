# Phase 3: Resilience & Fallback Chain - Context

**Gathered:** 2026-04-19
**Status:** Ready for planning

<domain>
## Phase Boundary

Transforma o gateway single-upstream da Fase 2 em um gateway multi-upstream resiliente:

1. Circuit breaker (`sony/gobreaker/v2`) por upstream — 6 no total: `local-llm`, `local-stt`, `local-embed`, `openrouter-chat`, `openai-whisper`, `openai-embed` (RES-01)
2. Retry com exponential backoff (`cenkalti/backoff/v5`) em non-streaming; fail-fast em streaming após primeiros bytes (RES-02, RES-05)
3. Fallback chain automática quando breaker OPEN: `local-llm → openrouter-chat (Qwen 3.5 27B via Fireworks)`; `local-stt → openai-whisper`; `local-embed → openai-text-embedding-3-small` (RES-03)
4. Probe proativo synthetic E2E a cada 10s em todos os upstreams; estado do breaker persistido em Redis (mirror) + DB (`upstreams.last_probe_*`) (RES-04)
5. Política de streaming em failover: fail-fast com 503 + cliente re-tenta end-to-end (nunca re-inject chunks) (RES-05)
6. Tool-call no-retry: gateway NUNCA retry tool-calling; retorna 502 com envelope OpenAI quando upstream morre após emitir tool-call chunk (RES-06, SC-4)
7. Context-window normalizado em 16k tokens entre local e OpenRouter (enforcement pre-dispatch via tokenize endpoint) (RES-07, SC-5)
8. Tenants `data_class: sensitive` NUNCA proxied para OpenAI/OpenRouter em failover: retry in-memory 3× exp-backoff aguardando breaker/primary voltar; se esgotar, 503 `upstream_unavailable_for_sensitive_tenant` + `Retry-After: 30` (RES-08, SC-3)
9. Introduz tabela `upstreams` (deferida da Fase 2 D-D3) como source-of-truth de config runtime; hot-reload via Postgres LISTEN/NOTIFY
10. `UPSTREAM_*_AUTH_BEARER` env vars resolvidos via coluna `auth_bearer_env` da tabela `upstreams`; Director injeta `Authorization: Bearer <resolved>` ao proxyar para OpenRouter/OpenAI
11. `GET /v1/health/upstreams` mostra estado live (closed/half-open/open) dos 6 upstreams + last probe results; consumível por métricas Prometheus stub (SC-2)

**Fora de escopo desta phase:**
- Rate-limit, quotas e cost-attribution por tenant → Fase 4
- Load shedding baseado em saturação (inflight + P95 + VRAM) → Fase 5
- Auto-provisioning emergency pod via Vast.ai → Fase 6
- Dashboard Next.js + Prometheus histograms ricos + WhatsApp/email alerts → Fase 7
- Integração das apps cliente (ConverseAI, Chat Ifix, Telefonia, Cobranças, Campanhas, voice-api) → Fases 8-9
- HTTPS/TLS + DNS `gateway.ifix.com.br` + admin SSO → Fase 10

</domain>

<decisions>
## Implementation Decisions

### Failover — trigger & probes

- **D-A1 (Breaker-open puro):** Gateway só desvia para fallback quando o `gobreaker` do upstream alvo está OPEN. Request com breaker CLOSED tenta primary normalmente; erros retornados atualizam o breaker. Sem "retry-first" ou "hedged parallel" nesta fase — probe proativo de 10s garante que o breaker abra rápido mesmo sem tráfego real (SC-1: ≤10s para desvio).
- **D-A2 (Probe synthetic E2E):** Por upstream, dispara mini-request real a cada 10s:
  - `local-llm` / `openrouter-chat`: `POST /v1/chat/completions` com `{"model":"qwen","messages":[{"role":"user","content":"ping"}],"max_tokens":1,"temperature":0}` — valida tokenizer, template Jinja, tool-call wrapper.
  - `local-stt` / `openai-whisper`: `POST /v1/audio/transcriptions` multipart com arquivo stub curto (WAV 1s silêncio) versionado no repo (`gateway/internal/upstreams/testdata/probe.wav`, ≤50 KB).
  - `local-embed` / `openai-embed`: `POST /v1/embeddings` com `{"input":"ping","model":"bge-m3|text-embedding-3-small"}`.
  - Cadência: **primary sempre é probed** (3 req/10s = 18 req/min total locais); **externos são probed sob demanda** — só quando o breaker do primary correspondente está OPEN ou HALF_OPEN, para economizar cota OpenRouter/OpenAI. Primeira transição CLOSED→OPEN dispara o primeiro probe externo imediatamente.
  - Timeout por probe: 5s (alinhado ao `probe_budget` do health-bridge Phase 1).
- **D-A3 (Thresholds gobreaker):** Strict — `ConsecutiveFailures >= 3` → OPEN; cooldown de 30s → HALF_OPEN; 1 success em HALF_OPEN → CLOSED; 1 failure em HALF_OPEN → OPEN (reset cooldown). Bate SC-1 (≤10s desvio) com folga: com probe 10s + 3 falhas = ~30s para OPEN em cenário de pod-morto-limpo; com tráfego real ativo o threshold é alcançado imediatamente.
- **D-A4 (Definição de falha):** Incrementa `gobreaker.Counts.ConsecutiveFailures` apenas em:
  - Resposta HTTP 5xx (500, 502, 503, 504) retornada pelo upstream
  - Timeout (`context.DeadlineExceeded` ou `net.Error.Timeout()`)
  - Probe synthetic E2E que falhou (status ≠ 2xx ou timeout)
  - **NUNCA:** 4xx (cliente errado, não upstream), `context.Canceled` (cliente desistiu), connection reset durante stream após first byte.
  - 429 do OpenRouter/OpenAI **não conta** como falha (rate-limit = capacidade, não saúde) — mas incrementa métrica separada `gateway_upstream_throttled_total{upstream,status}` e loga em slog com `module=PROXY` e `throttled=true`.

### Sensitive tenant policy (LGPD)

- **D-B1 (Retry in-memory curto):** Quando request chega com `data_class: sensitive` e o breaker do primary alvo está OPEN (ou `GET /primary_route` retorna 5xx), o handler aguarda via 3× exponential backoff com total ≤ ~4s:
  - Attempt 1 após 200ms
  - Attempt 2 após +800ms
  - Attempt 3 após +3s
  - Entre attempts, gateway re-consulta estado do breaker via Redis mirror (`gw:breaker:{upstream}`) — se voltou a CLOSED, despacha. Se ainda OPEN, próximo attempt.
  - Se esgotar sem sucesso → 503 (ver D-B2).
  - Implementação: buffered channel + `time.AfterFunc`; **não** cria goroutine extra por request.
- **D-B2 (Error envelope 503):** Resposta quando sensitive-retry esgota ou fail-fast em streaming:
  ```json
  {
    "error": {
      "type": "service_unavailable",
      "code": "upstream_unavailable_for_sensitive_tenant",
      "message": "Primary inference upstream is unavailable; sensitive-data tenants cannot be routed to external providers."
    }
  }
  ```
  Status HTTP `503`, header `Retry-After: 30`. Código discriminável de 503 genérico — app cliente (Cobranças, Telefonia) pode tratar separadamente do 503 de breaker OPEN para tenant normal.
- **D-B3 (Audit sensitive-blocked):** Registro em `audit_log` com:
  - `upstream = 'blocked_sensitive'` (valor reservado novo)
  - `error_code = 'upstream_unavailable_for_sensitive_tenant'`
  - `status_code = 503`
  - Demais colunas normais (`tenant_id`, `data_class='sensitive'`, `route`, `request_id`, `latency_ms`, etc.)
  - **Sem linha em `audit_log_content`** — consistente com política Fase 2 D-B2 (sensitive nunca persiste content).
  - Permite queries para dashboard Fase 7: `SELECT COUNT(*) FROM audit_log WHERE upstream='blocked_sensitive' GROUP BY tenant_id` (+ alerta WhatsApp se >N em janela 5min).
- **D-B4 (Sensitive streaming = fail-fast):** Requests `stream: true` de tenant sensitive **ignoram o retry in-memory**. Se breaker do primary alvo está OPEN no pre-dispatch: 503 imediato com mesmo envelope da D-B2. Justificativa: RES-05 já exige fail-fast para streams mid-flight; estender para pre-flight evita segurar HTTP stream aberto esperando breaker fechar (client pode ter headers lidos e travar esperando SSE chunks).

### OpenRouter provider pin

- **D-C1 (Provider pinado: Fireworks):** Único provider aceito atrás do OpenRouter para Qwen 3.5 27B. Fireworks tem a implementação mais estável de tool-calling nessa família de modelos (referência: PITFALLS.md §6, ClickHouse/Cline usam Fireworks como primário). Minimiza drift de comportamento entre `local-llm` (llama.cpp Hermes template) e `openrouter-chat` — crítico pra SC-4 (tool-call não dupliça entre falhas).
- **D-C2 (Injeção via request body):** Director do `httputil.ReverseProxy` para rota `openrouter-chat` modifica o body antes do dispatch, adicionando:
  ```json
  {
    "provider": {
      "order": ["fireworks"],
      "allow_fallbacks": false
    }
  }
  ```
  Config via env vars:
  - `UPSTREAM_LLM_OPENROUTER_PROVIDER_ORDER=fireworks` (lista CSV — multiple = fallback interno OpenRouter; mantido ['fireworks'] na v1)
  - `UPSTREAM_LLM_OPENROUTER_ALLOW_FALLBACKS=false`
  - `UPSTREAM_LLM_OPENROUTER_AUTH_BEARER=<openrouter_api_key>` (resolvido via coluna `auth_bearer_env` da tabela `upstreams` — ver D-D2)
  - `UPSTREAM_LLM_OPENROUTER_URL=https://openrouter.ai/api/v1` (base URL padrão)
  - Config é request-body-level, **não** header-level — sobrevive a proxies e é auditável.
- **D-C3 (Teste de drift tool-call):** Integration test opt-in em `gateway/internal/proxy/integration_test/tool_call_drift_test.go`:
  - 5-10 prompts conhecidos com tools definidos (ex: `get_weather(city)`, `calculator(expr)`, `sql_query(query)`)
  - Dispara contra `local-llm` (via GATEWAY_E2E_UPSTREAM_URL) e contra `openrouter-chat` (via `OPENROUTER_API_KEY`)
  - Assert schema shape (não conteúdo): `choices[0].finish_reason == 'tool_calls'`, `choices[0].message.tool_calls[*].function.name` ∈ lista esperada, `arguments` é JSON válido parseável
  - Skip silencioso se `OPENROUTER_API_KEY` ausente no CI — runs manuais por operador com `go test -tags=e2e -run=ToolCallDrift ./gateway/internal/proxy/integration_test/...`
  - Custo estimado: ~200 tokens/teste × 8 testes × 2 paths = 3200 tokens/run ≈ $0.01 via OpenRouter
  - Rodado manualmente pré-deploy de mudanças no fallback path ou upgrade de modelo.
- **D-C4 (Sem fallback de fallback para chat):** Se breaker `openrouter-chat` também abre (Fireworks down OU OpenRouter API down):
  - Request de tenant `normal` → 503 com envelope OpenAI `{error:{type:'service_unavailable', code:'upstream_unavailable', message:'All chat upstreams are unavailable'}}`. **Não** fallback para OpenAI chat (drift Qwen→GPT-4o-mini é muito maior que Qwen-local→Qwen-OpenRouter; viola decisão "Qwen fixo" do PROJECT.md).
  - Request de tenant `sensitive` → já bloqueado em D-B1 (retry in-memory expira e 503 de D-B2).
  - STT e embed: **independentes** — `local-stt→openai-whisper` e `local-embed→openai-text-embedding-3-small` continuam operando mesmo com OpenRouter down.
  - Documentar no runbook Fase 7/10.

### Breaker state + upstreams table

- **D-D1 (Breaker state híbrido: in-process autoritativo + Redis mirror):**
  - Cada processo do gateway tem seu `*gobreaker.CircuitBreaker[T]` in-process por upstream. Decisões no hot path são lockless (atomic ops via gobreaker v2) — zero RTT por request.
  - Goroutine auxiliar `breakerMirror` (uma por gateway process) escuta o callback `OnStateChange` do gobreaker e publica em Redis:
    - Chave: `gw:breaker:{upstream_name}` — Hash com fields `{state, since_unix, trip_count, last_failure_code}`
    - Pub/Sub: `PUBLISH gw:breaker:events {upstream, state, since, reason}` pra outras réplicas escutarem
  - Outras réplicas mantém subscription Pub/Sub e atualizam seu breaker local via `Fail()`/`Succeed()` chamadas sintéticas — convergência cross-replica em <1s. Fase 6 (2 réplicas reais) consume este contrato sem refactor.
  - Dashboard Fase 7 lê `gw:breaker:*` keys no Redis pra live view.
  - Fallback se Redis down: breakers continuam operando in-process (Redis publish falha silenciosamente com métrica `gateway_breaker_mirror_failures_total`).
- **D-D2 (Tabela `upstreams` completa com hot-reload):**
  - Schema:
    ```sql
    CREATE TABLE ai_gateway.upstreams (
      id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
      name            TEXT NOT NULL UNIQUE,           -- 'local-llm', 'openrouter-chat', etc.
      role            TEXT NOT NULL,                   -- enum CHECK: 'llm' | 'stt' | 'embed'
      tier            INT NOT NULL,                    -- 0=primary, 1=fallback; UNIQUE (role, tier)
      url_env         TEXT NOT NULL,                   -- env var name: 'UPSTREAM_LLM_URL'
      auth_bearer_env TEXT,                            -- 'UPSTREAM_LLM_OPENROUTER_AUTH_BEARER' (NULL = no auth)
      enabled         BOOLEAN NOT NULL DEFAULT true,
      weight          INT,                             -- NULL in Phase 3; Phase 5 populates for load-shedding
      circuit_config  JSONB NOT NULL DEFAULT '{}'::jsonb,  -- override defaults: {failures:3, cooldown_s:30}
      last_probe_at   TIMESTAMPTZ,
      last_probe_ms   INT,
      last_probe_status TEXT,                          -- 'ok' | 'failed' | 'timeout'
      last_probe_error  TEXT,
      created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
      updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
      CHECK (role IN ('llm','stt','embed')),
      CHECK (last_probe_status IS NULL OR last_probe_status IN ('ok','failed','timeout')),
      UNIQUE (role, tier)
    );
    CREATE INDEX idx_upstreams_enabled_role_tier ON ai_gateway.upstreams (enabled, role, tier);
    ```
  - **DB é source-of-truth runtime.** URLs e secrets ficam em env vars (coluna guarda só o *nome* do env var — gateway resolve `os.Getenv(row.url_env)` no load).
  - Gateway carrega no boot + a cada NOTIFY (D-D4). In-memory cache: `map[string]UpstreamConfig` com RWMutex; reload atomic swap.
  - `last_probe_*` colunas escritas pelo probe goroutine (uma UPDATE por upstream por probe cycle = 6 updates/10s = desprezível pro DO Postgres).
- **D-D3 (Seed via migration fixa):** Migration `0010_seed_upstreams.sql` insere as 6 linhas iniciais:
  ```sql
  INSERT INTO ai_gateway.upstreams (name, role, tier, url_env, auth_bearer_env) VALUES
    ('local-llm',       'llm',   0, 'UPSTREAM_LLM_URL',          NULL),
    ('openrouter-chat', 'llm',   1, 'UPSTREAM_LLM_OPENROUTER_URL','UPSTREAM_LLM_OPENROUTER_AUTH_BEARER'),
    ('local-stt',       'stt',   0, 'UPSTREAM_STT_URL',          NULL),
    ('openai-whisper',  'stt',   1, 'UPSTREAM_STT_OPENAI_URL',   'UPSTREAM_STT_OPENAI_AUTH_BEARER'),
    ('local-embed',     'embed', 0, 'UPSTREAM_EMBED_URL',        NULL),
    ('openai-embed',    'embed', 1, 'UPSTREAM_EMBED_OPENAI_URL', 'UPSTREAM_EMBED_OPENAI_AUTH_BEARER')
  ON CONFLICT (name) DO NOTHING;
  ```
  Operator muda URL editando env var no Portainer stack e reiniciando. Tier, enabled e circuit_config editáveis via `gatewayctl upstreams update <name> --tier=N --enabled=true --circuit-failures=5` (CLI adiciona subcomando neste phase).
- **D-D4 (Hot-reload via Postgres LISTEN/NOTIFY):**
  - Migration instala trigger:
    ```sql
    CREATE OR REPLACE FUNCTION ai_gateway.notify_upstreams_changed() RETURNS trigger AS $$
    BEGIN
      PERFORM pg_notify('upstreams_changed', COALESCE(NEW.id::text, OLD.id::text));
      RETURN NEW;
    END; $$ LANGUAGE plpgsql;

    CREATE TRIGGER upstreams_change_notify
    AFTER INSERT OR UPDATE OR DELETE ON ai_gateway.upstreams
    FOR EACH ROW EXECUTE FUNCTION ai_gateway.notify_upstreams_changed();
    ```
  - Gateway mantém conexão Postgres dedicada (fora do `pgxpool`) via `pgx.Connect` + `conn.Exec(ctx, "LISTEN upstreams_changed")` + `conn.WaitForNotification(ctx)` em loop.
  - Ao receber NOTIFY: re-executa `SELECT * FROM upstreams` e atomic-swap do in-memory map. Publica métrica `gateway_upstreams_reload_total{result}`.
  - Conexão LISTEN reconecta com backoff se cair (pgx não auto-reconecta LISTEN).
  - Latency de reload: <1s end-to-end (admin UPDATE → gateway notifica → reload).
  - **NÃO** usa poll 5s — LISTEN/NOTIFY é mais barato e idiomático pra Postgres + pgx.

### Claude's Discretion

- **Enforcement do 16k cap (RES-07, SC-5):**
  - Approach: pre-dispatch via tokenize endpoint. Llama.cpp server expõe `POST /tokenize` (já disponível no pod Fase 1); gateway adiciona helper em `gateway/internal/proxy/tokencount.go` que cacheia contagem por (request_body_hash) em Redis com TTL 60s.
  - Se `input_tokens > 16384` (para chat) ou `input_tokens > 8192` (para embed — BGE-M3 native cap): 400 com envelope OpenAI:
    ```json
    {"error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"Request exceeds 16384 token cap (actual: N). This cap is enforced regardless of whether the request is served by the primary or fallback upstream."}}
    ```
  - Cache hit em Redis + idempotency key → reusa contagem sem re-tokenizar.
  - Fast-path (Fase 5+ se probar overhead inaceitável): estimativa char→token (1 token ≈ 3.8 chars para Qwen) rejeita claramente-acima-do-cap sem chamar `/tokenize`; só chama `/tokenize` se estiver entre 80% e 120% do cap. Não implementado em Fase 3 — começa com tokenize autoritativo.
- **Retry non-stream details (RES-02):**
  - `cenkalti/backoff/v5` config: `MaxElapsedTime=1s`, `InitialInterval=100ms`, `MaxInterval=500ms`, `Multiplier=2.0`, `RandomizationFactor=0.3`.
  - Aplicado **apenas** em requests `stream: false`. Stream é fail-fast per RES-02/RES-05.
  - Aplicado **apenas** em erros retryable: 502, 503, 504, timeout. **Não** retry em 4xx, 5xx com `code: 'idempotency_key_reused_with_different_body'`, ou `context.Canceled`.
  - Retry do mesmo upstream; **não** muda de upstream durante retries (fallback para upstream diferente só via breaker OPEN — decisão D-A1).
  - Respeita header `Retry-After` do upstream se presente (`backoff.RetryAfterError`).
- **Tool-call detection em stream (RES-06, SC-4):**
  - Interceptor no `httputil.ReverseProxy.ModifyResponse` buffera primeiro chunk SSE parseando JSON de `choices[0].delta.tool_calls` ou `choices[0].finish_reason`.
  - Se detectar `tool_calls != nil` em qualquer delta antes do upstream desconectar: flag `tool_call_emitted=true` no request context.
  - Se upstream desconectar (`io.ErrUnexpectedEOF`, `net.ErrClosed`) com `tool_call_emitted=true`: envia evento SSE final `event: error\ndata: {"error":{"type":"upstream_disconnected","code":"tool_call_partial_stream","message":"Primary upstream disconnected after tool call emission; agent layer must retry with separate idempotency key."}}` + fecha stream. Gateway **não** faz failover.
  - Para não-streaming (`stream: false`): se resposta completa contém `tool_calls` e request falha em retry, retorna 502 com mesmo envelope.
  - Métrica `gateway_tool_call_partial_total{route,upstream}` incrementa.
- **UPSTREAM_*_AUTH_BEARER injection (todo Fase 3 resolvido):**
  - Director do ReverseProxy por upstream resolve `auth_bearer_env` da tabela `upstreams` no boot/reload.
  - Se `auth_bearer_env IS NOT NULL`: `req.Header.Set("Authorization", "Bearer " + os.Getenv(auth_bearer_env))` no Director antes do dispatch.
  - Valor vazio/missing → warn log `module=PROXY upstream=X auth_bearer_env=Y status=missing` + não seta header (upstream responde 401, breaker conta como falha).
  - Header do cliente `Authorization` é stripado pelo Director (política Fase 2) antes de injetar o upstream bearer.
- **Probe goroutine arquitetura:**
  - Um goroutine `probeLoop` no boot do gateway; `time.NewTicker(10 * time.Second)`.
  - Cada tick: dispara probe para cada upstream em paralelo via `errgroup.Group` com timeout 5s.
  - Resultado atualiza: (a) `gobreaker.Counts` via `cb.Succeed()`/`cb.Fail()` síncrono; (b) tabela `upstreams.last_probe_*` via UPDATE assincrono (buffered channel + batch flush 1s).
  - Métrica `gateway_probe_duration_ms{upstream}` (histogram) e `gateway_probe_failure_total{upstream,reason}` (counter).
- **`GET /v1/health/upstreams` endpoint (SC-2):**
  - Refactor do `gateway/internal/upstreams/health.go` existente (que hoje só agrega pod health-bridge :9100).
  - Novo payload:
    ```json
    {
      "status": "ok|degraded|failed",
      "upstreams": {
        "local-llm":       {"state":"closed","tier":0,"role":"llm","last_probe_ms":120,"last_probe_at":"2026-04-19T12:34:56Z"},
        "openrouter-chat": {"state":"closed","tier":1,"role":"llm","last_probe_ms":340,"last_probe_at":"2026-04-19T12:34:50Z"},
        ...
      }
    }
    ```
  - `status` derivado: `ok` se todos tier-0 CLOSED; `degraded` se algum tier-0 OPEN mas tier-1 CLOSED; `failed` se algum role tem 0 CLOSED.
  - Cache in-memory 2s (consultado por dashboard Fase 7 refresh).
- **Plumbing:**
  - Novo package `gateway/internal/breaker/` — wrappers sobre gobreaker v2 + state publisher + Pub/Sub subscriber.
  - Novo package `gateway/internal/upstreams/` expandido — `loader.go`, `probe.go`, `listen.go`, `health.go` (refactored).
  - Package `gateway/internal/proxy/` refactored: Director seleciona upstream via upstreams loader em cada request (tier 0 se CLOSED, senão tier 1 se CLOSED, senão breaker-aware fallback para 503 ou sensitive-retry).
  - Novo subcomando `gatewayctl upstreams {list,update,disable,enable}` para operador editar tabela sem SQL.
  - Testes integration com `testcontainers-go`: Postgres 16 + Redis 7 + mock HTTP server que simula 500/timeout/OK. Cobertura mínima: breaker state machine (CLOSED→OPEN→HALF_OPEN→CLOSED), fallback routing por role/tier, sensitive retry loop, hot-reload via LISTEN.

### Folded Todos

- **[STATE.md] Phase 3: Confirm OpenRouter upstream provider for Qwen 3.5 27B** → Resolvido em D-C1 (Fireworks pinado via `provider.order`).
- **[STATE.md] Phase 3: Add `UPSTREAM_LLM_AUTH_BEARER` (+ STT/EMBED variants) env to inject Authorization header in proxy Director** → Resolvido em D-D2/D-D3 via coluna `auth_bearer_env` da tabela `upstreams` e plumbing em Director (ver "Claude's Discretion / UPSTREAM_*_AUTH_BEARER injection").
- **[STATE.md] Phase 3: Revisit per-route WriteTimeout (chat=0 for SSE, embeddings=30s, audio=120s) to restore slow-client-DoS defense on non-streaming routes** → Fica registrado aqui. Plan deve incluir task para configurar per-route `WriteTimeout` no ponto onde o multi-upstream dispatcher injeta context deadline. Não é decisão de gray area — é refactor mecânico quando tocarmos em `http.Server` config.

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Project docs (internal)

- `.planning/PROJECT.md` — Vision, Core Value ("failover invisível"), constraints gerais (Go + Docker Compose + Postgres DO + Redis Ifix), Key Decisions table
- `.planning/REQUIREMENTS.md` §Resiliência — requirements RES-01..RES-08 (fonte de escopo desta phase)
- `.planning/REQUIREMENTS.md` §Out of Scope — regras explícitas (Qwen fixo, não TTS GPU, não Kubernetes, não aprovação manual)
- `.planning/ROADMAP.md` §Phase 3 — Goal, Depends-on (Phase 2), Success Criteria SC-1..SC-5
- `.planning/STATE.md` — Decisões travadas + open todos que tocam Fase 3 (Fireworks pin e UPSTREAM_*_AUTH_BEARER agora foldados aqui)
- `.planning/phases/02-gateway-core-multi-tenant-auth/02-CONTEXT.md` — Decisões Fase 2 que Fase 3 estende: D-A4 (`data_class` column em api_keys), D-B1 (audit_log tem `upstream` column), D-B2 (sensitive não persiste content), D-D3 (tabela `upstreams` deferida — agora Fase 3 cria), D-D5 (schema ai_gateway existente)
- `.planning/phases/01-gpu-pod-image-smoke-test/01-CONTEXT.md` — Decisões D-10..D-12 (health-bridge :9100 contract — Phase 3 consome) e D-13 (pkg/openai tipos compartilhados)

### Repo conventions (internal)

- `docs/CONVENTIONS.md` — gofmt/go vet/golangci-lint obrigatórios, slog `module=UPPER_SNAKE_CASE`, RFC3339, sentinel errors, conventional commits com scope
- `pkg/openai/types.go` — tipos OpenAI-compat compartilhados (Chat, Embedding, Transcription, Error, ToolCall) — Fase 3 importa e estende para error envelopes de failover (`upstream_unavailable_for_sensitive_tenant`, `tool_call_partial_stream`, `context_length_exceeded`, `upstream_unavailable`)
- `/home/pedro/projetos/pedro/CLAUDE.md` — convenções Ifix-wide (Sentry pattern, Better Auth pattern, kebab-case)

### Research bundle (internal)

- `.planning/research/SUMMARY.md` — resumo executivo da stack
- `.planning/research/STACK.md` §Resilience — `sony/gobreaker/v2` + `cenkalti/backoff/v5` + `x/time/rate`; por que NÃO `afex/hystrix-go` (deprecated)
- `.planning/research/STACK.md` §Telemetry — `log/slog` + `prometheus/client_golang` (contador `gateway_upstream_throttled_total`, histogram `gateway_probe_duration_ms`)
- `.planning/research/PITFALLS.md` §Pitfall 4 — Streaming failover semantics (D-A1 + D-B4 + streaming fail-fast)
- `.planning/research/PITFALLS.md` §Pitfall 5 — LGPD em failover (justifica D-B1/D-B2/D-B3 e data_class-aware routing)
- `.planning/research/PITFALLS.md` §Pitfall 6 — Tokenizer/context-window/tool-calling drift Qwen local vs OpenRouter (justifica D-C1 Fireworks pin + D-C3 drift test + enforcement 16k)
- `.planning/research/PITFALLS.md` §Pitfall 9 — Goroutine leaks em streams (guia cleanup do ModifyResponse tool-call interceptor)
- `.planning/research/ARCHITECTURE.md` §Gateway components — subsistemas router/auth/dispatcher/metrics e circuit-breaker placement

### Existing code (internal)

- `gateway/internal/upstreams/health.go` — implementação atual (5s cache + probe :9100 aggregator); Fase 3 refactora para multi-upstream E2E probes
- `gateway/internal/proxy/director.go` + `chat.go` + `embeddings.go` + `audio.go` — single-upstream reverse proxy; Fase 3 adiciona multi-upstream dispatcher com breaker check pre-dispatch
- `gateway/internal/audit/` — audit pipeline Fase 2 (schema já tem `upstream` column); Fase 3 adiciona valor reservado `blocked_sensitive`
- `gateway/internal/redisx/` — cliente Redis existente; Fase 3 usa para breaker mirror (Hash + Pub/Sub)
- `gateway/internal/config/` — config struct + env loading; Fase 3 expande com env vars UPSTREAM_*_OPENROUTER_*, UPSTREAM_*_OPENAI_*, UPSTREAM_*_AUTH_BEARER
- `gateway/db/migrations/` — goose migrations ordenadas; Fase 3 adiciona 0010_create_upstreams.sql, 0011_seed_upstreams.sql, 0012_audit_log_upstream_enum_expand.sql (se enum), 0013_upstreams_notify_trigger.sql
- `gateway/db/queries/` — sqlc queries; Fase 3 adiciona upstreams.sql (SELECT, UPDATE last_probe_*, INSERT via migration)
- `gateway/internal/integration_test/` — testcontainers harness Fase 2; Fase 3 adiciona cenários de breaker state machine, sensitive retry loop, hot-reload via LISTEN
- `pod/health-bridge/` — agregador :9100 do pod (Fase 1); Fase 3 **mantém como aggregator local** mas gateway probes cada upstream local (LLM, STT, embed) diretamente em paralelo — health-bridge vira dashboard internal do pod, não autoridade do gateway

### Upstream components (HIGH confidence)

- https://github.com/sony/gobreaker — gobreaker v2 (circuit breaker, generics)
- https://github.com/cenkalti/backoff — backoff v5 (exponential backoff + context)
- https://pkg.go.dev/net/http/httputil#ReverseProxy — ReverseProxy (Director + ModifyResponse + ErrorHandler)
- https://github.com/jackc/pgx — pgx v5 (LISTEN/NOTIFY via `Conn.WaitForNotification`)
- https://github.com/redis/go-redis — go-redis v9 (Pub/Sub para breaker mirror)
- https://github.com/pressly/goose — goose (migrations; trigger + function em 0013)
- https://pkg.go.dev/log/slog — slog (structured logging)
- https://pkg.go.dev/golang.org/x/sync/errgroup — probe paralelo com timeout compartilhado

### External reference (ecosystem context)

- https://openrouter.ai/docs/provider-routing — `provider.order` + `allow_fallbacks` (D-C2 mecânica de pin)
- https://openrouter.ai/docs/api-reference/chat-completion — OpenAI-compat request/response shape
- https://fireworks.ai/models/fireworks/qwen3-27b-instruct — capabilities + tool-calling support declaration
- https://platform.openai.com/docs/api-reference/audio/createTranscription — Whisper API contract (fallback STT)
- https://platform.openai.com/docs/api-reference/embeddings — text-embedding-3-small contract (fallback embed)
- https://github.com/danny-avila/LibreChat/discussions/9686 — max_tokens handling across OpenRouter providers (contexto pra SC-5 context window normalization)

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets

- **`gateway/internal/upstreams/health.go`** (97 linhas): Aggregator do health-bridge :9100 com 5s cache + 2s probe budget. Fase 3 mantém o cache/budget patterns mas expande para 6 upstreams E2E — refactor mais do que reescrita.
- **`gateway/internal/httpx/RequestIDFrom`**: context getter já usado no health handler; Fase 3 usa mesmo patrón para audit correlation em `blocked_sensitive` rows.
- **`gateway/internal/proxy/director.go` + `errors.go`**: Director customization e envelope OpenAI de erro já no padrão Fase 2; Fase 3 adiciona novos códigos de erro no mesmo shape (`upstream_unavailable_for_sensitive_tenant`, etc.).
- **`gateway/internal/redisx/`**: cliente `go-redis/v9` pronto. Fase 3 adiciona helpers `PublishBreakerEvent` + `SubscribeBreakerEvents` + `WriteBreakerState` (HSET). Sem nova dependência.
- **`gateway/internal/audit/`**: pipeline de escrita assíncrona via canal + batch flush existente; Fase 3 só adiciona novos valores enum em `upstream` column (`blocked_sensitive`, `openrouter-chat`, `openai-whisper`, `openai-embed`).
- **`gateway/internal/config/`**: struct de config + env loader Fase 2. Fase 3 adiciona campos: `UpstreamOpenrouterChatURL`, `UpstreamOpenrouterAllowFallbacks`, `UpstreamOpenrouterProviderOrder` (CSV parse), `UpstreamOpenrouterAuthBearer`, `UpstreamOpenaiWhisperURL`, `UpstreamOpenaiWhisperAuthBearer`, `UpstreamOpenaiEmbedURL`, `UpstreamOpenaiEmbedAuthBearer`, `ProbeIntervalSeconds` (default 10), `ProbeBudgetSeconds` (default 5), `BreakerConsecutiveFailures` (default 3), `BreakerCooldownSeconds` (default 30).
- **`pkg/openai/types.go`**: Error envelope e tipos compartilhados. Fase 3 define error codes como constantes no mesmo package.

### Established Patterns

- **Monorepo Go `github.com/ifixtelecom/gpu-ifix`** — Fase 3 vive em `gateway/internal/{breaker,upstreams,probe}` paralelo a `proxy/` existente.
- **slog NDJSON** com atributo `module=UPPER_SNAKE_CASE` — novos módulos: `module=BREAKER`, `module=PROBE`, `module=UPSTREAMS`.
- **Sentinel errors pacote-level** — Fase 3 define em `gateway/internal/breaker/errors.go`: `ErrBreakerOpen`, `ErrProbeTimeout`, `ErrSensitiveRetryExhausted`, `ErrToolCallPartialStream`, `ErrContextLengthExceeded`, `ErrUpstreamUnavailable`.
- **testcontainers-go** para integration — Fase 3 adiciona cenário com mock upstream server (HTTP) que simula degradação + testes de breaker state machine + hot-reload via LISTEN.
- **goose migrations** numeradas sequencialmente com `search_path = ai_gateway` no header. Fase 3 adiciona 4+ migrations (ver "Existing code" acima).
- **sqlc** geração type-safe — Fase 3 adiciona queries em `gateway/db/queries/upstreams.sql`.
- **httputil.ReverseProxy com FlushInterval: -1** para SSE — Fase 3 mantém e adiciona `ModifyResponse` hook para tool-call detection.
- **Sentry Go SDK ativo** — Fase 3 adiciona breadcrumbs em `OnStateChange` do breaker (transições de state).
- **Prometheus scaffold em `/metrics`** — Fase 3 expande: `gateway_breaker_state{upstream,state}` (gauge 0/1), `gateway_breaker_trips_total{upstream}` (counter), `gateway_upstream_throttled_total{upstream,status}` (counter), `gateway_probe_duration_ms{upstream}` (histogram), `gateway_probe_failure_total{upstream,reason}` (counter), `gateway_sensitive_retry_total{outcome}` (counter), `gateway_tool_call_partial_total{route,upstream}` (counter), `gateway_upstreams_reload_total{result}` (counter), `gateway_breaker_mirror_failures_total` (counter).

### Integration Points

- **Pod Fase 1 `ghcr.io/ifixtelecom/ifix-ai-pod`**: Fase 3 probes LLM :8000 + STT :8001 + embed :8002 diretamente via E2E (não mais só `:9100` aggregated). Health-bridge :9100 continua como debug view do pod, não autoridade do gateway.
- **DO Postgres (schema `ai_gateway`)**: Nova tabela `upstreams` + trigger NOTIFY + alteração do enum de `audit_log.upstream`. Conexão LISTEN dedicada FORA do `pgxpool` (pgx recomenda conexão dedicada para LISTEN).
- **Redis Ifix (namespace `gw:`)**: Novas chaves:
  - `gw:breaker:{upstream_name}` — Hash com state
  - `gw:breaker:events` — Pub/Sub channel
  - (Existentes mantidos: `gw:apikey:*`, `gw:idem:*`)
- **OpenRouter API** (`https://openrouter.ai/api/v1`): novo upstream externo. Traefik/firewall da VPS precisa permitir egress para `openrouter.ai:443`.
- **OpenAI API** (`https://api.openai.com/v1`): novos upstreams Whisper e embed. Egress já permitido (ConverseAI v4 usa OpenAI hoje).
- **Fase 4 consumes**: inflight counters por upstream (introduzidos aqui em `/v1/health/upstreams` response) alimentam rate-limit decisions.
- **Fase 5 consumes**: `weight` column (null aqui) + composite saturation via probe latency histograms.
- **Fase 6 consumes**: breaker state em Redis (Pub/Sub) para leader-elected spin-up trigger.
- **Fase 7 consumes**: `gateway_breaker_state` gauges + `audit_log.upstream='blocked_sensitive'` queries para dashboard e alertas.

</code_context>

<specifics>
## Specific Ideas

- **Breaker-open puro (D-A1) + probe synthetic agressivo (D-A2) + thresholds strict (D-A3)** é um trio coerente: decisão simples no hot path (CLOSED=go, OPEN=skip) compensada por sinal proativo denso (probe de 10s com 3-falhas = detecção ~30s sem tráfego real; muito mais rápido se houver tráfego). Isso bate SC-1 (≤10s desvio) sem introduzir complexidade de retry-first ou hedged.
- **Sensitive retry in-memory de ~4s (D-B1) casa com operacional da Vast.ai:** blips típicos de restart de pod emergem em 30-60s; os 4s pegam 95% das oscilações micro (ex: pod recebendo sinal TERM e chamando graceful shutdown) sem segurar request HTTP por tempo absurdo. Se pod está morto por mais de 4s, app cliente recebe 503 honesto e pode retry com backoff próprio.
- **Breaker híbrido in-process+mirror (D-D1)** foi decidido olhando pra Fase 6: quando houver 2 réplicas do gateway + emergency pod, breaker state **tem que** ser cross-replica pra evitar duas réplicas disparando spin-up simultâneo. Redis mirror é a ponte que permite Fase 6 adicionar distributed lock + single-reconciler sem refactor de breakers.
- **Postgres LISTEN/NOTIFY (D-D4)** é uma escolha deliberada — não é poll. Fase 5 vai adicionar thresholds de saturação em `upstreams.circuit_config` JSONB que precisam ser editáveis em runtime sem restart (SC-3 da Fase 5 explícito). Fase 3 estabelece o contrato; Fase 5 só adiciona campos no JSONB e usa o canal existente.
- **Fireworks pin (D-C1) é reavaliável em Fase 9/10.** Se LGPD review ou operational data revelar problemas com Fireworks (latency, cost, tool-call regressions), config via env var permite troca para Together sem redeploy (update `UPSTREAM_LLM_OPENROUTER_PROVIDER_ORDER=together` → restart → pronto).
- **Tool-call detection no ModifyResponse (D-C1 plumbing)** é proxy-agnostic: funciona pra primary e pra fallback, sem branching por upstream. Interceptor parseia SSE deltas e, em qualquer stream que tenha emitido `tool_calls`, aplica a regra "NO FAILOVER" automaticamente. Isso garante SC-4 (tool call nunca duplica) mesmo se futuramente adicionarmos mais upstreams.
- **Test harness com mock upstream HTTP** (testcontainers) é importante: unit tests de breaker não são suficientes — queremos integration tests que prove que, quando mock retorna 500 por 3 requests, o Director de fato para de enviar e cai pro tier-1 mock. Cobertura de state machine real.
- **`gatewayctl upstreams` CLI** é primeira superfície admin fora de SQL. Fase 2 já estabeleceu `gatewayctl tenant create / key create`; Fase 3 adiciona `upstreams list / update / disable / enable`. Fase 7 eventualmente promove para endpoints REST consumidos pelo dashboard Next.js.

</specifics>

<deferred>
## Deferred Ideas

- **Fallback-of-fallback para chat (OpenAI GPT-4o-mini tier-2)** — rejeitado em D-C4. Drift Qwen→GPT é muito maior que Qwen-local→Qwen-OpenRouter; viola decisão "Qwen fixo" do PROJECT.md. Reconsiderar em futura milestone se SC-1 provar ser inatingível só com local+OpenRouter.
- **Hedged parallel requests** — rejeitado em D-A1 Fase 3. Possível revisita em Fase 5 (Load Shedding) se p99 local insatisfatório.
- **Retry-first-then-fallback** — rejeitado em D-A1 Fase 3. Breaker-open puro + probe denso já bate SC-1.
- **CI obrigatório de tool-call drift em toda PR** — rejeitado em D-C3. Custo de cota OpenRouter + latency de CI. Reconsiderar em Fase 10 (GA) com cota dedicada.
- **Cost attribution por provider (OpenRouter Fireworks vs OpenAI direct)** — Fase 4 (billing).
- **Rate-limit 429 no breaker** — rejeitado em D-A4. 429 = capacidade, não saúde. Métrica separada `gateway_upstream_throttled_total` fica; quando Fase 4 adicionar outbound rate-limit, consome essa métrica.
- **Idempotency-Key scope em `/v1/embeddings` e `/v1/audio/transcriptions`** — continua deferido (Fase 2 D-C4). Multipart hashing de body grande fica pra Fase 4+ se algum app cliente pedir.
- **Per-tenant circuit breaker overrides** (ex: Cobranças com threshold 5 falhas, Telefonia com 3) — deferido. v1 usa thresholds globais. Reconsiderar em Fase 9 se app sensitive específico exigir tuning.
- **Dashboard UI para visualizar breaker state em tempo real** — Fase 7. Redis mirror já expõe state; Fase 7 só escreve UI.
- **WhatsApp/email alerts em breaker trip** — Fase 7. Audit log + métricas prontas nesta phase para Fase 7 consumir.
- **Upstream auth via OAuth token rotation (não env var)** — deferido; OpenRouter e OpenAI usam API keys estáticas por ora.
- **HEAD-based liveness** (D-A2 alternativa descartada) — rejeitado por não detectar "processo vivo mas lento/quebrado". Synthetic E2E pega degradação real.
- **Char-count fast-path para context window** — deferido (documentado em "Claude's Discretion"). Começa com tokenize autoritativo; adiciona fast-path só se overhead medido for proibitivo.
- **Per-route WriteTimeout fine-tune** (chat=0 / embed=30s / audio=120s) — registrado em "Folded Todos"; refactor mecânico durante Fase 3 execution, não gray area.

### Reviewed Todos (not folded)

- **[STATE.md] Phase 1 HUMAN-UAT** (smoke.yml run, Jinja tool-call validation, VRAM ceiling, cold-start) — continuam bloqueados; não tocam Fase 3.
- **[STATE.md] Phase 5: Tune saturation thresholds (inflight N, P95 ms, VRAM GB) from Phase 1 baseline** — pertence a Fase 5. Fase 3 deixa `upstreams.circuit_config` JSONB pronto.
- **[STATE.md] Phase 6: Timeboxed (3h) Vast.ai REST API spike** — Fase 6.
- **[STATE.md] Phase 7: Confirm Ifix WhatsApp provider** — Fase 7.
- **[STATE.md] Phase 7: Choose dashboard auth (Better Auth vs SSO)** — Fase 7/10.
- **[STATE.md] Phase 9: Obtain LGPD review sign-off** — Fase 9.
- **[STATE.md] Phase 2: SC-5 PARTIAL (post-push checklist)** — pré-requisito operacional de Fase 2; usuário precisa fazer antes de começar Fase 3 execute.

</deferred>

---

*Phase: 03-resilience-fallback-chain*
*Context gathered: 2026-04-19*
