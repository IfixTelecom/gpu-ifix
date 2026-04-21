// Package schedule (middleware.go): pre-dispatch routing decision. Writes
// an upstream_override onto the request context when the tenant is in
// peak mode AND the clock is outside the business window (D-C2), so the
// dispatcher can skip tier-0 entirely (GPU may be suspended off-hours)
// and dispatch straight to the tier-1 external upstream.
//
// 24/7 tenants never get an override — their tier-0 breaker state drives
// the normal fallback chain (Phase 3 dispatcher).
//
// Metric: every decision increments GatewayScheduleRouting{tenant,decision}
// with decision ∈ {"local", "off_hours_external"} so dashboards can show
// which tenants are actively using the external path.
package schedule

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auditctx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/tenants"
)

// upstreamForTier maps a tier int (from DecideUpstreamTier) to the
// upstream name the dispatcher expects. Today only Tier1 emits an override
// (openrouter-chat is the sole peak-off-hours destination); Tier0 means
// "no override; follow the normal breaker chain".
//
// If Phase 5 adds STT/embed off-hours routing, extend here.
func upstreamForTier(tier int) string {
	if tier == Tier1 {
		return "openrouter-chat"
	}
	return ""
}

// Middleware returns a chi-compatible middleware that decides the upstream
// override based on the tenant's schedule policy. Consumed by the
// dispatcher via auditctx.UpstreamOverrideFromContext.
//
// Fallthroughs (all pass-through, no override):
//   - no auth context (handled by auth/rate-limit earlier in the chain)
//   - malformed tenant_id (should never happen; auth guarantees UUID shape)
//   - tenant snapshot missing (freshly added, pending refresh)
//
// The decision is resolved via schedule.DecideUpstreamTier which already
// handles nil Location + wrap-around peak windows.
func Middleware(loader *tenants.Loader, log *slog.Logger) func(http.Handler) http.Handler {
	if log == nil {
		log = slog.Default()
	}
	log = log.With("module", "SCHEDULE")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ac, ok := auth.FromContext(r.Context())
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			tenantID, perr := uuid.Parse(ac.TenantID)
			if perr != nil {
				next.ServeHTTP(w, r)
				return
			}
			cfg, err := loader.Get(tenantID)
			if err != nil {
				next.ServeHTTP(w, r)
				return
			}
			ctx := r.Context()
			tier := DecideUpstreamTier(cfg, time.Now())
			if name := upstreamForTier(tier); name != "" {
				ctx = auditctx.WithUpstreamOverride(ctx, name)
				obs.GatewayScheduleRouting.WithLabelValues(cfg.Slug, "off_hours_external").Inc()
				log.Debug("schedule override",
					"tenant", cfg.Slug, "upstream", name)
			} else {
				obs.GatewayScheduleRouting.WithLabelValues(cfg.Slug, "local").Inc()
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
