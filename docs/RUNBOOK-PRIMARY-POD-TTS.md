# Primary Pod Runbook — Phase 06.7 (TTS on GPU + Embed 24/7 on CPU)

**Phase:** 06.7 — primary-pod TTS swap (Chatterbox in, embed out) + embed moved to 24/7 CPU
**Engine:** Chatterbox Multilingual (`ResembleAI/chatterbox`, Resemble AI, **MIT**) — Wave 0 GATE 1 swap from Kani-TTS-2-pt (which FAILED PT-BR quality). See `06.7-WAVE0-GATES.md`.
**Extends:** `gateway/docs/RUNBOOK-PRIMARY-POD.md` (Phase 6.6 — supervisord PID-1, 4-service co-location, schedule reconciler, FSM, `gatewayctl primary` CLI). This runbook documents ONLY the 06.7 deltas; for pod lifecycle / FSM / schedule / image-bump policy, read the 6.6 runbook first.

---

## Overview

Phase 6.6 ran four supervisord children on the peak-only primary pod: `llama` (LLM), `speaches` (STT), `infinity` (embed), `dcgm` (metrics). Phase 06.7 changes the model topology:

- **Embed LEAVES the pod.** Infinity (`intfloat/multilingual-e5-large`, 1024-dim) now runs **24/7 on CPU** at `n8n-ia-vm` (10.10.10.20:7997), wired as a **static** tier-0 upstream (`UPSTREAM_EMBED_URL`). RAG survives off-peak because embed no longer depends on the GPU pod being awake.
- **TTS JOINS the pod.** Chatterbox Multilingual runs as a 5th supervisord child (`[program:chatterbox]`, port **8003**), serving the OpenAI `POST /v1/audio/speech` contract with **zero-shot voice cloning**. TTS is a **dynamic** tier-0 role (the pod IP changes per lifecycle — needs the reconciler's `OverrideTier0`).
- **Piper stays as TTS tier-1 fallback.** When the pod is asleep/draining (or Chatterbox tier-0 is OPEN), the gateway falls back to voice-api Piper via a pure-Go mu-law→WAV-16kHz adapter (GATE 3 Option A). Piper does NOT clone → degraded default voice (not an error).

Net tier-0 dynamic-override roster moved from `{llm,stt,embed}` → **`{llm,stt,tts}`**; embed is now static.

```
                       PEAK (pod awake)                    OFF-PEAK (pod asleep)
  /v1/chat/completions  → pod llama :8000               → OpenRouter fallback
  /v1/audio/transcriptions → pod speaches :8001         → OpenAI Whisper fallback
  /v1/audio/speech      → pod chatterbox :8003 (clone)  → voice-api Piper (default voice, WAV 16kHz)
  /v1/embeddings        → n8n-ia-vm Infinity :7997 (24/7, ALWAYS this — static)
```

---

## Architecture delta (vs Phase 6.6)

### Supervisord children on the pod

| Child | Port | Phase 6.6 | Phase 06.7 |
|-------|------|-----------|------------|
| `llama` (LLM) | 8000 | ✅ | ✅ unchanged |
| `speaches` (STT) | 8001 | ✅ | ✅ unchanged |
| `infinity` (embed) | 8002 | ✅ on pod | ❌ **removed from supervisord** (venv kept in image for rollback only) |
| `dcgm` (metrics) | 9400 | ✅ | ✅ unchanged |
| `chatterbox` (TTS) | **8003** | — | ✅ **NEW** — 5th child, `/opt/chatterbox-venv` (isolated torch 2.6 cu124), `directory=/opt/chatterbox-data`, autorestart, priority 250 |

The Chatterbox child is a thin FastAPI shim (`pod/primary/chatterbox_server.py`) wrapping `chatterbox-tts==0.1.7`. It serves `POST /v1/audio/speech` + `GET /health` (200 only when the model is loaded; 503 while loading + warming at startup, ~30–40s cold). `language_id` is pinned server-side to `pt`. Native output is **24kHz 16-bit mono WAV** (`response_format=wav` default; `pcm` = raw PCM16LE).

### Off-pod embed (24/7 CPU)

- Host: `n8n-ia-vm` (VMID 101, 10.10.10.20). Deploy source: `deploy/embed/docker-compose.yml` + `deploy/embed/README.md`.
- Model: `intfloat/multilingual-e5-large` (1024-dim), Infinity `0.0.77`, `--engine torch --dtype float32` (the host CPU is AVX2-only Intel i7-8700 — the ONNX int8 AVX512-VNNI variant SIGILLs; torch fp32 bypasses it), `--url-prefix /v1` so it answers `/v1/embeddings` directly. Healthcheck hits `/health` (UNPREFIXED — Infinity serves `/health` even with the `/v1` prefix). `INFINITY_MODEL_WARMUP=false` + `cpus: 3.0` cap to protect the shared n8n host.
- Gateway wiring: `UPSTREAM_EMBED_URL=http://10.10.10.20:7997`. Embed is resolved as a **static** tier-0 upstream (D-03); it is **removed from the reconciler `OverrideTier0` map**. No FSM involvement.
- Perf: ~19.5 emb/s (batch 32) fp32; single query ~100–200ms. Fine for query-time RAG. Heavy corpus re-embed (Phase 8) should use the GPU opportunistically.

### Piper tier-1 fallback (GATE 3 Option A adapter)

- Tier-0 (Chatterbox on pod) → tier-1 **voice-api Piper** (`voice-api-piper-1` on vps-ifix-vm, model `pt_BR-miro-high.onnx`). `UPSTREAM_TTS_PIPER_URL` must point at the live Piper server; if unset the fallback is dropped and a tier-0-OPEN yields 503.
- Adapter (in the gateway, pure-Go — **no ffmpeg in the image**): translates `/v1/audio/speech {input, voice}` → Piper `POST /tts {text, voice}`; decodes Piper's raw **mu-law (ulaw) 8kHz** (`audio/basic`) via a 256-entry LUT and re-wraps as **WAV 16kHz 16-bit PCM mono** (`audio/wav`). `response_format=pcm` → raw PCM16LE (`audio/pcm`). `mp3`/`opus`/`flac`/`aac` → clean OpenAI-shaped 400.
- Piper does NOT voice-clone → degraded **default voice** in fallback windows. This is expected, not an error.

---

## Voice cloning — zero-shot, S3-WAV-durable (NO `.pt`)

**Contract (D-08 revised — Chatterbox zero-shot, see `06.7-05-SUMMARY.md`):**

```
voice_id ─┬─► Postgres ai_gateway.voices row (id, tenant_id, label, s3_key, created_at)
          └─► MinIO S3 object  <S3_VOICE_PREFIX>/<voice_id>.wav   (the reference WAV — the SINGLE durable artifact)
```

There is **NO persisted `.pt` speaker embedding** anywhere. Chatterbox clones zero-shot from the reference WAV passed as `audio_prompt_path` at synth time.

### Voice CRUD (`/v1/audio/voices`, gateway-side, tenant-isolated)

| Method | Route | Behavior |
|--------|-------|----------|
| `POST` | `/v1/audio/voices` | multipart upload (reference WAV + `label`). Generates a UUID `voice_id`, uploads to MinIO at `<S3_VOICE_PREFIX>/<voice_id>.wav`, writes a catalog row scoped to the authenticated tenant, returns `{voice_id, label}`. Upload size capped by `VOICE_MAX_UPLOAD_BYTES`. Idempotent upsert on `(tenant, label)`. |
| `GET` | `/v1/audio/voices` | lists the authenticated tenant's voices only (multi-tenant isolation, D-10). |
| `DELETE` | `/v1/audio/voices/{id}` | deletes S3 object then row. On S3-delete failure: 502, keep row (retryable). |

S3 keys are derived from the UUID `voice_id`, never from user input (path-traversal safe). The pod shim additionally enforces a `voice_id` allowlist regex `^[A-Za-z0-9_\-\.]+$` + explicit `..` rejection.

### Speech with a cloned voice (`/v1/audio/speech`)

`POST /v1/audio/speech {model, input, voice, response_format}`:
- `voice` ∈ `{None,"","random"}` → `audio_prompt_path=None` → Chatterbox default voice.
- `voice` = a `voice_id` → the pod resolves: **local cache hit** → use cached WAV; **cache miss** → lazily fetch `<S3_VOICE_PREFIX>/<voice_id>.wav` from MinIO, cache it, pass as `audio_prompt_path`; **no S3 object** → 404.
- `input` capped by `TTS_MAX_INPUT_CHARS` (gateway) / `CHATTERBOX_MAX_INPUT_CHARS` (pod).

### Why voices survive pod replacement (the key durability property)

Pod storage is **ephemeral** — a destroy + re-provision wipes the local voice cache. The voice still works because the durable artifact is the **S3 reference WAV**, not anything on the pod. After replacement, the first `/v1/audio/speech` with that `voice_id` is a cache MISS → the shim refetches the WAV from MinIO → passes it as `audio_prompt_path`. **No embedding regen, no `.pt` rebuild** — just an S3 GET. This is exactly what UAT scenario S3 proves live.

---

## Pitfall #11 re-assert (TTS now in scope)

Phase 6.6 tech debt #9: an emergency-pod cutback's `RestoreTier0` clears the tier-0 slot the primary wrote, and the primary reconciler did not auto-restore it. Adding `tts` to the dynamic-override map means the bug now spans **all three** dynamic roles (`llm`/`stt`/`tts`).

**Fix (Plan 08, commit 53f0e2f):** the primary reconciler's `evaluateReady` (the 1Hz Ready-tick) loops `{llm,stt,tts}` and, for any role whose `Tier0OverrideURL(role)` returns `set=false`, re-calls `OverrideTier0(role, <podURL>)` plus a `Warn` log. `embed` is excluded (static now). The loop runs even under `PRIMARY_POD_SCHEDULE_DISABLED`.

**Operator-visible behavior:** force an emerg cutback while primary is Ready → the tts slot is re-asserted on the next Ready-tick (≤~1s).

---

## Environment Variables (06.7 additions)

These are in addition to the 24 `PRIMARY_*` + 6 shared fields documented in the 6.6 runbook.

### Gateway (`ai-gateway-dev` on vps-ifix-vm)

| Var | Purpose |
|-----|---------|
| `UPSTREAM_TTS_URL` | tier-0 TTS placeholder. Empty at boot (the reconciler writes the live pod URL on Ready via `OverrideTier0("tts",...)`). When unset, the proxy falls back to a dead-localhost placeholder (`http://127.0.0.1:1`) so boot does not crash; the breaker holds tier-0 OPEN until the reconciler overrides. |
| `UPSTREAM_TTS_PIPER_URL` | tier-1 Piper fallback — point at the live voice-api Piper server. Unset → fallback dropped → tier-0-OPEN yields 503. |
| `UPSTREAM_EMBED_URL` | static embed tier-0 — `http://10.10.10.20:7997` (n8n-ia-vm Infinity, 24/7). |
| `TTS_MAX_INPUT_CHARS` | cap on `/v1/audio/speech` `input` length. |
| `VOICE_MAX_UPLOAD_BYTES` | cap on `/v1/audio/voices` reference-WAV upload size. |
| `S3_VOICE_PREFIX` | MinIO key prefix for voice WAVs — **MUST equal** the pod's `CHATTERBOX_S3_VOICE_PREFIX` so the pod finds the WAV the gateway uploaded. |

### Pod (`CHATTERBOX_*`, baked into supervisord env line)

| Var | Purpose |
|-----|---------|
| `CHATTERBOX_MODEL_CACHE_DIR` | HF/torch model cache (`/opt/chatterbox-data/models`). Weights are NOT baked into the image — downloaded from HF on first load. |
| `CHATTERBOX_VOICE_CACHE_DIR` | local reference-WAV cache (cache-miss → S3 refetch). |
| `CHATTERBOX_S3_VOICE_PREFIX` | MinIO key prefix — MUST equal the gateway `S3_VOICE_PREFIX`. |
| `CHATTERBOX_MAX_INPUT_CHARS` | pod-side input cap. |
| `CHATTERBOX_LANGUAGE_ID` | pinned `pt`. |
| `MINIO_ENDPOINT` / `MINIO_ACCESS_KEY` / `MINIO_SECRET_KEY` / `MINIO_BUCKET` | MinIO creds for the reference-WAV fetch (bucket `ai-gateway`). |

---

## Operator procedures

### Bring the pod up / down (unchanged from 6.6)

```bash
docker exec ai-gateway-dev_gateway /gatewayctl primary force-up --reason "tts_uat"
# wait Ready — Chatterbox cold load adds ~30-40s on top of llama/speaches loads
docker exec ai-gateway-dev_gateway /gatewayctl primary state
docker exec ai-gateway-dev_gateway /gatewayctl primary force-down
```

### Verify the TTS slot is wired (peak)

```bash
# gateway log should show tier-0 override for tts after Ready:
ssh vps-ifix-vm 'docker logs ai-gateway-dev_gateway --tail 200 | grep -i "tier-0 override.*tts\|OverrideTier0.*tts"'
```

### Synthesize with a cloned voice

```bash
# 1. upload a reference WAV -> voice_id
curl -sS -H "Authorization: Bearer $KEY" \
  -F "file=@reference.wav" -F "label=operator-voice" \
  https://ai-gateway-dev.ifixtelecom.com.br/v1/audio/voices
# -> {"voice_id":"<uuid>","label":"operator-voice"}

# 2. synth in that voice (24kHz WAV)
curl -sS -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"model":"chatterbox","input":"Olá, seu boleto venceu ontem.","voice":"<uuid>","response_format":"wav"}' \
  https://ai-gateway-dev.ifixtelecom.com.br/v1/audio/speech -o cloned.wav
```

### Embed (always available, 24/7)

```bash
curl -sS -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"model":"multilingual-e5-large","input":["ifix telecom"]}' \
  https://ai-gateway-dev.ifixtelecom.com.br/v1/embeddings | jq '.data[0].embedding | length'
# -> 1024  (works even when the GPU pod is asleep)
```

### Off-peak Piper fallback

With the pod drained/asleep, `/v1/audio/speech` is served by Piper → WAV 16kHz 16-bit mono, default voice (no clone). Verify with `file cloned.wav` → `RIFF (little-endian) data, WAVE audio ... 16000 Hz`.

---

## Cross-references

- `gateway/docs/RUNBOOK-PRIMARY-POD.md` — Phase 6.6 base (FSM, schedule, image-bump, `gatewayctl primary`).
- `06.7-WAVE0-GATES.md` — engine swap (GATE 1), Chatterbox VRAM/CUDA (GATE 2), Piper Option A (GATE 3), embed host+model (GATE 4).
- `06.7-CONTEXT.md` — D-03/D-05/D-06/D-07/D-08/D-11/D-13 decisions.
- `06.7-05-SUMMARY.md` — pod Chatterbox shim + zero-shot S3-WAV contract.
- `06.7-06-SUMMARY.md` — 24/7 CPU embed deploy (AVX2 fp32 fixes).
- `06.7-07-SUMMARY.md` — gateway `/v1/audio/speech` + `/v1/audio/voices` + Piper adapter.
- `06.7-08-SUMMARY.md` — reconciler embed→tts swap + Pitfall #11 Ready-tick re-assert.
- `06.7-HUMAN-UAT.md` — the live 5090 UAT scenario sheet (S1–S6 + cleanup).
