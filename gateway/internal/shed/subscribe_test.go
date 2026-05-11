// Package shed (subscribe_test.go): miniredis-backed round-trip for the
// Pub/Sub consumer goroutine (Subscribe) and the boot rehydration helper
// (HydrateFromRedis). Mirrors the pattern of
// gateway/internal/redisx/shed_test.go: one miniredis per test, helper
// tears down the redis.Client via t.Cleanup.
//
// Lifecycle invariants exercised by this suite:
//   - Subscribe applies a remote ShedEvent within ~500ms of PUBLISH.
//   - HydrateFromRedis hydrates remoteState from gw:shed:{upstream}
//     Hashes WITHOUT forcing the in-process FSM to that state
//     (the FSM is authoritative; D-C3).
//   - Subscribe survives a malformed payload and continues applying
//     subsequent valid events.
//   - MakePublishTransition writes the mirror Hash AND publishes the
//     event in a single call.
package shed

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

func newShedSetWithRedis(t *testing.T) (*Set, *redis.Client, *miniredis.Miniredis) {
	t.Helper()
	m := miniredis.RunT(t)
	c := redis.NewClient(&redis.Options{Addr: m.Addr()})
	t.Cleanup(func() { _ = c.Close() })
	s := NewSet(c, slog.Default(), Options{DefaultArmSeconds: 30, DefaultRecoverSeconds: 60})
	s.Rebuild([]string{"local-llm", "local-stt"})
	return s, c, m
}

func TestSubscribeAppliesRemoteEvent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s, c, _ := newShedSetWithRedis(t)

	go s.Subscribe(ctx, c)
	time.Sleep(80 * time.Millisecond) // let subscribe register

	ev := redisx.ShedEvent{Upstream: "local-llm", State: "on", SinceUnix: 100, Reason: "test"}
	if err := redisx.PublishShedEvent(ctx, c, ev); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// Poll up to 500ms for ApplyRemoteEvent to land
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if st, ok := s.RemoteState("local-llm"); ok && st == StateOn {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("remoteState not applied within 500ms")
}

func TestHydrateFromRedisAppliesExistingState(t *testing.T) {
	ctx := context.Background()
	s, c, _ := newShedSetWithRedis(t)
	if err := redisx.WriteShedState(ctx, c, "local-llm", "on", "seeded", 42, nil); err != nil {
		t.Fatalf("seed write: %v", err)
	}
	s.HydrateFromRedis(ctx, c, slog.Default())
	if st, ok := s.RemoteState("local-llm"); !ok || st != StateOn {
		t.Fatalf("hydrate: ok=%v state=%s", ok, st)
	}
}

func TestHydrateFromRedis_DoesNotForceInProcessFSM(t *testing.T) {
	// The hydrate helper MUST NOT alter in-process FSM state — remote
	// state is advisory only (CONTEXT.md D-C3). The FSM stays at
	// StateOff after hydration even if Redis reports "on".
	ctx := context.Background()
	s, c, _ := newShedSetWithRedis(t)
	_ = redisx.WriteShedState(ctx, c, "local-llm", "on", "remote", 42, nil)
	s.HydrateFromRedis(ctx, c, slog.Default())
	fsm, _ := s.Get("local-llm")
	if fsm.State() != StateOff {
		t.Fatalf("hydrate must not force FSM; got state=%s", fsm.State())
	}
}

func TestHydrateFromRedis_NilClientIsNoop(t *testing.T) {
	s, _, _ := newShedSetWithRedis(t)
	// Should not panic.
	s.HydrateFromRedis(context.Background(), nil, slog.Default())
}

func TestSubscribeHandlesMalformedPayload(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	s, c, _ := newShedSetWithRedis(t)
	go s.Subscribe(ctx, c)
	time.Sleep(80 * time.Millisecond)
	// Publish garbage directly via Redis, bypassing ShedEvent marshaling.
	if err := c.Publish(ctx, redisx.ShedEventsChannel, "not json").Err(); err != nil {
		t.Fatalf("publish raw: %v", err)
	}
	// Followed by a valid event — should still be applied.
	time.Sleep(80 * time.Millisecond)
	ev := redisx.ShedEvent{Upstream: "local-llm", State: "armed", SinceUnix: 100}
	if err := redisx.PublishShedEvent(ctx, c, ev); err != nil {
		t.Fatalf("publish valid: %v", err)
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if st, _ := s.RemoteState("local-llm"); st == StateArmed {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("subsequent valid event not applied after malformed")
}

func TestSubscribe_NilClientReturnsImmediately(t *testing.T) {
	s, _, _ := newShedSetWithRedis(t)
	done := make(chan struct{})
	go func() {
		s.Subscribe(context.Background(), nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Subscribe with nil client should return immediately")
	}
}

func TestPublishTransitionWritesAndPublishes(t *testing.T) {
	ctx := context.Background()
	_, c, _ := newShedSetWithRedis(t)
	pub := MakePublishTransition(c)
	pub("local-llm", StateOn, "test", &redisx.ShedEventSignals{Inflight: 4, P95Ms: 2100, VramMiB: 21000})
	// Allow any goroutine-internal dispatch (impl is synchronous but defensive).
	time.Sleep(30 * time.Millisecond)
	m, err := redisx.ReadShedState(ctx, c, "local-llm")
	if err != nil || m == nil {
		t.Fatalf("read: err=%v m=%v", err, m)
	}
	if m["state"] != "on" {
		t.Errorf("state = %q want on", m["state"])
	}
	if m["reason"] != "test" {
		t.Errorf("reason = %q want test", m["reason"])
	}
	if m["inflight"] != "4" {
		t.Errorf("inflight = %q want 4", m["inflight"])
	}
}

func TestMakePublishTransition_NilClientIsNoop(t *testing.T) {
	pub := MakePublishTransition(nil)
	// Must not panic.
	pub("local-llm", StateOn, "test", nil)
}

func TestParseState_KnownAndUnknown(t *testing.T) {
	tests := []struct {
		in   string
		want State
	}{
		{"off", StateOff},
		{"armed", StateArmed},
		{"on", StateOn},
		{"recovering", StateRecovering},
		{"garbage", StateOff}, // unknown defaults to Off
		{"", StateOff},
	}
	for _, tc := range tests {
		if got := parseState(tc.in); got != tc.want {
			t.Errorf("parseState(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}
