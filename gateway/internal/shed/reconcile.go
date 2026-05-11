// Package shed (reconcile.go): periodic HGETALL loop that closes the
// convergence gap left by lossy Pub/Sub (RESEARCH Pitfall 3
// mitigation #2). Forward-compat for Phase 6 multi-replica — in Phase 5
// single-replica this is a sanity net.
//
// Every reconcileInterval (default 30s), the loop reads gw:shed:{upstream}
// for every managed upstream and compares with the local remoteState
// overlay. Divergence increments
// gateway_shed_mirror_reconcile_total{result="diverged"} and corrects
// remoteState to match the Redis view. Agreement increments
// {result="ok"}. Errors increment {result="error"}.
//
// This does NOT alter in-process FSM state — only the remoteState
// overlay used by gatewayctl shed-state and the Phase 7 dashboard.
// The in-process FSM converges via its own Evaluate path on local
// signals (CONTEXT.md D-C3 authoritativeness invariant).
package shed

import (
	"context"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

// DefaultReconcileInterval is the default HGETALL cadence. 30s is
// CONTEXT.md D-C3 + RESEARCH Pitfall 3 — short enough that divergence
// during a missed Pub/Sub event resolves within a single dashboard
// refresh cycle, long enough that 3 HGETALL/30s is negligible Redis load.
const DefaultReconcileInterval = 30 * time.Second

// ReconcileLoop blocks until ctx is cancelled, running one
// reconcileOnce pass per `interval`. Intended to be started as
// `go set.ReconcileLoop(rootCtx, rdb, 30*time.Second, log)` from
// main.go wiring (Plan 05-06) alongside Subscribe and RunTicker.
//
// nil rdb returns immediately (Redis-disabled mode). interval <= 0
// falls back to DefaultReconcileInterval.
func (s *Set) ReconcileLoop(ctx context.Context, rdb *redis.Client, interval time.Duration, log *slog.Logger) {
	if rdb == nil {
		return
	}
	if interval <= 0 {
		interval = DefaultReconcileInterval
	}
	if log == nil {
		log = slog.Default()
	}
	log = log.With("module", "SHED_RECONCILE")
	t := time.NewTicker(interval)
	defer t.Stop()
	log.Info("shed reconcile loop started", "interval", interval)
	for {
		select {
		case <-ctx.Done():
			log.Info("reconcile loop stopping")
			return
		case <-t.C:
			s.reconcileOnce(ctx, rdb, log)
		}
	}
}

// reconcileOnce executes a single sweep: for every managed upstream,
// read gw:shed:{upstream}, compare with remoteState, correct on
// divergence. Each comparison emits exactly one
// gateway_shed_mirror_reconcile_total label increment so the counter
// rate equals the upstream-count × tick rate when everything is fine.
func (s *Set) reconcileOnce(ctx context.Context, rdb *redis.Client, log *slog.Logger) {
	diverged := 0
	ok := 0
	for _, name := range s.Names() {
		m, err := redisx.ReadShedState(ctx, rdb, name)
		if err != nil {
			obs.GatewayShedMirrorReconcile.WithLabelValues("error").Inc()
			log.Warn("reconcile: read failed", "upstream", name, "err", err)
			continue
		}
		if m == nil {
			// No record in Redis yet — treat as ok (first boot; the
			// local FSM has not yet produced a transition either).
			obs.GatewayShedMirrorReconcile.WithLabelValues("ok").Inc()
			ok++
			continue
		}
		redisState := parseState(m["state"])
		currentRemote, hasRemote := s.RemoteState(name)
		if !hasRemote || currentRemote != redisState {
			s.ApplyRemoteEvent(name, redisState)
			obs.GatewayShedMirrorReconcile.WithLabelValues("diverged").Inc()
			diverged++
			log.Debug("reconcile: remote state corrected",
				"upstream", name, "from", currentRemote.String(), "to", redisState.String())
			continue
		}
		obs.GatewayShedMirrorReconcile.WithLabelValues("ok").Inc()
		ok++
	}
	if diverged > 0 {
		log.Info("reconcile completed with divergence", "diverged", diverged, "ok", ok)
	}
}
