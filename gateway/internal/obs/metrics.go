// Package obs (metrics.go): Prometheus collectors exposed by /metrics.
// Phase 2 budget is two counters (per CONTEXT.md Plumbing); Phase 7 adds
// latency histograms + per-tenant labels. Keep cardinality bounded.
package obs

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// RequestsTotal counts all requests to gateway routes, labelled by
// route template (not raw path — bounded cardinality per CONTEXT.md
// Plumbing). Phase 7 adds latency histograms.
var RequestsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "gateway_requests_total",
		Help: "Total requests received by the gateway, by route template and HTTP status class.",
	},
	[]string{"route", "status"},
)

// AuditDroppedTotal counts audit events dropped because the writer
// buffer was full (D-B4 fail-safe). Non-zero value indicates backpressure.
var AuditDroppedTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "gateway_audit_dropped_total",
		Help: "Audit events dropped because the writer buffer was full.",
	},
)

// ApikeyTouchBufferedTotal counts Verify-path enqueues into TouchBuffer.
// Codex review [MEDIUM] 02-03 — debounced last_used_at updates.
var ApikeyTouchBufferedTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "gateway_apikey_touch_buffered_total",
		Help: "Total api_key touch enqueues into the debounced buffer.",
	},
)

// ApikeyTouchFlushTotal counts flush cycles (not individual UPDATEs).
// Codex review [MEDIUM] 02-03.
var ApikeyTouchFlushTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "gateway_apikey_touch_flush_total",
		Help: "Total flush cycles of the debounced api_key touch buffer.",
	},
)

// UpstreamsReloadTotal counts upstreams.Loader.Refresh invocations,
// labelled by outcome ("ok" | "error"). Phase 3 D-D2 — incremented at
// boot Refresh and on each LISTEN/NOTIFY-driven reload. Helps operators
// detect reload storms (Pitfall 7) or persistent DB read failures.
var UpstreamsReloadTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "gateway_upstreams_reload_total",
		Help: "Total upstreams loader Refresh invocations by outcome.",
	},
	[]string{"result"},
)

// Handler returns the /metrics endpoint handler.
func Handler() http.Handler { return promhttp.Handler() }
