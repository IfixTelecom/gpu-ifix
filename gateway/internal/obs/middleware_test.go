// Unit tests for the RequestsMiddleware (folded TODO Phase 4).
package obs_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auditctx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

func silentLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestRequestsMiddleware_IncrementsOn200 asserts the middleware bumps
// RequestsTotal{route, "2xx"} for a successful request routed through chi.
func TestRequestsMiddleware_IncrementsOn200(t *testing.T) {
	route := "/v1/health"
	before := metricValue(t, route, "2xx")

	r := chi.NewRouter()
	r.Use(obs.RequestsMiddleware(silentLog()))
	r.Get(route, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", route, nil)
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("handler status: want 200, got %d", rec.Code)
	}
	after := metricValue(t, route, "2xx")
	if after != before+1 {
		t.Fatalf("RequestsTotal{route=%s,status=2xx} want %g, got %g (before=%g)",
			route, before+1, after, before)
	}
}

// TestRequestsMiddleware_IncrementsOn4xx asserts the status class rollup
// works for handler-written 4xx responses.
func TestRequestsMiddleware_IncrementsOn4xx(t *testing.T) {
	route := "/v1/fourofour"
	before := metricValue(t, route, "4xx")

	r := chi.NewRouter()
	r.Use(obs.RequestsMiddleware(silentLog()))
	r.Get(route, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", route, nil)
	r.ServeHTTP(rec, req)

	after := metricValue(t, route, "4xx")
	if after != before+1 {
		t.Fatalf("RequestsTotal{route=%s,status=4xx} want %g, got %g",
			route, before+1, after)
	}
}

// TestRequestsMiddleware_FallsBackToRawPath asserts the middleware does not
// panic when mounted outside a chi router (RouteContext is nil) — the
// label falls back to r.URL.Path so /metrics still shows a data point for
// orphan requests.
func TestRequestsMiddleware_FallsBackToRawPath(t *testing.T) {
	path := "/outside-chi"
	before := metricValue(t, path, "2xx")

	h := obs.RequestsMiddleware(silentLog())(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", path, nil)
	h.ServeHTTP(rec, req)

	after := metricValue(t, path, "2xx")
	if after != before+1 {
		t.Fatalf("RequestsTotal{route=%s,status=2xx} want %g, got %g",
			path, before+1, after)
	}
}

// TestRequestsMiddleware_ObservesRouteHistogram asserts a request through
// the middleware bumps RequestDurationByRoute{route=...}_count by exactly 1,
// and that the route label uses the chi route TEMPLATE — never a raw path
// with embedded IDs (Pitfall 1 — bounded cardinality).
func TestRequestsMiddleware_ObservesRouteHistogram(t *testing.T) {
	route := "/v1/chat/completions"
	before := histCount(t, obs.RequestDurationByRoute, "route", route)

	r := chi.NewRouter()
	r.Use(obs.RequestsMiddleware(silentLog()))
	r.Post(route, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", route, nil)
	r.ServeHTTP(rec, req)

	after := histCount(t, obs.RequestDurationByRoute, "route", route)
	if after != before+1 {
		t.Fatalf("RequestDurationByRoute{route=%s}_count want %g, got %g (before=%g)",
			route, before+1, after, before)
	}
}

// TestRequestsMiddleware_ObservesUpstreamHistogram asserts the same request
// bumps RequestDurationByUpstream{upstream=...}_count by 1. With no
// dispatcher-stamped override the upstream falls back to the route-derived
// bounded default ("llm" for /v1/chat).
func TestRequestsMiddleware_ObservesUpstreamHistogram(t *testing.T) {
	upstream := "llm" // route-derived default for /v1/chat/*
	before := histCount(t, obs.RequestDurationByUpstream, "upstream", upstream)

	r := chi.NewRouter()
	r.Use(obs.RequestsMiddleware(silentLog()))
	r.Post("/v1/chat/completions", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.ServeHTTP(rec, req)

	after := histCount(t, obs.RequestDurationByUpstream, "upstream", upstream)
	if after != before+1 {
		t.Fatalf("RequestDurationByUpstream{upstream=%s}_count want %g, got %g (before=%g)",
			upstream, before+1, after, before)
	}
}

// TestRequestsMiddleware_UpstreamHonorsContextOverride asserts that when a
// handler stamps the factual upstream on the request context (the same
// auditctx key the audit middleware reads), the histogram records that
// bounded value rather than the route default — and never an unbounded one.
func TestRequestsMiddleware_UpstreamHonorsContextOverride(t *testing.T) {
	const stamped = "openrouter-chat"
	before := histCount(t, obs.RequestDurationByUpstream, "upstream", stamped)

	r := chi.NewRouter()
	r.Use(obs.RequestsMiddleware(silentLog()))
	r.Post("/v1/chat/completions", func(w http.ResponseWriter, req *http.Request) {
		// Mutate the request in place so the outer middleware (which reads
		// the same *http.Request post-ServeHTTP) sees the stamped value —
		// mirrors how the dispatcher stamps the factual upstream.
		*req = *req.WithContext(auditctx.WithBillingUpstream(req.Context(), stamped))
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.ServeHTTP(rec, req)

	after := histCount(t, obs.RequestDurationByUpstream, "upstream", stamped)
	if after != before+1 {
		t.Fatalf("RequestDurationByUpstream{upstream=%s}_count want %g, got %g (before=%g)",
			stamped, before+1, after, before)
	}
}

// histCount reads the _count of a HistogramVec series identified by a single
// label. Zero when the series has never been observed (HistogramVec
// auto-creates on first WithLabelValues).
func histCount(t *testing.T, hv *prometheus.HistogramVec, labelName, labelValue string) float64 {
	t.Helper()
	obs, err := hv.GetMetricWith(prometheus.Labels{labelName: labelValue})
	if err != nil {
		if strings.Contains(err.Error(), "inconsistent label") {
			t.Fatalf("label mismatch: %v (histogram labels changed?)", err)
		}
		t.Fatalf("unexpected error reading histogram: %v", err)
	}
	m := &dto.Metric{}
	if err := obs.(prometheus.Metric).Write(m); err != nil {
		t.Fatalf("writing histogram metric: %v", err)
	}
	if m.Histogram == nil {
		t.Fatalf("metric for %s=%s is not a histogram", labelName, labelValue)
	}
	return float64(m.Histogram.GetSampleCount())
}

// metricValue reads the current counter for (route, status_class). Zero
// when the combination has never been observed (CounterVec auto-creates).
func metricValue(t *testing.T, route, statusClass string) float64 {
	t.Helper()
	// Probe existence; Inc by 0 to avoid incrementing the counter just to
	// read it. testutil.ToFloat64 requires a concrete Counter.
	ctr, err := obs.RequestsTotal.GetMetricWith(prometheus.Labels{
		"route":  route,
		"status": statusClass,
	})
	if err != nil {
		// Unexpected; surface the label names so regressions in the
		// collector definition are obvious.
		if strings.Contains(err.Error(), "inconsistent label") {
			t.Fatalf("label mismatch: %v (RequestsTotal labels changed?)", err)
		}
		t.Fatalf("unexpected error reading RequestsTotal: %v", err)
	}
	return testutil.ToFloat64(ctr)
}
