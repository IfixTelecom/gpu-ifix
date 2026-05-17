---
status: pending
phase: 06-emergency-pod-template-refactor
plan: 06-06
source: [06-CONTEXT.md, 06-WAVE0-GATES.md, 06-SPIKE-runtype-args.md, 06.5-HUMAN-UAT.md]
started: 2026-05-16
estimated_total_cost_brl: "3-10"
operator: ___________
date_executed: ___________
final_status: pending  # pass | partial | fail
gate: blocking-burnt-bridge-mitigation  # 3/3 GREEN required before PR2 plan 06-07
---

# Phase 6 — HUMAN-UAT: 3 lifecycles LIVE Vast.ai (Strategy B)

Este documento dirige a UAT live, executada pelo operator (Pedro), que valida
o refactor Strategy B end-to-end antes do PR2 (`06-07-PLAN.md` — delete custom
image GHCR + Dockerfile + workflow). É o **gate burnt-bridge mitigation** de
CONTEXT.md D-08-B-risk.

A UAT consome credit real Vast.ai (estimado R$3-10) e exercita o stack inteiro:
gateway server + reconciler + leader election + Vast.ai REST API + Postgres
audit + Sentry breadcrumbs — porém agora com a image upstream
`ghcr.io/ggml-org/llama.cpp:server-cuda-b9128` + `Runtype=args` (não mais a
custom image GHCR + Runtype=ssh do bug STATE.md:85).

Runbook companheiro: [`gateway/docs/RUNBOOK-EMERGENCY-POD.md`](../../../gateway/docs/RUNBOOK-EMERGENCY-POD.md)
(seção "Deploy" — leia ANTES de iniciar qualquer lifecycle).

Spike empírico de validação: [`06-SPIKE-runtype-args.md`](./06-SPIKE-runtype-args.md)
(prova prévia de Strategy B viável + pattern `--entrypoint /bin/bash --args -c`).

---

## Pre-requisitos

Verificar TODAS as linhas abaixo ANTES de iniciar Lifecycle 1. Se qualquer
linha falhar, NÃO prosseguir — fix antes.

- [ ] **Stack `ai-gateway-dev` deployed** no Portainer com image dev mais
      recente que inclui os commits Wave 0-3 (Plans 06-01..06-05):
      `ssh vps-ifix-vm 'docker ps --filter name=ai-gateway-dev --format "{{.Image}} {{.Status}}"'`
      mostra build pós-Wave-3 (Up healthy).
- [ ] **Env vars Strategy B presentes em Portainer stack** (substituem os
      antigos `EMERGENCY_POD_IMAGE_TAG=v1.0` e similares):

      | Env var                          | Valor (Wave-0 default)                                                                                              |
      | -------------------------------- | -------------------------------------------------------------------------------------------------------------------- |
      | `EMERGENCY_TEMPLATE_IMAGE`       | `ghcr.io/ggml-org/llama.cpp:server-cuda-b9128`                                                                       |
      | `EMERGENCY_JINJA_TEMPLATE_KEY`   | `emerg-onstart/templates/qwen3.5-27b-tool-calling-1067302cc6d927210a84775b9a060f724da15debc168c79710cbf763512e9f67.jinja` |
      | `EMERGENCY_JINJA_TEMPLATE_SHA256`| `1067302cc6d927210a84775b9a060f724da15debc168c79710cbf763512e9f67`                                                   |
      | `EMERGENCY_LLAMA_ARGS`           | (vazio — lifecycle.go usa hardcoded const)                                                                           |

      Confirmar no Portainer UI: stack → Editor → "Environment variables".
      Após edição, hit "Update the stack" para recriar container com novos vars.
- [ ] **Env vars Phase 6.5 herdados** (não devem ter sido apagados):
      `VAST_AI_API_KEY`, `VAST_PRICE_CAP_DPH=0.40`,
      `MONTHLY_EMERGENCY_BUDGET_BRL=200`, `USD_TO_BRL_RATE=5.0`,
      `PROVISION_TRIGGER_FAILED_OVER_SECONDS=120`,
      `PROVISION_HEALTHY_DURATION_SECONDS=300`,
      `PROVISION_IDLE_GRACE_SECONDS=300`,
      `PROVISION_COLDSTART_BUDGET_SECONDS=600`,
      `PRIMARY_HOST_ID=0`, `VAST_API_QPS_LIMIT=1`.
- [ ] **`EMERGENCY_POD_IMAGE_TAG` REMOVIDO** do stack (era Strategy A; agora
      ignorado). Confirmar: `ssh vps-ifix-vm 'docker exec ai-gateway-dev_gateway env | grep EMERGENCY_POD_IMAGE_TAG'`
      retorna vazio.
- [ ] **Boot logs confirmam Strategy B ativa:**
      `ssh vps-ifix-vm 'docker logs ai-gateway-dev_gateway --since 5m 2>&1 | grep -E "vast.Ping|emergency reconciler started|EMERGENCY_TEMPLATE_IMAGE"'`
      mostra `vast.Ping ok` + `Phase 6 emergency reconciler started` +
      log line confirmando `EmergencyTemplateImage=ghcr.io/ggml-org/llama.cpp:server-cuda-b9128`.
- [ ] **Saldo Vast.ai ≥ $5:** <https://cloud.vast.ai/billing/> mostra balance
      suficiente pros 3 lifecycles (~$1 base + ~$3 buffer pra retries/falhas).
- [ ] **vast CLI configurado em ops-claude (opcional, só pra debug):**
      `vastai show user` retorna account info OK.
- [ ] **Nenhum lifecycle live órfão:**
      `ssh vps-ifix-vm 'docker exec ai-gateway-dev_gateway /gatewayctl emerg lifecycles --since 24h --format=json | jq "[.[] | select(.EndedAt.Valid == false)] | length"'`
      retorna `0`. Se `1+`, rodar `force-destroy` ou investigar via RUNBOOK.
- [ ] **MinIO Jinja key acessível:**
      `mc cat ifix/ai-gateway/emerg-onstart/templates/qwen3.5-27b-tool-calling-1067302cc6d927210a84775b9a060f724da15debc168c79710cbf763512e9f67.jinja | sha256sum`
      retorna `1067302cc6d927210a84775b9a060f724da15debc168c79710cbf763512e9f67`.
- [ ] **FSM at HEALTHY:**
      `ssh vps-ifix-vm 'docker exec ai-gateway-dev_gateway /gatewayctl emerg state --format=json'`
      retorna `{}` (empty mirror at boot) OR `{"state":"healthy",...}`.

> **Container name note:** o compose file não seta `container_name` explícito,
> então o Portainer stack-prefixed name é `ai-gateway-dev_gateway` ou
> `ai-gateway-dev-gateway-1` (depende da versão Compose). Substituir pelo
> nome real de `ssh vps-ifix-vm 'docker ps | grep gateway'` se os comandos
> abaixo retornarem 404.

---

## Lifecycle 1 — Cold-start happy path

**Pre-conditions:** FSM HEALTHY, sem lifecycle live (Pre-requisitos OK).

**Goal:** Provar que Strategy B chega `emergency_active` end-to-end com cold-start
P90 ≤ 6 min (Success Criteria SC-2). Resolve bug STATE.md:85 em produção.

### Steps

```bash
ssh vps-ifix-vm

# Capture timestamp de inicio
T0=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
echo "T0=$T0"

# Trigger force-provision com reason rastreável
docker exec ai-gateway-dev_gateway /gatewayctl emerg force-provision --reason "phase6_uat_l1_happy"

# Capture lifecycle_id retornado em logs (ou consultar via lifecycles --since 5m)
sleep 2
LC_ID=$(docker exec ai-gateway-dev_gateway /gatewayctl emerg lifecycles --since 5m --limit 1 --format=json | jq -r '.[0].ID')
echo "lifecycle_id=$LC_ID"

# Poll FSM state cada 30s ate EMERGENCY_ACTIVE OU timeout a 10min (SC-1 ceiling + buffer)
for i in {1..20}; do
  echo "=== iteration $i / $(date -u +"%H:%M:%SZ") ==="
  docker exec ai-gateway-dev_gateway /gatewayctl emerg state --format=json
  sleep 30
done

# Apos {"state":"emergency_active",...} com pod_url, capturar lifecycle row:
docker exec ai-gateway-dev_gateway /gatewayctl emerg lifecycles --since 30m --format=json | jq '.[0]'

# Smoke test: /v1/models retorna 200 + lista de modelos
POD_URL=$(docker exec ai-gateway-dev_gateway /gatewayctl emerg state --format=json | jq -r '.pod_url')
echo "pod_url=$POD_URL"
curl -sS -m 10 "$POD_URL/v1/models" | jq '.data[].id'

# Confirma Strategy B no payload Vast (NÃO ifix-ai-pod):
VAST_INST=$(docker exec ai-gateway-dev_gateway psql "$AI_GATEWAY_PG_DSN" -t -c \
  "SELECT vast_instance_id FROM ai_gateway.emergency_lifecycles WHERE id=$LC_ID;" | xargs)
echo "vast_instance_id=$VAST_INST"
# (Operator manual): https://cloud.vast.ai/instances/ → instance $VAST_INST → verificar:
#   - Image: ghcr.io/ggml-org/llama.cpp:server-cuda-b9128
#   - Runtype: args
#   - Disk: 40 GB

# Capture cold-start duration (started_at -> first health-pass event)
docker exec ai-gateway-dev_gateway psql "$AI_GATEWAY_PG_DSN" -c "
SELECT id,
       started_at,
       (SELECT (ev->>'ts')::timestamptz
          FROM jsonb_array_elements(events) ev
         WHERE ev->>'type' = 'healthy'
         ORDER BY (ev->>'ts')::timestamptz ASC
         LIMIT 1) AS first_healthy_at,
       accepted_dph
  FROM ai_gateway.emergency_lifecycles
 WHERE id=$LC_ID;
"

# Destroy manual para fechar lifecycle (não esperar cutback grace)
docker exec ai-gateway-dev_gateway /gatewayctl emerg force-destroy
sleep 10
docker exec ai-gateway-dev_gateway /gatewayctl emerg state --format=json

# Confirma audit row fechada
docker exec ai-gateway-dev_gateway /gatewayctl emerg lifecycles --since 30m --format=json \
  | jq '.[0] | {id, started: .StartedAt, ended: .EndedAt, shutdown: .ShutdownReason, dph: .AcceptedDph, brl: .TotalCostBrl, instance: .VastInstanceID}'
```

### Acceptance criteria

- FSM transita `healthy → failed_over → emergency_provisioning → emergency_active`
  em ≤10 min após `force-provision` (SC-1 ceiling).
- **Cold-start ≤ 6 min** medido como (first_healthy_at − started_at) — SC-2 P90.
- `emerg state` mostra `state=emergency_active` com `pod_url` não-vazio,
  `pod_instance_id` populado, `lifecycle_id` populado.
- `curl $POD_URL/v1/models` retorna **HTTP 200** com array `data[]` listando
  pelo menos 1 modelo Qwen.
- DB lifecycle row tem `vast_instance_id IS NOT NULL`, `accepted_dph ≤ 0.4001`,
  `trigger_reason='manual_force'`, `events` JSONB com entradas
  `offer_accepted` + `healthy`.
- Vast.ai dashboard (browser) confirma instance criada com **image
  `ghcr.io/ggml-org/llama.cpp:server-cuda-b9128`** + **Runtype `args`** —
  NÃO o antigo `ghcr.io/ifixtelecom/ifix-ai-pod`.
- Após `force-destroy`: FSM em `cooldown` ou `healthy`, audit row com
  `shutdown_reason='manual'`, instance gone in Vast UI dentro de 30s.
- **Bug STATE.md:85 RESOLVIDO em live:** o pod NÃO trava em
  `health_timeout 1800s` (sintoma do CMD-ignore bug). Hits `emergency_active`
  antes do `PROVISION_COLDSTART_BUDGET_SECONDS=600` cap.
- Sem Sentry events com level Error (warnings OK).

### Sign-off

| Field                                      | Operator fills                                |
| ------------------------------------------ | --------------------------------------------- |
| Start (T0)                                 | `___________________`                         |
| End (force-destroy completed)              | `___________________`                         |
| Duration total (force-provision → destroy) | `___________________`                         |
| Cold-start (started_at → first_healthy_at) | `___________________`                         |
| Cost USD                                   | `___________________`                         |
| Cost BRL (audit)                           | `___________________`                         |
| `vast_instance_id`                         | `___________________`                         |
| `shutdown_reason`                          | `___________________`                         |
| Sentry events (count + level)              | `___________________`                         |
| Verdict                                    | [ ] GREEN  [ ] RED                            |
| Operator notes                             | `___________________________________________` |

---

## Lifecycle 2 — Bid race retry / price-cap pressure

**Pre-conditions:** Lifecycle 1 fechado com `shutdown_reason='manual'`,
FSM at HEALTHY/COOLDOWN concluído.

**Goal:** Provar que retry logic + audit `shutdown_reason` funcionam quando
nenhuma oferta Vast aceita o bid (bid race exhaustion). Strategy B não muda
o reconciler — então comportamento deve ser idêntico ao Phase 6.5 D-A3
(retry exponential backoff 3x → close com `offer_race_lost`).

### Steps

```bash
ssh vps-ifix-vm

# (Portainer UI) — editar stack ai-gateway-dev:
#   VAST_PRICE_CAP_DPH=0.05   # artificialmente baixo, vai forçar offer rejection
# Hit "Update the stack" → container recreate.
# Aguardar boot + reconciler started (~30s).

# Confirmar novo cap aplicado:
ssh vps-ifix-vm 'docker exec ai-gateway-dev_gateway env | grep VAST_PRICE_CAP_DPH'
# Esperar VAST_PRICE_CAP_DPH=0.05

# Capture T0
T0=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
echo "T0=$T0"

# Trigger force-provision
docker exec ai-gateway-dev_gateway /gatewayctl emerg force-provision --reason "phase6_uat_l2_price_cap"

# Captura lifecycle_id
sleep 2
LC_ID=$(docker exec ai-gateway-dev_gateway /gatewayctl emerg lifecycles --since 5m --limit 1 --format=json | jq -r '.[0].ID')
echo "lifecycle_id=$LC_ID"

# Poll FSM ate state mudar pra cooldown (após 3 retries exhausted)
for i in {1..15}; do
  echo "=== iteration $i / $(date -u +"%H:%M:%SZ") ==="
  docker exec ai-gateway-dev_gateway /gatewayctl emerg state --format=json
  sleep 20
done

# Apos lifecycle close: verificar shutdown_reason
docker exec ai-gateway-dev_gateway /gatewayctl emerg lifecycles --since 30m --format=json \
  | jq '.[0] | {id, started: .StartedAt, ended: .EndedAt, shutdown: .ShutdownReason, vast_instance: .VastInstanceID}'

# Verificar Sentry breadcrumb (browser: https://sentry.io → subsystem:emerg shutdown_reason:offer_race_lost)
# Capturar event URL no Sign-off

# Verificar zero leaked instance no Vast dashboard
# https://cloud.vast.ai/instances/
# Espera: nenhuma instance ativa (ou histórica recente em "destroyed" se o reconciler chegou a criar antes de cancelar)

# REVERTER price cap pra default:
#   VAST_PRICE_CAP_DPH=0.40
# (Portainer UI) → "Update the stack"
```

### Acceptance criteria

- Lifecycle fecha com `shutdown_reason ∈ {offer_race_lost, no_offers_below_cap}`.
- `vast_instance_id` pode ser NULL (se rejeição happened pré-CreateInstance)
  OU populado + Vast UI mostra instance destroyed (se CreateInstance
  succeeded mas post-create destroy correu por health/timeout).
- Sentry breadcrumb gravado com tag `shutdown_reason=offer_race_lost`.
- FSM retorna a `healthy`/`cooldown` em ≤ 5 min (NÃO hangup infinito).
- Reconciler NÃO continua tentando indefinidamente (3 retries com exp
  backoff → close).
- ZERO leaked instance no Vast.ai dashboard 60s após close.
- Após reverter cap pra 0.40, próximo `force-provision` (Lifecycle 3) deve
  funcionar normalmente.

### Sign-off

| Field                                      | Operator fills                                |
| ------------------------------------------ | --------------------------------------------- |
| Start (T0)                                 | `___________________`                         |
| End (lifecycle closed)                     | `___________________`                         |
| Duration                                   | `___________________`                         |
| Cost USD                                   | `___________________` (likely ~0)             |
| Cost BRL (audit)                           | `___________________`                         |
| `vast_instance_id`                         | `___________________` (may be NULL)           |
| `shutdown_reason`                          | `___________________`                         |
| Sentry event URL                           | `___________________`                         |
| `VAST_PRICE_CAP_DPH` revertido pra 0.40    | [ ] yes  [ ] no                               |
| Verdict                                    | [ ] GREEN  [ ] RED                            |
| Operator notes                             | `___________________________________________` |

---

## Lifecycle 3 — Cancel-in-flight (primary recovery simulation)

**Pre-conditions:** Lifecycle 2 fechado + `VAST_PRICE_CAP_DPH=0.40` revertido,
FSM HEALTHY.

**Goal:** Provar que triple-layer cancel (context + pubsub + post-create
destroy per Phase 6.5 D-C3) funciona com Strategy B. Pod NÃO deve continuar
billing após cancel.

### Steps

```bash
ssh vps-ifix-vm

# T0
T0=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
echo "T0=$T0"

# Trigger force-provision
docker exec ai-gateway-dev_gateway /gatewayctl emerg force-provision --reason "phase6_uat_l3_cancel"

# Captura lifecycle_id
sleep 2
LC_ID=$(docker exec ai-gateway-dev_gateway /gatewayctl emerg lifecycles --since 5m --limit 1 --format=json | jq -r '.[0].ID')
echo "lifecycle_id=$LC_ID"

# Aguardar FSM=emergency_provisioning + vast_instance_id populado em audit
# (i.e., bid foi accepted + CreateInstance chamado, mas pod ainda em cold-start)
for i in {1..10}; do
  STATE=$(docker exec ai-gateway-dev_gateway /gatewayctl emerg state --format=json | jq -r '.state // "unknown"')
  INST=$(docker exec ai-gateway-dev_gateway psql "$AI_GATEWAY_PG_DSN" -t -c \
    "SELECT vast_instance_id FROM ai_gateway.emergency_lifecycles WHERE id=$LC_ID;" | xargs)
  echo "$(date -u +"%H:%M:%SZ") state=$STATE vast_instance_id=$INST"
  if [[ "$STATE" == "emergency_provisioning" && -n "$INST" && "$INST" != "NULL" ]]; then
    echo "READY TO CANCEL — vast_instance_id=$INST"
    break
  fi
  sleep 15
done

# CRITICAL: cancel ENQUANTO em cold-start (ANTES de emergency_active)
# Opção A — publish fake primary recovery event (simulates Phase 3 breaker.CLOSED)
docker exec infra-redis-1 redis-cli -n 5 PUBLISH gw:upstreams:events \
  '{"upstream":"local-llm","state":"closed","timestamp":'"$(date +%s)"'}'

# Opção B (fallback se A não cancelar — bug): force-destroy direto
# docker exec ai-gateway-dev_gateway /gatewayctl emerg force-destroy

# Aguardar cancel propagation
sleep 30

# Confirma FSM back to healthy/cooldown
docker exec ai-gateway-dev_gateway /gatewayctl emerg state --format=json

# Confirma lifecycle row closed com shutdown_reason='cancelled_in_flight'
docker exec ai-gateway-dev_gateway /gatewayctl emerg lifecycles --since 5m --format=json \
  | jq '.[0] | {id, started: .StartedAt, ended: .EndedAt, shutdown: .ShutdownReason, instance_id: .VastInstanceID}'

# Manual: verificar Vast dashboard NÃO mostra instance ativa
# https://cloud.vast.ai/instances/
# Se vast_instance_id estava populado, deve aparecer em histórico como "destroyed"
# Se vast_instance_id era NULL (cancel pré-CreateInstance), nada no Vast.
```

### Acceptance criteria

- FSM volta a `healthy` (ou `cooldown` brevemente) ≤30s após cancel publish.
- Lifecycle row close com `shutdown_reason='cancelled_in_flight'`.
- **ZERO leaked instance no Vast.ai dashboard** (triple-layer cancel
  garante no-leak per D-C3):
  - Se cancelled ANTES de CreateInstance: `vast_instance_id` NULL.
  - Se cancelled APÓS CreateInstance: `vast_instance_id` populado +
    Vast dashboard mostra destroyed (histórico, não ativo).
- Pod NÃO continua billing após cancel — Vast bill mostra cobrança
  proporcional só ao tempo de cold-start ativo (típico ≤30s = ~$0.003).
- Strategy B inline-args entrypoint preserva cancel semantics (vs
  Strategy A SSH-injected onstart que podia sobreviver ao cancel).

### Sign-off

| Field                                      | Operator fills                                |
| ------------------------------------------ | --------------------------------------------- |
| Start (T0)                                 | `___________________`                         |
| Cancel published at                        | `___________________`                         |
| End (FSM back to healthy)                  | `___________________`                         |
| Duration                                   | `___________________`                         |
| Cost USD                                   | `___________________` (likely ≤ $0.01)        |
| Cost BRL (audit)                           | `___________________`                         |
| `vast_instance_id`                         | `___________________` (NULL or destroyed)     |
| `shutdown_reason`                          | `___________________`                         |
| Vast UI confirm no active instance         | [ ] yes  [ ] no                               |
| Sentry event URL                           | `___________________`                         |
| Verdict                                    | [ ] GREEN  [ ] RED                            |
| Operator notes                             | `___________________________________________` |

---

## Final Sign-off

| Lifecycle      | Verdict          | Cost BRL       | Notes                                  |
| -------------- | ---------------- | -------------- | -------------------------------------- |
| 1 — Happy path | [ ] G [ ] R      | ____           | Cold-start: ____ min                   |
| 2 — Price cap  | [ ] G [ ] R      | ____           | shutdown_reason: ____                  |
| 3 — Cancel     | [ ] G [ ] R      | ____           | leaked? ____                           |
| **TOTAL**      | **[ ] 3/3 GREEN** | **R$ ____**   | **[ ] PR2 (plan 06-07) APPROVED**      |

- **Operator:** ___________
- **Date:** ___________
- **Total Vast.ai cost (sum L1+L2+L3 USD):** $ ___________
- **Total Vast.ai cost BRL:** R$ ___________
- **Sentry events linked (L1+L2+L3):** ___________
- **Overall GO/NO-GO for PR2 plan 06-07 cleanup:** [ ] GO  [ ] NO-GO

---

## Failure handling

**Se qualquer lifecycle FAIL (1, 2 ou 3 com verdict RED):**

1. **NÃO prosseguir para PR2 (plan 06-07)** — burnt-bridge mitigation NÃO
   satisfeita; rollback via `EMERGENCY_TEMPLATE_IMAGE=<old ifix-ai-pod tag>`
   ainda viável apenas enquanto Dockerfile + CI workflow existirem.
2. Documentar o failure mode no `Notes` de cada lifecycle row acima.
3. Capturar:
   - Logs gateway: `ssh vps-ifix-vm 'docker logs ai-gateway-dev_gateway --since 30m > /tmp/uat-fail-logs.txt'`
   - Vast.ai logs: `vastai logs <instance_id>` (em ops-claude — Strategy B
     NÃO permite SSH; logs via vastai CLI são o canal de debug, ver
     RUNBOOK Troubleshooting "Pitfall 3").
   - Sentry events com filter `subsystem:emerg`.
   - Audit row completo:
     ```bash
     docker exec ai-gateway-dev_gateway psql "$AI_GATEWAY_PG_DSN" -c \
       "SELECT * FROM ai_gateway.emergency_lifecycles WHERE id=<lc_id>;" \
       > /tmp/uat-fail-audit.txt
     ```
4. Abrir `/gsd:plan-phase --gaps` no Claude com:
   - Reason: "Phase 6 UAT failure — lifecycle N RED"
   - Evidence: paths acima + Sentry URLs
   - Decision needed: fix-in-place (gap closure) vs revert PR1
     (`git revert <PR1 commits>`)
5. Após gap closure GREEN, re-rodar UAT desde Lifecycle 1.

---

## STATE.md Update (após 3/3 GREEN)

Após operator confirma 3/3 GREEN + commit deste arquivo preenchido,
adicionar bullet em `.planning/STATE.md` (seção "Tech Debt / Open Todos"
ou similar):

```markdown
- **2026-05-?? Phase 6 PR1 UAT GREEN 3/3** — STATE.md:85 bug runtype=ssh
  CMD-ignore RESOLVIDO em produção (Strategy B `Runtype=args` validado live).
  PR2 (plan 06-07) cleanup APPROVED para execução. Total cost UAT: R$ ___.
```

---

## References

- Phase 6 CONTEXT: [`06-CONTEXT.md`](./06-CONTEXT.md) (Strategy B Locked block)
- Phase 6 RESEARCH: [`06-RESEARCH.md`](./06-RESEARCH.md) (HF endorsement +
  upstream ggml-org image rationale)
- Wave 0 Spike: [`06-SPIKE-runtype-args.md`](./06-SPIKE-runtype-args.md)
  (empirical validation of `--entrypoint /bin/bash --args -c` pattern)
- Wave 0 Operator Gates: [`06-WAVE0-GATES.md`](./06-WAVE0-GATES.md)
  (Decision 1 B2-40GB + MinIO key + Decision 4 args revised pattern)
- Plan template: [`06-06-PLAN.md`](./06-06-PLAN.md)
- Phase 6.5 sibling UAT (6-scenario template adapted down to 3):
  [`06.5-HUMAN-UAT.md`](../06.5-auto-provisioning-emergency-pod-vast-ai/06.5-HUMAN-UAT.md)
- Runbook: [`gateway/docs/RUNBOOK-EMERGENCY-POD.md`](../../../gateway/docs/RUNBOOK-EMERGENCY-POD.md)
- PR2 plan (BLOCKED by this UAT): [`06-07-PLAN.md`](./06-07-PLAN.md)
- Bug context: STATE.md:85 (runtype=ssh CMD-ignore — root cause this phase resolves)
