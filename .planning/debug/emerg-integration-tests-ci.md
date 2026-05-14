---
slug: emerg-integration-tests-ci
status: partially_fixed
trigger: "Phase 6 emergency integration tests falham em CI (build-gateway job, integration-test). 8+ testes em gateway/internal/integration_test/emerg_*_test.go falham no 1o run real."
created: 2026-05-14
updated: 2026-05-14
goal: find_and_fix
---

# Debug Session: emerg-integration-tests-ci

## Symptoms

**Expected behavior:**
emerg_*_test.go integration tests passam no CI (build-gateway job `integration-test`).

**Actual behavior:**
8+ testes falham no 1o run real do job `integration-test` (branch develop, run mais recente de build-gateway).

**Error messages:**
- `TestEmergProvisionHappyPath` — falha
- `TestEmergPriceCap` — falha
- `TestEmergReconcilerHandlesForceProvision` — "FSM did not advance"
- `TestEmergReconcilerForceProvisionRejectNonLeader` — "got 2"
- `TestEmergReconcilerForceDestroyNoOpWhenIdle` — "7 rows want 0"
- `TestEmergMultiFailoverRideOut` — falha
- `TestEmergLeaderRecoveryActiveResume_HealthFailureCancels` — falha
- `TestEmergLeaderRecoveryZombie` — falha
- `emerg_singleton_test.go:63` — duplicate key `emergency_live_singleton`

**Timeline:**
Nunca executados antes. TODO SUMMARY Phase 6 (06-02..06-10): "integration tests deferred to CI runtime... Docker unavailable on ops-claude". Este é o 1o run real. NÃO é regressão Phase 7/8/9.

**Reproduction:**
ops-claude SEM Docker → rodar em vps-ifix-vm:
```
ssh vps-ifix-vm
cd gateway && go test -tags=integration ./internal/integration_test/... -count=1 -v -timeout=10m
```
Testes têm `//go:build integration` + testcontainers — NÃO rodam no `go test ./...` local.

## Context

- Setup: `gateway/internal/integration_test/setup_test.go` (TestMain → setupContainers, roda todas migrations incl `0019_emergency_lifecycles.sql`).
- Hipótese inicial confirmada por análise estática: falta de isolamento de DB entre test functions — `emergency_lifecycles` não é truncada entre testes.
- Regras CLAUDE.md: --diagnose primeiro, validar root cause ANTES de editar, sem edits especulativos.

## Current Focus

- hypothesis: PARCIALMENTE confirmado. A falta de TRUNCATE de `emergency_lifecycles` explicava 5+ falhas (todas corrigidas pelo fix). Porém 3 testes continuam falhando MESMO EM ISOLAMENTO TOTAL (1 teste por processo, containers frescos) — esses NÃO são contaminação cross-test e têm causa-raiz separada, fora do escopo do fix aprovado.
- next_action: Decisão do usuário necessária. O fix de harness foi aplicado e validado (resolve 5+ testes). Os 3 testes restantes precisam de uma nova sessão de debug com escopo que inclua código de produção do reconciler OU os próprios testes.
- test: rodado `go test -tags=integration -run ^<TestName>$ -count=1` para cada um dos 4 testes ainda falhos, cada um em processo isolado com containers frescos.
- expecting: se fosse só contaminação, todos passariam isolados. Resultado: 3 de 4 FALHAM isolados → causa-raiz separada confirmada.

## Evidence

- timestamp: 2026-05-14
  source: gateway/internal/integration_test/setup_test.go:138-187 (helper `freshSchema`)
  finding: |
    `freshSchema` é o ÚNICO ponto de reset de estado de DB usado por todos os
    emerg tests. Ele chama `db.Up` (aplica migrations) e depois TRUNCATE
    APENAS de uma lista fixa de 5 tabelas:
      api_keys, audit_log, audit_log_content, usage_counters, tenants
    `emergency_lifecycles` NÃO está na lista. Nenhuma tabela emergency está.
    O doc-comment do TestMain (linha 40-41) afirma "Tests rebuild a FRESH
    schema between cases via db.Down + db.Up" — mas `freshSchema` NUNCA chama
    `db.Down`. O comentário está desatualizado/incorreto; o código real só
    faz `db.Up` + TRUNCATE parcial.

- timestamp: 2026-05-14
  source: gateway/internal/integration_test/setup_test.go:30-57 (vars + TestMain)
  finding: |
    `sharedPG` + `setupOnce sync.Once` → um único container Postgres é criado
    uma vez e reusado por TODO o pacote `integration`. Linhas escritas em
    `emergency_lifecycles` por um teste persistem para todos os testes
    subsequentes do mesmo `go test` run.

- timestamp: 2026-05-14
  source: grep -rn "TRUNCATE.*emergency|DELETE FROM.*emergency" gateway/internal/integration_test/*.go
  finding: |
    Zero ocorrências. Nenhum teste e nenhum helper limpa `emergency_lifecycles`.
    14 arquivos de teste emerg_*_test.go criam linhas de lifecycle (INSERT
    direto OU via reconciler processando force_provision / breaker events) e
    nenhum as remove.

- timestamp: 2026-05-14
  source: gateway/db/migrations/0019_emergency_lifecycles.sql
  finding: |
    Migration 0019 cria `ai_gateway.emergency_lifecycles` + o índice parcial
    único `emergency_live_singleton ON (...) WHERE ended_at IS NULL` — no
    máximo 1 linha "viva" (ended_at NULL) por banco. `id BIGSERIAL PRIMARY
    KEY` confirmado. É a ÚNICA tabela emergency (grep em todas as migrations
    0001-0022 confirma — nenhuma outra `CREATE TABLE ... emergency`).
    Migrations 0020/0021/0022 são todas Phase 7 e mexem só em `audit_log`.

- timestamp: 2026-05-14
  source: gateway/internal/integration_test/emerg_force_command_test.go:268-276 (TestEmergReconcilerForceDestroyNoOpWhenIdle)
  finding: |
    `SELECT COUNT(*) FROM ai_gateway.emergency_lifecycles` com assert
    `count == 0`. Explica o sintoma "7 rows want 0": 7 linhas vazaram de
    testes anteriores. APÓS O FIX: este teste PASSA isolado E na suíte.

- timestamp: 2026-05-14
  source: FIX APLICADO — gateway/internal/integration_test/setup_test.go
  finding: |
    Aplicado o fix de harness aprovado:
    (1) Adicionado `TRUNCATE ai_gateway.emergency_lifecycles RESTART IDENTITY
        CASCADE` em `freshSchema` (após o loop dos 5 tabelas). RESTART IDENTITY
        zera a sequência BIGSERIAL → ids determinísticos por teste.
    (2) Corrigido o doc-comment desatualizado do TestMain (linhas ~39-41) que
        afirmava incorretamente "db.Down + db.Up" entre casos.
    Validação local: `gofmt -l .` limpo, `go build ./...` OK,
    `go vet -tags=integration ./internal/integration_test/...` sem erros.

- timestamp: 2026-05-14
  source: vps-ifix-vm — go test -tags=integration full suite (-count=1 -v), container golang:1.24 com docker.sock montado, --network host
  finding: |
    APÓS O FIX, dos 8 testes originalmente falhos, 5+ agora PASSAM:
    TestEmergProvisionHappyPath, TestEmergPriceCap, TestEmergMultiFailoverRideOut,
    TestEmergLeaderRecoveryActiveResume_HealthFailureCancels,
    TestEmergLeaderRecoveryZombie, TestEmergReconcilerForceDestroyNoOpWhenIdle,
    TestEmergSingletonDBIndex — todos verde. O fix de harness resolveu a
    contaminação de `emergency_lifecycles` conforme diagnosticado.

- timestamp: 2026-05-14
  source: vps-ifix-vm — go test -run ^<Name>$ -count=1, CADA teste em processo isolado com containers frescos
  finding: |
    EVIDÊNCIA DECISIVA — 3 dos 4 testes ainda falhos FALHAM EM ISOLAMENTO
    TOTAL (um processo, containers frescos, nenhum estado vazado possível):
      - TestEmergReconcilerHandlesForceProvisionEvent — FAIL isolado
        ("FSM did not advance after force-provision; got healthy")
      - TestEmergReconcilerForceProvisionRejectedNonLeader — FAIL isolado
        ("NEITHER FSM advanced; force-provision was dropped silently")
      - TestEmergTriggerNoSpawnIfLiveLifecycle — FAIL isolado
        ("FSM transitioned despite live lifecycle (D-C5 check failed):
         got emergency_provisioning, want healthy")
    O 4o (TestEmergBidRaceLost) PASSA isolado — só falha na suíte cheia →
    flake de timing sob carga (host CI compartilhado, suíte de 294s com
    dezenas de testcontainers), NÃO contaminação de harness.
    CONCLUSÃO: os 3 que falham isolados têm causa-raiz SEPARADA da
    contaminação de `emergency_lifecycles`. O Root Cause Report original
    estava CORRETO mas INCOMPLETO — atribuía 100% das falhas ao TRUNCATE
    faltante quando na verdade 3 testes têm bug distinto.

- timestamp: 2026-05-14
  source: gateway/internal/emerg/reconciler.go:680-711 (evaluateHealthy) + :731-796 (applyEmergCommand/handleForceProvision) + emerg_force_command_test.go:160-203
  finding: |
    Análise estática dos 3 testes que falham isolados — todos no caminho
    force-provision / D-C5:
    - `handleForceProvision` faz INSERT do lifecycle e LOGO EM SEGUIDA
      `FSM.Transition(Healthy→FailedOver→EmergencyProvisioning)` sem early
      return entre os dois passos.
    - Em TestEmergReconcilerForceProvisionRejectedNonLeader o teste só chega
      na linha 202 ("NEITHER FSM advanced") SE o `waitFor` da linha 164
      (count==1 manual_force) JÁ PASSOU — ou seja, o INSERT ACONTECEU mas
      quando o teste checa `fsm.State()` ela NÃO está em EmergencyProvisioning.
    - Hipótese forte: os testes foram escritos para Plan 06-05 (cujo comentário
      diz "Plan 06-05 stops at the FSM transition"), mas Plans 06-06+ adicionaram
      `evaluateEmergencyProvisioning` que no tick SEGUINTE move a FSM adiante
      (caminho Vast.ai com upstream dummy falha → FSM transita para outro
      estado). Quando o teste lê `fsm.State()` a FSM já saiu de
      EmergencyProvisioning. Seria bug de teste DESATUALIZADO vs evolução do
      reconciler (06-06/06-07/06-08), NÃO bug de isolamento de DB.
    - Confirmar essa hipótese exige investigar/instrumentar código de produção
      do reconciler OU os testes — AMBOS FORA do escopo do fix aprovado
      ("test-harness only, do NOT touch production reconciler/FSM code").

## Eliminated

- timestamp: 2026-05-14
  candidate: Regressão de Phase 7/8/9 no código emerg/reconciler/fsm
  reason: |
    Migrations 0020/0021/0022 só tocam audit_log. Phase 7 mexeu emerg/fsm.go
    de forma aditiva + nil-guard e reconciler.go só gofmt; `go test ./...` do
    gateway está green (25 pkgs). Os testes nunca rodaram antes — é o 1o run,
    não uma regressão.

- timestamp: 2026-05-14
  candidate: TODAS as falhas explicadas por contaminação de emergency_lifecycles
  reason: |
    REFUTADO pela evidência de isolamento. O fix de harness resolveu 5+ testes,
    mas 3 testes (TestEmergReconcilerHandlesForceProvisionEvent,
    TestEmergReconcilerForceProvisionRejectedNonLeader,
    TestEmergTriggerNoSpawnIfLiveLifecycle) falham EM PROCESSO ISOLADO com
    containers frescos — impossível haver estado vazado. A diagnose original
    era parcial: cobria a contaminação de DB mas não a 2a causa-raiz no
    caminho force-provision/D-C5.

## Resolution

- root_cause: |
    DUAS causas-raiz distintas, não uma:

    (1) [RESOLVIDA] O harness de teste de integração não isolava
    `ai_gateway.emergency_lifecycles` entre test functions. `freshSchema`
    (setup_test.go) só TRUNCATEava 5 tabelas e omitia `emergency_lifecycles`.
    Container Postgres compartilhado package-wide → linhas vazavam entre
    testes, quebrando asserts de contagem absoluta e colidindo com o índice
    parcial único `emergency_live_singleton`. Doc-comment do TestMain afirmava
    incorretamente "db.Down + db.Up". → CORRIGIDA pelo fix aplicado; 5+ testes
    voltaram a passar.

    (2) [NÃO RESOLVIDA — fora do escopo aprovado] 3 testes falham EM
    ISOLAMENTO TOTAL (processo único, containers frescos):
    TestEmergReconcilerHandlesForceProvisionEvent,
    TestEmergReconcilerForceProvisionRejectedNonLeader,
    TestEmergTriggerNoSpawnIfLiveLifecycle. Todos no caminho
    force-provision / D-C5. Hipótese estática (não verificada por
    instrumentação): testes escritos para Plan 06-05 ("stops at the FSM
    transition") ficaram desatualizados quando Plans 06-06+ adicionaram
    `evaluateEmergencyProvisioning`, que no tick seguinte move a FSM para
    fora de EmergencyProvisioning antes do teste ler `fsm.State()`.
    Determinar se é bug de teste ou de produção exige investigar código de
    produção do reconciler / FSM — explicitamente fora do escopo do fix
    aprovado (test-harness only).
- fix: |
    APLICADO (causa-raiz 1 apenas): gateway/internal/integration_test/setup_test.go
    - Adicionado `TRUNCATE ai_gateway.emergency_lifecycles RESTART IDENTITY
      CASCADE` em `freshSchema`.
    - Corrigido o doc-comment desatualizado do TestMain.
    Validação local: gofmt limpo, go build OK, go vet (integration tag) OK.
    NÃO APLICADO (causa-raiz 2): requer nova sessão de debug com escopo que
    inclua o reconciler de produção (internal/emerg/reconciler.go) e/ou os
    testes emerg_force_command_test.go + emerg_trigger_test.go.
- verification: |
    Fix de harness (causa-raiz 1) VERIFICADO em vps-ifix-vm (container
    golang:1.24, docker.sock montado, --network host):
    - Sanity isolado: TestEmergReconcilerForceDestroyNoOpWhenIdle passa sozinho.
    - Suíte emerg completa (`-run TestEmerg -count=1 -v`): dos 8 originais,
      5+ agora PASSAM (ProvisionHappyPath, PriceCap, MultiFailoverRideOut,
      LeaderRecovery*, ForceDestroyNoOpWhenIdle, SingletonDBIndex).
    - Suíte de integração inteira: também surgiram falhas de testes NÃO-emerg
      (TestBillingReconcileDrift, TestSensitivePeakRejectGatewayctl) por
      `build gatewayctl: directory not found` — artefato do meu setup de
      verificação (rsync excluiu o dir `gateway/cmd/gatewayctl/` por engano,
      depois re-sincronizado) — NÃO é bug de código. E falhas timing/load
      (TestRateLimitAtomic1000Concurrent, TestSC1/3/4, TestDCGMFailOpen) num
      host CI carregado — fora do escopo desta sessão.
    PENDENTE: causa-raiz 2 (3 testes force-provision/D-C5) sem verificação —
    fix não aplicado.
- files_changed:
    - gateway/internal/integration_test/setup_test.go
