// Package shed (inflight.go): per-(upstream, tenant) inflight counter
// registry consumed by the FSM 2-of-3 saturation gate (CONTEXT.md D-A1
// inflight signal) and the per-tenant fairness hard cap (CONTEXT.md
// D-B1).
//
// Design (RESEARCH Pattern 4 + PATTERNS §inflight.go):
//   - global[upstream]      → atomic.Int64 (decision hot path)
//   - tenant[upstream][tid] → atomic.Int64 (per-tenant cap enforcement)
//   - RWMutex only on populate-once for the inner tenant map; Inc/Dec
//     and reads are lockless atomic ops on the *atomic.Int64.
//
// Increment is paired with a defer'd Dec in the shed middleware
// (Plan 05-06) — middleware is the single source of truth; dispatcher
// does NOT call Inc/Dec.
package shed

import (
	"sync"
	"sync/atomic"

	"github.com/google/uuid"
)

// InflightRegistry tracks in-flight requests per (upstream, tenant).
// Construct via NewInflightRegistry; mutate via Inc/Dec; read via
// GlobalInflight / TenantInflight.
type InflightRegistry struct {
	// global is preallocated at construction; new upstream names cannot
	// be added at runtime (Rebuild on upstream hot-reload re-creates the
	// registry via NewInflightRegistry).
	global map[string]*atomic.Int64

	mu     sync.RWMutex
	tenant map[string]map[uuid.UUID]*atomic.Int64
}

// NewInflightRegistry pre-allocates the global counter per upstream
// and the empty per-upstream tenant map. Tenant counters are created
// lazily on the first Inc for an unseen (upstream, tenantID) pair.
func NewInflightRegistry(upstreams []string) *InflightRegistry {
	r := &InflightRegistry{
		global: make(map[string]*atomic.Int64, len(upstreams)),
		tenant: make(map[string]map[uuid.UUID]*atomic.Int64, len(upstreams)),
	}
	for _, name := range upstreams {
		r.global[name] = &atomic.Int64{}
		r.tenant[name] = make(map[uuid.UUID]*atomic.Int64)
	}
	return r
}

// Inc bumps both the global and per-tenant counters for (upstream,
// tenantID). Inc on an unknown upstream is a silent no-op — the
// upstream set is fixed at construction and the middleware is expected
// to have resolved a real upstream before calling.
//
// The first Inc for a new tenantID on a known upstream takes the write
// lock to insert the counter; subsequent Inc/Dec for the same tenantID
// are lockless atomic.AddInt64.
func (r *InflightRegistry) Inc(upstream string, tenantID uuid.UUID) {
	if r == nil {
		return
	}
	g, ok := r.global[upstream]
	if !ok {
		return // unknown upstream — middleware bug; do not auto-create
	}
	g.Add(1)

	// Fast path: read-lock + map lookup. If the tenant counter exists,
	// just add — lockless on the AddInt64 itself.
	r.mu.RLock()
	tmap := r.tenant[upstream]
	c, exists := tmap[tenantID]
	r.mu.RUnlock()
	if exists {
		c.Add(1)
		return
	}

	// Slow path: write-lock + map insert. Re-check after taking the
	// write lock in case another goroutine inserted the counter while
	// we were waiting.
	r.mu.Lock()
	tmap = r.tenant[upstream]
	if c, exists = tmap[tenantID]; !exists {
		c = &atomic.Int64{}
		tmap[tenantID] = c
	}
	r.mu.Unlock()
	c.Add(1)
}

// Dec decrements both counters for (upstream, tenantID). A Dec without
// a matching Inc may temporarily push the counter negative; the registry
// stays arithmetically sound (the next Inc restores the balance) but
// dashboards may flicker — middleware MUST pair Inc with a defer'd Dec.
//
// Dec on an unknown upstream or unknown tenant is a silent no-op.
func (r *InflightRegistry) Dec(upstream string, tenantID uuid.UUID) {
	if r == nil {
		return
	}
	g, ok := r.global[upstream]
	if !ok {
		return
	}
	g.Add(-1)

	r.mu.RLock()
	tmap := r.tenant[upstream]
	c, exists := tmap[tenantID]
	r.mu.RUnlock()
	if exists {
		c.Add(-1)
	}
}

// GlobalInflight returns the current in-flight count summed across all
// tenants for the upstream. Returns 0 for an unknown upstream (defensive
// — hot-reload may rebuild the registry mid-request).
func (r *InflightRegistry) GlobalInflight(upstream string) int64 {
	if r == nil {
		return 0
	}
	g, ok := r.global[upstream]
	if !ok {
		return 0
	}
	return g.Load()
}

// TenantInflight returns the current in-flight count for one
// (upstream, tenantID) pair. Returns 0 if either is unknown.
func (r *InflightRegistry) TenantInflight(upstream string, tenantID uuid.UUID) int64 {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	tmap, ok := r.tenant[upstream]
	if !ok {
		r.mu.RUnlock()
		return 0
	}
	c, exists := tmap[tenantID]
	r.mu.RUnlock()
	if !exists {
		return 0
	}
	return c.Load()
}

// Upstreams returns a snapshot of the registered upstream names. Useful
// for the tick goroutine (Plan 05-05) to iterate the global gauge.
func (r *InflightRegistry) Upstreams() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.global))
	for n := range r.global {
		out = append(out, n)
	}
	return out
}

// TenantsForUpstream returns a snapshot of tenant UUIDs that currently
// have a counter for the given upstream. Used by the tick goroutine to
// publish gateway_inflight_tenant gauges (Plan 05-05). Cardinality
// bounded by D-D4 budget (~18 series).
func (r *InflightRegistry) TenantsForUpstream(upstream string) []uuid.UUID {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	tmap, ok := r.tenant[upstream]
	if !ok {
		return nil
	}
	out := make([]uuid.UUID, 0, len(tmap))
	for tid := range tmap {
		out = append(out, tid)
	}
	return out
}
