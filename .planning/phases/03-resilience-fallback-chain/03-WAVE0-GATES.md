# Phase 3 — Wave 0 Operator Gates

**Executed:** 2026-04-20 00:15
**Operator:** Pedro <pedro.araujo@ifixtelecom.com.br>
**Verifier:** Claude (orchestrator)

## Gate A — OpenRouter Slug for Qwen 3.5 27B

### Original D-C1 (Fireworks pin) — INVALIDATED

The plan assumed Fireworks served Qwen 3.5 27B. **Empirically false (2026-04-20):**

```bash
curl -X POST https://openrouter.ai/api/v1/chat/completions \
  -d '{"model":"qwen/qwen3.5-27b","provider":{"order":["fireworks"],"allow_fallbacks":false}, ...}'
# → {"error":{"message":"No endpoints found for qwen/qwen3.5-27b.","code":404}}
```

Fireworks does not currently serve **any** Qwen 3 model on OpenRouter (verified across `qwen/qwen3-32b`, `qwen/qwen3-235b-a22b-2507`, `qwen/qwen3-30b-a3b-instruct-2507`, `qwen/qwen3-vl-32b-instruct`).

### Resolution — Switch provider pin to Novita

OpenRouter `qwen/qwen3.5-27b` resolves to canonical slug `qwen/qwen3.5-27b-20260224`, served by:

| Provider | Status | Tool calls | Notes |
|----------|--------|------------|-------|
| Alibaba | -2 (degraded) | yes | Slow but functional |
| **Novita** | **0 (healthy)** | **yes** | **Picked** |
| AtlasCloud | 0 (healthy) | n/a | Pin name mismatch in test |
| Phala | 0 (healthy) | (untested) | Backup |

### Verification curl (Novita pin)

```bash
curl -sS -X POST https://openrouter.ai/api/v1/chat/completions \
  -H "Authorization: Bearer $OPENROUTER_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model":"qwen/qwen3.5-27b",
    "provider":{"order":["novita"],"allow_fallbacks":false},
    "messages":[{"role":"user","content":"reply with exactly: PONG"}],
    "max_tokens":10,"temperature":0
  }'
```

**Response excerpt:**
```json
{"model":"qwen/qwen3.5-27b-20260224","provider":"Novita","choices":[{"message":{"content":"PONG"}}]}
```

### Tool-call verification (D-C2 / RES-06 pre-flight)

```bash
curl ... -d '{
  "model":"qwen/qwen3.5-27b",
  "provider":{"order":["novita"],"allow_fallbacks":false},
  "messages":[{"role":"user","content":"What is the weather in Sao Paulo right now?"}],
  "tools":[{"type":"function","function":{"name":"get_weather", ...}}],
  "tool_choice":"auto","max_tokens":100
}'
```

**Response excerpt:**
```json
{
  "model":"qwen/qwen3.5-27b-20260224","provider":"Novita",
  "choices":[{
    "finish_reason":"tool_calls",
    "message":{"tool_calls":[{
      "id":"call_7aeb0998b25447f69ebef8ea",
      "function":{"name":"get_weather","arguments":"{\"city\": \"Sao Paulo\"}"}
    }]}
  }]
}
```

### Decision: PROCEED with revised D-C1

- **Model slug:** `qwen/qwen3.5-27b` (canonical: `qwen/qwen3.5-27b-20260224`)
- **Provider pin:** `novita` (replaces Fireworks)
- **Tool support:** confirmed (`finish_reason: "tool_calls"` with valid arguments)

### Env vars to configure in Portainer (post-Phase 3 deployment)

```
UPSTREAM_LLM_OPENROUTER_URL=https://openrouter.ai/api/v1
UPSTREAM_LLM_OPENROUTER_AUTH_BEARER=<operator mints OPENROUTER_API_KEY>
UPSTREAM_LLM_OPENROUTER_MODEL=qwen/qwen3.5-27b
UPSTREAM_LLM_OPENROUTER_PROVIDER_ORDER=novita
```

### D-C1 amendment recorded

CONTEXT.md decision D-C1 (pin Fireworks) is replaced by **D-C1' (pin Novita)** for the duration of Phase 3 execution. Reason: Fireworks does not serve Qwen 3 family on OpenRouter as of 2026-04-20. Future re-evaluation: monitor OpenRouter provider catalog quarterly; if Fireworks adds Qwen 3.x, consider switching back for parity with original ConverseAI provider mix (D-C1 rationale).

---

## Gate B — llama.cpp `/tokenize` endpoint

### Pod runtime not available

No GPU on dev VPS (`nvidia-smi: command not found`); no Vast.ai pod active. Direct test against the production pod image (`ghcr.io/ifixtelecom/ifix-ai-pod:develop`) is impossible from this environment.

### Verification path: upstream binary inspection

The pod Dockerfile (`pod/Dockerfile`) extracts `llama-server` from upstream:

```dockerfile
FROM ghcr.io/ggml-org/llama.cpp:server-cuda AS llama-bin
COPY --from=llama-bin /app/llama-server /usr/local/bin/llama-server
```

The `/tokenize` endpoint is a built-in route of llama-server (not opt-in). Verified by running the **CPU equivalent** of the same upstream image (`ghcr.io/ggml-org/llama.cpp:server`) locally with a minimal Qwen 2.5 0.5B Q4_K_M model:

```bash
docker run -d -p 18000:8000 -v /tmp/qwen-0.5b.gguf:/model.gguf:ro \
  ghcr.io/ggml-org/llama.cpp:server \
  --host 0.0.0.0 --port 8000 -m /model.gguf -np 1 --ctx-size 2048
```

**Verification curls:**

```bash
$ curl -sS http://localhost:18000/health
{"status":"ok"}

$ curl -sS http://localhost:18000/tokenize \
    -H "Content-Type: application/json" -d '{"content":"ping"}' | jq '.'
{
  "tokens": [
    9989
  ]
}
```

Endpoint exists on the same llama.cpp release line baked into the pod image (Pod Dockerfile pins `:server-cuda` of the same upstream tag family — only difference is CUDA backend which does not affect HTTP routing).

### Decision: PROCEED

- **Pod image tag verified for `/tokenize`:** `ghcr.io/ggml-org/llama.cpp:server-cuda` (upstream) → `ghcr.io/ifixtelecom/ifix-ai-pod:develop` (production tag)
- **Endpoint contract:** `POST /tokenize` with `{"content": "<text>"}` → `{"tokens": [<int_array>]}`
- **No pod image rebuild required**

### Residual UAT (deferred to Phase 6 / Wave 6 of Phase 3)

A live test against an actual production pod (Vast.ai or local with GPU) is still recommended once a pod is provisioned. Captured as part of `03-08-PLAN.md` UAT (real pod kill scenario) and Phase 6 (auto-provisioning).

---

## Sign-off

- [x] **Gate A** verified — slug `qwen/qwen3.5-27b` + provider `novita` (D-C1 amended)
- [x] **Gate B** verified — `/tokenize` confirmed on upstream llama-server (same binary as pod image)
- [x] Both gates **PASS** → Phase 3 implementation waves unblocked

## Follow-ups

1. **Wave 4 (03-06) — OpenRouter director:** must use `provider.order=["novita"]` (not `["fireworks"]`) when constructing the body rewrite. Update `D-C2` body-rewrite template accordingly.
2. **Wave 6 (03-08) — UAT:** include "verify /tokenize on a live Vast.ai pod" as one of the operator-only checks before declaring SC-1 fully PASS.
3. **CONTEXT.md amendment:** consider adding a note that D-C1 was revised to Novita on 2026-04-20 to preserve traceability.
