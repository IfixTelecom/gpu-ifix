// Package obs (middleware.go): request instrumentation middleware that
// finally wires obs.RequestsTotal{route,status} — a folded TODO left by
// Phase 2 (collector registered but never incremented) per STATE.md +
// CONTEXT.md D-D1 "metricsMiddleware (folded TODO)".
//
// Design:
//   - route is chi's RoutePattern (e.g. "/v1/chat/completions"), NOT the
//     raw URL path, so cardinality stays bounded per CONTEXT.md Plumbing.
//     If the middleware runs outside a chi route (e.g. unmatched path)
//     we fall back to the raw path; the /metrics dashboard treats
//     unmatched routes as an alert worth investigating.
//   - status is the status CLASS ("2xx"/"3xx"/"4xx"/"5xx") so the label
//     space stays at O(4) per route (Pitfall 13).
//   - Mounts last in the chain so it observes the final status code
//     (after auth/rate-limit/quota/dispatcher potentially wrote 4xx/5xx).
package obs

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// statusRecorder captures the response status for post-hoc label lookup.
// Mirrors the pattern used by httpx.Logger. Preserves all underlying
// interfaces by using http.ResponseController in tests — this simple
// wrapper is sufficient for status-only capture.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

// WriteHeader records the status then forwards.
func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

// Write ensures a default 200 is recorded even if the handler writes the
// body without calling WriteHeader first (the stdlib behavior).
func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// Flush is forwarded so SSE streams keep their per-chunk flush behavior.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// RequestsMiddleware instruments every request with RequestsTotal{route,
// status_class} AFTER the response has been written. Accepts the logger
// as a debug hook; the middleware itself is log-silent in the hot path.
func RequestsMiddleware(_ *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)

			// Label lookup: prefer the chi route pattern (bounded cardinality).
			// Falls back to raw path if the middleware runs outside chi.
			route := ""
			if rctx := chi.RouteContext(r.Context()); rctx != nil {
				route = rctx.RoutePattern()
			}
			if route == "" {
				route = r.URL.Path
			}
			if rec.status == 0 {
				rec.status = http.StatusOK
			}
			statusClass := strconv.Itoa(rec.status/100) + "xx"
			RequestsTotal.WithLabelValues(route, statusClass).Inc()
		})
	}
}
