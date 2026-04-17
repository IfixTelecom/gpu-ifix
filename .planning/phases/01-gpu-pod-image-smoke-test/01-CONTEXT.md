# Phase 1: GPU Pod Image & Smoke-Test - Context

**Gathered:** 2026-04-17
**Status:** Ready for planning

<domain>
## Phase Boundary

Entrega uma imagem Docker pré-construída e validada (`ghcr.io/ifixtelecom/ifix-ai-pod`) contendo os três servidores de inferência (llama.cpp server-cuda, Speaches, Infinity) + dcgm-exporter + health-bridge Go, com cold-start ≤5 min em uma Vast.ai RTX 4090 e ≥3 GB de headroom de VRAM sob carga realista.

Fora de escopo desta phase: gateway Go (Phase 2), auth multi-tenant (Phase 2), failover/circuit breaker (Phase 3), auto-provisioning de pod emergencial (Phase 6), dashboard (Phase 7), integrações de apps cliente (Phases 8-9).

</domain>

<decisions>
## Implementation Decisions

### Weights distribution strategy

- **D-01:** Imagem Docker magra (~2 GB) publicada em `ghcr.io/ifixtelecom/ifix-ai-pod`, contendo apenas binários (llama.cpp server-cuda, Speaches, Infinity, dcgm-exporter, health-bridge Go). Weights NÃO são embutidos na imagem.
- **D-02:** Weights (~20 GB — Qwen 3.5 27B Q4_K_M GGUF, Whisper large-v3, BGE-M3) ficam no **MinIO self-hosted da Ifix**. Requisitos confirmados pelo usuário: VPS dedicada (separada da VPS do gateway), endpoint público HTTPS, throughput ≥90 Mbps sustentada.
- **D-03:** Script `onstart` do pod Vast.ai baixa weights do MinIO em paralelo com startup dos serviços; health-bridge só responde "pronto" após todos os 3 weights estarem em disco e servers responderem probe real.
- **D-04:** Alvo de cold-start: pull imagem (1-2 min) + download weights (2-3 min em paralelo com startup) = **3-5 min total até aceitar tráfego**.
- **D-05:** Checksum (SHA-256) validado após download de cada weight; falha de checksum aborta startup e pod não entra em rotação.
- **D-06:** URLs dos weights versionadas (ex: `s3://ifix-ai-weights/qwen3.5-27b-Q4_K_M/v1.0.0/model.gguf`) — permite rollback de weights independente da imagem.

### Qwen configuration (starting point for smoke-test)

- **D-07:** Modelo: `unsloth/Qwen3.5-27B-GGUF` quantização **Q4_K_M** (doc default, paridade com OpenRouter FP16 para minimizar drift durante failover).
- **D-08:** Parâmetros llama.cpp server: `-np 2` (2 chats concorrentes), `--ctx-size 16384` (POD-06 enforçado), `--jinja --chat-template-file <patched-qwen3.5-template>` para tool-calling.
- **D-09:** Fallback documentado: se smoke-test (POD-07) mostrar VRAM > 21 GB sustentado, apertar `--ctx-size` para 12288 e re-testar. Se ainda não couber, reduzir para `-np 1` (último recurso — documentado como "não aceitável, revisar modelo").

### Health-bridge design

- **D-10:** Container dedicado no docker-compose do pod, binário Go único exposto na porta **9100**.
- **D-11:** Probes internos a cada 10s (dentro do pod, não atravessa internet): `POST /v1/chat/completions` trivial no Qwen, `POST /v1/audio/transcriptions` com áudio sintético curto no Whisper, `POST /v1/embeddings` no Infinity. Cronometra latency real.
- **D-12:** Expõe endpoints: `GET /health/llm`, `GET /health/stt`, `GET /health/embed`, `GET /health` (agregado). Payload JSON com `{status: healthy|degraded|failed, latency_ms: N, last_probe: timestamp, error?: string}`.
- **D-13:** Structs de request/response OpenAI-compat **compartilhadas** com o gateway Go (Phase 2) — mesmo módulo no repo (monorepo Go).

### Qwen tool-calling template

- **D-14:** Adota template comunitário patched (gist sudoingX) salvo no repo em `pod/templates/qwen3.5-27b-tool-calling.jinja`. Carregado via `--chat-template-file`.
- **D-15:** Validação no smoke-test: request sintético com função `get_weather` exige tool-call bem formado (shape OpenAI). Falha no shape = smoke-test falha = imagem não é publicada como tag estável.
- **D-16:** Revisão upstream planejada: checar repo oficial Qwen/unsloth a cada release major; migrar para template stock se upstream consertar o bug de role `developer`.

### Smoke-test (POD-07)

- **D-17:** Script Python asyncio (`pod/smoke/smoke.py`) versionado no repo. Executa paralelo:
  - 2 chats concorrentes: cada um envia prompt de ~8k tokens, espera resposta de ~500 tokens
  - 1 transcrição Whisper: áudio sintético de 8+ minutos
  - 1 batch de 10 embeddings simultâneo
  - Coleta métricas dcgm-exporter a cada 1s durante toda execução
- **D-18:** Saída estruturada: `smoke-report.json` com `{vram_peak_gb, vram_p95_gb, llm_p95_ttft_ms, llm_p95_tpot_ms, whisper_latency_s, embed_p95_ms, tool_call_valid: bool, errors: []}`. Artefato versionado por commit.
- **D-19:** Gates de sucesso do smoke-test (bloqueantes — imagem não vira tag estável se qualquer um falhar):
  - `vram_peak_gb ≤ 21.0`
  - `tool_call_valid == true`
  - Zero `errors` do tipo OOM/CUDA/crash
  - LLM `p95_ttft_ms ≤ 3000` (sanidade; tuning real em Phase 5)
- **D-20:** Dados do smoke-test **alimentam Phase 5** (saturation thresholds reais, não chutes). Arquivado em `.planning/phases/01-.../baseline/smoke-report-vX.Y.Z.json`.

### Pipeline CI

- **D-21:** GitHub Actions workflow `.github/workflows/build-pod.yml`: trigger em `push` para `main`/`develop`, builda `docker build --platform linux/amd64` com layers cached, push `ghcr.io/ifixtelecom/ifix-ai-pod:{branch}-{sha}` + tag `:latest-dev` / `:latest`.
- **D-22:** Workflow separado `.github/workflows/smoke.yml`: trigger **manual** (`workflow_dispatch`), cria pod Vast.ai temporário via REST API (job dedicado com credenciais no GH Secrets), executa `smoke.py`, coleta `smoke-report.json`, destrói pod, publica artifact. Custo alvo: ≤$0,25 por run (~30 min em 4090 a $0,35/h).
- **D-23:** Uma tag estável (`ghcr.io/ifixtelecom/ifix-ai-pod:v1.0.0`) só é criada após smoke-test passar em um run manual com todos os gates D-19 verdes. Promoção manual via `git tag` disparando workflow de release.

### Inference server choices (already locked by research)

- **D-24:** LLM: `llama.cpp` binário nativo `ghcr.io/ggml-org/llama.cpp:server-cuda` (NÃO `llama-cpp-python`).
- **D-25:** STT: `ghcr.io/speaches-ai/speaches` (NÃO `faster-whisper` + FastAPI custom).
- **D-26:** Embedding: `michaelf34/infinity` (NÃO `sentence-transformers`).
- **D-27:** GPU metrics: `nvcr.io/nvidia/k8s/dcgm-exporter:latest-ubuntu22.04` na porta 9400.

### Claude's Discretion

- Versão específica de CUDA e imagem base (escolher compatível com 4090 driver padrão Vast.ai — pesquisa aponta CUDA 12.x)
- Estrutura interna do repo (monorepo? `pod/` + `gateway/` + `dashboard/` separados?)
- Formato exato do `smoke-report.json` além dos campos mandatórios em D-18
- Exato limite de concorrência do Whisper no health-bridge (1 probe a cada 10s é conservador)
- Valores exatos de `MaxIdleConns`/`IdleConnTimeout` no health-bridge HTTP client
- Estratégia de upload inicial dos weights para MinIO (script one-shot a ser documentado)

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Project docs (internal)

- `.planning/PROJECT.md` — Project vision, constraints, Core Value ("failover invisível"), decisions table
- `.planning/REQUIREMENTS.md` §Infra — Inference Pod — Requirements POD-01 to POD-07 (source of truth for phase scope)
- `.planning/ROADMAP.md` §Phase 1 — Goal, Depends-on, Success Criteria
- `.planning/STATE.md` — Prior locked decisions (stack, Go language, Vast.ai primária, Qwen fixed)

### Research bundle (internal)

- `.planning/research/SUMMARY.md` — Executive summary, ajustes ao PROJECT.md (Qwen via llama.cpp nativo, Speaches, Infinity)
- `.planning/research/STACK.md` §Inference Servers on the GPU Pod — VRAM math, vLLM rejeitado, template gotchas Qwen 3.5
- `.planning/research/STACK.md` §Health-Checking the GPU Pod — probe priorities, circuit breaker state transitions
- `.planning/research/PITFALLS.md` §Pitfall 1 — VRAM budget collapse under concurrent load (informa gate D-19 vram_peak_gb ≤ 21)
- `.planning/research/PITFALLS.md` §Pitfall 4 — Vast.ai cold pulls (informa decisão D-01 a D-06 de hybrid weights)
- `.planning/research/PITFALLS.md` §Pitfall 6 — Tokenizer/context drift local vs OpenRouter (informa D-08 ctx=16384)

### External reference (project root)

- `ConverseAI_GPU_Stack_Guide.docx` — Documento de partida da Ifix com stack base Qwen + Whisper + BGE-M3 em RTX 4090. Este projeto estende esse setup.

### Upstream components (HIGH confidence)

- https://github.com/ggml-org/llama.cpp — llama-server CUDA binary, tool-calling flags (`--jinja`, `--chat-template-file`)
- https://huggingface.co/unsloth/Qwen3.5-27B-GGUF — Quant Q4_K_M (community standard, templates patched first)
- https://github.com/speaches-ai/speaches — Speaches Whisper server (OpenAI-compatible nativo)
- https://github.com/michaelfeil/infinity — Infinity embedding server (BGE-M3 dense mode)
- https://github.com/NVIDIA/dcgm-exporter — GPU metrics export para Prometheus (porta 9400)
- https://docs.vast.ai/api-reference/introduction — Vast.ai REST API (usada no workflow smoke.yml)
- https://docs.vast.ai/quickstart — Cold-pull timing 10-60 min (justifica hybrid weights)

### Community patches (MEDIUM confidence — VALIDATE in smoke-test)

- https://gist.github.com/sudoingX/c2facf7d8f7608c65c1024ef3b22d431 — Qwen 3.5 tool-calling Jinja template patched (gate D-15 valida)

### Environment / Infra

- Ifix MinIO — endpoint público HTTPS (URL exata a ser confirmada pelo usuário na Phase 1 execução); VPS separada do gateway; throughput ≥90 Mbps
- Ifix GitHub org — `ghcr.io/ifixtelecom/ifix-ai-pod` (novo repo/package)
- Ifix Portainer — NÃO aplicável a este pod (rodará em Vast.ai, não Portainer)

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets

- **Nenhum código do repositório gpu-ifix existe ainda** — projeto greenfield. Único artefato no diretório é `ConverseAI_GPU_Stack_Guide.docx` (documento de referência).
- Padrão Ifix de GH Actions + webhook Portainer existe em `converseai-v4` — mas **não aplicável aqui** porque o pod Vast.ai não é deployado via Portainer. O workflow de build é inspirado no padrão, o de smoke é novo.
- Padrão Ifix de uso de Python asyncio existe em `converseai-v4/agents/` (FastAPI + LangGraph) — vocabulário de asyncio + httpx já familiar ao time.

### Established Patterns (Ifix-wide, apontado por CLAUDE.md)

- **Docker Compose para dev** (padrão em todos os repos Ifix) — vale para o pod (docker-compose.yml no pod dispara os 4 containers)
- **GitHub Actions build + push ghcr.io** com tag `{branch}-{sha}` — aplicar ao build-pod.yml
- **Sentry integration** (padrão Ifix) — aplicar ao health-bridge Go e ao gateway (Phase 2). Phase 1 pode iniciar Sentry mas é opcional; erros do pod são mais visíveis via dcgm + logs
- **slog structured logging** em Go (Phase 2) — health-bridge já usa slog para consistência

### Integration Points

- **Gateway (Phase 2)** vai consumir: health-bridge `:9100/*`, llama.cpp `:8000`, Speaches `:8001`, Infinity `:8002`, dcgm-exporter `:9400`
- **Auto-provisioner (Phase 6)** vai consumir: mesma imagem `ghcr.io/ifixtelecom/ifix-ai-pod:{tag}`, depende de cold-start ≤5 min viável (gate desta phase)
- **Dashboard (Phase 7)** vai ler: dcgm-exporter via gateway (não direto)

</code_context>

<specifics>
## Specific Ideas

- **"Failover invisível" é o Core Value** — a escolha hybrid weights + MinIO Ifix é consequência direta. Se o pod emergencial da Phase 6 demora 30 min pra subir por imagem grande, o failover não é invisível, é degradado. Cada decisão desta phase foi filtrada por esse critério.
- **Usuário pediu "S3 da Ifix"** e confirmou que MinIO self-hosted Ifix atende os 3 requisitos críticos (VPS dedicada, endpoint público HTTPS, throughput ≥90 Mbps). Registrado como decisão informada apesar da recomendação inicial de Cloudflare R2 / DO Spaces — mitigação: acompanhar SLA real do MinIO nos primeiros 3 meses de operação e reconsiderar se houver incidente de indisponibilidade que afete spin-up emergencial.
- **Smoke-test é o gate da Phase 1, não cerimônia** — dados coletados alimentam thresholds reais da Phase 5 (saturation). Sem eles, Phase 5 seria chute.
- **Template tool-calling da comunidade** — se o smoke-test D-15 detectar que o gist não funciona com a versão atual do llama.cpp + GGUF Unsloth, escalar para fork próprio (Opção 3B-B) na fase execução. Não é decisão de CONTEXT.md, é fallback.

</specifics>

<deferred>
## Deferred Ideas

- **Fork próprio do template tool-calling** — só se o gist da comunidade falhar. Não escopar agora.
- **Multi-arch builds (arm64)** — descartado; Vast.ai só fornece GPUs x86_64.
- **Warm pool de pods Vast.ai pré-aquecidos** — reduziria cold-start, mas custa $0,35/h ocioso. Reconsiderar depois do dashboard da Phase 7 mostrar frequência real de failover.
- **Self-hosted dcgm-exporter alternative** (nvidia-smi polling via script) — research descartou, mas guardado como fallback se dcgm-exporter tiver problema de licença/CUDA mismatch.
- **Whisper large-v3-turbo** (faster mas ~1% pior em PT-BR) — reconsiderar se Phase 1 baseline mostrar Whisper p95 > 8s em áudios longos.
- **Imagem base custom** (derivada de vast-ai/base-image ao invés de nvidia/cuda) — só se o cold-pull real medido na Phase 1 estiver na casa dos 20+ min. Não otimizar prematuramente.

</deferred>

---

*Phase: 01-gpu-pod-image-smoke-test*
*Context gathered: 2026-04-17*
