---
phase: 09-client-integration-sensitive-tenants-telefonia-cobran-as-campanhas-voice-api
reviewed: 2026-05-14T00:00:00Z
depth: standard
files_reviewed: 1
files_reviewed_list:
  - scripts/integration-smoke/provision-tenants.sh
findings:
  critical: 0
  warning: 0
  info: 4
  total: 4
status: clean
---

# Phase 9: Code Review Report (Final re-review — iteration 2)

**Reviewed:** 2026-05-14T00:00:00Z
**Depth:** standard
**Files Reviewed:** 1
**Status:** clean

## Summary

Final re-review (iteration 2 of the `--auto` fix loop), scoped to the single
file the gsd-code-fixer touched in commit c94eb07: `provision-tenants.sh`. The
prior BLOCKER (CR-01) + all 5 original WARNINGs were resolved in iteration 1;
the iteration-1 re-review surfaced one new WARNING (WR-06 — the WR-04 bash
refactor made `--dry-run` silent about the 4 per-tenant `key create` commands).

**WR-06 is resolved.** The fix adds a stderr `log` line inside
`mint_tenant_key`'s dry-run branch (`provision-tenants.sh:246`), in a branch
that already `return 0`s:

```bash
if [[ "$DRY_RUN" -eq 1 ]]; then
  log "[dry-run] would mint tenant key for '$slug' (data_class=$data_class)"
  return 0
fi
```

Traced the `--dry-run --mint-keys` path: the caller loop (`:264-268`) invokes
`TENANT_KEYS["$slug"]="$(mint_tenant_key "$slug" "$dc")"` once per tenant (4×).
`run_gatewayctl`'s `[dry-run] would run:` printf still goes to stdout and is
still swallowed by the `$(...)` substitution — but the new `log` line writes to
**stderr** (`log()` at `:62` ends `>&2`), which `$(...)` does not capture, so it
reaches the operator's terminal. Net effect: `--dry-run --mint-keys` now emits 4
per-tenant key-mint-intent lines (telefonia/sensitive, cobrancas/sensitive,
campanhas/normal, voice-api/normal) — the exact preview that regressed.

**No new bug introduced.** Verified all four invariants the fix could plausibly
break:

- **Secret-once discipline** — the new line interpolates only `$slug` and
  `$data_class`, both sourced from the static `TENANT_SLUGS` / `TENANT_DATA_CLASS`
  arrays; no key material. Under `--dry-run` no `key create` runs, `parse_key` /
  `parse_id` are never reached (the `return 0` short-circuits), so there is no
  real key to leak.
- **Idempotency** — unchanged. Mint stays gated behind `--mint-keys` (`:201`);
  `--dry-run` still exits at `:279-282` before the stdout heredoc. No DB writes.
- **`TENANT_DATA_CLASS` single source of truth** — the new line *reads* the
  `data_class` function parameter (fed from `TENANT_DATA_CLASS[$i]`); it does not
  re-state an inline literal. The WR-04 single-source-of-truth invariant holds.
- **Array indexing** — no change to loop bounds; the caller loop still uses
  `"${!TENANT_SLUGS[@]}"` behind the WR-03 length guard (`:126-128`). The new
  line is in the function body, not the loop control.

`set -euo pipefail` interaction is also clean: `log` returns 0, `return 0`
follows, the function exits 0, the `$(...)` assignment succeeds — identical
control flow to pre-fix.

**All Phase 9 BLOCKER + WARNING findings are now resolved** (CR-01 + WR-01
through WR-06). Only the 4 non-blocking INFO items remain — none were in any fix
scope; all are cosmetic / defensive. They are carried forward below for
completeness and do not gate this phase.

## Info

(Carried forward — none in fix scope; all cosmetic / defensive. IN-01, IN-02,
and IN-04 reference `smoke-sensitive-failover.py` /
`sensitive-failover-report-schema.json`, which were out of this iteration's
single-file scope but reviewed clean in iteration 1.)

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
