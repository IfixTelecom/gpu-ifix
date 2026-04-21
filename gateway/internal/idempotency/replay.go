// Package idempotency (replay.go): context-scoped replay flag used by
// downstream middleware (quota rate-limit) to skip work that was already
// accounted for on the original request.
//
// Today the idempotency middleware short-circuits the replay path via
// replay(w, slot.Entry) + return BEFORE next.ServeHTTP runs, so downstream
// middleware never executes on a replay. This helper exists for D-D1's
// stated semantics ("rate-limit is skipped on idempotency replays; quota
// still consumes per Stripe") so future architectural changes (e.g.
// allowing replays to flow through for audit-only observability) can
// surface the flag without redesigning the call chain.
//
// Callers: quota/enforcer.go reads IsReplay(ctx) before invoking the Lua
// token-bucket check. If true, it returns early (pass-through) without
// consulting Redis.
package idempotency

import "context"

type replayKey struct{}

// WithReplay returns a derived context whose IsReplay(ctx) returns true.
// Use inside the idempotency middleware if/when a replay path is routed
// through downstream middleware rather than short-circuited.
func WithReplay(parent context.Context) context.Context {
	return context.WithValue(parent, replayKey{}, true)
}

// IsReplay reports whether ctx was marked as a replay by WithReplay.
// Safe on any context (returns false by default).
func IsReplay(ctx context.Context) bool {
	v, _ := ctx.Value(replayKey{}).(bool)
	return v
}
