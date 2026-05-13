//go:build integration

// Phase 6 Plan 06-05 Task 2 — emergency trigger gate (PRV-04 / SC-1).
//
// Drives the local-llm breaker Pub/Sub channel against a leader-elected
// reconciler and asserts:
//
//   - Sustained OPEN (≥ ProvisionTriggerFailedOverSeconds=1 in test cfg)
//     advances FSM HEALTHY → EMERGENCY_PROVISIONING. (TestEmergTriggerSustained)
//   - Transient OPEN→CLOSED (< 1s) does NOT trigger; FSM stays HEALTHY.
//     (TestEmergTriggerTransient)
//   - When a live lifecycle row already exists in the DB, sustained OPEN
//     does NOT trigger again (D-C5 reconciler check). (TestEmergTriggerNoSpawnIfLiveLifecycle)
//
// All tests reuse the defaultTestCfg helper from emerg_leader_test.go
// which sets ProvisionTriggerFailedOverSeconds=1 (RESEARCH Pitfall 13:
// accelerated timings keep integration runtime sub-30s).
package integration

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

// publishBreakerEvent is the single-call helper used by Plan 06-05 +
// downstream plans to drive the local-llm trigger. Wraps the canonical
// redisx.PublishBreakerEvent so test code stays compact.
func publishBreakerEvent(t *testing.T, rdb *redis.Client, upstream, state string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := redisx.PublishBreakerEvent(ctx, rdb, redisx.BreakerEvent{
		Upstream: upstream, State: state, SinceUnix: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("publishBreakerEvent(%s,%s): %v", upstream, state, err)
	}
}

// TestEmergTriggerSustained — single reconciler, miniredis-shared
// channel, OPEN published once. With ProvisionTriggerFailedOverSeconds=1,
// the FSM must reach EMERGENCY_PROVISIONING within ~3s (1s sustained +
// reconciler tick latency).
func TestEmergTriggerSustained(t *testing.T) {
	rootCtx, rootCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer rootCancel()

	pool, rdb := freshSchema(t, rootCtx)
	cfg := defaultTestCfg(t)

	fsm := emerg.NewFSM(slog.New(slog.DiscardHandler), nil)
	r := emerg.NewReconciler(emerg.Deps{
		DB:           pool,
		Redis:        rdb,
		Redsync:      redisx.NewEmergRedsync(rdb),
		FSM:          fsm,
		Cfg:          cfg,
		TickInterval: 100 * time.Millisecond,
		Log:          slog.New(slog.DiscardHandler),
	})

	ctx, cancel := context.WithCancel(rootCtx)
	defer cancel()
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()

	// Wait for leadership so the trigger gate is allowed to fire.
	if !waitFor(t, 3*time.Second, 50*time.Millisecond, r.IsLeader) {
		t.Fatalf("reconciler did not acquire leadership within 3s")
	}

	// Publish OPEN — sustained timer begins.
	publishBreakerEvent(t, rdb, "local-llm", "open")

	// Eventually FSM reaches EMERGENCY_PROVISIONING. Budget = 5s.
	if !waitFor(t, 5*time.Second, 100*time.Millisecond, func() bool {
		return fsm.State() == emerg.StateEmergencyProvisioning
	}) {
		t.Fatalf("FSM did not reach EMERGENCY_PROVISIONING after sustained OPEN; got %s",
			fsm.State())
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("Run did not return after ctx cancel")
	}
}

// TestEmergTriggerTransient — OPEN immediately followed by CLOSED at
// 200ms. With threshold=1s, the openSince must be reset BEFORE the
// reconciler observes it. FSM must remain HEALTHY.
func TestEmergTriggerTransient(t *testing.T) {
	rootCtx, rootCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer rootCancel()

	pool, rdb := freshSchema(t, rootCtx)
	cfg := defaultTestCfg(t)

	fsm := emerg.NewFSM(slog.New(slog.DiscardHandler), nil)
	r := emerg.NewReconciler(emerg.Deps{
		DB:           pool,
		Redis:        rdb,
		Redsync:      redisx.NewEmergRedsync(rdb),
		FSM:          fsm,
		Cfg:          cfg,
		TickInterval: 100 * time.Millisecond,
		Log:          slog.New(slog.DiscardHandler),
	})

	ctx, cancel := context.WithCancel(rootCtx)
	defer cancel()
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()

	if !waitFor(t, 3*time.Second, 50*time.Millisecond, r.IsLeader) {
		t.Fatalf("reconciler did not acquire leadership within 3s")
	}

	publishBreakerEvent(t, rdb, "local-llm", "open")
	time.Sleep(200 * time.Millisecond)
	publishBreakerEvent(t, rdb, "local-llm", "closed")

	// Wait long enough that a buggy implementation (one that armed the
	// trigger on the first OPEN without re-reading state on the tick)
	// would have fired. With threshold=1s, the gap between OPEN at t=0
	// and CLOSED at t=0.2s is shorter than the threshold, so the timer
	// resets to 0 BEFORE the gate would have fired.
	time.Sleep(2 * time.Second)

	if got := fsm.State(); got != emerg.StateHealthy {
		t.Fatalf("FSM transitioned on transient OPEN→CLOSED: got %s, want healthy", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("Run did not return after ctx cancel")
	}
}

// TestEmergTriggerNoSpawnIfLiveLifecycle — pre-seed an unclosed
// emergency_lifecycles row so D-C5 reconciler check fires. Sustained
// OPEN must NOT cause a transition; FSM stays HEALTHY and the reconciler
// logs an error (not asserted directly — visible in -v output).
func TestEmergTriggerNoSpawnIfLiveLifecycle(t *testing.T) {
	rootCtx, rootCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer rootCancel()

	pool, rdb := freshSchema(t, rootCtx)
	cfg := defaultTestCfg(t)

	// Pre-seed: insert an unclosed lifecycle row (ended_at IS NULL). The
	// partial unique index `emergency_live_singleton` (Plan 06-02)
	// guarantees ≤1 such row at a time, so this single INSERT trips the
	// D-C5 reconciler check on every subsequent trigger evaluation.
	if _, err := pool.Exec(rootCtx,
		`INSERT INTO ai_gateway.emergency_lifecycles (trigger_reason) VALUES ('manual_force')`); err != nil {
		t.Fatalf("pre-seed lifecycle: %v", err)
	}

	fsm := emerg.NewFSM(slog.New(slog.DiscardHandler), nil)
	r := emerg.NewReconciler(emerg.Deps{
		DB:           pool,
		Redis:        rdb,
		Redsync:      redisx.NewEmergRedsync(rdb),
		FSM:          fsm,
		Cfg:          cfg,
		TickInterval: 100 * time.Millisecond,
		Log:          slog.New(slog.DiscardHandler),
	})

	ctx, cancel := context.WithCancel(rootCtx)
	defer cancel()
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()

	if !waitFor(t, 3*time.Second, 50*time.Millisecond, r.IsLeader) {
		t.Fatalf("reconciler did not acquire leadership within 3s")
	}

	publishBreakerEvent(t, rdb, "local-llm", "open")
	// Allow >> 1s sustained — D-C5 must block the trigger every tick.
	time.Sleep(2500 * time.Millisecond)

	if got := fsm.State(); got != emerg.StateHealthy {
		t.Fatalf("FSM transitioned despite live lifecycle (D-C5 check failed): got %s, want healthy", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("Run did not return after ctx cancel")
	}
}
