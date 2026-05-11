// Package shed (mirror.go): publishing helper that mirrors a local FSM
// transition into Redis (HSET gw:shed:{upstream} + PUBLISH gw:shed:events)
// for cross-replica visibility (CONTEXT.md D-C3).
//
// Pattern follows gateway/internal/breaker/breaker.go publishTransition:
// best-effort, fire-and-forget, silent fallback on Redis failure with
// the GatewayShedMirrorFailures counter bumped. The in-process FSM is
// NEVER blocked by Redis I/O — failures keep the FSM operating with
// stale mirror state until reconcile.go closes the gap.
//
// The publishTransition closure is wired into Set.onChange at Set
// construction (Plan 05-06 main.go). Internally the closure dispatches
// to a small bounded worker pool (WR-03) so a transition storm cannot
// spawn unbounded goroutines all racing on Redis I/O with 2s timeouts.
// Backpressure is signalled by GatewayShedMirrorDropped rather than
// blocking the FSM tick.
package shed

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

// PublishTransitionFunc is the goroutine-suitable callback wired into
// Set.onChange. It HSETs gw:shed:{upstream} and PUBLISHes the event
// back-to-back. Errors increment GatewayShedMirrorFailures but DO NOT
// affect in-process FSM operation.
//
// Sig may be nil for synthetic transitions (operator override, remote
// event apply) where the publisher could not capture the signal snapshot.
type PublishTransitionFunc func(upstream string, to State, reason string, sig *redisx.ShedEventSignals)

// publishJob is the channel payload consumed by the bounded worker pool.
type publishJob struct {
	upstream string
	to       State
	reason   string
	sig      *redisx.ShedEventSignals
}

// mirrorPublishWorkers is the number of goroutines draining the publish
// queue. 2 workers is enough for the natural per-upstream FSM transition
// rate (1-2/day per upstream under healthy load; bursts during incidents
// bounded by hysteresis). Keeping the count small keeps Redis connection
// pool pressure flat during flapping incidents.
const mirrorPublishWorkers = 2

// mirrorPublishQueueDepth is the buffered channel depth. 64 is large
// enough to absorb a short flap burst across all upstreams without
// dropping; once full, additional transitions bump
// GatewayShedMirrorDropped instead of spawning goroutines (WR-03).
const mirrorPublishQueueDepth = 64

// MakePublishTransition constructs a closure bound to rdb plus a small
// background worker pool that drains the publish queue. Called once at
// Set construction in main.go wiring (Plan 05-06).
//
// When rdb is nil (Redis disabled by env or constructor invoked before
// NewClient succeeded), returns a no-op publisher so callers do not
// need to nil-check at every transition — the FSM continues to operate
// in-process exactly as if Redis were down (D-C3 fail-open). No worker
// goroutines are spawned in the nil case.
//
// Worker goroutines are intentionally leaked at process exit — the
// publish queue is unbuffered after the closure returns and a graceful
// shutdown should drain pending jobs naturally before the process
// terminates. Callers that need an explicit Stop hook should add one;
// none was wired in Phase 5.
func MakePublishTransition(rdb *redis.Client) PublishTransitionFunc {
	if rdb == nil {
		return func(upstream string, to State, reason string, sig *redisx.ShedEventSignals) {}
	}
	jobs := make(chan publishJob, mirrorPublishQueueDepth)
	for i := 0; i < mirrorPublishWorkers; i++ {
		go func() {
			for j := range jobs {
				doPublishTransition(rdb, j.upstream, j.to, j.reason, j.sig)
			}
		}()
	}
	return func(upstream string, to State, reason string, sig *redisx.ShedEventSignals) {
		select {
		case jobs <- publishJob{upstream: upstream, to: to, reason: reason, sig: sig}:
		default:
			// Worker pool saturated — drop this transition's mirror
			// publish. The in-process FSM is authoritative (D-C3);
			// the periodic reconcile loop (reconcile.go) will close
			// the convergence gap on the next sweep. Bumping the
			// counter lets dashboards detect flapping (WR-03).
			obs.GatewayShedMirrorDropped.Inc()
		}
	}
}

// doPublishTransition performs the actual HSET + PUBLISH back-to-back.
// Extracted so worker goroutines and tests can call the same code path.
func doPublishTransition(rdb *redis.Client, upstream string, to State, reason string, sig *redisx.ShedEventSignals) {
	ctx := context.Background()
	now := time.Now().Unix()
	if err := redisx.WriteShedState(ctx, rdb, upstream, to.String(), reason, now, sig); err != nil {
		obs.GatewayShedMirrorFailures.Inc()
	}
	ev := redisx.ShedEvent{
		Upstream:  upstream,
		State:     to.String(),
		SinceUnix: now,
		Reason:    reason,
		Signals:   sig,
	}
	if err := redisx.PublishShedEvent(ctx, rdb, ev); err != nil {
		obs.GatewayShedMirrorFailures.Inc()
	}
}
