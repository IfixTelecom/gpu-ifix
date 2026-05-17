# 06-07-SUMMARY.md — Plan 06-07 (Wave 5: PR2 cleanup)

**Plan:** 06-07 (Wave 5)
**Status:** GREEN — completed 2026-05-17
**Type:** human-action (autonomous: false) — agent prepared deletions + RUNBOOK edit; operator approved commit
**Cost:** $0.00 (doc + git)

---

## Output Artifacts

| Path | Change | Commit |
|------|--------|--------|
| `.github/workflows/build-pod.yml` | DELETED (-254 lines) | 064702e |
| `pod/Dockerfile` | DELETED (-77 lines) | 064702e |
| `pod/scripts/emerg-bootstrap.sh` | DELETED (-73 lines) | 064702e |
| `gateway/docs/RUNBOOK-EMERGENCY-POD.md` | "Reverting to Strategy A" rewritten + "GHCR Cleanup" subsection added | this commit |

Total deletions: **3 files, 404 lines**. PR2 = net negative.

---

## must_haves Truths Validation

| # | Truth (06-07-PLAN.md) | Status |
|---|------------------------|--------|
| 1 | `pod/Dockerfile` deletado | ✅ commit 064702e |
| 2 | `pod/scripts/emerg-bootstrap.sh` deletado | ✅ commit 064702e |
| 3 | `.github/workflows/build-pod.yml` deletado | ✅ commit 064702e |
| 4 | RUNBOOK "Reverting to Strategy A" reflete que rollback EXIGE git revert PR1 | ✅ this commit — new section states "NO LONGER POSSIBLE via config rollback" + 5-step `git revert PR2 → revert PR1 → CI rebuild → Portainer env flip → restart` flow (~30 min budget) |
| 5 | Plan 06-06 sign-off 3 lifecycles GREEN VERIFIED as pre-req | ⚠️ Pre-condition reinterpreted per Wave 4 sign-off: L1 GREEN live + L2/L3 deferred to integration tests with operator approval (`pr2_approved: true` in 06-HUMAN-UAT.md frontmatter). 06-06-SUMMARY.md documents the rationale. |
| 6 | Phase 1 `pod/onstart.sh` + `pod/scripts/download-weights.sh` + `pod/health-bridge/` PRESERVED | ✅ untouched — primary pod path independent of emergency pod refactor |
| 7 | Phase 1 `.github/workflows/smoke.yml` PRESERVED OR replaced | ✅ PRESERVED — smoke.yml does NOT `docker build` against the deleted `pod/Dockerfile` (it pulls a prebuilt image). Manual `workflow_dispatch` only; will start failing once GHCR package is deleted (~2 weeks per RUNBOOK observability window). Documented as separate Phase 1 concern; out of Phase 6 scope. |
| 8 | Full-suite gateway test continua GREEN | ✅ `go build ./...` clean; CI run 25980751573 confirmed 22/22 emerg integration GREEN against develop @ 19a66a3. PR2 touches zero Go code → bit-identical reconciler/cancel-in-flight/bid-race semantics. |

---

## smoke.yml decision rationale

`.github/workflows/smoke.yml` is Phase 1's manual-dispatch CI for the **primary pod** path (custom HOST-mode docker-compose). It references `ghcr.io/ifixtelecom/ifix-ai-pod:${IMAGE_TAG}` via the `IMAGE_POD_REPO + HB_TAG` env vars and pulls the prebuilt image — it does NOT execute `docker build -f pod/Dockerfile`.

Per orchestrator's bright-line rule (preserve smoke.yml if it does not touch the deleted Dockerfile), preserved untouched. Operational consequence: once the GHCR package `ifix-ai-pod` is deleted via `gh api -X DELETE` (≥2 weeks out, per RUNBOOK observability window), the "Verify image exists in GHCR" step will fail. That is a Phase 1 primary-pod path concern — refactoring/retiring smoke.yml or migrating it to upstream llama.cpp is out of Phase 6 scope.

If a future maintainer wants to re-arm primary pod CI, options:
1. Retire smoke.yml (low value — it's manual-only and rarely run)
2. Refactor smoke.yml to pull `ghcr.io/ggml-org/llama.cpp:server-cuda-b9128` and exercise the primary HOST-mode docker-compose path

Tracked here for visibility; no action required by Phase 6.

---

## Operator Action — GHCR Cleanup (≥2 weeks observability window)

After Phase 6 PR2 merges to `main` and the dev stack has run cleanly for 2+ weeks without rollback, operator (Pedro) can fully retire the legacy GHCR package:

```bash
# Verify no remaining references first
gh api /orgs/ifixtelecom/packages/container/ifix-ai-pod/versions --jq '[.[] | .metadata.container.tags] | flatten | unique'
# Confirm no Portainer stack (dev OR prod) still references ghcr.io/ifixtelecom/ifix-ai-pod

# Then delete:
gh api -X DELETE /orgs/ifixtelecom/packages/container/ifix-ai-pod
```

NOT scheduled here — operator-driven, post-confidence-window. Documented in RUNBOOK "GHCR Cleanup" subsection.

---

## Phase 6 Final Status

All 5 waves complete:

| Wave | Plan | Status | Key commit |
|------|------|--------|------------|
| 0 | 06-01 | ✅ | b997d25 (spike + gates) |
| 1 | 06-02 + 06-03 | ✅ | 881e9c6 (config) + d8c322c (vast types) |
| 2 | 06-04 | ✅ | 19942bc (lifecycle.go) |
| 3 | 06-05 | ✅ | e179104 (integ fixture) |
| 4 | 06-06 | ✅* | eaa6188 (sign-off) — *L1 GREEN func; L2/L3 deferred to CI; SC-2 perf gap documented |
| 5 | 06-07 | ✅ | 064702e (cleanup) + this commit (RUNBOOK + SUMMARY) |

**Hot-fixes carried during UAT (live discovery, all on develop):**

- `c75bf6b` — swap entrypoint → onstart in vast.CreateRequest (Vast API has no entrypoint field; vast-cli coerces --entrypoint into onstart_cmd at api/instances.py:85)
- `4896004` — drop apt-get install curl from onstart (debconf hang silent failure); add optional POD_DEBUG_SSH_PUBLIC_KEY inline sshd bootstrap
- `19a66a3` — rename EMERGENCY_DEBUG_SSH_PUBLIC_KEY → POD_DEBUG_SSH_PUBLIC_KEY (generic, reusable for primary pod)

**Phase 6 Success Criteria** (per ROADMAP.md):

| SC | Target | Status |
|----|--------|--------|
| SC-1 | Strategy B payload accepted by Vast.ai + container starts in args runtype | ✅ Lifecycle 39 |
| SC-2 | Cold-start P90 ≤ 6 min | ❌ KNOWN GAP — L1 actual 20m 58s (16.74 GB WAN download dominant). Architectural follow-ups tracked in 06-06-SUMMARY.md (Vast volumes + host pin, image pre-bake, geo-filter). Not a correctness blocker. |
| SC-3 | Jinja tool-calling template loaded + chat completions succeed | ✅ Lifecycle 39 — `system_fingerprint=b9128-856c3adac`, prompt 278 tok/s, predict 48 tok/s, Qwen3 thinking reasoning_content rendered |
| SC-4 | Force-destroy cleans up Vast instance + audit row closes | ✅ Lifecycle 39 destroy clean → FSM cooldown → healthy |
| SC-5 | Custom image GHCR + Dockerfile + build workflow + emerg-bootstrap.sh removed | ✅ commit 064702e |

**Functional refactor complete. Perf optimization deferred as known gap.**

---

## Cost Summary (Phase 6 total)

| Wave | Cost |
|------|------|
| 0 (spike) | ~$0.04 |
| 1-3 (code) | $0.00 |
| 4 (UAT — 5 lifecycles incl. failed attempts) | ~$0.28 |
| 5 (cleanup) | $0.00 |
| **Total Phase 6** | **~$0.32** |

Vast.ai balance: $7.15 → ~$6.83. Well within Phase 6 budget (R$3-10 originally projected; actual ~R$1.60 @ FX 5.0).

---

## Sign-off

Operator (Pedro): approves Phase 6 closeout. Burnt-bridge mitigation satisfied. PR2 cleanup landed. Custom image GHCR ready for delete in 2 weeks per observability window.

Claude (driver): commit RUNBOOK + this SUMMARY + STATE.md + ROADMAP.md updates, then push. Phase 6 ready for verifier or ship.
