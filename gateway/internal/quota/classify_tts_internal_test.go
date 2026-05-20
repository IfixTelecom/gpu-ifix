// Internal-package test for classifyRoute (unexported). Phase 06.7 (D-12) —
// /v1/audio/speech must classify to RouteClassTTS, not the RouteClassChat
// default fallback. Companion to the external TestClassifyRoute_TTS in
// enforcer_test.go (which locks the RouteClassTTS wire value).
package quota

import "testing"

func TestClassifyRoute_TTS_Internal(t *testing.T) {
	if got := classifyRoute("/v1/audio/speech"); got != RouteClassTTS {
		t.Errorf("classifyRoute(/v1/audio/speech) = %q, want %q", got, RouteClassTTS)
	}
	// Sibling routes keep their own classes (no regression).
	if got := classifyRoute("/v1/audio/transcriptions"); got != RouteClassSTT {
		t.Errorf("classifyRoute(/v1/audio/transcriptions) = %q, want %q", got, RouteClassSTT)
	}
	// Unknown path still defaults to chat.
	if got := classifyRoute("/v1/unknown"); got != RouteClassChat {
		t.Errorf("classifyRoute(/v1/unknown) = %q, want %q (default)", got, RouteClassChat)
	}
}
