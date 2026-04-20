# Phase 4: Multi-tenant Quotas, Billing & Schedule Routing - Discussion Log

> **Audit trail only.** Do not use as input to planning, research, or execution agents.
> Decisões estão capturadas em CONTEXT.md — este log preserva as alternativas consideradas.

**Date:** 2026-04-20
**Phase:** 04-multi-tenant-quotas-billing-schedule-routing
**Areas discussed:** Rate-limit + quota semantics, Billing schema & cost model, Schedule routing peak/24-7, Middleware chain & admin surface

---

## Rate-limit + quota semantics

### Q1: Algoritmo + escopo de chave para rate-limit (TEN-03, SC-5)

| Option | Description | Selected |
|--------|-------------|----------|
| Token bucket Lua | Lua atomic com refill por timestamp; cobre RPS+RPM no mesmo script; chave gw:rate:{tenant_id}:{route_class}:{window}; padrão Stripe/Cloudflare; SC-5 trivialmente atingido | ✓ |
| Sliding window log (ZSET) | Mais preciso (sem burst-at-boundary), mais Redis CPU; trivial em 6 tenants | |
| Fixed window INCR+EXPIRE | Mais simples; sujeito a burst no boundary; aceitável se NL>2x RPS configurado | |

**User's choice:** Token bucket Lua (Recommended)
**Notes:** Padrão da indústria + atende SC-5 sem complicar.

### Q2: Política em caso de Redis down (rate-limit + quota)

| Option | Description | Selected |
|--------|-------------|----------|
| RL fail-open + Quota fail-closed | RL deixa passar (preserva failover invisível); Quota retorna 503 (Pitfall 8 — sem visibilidade de custo, parar > torrar OpenRouter); audit upstream='quota_check_unavailable' | ✓ |
| Ambos fail-open | Prioriza disponibilidade total; risco noisy-neighbor + runaway cost | |
| Ambos fail-closed | Mais seguro mas viola core value 'failover invisível' | |

**User's choice:** RL fail-open + Quota fail-closed (Recommended)
**Notes:** Híbrido alinhado com core value e Pitfall 8.

### Q3: Timezone do rollover diário de quota

| Option | Description | Selected |
|--------|-------------|----------|
| America/Sao_Paulo | Alinha CLAUDE.md TZ-aware logger; cobre peak window 08-22h local | ✓ |
| UTC | Sem ambiguidade DST; mas 'diário' fica 21:00 BRT confunde reports operadores | |
| Per-tenant configurável | Coluna tenants.quota_timezone; over-engineering para 6 apps Ifix BR-only | |

**User's choice:** America/Sao_Paulo (Recommended)

### Q4: Discriminação de error code no envelope 429/quota

| Option | Description | Selected |
|--------|-------------|----------|
| Discriminado por período + dimensão | rate_limit_rps, rate_limit_rpm, quota_exceeded_daily_tokens, quota_exceeded_daily_audio_minutes, quota_exceeded_monthly_*; apps decidem programaticamente | ✓ |
| Discriminado só por período | rate_limit_exceeded, quota_exceeded_daily, quota_exceeded_monthly; perde discriminação de dimensão | |
| Code único rate_limit_exceeded | OpenAI compat literal; perde discriminação operacional | |

**User's choice:** Discriminado por período + dimensão (Recommended)
**Notes:** Permite Cobranças/Campanhas tomar ação automatizada.

---

## Billing schema & cost model

### Q1: Schema do billing — nova tabela billing_events vs evoluir usage_counters

| Option | Description | Selected |
|--------|-------------|----------|
| Nova billing_events append-only + usage_counters cache | billing_events {request_id, ts, tenant_id, ..., cost_local_brl, cost_local_phantom_brl, cost_external_brl, source}; PARTITION BY RANGE ts mensal; usage_counters vira sum-cache diário; idempotência ON CONFLICT | ✓ |
| Evoluir usage_counters adicionando cost_brl + audio_seconds + embeds_count | 1 tabela; perde rastreabilidade per-request; conflito com Pitfall 8 idempotência | |
| Evento por request em audit_log só | audit_log retém 90d hot; billing precisa anos de histórico | |

**User's choice:** Nova billing_events append-only + usage_counters cache (Recommended)
**Notes:** Resolve Pitfall 8 + permite reconcile + retention separada.

### Q2: Quando contar tokens em streaming (Pitfall 8)

| Option | Description | Selected |
|--------|-------------|----------|
| On-emission por chunk + final flush no [DONE] ou abnormal close | atomic counter in-process; defer flushBilling em close; cliente disconnect token 50 → DB grava 50 source='partial' | ✓ |
| On-completion only ([DONE] event) | Mais simples; cliente cair no meio → custo zero (viola Pitfall 8) | |
| Per-chunk INSERT | Disco/CPU explode em produção | |

**User's choice:** On-emission por chunk + final flush (Recommended)

### Q3: Onde mora a price table

| Option | Description | Selected |
|--------|-------------|----------|
| Tabela ai_gateway.prices em DB + hot-reload via LISTEN/NOTIFY | Schema {model, provider, unit, unit_cost_usd, valid_from, valid_to}; fx via gatewayctl prices set-fx; auditoria histórica; sem deploy para ajustar preço | ✓ |
| Hardcoded const Go com migrate | Simples; exige deploy gateway pra ajustar BRL fx semanal | |
| Env vars (PRICE_LOCAL_INPUT_TOKEN_USD, etc.) | ~12 vars + fx; sem auditabilidade histórica | |

**User's choice:** Tabela ai_gateway.prices em DB + hot-reload (Recommended)

### Q4: Cálculo do cost_local (GPU primary) — phantom cost

| Option | Description | Selected |
|--------|-------------|----------|
| Dual: cost_local_brl=0 (real) + cost_local_phantom_brl | cost_local_brl real é 0 (GPU fixed-cost); phantom = tokens × preço-OpenRouter-equivalente; reports SC-3 expõem ambos; "GPU economizou X reais este mês" | ✓ |
| Amortized: GPU mensal $/total_tokens | Volátil; mês baixo tráfego = unit cost alto; recalc retro complica idempotência | |
| cost_local_brl = 0 sempre, sem phantom | Simples; perde informação estratégica para exec/finance | |

**User's choice:** Dual: cost_local_brl=0 + cost_local_phantom_brl (Recommended)

---

## Schedule routing peak/24-7

### Q1: Onde fica a config de schedule (mode + window)

| Option | Description | Selected |
|--------|-------------|----------|
| Colunas em tenants + tabela schedules só se per-window | tenants.mode ENUM('24/7','peak'), peak_window_start TIME, peak_window_end TIME, schedule_timezone TEXT; hot-reload via LISTEN/NOTIFY config_changed | ✓ |
| Tabela ai_gateway.tenant_schedules separada | Permite múltiplas janelas; over-engineering para v1; reconsiderar se Cobranças pedir 'pular almoço' | |
| Config global + flag per-tenant | Janela global via env; tenant só boolean; rígido demais | |

**User's choice:** Colunas em tenants (Recommended)

### Q2: Em peak mode off-hours, qual upstream serve

| Option | Description | Selected |
|--------|-------------|----------|
| OpenRouter direto (mesma rota da Fase 3 fallback) | Skipa tier-0 mesmo se CLOSED; reusa Director openrouter-chat + Fireworks pin; sem drift de modelo; 503 off_hours_upstream_unavailable se OR também down | ✓ |
| Tenta local primeiro, se OFF cai pra OpenRouter | Aproveita GPU se ligada; viola justificativa 'desligar GPU à noite' | |
| Local sempre; off-hours só para alertas | Viola SC-4 explicitly | |

**User's choice:** OpenRouter direto (Recommended)

### Q3: Interação sensitive + peak (LGPD)

| Option | Description | Selected |
|--------|-------------|----------|
| Sensitive + peak é inválido na criação | gatewayctl rejeita + CHECK constraint + boot-time fail-fast detecta inconsistência via SQL bruto; defesa em profundidade | ✓ |
| Sensitive + peak: peak ignorado, sempre local | Silencioso; risco de surpresa para operador | |
| Sensitive + peak: 503 em off-hours | Cobranças/Telefonia OFF à noite, inaceitável | |

**User's choice:** Sensitive + peak é inválido na criação (Recommended)
**Notes:** Coerente com Fase 3 D-B1 e LGPD policy.

### Q4: Onde checa schedule (cache strategy)

| Option | Description | Selected |
|--------|-------------|----------|
| Pre-dispatch in-memory (sem cache RTT) | Loader pattern Fase 3 D-D4; map[tenant_id]TenantConfig com RWMutex; check time.Now().In(tz) vs window; zero RTT; decisão em request context | ✓ |
| Redis cache com TTL 60s | Custa 1 RTT/request; desnecessário (config muda raramente) | |
| DB lookup por request | Inviavel no hot path | |

**User's choice:** Pre-dispatch in-memory (Recommended)

---

## Middleware chain & admin surface

### Q1: Ordem do middleware chain no proxy

| Option | Description | Selected |
|--------|-------------|----------|
| auth → rate-limit → quota → schedule → tokencount → dispatcher → billing-flush | Ordem natural; idempotency entre auth e rate-limit (replay consome quota mas não rate-limit) | ✓ |
| auth → idempotency → rate-limit → quota → ... | Replay cacheado não consome bucket; mas atacante pode flood com mesma key | |
| schedule → auth → rate-limit → ... | Schedule é per-tenant; ordem não faz sentido | |

**User's choice:** auth → rate-limit → quota → schedule → tokencount → dispatcher → billing-flush (Recommended)

### Q2: Shape do admin reporting endpoint

| Option | Description | Selected |
|--------|-------------|----------|
| GET /admin/usage com daily breakdown + summary | Query ?tenant&from&to&granularity; response {summary, rows[]}; SC-3 fields atendidos; auth X-Admin-Key bcrypt | ✓ |
| GET /admin/usage summary-only + GET /admin/usage/daily separado | Mais granular; mais código | |
| Defer reporting endpoint para Fase 7 | Viola SC-3 explicit | |

**User's choice:** GET /admin/usage com daily breakdown + summary (Recommended)

### Q3: Subcomandos novos no gatewayctl

| Option | Description | Selected |
|--------|-------------|----------|
| Suite completa: tenant set-mode/set-quota, prices set/list/set-fx, usage report, admin-key create/revoke | Operator faz tudo via CLI sem SQL bruto | ✓ |
| Subcomandos mínimos: só tenant set-mode + prices set | Operator usa SQL bruto; viola padrão Fase 2 D-A3 | |
| Sem CLI; tudo via /admin REST + SQL | Coerente com Fase 2 D-A3 admin REST deferido; mas Fases 5/6 ainda querem CLI | |

**User's choice:** Suite completa (Recommended)

### Q4: Auth do /admin endpoint nesta fase

| Option | Description | Selected |
|--------|-------------|----------|
| X-Admin-Key bcrypt em DB + env bootstrap | Tabela admin_keys; migration seed key default; gatewayctl admin-key create/revoke; substituído por Better Auth/SSO em Fase 10 PRD-06 | ✓ |
| Reusa X-API-Key tenant com role='admin' coluna | Mistura conceitos (admin não pertence a tenant) | |
| Sem auth; bind 127.0.0.1 only via Traefik | Frágil; qualquer um na VPS lê dados sensíveis | |

**User's choice:** X-Admin-Key bcrypt em DB + env bootstrap (Recommended)

---

## Claude's Discretion

Áreas onde Claude tem flexibilidade na execução:
- Lua script shape (token bucket clássico Stripe/Cloudflare pattern)
- Headers `X-RateLimit-*` shape (OpenAI-compat espelhada literal)
- Quota defaults seed (conservadores; operador refina)
- Pricing seed values (research confirma valores Apr 2026 antes da migration)
- Audit log entries para rejeições (mesmo padrão Fase 3 D-B3)
- Per-route WriteTimeout restoration (folded TODO)
- Wire `obs.RequestsTotal.Inc()` (folded TODO; metricsMiddleware no fim do chain)
- Loader unificado vs múltiplos (config registry vs 4 loaders independentes — execute decide)

## Deferred Ideas

Capturadas em CONTEXT.md `<deferred>` section:
- Per-tenant inflight quotas + fairness queue → Fase 5
- Fall-of-fallback off-hours para OpenAI direct chat → rejeitado (viola "Qwen fixo")
- Per-tenant pricing override → fora do escopo v1
- Auto-rotate USD/BRL fx via API externa → Fase 7/10
- Cost reconciliation vs fatura externa → Fase 7/10
- Quota soft-warning thresholds (80%) → Fase 7
- Multiple peak windows per tenant → Fase 9 se demanda surgir
- Idempotency-Key em /v1/embeddings + /v1/audio/transcriptions → continua deferido
- Per-route P95/P99 histograms com label per-tenant → Fase 7
- Better Auth/SSO no admin endpoint → Fase 10 PRD-06
- Webhook on quota breach → Fase 7
- Audit cold-storage export para MinIO → Fase 7/10
- Phantom cost para STT/embed local → implementado mas heurístico
- Char-count fast-path para token estimation → Fase 5+
- Per-app circuit breaker → Fase 5

---

*Discussion log gerado: 2026-04-20*
