// Package breaker wraps sony/gobreaker/v2 circuit breakers per upstream,
// publishes state transitions to the Redis mirror hash, and subscribes to
// peer replicas' transitions for cross-replica convergence (CONTEXT.md D-D1).
// Authoritative state is the in-process *gobreaker.CircuitBreaker; Redis is
// a mirror, never the source of truth.
package breaker

import "errors"

var (
	// ErrBreakerOpen is returned by Set.Execute when the upstream's
	// gobreaker is OPEN. The dispatcher (internal/proxy/dispatcher.go)
	// wraps this into either a tier-1 fallback (normal tenant per D-A1)
	// or a sensitive retry loop (sensitive tenant per D-B1).
	// Maps to HTTP 503 with code "upstream_unavailable" when surfaced.
	ErrBreakerOpen = errors.New("breaker: circuit open")

	// ErrUpstreamUnavailable means every tier (primary + fallback) is
	// OPEN for the requested role. Surfaced as HTTP 503 with OpenAI
	// envelope code "upstream_unavailable" per CONTEXT.md D-C4.
	ErrUpstreamUnavailable = errors.New("breaker: all upstreams unavailable")
)
