"""pod/primary/test_chatterbox_shim.py — Phase 06.7 Plan 05 (Wave 1, GREEN).

Owned by Plan 06.7-05 (was Wave 0 RED scaffolding in Plan 02). These tests now
assert the real chatterbox_server shim behavior, mocking the model + MinIO so
they run WITHOUT a GPU or live S3.

ENGINE (authority: 06.7-WAVE0-GATES.md GATE 1/2, 06.7-CONTEXT.md D-05/D-06/D-08):
- Chatterbox Multilingual (`ResembleAI/chatterbox`, MIT). Package `chatterbox-tts==0.1.7`.
- API: m.generate(text, language_id="pt", audio_prompt_path=<ref_wav_or_None>, ...)
  -> tensor at m.sr == 24000 (NOT Kani's 22050).
- ZERO-SHOT clone: the reference WAV is passed as `audio_prompt_path`. There is
  NO persisted `.pt` speaker embedding. A voice survives pod replacement purely
  by refetching its reference WAV from MinIO/S3.

Run target: `pytest pod/primary/test_chatterbox_shim.py`
"""

from __future__ import annotations

import wave
from io import BytesIO

import pytest
from fastapi.testclient import TestClient

import chatterbox_server as srv


class _FakeTensor:
    """Stand-in for a Chatterbox output tensor: .cpu().numpy() -> list/array."""

    def __init__(self, samples):
        self._samples = samples

    def cpu(self):
        return self

    def numpy(self):
        return self._samples


class _FakeModel:
    """Records the args m.generate was called with so tests can assert that the
    shim passed `audio_prompt_path` (zero-shot) and `language_id="pt"`."""

    sr = 24000

    def __init__(self):
        self.calls = []

    def generate(self, text, language_id=None, audio_prompt_path=None, **kw):
        self.calls.append(
            {
                "text": text,
                "language_id": language_id,
                "audio_prompt_path": audio_prompt_path,
                "kw": kw,
            }
        )
        # ~0.1s of silence at 24kHz; values in [-1, 1].
        return _FakeTensor([0.0] * 2400)


@pytest.fixture
def model(monkeypatch):
    m = _FakeModel()
    monkeypatch.setattr(srv, "_model", m)
    monkeypatch.setattr(srv, "model_ready", True)
    return m


@pytest.fixture
def client():
    # TestClient(...) without entering the context manager skips lifespan, so
    # the real load_model() (which needs a GPU) never runs. The `model` fixture
    # injects the fake model directly.
    return TestClient(srv.app)


def test_speech_default_voice_returns_wav_24000(model, client, monkeypatch, tmp_path):
    """POST /v1/audio/speech with the default voice (voice="random" ->
    audio_prompt_path=None) returns a WAV whose sample rate is 24000 Hz
    (Chatterbox m.sr — NOT Kani's 22050)."""
    monkeypatch.setattr(srv, "CHATTERBOX_VOICE_CACHE_DIR", str(tmp_path))
    resp = client.post(
        "/v1/audio/speech",
        json={"model": "tts", "input": "Olá mundo.", "voice": "random", "response_format": "wav"},
    )
    assert resp.status_code == 200
    assert resp.headers["content-type"] == "audio/wav"
    with wave.open(BytesIO(resp.content), "rb") as wf:
        assert wf.getframerate() == 24000
        assert wf.getnchannels() == 1
        assert wf.getsampwidth() == 2
    # default voice => audio_prompt_path None, language pinned to pt
    assert model.calls[0]["audio_prompt_path"] is None
    assert model.calls[0]["language_id"] == "pt"


def test_voice_clone_roundtrip_upload_then_speak(model, client, monkeypatch, tmp_path):
    """Zero-shot voice-clone round-trip: a reference WAV already cached locally
    for voice_id `mariana` is passed straight as `audio_prompt_path` — NO `.pt`
    embedding is generated or persisted (Chatterbox zero-shot, D-08)."""
    monkeypatch.setattr(srv, "CHATTERBOX_VOICE_CACHE_DIR", str(tmp_path))
    ref = tmp_path / "mariana.wav"
    ref.write_bytes(b"RIFF....WAVEfake")

    # If S3 were touched the test would fail — assert no round-trip on cache hit.
    def _boom(key):  # pragma: no cover - must not be called
        raise AssertionError("S3 must not be hit on a local cache hit")

    monkeypatch.setattr(srv, "_s3_get_object", _boom)

    resp = client.post(
        "/v1/audio/speech",
        json={"model": "tts", "input": "Boleto vence amanhã.", "voice": "mariana"},
    )
    assert resp.status_code == 200
    # The cached reference WAV path was passed straight as audio_prompt_path.
    assert model.calls[0]["audio_prompt_path"] == str(ref)
    # No `.pt` embedding artifact was created anywhere in the cache dir.
    assert list(tmp_path.glob("*.pt")) == []


def test_voice_id_regex_rejects_path_traversal(model, client, monkeypatch, tmp_path):
    """A voice_id with path-traversal chars is rejected (400) before it is ever
    used to build an S3 key or local cache path (V12 mitigation)."""
    monkeypatch.setattr(srv, "CHATTERBOX_VOICE_CACHE_DIR", str(tmp_path))

    def _boom(key):  # pragma: no cover - must not be called
        raise AssertionError("S3 must not be hit for a rejected voice_id")

    monkeypatch.setattr(srv, "_s3_get_object", _boom)

    for bad in ["../etc/passwd", "..", "a/b", "foo bar", "x;rm"]:
        resp = client.post(
            "/v1/audio/speech",
            json={"model": "tts", "input": "hi", "voice": bad},
        )
        assert resp.status_code == 400, f"{bad!r} should be rejected"
    assert model.calls == []  # synth never reached


def test_health_returns_200_when_model_loaded(client, monkeypatch):
    """GET /health returns 200 only once the model is loaded (the gateway probe
    target on port 8003); 503 while still loading (REVIEWS action #4)."""
    # Not loaded yet -> 503.
    monkeypatch.setattr(srv, "_model", None)
    monkeypatch.setattr(srv, "model_ready", False)
    assert client.get("/health").status_code == 503

    # Loaded -> 200.
    monkeypatch.setattr(srv, "_model", _FakeModel())
    monkeypatch.setattr(srv, "model_ready", True)
    r = client.get("/health")
    assert r.status_code == 200
    assert r.json()["model_ready"] is True


def test_voice_cache_miss_refetches_from_s3(model, client, monkeypatch, tmp_path):
    """Pod-replacement survival (zero-shot — NO `.pt`): synth for a known
    voice_id whose reference WAV is NOT in the local cache (fresh pod). The shim
    fetches the reference WAV from MinIO/S3, caches it locally, and passes it as
    `audio_prompt_path`. Asserts an S3 GET fired for the reference WAV (.wav,
    NOT .pt) and that synth used the refetched WAV."""
    monkeypatch.setattr(srv, "CHATTERBOX_VOICE_CACHE_DIR", str(tmp_path))
    monkeypatch.setattr(srv, "CHATTERBOX_S3_VOICE_PREFIX", "voices")

    fetched = {}

    def _fake_s3(key):
        fetched["key"] = key
        return b"RIFF....WAVEfetched-from-s3"

    monkeypatch.setattr(srv, "_s3_get_object", _fake_s3)

    resp = client.post(
        "/v1/audio/speech",
        json={"model": "tts", "input": "Pod novo, voz sobrevive.", "voice": "joana"},
    )
    assert resp.status_code == 200
    # S3 GET fired for the reference WAV (zero-shot survival) — and it is a .wav,
    # never a .pt embedding.
    assert fetched["key"] == "voices/joana.wav"
    assert fetched["key"].endswith(".wav")
    assert not fetched["key"].endswith(".pt")
    # It was cached locally and passed as audio_prompt_path.
    cached = tmp_path / "joana.wav"
    assert cached.exists()
    assert model.calls[0]["audio_prompt_path"] == str(cached)


def test_unknown_voice_returns_404(model, client, monkeypatch, tmp_path):
    """A voice_id with no S3 object -> 404 (unknown voice)."""
    monkeypatch.setattr(srv, "CHATTERBOX_VOICE_CACHE_DIR", str(tmp_path))
    monkeypatch.setattr(srv, "_s3_get_object", lambda key: None)
    resp = client.post(
        "/v1/audio/speech",
        json={"model": "tts", "input": "hi", "voice": "ghost"},
    )
    assert resp.status_code == 404


def test_input_over_cap_rejected(model, client, monkeypatch, tmp_path):
    """input longer than CHATTERBOX_MAX_INPUT_CHARS -> 400 (DoS cap)."""
    monkeypatch.setattr(srv, "CHATTERBOX_MAX_INPUT_CHARS", 10)
    resp = client.post(
        "/v1/audio/speech",
        json={"model": "tts", "input": "x" * 11, "voice": "random"},
    )
    assert resp.status_code == 400


def test_unsupported_format_rejected(model, client):
    """response_format not in {wav,pcm} -> 400."""
    resp = client.post(
        "/v1/audio/speech",
        json={"model": "tts", "input": "hi", "voice": "random", "response_format": "mp3"},
    )
    assert resp.status_code == 400


def test_pcm_format_returns_raw_pcm(model, client):
    """response_format=pcm -> raw PCM bytes (no RIFF header)."""
    resp = client.post(
        "/v1/audio/speech",
        json={"model": "tts", "input": "hi", "voice": "random", "response_format": "pcm"},
    )
    assert resp.status_code == 200
    assert resp.headers["content-type"] == "audio/pcm"
    assert not resp.content.startswith(b"RIFF")
