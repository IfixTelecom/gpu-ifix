# 06-01-SUMMARY.md — Plan 06-01 (Wave 0: SPIKE + Gates)

**Plan:** 06-01 (Wave 0)
**Status:** GREEN — completed 2026-05-16
**Type:** human-action (autonomous: false) — driven by Claude session em ops-claude com operator (Pedro) approval
**Cost:** ~$0.04 Vast.ai spend (2 spike pods × ~3min each @ $0.32/h)

---

## Output Artifacts

| Path | Purpose |
|------|---------|
| `.planning/phases/06-emergency-pod-template-refactor/06-SPIKE-runtype-args.md` | Empirical evidence Strategy B viability (4 evidências capturadas) |
| `.planning/phases/06-emergency-pod-template-refactor/06-WAVE0-GATES.md` | Operator decisions (Jinja B2 + Disk 40GB + MinIO key/sha256 + revised args pattern) |
| MinIO `s3://ai-gateway/emerg-onstart/templates/qwen3.5-27b-tool-calling-1067302cc6d927210a84775b9a060f724da15debc168c79710cbf763512e9f67.jinja` | Jinja template uploaded + sha256-verified |

---

## must_haves Truths Validation

| # | Truth (06-01-PLAN.md) | Status |
|---|------------------------|--------|
| 1 | Spike confirma `b9128 + Runtype=args + Args=[--host --port --version]` funciona | ✅ via entrypoint override pattern (raw `--args` slice falhou — Open Question 4 RESOLVIDA) |
| 2 | Spike confirma llama-server eh PID 1 (crash detection clean) | ✅ inferred via exec replace pattern em script + version output direto |
| 3 | Spike confirma se onstart roda dentro do container | ✅ YES (via `--entrypoint /bin/bash --args -c '...'`); NO via `--onstart-cmd` literal |
| 4 | Operator decidiu Jinja B1 ou B2 | ✅ **B2 MinIO fetch + Disk 40GB** |
| 5 | (B2) Jinja uploaded MinIO + sha256 confirmed | ✅ `mc cat | sha256sum` == `1067302...512e9f67` |
| 6 | Grep survey confirma scope (6 arquivos predicted) | ✅ exatamente 6 (zero surprises) |

---

## Key Findings (BREAK from CONTEXT.md verbatim)

**🚨 Strategy B payload requires `--entrypoint /bin/bash --args -c "<script>"` — NOT raw 15-token args slice.**

CONTEXT.md D-07-B `Args=["--host","0.0.0.0",...]` literal interpretation falha porque ENTRYPOINT do image `ghcr.io/ggml-org/llama.cpp:server-cuda-b9128` é `llama-server` direto. Args mode preserva ENTRYPOINT salvo override.

**Plan 06-04 (Wave 2) consequence:** `buildCreateRequest` deve emitir:
```go
vast.CreateRequest{
    Image:      cfg.EmergencyTemplateImage,
    Disk:       40,
    Runtype:    "args",
    Entrypoint: "/bin/bash",
    Args:       []string{"-c", emergencyOnstartScript},
    Env:        emergencyEnvMap,
}
```

`emergencyOnstartScript` é raw-string Go (backtick) que termina em `exec /app/llama-server --host 0.0.0.0 --port 8000 -m /weights/qwen/model.gguf -ngl 99 -np 2 --ctx-size 16384 --jinja --chat-template-file /app/templates/qwen3.5-27b-tool-calling.jinja`.

**vast.CreateRequest struct (plan 06-03) consequence:** Precisa ALSO incluir `Entrypoint string \`json:"entrypoint,omitempty"\`` ALÉM de `Args []string \`json:"args,omitempty"\``. Adicionar pra Wave 1 task em 06-03.

---

## Downstream Plan Updates Required

| Plan | Update needed | Severity |
|------|----------------|----------|
| 06-03 | Add `Entrypoint string \`json:"entrypoint,omitempty"\`` to CreateRequest DTO (alem do `Args []string` ja planned) | minor — extra field |
| 06-04 | Substitute `Args=[15-token slice]` por `Entrypoint="/bin/bash" + Args=["-c", onstartScript]`; tests asserts mudam | medium — pattern shift, but artifacts/truths still valid intent |
| 06-WAVE0-GATES.md | (já docs revised pattern) | — |

---

## Cost Breakdown (Spike + Upload)

| Item | Cost |
|------|------|
| Vast pod #1 (Round 1 failed onstart-cmd, ~30s) | <$0.01 |
| Vast pod #2 (Round 2 entrypoint override, ~3min) | ~$0.03 |
| MinIO upload (8.4KB) | $0.00 (Ifix-owned bucket) |
| **Total Phase 6 spike** | **~$0.04** (under $0.10 budget) |

Vast.ai balance remaining: $7.11 (was $7.15). 06-06 HUMAN-UAT live (3 lifecycles ~$3) ainda dentro de saldo.

---

## Unblocked

✅ Wave 1 (06-02 + 06-03) pode iniciar paralelo.
✅ Plans 06-02..06-06 têm escopo determinístico (Jinja key + sha256 + entrypoint pattern).
✅ Burnt-bridge mitigation (D-08-B-risk) atendida — empirical confidence pra começar PR1.

---

## Sign-off

Pedro (operator): aprova continuação para Wave 1.
Claude (driver): docs commits via 06-01 wave commit junto com Wave 0 artifacts.
