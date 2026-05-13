# Phase 06 — Deferred Items

Out-of-scope discoveries logged during Plan execution. Each entry must
identify (a) where it was found, (b) what the issue is, (c) why it is
out-of-scope for the current plan, (d) when/where it should be addressed.

---

## 1. Pre-existing flake: `gateway/internal/auth` argon2id test hangs

- **Found during:** Plan 06-01, Task 2 verification (`go test -race
  ./gateway/internal/...`).
- **Issue:** `TestGenerateAPIKey_UniquePer1000` (and the surrounding
  argon2id-heavy tests) hang past 90s without `-race` and past 60s with
  `-race`. The test allocates argon2id hashes 1000 times and clearly
  exceeds the default test timeout on this VM (CPU-bound).
- **Confirmed pre-existing:** Stashed all Plan 06-01 changes and re-ran
  the same command on baseline (commit 9726632 minus this plan's edits)
  — the test still hangs. Phase 6 changes do **not** import or modify
  any code path under `gateway/internal/auth/`.
- **Out-of-scope rationale:** Scope boundary rule — Plan 06-01 only
  scaffolds `gateway/internal/emerg/`, `gateway/internal/config/`,
  `gateway/internal/obs/`, and `go.mod`. The auth package is untouched.
- **When to address:** Schedule a separate quick-fix plan (`/gsd:quick`)
  to either bump the test timeout (`-timeout 180s` in CI), reduce the
  iteration count in `TestGenerateAPIKey_UniquePer1000`, or run the
  argon2id sweep behind a `-short` skip. Do **not** block Phase 6 on
  this — Phase 6 plans 02-11 do not exercise this test path.
