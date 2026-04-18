package idempotency

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/audit"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
)

// MaxBodySize caps the body we'll canonical-hash. Larger bodies aren't
// supported for idempotency in Phase 2 (OpenAI chat payloads are well
// under 1 MB in practice). Matches the body-capture cap used by audit.
const MaxBodySize = 1 << 20 // 1 MiB

// cacheable lists the HTTP status classes we persist into the idempotency
// entry. 5xx is retryable — we don't want to lock a client into a transient
// upstream failure for 24h.
func cacheable(status int) bool {
	if status >= 200 && status < 300 {
		return true
	}
	if status == http.StatusUnprocessableEntity {
		return true
	}
	if status == http.StatusBadRequest {
		return true
	}
	return false
}

// Middleware returns a chi-compatible middleware. Mount ONLY on
// POST /v1/chat/completions per D-C4.
func Middleware(store *Store, log *slog.Logger) func(http.Handler) http.Handler {
	log = log.With("module", "IDEM")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			idemKey := r.Header.Get("Idempotency-Key")
			if idemKey == "" {
				next.ServeHTTP(w, r)
				return
			}

			// Path gate (defense-in-depth; wiring only mounts this on chat).
			if !strings.HasPrefix(r.URL.Path, "/v1/chat/completions") {
				httpx.WriteOpenAIError(w, http.StatusBadRequest,
					"invalid_request_error", "idempotency_key_unsupported_route",
					"Idempotency-Key is supported only on POST /v1/chat/completions.")
				return
			}

			ac, ok := auth.FromContext(r.Context())
			if !ok || ac.TenantID == "" {
				// Auth middleware should have stopped us; defensive 401.
				httpx.WriteOpenAIError(w, http.StatusUnauthorized,
					"authentication_error", "no_api_key",
					"Idempotency-Key requires an authenticated tenant.")
				return
			}

			// Read request body up to the cap (proxies and audit read too;
			// we buffer once here and reinject).
			body, err := io.ReadAll(io.LimitReader(r.Body, MaxBodySize))
			if err != nil {
				httpx.WriteOpenAIError(w, http.StatusBadRequest,
					"invalid_request_error", "body_read_error",
					"Could not read request body.")
				return
			}
			// Detect stream flag before hashing so we can reject early
			// without a Redis touch (D-C4).
			if isStreamingBody(body) {
				httpx.WriteOpenAIError(w, http.StatusBadRequest,
					"invalid_request_error", "idempotency_key_unsupported_stream",
					"Idempotency-Key is not supported on streaming requests.")
				return
			}

			hash, err := HashBody(body)
			if err != nil {
				httpx.WriteOpenAIError(w, http.StatusBadRequest,
					"invalid_request_error", "invalid_json_body",
					"Request body is not valid JSON.")
				return
			}

			ctx := r.Context()

			// 1. Observe current slot state (Codex review [MEDIUM] 02-06 —
			// distinguishes completed vs in-flight).
			slot, err := store.Get(ctx, ac.TenantID, idemKey)
			if err != nil {
				log.WarnContext(ctx, "idem get failed — proceeding to upstream", "err", err)
				// Degrade to direct-through: skip serialization, still run handler.
				slot = Slot{Kind: SlotEmpty}
			}

			switch slot.Kind {
			case SlotCompleted:
				if slot.Entry.RequestHash != hash {
					httpx.WriteOpenAIError(w, http.StatusUnprocessableEntity,
						"idempotency_conflict", "idempotency_key_reused_with_different_body",
						"Idempotency-Key conflict: body differs from original request.")
					return
				}
				if setter, ok := w.(audit.IdempotencyReplayedSetter); ok {
					setter.SetIdempotencyReplayed(true)
				}
				replay(w, slot.Entry)
				return

			case SlotInFlight:
				// Another request is actively computing the response.
				if slot.SentinelHash != "" && slot.SentinelHash != hash {
					httpx.WriteOpenAIError(w, http.StatusUnprocessableEntity,
						"idempotency_conflict", "idempotency_key_reused_with_different_body",
						"Idempotency-Key conflict: body differs from in-flight original request.")
					return
				}
				// Wait for the winner up to waitPollBudget (30s).
				entry, werr := store.WaitForComplete(ctx, ac.TenantID, idemKey, hash)
				if werr == nil && entry.Status != 0 {
					if setter, ok := w.(audit.IdempotencyReplayedSetter); ok {
						setter.SetIdempotencyReplayed(true)
					}
					replay(w, entry)
					return
				}
				if errors.Is(werr, ErrConflict) {
					httpx.WriteOpenAIError(w, http.StatusUnprocessableEntity,
						"idempotency_conflict", "idempotency_key_reused_with_different_body",
						"Idempotency-Key conflict: body differs from in-flight original request.")
					return
				}
				if errors.Is(werr, ErrInFlightTimeout) {
					w.Header().Set("Retry-After", "5")
					httpx.WriteOpenAIError(w, http.StatusConflict,
						"idempotency_in_flight", "idempotency_key_in_progress",
						"Another request with this Idempotency-Key is still being processed. Retry after 5 seconds.")
					return
				}
				// Sentinel expired / winner aborted — fall through to 'run as fresh'.
				log.InfoContext(ctx, "idem winner aborted or expired; retrying as fresh", "key", idemKey)
			}

			// 2. Cache MISS or SlotEmpty — acquire the IN_FLIGHT sentinel.
			winnerReqID := httpx.RequestIDFrom(ctx)
			acquired, err := store.AcquireInFlight(ctx, ac.TenantID, idemKey, winnerReqID, hash)
			if err != nil {
				log.WarnContext(ctx, "idem AcquireInFlight failed — proceeding without serialization", "err", err)
				acquired = true // degrade: run upstream anyway, no replay registration
			}
			if !acquired {
				// Lost the race between Get and AcquireInFlight — re-enter wait-poll.
				entry, werr := store.WaitForComplete(ctx, ac.TenantID, idemKey, hash)
				if werr == nil && entry.Status != 0 {
					if setter, ok := w.(audit.IdempotencyReplayedSetter); ok {
						setter.SetIdempotencyReplayed(true)
					}
					replay(w, entry)
					return
				}
				if errors.Is(werr, ErrConflict) {
					httpx.WriteOpenAIError(w, http.StatusUnprocessableEntity,
						"idempotency_conflict", "idempotency_key_reused_with_different_body",
						"Idempotency-Key conflict.")
					return
				}
				w.Header().Set("Retry-After", "5")
				httpx.WriteOpenAIError(w, http.StatusConflict,
					"idempotency_in_flight", "idempotency_key_in_progress",
					"Another request with this Idempotency-Key is still being processed.")
				return
			}

			// 3. WINNER path: we hold the sentinel. Run the handler; capture; Complete or Abort.
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
			cw := &captureWriter{ResponseWriter: w, buf: &bytes.Buffer{}}
			next.ServeHTTP(cw, r)

			if !cacheable(cw.status) {
				// 5xx: DEL the sentinel so a retry of the same key can proceed
				// without waiting 30s. Don't cache transient upstream failures.
				abortCtx, abortCancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer abortCancel()
				if err := store.Abort(abortCtx, ac.TenantID, idemKey); err != nil {
					log.WarnContext(ctx, "idem Abort failed", "err", err)
				}
				return
			}

			headers := map[string]string{}
			for _, h := range HeaderWhitelist {
				if v := w.Header().Get(h); v != "" {
					headers[h] = v
				}
			}
			entry := Entry{
				Status:      cw.status,
				Headers:     headers,
				Body:        cw.buf.Bytes(),
				RequestHash: hash,
				StoredAt:    time.Now(),
			}
			completeCtx, completeCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer completeCancel()
			if err := store.Complete(completeCtx, ac.TenantID, idemKey, entry); err != nil {
				log.WarnContext(ctx, "idem Complete failed", "err", err)
				// Fall back: try Abort so losers can retry as fresh.
				_ = store.Abort(completeCtx, ac.TenantID, idemKey)
			}
		})
	}
}

// isStreamingBody looks for `"stream":true` at the top level of the JSON body.
// A shallow parse is intentional — a top-level false or absent field passes.
func isStreamingBody(body []byte) bool {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return false
	}
	v, ok := m["stream"]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	return ok && b
}

// replay writes the cached entry to w with the X-Idempotency-Replayed flag.
func replay(w http.ResponseWriter, e Entry) {
	for k, v := range e.Headers {
		w.Header().Set(k, v)
	}
	w.Header().Set("X-Idempotency-Replayed", "true")
	w.WriteHeader(e.Status)
	_, _ = w.Write(e.Body)
}

// captureWriter records status and body so we can re-serve them. It passes
// writes through to the underlying ResponseWriter normally so the client
// sees the response streaming too (replay is only for the NEXT identical
// request).
type captureWriter struct {
	http.ResponseWriter
	buf    *bytes.Buffer
	status int
	wrote  bool
}

func (c *captureWriter) WriteHeader(code int) {
	c.status = code
	c.ResponseWriter.WriteHeader(code)
}

func (c *captureWriter) Write(b []byte) (int, error) {
	if !c.wrote && c.status == 0 {
		c.status = http.StatusOK
	}
	c.buf.Write(b)
	c.wrote = true
	return c.ResponseWriter.Write(b)
}

// Flush passes through for SSE-style flush, though streaming with
// Idempotency-Key is rejected upstream of this writer anyway.
func (c *captureWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Compile-time assertion: captureWriter implements IdempotencyReplayedSetter
// indirectly via embedded ResponseWriter — the outer audit writer does.
// We don't implement the interface on captureWriter itself; the setter
// assertion in Middleware targets the original w (which audit.Middleware
// wrapped as *audit.auditResponseWriter).
var _ http.ResponseWriter = (*captureWriter)(nil)
