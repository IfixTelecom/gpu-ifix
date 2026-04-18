// Package httpx_test exercises RequestID, logger ctx, and redactor wiring.
package httpx_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
)

func TestRequestID_SetsHeader(t *testing.T) {
	var got string
	h := httpx.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = httpx.RequestIDFrom(r.Context())
	}))
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	hdr := resp.Header.Get("X-Request-ID")
	if hdr == "" {
		t.Fatalf("expected X-Request-ID header, got none")
	}
	if _, err := uuid.Parse(hdr); err != nil {
		t.Fatalf("expected UUID in header, got %q (%v)", hdr, err)
	}
	if got != hdr {
		t.Fatalf("ctx request id %q != header %q", got, hdr)
	}
}

func TestRequestID_ClientHeaderPreservedAsClient(t *testing.T) {
	clientID := "018fb10c-1b36-7000-8000-000000000000"
	var gatewayID, observedClient string
	h := httpx.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gatewayID = httpx.RequestIDFrom(r.Context())
		observedClient = httpx.ClientRequestIDFrom(r.Context())
	}))
	srv := httptest.NewServer(h)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.Header.Set("X-Request-ID", clientID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	if observedClient != clientID {
		t.Errorf("expected client id %q, got %q", clientID, observedClient)
	}
	if gatewayID == clientID {
		t.Errorf("gateway id must not equal client id (got both = %q)", gatewayID)
	}
	if hdr := resp.Header.Get("X-Request-ID"); hdr != gatewayID {
		t.Errorf("response header %q != gateway-generated id %q", hdr, gatewayID)
	}
}

func TestRequestID_MalformedClientHeaderIgnored(t *testing.T) {
	var observedClient string
	h := httpx.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observedClient = httpx.ClientRequestIDFrom(r.Context())
	}))
	srv := httptest.NewServer(h)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.Header.Set("X-Request-ID", "not-a-uuid")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	resp.Body.Close()

	if observedClient != "" {
		t.Errorf("expected empty client id for malformed header, got %q", observedClient)
	}
}

func TestRequestID_UUIDv7FormatSortable(t *testing.T) {
	// UUIDv7 has a time-ordered prefix, so two IDs generated with a sleep
	// between them should compare in creation order as strings.
	gen := func() string {
		var id string
		h := httpx.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id = httpx.RequestIDFrom(r.Context())
		}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		return id
	}
	first := gen()
	time.Sleep(5 * time.Millisecond)
	second := gen()
	if first == "" || second == "" {
		t.Fatalf("empty ids: %q %q", first, second)
	}
	if first >= second {
		t.Errorf("UUIDv7 expected to be sortable: first=%q second=%q", first, second)
	}
}

func TestLoggerFrom_Default(t *testing.T) {
	h := httpx.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log := httpx.LoggerFrom(r.Context())
		if log == nil {
			t.Fatalf("LoggerFrom returned nil")
		}
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
}

func TestWithLogger_Roundtrip(t *testing.T) {
	var observed *slog.Logger
	base := slog.Default().With("test", "yes")
	h := httpx.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := httpx.WithLogger(r.Context(), base)
		observed = httpx.LoggerFrom(ctx)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if observed != base {
		t.Fatalf("WithLogger/LoggerFrom did not roundtrip")
	}
}
