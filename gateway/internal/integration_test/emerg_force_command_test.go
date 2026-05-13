//go:build integration

// Phase 6 Plan 06-05 Task 3 — gw:emerg:events command consumption
// (BLOCKER 2 fix 2026-05-13). Proves that EmergEvents published by
// gatewayctl (Plan 06-10) are consumed end-to-end by the
// leader-elected reconciler:
//
//   - force_provision_request: leader INSERTs lifecycle row with
//     trigger_reason='manual_force' AND advances FSM HEALTHY →
//     EMERGENCY_PROVISIONING. (TestEmergReconcilerHandlesForceProvisionEvent)
//   - When 2 reconcilers race, the lifecycle is INSERTed exactly once
//     (single-leader invariant — non-leader observes the event but
//     does NOT mutate state). (TestEmergReconcilerForceProvisionRejectedNonLeader)
//   - When no active lifecycle exists, force-destroy is a no-op
//     (logged Warn, no FSM mutation, no destroy call). (TestEmergReconcilerForceDestroyNoOpWhenIdle)
//
// The active-lifecycle force-destroy path (TestEmergReconcilerHandlesForceDestroyEvent)
// is DEFERRED to Plan 06-08 alongside the destroyAndCloseLifecycle helper
// it depends on.
package integration

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

// TestEmergReconcilerHandlesForceProvisionEvent — single reconciler
// acquires leadership, gatewayctl-style force_provision_request is
// published, reconciler consumes and INSERTs lifecycle row + advances
// FSM. Within 5s budget.
func TestEmergReconcilerHandlesForceProvisionEvent(t *testing.T) {
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

	if err := redisx.PublishEmergEvent(ctx, rdb, redisx.EmergEvent{
		Type:      "force_provision_request",
		Reason:    "smoke",
		ReplicaID: "gatewayctl",
		SinceUnix: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("PublishEmergEvent: %v", err)
	}

	// Eventually FSM advances + lifecycle row INSERTed with
	// trigger_reason='manual_force'.
	if !waitFor(t, 5*time.Second, 100*time.Millisecond, func() bool {
		return fsm.State() == emerg.StateEmergencyProvisioning
	}) {
		t.Fatalf("FSM did not advance after force-provision; got %s", fsm.State())
	}

	var count int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM ai_gateway.emergency_lifecycles WHERE trigger_reason = 'manual_force'`,
	).Scan(&count); err != nil {
		t.Fatalf("count manual_force lifecycles: %v", err)
	}
	if count != 1 {
		t.Fatalf("manual_force lifecycle count = %d, want 1", count)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("Run did not return after ctx cancel")
	}
}

// TestEmergReconcilerForceProvisionRejectedNonLeader — 2 reconcilers
// share 1 Redis. ONE will be leader. force-provision published once →
// leader INSERTs once + transitions; non-leader observes the event but
// does NOT INSERT (PRV-03 single-leader invariant carried into the
// command path).
func TestEmergReconcilerForceProvisionRejectedNonLeader(t *testing.T) {
	rootCtx, rootCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer rootCancel()

	pool, rdb := freshSchema(t, rootCtx)
	cfg := defaultTestCfg(t)

	fsm1 := emerg.NewFSM(slog.New(slog.DiscardHandler), nil)
	fsm2 := emerg.NewFSM(slog.New(slog.DiscardHandler), nil)

	ctx1, cancel1 := context.WithCancel(rootCtx)
	ctx2, cancel2 := context.WithCancel(rootCtx)
	defer cancel1()
	defer cancel2()

	r1 := emerg.NewReconciler(emerg.Deps{
		DB:           pool,
		Redis:        rdb,
		Redsync:      redisx.NewEmergRedsync(rdb),
		FSM:          fsm1,
		Cfg:          cfg,
		TickInterval: 100 * time.Millisecond,
		Log:          slog.New(slog.DiscardHandler),
	})
	r2 := emerg.NewReconciler(emerg.Deps{
		DB:           pool,
		Redis:        rdb,
		Redsync:      redisx.NewEmergRedsync(rdb),
		FSM:          fsm2,
		Cfg:          cfg,
		TickInterval: 100 * time.Millisecond,
		Log:          slog.New(slog.DiscardHandler),
	})

	done1 := make(chan struct{})
	done2 := make(chan struct{})
	go func() { r1.Run(ctx1); close(done1) }()
	go func() { r2.Run(ctx2); close(done2) }()

	// Wait for exactly one leader.
	if !waitFor(t, 3*time.Second, 50*time.Millisecond, func() bool {
		return r1.IsLeader() != r2.IsLeader()
	}) {
		t.Fatalf("expected exactly 1 leader; r1.IsLeader=%v r2.IsLeader=%v",
			r1.IsLeader(), r2.IsLeader())
	}

	// Publish force-provision command.
	if err := redisx.PublishEmergEvent(rootCtx, rdb, redisx.EmergEvent{
		Type:      "force_provision_request",
		Reason:    "smoke-2-replicas",
		ReplicaID: "gatewayctl",
		SinceUnix: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("PublishEmergEvent: %v", err)
	}

	// Eventually exactly 1 row is INSERTed (PRV-03: single-leader
	// invariant prevents duplicate INSERT). The partial unique index
	// `emergency_live_singleton` is the safety net at the DB layer; the
	// reconciler's leader-only filter is the primary defense.
	if !waitFor(t, 5*time.Second, 100*time.Millisecond, func() bool {
		var count int
		if err := pool.QueryRow(rootCtx,
			`SELECT COUNT(*) FROM ai_gateway.emergency_lifecycles WHERE trigger_reason = 'manual_force'`,
		).Scan(&count); err != nil {
			return false
		}
		return count == 1
	}) {
		var count int
		_ = pool.QueryRow(rootCtx,
			`SELECT COUNT(*) FROM ai_gateway.emergency_lifecycles WHERE trigger_reason = 'manual_force'`,
		).Scan(&count)
		t.Fatalf("expected exactly 1 manual_force lifecycle; got %d", count)
	}

	// Verify it stays at 1 (no late duplicate INSERT after the leader
	// processes the event).
	time.Sleep(500 * time.Millisecond)
	var count int
	if err := pool.QueryRow(rootCtx,
		`SELECT COUNT(*) FROM ai_gateway.emergency_lifecycles WHERE trigger_reason = 'manual_force'`,
	).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("late duplicate INSERT detected: count = %d, want 1", count)
	}

	// Verify exactly one FSM advanced (the leader's). The follower's FSM
	// must remain in HEALTHY because applyEmergCommand short-circuits on
	// !isLeader BEFORE the type switch.
	leaderAdvanced := fsm1.State() == emerg.StateEmergencyProvisioning
	followerAdvanced := fsm2.State() == emerg.StateEmergencyProvisioning
	if leaderAdvanced && followerAdvanced {
		t.Fatalf("BOTH FSMs advanced (PRV-03 violated)")
	}
	if !leaderAdvanced && !followerAdvanced {
		t.Fatalf("NEITHER FSM advanced; force-provision was dropped silently")
	}

	cancel1()
	cancel2()
	select {
	case <-done1:
	case <-time.After(3 * time.Second):
		t.Fatalf("r1 Run did not return after cancel")
	}
	select {
	case <-done2:
	case <-time.After(3 * time.Second):
		t.Fatalf("r2 Run did not return after cancel")
	}
}

// TestEmergReconcilerForceDestroyNoOpWhenIdle — FSM = HEALTHY (no
// active lifecycle); force_destroy_request must be a no-op (Warn log,
// no destroy call, no FSM mutation). The active-lifecycle destroy path
// is exercised in Plan 06-08 (TestEmergReconcilerHandlesForceDestroyEvent
// lands there alongside the destroyAndCloseLifecycle helper).
func TestEmergReconcilerForceDestroyNoOpWhenIdle(t *testing.T) {
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

	// Publish force-destroy with no active lifecycle.
	if err := redisx.PublishEmergEvent(ctx, rdb, redisx.EmergEvent{
		Type:      "force_destroy_request",
		Reason:    "smoke-idle",
		ReplicaID: "gatewayctl",
		SinceUnix: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("PublishEmergEvent: %v", err)
	}

	// Wait long enough for the subscriber to dispatch.
	time.Sleep(500 * time.Millisecond)

	if got := fsm.State(); got != emerg.StateHealthy {
		t.Fatalf("FSM mutated by no-op force-destroy: got %s, want healthy", got)
	}
	// Verify no lifecycle rows were touched.
	var count int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM ai_gateway.emergency_lifecycles`,
	).Scan(&count); err != nil {
		t.Fatalf("count lifecycles: %v", err)
	}
	if count != 0 {
		t.Fatalf("lifecycle rows created by no-op force-destroy: count = %d, want 0", count)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("Run did not return after ctx cancel")
	}
}
