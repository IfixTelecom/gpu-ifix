---
phase: 05-load-shedding-saturation-aware-routing
plan: 04
subsystem: dcgm-scraper
tags: [go, http-client, prometheus-expfmt, dcgm, fail-open, tdd]
requirements: [LSH-02]
threat_refs: [T-05-06, T-05-07]
validates_sc: [SC-2]
dependency_graph:
  requires:
    - dcgm.errors (Plan 05-01 — ErrDCGMScrapeFailed, ErrDCGMUnitMismatch)
    - obs.GatewayVramUsedMiB + obs.GatewayDcgmScrapeFailures (Plan 05-01)
    - config.DCGMExporterURL + config.ShedDcgmScrapeIntervalMs + config.ShedDcgmTimeoutMs (Plan 05-01)
    - github.com/prometheus/common/expfmt v0.62.0 (Plan 05-01 promoted to direct)
  provides:
    - dcgm.Scraper{} struct + dcgm.New(url, interval, timeout, log)
    - (*dcgm.Scraper).Run(ctx) goroutine
    - (*dcgm.Scraper).ReadMiB() (int64, bool) — lockless, nil-safe
  affects:
    - Plan 05-06 (FSM ticker) consumes ReadMiB() via TickerDeps.DCGM
    - Plan 05-09 (main.go wiring) constructs Scraper iff cfg.DCGMExporterURL != ""
tech_stack:
  added:
    - expfmt.TextParser (zero-value) for Prometheus text-format parsing
  patterns:
    - "ticker + ctx.Done loop (analog: gateway/internal/upstreams/probe.go)"
    - "lockless atomic publisher (analog: gateway/internal/breaker/breaker.go remoteOpen)"
    - "fail-open hysteresis (3-strike threshold)"
key_files:
  created:
    - gateway/internal/dcgm/scraper.go
    - gateway/internal/dcgm/scraper_test.go
  modified: []
decisions:
  - "Used zero-value expfmt.TextParser per upstream docs (no NewTextParser constructor in prometheus/common v0.62.0)"
  - "Dropped model.UTF8Validation parameter from plan literal — global NameValidationScheme knob, not constructor arg"
  - "ReadMiB on nil receiver returns (0, true) — same fail-open contract as 3-strike trip; lets main.go skip construction when DCGM_EXPORTER_URL is empty (Gate C)"
  - "Both gauge and counter branches accepted in scrape (dcgm-exporter emits gauge in prod; counter fallback keeps fixtures flexible)"
metrics:
  duration: "00:35"
  completed: 2026-05-11
  tasks_completed: 1
  files_changed: 2
  tests_added: 9
---

# Phase 5 Plan 04: DCGM HTTP Scraper Summary

HTTP poller que consome `DCGM_FI_DEV_FB_USED` (MiB) do dcgm-exporter no pod, publica gauge Prometheus e expõe leitura atômica para o FSM de shedding com semântica fail-open de 3 strikes.

## Scope Delivered

| Item | Status |
|------|--------|
| `gateway/internal/dcgm/scraper.go` (245 linhas, doc-rich) | done |
| `gateway/internal/dcgm/scraper_test.go` (9 tests, -race clean) | done |
| `go build ./...` clean | done |
| `go vet ./...` clean | done |
| `go test -race -count=1 ./gateway/internal/dcgm/...` clean | done |
| Threat mitigations T-05-06 (timeout/ctx) e T-05-07 (sanity bounds) | done |
| Gate C compliance (nil-safe ReadMiB) | done |

## Scraper API

```go
// Construtor — main.go só chama se cfg.DCGMExporterURL != "" (Gate C contract).
func New(url string, interval, timeout time.Duration, log *slog.Logger) *Scraper

// Goroutine; bloqueia até ctx cancel. Boot faz scrape sincrono inicial
// para que ReadMiB tenha valor antes do primeiro tick do FSM.
func (s *Scraper) Run(ctx context.Context)

// scrape: 1 GET + parse + sanity + atomic.Store; nunca mata a goroutine.
func (s *Scraper) scrape(ctx context.Context)

// fail: bump consecutiveFail, emite counter, log; flip vramUnknown em N>=3.
func (s *Scraper) fail(reason string, err error)

// Hot path FSM. nil receiver → (0, true) — fail-open by absence.
func (s *Scraper) ReadMiB() (int64, bool)
```

Constantes internas: `failOpenAfterN = 3`, `minValidVramMiB = 0`, `maxValidVramMiB = 1_000_000`, `dcgmMetricName = "DCGM_FI_DEV_FB_USED"`.

## Failure Reasons and Test Coverage

A tabela cobre todos os `reason` labels que vão para `gateway_dcgm_scrape_failures_total{reason}`:

| Reason label | Trigger | Test |
|---|---|---|
| `request_build` | `http.NewRequestWithContext` falhou (URL inválido) | implicit (URL malformado quebraria New ou Do — não coberto por teste dedicado; ocorrência é improvável em produção pois `cfg.DCGMExporterURL` é validado por boot fail-fast no main.go) |
| `http_error` | client.Do retornou erro (DNS, conn refused, timeout) | `TestScraper_RunStopsOnContextCancel` exercita o caminho indireto (ctx cancel durante scrape mata via Do) |
| `status_<n>` | resp.StatusCode != 200 (ex.: `status_503`) | `TestScraper_Status503FailsButNotYetOpen`, `TestScraper_FailOpenAfterThreeConsecutiveFailures`, `TestScraper_RecoverResetsCountersAndUnknown` |
| `parse_error` | expfmt rejeitou body (sintaxe inválida) | `TestScraper_ParseErrorFails` (body com `{` solto, erro real: `expected float as value, got "is"`) |
| `metric_missing` | families map não contém `DCGM_FI_DEV_FB_USED` ou família vazia | `TestScraper_MetricMissingFails` |
| `metric_not_gauge` | family encontrada mas m.Gauge e m.Counter nil | (não atingível com body de texto produzido por dcgm-exporter; ramo defensivo para fixtures futuros) |
| `sanity_check` | val < 0 ou val > 1_000_000 MiB | `TestScraper_SanityCheckRejectsImpossibleValues` (body com 9_999_999_999) |

## Test Roster (9 tests, all `-race` clean)

| Test | What it asserts |
|---|---|
| `TestScraper_SuccessPopulatesReadMiB` | Happy path retorna val=12345, unknown=false |
| `TestScraper_Status503FailsButNotYetOpen` | 1 falha incrementa consecutiveFail mas NÃO trip fail-open |
| `TestScraper_FailOpenAfterThreeConsecutiveFailures` | 3 falhas consecutivas → vramUnknown=true |
| `TestScraper_RecoverResetsCountersAndUnknown` | Após 3 falhas, 1 sucesso zera consecutiveFail e unknown |
| `TestScraper_ParseErrorFails` | Body inválido → reason=parse_error, consecutiveFail >= 1 |
| `TestScraper_MetricMissingFails` | Body sem DCGM_FI_DEV_FB_USED → reason=metric_missing |
| `TestScraper_SanityCheckRejectsImpossibleValues` | val=9999999999 → reason=sanity_check (T-05-07 defesa) |
| `TestScraper_RunStopsOnContextCancel` | Run retorna em <1s após ctx cancel; val cacheado preservado |
| `TestScraper_NilReceiverReadMiBReturnsUnknown` | `var s *Scraper; s.ReadMiB()` → (0, true) (Gate C contract) |

## Threat Mitigations Implemented

### T-05-06 (DoS — slow DCGM endpoint blocks goroutine)

- `http.Client.Timeout = cfg.ShedDcgmTimeoutMs` (default 2s)
- `http.NewRequestWithContext(ctx, ...)` ata o parent ctx, so o gateway shutdown propaga
- `TestScraper_RunStopsOnContextCancel` valida que Run retorna em <1s mesmo com ticker ativo

### T-05-07 (Tampering / signal poisoning)

- Sanity check `val < 0 || val > 1_000_000` rejeita valores impossíveis (legítimos: GPUs em 2026 não passam de 1 TB VRAM)
- Counter `gateway_dcgm_scrape_failures_total{reason="sanity_check"}` instruments tampering attempts para dashboards
- Test `TestScraper_SanityCheckRejectsImpossibleValues` cobre o caminho com val=9_999_999_999 MiB

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] expfmt API mismatch in plan literal code**

- **Found during:** Task 4.1 implementation (pre-write doc lookup)
- **Issue:** Plan A) action snippet uses `expfmt.NewTextParser(model.UTF8Validation).TextToMetricFamilies(...)`. This does not compile against `github.com/prometheus/common v0.62.0` — `NewTextParser` does not exist as a constructor. `TextParser`'s zero value is ready to use (upstream doc `expfmt/text_parse.go:52`), and `model.UTF8Validation` is a global `NameValidationScheme` knob, not a parser parameter.
- **Fix:** Replaced with `var parser expfmt.TextParser` (zero-value instantiation per upstream docs). Removed the unused `model` import and the dead `_ = dto.MetricType(0)` line from the plan since `dto.Gauge` is referenced only as a pointer field (`m.Gauge`) which doesn't require naming the `dto` package directly.
- **Files modified:** `gateway/internal/dcgm/scraper.go` (literal code only — semantic behavior identical to plan intent)
- **Commit:** `603a3d2`

### Auth Gates

None.

### Architectural Decisions

None — implementation matched plan intent module-by-module after the expfmt API fix.

## Known Stubs

None. ReadMiB() returning (0, true) on nil receiver is the **intentional** Gate C fail-open contract (documented in CONTEXT D-A3 / WAVE0-GATES Gate C), not a stub.

## Threat Flags

None — no new surface introduced beyond what the plan's `<threat_model>` already covers (T-05-06, T-05-07 are mitigated end-to-end here).

## TDD Gate Compliance

| Gate | Commit | Verification |
|---|---|---|
| RED | `1f92d19` (test(05-04): add failing tests…) | Pre-commit `go test ./gateway/internal/dcgm/...` failed with "undefined: New" + "undefined: Scraper" on 8 of 9 tests |
| GREEN | `603a3d2` (feat(05-04): implement dcgm.Scraper …) | Post-commit `go test -race -count=1 ./gateway/internal/dcgm/... -v` shows 9/9 PASS in 1.2s |
| REFACTOR | (skipped — code is already idiomatic and well-doc'd; no functional cleanup needed) | n/a |

Sequence is correct: `test` commit precedes `feat` commit; both are present in `git log --oneline -3`.

## Pending for Plan 05-06 / 05-09

Plan 05-06 (FSM ticker) consumes `dcgm.Scraper.ReadMiB()` via `TickerDeps.DCGM`. Wiring contract:

```go
// In TickerDeps (Plan 05-06):
DCGM interface{ ReadMiB() (int64, bool) }

// In main.go (Plan 05-09):
var scraper *dcgm.Scraper
if cfg.DCGMExporterURL != "" {
    scraper = dcgm.New(cfg.DCGMExporterURL,
        time.Duration(cfg.ShedDcgmScrapeIntervalMs)*time.Millisecond,
        time.Duration(cfg.ShedDcgmTimeoutMs)*time.Millisecond,
        log)
    go scraper.Run(ctx)
}
// scraper may be nil; ReadMiB() handles that defensively.
go shed.RunTicker(ctx, shed.TickerDeps{DCGM: scraper, ...}, log)
```

Note that `scraper` of static type `*dcgm.Scraper` satisfies the `interface{ ReadMiB() (int64, bool) }` even when it's a `nil` pointer because Go method calls dispatch through the value receiver and `(*Scraper).ReadMiB` guards `if s == nil`. The Plan 05-06 implementer must NOT add a `scraper != nil` guard at the wiring site — the contract is "always call ReadMiB, treat unknown=true as no-signal".

## Verification Trace

```
$ go vet ./...
(no output — clean)

$ go build ./...
(no output — clean)

$ go test -race -count=1 ./gateway/internal/dcgm/... -timeout=30s -v
=== RUN   TestScraper_SuccessPopulatesReadMiB
--- PASS: TestScraper_SuccessPopulatesReadMiB (0.00s)
=== RUN   TestScraper_Status503FailsButNotYetOpen
--- PASS: TestScraper_Status503FailsButNotYetOpen (0.00s)
=== RUN   TestScraper_FailOpenAfterThreeConsecutiveFailures
--- PASS: TestScraper_FailOpenAfterThreeConsecutiveFailures (0.00s)
=== RUN   TestScraper_RecoverResetsCountersAndUnknown
--- PASS: TestScraper_RecoverResetsCountersAndUnknown (0.00s)
=== RUN   TestScraper_ParseErrorFails
--- PASS: TestScraper_ParseErrorFails (0.00s)
=== RUN   TestScraper_MetricMissingFails
--- PASS: TestScraper_MetricMissingFails (0.00s)
=== RUN   TestScraper_SanityCheckRejectsImpossibleValues
--- PASS: TestScraper_SanityCheckRejectsImpossibleValues (0.00s)
=== RUN   TestScraper_RunStopsOnContextCancel
--- PASS: TestScraper_RunStopsOnContextCancel (0.15s)
=== RUN   TestScraper_NilReceiverReadMiBReturnsUnknown
--- PASS: TestScraper_NilReceiverReadMiBReturnsUnknown (0.00s)
PASS
ok  	github.com/ifixtelecom/gpu-ifix/gateway/internal/dcgm	1.200s

$ grep -n "regexp" gateway/internal/dcgm/scraper.go
(no output — RESEARCH §Don't Hand-Roll respected)

$ grep -n "expfmt\." gateway/internal/dcgm/scraper.go
25:// Parser choice: prometheus/common's expfmt.TextParser (zero-value-ready)
170:	var parser expfmt.TextParser
```

## Self-Check: PASSED

- **FOUND**: `gateway/internal/dcgm/scraper.go`
- **FOUND**: `gateway/internal/dcgm/scraper_test.go`
- **FOUND commit**: `1f92d19` (RED — test commit)
- **FOUND commit**: `603a3d2` (GREEN — feat commit)
- All 9 tests pass with `-race`
- `go build ./...` clean
- `go vet ./...` clean
- No `regexp` import in scraper.go (RESEARCH compliance)
- `expfmt.TextParser` present (line 170)
- Files modified strictly within plan scope (gateway/internal/dcgm/{scraper.go,scraper_test.go})
