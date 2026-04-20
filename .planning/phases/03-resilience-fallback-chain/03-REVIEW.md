---
phase: 03-resilience-fallback-chain
review_depth: standard
status: needs_attention
files_reviewed: 26
findings:
  critical: 0
  high: 5
  medium: 6
  low: 5
created: 2026-04-20T03:00:00Z
---

# Phase 3: Code Review Report

**Reviewed:** 2026-04-20T03:00:00Z
**Depth:** standard
**Files Reviewed:** 26
**Status:** needs_attention (0 critical, 5 high, 6 medium, 5 low)

---

## Summary

A substancial e cuidadosamente planejada implementação de resiliência multi-upstream foi entregue: circuit breakers por upstream, retry sensitivo LGPD-aware, hot-reload via LISTEN/NOTIFY, probe proativo, interceptor de tool-call SSE, 3 directors com body-rewrite e enforcement de token cap. O design geral é sólido — pitfalls documentados (errgroup zero-value, ctx-safe sleep, WHEN clause no trigger) foram todos abordados corretamente. Nenhuma vulnerabilidade de segurança crítica foi encontrada.

**Finding mais impactante (HIGH-01):** A variável `dropped` em `probe.go` é declarada como `uint64` mas protegida por `sync.Mutex` em `Dropped()` enquanto é incrementada sem lock em `enqueueUpdate()`. Em ambiente de alta concorrência isso é uma data race. A solução correta é usar `atomic.Uint64` consistentemente.

---

## High Findings

### HIGH-01: Data Race em `probe.Probe.dropped` — `sync.Mutex` usada de forma inconsistente com incremento sem lock

- **File:** `gateway/internal/upstreams/probe.go:91,304-308`
- **Issue:** O campo `dropped uint64` é lido em `Dropped()` via `p.mu.Lock()` mas é incrementado em `enqueueUpdate()` sem nenhum lock. O comentário na linha 91 diz "non-atomic OK (single producer at hot-path moment)" — porém o probe loop pode ter múltiplas goroutines `probeOne` rodando em paralelo via `errgroup.Group` no mesmo tick, todas podendo chamar `enqueueUpdate` concorrentemente. A mistura de mutex-para-leitura + sem-lock-para-escrita é uma data race.
- **Impact:** `dropped` pode ser corrompido silenciosamente; o `go race detector` reportaria essa condição. Em produção, a contagem incorreta oculta buffer overflow do probe writeback.
- **Fix:** Trocar para `atomic.Uint64`:

```go
// No campo da struct:
dropped atomic.Uint64

// Em enqueueUpdate (sem mutex):
p.dropped.Add(1)

// Em Dropped():
func (p *Probe) Dropped() uint64 {
    return p.dropped.Load()
}
// (remova o p.mu.Lock()/Unlock() do Dropped())
```

---

### HIGH-02: `writeSensitiveBlock` faz mutação in-place de `*http.Request` depois de `WriteHeader` potencialmente já ter sido chamado

- **File:** `gateway/internal/proxy/dispatcher.go:183`
- **Issue:** `*r = *r.WithContext(...)` é feito **antes** de `httpx.WriteOpenAIError`, o que é correto. Porém o comentário na linha 181-182 menciona "We can't pass a derived ctx upstream after WriteHeader" — a mutação da struct `r` in-place funciona aqui porque a chamada é síncrona, mas se o middleware `audit.Middleware` ler `r.Context()` em uma goroutine separada após `next.ServeHTTP` retornar, há uma possível race entre o `*r = *r.WithContext(...)` sendo aplicado e a leitura do `r.Context()` no middleware.

  Na prática, `audit.Middleware` lê `r.Context()` **após** `next.ServeHTTP(aw, r)` retornar (linha 78 do middleware), então é sequencial. Mas `*r = *r.WithContext(...)` substitui o valor apontado pela mesma referência `*http.Request` que o middleware ainda segura — isso é seguro em termos de sequenciamento, mas extremamente frágil: qualquer refactor que introduza concorrência (ex: audit writer em goroutine separada lendo `r`) quebraria isso.
- **Impact:** Atualmente funciona, mas cria um contrato implícito muito frágil. A mutação in-place de `*http.Request` viola o contrato da stdlib Go que diz que Request deve ser usado em apenas uma goroutine por vez.
- **Fix:** Propagar o contexto de forma mais robusta:

```go
// Em writeSensitiveBlock, em vez de mutar *r:
func (cfg DispatcherConfig) writeSensitiveBlock(w http.ResponseWriter, r *http.Request) {
    // Derive a new request with the override context, and use it for the
    // remainder of the call. The audit middleware reads r.Context() from the
    // same pointer, so we must update r in-place. This is a known limitation.
    // Alternatively, store the override in a response header that audit reads,
    // or pass a dedicated audit-key via a sync.Map keyed by request_id.
    //
    // For now: the in-place mutation is safe given sequential middleware, but
    // add a comment coupling this to audit.Middleware's sequential contract.
    *r = *r.WithContext(auditctx.WithUpstreamOverride(r.Context(), UpstreamBlockedSensitiveValue))
    // ^ Add prominent comment: MUST NOT be accessed concurrently; relies on
    // audit.Middleware reading r.Context() only after ServeHTTP returns.
    ...
}
```

---

### HIGH-03: `ToolCallTerminalGuard` re-panic incondicional — qualquer panic (inclusive real bugs) é relançado sem distinção

- **File:** `gateway/internal/proxy/toolcall.go:217-224`
- **Issue:** O `defer` na linha 210 faz `panic(rec)` em **qualquer** valor de `rec != nil`, incluindo panics de bugs reais que não são `http.ErrAbortHandler`. O comentário na linha 220 menciona "Only re-panic on http.ErrAbortHandler — that's the only panic value Go expects to surface from a handler. Any other panic is a real bug and should also propagate." — isso está correto na intenção, mas o código **não** distingue: ele relança indiscriminadamente.

  O problema é que para um panic real (ex: nil pointer dentro de um handler upstream), o gateway nunca chegará a registrar o erro estruturadamente via Sentry antes do re-panic, pois a lógica de emit da ferramenta de tool-call está antes da decisão de re-panic.
- **Impact:** Comportamento correto para `http.ErrAbortHandler` (relança OK). Para panics reais, o re-panic impede que o Sentry capture o stack trace completo do local original porque o `defer` consumiu o panic e o re-lançou de um local diferente.
- **Fix:**

```go
defer func() {
    rec := recover()
    if reqID != "" {
        flag := tci.Flag(reqID)
        if flag != nil && flag.Load() && !tw.sawDone {
            WriteSSEToolCallError(w, reqID, upstreamName, route)
            tci.Clear(reqID)
        }
    }
    if rec != nil {
        // Re-panic preserving the original value so http.Server's
        // recovery middleware and Sentry can classify it correctly.
        // http.ErrAbortHandler is the canonical "client disconnect" signal;
        // other values are real bugs that should propagate up.
        panic(rec)
    }
}()
```

Isso já é o que o código faz — o issue real é o comentário ser enganoso. O comportamento de re-panic irrestrito é funcionalmente correto; o problema é que Sentry não capturará o stack original para panics não-ErrAbortHandler. Para produção, adicione `sentry.RecoverWithContext(r.Context(), rec)` antes do re-panic.

---

### HIGH-04: `TokenCounter` usa `model` para chave de cache mas passa o `role` (não o model name) na chamada em `dispatcher.go`

- **File:** `gateway/internal/proxy/dispatcher.go:83`, `gateway/internal/proxy/tokencount.go:89`
- **Issue:** Em `dispatcher.go` linha 83, a chamada é:

```go
cfg.TokenCounter.Enforce(r.Context(), body, cfg.Role, cfg.ContextCap)
```

`cfg.Role` é `"llm"` ou `"embed"` — não o model name extraído do body. Já em `tokencount.go` linha 89, o parâmetro `model string` é usado na chave de cache:

```go
key := tokenCacheKey(model, hex.EncodeToString(sum[:]))
```

A consequência é que dois requests com corpos idênticos mas models diferentes (ex: `"qwen"` e `"qwen3"`) compartilham o mesmo cache slot porque ambos são chamados com `model="llm"`. Isso é benign para o enforcement (o tokenizer local é o mesmo independentemente do alias), mas a **semântica do cache está errada**: o comentário na linha 6-7 do tokencount.go diz explicitamente que o model é incluído na chave para "prevent cross-tokenizer collisions" — o que o código não garante.

Adicionalmente, quando o request vai para o fallback `openrouter-chat`, o tokenizer local ainda é consultado (o `TokenCounter` usa `cfg.UpstreamLLMURL` hardcoded), o que é correto por design (enforcement pré-dispatch). Mas a chave de cache com `model="llm"` para um request que tem `model="gpt-4o"` no body vai retornar uma contagem de tokens incorreta (Qwen tokenizer vs GPT-4 tokenizer) — ainda que fail-open, polui o cache.
- **Impact:** Cache hit falso: um request com `model="gpt-4o"` e body idêntico a um request anterior com `model="qwen"` vai reusar a contagem do tokenizer Qwen. Como a política é pass-through (fail-open), isso não bloqueia requests legítimos, mas pode aprovar requests `gpt-4o` ligeiramente acima do cap se o tokenizer Qwen sub-conta.
- **Fix:** Extrair o model name do body antes de chamar Enforce:

```go
// Em dispatcher.go, antes de Enforce:
modelName := extractModelName(body) // new helper: json.Unmarshal, m["model"].(string)
if _, terr := cfg.TokenCounter.Enforce(r.Context(), body, modelName, cfg.ContextCap); terr != nil {
```

---

### HIGH-05: `SensitiveRetry` consulta o breaker in-process (não o Redis mirror) — viola D-B1 explicitamente

- **File:** `gateway/internal/proxy/sensitive.go:54-61`
- **Issue:** A spec D-B1 diz: "Entre attempts, gateway re-consulta estado do breaker via Redis mirror (`gw:breaker:{upstream}`)". O código consulta `bs.Get(upstreamName)` que retorna o `*gobreaker.CircuitBreaker` **in-process**. Isso é funcionalmente correto em single-replica (o breaker in-process é autoritativo), mas:

  1. Viola o design explícito de D-B1 que especificou o Redis mirror como fonte de atualização entre attempts, com a razão de que o breaker pode ter fechado via probe em outro processo.
  2. Em multi-réplica (Fase 6): se o breaker fechou em outra réplica e o evento chegou via Pub/Sub, o overlay `remoteOpen` é limpo em `applyRemoteEvent` — mas o `gobreaker.State()` local ainda estará `OPEN` até a réplica local processar um sucesso. `SensitiveRetry` vai reportar OPEN mesmo quando outra réplica já fechou.
- **Impact:** Em single-replica, funciona corretamente (breaker in-process é autoritativo). Em Fase 6 (multi-réplica), um sensitive request pode receber 503 desnecessário porque a réplica local ainda mostra OPEN enquanto outra já fechou. Não é bloqueador agora, mas quebra a promessa de D-B1 e vai exigir refactor na Fase 6.
- **Fix (agora):** Consultar o overlay `remoteOpen` via `breakerSet.Execute` ou adicionar um método `IsLikelyClosed` que considera o overlay:

```go
// Alternativa simples que usa Execute para re-testar:
// Após o delay, tenta um Execute com uma fn que retorna nil imediatamente.
// Se o breaker estiver CLOSED, Execute passa; se OPEN, retorna ErrBreakerOpen.
_, execErr := bs.Execute(upstreamName, func() (*http.Response, error) {
    return nil, nil // sentinel probe
})
if execErr == nil {
    obs.SensitiveRetryTotal.WithLabelValues("closed").Inc()
    return true, nil
}
```

---

## Medium Findings

### MED-01: `probe.go` — Response body do probe não é lida antes de fechar (linha 275)

- **File:** `gateway/internal/upstreams/probe.go:275`
- **Issue:** `_ = resp.Body.Close()` sem fazer `io.Copy(io.Discard, resp.Body)` antes. Para respostas com body não consumido, `Close()` pode não retornar a conexão para o pool do `http.Transport`, causando "connection not reused" em Go's net/http (o transport só reutiliza a conexão se o body foi completamente drenado).
- **Impact:** Leak de conexões no probe client ao longo do tempo, especialmente quando o probe retorna 5xx com body (ex: mensagem de erro do upstream). Com 6 upstreams × probe a cada 10s, isso é ~36 conexões/min que podem vazar.
- **Fix:**

```go
_, _ = io.Copy(io.Discard, resp.Body)
_ = resp.Body.Close()
```

---

### MED-02: `health.go` — `upstreamStatus` nunca popula `last_probe_ms`, `last_probe_at`, `last_probe_status`

- **File:** `gateway/internal/upstreams/health.go:150-161`
- **Issue:** Os campos `LastProbeMs`, `LastProbeAt`, `LastProbeStatus` de `upstreamStatus` são declarados (linhas 43-47) e fazem parte do shape documentado em CONTEXT.md. Porém na `buildHealthResponse`, após criar `us := upstreamStatus{...}`, existe um bloco placeholder:

```go
if u.Tier >= 0 { /* always true; placeholder for future per-tier guards */
}
```

Os campos de probe nunca são populados. O payload de `/v1/health/upstreams` omite `last_probe_ms`, `last_probe_at`, `last_probe_status` de toda resposta, tornando o endpoint menos útil para operadores e quebrando a spec de CONTEXT.md "Claude's Discretion / GET /v1/health/upstreams".
- **Impact:** Dashboard Fase 7 não terá os dados de probe disponíveis no endpoint de health — vai precisar de uma query separada ao DB. Não é bloqueador para Fase 3 mas é um regression contra a spec.
- **Fix:** O `Loader.All()` retorna `UpstreamConfig` que não tem os campos `last_probe_*` (eles ficam na DB). A solução é adicionar esses campos ao `UpstreamConfig` e populá-los no `flushLoop` (ou aceitar que a health page mostra apenas estado do breaker). Como mínimo, remover o struct vazio e adicionar um TODO claro.

---

### MED-03: `openrouter_director.go` — Re-serialização via `map[string]json.RawMessage` pode alterar ordem de chaves (compatibilidade com logs/assinaturas)

- **File:** `gateway/internal/proxy/openrouter_director.go:77-91`
- **Issue:** `json.Marshal(m)` onde `m` é `map[string]json.RawMessage` não garante ordem de chaves. Go's `encoding/json` itera mapas em ordem aleatória. Se OpenRouter (ou algum middleware de logging/assinatura entre gateway e OpenRouter) for sensível à ordem de chaves, pode causar erros.

  Mais importante: ao re-serializar, campos com valores `json.RawMessage` são preservados byte-for-byte, mas o encoding final pode trocar aspas (ex: `{"stream":true}` vs `{"stream": true}` — spacing). Isso pode quebrar a idempotência de um request que o cliente enviou com um Content-MD5 ou Idempotency-Key.
- **Impact:** Baixo em produção (OpenRouter não valida ordem). Médio em testes que comparam body exato. Mencionado porque pode causar surpresas em Fase 4 quando idempotência em embeddings for adicionada.
- **Fix:** Documentar explicitamente que `injectProviderOrder` não preserva ordem de chaves, ou usar `json.Decoder` com `UseNumber()` para preservar formato numérico.

---

### MED-04: `subscribe.go` — Eventos do próprio processo recebidos via Pub/Sub (auto-echo) aplicam `remoteOpen` desnecessariamente

- **File:** `gateway/internal/breaker/subscribe.go:38-44`
- **Issue:** Quando o processo local publica um evento de estado via `publishTransition` (linha 221 em breaker.go), e também está subscrito ao mesmo canal (breakerSet.Subscribe), o evento retorna para o mesmo processo via `SubscribeBreakerEvents`. O handler `applyRemoteEvent` aplica o overlay `remoteOpen` para o upstream local.

  Para um evento OPEN do próprio processo: o breaker local já está OPEN (gobreaker autoritativo), então aplicar `remoteOpen` é redundante mas inócuo. Para um evento CLOSED/HALF-OPEN do próprio processo: `applyRemoteEvent` faz `delete(s.remoteOpen, ev.Upstream)` — também inócuo.

  O problema é mais sutil: se a sequência for (1) local processa OPEN, (2) publica, (3) recebe de volta, (4) a próxima req de outro processo já havia feito CLOSED, (5) o evento de CLOSED de outra réplica chega **antes** do auto-echo OPEN — então o auto-echo OPEN vai recolocar `remoteOpen` quando não deveria.
- **Impact:** Race condition de baixa probabilidade em multi-réplica: `remoteOpen` pode ser setado incorretamente por um auto-echo OPEN atrasado, causando rejeições desnecessárias de requests por `Cooldown` adicional.
- **Fix:** Adicionar um identificador de instância (ex: UUID gerado no boot) ao payload `BreakerEvent` e ignorar eventos com `InstanceID == localInstanceID` em `applyRemoteEvent`.

---

### MED-05: `dispatcher.go` — `SensitiveRetry` retorna `(bool, error)` mas o erro é ignorado, perdendo visibilidade de `ctx.Canceled`

- **File:** `gateway/internal/proxy/dispatcher.go:125-130`
- **Issue:**

```go
ok, _ := SensitiveRetry(r.Context(), cfg.Breaker, t0.Name)
if !ok {
    cfg.writeSensitiveBlock(w, r)
    return
}
```

O erro é descartado com `_`. Se `SensitiveRetry` retorna `(false, ctx.Err())` (client disconnect durante o wait), o dispatcher vai escrever a resposta 503 em um `ResponseWriter` para um cliente que já se desconectou. Isso não é um crash — o `Write` vai errar silenciosamente — mas a métrica `SensitiveRetryTotal{outcome=blocked_response}` será incrementada incorretamente para um request que foi cancelado, não bloqueado.
- **Impact:** Métrica inflada; auditoria imprecisa para requests cancelados durante o retry loop.
- **Fix:**

```go
ok, retryErr := SensitiveRetry(r.Context(), cfg.Breaker, t0.Name)
if !ok {
    if errors.Is(retryErr, context.Canceled) {
        // Client disconnected; nothing to write.
        return
    }
    cfg.writeSensitiveBlock(w, r)
    return
}
```

---

### MED-06: `config.go` — `UPSTREAM_HEALTH_BRIDGE_URL` ainda é variável obrigatória mas o Phase 3 tornou a health-bridge opcional/irrelevante

- **File:** `gateway/internal/config/config.go:138-153`
- **Issue:** `UPSTREAM_HEALTH_BRIDGE_URL` está na lista `required` (linha 152) e falha boot se ausente. O CONTEXT.md Phase 3 diz explicitamente: "Health-bridge :9100 continua como debug view do pod, não autoridade do gateway". A health do gateway agora vem do `upstreams.Loader` + `breaker.Set`, não da health-bridge.

  Isso significa que operadores que deployam apenas o gateway sem o pod ativo (ex: config parcial, teste) terão o boot falhando por uma variável que já não tem impacto na lógica de routing.
- **Impact:** Boot failure desnecessário em ambientes onde a health-bridge não está configurada (ex: Vast.ai pod down, manutenção). Não bloqueia funcionalidade de resiliência.
- **Fix:** Mover `UPSTREAM_HEALTH_BRIDGE_URL` para opcional com warn-log (consistente com os outros `UPSTREAM_*` Phase 3):

```go
// Em requiredOrder, remover "UPSTREAM_HEALTH_BRIDGE_URL"
// Em Load(), após a verificação de required:
if cfg.UpstreamHealthBridgeURL == "" {
    // Warn-only: health-bridge is now a pod-internal debug surface (Phase 3 D-D4)
    // not required for gateway operation.
}
```

---

## Low Findings

### LOW-01: `breaker.go` — `stateFloat` retorna `-1` para estado desconhecido sem log

- **File:** `gateway/internal/breaker/breaker.go:160-170`
- **Issue:** `stateFloat` retorna `-1` para estados não reconhecidos. A gauge `BreakerState` receberá `-1`, que não é documentada. Se `gobreaker/v2` adicionar um novo estado em versão futura, o gauge ficará em `-1` silenciosamente.
- **Fix:** Adicionar `slog.Warn("unknown breaker state", "state", st)` no default case.

---

### LOW-02: `tokencount.go` — `extractTokenizeText` ignora `tool_calls` e `system` messages no chat context

- **File:** `gateway/internal/proxy/tokencount.go:163-173`
- **Issue:** Para chat, apenas `messages[*].content` (string) é concatenado. `system` messages também têm content e são incluídas corretamente. Porém `tool_calls` (com `function.name` + `arguments`) e `tool_choice` são ignorados. Para requests com tools definidas (que podem representar 200-400 tokens no prompt de sistema do Qwen), a contagem será sub-estimada.
- **Impact:** Requests com tools complexas passarão pelo enforcement mesmo se estiverem ligeiramente acima do cap real. Conservador (fail-open), mas reduz a eficácia do enforcement RES-07.
- **Fix:** Incluir serialização de `tools` no texto tokenizado (ao menos o JSON bruto):

```go
if tools, ok := m["tools"]; ok {
    if b, err := json.Marshal(tools); err == nil {
        buf.Write(b)
    }
}
```

---

### LOW-03: `toolcall.go` — `tcGuardWriter.tail` usa `append(g.tail[:0], ...)` que pode reter a backing array de forma desnecessária

- **File:** `gateway/internal/proxy/toolcall.go:245-249`
- **Issue:**

```go
if len(combined) > 64 {
    g.tail = append(g.tail[:0], combined[len(combined)-64:]...)
```

`g.tail[:0]` reutiliza a backing array, o que é correto para eficiência. Mas `combined` é `append(g.tail, p...)` — se `g.tail` era a backing array compartilhada com `combined`, então `combined[len(combined)-64:]` é uma slice da mesma array que está sendo sobrescrita com `append`. Em Go, `append(g.tail[:0], combined[len(combined)-64:]...)` é seguro somente se os ranges não se sobreponham. Para `tail` de 64 bytes e `p` maior, há sobreposição possível.
- **Fix:** Usar `copy` explícito em vez de `append`:

```go
tail := make([]byte, 64)
n := copy(tail, combined[len(combined)-64:])
g.tail = tail[:n]
```

---

### LOW-04: `probe.go` — Probe STT com `model=whisper-1` pode falhar no upstream local (Speaches/whisper.cpp não aceita `whisper-1`)

- **File:** `gateway/internal/upstreams/probe.go:246`
- **Issue:** O field `model` no multipart do probe STT é `whisper-1` (hardcoded). Speaches (pod Phase 1 local STT) pode não reconhecer esse alias — o endpoint local provavelmente aceita o nome do modelo carregado (ex: `"faster-whisper"` ou o model file name). Uma resposta 4xx do Speaches seria classificada pelo breaker como "success" (D-A4 — 4xx não conta como falha), mas o `last_probe_status` ficaria como `"failed"`, confundindo operadores.
- **Impact:** Operadores verão `last_probe_status=failed` para `local-stt` mesmo quando o upstream está healthy. O breaker ficará CLOSED (correto), mas o dashboard mostrará sinal enganoso.
- **Fix:** Adicionar `PROBE_STT_MODEL` env var (default `"whisper-1"`; operador pode sobrescrever), ou tornar o model dinâmico baseado no `UpstreamConfig.Name` (ex: `whisper-1` para `openai-whisper`, empty string para `local-stt`).

---

### LOW-05: `openai_embed_director.go` — `dimensions=1024` hardcoded sem env var de override

- **File:** `gateway/internal/proxy/openai_embed_director.go:7,64-74`
- **Issue:** O valor `1024` é hardcoded para `dimensions` no rewrite do OpenAI embed. O comentário menciona que é para "BGE-M3 dimensional parity". Se o modelo local for trocado para um com dimensionalidade diferente (ex: 768 ou 3072), o fallback externo vai produzir vetores incompatíveis sem qualquer aviso em runtime.
- **Fix:** Adicionar `UPSTREAM_EMBED_DIMENSIONS` env var (default `1024`) e passar como parâmetro para `BuildOpenAIEmbedDirector`.

---

## Strengths

**Pitfall 3 (errgroup zero-value) foi implementado corretamente.** `probe.go` usa `var g errgroup.Group` (zero-value, SEM `errgroup.WithContext`) e cada `g.Go` sempre retorna `nil`, garantindo que a falha de um probe não cancela os outros 5 do mesmo tick. O comentário na linha 185 explica o rationale. Excelente execução de um pitfall não-óbvio.

**Pitfall 5 (ctx-safe sleep) foi implementado corretamente.** `sensitive.go` usa `select { case <-ctx.Done(): ...; case <-time.After(d): }` em vez de `time.Sleep(d)`. Cada attempt do retry loop é cancelável por disconnect do cliente sem goroutine leak. O comentário referencia `TestSensitiveRetry_ClientDisconnectExits`.

**Pitfall 7 (reload-storm) foi endereçado com precisão cirúrgica.** A migration `0009` cria dois triggers separados (Postgres não permite WHEN referenciando OLD em trigger de INSERT): `upstreams_insert_delete_notify` (sempre dispara) e `upstreams_update_notify` (dispara apenas quando colunas de config mudam, **não** `last_probe_*`). Isso previne que 6 probe writebacks/10s causem 6 hot-reloads/10s. Solução canônica e documentada.

**Atomic snapshot swap no Loader é lock-free e correto.** `atomic.Pointer[snapshot]` com `Store` atomico e leitura via `Load()` sem mutex no hot path. O snapshot é imutável — uma vez armazenado, nenhum campo é mutado. Defesiva cópia em `All()` via `copy()`. Pattern correto.

**IsSuccessful filter (D-A4) está correto.** `context.Canceled` e `HTTPError{Status >= 400 && < 500}` retornam `true` (não contam como falha), enquanto `context.DeadlineExceeded`, `net.Error` e 5xx retornam `false`. A classificação 429 via `HTTPError{Status: 429}` é tratada como 4xx — correto per D-A4. O type `HTTPError` elimina string parsing.

**Bearer tokens nunca serializam em JSON/logs.** `AuthBearer` em `UpstreamConfig` tem tag `json:"-"` (linha 24 de types.go), garantindo que nunca aparece em respostas de `/v1/health/upstreams`, gatewayctl JSON output, ou log lines que serializem a struct. Comentário explícito: "T-03-04-03 mitigation".

**Redis fallback gracioso (D-D1).** `publishTransition` roda em goroutine separada (`go s.publishTransition(n, to)`), não bloqueia a state machine do gobreaker, incrementa `BreakerMirrorFailuresTotal` e loga em WARN. Se Redis cair, breakers continuam operando in-process. A arquitetura híbrida está corretamente implementada.

**NOTIFY trigger split (INSERT/DELETE vs UPDATE) com `pg_trigger_depth() = 0`** previne cascade recursivo corretamente. O `pg_trigger_depth() = 0` garante que triggers aninhados não disparam notificações extras.

**Hot-reload via pgxlisten com reconexão.** `listen.go` usa `pgxlisten.Listener` (biblioteca do mesmo autor do pgx) que gerencia reconnect com delay configurável de 5s. O handler retorna `nil` em caso de falha de refresh (não derruba o listener), e erros de reconnect são logados via `LogError`. Correto.

---

_Reviewed: 2026-04-20T03:00:00Z_
_Reviewer: Claude (gsd-code-reviewer)_
_Depth: standard_
