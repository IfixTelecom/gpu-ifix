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
// construction (Plan 05-06 main.go). The Set fires it inside a goroutine
// to keep the FSM tick (1Hz) free of Redis I/O latency.
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

// MakePublishTransition constructs a closure bound to rdb. Called once
// at Set construction in main.go wiring (Plan 05-06).
//
// When rdb is nil (Redis disabled by env or constructor invoked before
// NewClient succeeded), returns a no-op publisher so callers do not
// need to nil-check at every transition — the FSM continues to operate
// in-process exactly as if Redis were down (D-C3 fail-open).
func MakePublishTransition(rdb *redis.Client) PublishTransitionFunc {
	if rdb == nil {
		return func(upstream string, to State, reason string, sig *redisx.ShedEventSignals) {}
	}
	return func(upstream string, to State, reason string, sig *redisx.ShedEventSignals) {
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
}
