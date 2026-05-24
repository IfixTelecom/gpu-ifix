package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/models"
)

// buildMultipartBody constructs a multipart/form-data body with the given
// model field value + a file part containing the supplied bytes. Returns
// the body bytes + the Content-Type header (with the writer's boundary).
//
// If modelValues has more than one element, a separate "model" form-field
// part is written for each (used to construct duplicate-model test fixtures).
// If modelValues is empty, no model form field is written.
func buildMultipartBody(t *testing.T, modelValues []string, fileName string, fileBytes []byte) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, mv := range modelValues {
		fw, err := w.CreateFormField("model")
		if err != nil {
			t.Fatalf("CreateFormField(model): %v", err)
		}
		if _, err := fw.Write([]byte(mv)); err != nil {
			t.Fatalf("write model: %v", err)
		}
	}
	if fileName != "" {
		// Write the file part with binary-safe headers so audio bytes pass through.
		hdr := make(textproto.MIMEHeader)
		hdr.Set("Content-Disposition", `form-data; name="file"; filename="`+fileName+`"`)
		hdr.Set("Content-Type", "audio/wav")
		fw, err := w.CreatePart(hdr)
		if err != nil {
			t.Fatalf("CreatePart(file): %v", err)
		}
		if _, err := fw.Write(fileBytes); err != nil {
			t.Fatalf("write file: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("writer.Close: %v", err)
	}
	return buf.Bytes(), w.FormDataContentType()
}

// parseMultipartFromBytes parses a forwarded multipart body and returns
// (modelFieldValue, fileBytes) for assertion purposes. Returns error
// if multipart parse fails.
func parseMultipartFromBytes(body []byte, contentType string) (modelField string, fileBytes []byte, err error) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", nil, err
	}
	if !strings.HasPrefix(mediaType, "multipart/") {
		return "", nil, http.ErrNotMultipart
	}
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for {
		part, perr := mr.NextPart()
		if perr == io.EOF {
			break
		}
		if perr != nil {
			return "", nil, perr
		}
		buf, _ := io.ReadAll(part)
		switch part.FormName() {
		case "model":
			modelField = string(buf)
		case "file":
			fileBytes = buf
		}
	}
	return modelField, fileBytes, nil
}

// loadProbeWAV reads the canonical audio fixture used across phase tests.
func loadProbeWAV(t *testing.T) []byte {
	t.Helper()
	// The probe.wav fixture lives at gateway/internal/upstreams/testdata.
	// From gateway/internal/proxy/ the relative path is ../upstreams/testdata/probe.wav.
	b, err := os.ReadFile("../upstreams/testdata/probe.wav")
	if err != nil {
		t.Fatalf("read probe.wav: %v", err)
	}
	return b
}

// TestOpenAIWhisperDirector_InjectsAuthBearer confirms the Authorization
// header is set on the outgoing request.
func TestOpenAIWhisperDirector_InjectsAuthBearer(t *testing.T) {
	srv, _, _ := captureUpstream(t)
	upstream, _ := url.Parse(srv.URL)
	resolver := models.NewResolverForTesting(nil)
	director := BuildOpenAIWhisperDirector(upstream, "sk-openai-abc", resolver, "openai-whisper", discardLogger())

	body, ct := buildMultipartBody(t, []string{"whisper"}, "a.wav", []byte("RIFFAUDIO"))
	req, _ := applyDirector(t, director, http.MethodPost, "/v1/audio/transcriptions", ct, body, nil, nil)
	if got := req.Header.Get("Authorization"); got != "Bearer sk-openai-abc" {
		t.Errorf("Authorization = %q, want Bearer sk-openai-abc", got)
	}
}

// Test 1 (R6 base): TestOpenAIWhisperDirector_RewritesModelInMultipart —
// resolver maps "whisper" → "whisper-1" on the openai-whisper upstream.
// Forwarded multipart body has the "model" form field rewritten AND audio
// bytes preserved.
func TestOpenAIWhisperDirector_RewritesModelInMultipart(t *testing.T) {
	t.Setenv("UPSTREAM_STT_OPENAI_MODEL", "")
	srv, _, _ := captureUpstream(t)
	upstream, _ := url.Parse(srv.URL)
	resolver := models.NewResolverForTesting(map[[2]string]string{
		{"whisper", "openai-whisper"}: "whisper-1",
	})
	director := BuildOpenAIWhisperDirector(upstream, "sk-openai-test", resolver, "openai-whisper", discardLogger())

	wav := loadProbeWAV(t)
	body, ct := buildMultipartBody(t, []string{"whisper"}, "probe.wav", wav)
	req, forwarded := applyDirector(t, director, http.MethodPost, "/v1/audio/transcriptions", ct, body, nil, nil)

	gotModel, gotFile, perr := parseMultipartFromBytes(forwarded, req.Header.Get("Content-Type"))
	if perr != nil {
		t.Fatalf("forwarded body parse: %v", perr)
	}
	if gotModel != "whisper-1" {
		t.Errorf("model = %q, want whisper-1", gotModel)
	}
	if !bytes.Equal(gotFile, wav) {
		t.Errorf("audio bytes mutated: got %d bytes, want %d bytes (byte-identical required)",
			len(gotFile), len(wav))
	}
}

// Test 2 (R6 base): TestOpenAIWhisperDirector_AudioBytesUnchangedByteIdentical —
// stresses the byte preservation with a payload that includes boundary-like
// sequences and zero bytes (which would mangle if any text decode happened).
func TestOpenAIWhisperDirector_AudioBytesUnchangedByteIdentical(t *testing.T) {
	t.Setenv("UPSTREAM_STT_OPENAI_MODEL", "")
	srv, _, _ := captureUpstream(t)
	upstream, _ := url.Parse(srv.URL)
	resolver := models.NewResolverForTesting(map[[2]string]string{
		{"whisper", "openai-whisper"}: "whisper-1",
	})
	director := BuildOpenAIWhisperDirector(upstream, "sk-openai-test", resolver, "openai-whisper", discardLogger())

	// Tricky payload: includes \r\n--boundary-like sequence + many zero bytes +
	// high bytes (0xff). If the helper decoded part bodies as strings or
	// re-encoded, these would corrupt.
	tricky := bytes.Join([][]byte{
		{0x00, 0x01, 0x02, 0x03, 0xff, 0xfe, 0xfd},
		[]byte("\r\n--fake-boundary-123\r\n"),
		{0x00, 0x00, 0x00, 0x00},
		bytes.Repeat([]byte{0xab, 0xcd}, 100),
	}, nil)

	body, ct := buildMultipartBody(t, []string{"whisper"}, "tricky.wav", tricky)
	req, forwarded := applyDirector(t, director, http.MethodPost, "/v1/audio/transcriptions", ct, body, nil, nil)

	_, gotFile, perr := parseMultipartFromBytes(forwarded, req.Header.Get("Content-Type"))
	if perr != nil {
		t.Fatalf("forwarded body parse: %v", perr)
	}
	if !bytes.Equal(gotFile, tricky) {
		t.Errorf("audio bytes mutated for tricky payload (zero bytes + boundary-like sequences)")
	}
}

// Test 3 (R6 base): TestOpenAIWhisperDirector_ContentTypeBoundaryRewritten —
// asserts the forwarded Content-Type header has a fresh boundary (the
// multipart.Writer always assigns a new one) AND the prefix is correct.
func TestOpenAIWhisperDirector_ContentTypeBoundaryRewritten(t *testing.T) {
	t.Setenv("UPSTREAM_STT_OPENAI_MODEL", "")
	srv, _, _ := captureUpstream(t)
	upstream, _ := url.Parse(srv.URL)
	resolver := models.NewResolverForTesting(map[[2]string]string{
		{"whisper", "openai-whisper"}: "whisper-1",
	})
	director := BuildOpenAIWhisperDirector(upstream, "sk-openai-test", resolver, "openai-whisper", discardLogger())

	body, ct := buildMultipartBody(t, []string{"whisper"}, "a.wav", []byte("RIFFAUDIO"))
	originalBoundary := ct // capture for comparison
	req, _ := applyDirector(t, director, http.MethodPost, "/v1/audio/transcriptions", ct, body, nil, nil)

	newCT := req.Header.Get("Content-Type")
	if !strings.HasPrefix(newCT, "multipart/form-data; boundary=") {
		t.Errorf("Content-Type prefix wrong: %q", newCT)
	}
	if newCT == originalBoundary {
		t.Errorf("Content-Type boundary not rewritten — multipart.Writer should produce a fresh boundary")
	}
}

// Test 4 (R6 base): TestOpenAIWhisperDirector_NonMultipartContentTypePassesThrough —
// a JSON body sent to the whisper director leaves the body untouched.
// Defensive: shouldn't happen in practice, but we don't want to corrupt the
// request if it does.
func TestOpenAIWhisperDirector_NonMultipartContentTypePassesThrough(t *testing.T) {
	srv, _, _ := captureUpstream(t)
	upstream, _ := url.Parse(srv.URL)
	resolver := models.NewResolverForTesting(map[[2]string]string{
		{"whisper", "openai-whisper"}: "whisper-1",
	})
	director := BuildOpenAIWhisperDirector(upstream, "sk-openai-test", resolver, "openai-whisper", discardLogger())

	body := []byte(`{"model":"whisper","file":"<base64...>"}`)
	_, out := applyDirector(t, director, http.MethodPost, "/v1/audio/transcriptions", "application/json", body, nil, nil)
	if !bytes.Equal(out, body) {
		t.Errorf("non-multipart body mutated: got %q want %q", string(out), string(body))
	}
}

// Test 5 (R6 base): TestOpenAIWhisperDirector_ResolverMissPassesModelUnchanged —
// resolver empty; the multipart model field stays as the alias. OpenAI may
// 400 the request but that's fine — breaker classifies 4xx as non-failure
// per D-A4.
func TestOpenAIWhisperDirector_ResolverMissPassesModelUnchanged(t *testing.T) {
	t.Setenv("UPSTREAM_STT_OPENAI_MODEL", "")
	srv, _, _ := captureUpstream(t)
	upstream, _ := url.Parse(srv.URL)
	resolver := models.NewResolverForTesting(nil)
	director := BuildOpenAIWhisperDirector(upstream, "sk-openai-test", resolver, "openai-whisper", discardLogger())

	body, ct := buildMultipartBody(t, []string{"whisper"}, "a.wav", []byte("RIFFAUDIO"))
	req, forwarded := applyDirector(t, director, http.MethodPost, "/v1/audio/transcriptions", ct, body, nil, nil)

	gotModel, _, perr := parseMultipartFromBytes(forwarded, req.Header.Get("Content-Type"))
	if perr != nil {
		t.Fatalf("forwarded body parse: %v", perr)
	}
	if gotModel != "whisper" {
		t.Errorf("model = %q, want whisper (alias passes through on resolver miss)", gotModel)
	}
}

// Test 6 (R6 — MultipartMissingModelInjectsTarget): construct multipart
// WITHOUT a "model" form field. Director MUST inject the resolved target
// using the canonical alias for this upstream (here: "whisper" → "whisper-1").
func TestOpenAIWhisperDirector_MultipartMissingModelInjectsTarget(t *testing.T) {
	t.Setenv("UPSTREAM_STT_OPENAI_MODEL", "")
	srv, _, _ := captureUpstream(t)
	upstream, _ := url.Parse(srv.URL)
	resolver := models.NewResolverForTesting(map[[2]string]string{
		{"whisper", "openai-whisper"}: "whisper-1",
	})
	director := BuildOpenAIWhisperDirector(upstream, "sk-openai-test", resolver, "openai-whisper", discardLogger())

	wav := loadProbeWAV(t)
	// No model field — only file.
	body, ct := buildMultipartBody(t, nil, "probe.wav", wav)
	req, forwarded := applyDirector(t, director, http.MethodPost, "/v1/audio/transcriptions", ct, body, nil, nil)

	gotModel, gotFile, perr := parseMultipartFromBytes(forwarded, req.Header.Get("Content-Type"))
	if perr != nil {
		t.Fatalf("forwarded body parse: %v", perr)
	}
	if gotModel != "whisper-1" {
		t.Errorf("model = %q, want whisper-1 (injected for missing-model multipart)", gotModel)
	}
	if !bytes.Equal(gotFile, wav) {
		t.Errorf("audio bytes mutated when injecting model field")
	}
}

// Test 7 (R6 — MultipartDuplicateModelRejected — WARNING-3): construct
// multipart with TWO "model" form fields. The WhisperAbortGuard wrapper
// returns HTTP 400 and the request is NEVER forwarded to upstream.
func TestOpenAIWhisperDirector_MultipartDuplicateModelRejected(t *testing.T) {
	t.Setenv("UPSTREAM_STT_OPENAI_MODEL", "")

	// Sentinel: count how often the inner handler is invoked. Must remain 0.
	var innerCalls int
	innerHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		innerCalls++
		w.WriteHeader(200)
	})

	resolver := models.NewResolverForTesting(map[[2]string]string{
		{"whisper", "openai-whisper"}: "whisper-1",
	})

	guard := whisperAbortGuard(innerHandler, resolver, "openai-whisper", discardLogger())

	body, ct := buildMultipartBody(t, []string{"whisper", "whisper-large"}, "a.wav", []byte("RIFFAUDIO"))
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/audio/transcriptions", bytes.NewReader(body))
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()

	guard.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (duplicate-model abort)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "duplicate 'model'") {
		t.Errorf("body does not mention duplicate-model rejection: %s", rec.Body.String())
	}
	// JSON envelope shape
	var env struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Errorf("body not valid JSON envelope: %v", err)
	}
	if env.Error.Type != "invalid_request_error" {
		t.Errorf("error.type = %q, want invalid_request_error", env.Error.Type)
	}
	if innerCalls != 0 {
		t.Errorf("innerHandler called %d times — guard must abort BEFORE invoking proxy", innerCalls)
	}
}

// Test 8 (R6 — InvalidMultipartParsePassesThrough): malformed multipart body
// (Content-Type claims multipart but body is broken). Director falls through
// to forward original body unchanged — never 500s internally.
func TestOpenAIWhisperDirector_InvalidMultipartParsePassesThrough(t *testing.T) {
	srv, _, _ := captureUpstream(t)
	upstream, _ := url.Parse(srv.URL)
	resolver := models.NewResolverForTesting(map[[2]string]string{
		{"whisper", "openai-whisper"}: "whisper-1",
	})
	director := BuildOpenAIWhisperDirector(upstream, "sk-openai-test", resolver, "openai-whisper", discardLogger())

	// Missing terminator + wrong boundary — multipart.NewReader will choke.
	mangled := []byte("--boundary123\r\nContent-Disposition: form-data; name=\"junk\"\r\n\r\nincomplete")
	_, out := applyDirector(t, director, http.MethodPost, "/v1/audio/transcriptions",
		"multipart/form-data; boundary=boundary123", mangled, nil, nil)

	if !bytes.Equal(out, mangled) {
		t.Errorf("malformed multipart body mutated — director must pass through on parse failure")
	}
}
