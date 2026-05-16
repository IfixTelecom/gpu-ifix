---
id: 260516-rym
slug: fix-force-provision-cooldown-transition
date: 2026-05-16
status: complete
---

# Quick Task: Fix handleForceProvision for FSM in Cooldown

## Problem

Bug crítico descoberto durante UAT live lifecycle 31 (Phase 6, 2026-05-16):

`gateway/internal/emerg/reconciler.go:858-859` chamava
`FSM.Transition(StateHealthy, StateFailedOver, ...)` +
`FSM.Transition(StateFailedOver, StateEmergencyProvisioning, ...)` assumindo
from-state=Healthy. Quando operator dispara `force-provision` logo após
falha (FSM em `cooldown`, ex: LC30 com `offer_race_lost`), o CAS interno
do `Transition()` falha silenciosamente — FSM continua em `cooldown`.

Mas `InsertEmergencyLifecycle` + `spawnProvisionGoroutine` já executaram,
criando pod Vast.ai cobrando $$ enquanto reconciler tick avalia
`evaluateCooldown` e ignora lifecycle órfão.

## Solution

1. `handleForceProvision`: ler `FSM.State()` atual ANTES de transitar.
2. Rejeitar com Warn se estado in `{EmergencyProvisioning, EmergencyActive, Recovering}`.
3. Transição via `SetState(StateEmergencyProvisioning, ...)` em vez de 2x `Transition(...)`.
   `SetState` faz CAS-loop até commit, ignorando from-state.
4. `reason` carrega from-state observado pra audit trail:
   `manual_force_provision:cooldown:operator_break_cooldown`.

## Files Changed

- `gateway/internal/emerg/reconciler.go:817-867` — handleForceProvision refactored.
- `gateway/internal/integration_test/emerg_force_command_cooldown_test.go` — NEW. 2 regression tests:
  - `TestEmergReconcilerForceProvisionFromCooldown`: FSM em Cooldown → force-provision → FSM advances + 1 lifecycle row.
  - `TestEmergReconcilerForceProvisionRejectedFromEmergencyActive`: FSM em EmergencyActive → force-provision → rejected + 0 lifecycle rows.

## Verification

- `go build ./...` ✓
- `go vet ./...` ✓ (inclui `-tags=integration`)
- `go test ./internal/emerg/...` ✓ (unit, 4.063s)
- `gofmt -l` ✓ (sem diff)
- Integration tests CI-deferred (precisam PG+Redis live; CI run-tests workflow executa).

## Workaround Pré-Fix

Operator dispara `gatewayctl emerg force-destroy` manual após confirmar
pod órfão via `gatewayctl emerg lifecycles`. Custo: até R$0.30/h até
operator detectar via UI Vast.ai.
