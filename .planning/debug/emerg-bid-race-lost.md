---
slug: emerg-bid-race-lost
status: resolved
trigger: "TestEmergBidRaceLost falha intermitentemente MESMO EM ISOLAMENTO TOTAL — FSM não retorna a Healthy após 3 bid race losses, fica travada em emergency_provisioning com create_hits=7. Bug real no abort-after-3 (D-A3) do reconciler de emergência."
created: 2026-05-14
updated: 2026-05-14
goal: find_and_fix
---

# Debug Session: emerg-bid-race-lost

## Symptoms

**Expected behavior:**
D-A3: quando todo `CreateInstance` da Vast.ai retorna 404 (`no_such_ask` — offer/ask perdido pra outro cliente), após **3 tentativas** com backoff 2s/4s/8s o `provisionLifecycle` retorna `ErrOfferRaceLost`, `startProvisioning` aborta o lifecycle com `shutdown_reason='offer_race_lost'` e a FSM transiciona `EmergencyProvisioning → Healthy`.

**Actual behavior:**
FSM fica travada em `emergency_provisioning`. `create_hits=7` (reconciler continua tentando criar instância em vez de abortar em 3). Teste estoura o budget de 30s do `waitFor`.

**Error messages:**
```
emerg_provision_happy_test.go:370: FSM did not return to Healthy after 3 bid race losses;
  got fsm=emergency_provisioning create_hits=7
--- FAIL: TestEmergBidRaceLost (30.72s)
```

**Timeline:**
1º run real da suíte de integração (Phase 6 adiou pra CI runtime). Descoberto na sessão `emerg-integration-tests-ci` durante o fix da contaminação de DB. Aquela sessão classificou este teste como "flake de timing, passa isolado" e o deixou fora de escopo — classificação **REFUTADA** por este run.

**Reproduction:**
```
ssh vps-ifix-vm
cd /root/gpu-ifix
docker run --rm -v /root/gpu-ifix:/src -v /var/run/docker.sock:/var/run/docker.sock --network host \
  -w /src/gateway golang:1.24 \
  go test -tags=integration -run "^TestEmergBidRaceLost$" ./internal/integration_test/... -count=1 -v -timeout=5m
```
Intermitente — rodar várias vezes pra reproduzir.

## Context

- Teste: `gateway/internal/integration_test/emerg_provision_happy_test.go:322-375` (`TestEmergBidRaceLost`).
- Código de produção suspeito: `gateway/internal/emerg/reconciler.go` — caminho `provisionLifecycle` / `startProvisioning` / abort-after-3 (`ErrOfferRaceLost`). Também `gateway/internal/emerg/fsm.go`.
- Mock: `newMockVastServer` com `mock.createStatus = 404` permanente; teste conta `mock.createHits`.
- Config do reconciler no teste: `TickInterval: 100 * time.Millisecond`, backoff 2s/4s/8s.
- Decisão de design: **D-A3** — 3 tentativas antes de abortar.
- **NÃO é contaminação de DB** (causa 1 da sessão `emerg-integration-tests-ci`, já corrigida em 9772d71): falha em container fresco, processo único, `freshSchema` já trunca `emergency_lifecycles`.
- **NÃO é flake puro de carga**: falha em isolamento total, sem outros testes no processo.
- Regras CLAUDE.md: --diagnose primeiro, validar root cause com EVIDÊNCIA antes de editar; se o fix mudar comportamento do reconciler de produção, validar com o usuário ANTES de editar; sem edits especulativos.

## Current Focus

- hypothesis: RESOLVED — re-trigger race confirmado e corrigido.
- next_action: nenhuma — fix aplicado, verificado (20/20 isolamento + -race limpo + suíte emerg sem regressão), commitado em develop. Aguardando decisão de push do usuário.
- test: `^TestEmergBidRaceLost$` rodou 20/20 PASS em isolamento + 1x `-race` PASS no container vps-ifix-vm.
- expecting: com o gate de re-trigger corrigido, `create_hits` para em exatamente 3, FSM fica estável em Cooldown — confirmado.

## Evidence

- timestamp: 2026-05-14
  source: vps-ifix-vm — go test -tags=integration -run ^TestEmergBidRaceLost$ -count=1, processo isolado, container golang:1.24 fresco
  finding: |
    RUN 1: FAIL — "FSM did not return to Healthy after 3 bid race losses;
      got fsm=emergency_provisioning create_hits=7" (30.72s)
    RUN 2: PASS (29.67s)
    RUN 3: PASS (15.62s)
    RUN 4: em andamento no momento da leitura do log.
    CONCLUSÃO PARCIAL: falha é INTERMITENTE mesmo em isolamento total
    (1 processo, container fresco, freshSchema já trunca emergency_lifecycles).
    Logo: race condition / bug de timing no PRÓPRIO reconciler de produção,
    NÃO contaminação de DB e NÃO flake de carga da suíte. A classificação
    "flake de timing, passa isolado" da sessão emerg-integration-tests-ci
    está REFUTADA. Nota: RUN 3 passou em 15.62s (~1 ciclo de provisioning:
    2+4+8=14s + slack) enquanto RUN 2 passou em 29.67s (~2 ciclos) — forte
    sinal de que MÚLTIPLOS ciclos de provisioning estão rodando.

- timestamp: 2026-05-14
  source: gateway/internal/integration_test/emerg_provision_happy_test.go:315-375 (TestEmergBidRaceLost)
  finding: |
    O teste força `mock.createStatus = 404` permanente, publica
    `breaker event local-llm open`, e espera (`waitFor` 30s) por
    `mock.createHits >= 3 && fsm.State() == StateHealthy`. O sintoma
    `create_hits=7` mostra que o reconciler fez 7 tentativas de
    CreateInstance sem nunca abortar — o gate "para após 3" não disparou
    nessa execução, OU disparou mas a FSM não transicionou pra Healthy,
    OU o lifecycle foi recriado e o contador de tentativas reiniciou.

- timestamp: 2026-05-14
  source: |
    reconciler.go:680-711 (evaluateHealthy) + lifecycle.go:185-293
    (provisionLifecycle) + lifecycle.go:144-159 (startProvisioning goroutine)
    + tracker.go:79-134 (ApplyEvent/SustainedFailedOverSeconds)
  finding: |
    ROOT CAUSE CONFIRMADO POR LEITURA DE CÓDIGO — é a 3ª possibilidade
    levantada na evidência anterior: "o lifecycle foi recriado e o contador
    de tentativas reiniciou".

    Sequência exata:
    1. Teste publica `local-llm open`. tracker.ApplyEvent seta openSince UMA vez.
    2. evaluateHealthy (reconciler.go:680) dispara trigger:
       Healthy → FailedOver → EmergencyProvisioning. O pre-check D-C5
       (ListLiveEmergencyLifecycles) passa porque startProvisioning ainda
       não inseriu a row.
    3. evaluateEmergencyProvisioning chama startProvisioning →
       InsertEmergencyLifecycle + spawn provisionLifecycle. A goroutine faz
       3x CreateInstance com backoff 2s+4s+8s ≈ 14s (lifecycle.go:215-287).
    4. Após 3 race losses, provisionLifecycle chama closeLifecycle
       (lifecycle.go:291) — que faz `activeLifecycle.Store(nil)` e
       UPDATE ended_at na row (lifecycle.go:459) — e retorna ErrOfferRaceLost.
       O defer da goroutine (lifecycle.go:155) transiciona
       EmergencyProvisioning → Healthy. ATÉ AQUI ESTÁ CORRETO: 3 hits,
       FSM Healthy.
    5. PORÉM o tracker continua `open` (o teste NUNCA publica `closed`).
       openSince foi setado há ~14s. Logo, no PRÓXIMO tick (100ms depois)
       evaluateHealthy roda de novo, vê SustainedFailedOverSeconds() ainda
       >= threshold (cfg de teste = ProvisionTriggerFailedOverSeconds = 1s),
       o pre-check D-C5 agora PASSA de novo (a row anterior foi fechada por
       closeLifecycle, então ListLiveEmergencyLifecycles retorna vazio), e o
       TRIGGER DISPARA UMA SEGUNDA VEZ — novo lifecycle, contador createHits
       continua acumulando (+3), FSM volta pra EmergencyProvisioning.

    Isso explica create_hits=6,7+ e a intermitência: o waitFor do teste
    amostra a cada 200ms procurando o instante em que
    `createHits>=3 && fsm==Healthy` ao mesmo tempo. Existe uma janela de
    ~100-200ms após cada lifecycle abortar em que ambos são verdade. Quando
    o sampler cai nessa janela → PASS (RUN 2, RUN 3). Quando perde (o tick
    do sampler cai durante o próximo ciclo de provisioning) → nunca pega o
    transiente → FAIL (RUN 1). Race sampler-vs-transiente puro.

    NATUREZA DO BUG: NÃO é apenas teste frágil. É comportamento real de
    produção indesejado — o abort D-A3 é IMEDIATAMENTE desfeito por
    re-trigger porque o breaker ainda está open. Em produção, com
    TickInterval=1s e ProvisionTriggerFailedOverSeconds=120s, o efeito é
    mais lento mas existe: após offer_race_lost, se o local-llm continuar
    open, o reconciler re-dispara provisioning em loop sem nenhum
    backoff/cooldown entre tentativas — exatamente a oscilação que o estado
    Cooldown foi desenhado pra suprimir, mas Cooldown só é alcançado pelo
    caminho de cutback (Recovering → Cooldown), NÃO pelo caminho de
    offer_race_lost. O caminho de falha de provisioning volta direto pra
    Healthy (lifecycle.go:155) sem passar por Cooldown.

- timestamp: 2026-05-14
  source: |
    fix verification — vps-ifix-vm, container golang:1.24, processo isolado.
    20x `^TestEmergBidRaceLost$` -count=1 + 1x mesmo teste com -race.
  finding: |
    FIX APLICADO E VERIFICADO.
    - 20/20 runs de isolamento PASS (tempos 29.7s–73.2s; variância vem do
      re-download de deps testcontainers a cada `docker run --rm`, não do
      teste). O flake 2/5 original ESTÁ ELIMINADO.
    - 1x run com `-race`: PASS (44.4s), sem data races reportadas.
    - O teste agora afirma create_hits == EXATAMENTE 3 + FSM == Cooldown
      (não mais a janela transiente Healthy). O upper bound em create_hits
      é a prova de que o loop de re-trigger está fechado.

## Eliminated

- timestamp: 2026-05-14
  candidate: Contaminação de DB entre testes (emergency_lifecycles não truncada)
  reason: |
    Causa 1 da sessão emerg-integration-tests-ci, já corrigida em 9772d71
    (`freshSchema` agora faz TRUNCATE ... RESTART IDENTITY CASCADE de
    emergency_lifecycles). Este teste falha em PROCESSO ISOLADO com
    container fresco — não há outro teste no run pra contaminar estado.

- timestamp: 2026-05-14
  candidate: Flake de timing por carga da suíte completa no host CI compartilhado
  reason: |
    REFUTADO. Falha reproduzida em isolamento total (RUN 1), 1 único teste
    no processo, container dedicado. Não é contenção de recursos da suíte.
    É race interno do reconciler.

- timestamp: 2026-05-14
  candidate: provisionLifecycle não para em 3 tentativas (loop `for attempt < 3` quebrado)
  reason: |
    REFUTADO por leitura de código. lifecycle.go:215 `for attempt := 0;
    attempt < 3; attempt++` está correto; lifecycle.go:289-292 fecha o
    lifecycle e retorna ErrOfferRaceLost após o loop. O contador chega a
    7 porque o lifecycle é RECRIADO (novo provisionLifecycle, novo loop
    de 0..3), não porque um único loop passa de 3.

- timestamp: 2026-05-14
  candidate: |
    Fix mínimo "Option A" — só rotear offer_race_lost por Cooldown,
    reusando o hold de ProvisionHealthyDurationSeconds existente.
  reason: |
    INSUFICIENTE (verificado em código numa continuação anterior, que
    corretamente PAROU em vez de aplicar um fix incompleto). `evaluateCooldown`
    segurava por `ProvisionHealthyDurationSeconds`, que a config de teste
    seta em 1s. Um Cooldown de 1s expira, FSM → Healthy, o breaker ainda
    está open, e `evaluateHealthy` re-dispara imediatamente — o loop NÃO
    fecha. O fix precisava de uma janela de cooldown de falha REAL,
    separada e mais longa que um ciclo de provisioning (2+4+8≈14s).

## Resolution

- root_cause: |
    Re-trigger race no reconciler. `provisionLifecycle` aborta corretamente
    em 3 tentativas (D-A3) e o defer da goroutine volta a FSM pra Healthy —
    mas o caminho de falha de provisioning NÃO passava por Cooldown e o
    tracker `local-llm` continua `open`. No tick seguinte (100ms no teste),
    `evaluateHealthy` re-dispara o trigger porque SustainedFailedOverSeconds
    ainda excede o threshold e o pre-check D-C5 passa (a row foi fechada).
    Resultado: ciclos repetidos de provisioning, `create_hits` acumula
    além de 3, e o `waitFor` do teste (sample 200ms) só capturava a janela
    transiente `createHits>=3 && fsm==Healthy` de forma intermitente.

- fix: |
    "Cooldown de falha com janela real" — aprovado pelo usuário.

    1. PRODUÇÃO — caminho de falha roteia por Cooldown. No error handler da
       goroutine de `startProvisioning` (lifecycle.go), quando o erro é
       `ErrOfferRaceLost`, a FSM agora transiciona
       `EmergencyProvisioning → Cooldown` (via novo helper `enterCooldown`)
       em vez de `→ Healthy`. Os demais erros (cancelled_in_flight,
       health_timeout, instance_terminal_state, no_offers_below_cap)
       mantêm o comportamento anterior de voltar pra Healthy — cancelamento
       é recuperação deliberada e os caminhos terminais pós-create têm
       semântica distinta que o backoff de bid-race não modela. O
       `shutdown_reason='offer_race_lost'` continua sendo gravado na row;
       só o estado-alvo da FSM mudou. A transição EmergencyProvisioning →
       Cooldown já é mecanicamente legal (fsm.go usa CAS livre, sem tabela
       de transições).

    2. PRODUÇÃO — janela de cooldown de falha real. Novo campo de config
       `ProvisionFailureCooldownSeconds` (env `PROVISION_FAILURE_COOLDOWN_SECONDS`),
       default **60s**. `evaluateCooldown` agora escolhe o hold:
       `ProvisionFailureCooldownSeconds` quando o Cooldown foi entrado a
       partir de uma FALHA de provisioning, vs o `ProvisionHealthyDurationSeconds`
       existente quando entrado pelo caminho normal de cutback/force-destroy.
       A distinção é feita por um novo campo `cooldownFromFailure atomic.Bool`
       no Reconciler, setado em lock-step com `cooldownEnteredAt` (via o
       helper `enterCooldown(from, now, reason, fromFailure)`), e resetado
       pra false quando Cooldown → Healthy. Os call-sites de cutback
       (`evaluateRecovering`) e force-destroy (`handleForceDestroy`) passam
       `fromFailure=false`; o error handler de offer_race_lost passa
       `fromFailure=true`.

       JUSTIFICATIVA do default 60s: precisa ser claramente maior que UM
       ciclo de tentativas de provisioning (2+4+8 ≈ 14s) pra que uma falha
       de ciclo único realmente faça backoff em vez de virar um hammer-loop
       de ~1s contra o spot market da Vast.ai. 60s dá ~4x esse ciclo —
       suficiente pra absorver a transiência típica de uma bid race
       (offers são relistadas em segundos no spot market) sem deixar a
       emergência indisponível por minutos. Fica abaixo do
       `ProvisionHealthyDurationSeconds=300s` do cutback porque a semântica
       é diferente: cutback espera o primário ESTABILIZAR; falha de
       provisioning só quer um backoff curto antes de re-tentar (bid races
       são transientes e VOCÊ QUER re-tentar). Operador pode sobrescrever
       via env. Após a janela expirar o sistema pode re-tentar provisioning
       — comportamento correto de produção, agora com backoff em vez de
       loop de martelo.

    3. TESTE — `TestEmergBidRaceLost` atualizado: seta
       `ProvisionFailureCooldownSeconds = 120` na config de teste (supera
       confortavelmente a janela de 30s do `waitFor`, então o estado
       pós-falha é OBSERVAVELMENTE estável). O predicado do `waitFor` mudou
       de "FSM == Healthy" para "FSM ∈ {Cooldown, Healthy}" após 3 create
       hits, e foram adicionadas duas asserções load-bearing:
       (a) `create_hits == EXATAMENTE 3` (o upper bound prova que o loop
       de re-trigger está fechado), re-checado após um settle de 2s; e
       (b) `fsm.State() == StateCooldown` (o caminho offer_race_lost
       estaciona a FSM em Cooldown, não Healthy). Os outros 9 testes emerg
       não precisaram de mudança — eles só entram em Cooldown pelo caminho
       de cutback/force-destroy (`fromFailure=false`), que continua usando
       `ProvisionHealthyDurationSeconds=1s` do `defaultTestCfg`. O novo
       campo de config é defaultado por `config.Load()` (60s) então todos
       compilam/comportam-se sem alteração. `config_test.go` ganhou o env
       na lista de clear + uma asserção de default (60) + uma de override.

- verification: |
    - `gofmt -l .` limpo, `go build ./...` limpo, `go vet -tags=integration
      ./internal/integration_test/...` + `go vet ./internal/emerg/...
      ./internal/config/...` limpos (local, ops-claude).
    - `go test ./internal/config/... ./internal/emerg/...` PASS (local).
    - `^TestEmergBidRaceLost$` rodou 20/20 PASS em ISOLAMENTO total no
      container golang:1.24 da vps-ifix-vm (`-count=1`, processo único,
      container fresco a cada run). O flake intermitente 2/5 original
      está ELIMINADO.
    - 1x `^TestEmergBidRaceLost$` com `-race`: PASS, sem data races.
    - Suíte emerg completa (`-run "^TestEmerg" -count=1`): 22/22 PASS
      (`ok ... 160.167s`, exit 0) no container vps-ifix-vm — ZERO
      regressão. TestEmergBidRaceLost PASS (17.08s) dentro da suíte
      completa, e os outros 21 testes emerg seguem verdes.

- files_changed:
    - gateway/internal/config/config.go (novo campo ProvisionFailureCooldownSeconds + loader, default 60s)
    - gateway/internal/config/config_test.go (clear list + default/override assertions)
    - gateway/internal/emerg/reconciler.go (campo cooldownFromFailure + helper enterCooldown + evaluateCooldown escolhe hold + call-sites cutback/force-destroy)
    - gateway/internal/emerg/lifecycle.go (startProvisioning error handler roteia ErrOfferRaceLost por Cooldown)
    - gateway/internal/integration_test/emerg_provision_happy_test.go (TestEmergBidRaceLost: cfg + waitFor predicate + upper-bound assertions)
