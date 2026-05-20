// Package proxy (tts_test.go): Phase 06.7 Wave 0 RED scaffolding (Nyquist
// gate). Skip stubs binding the TTS proxy (JSON->binary audio) + the GATE 3
// Piper tier-1 fallback adapter to their owning implementation plan.
//
// ENGINE: primary TTS = Chatterbox Multilingual (Wave 0 GATE 1 swap from
// Kani — see 06.7-WAVE0-GATES.md), serving OpenAI-compatible
// POST /v1/audio/speech and emitting 24kHz WAV. The proxy layer itself is
// engine-agnostic (it forwards JSON in -> binary audio out 1:1); the
// fallback adapter converts Piper ulaw 8kHz -> WAV 16kHz (GATE 3 Option A).
//
// OWNER map (authority: 06.7-02-PLAN.md <stub_ownership_map>):
//   - TestTTSProxy_JSONToBinaryAudio          -> Plan 06.7-07
//   - TestTTSProxy_ErrorEnvelope              -> Plan 06.7-07
//   - TestTTSProxy_PiperFallback_AdapterConverts -> Plan 06.7-07
package proxy

import "testing"

// TestTTSProxy_JSONToBinaryAudio asserts that NewTTSProxy forwards a JSON
// POST /v1/audio/speech body to the upstream Chatterbox server 1:1 (same
// path, same method), preserves the upstream Content-Type (e.g. audio/wav)
// and the binary audio bytes on the response, and STRIPS the client
// Authorization header via BuildDirector (same auth-strip contract as
// NewAudioProxy). Fake upstream returns audio bytes for the POST.
//
// OWNER: Plan 06.7-07 — implement NewTTSProxy + unskip + assert path/CT/body
// preservation + Authorization strip before COMPLETE.
func TestTTSProxy_JSONToBinaryAudio(t *testing.T) {
	t.Skip("OWNER Plan 06.7-07 — implement NewTTSProxy; assert /v1/audio/speech 1:1 forward, Content-Type + binary body preserved, client Authorization stripped")
}

// TestTTSProxy_ErrorEnvelope asserts that when the upstream TTS server is
// unreachable (dial error / refused), the proxy ErrorHandler returns an
// OpenAI-shaped error envelope with HTTP 502 ({"error":{...}}), not a bare
// Go transport error or a 500.
//
// OWNER: Plan 06.7-07 — wire ErrorHandler("tts", ...) + unskip + assert
// 502 OpenAI envelope before COMPLETE.
func TestTTSProxy_ErrorEnvelope(t *testing.T) {
	t.Skip("OWNER Plan 06.7-07 — TTS upstream unreachable -> OpenAI-shaped 502 envelope; assert status + error body shape")
}

// TestTTSProxy_PiperFallback_AdapterConverts asserts the GATE 3 Option A
// gateway adapter: when the tier-0 (Chatterbox) breaker is OPEN, the
// dispatcher routes the request to tier-1 Piper. The adapter translates the
// OpenAI JSON {input, voice} into Piper's form POST /tts {text, voice}, then
// converts Piper's raw mu-law 8kHz (Content-Type audio/basic) response into
// WAV 16kHz 16-bit PCM mono via the pure-Go mu-law LUT + RIFF writer (NO
// ffmpeg), setting Content-Type audio/wav on the converted response.
// response_format=pcm yields audio/pcm; unsupported formats -> clean 400.
//
// OWNER: Plan 06.7-07 — implement dispatcher tier-0->tier-1 TTS fallback +
// the ulaw->WAV adapter, unskip, and assert tier-1 Piper receives the
// translated request + the converted WAV 16kHz response before COMPLETE.
func TestTTSProxy_PiperFallback_AdapterConverts(t *testing.T) {
	t.Skip("OWNER Plan 06.7-07 — implement NewTTSProxy / dispatcher Piper fallback adapter; assert ulaw 8kHz -> WAV 16kHz, JSON->form translation, audio/wav Content-Type")
}
