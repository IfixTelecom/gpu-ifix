//go:build integration

// Regression test for handleForceProvision when FSM is in Cooldown.
//
// Bug discovered 2026-05-16 during UAT live lifecycle 31:
// handleForceProvision called Transition(StateHealthy, StateFailedOver, ...)
// + Transition(StateFailedOver, StateEmergencyProvisioning, ...). The CAS
// inside Transition fails silently when from-state != current state, so an
// FSM in Cooldown (e.g. after a recent offer_race_lost) stays in Cooldown.
// But InsertEmergencyLifecycle + spawnProvisionGoroutine already executed,
// creating an orphan pod billing $$ while the reconciler tick evaluated
// Cooldown and ignored the live lifecycle. Fix: switch to SetState which
// CAS-loops until commit regardless of current state, plus an explicit
// reject for from-states already in the emergency path.
//
// This test forces FSM into Cooldown via SetState, publishes a
// force_provision_request, and asserts the FSM advances out of Cooldown
// into the emergency path AND that exactly one manual_force lifecycle row
// is INSERTed.
package integration

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

// TestEmergReconcilerForceProvisionFromCooldown — FSM in Cooldown,
// operator dispatches force_provision_request, FSM must transition out
// of Cooldown into the emergency path and a manual_force lifecycle row
// must be INSERTed.
func TestEmergReconcilerForceProvisionFromCooldown(t *testing.T) {
	rootCtx, rootCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer rootCancel()

	pool, rdb := freshSchema(t, rootCtx)
	cfg := defaultTestCfg(t)

	fsm := emerg.NewFSM(slog.New(slog.DiscardHandler), nil)
	// Force FSM into Cooldown before the reconciler starts — simulates
	// the production sequence "force-provision attempt failed (e.g.
	// offer_race_lost) → FSM parked in Cooldown → operator force-provision
	// again before cooldown hold elapses".
	fsm.SetState(emerg.StateCooldown, time.Now(), "test_setup_cooldown")

	r := emerg.NewReconciler(emerg.Deps{
		DB:           pool,
		Redis:        rdb,
		Redsync:      redisx.NewEmergRedsync(rdb),
		FSM:          fsm,
		Cfg:          cfg,
		Vast:         forceProvisionVastMock(t),
		TickInterval: 100 * time.Millisecond,
		Log:          slog.New(slog.DiscardHandler),
	})
	r.SetHealthCheck(func(_ context.Context, _ string) bool { return true })

	ctx, cancel := context.WithCancel(rootCtx)
	defer cancel()
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()

	if !waitFor(t, 3*time.Second, 50*time.Millisecond, r.IsLeader) {
		t.Fatalf("reconciler did not acquire leadership within 3s")
	}

	// Confirm FSM is still in Cooldown after leader-recovery resume
	// (resume reads lifecycle.events; no events → state stays at the
	// preset Cooldown).
	if got := fsm.State(); got != emerg.StateCooldown {
		t.Fatalf("pre-force FSM state = %s, want cooldown", got)
	}

	// Sustained local-llm OPEN keeps the tracker in "open" so the next
	// evaluateEmergencyProvisioning tick does NOT take the D-C3 cancel
	// branch.
	publishBreakerEvent(t, rdb, "local-llm", "open")

	if err := redisx.PublishEmergEvent(ctx, rdb, redisx.EmergEvent{
		Type:      "force_provision_request",
		Reason:    "operator_break_cooldown",
		ReplicaID: "gatewayctl",
		SinceUnix: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("PublishEmergEvent: %v", err)
	}

	// Eventually the FSM advances out of Cooldown into the emergency
	// path. This is the regression assertion — pre-fix the FSM would
	// stay parked in Cooldown forever (the Transition CAS rejected
	// from-state=Healthy) while the lifecycle row + provision goroutine
	// already executed.
	if !waitFor(t, 5*time.Second, 100*time.Millisecond, func() bool {
		s := fsm.State()
		return s != emerg.StateCooldown && stateAtLeastProvisioning(s)
	}) {
		t.Fatalf("FSM did not advance out of Cooldown after force-provision; got %s", fsm.State())
	}

	// Exactly one manual_force lifecycle row INSERTed.
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

// TestEmergReconcilerForceProvisionRejectedFromEmergencyActive — FSM
// already in EmergencyActive (lifecycle in-flight), operator dispatches
// force_provision_request, reconciler must REJECT (no second lifecycle
// INSERTed, no FSM mutation).
func TestEmergReconcilerForceProvisionRejectedFromEmergencyActive(t *testing.T) {
	rootCtx, rootCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer rootCancel()

	pool, rdb := freshSchema(t, rootCtx)
	cfg := defaultTestCfg(t)

	fsm := emerg.NewFSM(slog.New(slog.DiscardHandler), nil)
	// Force FSM into EmergencyActive — simulates "pod already serving;
	// duplicate operator force-provision should be a no-op reject".
	fsm.SetState(emerg.StateEmergencyActive, time.Now(), "test_setup_active")

	r := emerg.NewReconciler(emerg.Deps{
		DB:           pool,
		Redis:        rdb,
		Redsync:      redisx.NewEmergRedsync(rdb),
		FSM:          fsm,
		Cfg:          cfg,
		Vast:         forceProvisionVastMock(t),
		TickInterval: 100 * time.Millisecond,
		Log:          slog.New(slog.DiscardHandler),
	})
	r.SetHealthCheck(func(_ context.Context, _ string) bool { return true })

	ctx, cancel := context.WithCancel(rootCtx)
	defer cancel()
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()

	if !waitFor(t, 3*time.Second, 50*time.Millisecond, r.IsLeader) {
		t.Fatalf("reconciler did not acquire leadership within 3s")
	}

	if err := redisx.PublishEmergEvent(ctx, rdb, redisx.EmergEvent{
		Type:      "force_provision_request",
		Reason:    "duplicate_operator_request",
		ReplicaID: "gatewayctl",
		SinceUnix: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("PublishEmergEvent: %v", err)
	}

	// Wait a few ticks for the reconciler to consume the event. The FSM
	// must NOT transition to Provisioning (the reject path returns
	// before InsertEmergencyLifecycle).
	time.Sleep(1 * time.Second)

	if got := fsm.State(); got != emerg.StateEmergencyActive {
		t.Fatalf("FSM mutated despite reject: got %s, want emergency_active", got)
	}

	var count int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM ai_gateway.emergency_lifecycles WHERE trigger_reason = 'manual_force'`,
	).Scan(&count); err != nil {
		t.Fatalf("count manual_force lifecycles: %v", err)
	}
	if count != 0 {
		t.Fatalf("manual_force lifecycle count = %d, want 0 (force-provision must reject when FSM already in emergency path)", count)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("Run did not return after ctx cancel")
	}
}
