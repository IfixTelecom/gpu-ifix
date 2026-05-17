---
phase: 06-emergency-pod-template-refactor
plan: 04
subsystem: gateway/emerg
tags: [lifecycle, refactor, strategy-b, phase-6-pr1, core, tdd]
requires: [06-02, 06-03]
provides:
  - "buildCreateRequest emits Strategy B Locked payload (Runtype=args + Entrypoint=/bin/bash + Args=[\"-c\", emergencyOnstart])"
  - "Inline bash bootstrap onstart (raw-string Go const) with MinIO Qwen weights download + sha256-c + optional Jinja fetch"
  - "Hard-coded 13-flag llama-server invocation overridable via Cfg.EmergencyLlamaArgs CSV"
  - "Whisper + BGE-M3 env keys removed (LLM-only emergency pod per Phase 6.5 D-C2)"
  - "9 new unit tests in lifecycle_test.go validating payload, JSON shape, determinism, B1/B2 modes, override path, and security guards"
affects:
  - gateway/internal/emerg/lifecycle.go
  - gateway/internal/emerg/lifecycle_test.go
tech-stack:
  patterns:
    - "Raw-string Go const for shell scripts (Pitfall 9 — eliminates fmt.Sprintf shell quoting bugs)"
    - "Append-only string concatenation for dynamic exec line (no fmt.Sprintf into bash)"
    - "Reconciler{deps: Deps{Cfg: config.Config{...}}} test harness pattern"
key-files:
  created: []
  modified:
    - gateway/internal/emerg/lifecycle.go
    - gateway/internal/emerg/lifecycle_test.go
decisions:
  - "Adopted 06-WAVE0-GATES.md Decision 4 pattern (entrypoint+args 2-element) over plan must_haves truth #6 (15-token verbatim Args slice). Rationale: spike Round 1 empirically proved `--onstart-cmd` does NOT shell-wrap in args runtype; image ENTRYPOINT is llama-server direct so bash override is mandatory."
  - "Used append-only string concatenation `emergencyOnstartHead + \"exec /app/llama-server \" + strings.Join(args, \" \")` instead of a single const + fmt.Sprintf. The llama args slice members are controlled (Go-side default or env-CSV) — no untrusted input crosses into bash, no shell-quoting required."
  - "Onstart length budget: 1015 chars empirical (vs 1500 char safety margin per must_haves truth #4 / vs 4048 Vast hard limit). Margin 485 / 3033 chars."
  - "B1 vs B2 Jinja support via single conditional `if cfg.EmergencyJinjaTemplateKey != \"\"` — env map gets keys only in B2; onstart `if [[ -n \"${EMERGENCY_JINJA_TEMPLATE_KEY:-}\" ]]` block skips fetch in B1."
metrics:
  duration_minutes: ~15
  tasks_completed: 3
  tasks_total: 3
  files_modified: 2
  tests_added: 9
  build_state: green
  vet_state: clean
  commits:
    - 50e606d: "test(06-04): RED — 9 failing tests for Strategy B buildCreateRequest payload"
    - 19942bc: "feat(06-04): GREEN — Strategy B buildCreateRequest (entrypoint+args 2-element)"
  completed: 2026-05-17T02:20:43Z
---

# Phase 6 Plan 04: Core lifecycle.go Refactor Summary

**One-liner:** Replaced `buildCreateRequest` to emit Strategy B Locked payload — `Runtype="args"` + `Entrypoint="/bin/bash"` + 2-element `Args=["-c", emergencyOnstart]` with inline bash bootstrap pulling Qwen weights and optional Jinja from MinIO; eliminates STATE.md:85 `Runtype="ssh"` CMD-ignore bug.

---

## Context

Wave 1 (Plans 06-02 + 06-03) landed two upstream-blocking refactors:

- **06-02** (commit `881e9c6`): Replaced `Cfg.EmergencyPodImageTag` with 4 new fields (`EmergencyTemplateImage`, `EmergencyJinjaTemplateKey`, `EmergencyJinjaTemplateSHA256`, `EmergencyLlamaArgs`).
- **06-03** (commit `d8c322c`): Added `Args []string` + `Entrypoint string` (both omitempty) to `vast.CreateRequest`.

After Wave 1, `go build ./...` was red at `gateway/internal/emerg/lifecycle.go:658` because `buildCreateRequest` still referenced the gone `EmergencyPodImageTag`. Plan 06-04 is the Wave 2 core lifecycle refactor that finishes the Strategy B Locked migration in the gateway code path.

---

## What Was Built

### 1. Package-level declarations (lifecycle.go)

**`emergencyLlamaArgsDefault []string`** — 13-flag canonical llama-server CLI invocation:

```go
var emergencyLlamaArgsDefault = []string{
    "--host", "0.0.0.0",
    "--port", "8000",
    "-m", "/weights/qwen/model.gguf",
    "-ngl", "99",
    "-np", "2",
    "--ctx-size", "16384",
    "--jinja",
    "--chat-template-file", "/app/templates/qwen3.5-27b-tool-calling.jinja",
}
```

**`emergencyOnstartHead string`** — raw-string Go const (backtick-delimited per Pitfall 9 RESEARCH.md:476) carrying the inline bash bootstrap:

```bash
set -e
mkdir -p /weights/qwen /app/templates
if ! command -v mc >/dev/null 2>&1; then
  apt-get update -qq && apt-get install -y -qq curl ca-certificates >/dev/null
  curl -sSL https://dl.min.io/client/mc/release/linux-amd64/mc -o /usr/local/bin/mc
  chmod +x /usr/local/bin/mc
fi
mc alias set ifix "$MINIO_ENDPOINT" "$MINIO_ACCESS_KEY" "$MINIO_SECRET_KEY" >/dev/null
if [ ! -f /weights/qwen/model.gguf ]; then
  mc cp "ifix/${MINIO_BUCKET}/${WEIGHTS_QWEN_KEY}" /weights/qwen/model.gguf
fi
echo "$WEIGHTS_QWEN_SHA256  /weights/qwen/model.gguf" | sha256sum -c -
if [ -n "${EMERGENCY_JINJA_TEMPLATE_KEY:-}" ]; then
  mc cp "ifix/${MINIO_BUCKET}/${EMERGENCY_JINJA_TEMPLATE_KEY}" /app/templates/qwen3.5-27b-tool-calling.jinja
  echo "$EMERGENCY_JINJA_TEMPLATE_SHA256  /app/templates/qwen3.5-27b-tool-calling.jinja" | sha256sum -c -
fi
```

**`buildEmergencyOnstart(llamaArgs []string) string`** — concatenator that appends `exec /app/llama-server <args>\n` to the head. PID 1 becomes llama-server via `exec` replacement so Vast crash detection works (Pitfall 3 RESEARCH.md:414).

### 2. Refactored `buildCreateRequest` (lifecycle.go)

| Field | Before (Strategy A — ssh CMD-ignore bug) | After (Strategy B Locked) |
|-------|-----------------------------------------|---------------------------|
| `Image` | `"ghcr.io/ifixtelecom/ifix-ai-pod:" + Cfg.EmergencyPodImageTag` | `Cfg.EmergencyTemplateImage` (default `ghcr.io/ggml-org/llama.cpp:server-cuda-b9128`) |
| `Runtype` | `"ssh"` (STATE.md:85 bug — CMD silently overridden) | `"args"` (preserves image ENTRYPOINT) |
| `Entrypoint` | (field did not exist on wire) | `"/bin/bash"` (REQUIRED override per spike Round 2) |
| `Args` | (field did not exist on wire) | `["-c", <emergencyOnstart>]` — 2 elements (spike Round 1 proved `--onstart-cmd` doesn't shell-wrap in args mode) |
| `Onstart` | `""` (image's baked CMD ran) | `""` (script lives in `Args[1]`; Vast onstart-cmd unusable in args runtype) |
| `Env` | 10 keys incl. WEIGHTS_WHISPER_* + WEIGHTS_BGE_M3_* | 7-9 keys (MinIO + Qwen + optional Jinja — Whisper/BGE removed per Phase 6.5 D-C2 LLM-only emergency pod) |
| `Disk` | `80` GB | `40` GB (06-WAVE0-GATES.md Decision 1 — opens more spot hosts) |

### 3. Nine new unit tests (lifecycle_test.go)

| Test | Coverage |
|------|----------|
| `TestBuildCreateRequest_StrategyB_args` | Asserts Image, Runtype, Entrypoint, Args length=2, Args[0]="-c", Args[1] contains `exec /app/llama-server` + `--jinja` + Qwen path + WEIGHTS_QWEN_SHA256; Label uses lifecycle ID; Disk=40; LLM-only env (no Whisper, no BGE); MinIO + Qwen + Jinja env keys present |
| `TestBuildCreateRequest_JSONShape` | Marshals payload; asserts top-level `image`/`runtype`/`entrypoint`/`args`/`env`/`disk` present, `image_args`/`args_str` absent, `onstart` empty when present |
| `TestBuildCreateRequest_DeterministicJSON` | 20 successive calls → byte-identical JSON (validates no time.Now, no rand, map key sort stability) |
| `TestBuildCreateRequest_JinjaB1Mode` | Empty `EmergencyJinjaTemplateKey` → no Jinja env keys (B1 fallback path) |
| `TestBuildCreateRequest_JinjaB2Mode` | Populated `EmergencyJinjaTemplateKey` + sha → both env keys forwarded (production B2 default) |
| `TestBuildCreateRequest_LlamaArgsOverride` | Cfg.EmergencyLlamaArgs CSV override replaces hard-coded default in exec line; default flags don't leak |
| `TestEmergencyOnstart_Under1500Chars` | Pitfall 4 length guard (Vast 4048 hard limit, 1500 safety margin) |
| `TestEmergencyOnstart_StartsWithSetE` | T-06-03 mitigation guard (sha256 mismatch must abort container) |
| `TestEmergencyOnstart_NoLegacyImage` | D-08-B / STATE.md:85 bug-fix guard — `ifix-ai-pod` string must not appear anywhere in payload |

---

## Key Decisions

### Decision 1: Pattern revised to entrypoint+args 2-element

**Plan must_haves truth #6** (verbatim): "Args slice contem 15 tokens exatos per CONTEXT.md D-07-B" — superseded by **06-WAVE0-GATES.md Decision 4** based on **06-SPIKE-runtype-args.md** empirical evidence:

- **Round 1 (failed):** Sending `--args --host 0.0.0.0 --port 8000 --version` directly produced runc error: `exec: "echo IN_CONTAINER > /tmp/marker; ...": no such file or directory`. Conclusion: `--onstart-cmd` does NOT shell-wrap in args runtype.
- **Round 2 (succeeded):** `--entrypoint /bin/bash --args -c '<bash-script-with-exec-llama>'` ran cleanly; container PID 1 became llama-server via `exec` replacement; CUDA backend loaded; version output captured.

Plan 06-04 implementation adopts Round 2 pattern. The 13 llama-server flags now live inside the onstart's final `exec /app/llama-server ...` line (built dynamically by `buildEmergencyOnstart`), not in the wire-level `Args` slice.

### Decision 2: Append-only string concatenation (no fmt.Sprintf)

Pitfall 9 (RESEARCH.md:476) bans `fmt.Sprintf` for shell-script assembly because it spawns quoting/escaping bugs when bash variables coexist with Go format verbs. Implementation uses:

```go
return emergencyOnstartHead + "exec /app/llama-server " + strings.Join(llamaArgs, " ") + "\n"
```

The `llamaArgs` slice is controlled (Go-side default const or operator-supplied env-CSV via `Cfg.EmergencyLlamaArgs`) — no untrusted input crosses into bash, so `strings.Join` with space separator is safe. Bash variables (`$MINIO_*`, `$WEIGHTS_QWEN_*`, `$EMERGENCY_JINJA_*`) resolve at script-execution time inside the container via Vast's `Env` map injection.

### Decision 3: Onstart length 1015 chars (empirical)

| Boundary | Value | Margin |
|----------|-------|--------|
| Plan target | 1500 chars | 485 chars under |
| Vast API hard limit (Pitfall 4) | 4048 chars | 3033 chars under |

Probe: temporary test ran `len(req.Args[1])` against `TestBuildCreateRequest_StrategyB_args` fixture → `ONSTART_LEN=1015`. Future growth (e.g., adding a third weights file) has 485 chars headroom before the sentinel test trips.

### Decision 4: B1/B2 Jinja support via single conditional

Single `if cfg.EmergencyJinjaTemplateKey != ""` branch on the Go side (adds 2 env keys); matching `if [[ -n "${EMERGENCY_JINJA_TEMPLATE_KEY:-}" ]]` block on the bash side (skips fetch + sha256 verify). Either mode is exercised by `TestBuildCreateRequest_JinjaB1Mode` / `TestBuildCreateRequest_JinjaB2Mode`. Production default is B2 per 06-WAVE0-GATES.md Decision 1.

---

## Deviations from Plan

### Plan vs WAVE0-GATES Pattern Reconciliation

Plan 06-04's `<interfaces>` block specified the **old D-07-B verbatim pattern** (15-token Args slice starting with `--host 0.0.0.0`). 06-WAVE0-GATES.md Decision 4 — landed AFTER the plan was authored but BEFORE this executor ran — explicitly supersedes this with the entrypoint+args 2-element pattern based on spike Round 2 evidence.

**Action taken:** Followed WAVE0-GATES.md as the authoritative source. No deviation flagged as "Rule 4" (architectural) because:
- WAVE0-GATES.md is operator-signed (Pedro, 2026-05-16)
- The plan's own `<context>` block references `06-WAVE0-GATES.md` as a must-read
- Plan must_haves truth #6 ("Args slice contem 15 tokens exatos") is explicitly called out by WAVE0-GATES as needing update
- Spike file (`06-SPIKE-runtype-args.md`) provides empirical Round 2 evidence

This is **planned plan revision**, not deviation — the orchestrator's hand-off prompt also explicitly warned of this delta with a "🚨 CRITICAL PAYLOAD PATTERN" callout.

### Tests structure

Plan called for "3 unit tests" (orchestrator prompt) / "6 tests" (plan Task 1) / "+2 sentinel tests" (plan Task 3) = 8 tests across 3 phases. Implementation collapsed Tasks 1+2+3 into **one RED commit + one GREEN commit** with **9 tests** (one extra `TestEmergencyOnstart_NoLegacyImage` defensive guard). Rationale: TDD cycle is cleaner with a single test commit; the plan's three-phase structure was a planning artifact rather than a functional requirement.

---

## Threat Model Verification (4/4 mitigated)

| Threat | Mitigation Status |
|--------|-------------------|
| T-06-01 (MinIO creds disclosure) | Env vars travel via `CreateRequest.Env` map; Sentry breadcrumb pattern preserved upstream (no secrets in `json.Marshal(req)` logging — already redacted per Phase 6.5 D-A5) |
| T-06-02 (Onstart shell quoting) | `emergencyOnstartHead` is raw-string Go const (backtick); ZERO `fmt.Sprintf`. `TestEmergencyOnstart_StartsWithSetE` + `TestEmergencyOnstart_Under1500Chars` guards |
| T-06-03 (Jinja template injection) | Onstart contains `sha256sum -c -` against `$EMERGENCY_JINJA_TEMPLATE_SHA256` (pinned to repo file `pod/templates/qwen3.5-27b-tool-calling.jinja.sha256`); `set -e` aborts on mismatch |
| T-06-04 (ghcr.io upstream image) | Tag `b9128` SHA-pinned by ggml-org build; operator can override via `EMERGENCY_TEMPLATE_IMAGE` env (e.g., `@sha256:HEX`); plan 06-04 does not touch tag |

---

## Build/Test/Vet Status

```
$ go build ./...
(green — no output)

$ go test ./gateway/internal/emerg/... -count=1 -timeout 60s
ok  	github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg	4.096s
ok  	github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast	0.028s

$ go vet ./gateway/internal/emerg/...
(clean — no output)

$ go vet ./...
(clean — no output)
```

---

## Success Criteria Mapping

| Plan SC | Status |
|---------|--------|
| SC-1 (HEALTHY → EMERGENCY_PROVISIONING happy path) | unit-test layer: payload assembly verified; UAT in plan 06-06 |
| SC-2 (cold-start P90 ≤6min) | unit-test layer N/A; UAT in plan 06-06 |
| SC-3 (Jinja template pinning) | `TestBuildCreateRequest_JinjaB2Mode` validates env forwarding; sha256-c gate in onstart |
| SC-4 (no custom GHCR image) | `TestEmergencyOnstart_NoLegacyImage` asserts `ifix-ai-pod` string absent everywhere in marshaled payload |
| Bug STATE.md:85 (runtype=ssh CMD-ignore) | Eliminated at unit-test layer — `TestBuildCreateRequest_StrategyB_args` asserts `Runtype == "args"`. UAT in plan 06-06 closes the loop. |

---

## Commits

| Hash | Type | Message |
|------|------|---------|
| `50e606d` | test | RED — 9 failing tests for Strategy B buildCreateRequest payload |
| `19942bc` | feat | GREEN — Strategy B buildCreateRequest (entrypoint+args 2-element) |

---

## Files Modified

| Path | Diff Stats |
|------|------------|
| `gateway/internal/emerg/lifecycle.go` | +136 / -42 (replaced `buildCreateRequest`, added 2 package-level decls + 1 helper, added `strings` import) |
| `gateway/internal/emerg/lifecycle_test.go` | +246 / -0 (added 9 tests + 1 helper constructor, added `strings` + `config` imports) |

Zero files deleted. Other functions in lifecycle.go (`provisionLifecycle`, `filterBelowCap`, `excludeHost`, `mustEventJSON`, `closeLifecycle`, `markHealthy`, `bestEffortDestroy`, `waitForReadyOrDestroy`, `podHealthURL`, `checkHealth`, `pgInt8`, `pgNumericFromFloat`, `captureBreadcrumb`, `captureTerminalSentry`, `SetVastClient`, `SetHealthCheck`, `ActivePodURL`, `IsActive`, `cancelActiveLifecycle`, `startProvisioning`, `spawnProvisionGoroutine`, `errReason`, `stripHealthSuffix`, `calculateCostBRL`) untouched.

---

## Self-Check: PASSED

- ✅ `gateway/internal/emerg/lifecycle.go` contains `Runtype: "args"` (1 match)
- ✅ `gateway/internal/emerg/lifecycle.go` contains `Entrypoint: "/bin/bash"` (1 match)
- ✅ `gateway/internal/emerg/lifecycle.go` contains `emergencyLlamaArgsDefault` (3 matches: var decl + 2 use sites)
- ✅ `gateway/internal/emerg/lifecycle.go` contains `emergencyOnstartHead` (3 matches)
- ✅ `gateway/internal/emerg/lifecycle.go` contains ZERO matches for `EmergencyPodImageTag` or `ifix-ai-pod`
- ✅ `gateway/internal/emerg/lifecycle_test.go` contains 9 new `TestBuildCreateRequest_*` + `TestEmergencyOnstart_*` functions
- ✅ Commit `50e606d` (test RED) exists in git log
- ✅ Commit `19942bc` (feat GREEN) exists in git log
- ✅ `go build ./...` GREEN
- ✅ `go test ./gateway/internal/emerg/... -count=1` GREEN
- ✅ `go vet ./gateway/internal/emerg/...` clean

---

## Next Steps

- **Plan 06-05** (Wave 3): Integration test refactor in `gateway/internal/integration_test/emerg_leader_test.go` — assert Strategy B payload reaches a stub Vast server (currently expects Strategy A wire shape).
- **Plan 06-06** (Wave 4): HUMAN-UAT — provision 3 consecutive lifecycles against real Vast.ai 4090 spot offers; measure cold-start P90; verify llama-server PID 1 + /v1/models 200 + chat completion.
- **Plan 06-07** (Wave 5 PR2): Deferred cleanup — remove `pod/templates/qwen3.5-27b-tool-calling.jinja` from repo if MinIO is sole source; sunset Phase 1 `ifix-ai-pod` image build pipeline.
