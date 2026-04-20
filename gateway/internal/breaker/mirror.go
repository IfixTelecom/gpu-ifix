package breaker

// This file exists as the Redis-mirror surface area. The actual publish
// path lives in breaker.go (publishTransition) because it needs access
// to Set state. The subscribe side lives in subscribe.go.
//
// Keep this file minimal; add per-replica state helpers here if Phase 6
// needs multi-process coordination beyond the remoteOpen overlay.

// Namespace is the Redis key prefix for breaker state hashes
// (gw:breaker:{upstream}). Exported so tests and ops tooling
// (gatewayctl, dashboard) can build the full key.
const Namespace = "gw:breaker:"
