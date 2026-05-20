---
status: resolved
trigger: "3 testes de integração primary falham no CI run 26041169815 (build-gateway, develop, 787db67): TestPrimaryDisabled_NoEvent_StaysAsleep, TestEmergCoexist_PrimaryReadyForceDestroysEmerg, TestRestartRecovery_HealthyInstanceRestoresReady. Plan 10 author validou apenas compile-only — CI é o 1o runtime real."
created: 2026-05-18
updated: 2026-05-18
goal: find_and_fix
---

# Debug Session: primary-integration-tests-ci

## Current Focus

reasoning_checkpoint:
  hypothesis: |
    DUAS causas-raiz distintas:
    (1) freshSchema não trunca primary_lifecycles → linhas vazam entre testes,
        e o partial unique index primary_live_singleton aborta InsertPrimaryLifecycle
        do próximo teste (falha 0.05s). Asserts de count também quebram.
        Explica falhas em: TestPrimaryDisabled_NoEvent_StaysAsleep,
        TestRestartRecovery_{Healthy,Orphan,Unhealthy,NoOpenRow}.
    (2) emerg.SubscribePrimaryEvents handler para "primary_ready" chama
        r.cancelActiveLifecycle(ctx, "primary_took_over"). Mas
        cancelActiveLifecycle é apenas Layer 1 (ctx cancel da goroutine de
        provisioning) — quando FSM=StateEmergencyActive a goroutine JÁ
        EXITED (markHealthy retornou nil), então Layer 1 é no-op. Nenhuma
        chamada a DestroyInstance. Nenhuma transição de FSM.
        O log diz "force-destroying emerg lifecycle (Pitfall #11)" mas o
        código não destrói.
        Função correta seria destroyAndCloseLifecycle (mesma usada por
        handleForceDestroy, linha 909 reconciler.go) + enterCooldown.
        Explica TestEmergCoexist_PrimaryReadyForceDestroysEmerg.
  confirming_evidence:
    - "Repro local em isolamento: Test 2 falha deterministicamente com
       'got emerg.FSM=emergency_active destroyHits=0' — comprovando que
       cancelActiveLifecycle NÃO destrói o instance"
    - "Repro local em suite cheia: 6 testes primary falham; sintomas
       casam exatamente com contaminação de primary_lifecycles (3 falhas
       0.05s = duplicate key, 1 conta-rows > 0)"
    - "Leitura do código de produção: subscribe.go:193 chama
       cancelActiveLifecycle; lifecycle.go:913 cancelActiveLifecycle
       implementa apenas Layer 1+2 (ctx cancel + pubsub), sem destroy.
       Comment em lifecycle.go:944 confirma: 'Layer 3 ... is enforced
       inside waitForReadyOrDestroy's ctx.Done() branch' — mas a
       goroutine já exited quando FSM=EmergencyActive"
    - "handleForceDestroy (reconciler.go:902) usa o padrão correto:
       destroyAndCloseLifecycle + enterCooldown — é EXATAMENTE o que
       o handler de primary_ready deveria fazer"
  falsification_test: |
    Para causa (1): se rodar os 6 testes primary EM ORDEM com a tabela
    primary_lifecycles VAZIA entre cada um, todos passam. Testado: PASS
    em isolamento confirmado para 5 de 6 (Test 2 sempre falha por outro
    motivo).
    Para causa (2): se trocar subscribe.go:193 para chamar
    destroyAndCloseLifecycle + enterCooldown, mock.destroyHits.Add(1) é
    chamado e FSM sai de EmergencyActive — Test 2 passa em isolamento.
  fix_rationale: |
    Causa (1) [test-only — harness fix]: adicionar
    "TRUNCATE ai_gateway.primary_lifecycles RESTART IDENTITY CASCADE"
    em freshSchema (paridade com fix de emergency_lifecycles do PR
    9772d71). RESTART IDENTITY zera o BIGSERIAL.
    Causa (2) [production — corrige bug real]: trocar
    cancelActiveLifecycle por destroyAndCloseLifecycle + enterCooldown
    em subscribe.go:193. Isso casa com a INTENÇÃO documentada em
    RESEARCH §Pitfall #11 ("force-destroy redundant emerg pod") e com
    o teste de integração que valida destroyHits >= 1. Unit test
    em subscribe_test.go:216-268 só valida Layer 2 (pubsub) — isso
    continua valendo, pois destroyAndCloseLifecycle internamente chama
    closeLifecycle que TAMBÉM publica em gw:emerg:events.
  blind_spots: |
    - Não confirmei via leitura completa se destroyAndCloseLifecycle
      publica 'cancel_in_flight' (essa é a verificação do unit test).
      Vou verificar antes de aplicar o fix.
    - Não rodei o suite full após o fix proposto para garantir zero
      regressão — vou rodar antes de commitar.
    - Há outros testes failing fora do escopo (TestSC*, TestSensitive*,
      TestRateLimit*, TestDCGM*, TestBillingReconcile*, TestTier1*) —
      não vou tocar, são phase/scope diferentes.

test: aplicar os 2 fixes (harness + production), rodar suite primary+emerg em isolamento, depois suite cheia
expecting: 6 testes primary passam + nenhuma regressão em emerg_*_test
next_action: confirmar que destroyAndCloseLifecycle publica cancel_in_flight, depois aplicar fixes

## Symptoms

expected:
  - TestPrimaryDisabled_NoEvent_StaysAsleep: PRIMARY_POD_SCHEDULE_DISABLED=true + no force-up event → FSM stays Asleep (soak-gate safety)
  - TestEmergCoexist_PrimaryReadyForceDestroysEmerg (Pitfall #11): emerg pod running + primary ready → emerg force-destroyed ≤30s
  - TestRestartRecovery_HealthyInstanceRestoresReady: gateway restart with open primary lifecycle → recovery resumes Ready (no pod leak)

actual:
  - Test 1 (primary_disabled_force_up_test.go:160): `Error: Not equal:` (expected vs actual mismatch)
  - Test 2 (primary_emerg_coexist_test.go:116): `Error: Condition never satisfied` (31.11s timeout)
  - Test 3 (primary_restart_recovery_test.go:54): `Error: Received unexpected error:` (0.05s — instant fail, likely setup error)

errors:
  - "Not equal" at primary_disabled_force_up_test.go:160
  - "Condition never satisfied" at primary_emerg_coexist_test.go:116 (timeout 31s)
  - "Received unexpected error" at primary_restart_recovery_test.go:54 (instant 0.05s)

reproduction: |
  CI run 26041169815, build-gateway / Integration tests job
  Command: go test -tags=integration -count=1 -race -timeout=10m ./internal/integration_test/...
  Local: ssh vps-ifix-vm + docker + run same command in gateway/ dir

started: 1o run real dos 3 testes — Plan 10 author validou compile-only

## Eliminated

(none yet)

## Evidence

- timestamp: 2026-05-18T15:42
  source: rodar os 3 testes isolados (-run filtrado) em vps-ifix-vm com docker.sock + golang:1.24
  finding: |
    Quando rodados sozinhos:
      - TestPrimaryDisabled_NoEvent_StaysAsleep — PASS (9.65s)
      - TestEmergCoexist_PrimaryReadyForceDestroysEmerg — FAIL deterministicamente (30.81s)
        Erro: "got emerg.FSM=emergency_active destroyHits=0"
      - TestRestartRecovery_HealthyInstanceRestoresReady — PASS (0.28s)
    Tests 1 e 3 passam isolados → falhas CI provavelmente são CONTAMINAÇÃO de
    primary_lifecycles (similar ao bug emerg fix anterior — freshSchema não
    trunca primary_lifecycles, e a tabela tem `primary_live_singleton`
    partial unique index).
    Test 2 falha em isolamento total (containers frescos, processo único)
    → BUG REAL no caminho primary_ready → emerg.SubscribePrimaryEvents.

- timestamp: 2026-05-18T16:08
  source: rodar full primary+emerg suite após os fixes em vps-ifix-vm
  finding: |
    34 testes integration passam após os fixes:
      - TestPrimaryDisabled_NoEvent_StaysAsleep PASS 9.28s
      - TestEmergCoexist_PrimaryReadyForceDestroysEmerg PASS 5.73s (era timeout 30s)
      - TestRestartRecovery_HealthyInstanceRestoresReady PASS 0.27s
      - TestRestartRecovery_OrphanInstance_ClosesLifecycle PASS 0.22s
      - TestRestartRecovery_UnhealthyInstance_ClosesLifecycle PASS 0.22s
      - TestRestartRecovery_NoOpenRow_NoOp PASS 2.16s
      - Todos demais TestEmerg* + TestPrimary* PASS
    Unit tests dos pacotes tocados também passam (emerg, primary, emerg/vast).

- timestamp: 2026-05-18T16:31
  source: CI run 26046468415 (build-gateway, develop, commit a73a1f2)
  finding: |
    Job "Integration tests (testcontainers-go)" — SUCCESS.
    Job "Go unit tests + sqlc codegen verify" — SUCCESS.
    Fixes verificados em CI real (testcontainers-go com Docker daemon do GHA).

## Resolution

root_cause: |
  DUAS causas-raiz distintas, ambas RESOLVIDAS:

  (1) [HARNESS] freshSchema (setup_test.go) não fazia TRUNCATE de
  ai_gateway.primary_lifecycles entre testes. Container Postgres
  compartilhado package-wide + partial unique index
  `primary_live_singleton ON ... WHERE ended_at IS NULL` →
  linhas vazavam entre testes, quebrando:
    - asserts de count absoluto (TestPrimaryDisabled_NoEvent_StaysAsleep
      esperava 0, viu 1+; TestRestartRecovery_NoOpenRow_NoOp idem)
    - próximos InsertPrimaryLifecycle (falha 0.05s com duplicate key)
      → quebra TestRestartRecovery_{Healthy,Orphan,Unhealthy}_*
  6 testes primary afetados.
  Idêntico ao bug emergency_lifecycles corrigido no commit 9772d71.

  (2) [PRODUCTION] emerg.SubscribePrimaryEvents handler para
  "primary_ready" (subscribe.go:193) chamava cancelActiveLifecycle —
  função projetada para CANCELAR provisioning em flight, NÃO para
  destruir um pod ativo. Quando FSM=StateEmergencyActive, a goroutine
  de provisioning JÁ EXITED (markHealthy retornou nil), então:
    - Layer 1 (ctx cancel) — no-op (goroutine não existe)
    - Layer 2 (publish cancel_in_flight) — funcionava
    - Layer 3 (destroy + close) — ESTAVA AUSENTE — comentado como
      "enforced inside waitForReadyOrDestroy's ctx.Done() branch" mas
      essa branch só roda DENTRO da goroutine de provisioning
  Resultado: log "force-destroying emerg lifecycle (Pitfall #11)" mas
  nenhum DestroyInstance, FSM travada em EmergencyActive.
  TestEmergCoexist_PrimaryReadyForceDestroysEmerg corretamente
  expunha esta lacuna.

fix: |
  APLICADO (ambas as causas):

  (1) gateway/internal/integration_test/setup_test.go — adicionado
  TRUNCATE ai_gateway.primary_lifecycles RESTART IDENTITY CASCADE
  em freshSchema após o TRUNCATE de emergency_lifecycles. RESTART
  IDENTITY zera a BIGSERIAL id sequence.

  (2) gateway/internal/emerg/subscribe.go — handler de "primary_ready"
  reescrito para a sequência force-destroy correta (paridade
  handleForceDestroy em reconciler.go:902):
    1. snapshot do lc = r.activeLifecycle.Load()
    2. cancelActiveLifecycle(ctx, "primary_took_over") — preserva o
       Layer 2 broadcast `cancel_in_flight` (mantém unit test verde:
       TestSubscribePrimaryEvents_ForceDestroyEmergOnPrimaryReady)
    3. destroyAndCloseLifecycle(ctx, lc, "primary_took_over") — Layer
       3 real: BestEffortDestroy(vast_instance_id) + closeLifecycle
    4. enterCooldown(StateEmergencyActive, now, "primary_took_over",
       false) — transição FSM EmergencyActive → Cooldown
  fromFailure=false: cutback deliberado, não falha → janela de
  suppression normal (ProvisionHealthyDurationSeconds).

verification: |
  Validado em vps-ifix-vm (golang:1.24 + docker.sock + --network host).

  Em isolamento (3 testes nomeados):
    - TestPrimaryDisabled_NoEvent_StaysAsleep — PASS (9.64s)
    - TestEmergCoexist_PrimaryReadyForceDestroysEmerg — PASS (6.03s,
      era 30.87s timeout antes do fix)
    - TestRestartRecovery_HealthyInstanceRestoresReady — PASS (0.28s)

  Suite primary+emerg integration completa (34 testes):
    - Todos passam: TestPrimaryCancelInflight_TripleLayer,
      TestPrimaryDisabled_*, TestPrimaryLeader_*, TestPrimaryProbe_*,
      TestRestartRecovery_* (4 testes), TestEmerg* (24 testes)
    - Sem regressão em TestEmergProvisionHappyPath, TestEmergCutback,
      TestEmergLeaderRecovery*, TestEmergBidRaceLost, etc.

  Unit tests dos pacotes tocados:
    - internal/emerg PASS (6.07s) — incluindo
      TestSubscribePrimaryEvents_ForceDestroyEmergOnPrimaryReady
    - internal/primary PASS (9.21s)
    - internal/emerg/vast PASS (1.09s)

  Suite full (./...): apenas falhas pré-existentes fora de escopo
  (TestBillingReconcileDrift, TestRateLimitAtomic1000Concurrent,
  TestSensitive*, TestSC*, TestDCGMFailOpen, TestTier1Unavailable*,
  internal/auth argon2 timeout) — nenhuma relacionada às minhas
  mudanças.

files_changed:
  - gateway/internal/integration_test/setup_test.go
  - gateway/internal/emerg/subscribe.go
