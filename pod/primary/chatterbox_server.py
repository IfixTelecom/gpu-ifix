"""pod/primary/chatterbox_server.py — Phase 06.7 Plan 05 (Wave 1).

GPU TTS service for the primary pod: a thin FastAPI shim wrapping
Chatterbox Multilingual (`ResembleAI/chatterbox`, MIT) and serving the OpenAI
`POST /v1/audio/speech` contract plus a `GET /health` lifecycle probe on
port 8003 (the 5th supervisord child).

ENGINE = Chatterbox Multilingual (Wave 0 GATE 1 selected this engine — see
06.7-WAVE0-GATES.md). Chatterbox clones ZERO-SHOT from a reference WAV passed
as `audio_prompt_path`; there is NO persisted speaker-embedding artifact. A
voice survives ephemeral pod replacement purely by refetching its reference WAV
from MinIO/S3 (lazy fetch on local-cache miss) — NO embedding regeneration.

ADAPT-VS-SHIM DECISION (D-05 / GATE 2): we write a thin FastAPI shim wrapping
`chatterbox-tts` directly rather than packaging `devnen/Chatterbox-TTS-Server`.
Rationale: (a) we need only the OpenAI `/v1/audio/speech` + `/health` + voice
resolution surface — the devnen server ships a Gradio UI, predefined-voice
management, and config layers we would have to trim and pin; (b) the shim is a
single auditable file with our exact contract (server-pinned `language_id=pt`,
env-driven paths/caps, zero-shot S3 WAV resolution, path-traversal guard) and
no surprise dependencies; (c) it stays importable + unit-testable WITHOUT a GPU
because the heavy imports (torch/chatterbox/torchaudio) are deferred into the
model-load path. The served contract is identical either way.

Config surface (env-driven, NOT hardcoded — REVIEWS action #6):
- CHATTERBOX_MODEL_CACHE_DIR  HF/torch model cache dir
- CHATTERBOX_VOICE_CACHE_DIR  local reference-WAV cache dir (optional latency cache)
- CHATTERBOX_S3_VOICE_PREFIX  S3 key prefix for reference WAVs (matches gateway Plan 07)
- CHATTERBOX_MAX_INPUT_CHARS  DoS cap on synth text length
- CHATTERBOX_LANGUAGE_ID       server-pinned language id (default "pt", D-06)
- MINIO_ENDPOINT/MINIO_ACCESS_KEY/MINIO_SECRET_KEY/MINIO_BUCKET  S3 creds (pod env)
"""

from __future__ import annotations

import io
import os
import re
import wave
from contextlib import asynccontextmanager
from pathlib import Path
from typing import Optional

from fastapi import FastAPI, HTTPException, Request
from fastapi.responses import JSONResponse, Response

# --- Config (env-driven; read once at import, NOT hardcoded) ---------------

CHATTERBOX_MODEL_CACHE_DIR = os.getenv(
    "CHATTERBOX_MODEL_CACHE_DIR", "/opt/chatterbox-data/models"
)
CHATTERBOX_VOICE_CACHE_DIR = os.getenv(
    "CHATTERBOX_VOICE_CACHE_DIR", "/opt/chatterbox-data/voices"
)
CHATTERBOX_S3_VOICE_PREFIX = os.getenv("CHATTERBOX_S3_VOICE_PREFIX", "voices")
CHATTERBOX_MAX_INPUT_CHARS = int(os.getenv("CHATTERBOX_MAX_INPUT_CHARS", "4000"))
CHATTERBOX_LANGUAGE_ID = os.getenv("CHATTERBOX_LANGUAGE_ID", "pt")

MINIO_ENDPOINT = os.getenv("MINIO_ENDPOINT", "")
MINIO_ACCESS_KEY = os.getenv("MINIO_ACCESS_KEY", "")
MINIO_SECRET_KEY = os.getenv("MINIO_SECRET_KEY", "")
MINIO_BUCKET = os.getenv("MINIO_BUCKET", "")

# Chatterbox native output sample rate (m.sr) — see GATE 2.
CHATTERBOX_SAMPLE_RATE = 24000

# voice_id allowlist — mirrors the Piper guard (Security V5/V12, T-06.7-08).
# S3 keys + local cache filenames are derived from this allowlist, never from
# raw free-text input.
_VOICE_ID_RE = re.compile(r"^[A-Za-z0-9_\-\.]+$")

SUPPORTED_FORMATS = {"wav", "pcm"}

# Module-level model handle + readiness flag. Tests inject a fake model here;
# in production it is set by the lifespan startup hook once Chatterbox is loaded
# on CUDA. `/health` returns 200 only when `model_ready` is True.
_model = None
model_ready = False


# --- Voice resolution (zero-shot; lazy S3 WAV fetch — NO embedding) ---------


def _validate_voice_id(voice_id: str) -> None:
    """Reject path-traversal / non-allowlisted voice ids before they are used
    to build an S3 key or local cache path (T-06.7-08, Security V5/V12)."""
    if not voice_id or ".." in voice_id or not _VOICE_ID_RE.match(voice_id):
        raise HTTPException(status_code=400, detail="invalid voice_id")


def _s3_get_object(key: str) -> Optional[bytes]:
    """Fetch an object from MinIO/S3. Returns the raw bytes, or None if the
    object does not exist. Imports boto3 lazily so the module imports without
    the S3 client present (tests monkeypatch this function directly).

    NOTE: there is NO speaker-embedding fetch here — the object is the
    reference WAV itself, passed straight to Chatterbox as `audio_prompt_path`.
    """
    import boto3  # lazy: not needed at import / in unit tests
    from botocore.exceptions import ClientError

    client = boto3.client(
        "s3",
        endpoint_url=MINIO_ENDPOINT,
        aws_access_key_id=MINIO_ACCESS_KEY,
        aws_secret_access_key=MINIO_SECRET_KEY,
    )
    try:
        resp = client.get_object(Bucket=MINIO_BUCKET, Key=key)
        return resp["Body"].read()
    except ClientError as exc:
        code = exc.response.get("Error", {}).get("Code", "")
        if code in ("NoSuchKey", "404", "NoSuchBucket"):
            return None
        raise


def resolve_voice_wav(voice: Optional[str]) -> Optional[str]:
    """Resolve an OpenAI `voice`/`voice_id` to a local reference-WAV path to be
    passed as Chatterbox `audio_prompt_path`.

    - voice in (None, "", "random") -> None (default voice, audio_prompt_path=None)
    - local cache hit -> use the cached WAV (no S3 round-trip)
    - cache miss -> fetch <S3_VOICE_PREFIX>/<voice_id>.wav from MinIO, cache it
      locally, then use it (pod-replacement survival; NO embedding, NO regen)
    - no S3 object for that voice_id -> 404 (unknown voice)
    """
    if voice is None or voice == "" or voice == "random":
        return None

    _validate_voice_id(voice)

    cache_dir = Path(CHATTERBOX_VOICE_CACHE_DIR)
    local_path = cache_dir / f"{voice}.wav"
    if local_path.exists():
        return str(local_path)

    # Cache miss: lazy fetch the reference WAV from S3 (zero-shot survival path).
    s3_key = f"{CHATTERBOX_S3_VOICE_PREFIX}/{voice}.wav"
    wav_bytes = _s3_get_object(s3_key)
    if wav_bytes is None:
        raise HTTPException(status_code=404, detail=f"unknown voice: {voice}")

    cache_dir.mkdir(parents=True, exist_ok=True)
    local_path.write_bytes(wav_bytes)
    return str(local_path)


# --- Synthesis + encoding ---------------------------------------------------


def _tensor_to_pcm16(wav) -> bytes:
    """Convert a Chatterbox output tensor (float [-1,1], shape [1, N] or [N])
    to 16-bit little-endian PCM bytes. Imports numpy lazily so the encode path
    is exercisable in tests with a plain list/array stand-in."""
    try:
        arr = wav.cpu().numpy()  # torch.Tensor
    except AttributeError:
        import numpy as _np

        arr = _np.asarray(wav)
    import numpy as np

    arr = np.asarray(arr, dtype=np.float32).reshape(-1)
    arr = np.clip(arr, -1.0, 1.0)
    pcm = (arr * 32767.0).astype("<i2")
    return pcm.tobytes()


def encode_wav(pcm16: bytes, sample_rate: int = CHATTERBOX_SAMPLE_RATE) -> bytes:
    """Wrap raw PCM16 mono bytes in a RIFF/WAV container at `sample_rate`."""
    buf = io.BytesIO()
    with wave.open(buf, "wb") as wf:
        wf.setnchannels(1)
        wf.setsampwidth(2)  # 16-bit
        wf.setframerate(sample_rate)
        wf.writeframes(pcm16)
    return buf.getvalue()


def synthesize(text: str, audio_prompt_path: Optional[str]) -> bytes:
    """Run Chatterbox inference and return raw PCM16 mono bytes at 24000 Hz.
    `language_id` is pinned server-side (D-06). `audio_prompt_path=None` = the
    default voice; a path = zero-shot clone from that reference WAV."""
    if _model is None:
        raise HTTPException(status_code=503, detail="model not loaded")
    wav = _model.generate(
        text,
        language_id=CHATTERBOX_LANGUAGE_ID,
        audio_prompt_path=audio_prompt_path,
        exaggeration=0.5,
        cfg_weight=0.5,
        temperature=0.8,
        repetition_penalty=2.0,
    )
    return _tensor_to_pcm16(wav)


# --- Model load + warmup (lifespan) -----------------------------------------


def load_model():
    """Load Chatterbox Multilingual on CUDA. Heavy imports are deferred here so
    the module imports under a GPU-less test environment."""
    os.environ.setdefault("HF_HOME", CHATTERBOX_MODEL_CACHE_DIR)
    os.environ.setdefault("TORCH_HOME", CHATTERBOX_MODEL_CACHE_DIR)
    from chatterbox.mtl_tts import ChatterboxMultilingualTTS

    return ChatterboxMultilingualTTS.from_pretrained("cuda")


@asynccontextmanager
async def lifespan(app: FastAPI):
    global _model, model_ready
    _model = load_model()
    # Warmup: one short synth so the first real request is not cold (load ~30-40s).
    try:
        synthesize("Olá.", audio_prompt_path=None)
    except Exception:
        # Warmup failure must not wedge readiness — the model object loaded.
        pass
    model_ready = True
    yield
    _model = None
    model_ready = False


app = FastAPI(title="chatterbox-tts-shim", lifespan=lifespan)


@app.get("/health")
async def health():
    """Lifecycle probe (port 8003). 200 only when the model is loaded; 503 while
    still loading at startup (REVIEWS action #4)."""
    if model_ready and _model is not None:
        return JSONResponse({"status": "ok", "model_ready": True})
    return JSONResponse({"status": "loading", "model_ready": False}, status_code=503)


@app.post("/v1/audio/speech")
async def audio_speech(request: Request):
    """OpenAI /v1/audio/speech: {model, input, voice, response_format} ->
    binary WAV/PCM at 24000 Hz mono (Chatterbox, language_id=pt)."""
    body = await request.json()

    text = body.get("input")
    if not isinstance(text, str) or text == "":
        raise HTTPException(status_code=400, detail="input is required")
    if len(text) > CHATTERBOX_MAX_INPUT_CHARS:
        raise HTTPException(
            status_code=400,
            detail=f"input exceeds {CHATTERBOX_MAX_INPUT_CHARS} chars",
        )

    response_format = body.get("response_format", "wav")
    if response_format not in SUPPORTED_FORMATS:
        raise HTTPException(
            status_code=400,
            detail=f"unsupported response_format: {response_format}",
        )

    # `voice` (OpenAI) doubles as our voice_id. resolve_voice_wav validates it
    # and returns a reference-WAV path (zero-shot) or None (default voice).
    voice = body.get("voice") or body.get("voice_id")
    audio_prompt_path = resolve_voice_wav(voice)

    pcm16 = synthesize(text, audio_prompt_path)

    if response_format == "pcm":
        return Response(content=pcm16, media_type="audio/pcm")
    return Response(content=encode_wav(pcm16), media_type="audio/wav")


if __name__ == "__main__":  # pragma: no cover
    import uvicorn

    uvicorn.run(app, host="0.0.0.0", port=8003)
