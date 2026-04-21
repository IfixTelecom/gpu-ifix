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
