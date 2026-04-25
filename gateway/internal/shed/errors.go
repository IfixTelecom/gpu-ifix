// Package shed (errors.go): sentinel errors for the saturation-aware
// load-shedding subsystem introduced by Phase 5 (CONTEXT.md §B/C/D).
//
// Each error wraps one semantic failure mode. The HTTP envelope used by
// the gateway to surface these to the client is documented alongside the
// error (OpenAI error format per pkg/openai).
package shed

import "errors"

var (
	// ErrShedOn signals the FSM is in StateOn for a given upstream; the
	// middleware overrides routing to tier-1 for normal tenants OR writes
	// a 503 for sensitive tenants. Not surfaced to the wire directly.
	ErrShedOn = errors.New("shed: upstream saturated")

	// ErrTenantCapExceeded signals the per-tenant inflight cap is at or
	// above `local_inflight_max_<role>`. Combined with FSM=ON the
	// middleware routes to tier-1 (normal) or 503 (sensitive, D-B3).
	ErrTenantCapExceeded = errors.New("shed: tenant inflight cap exceeded")

	// ErrSensitiveSaturated: tenant data_class=sensitive + FSM=ON + cap
	// reached. Surfaced as HTTP 503 code "upstream_saturated_for_sensitive_tenant"
	// with Retry-After: 5 (D-B3).
	ErrSensitiveSaturated = errors.New("shed: sensitive tenant saturated")

	// ErrAllChatUpstreamsSaturated: tier-0 shed AND tier-1 unavailable
	// (breaker OPEN or 429). Surfaced as HTTP 503
	// code "all_chat_upstreams_saturated" with Retry-After: 30 (D-D1).
	ErrAllChatUpstreamsSaturated = errors.New("shed: all chat upstreams saturated")

	// ErrShedForceTTLExceeded: operator override via gw:shed:force:{upstream}
	// received TTL above the enforced ceiling (typically 3600s).
	ErrShedForceTTLExceeded = errors.New("shed: force override TTL exceeds ceiling")

	// ErrShedConfigInvalid: circuit_config JSONB contains out-of-range
	// shed_* fields (inflight_max<=0, p95_ms<=0, vram_mib<=0, arm<5s, recover<5s).
	ErrShedConfigInvalid = errors.New("shed: invalid config values")
)
