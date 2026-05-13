// Package emerg (tracker.go): Plan 06-05 in-memory `local-llm` breaker
// state tracker. Each replica maintains its OWN tracker via the
// gw:breaker:events Pub/Sub channel (Phase 3 D-D1) so the FSM trigger
// gate can answer "has the primary chat upstream been failed-over for
// PROVISION_TRIGGER_FAILED_OVER_SECONDS?" without reaching across the
// network on every reconciler tick.
//
// State is intentionally minimal: a 3-valued lockless string (closed |
// half-open | open) plus an atomic openSince timestamp (unix-seconds at
// the most recent CLOSED→OPEN transition; 0 when not in OPEN). Both
// are atomic so the reconciler tick (Run goroutine) and the subscribe
// loop (Subscribe goroutine) can read/write without a mutex.
//
// CONTEXT.md D-C2 (signal source = local-llm chat ONLY): non-`local-llm`
// upstreams (local-stt, local-embed, openrouter-*, etc.) are dropped on
// the floor in ApplyEvent. STT/embed degrade gracefully via Phase 3
// fallback; saturation (Phase 5 shedding ON) is NOT a trigger.
//
// Pitfall 3 mitigation (Pub/Sub at-most-once): a missed OPEN event does
// NOT permanently desync the tracker — the next state-change event
// (HALF_OPEN/CLOSED) resets openSince to 0. Reconciler bottoms out on
// the DB query for live lifecycles (D-C5 reconciler check) before
// firing the trigger, so a missed OPEN can at worst delay (not skip)
// emergency provisioning until the breaker republishes.
package emerg

import (
	"sync/atomic"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

// localLlmTracker mirrors the `local-llm` breaker state per-replica. The
// reconciler reads SustainedFailedOverSeconds() each tick to decide
// whether the trigger gate has elapsed. Constructed via
// newLocalLlmTracker — the zero-value State is "closed" so a fresh
// tracker behaves identically to a process that has just observed a
// CLOSED event.
type localLlmTracker struct {
	state     atomic.Value // string: "closed" | "half-open" | "open"
	openSince atomic.Int64 // unix-seconds at most-recent CLOSED→OPEN transition; 0 when state != "open"
}

// newLocalLlmTracker returns a tracker initialised at "closed" /
// openSince=0. Callers (NewReconciler) construct exactly one tracker per
// replica and share it between the Subscribe goroutine (writer) and the
// reconciler tick (reader) via the Reconciler struct.
func newLocalLlmTracker() *localLlmTracker {
	t := &localLlmTracker{}
	t.state.Store("closed")
	return t
}

// ApplyEvent updates the tracker from a Phase 3 BreakerEvent. Drops any
// event whose Upstream != "local-llm" (D-C2 — chat is the only signal
// source). For local-llm events:
//   - OPEN:           store state="open" + set openSince to now (only if
//                     openSince==0 — idempotent on duplicate OPEN events
//                     so an event resend does not reset the sustained
//                     timer).
//   - HALF_OPEN/CLOSED: store the new state + reset openSince=0 so
//                     SustainedFailedOverSeconds returns 0 immediately.
//
// All writes are atomic; safe to call from the Subscribe goroutine while
// the reconciler tick reads concurrently.
func (t *localLlmTracker) ApplyEvent(ev redisx.BreakerEvent) {
	if ev.Upstream != "local-llm" {
		return
	}
	t.state.Store(ev.State)
	if ev.State == "open" {
		// Idempotent on duplicate OPEN: only set openSince on the FIRST
		// transition into OPEN. A resend (Pitfall 3) must NOT reset the
		// sustained timer or the trigger would never fire on a flaky
		// pub/sub link.
		if t.openSince.Load() == 0 {
			t.openSince.Store(time.Now().Unix())
		}
		return
	}
	// HALF_OPEN, CLOSED, or any future state — reset the sustained timer.
	t.openSince.Store(0)
}

// SustainedFailedOverSeconds returns the number of whole seconds the
// tracker has been in the OPEN state. Returns 0 when state != "open" OR
// openSince==0 (defensive — should never both be inconsistent because
// ApplyEvent writes them as a pair, but the read order is not atomic
// across the two atomics so we double-check before subtracting).
//
// The reconciler compares this against
// cfg.ProvisionTriggerFailedOverSeconds each tick; when the result
// crosses the threshold AND no live lifecycle exists in the DB, the
// FSM advances Healthy → FailedOver → EmergencyProvisioning.
func (t *localLlmTracker) SustainedFailedOverSeconds() int64 {
	s, _ := t.state.Load().(string)
	if s != "open" {
		return 0
	}
	since := t.openSince.Load()
	if since == 0 {
		return 0
	}
	return time.Now().Unix() - since
}

// State returns the most-recently observed `local-llm` breaker state.
// Used by gatewayctl emerg-state for operator visibility and by tests
// to assert tracker convergence after a publish.
func (t *localLlmTracker) State() string {
	s, _ := t.state.Load().(string)
	return s
}
