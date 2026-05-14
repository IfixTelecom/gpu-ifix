---
phase: 7
slug: observability-dashboard-alerting
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2026-05-14
---

# Phase 7 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | go test (gateway) + vitest (dashboard) — populated by planner from RESEARCH.md Validation Architecture |
| **Config file** | gateway: existing; dashboard: none — Wave 0 installs |
| **Quick run command** | `go test ./gateway/internal/alert/... ./gateway/internal/audit/... ./gateway/internal/obs/... -count=1` |
| **Full suite command** | `go test ./gateway/... -count=1 -race` + dashboard `vitest run` |
| **Estimated runtime** | ~TBD seconds — planner to confirm |

---

## Sampling Rate

- **After every task commit:** Run quick run command
- **After every plan wave:** Run full suite command
- **Before `/gsd-verify-work`:** Full suite must be green
- **Max feedback latency:** TBD — planner to confirm

---

## Per-Task Verification Map

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| _Populated by gsd-planner from PLAN.md task breakdown_ | | | | | | | | | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

*Populated by gsd-planner — expected: dashboard test framework install (vitest), shared Go test fixtures for the alert package, mock Chatwoot/ClickUp/Brevo clients.*

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| WhatsApp + email delivered within 60s of critical event | OBS-04, OBS-05 (SC-2) | Requires live Chatwoot/Brevo credentials + a real on-call routing target — provisioning unknowns per 07-RESEARCH.md | HUMAN-UAT: induce a critical event, confirm WhatsApp message + email arrive within 60s |
| ClickUp task opened for critical/warning alert | OBS-04, OBS-05 | Requires live ClickUp service token + target list | HUMAN-UAT: trigger a critical alert, confirm task appears in the target ClickUp list |

*Planner to refine — channels degrade gracefully to log + dashboard banner when credentials absent (gateway optional-feature pattern), so build is not blocked.*

---

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags
- [ ] Feedback latency < TBDs
- [ ] `nyquist_compliant: true` set in frontmatter

**Approval:** pending
