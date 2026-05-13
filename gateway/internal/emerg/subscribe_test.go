// Package emerg (subscribe_test.go): Plan 06-05 Task 1 unit tests for
// the gw:breaker:events subscribe loop. Drives miniredis +
// redisx.PublishBreakerEvent to verify the tracker converges.
package emerg

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

// newSubscribeTestReconciler constructs a Reconciler wired against a
// fresh miniredis. The DB pool is left nil — Plan 06-05 Task 1 only
// exercises the subscribe → tracker path. t.Cleanup tears down the
// miniredis + redis client.
func newSubscribeTestReconciler(t *testing.T) (*Reconciler, *redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() {
		_ = rdb.Close()
		mr.Close()
	})
	fsm := NewFSM(slog.New(slog.DiscardHandler), nil)
	r := NewReconciler(Deps{
		DB:           nil,
		Redis:        rdb,
		Redsync:      redisx.NewEmergRedsync(rdb),
		FSM:          fsm,
		Cfg:          config.Config{},
		TickInterval: 50 * time.Millisecond,
		Log:          slog.New(slog.DiscardHandler),
	})
	return r, rdb, mr
}

// TestSubscribe_AppliesLocalLlmEvent — publish an OPEN event for
// upstream=local-llm and verify the tracker converges to state=open
// with openSince > 0 within 1 second.
func TestSubscribe_AppliesLocalLlmEvent(t *testing.T) {
	r, rdb, _ := newSubscribeTestReconciler(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go r.Subscribe(ctx)
	// Allow the subscriber loop to register the SUBSCRIBE before publishing.
	time.Sleep(100 * time.Millisecond)

	if err := redisx.PublishBreakerEvent(ctx, rdb, redisx.BreakerEvent{
		Upstream: "local-llm", State: "open", SinceUnix: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("PublishBreakerEvent: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r.tracker.State() == "open" && r.tracker.openSince.Load() > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("tracker did not converge: state=%q openSince=%d",
		r.tracker.State(), r.tracker.openSince.Load())
}

// TestSubscribe_IgnoresNonLocalLlm — publish an OPEN for local-stt;
// tracker MUST remain in the closed initial state. Phase 6 D-C2: only
// local-llm chat is the trigger signal.
func TestSubscribe_IgnoresNonLocalLlm(t *testing.T) {
	r, rdb, _ := newSubscribeTestReconciler(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go r.Subscribe(ctx)
	time.Sleep(100 * time.Millisecond)

	if err := redisx.PublishBreakerEvent(ctx, rdb, redisx.BreakerEvent{
		Upstream: "local-stt", State: "open", SinceUnix: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("PublishBreakerEvent: %v", err)
	}

	// Wait long enough that a buggy implementation would have written.
	time.Sleep(300 * time.Millisecond)

	if got := r.tracker.State(); got != "closed" {
		t.Fatalf("tracker state mutated by non-local-llm event: got %q, want closed", got)
	}
	if got := r.tracker.openSince.Load(); got != 0 {
		t.Fatalf("tracker openSince mutated by non-local-llm event: got %d, want 0", got)
	}
}

// TestSubscribe_MalformedPayloadDoesNotCrash — publish a non-JSON
// payload; the subscriber must drop it (log Warn) and continue
// processing subsequent valid events. Threat T-6-W5-02.
func TestSubscribe_MalformedPayloadDoesNotCrash(t *testing.T) {
	r, rdb, _ := newSubscribeTestReconciler(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go r.Subscribe(ctx)
	time.Sleep(100 * time.Millisecond)

	// Publish raw garbage on the channel — bypasses the Marshal helper
	// so the subscriber sees an unparseable payload.
	if err := rdb.Publish(ctx, redisx.BreakerEventsChannel(), []byte("{not-json")).Err(); err != nil {
		t.Fatalf("raw publish: %v", err)
	}

	// Then publish a VALID local-llm OPEN event — subscriber must still
	// be alive to consume it.
	if err := redisx.PublishBreakerEvent(ctx, rdb, redisx.BreakerEvent{
		Upstream: "local-llm", State: "open", SinceUnix: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("PublishBreakerEvent: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r.tracker.State() == "open" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("subscriber did not survive malformed payload (tracker state = %q)",
		r.tracker.State())
}

// TestSubscribeEmergCommands_NonLeaderIgnores — even when a
// force_provision_request arrives, a non-leader reconciler must NOT
// mutate FSM state or write to the DB. Leader-only filter check.
func TestSubscribeEmergCommands_NonLeaderIgnores(t *testing.T) {
	r, rdb, _ := newSubscribeTestReconciler(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Confirm baseline — fresh reconciler is NOT leader.
	if r.IsLeader() {
		t.Fatalf("freshly constructed reconciler must not be leader")
	}

	go r.SubscribeEmergCommands(ctx)
	time.Sleep(100 * time.Millisecond)

	if err := redisx.PublishEmergEvent(ctx, rdb, redisx.EmergEvent{
		Type:      "force_provision_request",
		Reason:    "smoke",
		ReplicaID: "gatewayctl",
		SinceUnix: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("PublishEmergEvent: %v", err)
	}

	// Allow handler dispatch + drop on non-leader path.
	time.Sleep(200 * time.Millisecond)

	if got := r.deps.FSM.State(); got != StateHealthy {
		t.Fatalf("non-leader FSM mutated: got %s, want healthy", got)
	}
	if got := r.activeLifecycle.Load(); got != nil {
		t.Fatalf("non-leader activeLifecycle set: %+v", got)
	}
}
