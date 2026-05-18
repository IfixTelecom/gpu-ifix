// Package emerg (subscribe.go): Plan 06-05 Pub/Sub consumer for the two
// emergency-related Redis channels.
//
// Subscribe consumes gw:breaker:events (Phase 3 D-D1) and feeds each
// event into the per-replica localLlmTracker so the reconciler trigger
// gate can arm without polling the breaker hot path.
//
// SubscribeEmergCommands consumes gw:emerg:events (Phase 6 D-B1) and
// dispatches force-provision / force-destroy commands published by
// gatewayctl (Plan 06-10 BLOCKER 2 fix). Leader-only filtering happens
// inside applyEmergCommand — non-leader replicas observe events
// transparently for visibility but do NOT mutate state.
//
// Both consumers share the same reconnect-with-1s-backoff loop pattern
// replicated from gateway/internal/breaker/subscribe.go (the canonical
// Pub/Sub-with-redis-go pattern in this codebase). The consumers are
// spawned BEFORE the reconciler ticker (W11 ordering invariant — see
// reconciler.Run) so that a publish that arrives during boot is not
// silently lost (Pub/Sub is at-most-once and has no replay).
package emerg

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

// Subscribe consumes gw:breaker:events and feeds each event into the
// reconciler's tracker. Exits on ctx cancel. On channel drop, reconnects
// with 1s backoff. Designed to be invoked once at boot inside its own
// goroutine (`go r.Subscribe(rootCtx)` from reconciler.Run).
//
// Malformed JSON payloads are logged at Warn and dropped — they do NOT
// crash the loop (Threat T-6-W5-02: an operator publishing via redis-cli
// with a typo must not take down the trigger).
func (r *Reconciler) Subscribe(ctx context.Context) {
	log := r.deps.Log.With("subsystem", "emerg.subscribe")
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		ps := redisx.SubscribeBreakerEvents(ctx, r.deps.Redis)
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
				var ev redisx.BreakerEvent
				if err := json.Unmarshal([]byte(msg.Payload), &ev); err != nil {
					log.Warn("malformed breaker event", "payload", msg.Payload, "err", err)
					continue
				}
				r.tracker.ApplyEvent(ev)
				log.Debug("applied breaker event",
					"upstream", ev.Upstream, "state", ev.State)
			}
		}
		_ = ps.Close()
		log.Warn("breaker pubsub channel closed; reconnecting")
		select {
		case <-ctx.Done():
			return
		case <-time.After(1 * time.Second):
		}
	}
}

// SubscribeEmergCommands consumes gw:emerg:events and dispatches typed
// commands (force_provision_request, force_destroy_request) via
// applyEmergCommand. Self-published transition events (from this
// reconciler's own FSM onChange callback) are visibility-only on the
// non-leader path and ignored on the leader path — they exist for
// gatewayctl + cross-replica observation, not for action.
//
// Reconnect-with-backoff loop is identical to Subscribe (shared pattern
// from breaker/subscribe.go). Exits on ctx cancel; reconnects on channel
// drop after 1s backoff.
func (r *Reconciler) SubscribeEmergCommands(ctx context.Context) {
	log := r.deps.Log.With("subsystem", "emerg.commands")
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		ps := redisx.SubscribeEmergEvents(ctx, r.deps.Redis)
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
				var ev redisx.EmergEvent
				if err := json.Unmarshal([]byte(msg.Payload), &ev); err != nil {
					log.Warn("malformed emerg event", "payload", msg.Payload, "err", err)
					continue
				}
				r.applyEmergCommand(ctx, ev, log)
			}
		}
		_ = ps.Close()
		log.Warn("emerg pubsub channel closed; reconnecting")
		select {
		case <-ctx.Done():
			return
		case <-time.After(1 * time.Second):
		}
	}
}

// SubscribePrimaryEvents consumes gw:primary:events (the Phase 6.6 primary
// reconciler's Pub/Sub channel) and resolves Pitfall #11 (emerg + primary
// double-provision race / coexistence) using the Option B "primary
// precedence" handoff:
//
//   - Phase 6.6 RESEARCH.md Pitfall #11 — when the primary pod transitions
//     into StateReady (publishing a `primary_ready` event), an emergency
//     pod that happens to be running at the same time (e.g. an emerg
//     lifecycle that fired immediately before the schedule window opened)
//     becomes redundant. The emerg leader replica force-destroys its
//     active lifecycle so a single tier-0 LLM pod serves the peak window.
//
// Leader-only filter: non-leader emerg replicas drop events silently to
// keep log volume manageable (the leader is authoritative for FSM
// transitions; non-leaders observe via gw:emerg:events Pub/Sub instead).
//
// Reconnect-with-backoff loop is identical to SubscribeEmergCommands /
// Subscribe (shared pattern from breaker/subscribe.go). Exits on ctx
// cancel; reconnects on channel drop after 1s backoff.
//
// Spawned from gateway/cmd/gateway/main.go Plan 06.6-08 wiring as a
// separate goroutine in lock-step with primary.Reconciler.Start so a
// primary_ready publish that arrives during the boot gap is observed by
// this subscriber.
func (r *Reconciler) SubscribePrimaryEvents(ctx context.Context) {
	log := r.deps.Log.With("subsystem", "emerg.primary_events")
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		ps := redisx.SubscribePrimaryEvents(ctx, r.deps.Redis)
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
				var ev redisx.PrimaryEvent
				if err := json.Unmarshal([]byte(msg.Payload), &ev); err != nil {
					log.Warn("malformed primary event", "payload", msg.Payload, "err", err)
					continue
				}
				// Leader-only gate (PRV-03 single-leader invariant parity
				// with applyEmergCommand). Non-leaders observe but do NOT
				// mutate emerg state. Drop silently to keep log volume
				// manageable.
				if !r.isLeader.Load() {
					continue
				}
				switch ev.Type {
				case "primary_ready":
					// Pitfall #11 Option B — primary precedence handoff.
					// Only fires when emerg FSM is currently in
					// EmergencyActive (i.e. an emerg pod is actively
					// serving). Other emerg states (Healthy / Cooldown
					// / Recovering / Provisioning) are no-op: there is
					// no active emerg lifecycle to force-destroy, OR the
					// emerg path is already in the natural cutback /
					// destroy sequence.
					//
					// Force-destroy sequence (matches handleForceDestroy
					// in reconciler.go, which is the operator-initiated
					// analog):
					//   1. cancelActiveLifecycle — publishes Layer 2
					//      `cancel_in_flight` event on gw:emerg:events for
					//      cross-replica observers + cancels any lingering
					//      provisioning ctx (idempotent no-op when the
					//      goroutine already exited after markHealthy).
					//   2. destroyAndCloseLifecycle — actually destroys
					//      the Vast.ai instance + closes the lifecycle
					//      row. cancelActiveLifecycle ALONE does NOT
					//      destroy an EmergencyActive pod: Layer 3 is
					//      gated on the provisioning goroutine's
					//      ctx.Done() branch which has already exited.
					//   3. enterCooldown — transitions FSM
					//      EmergencyActive → Cooldown (normal cutback
					//      suppression window applies; fromFailure=false).
					if r.deps.FSM.State() == StateEmergencyActive {
						lc := r.activeLifecycle.Load()
						log.Info("primary_ready while emerg active; force-destroying emerg lifecycle (Pitfall #11)",
							"primary_lifecycle_id", ev.LifecycleID,
							"by_replica", ev.ReplicaID)
						// Layer 1+2 — ctx cancel + Pub/Sub broadcast.
						r.cancelActiveLifecycle(ctx, "primary_took_over")
						// Layer 3 — actual Vast destroy + DB close. Guard
						// against the lifecycle being nil (defensive: a
						// concurrent close race would surface as lc==nil).
						if lc != nil {
							if err := r.destroyAndCloseLifecycle(ctx, lc, "primary_took_over"); err != nil {
								log.Error("primary_took_over: destroyAndCloseLifecycle failed",
									"lifecycle_id", lc.ID,
									"err", err)
							}
						}
						// FSM transition. fromFailure=false — Pitfall #11
						// is a deliberate handoff, not a provisioning
						// failure; the shorter ProvisionHealthyDurationSeconds
						// suppression window applies.
						r.enterCooldown(StateEmergencyActive, time.Now(),
							"primary_took_over", false)
					} else {
						log.Debug("primary_ready observed; emerg not active",
							"emerg_state", r.deps.FSM.State().String(),
							"primary_lifecycle_id", ev.LifecycleID)
					}
				default:
					// Other primary event types (schedule_up_fired,
					// provisioning_started, draining_started, destroyed,
					// force_up_request, force_down_request,
					// cancel_in_flight) are not actionable by the emerg
					// reconciler — they are primary-internal state
					// transitions. Drop silently.
				}
			}
		}
		_ = ps.Close()
		log.Warn("primary pubsub channel closed; reconnecting")
		select {
		case <-ctx.Done():
			return
		case <-time.After(1 * time.Second):
		}
	}
}
