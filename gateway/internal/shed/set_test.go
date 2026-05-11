// Package shed (set_test.go): unit tests for the per-upstream FSM
// registry. Mirrors the pattern used by gateway/internal/breaker/breaker_test.go
// (Rebuild + Get + state preservation + callback threading).
//
// The Set holds no Redis dependency in these tests (rdb=nil is the
// "in-process-only" mode used by the unit suite). The Plan 05-04 Redis
// mirror lives in a separate file (mirror.go); Plan 05-05 wires the
// subscribe goroutine. This test only validates the in-process registry
// semantics.
package shed

import (
	"log/slog"
	"testing"
	"time"
)

func TestSet_RebuildAddsAndRemoves(t *testing.T) {
	s := NewSet(nil, slog.Default(), Options{DefaultArmSeconds: 30, DefaultRecoverSeconds: 60})
	s.Rebuild([]string{"a", "b"})
	if _, ok := s.Get("a"); !ok {
		t.Fatal("expected a after Rebuild")
	}
	if _, ok := s.Get("b"); !ok {
		t.Fatal("expected b after Rebuild")
	}
	if len(s.Names()) != 2 {
		t.Fatalf("expected 2 names, got %d", len(s.Names()))
	}
	s.Rebuild([]string{"b", "c"})
	if _, ok := s.Get("a"); ok {
		t.Fatal("a should be gone after second Rebuild")
	}
	if _, ok := s.Get("c"); !ok {
		t.Fatal("c should be present after second Rebuild")
	}
	if _, ok := s.Get("b"); !ok {
		t.Fatal("b should be preserved across Rebuild")
	}
}

func TestSet_RebuildPreservesState(t *testing.T) {
	s := NewSet(nil, slog.Default(), Options{DefaultArmSeconds: 1, DefaultRecoverSeconds: 1})
	s.Rebuild([]string{"a"})
	fA, _ := s.Get("a")
	fA.Transition(StateOn, "test")
	s.Rebuild([]string{"a", "b"}) // re-rebuild with a still present
	fA2, _ := s.Get("a")
	if fA2 != fA {
		t.Fatalf("rebuild should keep the same FSM pointer for unchanged names")
	}
	if fA2.State() != StateOn {
		t.Fatalf("rebuild should preserve existing FSM state; got %s", fA2.State())
	}
}

func TestSet_OnChangeCallbackThreadedThrough(t *testing.T) {
	var captured string
	onChange := func(upstream string, from, to State, reason string) {
		captured = upstream + ":" + to.String() + ":" + reason
	}
	s := NewSet(nil, slog.Default(), Options{OnChange: onChange})
	s.Rebuild([]string{"a"})
	f, _ := s.Get("a")
	f.Transition(StateOn, "test")
	if captured == "" {
		t.Fatal("onChange callback not fired")
	}
	want := "a:on:test"
	if captured != want {
		t.Fatalf("captured=%q, want %q", captured, want)
	}
}

func TestSet_RemoteEventStored(t *testing.T) {
	s := NewSet(nil, slog.Default(), Options{})
	s.Rebuild([]string{"a"})
	s.ApplyRemoteEvent("a", StateOn)
	st, ok := s.RemoteState("a")
	if !ok || st != StateOn {
		t.Fatalf("remote event not stored correctly: ok=%v state=%s", ok, st)
	}
}

func TestSet_RemoteState_UnknownReturnsFalse(t *testing.T) {
	s := NewSet(nil, slog.Default(), Options{})
	_, ok := s.RemoteState("never_seen")
	if ok {
		t.Fatal("RemoteState for unknown name should return ok=false")
	}
}

func TestSet_StateConvenience(t *testing.T) {
	s := NewSet(nil, slog.Default(), Options{DefaultArmSeconds: 1, DefaultRecoverSeconds: 1})
	s.Rebuild([]string{"local-llm"})
	// Default state is Off
	if got := s.State("local-llm"); got != StateOff {
		t.Fatalf("default state = %s, want off", got)
	}
	// Unknown upstream returns Off (defensive)
	if got := s.State("nonexistent"); got != StateOff {
		t.Fatalf("unknown upstream state = %s, want off", got)
	}
	// Force transition + verify convenience reflects it
	f, _ := s.Get("local-llm")
	f.Transition(StateArmed, "test")
	if got := s.State("local-llm"); got != StateArmed {
		t.Fatalf("after transition state = %s, want armed", got)
	}
}

func TestSet_ForEach_IteratesAll(t *testing.T) {
	s := NewSet(nil, slog.Default(), Options{})
	s.Rebuild([]string{"a", "b", "c"})
	seen := make(map[string]bool)
	s.ForEach(func(upstream string, f *FSM) {
		if f == nil {
			t.Fatalf("ForEach passed nil FSM for upstream=%s", upstream)
		}
		seen[upstream] = true
	})
	if len(seen) != 3 {
		t.Fatalf("ForEach saw %d upstreams, want 3: %v", len(seen), seen)
	}
}

func TestSet_DefaultsApplied(t *testing.T) {
	// Options with zero-value DefaultArmSeconds/DefaultRecoverSeconds
	// should fall back to D-C1 strict defaults (30s/60s).
	s := NewSet(nil, slog.Default(), Options{})
	s.Rebuild([]string{"a"})
	f, _ := s.Get("a")
	// We cannot directly read Config without an accessor; instead drive
	// a transition with elapsed=29s and expect "still arming" (proves
	// arm-default > 29s, i.e. is the 30s default).
	f.Evaluate(time.Unix(1000, 0), Signals{InflightOverMax: true, P95OverMax: true})
	f.Evaluate(time.Unix(1029, 0), Signals{InflightOverMax: true, P95OverMax: true})
	if f.State() != StateArmed {
		t.Fatalf("default arm should be 30s; elapsed 29s expected armed, got %s", f.State())
	}
}
