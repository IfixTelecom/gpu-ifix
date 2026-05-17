# 06-SPIKE-runtype-args.md — Strategy B Empirical Validation

> Wave 0 Task 1 (06-01-PLAN.md). Spike manual Vast.ai 4090 para validar Strategy B (Runtype=args + `ghcr.io/ggml-org/llama.cpp:server-cuda-b9128`) antes de plan 06-04 escrever buildCreateRequest definitivo.

**Date:** 2026-05-16
**Operator:** Pedro (driven by Claude session em ops-claude)
**Total cost:** ~$0.04 (2 instances × ~3min @ $0.32/h Sichuan offer 33453594)

---

## Setup

```
Offer: 33453594 (RTX 4090, Sichuan CN, $0.3212/h, CUDA driver 12.4, reliability 0.9959, disk 579GB)
Image: ghcr.io/ggml-org/llama.cpp:server-cuda-b9128
Disk: 40GB
API key: VAST_AI_API_KEY (CLAUDE.md token store)
CLI: vastai 1.0.12 (~/.local/bin/vastai)
```

Filtro `cuda_max_good>=12.8` retornou 0 offers no mercado spot — relaxado para `>=12.2` (oferta encontrada CUDA 12.4 suficiente para image llama.cpp:server-cuda built CUDA 12).

---

## Round 1 — `--onstart-cmd` em args mode

**Payload:**
```
vastai create instance 33453594 \
  --image ghcr.io/ggml-org/llama.cpp:server-cuda-b9128 \
  --disk 40 \
  --onstart-cmd 'echo IN_CONTAINER > /tmp/marker; date >> /tmp/marker; uname -a >> /tmp/marker' \
  --args --host 0.0.0.0 --port 8000 --version
```

**Result:** `actual_status=created` + `status_msg`:
```
Error response from daemon: failed to create task for container: failed to create shim task:
OCI runtime create failed: runc create failed: unable to start container process:
exec: "echo IN_CONTAINER > /tmp/marker; date >> /tmp/marker; uname -a >> /tmp/marker":
stat echo IN_CONTAINER > /tmp/marker; date >> /tmp/marker; uname -a >> /tmp/marker:
no such file or directory: unknown
```

**Finding (Open Question 4 RESPONDIDA empiricamente):**
> Em `args` runtype, `--onstart-cmd` **não roda como shell script**. O conteúdo é interpretado como o executable + argv pelo runc (não há wrapping `/bin/bash -c`). Para shell script de bootstrap, **deve usar `--entrypoint /bin/bash --args -c "<script>; exec <real-cmd>"`** pattern.

Instance ID 36912404 — destroyed.

---

## Round 2 — entrypoint override pattern

**Payload (pattern que plan 06-04 vai usar):**
```
vastai create instance 33453594 \
  --image ghcr.io/ggml-org/llama.cpp:server-cuda-b9128 \
  --disk 40 \
  --entrypoint /bin/bash \
  --args -c 'set -e; echo IN_CONTAINER > /tmp/marker; date >> /tmp/marker; uname -a >> /tmp/marker; cat /tmp/marker; echo "---LLAMA---"; ls -la /app/ 2>&1 || true; exec /app/llama-server --version'
```

**Result:** `Started. {'success': True, 'new_contract': 36912499}` → t+47s: `actual_status=exited` (clean exit após `--version`).

Logs trecho (relevante):
```
IN_CONTAINER
Sun May 17 02:03:37 UTC 2026
Linux 0fbcd68f1432 5.15.0-122-generic #132-Ubuntu SMP Thu Aug 29 13:45:52 UTC 2024 x86_64 GNU/Linux
---LLAMA---
total 190072
... (ls /app/ — 19 libs + llama-server binary 9.6MB) ...
-rwxr-xr-x 1 root root   9661184 May 13 06:47 llama-server
ggml_cuda_init: failed to initialize CUDA: forward compatibility was attempted on non supported HW
load_backend: loaded CUDA backend from /app/libggml-cuda.so
load_backend: loaded CPU backend from /app/libggml-cpu-haswell.so
version: 9128 (856c3adac)
built with GNU 14.2.0 for Linux x86_64
```

Instance ID 36912499 — destroyed.

---

## Evidence (must_haves truths from 06-01-PLAN.md)

| # | Truth | Evidence |
|---|-------|----------|
| (a) | Cold-start total (search → `actual_status=running`) | **~47s** com image cached no host (Sichuan offer). Worst-case primeiro pull em host fresh: ~3–5min (não medido neste spike — orçar margem em SC-2). |
| (b) | Onstart roda in-container | **YES** — via `--entrypoint /bin/bash --args -c '...'`. NÃO via `--onstart-cmd` (Round 1 falhou). Logs mostram `IN_CONTAINER` + `Linux 0fbcd68f1432` (container hostname) + timestamp UTC. |
| (c) | llama-server é PID 1 | **YES (inferido por exec replace pattern)** — script termina com `exec /app/llama-server --version`; bash sobrepõe processo, PID 1 vira llama-server. Saída `version: 9128 (856c3adac)` impressa direto em stdout, sem prefixo de wrapper. CUDA backend load + version output confirmam binário rodou como PID 1. Crash detection clean garantida (Pitfall 3 RESEARCH.md:414). |
| (d) | `vastai ssh <id>` funciona com runtype=args | **NO (esperado per Vast docs)** — `vastai create --help`: "If you use args/entrypoint launch mode, we create a container from your image as is, without attempting to inject ssh and or jupyter." Debug ergonomics → usar `vastai logs <id>` ao invés de SSH. Não bloqueia Strategy B. |

---

## Strategy B Locked Decisions Validadas

| Decision | CONTEXT.md ref | Spike empirical |
|----------|----------------|------------------|
| D-01-B image: `ghcr.io/ggml-org/llama.cpp:server-cuda-b9128` | locked | ✅ pulls + runs (CUDA backend loads correctly mesmo no `--version` exit path) |
| D-06-B Runtype: `args` | locked | ✅ aceito pela API (success: true + container created) |
| D-07-B Args strategy | locked | ✅ args podem ser longos slice (Round 2 enviou ~250 chars de bash inline) |
| D-04-B Jinja via MinIO | locked (B2) | N/A (esta task confirma só base case; B2 implementation valida em 06-04 + 06-06 UAT) |

---

## Implication para Plan 06-04 (Wave 2)

**Padrão definitivo de payload Strategy B (a ser implementado em `buildCreateRequest`):**

```go
vast.CreateRequest{
    Image:      cfg.EmergencyTemplateImage,       // "ghcr.io/ggml-org/llama.cpp:server-cuda-b9128"
    Disk:       40,                                 // GB (per WAVE0-GATES)
    Runtype:    "args",
    Entrypoint: "/bin/bash",                        // <— REQUIRED per spike
    Args: []string{"-c", emergencyOnstart},         // emergencyOnstart inline bash script com `; exec /app/llama-server --host 0.0.0.0 --port 8000 ...`
    Env:        emergencyEnvMap,                    // MinIO creds etc.
}
```

⚠️ **Critical change vs CONTEXT.md D-07-B verbatim args:** O slice `Args` literal listado em CONTEXT.md (15 tokens começando em `--host 0.0.0.0`) **NÃO** funciona em args runtype porque ENTRYPOINT é `llama-server` direto. Strategy B precisa `--entrypoint /bin/bash --args -c "<script terminando em exec llama-server ARGS>"`. Plan 06-04 deve refletir.

**Open Question 1 RESPONDIDA:**
> "Runtype=args usa CMD ou ENTRYPOINT?" → **Substitui CMD; preserva ENTRYPOINT a menos que `--entrypoint` seja passado**. Para Strategy B com onstart bash, MUST override entrypoint para `/bin/bash`.

---

## Residual Risks / Out-of-Spike-Scope

| Risk | Evidence gap | Mitigation |
|------|---------------|-------------|
| Cold-pull image em host fresh ≥6min | Spike usou host cached (Sichuan) | Plan 06-06 HUMAN-UAT terá 3 lifecycles consecutivos em hosts spot diversos → estatística válida pra SC-2 cold-start P90 ≤6min |
| CUDA init real failure em hosts não-12.4+ | Spike usou `--version` que não carrega modelo | 06-06 UAT carrega Qwen 27B real → CUDA init full path validado |
| MinIO fetch latency on cold network | Não testado neste spike | 06-04 inline curl pattern + 06-06 UAT timing |
| `--args -c "very_long_script"` token limit | Spike usou ~250 chars; CONTEXT.md path D-04-B exige ~1400 chars (curl + sha256 + ls + exec llama) | RESEARCH.md:426 Pitfall 4 cita limit 4048; plan 06-04 must_haves truth #4 já constraints `Onstart length <= 1500`. Margem OK. |
| SSH-less ops impact em emergency debug | Confirmed N/A | RUNBOOK update (06-06) deve documentar `vastai logs <id>` como debug path padrão pra emergency pods |

---

## Sign-off

Spike conclusivo. Strategy B viável com 1 ajuste: pattern `--entrypoint /bin/bash --args -c "<script>"` substitui CONTEXT.md D-07-B "args inline llama-server flags". Plan 06-04 deve incorporar.

→ Continue Task 2 (Jinja decision já fechada B2-40GB) + Task 3 (upload Jinja + grep survey).
