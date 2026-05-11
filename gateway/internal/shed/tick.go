// Package shed (tick.go): global FSM ticker goroutine that drives the
// 2-of-3 saturation gate (CONTEXT.md D-A1 + D-C1).
//
// A single goroutine iterates every managed FSM at `SHED_TICK_INTERVAL_MS`
// (default 1s; config/config.go), derives the composite Signals struct
// from the inflight registry + per-upstream latency ring + DCGM scraper,
// and calls FSM.Evaluate. Additionally, when an operator override is
// active in Redis (gw:shed:force:{upstream}), the ticker drives the FSM
// to the override state BEFORE Evaluate runs — see D-C5.
//
// VramReader is an interface so tests can inject a fake DCGM reader
// (DCGM scraper from Plan 05-04 implements ReadMiB; nil readers are
// safe and treated as "unknown", which reduces the gate to 1-of-2).
//
// The tick goroutine is the SINGLE place where Prometheus gauges
// gateway_inflight, gateway_inflight_tenant, gateway_p95_request_ms,
// and gateway_shed_force_active are updated on a periodic cadence.
// Other paths only mutate atomic counters; the gauge fanout lives here.
package shed

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

// VramReader abstracts the DCGM scraper so tests can provide mocks.
// Returns (vramMiB, unknown). When unknown==true the VRAM signal is
// dropped from the 2-of-3 composite (D-A3 fail-open).
type VramReader interface {
	ReadMiB() (int64, bool)
}

// Thresholds per upstream, derived from upstreams.CircuitConfig at tick
// time (Plan 05-02 extended the JSONB schema with shed_inflight_max,
// shed_p95_ms, shed_vram_used_mib). When all three fields are zero, the
// upstream has shedding disabled (tier-1 fallbacks, healthchecks, etc.)
// and the ticker skips evaluation entirely.
type Thresholds struct {
	InflightMax int64
	P95Ms       uint32
	VramMiB     int64
}

// TickerDeps is the bundle of dependencies the RunTicker goroutine
// consumes. Constructed once at boot (Plan 05-06 main.go) and passed
// by value — the ticker holds it for its lifetime.
type TickerDeps struct {
	// Set is the per-upstream FSM registry. nil disables the ticker.
	Set *Set

	// Inflight is the (upstream, tenant) inflight counter registry.
	// nil leaves globalInflight at 0 (signal effectively disabled).
	Inflight *InflightRegistry

	// Latency maps upstream name → rolling ring buffer of recent
	// latency samples. Missing entries leave p95 at 0.
	Latency map[string]*LatencyRing

	// VramReader supplies the DCGM-derived VRAM-used signal. nil is
	// safe — VRAM is treated as unknown and the 2-of-3 gate reduces
	// to inflight + p95 (D-A3 fail-open).
	VramReader VramReader

	// ThresholdSrc returns the per-upstream Thresholds for evaluation.
	// MUST be safe to call concurrently with hot-reloads. Zero-valued
	// Thresholds disable evaluation for that upstream.
	ThresholdSrc func(upstream string) Thresholds

	// Rdb enables the shed-force override path (gw:shed:force:{upstream}).
	// nil disables the override (FSM evaluates against signals only).
	Rdb *redis.Client

	// Interval is the tick cadence. <=0 defaults to 1 second.
	Interval time.Duration

	// TenantLabel converts a tenant UUID to its slug for the
	// gateway_inflight_tenant{upstream,tenant} Prometheus label.
	// nil disables per-tenant gauge emission (tests pass nil).
	TenantLabel func(tenant uuid.UUID) string
}

// RunTicker blocks until ctx cancellation. MUST be started via
// `go shed.RunTicker(rootCtx, deps, log)` from main.go wiring.
//
// nil Set returns immediately (allows wiring guards to use zero-value
// TickerDeps as a "shedding disabled" sentinel).
func RunTicker(ctx context.Context, d TickerDeps, log *slog.Logger) {
	if d.Set == nil {
		return
	}
	if log == nil {
		log = slog.Default()
	}
	interval := d.Interval
	if interval <= 0 {
		interval = 1 * time.Second
	}
	log = log.With("module", "SHED_FSM")
	t := time.NewTicker(interval)
	defer t.Stop()
	log.Info("shed FSM ticker started", "interval", interval)
	for {
		select {
		case <-ctx.Done():
			log.Info("FSM ticker stopping")
			return
		case now := <-t.C:
			d.runOneTick(ctx, now, log)
		}
	}
}

// runOneTick performs one evaluation pass over every managed FSM.
// Held as a method on *TickerDeps so unit tests can call it directly
// without spinning up the goroutine.
func (d *TickerDeps) runOneTick(ctx context.Context, now time.Time, log *slog.Logger) {
	d.Set.ForEach(func(upstream string, fsm *FSM) {
		// 1. Operator override (D-C5). If gw:shed:force:{upstream} is
		// set, drive the FSM to the override state and skip the
		// signal-based evaluation entirely. TTL is enforced by Redis;
		// once the key expires, GatewayShedForceActive drops to 0.
		if d.Rdb != nil {
			state, _, ok := redisx.GetShedForce(ctx, d.Rdb, upstream)
			if ok {
				obs.GatewayShedForceActive.WithLabelValues(upstream).Set(1)
				var target State
				switch state {
				case "off":
					target = StateOff
				case "on":
					target = StateOn
				default:
					// Unknown override value — log + ignore; the
					// override is malformed and the FSM continues
					// with its normal evaluation. We still skip the
					// rest of this tick to avoid double-evaluating
					// a malformed override.
					log.Warn("malformed shed-force value; ignoring", "upstream", upstream, "value", state)
					return
				}
				if fsm.State() != target {
					fsm.Transition(target, "operator_override")
				}
				return
			}
			obs.GatewayShedForceActive.WithLabelValues(upstream).Set(0)
		}

		// 2. Thresholds: zero-valued means shedding disabled for this
		// upstream (tier-1 fallback, healthcheck endpoint, etc.). Skip
		// evaluation — FSM stays in its current state, no metric churn.
		th := d.ThresholdSrc(upstream)
		if th.InflightMax == 0 && th.P95Ms == 0 && th.VramMiB == 0 {
			return
		}

		// 3. Derive signals from registries.
		globalInflight := int64(0)
		if d.Inflight != nil {
			globalInflight = d.Inflight.GlobalInflight(upstream)
		}
		p95 := uint32(0)
		if ring, ok := d.Latency[upstream]; ok {
			p95 = ring.P95()
			obs.GatewayP95RequestMs.WithLabelValues(upstream).Set(float64(p95))
		}
		vramMiB, vramUnknown := int64(0), true
		if d.VramReader != nil {
			vramMiB, vramUnknown = d.VramReader.ReadMiB()
		}

		sig := Signals{
			InflightOverMax: th.InflightMax > 0 && globalInflight >= th.InflightMax,
			P95OverMax:      th.P95Ms > 0 && p95 >= th.P95Ms,
			VramOverMax:     th.VramMiB > 0 && vramMiB >= th.VramMiB,
			VramUnknown:     vramUnknown,
		}
		fsm.Evaluate(now, sig)

		// 4. Update gauges (Prometheus fanout). The hot path mutates
		// only atomic counters; the gauge surface is updated once per
		// tick so dashboard refresh sees coherent numbers.
		obs.GatewayInflight.WithLabelValues(upstream).Set(float64(globalInflight))

		// Per-tenant gauge fanout. Inlined here (rather than calling
		// a hypothetical InflightRegistry.ObserveMetrics) because
		// inflight.go is owned by Plan 05-03 and parallel-execution
		// rules forbid modifying it from this plan. The public API
		// (TenantsForUpstream + TenantInflight) is sufficient.
		// Cardinality budget: 3 upstreams x 6 tenants = 18 series (D-D4).
		if d.Inflight != nil && d.TenantLabel != nil {
			for _, tid := range d.Inflight.TenantsForUpstream(upstream) {
				label := d.TenantLabel(tid)
				if label == "" {
					continue
				}
				obs.GatewayInflightTenant.
					WithLabelValues(upstream, label).
					Set(float64(d.Inflight.TenantInflight(upstream, tid)))
			}
		}
	})
}
