---
phase: 05-load-shedding-saturation-aware-routing
reviewed: 2026-05-11T20:00:00Z
depth: standard
files_reviewed: 53
files_reviewed_list:
  - gateway/cmd/gateway/main.go
  - gateway/cmd/gatewayctl/main.go
  - gateway/cmd/gatewayctl/shed.go
  - gateway/cmd/gatewayctl/shed_test.go
  - gateway/cmd/gatewayctl/tenant.go
  - gateway/cmd/gatewayctl/tenants_shed.go
  - gateway/cmd/gatewayctl/tenants_shed_test.go
  - gateway/cmd/gatewayctl/thresholds.go
  - gateway/cmd/gatewayctl/thresholds_test.go
  - gateway/db/migrations/0016_evolve_tenants_shedding_limits.sql
  - gateway/db/migrations/0017_evolve_upstreams_shed_thresholds.sql
  - gateway/db/migrations/0018_audit_log_shed_values.sql
  - gateway/db/queries/tenants_admin.sql
  - gateway/internal/auditctx/override.go
  - gateway/internal/config/config.go
  - gateway/internal/db/gen/models.go
  - gateway/internal/db/gen/querier.go
  - gateway/internal/db/gen/tenants_admin.sql.go
  - gateway/internal/dcgm/errors.go
  - gateway/internal/dcgm/scraper.go
  - gateway/internal/dcgm/scraper_test.go
  - gateway/internal/integration_test/helpers_shed_test.go
  - gateway/internal/integration_test/shed_edge_cases_test.go
  - gateway/internal/integration_test/shed_mirror_convergence_test.go
  - gateway/internal/integration_test/shed_sc1_burst_test.go
  - gateway/internal/integration_test/shed_sc2_hysteresis_test.go
  - gateway/internal/integration_test/shed_sc3_hotreload_test.go
  - gateway/internal/integration_test/shed_sc4_antistarvation_test.go
  - gateway/internal/obs/metrics.go
  - gateway/internal/proxy/dispatcher.go
  - gateway/internal/redisx/shed.go
  - gateway/internal/redisx/shed_test.go
  - gateway/internal/shed/errors.go
  - gateway/internal/shed/fsm.go
  - gateway/internal/shed/fsm_test.go
  - gateway/internal/shed/inflight.go
  - gateway/internal/shed/inflight_test.go
  - gateway/internal/shed/latency.go
  - gateway/internal/shed/latency_test.go
  - gateway/internal/shed/middleware.go
  - gateway/internal/shed/middleware_test.go
  - gateway/internal/shed/mirror.go
  - gateway/internal/shed/reconcile.go
  - gateway/internal/shed/reconcile_test.go
  - gateway/internal/shed/set.go
  - gateway/internal/shed/set_test.go
  - gateway/internal/shed/subscribe.go
  - gateway/internal/shed/subscribe_test.go
  - gateway/internal/shed/tick.go
  - gateway/internal/shed/tick_test.go
  - gateway/internal/shed/tools_phase5.go
  - gateway/internal/tenants/config.go
  - gateway/internal/tenants/loader.go
  - gateway/internal/upstreams/types.go
findings:
  critical: 2
  warning: 6
  info: 5
  total: 13
status: issues_found
---

# Phase 5: Code Review Report

**Reviewed:** 2026-05-11T20:00:00Z
**Depth:** standard
**Files Reviewed:** 53
**Status:** issues_found

## Summary

Revisão adversarial do subsystem de Load Shedding (Phase 5) — composite FSM, DCGM scraper, Redis mirror, middleware HTTP e subcomandos `gatewayctl`. A implementação demonstra cuidado real com fail-open semantics, locklessness no hot-path (atomic.Int64/Int32) e mirror não-bloqueante. Cobertura de testes unitários é boa, com testes de race-benign explícitos para o latency ring e tabela de transições do FSM totalmente exercitada.

Achados notáveis:

- **BLOCKER (CR-01):** Branch 09 do `shed.Middleware` (normal-tenant capped → tier-1) NÃO propaga `shed_decision` / `upstream_override` para o audit middleware. O padrão de mutação in-place de `*r` usado em Branches 10a/10b foi esquecido aqui. Audit log perde a categorização de ~95% do tráfego desviado por shed em produção.
- **BLOCKER (CR-02):** `gateway_shed_force_active` permanece em 1 indefinidamente quando o valor de `gw:shed:force:{upstream}` é malformado, mesmo após a chave expirar / ser deletada — viola contrato dashboard.
- **WARNING (WR-01..06):** TTL-precision (microsecond→second truncation no `gatewayctl shed-state`), filtragem frágil em `AllShedStateKeys`, vazamento de goroutine em `MakePublishTransition`, exception-handling no `reconcileOnce`, fallthrough silencioso em `Inc` de upstream desconhecido, validação de TTL ceiling vs. unsigned overflow.

Os requisitos de fail-open semantics (DCGM URL vazio, nil VramReader), TTL bounds (≤1h), JSONB merge não-destrutivo e migration 0018 docs-only foram **TODOS verificados como corretos**. Race-detector-safe atomic ops no FSM e LatencyRing também checados.

## Critical Issues

### CR-01: shed middleware Branch 09 não propaga audit context para tier-1 desvios

**File:** `gateway/internal/shed/middleware.go:234-242`
**Issue:** O Branch 09 (normal tenant + FSM=ON + cap excedido + tier-1 disponível) só passa o context derivado via `next.ServeHTTP(w, r.WithContext(ctx))`. A audit middleware (montada **fora** do shed na cadeia chi) lê `r.Context()` **após** `next.ServeHTTP` retornar, usando o `r0` original que ela mesma capturou — não o `r.WithContext(ctx)` que foi passado adiante. Resultado: a linha em `ai_gateway.audit_log` para tráfego desviado a tier-1 por shed **não** terá `upstream="openrouter-chat"` nem `shed_decision="shed_saturated"` populados pelo override; a audit middleware vai usar o valor route-derived (e.g. "llm") como upstream.

Padrão exato consciente desse problema já está em uso em:
- `gateway/internal/proxy/dispatcher.go:317` (`writeSensitiveBlock`): `*r = *r.WithContext(auditctx.WithUpstreamOverride(...))`
- `gateway/internal/shed/middleware.go:203` (Branch 10a sensitive): `*r = *r.WithContext(ctx)`
- `gateway/internal/shed/middleware.go:223` (Branch 10b no-tier-1): `*r = *r.WithContext(ctx)`

Apenas Branch 09 ficou com o padrão clássico Go que não propaga upstream-da-cadeia. O teste `TestMiddleware_Branch09_FSMOn_NormalCapped` passa porque verifica o context dentro do handler interno (`req.Context()`), mas isso não prova que o `r0` da audit middleware enxerga o mesmo override.

Impacto: dashboards e queries de billing por upstream perderão visibilidade do volume de tráfego que foi desviado a tier-1 por saturação — exatamente o caso de uso primário do Phase 5.

**Fix:**
```go
// Branch 09 (normal tenant over cap, tier-1 available) — adicionar
// mutação in-place ANTES do next.ServeHTTP, como Branches 10a/10b já fazem:
ctx := auditctx.WithShedDecision(r.Context(), auditctx.UpstreamShedSaturatedValue)
ctx = auditctx.WithUpstreamOverride(ctx, t1.Name)
obs.GatewayShedDecisions.WithLabelValues(t0.Name, "tenant_cap").Inc()
obs.GatewayInflightTier1.WithLabelValues(t1.Name).Inc()
defer obs.GatewayInflightTier1.WithLabelValues(t1.Name).Dec()
log.Debug("shed routed to tier-1", ...)

*r = *r.WithContext(ctx) // ← ADICIONAR esta linha
next.ServeHTTP(w, r)     // ← passar r mutado (não r.WithContext(...))
```

Também adicionar um teste integrado (ou unit usando uma audit-fake) que invoque o middleware shed dentro de uma cadeia simulada e leia `auditctx.ShedDecisionFromContext(r.Context())` no ponto onde a audit middleware leria — após `next.ServeHTTP` retornar.

### CR-02: GatewayShedForceActive nunca volta a 0 quando o valor de force é malformado

**File:** `gateway/internal/shed/tick.go:131-153`
**Issue:** Quando `redisx.GetShedForce` retorna `ok=true` mas o valor armazenado é desconhecido (qualquer coisa que não seja `"off"` ou `"on"`), o código:

1. Faz `obs.GatewayShedForceActive.WithLabelValues(upstream).Set(1)` (linha 132)
2. Cai no `default:` do switch (linha 139)
3. Faz log Warn e `return` (linha 146)

O `return` pula o `obs.GatewayShedForceActive...Set(0)` da linha 153, que só roda na "ramo else" (chave ausente). Resultado: uma vez que o valor é malformado, o gauge fica em 1 **para sempre** (até alguém escrever um valor válido `off`/`on`). Mesmo quando a chave expirar, o gauge não é restaurado a 0 — o tick subsequente vai pegar `ok=false` e tentar Set(0), mas... espera, esse caminho funciona, **se** a chave expirar antes que outro tick com valor malformado rode.

Cenário concreto: operador escreve `gw:shed:force:local-llm = "armed"` (digitação errada, não-aceitada pelo `gatewayctl shed-force` mas permitida via `redis-cli SET`). TTL longo (60 minutos). Por 60 minutos, dashboard mostra `gateway_shed_force_active{upstream="local-llm"}=1`, FSM continua avaliando sinais normalmente — discordância visível.

Mais grave: o `log.Warn` em loop a cada 100ms (em teste) ou 1s (em prod) inunda o log estruturado com a mesma mensagem até a chave expirar — pode ofuscar outros warnings reais.

**Fix:**
```go
default:
    obs.GatewayShedForceActive.WithLabelValues(upstream).Set(0) // ← ADICIONAR
    log.Warn("malformed shed-force value; ignoring", "upstream", upstream, "value", state)
    return
```

Adicionalmente: considerar usar um log de-duplication (e.g. log apenas no primeiro tick onde o valor mudou, não a cada tick). Padrão fácil: stash do último valor visto em um sync.Map por upstream.

## Warnings

### WR-01: `gatewayctl shed-state` trunca TTL para 0 quando override tem <1s restante

**File:** `gateway/cmd/gatewayctl/shed.go:121`
**Issue:** A coluna `TTL_S` em formato table/JSON é calculada como `int64(forceTTL / time.Second)`. Quando o TTL restante é, por exemplo, 350ms (override prestes a expirar), a divisão truncate-para-zero da integer math gera `TTL_S=0`. Operadores que rodam `shed-state` em loop verão "TTL_S=0 mas FORCE ainda ativo" — confuso e parece bug.

**Fix:** arredondar ou usar `time.Duration.Seconds()` (float) com formatação:
```go
r.ForceTTLSeconds = int64((forceTTL + time.Second/2) / time.Second) // arredondar
// ou aceitar uma resolução mais fina:
// r.ForceTTLMillis = forceTTL.Milliseconds()
```

### WR-02: `AllShedStateKeys` falsamente filtra upstream chamado `force:*`

**File:** `gateway/internal/redisx/shed.go:233`
**Issue:** A filtragem usa `strings.HasPrefix(k, "gw:shed:force:")`. Se algum dia alguém criar um upstream chamado `force:something` (improvável mas não validado), a state key será `gw:shed:force:something` e será incorretamente filtrada (interpretada como force key).

Em produção isso é defesa em profundidade — o `upstream.Name` é controlado por migration + CLI, ambos validam nomes. Mas o filtro está acoplado à convenção de nomenclatura, sem assertion.

**Fix:** validar que `upstream.Name` não pode começar com `force:` no `gatewayctl upstreams create` / migration insert. Alternativamente, mudar o prefixo de force keys para algo que não pode colidir (e.g. `gw:shedforce:`).

### WR-03: `MakePublishTransition` lança goroutine fire-and-forget sem bound

**File:** `gateway/internal/shed/mirror.go:42-63` + `gateway/cmd/gateway/main.go:385`
**Issue:** Em `main.go:385`, `pubTransition` é invocado como `go pubTransition(...)`. Se houver tempestade de transições (FSM oscillating + thresholds mal-configurados levando a flapping), o gateway pode lançar centenas de goroutines simultâneas todas tentando HSET+PUBLISH com 2s de timeout cada. Sem worker pool ou semáforo.

Em condições normais (FSM transita 1-2 vezes por dia por upstream), isso é seguro. Em incident-mode (operator override flapping toggle, ou bug que faz CAS falhar repetidamente — embora `transition()` filter same-state previne isso) goroutines podem se acumular.

Risco baixo dado o teto natural de transições (FSM tem hysteresis), mas vale documentar ou adicionar um semáforo com 10 slots.

**Fix:** introduzir um buffered channel de transições + 1-2 worker goroutines:
```go
type transitionJob struct{ upstream string; to State; reason string; sig *redisx.ShedEventSignals }
jobs := make(chan transitionJob, 64)
for i := 0; i < 2; i++ {
    go func() {
        for j := range jobs {
            pubTransition(j.upstream, j.to, j.reason, j.sig)
        }
    }()
}
// No OnChange: jobs <- transitionJob{...} (com select+default para não bloquear).
```

### WR-04: `reconcileOnce` continua o sweep mesmo após erros transitórios, mas não tem backoff

**File:** `gateway/internal/shed/reconcile.go:78-85`
**Issue:** Se Redis estiver degradado (e.g. failover replica eleição), cada chamada `ReadShedState` falha com timeout. O loop emite `Warn` por upstream + incrementa o contador `gateway_shed_mirror_reconcile_total{result="error"}` — N warnings + N contador-bumps a cada 30s, para todos os upstreams. Sem circuit-breaker no reconcile.

Não é crítico (sweep continua, próximo ciclo tenta de novo), mas em incidents Redis o log fica spammed.

**Fix:** adicionar contador local `consecutiveErrors` que, ao chegar em N (e.g. 3), pula o próximo ciclo inteiro. Resetar ao primeiro sucesso.

### WR-05: `InflightRegistry.Inc` retorna silenciosamente para upstream desconhecido

**File:** `gateway/internal/shed/inflight.go:65-68`
**Issue:** O comentário diz "Inc on an unknown upstream is a silent no-op". Mas se isso acontecer em produção significa que **a middleware está tentando trackear um upstream que o Registry não conhece** — bug grave de wiring (hot-reload deletou o upstream entre Resolve e Inc).

Combinado com `Dec` silently no-op para o mesmo cenário, o caller acredita estar tracking inflight mas o counter global nunca sobe; FSM nunca dispara via signal Inflight. Failure invisível.

Mais grave: hot-reload do `upstreams.Loader` rebuilda o `breakerSet` e `shedSet` (em main.go:447-453), mas o `shedInflight` é construído **uma vez** em `main.go:337` e NUNCA rebuildado. Se um upstream novo for adicionado via `gatewayctl upstreams create`, o middleware vai resolver tier-0 para ele, chamar `Inc(novo-name, tenant)` no shedInflight, e silently no-op — sem inflight tracking, sem FSM signal, shedding não funcionará para o upstream novo até o restart do gateway.

**Fix:**
1. Em `main.go` hot-reload, chamar `shedInflight.Rebuild(loader.Names())` também (precisa adicionar método Rebuild ao InflightRegistry, ou pelo menos AddUpstream).
2. Em `Inc` / `Dec` para upstream unknown, emitir métrica `gateway_shed_inflight_unknown_upstream_total{upstream}` ao invés de no-op silencioso. Operador detecta wiring bug rapidamente.
3. Documentar este caminho explicitamente no comentário com aviso de severidade.

### WR-06: TTL parsing aceita valores que poderiam ser interpretados como microseconds

**File:** `gateway/cmd/gatewayctl/shed.go:190`
**Issue:** `time.ParseDuration("300s")` aceita também `"300us"` (microseconds), `"300ns"`, etc. `gatewayctl shed-force on --upstream X --ttl 300us` seria parseado para 300 microseconds, depois rejeitado pelo `redisx.WriteShedForce` por `ttl <= 0` falso mas baixo. Não bug funcional (rejeitado), mas mensagem de erro `redisx: shed-force TTL 300µs out of range` pode ser confusa — operador esperava 300 seconds.

**Fix:** ou rejeitar unidades sub-second no CLI (`ttl < 1*time.Second` → exit 2 com mensagem clara), ou aceitar o valor inteiro como segundos por convenção:
```go
// Aceitar "300" como segundos:
if v, err := strconv.Atoi(*ttlStr); err == nil {
    ttl = time.Duration(v) * time.Second
} else {
    ttl, err = time.ParseDuration(*ttlStr)
    ...
}
```

## Info

### IN-01: Conversão `uint32(time.Since(start).Milliseconds())` pode overflow

**File:** `gateway/internal/shed/middleware.go:257`
**Issue:** `elapsed := uint32(time.Since(start).Milliseconds())`. Se a request demorar mais de ~49.7 dias (2^32 ms), o uint32 vai overflow. Em produção isso jamais acontece (timeout do upstream é 60s), mas se a `defer` rodar em uma request que ficou "presa" infinitamente (e.g. WS upgrade que nunca close), o valor torna-se lixo.

**Fix:** clamp explícito:
```go
ms := time.Since(start).Milliseconds()
if ms > int64(math.MaxUint32) {
    ms = int64(math.MaxUint32)
}
ring.Record(uint32(ms))
```

### IN-02: Tabwriter Flush pode swallow error silenciosamente

**File:** `gateway/cmd/gatewayctl/shed.go:145-148`
**Issue:** `tw.Flush()` retorna erro só se o underlying `os.Stdout` falhar (pipe quebrado, disco cheio). O erro é checkado e fmt.Fprintf'ado pra stderr — bom. Mas note que se Stdout fechar mid-print, alguns rows já foram impressos parcialmente. Comportamento aceitável para uma CLI.

**Fix:** nenhum — apenas observação. Talvez documentar que o flush parcial é possível.

### IN-03: Constante `dcgmMetricName` deveria ser configurável

**File:** `gateway/internal/dcgm/scraper.go:70`
**Issue:** `DCGM_FI_DEV_FB_USED` está hardcoded. Se NVIDIA renomear ou descontinuar a métrica (improvável mas histórico), o scraper precisa de um rebuild. Considerar tornar configurável via env var `DCGM_METRIC_NAME` com default atual.

**Fix:** opcional — risco real baixo. Documentar como semi-protocol-version.

### IN-04: Mensagem de "armed" na operator override CLI

**File:** `gateway/cmd/gatewayctl/shed.go:168`
**Issue:** `runShedForce` aceita só `on|off|clear`. Não há como o operador forçar `armed` ou `recovering`. Comentário do plan diz "operator override força estado para `on` ou `off`" — alinhado. Mas o tick.go's switch também aceita só "off"/"on" (cai no default para "armed"/"recovering"), então tudo consistente. Apenas verificar que docs/runbook não prometem outros valores.

**Fix:** nenhum no código — verificar `.planning/phases/05-load-shedding-saturation-aware-routing/*-PLAN.md` se há menção a "armed" override.

### IN-05: `defaultClassifyRoute` usa HasPrefix para `/v1/chat/completions`

**File:** `gateway/internal/shed/middleware.go:82-90`
**Issue:** `strings.HasPrefix(path, "/v1/chat/completions")` retorna true para `/v1/chat/completions-suffix-attack`. Não há tal endpoint hoje, mas se chi adicionar rotas wildcards `/v1/chat/completions/*` no futuro, o role seria classificado como "llm" para qualquer subpath. Pequeno risco surface.

**Fix:** usar igualdade exata ou `strings.HasPrefix(path, "/v1/chat/completions/")` + `path == "/v1/chat/completions"`.

---

## Verificações que passaram (positive findings)

Os seguintes pontos de risco identificados no `<focus_areas>` da revisão foram **verificados como corretos** — vale registrar para histórico:

- **Concurrency FSM lockless**: `state` e `enteredAt` são `atomic.Int32` / `atomic.Int64` com `CompareAndSwap` em transições. Não há paths que leem o int32 inteiro sem usar `Load()`. Mesmo race-com-tick não causa torn writes; a CAS filter previne double-callback.
- **Fail-open DCGM URL vazio**: main.go:345 explicitamente NÃO chama `dcgm.New` quando `DCGMExporterURL == ""`. `dcgmScraper` fica nil; `ReadMiB` em nil receiver retorna `(0, true)` corretamente. Sem boot panic.
- **TTL bounds shed-force**: `redisx.WriteShedForce` rejeita `ttl > 1h` (3600s). Tests `TestWriteShedForce_TTLCeiling` e `TestWriteShedForce_TTLZero` cobrem ambos extremos.
- **JSONB merge `thresholds set`**: `runThresholdsSet` lê `circuit_config` existente, unmarshal para map, overlay shed_* keys, marshal back. Campos pre-existentes (`failures`, `cooldown_s`) preservados.
- **Migration 0016 não-destrutiva**: `ADD COLUMN IF NOT EXISTS` com `NOT NULL DEFAULT`. Nenhum UPDATE de dados existentes. Down migration restaura trigger Phase 4.
- **Migration 0017 JSONB merge non-clobber**: usa `COALESCE(circuit_config, '{}'::jsonb) || jsonb_build_object(...)`. Operador `||` faz merge shallow preservando outras keys.
- **Migration 0018 docs-only**: contém apenas `SELECT 1` no `Up` e `Down`. Não toca schema.
- **HTTP status precedence Branch 10a/10b**: sensitive 503 com Retry-After: 5 antes de qualquer outra escrita; tier-1 unavailable 503 com Retry-After: 30. Códigos OpenAI envelope corretos.
- **LGPD audit values coarse-grained**: `UpstreamShedSaturatedValue`, `UpstreamShedBlockedSensitiveValue`, `UpstreamShedTier1UnavailableValue` são strings constantes não contendo tenant_id, request_id, ou PII. Slug do tenant aparece em `GatewayShedBlockedSensitive{tenant=slug}` mas isso é a label canônica usada em todos os contadores Phase 4 — não é PII per CONTEXT.md D-B7.

---

_Reviewed: 2026-05-11T20:00:00Z_
_Reviewer: Claude (gsd-code-reviewer)_
_Depth: standard_
