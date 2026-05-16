---
id: 260516-rym
slug: fix-force-provision-cooldown-transition
date: 2026-05-16
status: complete
---

# Summary

Fix bug crítico de FSM-transition em `handleForceProvision` descoberto
durante UAT live de Phase 6 (lifecycle 31).

## Changes

- `gateway/internal/emerg/reconciler.go:817-867`:
  - Adiciona precheck `FSM.State()` que rejeita force-provision se atual
    state é `{EmergencyProvisioning, EmergencyActive, Recovering}`.
  - Substitui 2x `Transition(from, to)` por `SetState(StateEmergencyProvisioning)`
    — CAS-loop converge regardless of current state.
  - `reason` agora carrega from-state observado pra audit trail.
  - Log `force-provision accepted` inclui `from_state` field.
- `gateway/internal/integration_test/emerg_force_command_cooldown_test.go`: NEW.
  2 regression tests cobrindo from-cooldown e reject-from-active paths.

## Validation

- `go build ./...` ✓
- `go vet -tags=integration ./...` ✓
- `go test ./internal/emerg/...` ✓ (4.063s)
- `gofmt -l` ✓

## Impact

Elimina classe de bug "pod órfão queima $$ enquanto FSM travada em
cooldown". Force-provision agora funciona de qualquer estado não-emergente
(Healthy, Degraded, FailedOver, Cooldown) e rejeita explicitamente de
estados já em emergency path com Warn log.

## Quick Stats

- 1 source file changed (~30 lines refactored)
- 1 new test file (~190 lines, 2 regression tests)
- 0 schema changes
- 0 dependencies added
