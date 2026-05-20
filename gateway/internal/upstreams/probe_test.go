// Package upstreams (probe_test.go): Phase 06.7 Wave 0 RED scaffolding
// (Nyquist gate). Skip stub binding the `tts` probe behavior to its owning
// implementation plan. These assertions are ENGINE-AGNOSTIC: they cover the
// `tts` ROLE plumbing inside Probe.dispatch (probe path + success/failure
// classification) regardless of whether the TTS server on :8003 is
// Chatterbox Multilingual (the Wave 0 GATE 1 engine swap from Kani) or any
// other OpenAI-compatible /v1/audio/speech server.
//
// OWNER map (authority: 06.7-02-PLAN.md <stub_ownership_map>):
//   - TestProbe_TTS_PostsAudioSpeech -> Plan 06.7-03
package upstreams

import "testing"

// TestProbe_TTS_PostsAudioSpeech asserts that Probe.dispatch, when given an
// UpstreamConfig with Role=="tts", POSTs to <URL>/v1/audio/speech with a
// tiny synthetic speech body (e.g. {"model":"chatterbox","input":"ping",
// "voice":"default"}), treats a 200 response carrying audio bytes as
// breaker SUCCESS, and treats a 5xx as a *breaker.HTTPError failure (mirror
// the existing "embed"/"llm" case assertions in probe.go dispatch switch).
//
// OWNER: Plan 06.7-03 — the plan that adds the `case "tts"` arm to
// Probe.dispatch MUST unskip this and make it assert the real path + status
// handling before that plan is COMPLETE.
func TestProbe_TTS_PostsAudioSpeech(t *testing.T) {
	t.Skip("OWNER Plan 06.7-03 — implement Probe.dispatch case \"tts\" -> POST /v1/audio/speech; assert 200=success, 5xx=breaker failure")
}
