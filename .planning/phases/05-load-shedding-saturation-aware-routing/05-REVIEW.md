---
phase: 05-load-shedding-saturation-aware-routing
reviewed: 2026-05-11T22:00:00Z
depth: standard
files_reviewed: 20
files_reviewed_list:
  - gateway/cmd/gateway/main.go
  - gateway/cmd/gatewayctl/shed.go
  - gateway/internal/auditctx/override.go
  - gateway/internal/dcgm/scraper.go
  - gateway/internal/obs/metrics.go
  - gateway/internal/proxy/dispatcher.go
  - gateway/internal/redisx/shed.go
  - gateway/internal/shed/fsm.go
  - gateway/internal/shed/inflight.go
  - gateway/internal/shed/latency.go
  - gateway/internal/shed/middleware.go
  - gateway/internal/shed/mirror.go
  - gateway/internal/shed/reconcile.go
  - gateway/internal/shed/set.go
  - gateway/internal/shed/subscribe.go
  - gateway/internal/shed/tick.go
  - gateway/internal/tenants/config.go
  - gateway/internal/tenants/loader.go
  - gateway/internal/upstreams/loader.go
  - gateway/internal/upstreams/types.go
findings:
  critical: 0
  warning: 2
  info: 4
  total: 6
status: issues_found
prior_review: 05-REVIEW.iter2.md
prior_findings_resolved:
  blockers: 2  # CR-01, CR-02
  warnings: 6  # WR-01..WR-06
---

# Phase 5: Code Review Report — Iteration 3 (Fix-Pass Validation)

**Reviewed:** 2026-05-11T22:00:00Z
**Depth:** standard
**Files Reviewed:** 20
**Status:** issues_found (no BLOCKERs — only follow-up gaps from the fix patches)

## Summary

Validação adversarial do fix-pass aplicado entre 3ea48b7 e 23d97ad sobre o
relatório anterior (`05-REVIEW.iter2.md`). Os 2 BLOCKERs (CR-01, CR-02) e os 6
WARNINGs (WR-01..WR-06) foram **inspecionados in-loco** no código atual.

**Veredito por finding anterior:**

| Finding | Status | Commit | Evidência no código atual |
|---------|--------|--------|---------------------------|
| CR-01 (middleware Branch 09 audit) | RESOLVED | 3ea48b7 | `middleware.go:248` — `*r = *r.WithContext(ctx)` antes do `next.ServeHTTP(w, r)`. Padrão idêntico aos Branches 10a (linha 203) e 10b (linha 223). |
| CR-02 (force gauge stuck em 1) | RESOLVED | ed9fc10 | `tick.go:152` — `obs.GatewayShedForceActive.WithLabelValues(upstream).Set(0)` no branch `default` do switch antes do `return`. |
| WR-01 (TTL_S truncated) | RESOLVED | 311393e | `shed.go:127` — `int64((forceTTL + time.Second/2) / time.Second)` arredondamento. |
| WR-02 (`force:*` collision) | RESOLVED | 8cf3d02 | `upstreams/loader.go:82-87` — `strings.HasPrefix(r.Name, "force:")` skip com log warn `status=reserved_name`. |
| WR-03 (goroutine-per-transition) | RESOLVED | fa630c4 | `mirror.go:74-98` — worker pool 2x64; `select default → GatewayShedMirrorDropped.Inc()`. Métrica nova `gateway_shed_mirror_dropped_total` declarada em `obs/metrics.go:306-309`. |
| WR-04 (reconcile no backoff) | RESOLVED | 5c50ccb | `reconcile.go:50-97` — `reconcileErrorBackoffThreshold=3`, `skipNextCycle` flag. `reconcileOnce` retorna `bool` (`allErrors`). |
| WR-05 (silent Inc/Dec no-op) | RESOLVED | d983493 | `inflight.go:88,143` — emite `GatewayShedInflightUnknownUpstream{op=inc\|dec}`; `inflight.go:208-221` — `AddUpstream`; `main.go:464` — hot-reload listener chama `shedInflight.AddUpstream(n)`. |
| WR-06 (sub-second TTL) | RESOLVED | 23d97ad | `shed.go:207-212` — `if ttl > 0 && ttl < time.Second { exit 2 }` antes de abrir Redis. |

**Build & test status (validado neste reviewer):**

```
go build ./...                            # exit 0
go vet ./internal/shed/... ./internal/redisx/...
                                          # exit 0 (sem warnings)
go test -short ./internal/shed/...        # ok 0.512s
go test -short ./internal/redisx/...      # ok 2.117s
go test -short ./internal/upstreams/...   # ok 0.073s
go test -short ./internal/dcgm/...        # ok 0.167s
go test -short ./cmd/gatewayctl/...       # ok 5.253s
```

**Regressões introduzidas pelo fix-pass — 2 WARNINGs + 4 Info:**

Nenhuma das regressões é blocker — não há corruption de dados, vazamento de
segurança, ou crash. São defeitos de qualidade/observabilidade que valem
follow-up no próximo phase ou em um Plan 05-09 de hardening:

- **WR-FIX-01**: `MakePublishTransition` deixa workers órfãos em todos os
  test setups que reusam o helper (sem hook de Stop). Acumula goroutines em
  test suite long-running. Comentário em `subscribe_test.go:141` ficou stale
  ("synchronous but defensive" — não é mais sync).
- **WR-FIX-02**: Mensagem de erro do WR-06 fix usa hint matematicamente
  incorreto para typos `ms`/`ns`. Para `--ttl 500ms`, a mensagem sugere
  "did you mean 500000s?" — desorientador.
- **IN-FIX-01..04**: documentação desatualizada (`Inc no-op` agora bumpa
  métrica; comentário do `reconcile.go:88-90` discrepante com loop real);
  faltam testes unitários para `AddUpstream` e para o backoff de 3 erros
  consecutivos no reconcile.

## Critical Issues

Nenhum. Os 2 BLOCKERs anteriores (CR-01, CR-02) estão resolvidos e nenhum
novo defeito de severidade Critical foi introduzido pelos patches.

## Warnings

### WR-FIX-01: Worker pool em `MakePublishTransition` vaza goroutines em testes e não tem Stop hook

**File:** `gateway/internal/shed/mirror.go:74-98`
**Issue:** O fix do WR-03 substitui `go pubTransition(...)` por um worker pool
fixo de 2 goroutines + buffered channel de 64. Cada chamada a
`MakePublishTransition(rdb)` (com rdb != nil) spawnar 2 goroutines NOVAS.
Problemas:

1. **Leak em testes:** `subscribe_test.go:139` (`TestPublishTransitionWritesAndPublishes`)
   e `shed_mirror_convergence_test.go:69` chamam `MakePublishTransition`
   uma vez por teste. Em uma `go test ./...` rodando ambos, a binária de
   teste acumula 4 workers órfãos (2 por chamada) que bloqueiam em
   `for j := range jobs` até o processo morrer. Inofensivo per-test mas
   amplifica em test suite long-running com `-count=N`.

2. **Sem graceful shutdown:** main.go invoca `MakePublishTransition` em
   `cmd/gateway/main.go:361` e nunca pode parar os workers — quando o
   gateway recebe SIGTERM, o `ctx.Done()` para o ticker e os listeners,
   mas os 2 workers continuam bloqueados em `range jobs` (canal nunca
   fechado). O 2s `redisOpTimeout` do `doPublishTransition` limita o
   pior caso, mas se um job estiver em-flight quando SIGTERM chegar, o
   container fica em "Terminating" por até 2s extras antes do kernel
   SIGKILL. Em Kubernetes com graceful shutdown longo, é uma penalidade
   de 2s sem ganho funcional.

3. **Comentário stale:** `subscribe_test.go:141-142` diz "Allow any
   goroutine-internal dispatch (impl is synchronous but defensive)" — a
   impl NÃO é mais síncrona (é async via worker pool). O `time.Sleep(30ms)`
   defensivo agora é load-bearing, não puramente defensive. Comentário
   precisa ser atualizado para refletir o novo contrato.

**Risco em produção:** baixo (worker leak não cresce; é fixo em 2 por
processo gateway). Em testes: cresce O(N) com N testes que chamam
`MakePublishTransition`. Em CI long-running com `-count=100` ou `go test
-race`, pode aumentar memória de teste sem comprometer correctness.

**Fix:**

Opção 1 (mínima) — atualizar comentários stale:
```go
// subscribe_test.go:140-141
pub := MakePublishTransition(c)
pub("local-llm", StateOn, ...)
// Worker pool is asynchronous; allow up to 50ms for the bounded
// channel dispatch + Redis HSET to complete before reading state.
time.Sleep(50 * time.Millisecond)
```

Opção 2 (com hook de Stop) — retornar a struct ao invés do closure:
```go
type Publisher struct {
    rdb  *redis.Client
    jobs chan publishJob
    done chan struct{}
}

func (p *Publisher) Publish(upstream string, to State, reason string, sig *redisx.ShedEventSignals) {...}
func (p *Publisher) Stop() {
    close(p.jobs)  // workers drain remaining jobs and exit
    <-p.done       // wait for all workers
}
```

main.go: `defer pub.Stop()` antes do `<-ctx.Done()`. Tests: `t.Cleanup(pub.Stop)`.

Opção 3 (drop-in) — keep o closure API, mas armazenar `jobs` em um
package-level singleton com `Once`. Único processo, único pool. Tests
que precisam de isolamento podem usar uma factory alternativa que
fecha o pool em `t.Cleanup`.

### WR-FIX-02: Hint de erro do WR-06 sugere valor matematicamente errado para typos `ms`/`ns`

**File:** `gateway/cmd/gatewayctl/shed.go:207-212`
**Issue:** O fix do WR-06 emite:
```go
fmt.Fprintf(os.Stderr,
    "invalid --ttl %q: must be at least 1 second (got %s — did you mean %ds?)\n",
    *ttlStr, ttl, int64(ttl/time.Microsecond))
```

O hint `did you mean %ds?` usa `int64(ttl/time.Microsecond)`. Isso só está
correto para input em microsegundos:

| Input | ttl | `ttl/time.Microsecond` | Hint exibido | Correto? |
|-------|-----|------------------------|--------------|----------|
| `--ttl 300us` | 300µs | 300 | "did you mean 300s?" | sim |
| `--ttl 5ms` | 5ms | 5000 | "did you mean 5000s?" | **não** — usuário queria 5s |
| `--ttl 300ns` | 300ns | 0 | "did you mean 0s?" | **não** — sem hint útil |
| `--ttl 500ms` | 500ms | 500000 | "did you mean 500000s?" | **não** — usuário queria 0.5s (inválido) ou 500s |

O hint é actively misleading para o caso comum de typo `ms` ao invés de `s`
(operadores acostumados a setar timeouts em ms podem confundir).

**Impacto:** operador segue o hint cegamente e configura um TTL absurdo
(500000s = ~6 dias). `WriteShedForce` rejeita por `ttl > 1h`, então o
override não é aplicado — mas isso resulta em dois ciclos de tentativa e
erro ao invés de um.

**Fix:** ou (a) parar de emitir hint quando o valor não é microseconds; ou
(b) extrair o valor numérico do string original e sugerir `{num}s`:

```go
if ttl > 0 && ttl < time.Second {
    // Try to extract the numeric prefix to suggest the user-intended
    // value-in-seconds variant. Handles "500ms" → "500s", not 500000s.
    re := regexp.MustCompile(`^(\d+)([a-zµ]+)$`)
    if m := re.FindStringSubmatch(strings.TrimSpace(*ttlStr)); m != nil {
        fmt.Fprintf(os.Stderr,
            "invalid --ttl %q: must be at least 1 second (got %s — did you mean %ss?)\n",
            *ttlStr, ttl, m[1])
    } else {
        fmt.Fprintf(os.Stderr,
            "invalid --ttl %q: must be at least 1 second (got %s)\n",
            *ttlStr, ttl)
    }
    return 2
}
```

## Info

### IN-FIX-01: Comentário do `reconcile.go:88-90` discrepa do código real

**File:** `gateway/internal/shed/reconcile.go:88-90`
**Issue:** O comentário diz:
```go
// Do not reset the counter here — keep skipping
// every other cycle while Redis is degraded.
consecutiveErrors = 0
```

Mas o código IMEDIATAMENTE faz `consecutiveErrors = 0` na linha seguinte.
Isso significa que o backoff NÃO é "keep skipping every other cycle"; é
"after 3 errors skip 1, then run 3 more, skip 1, repeat" (rate-limit de
log de ~25% de redução durante outage, não 50%).

Não é bug — o spirit do WR-04 (rate-limit log spam) ainda é atendido — mas
a documentação confunde quem precisa raciocinar sobre o comportamento em
incident.

**Fix:** corrigir o comentário para refletir o padrão real:
```go
// Reset the counter after triggering the skip so the loop emits N more
// log lines + counter bumps before the next skip — gives ~25% log
// rate reduction during sustained Redis outage. To extend the silence
// further (e.g., skip every other cycle), keep the counter at
// threshold and skip on each subsequent tick.
consecutiveErrors = 0
```

### IN-FIX-02: Sem teste unitário para `InflightRegistry.AddUpstream`

**File:** `gateway/internal/shed/inflight_test.go`
**Issue:** O fix do WR-05 adiciona o método `AddUpstream` (linhas 208-221
em inflight.go). Não há teste cobrindo:

- AddUpstream em registry vazio (caso boot)
- AddUpstream idempotente (chamado 2x com mesmo nome)
- AddUpstream em receiver nil (defesa)
- AddUpstream com nome vazio (defesa)
- Inc após AddUpstream funciona (não bumpa `GatewayShedInflightUnknownUpstream`)

`TestInflightRegistry_UnknownUpstreamIncNoop` (linha 85-94) também tem
comentário stale: a função agora NÃO é "silent no-op" — ela bumpa o
contador `GatewayShedInflightUnknownUpstream{upstream,op=inc}`. O teste
não verifica isso.

**Fix:** adicionar suite:
```go
func TestInflightRegistry_AddUpstream_IdempotentAndIncrementable(t *testing.T) {
    r := NewInflightRegistry([]string{"local-llm"})
    r.AddUpstream("new-llm")
    r.AddUpstream("new-llm") // idempotent
    r.AddUpstream("")         // no-op
    tenant := uuid.New()
    r.Inc("new-llm", tenant)
    if g := r.GlobalInflight("new-llm"); g != 1 {
        t.Fatalf("AddUpstream + Inc should land at 1, got %d", g)
    }
}
```

### IN-FIX-03: Sem teste para o backoff de 3 erros consecutivos no reconcile

**File:** `gateway/internal/shed/reconcile_test.go`
**Issue:** Fix do WR-04 adiciona `reconcileErrorBackoffThreshold=3`, mas
`reconcile_test.go` não tem teste verificando:

- Que após 3 ciclos full-error consecutive, o próximo ciclo é skipped (no
  log warn "sustained errors")
- Que um único ciclo bem-sucedido reseta o contador
- Que `reconcileOnce` retorna `true` quando TODOS os upstreams falham
- Que `reconcileOnce` retorna `false` quando pelo menos um upstream
  sucede (mesmo que outros falhem)

O cenário-modelo (Redis com falha simulada via miniredis Close + reabrir)
é não-trivial mas testável.

**Fix:** adicionar `TestReconcileLoop_BackoffAfter3Errors` usando
miniredis.Close() para forçar erro, contar `GatewayShedMirrorReconcile{result=error}`
samples antes e depois do 4º tick.

### IN-FIX-04: Constantes `mirrorPublishWorkers` e `mirrorPublishQueueDepth` não calibradas empiricamente

**File:** `gateway/internal/shed/mirror.go:51,57`
**Issue:** O fix do WR-03 hardcoded:
- `mirrorPublishWorkers = 2`
- `mirrorPublishQueueDepth = 64`

Os comentários justificam ("2 is enough for 1-2/day per upstream"), mas
não há instrumentação que confirme isso em produção. Se o flapping
incident (cenário pior caso) ultrapassar 64 transições pending, as
seguintes drop silently com bump em `gateway_shed_mirror_dropped_total`.
O reconcile loop fecha o gap, mas há janela de divergência entre
dashboard e estado real.

Não é bug funcional — é uma escolha de constante que vale ser revisitada
após primeiro incident real.

**Fix:** tornar configurável via env vars `SHED_MIRROR_PUBLISH_WORKERS` e
`SHED_MIRROR_PUBLISH_QUEUE_DEPTH`, ambos com defaults atuais. Permitiria
turn-up rápido em incident sem deploy.

---

## Verificações que passaram (positive findings)

Os pontos de risco identificados no `<focus_areas>` desta revisão foram
**verificados como corretos** — vale registrar para histórico:

- **CR-01 audit propagation**: `middleware.go:248` espelha o padrão dos
  Branches 10a (linha 203) e 10b (linha 223). O teste
  `TestMiddleware_Branch09_FSMOn_NormalCapped` (linhas 358-375) confirma
  que o handler interno enxerga `auditctx.UpstreamOverrideFromContext` ==
  `"openrouter-chat"` e `auditctx.ShedDecisionFromContext` ==
  `auditctx.UpstreamShedSaturatedValue`. Audit middleware lê o mesmo `*r`
  pointer pós-`next.ServeHTTP` → enxerga overrides.
- **CR-02 force gauge reset**: `tick.go:152` chama `Set(0)` no branch
  `default` ANTES do `log.Warn + return`. Não há mais caminho onde o
  gauge fica em 1 indefinidamente para valor malformado.
- **WR-03 worker pool**: `mirror.go:78-85` cria buffered channel + 2
  workers em construção; closure usa `select default → metric` —
  non-blocking. Convergência preservada via reconcile loop (30s).
- **WR-05 RWMutex hot path**: `inflight.go:73-80` agora pega RLock breve
  para resolver `g`, `tmap`, `c`, `cok` coerentemente, depois atomic ops
  sem lock. Microbenchmark esperado <50ns/op para read lock contention.
- **WR-02 hot-reload propagation**: `upstreams/loader.go:82-87` valida
  prefix `force:` em cada `Refresh()`, então hot-reload de upstreams
  rejeita nomes reservados imediatamente. Tier-0/1 resolution não vai
  retornar um upstream com nome `force:*`.
- **shedInflight hot-reload (`main.go:450-466`)**: o listener
  `upstreams.ListenAndReload` chama `shedInflight.AddUpstream(n)` para
  cada nome em `loader.Names()` após `breakerSet.Rebuild` e
  `shedSet.Rebuild` — três rebuilds coordenados em uma única closure.
- **`Inc/Dec` unknown upstream signal**: ambos bumpam
  `GatewayShedInflightUnknownUpstream{upstream, op}` ao invés de no-op
  silencioso. Operadores agora têm sinal observável de wiring bug.
- **Fail-open semantics preservadas**: TTL bounds (≤1h), DCGM URL vazio
  (nil scraper retorna `(0, true)`), `MakePublishTransition(nil)` retorna
  noop closure, `ReconcileLoop` com `rdb=nil` retorna imediatamente.
  Sem regressões nos paths fail-open.

---

_Reviewed: 2026-05-11T22:00:00Z_
_Reviewer: Claude (gsd-code-reviewer)_
_Depth: standard_
_Prior review:_ `05-REVIEW.iter2.md` (2 BLOCKERs + 6 WARNINGs — all resolved)
_Re-review trigger: fix-pass commits 3ea48b7..23d97ad_
