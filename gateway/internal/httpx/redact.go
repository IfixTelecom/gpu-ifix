// Package httpx (redact.go): slog handler wrapper that redacts sensitive
// attribute values before they reach the underlying encoder. Shared with
// the Sentry BeforeSend hook in gateway/internal/obs so redaction is
// duplicated at every observability exit (D-B7).
package httpx

import (
	"context"
	"log/slog"
	"strings"
)

// sensitiveKeys enumerates attribute keys whose VALUES should never be
// emitted into logs or Sentry events. Match is case-insensitive. Shared
// with obs.Sentry BeforeSend (D-B7 duplicar a proteção).
var sensitiveKeys = map[string]struct{}{
	"authorization":       {},
	"x-api-key":           {},
	"cookie":              {},
	"proxy-authorization": {},
	"api_key":             {},
	"apikey":              {},
}

// IsSensitiveKey reports whether k matches the sensitive-keys list (case-insensitive).
func IsSensitiveKey(k string) bool {
	_, ok := sensitiveKeys[strings.ToLower(k)]
	return ok
}

// Redactor wraps a slog.Handler and replaces sensitive attribute VALUES
// with "***REDACTED***" before the inner handler sees them. Applied to
// EVERY record (not just errors) per D-B7.
type Redactor struct{ inner slog.Handler }

// NewRedactor returns a handler that redacts sensitive keys before writing.
func NewRedactor(inner slog.Handler) slog.Handler {
	return &Redactor{inner: inner}
}

// Enabled defers to the inner handler.
func (r *Redactor) Enabled(ctx context.Context, lvl slog.Level) bool {
	return r.inner.Enabled(ctx, lvl)
}

// Handle rebuilds the record's attrs with redaction applied.
func (r *Redactor) Handle(ctx context.Context, rec slog.Record) error {
	clone := slog.NewRecord(rec.Time, rec.Level, rec.Message, rec.PC)
	rec.Attrs(func(a slog.Attr) bool {
		clone.AddAttrs(redactAttr(a))
		return true
	})
	return r.inner.Handle(ctx, clone)
}

// WithAttrs redacts pre-bound attrs before delegating.
func (r *Redactor) WithAttrs(attrs []slog.Attr) slog.Handler {
	red := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		red[i] = redactAttr(a)
	}
	return &Redactor{inner: r.inner.WithAttrs(red)}
}

// WithGroup delegates unchanged (group names are never sensitive).
func (r *Redactor) WithGroup(name string) slog.Handler {
	return &Redactor{inner: r.inner.WithGroup(name)}
}

func redactAttr(a slog.Attr) slog.Attr {
	if IsSensitiveKey(a.Key) {
		return slog.String(a.Key, "***REDACTED***")
	}
	return a
}
