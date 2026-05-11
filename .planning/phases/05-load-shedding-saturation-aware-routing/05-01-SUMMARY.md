---
phase: 05-load-shedding-saturation-aware-routing
plan: 01
subsystem: infra
tags: [shed, dcgm, config, prometheus, auditctx, operator-gates]

# Dependency graph
requires:
  - phase: 04
    provides: tenants/upstreams schema + inflight surface, obs scaffolding, auditctx override pattern
provides:
  - prometheus/common@v0.62 promoted to direct dep
  - vegeta@v12.12 added (load-gen for Plan 05-08)
  - sentinel errors for gateway/internal/shed (FSM/registry/scraper)
  - sentinel errors for gateway/internal/dcgm (scraper failure taxonomy)
  - 5 new config env vars (DCGM_EXPORTER_URL, ShedLatencyRingSize, ShedTickIntervalMs, ShedDcgmScrapeIntervalMs, ShedDcgmTimeoutMs)
  - auditctx shed helpers (WithShedDecision, ShedDecisionFromContext, 3 upstream constants)
  - 14 Prometheus collectors registered for shed/dcgm subsystem
  - 3 operator gates resolved (audit_log.upstream type, tenant slug seed policy, DCGM URL boot policy)
affects: [05-02, 05-03, 05-04, 05-05, 05-06, 05-07, 05-08]

# Tech tracking
tech-stack:
  added:
    - github.com/prometheus/common@v0.62 (promoted to direct)
    - github.com/tsenart/vegeta/v12@v12.12 (test-only)
  patterns:
    - "sentinel-error-per-package: shed and dcgm each own their domain errors; no cross-package error reuse"
    - "metric naming: gateway_shed_* / gateway_dcgm_* / gateway_vram_* — three subsystem prefixes for shed phase"
    - "auditctx constants for upstream override values — keep audit string keys out of shed business code"
    - "operator-gates artifact: blocking checkpoint decisions written to WAVE0-GATES.md before downstream plans consume them"

key-files:
  created:
    - gateway/internal/shed/errors.go
    - gateway/internal/dcgm/errors.go
    - gateway/internal/shed/tools_phase5.go
    - .planning/phases/05-load-shedding-saturation-aware-routing/05-WAVE0-GATES.md
  modified:
    - go.mod
    - go.sum
    - gateway/internal/config/config.go
    - gateway/internal/auditctx/override.go
    - gateway/internal/obs/metrics.go

key-decisions:
  - "14 collectors instead of 11 — added GatewayInflightTier1, GatewayShedMirrorReconcile, GatewayShedBlockedSensitive to cover Pitfall 3 (reconcile divergence visibility) and tier-1 dashboarding"
  - "Gate A resolved by source audit (audit_log.upstream is TEXT with no CHECK in migration 0003); migration 0018 becomes docs-only"
  - "Gate B chose default-only path — migration 0016 does not seed per-tenant caps; operator sets caps via gatewayctl post-deploy"
  - "Gate C boots fail-open (DCGM_EXPORTER_URL=\"\") — VRAM signal disabled until pod is live, FSM operates on inflight+P95 alone"

patterns-established:
  - "Operator gate file: blocking human-action checkpoints write decisions to a phase-local WAVE0-GATES.md artifact; downstream plans reference it instead of re-asking the operator"
  - "Fail-open subsystem boot: empty config string disables a subsystem cleanly (DCGM scraper exits its goroutine, ReadMiB returns 0, FSM skips that signal)"

requirements-completed: [LSH-01, LSH-02, LSH-03, LSH-04, LSH-05]

# Metrics
duration: ~3 days (2026-05-09 → 2026-05-11, gate resolution gated on operator availability)
completed: 2026-05-11
---

# Phase 5 Plan 01 — Shed Subsystem Scaffolding + Operator Gates

**Phase 5 dependency surface set: deps promoted, sentinel errors created, config/auditctx/obs extended, and the 3 blocking operator gates resolved so migrations 0016–0018 and DCGM scraping can proceed.**

## Performance

- **Tasks:** 3 (2 autonomous + 1 human-action checkpoint)
- **Files modified:** 5 source files + 1 planning artifact (WAVE0-GATES.md)
- **New collectors:** 14 (3 above RESEARCH's "~11" — within cardinality budget)
- **New env vars:** 5

## Accomplishments

- Wired the shed/dcgm package skeleton (errors only) so Plan 05-03 / 05-04 / 05-05 can `import` without circular fanout.
- Promoted `prometheus/common` to a direct dep so Plan 05-04's `expfmt`-based parser (no regex) is a stable import.
- Added vegeta as a test dep so Plan 05-08's load-gen can be vendored without a phase-edge `go mod` perturbation.
- Closed the 3 operator gates that block downstream work:
  - **Gate A** (audit_log.upstream type) — resolved to TEXT-no-CHECK by reading migration 0003; migration 0018 collapses to docs-only.
  - **Gate B** (tenant slug seeding) — operator chose default-only; migration 0016 adds columns + defaults, no `UPDATE` seed. Risk of slug mismatch (`voice-api` vs `voice_api`) is moot since column defaults are inert.
  - **Gate C** (DCGM_EXPORTER_URL) — no Vast.ai pod active; boot with empty URL → VRAM signal disabled → FSM still operates on inflight + P95.

## Task Commits

1. **Task 1.1 — Promote deps + scaffold sentinel errors** — `d320ba1` (feat: scaffold shed+dcgm packages and promote phase-5 deps)
2. **Task 1.2 — Env config + auditctx shed helpers + obs collectors** — `f2464ea` (feat: extend config/auditctx/obs for phase-5 shed subsystem)
3. **Task 1.3 — Operator gates (checkpoint:human-action)** — to be committed with this SUMMARY (`docs(05-01): close operator gates A/B/C + plan summary`)

## Files Created / Modified

- `gateway/internal/shed/errors.go` — domain sentinels (FSM, registry, latency, mirror)
- `gateway/internal/dcgm/errors.go` — scrape failure taxonomy (http_error, status_<n>, parse_error, metric_missing, metric_not_gauge, sanity_check)
- `gateway/internal/shed/tools_phase5.go` — internal use of prometheus/common (keeps it a real direct dep so `go mod tidy` cannot demote it before Plan 05-04 lands)
- `gateway/internal/config/config.go` — +5 env-loaded fields; preserve existing config struct conventions
- `gateway/internal/auditctx/override.go` — +WithShedDecision / ShedDecisionFromContext + 3 audit value constants
- `gateway/internal/obs/metrics.go` — +14 collectors (Inflight×3, Shed×8, Vram, P95, DcgmScrapeFailures)
- `go.mod` / `go.sum` — prometheus/common direct, vegeta added
- `.planning/phases/05-load-shedding-saturation-aware-routing/05-WAVE0-GATES.md` — gate-resolution artifact

## Decisions Made

See key-decisions in frontmatter. The substantive one is Gate C: booting fail-open means Phase 5 ships even if Phase 1's HUMAN-UAT smoke is still pending — operators can wire DCGM after the fact via Portainer env update + container restart. This intentionally couples Phase 5 release to a less-fragile prerequisite.

## Deviations from Plan

### Collector count drift (advisory)

- **Plan said:** "11 collectors registered."
- **Implemented:** 14 collectors.
- **Why:** RESEARCH Pitfall 3 demands reconcile observability (`gateway_shed_mirror_reconcile_total`), tier-1 dashboarding requires `gateway_inflight_tier1`, and the sensitive-tenant block path needs `gateway_shed_blocked_sensitive_total` for LGPD reporting. All 14 share the existing `{upstream}` / `{upstream,tenant}` / `{reason}` label dimensions — cardinality stays well under the 10k-series budget (~63 series at 3 upstreams × 6 tenants).
- **Impact downstream:** Plan 05-05 (mirror) and Plan 05-06 (middleware) reference the additional names; Plan 05-08 integration tests assert against the 14-name set.

No other deviations.
