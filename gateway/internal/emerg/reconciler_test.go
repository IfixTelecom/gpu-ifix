// Package emerg (reconciler_test.go): Plan 06-04 unit tests for the
// leader-elected reconciler.
//
// Coverage focus (per 06-PLAN-04 <behavior> + 06-RESEARCH.md Pitfall 4 + 8):
//   - Lock acquisition + IsLeader() flips true after first tick (miniredis)
//   - Graceful shutdown via ctx.Done() does NOT panic and Unlock uses a
//     SEPARATE context (Pitfall 8) so a cancelled parent ctx does not
//     swallow UnlockContext.
//   - Renew elapsed-based cadence: lastExtendUnix advances on the first
//     extend window (>=10s).
//   - Pitfall 4 enforcement around Extend semantics is exercised in the
//     integration test (TestEmergLeaderLockBlocks2ndReplica) where two
//     reconcilers race against the same Redis. Mocking the redsync mutex
//     directly is intentionally not done here — wiring would require a
//     `mutexLike` interface that does not exist in production code, and
//     the integration test gives stronger evidence.
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

// newReconcilerForTest spins up an in-process miniredis + go-redis
// client + Reconciler wired with a fast 50ms tick. The DB pool is left
// nil — Plan 04 does not exercise any DB query path (evaluateTick is a
// stub). t.Cleanup closes the redis client and the miniredis server.
func newReconcilerForTest(t *testing.T) (*Reconciler, *redis.Client, *miniredis.Miniredis) {
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
		DB:           nil, // Plan 04 evaluateTick is a stub; no DB needed.
		Redis:        rdb,
		Redsync:      redisx.NewEmergRedsync(rdb),
		FSM:          fsm,
		Cfg:          config.Config{},
		TickInterval: 50 * time.Millisecond,
		Log:          slog.New(slog.DiscardHandler),
	})
	return r, rdb, mr
}

// TestReconcilerNewDefaults verifies NewReconciler fills in defaults so a
// minimal Deps still yields a usable Reconciler.
func TestReconcilerNewDefaults(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	fsm := NewFSM(slog.New(slog.DiscardHandler), nil)
	r := NewReconciler(Deps{Redis: rdb, FSM: fsm})
	if r == nil {
		t.Fatalf("NewReconciler returned nil")
	}
	if r.deps.TickInterval != 1*time.Second {
		t.Fatalf("TickInterval default = %v, want 1s", r.deps.TickInterval)
	}
	if r.deps.Log == nil {
		t.Fatalf("Log default should be slog.Default()")
	}
	if r.deps.Redsync == nil {
		t.Fatalf("Redsync should be auto-constructed when nil")
	}
	if r.replicaID == "" {
		t.Fatalf("replicaID should be populated from os.Hostname()")
	}
	if r.IsLeader() {
		t.Fatalf("freshly-constructed reconciler must NOT be leader")
	}
	if r.State() != StateHealthy {
		t.Fatalf("State() = %s, want healthy (proxy to FSM initial state)", r.State())
	}
}

// TestReconcilerLockAcquire proves a single reconciler eventually
// acquires the redsync lock and IsLeader() flips true. Drives the Run
// loop and waits for the first lock-acquire tick.
func TestReconcilerLockAcquire(t *testing.T) {
	r, _, _ := newReconcilerForTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(done)
	}()

	// Wait up to 1s for leadership; tick is 50ms so first acquire should
	// be within ~100ms.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if r.IsLeader() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !r.IsLeader() {
		t.Fatalf("reconciler did not acquire leadership within 1s")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("Run did not return after ctx cancel")
	}
}

// TestReconcilerSeparateUnlockCtx proves Pitfall 8 enforcement: when the
// parent context is cancelled, the deferred Unlock uses a SEPARATE
// context.Background() with timeout — so the shutdown unlocks the key
// in Redis even though the parent ctx is already context.Canceled.
func TestReconcilerSeparateUnlockCtx(t *testing.T) {
	r, _, mr := newReconcilerForTest(t)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(done)
	}()

	// Wait for leadership so we know the key is held.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if r.IsLeader() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !r.IsLeader() {
		t.Fatalf("could not acquire leadership; cannot test unlock path")
	}

	// Confirm the key exists in Redis BEFORE cancel.
	if !mr.Exists(redisx.EmergLockKey()) {
		t.Fatalf("expected %q to exist in Redis while leader holds lock", redisx.EmergLockKey())
	}

	// Cancel parent ctx — Run() must Unlock via a separate background ctx.
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("Run did not return after ctx cancel (did Unlock block?)")
	}

	// After graceful shutdown, the lock key should be released. miniredis
	// doesn't tick TTL automatically; an explicit Unlock that uses a
	// non-cancelled ctx is the only way the key disappears within 3s.
	if mr.Exists(redisx.EmergLockKey()) {
		t.Fatalf("expected %q to be DEL after graceful shutdown (Pitfall 8: separate Unlock ctx)", redisx.EmergLockKey())
	}
}

// TestReconcilerExtendCadence proves the elapsed-based renew gate works:
// runOneTick called repeatedly should NOT call ExtendContext until the
// 10-second elapsed window has passed. Asserts lastExtendUnix advances
// only on the renew tick.
//
// We cannot mock the mutex directly (no interface seam) so we drive the
// reconciler with a real miniredis lock and inspect the lastExtendUnix
// counter. The first acquire-tick records lastExtendUnix=now. A second
// tick within <10s elapsed must NOT reset lastExtendUnix (no Extend
// fired). A simulated tick at now+11s WILL fire Extend (lastExtendUnix
// advances).
func TestReconcilerExtendCadence(t *testing.T) {
	r, _, _ := newReconcilerForTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Build the same mutex Run() uses, so we can drive runOneTick directly.
	mtx := r.deps.Redsync.NewMutex(redisx.EmergLockKey(), defaultMutexOptions()...)

	t0 := time.Unix(1_700_000_000, 0)
	r.runOneTick(ctx, mtx, t0, slog.New(slog.DiscardHandler))
	if !r.IsLeader() {
		t.Fatalf("expected leadership after first tick")
	}
	firstExtend := r.lastExtendUnix.Load()
	if firstExtend != t0.Unix() {
		t.Fatalf("lastExtendUnix = %d, want %d (set on initial acquire)", firstExtend, t0.Unix())
	}

	// Tick at t0+5s — under the 10s renew window; lastExtendUnix MUST NOT
	// advance and ExtendContext MUST NOT have been called.
	r.runOneTick(ctx, mtx, t0.Add(5*time.Second), slog.New(slog.DiscardHandler))
	if r.lastExtendUnix.Load() != firstExtend {
		t.Fatalf("lastExtendUnix advanced under 10s elapsed window: got %d, want %d",
			r.lastExtendUnix.Load(), firstExtend)
	}

	// Tick at t0+11s — over the 10s renew window; ExtendContext MUST fire
	// and lastExtendUnix MUST advance.
	r.runOneTick(ctx, mtx, t0.Add(11*time.Second), slog.New(slog.DiscardHandler))
	if r.lastExtendUnix.Load() != t0.Add(11*time.Second).Unix() {
		t.Fatalf("lastExtendUnix did not advance after renew: got %d, want %d",
			r.lastExtendUnix.Load(), t0.Add(11*time.Second).Unix())
	}
}

// TestReconcilerCedeOnExtendFail proves Pitfall 4 enforcement: when
// ExtendContext returns (false, _) — simulated by deleting the lock key
// from Redis behind the reconciler's back — the reconciler cedes
// leadership (isLeader -> false) on the next renew tick.
//
// Setup: acquire lock in tick 1, manually DEL the key in miniredis,
// drive a renew-window tick, assert isLeader==false.
func TestReconcilerCedeOnExtendFail(t *testing.T) {
	r, _, mr := newReconcilerForTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mtx := r.deps.Redsync.NewMutex(redisx.EmergLockKey(), defaultMutexOptions()...)
	t0 := time.Unix(1_700_000_000, 0)
	r.runOneTick(ctx, mtx, t0, slog.New(slog.DiscardHandler))
	if !r.IsLeader() {
		t.Fatalf("expected leadership after first tick")
	}

	// Simulate quorum loss / external DEL: remove the key. Next Extend
	// MUST return (_, ErrLockAlreadyExpired) which is NOT (true, nil) ->
	// reconciler must cede leadership per Pitfall 4.
	mr.Del(redisx.EmergLockKey())

	r.runOneTick(ctx, mtx, t0.Add(11*time.Second), slog.New(slog.DiscardHandler))
	if r.IsLeader() {
		t.Fatalf("reconciler should have ceded leadership after Extend failed (Pitfall 4)")
	}
}
