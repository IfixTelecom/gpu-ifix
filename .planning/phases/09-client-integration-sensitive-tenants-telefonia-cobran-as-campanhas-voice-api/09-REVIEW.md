---
phase: 09-client-integration-sensitive-tenants-telefonia-cobran-as-campanhas-voice-api
reviewed: 2026-05-14T00:00:00Z
depth: standard
files_reviewed: 3
files_reviewed_list:
  - scripts/integration-smoke/provision-tenants.sh
  - scripts/integration-smoke/smoke-sensitive-failover.py
  - scripts/integration-smoke/sensitive-failover-report-schema.json
findings:
  critical: 0
  warning: 1
  info: 4
  total: 5
status: issues_found
---

# Phase 9: Code Review Report (Re-review — iteration 1)

**Reviewed:** 2026-05-14T00:00:00Z
**Depth:** standard
**Files Reviewed:** 3
**Status:** issues_found

## Summary

Re-review of the Phase 9 sensitive-tenant integration-smoke trio after the
gsd-code-fixer applied fixes for the prior BLOCKER (CR-01) + all 5 WARNINGs.

**The BLOCKER (CR-01) is fully resolved.** Both halves landed correctly:

1. Empty/missing `X-Request-ID` is now a HARD precondition
   (`smoke-sensitive-failover.py:639-649`): the script writes an *unevaluated*
   report and `return 1` before the streaming step and the audit gates ever
   run — no longer a soft audit-gate miss that leaves `fail_closed` reading
   GREEN.
2. `never_external_ok` is now conjunctive with `fail_closed["ok"]`
   (`:687-691`): a 200-with-audit-row can no longer read `never_external: true`
   because `fail_closed["ok"]` requires status 503 + envelope + `Retry-After:
   30`. Traced the genuine-pass path — all three conjuncts are True on a real
   RES-08 fail-closed, so the stricter gate does NOT false-RED a real pass.

**All 5 WARNINGs are resolved:**
- WR-01: `-1` sentinel is now written raw (`:711`, `:810`) — the `max(...,0)`
  mask is gone; schema widened to `minimum: -1` with a documented meaning;
  gate logic still checks `== 0` on the raw value so `-1` never passes.
- WR-02: streaming gate is documented as timing+status-only in four places
  (module docstring `:26-32`, function docstring `:396-401`, `apply_gates`
  docstring `:523-525`, schema `description` `:85`).
- WR-03: parallel-array length-equality assertions added
  (`provision-tenants.sh:126-131`) for both the tenant and quota array groups.
- WR-04: `TENANT_DATA_CLASS` is now the single source of truth — the key-mint
  loop (`:254-258`) iterates `TENANT_SLUGS` + `TENANT_DATA_CLASS` by index;
  the inline data_class literals are gone.
- WR-05: `jsonschema` is imported at module top (`:101-102`) — a missing dep
  is now a startup error; `ValidationError` forces `errors[]` + `return 1`
  (`:762-776`); the unevaluated-report path re-raises on schema-invalid
  (`:829-836`).

**No new BLOCKERs or WARNINGs from the schema change or the conjunctive gate.**
The schema still rejects genuinely-bad values (`-2` and below, non-integers);
`additionalProperties: false` is intact on every object. The conjunctive
`never_external` gate is not over-strict.

**One new WARNING** was introduced by the WR-04 bash refactor: under `--dry-run`
the new command-substitution-into-associative-array pattern swallows the
`[dry-run] would run:` echo lines for the key-mint step, so `--dry-run` no
longer shows what the `key create` calls would do. Detail below. The four prior
INFO items remain (none were in fix scope).

## Warnings

### WR-06: `--dry-run` no longer echoes the `key create` commands — the WR-04 refactor captures the dry-run output into the key array instead of printing it

**File:** `scripts/integration-smoke/provision-tenants.sh:138-144`, `:233-258`

**Issue:** The WR-04 fix moved key minting into
`TENANT_KEYS["$slug"]="$(mint_tenant_key "$slug" "$dc")"` (line 257) — a
command substitution. Under `--dry-run`, `mint_tenant_key` calls
`run_gatewayctl key create ...`, and `run_gatewayctl` in dry-run mode does
`printf '[dry-run] would run: ...'` to **stdout** (line 140). Because the call
is now inside `$(...)`, that `[dry-run] would run:` line is captured into
`TENANT_KEYS["$slug"]` instead of reaching the operator's terminal. The script
then exits at line 271 (`dry-run complete`) before the final heredoc, so the
captured value is silently discarded. Net effect: `--dry-run` still echoes the
`tenant create` and `tenant set-quota` commands (those are not in a command
substitution) but is now **silent about the 4 `key create` commands** — the
exact step `--dry-run` exists to preview. The `admin-key create` call (line
261) is not in a substitution, so it still echoes correctly; only the
per-tenant `key create` lines regressed.

This is a dry-run-fidelity regression, not a security or correctness bug in the
real (`--mint-keys`, non-dry-run) path — that path is correct: idempotency
(mint gated behind `--mint-keys`), secret-once discipline (keys only in the
assoc array + the one stdout heredoc, never `log()`), and no array desync
(single-array iteration) are all preserved.

**Fix:** In `mint_tenant_key`, echo the dry-run line to stderr (or via `log`)
instead of letting `run_gatewayctl`'s stdout printf be captured. Simplest:
special-case dry-run inside `mint_tenant_key` so the would-run line is logged,
not returned:

```bash
mint_tenant_key() {
  local slug="$1" data_class="$2"
  run_gatewayctl key create --tenant "$slug" --data-class "$data_class"
  if [[ "$DRY_RUN" -eq 1 ]]; then
    log "[dry-run] would mint key for $slug (data_class=$data_class)"
    return 0
  fi
  ...
}
```

(`run_gatewayctl`'s own dry-run `printf` then goes to the captured-and-discarded
substitution as before, but the `log` line surfaces the intent on stderr.)
Alternatively, have `run_gatewayctl` send its `[dry-run] would run:` line to
stderr.

## Info

(Carried forward from the prior review — none were in the fix scope. Re-listed
for completeness; all remain valid and all are cosmetic / defensive.)

### IN-01: `print_operator_prestep` always prints even when the breaker is already open

**File:** `scripts/integration-smoke/smoke-sensitive-failover.py:602`

**Issue:** In `operator-prestep` mode the multi-line instruction block is
printed unconditionally before `ensure_tier0_open` polls. If the operator
already tripped the breaker, the smoke confirms `open` on the first poll but
the operator was still shown a wall of "do this first" instructions.

**Fix:** Do a single fast probe first; only print the prestep block if
`local-llm` is not already `open`.

### IN-02: `git_sha` best-effort block silently swallows all exceptions

**File:** `scripts/integration-smoke/smoke-sensitive-failover.py:746-747`

**Issue:** `except Exception: pass` discards everything, including non-git
errors. `git_sha` is genuinely optional per the schema so this is acceptable,
but a bare `pass` with no log line means a missing `git_sha` is
indistinguishable from "git not present" vs "subprocess bug."

**Fix:** Add a `log.debug` in the handler.

### IN-03: `GW_OUT` from a previous gatewayctl call could leak into a future no-output code path

**File:** `scripts/integration-smoke/provision-tenants.sh:136-149`

**Issue:** `GW_OUT` / `GW_RC` are module-level vars reused across every
`run_gatewayctl` call. `run_gatewayctl` always reassigns both, so current code
is correct — but a future edit that reads `GW_OUT` without calling
`run_gatewayctl` first would read a stale value.

**Fix:** Reset `GW_OUT=""; GW_RC=0` at the top of `run_gatewayctl` before the
`set +e` block for defensiveness.

### IN-04: schema allows `retry_after` as string OR integer but the script only ever emits a string

**File:** `scripts/integration-smoke/sensitive-failover-report-schema.json:45`, `smoke-sensitive-failover.py:359`

**Issue:** Schema `fail_closed.retry_after` is `type: ["string", "integer"]`
but `run_fail_closed_request` always assigns `r.headers.get("Retry-After", "")`
— always a string. The integer branch of the union is dead relative to this
producer.

**Fix:** Tighten to `type: string` or document why the union exists.

---

_Reviewed: 2026-05-14T00:00:00Z_
_Reviewer: Claude (gsd-code-reviewer)_
_Depth: standard_
