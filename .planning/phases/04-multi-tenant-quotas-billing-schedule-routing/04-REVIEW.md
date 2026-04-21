---
phase: 04-multi-tenant-quotas-billing-schedule-routing
reviewed: 2026-04-21T00:00:00Z
depth: standard
files_reviewed: 37
files_reviewed_list:
  - gateway/cmd/gateway/main.go
  - gateway/cmd/gatewayctl/main.go
  - gateway/cmd/gatewayctl/admin_key.go
  - gateway/cmd/gatewayctl/billing.go
  - gateway/cmd/gatewayctl/prices.go
  - gateway/cmd/gatewayctl/tenant.go
  - gateway/db/queries/admin_keys.sql
  - gateway/db/queries/billing.sql
  - gateway/db/queries/fx_rates.sql
  - gateway/db/queries/prices.sql
  - gateway/db/queries/tenants_admin.sql
  - gateway/db/queries/usage_counters.sql
  - gateway/internal/admin/errors.go
  - gateway/internal/admin/middleware.go
  - gateway/internal/admin/usage.go
  - gateway/internal/audit/writer.go
  - gateway/internal/auditctx/override.go
  - gateway/internal/billing/accountant.go
  - gateway/internal/billing/cost.go
  - gateway/internal/billing/errors.go
  - gateway/internal/billing/events.go
  - gateway/internal/billing/flusher.go
  - gateway/internal/billing/fx_loader.go
  - gateway/internal/billing/listen.go
  - gateway/internal/billing/prices.go
  - gateway/internal/billing/prices_loader.go
  - gateway/internal/config/config.go
  - gateway/internal/httpx/requestid.go
  - gateway/internal/idempotency/replay.go
  - gateway/internal/obs/metrics.go
  - gateway/internal/obs/middleware.go
  - gateway/internal/proxy/director.go
  - gateway/internal/proxy/dispatcher.go
  - gateway/internal/proxy/interceptor_usage.go
  - gateway/internal/proxy/openrouter_director.go
  - gateway/internal/quota/bucket.go
  - gateway/internal/quota/counters.go
  - gateway/internal/quota/enforcer.go
  - gateway/internal/quota/errors.go
  - gateway/internal/quota/lua.go
  - gateway/internal/quota/scripts/token_bucket.lua
  - gateway/internal/schedule/errors.go
  - gateway/internal/schedule/middleware.go
  - gateway/internal/schedule/policy.go
  - gateway/internal/schedule/window.go
  - gateway/internal/tenants/config.go
  - gateway/internal/tenants/errors.go
  - gateway/internal/tenants/listen.go
  - gateway/internal/tenants/loader.go
  - pkg/openai/types.go
findings:
  blocker: 2
  high: 4
  medium: 6
  low: 4
  nit: 3
  total: 19
status: has-findings
---

# Phase 4: Code Review Report

**Reviewed:** 2026-04-21
**Depth:** standard (per-file analysis com checks específicos para Go + SQL + Lua)
**Files Reviewed:** 37 (fontes Phase 4, excluindo `_test.go`, `db/gen/`, migrations)
**Status:** has-findings

## Summary

A Fase 4 entrega uma arquitetura coerente e bem comentada (atomic.Pointer snapshots, pgxlisten NOTIFY, Lua bucket Stripe-canonical, CTE idempotente), mas a revisão encontrou **dois BLOCKERs que invalidam o objetivo central da fase**:

1. `billing.Flusher` está instanciado e rodando em goroutine, porém **nenhum caminho de produção chama `flusher.Enqueue`** — `billing_events` nunca recebe linhas em produção. O `UsageInterceptor` capta tokens para o `Accountant` mas não tem referência ao flusher nem aos loaders de preço/FX. Plano 04-06 (PATTERNS.md linhas 910-922) prevê `proxy.NewUsageInterceptor(billingFlusher, pricesLoader, fxLoader, tenantsLoader, log)` mas a assinatura real é `NewUsageInterceptor(a *billing.Accountant, log *slog.Logger)`. Sem esse wiring o `/admin/usage`, o `gatewayctl billing reconcile` e a UI do SC-3 retornarão zero.
2. O `Accountant.Delete` não é chamado por ninguém em produção — cada `request_id` adiciona ~40 bytes permanentes ao `atomic.Pointer[map]` (copy-on-write). Em carga sustentada o mapa cresce linearmente até OOM; o próprio godoc de `Accountant.Delete` reconhece "forgetting to call leaks ~32 bytes per request_id".

Quatro HIGH incluem: (i) o script Lua divide por zero se `rps_rate=0` ou `rpm_rate=0` e o middleware apenas bypassa quando **ambos** são zero; (ii) `UsageInterceptor.ExtractFromBody` nunca é chamado no dispatcher de produção (JSON não-streaming perde usage); (iii) o override de schedule pode rotear tenants `data_class=sensitive` para OpenRouter quando o CHECK constraint é contornado (falha a triple-defense de LGPD D-C1 path 3 no hot-path); (iv) a ordem do chain coloca `obs.RequestsMiddleware` APÓS auth/audit/rate-limit/quota/schedule, então as rejeições 4xx/5xx mais interessantes **não são contadas em `gateway_requests_total`**.

Seis MEDIUM cobrem desvios de configurabilidade (`cfg.QuotaFailOpen` parseado mas nunca lido, contradiz o RUNBOOK), idempotency replay dead-code (nenhum caller usa `WithReplay` em produção), Retry-After em segundo sobre rate-limit RPS (mínimo 1s é 1000ms de lockout para uma janela que deveria reabrir em <1s), deleção defensiva do `Accountant` em falhas de SSE (close abortado vaza slot), e mais.

Os demais LOW/NIT são polimento: TouchAdminKeyLastUsed declarado mas nunca chamado (coluna `last_used_at` sempre NULL), `numericFromFloat` usa `big.Float.Int` que truncará para zero quando `f*1e6 < 1` silenciosamente, duplicação de `dataClassString` em 3 pacotes, etc.

---

## BLOCKER

### BL-01: `billing.Flusher` nunca recebe eventos em produção

**File:** `gateway/cmd/gateway/main.go:350-354`, `gateway/internal/proxy/interceptor_usage.go:31-49`
**Category:** bug
**Issue:** `billingFlusher := billing.NewFlusher(pool, log); go billingFlusher.Run(ctx)` é criado e roda, mas o único consumidor de `Flusher.Enqueue` no repositório é `gateway/internal/integration_test/*`. O `UsageInterceptor` foi construído com apenas `(accountant, log)` — não tem referência ao flusher, aos loaders de preços/FX ou ao tenants loader para converter `RequestUsage` em `billing.Event` e enfileirar. Efeito: `ai_gateway.billing_events` permanece vazio, o UPSERT de `usage_counters` via CTE nunca dispara, `/admin/usage` retorna zeros, `gatewayctl billing reconcile` sempre reporta "no drift" e todas as métricas `gateway_billing_flush_total` ficam em 0. O "linhas 352-354 `_ = pricesLoader; _ = fxLoader; _ = accountant`" confirma que o autor sabia que faltava consumi-los.

**Fix:**
```go
// Em main.go, passar o flusher + loaders para o interceptor:
usageInterceptor := proxy.NewUsageInterceptor(accountant, billingFlusher,
    pricesLoader, fxLoader, tenantsLoader, cfg.USDBRLDefault, log)

// Em interceptor_usage.go, adicionar um hook pós-Close que:
//   1) lê o RequestUsage do Accountant
//   2) chama billing.ComputeCostBRL para cost_local_phantom + cost_external
//   3) monta billing.Event{Source: "final"|"partial"}
//   4) flusher.Enqueue(event)
//   5) accountant.Delete(reqID)
// Como o interceptor é aplicado via ModifyResponse, o hook precisa rodar no
// Close() do teeReader (final SSE) ou após a leitura completa do body JSON
// no dispatcher para non-streaming (exige também chamar ExtractFromBody).
```

**Rationale:** Sem este wire-up a Fase 4 entrega o esqueleto (schema, loaders, flusher, interceptor, admin handler) mas não o comportamento contratado: nenhum event de billing é persistido. Impacto direto em D-D2 (GET /admin/usage deve consumir billing_events), D-D4 (reconcile só faz sentido com dados na tabela) e no SC-2 (smoke test de partial-flush).

---

### BL-02: `Accountant.Delete` nunca chamado — vazamento linear por `request_id`

**File:** `gateway/internal/billing/accountant.go:44-79`, `gateway/internal/proxy/interceptor_usage.go:66,89`
**Category:** bug (leak de memória)
**Issue:** O padrão copy-on-write do accountant constrói um novo `map[string]*RequestUsage` a cada `Set` (incluindo todas as chaves antigas) e o grava via `atomic.Pointer`. Em produção, toda requisição de chat via SSE chama `accountant.Set(reqID, usage)` (linha 66). Nenhum caminho chama `accountant.Delete` — grep confirma que só `cost_test.go` exercita o método. Em RPS=20 sustentado, o mapa cresce ~1,7M entradas/dia e cada Set aloca O(n) memória para a nova cópia: o sistema vai OOM em horas ou dias.

**Fix:**
```go
// Em interceptor_usage.go usageTeeReader.Close() (SSE) e no chamador de
// ExtractFromBody (non-streaming), após persistir o billing.Event:
defer u.accountant.Delete(reqID)

// Alternativamente, mover para um ttlMap com eviction após 5 min — mais
// robusto caso o cliente desconecte antes do flush.
```
**Rationale:** O próprio godoc (`// Best-effort cleanup — forgetting to call leaks ~32 bytes per request_id`) admite o risco. Além do leak de bytes, cada `Set` é O(n) porque copia o mapa inteiro, então o custo por requisição cresce linearmente — a 100k reqs acumuladas, cada nova requisição aloca um mapa de 100k+1 entradas. Isso vaza memória **e** degrada latência.

---

## HIGH

### HI-01: Lua token-bucket diverge por zero quando apenas uma janela é desabilitada

**File:** `gateway/internal/quota/scripts/token_bucket.lua:34,38,46,47`, `gateway/internal/quota/enforcer.go:101-104`
**Category:** bug
**Issue:** O middleware só bypassa o Lua quando `RPSCapacity <= 0 AND RPMCapacity <= 0`. Se o operador seta apenas `rps_limit=0` (para desabilitar o burst control mantendo RPM), `rpsRatePerMs=0` é passado ao Lua. Dentro do script: `rps_filled = min(0, 0 + ...*0) = 0`. `rps_filled < req` (req=1) → toma o branch de rejeição e computa `math.ceil((req - 0) / 0)` → `inf`/`nan` (Lua retorna `math.huge`). O retorno para o middleware vira uma reset_ms absurda e o Retry-After sai ilegível. Pior: qualquer requisição com a janela "disabled" é **rejeitada** em vez de passar, invertendo a semântica documentada em `bucket.go:31-35` ("0 capacity disables the corresponding window").

**Fix:**
```lua
-- No topo do script, após parsear rps_rate/rpm_rate:
if rps_cap <= 0 then
    -- Pula a janela RPS: trata como "always allowed" nessa dimensão
    rps_filled = req
    rps_rate = 1 -- evita divisão por zero na TTL line
end
if rpm_cap <= 0 then
    rpm_filled = req
    rpm_rate = 1
end
```
Alternativa (preferida): validar em `enforcer.go` que cada dimensão é `>0` ou fazer duas chamadas Lua distintas (uma por janela). O skip total só deveria acontecer se AMBAS forem 0.

**Rationale:** A seed de migration 0013 dá defaults `rps_limit=20, rpm_limit=600`, mas um operador seguindo o RUNBOOK pode zerar uma dimensão; o test suite não cobre esse caso porque `quota/lua_test.go` roda com capacidades > 0 (miniredis só testa o feliz).

---

### HI-02: `UsageInterceptor.ExtractFromBody` não é chamado para chat não-streaming

**File:** `gateway/internal/proxy/interceptor_usage.go:51-73`, `gateway/internal/proxy/dispatcher.go:236-253`
**Category:** bug
**Issue:** O `Intercept` só instala o teeReader quando `Content-Type: text/event-stream` (linha 58). Para chat não-streaming (stream=false, JSON), retorna nil sem registrar nada. O godoc diz "caller should invoke ExtractFromBody post-Read of the buffered body" — mas o dispatcher chama `proxy.ServeHTTP(w, r)` que escreve direto no `ResponseWriter` e não bufferiza o body do upstream. `ExtractFromBody` só é chamado em `interceptor_usage_test.go:166`. Resultado: toda requisição de chat não-streaming **perde** os tokens gastos — `usage` permanece zerado no accountant, o billing_event subsequente (se BL-01 fosse fixado) gravaria 0 tokens e a cobrança seria inconsistente.

**Fix:** criar um interceptor paralelo para non-streaming (buffer do response body antes de escrever no cliente) ou adicionar um hook `Intercept` que tee-reader também responses JSON com `Content-Type: application/json`, detecta `{"usage":...}` no final e atualiza o `RequestUsage`. O tee seria descartável para audio/embed.

**Rationale:** A integração já documenta "Pitfall 5" para SSE (injeção de `stream_options.include_usage`), mas o caminho JSON ficou sem captura.

---

### HI-03: Schedule override + sensitive — triple-defense incompleta no hot-path

**File:** `gateway/internal/schedule/middleware.go:57-86`, `gateway/internal/proxy/dispatcher.go:91-98`
**Category:** security (LGPD)
**Issue:** O middleware de schedule não consulta `cfg.DataClass` — gera o override `openrouter-chat` sempre que `mode=peak AND fora da janela`. No dispatcher, `dispatchOverride` (linha 94-98) é executado **antes** do check `sensitive := ac.DataClass == auth.DataClassSensitive` (linha 139), portanto um tenant `data_class=sensitive` com `mode=peak` (cenário que o CHECK constraint proíbe, mas que um superuser pode criar via DDL) **seria roteado para OpenRouter**, violando LGPD. A triple-defense documentada (D-C1 path 3) depende do `CheckSensitivePeakInvariant` em boot, que só roda uma vez. Refreshes subsequentes do tenants loader só emitem `log.Warn` e não bloqueiam o estado inválido.

**Fix:** no `schedule.Middleware`, após `DecideUpstreamTier`, adicionar `if cfg.DataClass == "sensitive" { ctx = auditctx.WithUpstreamOverride(ctx, "blocked_sensitive"); ... }`. No `dispatcher.dispatchOverride`, rejeitar quando `auth.FromContext` retornar DataClass sensitive mesmo com override não-vazio. Alternativa mais robusta: adicionar `CheckSensitivePeakInvariant` dentro do próprio `tenants.Loader.Refresh` — se a contagem voltar > 0, panic ou desativar todos os tenants inválidos do snapshot.

**Rationale:** "CHECK constraint impossibilita" não é defesa-em-profundidade — ops pode executar `ALTER TABLE ... DROP CONSTRAINT` por engano, e a triple-defense de CONTEXT.md D-C1 existe exatamente para evitar que um erro de schema vaze dados sensíveis ao OpenRouter.

---

### HI-04: `obs.RequestsMiddleware` montado após auth/rate-limit/quota — não conta rejeições

**File:** `gateway/cmd/gateway/main.go:574-597`
**Category:** maintainability (observability)
**Issue:** A ordem `pg.Use()` é outermost→innermost. A sequência atual é `auth → audit → rate-limit → quota → schedule → obs.RequestsMiddleware → ...`. Quando auth emite 401, rate-limit emite 429, quota emite 429/503 ou schedule emite 503, a resposta escreve direto no `http.ResponseWriter` ANTES de o wrapper `statusRecorder` (definido dentro de `obs.RequestsMiddleware`) estar instalado. Consequência: `gateway_requests_total{status="4xx"|"5xx"}` conta apenas o que passa para dentro do handler final. Todas as respostas 429 de rate-limit ficam invisíveis em `/metrics` — exatamente o caso que o dashboard mais quer observar.

**Fix:** mover `pg.Use(obs.RequestsMiddleware(log))` para a primeira posição do grupo (logo após `RequestID` e antes de `auth.Middleware`), ou colocá-lo no `r.Use()` global (inclui /health e /metrics, mas esses podem ser filtrados por route).

**Rationale:** O comentário na linha 595-596 ("Mounted last so it observes the final status emitted by any middleware earlier in the chain") é exatamente o oposto da semântica real do chi — `Use` empilha, o mais cedo é o mais externo, só o middleware MAIS EXTERNO vê os status codes emitidos por middlewares internos.

---

## MEDIUM

### ME-01: `cfg.QuotaFailOpen` parseado mas nunca consultado pelo middleware

**File:** `gateway/internal/config/config.go:93,165`, `gateway/internal/quota/enforcer.go:228-243`
**Category:** maintainability (configurability drift)
**Issue:** O RUNBOOK-QUOTAS-BILLING.md linha 198 documenta: "If Postgres is down for >5 min: emergency override — set `AI_GATEWAY_QUOTA_FAIL_OPEN=true` in Portainer and restart the stack". Mas `handleQuotaError` ignora o flag — `ErrQuotaCheckUnavailable` sempre retorna 503. Dois riscos: (a) operador assume que o runbook funciona e aplica o override, mas a emergência persiste; (b) o flag está disponível em config, o que dá falsa sensação de segurança.

**Fix:** `QuotaMiddleware` deve receber `failOpen bool` (como `RateLimitMiddleware` já recebe). Quando `failOpen=true` e `checker.CheckQuota*` retorna `ErrQuotaCheckUnavailable`, incrementar `GatewayQuotaCheckFailures` e chamar `next.ServeHTTP(w, r)` em vez de 503. Atualizar `main.go:588` para passar `cfg.QuotaFailOpen`.

**Rationale:** Deriva entre doc e código é a causa mais comum de "my runbook didn't work" em incidentes.

---

### ME-02: Replay semantics em produção são dead code

**File:** `gateway/internal/idempotency/replay.go`, `gateway/internal/quota/enforcer.go:64-67`, `gateway/cmd/gateway/main.go:574-623`
**Category:** maintainability
**Issue:** `idempotency.WithReplay` nunca é chamado em código de produção (só em tests). O check `if idempotency.IsReplay(r.Context())` em `RateLimitMiddleware:65` sempre retorna false. Além disso, o idempotency.Middleware é montado PER-HANDLER (linha 615) — depois do rate-limit global no chain. Então mesmo se algum dia chamasse `WithReplay`, o rate-limit já teria executado e consumido tokens. D-D1 diz "replays do not re-consume RPS/RPM" mas a ordem atual do chain torna isso impossível.

**Fix:** ou (a) mover o idempotency middleware para antes de rate-limit no chain (arquitetural; idempotency precisa de tenant_id do auth, mas roda antes do rate-limit); ou (b) remover o check `IsReplay` e o arquivo `replay.go` inteiro, documentando que a semântica D-D1 é aspiracional e não enforced. Alternativa minimalista: o idempotency middleware, ao detectar um replay, chama `next.ServeHTTP(w, r.WithContext(WithReplay(r.Context())))` e o dispatcher (não o rate-limit) checa a flag para emitir headers X-Idempotency-Replayed.

**Rationale:** Código que alega semântica que não é verificada é pior que código sem a semântica — auditores vão assumir o comportamento documentado.

---

### ME-03: `Accountant.Set` em falha de SSE (cliente aborta, tee.Close nunca roda) vaza slot

**File:** `gateway/internal/proxy/interceptor_usage.go:54-73,125-132`
**Category:** bug
**Issue:** `Intercept` chama `accountant.Set(reqID, usage)` ao instalar o tee. O tee só é fechado via `usageTeeReader.Close()` — mas se o cliente aborta a conexão, o http servidor pode não chamar `Close` em todos os cenários (depende da chain de Close no `http.Response.Body`). Mesmo com BL-01 e BL-02 resolvidos, um fluxo SSE que não atinge um frame de usage deixa o slot preenchido com zeros, e a eventual chamada a `Delete` no BL-02 fix limpa só os casos felizes.

**Fix:** adicionar `defer u.accountant.Delete(reqID)` no final de `scanFrames`/`Close`; consumir um timeout em `RequestUsage` ou rodar um reaper goroutine que percorre o snapshot a cada 60s e apaga entradas mais velhas que 5 min. Registrar o timestamp de `Set` em `RequestUsage` para o reaper usar.

**Rationale:** Cria burst leaks sob cargas com alta taxa de abort (comum em SSE onde o usuário pressiona stop no chatbot). Combinado com BL-02, agrava o crescimento de memória.

---

### ME-04: `parseWindowHours` aceita "08-08" (janela de duração zero) e "-5-10" (inválido aritmético)

**File:** `gateway/cmd/gatewayctl/tenant.go:302-317`
**Category:** bug
**Issue:** `strings.SplitN(s, "-", 2)` para input "-5-10" retorna `["", "5-10"]`, o primeiro item vira `parts[0]==""` → cai no error path. OK. Mas "8-8" retorna `[8, 8]` — passa sem warning; a janela fica `[08:00, 08:00)`, que o `InWindow` tratará como "nunca". Pior, "24-05" deveria cair no check `s1 < 0 || s1 > 23`, mas `fmt.Sscanf("24", "%d", &s1)` retorna `n1=1, err1=nil`, `s1=24`, depois rejeita em `s1 > 23`. OK. Mas "08-23" funciona e "23-08" (overnight) também é aceito — é o `InWindow` em `window.go` que decide. O teste não cobre a janela trivial `HH-HH` (zero-duration).

**Fix:**
```go
if s1 == s2 {
    return 0, 0, fmt.Errorf("invalid --window %q; start == end produces a zero-duration window", s)
}
```
Minutes precision (`HH:MM-HH:MM`) seria um follow-up.

**Rationale:** Edge case silencioso levará operadores a configurar uma janela que, na prática, nunca abre — sem mensagem de erro.

---

### ME-05: `PricesLoader.Get` cost pricing cai a 0 silenciosamente quando price absente

**File:** `gateway/internal/billing/cost.go:29-36`, `gateway/internal/billing/prices_loader.go:79-89`
**Category:** bug
**Issue:** `ComputeCostBRL` emite um `WARN` quando o price não é encontrado (`log.Warn("price missing — cost will be 0"...)`) mas continua retornando 0 em vez de sinalizar ao caller que o billing_event terá cost incorreto. O Flusher (se fosse usado — veja BL-01) gravaria `cost_external_brl=0` para todas as requisições durante o intervalo entre um deploy com model novo e uma inserção em `prices`. Não há métrica `gateway_prices_missing_total` nem contador em `obs/metrics.go` — só o log WARN.

**Fix:** adicionar `obs.GatewayPricesMissing.WithLabelValues(model, provider, unit).Inc()` para que dashboards alarmem antes de drift de cobrança se acumular. Opcionalmente, `billing.Event.CostAttribution = "unpriced"` que o reconcile sabe reprocessar.

**Rationale:** Phantom cost de 0 significa que `SUM(cost_external_brl)` no SC-3 /admin/usage subestima o custo real. Operador descobre só na conciliação mensal.

---

### ME-06: `bootstrapAdminKey` imprime a key gerada em log não-estruturado — surviva mesmo com redactor

**File:** `gateway/cmd/gateway/main.go:790-792`
**Category:** security
**Issue:** O bootstrap loga via `log.Warn("ROTATE THIS KEY IMMEDIATELY: bootstrap admin key generated", "key", bootstrap)`. O `httpx.NewRedactor` (linha 865) redata atributos por nome — a key é atributada como `"key"` que é um nome **genérico** e provavelmente não está na whitelist de redação (veja `httpx/redactor.go`, fora do escopo deste review, mas o risco é real). Se o Portainer/Sentry ingerir esses logs, a chave plaintext pode ficar exposta na trilha de logs por meses.

**Fix:** (a) imprimir a key APENAS em stdout não-estruturado (`fmt.Fprintln(os.Stderr, "...")`) sem passar pelo logger; (b) verificar que o Redactor efetivamente lista `"key"` ou renomear para `"admin_key_plaintext"` que esteja explicitamente na lista; (c) aceitar apenas `AI_GATEWAY_ADMIN_KEY_BOOTSTRAP` não-vazio (fail-closed) e deprecar a geração automática com logging.

**Rationale:** A mesma decisão sobre bcrypt + sha256 lookup mostra que a equipe sabe quão sensível é a key. Permitir que ela pingue em um log estruturado vai contra o próprio padrão de segurança.

---

## LOW

### LO-01: `numericFromFloat` trunca sem warning quando `f*1e6 < 0.5`

**File:** `gateway/internal/billing/events.go:56-72`
**Category:** bug
**Issue:** Valores muito pequenos (e.g. `cost_external_brl = 1e-9 BRL`) multiplicados por 1e6 ficam abaixo de 1.0; `big.Float.Int(nil)` trunca para 0. O comentário de doc garante "NUMERIC(10,6) exato" mas o resultado pode ser 0 silencioso. Em Phase 4 talvez não importe (BRL de 1 micro-centavo), mas o próximo plano (custos granulares por token) pode mostrar o erro.

**Fix:** usar `strconv.FormatFloat(f, 'f', 6, 64)` e `pgtype.Numeric.Scan(string)` em vez do round-trip via big.Float. Mais simples e preserva o scale explicitamente.

**Rationale:** NUMERIC(10,6) suporta 0.000001 como menor valor positivo; truncação silenciosa esconde bugs de custo.

---

### LO-02: Duplicação de `dataClassString` em 3 arquivos

**File:** `gateway/cmd/gatewayctl/tenant.go:322-331`, `gateway/cmd/gatewayctl/billing.go` (indireto via package), `gateway/internal/admin/usage.go:287-301`, `gateway/internal/tenants/loader.go:193-201`
**Category:** maintainability
**Issue:** Quatro implementações quase idênticas do helper que converte `interface{}` (shape do pgx enum) para `string`. Cada cópia pode divergir ao longo do tempo (e.g. tratar `nil` de maneira diferente — admin/usage retorna "", tenants/loader também retorna "", mas tenant.go NÃO trata `nil` explicitamente).

**Fix:** exportar `tenants.CoerceDataClass(v interface{}) string` e importar dos três consumers.

**Rationale:** DRY é menos sobre repetição e mais sobre pontos únicos de evolução.

---

### LO-03: `Retry-After` mínimo de 1s inflaciona janela de RPS curta

**File:** `gateway/internal/quota/enforcer.go:152-157`
**Category:** bug (semântica cliente)
**Issue:** O retry depois de 200ms (valor que o Lua retorna quando `res.ResetRPSms=200`) é arredondado para `Retry-After: 1`. Em rate-limit RPS de alta cardinalidade (20 req/s), 800ms de backoff adicional por cliente é overkill — os clientes OpenAI-compat respeitam Retry-After e o throughput cai ~5x. Para RPM faz sentido arredondar, para RPS não.

**Fix:** usar `Retry-After-Ms` (não-padrão mas melhor) ou emitir `Retry-After: 0` quando `retryMs < 1000` e sinalizar via outro header.

**Rationale:** Cumpre o RFC 7231 mas inflaciona backoff em RPS, gerando UX ruim. Low porque é um tradeoff conhecido, não um bug puro.

---

### LO-04: `TouchAdminKeyLastUsed` declarada na sqlc mas nunca chamada

**File:** `gateway/db/queries/admin_keys.sql:27-31`, `gateway/internal/admin/middleware.go:89-146`
**Category:** maintainability
**Issue:** A coluna `last_used_at` em `admin_keys` permanece NULL — o output de `gatewayctl admin-key list` sempre mostra "-" na coluna LAST_USED, e operadores não conseguem identificar chaves órfãs para rotacionar.

**Fix:** no final de `Verifier.Verify` (positive path), disparar `go func(){ _ = q.TouchAdminKeyLastUsed(ctx, row.ID) }()` com um ctx separado e timeout de 3s. Alternativamente usar um buffer debounced como `auth.TouchBuffer` (já existe padrão).

**Rationale:** Feature operacional documentada na tabela comments mas não entregue.

---

## NIT

### NI-01: Comentário em `main.go:594-596` diz "Mounted last so it observes final status"

**File:** `gateway/cmd/gateway/main.go:594-596`
**Category:** maintainability
**Issue:** Contradiz HI-04. Independente da correção, o comentário precisa ser reescrito.

**Fix:** "Mounted first so it wraps every subsequent middleware's response writer."

---

### NI-02: `pkg/openai/types.go` lista `OffHoursUpstreamUnavailableCode` mas schedule não tem middleware que emita esse código

**File:** `pkg/openai/types.go:168`, `gateway/internal/schedule/middleware.go` (ausente), `gateway/internal/proxy/dispatcher.go:210-213`
**Category:** maintainability
**Issue:** A constante é usada apenas em `dispatcher.go:212` para strings hard-coded `"off_hours_upstream_unavailable"`. O schedule middleware nunca emite um status; ele apenas seta o override. A constante em pkg/openai é documentação sem enforce.

**Fix:** substituir a string literal no dispatcher pela constante: `openai.OffHoursUpstreamUnavailableCode`. Mesmo tratamento para os 13 códigos — atualmente são strings soltas.

**Rationale:** Desvios ortográficos passam despercebidos (e.g. `"off_hours_upstream_unavailable"` vs `"off-hours-upstream-unavailable"`).

---

### NI-03: `formatDate(pgtype.Date{Valid:false})` retorna "-" mas JSON escreve como string não-ISO

**File:** `gateway/cmd/gatewayctl/billing.go:442-447`, `gateway/internal/admin/usage.go:218-221`
**Category:** maintainability
**Issue:** Quando o SUM de billing_events não tem linhas e o GROUP BY não produz nenhum row, `row.Date` é sempre Valid (porque é o resultado do GROUP BY). Mas para robustez defensiva — e porque o código já tem o branch — o JSON sai como `"date": "-"` em vez de omitir o campo ou emitir um ISO válido. Consumidores do SC-3 vão falhar ao parsear. `admin/usage.go:218` faz a mesma coisa com `date = ""`.

**Fix:** quando `!d.Valid`, omitir a linha do array ou emitir um sentinel claro como null.

**Rationale:** Defensivo, mas cosmético; só se manifesta em edge cases.

---

## Files Reviewed — Summary Table

| Área | Arquivos | Findings |
|------|---------|----------|
| main.go wiring | `cmd/gateway/main.go` | BL-01, HI-04, ME-06, NI-01 |
| gatewayctl | `cmd/gatewayctl/*.go` | ME-04, LO-02, NI-03 |
| admin | `internal/admin/*.go` | LO-04 |
| auditctx | `internal/auditctx/override.go` | — |
| audit writer | `internal/audit/writer.go` | — (limpo) |
| billing | `internal/billing/*.go` | BL-01, BL-02, ME-03, ME-05, LO-01 |
| config | `internal/config/config.go` | ME-01 |
| httpx | `internal/httpx/requestid.go` | — |
| idempotency | `internal/idempotency/replay.go` | ME-02 |
| obs | `internal/obs/*.go` | HI-04 |
| proxy | `internal/proxy/*.go` | BL-01, BL-02, HI-02, HI-03, ME-03 |
| quota | `internal/quota/*.go`, `scripts/*.lua` | HI-01, ME-02, LO-03 |
| schedule | `internal/schedule/*.go` | HI-03 |
| tenants | `internal/tenants/*.go` | — (limpo) |
| queries SQL | `db/queries/*.sql` | LO-04 (TouchAdminKeyLastUsed nunca usado) |
| pkg/openai | `pkg/openai/types.go` | NI-02 |

---

_Reviewed: 2026-04-21_
_Reviewer: Claude (gsd-code-reviewer)_
_Depth: standard_
