# 06-WAVE0-GATES.md — Operator Decisions (Phase 6 Wave 0)

> Wave 0 Task 2 + Task 3 (06-01-PLAN.md). Operator-level decisions que pousam constraints determinísticos pra Tasks de plans 06-02 .. 06-06.

**Decided:** 2026-05-16
**Operator:** Pedro
**Reference spike:** [06-SPIKE-runtype-args.md](./06-SPIKE-runtype-args.md)

---

## Decision 1 — Jinja Strategy

| Field | Value |
|-------|-------|
| **Strategy** | **B2 — MinIO fetch** |
| **Disk filter** | **40 GB** |
| **CONTEXT.md anchor** | D-04-B option B2 (default) |

**Rationale:** Sem nova GHCR custom image. Preserva intent D-08-B ("no custom image"). Disk=40GB abre mais hosts spot market (RESEARCH.md OQ6). Onstart inline cresce ~5 linhas (curl + sha256-c + write), aceitável (token margin <1500 confirmed por spike).

---

## Decision 2 — Jinja MinIO Coordinates

| Field | Value |
|-------|-------|
| **Bucket** | `ai-gateway` |
| **Key** | `emerg-onstart/templates/qwen3.5-27b-tool-calling-1067302cc6d927210a84775b9a060f724da15debc168c79710cbf763512e9f67.jinja` |
| **sha256** | `1067302cc6d927210a84775b9a060f724da15debc168c79710cbf763512e9f67` |
| **Size** | 8595 bytes (8.4 KiB) |
| **ETag** | `0fac667d1759def875afb1d0fc4d7f00` |
| **Upload date** | 2026-05-16 23:05:18 -03 |
| **Verified** | `mc cat ... | sha256sum` == sha256 acima ✅ |

**Source-of-truth no repo:** `pod/templates/qwen3.5-27b-tool-calling.jinja` (kept para reprodução upload futura).

**Config defaults pra plan 06-02 (que adiciona fields em `gateway/internal/config/config.go`):**

```go
EmergencyJinjaTemplateKey:     "emerg-onstart/templates/qwen3.5-27b-tool-calling-1067302cc6d927210a84775b9a060f724da15debc168c79710cbf763512e9f67.jinja",
EmergencyJinjaTemplateSHA256:  "1067302cc6d927210a84775b9a060f724da15debc168c79710cbf763512e9f67",
```

Não-vazios por default (D-04-B locked B2). Override via env `EMERGENCY_JINJA_TEMPLATE_KEY` / `EMERGENCY_JINJA_TEMPLATE_SHA256` se rota futura precisar trocar arquivo.

---

## Decision 3 — Image Tag (D-01-B Confirm)

| Field | Value |
|-------|-------|
| **EmergencyTemplateImage default** | `ghcr.io/ggml-org/llama.cpp:server-cuda-b9128` |
| **Empirical evidence** | Spike Round 2 — image pull + run ✅ |
| **Runtype** | `args` (vast.CreateRequest.Runtype) |
| **Entrypoint override** | `/bin/bash` (REQUIRED — see SPIKE finding) |

---

## Decision 4 — Args Strategy (D-07-B Revisao Pós-Spike)

CONTEXT.md D-07-B verbatim args slice de 15 tokens (`--host 0.0.0.0 ...`) **NÃO funciona** em args runtype porque ENTRYPOINT do image é `llama-server` direto. Spike provou que precisa override entrypoint.

**Strategy B revised payload:**

```go
vast.CreateRequest{
    Image:      cfg.EmergencyTemplateImage,
    Disk:       40,
    Runtype:    "args",
    Entrypoint: "/bin/bash",
    Args:       []string{"-c", emergencyOnstart},   // 2 elements only
    Env:        emergencyEnvMap,
}
```

Onde `emergencyOnstart` é raw-string Go (backtick) terminando em:
```bash
exec /app/llama-server \
  --host 0.0.0.0 \
  --port 8000 \
  -m /weights/qwen/model.gguf \
  -ngl 99 \
  -np 2 \
  --ctx-size 16384 \
  --jinja \
  --chat-template-file /app/templates/qwen3.5-27b-tool-calling.jinja
```

Plan 06-04 must_haves truth #6 ("Args slice contem 15 tokens exatos") DEVE ser **atualizado** pra refletir 2-element `[]string{"-c", <script>}` com llama-server flags _dentro_ do script (via `exec`).

**Action item para plan 06-04:** Verificar que must_haves description reflete revised pattern OU atualizar via plan-revision step (deviation handling).

---

## Decision 5 — Grep Survey Scope (Wave 1 Whitelist)

**Comando executado em ops-claude (2026-05-16):**
```bash
grep -rln "EmergencyPodImageTag\|ifix-ai-pod\|EMERGENCY_POD_IMAGE_TAG\|Runtype.*\"ssh\"" gateway/ --include='*.go'
```

**Resultado: exatamente 6 arquivos (matches PATTERNS.md predicted):**

| # | File | Touched by plan |
|---|------|------------------|
| 1 | `gateway/internal/config/config.go` | 06-02 |
| 2 | `gateway/internal/config/config_test.go` | 06-02 |
| 3 | `gateway/internal/emerg/lifecycle.go` | 06-04 |
| 4 | `gateway/internal/emerg/vast/types.go` | 06-03 |
| 5 | `gateway/internal/emerg/errors.go` | 06-02 ou 06-04 (comentário só — cleanup) |
| 6 | `gateway/internal/integration_test/emerg_leader_test.go` | 06-05 |

✅ Scope batido — Wave 1+ não precisa expandir. Nenhuma surpresa.

---

## Sign-off

Operator decisions fechadas. Plans 06-02, 06-03, 06-04, 06-05 podem prosseguir com Tasks determinísticas. Atenção especial pra plan 06-04: deviation pattern entrypoint+args bash precisa documentação no SUMMARY.

→ Wave 1 unblocked (06-02 + 06-03 paralelo).
