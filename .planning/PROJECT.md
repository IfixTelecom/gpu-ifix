# ifix-ai-gateway

## What This Is

Plataforma central de IA da Ifix Telecom: um gateway HTTP que serve LLM, transcrição (STT) e embeddings para todas as aplicações da empresa (ConverseAI v4, Chat Ifix, Telefonia/NextBilling, Cobranças, Campanhas, voice-api). Roda em GPU própria (RTX 4090 alugada na Vast.ai) com failover automático para OpenRouter e spin-up emergencial paralelo de uma segunda GPU quando a primária cai ou satura.

## Core Value

**Nenhuma aplicação da Ifix sente quando a GPU cai.** Failover deve ser invisível para o cliente final — chamadas continuam respondendo dentro do SLO, mesmo durante incidentes ou picos de demanda.

## Requirements

### Validated

(None yet — ship to validate)

### Active

- [ ] Subir stack de IA na GPU primária (Vast.ai 4090): Qwen 3.5 27B (LLM, porta 8000), Whisper large-v3 (STT, porta 8001), BGE-M3 (embedding, porta 8002), todos com APIs compatíveis com formato OpenAI
- [ ] Gateway HTTP em Go que roteia requests para LLM, STT e embedding, com APIs OpenAI-compatíveis (`/v1/chat/completions`, `/v1/embeddings`, `/v1/audio/transcriptions`)
- [ ] Autenticação multi-tenant: API key por aplicação, com quotas e rate-limit individuais
- [ ] Health-check periódico nos três serviços + circuit breaker (abre quando N falhas seguidas ou latência acima de threshold)
- [ ] Failover automático para OpenRouter (Qwen 3.5 27B), OpenAI Whisper API e OpenAI text-embedding-3-small quando GPU primária cai
- [ ] Load shedding: detectar saturação por utilização de GPU/VRAM e desviar overflow para OpenRouter sem esperar falha real
- [ ] Spin-up emergencial paralelo de pod Vast.ai quando primária cai (auto, com guardrails: limite de preço/h e máximo 1 pod emergencial ativo)
- [ ] Cutback automático para primária quando ela voltar saudável por 5 min, com grace period de 5 min antes de desligar pod emergencial
- [ ] Modos de operação configuráveis por app: 24/7 (GPU sempre ligada) OU pico/vale (08–22h local, fora do horário roteia para OpenRouter)
- [ ] Dashboard próprio com métricas: latência, error rate, custo, requests por app, status do failover
- [ ] Alertas críticos via WhatsApp/email (queda da GPU, ativação do failover, spin-up emergencial, ultrapassagem de quota)
- [ ] Integração nas apps clientes v1: ConverseAI v4 (chat + agents), Chat Ifix (transcrição), Telefonia/NextBilling (transcrição), Cobranças, Campanhas, voice-api
- [ ] Persistência em Postgres compartilhado (Digital Ocean) para config, API keys, quotas, auditoria, billing
- [ ] Redis para estado quente: rate-limit, circuit breaker state, métricas curtas
- [ ] Deploy via Docker Compose em VPS dedicada (4 vCPU)

### Out of Scope

- TTS rodando em GPU — voice-api continua em CPU por ora; reconsiderar em milestone futura quando perfilar custo/benefício
- Modelos diferentes do Qwen 3.5 27B (Llama 3.3, Mixtral, etc.) — fixar Qwen para minimizar drift de comportamento entre primário e fallback OpenRouter
- ElevenLabs ou TTS premium — não está no escopo desta milestone
- Coqui XTTS-v2 ou voice cloning — descartado por consumir VRAM que comprometeria Qwen
- Kubernetes / Docker Swarm — Docker Compose simples atende a complexidade desta etapa
- Aprovação manual para spin-up emergencial — automatizado para garantir failover invisível
- Dashboards Grafana/Prometheus — dashboard próprio (Next.js) é suficiente para v1

## Context

**Ecossistema atual:**
- Empresa opera VPS dev (esta máquina, 178.156.150.21) e VPS prod (5.161.207.105), ambas via Portainer com deploy automático via webhook GitHub
- Várias aplicações já em produção consomem IA externa hoje (Anthropic, OpenAI direto): ConverseAI v4 (chat + agents Python), Chat Ifix (transcrição de áudios em conversas), Telefonia/NextBilling (transcrição de ligações), Cobranças/Campanhas (LLM para personalização), voice-api (TTS rodando em CPU hoje)
- Padrão da empresa: TypeScript + Bun para apps, Python para agents AI, Postgres + Redis para infra
- Postgres compartilhado em Digital Ocean já em uso pelas apps existentes

**Documento de partida:**
- `ConverseAI_GPU_Stack_Guide.docx` (raiz do projeto) detalha o setup base: stack Qwen + Whisper + BGE-M3 em RTX 4090, scripts de inicialização (llama.cpp server, FastAPI para Whisper/Embedding), comandos tmux, validação por curl, integração via env vars OpenAI-like. Este projeto **estende** esse setup adicionando: gateway Go multi-tenant, failover resiliente, spin-up emergencial, observabilidade central.

**Estimativas de VRAM (do doc):**
- Qwen 3.5 27B Q4_K_M: ~16 GB
- Whisper large-v3: ~3 GB
- BGE-M3: ~1 GB
- KV cache + overhead: ~2-3 GB
- Total: ~22-23 GB (margem 1-2 GB em RTX 4090 24 GB; `max_model_len=16384` no vLLM por segurança)

**Custos de referência (Abril 2026):**
- Vast.ai 4090: ~$0,35/h (~$84/mês 24/7)
- OpenRouter Qwen 3.5 27B: pay-per-token (custo variável conforme tráfego)
- OpenAI Whisper API: ~$0,006/min de áudio
- OpenAI text-embedding-3-small: $0,02/M tokens

**Justificativa Vast.ai como primária (não RunPod Secure):**
- Custo significativamente menor (~$84/mês vs ~$165/mês)
- Aceito porque: failover robusto + spin-up emergencial paralelo cobrem instabilidade do host privado típica da Vast.ai

## Constraints

- **Tech stack — Gateway**: Go — escolhido para performance de proxy alta e binário estático leve no deploy de 4 vCPU; difere do padrão TS da empresa, mas natural para gateway
- **Tech stack — IA**: Stack do doc (Qwen 3.5 27B Q4_K_M via llama-cpp-python, faster-whisper, sentence-transformers BGE-M3) — não trocar para minimizar drift
- **Tech stack — Persistência**: Postgres compartilhado Digital Ocean (schema dedicado) + Redis — reuso de infra
- **Tech stack — Deploy**: Docker Compose + Portainer com webhook GitHub — segue padrão converseai-v4
- **Hardware**: RTX 4090 24 GB — margem mínima de VRAM com 3 modelos; TTS na GPU está fora de escopo por isso
- **Compatibilidade**: APIs do gateway devem ser OpenAI-compatible (`/v1/chat/completions`, `/v1/embeddings`, `/v1/audio/transcriptions`) para que apps clientes só troquem `base_url` + `api_key`
- **Failover invisível**: requests não devem falhar para o cliente final; degradar latência é aceitável, perder request não
- **Multi-tenant**: cada app autentica com API key própria; quotas e contabilização de custo separadas por app
- **Guardrails operacionais**: max preço/hora Vast.ai (ex: $0,40/h), máximo 1 pod emergencial ativo simultâneo, alerta diário de uso acumulado
- **Auto-shutdown**: pod emergencial desliga sozinho após primário ficar saudável 5 min + 5 min de grace period

## Key Decisions

| Decision | Rationale | Outcome |
|----------|-----------|---------|
| Go como linguagem do gateway | Performance, binário estático, baixo overhead em VPS 4 vCPU | — Pending |
| Vast.ai como GPU primária (não RunPod Secure) | Custo ~50% menor; failover paralelo cobre instabilidade | — Pending |
| Qwen 3.5 27B fixo (LLM primário e fallback OpenRouter) | Mesmo modelo evita drift de comportamento durante failover | — Pending |
| TTS continua em CPU (voice-api) | VRAM da 4090 já apertada com LLM+STT+Embed; reavaliar em milestone futura | — Pending |
| Detecção de saturação por GPU util/VRAM (não queue depth) | Mais simples, sinal direto, não exige instrumentação fina dos servidores de modelo | — Pending |
| Postgres compartilhado Digital Ocean | Reuso de infra existente; schema dedicado para isolar | — Pending |
| Spin-up emergencial automático com guardrails (não aprovação manual) | Failover invisível exige autonomia; cap de preço/h e 1-pod-ativo previnem desperdício | — Pending |
| Dashboard próprio Next.js + WhatsApp/email (não Grafana) | Menos infra para manter; alertas críticos chegam onde a equipe está | — Pending |

## Evolution

This document evolves at phase transitions and milestone boundaries.

**After each phase transition** (via `/gsd-transition`):
1. Requirements invalidated? → Move to Out of Scope with reason
2. Requirements validated? → Move to Validated with phase reference
3. New requirements emerged? → Add to Active
4. Decisions to log? → Add to Key Decisions
5. "What This Is" still accurate? → Update if drifted

**After each milestone** (via `/gsd-complete-milestone`):
1. Full review of all sections
2. Core Value check — still the right priority?
3. Audit Out of Scope — reasons still valid?
4. Update Context with current state

---
*Last updated: 2026-04-17 after initialization*
