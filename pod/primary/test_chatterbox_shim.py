"""pod/primary/test_chatterbox_shim.py — Phase 06.7 Wave 0 RED scaffolding.

Nyquist gate: skip stubs binding the Chatterbox TTS shim behaviors added in
Wave 1 (Plan 06.7-05) to their owning implementation plan, so no shim work
ships without an automated verify target.

ENGINE (authority: 06.7-WAVE0-GATES.md GATE 1/2, 06.7-CONTEXT.md D-05/D-06/D-08):
- Chatterbox Multilingual (`ResembleAI/chatterbox`, MIT) replaced Kani at the
  Wave 0 GATE 1 swap. Package `chatterbox-tts==0.1.7`.
- API: `from chatterbox.mtl_tts import ChatterboxMultilingualTTS`;
  `m = ChatterboxMultilingualTTS.from_pretrained("cuda")`;
  `wav = m.generate(text, language_id="pt", audio_prompt_path=<ref_wav_or_None>, ...)`;
  tensor at `m.sr == 24000` (NOT Kani's 22050); save via
  `torchaudio.save(path, wav.cpu(), 24000)`.
- ZERO-SHOT clone: the reference WAV is passed as `audio_prompt_path`. There
  is NO persisted `.pt` speaker embedding. A voice survives pod replacement
  purely by refetching its reference WAV from MinIO/S3.
- OpenAI-compat server `devnen/Chatterbox-TTS-Server` exposes
  POST /v1/audio/speech + GET /health on port 8003.

OWNER map (authority: 06.7-02-PLAN.md <stub_ownership_map>) — all OWNER 05.
Run target: `pytest pod/primary/test_chatterbox_shim.py`
"""

from __future__ import annotations

import pytest


@pytest.mark.skip(reason="OWNER Plan 06.7-05: implement chatterbox_shim")
def test_speech_default_voice_returns_wav_24000():
    """POST /v1/audio/speech with the default voice (audio_prompt_path=None)
    returns a WAV whose sample rate is 24000 Hz (Chatterbox m.sr — NOT Kani's
    22050). Asserts the shim saves at 24000 and reports it in the WAV header.

    OWNER: Plan 06.7-05.
    """


@pytest.mark.skip(reason="OWNER Plan 06.7-05: implement chatterbox_shim")
def test_voice_clone_roundtrip_upload_then_speak():
    """Zero-shot voice-clone round-trip: upload a reference WAV, then synth
    with its voice_id. Asserts the shim passes the reference WAV directly as
    `audio_prompt_path` to m.generate — NO `.pt` embedding is generated or
    persisted (Chatterbox zero-shot, D-08).

    OWNER: Plan 06.7-05.
    """


@pytest.mark.skip(reason="OWNER Plan 06.7-05: implement chatterbox_shim")
def test_voice_id_regex_rejects_path_traversal():
    """A voice_id containing path-traversal characters (e.g. "../", absolute
    paths, NUL) is rejected by the shim's voice_id regex before it is ever
    used to build an S3 key or a local cache path (V12 mitigation).

    OWNER: Plan 06.7-05.
    """


@pytest.mark.skip(reason="OWNER Plan 06.7-05: implement chatterbox_shim")
def test_health_returns_200_when_model_loaded():
    """GET /health returns HTTP 200 once the Chatterbox model is loaded on
    CUDA — this is the gateway probe target on port 8003 (REVIEWS action #4).

    OWNER: Plan 06.7-05.
    """


@pytest.mark.skip(reason="OWNER Plan 06.7-05: implement chatterbox_shim")
def test_voice_cache_miss_refetches_from_s3():
    """Pod-replacement survival (zero-shot — NO `.pt`): synth requested for a
    known voice_id whose reference WAV is NOT in the local cache (fresh pod).
    The shim fetches the reference WAV from MinIO/S3, optionally caches it
    locally, and passes it directly as `audio_prompt_path` to m.generate.
    Asserts an S3 GET fires for the reference WAV and NO embedding regen /
    NO `.pt` is involved (REVIEWS action #1).

    OWNER: Plan 06.7-05.
    """
