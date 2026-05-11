// Package shed (subscribe.go): cross-replica Pub/Sub consumer goroutine
// + boot rehydration helper (CONTEXT.md D-C3, RESEARCH Pitfall 3).
//
// Subscribe reads gw:shed:events and applies each remote ShedEvent to
// the local Set.remoteState overlay. This does NOT force the in-process
// FSM to the remote state — convergence between replicas is handled by
// the periodic reconcile loop in reconcile.go. The remoteState overlay
// is used by gatewayctl shed-state for the "peer reports …" dashboard
// line and by reconcile.go to detect divergence.
//
// HydrateFromRedis is the boot-time companion: it HGETALLs every managed
// upstream once at startup and seeds remoteState from the Hash mirror.
// This closes the "new replica sees no remote state until next event"
// gap left by lossy Pub/Sub (RESEARCH Pitfall 3 mitigation #1).
//
// Pattern is a 1:1 copy of gateway/internal/breaker/subscribe.go:
// reconnect-with-backoff (1s after PubSub channel drops), graceful
// ctx.Done() handling, malformed payloads logged + ignored.
package shed

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

// Subscribe blocks reading from the shed events channel. On Redis
// disconnects or PubSub channel drops, the loop reconnects after 1s
// backoff. Intended to be called via `go set.Subscribe(rootCtx, rdb)`
// from main.go wiring AFTER HydrateFromRedis completes.
//
// When rdb is nil (Redis disabled), returns immediately and emits a
// single WARN — the FSM still operates in-process.
func (s *Set) Subscribe(ctx context.Context, rdb *redis.Client) {
	if rdb == nil {
		s.log.Warn("shed subscribe disabled — no Redis client")
		return
	}
	log := s.log.With("subsystem", "subscribe")
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		ps := redisx.SubscribeShedEvents(ctx, rdb)
		ch := ps.Channel()
		drained := false
		for !drained {
			select {
			case <-ctx.Done():
				_ = ps.Close()
				return
			case msg, ok := <-ch:
				if !ok {
					drained = true
					break
				}
				var ev redisx.ShedEvent
				if err := json.Unmarshal([]byte(msg.Payload), &ev); err != nil {
					log.Warn("malformed shed event", "payload", msg.Payload, "err", err)
					continue
				}
				state := parseState(ev.State)
				s.ApplyRemoteEvent(ev.Upstream, state)
				log.Debug("applied remote shed event",
					"upstream", ev.Upstream, "state", ev.State, "reason", ev.Reason)
			}
		}
		_ = ps.Close()
		log.Warn("shed pubsub channel closed; reconnecting")
		select {
		case <-ctx.Done():
			return
		case <-time.After(1 * time.Second):
		}
	}
}

// parseState maps the wire state string ("off" | "armed" | "on" |
// "recovering") to the typed State int32. Unknown / empty inputs map to
// StateOff so a publisher bug cannot drive remoteState to a garbage
// value — the next valid event will correct it.
func parseState(s string) State {
	switch s {
	case "off":
		return StateOff
	case "armed":
		return StateArmed
	case "on":
		return StateOn
	case "recovering":
		return StateRecovering
	}
	return StateOff
}

// HydrateFromRedis reads gw:shed:{upstream} for every currently-managed
// upstream and populates remoteState. MUST be called ONCE at boot,
// BEFORE Subscribe starts, so the replica knows the cluster-wide view
// before the first event arrives (RESEARCH Pitfall 3 mitigation).
//
// Does NOT alter in-process FSMs — remote state is advisory only
// (CONTEXT.md D-C3). The in-process FSM converges to its own evaluation
// of the local signals; the dashboard surfaces remote/local divergence
// via gatewayctl shed-state.
//
// nil rdb is a silent no-op (Redis-disabled mode).
func (s *Set) HydrateFromRedis(ctx context.Context, rdb *redis.Client, log *slog.Logger) {
	if rdb == nil {
		return
	}
	if log == nil {
		log = slog.Default()
	}
	log = log.With("module", "SHED_HYDRATE")
	for _, name := range s.Names() {
		m, err := redisx.ReadShedState(ctx, rdb, name)
		if err != nil {
			log.Warn("hydrate: read failed", "upstream", name, "err", err)
			continue
		}
		if m == nil {
			continue
		}
		state := parseState(m["state"])
		s.ApplyRemoteEvent(name, state)
		log.Debug("hydrate: applied remote state", "upstream", name, "state", state.String())
	}
}
