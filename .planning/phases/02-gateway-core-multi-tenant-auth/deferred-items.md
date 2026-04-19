# Deferred items — Phase 2

Items encountered during execution that are OUT OF SCOPE for the current plan
and must be addressed later.

---

## 2026-04-19 — Plan 02-08

### 1. `go test ./gateway/internal/auth/... -race` exceeds 5 min budget

**Found during:** Task 2 verification pass (`go test ./gateway/... -race -timeout=5m`).

**Symptom:** `gateway/internal/auth` package runs in ~104 s without `-race`, but
times out at 300 s under `-race` (argon2id pure-Go path is serialized under the
race detector and the auth test suite runs dozens of hashes serially).

**Scope:** Pre-existing — introduced by Plan 02-03 (argon2id API-key hashing)
and exercised heavily by Plan 02-07 integration tests. NOT caused by 02-08
Dockerfile/workflow changes.

**Impact on CI:** The new `build-gateway.yml` calls
`go test ./gateway/... ./pkg/openai/... ./pod/... -count=1 -race -timeout=5m`
— on the GitHub-hosted runner (4 vCPU, faster than local dev VPS for single-
thread work) argon2id with `-race` should finish inside the budget, but it is
close to the edge. If CI flakes on first run, the fix is one of:

- Bump the workflow's `go test` timeout to `-timeout=10m`
- Split the `auth` tests into a `testing.Short()`-gated fast set and a
  `-short`-skipped slow set; the CI `test` job runs `-short` and the
  `integration-test` job runs the full set without `-race`
- Move argon2id tests into the `integration` build-tag bucket

**Deferred owner:** Next plan that touches CI timings (potentially 02-09 if the
audit export tests push wall time further) or the first time this times out
in a real CI run.
