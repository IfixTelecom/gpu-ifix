// Package redisx (breaker.go): helpers for the cross-replica circuit
// breaker mirror introduced by Phase 3 plan 03-03 (CONTEXT.md D-D1).
//
// Authoritative breaker state lives in-process in
// gateway/internal/breaker.Set; these helpers only persist a *mirror*
// in Redis (Hash `gw:breaker:{name}` + Pub/Sub `gw:breaker:events`) so
// other replicas can short-circuit known-dead upstreams without first
// learning about the failure themselves.
//
// All helpers use a 2-second timeout — Redis SHOULD NEVER block the
// gobreaker state machine. Callers ignore errors at the hot-path level
// (publishTransition runs in its own goroutine) and bump the
// `gateway_breaker_mirror_failures_total` Prometheus counter on failure.
package redisx

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"
)

// breakerEventsChannel is the Pub/Sub channel name for breaker state
// transitions. Exposed read-only via BreakerEventsChannel().
const breakerEventsChannel = "gw:breaker:events"

// BreakerEvent is the payload published to gw:breaker:events when a
// local gobreaker state transition fires. Kept flat + small so JSON
// unmarshal is cheap on high-frequency changes.
type BreakerEvent struct {
	Upstream  string `json:"upstream"`
	State     string `json:"state"`      // "closed" | "half-open" | "open"
	SinceUnix int64  `json:"since_unix"` // time.Now().Unix() at transition
	Reason    string `json:"reason,omitempty"`
}

// WriteBreakerState HSETs gw:breaker:{name} with the current state
// fields. 2-second timeout — state writes MUST NOT block the gobreaker
// state machine.
func WriteBreakerState(ctx context.Context, rdb *redis.Client, name, state string, sinceUnix int64) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return rdb.HSet(ctx, "gw:breaker:"+name, map[string]any{
		"state":      state,
		"since_unix": sinceUnix,
	}).Err()
}

// PublishBreakerEvent marshals the event JSON and publishes to
// gw:breaker:events.
func PublishBreakerEvent(ctx context.Context, rdb *redis.Client, ev BreakerEvent) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	payload, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	return rdb.Publish(ctx, breakerEventsChannel, payload).Err()
}

// SubscribeBreakerEvents returns a *redis.PubSub subscribed to
// gw:breaker:events. Caller owns Close().
func SubscribeBreakerEvents(ctx context.Context, rdb *redis.Client) *redis.PubSub {
	return rdb.Subscribe(ctx, breakerEventsChannel)
}

// BreakerEventsChannel exposes the channel name so tests can round-trip
// Publish/Subscribe without reaching into an unexported constant.
func BreakerEventsChannel() string { return breakerEventsChannel }
