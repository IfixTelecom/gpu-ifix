// Package httpx (logger.go): per-request slog enrichment middleware.
// Builds a logger with request_id, method, path, optional client_request_id
// and stores it in ctx via WithLogger. Emits a single "request" Info record
// when the handler returns, with status + bytes + latency.
package httpx

import (
	"log/slog"
	"net/http"
	"time"
)

// Logger is middleware that binds a per-request logger (with module,
// request_id, client_request_id, method, path) into ctx and logs one
// summary record after the handler returns. The logger is wrapped in
// NewRedactor() upstream so sensitive attr VALUES are always redacted.
func Logger(base *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			reqID := RequestIDFrom(r.Context())
			cliID := ClientRequestIDFrom(r.Context())
			attrs := []any{
				"request_id", reqID,
				"method", r.Method,
				"path", r.URL.Path,
			}
			if cliID != "" {
				attrs = append(attrs, "client_request_id", cliID)
			}
			reqLog := base.With(attrs...)
			ctx := WithLogger(r.Context(), reqLog)

			sw := &statusWriter{ResponseWriter: w, status: 200}
			next.ServeHTTP(sw, r.WithContext(ctx))

			reqLog.Info("request",
				"status", sw.status,
				"bytes", sw.bytes,
				"latency_ms", time.Since(start).Milliseconds(),
			)
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(code int) { w.status = code; w.ResponseWriter.WriteHeader(code) }

func (w *statusWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

// Flush passes through for SSE (reverse proxy relies on this when
// FlushInterval:-1 is configured).
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
