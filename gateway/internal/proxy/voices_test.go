// Package proxy (voices_test.go): Phase 06.7 Wave 0 RED scaffolding (Nyquist
// gate), UNSKIPPED + asserting real behavior by the owning plan 06.7-07.
//
// ENGINE: Chatterbox Multilingual is ZERO-SHOT — a voice is cloned at synth
// time by passing a reference WAV as audio_prompt_path. There is NO persisted
// .pt speaker embedding (D-08 revised). Therefore voice-create persists ONLY
// the reference WAV (to S3) + a catalog row (Postgres); pod replacement is
// survived by refetching that WAV, not by regenerating any embedding.
//
// OWNER map (authority: 06.7-02-PLAN.md <stub_ownership_map>) — all OWNER 07:
//   - TestVoices_CreateUploadsToS3AndReturnsVoiceID
//   - TestVoices_TenantIsolation_ListOnlyOwn          (D-10, threat T-06.7-02)
//   - TestVoices_RejectsOversizeWAV                   (DoS cap)
//   - TestVoices_S3KeyFromUUIDNotInput                (path-traversal, T-06.7-03)
//   - TestVoices_DeleteRemovesRowAndS3Object
//   - TestVoices_UnsupportedResponseFormatReturns400
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

// --- fakes ---------------------------------------------------------------

type fakeVoiceStore struct {
	mu   sync.Mutex
	rows []gen.AiGatewayVoice
}

func (f *fakeVoiceStore) CreateVoice(_ context.Context, arg gen.CreateVoiceParams) (gen.AiGatewayVoice, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := arg.ID
	if id == uuid.Nil {
		// Seed helper paths that don't supply an id (mirror the DB default).
		id = uuid.New()
	}
	row := gen.AiGatewayVoice{
		ID:       id,
		TenantID: arg.TenantID,
		Label:    arg.Label,
		S3Key:    arg.S3Key,
	}
	f.rows = append(f.rows, row)
	return row, nil
}

func (f *fakeVoiceStore) ListVoicesByTenant(_ context.Context, tenantID uuid.UUID) ([]gen.AiGatewayVoice, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []gen.AiGatewayVoice
	for _, r := range f.rows {
		if r.TenantID == tenantID {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeVoiceStore) GetVoiceForTenant(_ context.Context, arg gen.GetVoiceForTenantParams) (gen.AiGatewayVoice, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.rows {
		if r.ID == arg.ID && r.TenantID == arg.TenantID {
			return r, nil
		}
	}
	return gen.AiGatewayVoice{}, errVoiceNotFound
}

func (f *fakeVoiceStore) DeleteVoiceForTenant(_ context.Context, arg gen.DeleteVoiceForTenantParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	kept := f.rows[:0]
	for _, r := range f.rows {
		if r.ID == arg.ID && r.TenantID == arg.TenantID {
			continue
		}
		kept = append(kept, r)
	}
	f.rows = kept
	return nil
}

var errVoiceNotFound = &voiceErr{"not found"}

type voiceErr struct{ s string }

func (e *voiceErr) Error() string { return e.s }

type fakeObjectStore struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newFakeObjectStore() *fakeObjectStore {
	return &fakeObjectStore{objects: map[string][]byte{}}
}

func (f *fakeObjectStore) PutObject(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	b, _ := io.ReadAll(r)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[key] = b
	return nil
}

func (f *fakeObjectStore) RemoveObject(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, key)
	return nil
}

func (f *fakeObjectStore) has(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.objects[key]
	return ok
}

func (f *fakeObjectStore) keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.objects))
	for k := range f.objects {
		out = append(out, k)
	}
	return out
}

// wrapWithTenant injects an AuthContext with the given tenant id so handlers'
// auth.MustFromContext succeeds (mirrors the real auth.Middleware).
func wrapWithTenant(h http.Handler, tenantID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := auth.WithContext(r.Context(), auth.AuthContext{
			TenantID:  tenantID,
			APIKeyID:  "key-1",
			DataClass: auth.DataClassNormal,
			KeyPrefix: "ifix_sk_****test",
		})
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}

func newVoiceHandlers(store VoiceStore, objects VoiceObjectStore) *VoiceHandlers {
	return &VoiceHandlers{
		Store:          store,
		Objects:        objects,
		S3VoicePrefix:  "voices",
		MaxUploadBytes: 1024,
		Log:            discardLogger(),
	}
}

// multipartWAV builds a multipart body with a "file" part of size bytes + a
// "label" field, returning the body + the Content-Type header.
func multipartWAV(t *testing.T, label string, size int) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "ref.wav")
	_, _ = fw.Write(bytes.Repeat([]byte{0x00}, size))
	_ = mw.WriteField("label", label)
	_ = mw.Close()
	return &buf, mw.FormDataContentType()
}

// --- tests ---------------------------------------------------------------

// TestVoices_CreateUploadsToS3AndReturnsVoiceID asserts POST uploads the WAV to
// S3, inserts a catalog row, and returns a generated voice_id (no .pt, D-08).
func TestVoices_CreateUploadsToS3AndReturnsVoiceID(t *testing.T) {
	store := &fakeVoiceStore{}
	objects := newFakeObjectStore()
	h := newVoiceHandlers(store, objects)
	gateway := httptest.NewServer(wrapWithTenant(http.HandlerFunc(h.Create), uuid.New().String()))
	defer gateway.Close()

	body, ct := multipartWAV(t, "miro", 256)
	req, _ := http.NewRequest("POST", gateway.URL+"/v1/audio/voices", body)
	req.Header.Set("Content-Type", ct)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status got %d, want 201", resp.StatusCode)
	}
	var out voiceDTO
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.VoiceID == "" {
		t.Errorf("no voice_id returned")
	}
	if out.Label != "miro" {
		t.Errorf("label got %q", out.Label)
	}
	if len(store.rows) != 1 {
		t.Fatalf("catalog rows got %d, want 1", len(store.rows))
	}
	if len(objects.keys()) != 1 {
		t.Fatalf("S3 objects got %d, want 1", len(objects.keys()))
	}
	// No .pt object — only the reference WAV.
	for _, k := range objects.keys() {
		if strings.HasSuffix(k, ".pt") {
			t.Errorf("a .pt object was persisted (%q) — must be zero-shot WAV only", k)
		}
		if !strings.HasSuffix(k, ".wav") {
			t.Errorf("S3 object %q is not a .wav", k)
		}
	}
}

// TestVoices_TenantIsolation_ListOnlyOwn asserts GET returns ONLY the caller's
// voices, never another tenant's (D-10 / T-06.7-13).
func TestVoices_TenantIsolation_ListOnlyOwn(t *testing.T) {
	store := &fakeVoiceStore{}
	objects := newFakeObjectStore()
	tenantA := uuid.New()
	tenantB := uuid.New()
	// Seed one voice per tenant directly.
	_, _ = store.CreateVoice(context.Background(), gen.CreateVoiceParams{TenantID: tenantA, Label: "a", S3Key: "voices/a.wav"})
	_, _ = store.CreateVoice(context.Background(), gen.CreateVoiceParams{TenantID: tenantB, Label: "b", S3Key: "voices/b.wav"})

	h := newVoiceHandlers(store, objects)
	gateway := httptest.NewServer(wrapWithTenant(http.HandlerFunc(h.List), tenantA.String()))
	defer gateway.Close()

	resp, err := http.Get(gateway.URL + "/v1/audio/voices")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Data []voiceDTO `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Data) != 1 {
		t.Fatalf("tenant A list got %d voices, want 1 (cross-tenant leak?)", len(out.Data))
	}
	if out.Data[0].Label != "a" {
		t.Errorf("tenant A saw label %q — expected only its own 'a'", out.Data[0].Label)
	}
}

// TestVoices_RejectsOversizeWAV asserts an oversize upload is rejected with a
// clean 4xx (DoS cap).
func TestVoices_RejectsOversizeWAV(t *testing.T) {
	store := &fakeVoiceStore{}
	objects := newFakeObjectStore()
	h := newVoiceHandlers(store, objects) // MaxUploadBytes = 1024
	// Wrap with MaxBytesHandler exactly as main.go mounts it.
	mounted := http.MaxBytesHandler(http.HandlerFunc(h.Create), 1024)
	gateway := httptest.NewServer(wrapWithTenant(mounted, uuid.New().String()))
	defer gateway.Close()

	body, ct := multipartWAV(t, "big", 4096) // exceeds 1024 cap
	req, _ := http.NewRequest("POST", gateway.URL+"/v1/audio/voices", body)
	req.Header.Set("Content-Type", ct)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 400 || resp.StatusCode >= 500 {
		t.Fatalf("oversize upload status got %d, want a 4xx", resp.StatusCode)
	}
	if len(store.rows) != 0 || len(objects.keys()) != 0 {
		t.Errorf("oversize upload should persist nothing; rows=%d objects=%d", len(store.rows), len(objects.keys()))
	}
}

// TestVoices_S3KeyFromUUIDNotInput asserts the S3 object key derives from a
// server-generated UUID, not from any client-supplied label/filename
// (path-traversal mitigation, T-06.7-14).
func TestVoices_S3KeyFromUUIDNotInput(t *testing.T) {
	store := &fakeVoiceStore{}
	objects := newFakeObjectStore()
	h := newVoiceHandlers(store, objects)
	gateway := httptest.NewServer(wrapWithTenant(http.HandlerFunc(h.Create), uuid.New().String()))
	defer gateway.Close()

	// A legitimate (regex-passing) label that must NOT appear in the key.
	body, ct := multipartWAV(t, "evil_label", 64)
	req, _ := http.NewRequest("POST", gateway.URL+"/v1/audio/voices", body)
	req.Header.Set("Content-Type", ct)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status got %d, want 201", resp.StatusCode)
	}

	if len(store.rows) != 1 {
		t.Fatalf("rows got %d", len(store.rows))
	}
	row := store.rows[0]
	// Key = voices/<uuid>.wav — must contain the voice UUID and NOT the label.
	if !strings.Contains(row.S3Key, row.ID.String()) {
		t.Errorf("S3 key %q does not contain the server-generated voice UUID %q", row.S3Key, row.ID.String())
	}
	if strings.Contains(row.S3Key, "evil_label") {
		t.Errorf("S3 key %q leaked the client label (path-traversal risk)", row.S3Key)
	}
	if !strings.HasPrefix(row.S3Key, "voices/") {
		t.Errorf("S3 key %q does not use the configured prefix", row.S3Key)
	}
}

// TestVoices_DeleteRemovesRowAndS3Object asserts DELETE removes BOTH the
// catalog row AND the S3 object.
func TestVoices_DeleteRemovesRowAndS3Object(t *testing.T) {
	store := &fakeVoiceStore{}
	objects := newFakeObjectStore()
	tenant := uuid.New()
	// Seed a voice + its S3 object.
	row, _ := store.CreateVoice(context.Background(), gen.CreateVoiceParams{
		TenantID: tenant, Label: "v", S3Key: "voices/seed.wav",
	})
	_ = objects.PutObject(context.Background(), row.S3Key, strings.NewReader("wav"), 3, "audio/wav")

	h := newVoiceHandlers(store, objects)
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /v1/audio/voices/{id}", h.Delete)
	gateway := httptest.NewServer(wrapWithTenant(mux, tenant.String()))
	defer gateway.Close()

	req, _ := http.NewRequest("DELETE", gateway.URL+"/v1/audio/voices/"+row.ID.String(), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status got %d, want 204", resp.StatusCode)
	}
	if len(store.rows) != 0 {
		t.Errorf("catalog row not removed: %d remain", len(store.rows))
	}
	if objects.has("voices/seed.wav") {
		t.Errorf("S3 object not removed")
	}
}

// TestVoices_UnsupportedResponseFormatReturns400 asserts a speech request with
// an unsupported response_format returns a clean OpenAI-shaped 400 (asserted
// here against the Piper adapter which owns response_format validation).
func TestVoices_UnsupportedResponseFormatReturns400(t *testing.T) {
	piper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/basic")
		_, _ = w.Write([]byte{0xFF})
	}))
	defer piper.Close()
	adapter, err := NewPiperTTSAdapter(piper.URL, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewServer(wrapWithMiddleware(adapter))
	defer gateway.Close()

	resp, err := http.Post(gateway.URL+"/v1/audio/speech", "application/json",
		strings.NewReader(`{"input":"x","voice":"v","response_format":"flac"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status got %d, want 400", resp.StatusCode)
	}
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("not an OpenAI error envelope: %v", err)
	}
	if env.Error.Code == "" {
		t.Errorf("error envelope missing code")
	}
}
