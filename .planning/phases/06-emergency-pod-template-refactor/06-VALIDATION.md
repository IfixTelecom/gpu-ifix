---
phase: 6
slug: emergency-pod-template-refactor
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2026-05-16
---

# Phase 6 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.
> Strategy B locked: `ghcr.io/ggml-org/llama.cpp:server-cuda-b9128` + `runtype=args`.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | `go test` (Go 1.22+, existing reconciler stack) |
| **Config file** | `gateway/go.mod` (existing) |
| **Quick run command** | `cd gateway && go test ./internal/emerg/... -run TestBuildCreateRequest -count=1` |
| **Full suite command** | `cd gateway && go test ./... -count=1 -timeout 120s` |
| **Estimated runtime** | ~25s quick, ~90s full |

---

## Sampling Rate

- **After every task commit:** Run quick command (≤30s feedback)
- **After every plan wave:** Run full suite (≤120s)
- **Before `/gsd:verify-work`:** Full suite + live UAT 3 lifecycles consecutivos GREEN
- **Max feedback latency:** 30 seconds (quick), 120 seconds (full)

---

## Per-Task Verification Map

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| 06-01-XX | 01 spike | 0 | PRV-06 (image) | — | N/A (spike) | manual | `vast create` UI/CLI workflow | N/A | ⬜ pending |
| 06-02-XX | 02 config | 1 | PRV-06 | T-06-01 (env-leak) | secrets via env, never logged | unit | `go test ./internal/config -run TestEmergencyTemplate -count=1` | ❌ W0 | ⬜ pending |
| 06-03-XX | 03 lifecycle | 1 | PRV-06 + PRV-01..10 | T-06-02 (payload-tamper) | Runtype/Args/Onstart matches Strategy B | unit | `go test ./internal/emerg -run TestBuildCreateRequest -count=1` | ❌ W0 | ⬜ pending |
| 06-04-XX | 04 jinja | 2 | PRV-06 | T-06-03 (template-injection) | sha256 verified before mount | unit + integration | `go test ./internal/emerg -run TestJinjaFetch -count=1` | ❌ W0 | ⬜ pending |
| 06-05-XX | 05 integration | 2 | PRV-01..10 | T-06-04 (mock-divergence) | integration_test/emerg_* updated for args runtype | integration | `go test ./internal/integration_test/emerg_... -count=1` | ✅ (existing) | ⬜ pending |
| 06-06-XX | 06 live UAT | 3 | All | T-06-05 (live-failure) | 3 lifecycles consecutive emergency_active end-to-end | manual | `gatewayctl emerg force-provision` × 3 | N/A | ⬜ pending |
| 06-07-XX | 07 cleanup | 4 | — | T-06-06 (rollback-loss) | PR2 separado SOMENTE após UAT green | manual | git diff PR2 review | N/A | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

> Mapeamento exato Task ID será preenchido pelo planner em PLAN.md frontmatter.

---

## Wave 0 Requirements

- [ ] `gateway/internal/emerg/lifecycle_test.go` — `TestBuildCreateRequest_StrategyB` (asserts Image, Runtype="args", Args slice exato, Onstart contém curl MinIO + sha256-c)
- [ ] `gateway/internal/config/config_test.go` — `TestEmergencyTemplateEnvLoad` (env→struct map, validation se string vazia)
- [ ] `gateway/internal/emerg/jinja_fetch_test.go` (se D-04-B opção B2) — `TestFetchJinjaSHA256Mismatch_Errors`
- [ ] `gateway/internal/integration_test/emerg_strategy_b_test.go` — fake Vast client, full provisionLifecycle path com nova payload

*Spike paralelo (Wave 0):* `.planning/phases/06-emergency-pod-template-refactor/06-SPIKE-runtype-args.md` — Pedro provisiona 1 pod Vast manual com Image+Runtype=args+Args para validar empíricamente. Output: confirmar (a) ENTRYPOINT honored, (b) llama-server PID 1, (c) onstart roda no host antes do container, (d) cache-hit `ghcr.io/ggml-org/llama.cpp:server-cuda-b9128` em ≥3 hosts 4090 distintos.

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| Cold-start ≤6min em 90% dos hosts 4090 | Success Criteria #2 | Depende de Vast offer pool e cache-hit empírico | Provisionar 10 pods consecutivos, medir search→/v1/models OK, computar p90 |
| Runtype=ssh CMD-ignore bug resolved | Success Criteria #3 / STATE.md bug #6 | Comparação live com baseline atual | Provisionar 3 pods Strategy B, confirmar `actual_status=running` + `/v1/models` OK end-to-end (vs baseline custom image timeout 1800s) |
| Iteração dev sem rebuild image | Success Criteria #4 | Mudar onstart inline em lifecycle.go → next provision pega versão nova sem CI image build | Editar onstart string, commit gateway, redeploy gateway, force-provision, verificar onstart diferente foi usado |
| Cleanup PR2 só após UAT green | D-08-B-risk | Burnt-bridge mitigation manual | Operator gate — PR1 merged + 3 lifecycles UAT GREEN → abrir PR2 deletion |

---

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags
- [ ] Feedback latency < 30s
- [ ] `nyquist_compliant: true` set in frontmatter

**Approval:** pending
