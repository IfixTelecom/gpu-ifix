# Project Research Summary

**Project:** ifix-ai-gateway
**Domain:** AI Inference Gateway with multi-tenant routing, automatic failover, and auto-provisioned emergency GPU pods
**Researched:** 2026-04-17
**Confidence:** HIGH

## Executive Summary

**ifix-ai-gateway é um Go HTTP gateway OpenAI-compatible que concentra todas as chamadas de IA da Ifix Telecom (LLM, STT, embedding) sobre uma GPU RTX 4090 própria na Vast.ai, com failover para OpenRouter/OpenAI e spin-up emergencial automático de uma segunda GPU Vast.ai.** A pesquisa confirma que o valor do produto está na combinação: gateway OpenAI-compatível + consciência do hardware próprio (load shedding por saturação da GPU) + provisionamento emergencial. Nenhum gateway off-the-shelf (LiteLLM, Portkey, Bifrost, OneAPI, Cloudflare AI Gateway) oferece todas as três capacidades — portanto, custom build em Go é a decisão certa.

**A pesquisa surfacou 3 decisões técnicas no PROJECT.md que merecem revisão antes do roadmap** (ver seção "Conflitos e Ajustes" abaixo): substituir `llama-cpp-python` Python server por `llama.cpp` nativo; usar Infinity no lugar de sentence-transformers para embeddings; adotar Speaches no lugar de FastAPI custom para Whisper. Além disso, a detecção de saturação por GPU util/VRAM (decidida no PROJECT.md) é insuficiente sozinha — o padrão correto é sinal composto (inflight counter no gateway + latência P95 + VRAM) com histerese.

**Riscos principais identificados:** margem de VRAM apertada (1-2 GB na 4090 só no boot — cresce com KV cache e concorrência), cold pulls na Vast.ai de 10-60 minutos (exige imagem Docker pré-construída com weights), streaming mid-response não tem replay consistente (precisa política explícita: fail-fast / no-retry), risco de "spin-loop" se múltiplas instâncias do gateway concorrerem no provisionamento (precisa Redis distributed lock), e compliance LGPD ao enviar dados sensíveis de telefonia/cobranças para OpenAI durante failover.

## Key Findings

### Recommended Stack

Go como linguagem do gateway confirmada (chi v5 + stdlib `httputil.ReverseProxy` para streaming SSE; **NÃO usar Fiber** — incompatível com `http.Flusher`). Três departures do doc original:

**Core technologies:**
- **Gateway HTTP**: `go-chi/chi v5` + `httputil.ReverseProxy` + `slog` — streaming-compatible, stdlib-heavy, baixo overhead
- **LLM server**: `llama.cpp` (binário `llama-server` nativo + CUDA) rodando `unsloth/Qwen3.5-27B-GGUF Q4_K_M` — **substitui `llama-cpp-python` do doc**. Mais simples, sem camada Python, flag control limpo para tool-calling. Carrega template Jinja patched para Qwen (evita bug de tool-calling com role "developer")
- **STT server**: `speaches-ai/speaches` — **substitui `faster-whisper` + FastAPI custom do doc**. Sucessor ativo do `fedirz/faster-whisper-server`, OpenAI-compatible out of the box, Docker-native, zero custom server code
- **Embedding server**: `michaelf34/infinity` — **substitui `sentence-transformers` do doc**. 2-3× throughput para BGE-M3 mesma VRAM, OpenAI-compatible (caveat: BGE-M3 sparse-vector não suportado; só dense — aceitável para RAG do Ifix)
- **Resilience**: `sony/gobreaker/v2` (circuit breaker), `cenkalti/backoff/v5` (retry), `golang.org/x/time/rate` (rate limit)
- **Postgres**: `jackc/pgx/v5`
- **Redis**: `redis/go-redis/v9`
- **Distributed lock (leader-election + spin-loop prevention)**: `go-redsync/redsync`
- **Metrics**: `prometheus/client_golang` + `NVIDIA/dcgm-exporter` sidecar no pod GPU
- **Vast.ai automation**: REST API direto (não há Go SDK maduro; `aalekhpatel07/go-client-vastai` tem 4 commits e 0 stars — **não usar**)
- **OpenRouter/OpenAI**: HTTP direto (OpenRouter "official" Go SDK marca `not yet ready for production`)
- **Dashboard**: Next.js 15 + shadcn/Recharts (segue padrão converseai-v4)
- **Deploy**: Docker Compose + Portainer + webhook GitHub (segue padrão Ifix); pod GPU via imagem pré-construída `ghcr.io/ifixtelecom/ifix-ai-pod:vX.Y`

**NÃO usar (descartados com evidência):**
- **vLLM para Qwen 3.5 27B na 4090 single-GPU** — launch configs da comunidade usam tensor-parallel em 2 GPUs; [vLLM bug #37080](https://github.com/vllm-project/vllm/issues/37080) faz INT4 usar mais VRAM que FP8. Não é opção sem upgrade de hardware
- **TGI** — HuggingFace colocou em maintenance mode (Dec 11, 2025); recomendação oficial é vLLM ou SGLang
- **Fiber (Go web framework)** — construído sobre `fasthttp`, quebra `httputil.ReverseProxy` + `http.Flusher` (dealbreaker para SSE streaming LLM)
- **LiteLLM / Bifrost / Portkey como base** — nenhum deles suporta auto-provisioning de pods Vast.ai nem load-shedding por saturação de GPU

Ver `STACK.md` para detalhes completos.

### Expected Features

A pesquisa mapeou **21 features table stakes + 7 diferenciadores + 2 deferidos + 12 anti-features** (explicitamente não construir).

**Must have (table stakes — apps param de usar sem isso):**
- OpenAI-compatible endpoints (`/v1/chat/completions`, `/v1/embeddings`, `/v1/audio/transcriptions`) — apps só trocam `base_url`
- Streaming SSE para chat (com `FlushInterval: -1` obrigatório no Go)
- Tool/function calling pass-through
- Multi-provider fallback chain (local → OpenRouter → OpenAI)
- Circuit breaker por upstream (6 total: local-LLM, local-STT, local-embed, OpenRouter, OpenAI-Whisper, OpenAI-embed)
- Rate limiting por API key
- Logging estruturado com request IDs
- Token counting e custo por request
- Health checks por upstream (nível de modelo, não só container)
- Model alias mapping (cliente pede "qwen", gateway resolve para versão atual)
- Retries com backoff exponencial (NÃO fazer retry em streaming após primeiros bytes escritos no cliente)
- Idempotency keys para mutations
- Error format consistente (OpenAI-compatible error envelopes)
- API key auth + quota por tenant
- Request/response audit log

**Should have (diferenciadores Ifix — onde o projeto se justifica vs adotar LiteLLM):**
- **Load shedding por saturação da GPU** — sinal composto (inflight counter no gateway + P95 latency + VRAM) com histerese, desvia overflow para OpenRouter antes de falhar
- **Auto-provisioning emergencial de pods Vast.ai** — com guardrails (max $0,40/h, max 1 pod ativo, grace period 5min)
- **Cost attribution e billing por app** — cada API key é um centro de custo com reporting
- **Schedule-based routing por tenant** — 24/7 ou pico/vale (08-22h) configurável por API key
- **Audio chunking no gateway para Whisper** — requests longos (>10min) são chunked antes de enviar ao pod
- **Embedding batching** — agrupa requests pequenos antes de enviar ao servidor
- **Data-class tagging por API key** — apps de telefonia/cobranças marcados "sensível" usam política de failover diferente (LGPD)

**Defer (v2+ — fora de escopo dessa milestone):**
- Semantic caching — grey zone de similaridade (0.85-0.92) gera respostas incorretas; adiar até ter dados de produção
- Request shadowing/canary — útil mas não bloqueante

**Anti-features (deliberate — NÃO construir, previne scope creep):**
- PII redaction centralizada — deixar para cada app (regras são domain-specific)
- SSO / RBAC granular — 4 admins total, API key + role simples basta
- Multi-region — Ifix não tem deploy fora do Brasil
- Prompt engineering helpers — escopo das apps, não do gateway
- TTS na GPU — VRAM apertada; voice-api continua CPU

Ver `FEATURES.md` para complexidade (S/M/L), dependências e matriz comparativa (LiteLLM, Portkey, Bifrost, Helicone, Cloudflare AI Gateway, OneAPI).

### Architecture Approach

**Padrão recomendado:** "fat gateway + dumb inference pods". Gateway Go monolítico (único binário) concentra roteamento, breakers, FSM de failover, provisionamento e alertas. Pods GPU rodam apenas os 3 servidores de modelo + `dcgm-exporter` sidecar + health-bridge leve. Split control-plane/data-plane é overkill para v1 — estrutura de código mantém split latente para upgrade futuro barato.

**Major components:**
1. **Gateway (Go)** — VPS dedicada (4 vCPU, separada da GPU para detectar quedas com confiabilidade). Subsistemas: router (chi), auth (pgx + Redis cache), circuit breakers, dispatcher (load shed + fallback chain), metrics emitter, alert dispatcher, provisioner (state machine), leader-elector (Redis distributed lock para HA futura)
2. **Inference pod (Vast.ai 4090)** — Docker Compose com `llama.cpp:server-cuda` (8000) + `speaches` (8001) + `infinity` (8002) + `dcgm-exporter` (9400) + health-bridge (9090). Imagem pré-construída `ghcr.io/ifixtelecom/ifix-ai-pod:vX.Y` com weights incluídos (cold-start 3-5min vs 10-20min re-download)
3. **State stores** — Postgres compartilhado Digital Ocean (schema dedicado: API keys, quotas, audit, billing); Redis (rate-limit counters, circuit state, pod registry, latência recente, distributed lock)
4. **Dashboard backend** — mesmo gateway Go expõe `/admin/metrics` consumida por Next.js 15 (BFF-style). Sem Prometheus separado (métricas via endpoint do próprio gateway)
5. **Alert dispatcher** — worker no gateway com severity tiers (critical → WhatsApp+email; warning → email; info → dashboard). WhatsApp via Evolution API (confirmar provedor Ifix na fase)

**Failover state machine (9 estados no Redis):** HEALTHY → DEGRADED → FAILED_OVER → EMERGENCY_PROVISIONING → EMERGENCY_ACTIVE → RECOVERING → COOLDOWN → OFF_HOURS → MAINTENANCE. Leader-only avança estado; réplicas leem snapshot 1Hz para decisão de rota.

**Model-weight strategy:** imagem Docker pré-construída publicada no `ghcr.io/ifixtelecom/ifix-ai-pod`. Qwen GGUF + Whisper weights + BGE-M3 dentro da imagem. Fallback em S3/R2 se imagem crescer demais. Descartado: re-download (10-20min frio) e volume persistente Vast.ai (bill idle).

**Deployment topology:** Single VPS gateway para v1. Postgres DO na mesma região do VPS (latência baixa). DNS: `gateway.ifix.com.br` → VPS IP via Cloudflare. Cloudflare Tunnel opcional para secure ingress.

Ver `ARCHITECTURE.md` para diagrama de fluxo por tipo de request, state machine completa e lifecycle de auto-provisioning.

### Critical Pitfalls

Top 5 pitfalls que definem phases do roadmap:

1. **VRAM margin de 1-2 GB é estado de boot, não runtime** — KV cache de Qwen 27B cresce com contexto; Whisper batches aumentam uso; 100% GPU util é NORMAL durante inferência (não significa saturado). **Mitigação:** Phase 1 mede VRAM empiricamente sob carga (2 chats concorrentes 8k + 1 Whisper longa); 3 GB de headroom mínimo; `max_model_len=16384` enforçado; alerta em `>21 GB`

2. **Streaming failover mid-response é fundamentalmente não resolvível sem cooperação do cliente** — quando primary cai durante chunk, não dá para "replay" sem duplicar billing. **Mitigação:** Phase 3 fixa política explícita: `no-retry-for-streams` + status 503 + cliente aceita retry end-to-end. Tool calls: idempotency key separado; gateway NÃO retry, agent layer retry

3. **Vast.ai cold pulls 10-60 min** — imagem não cacheada no host, downloads longos durante incidente, pods stuck em "loading" contando no budget. **Mitigação:** Phase 4 usa imagem pré-construída + filtro de hosts por network capability + health probe no modelo (não só container running) + budget cap inclui tempo de provisioning

4. **Spin-loop de provisioning** — detecção pensa que primary caiu, provisioner dispara, primary recupera, novo pod é desperdiçado; múltiplas instâncias do gateway correm entre si gerando pods fantasma. **Mitigação:** Redis distributed lock (`redsync`) + reconciliador single-leader + cancelamento in-flight quando primary recupera durante provisioning

5. **LGPD em failover de telefonia/cobranças** — dados sensíveis sendo enviados para OpenAI/OpenRouter durante failover pode violar base legal e disclosure de sub-processadores. **Mitigação:** Phase 2 schema tem campo `data_class` em API keys (`sensitive` | `normal`); apps sensitive têm política de failover diferente (ex: enfileira ao invés de enviar para external); revisão legal Ifix antes de GA

Outros 9 pitfalls cobertos em `PITFALLS.md`: tokenizer drift entre local/OpenRouter, context window mismatch, ruído neighbor multi-tenant, cardinality explosion em métricas, goroutine leaks em streams longos, connection pool HTTP, request cancellation propagation, slog vs zerolog performance, Qwen tool-call inconsistência no OpenRouter.

## Conflitos e Ajustes ao PROJECT.md

Três itens merecem revisão antes de fechar requirements:

| Item no PROJECT.md | Ajuste sugerido pela pesquisa | Impacto |
|---|---|---|
| "Qwen 3.5 27B via `llama-cpp-python`" | Usar `llama.cpp` nativo (binário `llama-server` + CUDA) | Remove camada Python; flag control para tool-calling é mais limpo; elimina dependências pip extras |
| "Whisper via `faster-whisper` + FastAPI custom" | Usar `speaches-ai/speaches` | Zero custom server code; OpenAI-compatible nativo; mantido ativamente em 2026 |
| "Embedding via `sentence-transformers`" | Usar `michaelf34/infinity` | 2-3× throughput mesma VRAM; OpenAI-compatible. Caveat: BGE-M3 sparse-vector não suportado (só dense — aceitável para RAG Ifix) |
| "Detecção de saturação por GPU util / VRAM" | Ampliar para sinal composto: inflight counter no gateway (primário) + P95 latency + VRAM (secundários) + histerese | GPU util 100% é NORMAL durante inferência; simples threshold gera flapping e desvio incorreto para OpenRouter (custo) |

Essas mudanças **preservam todas as decisões estruturais** (linguagem Go, Vast.ai primária, OpenRouter fallback, guardrails, auto-shutdown) — apenas trocam bibliotecas específicas por opções mais maduras em 2026 e refinam a detecção de saturação.

## Implications for Roadmap

Baseado na convergência dos 4 agents, estrutura de phases sugerida (granularity "fine" — 8-12 phases):

### Phase 1: Fundação GPU pod (imagem Docker pré-construída)
**Rationale:** Cold pull Vast.ai é 10-60min; imagem pré-construída com weights vai ser dependência crítica de Phase 4 (auto-provisioning). Construir e validar cedo, em isolado.
**Delivers:** Imagem `ghcr.io/ifixtelecom/ifix-ai-pod:v1.0.0` com llama.cpp + speaches + infinity + dcgm-exporter + health-bridge, smoke-test em pod Vast.ai de teste
**Addresses:** Table stakes inference servers
**Avoids:** Pitfall #3 (cold pulls), pitfall #1 (mede VRAM empiricamente sob carga)

### Phase 2: Gateway scaffold + multi-tenant auth
**Rationale:** Sem gateway, nada roteia. Auth primeiro porque todo endpoint depende dela; schema de API keys precisa do campo `data_class` antes de emitir chaves (pitfall #5).
**Delivers:** Go binary com chi router + slog + pgx + go-redis + Prometheus + Sentry. Endpoints OpenAI-compat (`/v1/chat/completions`, `/v1/embeddings`, `/v1/audio/transcriptions`). API key auth com quota por tenant e `data_class`. Roteamento único ao pod primário. Schema Postgres e migrations.
**Uses:** chi v5, pgx/v5, go-redis/v9, slog
**Avoids:** Pitfall #5 (schema com data_class desde v1)

### Phase 3: Resiliência — circuit breakers + fallback chain (external only)
**Rationale:** Entrega primeira metade do Core Value ("failover invisível") sem depender de Phase 4 (auto-provisioning). Já atende quando primary cai — só não sobe pod emergencial ainda.
**Delivers:** gobreaker/v2 por upstream (6 breakers). backoff/v5 com context. Fallback chain: local → OpenRouter → OpenAI. Política explícita de streaming (no-retry after first bytes). Idempotency keys. Error format consistente.
**Uses:** gobreaker/v2, backoff/v5
**Avoids:** Pitfall #2 (streaming failover), parte do #4 (sem spin-up, sem spin-loop possível)

### Phase 4: Multi-tenant — rate limiting + quotas + cost attribution
**Rationale:** Antes de ativar load shedding (Phase 5), precisa saber quem está gerando tráfego para decidir quem é shed. Cost attribution bloqueia quotas.
**Delivers:** Rate limit por API key. Quotas diárias/mensais. Token counting + custo por request. Tabela de billing (append-only). Reporting por app.
**Uses:** golang.org/x/time/rate, Redis Lua atomic ops para contadores

### Phase 5: Load shedding — saturation-aware routing
**Rationale:** Diferenciador central do produto. Depende de Phase 3 (fallback chain existente) e Phase 4 (inflight counter por tenant).
**Delivers:** Inflight counter no gateway (atomic), integração com dcgm-exporter para VRAM, sinal composto com histerese, overflow para OpenRouter sob saturação. Schedule-based routing (24/7 ou pico/vale por app).
**Avoids:** Pitfall #1 (detecção composta, não só GPU util)

### Phase 6: Auto-provisioning emergencial Vast.ai
**Rationale:** Isolado, alto risco. Vast.ai sem Go SDK; precisa build-your-own. Dependente de imagem Phase 1 e state machine Phase 3.
**Delivers:** Cliente REST Vast.ai em Go. State machine de pod emergencial (9 estados no Redis, leader-only). Guardrails (preço max $0,40/h, 1 pod ativo, auto-shutdown + grace period). Cancelamento in-flight quando primary recupera.
**Uses:** Vast.ai REST API (direto), redsync
**Avoids:** Pitfall #4 (distributed lock + reconciler single-leader)

### Phase 7: Dashboard + alerting
**Rationale:** Pode correr paralelo a Phase 6 uma vez que FSM existe (good splitting).
**Delivers:** Next.js 15 dashboard (latência, error rate, custo, requests por app, status failover). WhatsApp/email alerts com severity tiers. Rate-limit de alertas.
**Uses:** Next.js 15 + shadcn + Recharts, Evolution API (ou provider Ifix) para WhatsApp

### Phase 8: Integração apps clientes — ConverseAI v4
**Rationale:** Primeiro cliente. Agents Python + API Elysia trocam `base_url`.
**Delivers:** ConverseAI v4 apontando para gateway. Rollback plan pronto.

### Phase 9: Integração apps clientes — Chat Ifix (transcrição Whisper)
**Rationale:** Baixo risco (transcrição mais isolada que chat), boa segunda migração.

### Phase 10: Integração apps clientes — Telefonia/NextBilling
**Rationale:** Dados sensíveis; precisa `data_class=sensitive` validado (pitfall #5). Precisa revisão LGPD antes de GA.

### Phase 11: Integração apps clientes — Cobranças, Campanhas, voice-api
**Rationale:** Últimos; padrão já estabelecido, risco baixo.

### Phase 12: Produção + observabilidade reforçada + load tests
**Rationale:** Hardening final antes de declarar GA. Audit de pitfalls, adjust de thresholds, pen test leve.
**Delivers:** Load test com 3 apps simultâneas. Chaos test (matar pod primário, medir tempo até recover). Audit logs completos. Runbook de incidentes.

### Phase Ordering Rationale

- **Pod primeiro** porque é bloqueante de Phase 4 (auto-provisioning depende de imagem validada) e cold pulls Vast.ai são 10-60min (surpresa ruim se descobre em Phase 4)
- **Gateway sem failover depois** porque entrega valor imediatamente (apps já podem migrar `base_url` e cortar dependência externa direta)
- **Resilience sem provisioning** porque entrega 50% do Core Value sem depender da parte mais arriscada (Vast.ai API)
- **Multi-tenant antes de load shedding** porque shedding precisa saber "de quem é o tráfego" para decidir
- **Auto-provisioning isolado** porque risco técnico alto + no Go SDK + API quirks próprias — merece phase dedicada
- **Integração apps em ordem de risco crescente**: ConverseAI (conhecemos), Chat Ifix (isolado), Telefonia (LGPD), Cobranças/Campanhas (último)

### Research Flags

Phases com research adicional necessária durante `/gsd-plan-phase`:

- **Phase 1:** Qwen 3.5 27B tool-calling template patched — validar contra upstream, testar antes de imagem final. Quantização Q4_K_M vs Q5_K_M tradeoff qualidade/VRAM na 4090. VRAM real sob carga (não só boot)
- **Phase 5:** Threshold reais de saturação (85% GPU util, 22 GB VRAM são defaults — tunar após Phase 1 baseline). Qual histerese previne flapping sem mascarar saturação real
- **Phase 6:** Vast.ai API quirks reais (SSH key injection, onstart flows, rate limits da API, exposure de portas, bid acceptance timing). **Spike timeboxed antes de committar phase**
- **Phase 7:** Provider WhatsApp Ifix preferido (Evolution API? Z-API? Chatwoot? API própria?). Better Auth dashboard — instance própria ou compartilhada com ConverseAI?

Phases com padrão estabelecido (research leve):

- **Phase 2:** Go + chi + pgx + Redis é padrão, amplo material
- **Phase 3:** gobreaker e backoff são estáveis, exemplos abundantes
- **Phase 4:** Rate limit por Redis Lua é padrão
- **Phases 8-11:** Integração de clientes é trocar env vars; plano de rollback por app

## Confidence Assessment

| Area | Confidence | Notes |
|------|------------|-------|
| Stack | HIGH | Todas escolhas verificadas com docs oficiais/GitHub recente. Bifrost/LiteLLM/Portkey descartados com evidência específica. vLLM não cabe na 4090 single-GPU confirmado por bug report + launch configs da comunidade |
| Features | HIGH | Benchmark cruzado de 6 gateways em produção (LiteLLM, Portkey, Bifrost, Cloudflare, Helicone, OneAPI) — categorização table stakes/diferenciadores/anti é consistente |
| Architecture | HIGH | "Fat gateway + dumb pods" segue Envoy AI Gateway + Bifrost. State machine é design prescritivo (não benchmarked), mas backed by Envoy AI Gateway two-phase fallback |
| Pitfalls | HIGH | Cada pitfall tem fonte primária (GitHub issues vLLM/llama.cpp/Roo-Code, post-mortem público OpenRouter Feb 2026, docs oficiais Vast.ai, NVIDIA DCGM). Auto-provisioning pitfalls MEDIUM (analogia de K8s autoscalers, não Vast.ai-native) |

**Overall confidence:** HIGH — pesquisa suporta decisões estruturais do PROJECT.md e oferece ajustes pontuais em bibliotecas/algoritmos, não em arquitetura.

### Gaps to Address

- **VRAM empírica sob carga** — resolver em Phase 1 (smoke test com 2 chats 8k + 1 Whisper)
- **Thresholds de saturação (util %, VRAM GB, latency P95)** — tunar em Phase 5 após baseline de produção
- **Vast.ai REST API specifics** — spike timeboxed de 3h no início de Phase 6 antes de commit
- **Qwen 3.5 27B no OpenRouter — qual provider upstream (Together? Fireworks? DeepInfra?)** e comportamento de tool-calling — teste de integração em Phase 3
- **LGPD — revisão legal Ifix** se políticas de privacy cobrem OpenAI/OpenRouter como sub-processadores — externa ao research, pedido ao time jurídico antes de Phase 10
- **WhatsApp provider Ifix** — confirmar com time em Phase 7
- **Headroom Postgres DO compartilhado** para write rate de billing — avaliar em Phase 4 (pode exigir batch insert ou instância dedicada)

## Sources

### Primary (HIGH confidence — verified via official docs or GitHub)

- [Bifrost GitHub](https://github.com/maximhq/bifrost) — gateway features comparison
- [LiteLLM Proxy GitHub](https://github.com/BerriAI/litellm) — features benchmark
- [Portkey Gateway GitHub](https://github.com/Portkey-AI/gateway) — fallbacks documentation
- [Cloudflare AI Gateway docs](https://developers.cloudflare.com/ai-gateway/) — features list
- [Envoy AI Gateway System Architecture](https://aigateway.envoyproxy.io/docs/concepts/architecture/system-architecture/) — arquitetura reference
- [chi v5](https://github.com/go-chi/chi), [sony/gobreaker](https://github.com/sony/gobreaker), [cenkalti/backoff v5](https://github.com/cenkalti/backoff), [pgx v5](https://github.com/jackc/pgx), [go-redis v9](https://github.com/redis/go-redis), [go-redsync](https://github.com/go-redsync/redsync) — bibliotecas Go
- [llama.cpp](https://github.com/ggml-org/llama.cpp), [Speaches](https://github.com/speaches-ai/speaches), [Infinity](https://github.com/michaelfeil/infinity) — inference servers
- [Unsloth Qwen3.5-27B-GGUF](https://huggingface.co/unsloth/Qwen3.5-27B-GGUF) — modelo específico
- [vLLM Qwen3.5 INT4 VRAM bug #37080](https://github.com/vllm-project/vllm/issues/37080) — vLLM não viável na 4090 single
- [TGI maintenance mode announcement (premai.io)](https://blog.premai.io/llm-inference-servers-compared-vllm-vs-tgi-vs-sglang-vs-triton-2026/) — TGI descontinuado
- [Vast.ai API Reference](https://docs.vast.ai/api-reference/introduction) — REST API
- [Vast.ai Quickstart (image pull 10-60 min)](https://docs.vast.ai/quickstart) — cold-start timing
- [OpenRouter Outages Feb 17/19 2026](https://openrouter.ai/announcements/openrouter-outages-on-february-17-and-19-2026) — post-mortem de fallback único insuficiente
- [NVIDIA DCGM Exporter](https://github.com/NVIDIA/dcgm-exporter) — GPU metrics
- [Go issue #27816](https://github.com/golang/go/issues/27816) — ReverseProxy streaming specifics
- [Redis Distributed Locks](https://redis.io/docs/latest/develop/clients/patterns/distributed-locks/) — Redlock pattern

### Secondary (MEDIUM confidence — community consensus)

- [Top 5 Enterprise LLM Gateways 2026 (getmaxim.ai)](https://www.getmaxim.ai/articles/top-5-enterprise-llm-gateways-in-2026/) — comparison article
- [Portkey: Retries, Fallbacks, Circuit Breakers](https://portkey.ai/blog/retries-fallbacks-and-circuit-breakers-in-llm-apps/) — padrões de resilience
- [Implementing Circuit Breakers for LLM Services in Go (dasroot.net)](https://dasroot.net/posts/2026/02/implementing-circuit-breakers-for-llm-services-in-go/) — Go-specific
- [QuantTrio Qwen3.5-27B-AWQ launch configs](https://huggingface.co/QuantTrio/Qwen3.5-27B-AWQ/discussions/1) — vLLM TP=2 requirement
- [llm-d 0.3 blog](https://llm-d.ai/blog/llm-d-v0.3-expanded-hardware-faster-perf-and-igw-ga) — saturation-aware patterns
- [How We Handle LLM Provider Failover (llmgateway.io)](https://llmgateway.io/blog/how-we-handle-llm-provider-failover) — streaming failover policy
- [Goroutine Leaks in Go: 4 Patterns + Go 1.26 profile](https://dev.to/gabrielanhaia/goroutine-leaks-in-go-the-4-patterns-and-the-new-profile-in-go-126-5e73) — Go-specific pitfalls

### Tertiary (LOW confidence — single source, needs validation)

- [Qwen3.5 patched Jinja template gist](https://gist.github.com/sudoingX/c2facf7d8f7608c65c1024ef3b22d431) — community patch, validar upstream
- [aalekhpatel07/go-client-vastai](https://github.com/aalekhpatel07/go-client-vastai) — abandonado (4 commits, 0 stars), confirma ausência de Go SDK
- [FPF: Brazil ANPD Preliminary Study on Generative AI](https://fpf.org/blog/brazils-anpd-preliminary-study-on-generative-ai-highlights-the-dual-nature-of-data-protection-law-balancing-rights-with-technological-innovation/) — LGPD genérico, precisa counsel Ifix

---
*Research completed: 2026-04-17*
*Ready for roadmap: yes*
