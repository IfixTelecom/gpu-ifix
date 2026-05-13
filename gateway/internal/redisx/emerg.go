// Package redisx (emerg.go): helpers for the cross-replica emergency
// pod mirror introduced by Phase 6 plan 06-03 (CONTEXT.md D-B1, D-B2).
//
// Authoritative emergency FSM state lives in-process in
// gateway/internal/emerg.FSM; these helpers only persist a *mirror*
// in Redis (Hash `gw:emerg:state` + Pub/Sub `gw:emerg:events`) so
// other replicas + gatewayctl can observe the live FSM without
// reaching into the leader's process memory.
//
// `gw:emerg:lock` is a redsync v4 distributed mutex (D-B2: TTL 30s,
// renew every 10s = 1/3 TTL); the leader-elected reconciler in
// internal/emerg holds it. NewEmergRedsync wraps go-redsync v4 so
// callers do not import redsyncredis/v9 directly — single point of
// truth for the goredis pool adapter.
//
// All helpers use the shared 2-second redisOpTimeout (declared in
// shed.go) — Redis SHOULD NEVER block the FSM hot path. Callers ignore
// errors at the hot path level and bump the
// `gateway_breaker_mirror_failures_total` Prometheus counter at the
// publishTransition site (mirror philosophy from breaker.go +
// shed.go).
package redisx

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/go-redsync/redsync/v4"
	redsyncredis "github.com/go-redsync/redsync/v4/redis/goredis/v9"
	"github.com/redis/go-redis/v9"
)

const (
	// EmergEventsChannel is the Pub/Sub channel name for emergency FSM
	// transitions and lifecycle events. Cross-replica subscribers
	// consume this channel and feed events into the local view via
	// internal/emerg/subscribe.go (added in Plan 04).
	EmergEventsChannel = "gw:emerg:events"

	// emergStateKeyPrefix + "state" is the single Hash key holding the
	// authoritative-replica's mirror of the live FSM. Single Hash (5
	// fields) — NOT one Hash per upstream like shed.go — because there
	// is only ever 1 emergency lifecycle live (PRV-05 invariant).
	emergStateKeyPrefix = "gw:emerg:"

	// emergLockKey is the redsync v4 distributed mutex key. The
	// leader-elected reconciler holds it; non-leader replicas observe
	// state via Pub/Sub.
	emergLockKey = "gw:emerg:lock"

	// redisOpTimeout is intentionally NOT redeclared here — the
	// package-level constant lives in shed.go (= 2 * time.Second).
	// Re-declaring would be a compile error AND a divergence risk if
	// the value ever changes.
)

// EmergStateKey returns the canonical "gw:emerg:state" Hash key.
// Wrapped in a function (vs an exported const) to mirror ShedStateKey
// and to leave room for a per-tenant key-shard rollout in v2 without
// breaking the 35+ callsites projected for Plan 04+.
func EmergStateKey() string { return emergStateKeyPrefix + "state" }

// EmergLockKey returns the redsync mutex key. Exposed via getter so
// gatewayctl can pretty-print "leader holds gw:emerg:lock" without
// reaching into the unexported constant.
func EmergLockKey() string { return emergLockKey }

// EmergEvent is the JSON payload published on EmergEventsChannel
// whenever a local FSM transitions or a lifecycle event fires
// (cancel-in-flight, lifecycle-close). Kept flat + small so the
// JSON unmarshal cost is negligible on the (rare) high-frequency
// transitions during recovery storms.
//
// Type values:
//   - "transition"       — FSM state change (state, reason, lifecycle_id)
//   - "cancel_in_flight" — leader cancelled mid-provisioning (lifecycle_id)
//   - "lifecycle_close"  — lifecycle ended (lifecycle_id, reason)
//
// Payload is map[string]any so callers can ride per-Type extensions
// (e.g., {offer_id, dph} on accept) without breaking the schema.
// CONTEXT D-D3 + threat T-6-W3-03: Payload MUST contain only
// non-sensitive IDs (Vast offer_id, instance_id) — never request bodies
// or PII. Convention enforced via code review.
type EmergEvent struct {
	Type        string         `json:"type"`
	State       string         `json:"state"`
	LifecycleID int64          `json:"lifecycle_id,omitempty"`
	Reason      string         `json:"reason,omitempty"`
	SinceUnix   int64          `json:"since_unix"`
	ReplicaID   string         `json:"replica_id"`
	Payload     map[string]any `json:"payload,omitempty"`
}

// WriteEmergState mirrors the current FSM state to Redis as a Hash
// with five fields: state, lifecycle_id, pod_url, pod_instance_id,
// entered_at. Best-effort with a 2-second timeout; callers log
// failures via gateway_breaker_mirror_failures_total and continue
// with the in-process FSM (D-C3 fail-soft philosophy).
//
// Returns an error on nil client so wiring bugs (mirror constructor
// invoked before NewClient) fail loud at test time.
func WriteEmergState(ctx context.Context, rdb *redis.Client, state, lifecycleID, podURL, podInstanceID string, enteredUnix int64) error {
	if rdb == nil {
		return fmt.Errorf("redisx: nil client")
	}
	ctx, cancel := context.WithTimeout(ctx, redisOpTimeout)
	defer cancel()
	return rdb.HSet(ctx, EmergStateKey(), map[string]any{
		"state":           state,
		"lifecycle_id":    lifecycleID,
		"pod_url":         podURL,
		"pod_instance_id": podInstanceID,
		"entered_at":      enteredUnix,
	}).Err()
}

// PublishEmergEvent marshals the event JSON and PUBLISHes to
// EmergEventsChannel. 2-second timeout; failures increment the
// mirror-failures counter at the call site.
func PublishEmergEvent(ctx context.Context, rdb *redis.Client, ev EmergEvent) error {
	if rdb == nil {
		return fmt.Errorf("redisx: nil client")
	}
	ctx, cancel := context.WithTimeout(ctx, redisOpTimeout)
	defer cancel()
	payload, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	return rdb.Publish(ctx, EmergEventsChannel, payload).Err()
}

// SubscribeEmergEvents returns a *redis.PubSub attached to
// EmergEventsChannel. The caller owns the PubSub and MUST Close() it
// on shutdown or reconnect — the subscribe.go consumer loop (Plan 04)
// handles reconnect semantics with a 1-second backoff (mirrors
// breaker/subscribe.go).
func SubscribeEmergEvents(ctx context.Context, rdb *redis.Client) *redis.PubSub {
	return rdb.Subscribe(ctx, EmergEventsChannel)
}

// NewEmergRedsync wraps go-redsync v4 with the goredis/v9 pool adapter
// and returns a *redsync.Redsync ready to mint mutexes for emergLockKey.
// Single point of truth for the adapter import — Plan 04 callers use
//
//	rs := redisx.NewEmergRedsync(rdb)
//	mtx := rs.NewMutex(redisx.EmergLockKey(),
//	    redsync.WithExpiry(30*time.Second),
//	    redsync.WithTries(1),
//	    redsync.WithRetryDelay(0),
//	)
//
// without ever importing "github.com/go-redsync/redsync/v4/redis/goredis/v9"
// themselves.
func NewEmergRedsync(rdb *redis.Client) *redsync.Redsync {
	pool := redsyncredis.NewPool(rdb)
	return redsync.New(pool)
}
