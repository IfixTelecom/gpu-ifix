---
phase: 07-observability-dashboard-alerting
plan: 04
subsystem: infra
tags: [go, alerting, chatwoot, clickup, brevo, gobreaker, backoff, smtp, prometheus]

# Dependency graph
requires:
  - phase: 07-observability-dashboard-alerting
    provides: "07-01 — 13 optional Chatwoot/ClickUp/Brevo Config fields, build-tag-free FakeChatwoot/FakeClickUp/FakeBrevo recording fakes, obs cardinality-budget header"
  - phase: 02-gateway-http-go
    provides: "obs/metrics.go promauto collectors, the external-HTTP-client pattern (emerg/vast/client.go), proxy/retry.go backoff pattern, breaker/breaker.go gobreaker pattern"
provides:
  - "redisx/alert.go — gw:alert:dedup: key namespace const + AlertDedupKey builder (07-05's dedup gate keys against this)"
  - "alert/client.go — the Channel interface + Message struct + Severity type (the interface-first contract 07-05's alerter and 07-06's main.go program against)"
  - "alert/chatwoot.go — Chatwoot Application API client (gobreaker-wrapped, api_access_token isolated)"
  - "alert/clickup.go — ClickUp v2 task client (backoff.Permanent for 4xx-except-429, X-RateLimit-Reset awareness, gobreaker-wrapped)"
  - "alert/brevo.go — net/smtp Brevo sender (gobreaker + short backoff retry, injectable sendMail)"
  - "obs.AlertSendsTotal — gateway_alert_sends_total counter (channel x result, 6 series)"
affects: [07-05, 07-06]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Alert delivery channel: each external client implements alert.Channel, wraps its outbound call in its own gobreaker.CircuitBreaker, isolates its credential in one setAuthHeader/smtpAuth method, and returns secret-free status sentinels"
    - "Breaker-gated HTTP: the gobreaker fn must itself return an error for non-2xx (otherwise gobreaker counts an HTTP 500 as success and never trips) — the non-2xx body is drained+closed inside fn, the live *http.Response is returned only on 2xx"
    - "ClickUp resilience stack re-expressed in Go: backoff.Retry (classify) -> gobreaker (5xx/network only via IsSuccessful) -> adaptiveRateLimiter (X-RateLimit-* header awareness), mirroring the cobrancas-api TS pattern with zero new deps"

key-files:
  created:
    - gateway/internal/redisx/alert.go
    - gateway/internal/alert/client.go
    - gateway/internal/alert/chatwoot.go
    - gateway/internal/alert/chatwoot_test.go
    - gateway/internal/alert/clickup.go
    - gateway/internal/alert/clickup_test.go
    - gateway/internal/alert/brevo.go
    - gateway/internal/alert/brevo_test.go
  modified:
    - gateway/internal/obs/metrics.go

key-decisions:
  - "The gobreaker fn returns a typed error for non-2xx status so a 500 actually trips the breaker — discovered by the first failing TestChatwoot_Send_BreakerOpens run; gobreaker only counts fn-returned errors as failures, and the original code classified status AFTER cb.Execute returned"
  - "ClickUp breaker IsSuccessful excludes 4xx (incl. 401, 429) from the failure count — a 4xx is a client-side/throttle fault, not a ClickUp-health signal — same philosophy as breaker.IsSuccessful; only 5xx + network trip the breaker"
  - "BrevoClient.sendMail is an injectable func field (defaults to net/smtp.SendMail) so the tests drive success/failure/breaker-open without a live SMTP relay — net/smtp has no context-aware/mockable seam otherwise"
  - "clickupHTTPError + clickupRateLimitError are typed errors carrying only the status code — lets both the gobreaker IsSuccessful filter and the backoff classifier read the status without string-matching, and keeps the URL/token out of every error string"

patterns-established:
  - "alert.Channel implementation checklist: var _ Channel = (*XClient)(nil) compile assertion, one credential-isolation method, own gobreaker, secret-free sentinel errors, obs.AlertSendsTotal increment on both ok and err paths"
  - "Test constructor convention: NewXClientWithBaseURL(cfg, url) points the client at an httptest.Server, mirroring emerg/vast/client.go's NewClientWithBaseURL"

requirements-completed: [OBS-04, OBS-05]

# Metrics
duration: ~25min
completed: 2026-05-14
---

# Phase 7 Plan 04: Outbound Alert Clients + Redis Alert-Helper Summary

**The three leaf-node alert delivery clients — Chatwoot (Application API / WhatsApp), ClickUp (v2 task creation), Brevo (net/smtp) — each implementing the `alert.Channel` contract, wrapped in its own gobreaker, with credentials isolated to one method and secret-free errors; plus the `gw:alert:dedup:` Redis key namespace and the `gateway_alert_sends_total` counter.**

## Performance

- **Duration:** ~25 min
- **Started:** 2026-05-14T (worktree execution)
- **Completed:** 2026-05-14
- **Tasks:** 3
- **Files modified:** 9 (8 created, 1 modified)

## Accomplishments

- **Interface-first contract** — `alert/client.go` defines the `Channel` interface (`Name()` + `Send(ctx, Message) error`), the `Message` struct (`Severity`, `Title`, `Body`, `Fingerprint`), and the `Severity` type. 07-05's alerter and 07-06's main.go program against this, never against a concrete client.
- **Redis alert namespace** — `redisx/alert.go` owns `AlertDedupKeyPrefix = "gw:alert:dedup:"` + the `AlertDedupKey(fingerprint)` builder, following the `emerg.go`/`shed.go` key-builder convention. The SET NX dedup policy itself is deliberately left to `alert/dedup.go` (07-05).
- **Chatwoot client** — `chatwoot.go`: POSTs a conversation+message to the Application API (`/api/v1/accounts/{id}/conversations`), `api_access_token` set in exactly one `setAuthHeader` method (`grep -c` returns 1), gobreaker-wrapped, status sentinels that never embed the URL or token.
- **ClickUp client** — `clickup.go`: creates a v2 task with raw-token auth (no `Bearer` prefix), classifies 4xx-except-429 as `backoff.Permanent` (Pitfall 6 — a static-token 401 is unrecoverable, so the retry stops at one server hit), honors `X-RateLimit-Reset` on 429 via `backoff.RetryAfter` + an `adaptiveRateLimiter` that reads `X-RateLimit-*` headers, gobreaker-wrapped with an `IsSuccessful` that excludes 4xx.
- **Brevo client** — `brevo.go`: sends a plain-text RFC822 message via `net/smtp.SendMail` (`smtp.PlainAuth` isolated in one method), wrapped in a gobreaker + a short bounded backoff retry; `sendMail` is an injectable func field for testing.
- **Metric** — `obs.AlertSendsTotal` (`gateway_alert_sends_total`, labels `channel` × `result` = 6 series) added under the existing Phase 7 cardinality-budget header.
- **Zero new Go dependencies** — only stdlib (`net/http`, `net/smtp`) + the already-vendored `cenkalti/backoff/v5` and `sony/gobreaker/v2`.

## Task Commits

Each task was committed atomically:

1. **Task 1: redisx/alert.go + alert/client.go contract + obs counter** - `f616f9d` (feat)
2. **Task 2: Chatwoot + Brevo clients** - `6bf9ca4` (feat)
3. **Task 3: ClickUp client (backoff.Permanent + rate-limit awareness)** - `708a122` (feat)

**Plan metadata:** committed with this SUMMARY (docs: complete plan)

## Files Created/Modified

- `gateway/internal/redisx/alert.go` - `gw:alert:dedup:` key namespace const + `AlertDedupKey` builder; doc explains the 5-minute TTL dedup philosophy and the policy/key-layout split with `alert/dedup.go`
- `gateway/internal/alert/client.go` - `Channel` interface, `Message` struct, `Severity` type + `SeverityCritical`/`SeverityWarning` consts — the interface-first contract
- `gateway/internal/alert/chatwoot.go` - Chatwoot Application API client; `ChatwootConfig`, dual constructor, `setAuthHeader` (one `api_access_token` site), gobreaker, secret-free `statusError`
- `gateway/internal/alert/chatwoot_test.go` - success (path/header/body asserts), secret-free 500 error, breaker-opens-after-N
- `gateway/internal/alert/clickup.go` - ClickUp v2 task client; `ClickUpConfig`, dual constructor, `setAuthHeader` (raw token), `adaptiveRateLimiter`, `clickupHTTPError`/`clickupRateLimitError` typed errors, `classify` backoff verdict mapper, gobreaker with 4xx-excluding `IsSuccessful`
- `gateway/internal/alert/clickup_test.go` - success (raw-token assert), 401-not-retried (exactly 1 hit), 500-is-retried, 429-honors-X-RateLimit-Reset
- `gateway/internal/alert/brevo.go` - Brevo `net/smtp` sender; `BrevoConfig`, `NewBrevoClient`, `smtpAuth` (one `smtp.PlainAuth` site), `buildMessage` RFC822 builder, gobreaker + bounded backoff, injectable `sendMail`
- `gateway/internal/alert/brevo_test.go` - success (addr/from/to/headers asserts), secret-free SMTP-failure error, breaker-opens-after-N
- `gateway/internal/obs/metrics.go` - added `AlertSendsTotal` counter + updated the Phase 7 cardinality-budget header (~111 series total)

## Decisions Made

- **The gobreaker fn must return an error for non-2xx.** gobreaker only counts the `fn` passed to `cb.Execute` as a failure when that `fn` returns an error. The original Chatwoot `Send` did the HTTP call inside `fn` (returning `(resp, nil)` even on a 500) and classified the status *after* `cb.Execute` returned — so the breaker never tripped. Fixed so `fn` returns a typed status error for non-2xx (draining+closing the body inside `fn`); the live `*http.Response` is returned only on 2xx. This is now an established pattern note for the channel-implementation checklist.
- **ClickUp breaker excludes 4xx from the failure count.** `newClickUpBreaker`'s `IsSuccessful` returns `true` for any 4xx (including 401 and 429) — a 4xx is a client-side or throttle condition, not a ClickUp-health signal. Only 5xx + transport errors trip the breaker. This mirrors `breaker.IsSuccessful` exactly and prevents a bad-token 401 from also tripping the circuit (which would mask a later recovery).
- **`BrevoClient.sendMail` is an injectable func field.** `net/smtp.SendMail` opens its own connection and has no context or interface seam, so the tests would otherwise need a live SMTP relay. Defaulting the field to `net/smtp.SendMail` in `NewBrevoClient` keeps production wiring trivial while letting tests drive success/failure/breaker-open deterministically.
- **Typed status errors (`clickupHTTPError`, `clickupRateLimitError`).** Carrying only the integer status (never the URL or token) lets both the gobreaker `IsSuccessful` filter and the backoff `classify` mapper read the status via `errors.As` without string-matching — and guarantees the error string is secret-free even when wrapped with `%w` up the stack.

## Deviations from Plan

None - plan executed exactly as written. The plan's tasks, file list, acceptance criteria, and threat model were all satisfied as specified. The one mid-task correction (gobreaker fn returning an error for non-2xx) was not a deviation from the plan — it was the plan's own breaker-opens acceptance criterion failing on the first test run and being fixed within the same task, exactly as intended.

## Issues Encountered

- **`TestChatwoot_Send_BreakerOpens` failed on the first run.** The breaker was not tripping because the gobreaker `fn` returned `(resp, nil)` for an HTTP 500 (the transport call succeeded; only the status was bad). Resolved by moving the non-2xx → typed-error classification *inside* the gobreaker `fn`, so gobreaker sees the failure. Re-ran green; the same fix shape was applied proactively to ClickUp. No analogous issue for Brevo — `net/smtp.SendMail` returns a real error on failure, which the breaker sees directly.
- **`grep -c` acceptance criteria for `api_access_token` (Chatwoot) and `Authorization` (ClickUp) required exactly 1.** Initial drafts mentioned the header names in doc comments, inflating the count. Reworded the comments to refer to "the API auth token" / "the auth header" so the literal header-name string appears only on the actual `req.Header.Set` line. Intent (one credential-isolation site) was always satisfied; this was a literal-grep-count adjustment.

## User Setup Required

None directly from this plan — the three clients consume the 13 optional Config fields that 07-01 already added. External-service credentials (Chatwoot token + on-call IDs, ClickUp token + list ID, Brevo SMTP creds + recipient list) remain operator prerequisites tracked in 07-01-SUMMARY's "User Setup Required" and the Phase 7 HUMAN-UAT plan. Each unset channel stays disabled; the WARN-on-disabled logging + the actual client wiring into the alerter land in 07-05/07-06.

## Threat Surface

No new threat surface beyond the plan's `<threat_model>`. All four registered threats are mitigated as designed:

- **T-07-11 (Information Disclosure)** — each client isolates its credential in one method (`grep -c` confirms exactly 1 site for `api_access_token` and `Authorization`); all errors are status-only sentinels (`clickupHTTPError`, `chatwoot: unexpected HTTP status N`, `brevo: send failed: <smtp err>`); error bodies are read through `io.LimitReader(_, 16*1024)`. Tests explicitly assert the error string contains neither the token nor the base-URL host.
- **T-07-12 (DoS)** — each client has a fixed package-level HTTP timeout const (`chatwootHTTPTimeout`, `clickupHTTPTimeout`) and its own `gobreaker.CircuitBreaker`; tests prove the breaker opens after the configured failure count and subsequently fails fast without hitting the server.
- **T-07-13 (Spoofing / 401 retry storm)** — `ClickUpClient.classify` returns `backoff.Permanent` for any 4xx-except-429; `TestClickUp_Send_401NotRetried` asserts exactly one server hit on a 401.
- **T-07-14 (Tampering / untrusted response body)** — response bodies are read only through `io.LimitReader`; only the status code (and, for 429, the `X-RateLimit-Reset` integer) is interpreted; no response field is reflected into a privileged operation or an error string.

## Known Stubs

None. All three clients are full implementations wired against `*Config` structs; `BrevoClient.sendMail` defaults to the real `net/smtp.SendMail` (the injectable field is a test seam, not a stub). The clients are not yet *wired into* the gateway — the alerter that consumes `[]Channel` is 07-05 and the main.go construction is 07-06 — but that is the planned interface-first ordering, not an incomplete deliverable of this plan.

## Next Phase Readiness

- **07-05 unblocked.** The `Channel` contract, the `Message` struct, the `Severity` type, the `redisx.AlertDedupKey` namespace, and the `obs.AlertSendsTotal` counter all exist and build — 07-05's `alerter.go` + `dedup.go` can program against fixed contracts.
- **07-06 unblocked.** The three concrete clients + their `New*Client` / `New*ClientWithBaseURL` constructors exist; main.go can construct them from `config.Config`, type-assert to `Channel`, and hand the slice to the alerter.
- **Verification green.** `cd gateway && go build ./...` exits 0; `go test ./internal/alert/ ./internal/redisx/ ./internal/obs/ -count=1 -race` passes; `go vet` clean on all three packages; `grep -rn "http-client|resty|gohttp" internal/alert/` returns nothing (zero new HTTP-client deps); all three clients carry a `var _ Channel = (*XClient)(nil)` compile-time assertion.
- **No blockers.**

## Self-Check: PASSED

All 8 created files exist on disk; the 1 modified file (`obs/metrics.go`) builds. All 3 task commits (`f616f9d`, `6bf9ca4`, `708a122`) are reachable in git history. `go build ./...` + `go test -race` on the three touched packages are green.

---
*Phase: 07-observability-dashboard-alerting*
*Completed: 2026-05-14*
