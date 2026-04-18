// Package httpx implements HTTP middleware and helpers shared by the
// gateway server and its handlers. Request-ID propagation, the slog
// redactor, and the OpenAI error envelope helper live here. No knowledge
// of auth, audit, proxy, or database lives in this package — those are
// wired on top by their respective packages.
package httpx

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
)

type ctxKey int

const (
	requestIDKey ctxKey = iota
	clientRequestIDKey
	loggerKey
)

// RequestID injects a UUIDv7 as the authoritative gateway request ID and
// threads it through the context. Response header X-Request-ID always
// carries OUR id — if the client sent X-Request-ID we keep it in ctx as
// client_request_id but never use it as the audit key. Rationale: clients
// cannot forge our audit-log primary keys (CONTEXT.md Plumbing).
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid, err := uuid.NewV7()
		if err != nil {
			rid = uuid.New() // uuid.NewV7 effectively never fails; defensive fallback
		}
		ridStr := rid.String()
		w.Header().Set("X-Request-ID", ridStr)

		ctx := context.WithValue(r.Context(), requestIDKey, ridStr)
		if client := r.Header.Get("X-Request-ID"); client != "" {
			if _, perr := uuid.Parse(client); perr == nil {
				ctx = context.WithValue(ctx, clientRequestIDKey, client)
			}
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequestIDFrom returns the gateway-generated UUIDv7 request ID or "".
func RequestIDFrom(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}

// ClientRequestIDFrom returns the client-supplied X-Request-ID if it was
// a valid UUID, else "".
func ClientRequestIDFrom(ctx context.Context) string {
	if v, ok := ctx.Value(clientRequestIDKey).(string); ok {
		return v
	}
	return ""
}

// WithLogger stashes a slog.Logger derived with per-request attributes in ctx.
func WithLogger(ctx context.Context, log *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey, log)
}

// LoggerFrom returns the per-request logger or the default logger if none set.
func LoggerFrom(ctx context.Context) *slog.Logger {
	if v, ok := ctx.Value(loggerKey).(*slog.Logger); ok {
		return v
	}
	return slog.Default()
}
