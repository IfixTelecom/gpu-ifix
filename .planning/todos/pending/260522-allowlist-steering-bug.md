---
created: 2026-05-22
resolved: 2026-05-22
priority: high
source: 06.8-03 live-UAT
resolution_plan: 06.8-05
tags: [gateway, primary, allowlist, vast, bug, resolved]
---

# BUG (RESOLVED): PRIMARY_VAST_MACHINE_ALLOWLIST did not steer primary offer selection

Discovered during Phase 06.8 Plan 03 live-UAT (2026-05-22).

**Symptom:** the primary reconciler's allowlist-first preference pass is not effective.
With `PRIMARY_VAST_MACHINE_ALLOWLIST=43803` (host confirmed available, $0.401, rel 0.995),
`gatewayctl primary force-up` still picked an unrelated global-cheapest host (47525 Quebec).
Same with a 4-host allowlist → picked 76546 California (not in list). Selection always
broadens to the global-cheapest qualifying offer regardless of the allowlist env.

**Impact:** cannot deterministically target a known-good-CDI 2×3090 host (43803). Combined
with widespread broken-CDI on 2×3090 hosts (55942, 45778 confirmed), the gateway cannot
reliably bring a 2×3090 primary pod to markReady. Blocks GW-2GPU full validation (Phase 06.8).

**Where:** gateway/internal/config/config.go L196 (PrimaryVastMachineAllowlist parsed) +
gateway/internal/emerg/vast/types.go WithMachineAllowlist (L258) — verify the primary
reconciler actually calls the allowlist-scoped search FIRST and only broadens on zero offers.
The runtime evidence says the allowlist pass is skipped or returns the broadened result.

**Fix + re-test:** wire/repair the allowlist-first pass; then re-run the 06.8-03 2×3090
force-up UAT targeting 43803 → expect markReady + LLM/STT/TTS/DCGM 200 + nvidia-smi split.

**Catalog from this session:** broken-CDI machine_ids 55942, 45778 (now in dev blocklist);
known-good 43803 (Estonia, spike-proven).

## RESOLVED 2026-05-22 — root cause was deploy staleness, not a code bug (Plan 06.8-05)

**Diagnosis:** `.planning/phases/06.8-multi-pod-gpu-topology-sizing-stt-fix/06.8-ALLOWLIST-DIAGNOSIS.md` (commit `37a91cb`). The deployed `ai-gateway-dev` binary at the time of the 06.8-03 PARTIAL was built `2026-05-21T21:13:54Z` — ~2h20m **before** commit `6f57698` (the allowlist feature, `2026-05-21T23:33Z`). `strings | grep` on the deployed binary returned **0 hits** for `PRIMARY_VAST_MACHINE_ALLOWLIST` / `WithMachineAllowlist` / `PrimaryVastMachineAllowlist` / `primary allowlist exhausted`. The Portainer webhook fired but the local Docker daemon retained the previous digest (matches the STATE.md 2026-05-19 documented "Container redeploy gotcha"). Source on `develop` is correct; hypotheses H1 (wire encoding) and H2 (`FilterBelowCap` eliminates) were DISPROVEN with live evidence.

**Repair landed:**
- `d689321` test(gateway/vast): composition sub-test asserting `WithMachineAllowlist∘DefaultSearchFilter` preserves all DefaultSearchFilter fields → CI guard against future refactors stripping the allowlist branch unnoticed.
- Deploy: `docker compose up -d --force-recreate gateway` on `vps-ifix-vm` after the CI build of `d689321` landed on GHCR (digest `sha256:1890ecdc7dee…`). Allowlist symbols verified present in the running binary.

**Re-UAT evidence (`06.8-GW-2GPU-LIVE-UAT.md` "Re-UAT 2026-05-22 (Plan 06.8-05) — PASS" section):** `PRIMARY_VAST_MACHINE_ALLOWLIST=55158` (substitute for 43803, which was transient `rentable:false`) → gateway picked offer 31139421 machine_id=55158 host_id=167329 dph $0.482 Germany DE, **no `primary allowlist exhausted` log line, no broaden**. markReady reached 10m51s later. 4 endpoints 200 (incl. STT `/v1/audio/transcriptions` 200 with correct PT-BR transcription). `nvidia-smi` 2-GPU split. Force-down clean; total cost ~$0.10.

**Catalog update:** known-good 2×3090 hosts (currently safe for allowlist): **43803** (Estonia, spike-proven 2026-05-21), **55158** (Germany, re-UAT-proven 2026-05-22). Broken-CDI blocklist unchanged: 55942, 45778. Hosts to avoid for cold-start budget reasons (transatlantic MinIO-DE): 76546 California.
