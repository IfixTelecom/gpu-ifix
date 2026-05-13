//go:build integration

// Phase 6 Plan 06-04 Task 2 — leader-election PRV-03 / SC-2 invariant.
//
// Proves the redsync v4 distributed mutex (gw:emerg:lock, TTL 30s, renew
// 10s = 1/3 TTL) enforces single-leader semantics across two reconciler
// goroutines sharing the same Redis. SC-2 asserts that running 2
// gateway replicas NEVER produces more than 1 emergency pod — the
// underlying invariant is "exactly one replica is leader at a time."
//
// Failover sub-scenario: cancelling the leader's context releases the
// lock (Pitfall 8 separate-ctx Unlock); the surviving replica eventually
// acquires the lock. We accept up to ~1.5s for the survivor to flip
// (TickInterval=100ms × a few retries) — well below the 30s TTL.
package integration

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

// defaultTestCfg returns a minimal Phase 6 config with accelerated
// timings so emerg integration tests run sub-30s. Plans 05-08 will reuse
// this helper — RESEARCH.md Pitfall 13 ("multi-minute test runtime via
// real-second timings") asks every emerg integration test to override
// the four PROVISION_*_SECONDS knobs.
func defaultTestCfg(t *testing.T) config.Config {
	t.Helper()
	cfg, err := loadBaseConfig()
	if err != nil {
		t.Fatalf("loadBaseConfig: %v", err)
	}
	// Accelerated Phase 6 knobs (default 600/300/300/120; RESEARCH Pitfall 13).
	cfg.ProvisionTriggerFailedOverSeconds = 1
	cfg.ProvisionHealthyDurationSeconds = 1
	cfg.ProvisionIdleGraceSeconds = 1
	cfg.ProvisionColdStartBudgetSeconds = 5
	cfg.VastPriceCapDPH = 0.40
	cfg.USDToBRLRate = 5.0
	cfg.MonthlyEmergencyBudgetBRL = 200.0
	cfg.EmergencyPodImageTag = "v1.0"
	cfg.VastAPIQPSLimit = 1
	return cfg
}

// TestEmergLeaderLockBlocks2ndReplica proves PRV-03 / SC-2: two
// reconciler instances sharing the same Redis MUST elect exactly 1
// leader. Failover sub-test: cancelling the leader frees the lock and
// the survivor acquires it within a few ticks.
func TestEmergLeaderLockBlocks2ndReplica(t *testing.T) {
	rootCtx, rootCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer rootCancel()

	pool, rdb := freshSchema(t, rootCtx)
	cfg := defaultTestCfg(t)

	// Two FSMs, two reconcilers, ONE shared Redis. Different FSM
	// instances simulate two distinct replicas — the lock acquisition
	// race is what we are testing, not FSM internals.
	fsm1 := emerg.NewFSM(slog.New(slog.DiscardHandler), nil)
	fsm2 := emerg.NewFSM(slog.New(slog.DiscardHandler), nil)

	// Per-replica contexts so we can cancel one without taking down both.
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

	// Wait up to 2s for exactly one to claim leadership. Tick=100ms so
	// the first lock acquire happens at ~100ms; we check repeatedly.
	if !waitFor(t, 2*time.Second, 50*time.Millisecond, func() bool {
		return r1.IsLeader() != r2.IsLeader()
	}) {
		t.Fatalf("expected exactly 1 leader within 2s; r1.IsLeader=%v r2.IsLeader=%v",
			r1.IsLeader(), r2.IsLeader())
	}

	// Identify leader/follower for the failover phase.
	var leader, follower *emerg.Reconciler
	var leaderCancel, followerCancel context.CancelFunc
	var leaderDone, followerDone chan struct{}
	if r1.IsLeader() {
		leader, follower = r1, r2
		leaderCancel, followerCancel = cancel1, cancel2
		leaderDone, followerDone = done1, done2
	} else {
		leader, follower = r2, r1
		leaderCancel, followerCancel = cancel2, cancel1
		leaderDone, followerDone = done2, done1
	}

	if !leader.IsLeader() {
		t.Fatalf("leader.IsLeader() should be true")
	}
	if follower.IsLeader() {
		t.Fatalf("follower.IsLeader() should be false (PRV-03 violated)")
	}

	// Failover: cancel the leader. Pitfall 8 separate-ctx Unlock should
	// release the lock cleanly. Survivor MUST acquire within a few ticks.
	leaderCancel()
	select {
	case <-leaderDone:
	case <-time.After(3 * time.Second):
		t.Fatalf("leader Run() did not return after ctx cancel")
	}

	// Follower should now flip to leader. Allow up to 2s — the lock has
	// been Unlock'd so the follower's next LockContext call (within
	// 100ms) will succeed.
	if !waitFor(t, 2*time.Second, 50*time.Millisecond, follower.IsLeader) {
		t.Fatalf("follower did not acquire leadership after primary leader was cancelled within 2s")
	}

	// Cleanup: cancel survivor and wait.
	followerCancel()
	select {
	case <-followerDone:
	case <-time.After(3 * time.Second):
		t.Fatalf("follower Run() did not return after ctx cancel")
	}

	// Sanity: replica IDs should be populated (os.Hostname()).
	if leader.ReplicaID() == "" {
		t.Errorf("leader.ReplicaID() empty — should be os.Hostname()")
	}
	if follower.ReplicaID() == "" {
		t.Errorf("follower.ReplicaID() empty — should be os.Hostname()")
	}
}

// waitFor polls cond every step until it returns true or until budget
// elapses. Returns true on success. Local helper to avoid pulling in
// stretchr/testify just for this one place.
func waitFor(t *testing.T, budget, step time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(step)
	}
	return cond()
}
