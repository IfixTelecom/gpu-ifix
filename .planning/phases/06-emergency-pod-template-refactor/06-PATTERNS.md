# Phase 6: Emergency-Pod Template Refactor (Strategy B) - Pattern Map

**Mapped:** 2026-05-16
**Files analyzed:** 13 (8 refactor/edit, 1 new optional, 4 delete-PR2)
**Analogs found:** 13 / 13 (todos têm analog dentro do próprio módulo)

## File Classification

| Arquivo a tocar | Role | Data Flow | Closest Analog | Match Quality |
|-----------------|------|-----------|----------------|---------------|
| `gateway/internal/emerg/lifecycle.go` (refactor `buildCreateRequest`) | service (request builder) | request-response (Vast PUT) | `gateway/internal/emerg/lifecycle.go:649-697` (próprio função atual) | exact (substituição in-place) |
| `gateway/internal/emerg/vast/types.go` (add `Args []string`) | DTO | request-response | `gateway/internal/emerg/vast/types.go:120-129` (`CreateRequest` struct atual) | exact |
| `gateway/internal/emerg/vast/client.go` (verify marshalling) | service (HTTP client) | request-response | `gateway/internal/emerg/vast/client.go:199-238` (`CreateInstance`) | exact — `json.Marshal(body)` já trata novo field via JSON tag (0 lines change) |
| `gateway/internal/config/config.go` (add fields, remove `EmergencyPodImageTag`) | config | startup-load | `gateway/internal/config/config.go:119-149` (block Phase 6 emergency-pod) | exact (extensão do bloco existente) |
| `gateway/internal/config/config_test.go` (update Phase 6 tests) | test | unit | `gateway/internal/config/config_test.go:478-579` (`TestLoad_Phase6Defaults` + `_CustomValues`) | exact |
| `gateway/.env.portainer.dev` (substituir env var) | config | startup-load | linhas correlatas para `WEIGHTS_QWEN_*`/`MINIO_*` no mesmo arquivo | role-match (mesmo formato KEY=VALUE) |
| `gateway/internal/emerg/lifecycle_test.go` (novo `TestBuildCreateRequest_*`) | test | unit | `gateway/internal/emerg/lifecycle_test.go:24-65` (`TestFilterBelowCap_*`, `TestExcludeHost`) | role-match (mesma estratégia: helpers puros + testify/require) |
| `gateway/internal/integration_test/emerg_leader_test.go:47` (atualizar `cfg.EmergencyPodImageTag`) | test | integration | mesmo arquivo, `defaultTestCfg` | exact |
| `pod/Dockerfile` (DELETE — PR2 D-08-B) | infra build | n/a | — | n/a |
| `pod/scripts/emerg-bootstrap.sh` (DELETE — PR2 D-08-B) | shell script | runtime | `pod/onstart.sh:80-92` + `pod/scripts/download-weights.sh:46-66` (padrão sha256-verify reusado inline) | exact |
| `.github/workflows/build-pod.yml` (DELETE — PR2 D-08-B) | CI/CD | event-driven | — | n/a |
| `pod/templates/qwen3.5-27b-tool-calling.jinja` + `.sha256` (CONDICIONAL B1: manter & rebuild image; B2: upload MinIO + delete) | template asset | runtime | mesmo arquivo | n/a |
| `pod/sshd_config` | — | — | NÃO existe no repo (confirmed via `test -f`) — nada a deletar | n/a |

## Pattern Assignments

### `gateway/internal/emerg/lifecycle.go` — refactor `buildCreateRequest` (service, request-response)

**Analog:** mesmo arquivo, linhas 649-697 (substituição in-place).

**Current code to replace** (lifecycle.go:649-697):

```go
// buildCreateRequest assembles the CreateRequest body for PUT /asks/{id}/.
// The image tag comes from Cfg.EmergencyPodImageTag (default "v1.0").
// ...
func (r *Reconciler) buildCreateRequest(offer vast.Offer, lifecycleID int64) vast.CreateRequest {
	return vast.CreateRequest{
		ClientID: "me",
		Image:    "ghcr.io/ifixtelecom/ifix-ai-pod:" + r.deps.Cfg.EmergencyPodImageTag,
		Env: map[string]string{
			"-p 8000:8000": "1",
			"MINIO_ENDPOINT":         r.deps.Cfg.MinioEndpoint,
			// ... 9 outros env vars MINIO/WEIGHTS_* incluindo WHISPER + BGE_M3
			"WEIGHTS_BGE_M3_SHA256":  r.deps.Cfg.WeightsBGEM3SHA256,
		},
		Onstart: "",
		Runtype: "ssh",
		Disk:    80,
		Label:   fmt.Sprintf("ifix-emerg-lifecycle-%d", lifecycleID),
		TargetState: "running",
	}
}
```

**New pattern to copy** — combinar (a) raw-string onstart (Pitfall 9, RESEARCH.md:484-493), (b) `args` []string (RESEARCH.md:264-292), (c) Env map mantém pattern Vast `-p HOST:CONTAINER` (linhas 660-665 preservar):

```go
// Onstart raw-string (Pitfall 9 RESEARCH.md:476-495) — sem fmt.Sprintf, vars vêm de Env map.
const emergencyOnstart = `#!/bin/bash
set -euo pipefail
WEIGHTS_PATH=/weights/qwen/model.gguf
mkdir -p "$(dirname "$WEIGHTS_PATH")"
if [[ ! -f "$WEIGHTS_PATH" ]]; then
  apt-get update && apt-get install -y curl ca-certificates >/dev/null
  curl -sL https://dl.min.io/client/mc/release/linux-amd64/mc -o /usr/local/bin/mc
  chmod +x /usr/local/bin/mc
  mc alias set ifix "$MINIO_ENDPOINT" "$MINIO_ACCESS_KEY" "$MINIO_SECRET_KEY" >/dev/null
  mc cp "ifix/${MINIO_BUCKET}/${WEIGHTS_QWEN_KEY}" "$WEIGHTS_PATH"
  ACTUAL=$(sha256sum "$WEIGHTS_PATH" | awk '{print $1}')
  [[ "$ACTUAL" = "$WEIGHTS_QWEN_SHA256" ]] || { echo "sha256 mismatch"; exit 1; }
fi
# (Opção B2: + 5 linhas para download Jinja template MinIO + sha256-verify)
`
```

**sha256-verify pattern (reused from `pod/scripts/emerg-bootstrap.sh:52-58`):**

```bash
ACTUAL=$(sha256sum "$WEIGHTS_PATH" | awk '{print $1}')
if [[ "$ACTUAL" != "$WEIGHTS_QWEN_SHA256" ]]; then
  echo "[emerg-bootstrap] FATAL: SHA-256 mismatch on $WEIGHTS_PATH" >&2
  echo "  expected: $WEIGHTS_QWEN_SHA256" >&2
  echo "  actual:   $ACTUAL" >&2
  exit 1
fi
```

**Env map pattern to preserve** (lifecycle.go:660-680, simplificar — remover WHISPER/BGE_M3 que não são usados em LLM-only emergency pod):

```go
Env: map[string]string{
    "-p 8000:8000":       "1",  // Vast Docker port forwarding convention
    "MINIO_ENDPOINT":     r.deps.Cfg.MinioEndpoint,
    "MINIO_BUCKET":       r.deps.Cfg.MinioBucket,
    "MINIO_ACCESS_KEY":   r.deps.Cfg.MinioAccessKey,
    "MINIO_SECRET_KEY":   r.deps.Cfg.MinioSecretKey,
    "WEIGHTS_QWEN_KEY":   r.deps.Cfg.WeightsQwenKey,
    "WEIGHTS_QWEN_SHA256": r.deps.Cfg.WeightsQwenSHA256,
    // B2 only:
    // "JINJA_TEMPLATE_KEY":    r.deps.Cfg.EmergencyJinjaTemplateKey,
    // "JINJA_TEMPLATE_SHA256": r.deps.Cfg.EmergencyJinjaTemplateSHA256,
},
```

**Args pattern (NEW)** — Vast `args` field (REMAINDER semantics, RESEARCH.md:264-280):

```go
Args: []string{
    "--host", "0.0.0.0",
    "--port", "8000",
    "-m", "/weights/qwen/model.gguf",
    "-ngl", "99",
    "-np", "2",
    "--ctx-size", "16384",
    "--jinja",
    "--chat-template-file", "/app/templates/qwen3.5-27b-tool-calling.jinja",
},
```

**Final assembly** (substitui linhas 649-697 inteiras):

```go
func (r *Reconciler) buildCreateRequest(offer vast.Offer, lifecycleID int64) vast.CreateRequest {
    cfg := r.deps.Cfg
    return vast.CreateRequest{
        ClientID:    "me",
        Image:       cfg.EmergencyTemplateImage,  // "ghcr.io/ggml-org/llama.cpp:server-cuda-b9128"
        Runtype:     "args",                       // D-06-B (NÃO "ssh", NÃO "ssh_proxy")
        Onstart:     emergencyOnstart,             // raw string const (acima)
        Args:        emergencyLlamaArgs,           // []string const ou config (acima)
        Env:         map[string]string{ /* ver acima */ },
        Disk:        40,                           // D-04-B disk budget reduction (era 80)
        Label:       fmt.Sprintf("ifix-emerg-lifecycle-%d", lifecycleID),
        TargetState: "running",
    }
}
```

---

### `gateway/internal/emerg/vast/types.go` — adicionar `Args []string`

**Analog:** mesmo arquivo, `CreateRequest` struct linhas 120-129.

**Current struct** (types.go:120-129):

```go
type CreateRequest struct {
	ClientID    string            `json:"client_id"` // always "me"
	Image       string            `json:"image"`
	Env         map[string]string `json:"env"`
	Onstart     string            `json:"onstart"`
	Runtype     string            `json:"runtype"` // "ssh"
	Disk        int               `json:"disk"`
	Label       string            `json:"label"`
	TargetState string            `json:"target_state,omitempty"` // "running" default
}
```

**Modification** — adicionar 1 campo + atualizar comentário do `Runtype`:

```go
type CreateRequest struct {
	ClientID    string            `json:"client_id"`
	Image       string            `json:"image"`
	Env         map[string]string `json:"env"`
	Onstart     string            `json:"onstart"`
	// Runtype values: "args" (Strategy B, preserves image ENTRYPOINT),
	// "ssh_proxy" (replaces ENTRYPOINT with vast-ai/base-image chain),
	// "ssh" (deprecated alias for ssh_proxy — Phase 6 root-cause STATE.md:85).
	Runtype     string            `json:"runtype"`
	// Args is the JSON `args` field (NOT image_args, NOT args_str — VERIFIED via
	// vast-cli/vast.py:2509 json_blob["args"] = args.args). Array of CLI tokens
	// passed REMAINDER-style to the image ENTRYPOINT when Runtype="args".
	// Empty omitempty so non-args runtypes (ssh_proxy) don't send the field.
	Args        []string          `json:"args,omitempty"`
	Disk        int               `json:"disk"`
	Label       string            `json:"label"`
	TargetState string            `json:"target_state,omitempty"`
}
```

**Comment header pattern** (types.go:1-28) — preservar convenções do package:
- Field-shape rationale em block comment no topo
- Cada campo tem `// VERIFIED via <fonte>` quando empírico
- Tag JSON sempre lowercase (Vast convention)

---

### `gateway/internal/emerg/vast/client.go` — verify marshalling (0 lines change)

**Analog:** mesmo arquivo, `CreateInstance` linhas 199-238.

**Existing code that already handles the new field** (client.go:199-203):

```go
func (c *Client) CreateInstance(ctx context.Context, offerID int64, body CreateRequest) (Instance, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return Instance{}, fmt.Errorf("vast: marshal create request: %w", err)
	}
	u := fmt.Sprintf("%s/asks/%d/", c.baseURL, offerID)
```

**Verdict:** `json.Marshal(body)` auto-serializa qualquer field exportado novo via JSON tag. **0 lines change em client.go.**

**Plan action:** apenas adicionar test assertion no payload JSON serializado (unit test em lifecycle_test.go ou client_test.go) confirmando `"args":[...]` aparece quando preenchido e omitido (omitempty) quando vazio.

---

### `gateway/internal/config/config.go` — add fields, remove `EmergencyPodImageTag`

**Analog:** mesmo arquivo, bloco Phase 6 linhas 119-149 (struct definition) + 240-265 (Load() defaults).

**Current struct fields to keep** (config.go:125-149) — apenas `EmergencyPodImageTag` (linha 125) é REMOVIDO. As linhas 126-149 (MonthlyEmergencyBudgetBRL, PrimaryHostID, etc.) ficam inalteradas.

**Current load block** (config.go:243):

```go
// Phase 6 — emergency pod (CONTEXT.md D-A1..D-D4). All defaults
// conservative. Operator confirms production values via
// 06-WAVE0-GATES.md before Phase 6 LIVE UAT (Plan 06-11).
EmergencyPodImageTag:              envOr("EMERGENCY_POD_IMAGE_TAG", "v1.0"),
MonthlyEmergencyBudgetBRL:         floatOr(os.Getenv("MONTHLY_EMERGENCY_BUDGET_BRL"), 200.0),
```

**Pattern to copy for new fields** — segue exato padrão `envOr` + comment style:

```go
// Phase 6 refactor (CONTEXT.md D-01-B..D-08-B Strategy B). Replaces
// EmergencyPodImageTag with public llama.cpp image + Vast args runtype.
EmergencyTemplateImage:        envOr("EMERGENCY_TEMPLATE_IMAGE", "ghcr.io/ggml-org/llama.cpp:server-cuda-b9128"),
EmergencyJinjaTemplateKey:     os.Getenv("EMERGENCY_JINJA_TEMPLATE_KEY"),     // empty se B1 (image overlay)
EmergencyJinjaTemplateSHA256:  os.Getenv("EMERGENCY_JINJA_TEMPLATE_SHA256"),  // empty se B1
// EmergencyLlamaArgs opcional — se vazio, lifecycle.go usa const default. CSV ou JSON.
EmergencyLlamaArgs:            csvOr(os.Getenv("EMERGENCY_LLAMA_ARGS"), nil),
```

**Struct doc pattern** (config.go:119-124, copy header style for new block):

```go
// Phase 6 refactor (D-01-B..D-08-B Strategy B) — public llama.cpp image
// + Vast runtype=args. EmergencyPodImageTag REMOVED; the 4 fields below
// replace it. Defaults match RESEARCH.md Pattern 1 and D-07-B args payload.
EmergencyTemplateImage        string   // EMERGENCY_TEMPLATE_IMAGE (default "ghcr.io/ggml-org/llama.cpp:server-cuda-b9128"; SHA-pinned tag)
EmergencyJinjaTemplateKey     string   // EMERGENCY_JINJA_TEMPLATE_KEY (MinIO object key; B2 only — empty disables MinIO-fetch)
EmergencyJinjaTemplateSHA256  string   // EMERGENCY_JINJA_TEMPLATE_SHA256 (hex; B2 only)
EmergencyLlamaArgs            []string // EMERGENCY_LLAMA_ARGS (CSV; empty = hardcoded const in lifecycle.go per D-07-B)
```

**csvOr helper já existe** (config.go:340-355) — reusar 1:1 para `EMERGENCY_LLAMA_ARGS`. Pattern já validado por `UpstreamOpenRouterProviderOrder` (config.go:202) + `AlertEmailTo` (config.go:279).

**Required-env check** (config.go:286-310) — NÃO adicionar `EMERGENCY_*` ao `requiredOrder`. Pattern segue Phase 6 graceful-degrade: `VastAIAPIKey` empty NÃO fail boot (config.go:122-124 comment + 134 doc).

---

### `gateway/internal/config/config_test.go` — atualizar Phase 6 tests

**Analog:** mesmo arquivo, `TestLoad_Phase6Defaults` (linhas 478-524) + `TestLoad_Phase6CustomValues` (linhas 529-579).

**phase6OptionalEnv list to update** (config_test.go:449-462):

```go
var phase6OptionalEnv = []string{
	"EMERGENCY_POD_IMAGE_TAG",  // REMOVE
	// ADD:
	// "EMERGENCY_TEMPLATE_IMAGE",
	// "EMERGENCY_JINJA_TEMPLATE_KEY",
	// "EMERGENCY_JINJA_TEMPLATE_SHA256",
	// "EMERGENCY_LLAMA_ARGS",
	"MONTHLY_EMERGENCY_BUDGET_BRL",
	// ... resto inalterado
}
```

**Assertion pattern to copy** (config_test.go:488-490 + 573-575):

```go
// Default test
if cfg.EmergencyPodImageTag != "v1.0" {                                                      // DELETE
	t.Errorf("EmergencyPodImageTag = %q, want v1.0", cfg.EmergencyPodImageTag)
}
// ADD:
if cfg.EmergencyTemplateImage != "ghcr.io/ggml-org/llama.cpp:server-cuda-b9128" {
	t.Errorf("EmergencyTemplateImage default = %q, want ghcr.io/ggml-org/llama.cpp:server-cuda-b9128", cfg.EmergencyTemplateImage)
}

// Custom-value test (mirror pattern at line 543):
t.Setenv("EMERGENCY_POD_IMAGE_TAG", "v1.1-rc2")    // DELETE
t.Setenv("EMERGENCY_TEMPLATE_IMAGE", "ghcr.io/ggml-org/llama.cpp:server-cuda-b9200")  // ADD
```

---

### `gateway/internal/emerg/lifecycle_test.go` — novo `TestBuildCreateRequest_*`

**Analog:** mesmo arquivo, `TestFilterBelowCap_Epsilon` (linhas 24-39) + `TestExcludeHost` (linhas 49-65) + `TestMustEventJSON` (linhas 69-88).

**Test imports pattern** (lifecycle_test.go:1-19):

```go
package emerg

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
)
```

**Test structure pattern** (lifecycle_test.go:120-135) — `&Reconciler{}` minimal init com `deps.Cfg`:

```go
func TestBuildCreateRequest_StrategyB_args(t *testing.T) {
	r := &Reconciler{
		deps: Deps{
			Cfg: config.Config{
				EmergencyTemplateImage: "ghcr.io/ggml-org/llama.cpp:server-cuda-b9128",
				MinioEndpoint:          "https://s3.example.com",
				MinioBucket:            "ai-gateway",
				MinioAccessKey:         "AKID",
				MinioSecretKey:         "SK",
				WeightsQwenKey:         "qwen/model.gguf",
				WeightsQwenSHA256:      "abc123",
			},
		},
	}
	req := r.buildCreateRequest(vast.Offer{ID: 42}, int64(7))

	require.Equal(t, "args", req.Runtype, "Strategy B uses runtype=args (D-06-B)")
	require.Equal(t, "ghcr.io/ggml-org/llama.cpp:server-cuda-b9128", req.Image)
	require.Contains(t, req.Args, "--jinja")
	require.Contains(t, req.Args, "/weights/qwen/model.gguf")
	require.Contains(t, req.Onstart, "WEIGHTS_QWEN_SHA256")
	require.Equal(t, "ifix-emerg-lifecycle-7", req.Label)
	require.NotContains(t, req.Env, "WEIGHTS_BGE_M3_KEY", "BGE_M3 not needed in LLM-only emergency pod")
}

// TestBuildCreateRequest_JSONShape — confirm json.Marshal emits `args` (not image_args)
// and omits when empty (Pitfall 5 + RESEARCH.md verify vast-cli source).
func TestBuildCreateRequest_JSONShape(t *testing.T) {
	r := &Reconciler{ /* same cfg */ }
	req := r.buildCreateRequest(vast.Offer{ID: 42}, int64(7))
	payload, err := json.Marshal(req)
	require.NoError(t, err)
	require.Contains(t, string(payload), `"args":[`)
	require.NotContains(t, string(payload), `"image_args"`)
	require.NotContains(t, string(payload), `"args_str"`)
}
```

**Edge-case + naming convention** copied from `TestFilterBelowCap_EmptyInput` (lifecycle_test.go:42-46):

```go
// TestBuildCreateRequest_DeterministicJSON — RESEARCH.md PRV-06 / test map line 654.
// Run 20x; payload bytes must be identical (no time.Now, no rand).
func TestBuildCreateRequest_DeterministicJSON(t *testing.T) {
	r := &Reconciler{ /* cfg */ }
	first, _ := json.Marshal(r.buildCreateRequest(vast.Offer{ID: 42}, int64(7)))
	for i := 0; i < 20; i++ {
		again, _ := json.Marshal(r.buildCreateRequest(vast.Offer{ID: 42}, int64(7)))
		require.Equal(t, first, again, "iteration %d diverges from first", i)
	}
}
```

---

### `gateway/.env.portainer.dev` — substituir env var

**Analog:** mesmo arquivo. Atual `EMERGENCY_POD_IMAGE_TAG` aparece em `TAG=latest-dev` (linha 18) que é a TAG da imagem do GATEWAY (não do pod). Buscar a entrada Phase 6 emergency-pod (não está listada nas linhas 1-82; tem que ler resto do arquivo se existir).

**Pattern format** (mesmo arquivo, linhas 30, 47, 80) — `KEY=VALUE` simples + comment blocks `# ===...`:

```bash
# ============================================================
# Phase 6 — emergency-pod (Vast.ai auto-prov)
# ============================================================
# Strategy B Locked (Phase 6 CONTEXT.md D-01-B): public llama.cpp image
# + runtype=args. EMERGENCY_POD_IMAGE_TAG REMOVED — substituído por:
EMERGENCY_TEMPLATE_IMAGE=ghcr.io/ggml-org/llama.cpp:server-cuda-b9128
EMERGENCY_JINJA_TEMPLATE_KEY=emerg-onstart/templates/qwen3.5-27b-tool-calling-<sha>.jinja
EMERGENCY_JINJA_TEMPLATE_SHA256=<hex>
# EMERGENCY_LLAMA_ARGS=  # vazio → usa const default em lifecycle.go
```

**Action note:** Plano deve adicionar grep verification que `EMERGENCY_POD_IMAGE_TAG` NÃO aparece mais no arquivo após edição (linha removida limpa).

---

### `gateway/internal/integration_test/emerg_leader_test.go` (e potencialmente outros `emerg_*_test.go`)

**Analog:** mesmo arquivo, `defaultTestCfg` linhas 33-50.

**Current line that needs update** (emerg_leader_test.go:47):

```go
cfg.EmergencyPodImageTag = "v1.0"   // DELETE
```

**Replacement pattern:**

```go
cfg.EmergencyTemplateImage = "ghcr.io/ggml-org/llama.cpp:server-cuda-b9128"
```

**Wave 0 survey command** (per RESEARCH.md Pitfall 7 + Wave 0 Gaps line 675):

```bash
# Confirmar abrangência ANTES de implementar
grep -rln "EmergencyPodImageTag\|ifix-ai-pod\|Runtype.*\"ssh\"$\|EMERGENCY_POD_IMAGE_TAG" \
  gateway/ --include='*.go'
```

Confirmed scope (grep do meu mapping):
- `gateway/internal/config/config.go` (deletion + replacement)
- `gateway/internal/config/config_test.go` (3 ocorrências)
- `gateway/internal/emerg/lifecycle.go` (650, 658, 687)
- `gateway/internal/emerg/vast/types.go` (125)
- `gateway/internal/emerg/errors.go` (linha 6 — comentário, baixa prioridade)
- `gateway/internal/integration_test/emerg_leader_test.go` (47)

**Não há** ocorrências em outros `emerg_*_test.go` integration tests (Wave 0 survey confirmed via `grep -rln`).

---

### `pod/Dockerfile`, `pod/scripts/emerg-bootstrap.sh`, `.github/workflows/build-pod.yml` — DELETE (PR2 separado, D-08-B)

**Action:** delete files. Não há analog porque é eliminação, não substituição.

**Pattern carried-over from emerg-bootstrap.sh** — a lógica de sha256-verify (linhas 52-58) e mc download (linhas 45-50) NÃO morre, vai para o `onstart` raw-string em `lifecycle.go:buildCreateRequest` (ver excerpt acima na seção lifecycle.go).

**Sequencing** (CONTEXT.md `<specifics>` line 163):
- **PR1:** Refactor (`lifecycle.go` + `types.go` + `config.go` + `config_test.go` + `lifecycle_test.go` + `emerg_leader_test.go` + `.env.portainer.dev`). NÃO toca `pod/Dockerfile`, `pod/scripts/emerg-bootstrap.sh`, `build-pod.yml`.
- **UAT live:** 3 lifecycles consecutivos GREEN.
- **PR2:** Delete-only (`pod/Dockerfile`, `pod/scripts/emerg-bootstrap.sh`, `.github/workflows/build-pod.yml`). Inclui também `EmergencyPodImageTag` field se ainda restou (já tirado em PR1).

---

### CONDICIONAL B1 vs B2 — Jinja template strategy

**B1 (custom overlay image):** Adiciona pasta nova `pod/qwen-templates/Dockerfile` + workflow `.github/workflows/build-qwen-templates.yml`. Analog: `pod/Dockerfile` + `.github/workflows/build-pod.yml` atuais (template imagem mínima ~10KB layer).

**B2 (MinIO upload + onstart fetch):** Adiciona script `pod/scripts/upload-jinja.sh` + workflow `.github/workflows/upload-jinja-template.yml`. Analog: `pod/scripts/upload-weights.sh` (já existe — listado em `pod/scripts/`). Pattern de upload MinIO já validado Phase 1.

**Plan-phase Wave 0 deve decidir.** CONTEXT.md D-04-B recomenda B2 default.

## Shared Patterns

### Pattern: Vast `Env` map — Docker `-p` port forwarding

**Source:** `gateway/internal/emerg/lifecycle.go:660-665` + comment de types.go:111-113.

**Apply to:** todo `buildCreateRequest` Vast (Phase 6 refactor mantém essa convenção).

```go
"-p 8000:8000": "1",   // chave LITERAL com flag Docker; valor "1" é arbitrário
```

### Pattern: sha256-verify-before-exec

**Source:** `pod/scripts/emerg-bootstrap.sh:52-58` + `pod/scripts/download-weights.sh:46-66`.

**Apply to:** novo onstart inline em `lifecycle.go`. Mandatory para qualquer artefato baixado em runtime (Phase 1 D-05, Phase 6 D-03-B).

```bash
ACTUAL=$(sha256sum "$WEIGHTS_PATH" | awk '{print $1}')
[[ "$ACTUAL" = "$WEIGHTS_QWEN_SHA256" ]] || { echo "sha256 mismatch"; exit 1; }
```

### Pattern: `envOr` / `atoiOr` / `floatOr` / `csvOr` config loading

**Source:** `gateway/internal/config/config.go:317-355`.

**Apply to:** todos novos fields Phase 6 refactor em `config.go:Load()`. Comportamento: env var vazia → default; valor bogus em `floatOr`/`atoiOr` → default (testado em `TestLoad_Phase6FloatOrBogusValue` config_test.go:585+).

```go
EmergencyTemplateImage: envOr("EMERGENCY_TEMPLATE_IMAGE", "ghcr.io/ggml-org/llama.cpp:server-cuda-b9128"),
EmergencyLlamaArgs:     csvOr(os.Getenv("EMERGENCY_LLAMA_ARGS"), nil),
```

### Pattern: Test struct usando `testify/require` + `t.Helper()`

**Source:** `gateway/internal/emerg/lifecycle_test.go:24-65` + `gateway/internal/config/config_test.go:478-524`.

**Apply to:** novos `TestBuildCreateRequest_*` + `TestEmergencyConfig_TemplateRefactor`.

```go
import "github.com/stretchr/testify/require"
require.Equal(t, expected, actual, "context message")
require.Contains(t, slice, element)
```

### Pattern: Sentry breadcrumb / structured logging (cross-cutting)

**Source:** `gateway/internal/emerg/fsm.go` (Sentry CaptureMessage + breadcrumb pattern, CONTEXT.md `<code_context>` line 137).

**Apply to:** Quando `buildCreateRequest` é chamado em provisionLifecycle, adicionar breadcrumb com `emerg_template_image=<image>` para facilitar debug futuro (CONTEXT.md `<code_context>` linha 137 sugere; plan decide se opcional).

### Pattern: Raw-string Go literals para bash bodies (Pitfall 9)

**Source:** RESEARCH.md:484-493.

**Apply to:** `onstart` const em `lifecycle.go`. Evita escape hazards de `fmt.Sprintf` + `$VAR` bash interpolation collision.

```go
const emergencyOnstart = `#!/bin/bash
...
$MINIO_ENDPOINT vem do Env map (Vast injeta), NÃO interpolado por Go.
`
```

## No Analog Found

Nenhum. Todos os arquivos têm analog próximo no mesmo módulo (emerg/, config/, integration_test/) ou no `pod/scripts/` (para a lógica MinIO/sha256 que migra de shell para Go raw-string).

## Metadata

**Analog search scope:**
- `gateway/internal/emerg/` (lifecycle.go, lifecycle_test.go, vast/types.go, vast/client.go, fsm.go)
- `gateway/internal/config/` (config.go, config_test.go)
- `gateway/internal/integration_test/emerg_*` (15 files)
- `pod/scripts/` (emerg-bootstrap.sh, download-weights.sh, upload-weights.sh, onstart.sh)
- `pod/Dockerfile`, `pod/templates/`
- `gateway/.env.portainer.dev` + `.env.portainer.example`
- `.github/workflows/build-pod.yml`

**Files scanned (read):** 11
**Files grep'd (no full read needed):** 7
**Pattern extraction date:** 2026-05-16

---

## PATTERN MAPPING COMPLETE

**Phase:** 6 - Emergency-Pod Template Refactor (Strategy B)
**Files classified:** 13
**Analogs found:** 13 / 13

### Coverage
- Files with exact analog: 11 (mesmo arquivo / mesmo módulo)
- Files with role-match analog: 2 (`.env.portainer.dev` env block, `lifecycle_test.go` novos testes seguem padrão dos existentes)
- Files with no analog: 0

### Key Patterns Identified
1. **Substituição in-place** do `buildCreateRequest` mantém estrutura (`vast.CreateRequest{}` literal + `Env` map convenção Docker `-p`) — apenas 4 campos mudam: Image, Runtype, Onstart, +Args.
2. **`csvOr`/`envOr`/`atoiOr`/`floatOr`** helpers já cobrem todos os novos Phase 6 env vars — 0 helpers novos.
3. **sha256-verify pattern** migra de shell (`emerg-bootstrap.sh:52-58`) para Go raw-string `onstart` inline em `lifecycle.go` — semântica idêntica, fonte muda.
4. **Test patterns** (`testify/require`, `&Reconciler{deps:Deps{Cfg:...}}`, deterministic JSON assertion) já estabelecidos em `lifecycle_test.go` + `config_test.go` — extensão direta.
5. **PR1/PR2 split** (CONTEXT.md `<specifics>` 163): refactor primeiro, delete depois. Burnt-bridge mitigation via 3-lifecycle UAT entre PRs.

### Ready for Planning
Pattern mapping complete. Planner pode agora referenciar `06-PATTERNS.md` em cada PLAN.md gerado (Wave 0 survey, Plan 06-01..06-NN actions).
