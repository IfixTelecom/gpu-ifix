---
phase: 05-load-shedding-saturation-aware-routing
fixed_at: 2026-05-11T22:30:00Z
review_path: .planning/phases/05-load-shedding-saturation-aware-routing/05-REVIEW.md
iteration: 2
findings_in_scope: 2
fixed: 2
skipped: 0
status: all_fixed
---

# Phase 5: Code Review Fix Report — Iteration 2

**Fixed at:** 2026-05-11T22:30:00Z
**Source review:** `.planning/phases/05-load-shedding-saturation-aware-routing/05-REVIEW.md`
**Iteration:** 2

**Summary:**
- Findings in scope: 2 (2 Warnings; 4 Info findings deliberately deferred)
- Fixed: 2
- Skipped: 0

Both Warning regressions from the iter-3 review were straightforward
documentation/UX defects from prior fix patches — no logic changes required.
Verified via `go build ./...` (exit 0) and targeted unit tests
(`go test -short ./internal/shed/... ./cmd/gatewayctl/...`).

## Fixed Issues

### WR-FIX-02: Hint de erro do WR-06 sugere valor matematicamente errado para typos `ms`/`ns`

**Files modified:** `gateway/cmd/gatewayctl/shed.go`
**Commit:** `70fb6f9`
**Applied fix:**

Substituiu o hint `did you mean %ds?` baseado em `int64(ttl/time.Microsecond)`
— que produzia mensagens activement misleading como "did you mean 500000s?"
para o typo comum `--ttl 500ms` — por extração do prefixo numérico da string
original via regex `^(\d+(?:\.\d+)?)([a-zµ]+)$`. Agora `--ttl 500ms` sugere
"did you mean 500s?". Quando o input não casa com o shape simples
`<número><unidade>` (e.g., compound `1m500ms`), o hint é omitido em vez de
emitir um valor errado. Adicionou import `regexp` + var package-level
`ttlNumericPrefixRe` (compilada uma vez). Build + test suite passam (exit 0,
`go test -short ./cmd/gatewayctl/...` → ok 5.207s).

### WR-FIX-01: Worker pool em `MakePublishTransition` vaza goroutines em testes e não tem Stop hook

**Files modified:** `gateway/internal/shed/subscribe_test.go`
**Commit:** `4ef3f23`
**Applied fix:**

Adotou a **Opção 1 (mínima)** do REVIEW.md: atualizou o comentário stale em
`subscribe_test.go:141`. O comentário anterior dizia "impl is synchronous
but defensive" — não é mais verdade depois do worker-pool do WR-03. O texto
novo documenta explicitamente que (a) `MakePublishTransition` dispatcha
para o pool buffered (`mirrorPublishWorkers` workers, depth
`mirrorPublishQueueDepth`); (b) o sleep agora é **load-bearing**, não
defensivo; (c) bump de 30ms → 50ms para folga sobre o round-trip miniredis.
Mudança puramente de comentário/timing de teste — sem alteração de runtime
behavior. O worker-pool lifecycle issue (Stop hook, leak em test suite
long-running) fica de follow-up em phase futuro como o próprio review
sinalizou ("Não é blocker — defeito de qualidade"). Test
`TestPublishTransitionWritesAndPublishes` continua passando (ok 0.061s).

## Deferred (out of scope for this iteration)

As 4 findings Info do REVIEW.md foram deixadas para hardening dedicado,
conforme o escopo `critical_warning`:

- **IN-FIX-01**: comentário discrepante em `reconcile.go:88-90`
- **IN-FIX-02**: faltam testes para `InflightRegistry.AddUpstream`
- **IN-FIX-03**: faltam testes para o backoff de 3 erros no reconcile loop
- **IN-FIX-04**: constantes `mirrorPublishWorkers`/`QueueDepth` não configuráveis via env

---

_Fixed: 2026-05-11T22:30:00Z_
_Fixer: Claude (gsd-code-fixer)_
_Iteration: 2_
