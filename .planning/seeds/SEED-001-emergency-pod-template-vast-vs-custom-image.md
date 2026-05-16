---
id: SEED-001
status: dormant
planted: 2026-05-16
planted_during: v1.0 milestone, Phase 6 UAT live (lifecycle 33)
trigger_when: when revisiting Phase 6 emergency-pod cold-start time OR when iteration speed on pod/onstart.sh becomes blocker OR when starting Phase 10 production hardening (review cost/time of failover path)
scope: gateway/internal/emerg/reconciler, pod/onstart.sh, pod/Dockerfile, .github/workflows/build-pod.yml, gateway/.env.portainer.dev (EMERGENCY_POD_IMAGE_TAG)
---

# SEED-001 — Refator emergency-pod: template Vast.ai Ubuntu+CUDA vs custom image GHCR

## One-line summary

Avaliar trocar custom image `ghcr.io/ifixtelecom/ifix-ai-pod:latest-dev` por template Vast.ai Ubuntu+CUDA pré-cacheado, com onstart.sh baixando llama-server binário pré-built do GitHub release.

## Motivação

Custom image atual baked com CUDA + llama.cpp + onstart.sh. Cold-start path medido durante UAT live Phase 6:

- Pull image 2.3GB (cold ~3-5min em host fresh, cache hit ~30s-1min em host que já puxou nossa image)
- bootstrap.sh: `mc cp` Qwen 16GB MinIO (~2-5min em link ~1Gbps)
- llama-server load GGUF na VRAM 4090 (~2min)
- Total cold: **~7-12min**

Vast.ai tem templates Ubuntu+CUDA oficiais pré-cacheados em ~todos hosts da rede (pull ~30s consistente). Se onstart.sh baixar llama-server binário pré-built do `github.com/ggml-org/llama.cpp/releases` em vez de baked na image:

- Pull template (cached): ~30s
- Download llama-server binário (~50-80MB GitHub): ~10s
- Bootstrap weights MinIO (idêntico): ~2min
- llama-server load GPU: ~2min
- Total cold: **~5min**

Ganho estimado: **2-4min de cold-start failover** + iteração dev drasticamente mais rápida (sem rebuild image custom em cada mudança no onstart.sh).

## Trade-offs medidos durante UAT 2026-05-16

| Aspecto | Custom image GHCR (atual) | Template Vast + llama bin |
|---------|---------------------------|---------------------------|
| Pull cold (host fresh) | ~3-5min medido LC32 | hipótese ~30s |
| Pull cache-hit | ~1min medido LC33 | hipótese ~30s (idem) |
| Bootstrap weights | ~2-5min | idem |
| llama load | ~2min | idem |
| Iteration speed (mudou onstart) | rebuild image + push GHCR + Vast pull novo (~10-15min) | só edita script no repo + redeploy gateway (~1min) |
| Dependência runtime | self-contained na image | github.com/ggml-org/llama.cpp releases (SPOF) |
| Risco fallback | image baked | template + binário externo |

## Candidatos de template (a pesquisar)

- `nvidia/cuda:12.1-runtime-ubuntu22.04` (Docker Hub oficial NVIDIA)
- `nvidia/cuda:12.4-runtime-ubuntu22.04` (CUDA mais novo)
- `pytorch/pytorch:2.1.0-cuda12.1-cudnn8-runtime` (Docker Hub)
- "Vast.ai templates" oficiais listados no UI (precisa confirmar quais existem + estabilidade + cache hit rates reais)

## Pontos abertos pra discuss-phase

1. Qual template oficial cobre 4090 sm_89 compute capability sem retrabalho?
2. Llama.cpp binário pré-built tem quants Q4_K_M habilitados com CUDA support? Latest release `b6987` (2026-05) — confirmar via inspeção.
3. Versão CUDA do template precisa cobrir CUDA-X do binário llama (matriz de compat).
4. llama-server precisa rodar como PID 1 ou via supervisor. onstart.sh Vast roda com `exec` ou `&`?
5. Fallback strategy: se template falhar, voltar pra custom image? Manter Dockerfile + build-pod.yml CI como backup vs descontinuar.
6. Health-bridge sidecar: mantém image separada `ghcr.io/.../ifix-ai-pod-health-bridge` ou inline no onstart.sh?
7. Impacto em testes integration: precisam mockar template Vast em vez de assumir custom image?
8. Cache-hit rate empírico: Vast templates **realmente** cacheados em ~todo host 4090? Validar com `vast search offers --raw` + scan de `cached_images`.

## Risco

- Pior path failover se hipótese de cache-hit mal-medida: pull cold de template pode não ser tão rápido quanto esperado em hosts secundários.
- Dependência runtime `github.com/ggml-org/llama.cpp` releases — SPOF. Mitigação: mirror llama-bin no nosso MinIO + onstart.sh tenta primário (GitHub) com fallback MinIO.
- Build llama from source on-pod aumenta cold-start severamente (~10min de cmake). **Descartar essa opção** — só usar binário pré-built.

## Implementação esperada (rascunho)

1. **Spike paralelo**: pod manual via Vast UI com template Ubuntu+CUDA candidato, ssh in, baixa llama-bin GitHub, baixa Qwen MinIO, start llama-server, mede tempo total cold-start + bench inferência. Sem código no repo — só evidência empírica.
2. **Quick task implementação**: trocar `EMERGENCY_POD_IMAGE_TAG` default em `gateway/.env.portainer.dev` + reescrever `pod/onstart.sh` pra baixar llama-server binário do GitHub release (versão pinned com sha256 verificável). Adicionar fallback MinIO mirror.
3. **CI build-pod.yml**: marcar custom image como fallback/backup OU descontinuar totalmente (e remover de `gateway/internal/config/config.go` default `EmergencyPodImageTag`).
4. **Reconciler search-offers**: atualizar filter se template requer args específicos (storage size etc.) — pode precisar mais disco que custom image.
5. **Tests regression**: criar integration test simulando template Vast (mock Vast.ai server pra CreateInstance + verify image_uuid).

## Próxima sessão

`/gsd-discuss-phase` puxa este seed como base. Surface assumptions:
- cache hit rates Vast.ai templates por classe de GPU
- CUDA version compatibility com llama.cpp release
- llama-server binário disponibilidade + sha256 pinning

Then `/gsd-plan-phase` cria plan de implementação.

## Breadcrumbs

- `gateway/internal/config/config.go:243` — `EmergencyPodImageTag` default = `"v1.0"` (env `EMERGENCY_POD_IMAGE_TAG`)
- `gateway/.env.portainer.dev:34` — atual `EMERGENCY_POD_IMAGE_TAG=latest-dev`
- `pod/onstart.sh` — bootstrap script atual
- `pod/Dockerfile` — Dockerfile custom image
- `.github/workflows/build-pod.yml` — CI build/push
- STATE.md tech debt items #2 (GPU error detection) + #4 (recovery FSM reset) + #5 (mirror stale) — mesmo subsistema, considerar fix conjunto
- Quick task `260516-rym` — fix handleForceProvision cooldown bug (relacionado, mesmo UAT)
