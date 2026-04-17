# Phase 1: GPU Pod Image & Smoke-Test - Discussion Log

> **Audit trail only.** Do not use as input to planning, research, or execution agents.
> Decisions are captured in `01-CONTEXT.md` — this log preserves the alternatives considered.

**Date:** 2026-04-17
**Phase:** 01-gpu-pod-image-smoke-test
**Areas discussed:** Weights strategy, Qwen quantization/concurrency, Health-bridge + tool-calling template, Smoke-test + CI pipeline

---

## Weights Strategy

### Question 1: Qual estratégia de weights você escolhe para o pod?

| Option | Description | Selected |
|--------|-------------|----------|
| Hybrid: imagem magra + R2 (Recomendado) | Imagem ~2GB com binários, weights baixados de R2/DO Spaces no onstart. Cold-start 3-5min. Viável para Phase 6 emergencial. Ligeiramente mais complexo (script + checksum). | ✓ |
| Baked: tudo embutido na imagem | Imagem ~20GB com weights dentro. 1 artefato único, sem dependência externa. Cold-start 10-60min na Vast.ai. Pode comprometer Phase 6 (spin-up em 5min). | |
| Split layered: weights em layer separada | Imagem de 20GB mas organizada em layers (base + weights). Reutiliza cache quando host Vast.ai já pegou a layer antes. Imprevisível: depende do cache do host. | |

**User's choice:** Hybrid (imagem magra + bucket externo) — com pergunta de follow-up: "imagem no s3 da ifix. isso muda algo?"

### Question 2: Onde guardar os weights (~20GB)?

| Option | Description | Selected |
|--------|-------------|----------|
| Cloudflare R2 (Recomendado inicial) | Egress zero, SLA 99.9%, edge global, funciona em qualquer região Vast.ai. Precisa criar conta/bucket novo. | |
| DigitalOcean Spaces | Ifix já tem conta DO. Custo similar. Egress cobrado. Região única — latency maior se pod estiver em outra região. | |
| MinIO self-hosted | Infra Ifix existente, zero custo adicional. Risco: se VPS MinIO cair, pod emergencial não sobe. | ✓ |

**User's choice:** MinIO self-hosted. Follow-up do Claude: validação de 3 requisitos críticos (VPS separada do gateway, endpoint público HTTPS, throughput ≥90 Mbps).

**User's confirmation:** "nosso minio tem todos os requisistos."

**Notes:** Decisão registrada como informada. Mitigação planejada: acompanhar SLA real do MinIO Ifix nos primeiros 3 meses; se indisponibilidade afetar spin-up emergencial da Phase 6, reconsiderar migração para R2/DO Spaces especificamente para weights.

---

## Qwen Quantization + Concurrency

### Question: Qual configuração inicial de Qwen (ponto de partida para smoke-test)?

| Option | Description | Selected |
|--------|-------------|----------|
| Q4_K_M + np=2 + ctx=16384 (Recomendado) | Doc default. 2 chats concorrentes com 16k tokens. Paridade de qualidade com OpenRouter (FP16). Smoke-test valida VRAM ≤21GB; fallback apertar ctx para 12288. | ✓ |
| Q4_K_M + np=2 + ctx=8192 | Conservador. 5GB headroom VRAM, muito seguro. Perde chats longos. | |
| Q5_K_M + np=2 + ctx=8192 | Qualidade um pouco melhor. Ctx reduzido para caber. Comunidade menor — patches chegam mais tarde. | |

**User's choice:** Q4_K_M + np=2 + ctx=16384 (Recomendado)

**Notes:** Smoke-test POD-07 mede VRAM empírica. Fallback documentado em D-09: se VRAM > 21GB, apertar ctx para 12288; última tentativa reduzir np para 1 com flag de alerta "não aceitável, revisar modelo".

---

## Health-bridge + Tool-calling Template

### Question A: Health-bridge do pod (probe por modelo, porta 9100)

| Option | Description | Selected |
|--------|-------------|----------|
| Go micro-binary (Recomendado) | Binário Go na porta 9100, probes internos a cada 10s com latência. Consistente com gateway, ~300 linhas, structs OpenAI-compat reutilizáveis na Phase 2. | ✓ |
| Python shim (FastAPI) | Script Python ~50 linhas. Mais familiar, menor código. Traz dependência Python (~80MB RAM extra). | |
| Gateway consulta direto (sem bridge) | Pod fica 'burro'. Gateway bate em cada servidor pela internet. Menos código, mais latência de probe, mais superfície de firewall exposta. | |

**User's choice:** Go micro-binary (Recomendado)

### Question B: Template tool-calling do Qwen 3.5 27B

| Option | Description | Selected |
|--------|-------------|----------|
| Template da comunidade + validar (Recomendado) | Baixar gist sudoingX, salvar no repo, usar com --chat-template-file, testar no smoke-test. Baixo risco, manutenção mínima. | ✓ |
| Fork próprio | Copiar template e manter no repo com patches próprios. Mais controle; mais overhead. | |
| Template stock (aceitar bug) | Sem patch. Tool-calling vai falhar no role 'developer'. Não recomendado. | |

**User's choice:** Template da comunidade + validar (Recomendado)

**Notes:** Fallback registrado em specifics: se smoke-test (D-15) detectar que gist não funciona com versão atual do llama.cpp + GGUF Unsloth, escalar para fork próprio (Opção B) na fase de execução.

---

## Smoke-test + CI Pipeline

### Question A: Smoke-test — como validar o pod sob carga (POD-07)?

| Option | Description | Selected |
|--------|-------------|----------|
| Scripted Python asyncio (Recomendado) | smoke.py versionado. 2 chats async + Whisper + dcgm 1Hz. Saída smoke-report.json. Dados viram baseline para Phase 5 thresholds. | ✓ |
| k6 ou Locust | Ferramenta profissional. Relatório HTML. Mais cerimônia; parser extra pra integrar com Phase 5. | |
| Manual documentado (markdown + curl) | Operador executa passos. Rápido; não reprodutível; sem dados estruturados. | |

**User's choice:** Scripted Python asyncio (Recomendado)

### Question B: Pipeline CI — build e publish da imagem do pod

| Option | Description | Selected |
|--------|-------------|----------|
| GH Actions auto + ghcr.io + smoke manual (Recomendado) | Padrão Ifix. Build automático a cada push; tag SHA+branch; push ghcr.io. Smoke-test trigger manual (~$0,17 por run). | ✓ |
| Build local + manual push | Dev builda e pusha na mão. Zero CI. Rápido no começo; não rastreável. | |
| Build na VPS dev + push | Esta VPS não tem GPU — só valida docker build. Equivalente ao A em valor, menos automação. | |

**User's choice:** GH Actions auto + ghcr.io + smoke manual (Recomendado)

**Notes:** Smoke-test roda em Vast.ai real (único jeito de medir VRAM de verdade). Trigger manual porque cada run custa ~$0,17 e 30 min — não faz sentido rodar em todo PR.

---

## Claude's Discretion

Áreas onde o usuário deixou flexibilidade para Claude decidir durante planejamento/execução:

- Versão específica de CUDA e imagem base
- Estrutura interna do repo (monorepo vs multi-repo)
- Formato exato do smoke-report.json além dos campos mandatórios (D-18)
- Limite de concorrência do Whisper no health-bridge (1 probe/10s é conservador)
- Valores exatos de configuração do HTTP client Go
- Estratégia de upload inicial dos weights para MinIO

## Deferred Ideas (full list in CONTEXT.md)

- Fork próprio do template tool-calling
- Multi-arch builds (arm64)
- Warm pool de pods Vast.ai pré-aquecidos
- Self-hosted dcgm-exporter alternative
- Whisper large-v3-turbo
- Imagem base custom (derivada de vast-ai/base-image)

---

*Discussion log generated: 2026-04-17*
