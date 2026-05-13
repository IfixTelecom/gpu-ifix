// Package emerg (reconciler.go): Plan 06-04 leader-elected reconciler
// loop. Drives the 7-state emergency FSM at 1Hz (configurable via
// Deps.TickInterval for tests) inside a redsync v4 distributed mutex
// (CONTEXT.md D-B2: TTL 30s, renew every 10s = 1/3 TTL).
//
// PRV-03 ("Apenas o leader avança o FSM") rests entirely on this file.
// The reconciler exposes a tiny public surface so downstream plans
// (05 trigger, 06 provisioning, 07 cancel/recovery, 08 cutback) only
// need IsLeader() / State() / ReplicaID() to gate their actions.
//
// Pitfall enforcement (RESEARCH.md Pitfall 4 + 8):
//
//   - Pitfall 4: redsync.Mutex.ExtendContext returns (bool, error). The
//     production code checks both — ANY combination other than (true,
//     nil) is treated as lost leadership. Quietly returning `(false,
//     nil)` (single-Redis quorum nuance) would otherwise cause two
//     replicas to think they hold the lock simultaneously and BOTH
//     advance the FSM → split-brain → DB unique-index violation.
//
//   - Pitfall 8: when the parent ctx is cancelled, `defer mutex.UnlockContext(ctx)`
//     is a footgun — UnlockContext fails immediately because of the
//     cancelled ctx. We use a SEPARATE context.Background() with a 2s
//     timeout for the graceful release path. Failures are ignored
//     (TTL=30s catches anything missed).
//
// evaluateTick is intentionally a no-op stub in this plan. Plans 05-08
// extend it incrementally — each downstream plan owns one transition
// branch (trigger / provision / cancel / cutback). Keeping the stub
// here is the deliberate seam.
package emerg

import (
	"context"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	"github.com/go-redsync/redsync/v4"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

// emergLockExpiry is the redsync mutex TTL (CONTEXT.md D-B2). Constant
// at 30s — drift-tolerant, survives a Pub/Sub blip + Redis pause without
// a leader losing its lease. If ops needs to retune in the field, add an
// env var; for now operators rely on the default.
const emergLockExpiry = 30 * time.Second

// emergLockRenewInterval is how often the leader extends the lease while
// it holds leadership. Set to 1/3 of emergLockExpiry so two consecutive
// Pub/Sub blips can be absorbed without losing the lease.
const emergLockRenewInterval = 10 * time.Second

// Deps is the dependency bundle injected into NewReconciler. Caller-
// owned construction so tests can pass a miniredis-backed redis.Client
// + a stub FSM, and the production wiring (Plan 09 main.go) passes
// real instances.
//
// All fields are required EXCEPT TickInterval (defaults to 1s), Log
// (defaults to slog.Default), Redsync (auto-built from Redis when
// nil) and DB (Plan 04 only — evaluateTick is a stub; downstream plans
// require a real *pgxpool.Pool).
type Deps struct {
	// DB is the Postgres pool used by Plans 05-08 for lifecycle DB
	// queries (orphan recovery, lifecycle insert/close, monthly cost
	// aggregate). Plan 04 does not exercise the DB path — leave nil
	// in tests that only verify the leader-election semantics.
	DB *pgxpool.Pool

	// Redis is the go-redis v9 client. MUST be non-nil — used for
	// redsync mutex construction and (downstream plans) the Pub/Sub
	// + Hash mirror of the FSM state.
	Redis *redis.Client

	// Redsync is the go-redsync v4 instance used to mint the leader
	// mutex. When nil, NewReconciler wraps Deps.Redis via
	// redisx.NewEmergRedsync — single point of truth for the
	// goredis/v9 pool adapter.
	Redsync *redsync.Redsync

	// FSM is the in-process 7-state emergency FSM. MUST be non-nil —
	// State() proxies through to f.State() and Plans 05-08 transition
	// it from inside evaluateTick.
	FSM *FSM

	// Cfg holds the Phase 6 env-driven knobs (D-A1..D-D4 + the 11
	// Phase 6 fields added in Plan 06-01). Plan 04 does not consume
	// any cfg field (evaluateTick stub) but we accept it here to lock
	// the constructor signature so downstream plans can reuse without
	// breaking callers.
	Cfg config.Config

	// TickInterval is the cadence of the Run loop. <=0 defaults to 1s
	// (CONTEXT.md D-B3). Tests pass a small value (50-100ms) to
	// accelerate convergence; production uses 1s.
	TickInterval time.Duration

	// Log is the structured logger. nil defaults to slog.Default(); the
	// reconciler attaches a `subsystem=emerg.reconciler` field plus the
	// per-replica replicaID at Run start.
	Log *slog.Logger
}

// Reconciler is the leader-elected loop owner. Construct via
// NewReconciler then start with `go r.Run(ctx)`. IsLeader is safe to
// call from any goroutine (atomic.Load).
//
// q is the sqlc-generated query handle. Plan 04 does not exercise it;
// it is constructed eagerly so Plans 05-08 do not need to re-instantiate
// inside hot paths.
type Reconciler struct {
	deps           Deps
	isLeader       atomic.Bool
	lastExtendUnix atomic.Int64 // unix-seconds of the most recent successful Extend or initial Lock
	replicaID      string
	q              *gen.Queries
}

// NewReconciler constructs a Reconciler with sensible defaults applied
// in-place to the Deps struct (TickInterval, Log, Redsync). The
// replicaID is derived from os.Hostname() — in dev it surfaces the
// container ID; in prod it surfaces the pod hostname.
//
// The function does NOT validate Deps.Redis or Deps.FSM — wiring bugs
// (passing a nil client) surface as the first method call panicking,
// which is the cheaper failure mode than a nil-check labyrinth.
func NewReconciler(deps Deps) *Reconciler {
	if deps.TickInterval <= 0 {
		deps.TickInterval = 1 * time.Second
	}
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	if deps.Redsync == nil {
		deps.Redsync = redisx.NewEmergRedsync(deps.Redis)
	}
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}
	r := &Reconciler{
		deps:      deps,
		replicaID: hostname,
	}
	if deps.DB != nil {
		r.q = gen.New(deps.DB)
	}
	return r
}

// IsLeader returns true iff this replica currently holds the
// gw:emerg:lock redsync mutex. Lockless atomic.Load — safe to call from
// the request hot path (e.g., dispatcher checks before routing to the
// emergency pod).
func (r *Reconciler) IsLeader() bool {
	return r.isLeader.Load()
}

// State proxies to the in-process FSM. Lockless atomic.Load.
// Convenience for callers that already hold a *Reconciler reference.
func (r *Reconciler) State() State {
	return r.deps.FSM.State()
}

// ReplicaID returns the per-replica identifier (os.Hostname() at
// boot). Used by Plan 05+ to tag Pub/Sub events and by gatewayctl to
// pretty-print "leader=<replicaID>".
func (r *Reconciler) ReplicaID() string {
	return r.replicaID
}

// defaultMutexOptions returns the canonical CONTEXT.md D-B2 mutex
// options: TTL 30s, single Try, zero retry-delay. Pulled out of Run()
// so reconciler_test.go can build the same mutex when driving
// runOneTick directly.
func defaultMutexOptions() []redsync.Option {
	return []redsync.Option{
		redsync.WithExpiry(emergLockExpiry),
		redsync.WithTries(1),
		redsync.WithRetryDelay(0),
	}
}

// Run is the reconciler main loop. Blocks until ctx cancellation.
// MUST be started inside its own goroutine: `go r.Run(rootCtx)`.
//
// On ctx.Done(), Run releases the lock via a SEPARATE
// context.Background() with a 2s timeout (Pitfall 8) so a parent
// shutdown does not swallow the UnlockContext call.
func (r *Reconciler) Run(ctx context.Context) {
	log := r.deps.Log.With("subsystem", "emerg.reconciler", "replica", r.replicaID)
	interval := r.deps.TickInterval
	if interval <= 0 {
		interval = 1 * time.Second
	}

	mutex := r.deps.Redsync.NewMutex(redisx.EmergLockKey(), defaultMutexOptions()...)

	t := time.NewTicker(interval)
	defer t.Stop()

	log.Info("emerg reconciler started", "interval", interval, "lock_key", redisx.EmergLockKey())

	for {
		select {
		case <-ctx.Done():
			// Pitfall 8: parent ctx already cancelled; UnlockContext
			// would short-circuit if we passed `ctx` here. Use a fresh
			// background ctx with a 2s budget. Ignore the error — TTL
			// expiry catches any missed unlock.
			if r.isLeader.Load() {
				releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 2*time.Second)
				_, _ = mutex.UnlockContext(releaseCtx)
				releaseCancel()
				r.isLeader.Store(false)
			}
			log.Info("emerg reconciler stopping")
			return
		case now := <-t.C:
			r.runOneTick(ctx, mutex, now, log)
		}
	}
}

// runOneTick performs ONE leader-election + evaluate pass. Held as a
// method (not inlined inside Run) so unit tests can drive single ticks
// deterministically without spinning the goroutine.
//
// Two paths:
//
//  1. Non-leader: try LockContext. On nil error, become leader and
//     record lastExtendUnix=now (so the renew gate uses the acquire
//     time as its baseline). On any error, return — someone else holds
//     the lock; observe via Pub/Sub (Plan 05 subscribe.go).
//
//  2. Leader: if elapsed since lastExtendUnix >= emergLockRenewInterval
//     (10s), call ExtendContext. Pitfall 4: ANY combination other than
//     (true, nil) means we lost the lock — flip is_leader=false and
//     return. Next tick will re-attempt Lock.
func (r *Reconciler) runOneTick(ctx context.Context, mutex *redsync.Mutex, now time.Time, log *slog.Logger) {
	if !r.isLeader.Load() {
		if err := mutex.LockContext(ctx); err != nil {
			// Someone else holds the lock — non-leader path. We do NOT
			// log at every tick; the warn would be noisy. Plan 05 will
			// observe state via Pub/Sub instead.
			return
		}
		r.isLeader.Store(true)
		r.lastExtendUnix.Store(now.Unix())
		log.Info("acquired leadership", "fsm_state", r.deps.FSM.State().String())
		// Plan 07 wires r.recoverOrphanLifecycles(ctx) here so a fresh
		// leader (e.g., crash recovery) reconciles in-flight lifecycles
		// before evaluating new transitions.
	} else {
		// Renew gate: only call ExtendContext when 10s have elapsed since
		// the last successful extend (or initial acquire). This keeps
		// Redis traffic low (one Extend per 10s instead of per tick) and
		// matches CONTEXT.md D-B2 1/3-TTL renew cadence.
		if now.Unix()-r.lastExtendUnix.Load() >= int64(emergLockRenewInterval.Seconds()) {
			ok, err := mutex.ExtendContext(ctx)
			if err != nil || !ok {
				// Pitfall 4: any non-(true, nil) combination = lost
				// leadership. Single-Redis usually returns either
				// (true, nil) or (false, ErrLockAlreadyExpired); the
				// (false, nil) quorum nuance is rare but possible —
				// we treat ALL non-success as identical.
				log.Warn("lost leadership; ceding", "err", err, "ok", ok)
				r.isLeader.Store(false)
				return
			}
			r.lastExtendUnix.Store(now.Unix())
		}
	}

	// Leader path: evaluate FSM transitions. STUB in Plan 04.
	r.evaluateTick(ctx, now, log)
}

// evaluateTick is the FSM transition evaluation hook. Plan 04 leaves it
// as a Debug-level log so the leader path is exercised in tests without
// pulling in trigger/provisioning logic. Plans 05-08 extend this method
// incrementally:
//
//   - Plan 05 (trigger): observe local-llm breaker.OPEN sustain timer;
//     transition HEALTHY/DEGRADED → FAILED_OVER → EMERGENCY_PROVISIONING.
//   - Plan 06 (provisioning): drive Vast.ai bid + create + /health poll;
//     transition EMERGENCY_PROVISIONING → EMERGENCY_ACTIVE.
//   - Plan 07 (cancel/recovery): cancel-in-flight + leader-recovery
//     orphan reconcile.
//   - Plan 08 (cutback): RECOVERING grace + IDLE_GRACE destroy +
//     COOLDOWN suppression window.
func (r *Reconciler) evaluateTick(_ context.Context, _ time.Time, log *slog.Logger) {
	log.Debug("leader tick (stub — Plans 05-08 implement transitions)",
		"state", r.deps.FSM.State().String())
}
