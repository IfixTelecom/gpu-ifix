// Package obs (observability) wires Sentry initialization and Prometheus
// counters for the gateway. All observability concerns live here; HTTP
// middleware (request-id, logging, recovery) is in gateway/internal/httpx.
package obs

import (
	"time"

	sentry "github.com/getsentry/sentry-go"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
)

// Init brings up the Sentry client. When cfg.SentryDSN is empty the call
// is a no-op (returns nil) — Sentry is opt-in per env var. BeforeSend
// (the beforeSend function below) redacts sensitive request headers,
// request/response bodies, and body payloads stuffed into Extra so they
// never fly to Sentry even on panic (D-B7 duplicar a proteção; OBS-08).
func Init(cfg config.Config) error {
	if cfg.SentryDSN == "" {
		return nil
	}
	return sentry.Init(sentry.ClientOptions{
		Dsn:              cfg.SentryDSN,
		Environment:      cfg.Env,
		Release:          BuildVersion,
		TracesSampleRate: 0.0, // Phase 2 minimal obs budget (CONTEXT.md Plumbing)
		BeforeSend:       beforeSend,
	})
}

// beforeSend is the Sentry scrub hook. Extracted as a package-level
// function (not an inline closure) so it is unit-testable without a live
// DSN — sentry_test.go exercises it directly.
//
// It scrubs three trust-boundary leak vectors before an event leaves the
// process (T-07-04):
//   - sensitive request headers → "***REDACTED***" (via httpx.IsSensitiveKey,
//     the single shared sensitive-key list — not duplicated here)
//   - the bespoke Cookies field → cleared
//   - the request body (event.Request.Data — may carry prompts / api keys
//     on a panic that captured a chat request) → "***REDACTED***"
//   - request_body / response_body payloads any handler stuffed into
//     event.Extra → deleted
//
// Always returns the event (never nil) — scrubbing must not suppress the
// error report, only sanitize it.
func beforeSend(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
	if event.Request != nil {
		for k := range event.Request.Headers {
			if httpx.IsSensitiveKey(k) {
				event.Request.Headers[k] = "***REDACTED***"
			}
		}
		// Also strip Cookies field (bespoke field on sentry.Request).
		event.Request.Cookies = ""
		// OBS-08: the request body may contain prompts / api keys — drop
		// it entirely. Safest redaction; the dashboard never needs the
		// raw body, and audit_log_content is the authoritative store.
		if event.Request.Data != "" {
			event.Request.Data = "***REDACTED***"
		}
	}
	// OBS-08: scrub body payloads any handler stuffed into Extra.
	// delete on a nil map is a safe no-op.
	delete(event.Extra, "request_body")
	delete(event.Extra, "response_body")
	return event
}

// Flush waits up to timeout for pending Sentry events to send.
func Flush(timeout time.Duration) { sentry.Flush(timeout) }
