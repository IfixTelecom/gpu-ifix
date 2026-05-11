// Package dcgm (scraper_test.go): unit tests for the HTTP scraper that
// pulls DCGM_FI_DEV_FB_USED from the pod's :9400/metrics endpoint.
//
// Coverage budget (CONTEXT.md D-A3 / 05-04-PLAN must_haves):
//
//   - success path populates ReadMiB (12345 sample value)
//   - 503 status increments consecutiveFail but does NOT trip fail-open yet
//   - 3 consecutive failures flip vramUnknown=true (fail-open)
//   - recovery from fail-open: one good scrape zeroes consecutiveFail
//     and clears vramUnknown
//   - parse error on garbled body
//   - missing DCGM_FI_DEV_FB_USED metric in the body
//   - sanity check rejects impossible values (> 1_000_000 MiB)
//   - Run(ctx) returns within 1s of ctx cancel
//   - nil receiver ReadMiB returns (0, true) — defensive boot path
//     when DCGM_EXPORTER_URL is empty and main.go elects not to
//     construct a Scraper
package dcgm

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

const validMetricsBody = `# HELP DCGM_FI_DEV_FB_USED Framebuffer memory used (in MiB).
# TYPE DCGM_FI_DEV_FB_USED gauge
DCGM_FI_DEV_FB_USED{gpu="0",UUID="GPU-test"} 12345
# HELP DCGM_FI_DEV_FB_FREE Framebuffer memory free (in MiB).
# TYPE DCGM_FI_DEV_FB_FREE gauge
DCGM_FI_DEV_FB_FREE{gpu="0",UUID="GPU-test"} 12000
`

func TestScraper_SuccessPopulatesReadMiB(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte(validMetricsBody))
	}))
	defer srv.Close()

	s := New(srv.URL, 100*time.Millisecond, 1*time.Second, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.scrape(ctx)
	val, unknown := s.ReadMiB()
	if unknown {
		t.Fatal("unknown should be false after successful scrape")
	}
	if val != 12345 {
		t.Fatalf("expected 12345, got %d", val)
	}
}

func TestScraper_Status503FailsButNotYetOpen(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	s := New(srv.URL, 100*time.Millisecond, 500*time.Millisecond, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.scrape(ctx)
	_, unknown := s.ReadMiB()
	if unknown {
		t.Fatal("1 failure should not trigger fail-open yet")
	}
	if got := s.consecutiveFail.Load(); got != 1 {
		t.Fatalf("consecutiveFail=%d want 1", got)
	}
}

func TestScraper_FailOpenAfterThreeConsecutiveFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	s := New(srv.URL, 100*time.Millisecond, 500*time.Millisecond, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for i := 0; i < 3; i++ {
		s.scrape(ctx)
	}
	_, unknown := s.ReadMiB()
	if !unknown {
		t.Fatal("3 consecutive failures should flip vramUnknown=true")
	}
}

func TestScraper_RecoverResetsCountersAndUnknown(t *testing.T) {
	var mode atomic.Int32 // 0=fail, 1=ok
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if mode.Load() == 0 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte(validMetricsBody))
	}))
	defer srv.Close()
	s := New(srv.URL, 100*time.Millisecond, 500*time.Millisecond, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for i := 0; i < 3; i++ {
		s.scrape(ctx)
	}
	_, unknown := s.ReadMiB()
	if !unknown {
		t.Fatal("precondition: expected unknown after 3 failures")
	}
	mode.Store(1)
	s.scrape(ctx)
	val, unknown2 := s.ReadMiB()
	if unknown2 {
		t.Fatal("recovery should flip unknown back to false")
	}
	if val != 12345 {
		t.Fatalf("recovery expected val=12345, got %d", val)
	}
	if got := s.consecutiveFail.Load(); got != 0 {
		t.Fatalf("consecutiveFail should reset to 0, got %d", got)
	}
}

func TestScraper_ParseErrorFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		// Malformed prometheus text: stray '{' with no metric name in valid form.
		_, _ = w.Write([]byte("this is { not prometheus text format\n"))
	}))
	defer srv.Close()
	s := New(srv.URL, 100*time.Millisecond, 500*time.Millisecond, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.scrape(ctx)
	if got := s.consecutiveFail.Load(); got < 1 {
		t.Fatalf("parse error should increment consecutiveFail, got %d", got)
	}
}

func TestScraper_MetricMissingFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte(`# HELP other Unrelated metric.
# TYPE other gauge
other 42
`))
	}))
	defer srv.Close()
	s := New(srv.URL, 100*time.Millisecond, 500*time.Millisecond, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.scrape(ctx)
	if got := s.consecutiveFail.Load(); got < 1 {
		t.Fatalf("missing DCGM_FI_DEV_FB_USED should fail, consecutiveFail=%d", got)
	}
}

func TestScraper_SanityCheckRejectsImpossibleValues(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte(`# HELP DCGM_FI_DEV_FB_USED Framebuffer memory used (in MiB).
# TYPE DCGM_FI_DEV_FB_USED gauge
DCGM_FI_DEV_FB_USED 9999999999
`))
	}))
	defer srv.Close()
	s := New(srv.URL, 100*time.Millisecond, 500*time.Millisecond, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.scrape(ctx)
	if got := s.consecutiveFail.Load(); got < 1 {
		t.Fatalf("out-of-range value should fail sanity check, consecutiveFail=%d", got)
	}
}

func TestScraper_RunStopsOnContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte(validMetricsBody))
	}))
	defer srv.Close()
	s := New(srv.URL, 50*time.Millisecond, 500*time.Millisecond, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.Run(ctx)
		close(done)
	}()
	time.Sleep(150 * time.Millisecond) // allow ~2-3 ticks
	cancel()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("Run did not return within 1s of ctx cancel")
	}
	val, _ := s.ReadMiB()
	if val != 12345 {
		t.Fatalf("expected cached 12345 after graceful stop, got %d", val)
	}
}

func TestScraper_NilReceiverReadMiBReturnsUnknown(t *testing.T) {
	var s *Scraper
	val, unknown := s.ReadMiB()
	if !unknown {
		t.Fatal("nil receiver should return unknown=true")
	}
	if val != 0 {
		t.Fatalf("nil receiver expected val=0, got %d", val)
	}
}
