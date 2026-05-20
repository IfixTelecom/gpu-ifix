// Package proxy (voices.go): the /v1/audio/voices CRUD surface — the first Go
// S3 consumer in the gateway. It manages ZERO-SHOT voice clones for the
// Chatterbox Multilingual engine (Wave 0 GATE 1).
//
// VOICE PERSISTENCE CONTRACT (D-08 revised — Chatterbox zero-shot):
//
//	voice_id ─┬─► Postgres ai_gateway.voices row (id, tenant_id, label, s3_key)
//	          └─► MinIO S3 object  <S3VoicePrefix>/<voice_id>.wav  (reference WAV)
//
// This handler persists ONLY the reference WAV + the catalog row. It does NOT
// generate or push any speaker-embedding file (no dot-pt) — Chatterbox clones
// zero-shot.
// On a speech request the pod (Plan 05) fetches <S3VoicePrefix>/<voice_id>.wav
// and passes it directly as audio_prompt_path; the pod's local cache is
// rebuildable from this durable S3 WAV. S3VoicePrefix here MUST match the
// pod's CHATTERBOX_S3_VOICE_PREFIX so the pod can find the WAV.
//
// Security (06.7 threat register):
//   - T-06.7-13 (tenant isolation): EVERY handler reads the tenant from
//     auth.MustFromContext — NEVER from the request body — and every sqlc
//     query filters by tenant_id (D-10 / ASVS V4).
//   - T-06.7-14 (path traversal): the S3 object key is derived from a
//     server-generated UUID + S3VoicePrefix, never from client input; label
//     is validated against ^[A-Za-z0-9_\-\.]+$ and `..` is rejected (ASVS V5/V12).
//   - T-06.7-15 (DoS): the upload route is wrapped with http.MaxBytesHandler
//     (VoiceMaxUploadBytes) in cmd/gateway/main.go.
package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
)

// labelPattern enforces a safe character set on the client-supplied voice
// label (T-06.7-14). It is also defense-in-depth: the S3 key never derives
// from the label, but a clean label keeps the catalog readable + log-safe.
var labelPattern = regexp.MustCompile(`^[A-Za-z0-9_\-\.]+$`)

// VoiceStore is the catalog persistence surface used by the handlers. It is
// the subset of *gen.Queries the voices CRUD needs, declared as an interface
// so tests can inject a fake without a live Postgres (T-06.7-13 assertions run
// hermetically).
type VoiceStore interface {
	CreateVoice(ctx context.Context, arg gen.CreateVoiceParams) (gen.AiGatewayVoice, error)
	ListVoicesByTenant(ctx context.Context, tenantID uuid.UUID) ([]gen.AiGatewayVoice, error)
	GetVoiceForTenant(ctx context.Context, arg gen.GetVoiceForTenantParams) (gen.AiGatewayVoice, error)
	DeleteVoiceForTenant(ctx context.Context, arg gen.DeleteVoiceForTenantParams) error
}

// VoiceObjectStore is the S3 object surface used by the handlers (PutObject on
// create, RemoveObject on delete). Declared as an interface so tests inject a
// fake S3 (no live MinIO) and assert the UUID-derived key + delete cleanup.
// The concrete implementation is minioObjectStore (MinIO Go SDK), built at
// boot in cmd/gateway/main.go from config.Minio* creds.
type VoiceObjectStore interface {
	PutObject(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	RemoveObject(ctx context.Context, key string) error
}

// VoiceHandlers bundles the dependencies for the three voice handlers. Build
// one at boot and mount its methods (cmd/gateway/main.go).
type VoiceHandlers struct {
	Store          VoiceStore
	Objects        VoiceObjectStore
	S3VoicePrefix  string
	MaxUploadBytes int64
	Log            *slog.Logger
}

// objectKey derives the S3 object key from the server-generated voice UUID and
// the configured prefix — NEVER from client input (T-06.7-14 path traversal).
func (h *VoiceHandlers) objectKey(voiceID uuid.UUID) string {
	prefix := strings.Trim(h.S3VoicePrefix, "/")
	return fmt.Sprintf("%s/%s.wav", prefix, voiceID.String())
}

// tenantUUID parses the authenticated tenant id (string on AuthContext) into a
// uuid.UUID for the sqlc queries. The id always comes from auth.MustFromContext
// — never from the request body (D-10).
func tenantUUID(ctx context.Context) (uuid.UUID, error) {
	ac := auth.MustFromContext(ctx)
	return uuid.Parse(ac.TenantID)
}

// Create handles POST /v1/audio/voices. Multipart form with a "file" (the
// reference WAV) + a "label". It generates a UUID voice_id, uploads the WAV to
// MinIO at <S3VoicePrefix>/<voice_id>.wav, writes the catalog row scoped to the
// authenticated tenant, and returns {voice_id, label}.
//
// Persists the reference WAV + row ONLY — NO speaker-embedding file (no dot-pt)
// is generated or pushed (Chatterbox zero-shot; the pod fetches the WAV lazily
// as audio_prompt_path per the Plan 05 contract).
//
// IDEMPOTENCY STRATEGY (documented): upsert-on-(tenant_id, label). On a client
// retry with the same label, Create first lists the tenant's voices and, if a
// voice with that label already exists, returns it WITHOUT creating a duplicate
// orphan S3 object or row. This makes the upload safe to retry. (An optional
// Idempotency-Key header is reserved for a future per-request dedup store; the
// label-upsert covers the common "client retried the same upload" case today.)
func (h *VoiceHandlers) Create(w http.ResponseWriter, r *http.Request) {
	tenant, err := tenantUUID(r.Context())
	if err != nil {
		httpx.WriteOpenAIError(w, http.StatusUnauthorized,
			"authentication_error", "invalid_tenant",
			"Authenticated tenant id is not a valid UUID.")
		return
	}

	// MaxBytesHandler in main.go caps the body; ParseMultipartForm here keeps
	// the in-memory buffer small (the rest spills to a capped temp file, then
	// the MaxBytesReader trips if the total exceeds VoiceMaxUploadBytes -> 4xx).
	if perr := r.ParseMultipartForm(1 << 20); perr != nil {
		// A MaxBytesReader trip surfaces here as a generic error; report 413.
		httpx.WriteOpenAIError(w, http.StatusRequestEntityTooLarge,
			"invalid_request_error", "upload_too_large",
			"The uploaded reference WAV exceeds the allowed size.")
		return
	}

	label := strings.TrimSpace(r.FormValue("label"))
	if label == "" || strings.Contains(label, "..") || !labelPattern.MatchString(label) {
		httpx.WriteOpenAIError(w, http.StatusBadRequest,
			"invalid_request_error", "invalid_label",
			"label is required and must match ^[A-Za-z0-9_\\-\\.]+$.")
		return
	}

	file, hdr, ferr := r.FormFile("file")
	if ferr != nil {
		httpx.WriteOpenAIError(w, http.StatusBadRequest,
			"invalid_request_error", "missing_file",
			"A reference WAV 'file' is required.")
		return
	}
	defer file.Close()

	// Defense-in-depth size check (the route MaxBytesHandler is the primary cap).
	if h.MaxUploadBytes > 0 && hdr.Size > h.MaxUploadBytes {
		httpx.WriteOpenAIError(w, http.StatusRequestEntityTooLarge,
			"invalid_request_error", "upload_too_large",
			"The uploaded reference WAV exceeds the allowed size.")
		return
	}

	// IDEMPOTENCY: upsert-on-(tenant, label). If a voice with this label
	// already exists for the tenant, return it instead of creating a duplicate.
	existing, lerr := h.Store.ListVoicesByTenant(r.Context(), tenant)
	if lerr == nil {
		for _, v := range existing {
			if v.Label == label {
				h.writeVoice(w, http.StatusOK, v)
				return
			}
		}
	}

	voiceID := uuid.New()
	key := h.objectKey(voiceID)

	ct := hdr.Header.Get("Content-Type")
	if ct == "" {
		ct = "audio/wav"
	}
	if perr := h.Objects.PutObject(r.Context(), key, file, hdr.Size, ct); perr != nil {
		h.Log.ErrorContext(r.Context(), "voice S3 put failed",
			"err", perr, "request_id", httpx.RequestIDFrom(r.Context()))
		httpx.WriteOpenAIError(w, http.StatusBadGateway,
			"api_error", "storage_unavailable",
			"Could not persist the reference WAV to storage.")
		return
	}

	row, cerr := h.Store.CreateVoice(r.Context(), gen.CreateVoiceParams{
		ID:       voiceID, // same UUID the s3_key is derived from (pod fetches by id)
		TenantID: tenant,
		Label:    label,
		S3Key:    key,
	})
	if cerr != nil {
		// Roll back the orphan S3 object so a failed row insert leaves no
		// dangling WAV (cleanup symmetry with Delete).
		_ = h.Objects.RemoveObject(r.Context(), key)
		h.Log.ErrorContext(r.Context(), "voice row insert failed",
			"err", cerr, "request_id", httpx.RequestIDFrom(r.Context()))
		httpx.WriteOpenAIError(w, http.StatusInternalServerError,
			"api_error", "catalog_write_failed",
			"Could not persist the voice catalog row.")
		return
	}

	h.writeVoice(w, http.StatusCreated, row)
}

// List handles GET /v1/audio/voices — returns ONLY the caller's voices
// (tenant-scoped via auth.MustFromContext; T-06.7-13 / D-10).
func (h *VoiceHandlers) List(w http.ResponseWriter, r *http.Request) {
	tenant, err := tenantUUID(r.Context())
	if err != nil {
		httpx.WriteOpenAIError(w, http.StatusUnauthorized,
			"authentication_error", "invalid_tenant",
			"Authenticated tenant id is not a valid UUID.")
		return
	}
	rows, lerr := h.Store.ListVoicesByTenant(r.Context(), tenant)
	if lerr != nil {
		h.Log.ErrorContext(r.Context(), "voice list failed",
			"err", lerr, "request_id", httpx.RequestIDFrom(r.Context()))
		httpx.WriteOpenAIError(w, http.StatusInternalServerError,
			"api_error", "catalog_read_failed",
			"Could not read the voice catalog.")
		return
	}
	out := struct {
		Data []voiceDTO `json:"data"`
	}{Data: make([]voiceDTO, 0, len(rows))}
	for _, v := range rows {
		out.Data = append(out.Data, voiceDTO{VoiceID: v.ID.String(), Label: v.Label})
	}
	writeJSON(w, http.StatusOK, out)
}

// Delete handles DELETE /v1/audio/voices/{id}. It fetches the voice scoped to
// the caller's tenant, deletes the S3 object, THEN deletes the catalog row.
//
// S3-DELETE-FAILURE BEHAVIOR (documented): delete the row ONLY after the S3
// delete succeeds. On S3 failure, return 502 and KEEP the row so the client
// can safely retry the DELETE (no orphaned row pointing at a still-present
// object, and no row deleted while the object lingers). Deleting another
// tenant's id is a no-op/404 (GetVoiceForTenant returns no row).
func (h *VoiceHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	tenant, err := tenantUUID(r.Context())
	if err != nil {
		httpx.WriteOpenAIError(w, http.StatusUnauthorized,
			"authentication_error", "invalid_tenant",
			"Authenticated tenant id is not a valid UUID.")
		return
	}
	idStr := strings.TrimSpace(r.PathValue("id"))
	id, perr := uuid.Parse(idStr)
	if perr != nil {
		httpx.WriteOpenAIError(w, http.StatusBadRequest,
			"invalid_request_error", "invalid_voice_id",
			"The voice id path parameter must be a UUID.")
		return
	}

	row, gerr := h.Store.GetVoiceForTenant(r.Context(), gen.GetVoiceForTenantParams{
		ID:       id,
		TenantID: tenant,
	})
	if gerr != nil {
		// Not found for this tenant (or another tenant's id) -> 404 no-op.
		httpx.WriteOpenAIError(w, http.StatusNotFound,
			"invalid_request_error", "voice_not_found",
			"No voice with that id for the authenticated tenant.")
		return
	}

	// Delete the S3 object FIRST. If it fails, keep the row + 502 for safe retry.
	if derr := h.Objects.RemoveObject(r.Context(), row.S3Key); derr != nil {
		h.Log.ErrorContext(r.Context(), "voice S3 delete failed; keeping row for retry",
			"err", derr, "s3_key", row.S3Key, "request_id", httpx.RequestIDFrom(r.Context()))
		httpx.WriteOpenAIError(w, http.StatusBadGateway,
			"api_error", "storage_unavailable",
			"Could not delete the reference WAV from storage; the voice was not removed. Retry.")
		return
	}

	if delErr := h.Store.DeleteVoiceForTenant(r.Context(), gen.DeleteVoiceForTenantParams{
		ID:       id,
		TenantID: tenant,
	}); delErr != nil {
		h.Log.ErrorContext(r.Context(), "voice row delete failed after S3 delete",
			"err", delErr, "request_id", httpx.RequestIDFrom(r.Context()))
		httpx.WriteOpenAIError(w, http.StatusInternalServerError,
			"api_error", "catalog_write_failed",
			"The reference WAV was deleted but the catalog row removal failed.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// voiceDTO is the public shape of a voice in API responses (never leaks
// tenant_id or the raw s3_key).
type voiceDTO struct {
	VoiceID string `json:"voice_id"`
	Label   string `json:"label"`
}

func (h *VoiceHandlers) writeVoice(w http.ResponseWriter, status int, v gen.AiGatewayVoice) {
	writeJSON(w, status, voiceDTO{VoiceID: v.ID.String(), Label: v.Label})
}

// writeJSON emits a JSON success body with the given status. (httpx only
// ships the error-envelope helper; success bodies are written locally.)
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// minioObjectStore is the concrete VoiceObjectStore backed by the MinIO Go SDK.
// Built at boot from config.Minio* creds (cmd/gateway/main.go).
type minioObjectStore struct {
	client *minio.Client
	bucket string
}

// NewMinioObjectStore constructs the boot-time S3 client for voice WAVs.
// endpoint is the MINIO_ENDPOINT (e.g. https://s3.ifixtelecom.com.br); the
// scheme decides TLS.
func NewMinioObjectStore(endpoint, accessKey, secretKey, bucket string) (VoiceObjectStore, error) {
	secure := strings.HasPrefix(strings.ToLower(endpoint), "https://")
	host := strings.TrimPrefix(strings.TrimPrefix(endpoint, "https://"), "http://")
	host = strings.TrimRight(host, "/")
	cl, err := minio.New(host, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: secure,
	})
	if err != nil {
		return nil, fmt.Errorf("proxy/voices: minio client: %w", err)
	}
	return &minioObjectStore{client: cl, bucket: bucket}, nil
}

func (m *minioObjectStore) PutObject(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	_, err := m.client.PutObject(ctx, m.bucket, key, r, size, minio.PutObjectOptions{
		ContentType: contentType,
	})
	return err
}

func (m *minioObjectStore) RemoveObject(ctx context.Context, key string) error {
	return m.client.RemoveObject(ctx, m.bucket, key, minio.RemoveObjectOptions{})
}
