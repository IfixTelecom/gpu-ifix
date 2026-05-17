---
phase: 06-emergency-pod-template-refactor
plan: 02
subsystem: gateway/internal/config
tags: [config, env-vars, refactor, phase-6-pr1, strategy-b-locked]
requires: [06-01]
provides:
  - "Cfg.EmergencyTemplateImage (string, default ghcr.io/ggml-org/llama.cpp:server-cuda-b9128)"
  - "Cfg.EmergencyJinjaTemplateKey (string, default production qwen3.5-27b Jinja path)"
  - "Cfg.EmergencyJinjaTemplateSHA256 (string, default 1067302...512e9f67)"
  - "Cfg.EmergencyLlamaArgs ([]string CSV, default nil)"
affects:
  - "gateway/internal/emerg/lifecycle.go (consumes 4 new fields in plan 06-04)"
  - "gateway/internal/integration_test/emerg_leader_test.go (asserts new fields in plan 06-05)"
tech-stack:
  added: []
  patterns: ["envOr/csvOr helper reuse for new fields; non-empty Strategy B Locked defaults per WAVE0-GATES"]
key-files:
  created: []
  modified:
    - "gateway/internal/config/config.go"
    - "gateway/internal/config/config_test.go"
    - "gateway/.env.portainer.dev (git-ignored — operator must mirror to Portainer UI)"
decisions:
  - "Defaults for EmergencyJinjaTemplateKey + EmergencyJinjaTemplateSHA256 are NON-EMPTY per 06-WAVE0-GATES.md Decision 2 (Strategy B2 LOCKED), overriding the plan's must_haves.truths phrasing which suggested empty defaults"
  - "EmergencyPodImageTag fully removed from config.go and config_test.go — lifecycle.go build failure is expected and will be fixed in plan 06-04 (per plan objective)"
metrics:
  duration_minutes: 5
  completed_date: "2026-05-17"
---

# Phase 6 Plan 02: Config Refactor — Strategy B Locked Summary

Substitui o campo `EmergencyPodImageTag` (custom GHCR image legacy) por 4 campos da Strategy B Locked (`EmergencyTemplateImage`, `EmergencyJinjaTemplateKey`, `EmergencyJinjaTemplateSHA256`, `EmergencyLlamaArgs`), com defaults non-empty conforme 06-WAVE0-GATES.md Decision 2 (B2 LOCKED). Wave 1 PR1 paralelo a 06-03; lifecycle.go (06-04) consome os 4 novos fields na Wave 2.

## Output Artifacts

| Path | Change | Commit |
|------|--------|--------|
| `gateway/internal/config/config.go` | -1 field (`EmergencyPodImageTag`) + 4 new fields, comment header updated (`twelve` → `fifteen`) | `881e9c6` |
| `gateway/internal/config/config_test.go` | `phase6OptionalEnv` slice rewritten; `TestLoad_Phase6Defaults` + `TestLoad_Phase6CustomValues` exercise the 4 new fields with non-empty B2 LOCKED defaults | `f2d9c37` (RED) + `881e9c6` (GREEN) |
| `gateway/.env.portainer.dev` | Added Phase 6 Strategy B Locked block (3 active vars; 4th `EMERGENCY_LLAMA_ARGS` commented) | not committed (git-ignored) — operator must mirror to Portainer UI |

## Commits

| Hash | Gate | Type | Description |
|------|------|------|-------------|
| `f2d9c37` | RED | test(06-02) | Add failing tests for Strategy B Locked config fields |
| `881e9c6` | GREEN | feat(06-02) | Replace `EmergencyPodImageTag` with 4 Strategy B Locked fields |

(No REFACTOR commit — the GREEN diff was already minimal and idiomatic.)

## must_haves Truths Validation

| # | Truth (06-02-PLAN.md) | Status |
|---|-----------------------|--------|
| 1 | `EmergencyPodImageTag` field REMOVIDO de Config struct e Load() (config.go) | ✅ `grep -c EmergencyPodImageTag gateway/internal/config/config.go` → 0 |
| 2 | `EMERGENCY_POD_IMAGE_TAG` env var REMOVIDA de `phase6OptionalEnv` e `.env.portainer.dev` | ✅ both 0 occurrences in test slice and active env block |
| 3 | 4 new fields exist in Config struct + Load() | ✅ `EmergencyTemplateImage`, `EmergencyJinjaTemplateKey`, `EmergencyJinjaTemplateSHA256`, `EmergencyLlamaArgs` declared + loaded |
| 4 | `TestLoad_Phase6Defaults` asserts the 4 new defaults without referencing `EmergencyPodImageTag` | ✅ |
| 5 | `TestLoad_Phase6CustomValues` exercises override of the 4 fields with `t.Setenv` | ✅ EMERGENCY_TEMPLATE_IMAGE, EMERGENCY_JINJA_TEMPLATE_KEY, EMERGENCY_JINJA_TEMPLATE_SHA256, EMERGENCY_LLAMA_ARGS overrides asserted |
| 6 | Go build success (sem occurrences remanescentes de `Cfg.EmergencyPodImageTag` fora de lifecycle.go) | ✅ `go build ./internal/config/...` GREEN; `go build ./...` fails ONLY at `internal/emerg/lifecycle.go:658` as expected per plan (06-04 scope) |

## WAVE0-GATES Deviation Applied

The plan's `must_haves.truths` line 21 states the new Jinja key + sha256 fields should default to empty. **Overridden** per `06-WAVE0-GATES.md` Decision 2 (Strategy B2 LOCKED, committed in `b997d25`), which specifies non-empty production MinIO coordinates as the project-wide default:

| Field | Plan-text default | WAVE0-GATES default (applied) |
|-------|-------------------|-------------------------------|
| `EmergencyJinjaTemplateKey` | empty (B1 image overlay) | `emerg-onstart/templates/qwen3.5-27b-tool-calling-1067302cc6d927210a84775b9a060f724da15debc168c79710cbf763512e9f67.jinja` (B2 MinIO fetch) |
| `EmergencyJinjaTemplateSHA256` | empty | `1067302cc6d927210a84775b9a060f724da15debc168c79710cbf763512e9f67` |

Both code (`config.go` Load) and tests (`TestLoad_Phase6Defaults` assertions) reflect the WAVE0-GATES B2 values. Plan template defaults to `os.Getenv` (empty fallback) were replaced with `envOr(..., "<production value>")`. Override via env var works as plan specified — only the *default* is non-empty.

Rationale: WAVE0-GATES.md is the binding operator-level decision artifact for Phase 6; it supersedes any plan-template default whenever the two disagree. See plan `<context>` block which lists `06-CONTEXT.md` and (implicitly) `06-WAVE0-GATES.md` as authoritative sources.

## Build + Test Results

```
$ cd gateway && go test ./internal/config -run TestLoad_Phase6 -count=1 -v
=== RUN   TestLoad_Phase6Defaults
--- PASS: TestLoad_Phase6Defaults (0.00s)
=== RUN   TestLoad_Phase6CustomValues
--- PASS: TestLoad_Phase6CustomValues (0.00s)
=== RUN   TestLoad_Phase6FloatOrBogusValue
--- PASS: TestLoad_Phase6FloatOrBogusValue (0.00s)
PASS
ok   github.com/ifixtelecom/gpu-ifix/gateway/internal/config  0.003s

$ cd gateway && go test ./internal/config/... -count=1
ok   github.com/ifixtelecom/gpu-ifix/gateway/internal/config  0.006s

$ cd gateway && go build ./internal/config/...
(no output — GREEN)

$ cd gateway && go build ./...
# github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg
internal/emerg/lifecycle.go:658:61: r.deps.Cfg.EmergencyPodImageTag undefined
        (type config.Config has no field or method EmergencyPodImageTag)
```

The lifecycle.go failure is **expected and inside plan scope** per plan `<objective>`:
> "Build deve passar parcialmente (lifecycle.go ainda referencia EmergencyPodImageTag — sera fixado em 06-04, mesma wave 2 separada por design)."

The `internal/config` package builds and tests cleanly in isolation.

## Verification Block (per plan `<verification>`)

| Check | Expected | Actual | Result |
|-------|----------|--------|--------|
| `go test ./internal/config -run TestLoad_Phase6 -count=1 -v` | 3 sub-tests GREEN | 3 GREEN | ✅ |
| `grep -c EmergencyPodImageTag gateway/internal/config/config.go` | 0 | 0 | ✅ |
| `grep -c EmergencyPodImageTag gateway/internal/config/config_test.go` | 0 | 0 | ✅ |
| `grep -c EmergencyTemplateImage gateway/internal/config/config.go` | ≥2 | 3 (struct field + Load loader + comment) | ✅ |
| `grep -c '^EMERGENCY_TEMPLATE_IMAGE=' gateway/.env.portainer.dev` | 1 | 1 | ✅ |
| `grep -c '^EMERGENCY_POD_IMAGE_TAG=' gateway/.env.portainer.dev` | 0 | 0 | ✅ |

All 6 verification checks pass.

## Operator Action Required (Portainer UI)

`gateway/.env.portainer.dev` is git-ignored by design (file header lines 1-3). Operator MUST mirror the new Phase 6 block to the Portainer stack `ai-gateway-dev` Environment-variables panel manually BEFORE the next deploy that exercises the 4 new fields (i.e., plans 06-04+). Required updates:

| Action | Variable | Value |
|--------|----------|-------|
| REMOVE | `EMERGENCY_POD_IMAGE_TAG` | (entire line — field no longer read) |
| ADD | `EMERGENCY_TEMPLATE_IMAGE` | `ghcr.io/ggml-org/llama.cpp:server-cuda-b9128` |
| ADD | `EMERGENCY_JINJA_TEMPLATE_KEY` | `emerg-onstart/templates/qwen3.5-27b-tool-calling-1067302cc6d927210a84775b9a060f724da15debc168c79710cbf763512e9f67.jinja` |
| ADD | `EMERGENCY_JINJA_TEMPLATE_SHA256` | `1067302cc6d927210a84775b9a060f724da15debc168c79710cbf763512e9f67` |
| (optional) | `EMERGENCY_LLAMA_ARGS` | leave unset → uses hardcoded const in `lifecycle.go` per D-07-B |

The Go defaults are safe and identical to the values above, so a Portainer stack with NO Phase 6 env vars set will still boot correctly — but the operator should set them anyway so the source-of-truth lives in Portainer (not silently in code).

Verification command for operator after Portainer update + redeploy:
```bash
ssh vps-ifix-vm 'docker exec ai-gateway-dev-api env | grep -E "^EMERGENCY_(TEMPLATE_IMAGE|JINJA|LLAMA)"'
```
Should print 2 or 3 lines (3 if EMERGENCY_LLAMA_ARGS was also set; not required).

## Downstream Plan Dependencies

| Plan | Wave | Consumes |
|------|------|----------|
| 06-04 (lifecycle.go) | 2 | All 4 new fields via `r.deps.Cfg.Emergency*` — replaces line 658 (currently broken) with B2 onstart script construction |
| 06-05 (emerg_leader_test.go) | 3 | Test-side `cfg.EmergencyPodImageTag = "v1.0"` assignment (line 47) must be rewritten to use the new 4 fields |
| 06-06 (errors.go cleanup) | 3 | Comment-only cleanup of stale `EmergencyPodImageTag` references |

## Deviations from Plan

### Configuration

**1. [Rule 3 - Blocking issue + WAVE0-GATES authority] Jinja default values changed from empty to production MinIO coordinates**
- **Found during:** Pre-implementation context read (06-WAVE0-GATES.md b997d25 Decision 2)
- **Issue:** Plan must_haves.truths line 21 said `EmergencyJinjaTemplateKey` and `EmergencyJinjaTemplateSHA256` default to empty, but `06-WAVE0-GATES.md` Decision 2 (committed AFTER the plan was authored) LOCKED Strategy B2 with non-empty production MinIO coordinates as the project-wide default.
- **Fix:** Defaults in `config.go` Load() use `envOr` with the WAVE0-GATES B2 values; test assertions in `TestLoad_Phase6Defaults` assert the same B2 values. Override via env var still works as plan specified.
- **Files modified:** `gateway/internal/config/config.go`, `gateway/internal/config/config_test.go`
- **Commit:** `881e9c6` (code) + `f2d9c37` (test)
- **Rationale:** WAVE0-GATES.md is the binding operator decision artifact for Phase 6; supersedes plan template defaults when the two disagree.

### Scope

**2. [Plan scope, not a deviation] `.env.portainer.dev` not in git**
- **Issue:** File is git-ignored per its own header (`NÃO COMMITAR (já protegido por .gitignore)`). Edits made on disk are visible to the local executor but cannot be committed.
- **Action:** Documented operator-side action in "Operator Action Required" section above.

## Self-Check: PASSED

- ✅ `gateway/internal/config/config.go` exists, modified
- ✅ `gateway/internal/config/config_test.go` exists, modified
- ✅ `gateway/.env.portainer.dev` exists, modified (not committed — git-ignored by design)
- ✅ Commit `f2d9c37` (RED) found in `git log`
- ✅ Commit `881e9c6` (GREEN) found in `git log`
- ✅ Config package tests GREEN (3 sub-tests of TestLoad_Phase6*)
- ✅ All 6 plan verification checks pass
