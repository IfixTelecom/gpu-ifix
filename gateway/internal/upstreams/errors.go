package upstreams

import "errors"

var (
	// ErrProbeTimeout is reported by the probe goroutine when the 5s
	// shared deadline elapses before an upstream responds. Counts as
	// a breaker failure per CONTEXT.md D-A4. NOT surfaced to clients
	// directly — the probe loop logs it and calls cb.Fail().
	ErrProbeTimeout = errors.New("upstreams: probe timeout")

	// ErrUpstreamNotFound means the dispatcher requested a (role, tier)
	// combination that has no corresponding row in the in-memory map
	// (either row is disabled or was never seeded). Maps to HTTP 503
	// with code "upstream_unavailable".
	ErrUpstreamNotFound = errors.New("upstreams: row not found for role/tier")
)
