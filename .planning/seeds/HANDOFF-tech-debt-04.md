# HANDOFF — Tech Debt #4 (chat_completions override dispatch)

**Created:** 2026-05-22
**Last action:** /gsd:debug --diagnose primary-override-dispatch-503 → root_cause_found
**Next session goal:** Regression test + live UAT to flip tech debt #4 from OPEN → CLOSED in STATE.md

---

## TL;DR

Tech debt #4 (STATE.md Phase 06.6) é **HISTÓRICO** — fix já mergeado em `30f90e7` + `12f7479`, deployado na imagem `d6893211` em `vps-ifix-vm`. Falta apenas:

1. Regression test em Go (zero custo, ~30min)
2. UAT live Vast 4090 gateway-path (~$0.40, ~30min wall)
3. Update STATE.md: `OPEN → CLOSED — verified <date>`

---

## Contexto técnico (não re-investigar)

### Bug original (commit `bda05fb`, parent de `af81836`)

- `Loader.Resolve("llm"|"stt", 0)` com `OverrideTier0` ativo retorna sintético `Name="emergency_pod_<role>"` (`gateway/internal/upstreams/loader.go:205-237`).
- `dispatcher.dispatchTo("emergency_pod_llm", …)` faz lookup `cfg.Proxies["emergency_pod_llm"]` (`gateway/internal/proxy/dispatcher.go:323-329`).
- `bda05fb` `llmRoleProxies` continha SÓ `{"local-llm"}` + opcional `"openrouter-chat"`. Sem `"emergency_pod_llm"`.
- Lookup miss → branch `!ok` → 503 `{"error":{"type":"service_unavailable","code":"upstream_unavailable","message":"Upstream proxy not registered."}}`.
- Direct-probe ao pod 200 OK porque bypassa gateway.

### Fix já shipped

| Commit | Escopo |
|--------|--------|
| `30f90e7` | `fix(06.7)`: register dynamic `emergency_pod_{llm,stt}` proxies |
| `12f7479` | `fix(06.7-07)`: register dynamic `emergency_pod_tts` proxy |

Ambos ancestors de HEAD `1e3c62e`. Presentes na imagem `d6893211` (latest-dev) rodando em `vps-ifix-vm`.

### Hipótese STATE.md descartada

STATE narrative (UAT 14): "Probe loop ainda lê UPSTREAM_LLM_URL, não a override URL → bug é no probe."

**FALSA.** Probe ortogonal: `OverrideTier0` short-circuita `Resolve` ANTES do breaker check de local-llm. Bug real era proxy-registration gap (não probe routing). Probe continua probendo placeholders (E7 da sessão), mas tráfego flui via `emergency_pod_<role>` corretamente.

---

## Próximas tarefas (ordem)

### 1. Regression test — Dispatcher 503 quando emergency_pod_* faltando

**Arquivo:** `gateway/internal/proxy/dispatcher_test.go` (existente, append novo test).

**Objetivo:** Boot mini dispatcher com `llmRoleProxies` SEM `"emergency_pod_llm"`. Set `OverrideTier0("llm", "http://fake-pod:8000")` no Loader. Issue request. Assert response = 503 com envelope exato:

```json
{"error":{"type":"service_unavailable","code":"upstream_unavailable","message":"Upstream proxy not registered."}}
```

**Por quê:** Protege contra drift se refactor futuro dropar registration de `emergency_pod_*`. Captura exatamente o bug `bda05fb`.

**Esboço:**
```go
func TestDispatcher_503WhenEmergencyProxyMissing(t *testing.T) {
    loader := upstreams.NewLoader(...) // local-llm row only
    loader.OverrideTier0("llm", "http://fake-pod:8000")
    breaker := breaker.NewSet(...)

    cfg := proxy.DispatcherConfig{
        Role:    "llm",
        Loader:  loader,
        Breaker: breaker,
        Proxies: map[string]http.Handler{
            "local-llm": http.HandlerFunc(...), // intentionally NO emergency_pod_llm
        },
    }
    handler := proxy.NewDispatcher(cfg)

    rw := httptest.NewRecorder()
    req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"qwen3","messages":[...]}`))
    handler.ServeHTTP(rw, req)

    require.Equal(t, 503, rw.Code)
    require.Contains(t, rw.Body.String(), "Upstream proxy not registered")
}
```

**Verificar tests companion existem:** `TestDispatcher_RoutesToEmergencyProxy_WhenOverrideActive` (caminho positivo) — se NÃO existir, adicionar TAMBÉM (boot com `emergency_pod_llm` registrado → assert 200).

**Run:** `cd gateway && go test ./internal/proxy/... -run TestDispatcher -v`

**Commit:** `test(gateway/proxy/dispatcher): regression — 503 when emergency_pod_<role> proxy missing`

### 2. UAT live Vast 4090 — gateway-path chat completions

**Pre-flight check (free):**
```bash
ssh vps-ifix-vm 'docker logs ai-gateway-dev --tail 50 | grep -E "loaded|upstreams refreshed|tier-0"'
ssh vps-ifix-vm 'docker ps --filter name=ai-gateway-dev --format "{{.Image}}"'
# Confirmar imagem d6893211 ou mais novo (deve conter 30f90e7 + 12f7479)
```

**Provisionar pod:**
```bash
gatewayctl primary force-up --reason "td04_uat_gateway_path"
# Esperar log: "primary_ready observed; emerg not active" (~5-25min cold-start)
```

**Issue gateway-path probe:**
```bash
# Pegar pod URL para comparação
gatewayctl primary state | grep -E "url|fingerprint"

# Criar tenant key fresh
KEY=$(gatewayctl key create -tenant td04-uat-converseai --raw)

# Gateway path
GW="http://localhost:GW_PORT"  # ou DNS interno do container
curl -sS -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  $GW/v1/chat/completions \
  -d '{"model":"qwen3","messages":[{"role":"user","content":"hi"}],"stream":false}' \
  | jq '{system_fingerprint, content: .choices[0].message.content, error}'
```

**Assertions:**
- HTTP 200 (não 503)
- `system_fingerprint` matching `b9191-*` (prova foi pro pod, não OpenRouter)
- `choices[0].message.content` não-vazio
- Zero ocorrência de "Upstream proxy not registered" em `docker logs ai-gateway-dev`

**Direct-probe baseline (sanity):**
```bash
POD_URL=$(gatewayctl primary state --raw | jq -r '.upstreams.llm')
curl -sS $POD_URL/v1/chat/completions -d '...' | jq .system_fingerprint
# Deve casar com a resposta do gateway-path
```

**Cleanup:**
```bash
gatewayctl primary force-down --reason "td04_uat_complete"
# Wait BestEffortDestroy → Vast instance count 0
```

**Custo esperado:** ~$0.30-0.40 (Vast 4090 ~25min wall).

### 3. Update STATE.md

Editar narrative Phase 06.6 tech debt list, item #4:

```diff
- 4. (was #4) Gateway `chat_completions` proxy returns 503 `Upstream proxy not registered`...
+ 4. ~~Gateway `chat_completions` proxy returns 503 `Upstream proxy not registered`~~
+    **RESOLVED <date> via 30f90e7 + 12f7479 (already shipped before debug session).**
+    Verified gateway-path UAT <date>: lifecycle <N>, pod URL <ip>:<port>, system_fingerprint=b9191-*, gateway 200 + matching fingerprint. Regression test in dispatcher_test.go locks the contract.
```

Atualizar também:
- `.planning/PROJECT.md` Active list se há referência ao bug.
- `.planning/debug/primary-override-dispatch-503.md` → mover para `.planning/debug/resolved/` (se convenção do projeto for essa).

### 4. Commit

```
docs(06.6): close tech debt #4 — emergency_pod_* dispatch wiring

UAT <date> on lifecycle <N> proved gateway-path /v1/chat/completions
returns 200 with system_fingerprint=b9191-* (matches direct-probe).
Fix shipped in 30f90e7 + 12f7479; regression test landed in
dispatcher_test.go. STATE.md updated.
```

---

## Estado atual do branch

- Branch: `develop` (tip `7330aa0` — chore move HANDOFF/MINIO into seeds/)
- Last test run: green (per Phase 06.8 verification commit `1e3c62e`)
- Untracked working tree files: `.claude/`, `ConverseAI_GPU_Stack_Guide.docx*`, `gatewayctl` binary. Ignore — não tocar.
- `M .planning/config.json` — verificar diff antes de qualquer commit relacionado.

---

## Comandos rápidos para próxima sessão

```bash
# Resumir sessão debug
cat .planning/debug/primary-override-dispatch-503.md | head -160

# Confirmar fixes presentes
git log --oneline 30f90e7^..12f7479
git merge-base --is-ancestor 30f90e7 HEAD && echo "30f90e7 OK"
git merge-base --is-ancestor 12f7479 HEAD && echo "12f7479 OK"

# Confirmar deploy image
ssh vps-ifix-vm 'docker inspect ai-gateway-dev --format "{{.Image}}"'

# Tests companion existem?
grep -rn "TestDispatcher" gateway/internal/proxy/dispatcher_test.go
```

---

## Coisas que NÃO fazer

- Não rodar `/gsd-debug` de novo para `primary-override-dispatch-503` — sessão já resolvida.
- Não editar source files relacionados ao override dispatch sem rodar regression test primeiro (CLAUDE.md anti-blind-commit gate).
- Não gastar Vast antes da regression test rodar local — barato fechar análise estática primeiro.
- Não cair na hipótese antiga "probe loop é o bug" — descartada (E7 da sessão prova).
