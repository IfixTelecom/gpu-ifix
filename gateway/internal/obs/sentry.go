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
// redacts sensitive request headers so they never fly to Sentry even on
// panic (D-B7 duplicar a proteção).
func Init(cfg config.Config) error {
	if cfg.SentryDSN == "" {
		return nil
	}
	return sentry.Init(sentry.ClientOptions{
		Dsn:              cfg.SentryDSN,
		Environment:      cfg.Env,
		Release:          BuildVersion,
		TracesSampleRate: 0.0, // Phase 2 minimal obs budget (CONTEXT.md Plumbing)
		BeforeSend: func(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
			if event.Request != nil {
				for k := range event.Request.Headers {
					if httpx.IsSensitiveKey(k) {
						event.Request.Headers[k] = "***REDACTED***"
					}
				}
				// Also strip Cookies field (bespoke field on sentry.Request).
				event.Request.Cookies = ""
			}
			return event
		},
	})
}

// Flush waits up to timeout for pending Sentry events to send.
func Flush(timeout time.Duration) { sentry.Flush(timeout) }
