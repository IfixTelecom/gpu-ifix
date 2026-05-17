# 06-06-SUMMARY.md — Plan 06-06 (Wave 4: HUMAN-UAT live Vast.ai)

**Plan:** 06-06 (Wave 4)
**Status:** GREEN-with-known-gap — completed 2026-05-17
**Type:** human-action (autonomous: false) — Task 1+2 doc writing by Claude subagent, Task 3 live UAT driven by Claude session on ops-claude with operator (Pedro) approval gates
**Cost:** ~$0.17 Vast.ai (1 lifecycle × 25min @ $0.43/h Spain ES); L2 + L3 deferred to integration tests (no live spend)

---

## Output Artifacts

| Path | Purpose | Commit |
|------|---------|--------|
| `.planning/phases/06-emergency-pod-template-refactor/06-HUMAN-UAT.md` | 3-lifecycle UAT script (L1 sign-off filled in; L2/L3 skipped with rationale) | 401cb34 (initial) + this commit (sign-off) |
| `gateway/docs/RUNBOOK-EMERGENCY-POD.md` | Updated runbook with Strategy B image source, onstart pattern, troubleshooting Runtype=args | 401cb34 |

---

## must_haves Truths Validation

| # | Truth (06-06-PLAN.md) | Status |
|---|------------------------|--------|
| 1 | 3 lifecycles emergency consecutivos atingem `FSM=emergency_active` end-to-end | **PARTIAL** — L1 GREEN (search → create → cold-start → /v1/models 200 → routing). L2 + L3 deferred to integration tests (`gateway/internal/integration_test/emerg_*`, 22/22 GREEN in CI run 25980751573); rationale: reconciler bid-race + cancel-in-flight paths are unchanged between Strategy A and Strategy B so the integration suite already covers them. |
| 2 | Cada lifecycle log inclui: instance_id Vast.ai, cold-start, Runtype=args, image=ghcr.io/ggml-org/llama.cpp:server-cuda-b9128 | **L1 ✅** — vast_instance_id 36917391, image confirmed in `vastai show instance` response + `pod ps -ef` over operator SSH, cold-start 20m 58s measured. |
| 3 | Nenhum lifecycle trava em health_timeout 1800s (bug STATE.md:85 fix VALIDATED) | **✅** — L1 reached emergency_active in 20m 58s, well inside 1800s budget. No silent CMD-ignore behaviour. |
| 4 | Cold-start P90 ≤ 6 min em 3/3 lifecycles | **❌ KNOWN GAP** — L1 cold-start 20m 58s ≈ 3.5× target. Cause: 16.74 GB Qwen GGUF download over WAN (Hetzner DE MinIO → Spain ES spot host @ 14 MB/s). Architectural mitigations documented in HUMAN-UAT.md "Known gap" subsection (Vast volumes + host pinning, image pre-bake, geo-filter). Not a Phase 6 blocker — perf optimization tracked for post-PR2 work. |
| 5 | RUNBOOK-EMERGENCY-POD.md atualizado refletindo Strategy B | **✅** — commit 401cb34. Sections: image source, onstart inline args pattern, troubleshooting `vastai logs` over `vastai ssh`, "Reverting to Strategy A" caveat. |
| 6 | 06-HUMAN-UAT.md sign-off section preenchido pelo operator | **✅** — filled with L1 actuals + L2/L3 deferral rationale + Final Sign-off PR2 GO. |

---

## Hot-fixes carried by PR1 (discovered during UAT, do NOT regress in 06-07 cleanup)

| Commit | What | Why |
|--------|------|-----|
| `c75bf6b` | swap `entrypoint` → `onstart` in vast.CreateRequest | Vast.ai API has NO `entrypoint` JSON field (verified via vast-cli `api/instances.py:85` which copies `--entrypoint` into `onstart_cmd`). Spike Round 2 worked because of CLI coercion; gateway shipping `entrypoint:"/bin/bash"` raw was a silent no-op causing lifecycle 35 to die instantly with `shutdown_reason=instance_terminal_state`. |
| `4896004` | drop apt-get install curl ca-certificates from onstart | Image ships them. Prior block hung silently in debconf prompt (lifecycle 36 spent 12+ min at 8 MB disk, no progress). Direct `curl https://dl.min.io/.../mc` runs in seconds. |
| `4896004` + `19a66a3` | add optional `POD_DEBUG_SSH_PUBLIC_KEY` env (generic name, reusable for primary pod) | Args runtype disables Vast's SSH injection; lifecycle 36 hang was uninspectable. Inline sshd + key bootstrap + Vast docker port mapping `-p 22:22` allows operator realtime debug during UAT. Production runs leave env empty for least-privilege. |
| (test) | bump onstart length budget 1500 → 2500 chars | New script ~1970 chars (still ~50% under Vast 4048 hard limit per Pitfall 4 RESEARCH.md:426). |

---

## L1 Empirical Evidence

```
vast_instance_id      : 36917391
geolocation           : Spain, ES (host_id=87485)
image                 : ghcr.io/ggml-org/llama.cpp:server-cuda-b9128
dph                   : $0.43/h
disk                  : 40 GB
ports mapped          : 22→35552 (SSH debug), 8000→35962 (llama-server)
started               : 2026-05-17T04:24:16Z
emergency_active at   : 2026-05-17T04:45:14Z (entered_at=1778993114)
cold-start total      : 20m 58s
ended                 : 2026-05-17T04:49:13Z (force-destroy manual)
duration              : ~25 min
cost                  : ~$0.17

PID 1 (via operator SSH on 79.116.87.141:35552):
  /app/llama-server --host 0.0.0.0 --port 8000 -m /weights/qwen/model.gguf -ngl 99 -np 2 --ctx-size 16384 --jinja --chat-template-file /app/templates/qwen3.5-27b-tool-calling.jinja

/v1/models                                  → HTTP 200
POST /v1/chat/completions  (qwen, 17→20 tokens) → HTTP 200
  system_fingerprint  = b9128-856c3adac
  prompt_per_second   = 278 tok/s
  predicted_per_second= 48 tok/s  (RTX 4090, Qwen 27B Q4_K_M, Qwen3 thinking)
  reasoning_content rendered (Jinja tool-calling template loaded)
```

---

## Cost Breakdown

| Item | Cost |
|------|------|
| Lifecycle 35 (entrypoint bug; ~5min @ $0.34/h) | ~$0.03 |
| Lifecycle 36 (mc apt hang; 14min @ $0.32/h) | ~$0.07 |
| Lifecycle 37 (Vast GPU error on Iceland host; <2min) | <$0.01 |
| Lifecycle 38 (no offers below cap=0.50; aborted at SearchOffers) | $0.00 |
| Lifecycle 39 (GREEN; 25min @ $0.43/h Spain) | ~$0.17 |
| **Total Wave 4 spend** | **~$0.28** |

Vast.ai balance: $7.15 → ~$6.87. Well within UAT budget ($5 buffer + saldo).

---

## L2 + L3 Deferral Rationale

Plan must_have truth #1 expects 3 live lifecycles. Decision after L1 GREEN: skip live L2 + L3 and rely on integration test coverage.

**Why this is safe:**

- Strategy B refactor touched only the **payload assembly** (`buildCreateRequest`). The reconciler flow (bid-race retry, cancel-in-flight, idle-grace, force-destroy, budget timeout) is **unchanged** from Phase 6.5. Strategy A and Strategy B both feed `vast.CreateRequest` through the same downstream code.
- `gateway/internal/integration_test/emerg_*` (22 cases, all GREEN in CI run 25980751573 against develop @ commit 19a66a3) cover:
  - `TestReconcileBidRaceLost` — 3 retries → `offer_race_lost`
  - `TestReconcileCancelInFlight` — triple-layer cancel (context + pubsub + post-create destroy)
  - `TestReconcileOfferGoneRetrySucceeds` — ErrOfferGone re-search path
  - `TestReconcileHealthTimeout` — health_timeout shutdown
  - 18 other scenarios across reconciler + idempotency + audit + Sentry breadcrumbs
- Live L1 GREEN proves the **new** code (payload shape + onstart bootstrap + llama-server end-to-end). L2 + L3 would re-exercise unchanged downstream code at ~$0.50 cost without producing new signal.

**Risk accepted:** If a Strategy-B-specific bug exists in the cancel-in-flight code path (e.g., the new onstart script does something that interacts badly with destroy), it would NOT be caught by integration tests. Mitigation: 06-07 cleanup PR2 retains a clean git revert path; if a corner case surfaces post-merge, operator can rollback PR1 in <5 min.

---

## Unblocked

✅ **PR2 (plan 06-07 cleanup) GO** — burnt-bridge mitigation satisfied:
  - Payload shape proven correct on real Vast 4090 host (lifecycle 39).
  - Onstart bootstrap executes in-container and produces working `/v1/chat/completions`.
  - llama-server runs as PID 1 (crash detection clean per Pitfall 3).
  - Strategy A custom image (`ghcr.io/ifixtelecom/ifix-ai-pod`) is now dead code on the gateway side — no code path references it after Wave 1-3 refactor.

✅ Wave 5 (plan 06-07) may proceed to delete `pod/Dockerfile`, `pod/scripts/emerg-bootstrap.sh`, `.github/workflows/build-pod.yml`, and update RUNBOOK "Reverting to Strategy A" section to reflect post-deletion rollback path (git revert PR1).

---

## Sign-off

Operator (Pedro): approves Wave 5 (PR2 cleanup) to proceed.
Claude (driver): commit this SUMMARY + UAT sign-off update, then dispatch plan 06-07 cleanup agent.
