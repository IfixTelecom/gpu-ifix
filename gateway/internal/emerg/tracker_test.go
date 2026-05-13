// Package emerg (tracker_test.go): Plan 06-05 Task 1 unit tests for
// localLlmTracker — covers idempotent OPEN, OPEN→CLOSED reset, foreign
// upstream filtering, and SustainedFailedOverSeconds arithmetic.
package emerg

import (
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

// TestTracker_OpenSince — Apply OPEN sets openSince > 0; a SECOND OPEN
// event MUST NOT reset openSince (idempotent on duplicate publish). State
// converges to "open".
func TestTracker_OpenSince(t *testing.T) {
	tr := newLocalLlmTracker()
	if tr.State() != "closed" {
		t.Fatalf("initial state = %q, want closed", tr.State())
	}

	tr.ApplyEvent(redisx.BreakerEvent{Upstream: "local-llm", State: "open"})
	first := tr.openSince.Load()
	if first == 0 {
		t.Fatalf("openSince = 0 after OPEN event")
	}
	if got := tr.State(); got != "open" {
		t.Fatalf("State after OPEN = %q, want open", got)
	}

	// Force a non-zero gap so a buggy implementation that overwrote
	// openSince would surface.
	time.Sleep(1100 * time.Millisecond)
	tr.ApplyEvent(redisx.BreakerEvent{Upstream: "local-llm", State: "open"})
	second := tr.openSince.Load()
	if second != first {
		t.Fatalf("openSince mutated on duplicate OPEN: first=%d second=%d", first, second)
	}
}

// TestTracker_OpenToClose — after OPEN, a CLOSED event resets openSince
// to 0 and SustainedFailedOverSeconds returns 0.
func TestTracker_OpenToClose(t *testing.T) {
	tr := newLocalLlmTracker()
	tr.ApplyEvent(redisx.BreakerEvent{Upstream: "local-llm", State: "open"})
	if tr.openSince.Load() == 0 {
		t.Fatalf("openSince should be > 0 after OPEN")
	}
	tr.ApplyEvent(redisx.BreakerEvent{Upstream: "local-llm", State: "closed"})
	if tr.openSince.Load() != 0 {
		t.Fatalf("openSince = %d after CLOSED, want 0", tr.openSince.Load())
	}
	if got := tr.SustainedFailedOverSeconds(); got != 0 {
		t.Fatalf("SustainedFailedOverSeconds after CLOSED = %d, want 0", got)
	}
	if got := tr.State(); got != "closed" {
		t.Fatalf("State after CLOSED = %q, want closed", got)
	}
}

// TestTracker_HalfOpenResets — HALF_OPEN counts as recovery: openSince
// resets to 0 and SustainedFailedOverSeconds returns 0. Phase 3 emits
// half-open as the breaker probes, so a long sustained-OPEN that flips
// to half-open MUST disarm the trigger.
func TestTracker_HalfOpenResets(t *testing.T) {
	tr := newLocalLlmTracker()
	tr.ApplyEvent(redisx.BreakerEvent{Upstream: "local-llm", State: "open"})
	tr.ApplyEvent(redisx.BreakerEvent{Upstream: "local-llm", State: "half-open"})
	if tr.openSince.Load() != 0 {
		t.Fatalf("openSince = %d after half-open, want 0", tr.openSince.Load())
	}
	if got := tr.SustainedFailedOverSeconds(); got != 0 {
		t.Fatalf("SustainedFailedOverSeconds after half-open = %d, want 0", got)
	}
}

// TestTracker_IgnoresOtherUpstreams — events for local-stt, local-embed,
// openrouter-* must NOT mutate the tracker. D-C2: only local-llm chat is
// the trigger signal.
func TestTracker_IgnoresOtherUpstreams(t *testing.T) {
	tr := newLocalLlmTracker()
	for _, up := range []string{"local-stt", "local-embed", "openrouter-chat", "openrouter-stt"} {
		tr.ApplyEvent(redisx.BreakerEvent{Upstream: up, State: "open"})
		if got := tr.State(); got != "closed" {
			t.Fatalf("state mutated by upstream=%q: got %q, want closed", up, got)
		}
		if got := tr.openSince.Load(); got != 0 {
			t.Fatalf("openSince mutated by upstream=%q: got %d, want 0", up, got)
		}
		if got := tr.SustainedFailedOverSeconds(); got != 0 {
			t.Fatalf("SustainedFailedOverSeconds mutated by upstream=%q: got %d, want 0", up, got)
		}
	}
}

// TestTracker_SustainedFailedOver — when openSince is set far in the
// past, SustainedFailedOverSeconds returns the elapsed delta. Drives the
// reconciler trigger arithmetic.
func TestTracker_SustainedFailedOver(t *testing.T) {
	tr := newLocalLlmTracker()
	tr.state.Store("open")
	// 150 seconds ago.
	tr.openSince.Store(time.Now().Unix() - 150)
	got := tr.SustainedFailedOverSeconds()
	if got < 150 || got > 152 {
		t.Fatalf("SustainedFailedOverSeconds = %d, want 150-152", got)
	}
}

// TestTracker_NoSinceWhenClosed — defensively, even if state="closed"
// somehow co-exists with openSince > 0, SustainedFailedOverSeconds
// returns 0. Prevents a stale openSince from leaking into the trigger
// gate.
func TestTracker_NoSinceWhenClosed(t *testing.T) {
	tr := newLocalLlmTracker()
	tr.state.Store("closed")
	tr.openSince.Store(time.Now().Unix() - 999)
	if got := tr.SustainedFailedOverSeconds(); got != 0 {
		t.Fatalf("SustainedFailedOverSeconds with state=closed = %d, want 0", got)
	}
}
