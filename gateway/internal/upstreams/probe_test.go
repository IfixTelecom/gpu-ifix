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

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
)

// newTTSProbe builds a Probe wired to an in-memory loader + a breaker for the
// given upstream name. Only Probe.dispatch (which uses p.client) is exercised
// by the TTS probe test, so q==nil (no Postgres writeback).
func newTTSProbe(name string, cfgs ...UpstreamConfig) *Probe {
	l := NewLoaderInMemory(cfgs...)
	bs := breaker.NewSet(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), breaker.Options{}, []string{name})
	return NewProbe(l, bs, nil, ProbeConfig{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// TestProbe_TTS_PostsAudioSpeech asserts that Probe.dispatch, when given an
// UpstreamConfig with Role=="tts", POSTs to <URL>/v1/audio/speech with a
// synthetic JSON speech body, treats a 200 response carrying audio bytes as
// breaker SUCCESS, and treats a 5xx as a *breaker.HTTPError failure (mirror
// the existing "embed"/"llm" case assertions in probe.go dispatch switch).
//
// OWNER: Plan 06.7-03 — unskipped + asserting real path + status handling.
func TestProbe_TTS_PostsAudioSpeech(t *testing.T) {
	// --- 200 + audio bytes -> success (no error) ---
	t.Run("200_success", func(t *testing.T) {
		var gotPath, gotMethod, gotCT string
		var gotBody map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotMethod = r.Method
			gotCT = r.Header.Get("Content-Type")
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &gotBody)
			w.Header().Set("Content-Type", "audio/pcm")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte{0x00, 0x01, 0x02, 0x03}) // synthetic audio bytes
		}))
		defer srv.Close()

		u := UpstreamConfig{Name: "primary-tts", Role: "tts", Tier: 0, URL: srv.URL, Enabled: true}
		p := newTTSProbe(u.Name, u)

		resp, err := p.dispatch(context.Background(), u)
		if err != nil {
			t.Fatalf("dispatch(tts) returned error on 200: %v", err)
		}
		if resp == nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 response, got %+v", resp)
		}
		if gotMethod != http.MethodPost {
			t.Errorf("method = %q, want POST", gotMethod)
		}
		if gotPath != "/v1/audio/speech" {
			t.Errorf("path = %q, want /v1/audio/speech", gotPath)
		}
		if gotCT != "application/json" {
			t.Errorf("content-type = %q, want application/json", gotCT)
		}
		if gotBody["input"] != "ping" {
			t.Errorf("body.input = %v, want ping", gotBody["input"])
		}
		if gotBody["response_format"] != "pcm" {
			t.Errorf("body.response_format = %v, want pcm", gotBody["response_format"])
		}
	})

	// --- 5xx -> *breaker.HTTPError failure ---
	t.Run("5xx_failure", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		}))
		defer srv.Close()

		u := UpstreamConfig{Name: "primary-tts", Role: "tts", Tier: 0, URL: srv.URL, Enabled: true}
		p := newTTSProbe(u.Name, u)

		_, err := p.dispatch(context.Background(), u)
		if err == nil {
			t.Fatalf("dispatch(tts) returned nil error on 502, want *breaker.HTTPError")
		}
		var he *breaker.HTTPError
		if !errors.As(err, &he) {
			t.Fatalf("error type = %T, want *breaker.HTTPError", err)
		}
		if he.Status != http.StatusBadGateway {
			t.Errorf("HTTPError.Status = %d, want 502", he.Status)
		}
	})
}
