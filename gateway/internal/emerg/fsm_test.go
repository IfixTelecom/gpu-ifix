// Package emerg (fsm_test.go): deterministic unit tests for the
// Phase 6 7-state emergency FSM (CONTEXT.md domain item 1, BLOCKER 3
// revision: OFF_HOURS + MAINTENANCE deferred).
//
// All tests drive Transition with an explicit time.Time so transitions
// are independent of wall-clock skew (mirrors Phase 5 shed/fsm_test.go
// approach). Race-detector friendly: every concurrent test launches its
// goroutines before mutating the FSM.
package emerg

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

// allStatesForTest is the canonical 7-state ordering for assertions.
// Kept in a helper so tests do not duplicate the state list (and so
// adding a state in production must touch the helper, surfacing test
// breakage immediately).
var allStatesForTest = []State{
	StateHealthy,
	StateDegraded,
	StateFailedOver,
	StateEmergencyProvisioning,
	StateEmergencyActive,
	StateRecovering,
	StateCooldown,
}

// resetEmergGauge zeroes every label of GatewayEmergencyState so a
// later assertion of "1 on the new state, 0 on others" is deterministic
// even if a previous test left labels set. Cheap — 7 Set(0) calls.
func resetEmergGauge(t *testing.T) {
	t.Helper()
	for _, s := range allStatesForTest {
		obs.GatewayEmergencyState.WithLabelValues(s.String()).Set(0)
	}
}

func TestFSMStateString(t *testing.T) {
	cases := map[State]string{
		StateHealthy:               "healthy",
		StateDegraded:              "degraded",
		StateFailedOver:            "failed_over",
		StateEmergencyProvisioning: "emergency_provisioning",
		StateEmergencyActive:       "emergency_active",
		StateRecovering:            "recovering",
		StateCooldown:              "cooldown",
		State(99):                  "unknown",
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Fatalf("State(%d).String() = %q, want %q", s, got, want)
		}
	}
}

func TestFSMParseState(t *testing.T) {
	// Round-trip: every valid state string must parse back to the same
	// State value. Used by leader-recovery resumeFSMFromEvents in Plan 07.
	for _, s := range allStatesForTest {
		got, err := ParseState(s.String())
		if err != nil {
			t.Fatalf("ParseState(%q) returned err=%v, want nil", s.String(), err)
		}
		if got != s {
			t.Fatalf("ParseState(%q) = %d, want %d", s.String(), got, s)
		}
	}
	// Invalid strings must error (so resumeFSMFromEvents can detect a
	// corrupted JSONB events payload and abort the resume).
	if _, err := ParseState("nonexistent_state"); err == nil {
		t.Fatalf("ParseState(\"nonexistent_state\") returned nil err, want non-nil")
	}
	if _, err := ParseState(""); err == nil {
		t.Fatalf("ParseState(\"\") returned nil err, want non-nil")
	}
}

func TestFSMInitialStateIsHealthy(t *testing.T) {
	resetEmergGauge(t)
	f := NewFSM(slog.Default(), nil)
	if f.State() != StateHealthy {
		t.Fatalf("initial state = %s, want healthy", f.State())
	}
	if f.EnteredAt().IsZero() {
		t.Fatalf("EnteredAt() should be non-zero at construction")
	}
}

func TestFSMTransitions(t *testing.T) {
	// Walk the canonical happy-path sequence:
	// Healthy → Degraded → FailedOver → EmergencyProvisioning →
	// EmergencyActive → Recovering → Cooldown.
	resetEmergGauge(t)
	f := NewFSM(slog.Default(), nil)

	steps := []struct {
		from, to State
		reason   string
	}{
		{StateHealthy, StateDegraded, "primary_breaker_open"},
		{StateDegraded, StateFailedOver, "tier1_serving"},
		{StateFailedOver, StateEmergencyProvisioning, "trigger_sustained"},
		{StateEmergencyProvisioning, StateEmergencyActive, "pod_health_passed"},
		{StateEmergencyActive, StateRecovering, "primary_recovered"},
		{StateRecovering, StateCooldown, "idle_grace_elapsed"},
	}

	now := time.Unix(1000, 0)
	for _, step := range steps {
		now = now.Add(1 * time.Second)
		f.Transition(step.from, step.to, now, step.reason)
		if f.State() != step.to {
			t.Fatalf("Transition(%s→%s): got %s", step.from, step.to, f.State())
		}
		if f.EnteredAt().Unix() != now.Unix() {
			t.Fatalf("Transition(%s→%s): EnteredAt=%d, want %d",
				step.from, step.to, f.EnteredAt().Unix(), now.Unix())
		}
	}
}

func TestFSMObsGaugeReset(t *testing.T) {
	// After a transition into StateDegraded, the gauge MUST be 1 on
	// "degraded" and 0 on every other state label.
	resetEmergGauge(t)
	f := NewFSM(slog.Default(), nil)
	f.Transition(StateHealthy, StateDegraded, time.Unix(1000, 0), "test")

	for _, s := range allStatesForTest {
		got := testutil.ToFloat64(obs.GatewayEmergencyState.WithLabelValues(s.String()))
		want := 0.0
		if s == StateDegraded {
			want = 1.0
		}
		if got != want {
			t.Fatalf("gauge[state=%s] = %v, want %v", s.String(), got, want)
		}
	}
}

func TestFSMOnChangeCallback(t *testing.T) {
	resetEmergGauge(t)

	type capture struct {
		from, to State
		reason   string
	}
	var (
		mu       sync.Mutex
		captured []capture
	)

	onChange := func(from, to State, reason string) {
		mu.Lock()
		defer mu.Unlock()
		captured = append(captured, capture{from, to, reason})
	}

	f := NewFSM(slog.Default(), onChange)
	f.Transition(StateHealthy, StateDegraded, time.Unix(1000, 0), "test_reason")
	f.Transition(StateDegraded, StateFailedOver, time.Unix(1001, 0), "another_reason")

	mu.Lock()
	defer mu.Unlock()
	if len(captured) != 2 {
		t.Fatalf("captured %d transitions, want 2: %+v", len(captured), captured)
	}
	if captured[0].from != StateHealthy || captured[0].to != StateDegraded || captured[0].reason != "test_reason" {
		t.Fatalf("captured[0] = %+v", captured[0])
	}
	if captured[1].from != StateDegraded || captured[1].to != StateFailedOver || captured[1].reason != "another_reason" {
		t.Fatalf("captured[1] = %+v", captured[1])
	}
}

func TestFSMInvalidTransition(t *testing.T) {
	// CAS guard: if the caller passes a from-state that does NOT match
	// the current state, transition is silently ignored (D-C1 semantics).
	resetEmergGauge(t)
	var callbackCount atomic.Int32
	onChange := func(from, to State, reason string) {
		callbackCount.Add(1)
	}
	f := NewFSM(slog.Default(), onChange)

	// Current state is StateHealthy (initial). Try to transition from
	// EmergencyActive — the CAS will fail.
	f.Transition(StateEmergencyActive, StateDegraded, time.Unix(1000, 0), "wrong_from")
	if f.State() != StateHealthy {
		t.Fatalf("state should remain Healthy after invalid transition; got %s", f.State())
	}
	if callbackCount.Load() != 0 {
		t.Fatalf("onChange should not fire on invalid transition; got %d calls", callbackCount.Load())
	}

	// Same-state transition (Healthy→Healthy) is also a noop.
	f.Transition(StateHealthy, StateHealthy, time.Unix(1001, 0), "noop")
	if callbackCount.Load() != 0 {
		t.Fatalf("onChange should not fire on same-state transition; got %d calls", callbackCount.Load())
	}
}

func TestFSMTransitionCAS(t *testing.T) {
	// Two goroutines race to do Healthy→Degraded. Exactly one CAS wins
	// → onChange fires exactly once. Final state = Degraded.
	resetEmergGauge(t)
	var callbackCount atomic.Int32
	onChange := func(from, to State, reason string) {
		callbackCount.Add(1)
	}
	f := NewFSM(slog.Default(), onChange)

	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	done.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer done.Done()
			start.Wait()
			f.Transition(StateHealthy, StateDegraded, time.Unix(1000, 0), "race")
		}()
	}
	start.Done()
	done.Wait()

	if f.State() != StateDegraded {
		t.Fatalf("after race: got %s, want degraded", f.State())
	}
	if got := callbackCount.Load(); got != 1 {
		t.Fatalf("onChange called %d times, want exactly 1 (CAS guarantees idempotency)", got)
	}
}

func TestFSMSetState(t *testing.T) {
	// SetState forces the FSM to a target state regardless of current
	// state. Used by Plan 07 leader-recovery resumeFSMFromEvents.
	resetEmergGauge(t)
	var captured []State
	var mu sync.Mutex
	onChange := func(from, to State, reason string) {
		mu.Lock()
		defer mu.Unlock()
		captured = append(captured, to)
	}
	f := NewFSM(slog.Default(), onChange)

	now := time.Unix(2000, 0)
	f.SetState(StateEmergencyActive, now, "leader_recovery_resume")
	if f.State() != StateEmergencyActive {
		t.Fatalf("after SetState: got %s, want emergency_active", f.State())
	}
	if f.EnteredAt().Unix() != now.Unix() {
		t.Fatalf("after SetState: EnteredAt=%d, want %d", f.EnteredAt().Unix(), now.Unix())
	}

	// Calling SetState with same state is a noop (no callback).
	f.SetState(StateEmergencyActive, time.Unix(2010, 0), "no_change")

	mu.Lock()
	defer mu.Unlock()
	if len(captured) != 1 {
		t.Fatalf("captured %d transitions, want 1: %v", len(captured), captured)
	}
}

func TestFSMConcurrentReadDuringTransition(t *testing.T) {
	// Race-detector smoke: hammer State() in one goroutine while
	// another runs Transition. Lockless atomic.Load must not race with
	// CompareAndSwap.
	resetEmergGauge(t)
	f := NewFSM(slog.Default(), nil)

	var done sync.WaitGroup
	done.Add(1)

	stop := make(chan struct{})
	go func() {
		defer done.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = f.State()
				_ = f.EnteredAt()
			}
		}
	}()

	now := time.Unix(1000, 0)
	for i := 0; i < 100; i++ {
		f.Transition(StateHealthy, StateDegraded, now.Add(time.Duration(i)*time.Second), "race")
		f.SetState(StateHealthy, now.Add(time.Duration(i)*time.Second), "reset")
	}

	close(stop)
	done.Wait()
}
