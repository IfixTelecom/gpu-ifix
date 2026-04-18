// Package httpx (recoverer.go): panic-recovery middleware that writes an
// OpenAI envelope 500 and sends the panic to Sentry (if initialized).
package httpx

import (
	"log/slog"
	"net/http"
	"time"

	sentry "github.com/getsentry/sentry-go"
)

// Recoverer catches panics in downstream handlers, reports to Sentry (if
// initialized), writes an OpenAI envelope 500, and keeps the server alive.
func Recoverer(base *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					base.ErrorContext(r.Context(), "panic recovered",
						"panic", rec,
						"request_id", RequestIDFrom(r.Context()),
					)
					sentry.CurrentHub().Recover(rec)
					sentry.Flush(500 * time.Millisecond)
					WriteOpenAIError(w, http.StatusInternalServerError,
						"api_error", "internal_error",
						"The gateway encountered an unexpected error.")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
