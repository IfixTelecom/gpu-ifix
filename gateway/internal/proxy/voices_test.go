// Package proxy (voices_test.go): Phase 06.7 Wave 0 RED scaffolding (Nyquist
// gate). Skip stubs binding the voice-catalog CRUD handlers (create / list /
// delete) to their owning implementation plan, encoding the V4 tenant-
// isolation, V5/V12 path-traversal, and DoS-cap mitigations as RED tests.
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

import "testing"

// TestVoices_CreateUploadsToS3AndReturnsVoiceID asserts POST /v1/voices with
// a reference WAV uploads the WAV to S3 (mocked via an S3 interface), inserts
// a catalog row, and returns a generated voice_id. Persists the reference WAV
// + catalog row ONLY — NO .pt embedding (Chatterbox zero-shot, D-08).
//
// OWNER: Plan 06.7-07.
func TestVoices_CreateUploadsToS3AndReturnsVoiceID(t *testing.T) {
	t.Skip("OWNER Plan 06.7-07 — implement voices create handler; assert S3 upload of reference WAV + catalog row + returned voice_id (NO .pt, zero-shot D-08)")
}

// TestVoices_TenantIsolation_ListOnlyOwn asserts GET /v1/voices returns ONLY
// voices owned by the requesting tenant (from auth.MustFromContext), never
// another tenant's rows (D-10 / threat T-06.7-02 Information Disclosure).
//
// OWNER: Plan 06.7-07.
func TestVoices_TenantIsolation_ListOnlyOwn(t *testing.T) {
	t.Skip("OWNER Plan 06.7-07 — implement voices list handler; assert tenant isolation — list returns only the caller's voices, never cross-tenant (D-10)")
}

// TestVoices_RejectsOversizeWAV asserts the create handler rejects a
// reference WAV larger than the configured cap with a clean 4xx (DoS
// mitigation — an unbounded upload would exhaust pod/S3 resources).
//
// OWNER: Plan 06.7-07.
func TestVoices_RejectsOversizeWAV(t *testing.T) {
	t.Skip("OWNER Plan 06.7-07 — implement WAV size cap; assert oversize upload -> clean 4xx (DoS cap)")
}

// TestVoices_S3KeyFromUUIDNotInput asserts the S3 object key is derived from
// a server-generated UUID, NOT from any client-supplied filename or voice
// name — preventing path traversal into other tenants' S3 prefixes (V5/V12
// mitigation / threat T-06.7-03 Tampering).
//
// OWNER: Plan 06.7-07.
func TestVoices_S3KeyFromUUIDNotInput(t *testing.T) {
	t.Skip("OWNER Plan 06.7-07 — assert S3 key derives from server UUID, not client input (path-traversal mitigation, T-06.7-03)")
}

// TestVoices_DeleteRemovesRowAndS3Object asserts DELETE /v1/voices/{id}
// removes BOTH the Postgres catalog row AND the S3 reference WAV object
// (cleanup semantics — no orphaned S3 objects after delete).
//
// OWNER: Plan 06.7-07.
func TestVoices_DeleteRemovesRowAndS3Object(t *testing.T) {
	t.Skip("OWNER Plan 06.7-07 — implement voices delete handler; assert both catalog row + S3 object removed")
}

// TestVoices_UnsupportedResponseFormatReturns400 asserts that a TTS / voices
// request with an unsupported response_format (mp3/opus/flac/aac) returns a
// clean OpenAI-shaped 400 rather than a 500 or a silent default.
//
// OWNER: Plan 06.7-07.
func TestVoices_UnsupportedResponseFormatReturns400(t *testing.T) {
	t.Skip("OWNER Plan 06.7-07 — unsupported response_format -> clean OpenAI-shaped 400")
}
