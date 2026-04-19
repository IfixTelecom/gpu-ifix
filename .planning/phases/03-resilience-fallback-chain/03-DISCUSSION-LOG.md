# Phase 3: Resilience & Fallback Chain - Discussion Log

> **Audit trail only.** Do not use as input to planning, research, or execution agents.
> Decisions are captured in CONTEXT.md — this log preserves the alternatives considered.

**Date:** 2026-04-19
**Phase:** 03-resilience-fallback-chain
**Areas discussed:** Failover (trigger & probes), Política sensitive (LGPD), OpenRouter provider pin, Estado do breaker + tabela upstreams

---

## Gray areas selection

| Option | Description | Selected |
|--------|-------------|----------|
| Failover: trigger & probes | Sinal que aciona fallback + probe proativo de 10s | ✓ |
| Política sensitive (LGPD) | O que acontece com `data_class: sensitive` quando primary down | ✓ |
| OpenRouter provider pin | Fireworks/Together/DeepInfra por trás do OpenRouter Qwen 3.5 27B | ✓ |
| Estado do breaker + tabela upstreams | In-process vs Redis vs híbrido + schema `upstreams` | ✓ |

**User's choice:** all four areas selected (multiSelect).
**Notes:** As duas gray areas não-selecionadas (Enforcement 16k cap, detalhes fine-tuning de retry non-stream) ficam na discrição do planner com defaults documentados em CONTEXT.md §Claude's Discretion.

---

## Failover — trigger & probes

### Q1: Qual sinal aciona o fallback para o próximo upstream?

| Option | Description | Selected |
|--------|-------------|----------|
| Breaker-open puro | Só desvia quando `gobreaker` está OPEN; probe proativo 10s alimenta o estado. Simples, determinístico. | ✓ |
| Retry-first-then-fallback | Toda falha tenta 1x no mesmo upstream antes de marcar falha pro breaker. | |
| Hedged parallel | Dispara em primary + secondary simultaneamente. Reservado para Fase 5 se necessário. | |

**User's choice:** Breaker-open puro (Recommended)
**Notes:** Combina com probe synthetic denso da Q2 para cobrir SC-1 (≤10s desvio).

### Q2: Como o probe proativo de 10s (RES-04) deve ser feito?

| Option | Description | Selected |
|--------|-------------|----------|
| Synthetic E2E | Mini-request real por upstream (chat `max_tokens:1`, STT stub wav, embed "ping"); detecta latência real. | ✓ |
| HTTP /health liveness | `/health` nos locais + HEAD nos externos. Cego para degradação. | |
| Híbrido liveness+synthetic on-degrade | Liveness sempre, synthetic só quando degradado. Adiciona estado extra. | |

**User's choice:** Synthetic E2E (Recommended)
**Notes:** Externos (OpenRouter/OpenAI) são probed sob demanda (só quando primary OPEN) pra economizar cota.

### Q3: Thresholds do `gobreaker` (consecutive failures + timeout de reset)?

| Option | Description | Selected |
|--------|-------------|----------|
| Strict: 3 falhas → open, 30s → half-open | Abrir rápido favorece SC-1. PITFALLS.md sugere 5; ajustado pra 3. | ✓ |
| Conservative: 5 falhas → open, 60s half-open | Menos flap em transientes mas arranha SC-1 com probe só. | |
| Per-upstream: locais strict, externos conservative | Locais 3/30s, externos 5/60s (evita 429 derrubar breaker). | |

**User's choice:** Strict: 3 falhas → open, 30s → half-open (Recommended)
**Notes:** Justificativa de rejeitar per-upstream: 429 já não conta como falha (Q4), então o racional do conservative em externos cai.

### Q4: O que conta como 'falha' no contador do breaker?

| Option | Description | Selected |
|--------|-------------|----------|
| 5xx + timeout + probe-fail | 4xx nunca; 429 não (métrica separada `gateway_upstream_throttled_total`). | ✓ |
| Só probe synthetic | Tráfego real passa sempre; detecção depende 100% do probe. | |
| 5xx + timeout + 429 + probe-fail | Incluí 429 como sinal de saúde (risco: trial account derruba breaker). | |

**User's choice:** 5xx + timeout + probe-fail (Recommended)
**Notes:** 429 fica em métrica separada pra Fase 4 consumir ao decidir outbound rate-limit.

---

## Política sensitive (LGPD)

### Q1: Quando primary está down e request chega com `data_class: sensitive`, o que o gateway faz?

| Option | Description | Selected |
|--------|-------------|----------|
| Retry in-memory curto | 3× exp-backoff in-memory (200ms → 800ms → 3s, total ~4s) aguardando breaker/probe voltar. | ✓ |
| Fail-closed imediato 503 | Zero espera — breaker OPEN + sensitive = 503 direto. | |
| Fila Redis com worker | Enfileira em `gw:sensitive:queue:{tenant_id}` + long-poll. | |

**User's choice:** Retry in-memory curto (Recommended)
**Notes:** Os 4s pegam 95% dos blips micro de Vast.ai sem segurar HTTP por tempo absurdo.

### Q2: Qual status code + código de erro quando a política sensitive bloqueia o request?

| Option | Description | Selected |
|--------|-------------|----------|
| 503 + `upstream_unavailable_for_sensitive_tenant` | Envelope OpenAI com código específico; `Retry-After: 30`. | ✓ |
| 429 + `sensitive_tenant_backpressure` | Trata como throttling explicitamente. | |
| 502 + `primary_only_unreachable` | Bad Gateway semanticamente correto mas menos discernível. | |

**User's choice:** 503 + `upstream_unavailable_for_sensitive_tenant` (Recommended)
**Notes:** Código discriminável de 503 genérico — app cliente (Cobranças, Telefonia) pode tratar separado.

### Q3: Como o audit_log registra a decisão sensitive-blocked?

| Option | Description | Selected |
|--------|-------------|----------|
| Linha em `audit_log` com `upstream='blocked_sensitive'` | Mesma tabela; permite COUNT queries pro dashboard Fase 7. | ✓ |
| Tabela separada `sensitive_block_events` | Legal-friendly mas fragmenta query patterns. | |
| Flag booleana + campo texto | Requer alter table de audit_log; aumenta row size. | |

**User's choice:** Linha em `audit_log` com `upstream='blocked_sensitive'` (Recommended)
**Notes:** `audit_log_content` fica ausente (consistente com Fase 2 D-B2 — sensitive nunca persiste content).

### Q4: Sensitive tenant em `stream: true` — aplica o mesmo retry in-memory ou fail-fast imediato?

| Option | Description | Selected |
|--------|-------------|----------|
| Fail-fast imediato 503 | Streaming sensitive com breaker OPEN pre-dispatch = 503 já. Consistente com RES-05. | ✓ |
| Mesmo retry in-memory (4s) | Gentil mas client HTTP pode ter lido headers e travar. | |

**User's choice:** Fail-fast imediato 503 (Recommended)
**Notes:** Estender o fail-fast de RES-05 para pre-flight evita HTTP stream aberto esperando breaker.

---

## OpenRouter provider pin

### Q1: Qual provider por trás do OpenRouter (p/ Qwen 3.5 27B) pinar no launch?

| Option | Description | Selected |
|--------|-------------|----------|
| Fireworks | Mais estável em tool-calling Qwen; referência ClickHouse/Cline. | ✓ |
| Together | Mais barato; variações de schema em prompts longos. | |
| DeepInfra | Quality similar; menos docs sobre edge cases. | |
| Auto com `provider.order` fixo | Lista com fallback interno OpenRouter; quebra reprodutibilidade durante SC-4. | |

**User's choice:** Fireworks (Recommended)
**Notes:** Config via env var permite troca sem redeploy se operational data indicar mudança.

### Q2: Como injetar o pin de provider no request pro OpenRouter?

| Option | Description | Selected |
|--------|-------------|----------|
| Campo `provider` no body + `allow_fallbacks:false` | Director modifica body; config via env; reprodutibilidade máxima. | ✓ |
| HTTP-Referer + provider routing rule no OpenRouter dashboard | Config fora do repo; viola infra-as-code. | |
| Sem pin — deixar OpenRouter auto-route | Não compatível com SC-4 sem validação empírica ampla. | |

**User's choice:** Campo `provider` no body + `allow_fallbacks:false` (Recommended)
**Notes:** Auditável no log; request body-level sobrevive a proxies.

### Q3: Estratégia de teste contra drift de tool-call entre local e OpenRouter?

| Option | Description | Selected |
|--------|-------------|----------|
| CI suite de contrato + manual smoke em deploy | Opt-in (`OPENROUTER_API_KEY`), schema-shape asserts; rodado pré-deploy. | ✓ |
| CI obrigatório em todo PR | Custo cota + latency CI; Fase 10+. | |
| Sem teste automatizado — runbook só | Pass mas fácil esquecer. | |

**User's choice:** CI suite de contrato + manual smoke em deploy (Recommended)
**Notes:** ~$0.01/run via OpenRouter; skip silencioso sem API key.

### Q4: Se Fireworks cair (breaker OpenRouter OPEN), pula p/ OpenAI direto ou enfileira/503?

| Option | Description | Selected |
|--------|-------------|----------|
| Não há fallback p/ chat (503 OpenAI-compat error) | Drift Qwen→GPT muito grande; viola "Qwen fixo". STT/embed mantêm OpenAI próprio. | ✓ |
| Adicionar OpenAI GPT-4o-mini como 3º tier chat | Viola decisão PROJECT.md. | |
| Retry Fireworks mais algumas vezes antes de desistir | Contradiz D-A1 breaker-open puro. | |

**User's choice:** Não há fallback p/ chat (503 OpenAI-compat error) (Recommended)
**Notes:** Documentar no runbook Fase 7/10.

---

## Estado do breaker + tabela `upstreams`

### Q1: Onde vive o estado do `gobreaker` (closed/half-open/open)?

| Option | Description | Selected |
|--------|-------------|----------|
| Híbrido: in-process autoritativo + mirror Redis | Atomic in-process no hot path + Pub/Sub pra Fase 6 + visibility dashboard Fase 7. | ✓ |
| Puramente in-process | Simples, zero-deps mas Fase 6 com 2 réplicas tem visões divergentes. | |
| Puramente Redis-shared | Consistência forte mas +1 RTT por request no hot path. | |

**User's choice:** Híbrido: in-process autoritativo + mirror Redis (Recommended)
**Notes:** Prepara Fase 6 (emergency pod leader election) sem refactor.

### Q2: Schema da tabela `upstreams` (deferida da Fase 2 D-D3)?

| Option | Description | Selected |
|--------|-------------|----------|
| Completa com hot-reload | Full schema + circuit_config JSONB + last_probe_* + LISTEN/NOTIFY. | ✓ |
| Mínima só com state persistido | URL/auth em env; perde hot-reload de tier/enabled. | |
| Env vars continuam autoritativos; DB só para observabilidade | Restart pra trocar; fere padrão 'config via DB' que Fase 5 vai precisar. | |

**User's choice:** Completa com hot-reload (Recommended)
**Notes:** Coluna `url_env` e `auth_bearer_env` guardam nome do env var (não o valor) — gateway resolve no boot/reload.

### Q3: Bootstrap — como a tabela `upstreams` é populada no primeiro deploy?

| Option | Description | Selected |
|--------|-------------|----------|
| Migration SQL inicial com INSERTs fixos | Migration `0011_seed_upstreams.sql`; determinístico. | ✓ |
| gatewayctl subcommand `upstreams seed` | Flex mas one-more-step pro operator. | |
| Gateway auto-seed no boot se tabela vazia | Conveniente mas esconde config drift. | |

**User's choice:** Migration sql inicial com INSERTs fixos (Recommended)
**Notes:** 6 linhas fixas (local-llm, openrouter-chat, local-stt, openai-whisper, local-embed, openai-embed).

### Q4: Como o gateway detecta mudanças na tabela `upstreams` em runtime (hot-reload)?

| Option | Description | Selected |
|--------|-------------|----------|
| Postgres LISTEN/NOTIFY | Trigger + conexão dedicada fora do pgxpool; reload <1s. | ✓ |
| Poll 5s | Simples mas latency 5s. | |
| Apenas restart (sem hot-reload) | Defeita o ponto de config em DB. | |

**User's choice:** Postgres LISTEN/NOTIFY (Recommended)
**Notes:** Fase 5 vai reutilizar este canal pra thresholds de saturação.

---

## Claude's Discretion

Áreas onde o usuário não quis discutir (ficam na discrição do planner seguindo defaults documentados em CONTEXT.md):

- **Enforcement do 16k cap (RES-07, SC-5):** pre-dispatch via `POST /tokenize` do llama.cpp server + cache Redis 60s. Fast-path char→token só se overhead medido for proibitivo (Fase 5+).
- **Retry non-stream details (RES-02):** `cenkalti/backoff/v5` com `MaxElapsedTime=1s`, `InitialInterval=100ms`, `Multiplier=2.0`, `RandomizationFactor=0.3`. Respeita `Retry-After` via `backoff.RetryAfterError`.
- **Tool-call detection em stream (RES-06, SC-4):** Interceptor no `ModifyResponse` parseia SSE deltas, marca `tool_call_emitted=true` no context; disconnect após flag = 502 com código `tool_call_partial_stream`.
- **`UPSTREAM_*_AUTH_BEARER` injection:** Director resolve `auth_bearer_env` da tabela `upstreams`; injeta `Authorization: Bearer <valor>` antes do dispatch.
- **Probe goroutine arquitetura:** ticker 10s + `errgroup.Group` parallel probes + buffered channel para UPDATE `last_probe_*`.
- **`/v1/health/upstreams` payload refactor:** novo shape com mapa `upstreams` (state/tier/role/last_probe_ms/last_probe_at).

## Deferred Ideas

Ideias levantadas e não adotadas (ver CONTEXT.md §deferred para racional completo):

- Fallback-of-fallback OpenAI GPT-4o-mini para chat
- Hedged parallel requests
- Retry-first-then-fallback
- CI obrigatório de tool-call drift em toda PR
- Cost attribution por provider
- Rate-limit 429 como sinal de breaker
- Idempotency-Key em embeddings/transcriptions
- Per-tenant circuit breaker overrides
- Dashboard UI de breaker state (Fase 7)
- WhatsApp/email alerts em breaker trip (Fase 7)
- OAuth token rotation para upstream auth
- HEAD-based liveness
- Char-count fast-path para context window
- Per-route WriteTimeout fine-tune (fold como refactor mecânico)
