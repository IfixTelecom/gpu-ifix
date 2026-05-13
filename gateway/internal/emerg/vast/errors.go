// Package vast contains the Phase 6 thin Go client for the Vast.ai
// REST API (https://vast.ai/api/v0). Operations: search_offers,
// create_instance, get_instance, destroy_instance, ping. Sentinel
// errors below let callers (internal/emerg/lifecycle.go) implement
// retry/abort logic without parsing HTTP envelopes themselves.
//
// Threat note (T-6-01): VAST_AI_API_KEY is passed only via the
// Authorization: Bearer header. It MUST NOT appear in error
// messages, log lines, Sentry breadcrumbs, or panic stack traces.
// VastError below intentionally omits the request URL/headers.
package vast

import (
	"errors"
	"net/http"
)

var (
	// ErrOfferGone signals that PUT /asks/{id}/ returned 404 or 410
	// with envelope error="no_such_ask" — the offer was accepted by
	// another buyer between search_offers and create_instance. The
	// lifecycle handles this by re-running search and retrying the
	// bid up to 3 times (D-A3); only after 3 race losses does the
	// caller surface emerg.ErrOfferRaceLost.
	ErrOfferGone = errors.New("vast: offer no longer available (404/410 no_such_ask)")

	// ErrInstanceNotFound signals that GET /instances/{id} or
	// DELETE /instances/{id} returned 404 with envelope
	// error="no_such_instance". For the leader-recovery path
	// (D-D5) this means the instance was destroyed by Vast.ai
	// (or another operator), and the orphan lifecycle row must
	// be closed with shutdown_reason='leader_recovery_lost'.
	ErrInstanceNotFound = errors.New("vast: instance not found (404 no_such_instance)")

	// ErrRateLimited signals HTTP 429 from the Vast.ai API. The
	// vast.Client honors a conservative 1 req/s token bucket
	// (VAST_API_QPS_LIMIT, RESEARCH Open Question 12) so this
	// error should be rare; when it does fire, callers should
	// back off and retry rather than abort.
	ErrRateLimited = errors.New("vast: HTTP 429 rate limited")

	// ErrUnauthorized signals HTTP 401 or 403 from the Vast.ai
	// API — the VAST_AI_API_KEY is invalid, expired, or revoked.
	// Detected at boot via Ping() (vast.Client.Ping() calls
	// /users/current); fail-loud at startup keeps a dead key
	// from silently disabling the entire emergency reconciler.
	ErrUnauthorized = errors.New("vast: HTTP 401/403 — VAST_AI_API_KEY invalid")
)

// VastError wraps a non-sentinel HTTP failure from the Vast.ai API
// (e.g., 5xx with no specific envelope code). Status is the raw HTTP
// status; Code is the value of the "error" field in the JSON envelope
// (or "server_error" / "unauthorized" for synthetic codes); Msg is the
// human-readable "msg" field. The Error() method intentionally formats
// Status via http.StatusText so the API key never appears even if a
// caller wraps this with %w in a log line.
type VastError struct {
	Status int
	Code   string
	Msg    string
}

func (e *VastError) Error() string {
	return "vast: HTTP " + http.StatusText(e.Status) + ": " + e.Msg
}
