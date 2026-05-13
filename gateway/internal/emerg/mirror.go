package emerg

// This file exists as the Redis-mirror surface area marker for the
// Phase 6 emergency reconciler. Hash/Pub-Sub publish helpers live in
// internal/redisx/emerg.go (added in plan 06-04); subscribe-side
// fan-out lives in internal/emerg/subscribe.go (added in plan 06-04).
//
// Keep this file minimal — it is the canonical place to declare the
// Redis namespace constant so tests, gatewayctl, and the dashboard
// can build full keys without string-duplication drift.

// Namespace is the Redis key prefix for all Phase 6 emergency state:
// gw:emerg:state (Hash), gw:emerg:lock (redsync mutex), gw:emerg:events
// (Pub/Sub). Mirrors the Phase 3 breaker.Namespace ("gw:breaker:") and
// Phase 5 shed.Namespace pattern (CONTEXT.md D-B1).
const Namespace = "gw:emerg:"
